// Command pluginbundle packs the official plugins into the bundle the
// binary embeds (internal/plugin/bundled). `make plugins` runs it after
// building every plugin's module:
//
//	go run ./scripts/pluginbundle -o internal/plugin/bundled/packs plugins/toc plugins/related ...
//
// For each plugin directory it packs the package with plugin.Pack, the
// packer of `sap13ns plugin pack`, scans it as an upload is scanned, and
// writes <id>.zip. A blocked package fails the build. Packing is
// reproducible, so a package whose bytes did not change is left as it is,
// without a new scan. Packages in the output directory that no plugin
// produced (a plugin removed from plugins/) are deleted.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"sap13ns/internal/plugin"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "pluginbundle:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("pluginbundle", flag.ContinueOnError)
	out := fs.String("o", "", "the bundle directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" || fs.NArg() == 0 {
		return errors.New("usage: pluginbundle -o DIR PLUGIN-DIR...")
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	ctx := context.Background()
	keep := map[string]bool{}
	var total int64
	for _, dir := range fs.Args() {
		name, size, err := bundle(ctx, dir, *out)
		if err != nil {
			return fmt.Errorf("%s: %w", dir, err)
		}
		if keep[name] {
			return fmt.Errorf("%s: two plugins pack as %s", dir, name)
		}
		keep[name] = true
		total += size
	}
	stale, err := filepath.Glob(filepath.Join(*out, "*.zip"))
	if err != nil {
		return err
	}
	for _, p := range stale {
		if !keep[filepath.Base(p)] {
			fmt.Printf("removed %s\n", p)
			if err := os.Remove(p); err != nil {
				return err
			}
		}
	}
	fmt.Printf("%d official plugins, %.1f MiB in %s\n", len(keep), float64(total)/(1<<20), *out)
	return nil
}

// bundle packs and scans the plugin in dir into out/<id>.zip.
func bundle(ctx context.Context, dir, out string) (string, int64, error) {
	var buf bytes.Buffer
	m, err := plugin.Pack(dir, &buf)
	if err != nil {
		return "", 0, err
	}
	if filepath.Base(filepath.Clean(dir)) != m.ID {
		return "", 0, fmt.Errorf("the id %q is not the directory's name", m.ID)
	}
	name := m.ID + ".zip"
	path := filepath.Join(out, name)
	if have, err := os.ReadFile(path); err == nil && bytes.Equal(have, buf.Bytes()) {
		fmt.Printf("%s %s: unchanged\n", m.ID, m.Version)
		return name, int64(buf.Len()), nil
	}
	rep, err := plugin.ScanPackage(ctx, bytes.NewReader(buf.Bytes()), int64(buf.Len()), plugin.ScanOptions{}, "")
	if err != nil {
		return "", 0, err
	}
	fmt.Printf("%s %s: %s (%d critical, %d high, %d medium, %d info), %d KiB\n", m.ID, m.Version, rep.Verdict,
		rep.Count(plugin.SevCritical), rep.Count(plugin.SevHigh), rep.Count(plugin.SevMedium), rep.Count(plugin.SevInfo), (buf.Len()+1023)/1024)
	if rep.Blocked() {
		var rules []string
		for _, f := range rep.Findings {
			if f.Severity == plugin.SevCritical {
				rules = append(rules, f.Rule+": "+f.Message)
			}
		}
		return "", 0, fmt.Errorf("blocked by the scanner: %s", strings.Join(rules, "; "))
	}
	tmp, err := os.CreateTemp(out, ".pack-*")
	if err != nil {
		return "", 0, err
	}
	defer os.Remove(tmp.Name()) // a no-op once renamed
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		return "", 0, err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return "", 0, err
	}
	if err := tmp.Close(); err != nil {
		return "", 0, err
	}
	return name, int64(buf.Len()), os.Rename(tmp.Name(), path)
}
