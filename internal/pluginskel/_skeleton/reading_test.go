package main

import (
	"strings"
	"testing"

	"github.com/vicktor/sap13ns/pluginsdk"
	"github.com/vicktor/sap13ns/pluginsdk/sdktest"
)

func TestReadingNote(t *testing.T) {
	if got := ReadingNote(500, 200, "es"); got != `<p class="reading-time">3 min de lectura</p>` {
		t.Fatalf("got %q", got)
	}
	if got := ReadingNote(10, 0, "en"); got != `<p class="reading-time">1 min read</p>` {
		t.Fatalf("got %q", got)
	}
}

func TestReadingFilter(t *testing.T) {
	env := sdktest.New(t)
	env.Plugin.Settings["words_per_minute"] = "100"
	out, err := env.Filter("<p>Body</p>", &pluginsdk.Post{Type: "post", WordCount: 250, Lang: "en"})
	if err != nil || !strings.HasSuffix(out, "3 min read</p>") {
		t.Fatalf("got %q, %v", out, err)
	}
	// Pages are left alone: the host keeps the HTML as it was.
	if out, _ := env.Filter("<p>Page</p>", &pluginsdk.Post{Type: "page"}); out != "<p>Page</p>" {
		t.Fatalf("page changed: %q", out)
	}
}
