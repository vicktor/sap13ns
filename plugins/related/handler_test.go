package main

import (
	"strings"
	"testing"

	"github.com/vicktor/sap13ns/pluginsdk"
	"github.com/vicktor/sap13ns/pluginsdk/sdktest"
)

func newEnv(t *testing.T) (*sdktest.Env, *pluginsdk.Post) {
	env := sdktest.New(t)
	clear(logged)
	current := post(1, "es", "2026-05-01T10:00:00Z", tag("go"), cat("dev"))
	current.Title = "Primeros pasos con Go"
	other := post(2, "es", "2026-01-01T23:30:00Z", tag("go"))
	other.Title = "Go & WebAssembly"
	other.Image = "https://example.test/media/go.jpg"
	env.Posts = []pluginsdk.Post{current, other, post(3, "en", "2026-02-01T10:00:00Z", tag("go"))}
	return env, &current
}

func TestFilterHook(t *testing.T) {
	env, current := newEnv(t)
	env.Site.Timezone = "Europe/Madrid"
	got, err := env.Filter("<p>Hola</p>", current)
	want := "<p>Hola</p>\n" + `<aside class="card related-posts" aria-label="Artículos relacionados"><header><h2 class="related-posts-title">Artículos relacionados</h2></header>` +
		`<section><ul class="widget-posts related-posts-list"><li><a href="https://example.test/blog/p2/"><span>Go &amp; WebAssembly<time datetime="2026-01-01T23:30:00Z">2 ene 2026</time></span></a></li></ul></section></aside>`
	if err != nil || got != want {
		t.Fatalf("got\n%s\nwant\n%s\n%v", got, want, err)
	}

	env.Plugin.Settings = map[string]string{"title": "More", "title_en": "Read next", "show_images": "true"}
	if got, _ := env.Filter("<p>Hola</p>", current); !strings.Contains(got, `aria-label="More"`) || !strings.Contains(got, `<img src="https://example.test/media/go.jpg"`) {
		t.Errorf("settings not used: %s", got)
	}
	current.Lang = "en"
	if got, _ := env.Filter("<p>Hi</p>", current); !strings.Contains(got, `>Read next</h2>`) || !strings.Contains(got, `href="https://example.test/blog/p3/"`) {
		t.Errorf("english post: %s", got)
	}
}

func TestFilterHookLeavesHTML(t *testing.T) {
	env, current := newEnv(t)
	page := *current
	page.Type = "page"
	lonely := post(9, "es", "2026-05-01T10:00:00Z", tag("rust"))
	for name, tc := range map[string]struct {
		post     *pluginsdk.Post
		settings map[string]string
	}{
		"page":               {&page, nil},
		"no post":            {nil, nil},
		"nothing related":    {&lonely, nil},
		"after content off":  {current, map[string]string{"after_content": "false"}},
		"posts:read refused": {current, nil},
	} {
		t.Run(name, func(t *testing.T) {
			env.Plugin.Settings = map[string]string{}
			for k, v := range tc.settings {
				env.Plugin.Settings[k] = v
			}
			env.Denied["posts:read"] = name == "posts:read refused"
			r, err := env.Call(pluginsdk.HookContentFilter, pluginsdk.ContentFilter{HTML: "<p>x</p>", Post: tc.post})
			if err != nil || strings.Contains(string(r.Raw), `"html"`) {
				t.Fatalf("returned HTML: %s, %v", r.Raw, err)
			}
		})
	}
	// The refusal is logged once, not returned as an error.
	if len(env.Logs) != 1 || !strings.Contains(env.Logs[0].Text, "cannot list related posts") {
		t.Fatalf("logs: %+v", env.Logs)
	}
}

func TestWidgetHook(t *testing.T) {
	env, current := newEnv(t)
	widget := func(kind, url string, config map[string]string) (string, error) {
		r, err := env.Call(pluginsdk.HookWidget, pluginsdk.Widget{Widget: "related", Config: config,
			Page: &pluginsdk.Page{Kind: kind, URL: url, Lang: "es", Title: current.Title}})
		if err != nil {
			return "", err
		}
		return r.HTML, nil
	}
	got, err := widget("single", strings.TrimSuffix(current.URL, "/"), map[string]string{"count": ""})
	want := `<ul class="widget-posts related-widget"><li><a href="https://example.test/blog/p2/"><span>Go &amp; WebAssembly<time datetime="2026-01-01T23:30:00Z">1 ene 2026</time></span></a></li></ul>`
	if err != nil || got != want {
		t.Fatalf("got\n%s\nwant\n%s\n%v", got, want, err)
	}

	// The widget's count wins over the setting.
	env.Posts = append(env.Posts, post(4, "es", "2026-03-01T10:00:00Z", cat("dev")))
	env.Plugin.Settings["count"] = "1"
	if got, _ := widget("single", current.URL, nil); strings.Count(got, "<li>") != 1 {
		t.Errorf("setting count: %s", got)
	}
	if got, _ := widget("single", current.URL, map[string]string{"count": "5"}); strings.Count(got, "<li>") != 2 {
		t.Errorf("widget count: %s", got)
	}

	for name, page := range map[string][2]string{
		"home":         {"home", "https://example.test/"},
		"archive":      {"archive", current.URL},
		"unknown post": {"single", "https://example.test/blog/missing/"},
	} {
		if got, err := widget(page[0], page[1], nil); err != nil || got != "" {
			t.Errorf("%s: %q, %v", name, got, err)
		}
	}
	if r, err := env.Call(pluginsdk.HookWidget, pluginsdk.Widget{Widget: "other", Page: &pluginsdk.Page{Kind: "single", URL: current.URL}}); err != nil || r.HTML != "" {
		t.Errorf("other widget: %+v, %v", r, err)
	}
	env.Denied["posts:read"] = true
	if got, err := widget("single", current.URL, nil); err != nil || got != "" || len(env.Logs) != 1 {
		t.Errorf("refused: %q, %v, %+v", got, err, env.Logs)
	}
}

func TestUnknownTimeZone(t *testing.T) {
	env, current := newEnv(t)
	env.Site.Timezone = "Mars/Olympus"
	for range 2 {
		if got, err := env.Filter("", current); err != nil || !strings.Contains(got, `>1 ene 2026</time>`) {
			t.Fatalf("got %s, %v", got, err)
		}
	}
	if len(env.Logs) != 1 || !strings.Contains(env.Logs[0].Text, "Mars/Olympus") {
		t.Fatalf("logs: %+v", env.Logs)
	}
	if r, err := env.Call(pluginsdk.HookHeadMeta, map[string]string{}); err != nil || len(r.Raw) != 2 {
		t.Fatalf("other hooks: %s, %v", r.Raw, err)
	}
}
