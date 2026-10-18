//go:build wasip1

package main

import (
	"github.com/vicktor/sap13ns/pluginsdk"
)

// The SDK's Post type has no language, so the payload is decoded here.
type filterPayload struct {
	HTML string `json:"html"`
	Post *struct {
		Type string `json:"type"`
		Lang string `json:"lang"`
	} `json:"post"`
}

func init() {
	pluginsdk.Handle(func(req *pluginsdk.Request) (any, error) {
		if req.Hook != pluginsdk.HookContentFilter {
			return nil, nil
		}
		var p filterPayload
		if err := req.Decode(&p); err != nil {
			return nil, err
		}
		typ, lang := "", ""
		if p.Post != nil {
			typ, lang = p.Post.Type, p.Post.Lang
		}
		out, changed := Filter(p.HTML, OptionsFrom(req.Plugin.Settings, typ, lang, req.Site.Language))
		if !changed {
			return nil, nil // the host keeps the HTML as it was
		}
		return pluginsdk.HTML(out), nil
	})
}

func main() {}
