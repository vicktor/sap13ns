// Test fixture: [callout type="info|success|warning|error" title="..."]Text[/callout]
// and [kbd]Ctrl+K[/kbd]. Output uses Basecoat classes, which every theme ships.
package main

import (
	"strings"

	"github.com/vicktor/sap13ns/pluginsdk"
)

var icons = map[string]string{
	"info":    "info",
	"success": "circle-check",
	"warning": "triangle-alert",
	"error":   "circle-alert",
}

func init() {
	pluginsdk.Handle(func(req *pluginsdk.Request) (any, error) {
		if req.Hook != pluginsdk.HookShortcode {
			return nil, nil
		}
		var sc pluginsdk.Shortcode
		if err := req.Decode(&sc); err != nil {
			return nil, err
		}
		switch sc.Name {
		case "kbd":
			return pluginsdk.HTML(`<kbd class="kbd">` + pluginsdk.Escape(sc.Inner) + `</kbd>`), nil
		case "callout":
			kind := sc.Attrs["type"]
			icon, ok := icons[kind]
			if !ok {
				kind, icon = "info", "info"
			}
			var b strings.Builder
			b.WriteString(`<div class="alert sc-callout" role="note" data-category="` + kind + `"`)
			if kind == "error" {
				b.WriteString(` data-variant="destructive"`)
			}
			b.WriteString(`><span data-icon="` + icon + `"></span>`)
			if t := strings.TrimSpace(sc.Attrs["title"]); t != "" {
				b.WriteString(`<h2>` + pluginsdk.Escape(t) + `</h2>`)
			}
			if sc.Inner != "" {
				b.WriteString(`<section>`)
				for _, para := range strings.Split(sc.Inner, "\n\n") {
					b.WriteString(`<p>` + pluginsdk.Escape(strings.TrimSpace(para)) + `</p>`)
				}
				b.WriteString(`</section>`)
			}
			b.WriteString(`</div>`)
			return pluginsdk.HTML(b.String()), nil
		}
		return nil, nil
	})
}

func main() {}
