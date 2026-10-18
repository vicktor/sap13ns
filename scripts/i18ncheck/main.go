// Command i18ncheck compares the interface strings used by the code with a UI
// catalog (ui/i18n/<lang>.json, English → translation).
//
//	go run ./scripts/i18ncheck -lang es          # missing and stale keys
//	go run ./scripts/i18ncheck -lang es -strict  # exit status 1 when a key is missing
//	go run ./scripts/i18ncheck -dynamic          # also list calls whose key is not a literal
//	go run ./scripts/i18ncheck -keys             # print every key with one location, as JSON
//
// Keys are collected from:
//
//   - templates (ui/**/*.html): {{T "..."}}, (T "..." args) and
//     plural N "%d one" "%d many";
//   - Go files (internal/** and cmd/**, tests excluded): s.t("..."),
//     s.tr(r, "..."), catalogs.T(lang, "...") and calls to local translators,
//     that is variables and parameters of type func(string, ...any) string;
//   - the tables that dynamic calls read: when the key indexes a package-level
//     map literal (s.t(roleLabels[role])) or is a field of a range variable over
//     a slice literal (for _, o := range opts { s.t(o.Label) }), the literal
//     values are taken from the table;
//   - struct fields: when the key is a field of a variable of a declared struct
//     type (for _, f := range t.Fields { s.t(f.Label) }), the constants given
//     for that field in every literal of the type, including those built by
//     small constructors and pair helpers (see gotypes.go).
//
// Any other call whose key is not a constant string is a dynamic call site: its
// strings cannot be extracted, so the tool lists them for a human to check.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	root := flag.String("root", ".", "repository root")
	lang := flag.String("lang", "es", "catalog to check (ui/i18n/<lang>.json)")
	strict := flag.Bool("strict", false, "exit with status 1 when a key is missing from the catalog")
	dynamic := flag.Bool("dynamic", false, "list the dynamic call sites (keys that cannot be extracted)")
	keys := flag.Bool("keys", false, "print every extracted key and its first location as JSON, then exit")
	verbose := flag.Bool("v", false, "show every location of a missing key")
	flag.Parse()

	ex := NewExtractor()
	if err := ex.ScanRepo(*root); err != nil {
		fmt.Fprintln(os.Stderr, "i18ncheck:", err)
		os.Exit(2)
	}
	if *keys {
		out := map[string]string{}
		for k, locs := range ex.Keys {
			out[k] = locs[0].String()
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
		return
	}

	catPath := filepath.Join(*root, "ui", "i18n", *lang+".json")
	cat, err := loadCatalog(catPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "i18ncheck:", err)
		os.Exit(2)
	}
	missing := report(os.Stdout, ex, cat, *lang, *dynamic, *verbose)
	if *strict && missing > 0 {
		os.Exit(1)
	}
}

func loadCatalog(p string) (map[string]string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	return m, nil
}

