package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"

	"sap13ns/internal/theme"
	"sap13ns/internal/themeimport"
	"sap13ns/internal/web"
)

const themeUsage = `usage:
  sap13ns theme list [-env FILE] [-site S]
  sap13ns theme install [-env FILE] [-site S] <file.zip>
  sap13ns theme remove [-env FILE] [-site S] <slug>
  sap13ns theme import [-env FILE] [-site S] [-slug S] [-o out.zip] <theme.zip>`

// themeCmd implements "sap13ns theme list|install|remove": the themes of a
// site's own directory, validated as an upload in Appearance → Themes is.
// The command line is the operator's: it skips the plan's theme_uploads
// gate and the storage count, which the network's daily measurement
// corrects. "import" converts a WordPress block theme
// (internal/themeimport, docs/THEME-IMPORT.md) and installs the result
// as "install" does; with -o it only writes the converted archive.
func themeCmd(args []string) error {
	if len(args) == 0 {
		return errors.New(themeUsage)
	}
	sub, args := args[0], args[1:]
	switch sub {
	case "list", "install", "remove", "import":
	default:
		return errors.New(themeUsage)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		args = append(args[1:], args[0]) // the argument may come before the flags
	}
	var siteSlug, outPath, slug string
	cfg, log, fs, err := setup("theme "+sub, args, false, func(f *flag.FlagSet) {
		f.StringVar(&siteSlug, "site", "", "network: the site")
		if sub == "import" {
			f.StringVar(&outPath, "o", "", "write the converted theme to this file instead of installing it")
			f.StringVar(&slug, "slug", "", "the slug of the converted theme (default wp-<folder>)")
		}
	})
	if err != nil {
		return err
	}
	pos := fs.Args()
	switch {
	case sub == "list" && len(pos) != 0, sub != "list" && len(pos) != 1:
		return errors.New(themeUsage)
	}
	var imported *themeimport.Result
	if sub == "import" {
		if imported, err = convertTheme(pos[0], slug); err != nil {
			return err
		}
		for _, line := range strings.Split(strings.TrimSuffix(imported.Report.Summary(), "\n"), "\n") {
			fmt.Fprintln(out, termSafe(line)) // the theme's own texts are in it
		}
		if outPath != "" {
			pkg, err := imported.Zip()
			if err != nil {
				return err
			}
			if err := os.WriteFile(outPath, pkg, 0o640); err != nil {
				return err
			}
			fmt.Fprintf(out, "wrote %s (%d bytes); install it with: sap13ns theme install %s\n", outPath, len(pkg), outPath)
			return nil
		}
	}
	t, err := openTarget(ctx, cfg, log, siteSlug, 2)
	if err != nil {
		return err
	}
	defer t.close()
	settings, err := t.store.LoadSettings(ctx)
	if err != nil {
		return err
	}
	m, err := web.ThemeManager(t.cfg, uiFS(cfg), log)
	if err != nil {
		return err
	}
	active := m.Active(settings.ActiveTheme).Slug

	switch sub {
	case "list":
		tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "SLUG\tNAME\tVERSION\tSOURCE\tACTIVE")
		for _, th := range m.List() {
			source := "network"
			switch {
			case th.Builtin:
				source = "built-in"
			case th.Uploaded:
				source = "uploaded"
			case th.Own:
				source = "site"
			}
			on := ""
			if th.Slug == active {
				on = "yes"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", th.Slug, termSafe(th.Name), termSafe(th.Version), source, on)
		}
		for _, f := range m.Failures() {
			fmt.Fprintf(tw, "%s\t%s\t\tnot loaded\t\n", f.Slug, termSafe(f.Err))
		}
		return tw.Flush()

	case "install":
		f, err := os.Open(pos[0])
		if err != nil {
			return err
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil {
			return err
		}
		if fi.Size() > theme.MaxArchiveBytes {
			return fmt.Errorf("the archive is larger than %d MB", theme.MaxArchiveBytes>>20)
		}
		if k := themeimport.Detect(f, fi.Size()); k.Convertible() {
			return fmt.Errorf("%s is a WordPress block theme: convert it with sap13ns theme import %s", filepath.Base(pos[0]), pos[0])
		}
		return installTheme(ctx, t, m, active, f, fi.Size(), filepath.Base(pos[0]), "")

	case "import":
		pkg, err := imported.Zip()
		if err != nil {
			return err
		}
		return installTheme(ctx, t, m, active, bytes.NewReader(pkg), int64(len(pkg)), filepath.Base(pos[0]), imported.Report.Source)

	default: // remove
		slug := strings.TrimSpace(pos[0])
		if slug == settings.ActiveTheme {
			active = slug
		}
		n, err := m.Remove(ctx, slug, active, nil)
		switch {
		case errors.Is(err, theme.ErrActive):
			return fmt.Errorf("%s is the active theme: activate another one first", slug)
		case errors.Is(err, theme.ErrNotOwn):
			return fmt.Errorf("%s is not a theme of the site's own directory (built-in and network themes stay)", termSafe(slug))
		case err != nil:
			return err
		}
		t.store.Audit(ctx, 0, "theme.deleted", slug, netipZero, map[string]string{"source": "cli", "bytes": strconv.FormatInt(n, 10)})
		fmt.Fprintf(out, "removed %s (%d bytes)\n", slug, n)
		return nil
	}
}

// convertTheme converts a WordPress block theme archive.
func convertTheme(file, slug string) (*themeimport.Result, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	switch k := themeimport.Detect(f, fi.Size()); k {
	case themeimport.KindWordPressBlock:
	case themeimport.KindSap13ns:
		return nil, fmt.Errorf("%s is a sap13ns theme already: install it with sap13ns theme install %s", filepath.Base(file), file)
	case themeimport.KindUnknown:
		// Convert says why (not a .zip, too large, no block templates).
	default:
		return nil, fmt.Errorf("%s is a %s theme, which sap13ns cannot convert yet (see docs/THEME-IMPORT.md)", filepath.Base(file), k)
	}
	res, err := themeimport.Convert(f, fi.Size(), themeimport.Options{Slug: slug})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(file), err)
	}
	return res, nil
}

