package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"sap13ns/internal/store"
)

var consentRev = regexp.MustCompile(`data-rev="([0-9a-f]+)"`)

func TestConsentOnPublicPages(t *testing.T) {
	e := newCustomEnv(t)

	// By default the core lists its necessary cookies and the click-to-load
	// video players: a footer link and the dialog, but no banner.
	rec := e.get("/", nil)
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /: %d", rec.Code)
	}
	for _, s := range []string{
		`<a href="#sap-consent" data-consent-open>`,
		`<dialog id="sap-consent"`,
		`/static/consent.js`,
		`/static/consent.css`,
		`data-consent-category="necessary"`,
		`data-consent-category="media"`,
		`data-consent-toggle="media"`,
		`data-service="youtube"`,
		`data-service="vimeo"`,
		`data-name="sap13ns-consent"`,
		`data-name="themeMode"`,
		`data-name="` + e.s.cookieName() + `"`,
		`Powered by`,
	} {
		if !strings.Contains(body, s) {
			t.Errorf("missing %s", s)
		}
	}
	if strings.Contains(body, "data-consent-banner") {
		t.Error("a banner for click-to-load content only")
	}
	if n := strings.Count(body, `<dialog id="sap-consent"`); n != 1 {
		t.Errorf("%d dialogs", n)
	}
	// The stylesheet comes before the theme's, so the theme can restyle it.
	if strings.Index(body, "consent.css") > strings.Index(body, "theme.css") {
		t.Error("consent.css after theme.css")
	}
	// No style attribute or inline script in what the consent adds.
	dialog := body[strings.Index(body, `<dialog id="sap-consent"`):]
	if strings.Contains(dialog, "style=") || strings.Contains(dialog, "<script") || strings.Contains(dialog, " on") && regexp.MustCompile(`\son[a-z]+=`).MatchString(dialog) {
		t.Error("inline style or script in the consent markup")
	}
	rev := consentRev.FindStringSubmatch(body)
	if rev == nil {
		t.Fatal("no revision")
	}

	// The administrator wants the banner anyway; a new revision asks again.
	e.saveSettings(func(st *store.Settings) { st.Consent.AlwaysBanner = true; st.Consent.Revision = 3 })
	e.s.contentChanged()
	body = e.get("/", nil).Body.String()
	if !strings.Contains(body, `data-consent-banner`) || !strings.Contains(body, `data-consent-reject`) || !strings.Contains(body, `data-consent-accept`) {
		t.Error("no banner with accept and reject")
	}
	if r2 := consentRev.FindStringSubmatch(body); r2 == nil || r2[1] == rev[1] {
		t.Errorf("revision unchanged: %v", r2)
	}

	// Own texts and policy link.
	e.saveSettings(func(st *store.Settings) {
		st.Consent.Title, st.Consent.Text, st.Consent.PolicyURL = "Galletas", "Usamos galletas.", "/cookies"
	})
	e.s.contentChanged()
	body = e.get("/", nil).Body.String()
	for _, s := range []string{`>Galletas</h2>`, `Usamos galletas.`, `<a href="/cookies">`} {
		if !strings.Contains(body, s) {
			t.Errorf("missing %s", s)
		}
	}

	// Without video embeds only necessary storage is left: no toggles,
	// no banner even when asked for, nothing to accept.
	e.setVideoEmbeds("off")
	body = e.get("/", nil).Body.String()
	if strings.Contains(body, "data-consent-toggle") || strings.Contains(body, "data-consent-banner") || strings.Contains(body, "data-consent-accept") {
		t.Error("something to accept with necessary storage only")
	}
	if !strings.Contains(body, `<dialog id="sap-consent"`) {
		t.Error("no cookie list")
	}

	// Off: nothing at all.
	e.saveSettings(func(st *store.Settings) { st.Consent.Off = true })
	e.s.contentChanged()
	body = e.get("/", nil).Body.String()
	if strings.Contains(body, "consent") {
		t.Error("consent markup while it is off")
	}
	if !strings.Contains(body, "Powered by") {
		t.Error("the credit went with the consent")
	}
}

func TestConsentTranslated(t *testing.T) {
	e := newCustomEnv(t)
	e.saveSettings(func(st *store.Settings) {
		st.Language, st.Languages = "es", []string{"en"}
		st.Consent.Texts = map[string]store.ConsentText{"en": {Title: "Cookie jar"}}
	})
	e.s.contentChanged()
	es := e.get("/", nil).Body.String()
	en := e.get("/en/", nil).Body.String()
	if !strings.Contains(es, "Configuración de cookies") || !strings.Contains(es, "Estrictamente necesarias") {
		t.Error("the Spanish page is not in Spanish")
	}
	if !strings.Contains(en, "Cookie settings") || !strings.Contains(en, "Strictly necessary") {
		t.Error("the English page is not in English")
	}
	e.saveSettings(func(st *store.Settings) { st.Consent.AlwaysBanner = true })
	e.s.contentChanged()
	if en = e.get("/en/", nil).Body.String(); !strings.Contains(en, ">Cookie jar</h2>") {
		t.Error("no English title")
	}
	if es = e.get("/", nil).Body.String(); !strings.Contains(es, ">Cookies en este sitio</h2>") {
		t.Error("the main language does not use its default title")
	}
}

func TestConsentAdmin(t *testing.T) {
	e := newCustomEnv(t)
	rec := e.get("/admin/settings/privacy", e.admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET: %d", rec.Code)
	}
	body := rec.Body.String()
	for _, s := range []string{"youtube-nocookie.com", "sap13ns-consent", "themeMode", `name="policy_url"`} {
		if !strings.Contains(body, s) {
			t.Errorf("missing %s", s)
		}
	}
	if rec := e.get("/admin/settings/privacy", e.author); rec.Code == http.StatusOK {
		t.Error("an author opened the privacy settings")
	}
	if strings.Contains(body, `id="sap-consent"`) {
		t.Error("the consent dialog in the admin")
	}

	form := url.Values{"enabled": {"1"}, "title": {"Cookies"}, "text": {"We bake."}, "policy_url": {"javascript:alert(1)"}}
	if rec := e.postForm("/admin/settings/privacy", form); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a javascript: policy link: %d", rec.Code)
	}
	form.Set("policy_url", "/privacidad#cookies")
	form.Set("ask_again", "1")
	if rec := e.postForm("/admin/settings/privacy", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	c := e.s.settings().Consent
	if c.Off || c.Title != "Cookies" || c.Text != "We bake." || c.PolicyURL != "/privacidad#cookies" || c.Revision != 1 {
		t.Errorf("saved %+v", c)
	}
	form.Del("enabled")
	form.Del("ask_again")
	e.postForm("/admin/settings/privacy", form)
	if c := e.s.settings().Consent; !c.Off || c.Revision != 1 {
		t.Errorf("turned off: %+v", c)
	}
}
