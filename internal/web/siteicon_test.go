package web

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"sap13ns/internal/store"
)

// setIcon makes id the site icon, as saving the settings page does.
func (e *customEnv) setIcon(id int64) {
	e.t.Helper()
	ctx := context.Background()
	st := e.s.settings()
	st.SiteIcon = id
	if err := e.st.SaveSettings(ctx, st); err != nil {
		e.t.Fatal(err)
	}
	if err := e.s.ReloadSettings(ctx); err != nil {
		e.t.Fatal(err)
	}
	e.s.contentChanged()
}

// The site icon replaces the sap13ns icon in the page head and the
// manifest, is served as square PNGs, and falls back to the sap13ns
// files when there is none or it cannot be drawn.
func TestSiteIcon(t *testing.T) {
	e := newCustomEnv(t)
	if body := e.get("/", nil).Body.String(); !strings.Contains(body, "favicon.svg") || strings.Contains(body, "/site-icon/") {
		t.Fatal("without an icon the head links the sap13ns icon")
	}
	if rec := e.get("/site-icon/32.png", nil); rec.Code != 302 || !strings.Contains(rec.Header().Get("Location"), "/static/brand/favicon-32.png") {
		t.Fatalf("no icon: %d %s", rec.Code, rec.Header().Get("Location"))
	}

	img := image.NewNRGBA(image.Rect(0, 0, 300, 200))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	img.Set(5, 5, color.NRGBA{255, 0, 0, 255})
	var buf bytes.Buffer
	png.Encode(&buf, img)
	dir := filepath.Join(e.s.cfg.UploadsDir, "2026", "11")
	os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(filepath.Join(dir, "icon.png"), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	id, err := e.st.CreateMedia(context.Background(), &store.Media{Path: "2026/11/icon.png", OriginalName: "icon.png", MIME: "image/png",
		Size: int64(buf.Len()), Width: 300, Height: 200, SHA256: "icon", Variants: map[string]store.MediaVariant{}})
	if err != nil {
		t.Fatal(err)
	}
	e.setIcon(id)
	v := "?v=" + strconv.FormatInt(id, 10)
	body := e.get("/", nil).Body.String()
	for _, want := range []string{`href="/site-icon/32.png` + v, `href="/site-icon/180.png` + v} {
		if !strings.Contains(body, want) {
			t.Errorf("head lacks %s", want)
		}
	}
	if strings.Contains(body, "favicon.svg") {
		t.Error("the sap13ns icon is still linked")
	}
	if m := e.get("/manifest.webmanifest", nil).Body.String(); !strings.Contains(m, "/site-icon/512.png"+v) || strings.Contains(m, "maskable") {
		t.Errorf("manifest: %s", m)
	}
	rec := e.get("/site-icon/512.png"+v, nil)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("icon: %d %v", rec.Code, rec.Header())
	}
	got, err := png.Decode(rec.Body)
	if err != nil || got.Bounds().Dx() != 512 || got.Bounds().Dy() != 512 {
		t.Fatalf("icon image: %v %v", err, got)
	}
	if page := e.get("/admin/settings", e.admin).Body.String(); !strings.Contains(page, `name="site_icon"`) ||
		!strings.Contains(page, `value="`+strconv.FormatInt(id, 10)+`"`) || !strings.Contains(page, `class="site-icon-preview" src="/site-icon/192.png`+v) {
		t.Error("the settings page does not offer the icon")
	}
	for _, bad := range []string{"/site-icon/33.png", "/site-icon/32.jpg", "/site-icon/x.png", "/site-icon/.png"} {
		if rec := e.get(bad, nil); rec.Code != 404 {
			t.Errorf("%s: %d", bad, rec.Code)
		}
	}

	// A file that cannot be drawn falls back, and is not retried.
	svg := e.addMedia("image/svg+xml", "logo.svg")
	e.setIcon(svg)
	if rec := e.get("/site-icon/192.png", nil); rec.Code != 302 || !strings.Contains(rec.Header().Get("Location"), "/static/brand/icon-192.png") {
		t.Fatalf("broken icon: %d %s", rec.Code, rec.Header().Get("Location"))
	}
}
