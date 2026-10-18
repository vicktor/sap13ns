//go:build wasip1

package main

import (
	"strings"

	"github.com/vicktor/sap13ns/pluginsdk"
)

// The host's post JSON carries the language; the SDK's Post type does not
// (yet), so the payload is decoded into local types.
type postInfo struct {
	Type string `json:"type"`
	Lang string `json:"lang"`
}

type filterPayload struct {
	HTML string    `json:"html"`
	Post *postInfo `json:"post"`
}

func init() {
	pluginsdk.Handle(func(req *pluginsdk.Request) (any, error) {
		if req.Hook != pluginsdk.HookContentFilter {
			return nil, nil
		}
		var p filterPayload
		if err := req.Decode(&p); err != nil {
			pluginsdk.Log(pluginsdk.Warn, "invalid content.filter payload: "+err.Error())
			return nil, nil
		}
		lang := req.Site.Language
		if p.Post != nil && p.Post.Lang != "" {
			lang = p.Post.Lang
		}
		out, changed := Filter(p.HTML, Config{
			SiteURL:          req.Site.URL,
			Lang:             lang,
			NewTab:           req.Setting("new_tab") == "true",
			SponsoredDomains: ParseDomains(req.Setting("sponsored_domains")),
			UTMDomains:       ParseDomains(req.Setting("utm_domains")),
			UTMSource:        strings.TrimSpace(req.Setting("utm_source")),
			UTMMedium:        strings.TrimSpace(req.Setting("utm_medium")),
			UTMCampaign:      strings.TrimSpace(req.Setting("utm_campaign")),
		})
		if !changed {
			return nil, nil
		}
		return pluginsdk.HTML(out), nil
	})
}

func main() {}
