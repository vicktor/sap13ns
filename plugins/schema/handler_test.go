package main

import (
	"encoding/json"
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

func TestHandlerPost(t *testing.T) {
	env := newEnv(t)
	env.Denied["posts:read"] = true // a posts_query would fail and log
	post := samplePost()
	g := headLD(t, env, &pluginsdk.Page{Kind: "single", URL: post.URL, Lang: "en", Title: post.Title, Post: post})
	a := g["BlogPosting"][0]
	if a["datePublished"] != post.PublishedAt || a["dateModified"] != post.UpdatedAt || a["image"] == nil ||
		a["articleSection"] != "News" || a["wordCount"] != float64(420) {
		t.Errorf("article = %v", a)
	}
	if len(g["ImageObject"]) != 1 || len(g["BreadcrumbList"]) != 1 || len(env.Logs) != 0 {
		t.Errorf("graph %v, logs %+v", g, env.Logs)
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
