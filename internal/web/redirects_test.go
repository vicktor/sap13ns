package web

import (
	"context"
	"strings"
	"testing"
	"time"

	"sap13ns/internal/store"
	"sap13ns/internal/testutil"
)

func TestMatchRedirect(t *testing.T) {
	rules := map[string]store.Redirect{}
	for i, r := range []store.Redirect{
		{Source: "/2023/01/01/caf%c3%a9-con-le%c3%b1a/", Target: "/blog/cafe-con-lena", Status: 301},
		{Source: "/2020/01/15/hola-mundo/", Target: "/blog/hola-mundo", Status: 301},
		{Source: "/acerca-de/", Target: "/acerca-de", Status: 301},
		{Source: "/old", Target: "https://example.org/new", Status: 302},
		{Source: "/?p=12", Target: "/blog/doce", Status: 301},
		{Source: "/borrado", Status: 410},
		{Source: "/loop", Target: "/loop?x=1", Status: 301},
	} {
		r.ID = int64(i + 1)
		rules[redirectKey(r.Source)] = r
	}
	lookup := func(k string) (store.Redirect, bool) { r, ok := rules[k]; return r, ok }
	for _, c := range []struct {
		path, query, want string
	}{
		{"/2023/01/01/café-con-leña/", "", "/blog/cafe-con-lena"},
		{"/2023/01/01/café-con-leña", "", "/blog/cafe-con-lena"},
		{"/2020/01/15/hola-mundo", "", "/blog/hola-mundo"},
		{"/2020/01/15/hola-mundo/", "utm_source=x", "/blog/hola-mundo"},
		{"/acerca-de/", "", "/acerca-de"},
		{"/acerca-de", "", ""}, // would loop
		{"/old/", "", "https://example.org/new"},
		{"/", "p=12", "/blog/doce"},
		{"/", "p=13", ""},
		{"/borrado/", "", "gone"},
		{"/loop", "", ""},
		{"/nothing", "", ""},
	} {
		rd, ok := matchRedirect(c.path, c.query, lookup)
		got := ""
		if ok {
			got = rd.Target
			if rd.Status == 410 {
				got = "gone"
			}
		}
		if got != c.want {
			t.Errorf("matchRedirect(%q, %q) = %q, want %q", c.path, c.query, got, c.want)
		}
	}
}

// TestCheckRestoredRedirect: a restored redirect meets the rules of the
// redirects page and may not hide a post of the restored site.
func TestCheckRestoredRedirect(t *testing.T) {
	st := testutil.Store(t)
	ctx := context.Background()
	author, err := st.CreateUser(ctx, store.NewUser{Username: "rita", Email: "rita@example.com", Role: "admin", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, err := st.CreatePost(ctx, store.PostInput{Type: store.TypePost, Status: store.StatusPublished, Title: "Hola", Slug: "hola", Lang: "en",
		AuthorID: author, CommentStatus: "open", PublishedAt: &now}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		src, dst string
		status   int
		ok       bool
	}{
		{"/viejo", "/blog/hola", 301, true},
		{"https://old.example/viejo?x=1", "https://elsewhere.example/", 302, true},
		{"/gone", "", 410, true},
		{"/blog/hola", "/elsewhere", 301, false}, // the post answers there
		{"/blog/hola/", "/blog/hola", 301, true}, // a variant of its own address
		{"/admin/x", "/", 301, false},            // reserved
		{"//evil.example", "/", 301, false},
		{"/a", "javascript:alert(1)", 301, false},
		{"/a", "/b", 200, false},
	} {
		src, _, err := CheckRestoredRedirect(ctx, st, "en", c.src, c.dst, c.status)
		if (err == nil) != c.ok {
			t.Errorf("%s → %s (%d): err = %v, want ok %v", c.src, c.dst, c.status, err, c.ok)
		}
		if err == nil && !strings.HasPrefix(src, "/") {
			t.Errorf("%s: source %q not normalised", c.src, src)
		}
	}
}
