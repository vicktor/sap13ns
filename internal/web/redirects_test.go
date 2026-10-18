package web

import (
	"testing"

	"sap13ns/internal/store"
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
