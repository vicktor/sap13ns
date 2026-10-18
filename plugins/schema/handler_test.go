package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/vicktor/sap13ns/pluginsdk"
	"github.com/vicktor/sap13ns/pluginsdk/sdktest"
)

func newEnv(t *testing.T) *sdktest.Env {
	env := sdktest.New(t)
	env.Site = testSite
	logged = map[string]bool{} // logOnce state of the previous test
	return env
}

// shortPost is what the host's renderPage sends to head.meta today.
func shortPost() *pluginsdk.Post {
	p := samplePost()
	return &pluginsdk.Post{ID: p.ID, Type: p.Type, Title: p.Title, Slug: p.Slug, Lang: p.Lang, URL: p.URL, Excerpt: p.Excerpt, Author: p.Author}
}

func headLD(t *testing.T, env *sdktest.Env, page *pluginsdk.Page) map[string][]map[string]any {
	t.Helper()
	resp, err := env.Head(page)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.JSONLD) != 1 || len(resp.Meta) != 0 {
		t.Fatalf("want one JSON-LD object and no meta tags, got %s", resp.Raw)
	}
	var raw map[string]any
	if err := json.Unmarshal(resp.JSONLD[0], &raw); err != nil {
		t.Fatal(err)
	}
	out := map[string][]map[string]any{}
	for _, v := range raw["@graph"].([]any) {
		m := v.(map[string]any)
		out[m["@type"].(string)] = append(out[m["@type"].(string)], m)
	}
	return out
}

func TestHandlerLookupMergesFullPost(t *testing.T) {
	env := newEnv(t)
	full := samplePost()
	other := samplePost()
	other.ID, other.URL, other.PublishedAt = 8, site+"/blog/hello-world-again", "2026-06-01T00:00:00Z"
	env.Posts = []pluginsdk.Post{*other, *full}
	g := headLD(t, env, &pluginsdk.Page{Kind: "single", URL: full.URL, Lang: "en", Title: full.Title, Post: shortPost()})
	a := g["BlogPosting"][0]
	if a["datePublished"] != full.PublishedAt || a["image"] == nil || a["articleSection"] != "News" || a["wordCount"] != float64(420) {
		t.Errorf("lookup not merged: %v", a)
	}
}

func TestHandlerLookupDenied(t *testing.T) {
	env := newEnv(t)
	env.Denied["posts:read"] = true
	env.Posts = []pluginsdk.Post{*samplePost()}
	g := headLD(t, env, &pluginsdk.Page{Kind: "single", URL: samplePost().URL, Lang: "en", Title: "Hello world", Post: shortPost()})
	a := g["BlogPosting"][0]
	if _, ok := a["datePublished"]; ok {
		t.Errorf("a date without the lookup: %v", a)
	}
	if a["headline"] != "Hello world" || a["author"].(map[string]any)["name"] != "Ada Lovelace" {
		t.Errorf("known fields missing: %v", a)
	}
	if len(env.Logs) != 1 || !strings.Contains(env.Logs[0].Text, "posts:read") {
		t.Errorf("logs = %+v", env.Logs)
	}
}

func TestHandlerLookupNotFound(t *testing.T) {
	env := newEnv(t)
	other := samplePost()
	other.ID = 99
	env.Posts = []pluginsdk.Post{*other} // same title, different post
	g := headLD(t, env, &pluginsdk.Page{Kind: "single", URL: samplePost().URL, Lang: "en", Title: "Hello world", Post: shortPost()})
	if _, ok := g["BlogPosting"][0]["datePublished"]; ok {
		t.Error("data merged from another post")
	}
}

func TestHandlerSkipsLookupForCompletePosts(t *testing.T) {
	env := newEnv(t)
	env.Denied["posts:read"] = true // would log if a query were made
	full := samplePost()
	g := headLD(t, env, &pluginsdk.Page{Kind: "single", URL: full.URL, Lang: "en", Title: full.Title, Post: full})
	if g["BlogPosting"][0]["datePublished"] != full.PublishedAt || len(env.Logs) != 0 {
		t.Errorf("complete post: %v, logs %+v", g["BlogPosting"][0], env.Logs)
	}
}

func TestHandler404AndOtherHooks(t *testing.T) {
	env := newEnv(t)
	resp, err := env.Head(&pluginsdk.Page{Kind: "404", URL: site + "/x", Lang: "en"})
	if err != nil || len(resp.JSONLD) != 0 {
		t.Fatalf("404: %s, %v", resp.Raw, err)
	}
	resp, err = env.Call(pluginsdk.HookWidget, map[string]string{})
	if err != nil || string(resp.Raw) != "{}" {
		t.Fatalf("other hook: %s, %v", resp.Raw, err)
	}
}

func TestHandlerWarningsLoggedOnce(t *testing.T) {
	env := newEnv(t)
	env.Plugin.Settings["publisher_url"] = "not a url"
	for range 3 {
		headLD(t, env, &pluginsdk.Page{Kind: "home", URL: site + "/", Lang: "en"})
	}
	if len(env.Logs) != 1 {
		t.Errorf("logs = %+v", env.Logs)
	}
}

func TestSearchQuery(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Hello world", "Hello world"},
		{"¿Qué es sap13ns? Una guía", "Qué es sap13ns Una guía"},
		{`Go or Rust: "the" -choice`, "Go Rust the choice"},
		{"e-mail & don't", "e mail don t"},
		{"one two three four five six seven eight nine ten", "one two three four five six seven eight"},
		{"🚀 !!!", ""},
		{"日本語 タイトル", "日本語 タイトル"},
	} {
		if got := SearchQuery(tc.in); got != tc.want {
			t.Errorf("SearchQuery(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestLookupQuery(t *testing.T) {
	q, ok := LookupQuery(&pluginsdk.Post{Type: "page", Lang: "es", Title: "Sobre mí"})
	if !ok || q.Type != "page" || q.Lang != "es" || q.Search != "Sobre mí" || q.Limit != 20 {
		t.Errorf("query = %+v, %v", q, ok)
	}
	if _, ok := LookupQuery(&pluginsdk.Post{Title: "🚀"}); ok {
		t.Error("an emoji title has nothing to search")
	}
	if !NeedsLookup(shortPost()) || NeedsLookup(samplePost()) || NeedsLookup(nil) {
		t.Error("NeedsLookup")
	}
}
