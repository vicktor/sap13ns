package plugin

import (
	"strconv"
	"strings"
	"testing"
)

func TestPrivacyManifest(t *testing.T) {
	base := func(decls string) string {
		return `{"abi": 1, "id": "p1", "name": "P", "version": "1.0.0", "hooks": ["shortcode"], "shortcodes": ["p"], "privacy": [` + decls + `]}`
	}
	service := `{"type": "service", "name": "YouTube", "service": "youtube", "category": "media", "provider": "Google Ireland Limited",
		"purpose": {"en": "Plays videos.", "es": "Reproduce vídeos."}, "policy": "https://policies.google.com/privacy", "click_to_load": true}`
	for _, tc := range []struct {
		name, decls string
		ok          bool
	}{
		{"none", ``, true},
		{"service", service, true},
		{"cookie", `{"type": "cookie", "name": "_pk_id.*", "category": "analytics", "purpose": "Counts visits.", "lifetime": "13 months"}`, true},
		{"storage", `{"type": "storage", "name": "my-plugin:open", "category": "preferences", "purpose": "Remembers the open panel.", "lifetime": {"en": "Until cleared"}}`, true},
		{"resource", `{"type": "resource", "name": "Images", "category": "media", "provider": "Meta", "purpose": "Feed images."}`, true},
		{"necessary", `{"type": "cookie", "name": "cart", "category": "necessary", "purpose": "Your cart.", "lifetime": "1 day"}`, true},
		{"unknown type", `{"type": "pixel", "name": "x", "category": "media", "provider": "X", "purpose": "x"}`, false},
		{"unknown category", `{"type": "cookie", "name": "x", "category": "marketing", "purpose": "x", "lifetime": "1 day"}`, false},
		{"cookie without lifetime", `{"type": "cookie", "name": "x", "category": "analytics", "purpose": "x"}`, false},
		{"cookie name with a space", `{"type": "cookie", "name": "a b", "category": "analytics", "purpose": "x", "lifetime": "1 day"}`, false},
		{"cookie name with a semicolon", `{"type": "cookie", "name": "a;b", "category": "analytics", "purpose": "x", "lifetime": "1 day"}`, false},
		{"wildcard inside", `{"type": "cookie", "name": "a*b", "category": "analytics", "purpose": "x", "lifetime": "1 day"}`, false},
		{"no purpose", `{"type": "cookie", "name": "x", "category": "analytics", "lifetime": "1 day"}`, false},
		{"empty purpose", `{"type": "cookie", "name": "x", "category": "analytics", "purpose": "", "lifetime": "1 day"}`, false},
		{"markup in purpose", `{"type": "cookie", "name": "x", "category": "analytics", "purpose": "<b>x</b>", "lifetime": "1 day"}`, false},
		{"long purpose", `{"type": "cookie", "name": "x", "category": "analytics", "purpose": "` + strings.Repeat("a", 301) + `", "lifetime": "1 day"}`, false},
		{"service without id", `{"type": "service", "name": "YouTube", "category": "media", "provider": "Google", "purpose": "x"}`, false},
		{"service id on a cookie", `{"type": "cookie", "name": "x", "service": "x1", "category": "analytics", "purpose": "x", "lifetime": "1 day"}`, false},
		{"bad service id", `{"type": "service", "name": "YouTube", "service": "You Tube", "category": "media", "provider": "Google", "purpose": "x"}`, false},
		{"service without provider", `{"type": "service", "name": "YouTube", "service": "youtube", "category": "media", "purpose": "x"}`, false},
		{"click to load on a resource", `{"type": "resource", "name": "Images", "category": "media", "provider": "Meta", "purpose": "x", "click_to_load": true}`, false},
		{"http policy", `{"type": "resource", "name": "Images", "category": "media", "provider": "Meta", "purpose": "x", "policy": "http://meta.example"}`, false},
		{"javascript policy", `{"type": "resource", "name": "Images", "category": "media", "provider": "Meta", "purpose": "x", "policy": "javascript:alert(1)"}`, false},
		{"long language key", `{"type": "resource", "name": "Images", "category": "media", "provider": "Meta", "purpose": {"abcdefghijklm": "x"}}`, false},
		{"unknown field", `{"type": "resource", "name": "Images", "category": "media", "provider": "Meta", "purpose": "x", "domain": "a"}`, false},
	} {
		_, err := ParseManifest([]byte(base(tc.decls)))
		if (err == nil) != tc.ok {
			t.Errorf("%s: err = %v", tc.name, err)
		}
	}
	var many []string
	for i := range maxPrivacyDecls + 1 {
		many = append(many, `{"type": "cookie", "name": "c`+strconv.Itoa(i)+`", "category": "analytics", "purpose": "x", "lifetime": "1 day"}`)
	}
	if _, err := ParseManifest([]byte(base(strings.Join(many, ",")))); err == nil {
		t.Errorf("accepted %d privacy entries", maxPrivacyDecls+1)
	}
	m, err := ParseManifest([]byte(base(service)))
	if err != nil {
		t.Fatal(err)
	}
	if d := m.Privacy[0]; d.Purpose.In("es") != "Reproduce vídeos." || !d.ClickToLoad || d.Service != "youtube" {
		t.Errorf("parsed %+v", d)
	}
}
