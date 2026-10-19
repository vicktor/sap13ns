package themeimport

import (
	"bytes"
	"testing"
)

func TestDetect(t *testing.T) {
	for want, names := range map[Kind][]string{
		KindSap13ns:          {"mine/theme.json", "mine/templates/layout.html", "mine/static/theme.css"},
		KindWordPressBlock:   {"tt5/style.css", "tt5/theme.json", "tt5/templates/index.html", "tt5/parts/header.html"},
		KindWordPressClassic: {"style.css", "index.php", "functions.php"},
		KindGhost:            {"casper/package.json", "casper/index.hbs", "casper/post.hbs", "casper/partials/x.hbs"},
		KindHugo:             {"ananke/theme.toml", "ananke/layouts/_default/baseof.html", "ananke/layouts/index.html"},
		KindJekyll:           {"minima/_layouts/default.html", "minima/_includes/head.html", "minima/minima.gemspec"},
		KindUnknown:          {"readme.txt", "x/y.html"},
	} {
		files := map[string][]byte{}
		for _, n := range names {
			files[n] = []byte("x")
		}
		src := zipOf(t, files)
		if got := Detect(bytes.NewReader(src), int64(len(src))); got != want {
			t.Errorf("Detect(%v) = %q, want %q", names, got, want)
		}
	}
	src := zipOf(t, fixture(t))
	if got := Detect(bytes.NewReader(src), int64(len(src))); got != KindWordPressBlock || !got.Convertible() {
		t.Errorf("the fixture is %q", got)
	}
	if got := Detect(bytes.NewReader([]byte("not a zip")), 9); got != KindUnknown {
		t.Errorf("not a zip: %q", got)
	}
}
