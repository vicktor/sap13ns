//go:build wasip1

package main

import (
	"github.com/vicktor/sap13ns/pluginsdk"
)

// The SDK's Post type has no language, so payloads are decoded here.
type postInfo struct {
	Lang string `json:"lang"`
}

type filterPayload struct {
	HTML string    `json:"html"`
	Post *postInfo `json:"post"`
}

type shortcodePayload struct {
	Name  string            `json:"name"`
	Attrs map[string]string `json:"attrs"`
	Inner string            `json:"inner"`
}

func init() {
	pluginsdk.Handle(func(req *pluginsdk.Request) (any, error) {
		switch req.Hook {
		case pluginsdk.HookShortcode:
			var sc shortcodePayload
			if err := req.Decode(&sc); err != nil {
				return nil, err
			}
			switch sc.Name {
			case "callout":
				return pluginsdk.HTML(Callout(sc.Attrs, sc.Inner)), nil
			case "kbd":
				return pluginsdk.HTML(Kbd(sc.Inner)), nil
			}
		case pluginsdk.HookContentFilter:
			if req.Setting("markdown_alerts") == "false" {
				return nil, nil
			}
			var p filterPayload
			if err := req.Decode(&p); err != nil {
				return nil, err
			}
			lang := req.Site.Language
			if p.Post != nil && p.Post.Lang != "" {
				lang = p.Post.Lang
			}
			if out, changed := FilterAlerts(p.HTML, lang); changed {
				return pluginsdk.HTML(out), nil
			}
		}
		return nil, nil // nothing to change: the host keeps the HTML
	})
}

func main() {}
