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

// TestCleanAssistantBubble: the chat button's place and label follow the
// page's rules on restore too.
func TestCleanAssistantBubble(t *testing.T) {
	cur := store.DefaultAssistantSettings()
	cur.BubblePosition, cur.BubbleOffset, cur.BubbleLabel = store.BubbleLeft, store.BubbleRaised, "Help?"
	in := cur
	in.BubblePosition, in.BubbleOffset, in.BubbleLabel = "top", "", strings.Repeat("x", MaxBubbleLabel+1)
	in.Text = map[string]store.AssistantText{"es": {Greeting: "Hola", BubbleLabel: "¿Dudas?\n¿Sí?"}}
	if bad := Assistant(in); !slices.Equal(bad, []string{"bubble_position", "bubble_offset", "bubble_label", "text"}) {
		t.Errorf("bubble checks: %v", bad)
	}
	out, reset := CleanAssistant(in, cur)
	if !slices.Equal(reset, []string{"bubble_position", "bubble_offset", "bubble_label", "text"}) ||
		out.BubblePosition != store.BubbleLeft || out.BubbleOffset != store.BubbleRaised || out.BubbleLabel != "Help?" || out.Text != nil {
		t.Errorf("cleaned to the current values: %v %+v", reset, out)
	}
	// When the current values fail too (a site that never saved them), the
	// defaults: bottom right, in the corner, no label.
	out, _ = CleanAssistant(in, store.AssistantSettings{})
	if out.BubblePosition != store.BubbleRight || out.BubbleOffset != store.BubbleNormal || out.BubbleLabel != "" {
		t.Errorf("cleaned to the defaults: %+v", out)
	}
	ok := store.DefaultAssistantSettings()
	ok.BubbleLabel = strings.Repeat("ñ", MaxBubbleLabel) // characters, not bytes
	ok.Text = map[string]store.AssistantText{"es": {BubbleLabel: "¿Dudas?"}}
	for _, p := range store.BubblePositions {
		for _, o := range store.BubbleOffsets {
			ok.BubblePosition, ok.BubbleOffset = p, o
			if bad := Assistant(ok); len(bad) > 0 {
				t.Errorf("%s %s refused: %v", p, o, bad)
			}
		}
	}
	if out, reset := CleanAssistant(ok, cur); len(reset) > 0 || out.BubbleLabel != ok.BubbleLabel {
		t.Errorf("valid bubble changed: %v", reset)
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

// TestGravatarSettings: Gravatar is on for users by default and off for
// commenters, whose images need the users' setting; a restore that has
// them on alone turns them off.
func TestGravatarSettings(t *testing.T) {
	def := store.DefaultSettings()
	if !def.GravatarUsers || def.GravatarCommenters {
		t.Fatalf("defaults: users %v, commenters %v", def.GravatarUsers, def.GravatarCommenters)
	}
	in := def
	in.GravatarUsers, in.GravatarCommenters = false, true
	if bad := Settings(in, Site{}); !slices.Equal(bad, []string{"gravatar_commenters"}) {
		t.Errorf("commenters without users: %v", bad)
	}
	out, reset := CleanSettings(in, def, Site{})
	if !slices.Equal(reset, []string{"gravatar_commenters"}) || out.GravatarCommenters || out.GravatarUsers {
		t.Errorf("cleaned: %v, users %v, commenters %v", reset, out.GravatarUsers, out.GravatarCommenters)
	}
	in.GravatarUsers = true
	if bad := Settings(in, Site{}); len(bad) > 0 {
		t.Errorf("both on: %v", bad)
	}
}

func TestLoginPath(t *testing.T) {
	for p, ok := range map[string]bool{
		"/entrar": true, "/team/access": true, "/acceso-123": true, "/abc": true, "/" + strings.Repeat("a", 41): true,
		"/ab": false, "/" + strings.Repeat("a", 42): false, "/Entrar": false, "entrar": false, "/entrar/": false, "/a/b/c": false,
		"/abc/def/ghi": false, "/-abc": false, "/a_b": false, "/über": false, "/entrar.php": false, "/entrar?x=1": false,
		"//entrar": false, "": false,
	} {
		if LoginPath.MatchString(p) != ok {
			t.Errorf("LoginPath(%q) = %v", p, !ok)
		}
	}

	// Without a check of the site, only the default passes.
	st := store.DefaultSettings()
	if bad := Settings(st, Site{}); len(bad) > 0 {
		t.Errorf("/login: %v", bad)
	}
	st.LoginPath = "/entrar"
	if bad := Settings(st, Site{}); !slices.Equal(bad, []string{"login_path"}) {
		t.Errorf("/entrar without a check: %v", bad)
	}
	var asked []string
	site := Site{SignIn: func(p string) bool { asked = append(asked, p); return p == "/entrar" }}
	for p, ok := range map[string]bool{"/entrar": true, "/otra": false, "/Entrar": false, "": false, "/login": true} {
		st.LoginPath = p
		if bad := Settings(st, site); (len(bad) == 0) != ok {
			t.Errorf("%q: %v", p, bad)
		}
	}
	slices.Sort(asked)
	if !slices.Equal(asked, []string{"/entrar", "/otra"}) {
		t.Errorf("asked the site about %v", asked) // never about a bad form nor the default
	}

	// A restore keeps this site's address when the backup's is taken here.
	in, cur := store.DefaultSettings(), store.DefaultSettings()
	in.LoginPath, cur.LoginPath = "/otra", "/entrar"
	if out, reset := CleanSettings(in, cur, site); out.LoginPath != "/entrar" || !slices.Equal(reset, []string{"login_path"}) {
		t.Errorf("taken: %q %v", out.LoginPath, reset)
	}
	in.LoginPath = "/entrar"
	if out, reset := CleanSettings(in, store.DefaultSettings(), site); out.LoginPath != "/entrar" || len(reset) > 0 {
		t.Errorf("free: %q %v", out.LoginPath, reset)
	}
}

func TestUnderSignIn(t *testing.T) {
	for src, under := range map[string]bool{
		"/entrar": true, "/entrar/": true, "/ENTRAR": true, "/entrar/forgot": true, "/entrar?x=1": true, "/entrar/x?y=1": true,
		"/entrar-2": false, "/entrarx": false, "/": false, "/blog/entrar": false, "/e": false,
	} {
		if UnderSignIn("/entrar", src) != under {
			t.Errorf("UnderSignIn(/entrar, %q) = %v", src, !under)
		}
	}
	if !UnderSignIn("/team/access", "/team/access/reset") || UnderSignIn("/team/access", "/team") {
		t.Error("two segments")
	}
}