// report prints the comparison and returns the number of missing keys.
func report(w io.Writer, ex *Extractor, cat map[string]string, lang string, showDynamic, verbose bool) int {
	var missing, stale, empty []string
	for k := range ex.Keys {
		v, ok := cat[k]
		switch {
		case !ok:
			missing = append(missing, k)
		case v == "":
			empty = append(empty, k)
		}
	}
	for k := range cat {
		if _, ok := ex.Keys[k]; !ok {
			stale = append(stale, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	sort.Strings(empty)

	fmt.Fprintf(w, "i18ncheck: %d keys in the code (%d from tables), %d in %s.json\n", len(ex.Keys), ex.tableKeys(), len(cat), lang)
	for _, warn := range ex.Warnings {
		fmt.Fprintln(w, "warning:", warn)
	}
	if len(missing) > 0 {
		fmt.Fprintf(w, "\nmissing from %s.json (%d):\n", lang, len(missing))
		for _, k := range missing {
			locs := ex.Keys[k]
			if verbose {
				fmt.Fprintf(w, "  %q\n", k)
				for _, l := range locs {
					fmt.Fprintf(w, "      %s\n", l)
				}
				continue
			}
			more := ""
			if len(locs) > 1 {
				more = fmt.Sprintf(" (+%d)", len(locs)-1)
			}
			fmt.Fprintf(w, "  %q  %s%s\n", k, locs[0], more)
		}
	}
	if len(empty) > 0 {
		fmt.Fprintf(w, "\nempty translations in %s.json (%d):\n", lang, len(empty))
		for _, k := range empty {
			fmt.Fprintf(w, "  %q  %s\n", k, ex.Keys[k][0])
		}
	}
	if len(stale) > 0 {
		fmt.Fprintf(w, "\nstale in %s.json, no longer used (%d):\n", lang, len(stale))
		for _, k := range stale {
			fmt.Fprintf(w, "  %q\n", k)
		}
	}
	unresolved := 0
	for _, d := range ex.Dynamic {
		if d.Kind == DynUnresolved {
			unresolved++
		}
	}
	if showDynamic {
		fmt.Fprintf(w, "\ndynamic call sites (%d):\n", len(ex.Dynamic))
		for _, d := range ex.Dynamic {
			fmt.Fprintf(w, "  %-11s %s  %s\n", d.Kind, d.Loc, d.Expr)
		}
	} else if len(ex.Dynamic) > 0 {
		fmt.Fprintf(w, "\n%d dynamic call sites (%d unresolved); -dynamic lists them\n", len(ex.Dynamic), unresolved)
	}
	if len(missing) == 0 && len(stale) == 0 && len(empty) == 0 {
		fmt.Fprintln(w, "ok")
	}
	return len(missing)
}

// Loc is a position in a source file, relative to the repository root.
type Loc struct {
	File string
	Line int
}

func (l Loc) String() string { return fmt.Sprintf("%s:%d", l.File, l.Line) }

// Dynamic call kinds.
const (
	DynTable      = "table"      // key read from a literal table; its values were extracted
	DynForward    = "forward"    // key is a parameter of the enclosing function (a translator wrapper)
	DynUnresolved = "unresolved" // key computed at run time; check its sources by hand
)

// DynamicSite is a translation call whose key is not a constant string.
type DynamicSite struct {
	Loc  Loc
	Expr string
	Kind string
}

// Extractor accumulates keys from templates and Go files.
type Extractor struct {
	Keys     map[string][]Loc
	Dynamic  []DynamicSite
	Warnings []string
	literal  map[string]bool // keys written as literals at a call site
}

func NewExtractor() *Extractor {
	return &Extractor{Keys: map[string][]Loc{}, literal: map[string]bool{}}
}

// add records a key found as a literal argument of a translation call.
func (e *Extractor) add(key string, l Loc) {
	if key == "" {
		return
	}
	e.literal[key] = true
	e.Keys[key] = append(e.Keys[key], l)
}

// addTable records a key taken from a table that a dynamic call reads.
func (e *Extractor) addTable(key string, l Loc) {
	if key == "" {
		return
	}
	e.Keys[key] = append(e.Keys[key], l)
}

// tableKeys counts the keys that only come from tables.
func (e *Extractor) tableKeys() int {
	n := 0
	for k := range e.Keys {
		if !e.literal[k] {
			n++
		}
	}
	return n
}

// ScanRepo extracts keys from ui/**/*.html and the Go files under internal/
// and cmd/.
func (e *Extractor) ScanRepo(root string) error {
	err := filepath.WalkDir(filepath.Join(root, "ui"), func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "node_modules" || d.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".html") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		e.ScanTemplate(rel(root, p), string(b))
		return nil
	})
	if err != nil {
		return err
	}
	g := newGoScanner(e, root)
	for _, dir := range []string{"internal", "cmd"} {
		if _, err := os.Stat(filepath.Join(root, dir)); os.IsNotExist(err) {
			continue
		}
		if err := g.addTree(filepath.Join(root, dir)); err != nil {
			return err
		}
	}
	g.scan()
	e.applyHandSources(root)
	sort.Slice(e.Dynamic, func(i, j int) bool {
		a, b := e.Dynamic[i].Loc, e.Dynamic[j].Loc
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Line < b.Line
	})
	return nil
}

func rel(root, p string) string {
	if r, err := filepath.Rel(root, p); err == nil {
		return filepath.ToSlash(r)
	}
	return filepath.ToSlash(p)
}
