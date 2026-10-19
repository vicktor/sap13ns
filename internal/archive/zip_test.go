package archive

import (
	"archive/zip"
	"bytes"
	"testing"
)

func zipOf(t *testing.T, names ...string) *bytes.Reader {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, n := range names {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte("x"))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(buf.Bytes())
}

// SkipHidden leaves hidden entries out instead of refusing the archive,
// and still refuses a path that climbs out.
func TestSkipHidden(t *testing.T) {
	l := Limits{MaxBytes: 1 << 20, MaxEntries: 10, MaxUnpacked: 1 << 20, MaxRatio: 100}
	r := zipOf(t, "theme/style.css", "theme/.github/workflow.yml", "theme/.editorconfig")
	if _, err := Open(r, r.Size(), "style.css", l); err == nil {
		t.Fatal("hidden files were accepted without SkipHidden")
	}
	l.SkipHidden = true
	z, err := Open(r, r.Size(), "style.css", l)
	if err != nil {
		t.Fatal(err)
	}
	if got := z.Files(); len(got) != 1 || got[0] != "style.css" {
		t.Fatalf("files: %v", got)
	}
	r = zipOf(t, "theme/style.css", "theme/a/../../evil")
	if _, err := Open(r, r.Size(), "style.css", l); err == nil {
		t.Fatal("a path that climbs out was accepted")
	}
}
