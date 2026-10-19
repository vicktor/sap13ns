package bundled

import (
	"io/fs"
	"testing"

	"sap13ns/internal/plugin"
)

// TestBundle: a fresh clone embeds the README alone; after make plugins,
// every package in the bundle must be readable.
func TestBundle(t *testing.T) {
	if _, err := fs.Stat(FS(), "README.md"); err != nil {
		t.Fatal(err)
	}
	o, err := plugin.LoadOfficial(FS())
	if err != nil {
		t.Fatalf("a package of the bundle cannot be read (run make plugins again): %v", err)
	}
	for _, p := range o.List() {
		if p.License != "MIT" || p.Author != "sap13ns" {
			t.Errorf("%s: an official plugin is by sap13ns, under the MIT licence: %q %q", p.ID, p.Author, p.License)
		}
	}
}
