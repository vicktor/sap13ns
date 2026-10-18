//go:build wasip1

// Test fixture for the host hooks. It reports what it receives (settings,
// page language, PII, query results) so that the host tests can check
// filtering, permissions and limits from the outside.
package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/vicktor/sap13ns/pluginsdk"
)

func init() {
	pluginsdk.Handle(func(req *pluginsdk.Request) (any, error) {
		switch req.Hook {
		case pluginsdk.HookShortcode:
			var sc pluginsdk.Shortcode
			req.Decode(&sc)
			if sc.Name == "forms" {
				return pluginsdk.HTML(pluginsdk.FormPlaceholder("contact") + `<div data-form="other.contact"></div>` +
					pluginsdk.FormPlaceholder("missing") + `<p data-form="x">p</p>`), nil
			}
			q := pluginsdk.PostsQuery{Category: sc.Attrs["category"], Tag: sc.Attrs["tag"], Lang: sc.Attrs["lang"], Limit: 20}
			for _, s := range strings.Fields(sc.Attrs["exclude"]) {
				id, _ := strconv.ParseInt(s, 10, 64)
				q.ExcludeIDs = append(q.ExcludeIDs, id)
			}
			posts, err := pluginsdk.QueryPosts(q)
			if err != nil {
				return nil, err
			}
			var titles []string
			for _, p := range posts {
				titles = append(titles, p.Title+"/"+p.Lang)
			}
			return pluginsdk.HTML(strings.Join(titles, ",")), nil

		case pluginsdk.HookEvent:
			var p pluginsdk.Post
			req.Decode(&p)
			pluginsdk.Log(pluginsdk.Info, req.Event+" "+p.Slug)
			return nil, nil

		case pluginsdk.HookContentFilter:
			var c pluginsdk.ContentFilter
			req.Decode(&c)
			return pluginsdk.HTML(c.HTML + `<p class="filtered">` + c.Context + `</p>`), nil

		case pluginsdk.HookWidget:
			var w pluginsdk.Widget
			req.Decode(&w)
			s := req.Plugin.Settings
			out := fmt.Sprintf("count=%s show=%s until=%s lang=%s tz=%s starts=%s key=%s mode=%s limit=%s",
				w.Config["count"], w.Config["show"], w.Config["until"], w.Page.Lang, req.Site.Timezone,
				s["starts"], s["api_key"], s["mode"], s["limit"])
			if w.Page.Post != nil {
				out += " post=" + w.Page.Post.Slug
			}
			return pluginsdk.HTML(out), nil

		case pluginsdk.HookHeadMeta:
			var pg pluginsdk.Page
			req.Decode(&pg)
			meta := []pluginsdk.Meta{{Name: "author", Content: "Ada"}, {Name: "bad name!", Content: "x"}, {Name: "a", Property: "b", Content: "both"}}
			if p := pg.Post; p != nil {
				meta = append(meta, pluginsdk.Meta{Name: "keywords", Content: fmt.Sprintf("published=%t updated=%t tags=%v", p.PublishedAt != "", p.UpdatedAt != "", p.Tags)})
			}
			return pluginsdk.Head(meta,
				map[string]any{"@type": "Thing", "name": "</script><script>alert(1)</script>"},
				[]string{"not", "an", "object"},
				map[string]any{"@type": "Big", "data": strings.Repeat("x", 20<<10)}), nil

		case pluginsdk.HookCommentCheck:
			var c pluginsdk.CommentCheck
			req.Decode(&c)
			switch {
			case strings.Contains(c.Content, "loop"):
				for {
				}
			case strings.Contains(c.Content, "fail"):
				return nil, errors.New("backend down")
			case c.Email != "":
				return pluginsdk.Verdict("spam", "saw "+c.Email+" "+c.IP+" "+c.UserAgent), nil
			case strings.Contains(c.Content, "timing"):
				return pluginsdk.Verdict("pending", fmt.Sprintf("elapsed %d", c.ElapsedMS)), nil
			case strings.Contains(c.Content, "hold"):
				return pluginsdk.Verdict("pending", "held by "+req.Plugin.ID), nil
			case strings.Contains(c.Content, "weird"):
				return pluginsdk.Verdict("maybe", ""), nil
			}
			return pluginsdk.Verdict("ok", ""), nil

		case pluginsdk.HookFormSubmit:
			var f pluginsdk.FormSubmission
			req.Decode(&f)
			to := f.Fields["to"]
			if to == "" {
				to = "admin"
			}
			n, _ := strconv.Atoi(f.Fields["n"])
			n = max(n, 1)
			sent := 0
			var last error
			for range n {
				if err := pluginsdk.SendMail(pluginsdk.Mail{To: to, Subject: "Contact", Body: "from " + f.Fields["email"], ReplyTo: f.Fields["email"]}); err != nil {
					last = err
					break
				}
				sent++
			}
			if last != nil {
				return pluginsdk.FormErrors(fmt.Sprintf("sent %d: %v", sent, last), map[string]string{"to": "refused", "bogus": "not a field"}), nil
			}
			return pluginsdk.FormOK(fmt.Sprintf("sent %d in %s", sent, f.Lang)), nil
		}
		return nil, nil
	})
}

func main() {}
