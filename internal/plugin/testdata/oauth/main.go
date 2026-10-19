//go:build wasip1

// Test fixture for OAuth connections: an event whose post slug names a
// provider logs what pluginsdk.OAuth returns, and the [token] shortcode
// tries the same while a page renders, where it must be denied.
package main

import (
	"strings"

	"github.com/vicktor/sap13ns/pluginsdk"
)

func token(provider string) string {
	t, err := pluginsdk.OAuth(provider)
	if err != nil {
		return "oauth " + provider + ": " + err.Error()
	}
	return "token " + provider + " " + t.AccessToken + " " + t.ExpiresAt + " " + t.AccountID + " " + t.AccountName + " " +
		strings.Join(t.Scopes, ",")
}

func init() {
	pluginsdk.Handle(func(req *pluginsdk.Request) (any, error) {
		switch req.Hook {
		case pluginsdk.HookEvent:
			var p struct {
				Slug     string `json:"slug"`
				Provider string `json:"provider"`
			}
			req.Decode(&p)
			if req.Event == pluginsdk.EventOAuthConnected {
				pluginsdk.Log(pluginsdk.Info, "connected "+p.Provider)
				return nil, nil
			}
			for _, provider := range strings.Split(p.Slug, "+") {
				pluginsdk.Log(pluginsdk.Info, token(provider))
			}
		case pluginsdk.HookShortcode:
			return pluginsdk.HTML(pluginsdk.Escape(token("demo"))), nil
		}
		return nil, nil
	})
}

func main() {}
