package web

import (
	"net/http"
	"strings"
	"testing"
)

// Animations (the motion library): a page loads motion.css only when
// something on it moves. A layout animates a section, a section's blocks
// one after another, or one block; the customiser sets the level on
// <html>, and the preview always loads the library.
func TestMotion(t *testing.T) {
	e := newCustomEnv(t)
	const motionCSS = `/static/motion.css`
	if body := e.get("/", nil).Body.String(); strings.Contains(body, motionCSS) {
		t.Fatal("a page without animations loads the motion library")
	}
	if body := e.get("/?customize=draft", e.admin).Body.String(); !strings.Contains(body, motionCSS) {
		t.Error("the customiser's preview does not load the motion library")
	}

	rec, res := e.layout(`{"layout": {"pages": {"home": {"sections": [
	  {"id": "a", "layout": "1", "style": {"animation": "fade-up"}, "columns": [[{"id": "t1", "type": "rich-text", "settings": {"text": "One"}}]]},
	  {"id": "b", "layout": "1-1", "style": {"animation": "zoom-in", "sequence": "on"}, "columns": [[{"id": "t2", "type": "rich-text", "settings": {"text": "Two", "animation": "pop"}}], []]},
	  {"id": "c", "layout": "1", "columns": [[{"id": "t3", "type": "rich-text", "settings": {"text": "Three", "animation": "float"}}]]}]}}}}`)
	if rec.Code != http.StatusOK || !res.OK {
		t.Fatalf("draft: %d %s", rec.Code, rec.Body)
	}
	if rec, _ := e.layout(`{"layout": {"pages": {"home": {"sections": [{"id": "a", "layout": "1", "style": {"animation": "spin"}, "columns": [[]]}]}}}}`); rec.Code == http.StatusOK {
		t.Error("an unknown animation was saved")
	}
	if rec := e.builderPost("/publish"); rec.Code != http.StatusSeeOther {
		t.Fatalf("publish: %d", rec.Code)
	}
	body := e.get("/", nil).Body.String()
	for _, want := range []string{motionCSS, `id="sap-a" data-sap-animate="fade-up"`, `<div class="sap-col" data-sap-stagger="zoom-in">`,
		`<div class="sap-motion-block" data-sap-animate="float">`} {
		if !strings.Contains(body, want) {
			t.Errorf("home lacks %q", want)
		}
	}
	// In a sequence the blocks play the section's animation, not their own.
	if strings.Contains(body, `data-sap-animate="pop"`) || strings.Contains(body, "sap-animation-") {
		t.Error("a block of a sequence keeps its own animation, or the animation became a class")
	}

	// The customiser's level reaches <html>, and the preview's classes.
	rec2, dr := e.draft(`{"motion": {"level": "subtle"}}`)
	if rec2.Code != http.StatusOK || !strings.Contains(dr.Classes, "sap-motion-subtle") {
		t.Fatalf("draft: %d %+v", rec2.Code, dr)
	}
	if rec := e.post("/publish", e.admin, e.csrf); rec.Code != http.StatusSeeOther {
		t.Fatalf("publish design: %d", rec.Code)
	}
	if body := e.get("/search?q=x", nil).Body.String(); !strings.Contains(body, "sap-motion-subtle") || !strings.Contains(body, motionCSS) {
		t.Error("a page of a site with a motion level lacks its class or the library")
	}
	// The customiser offers the animations and the level.
	cz := e.get("/admin/appearance/themes/"+e.slug+"/customize", e.admin).Body.String()
	for _, want := range []string{`name="motion.level"`, "Volver a reproducir las animaciones"} {
		if !strings.Contains(cz, want) {
			t.Errorf("customiser lacks %q", want)
		}
	}
}
