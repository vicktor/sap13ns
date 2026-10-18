// The callouts plugin renders notes, tips and warnings with the Basecoat
// alert component every theme styles:
//
//   - the [callout type="info|success|warning|error" title="…"]…[/callout]
//     and [kbd]Ctrl+K[/kbd] shortcodes of the former example plugin, and
//   - GitHub-style Markdown alerts (a blockquote starting with [!NOTE],
//     [!TIP], [!IMPORTANT], [!WARNING] or [!CAUTION]) through content.filter.
//
// This file and alerts.go are plain Go so they can be tested natively;
// main.go adapts the sap13ns plugin SDK (wasip1 only).
package main

import (
	"html"
	"strings"
)

var calloutIcons = map[string]string{
	"info":    "info",
	"success": "circle-check",
	"warning": "triangle-alert",
	"error":   "circle-alert",
}

// Callout renders [callout]. The inner text is escaped; blank lines separate
// paragraphs. Unknown types fall back to info.
func Callout(attrs map[string]string, inner string) string {
	kind := strings.ToLower(strings.TrimSpace(attrs["type"]))
	icon, ok := calloutIcons[kind]
	if !ok {
		kind, icon = "info", "info"
	}
	var b strings.Builder
	b.WriteString(`<div class="alert sc-callout" role="note" data-category="` + kind + `"`)
	if kind == "error" {
		b.WriteString(` data-variant="destructive"`)
	}
	b.WriteString(`><span data-icon="` + icon + `"></span>`)
	if t := strings.TrimSpace(attrs["title"]); t != "" {
		// <strong> rather than a heading: callout titles are not part of the
		// document outline (or of the table of contents).
		b.WriteString(`<strong>` + html.EscapeString(t) + `</strong>`)
	}
	if paras := paragraphs(inner); len(paras) > 0 {
		b.WriteString(`<section>`)
		for _, p := range paras {
			b.WriteString(`<p>` + html.EscapeString(p) + `</p>`)
		}
		b.WriteString(`</section>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// Kbd renders [kbd]. The host sanitizer drops <kbd> elements today, so the
// styled .kbd class sits on a span that also survives; the inner <kbd> keeps
// the semantics wherever it is allowed.
func Kbd(inner string) string {
	keys := strings.Join(strings.Fields(inner), " ")
	if keys == "" {
		return ""
	}
	return `<span class="kbd"><kbd>` + html.EscapeString(keys) + `</kbd></span>`
}

func paragraphs(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	var out []string
	for _, p := range strings.Split(s, "\n\n") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
