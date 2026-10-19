package sitecheck

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"sap13ns/internal/store"
)

func TestDefaultsPass(t *testing.T) {
	if bad := Settings(store.DefaultSettings(), Site{}); len(bad) > 0 {
		t.Errorf("default settings fail: %v", bad)
	}
	if bad := AI(store.DefaultAISettings()); len(bad) > 0 {
		t.Errorf("default AI settings fail: %v", bad)
	}
	if bad := Assistant(store.DefaultAssistantSettings()); len(bad) > 0 {
		t.Errorf("default assistant settings fail: %v", bad)
	}
}

func TestCleanSettings(t *testing.T) {
	cur := store.DefaultSettings()
	cur.CommentModeration, cur.FeedItems, cur.OGAccent = "all", 30, "#123456"
	cur.SessionDays = 500 // fails too: the default then
	in := store.DefaultSettings()
	in.SiteTitle, in.Languages = "Restored", []string{"en", "xx", "es", "en"}
	in.SiteText = map[string]store.SiteText{"en": {SiteTitle: "EN"}, "fr": {SiteTitle: "FR"}}
	in.WebSubHub = "http://127.0.0.1/hub"
	in.CommentModeration, in.FeedItems, in.OGAccent, in.SessionDays = "nobody", 0, "red", 0
	in.FrontPage, in.FrontPageID = "page", 7
	in.DefaultRole = "admin"

	public := Site{PublicPage: func(id int64) bool { return id == 7 }}
	out, reset := CleanSettings(in, cur, public)
	want := []string{"languages", "site_text", "comment_moderation", "default_role", "feed_items", "websub_hub", "og_accent", "session_days"}
	if !slices.Equal(reset, want) {
		t.Errorf("reset %v, want %v", reset, want)
	}
	if out.SiteTitle != "Restored" || !slices.Equal(out.Languages, []string{"en"}) || !maps.Equal(out.SiteText, map[string]store.SiteText{"en": {SiteTitle: "EN"}}) ||
		out.WebSubHub != "" || out.CommentModeration != "all" || out.FeedItems != 30 || out.OGAccent != "#123456" ||
		out.SessionDays != 14 || out.DefaultRole != "subscriber" || out.FrontPage != "page" || out.FrontPageID != 7 {
		t.Errorf("cleaned: %+v", out)
	}
	if bad := Settings(out, public); len(bad) > 0 {
		t.Errorf("cleaned settings still fail: %v", bad)
	}
	// Without the page, the front page goes back to the posts.
	out, reset = CleanSettings(in, cur, Site{})
	if out.FrontPage != "posts" || !slices.Contains(reset, "front_page") {
		t.Errorf("front page: %s %v", out.FrontPage, reset)
	}
	// A valid value is never changed.
	if out, reset := CleanSettings(cur, store.DefaultSettings(), Site{}); len(reset) != 1 || out.CommentModeration != "all" {
		t.Errorf("valid settings changed: %v", reset)
	}
	if bad := Settings(store.Settings{ActiveTheme: "../x"}, Site{}); !slices.Contains(bad, "active_theme") {
		t.Errorf("theme name: %v", bad)
	}
	if bad := Settings(store.DefaultSettings(), Site{Theme: func(string) bool { return false }}); !slices.Equal(bad, []string{"active_theme"}) {
		t.Errorf("uninstalled theme: %v", bad)
	}
}

func TestCleanAI(t *testing.T) {
	cur := store.DefaultAISettings()
	cur.APIKeyEnc = []byte("key")
	in := cur
	in.Provider, in.BaseURL, in.MonthlyImages, in.TextModel = "evil", "http://127.0.0.1:8080", -1, "m"
	out, reset := CleanAI(in, cur)
	if !slices.Equal(reset, []string{"provider", "monthly_images", "base_url"}) || out.Provider != "anthropic" || out.BaseURL != "" ||
		out.TextModel != "m" || string(out.APIKeyEnc) != "key" {
		t.Errorf("AI: %v %+v", reset, out)
	}
	in.BaseURL, in.AllowLocal = "http://localhost:11434", true
	if bad := AI(in); slices.Contains(bad, "base_url") {
		t.Error("local models refused with allow_local")
	}
	as := store.DefaultAssistantSettings()
	as.Suggestions = []string{"a", "", "c"}
	as.Text = map[string]store.AssistantText{"zz": {Greeting: "?"}}
	as.EmbedBaseURL = "https://user:pw@embed.example.com"
	if bad := Assistant(as); !slices.Equal(bad, []string{"suggestions", "text", "embed_base_url"}) {
		t.Errorf("assistant: %v", bad)
	}
}

func TestMenuURL(t *testing.T) {
	for u, want := range map[string]bool{
		"/": true, "/blog/x?y=1#z": true, "#section": true, "https://example.com/": true, "http://example.com": true,
		"": false, "//evil.example": false, "javascript:alert(1)": false, "JavaScript:alert(1)": false, "data:text/html,x": false,
		"/a b": false, "https://": false, "mailto:x@example.com": false, "/" + strings.Repeat("a", 1000): false,
	} {
		if MenuURL(u) != want {
			t.Errorf("MenuURL(%q) = %v", u, !want)
		}
	}
	if MenuItem("", "/") || MenuItem(strings.Repeat("x", 101), "/") || !MenuItem("Home", "/") {
		t.Error("MenuItem labels")
	}
}

func TestWidgetsAndOptions(t *testing.T) {
	for in, want := range map[string]string{"3": "3", "0": "5", "21": "5", "x": "5", "": "5"} {
		if got := WidgetConfig("recent_posts", map[string]string{"count": in, "other": "x"}); !maps.Equal(got, map[string]string{"count": want}) {
			t.Errorf("count %q: %v", in, got)
		}
	}
	long := strings.Repeat("é", MaxWidgetText) // 2 bytes each
	if got := WidgetConfig("text", map[string]string{"text": long})["text"]; len(got) > MaxWidgetText || !strings.HasPrefix(long, got) || len(got) < MaxWidgetText-1 {
		t.Errorf("text widget: %d bytes", len(got))
	}
	if got := WidgetConfig("search", map[string]string{"x": "y"}); len(got) != 0 {
		t.Errorf("search widget: %v", got)
	}
	if WidgetTitle(strings.Repeat("ñ", 150)) != strings.Repeat("ñ", 100) {
		t.Error("widget title")
	}
	if !Widget(store.Widget{Area: "sidebar", Lang: "en"}, []string{"es", "en"}) || Widget(store.Widget{Area: "sidebar", Lang: "fr"}, []string{"es"}) ||
		Widget(store.Widget{Area: "../x"}, nil) {
		t.Error("widget place")
	}
	out, bad := ThemeOptions(map[string]string{"accent": " violet ", "card_images": "true", "bad key": "x", "long": strings.Repeat("x", 2001)})
	if !maps.Equal(out, map[string]string{"accent": "violet", "card_images": "true"}) || !slices.Equal(bad, []string{"bad key", "long"}) {
		t.Errorf("theme options: %v %v", out, bad)
	}
}
