package web

import (
	"context"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"sap13ns/internal/store"
)

// setChat saves the switches of the AI provider and the site assistant the
// way their pages do, with edit applied to the assistant's settings.
func (e *customEnv) setChat(assistantOn, aiOn bool, edit func(*store.AssistantSettings)) {
	e.t.Helper()
	ctx := context.Background()
	ai := store.DefaultAISettings()
	ai.Enabled = aiOn
	if err := e.st.SaveAISettings(ctx, ai); err != nil {
		e.t.Fatal(err)
	}
	as := store.DefaultAssistantSettings()
	as.Enabled = assistantOn
	if edit != nil {
		edit(&as)
	}
	if err := e.st.SaveAssistantSettings(ctx, as); err != nil {
		e.t.Fatal(err)
	}
	e.s.resetAI()
	e.s.resetAssistant()
}

func (e *customEnv) savedAssistant() store.AssistantSettings {
	e.t.Helper()
	as, err := e.st.AssistantSettings(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	return as
}

// postForm posts an admin form as the administrator.
func (e *customEnv) postForm(target string, form url.Values) *httptest.ResponseRecorder {
	e.t.Helper()
	form.Set("csrf", e.csrf)
	req := httptest.NewRequest("POST", "http://example.test"+target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return e.do(req, e.admin)
}

// assistantForm is the Site assistant page as it is first shown.
func assistantForm() url.Values {
	return url.Values{"enabled": {"1"}, "embed_provider": {"same"}, "per_ip_hour": {"20"}, "daily_requests": {"300"},
		"monthly_requests": {"3000"}, "bubble_position": {"right"}, "bubble_offset": {"normal"}}
}

var (
	bubbleRoot  = regexp.MustCompile(`<div class="(sap-assistant[^"]*)" data-assistant`)
	bubbleFab   = regexp.MustCompile(`(?s)<a class="sap-assistant-fab"[^>]*>.*?</a>`)
	bubbleLabel = regexp.MustCompile(`<span class="sap-assistant-label">([^<]*)</span>`)
)

// bubble returns the classes of the chat widget on a public page, "" when
// there is none, and its button's markup.
func (e *customEnv) bubble(target string) (class, fab string) {
	e.t.Helper()
	body := e.get(target, nil).Body.String()
	if m := bubbleRoot.FindStringSubmatch(body); m != nil {
		class = m[1]
	}
	return class, bubbleFab.FindString(body)
}

// TestAssistantBubble: the chat button shows only with both switches on,
// in the corner and at the height chosen, with its label in the page's
// language, and as a link to /ask for browsers without JavaScript.
func TestAssistantBubble(t *testing.T) {
	e := newCustomEnv(t)
	e.setLangs("en", "es", "fr")
	for _, c := range []struct{ assistant, ai bool }{{false, false}, {true, false}, {false, true}} {
		e.setChat(c.assistant, c.ai, nil)
		if class, _ := e.bubble("/"); class != "" {
			t.Errorf("assistant %v, AI %v: the button shows (%s)", c.assistant, c.ai, class)
		}
		if rec := e.get("/ask", nil); rec.Code != http.StatusNotFound {
			t.Errorf("assistant %v, AI %v: /ask answers %d", c.assistant, c.ai, rec.Code)
		}
	}

	e.setChat(true, true, nil)
	class, fab := e.bubble("/")
	if class != "sap-assistant sap-assistant-right sap-assistant-offset-normal" {
		t.Errorf("default place: %q", class)
	}
	name := e.s.i18n.T("en", "Site assistant")
	if !strings.Contains(fab, `href="/ask"`) || !strings.Contains(fab, `aria-haspopup="dialog"`) ||
		!strings.Contains(fab, `aria-label="`+name+`"`) || strings.Contains(fab, "sap-assistant-label") {
		t.Errorf("button without a label: %s", fab)
	}
	if class, _ := e.bubble("/es/"); !strings.Contains(class, "sap-assistant-right") {
		t.Errorf("Spanish page: %q", class)
	}
	// The page without JavaScript does not open the widget over itself.
	if body := e.get("/ask", nil).Body.String(); bubbleRoot.MatchString(body) || !strings.Contains(body, `class="sap-ask"`) {
		t.Error("/ask shows the floating button")
	}

	e.setChat(true, true, func(as *store.AssistantSettings) {
		as.Name = "Ayudante"
		as.BubblePosition, as.BubbleOffset, as.BubbleLabel = store.BubbleLeft, store.BubbleHigh, "Questions?"
		as.Text = map[string]store.AssistantText{"es": {BubbleLabel: "¿Dudas?"}}
	})
	for target, want := range map[string]string{"/": "Questions?", "/es/": "¿Dudas?", "/fr/": "Questions?"} {
		class, fab := e.bubble(target)
		if class != "sap-assistant sap-assistant-left sap-assistant-offset-high" {
			t.Errorf("%s: place %q", target, class)
		}
		m := bubbleLabel.FindStringSubmatch(fab)
		if m == nil || html.UnescapeString(m[1]) != want {
			t.Errorf("%s: label %v, want %q", target, m, want)
		}
		// The visible label starts the button's name (no aria-label to
		// hide it), and the assistant's name follows for screen readers.
		if strings.Contains(fab, "aria-label=") || !strings.Contains(fab, `<span class="sap-assistant-sr">Ayudante</span>`) {
			t.Errorf("%s: accessible name: %s", target, fab)
		}
	}
	if _, fab := e.bubble("/es/"); !strings.Contains(fab, `href="/es/ask"`) {
		t.Errorf("Spanish fallback link: %s", fab)
	}

	e.setChat(true, true, func(as *store.AssistantSettings) { as.BubbleOffset = store.BubbleRaised })
	if class, _ := e.bubble("/"); class != "sap-assistant sap-assistant-right sap-assistant-offset-raised" {
		t.Errorf("raised: %q", class)
	}
	// A value the page would not save (an old or edited row) falls back to
	// the default place rather than to a class no theme styles.
	e.setChat(true, true, func(as *store.AssistantSettings) { as.BubblePosition, as.BubbleOffset = "top", "" })
	if class, _ := e.bubble("/"); class != "sap-assistant sap-assistant-right sap-assistant-offset-normal" {
		t.Errorf("unknown place: %q", class)
	}
}

// TestAssistantSettingsSave: the page saves the button's place and labels,
// and refuses what sitecheck refuses.
func TestAssistantSettingsSave(t *testing.T) {
	e := newCustomEnv(t)
	e.setLangs("en", "es")
	e.setChat(false, true, nil)
	tr := func(key string) string { return e.s.i18n.T(e.s.mainLang(), key) }

	page := e.get("/admin/settings/assistant", e.admin).Body.String()
	for _, want := range []string{`name="bubble_position" value="right" data-bubble="position" checked`, `name="bubble_offset" value="normal" data-bubble="offset" checked`,
		`id="bubble_label_es" name="bubble_label_es"`, `data-bubble-preview data-position="right" data-offset="normal"`} {
		if !strings.Contains(page, want) {
			t.Errorf("settings page lacks %s", want)
		}
	}

	form := assistantForm()
	form.Set("bubble_position", "left")
	form.Set("bubble_offset", "raised")
	form.Set("bubble_label", "  Questions?  ")
	form.Set("bubble_label_es", "¿Dudas?")
	if rec := e.postForm("/admin/settings/assistant", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	as := e.savedAssistant()
	if !as.Enabled || as.BubblePosition != "left" || as.BubbleOffset != "raised" || as.BubbleLabel != "Questions?" || as.Text["es"].BubbleLabel != "¿Dudas?" {
		t.Errorf("saved: %+v", as)
	}
	page = e.get("/admin/settings/assistant", e.admin).Body.String()
	if !strings.Contains(page, `value="left" data-bubble="position" checked`) || !strings.Contains(page, `value="raised" data-bubble="offset" checked`) ||
		!strings.Contains(page, `data-position="left" data-offset="raised"`) || !strings.Contains(page, `<span class="bubble-preview-label" data-bubble-preview-label>Questions?</span>`) ||
		!strings.Contains(page, `value="¿Dudas?"`) {
		t.Error("the page does not show what was saved")
	}

	for _, c := range []struct {
		field, value, msg string
	}{
		{"bubble_position", "top", "Choose where the chat button goes."},
		{"bubble_position", "", "Choose where the chat button goes."},
		{"bubble_offset", "sky", "Choose where the chat button goes."},
		{"bubble_label", strings.Repeat("x", 31), "The text is too long."},
		{"bubble_label_es", strings.Repeat("ñ", 31), "The text is too long."},
	} {
		form := assistantForm()
		form.Set(c.field, c.value)
		rec := e.postForm("/admin/settings/assistant", form)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(html.UnescapeString(rec.Body.String()), tr(c.msg)) {
			t.Errorf("%s=%q: %d, want the message %q", c.field, c.value, rec.Code, tr(c.msg))
		}
	}
	if got := e.savedAssistant(); got.BubblePosition != "left" || got.BubbleLabel != "Questions?" {
		t.Errorf("a refused form changed the settings: %+v", got)
	}
	// Thirty characters are fine, whatever their size in bytes.
	form = assistantForm()
	form.Set("bubble_label", strings.Repeat("ñ", 30))
	if rec := e.postForm("/admin/settings/assistant", form); rec.Code != http.StatusSeeOther {
		t.Errorf("30 characters refused: %d", rec.Code)
	}
}

// TestAssistantCacheInvalidation: cached pages follow the switches and the
// button's place as soon as they are saved, from either page.
func TestAssistantCacheInvalidation(t *testing.T) {
	e := newCustomEnv(t)
	e.setChat(false, true, nil)
	for i := 0; i < 2; i++ {
		e.get("/", nil)
	}
	if rec := e.get("/", nil); rec.Header().Get("X-Cache") != "HIT" || bubbleRoot.MatchString(rec.Body.String()) {
		t.Fatalf("cached home: %s", rec.Header().Get("X-Cache"))
	}

	if rec := e.postForm("/admin/settings/assistant", assistantForm()); rec.Code != http.StatusSeeOther {
		t.Fatalf("turn on: %d", rec.Code)
	}
	if class, _ := e.bubble("/"); class != "sap-assistant sap-assistant-right sap-assistant-offset-normal" {
		t.Errorf("after turning it on: %q", class)
	}
	e.get("/", nil) // cached again

	form := assistantForm()
	form.Set("bubble_position", "left")
	form.Set("bubble_label", "Help")
	e.postForm("/admin/settings/assistant", form)
	if class, fab := e.bubble("/"); !strings.Contains(class, "sap-assistant-left") || !strings.Contains(fab, ">Help<") {
		t.Errorf("after moving it: %q %s", class, fab)
	}

	// The AI provider's switch hides and shows the button too.
	ai := url.Values{"provider": {"anthropic"}, "min_role": {"author"}, "monthly_requests": {"300"}, "monthly_images": {"30"}}
	e.get("/", nil)
	if rec := e.postForm("/admin/settings/ai", ai); rec.Code != http.StatusSeeOther {
		t.Fatalf("AI off: %d %s", rec.Code, rec.Body.String())
	}
	if class, _ := e.bubble("/"); class != "" {
		t.Errorf("the button stays with the AI provider off: %q", class)
	}
	ai.Set("enabled", "1")
	e.postForm("/admin/settings/ai", ai)
	if class, _ := e.bubble("/"); !strings.Contains(class, "sap-assistant-left") {
		t.Errorf("the button does not come back with the AI provider: %q", class)
	}

	form.Del("enabled")
	e.postForm("/admin/settings/assistant", form)
	if class, _ := e.bubble("/"); class != "" {
		t.Errorf("after turning it off: %q", class)
	}
}

// TestChatStateInAdmin: the AI provider page says when the chat is off or
// waiting, and the Site assistant page and the dashboard show its state.
func TestChatStateInAdmin(t *testing.T) {
	e := newCustomEnv(t)
	tr := func(key string) string { return html.EscapeString(e.s.i18n.T(e.s.mainLang(), key)) }
	alert := regexp.MustCompile(`(?s)<div class="alert"[^>]*data-chat-alert>.*?</div>`)
	stat := regexp.MustCompile(`(?s)<a class="card stat" href="/admin/settings/assistant" data-chat-stat>.*?</a>`)
	for _, c := range []struct {
		assistant, ai bool
		alert, badge  string
		stat          string
	}{
		{false, true, "The chat on your site is off.", "Off", "Off"},
		{false, false, "The chat on your site is off.", "Off", "Off"},
		{true, false, "The chat on your site is waiting for the AI provider.", "Waiting for the AI provider", "Waiting"},
		{true, true, "", "On", "On"},
	} {
		e.setChat(c.assistant, c.ai, nil)
		page := e.get("/admin/settings/ai", e.admin).Body.String()
		if !strings.Contains(page, "<h1>"+tr("AI provider")+"</h1>") {
			t.Error("the AI page is not called AI provider")
		}
		got := alert.FindString(page)
		switch {
		case c.alert == "" && got != "":
			t.Errorf("assistant %v, AI %v: alert %s", c.assistant, c.ai, got)
		case c.alert != "" && !strings.Contains(got, tr(c.alert)):
			t.Errorf("assistant %v, AI %v: alert %q, want %q", c.assistant, c.ai, got, c.alert)
		case !c.assistant && !strings.Contains(got, `<a href="/admin/settings/assistant">`+tr("Site assistant")+`</a>`):
			t.Errorf("no link to the Site assistant page: %s", got)
		}
		page = e.get("/admin/settings/assistant", e.admin).Body.String()
		if !strings.Contains(page, `<h2>`+tr("Chat on your site")+` <span class="badge"`) || !strings.Contains(page, tr(c.badge)+`</span></h2>`) {
			t.Errorf("assistant %v, AI %v: no %q badge on the Site assistant page", c.assistant, c.ai, c.badge)
		}
		if got := stat.FindString(e.get("/admin/", e.admin).Body.String()); !strings.Contains(got, ">"+tr(c.stat)+"</strong>") {
			t.Errorf("assistant %v, AI %v: dashboard %s", c.assistant, c.ai, got)
		}
	}
	// Authors do not see the card, which links to a page they cannot open.
	if body := e.get("/admin/", e.author).Body.String(); strings.Contains(body, "data-chat-stat") {
		t.Error("an author sees the chat card")
	}
	// The settings menu names the page the same way.
	if body := e.get("/admin/settings", e.admin).Body.String(); !strings.Contains(body, `<a href="/admin/settings/ai">`) ||
		!strings.Contains(body, tr("AI provider")+"</a>") {
		t.Error("the settings menu does not say AI provider")
	}
}
