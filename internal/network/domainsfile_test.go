package network

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDomainsFile(t *testing.T) {
	var path string
	e := newNetEnv(t, func(o *RegistryOptions) {
		path = filepath.Join(o.Config.DataDir, "custom-domains")
		o.Config.DomainsFile = path
	})
	read := func() string {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	// Written at start, empty: the portal and the default hosts are not
	// custom domains.
	if got := read(); got != "" {
		t.Fatalf("at start: %q", got)
	}
	a := e.tenant("alpha", "business")
	for _, h := range []string{"www.alpha.example", "alpha.example"} {
		if _, err := e.n.AddDomain(e.ctx, a.ID, h); err != nil {
			t.Fatal(err)
		}
	}
	e.refresh()
	if got := read(); got != "" {
		t.Fatalf("unverified domains listed: %q", got)
	}
	for _, h := range []string{"www.alpha.example", "alpha.example"} {
		if err := e.n.VerifyDomain(e.ctx, h); err != nil {
			t.Fatal(err)
		}
	}
	e.refresh()
	if got := read(); got != "alpha.example\nwww.alpha.example\n" {
		t.Fatalf("verified: %q", got)
	}

	// Unchanged lists are not rewritten.
	before, _ := os.Stat(path)
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	e.refresh()
	if after, _ := os.Stat(path); after.Mode().Perm() != 0o600 || !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("rewritten although nothing changed")
	}

	if err := e.n.RemoveDomain(e.ctx, a.ID, "alpha.example"); err != nil {
		t.Fatal(err)
	}
	e.refresh()
	if got := read(); got != "www.alpha.example\n" {
		t.Fatalf("after a removal: %q", got)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o644 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
}
