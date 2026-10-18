package sdktest_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/vicktor/sap13ns/pluginsdk"
	"github.com/vicktor/sap13ns/pluginsdk/sdktest"
)

// A small plugin exercising every host function.
func init() {
	pluginsdk.Handle(func(req *pluginsdk.Request) (any, error) {
		switch req.Hook {
		case pluginsdk.HookShortcode:
			var sc pluginsdk.Shortcode
			if err := req.Decode(&sc); err != nil {
				return nil, err
			}
			switch sc.Name {
			case "count":
				n := 0
				if v, err := pluginsdk.KVGet("n"); err == nil {
					n = len(v)
				} else if !errors.Is(err, pluginsdk.ErrNotFound) {
					return nil, err
				}
				if err := pluginsdk.KVSet("n", []byte(strings.Repeat("x", n+1))); err != nil {
					return nil, err
				}
				return pluginsdk.HTML("<b>" + strings.Repeat("|", n+1) + "</b>"), nil
			case "posts":
				posts, err := pluginsdk.QueryPosts(pluginsdk.PostsQuery{Tag: sc.Attrs["tag"], ExcludeIDs: []int64{2}})
				if err != nil {
					return nil, err
				}
				var titles []string
				for _, p := range posts {
					titles = append(titles, p.Title)
				}
				return pluginsdk.HTML(strings.Join(titles, ",")), nil
			case "boom":
				panic("boom")
			}
			return pluginsdk.HTML(req.Setting("greeting") + ", " + pluginsdk.Escape(sc.Attrs["name"])), nil
		case pluginsdk.HookHeadMeta:
			return pluginsdk.Head([]pluginsdk.Meta{{Name: "x", Content: "y"}}, map[string]any{"@type": "Thing"}), nil
		case pluginsdk.HookCommentCheck:
			var c pluginsdk.CommentCheck
			req.Decode(&c)
			if strings.Count(c.Content, "http") > 2 {
				return pluginsdk.Verdict(pluginsdk.VerdictSpam, "links"), nil
			}
			return nil, nil
		case pluginsdk.HookFormSubmit:
			var f pluginsdk.FormSubmission
			req.Decode(&f)
			if f.Fields["message"] == "" {
				return pluginsdk.FormErrors("missing", map[string]string{"message": "required"}), nil
			}
			if err := pluginsdk.SendMail(pluginsdk.Mail{To: f.Fields["to"], Subject: "Hi", Body: f.Fields["message"]}); err != nil {
				return pluginsdk.FormErrors(err.Error(), nil), nil
			}
			return pluginsdk.FormOK("sent"), nil
		case pluginsdk.HookEvent:
			resp, err := pluginsdk.Fetch(pluginsdk.HTTPRequest{URL: "https://api.example.test/ping"})
			if err != nil {
				pluginsdk.Log(pluginsdk.Warn, "fetch: "+err.Error())
				return nil, nil
			}
			pluginsdk.Log(pluginsdk.Info, req.Event+" "+resp.Body)
		}
		return nil, nil
	})
}

func TestShortcodeAndKV(t *testing.T) {
	env := sdktest.New(t)
	env.Plugin.Settings["greeting"] = "Hola"
	if html, err := env.Shortcode("hello", map[string]string{"name": "<Ada>"}, ""); err != nil || html != "Hola, &lt;Ada&gt;" {
		t.Fatalf("hello = %q, %v", html, err)
	}
	for i, want := range []string{"<b>|</b>", "<b>||</b>"} {
		if html, err := env.Shortcode("count", nil, ""); err != nil || html != want {
			t.Fatalf("count #%d = %q, %v", i, html, err)
		}
	}
	env.Denied["kv"] = true
	if _, err := env.Shortcode("count", nil, ""); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("kv without permission: %v", err)
	}
	if _, err := env.Shortcode("boom", nil, ""); err == nil || err.Error() != "plugin panic" {
		t.Fatalf("panic = %v", err)
	}
}

func TestPostsQuery(t *testing.T) {
	env := sdktest.New(t)
	tag := []pluginsdk.Term{{Taxonomy: "tag", Slug: "go"}}
	env.Posts = []pluginsdk.Post{{ID: 1, Title: "A", Terms: tag}, {ID: 2, Title: "B", Terms: tag}, {ID: 3, Title: "C"}}
	if html, err := env.Shortcode("posts", map[string]string{"tag": "go"}, ""); err != nil || html != "A" {
		t.Fatalf("posts = %q, %v", html, err)
	}
}

func TestHeadCommentForm(t *testing.T) {
	env := sdktest.New(t)
	r, err := env.Head(&pluginsdk.Page{Kind: "home"})
	if err != nil || len(r.Meta) != 1 || len(r.JSONLD) != 1 {
		t.Fatalf("head = %+v, %v", r, err)
	}
	var ld map[string]string
	if json.Unmarshal(r.JSONLD[0], &ld); ld["@type"] != "Thing" {
		t.Fatalf("jsonld = %s", r.JSONLD[0])
	}
	if v, _, err := env.CheckComment(pluginsdk.CommentCheck{Content: "http http http"}); err != nil || v != pluginsdk.VerdictSpam {
		t.Fatalf("verdict = %q, %v", v, err)
	}
	if v, _, _ := env.CheckComment(pluginsdk.CommentCheck{Content: "hello"}); v != pluginsdk.VerdictOK {
		t.Fatalf("verdict = %q", v)
	}

	res, err := env.SubmitForm("contact", map[string]string{})
	if err != nil || res.OK || res.Errors["message"] != "required" {
		t.Fatalf("form = %+v, %v", res, err)
	}
	if res, _ := env.SubmitForm("contact", map[string]string{"message": "hi", "to": "visitor@evil.test"}); res.OK {
		t.Fatal("mail to an arbitrary address was accepted")
	}
	env.MailRecipients = []string{"Team@Example.test"}
	for _, to := range []string{"admin", "team@example.test"} {
		if res, _ := env.SubmitForm("contact", map[string]string{"message": "hi", "to": to}); !res.OK {
			t.Fatalf("mail to %s refused: %+v", to, res)
		}
	}
	if len(env.Mails) != 2 {
		t.Fatalf("mails = %d", len(env.Mails))
	}
}

func TestFetch(t *testing.T) {
	env := sdktest.New(t)
	env.Event("post.published", nil)
	if len(env.Logs) != 1 || !strings.Contains(env.Logs[0].Text, "permission denied") {
		t.Fatalf("logs = %+v", env.Logs)
	}
	env.HTTP = func(req pluginsdk.HTTPRequest) (*pluginsdk.HTTPResponse, error) {
		return &pluginsdk.HTTPResponse{Status: 200, Body: "pong"}, nil
	}
	env.Event("post.published", nil)
	if got := env.Logs[len(env.Logs)-1].Text; got != "post.published pong" {
		t.Fatalf("log = %q", got)
	}
}

func TestNoHostOutsideSandbox(t *testing.T) {
	if _, err := pluginsdk.KVGet("k"); !errors.Is(err, pluginsdk.ErrUnavailable) {
		t.Fatalf("KVGet without a host = %v", err)
	}
}
