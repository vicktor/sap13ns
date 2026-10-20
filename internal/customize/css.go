package customize

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"sap13ns/internal/motion"
)

// Curated font stacks. Only locally installed fonts: the CSP allows fonts
// from this site only, so there are no web-font CDNs.
var fontStacks = map[string]string{
	"system":    `system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif`,
	"humanist":  `Seravek, "Gill Sans Nova", Ubuntu, Calibri, "DejaVu Sans", source-sans-pro, sans-serif`,
	"geometric": `Avenir, Montserrat, Corbel, "URW Gothic", source-sans-pro, sans-serif`,
	"serif":     `Charter, "Bitstream Charter", "Sitka Text", Cambria, Georgia, serif`,
	"mono":      `ui-monospace, "Cascadia Code", "Source Code Pro", Menlo, Consolas, "DejaVu Sans Mono", monospace`,
}

// FontStack returns the CSS font-family list of a curated stack.
func FontStack(name string) string { return fontStacks[name] }

// Resolver turns media ids into public URLs ("" when the file is missing).
type Resolver func(id int64) string

// Stylesheet renders the design as CSS: input tokens on :root, a few
// structural rules for options that are not plain values, then the
// custom CSS. An empty design renders an empty string.
func (d *Design) Stylesheet(media Resolver) string {
	var b strings.Builder
	var vars []string
	set := func(name, value string) { vars = append(vars, fmt.Sprintf("  %s: %s;", name, value)) }

	for _, sc := range []struct {
		name string
		m    map[string]string
	}{{"light", d.Light}, {"dark", d.Dark}} {
		for _, tok := range ColorTokens {
			c, ok := ParseHex(sc.m[tok])
			if !ok {
				continue
			}
			set("--sap-"+sc.name+"-"+tok, c.Hex())
			set("--sap-"+sc.name+"-"+tok+"-rgb", c.Triplet())
			if tok == "background" {
				// Highlight and shade of the surface, for themes that
				// draw with light (neumorphism's paired shadows).
				if sc.name == "light" {
					set("--sap-light-background-hi", "rgb("+Mix(c, RGB{255, 255, 255}, 0.75).Triplet()+" / 0.9)")
					set("--sap-light-background-lo", "rgb("+Mix(c, RGB{40, 48, 70}, 0.42).Triplet()+" / 0.62)")
				} else {
					set("--sap-dark-background-hi", "rgb("+Mix(c, RGB{255, 255, 255}, 0.16).Triplet()+" / 0.62)")
					set("--sap-dark-background-lo", "rgb("+Mix(c, RGB{0, 0, 0}, 0.8).Triplet()+" / 0.75)")
				}
			}
		}
	}

	var faces []string
	font := func(role string, f Font) {
		var stack string
		switch f.Stack {
		case "":
			return
		case "body":
			stack = "var(--sap-font-body, var(--font-sans, inherit))"
		case "media":
			u := ""
			if media != nil {
				u = media(f.Media)
			}
			if u == "" || checkURLValue(u) != nil {
				return
			}
			family := "sap-" + role
			faces = append(faces, fmt.Sprintf("@font-face {\n  font-family: %q;\n  src: url(%q) format(\"woff2\");\n  font-weight: 100 900;\n  font-display: swap;\n}\n", family, u))
			fallback := fontStacks["system"]
			if role == "mono" {
				fallback = fontStacks["mono"]
			}
			stack = fmt.Sprintf("%q, %s", family, fallback)
		default:
			stack = fontStacks[f.Stack]
		}
		if stack != "" {
			set("--sap-font-"+role, stack)
		}
	}
	font("body", d.Fonts.Body)
	font("heading", d.Fonts.Heading)
	font("mono", d.Fonts.Mono)

	if v, ok := TextScales[d.Scale.Text]; ok {
		set("--sap-text-scale", v)
	}
	if v, ok := RadiusScales[d.Scale.Radius]; ok {
		set("--sap-radius-scale", v)
	}
	if v, ok := DensityScales[d.Scale.Density]; ok {
		set("--sap-space-scale", v)
	}
	if v, ok := ShadowScales[d.Scale.Shadow]; ok {
		set("--sap-shadow-scale", v)
	}
	if v, ok := Widths[d.Scale.Width]; ok {
		set("--sap-content-width", v)
	}

	for _, f := range faces {
		b.WriteString(f)
	}
	if len(vars) > 0 {
		b.WriteString(":root {\n")
		b.WriteString(strings.Join(vars, "\n"))
		b.WriteString("\n}\n")
	}
	if d.Fonts.Heading.Stack != "" {
		// Zero specificity: theme rules on specific headings still win,
		// but every theme's base heading font follows the choice.
		b.WriteString(":where(h1, h2, h3, h4, h5, h6) { font-family: var(--sap-font-heading); }\n")
	}
	if n := d.Footer.Columns; n > 0 {
		// At most n columns; they wrap on narrow screens because no
		// column gets narrower than 12rem.
		fmt.Fprintf(&b, ":root .widget-area.widget-area-footer { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%%, max(12rem, (100%% - %d * 2.5rem) / %d)), 1fr)); }\n", n-1, n)
		b.WriteString(":root .widget-area.widget-area-footer > .widget { grid-column: auto; }\n")
	}
	if len(d.Motion.Content) > 0 {
		// The theme's texts and images the design animates: the motion
		// library's keyframes (ui/static/motion.css), paused until
		// motion.js marks the element in view.
		keys := make([]string, 0, len(d.Motion.Content))
		for k := range d.Motion.Content {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			a, ok := motion.Lookup(d.Motion.Content[k])
			if !ok || !safeKey(k) {
				continue
			}
			dur, iter, ease := "var(--sap-motion-duration)", "1", "var(--sap-motion-ease)"
			if a.Loop {
				dur, iter, ease = "calc(var(--sap-motion-duration) * 5)", "infinite", "ease-in-out"
			}
			fmt.Fprintf(&b, ".sap-motion [data-sap-edit=%q] { animation: sap-%s %s %s 0s %s both paused; }\n", k, a.Name, dur, ease, iter)
		}
	}
	if css := strings.TrimSpace(d.CSS); css != "" {
		b.WriteString("\n/* Custom CSS */\n")
		b.WriteString(css)
		b.WriteString("\n")
	}
	if b.Len() == 0 {
		return ""
	}
	return "/* sap13ns customiser (generated) */\n" + b.String()
}

// safeKey reports whether a content key can go in a CSS attribute
// selector as it is (theme.json keys are [a-z0-9_-]).
func safeKey(k string) bool {
	if k == "" || len(k) > 40 {
		return false
	}
	for _, r := range k {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

// Hash is the short content hash used in stylesheet URLs.
func Hash(css string) string {
	sum := sha256.Sum256([]byte(css))
	return hex.EncodeToString(sum[:8])
}

// checkURLValue applies the custom-CSS url() rule to generated URLs.
func checkURLValue(u string) error {
	t := &cssTokenizer{line: 1}
	return t.checkURL(u)
}