// installTheme installs a theme archive as an upload is installed, and
// records it in the audit log.
func installTheme(ctx context.Context, t *target, m *theme.Manager, active string, r io.ReaderAt, size int64, file, importedFrom string) error {
	res, err := m.Install(ctx, r, size, theme.InstallOptions{Source: "cli", ImportedFrom: importedFrom})
	if ref, ok := theme.IsRefusal(err); ok {
		var codes []string
		for _, p := range ref.Problems {
			fmt.Fprintln(out, "  "+termSafe(p.Error()))
			if !slices.Contains(codes, p.Code) {
				codes = append(codes, p.Code)
			}
		}
		details := map[string]string{"source": "cli", "file": file, "problems": strconv.Itoa(len(ref.Problems)),
			"rules": strings.Join(codes, " ")}
		if importedFrom != "" {
			details["imported_from"] = importedFrom
		}
		if res != nil {
			details["sha256"] = res.SHA256
		}
		t.store.Audit(ctx, 0, "theme.refused", ref.Slug, netipZero, details)
		return fmt.Errorf("refused: %d problems; nothing was installed", len(ref.Problems))
	}
	if err != nil {
		return err
	}
	auditAction := "theme.uploaded"
	if res.Replaced {
		auditAction = "theme.updated"
	}
	details := map[string]string{"source": "cli", "version": res.Version,
		"previous": res.Previous, "sha256": res.SHA256, "bytes": strconv.FormatInt(res.Bytes, 10), "file": file}
	if importedFrom != "" {
		details["imported_from"] = importedFrom
	}
	t.store.Audit(ctx, 0, auditAction, res.Slug, netipZero, details)
	action := "installed"
	if res.Replaced {
		action = "replaced"
		if res.Previous != "" {
			action = "replaced version " + termSafe(res.Previous)
		}
	}
	fmt.Fprintf(out, "%s %s %s (%d bytes). It is not active: preview it with ?preview_theme=%s, then activate it in Admin → Appearance → Themes.\n",
		res.Slug, termSafe(res.Version), action, res.Bytes, res.Slug)
	if res.Slug == active {
		fmt.Fprintln(out, "It is the active theme: a running server shows the new version after Admin → Appearance → Themes is reloaded.")
	}
	return nil
}
