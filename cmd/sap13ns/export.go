package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"sap13ns/internal/export"
	"sap13ns/internal/store"
	"sap13ns/internal/web"
)

// exportCmd implements "sap13ns export": the content of a site as a WXR
// file (.xml) or a .zip with the uploads and Markdown copies, the same
// export as Tools → Export.
func exportCmd(args []string) error {
	var dest, site string
	var drafts, pii, noMedia, pending bool
	cfg, log, _, err := setup("export", args, true, func(f *flag.FlagSet) {
		f.StringVar(&dest, "o", "", "output file: name.xml (WXR) or name.zip (with uploads and Markdown); - writes the WXR to stdout")
		f.BoolVar(&drafts, "drafts", false, "also export drafts, pending and private posts")
		f.BoolVar(&pii, "pii", false, "include commenters' e-mail and IP addresses and authors' e-mail addresses")
		f.BoolVar(&noMedia, "no-media", false, "only the featured images of the exported posts, and no uploads in a .zip")
		f.BoolVar(&pending, "pending-comments", false, "also export comments awaiting moderation")
		f.StringVar(&site, "site", "", "network: the site")
	})
	if err != nil {
		return err
	}
	format := strings.ToLower(filepath.Ext(dest))
	switch {
	case dest == "-":
		format = ".xml"
	case dest == "":
		return errors.New("usage: sap13ns export -o FILE.xml|FILE.zip [-drafts] [-pii] [-no-media] [-site S]")
	case format != ".xml" && format != ".zip":
		return fmt.Errorf("%s: the output must end in .xml or .zip", dest)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	t, err := openTarget(ctx, cfg, log, site, 4)
	if err != nil {
		return err
	}
	defer t.close()
	set, err := t.store.LoadSettings(ctx)
	if err != nil {
		return err
	}
	loc, err := time.LoadLocation(set.Timezone)
	if err != nil {
		loc = time.UTC
	}
	opts := export.Options{Media: !noMedia, PersonalData: pii, PendingComments: pending,
		Site: export.Site{URL: t.cfg.BaseURL.String(), Title: set.SiteTitle, Description: set.Tagline, Language: set.Language,
			Location: loc, Generator: "sap13ns " + web.Version, UploadsDir: t.cfg.UploadsDir}}
	if drafts {
		opts.Statuses = []string{store.StatusPublished, store.StatusDraft, store.StatusPending, store.StatusPrivate}
	}
	write := func(w io.Writer) (*export.Report, error) {
		if format == ".zip" {
			return export.WriteZip(ctx, t.store, w, opts)
		}
		return export.WriteWXR(ctx, t.store, w, opts)
	}
	var rep *export.Report
	msg := out // the summary; on stderr when the export is on stdout
	if dest == "-" {
		msg = os.Stderr
		if rep, err = write(out); err != nil {
			return err
		}
	} else if rep, err = writeFileAtomic(dest, write); err != nil {
		return err
	}
	fmt.Fprintf(msg, "exported %d posts, %d pages, %d media items, %d comments, %d authors, %d categories, %d tags, %d redirects",
		rep.Posts, rep.Pages, rep.Attachments, rep.Comments, rep.Authors, rep.Categories, rep.Tags, rep.Redirects)
	if format == ".zip" {
		fmt.Fprintf(msg, ", %d files", rep.Files)
	}
	if dest != "-" {
		fmt.Fprintf(msg, " to %s", dest)
	}
	fmt.Fprintln(msg)
	return nil
}

// writeFileAtomic writes name through a temporary file in its directory,
// readable by its owner only (an export may hold personal data), and
// renames it into place once complete.
func writeFileAtomic(name string, write func(io.Writer) (*export.Report, error)) (rep *export.Report, err error) {
	tmp, err := os.CreateTemp(filepath.Dir(name), ".sap13ns-export-*.tmp")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return nil, err
	}
	if rep, err = write(tmp); err != nil {
		return rep, err
	}
	if err := tmp.Sync(); err != nil {
		return rep, err
	}
	if err := tmp.Close(); err != nil {
		return rep, err
	}
	return rep, os.Rename(tmp.Name(), name)
}
