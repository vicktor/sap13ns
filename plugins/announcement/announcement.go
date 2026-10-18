// Announcement: a scheduled message with an optional link, rendered as a
// widget.
//
// The message is shown only between starts_at (inclusive) and ends_at
// (exclusive); outside that window, or without a message, the widget renders
// nothing and the server hides it. Messages accept a Markdown-lite subset
// (**bold** and [links](url)) and everything else is escaped. Spanish and
// English pages can have their own message and link label.
package main

import (
	"fmt"
	"html"
	"net/url"
	"strings"
	"time"
)

var styles = map[string]string{
	"info":    "info",
	"success": "circle-check",
	"warning": "triangle-alert",
}

// Render builds the widget HTML for a page at pageURL at time now. settings
// are the plugin settings merged with their defaults; siteLang is the site's
// main language. It returns "" when nothing must be shown, and warnings about
// settings that could not be used.
func Render(settings map[string]string, pageURL, siteLang string, now time.Time) (string, []string) {
	get := func(k string) string { return strings.TrimSpace(settings[k]) }
	lang := pageLang(pageURL, siteLang)

	msg := get("message")
	if lang != "" && get("message_"+lang) != "" {
		msg = get("message_" + lang)
	}
	if msg == "" {
		return "", nil
	}

	var warnings []string
	loc := time.UTC
	if tz := get("timezone"); tz != "" {
		l, err := time.LoadLocation(tz)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("unknown timezone %q; using UTC", tz))
		} else {
			loc = l
		}
	}
	start, hasStart, err := parseTime(get("starts_at"), loc, false)
	if err != nil {
		return "", append(warnings, "starts_at: "+err.Error())
	}
	end, hasEnd, err := parseTime(get("ends_at"), loc, true)
	if err != nil {
		return "", append(warnings, "ends_at: "+err.Error())
	}
	if hasStart && hasEnd && !end.After(start) {
		return "", append(warnings, "ends_at must be later than starts_at")
	}
	if hasStart && now.Before(start) || hasEnd && !now.Before(end) {
		return "", warnings
	}

	style := get("style")
	icon, ok := styles[style]
	if !ok {
		style, icon = "info", styles["info"]
	}
	label := "Announcement"
	if lang == "es" {
		label = "Anuncio"
	}

	var b strings.Builder
	b.WriteString(`<section class="alert announcement" data-category="` + style + `" aria-label="` + label + `">`)
	b.WriteString(`<span data-icon="` + icon + `"></span><section><p>`)
	first := true
	for _, line := range strings.Split(strings.ReplaceAll(msg, "\r\n", "\n"), "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		if !first {
			b.WriteString("<br>")
		}
		first = false
		b.WriteString(Inline(line))
	}
	b.WriteString("</p>")
	if href := get("link_url"); href != "" && safeURL(href) {
		text := get("link_label")
		if lang != "" && get("link_label_"+lang) != "" {
			text = get("link_label_" + lang)
		}
		if text == "" {
			text = "Learn more"
			if lang == "es" {
				text = "Más información"
			}
		}
		// The .btn class sits on a span: themes colour links inside widget
		// areas (".site-footer .widget a"), which would override a.btn.
		b.WriteString(`<p><a href="` + html.EscapeString(href) + `"><span class="btn">` + html.EscapeString(text) + `</span></a></p>`)
	} else if href != "" {
		warnings = append(warnings, fmt.Sprintf("link_url %q is not an http(s) URL or a site path", href))
	}
	b.WriteString("</section></section>")
	return b.String(), warnings
}

// pageLang returns "es" or "en" for pages under /es/ or /en/, otherwise the
// site language normalised the same way ("" for other languages).
func pageLang(pageURL, siteLang string) string {
	if u, err := url.Parse(pageURL); err == nil {
		for _, l := range []string{"es", "en"} {
			if u.Path == "/"+l || strings.HasPrefix(u.Path, "/"+l+"/") {
				return l
			}
		}
	}
	l := strings.ToLower(strings.TrimSpace(siteLang))
	switch {
	case l == "es" || strings.HasPrefix(l, "es-"):
		return "es"
	case l == "en" || strings.HasPrefix(l, "en-"):
		return "en"
	}
	return ""
}

// Layouts accepted for times without an offset, most specific first.
var localLayouts = []string{"2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02 15:04", "2006-01-02T15:04"}

// parseTime reads a starts_at/ends_at value. Times without an offset are in
// loc. A date alone means the start of that day, or for an end bound the end
// of that day.
func parseTime(s string, loc *time.Location, isEnd bool) (time.Time, bool, error) {
	if s == "" {
		return time.Time{}, false, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true, nil
	}
	if t, err := time.Parse("2006-01-02T15:04Z07:00", s); err == nil {
		return t, true, nil
	}
	for _, layout := range localLayouts {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t, true, nil
		}
	}
	if t, err := time.ParseInLocation("2006-01-02", s, loc); err == nil {
		if isEnd {
			t = t.AddDate(0, 0, 1)
		}
		return t, true, nil
	}
	return time.Time{}, false, fmt.Errorf("cannot read %q (use 2026-11-01 09:00, 2026-11-01 or RFC 3339)", s)
}

// Inline renders Markdown-lite: **bold** and [text](url). Everything else is
// HTML-escaped; unclosed markers and links with unsafe URLs stay as text.
func Inline(s string) string {
	var b strings.Builder
	inline(&b, s, true, true)
	return b.String()
}

func inline(b *strings.Builder, s string, bold, links bool) {
	text := 0 // start of the pending plain text
	flush := func(i int) {
		b.WriteString(html.EscapeString(s[text:i]))
	}
	for i := 0; i < len(s); {
		if bold && strings.HasPrefix(s[i:], "**") {
			if j := strings.Index(s[i+2:], "**"); j > 0 {
				flush(i)
				b.WriteString("<strong>")
				inline(b, s[i+2:i+2+j], false, links)
				b.WriteString("</strong>")
				i += j + 4
				text = i
				continue
			}
		}
		if links && s[i] == '[' {
			if label, href, n, ok := parseLink(s[i:]); ok {
				flush(i)
				if safeURL(href) {
					// Widget areas style links without an underline
					// (".widget a"); <u> keeps links in running text
					// recognisable in every theme.
					b.WriteString(`<a href="` + html.EscapeString(href) + `"><u>`)
					inline(b, label, bold, false)
					b.WriteString("</u></a>")
				} else {
					inline(b, label, bold, false)
				}
				i += n
				text = i
				continue
			}
		}
		i++
	}
	flush(len(s))
}

// parseLink reads "[label](url)" at the start of s and returns its parts and
// length. Parentheses inside the URL must be balanced.
func parseLink(s string) (label, href string, n int, ok bool) {
	end := strings.IndexByte(s, ']')
	if end <= 1 || end+1 >= len(s) || s[end+1] != '(' {
		return "", "", 0, false
	}
	depth := 0
	for i := end + 2; i < len(s); i++ {
		switch s[i] {
		case ' ', '\t':
			return "", "", 0, false
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
				continue
			}
			href = s[end+2 : i]
			if href == "" {
				return "", "", 0, false
			}
			return s[1:end], href, i + 1, true
		}
	}
	return "", "", 0, false
}

// safeURL accepts http(s) and mailto: URLs and site-relative paths or
// fragments.
func safeURL(u string) bool {
	if strings.ContainsAny(u, " \t\r\n\x00") {
		return false
	}
	l := strings.ToLower(u)
	return strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "mailto:") ||
		strings.HasPrefix(u, "/") || strings.HasPrefix(u, "#")
}
