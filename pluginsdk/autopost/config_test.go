package autopost

import (
	"strings"
	"testing"
	"time"
)

func TestConfigFrom(t *testing.T) {
	c := ConfigFrom(map[string]string{})
	if c.PostTypes != "posts" || c.SkipOlder != 48*time.Hour || c.MaxHashtags != 5 || c.DryRun || len(c.Warnings) != 0 {
		t.Errorf("defaults: %+v", c)
	}
	c = ConfigFrom(map[string]string{"max_hashtags": "50", "skip_older_than_hours": "1.5", "dry_run": "true", "post_types": "both",
		"template": "T", "template_es": "E", "template_en": "N"})
	if c.MaxHashtags != 20 || c.SkipOlder != 90*time.Minute || !c.DryRun || c.PostTypes != "both" ||
		c.Template != "T" || c.TemplateES != "E" || c.TemplateEN != "N" {
		t.Errorf("normalized: %+v", c)
	}
	c = ConfigFrom(map[string]string{"max_hashtags": "-1", "skip_older_than_hours": "0", "post_types": "bogus"})
	if c.MaxHashtags != 5 || c.SkipOlder != 48*time.Hour || c.PostTypes != "posts" {
		t.Errorf("invalid: %+v", c)
	}
}

func TestWants(t *testing.T) {
	tests := []struct {
		setting, typ string
		want         bool
	}{
		{"posts", "post", true}, {"posts", "", true}, {"posts", "page", false},
		{"pages", "post", false}, {"pages", "page", true},
		{"both", "post", true}, {"both", "page", true},
	}
	for _, tt := range tests {
		if got := (Config{PostTypes: tt.setting}).Wants(tt.typ); got != tt.want {
			t.Errorf("%s/%s: %v", tt.setting, tt.typ, got)
		}
	}
}

func TestTemplateFor(t *testing.T) {
	c := Config{TemplateES: "ES", TemplateEN: "EN"}
	if c.TemplateFor("es-MX", "post", nil) != "ES" || c.TemplateFor("en-GB", "post", nil) != "EN" {
		t.Error("language templates")
	}
	if got := c.TemplateFor("fr", "page", nil); got != "New page: {title}\n\n{url}\n\n{hashtags}" {
		t.Errorf("default: %q", got)
	}
	c.Template = "ALL"
	if c.TemplateFor("fr", "post", nil) != "ALL" {
		t.Error("general template")
	}
	own := func(lang, typ string) string { return "own " + lang + " " + typ }
	if got := (Config{}).TemplateFor("de", "post", own); got != "own de post" {
		t.Errorf("plugin default: %q", got)
	}
	if !strings.HasPrefix(DefaultTemplate("es", "page"), "Nueva página:") || !strings.HasPrefix(DefaultTemplate("es", "post"), "Nueva entrada:") {
		t.Error("Spanish defaults")
	}
}

func TestLanguages(t *testing.T) {
	tests := []struct {
		lang    string
		spanish bool
		base    string
	}{
		{"es", true, "es"},
		{"ES-mx", true, "es"},
		{"es_AR", true, "es"},
		{"est", false, "est"},
		{"en", false, "en"},
		{"pt-BR", false, "pt"},
		{"", false, ""},
		{"x", false, ""},
		{"e1", false, ""},
	}
	for _, tt := range tests {
		if IsSpanish(tt.lang) != tt.spanish || BaseLang(tt.lang) != tt.base {
			t.Errorf("%q: spanish %v, base %q", tt.lang, IsSpanish(tt.lang), BaseLang(tt.lang))
		}
	}
}

func TestBaseURL(t *testing.T) {
	tests := map[string]string{
		"https://Mastodon.Social/@alice": "https://mastodon.social",
		"https://pds.example.org/xrpc/":  "https://pds.example.org",
		"http://mastodon.social":         "",
		"https://user:pass@example.org":  "",
		"mastodon.social":                "",
	}
	for in, want := range tests {
		got, ok := BaseURL(in)
		if got != want || ok != (want != "") {
			t.Errorf("BaseURL(%q) = %q, %v", in, got, ok)
		}
	}
	if HostOf("https://social.example.org") != "social.example.org" {
		t.Error("HostOf")
	}
}
