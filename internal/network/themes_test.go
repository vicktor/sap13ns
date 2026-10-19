package network

import (
	"archive/zip"
	"bytes"
	"errors"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// themeZip is a small valid theme in folder slug: marker shows on its
// pages.
func themeZip(t *testing.T, slug, marker string) []byte {
	t.Helper()
	files := map[string]string{
		"theme.json": `{"name": "Tiny", "version": "1.0.0"}`,
		"templates/layout.html": `{{define "layout"}}<!doctype html><html lang="{{.Site.Lang}}"><head>{{template "shared/head" .}}</head>
<body><main>{{template "content" .}}</main><footer>{{template "shared/footer-meta" .}}</footer></body></html>{{end}}`,
		"templates/home.html":   `{{define "content"}}<p>` + marker + `</p>{{range .Posts}}<h2>{{.Title}}</h2>{{end}}{{end}}`,
		"templates/single.html": `{{define "content"}}<p>` + marker + `</p>{{with .Post}}<h1>{{.Title}}</h1>{{end}}{{end}}`,
		"templates/404.html":    `{{define "content"}}<p>` + marker + `</p>{{end}}`,
		"static/theme.css":      `body{margin:0}` + strings.Repeat("\n/* padding */", 200),
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(slug + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(content))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestThemePolicy: the plan's theme_uploads decides, the files count
// against its storage, and a site's themes are its own.
func TestThemePolicy(t *testing.T) {
	e := newNetEnv(t, nil)
	if _, err := e.n.db.Exec(e.ctx, `INSERT INTO plans (slug, name, paid, hand_approved, commercial, sites_per_grant, storage_bytes,
		custom_domains, plugin_uploads, theme_uploads, position) VALUES ('cramped', 'Cramped', false, false, false, 1, 1, 0, false, true, 9)`); err != nil {
		t.Fatal(err)
	}
	themes := func(s *Site) []string {
		t.Helper()
		ents, err := os.ReadDir(filepath.Join(e.n.Dir(s.ID), "themes"))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Fatal(err)
		}
		var names []string
		for _, d := range ents {
			names = append(names, d.Name())
		}
		return names
	}
	pkg := themeZip(t, "tiny", "tiny-marker")
	const upload = "/admin/appearance/themes/upload"

	// Personal: no form, and a POST is refused.
	free := e.personal("free")
	fb, fcsrf := e.siteAdmin(free, "admin")
	freeURL := "https://" + free.PrimaryHost
	page := fb.get(freeURL + "/admin/appearance/themes")
	// The hint names the plan, in the site's language.
	if body := page.Body.String(); page.Code != http.StatusOK || strings.Contains(body, `action="`+upload+`"`) ||
		!strings.Contains(body, "On the Personal plan") && !strings.Contains(body, "En el plan Personal") {
		t.Fatalf("Personal themes page: %d", page.Code)
	}
	if rec := fb.send(free, upload, fcsrf, "theme", "tiny.zip", pkg, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("Personal upload: %d", rec.Code)
	}
	if names := themes(free); len(names) != 0 {
		t.Fatalf("left %v", names)
	}

	// Pro: installed into the site's directory, counted, not activated.
	pro := e.tenant("pro", "pro")
	pb, pcsrf := e.siteAdmin(pro, "admin")
	proURL := "https://" + pro.PrimaryHost
	before := e.storage(pro.ID)
	if rec := pb.send(pro, upload, pcsrf, "theme", "tiny.zip", pkg, ""); rec.Code != http.StatusSeeOther {
		t.Fatalf("Pro upload: %d %s", rec.Code, rec.Body.String())
	}
	used := e.storage(pro.ID) - before
	if names := themes(pro); len(names) != 1 || names[0] != "tiny" || used < int64(len(`body{margin:0}`)) {
		t.Fatalf("installed %v, %d bytes counted", names, used)
	}
	files, err := dirBytes(filepath.Join(e.n.Dir(pro.ID), "themes"))
	if err != nil || files != used {
		t.Fatalf("counted %d, on disk %d (%v)", used, files, err)
	}
	if audits(t, e.siteStore(pro), "theme.uploaded") != 1 {
		t.Fatal("not audited")
	}
	if body := pb.get(proURL + "/?preview_theme=tiny").Body.String(); !strings.Contains(body, "tiny-marker") {
		t.Fatal("no preview")
	}
	if body := e.do("GET", proURL+"/").Body.String(); strings.Contains(body, "tiny-marker") {
		t.Fatal("activated on upload")
	}

	// Another site does not see it, as admin or not.
	if body := fb.get(freeURL + "/admin/appearance/themes").Body.String(); strings.Contains(body, `id="theme-tiny"`) {
		t.Fatal("listed on another site")
	}
	if body := fb.get(freeURL + "/?preview_theme=tiny").Body.String(); strings.Contains(body, "tiny-marker") {
		t.Fatal("previewed on another site")
	}
	if rec := fb.do("POST", freeURL+"/admin/appearance/themes/tiny/activate", url.Values{"csrf": {fcsrf}}); rec.Code != http.StatusNotFound {
		t.Fatalf("activated on another site: %d", rec.Code)
	}
	if rec := e.do("GET", freeURL+"/themes/tiny/static/theme.css"); rec.Code != http.StatusNotFound {
		t.Fatalf("served on another site: %d", rec.Code)
	}

	// Deleting gives the bytes back.
	if rec := pb.do("POST", proURL+"/admin/appearance/themes/tiny/delete", url.Values{"csrf": {pcsrf}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("delete: %d", rec.Code)
	}
	if names := themes(pro); len(names) != 0 || e.storage(pro.ID) != before {
		t.Fatalf("after the delete: %v, %d counted (was %d)", names, e.storage(pro.ID), before)
	}

	// Over the plan's storage: 413, nothing left, nothing counted.
	small := e.tenant("small", "cramped")
	sb, scsrf := e.siteAdmin(small, "admin")
	was := e.storage(small.ID)
	if rec := sb.send(small, upload, scsrf, "theme", "tiny.zip", pkg, ""); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over the quota: %d %s", rec.Code, rec.Body.String())
	}
	if names := themes(small); len(names) != 0 || e.storage(small.ID) != was {
		t.Fatalf("over the quota: left %v, %d counted (was %d)", names, e.storage(small.ID), was)
	}

	// A downgrade stops uploads; the uploaded themes stay.
	if rec := pb.send(pro, upload, pcsrf, "theme", "tiny.zip", pkg, ""); rec.Code != http.StatusSeeOther {
		t.Fatalf("upload again: %d", rec.Code)
	}
	e.setPlan(pro, PlanPersonal)
	if rec := pb.send(pro, upload, pcsrf, "theme", "tiny.zip", themeZip(t, "tiny", "v2"), ""); rec.Code != http.StatusForbidden {
		t.Fatalf("after the downgrade: %d", rec.Code)
	}
	if body := pb.get(proURL + "/admin/appearance/themes").Body.String(); !strings.Contains(body, `id="theme-tiny"`) ||
		strings.Contains(body, `action="`+upload+`"`) {
		t.Fatal("after the downgrade")
	}
	if body := pb.get(proURL + "/?preview_theme=tiny").Body.String(); !strings.Contains(body, "tiny-marker") {
		t.Fatal("the downgrade changed the theme")
	}
}
