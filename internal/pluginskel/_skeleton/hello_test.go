package main

import (
	"testing"

	"github.com/vicktor/sap13ns/pluginsdk/sdktest"
)

func TestHello(t *testing.T) {
	if got := Hello("Hola", "<Ada>"); got != `<p class="hello">Hola, &lt;Ada&gt;!</p>` {
		t.Fatalf("Hello = %q", got)
	}
	if got := Hello("", ""); got != `<p class="hello">Hello, world!</p>` {
		t.Fatalf("defaults: %q", got)
	}
}

func TestHelloShortcode(t *testing.T) {
	env := sdktest.New(t)
	env.Plugin.Settings["greeting"] = "Hi"
	html, err := env.Shortcode("hello", map[string]string{"name": "Ada"}, "")
	if err != nil || html != `<p class="hello">Hi, Ada!</p>` {
		t.Fatalf("got %q, %v", html, err)
	}
}
