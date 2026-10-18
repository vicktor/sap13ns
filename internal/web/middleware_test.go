package web

import "testing"

func TestSafeNext(t *testing.T) {
	cases := map[string]string{
		"":                          "/admin/",
		"/admin/posts?status=draft": "/admin/posts?status=draft",
		"/es/blog/hola":             "/es/blog/hola",
		"https://evil.example/":     "/admin/",
		"//evil.example/":           "/admin/",
		"/\\evil.example":           "/admin/",
		"/admin\\..\\x":             "/admin/",
		"/%2F%2Fevil.example":       "/admin/",
		"/\tevil":                   "/admin/",
		"/a\r\nSet-Cookie: x=1":     "/admin/",
		"/a\x7f":                    "/admin/",
		"javascript:alert(1)":       "/admin/",
		"admin":                     "/admin/",
		"/admin/#frag":              "/admin/",
	}
	for in, want := range cases {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}
