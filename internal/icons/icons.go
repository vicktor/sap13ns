// Package icons renders Lucide icons (https://lucide.dev, ISC licence) as
// inline SVG. Only the icons listed in scripts/icons.txt are embedded.
package icons

import (
	"embed"
	"html/template"
	"regexp"
	"sort"
	"strings"
	"sync"
)

//go:embed lucide/*.svg
var files embed.FS

var (
	once  sync.Once
	inner map[string]string
	valid = regexp.MustCompile(`^[a-zA-Z0-9 _:./\[\]-]*$`)
)

func load() {
	inner = map[string]string{}
	entries, _ := files.ReadDir("lucide")
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".svg")
		b, _ := files.ReadFile("lucide/" + e.Name())
		inner[name] = strings.TrimSpace(string(b))
	}
}

// SVG returns the icon markup, or "" for unknown names. extraClass is added to
// the class attribute and must only contain class-name characters.
func SVG(name, extraClass string) string {
	once.Do(load)
	body, ok := inner[name]
	if !ok {
		return ""
	}
	if !valid.MatchString(extraClass) {
		extraClass = ""
	}
	cls := "lucide lucide-" + name
	if extraClass != "" {
		cls += " " + extraClass
	}
	return `<svg xmlns="http://www.w3.org/2000/svg" class="` + cls + `" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false">` + body + `</svg>`
}

// Template is the html/template function: {{icon "sun"}} or {{icon "sun" "size-4"}}.
func Template(name string, class ...string) template.HTML {
	return template.HTML(SVG(name, strings.Join(class, " ")))
}

// Names lists the embedded icons (used by the admin "icons" reference).
func Names() []string {
	once.Do(load)
	out := make([]string, 0, len(inner))
	for k := range inner {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
