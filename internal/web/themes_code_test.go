package web

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestThemeCodeEditor: a built-in theme's first save installs a copy (with
// the original's options), later saves change the copy, and a save the
// checks refuse changes nothing.
func TestThemeCodeEditor(t *testing.T) {
	e := newPluginEnv(t)
	e.english()
	ctx := context.Background()
	get := func(target string) (int, string) {
		t.Helper()
		rec := e.do("GET", target, nil, "", e.cookie)
		return rec.Code, rec.Body.String()
	}
	save := func(slug, file, content string) (int, string, string) {
		t.Helper()
		rec := e.admin("/admin/appearance/themes/"+slug+"/code", url.Values{"file": {file}, "content": {content}})
		return rec.Code, rec.Header().Get("Location"), rec.Body.String()
	}

	if _, b := get("/admin/appearance/themes"); !strings.Contains(b, `href="/admin/appearance/themes/onepage/code"`) {
		t.Fatal("no Edit code button on the themes page")
	}
	code, b := get("/admin/appearance/themes/onepage/code")
	if code != http.StatusOK || !strings.Contains(b, "comes with sap13ns and cannot change") || !strings.Contains(b, `data-code-editor="html"`) ||
		!strings.Contains(b, `value="templates/layout.html"`) || !strings.Contains(b, "?file=static%2ftheme.css") {
		t.Fatalf("editor page: %d", code)
	}
	// The compiled stylesheet opens one rule a line.
	if _, b := get("/admin/appearance/themes/onepage/code?file=static/theme.css"); strings.Count(b, "\n") < 200 || !strings.Contains(b, `data-code-editor="css"`) {
		t.Fatal("the stylesheet is not formatted")
	}

	if err := e.s.store.SaveThemeOptions(ctx, "onepage", map[string]string{"accent": "x"}); err != nil {
		t.Fatal(err)
	}
	orig, _ := e.s.themes.Get("onepage")
	layout, err := orig.ReadEditable("templates/layout.html")
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(layout), "</main>", `</main><p class="code-mark">mark-one</p>`, 1)
	code, loc, body := save("onepage", "templates/layout.html", edited)
	if code != http.StatusSeeOther || loc != "/admin/appearance/themes/onepage-custom/code?file=templates%2Flayout.html" {
		t.Fatalf("copy: %d %s %s", code, loc, body)
	}
	cp, ok := e.s.themes.Get("onepage-custom")
	if !ok || !cp.Own || !cp.Uploaded || cp.Name != "Onepage (custom)" || cp.Upload.Source != "editor" {
		t.Fatalf("the copy: %+v", cp)
	}
	if b, _ := orig.ReadEditable("templates/layout.html"); strings.Contains(string(b), "mark-one") {
		t.Fatal("the built-in theme changed")
	}
	if opts, _ := e.s.store.ThemeOptions(ctx, "onepage-custom"); opts["accent"] != "x" {
		t.Fatalf("the options were not copied: %v", opts)
	}
	if e.audited("theme.copied") != 1 {
		t.Fatal("not audited")
	}
	if code, b := get("/?preview_theme=onepage-custom"); code != http.StatusOK || !strings.Contains(b, "mark-one") {
		t.Fatalf("preview of the copy: %d", code)
	}
	// The copy's page says it can be edited in place.
	if _, b := get("/admin/appearance/themes/onepage-custom/code"); strings.Contains(b, "cannot change") || !strings.Contains(b, "mark-one") {
		t.Fatal("the copy's editor")
	}

	// A second save changes the copy itself.
	code, loc, _ = save("onepage-custom", "templates/layout.html", strings.Replace(edited, "mark-one", "mark-two", 1))
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/admin/appearance/themes/onepage-custom/code") || e.audited("theme.edited") != 1 {
		t.Fatalf("edit: %d %s", code, loc)
	}
	if _, b := get("/?preview_theme=onepage-custom"); !strings.Contains(b, "mark-two") {
		t.Fatal("the edit is not live")
	}
	if _, ok := e.s.themes.Get("onepage-custom-2"); ok {
		t.Fatal("an edit of the copy made another copy")
	}

	// Refused: the checks' problem is shown, nothing changes.
	code, _, body = save("onepage-custom", "templates/layout.html", strings.Replace(edited, "</main>", "</main>{{nosuch 1}}", 1))
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "The change was not saved") || !strings.Contains(body, "nosuch") {
		t.Fatalf("refused: %d", code)
	}
	if _, b := get("/?preview_theme=onepage-custom"); !strings.Contains(b, "mark-two") {
		t.Fatal("a refused save changed the theme")
	}

	// Only the theme's text files.
	for _, f := range []string{"../theme.json", "static/fonts/x.woff2", ".sap13ns-upload.json", "templates/nosuch.html"} {
		if code, _, _ := save("onepage-custom", f, "x"); code != http.StatusNotFound {
			t.Errorf("%s: %d", f, code)
		}
	}
	ents, _ := os.ReadDir(e.s.cfg.ThemesDir)
	if len(ents) != 1 || ents[0].Name() != "onepage-custom" {
		t.Fatalf("in the themes directory: %v", ents)
	}
	if _, err := os.Stat(filepath.Join(e.s.cfg.ThemesDir, "onepage-custom", "src", "theme.css")); err != nil {
		t.Error("the copy lost the stylesheet's source")
	}

	// A plan without theme uploads cannot edit code.
	e.s.limits = &themeLimits{policy: ThemePolicy{Plan: "Personal"}}
	if code, _ := get("/admin/appearance/themes/onepage-custom/code"); code != http.StatusForbidden {
		t.Fatalf("Personal, editor: %d", code)
	}
	if code, _, _ := save("onepage-custom", "templates/layout.html", edited); code != http.StatusForbidden {
		t.Fatalf("Personal, save: %d", code)
	}
	if _, b := get("/admin/appearance/themes"); strings.Contains(b, "/code\"") {
		t.Fatal("Edit code shows on the Personal plan")
	}
}
