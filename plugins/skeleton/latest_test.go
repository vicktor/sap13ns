package main

import (
	"strings"
	"testing"

	"github.com/vicktor/sap13ns/pluginsdk"
	"github.com/vicktor/sap13ns/pluginsdk/sdktest"
)

func TestLatestWidget(t *testing.T) {
	env := sdktest.New(t)
	env.Posts = []pluginsdk.Post{
		{ID: 1, Type: "post", Title: "First", URL: "https://example.test/blog/first", Lang: "en"},
		{ID: 2, Type: "post", Title: "Segundo", URL: "https://example.test/es/blog/segundo", Lang: "es"},
		{ID: 3, Type: "post", Title: "Third & last", URL: "https://example.test/blog/third", Lang: "en"},
	}
	html, err := env.Widget("latest", map[string]string{"count": "5"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, "Third &amp; last") || strings.Contains(html, "Segundo") {
		t.Fatalf("want the English posts only, escaped: %s", html)
	}

	env.Denied["posts:read"] = true // as if the permission were missing
	if _, err := env.Widget("latest", nil); err == nil {
		t.Fatal("QueryPosts without permission should fail")
	}
}

func TestWidgetCount(t *testing.T) {
	for in, want := range map[string]int{"": 3, "0": 3, "x": 3, "4": 4, "50": 10} {
		if got := widgetCount(in); got != want {
			t.Errorf("widgetCount(%q) = %d, want %d", in, got, want)
		}
	}
}
