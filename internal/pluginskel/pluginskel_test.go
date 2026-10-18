package pluginskel

import (
	"bytes"
	"encoding/json"
	"flag"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"sap13ns/internal/plugin"
)

var update = flag.Bool("update", false, "copy plugins/skeleton into _skeleton")

const source = "../../plugins/skeleton"

// skeletonFiles lists the files of plugins/skeleton that belong to the
// template (build outputs excluded).
func skeletonFiles(t *testing.T) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	err := filepath.WalkDir(source, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(source, p)
		rel = filepath.ToSlash(rel)
		if rel == "plugin.wasm" || strings.HasSuffix(rel, ".zip") || strings.HasSuffix(rel, ".sha256") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if rel == "go.mod" || rel == "go.sum" {
			rel += ".txt"
		}
		out[rel] = b
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSkeletonInSync(t *testing.T) {
	want := skeletonFiles(t)
	if *update {
		os.RemoveAll(Root)
		for name, b := range want {
			p := filepath.Join(Root, filepath.FromSlash(name))
			os.MkdirAll(filepath.Dir(p), 0o755)
			if err := os.WriteFile(p, b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	got := map[string][]byte{}
	fs.WalkDir(embedded, Root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			got[strings.TrimPrefix(p, Root+"/")], _ = embedded.ReadFile(p)
		}
		return err
	})
	for name, b := range want {
		if !bytes.Equal(got[name], b) {
			t.Errorf("%s differs from plugins/skeleton; run: go test ./internal/pluginskel -update", name)
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("%s is not in plugins/skeleton any more; run: go test ./internal/pluginskel -update", name)
		}
	}
}

func TestGenerateAll(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hello")
	if err := Generate(dir, Options{ID: "hello-world", SDK: "/opt/sap13ns/pluginsdk"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := plugin.ParseManifest(raw)
	if err != nil {
		t.Fatalf("generated manifest is invalid: %v\n%s", err, raw)
	}
	if m.ID != "hello-world" || m.Name != "Hello world" || len(m.Hooks) != len(Hooks()) {
		t.Fatalf("manifest: %+v", m)
	}
	mod, _ := os.ReadFile(filepath.Join(dir, "go.mod"))
	if !bytes.Contains(mod, []byte("module example.com/hello-world\n")) || !bytes.Contains(mod, []byte("=> /opt/sap13ns/pluginsdk")) {
		t.Fatalf("go.mod:\n%s", mod)
	}
	readme, _ := os.ReadFile(filepath.Join(dir, "README.md"))
	if bytes.Contains(readme, []byte("my-plugin")) {
		t.Fatal("README still mentions my-plugin")
	}
}

func TestGeneratePrunes(t *testing.T) {
	dir := t.TempDir()
	if err := Generate(dir, Options{ID: "contact-only", Hooks: []string{"form.submit"}}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "plugin.json"))
	m, err := plugin.ParseManifest(raw)
	if err != nil {
		t.Fatalf("%v\n%s", err, raw)
	}
	if !slices.Equal(m.Hooks, []string{"shortcode", "form.submit"}) || !slices.Equal(m.Shortcodes, []string{"contact"}) ||
		!slices.Equal(m.Permissions, []string{"mail"}) || len(m.Widgets) != 0 || len(m.Settings) != 1 || len(m.Forms) != 1 {
		t.Fatalf("pruned manifest:\n%s", raw)
	}
	for _, gone := range []string{"hello.go", "latest.go", "events_test.go"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); err == nil {
			t.Errorf("%s should have been removed", gone)
		}
	}
	main, _ := os.ReadFile(filepath.Join(dir, "main.go"))
	if bytes.Contains(main, []byte("hello.go")) || !bytes.Contains(main, []byte("contact.go")) {
		t.Fatal("main.go overview not pruned")
	}
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatal(err)
	}
	if err := Generate(dir, Options{ID: "again"}); err == nil {
		t.Fatal("generating into a non-empty directory must fail")
	}
	if err := Generate(t.TempDir(), Options{ID: "x", Hooks: []string{"nope"}}); err == nil {
		t.Fatal("bad id and hook accepted")
	}
}

// TestGeneratedBuilds compiles and tests generated projects against the
// SDK of this checkout (skipped with -short).
func TestGeneratedBuilds(t *testing.T) {
	if testing.Short() {
		t.Skip("builds plugins")
	}
	sdk, _ := filepath.Abs("../../pluginsdk")
	for _, hooks := range [][]string{nil, {"widget"}, {"comment.check", "head.meta"}} {
		dir := t.TempDir()
		if err := Generate(dir, Options{ID: "built", SDK: sdk, Hooks: hooks}); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"vet", "./..."}, {"test", "./..."}} {
			cmd := exec.Command("go", args...)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOWORK=off")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("hooks %v: go %v: %v\n%s", hooks, args, err, out)
			}
		}
	}
}
