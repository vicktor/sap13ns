package themeimport

import (
	"archive/zip"
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture reads testdata/fixture, a small block theme, as archive
// entries under its folder.
func fixture(t testing.TB) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	root := filepath.Join("testdata", "fixture")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		files["fixture/"+filepath.ToSlash(rel)] = b
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// zipOf writes entries as an archive, in the order of names when given.
func zipOf(t testing.TB, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, b := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func convertFiles(t testing.TB, files map[string][]byte) (*Result, error) {
	t.Helper()
	src := zipOf(t, files)
	return Convert(bytes.NewReader(src), int64(len(src)), Options{})
}

func mustConvert(t testing.TB, files map[string][]byte) *Result {
	t.Helper()
	res, err := convertFiles(t, files)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	return res
}

// minimal is a block theme of one index template.
func minimal(index string, extra ...string) map[string][]byte {
	files := map[string][]byte{
		"mini/style.css":            []byte("/*\nTheme Name: Mini\nVersion: 1.0\nLicense: GPLv2 or later\n*/\n"),
		"mini/templates/index.html": []byte(index),
	}
	for i := 0; i+1 < len(extra); i += 2 {
		files["mini/"+extra[i]] = []byte(extra[i+1])
	}
	return files
}

// content is the content template written for a page.
func content(t testing.TB, res *Result, page string) string {
	t.Helper()
	b, ok := res.Files["templates/"+page+".html"]
	if !ok {
		t.Fatalf("no templates/%s.html", page)
	}
	return string(b)
}

func hasNote(r *Report, code string) bool {
	for _, n := range r.Notes {
		if n.Code == code {
			return true
		}
	}
	return false
}

// generated is every text file of a result.
func generated(res *Result) string {
	var b strings.Builder
	for name, f := range res.Files {
		if strings.HasSuffix(name, ".html") || strings.HasSuffix(name, ".css") || strings.HasSuffix(name, ".json") {
			b.WriteString(name + "\n" + string(f) + "\n")
		}
	}
	return b.String()
}
