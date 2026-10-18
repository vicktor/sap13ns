package main

import (
	"strings"

	"github.com/vicktor/sap13ns/pluginsdk"
)

// [hello name="Ada"] → <p class="hello">Hello, Ada!</p>
func init() {
	shortcodes["hello"] = func(req *pluginsdk.Request, sc pluginsdk.Shortcode) (string, error) {
		return Hello(req.Setting("greeting"), sc.Attrs["name"]), nil
	}
}

// Hello is plain Go, so it is easy to test. Escape everything that comes
// from settings, attributes or posts; the host sanitizes the result again.
func Hello(greeting, name string) string {
	greeting, name = strings.TrimSpace(greeting), strings.TrimSpace(name)
	if greeting == "" {
		greeting = "Hello"
	}
	if name == "" {
		name = "world"
	}
	return `<p class="hello">` + pluginsdk.Escape(greeting) + ", " + pluginsdk.Escape(name) + "!</p>"
}
