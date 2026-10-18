package theme

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The bundled themes declare their default colours twice: as fallbacks in
// src/theme.css (var(--sap-light-primary, #0071e3)) and in theme.json
// "tokens" for the customiser. This keeps the two in step.
func TestBundledTokensMatchCSS(t *testing.T) {
	dirs, _ := filepath.Glob("../../ui/themes/*/theme.json")
	if len(dirs) == 0 {
		t.Skip("no bundled themes")
	}
	rule := regexp.MustCompile(`(?m)(^|\})\s*([^{}@]+?)\s*\{([^{}]*)\}`)
	decl := regexp.MustCompile(`--([a-z-]+):\s*var\(--sap-(light|dark)-([a-z-]+), (#[0-9a-f]{3,6})\)`)
	for _, mf := range dirs {
		slug := filepath.Base(filepath.Dir(mf))
		raw, err := os.ReadFile(mf)
		if err != nil {
			t.Fatal(err)
		}
		var m Manifest
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("%s: %v", slug, err)
		}
		if m.Tokens == nil {
			t.Errorf("%s: theme.json has no tokens", slug)
			continue
		}
		for _, sc := range []map[string]string{m.Tokens.Light, m.Tokens.Dark} {
			for _, k := range []string{"background", "foreground", "card", "card-foreground", "muted", "muted-foreground",
				"border", "primary", "primary-foreground", "accent", "accent-foreground"} {
				if !regexp.MustCompile(`^#[0-9a-f]{6}$`).MatchString(sc[k]) {
					t.Errorf("%s: token %s = %q", slug, k, sc[k])
				}
			}
		}
		css, err := os.ReadFile(filepath.Join(filepath.Dir(mf), "src", "theme.css"))
		if err != nil {
			t.Fatal(err)
		}
		checked := 0
		seenRoot := false
		for _, r := range rule.FindAllStringSubmatch(string(css), -1) {
			sel := strings.TrimSpace(r[2])
			var want func(scheme, tok string) (string, bool)
			switch {
			case sel == ":root" && !seenRoot, sel == ".dark":
				if sel == ":root" {
					seenRoot = true
				}
				want = func(scheme, tok string) (string, bool) {
					v, ok := map[string]map[string]string{"light": m.Tokens.Light, "dark": m.Tokens.Dark}[scheme][tok]
					return v, ok
				}
			case regexp.MustCompile(`^(\.dark)?\.(accent|palette)-[a-z]+$`).MatchString(sel):
				parts := strings.Split(strings.TrimPrefix(sel, ".dark"), "-")
				key, value := strings.TrimPrefix(parts[0], "."), parts[1]
				o := m.Tokens.Options[key][value]
				want = func(scheme, tok string) (string, bool) {
					v, ok := map[string]map[string]string{"light": o.Light, "dark": o.Dark}[scheme][tok]
					return v, ok
				}
			default:
				continue
			}
			for _, d := range decl.FindAllStringSubmatch(r[3], -1) {
				prop, scheme, tok, hex := d[1], d[2], d[3], d[4]
				if prop != tok {
					continue // a private variable that follows the token
				}
				got, ok := want(scheme, tok)
				if !ok {
					t.Errorf("%s %s: --%s falls back to %s but theme.json has no %s.%s", slug, sel, prop, hex, scheme, tok)
					continue
				}
				if got != hex {
					t.Errorf("%s %s: --%s falls back to %s, theme.json says %s", slug, sel, prop, hex, got)
				}
				checked++
			}
		}
		if checked < 6 {
			t.Errorf("%s: only %d token fallbacks found in theme.css", slug, checked)
		}
	}
}
