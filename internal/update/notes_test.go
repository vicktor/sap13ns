package update

import (
	"os"
	"strings"
	"testing"
)

const changelog = `# Changelog

Intro text.

## v1.2.0 — 2026-11-02

- New thing.

### Details

## Not a version

More of 1.2.0.

## v1.1.1

- A fix.

## v1.1.0 - 2026-10-01

- First.
`

func TestChanges(t *testing.T) {
	cs := Changes(changelog)
	if len(cs) != 3 {
		t.Fatalf("%d sections: %+v", len(cs), cs)
	}
	want := []Change{{"v1.2.0", "2026-11-02", ""}, {"v1.1.1", "", "- A fix."}, {"v1.1.0", "2026-10-01", "- First."}}
	for i, w := range want {
		if cs[i].Version != w.Version || cs[i].Date != w.Date {
			t.Errorf("section %d: %+v, want %+v", i, cs[i], w)
		}
	}
	if b := cs[0].Body; !strings.Contains(b, "New thing") || !strings.Contains(b, "### Details") || !strings.Contains(b, "## Not a version") {
		t.Errorf("v1.2.0 body: %q", b)
	}
	if cs[1].Body != want[1].Body {
		t.Errorf("v1.1.1 body: %q", cs[1].Body)
	}
}

func TestSince(t *testing.T) {
	got := func(current string) string {
		var vs []string
		for _, c := range Since(changelog, current) {
			vs = append(vs, c.Version)
		}
		return strings.Join(vs, " ")
	}
	for current, want := range map[string]string{"1.1.0": "v1.2.0 v1.1.1", "v1.1.1": "v1.2.0", "1.2.0": "", "dev": "", "0.9.0": "v1.2.0 v1.1.1 v1.1.0"} {
		if g := got(current); g != want {
			t.Errorf("since %s: %q, want %q", current, g, want)
		}
	}
	if Since("**Full Changelog**: https://github.com/x/y/compare/v1...v2", "v1.0.0") != nil {
		t.Error("notes without sections give changes")
	}
}

func TestNotes(t *testing.T) {
	n, err := Notes(changelog, "v1.1.1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(n, "v1.2.0") || !strings.HasPrefix(n, "## v1.1.1\n\n- A fix.") || !strings.Contains(n, "## v1.1.0 — 2026-10-01\n\n- First.") {
		t.Fatalf("notes:\n%s", n)
	}
	// What Notes writes reads back the same.
	if s := Since(n, "v1.0.0"); len(s) != 2 || s[1].Date != "2026-10-01" {
		t.Fatalf("round trip: %+v", s)
	}
	if _, err := Notes(changelog, "v1.3.0"); err == nil {
		t.Fatal("a version without a section has notes")
	}

	// Older sections are dropped past the cap, whole.
	big := "## v2.0.0\n\n" + strings.Repeat("x", maxNotes-20) + "\n\n## v1.0.0\n\n- Old.\n"
	if n, err := Notes(big, "v2.0.0"); err != nil || strings.Contains(n, "v1.0.0") || len(n) > maxNotes {
		t.Fatalf("capped notes: %d bytes, err %v", len(n), err)
	}
}

// The repository's own changelog has a section for every release tag the
// workflow could publish: at least it parses, newest first.
func TestRepositoryChangelog(t *testing.T) {
	md, err := readFile("../../CHANGELOG.md")
	if err != nil {
		t.Fatal(err)
	}
	cs := Changes(md)
	if len(cs) == 0 {
		t.Fatal("CHANGELOG.md has no release sections")
	}
	for i := 1; i < len(cs); i++ {
		if c, err := Compare(cs[i-1].Version, cs[i].Version); err != nil || c <= 0 {
			t.Errorf("%s comes before %s: newest first", cs[i-1].Version, cs[i].Version)
		}
	}
	for _, c := range cs {
		if c.Date == "" || strings.TrimSpace(c.Body) == "" {
			t.Errorf("%s has no date or no changes", c.Version)
		}
	}
}

func readFile(name string) (string, error) {
	b, err := os.ReadFile(name)
	return string(b), err
}
