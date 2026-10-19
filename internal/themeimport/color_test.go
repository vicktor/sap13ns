package themeimport

import "testing"

// The derived dark palette keeps text readable: the background and text
// swap, and a vivid accent (a link colour) is lifted until it reads on
// the dark background.
func TestDarken(t *testing.T) {
	white, _ := parseColour("#ffffff")
	black, _ := parseColour("#000000")
	bg, fg := darken(white), darken(black)
	if contrast(bg, fg) < 12 {
		t.Errorf("text on background: %.1f:1", contrast(bg, fg))
	}
	for _, accent := range []string{"#5344f4", "#345c00", "#c4453b", "#0693e3", "#9b51e0"} {
		c, ok := parseColour(accent)
		if !ok {
			t.Fatal(accent)
		}
		if r := contrast(darken(c), bg); r < 4.5 {
			t.Errorf("%s in dark mode: %s, %.1f:1 on the background", accent, darken(c).hex(), r)
		}
	}
	// A pale tint becomes a dark one, not a glowing panel.
	tint, _ := parseColour("#f6f6f6")
	if l := darken(tint).oklab().l; l > 0.3 {
		t.Errorf("a pale tint becomes lightness %.2f", l)
	}
}
