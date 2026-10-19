// The x-autopost plugin posts new articles to X when they are published.
//
// pluginsdk/autopost runs the events, the queue, the retries and the
// record of what was posted (a post is never sent twice). This plugin
// gives it the X network: the length X counts (count.go), the request to
// POST /2/tweets signed with OAuth 1.0a (oauth1.go, x.go) and what its
// errors mean.
//
// Everything except main.go is plain Go, tested natively with sdktest.
package main

import (
	"strings"
	"time"

	"github.com/vicktor/sap13ns/pluginsdk"
	"github.com/vicktor/sap13ns/pluginsdk/autopost"
)

// Config holds the plugin settings, normalized.
type Config struct {
	autopost.Config
	OAuth1
}

// ConfigFrom reads the plugin settings.
func ConfigFrom(s map[string]string) Config {
	get := func(k string) string { return strings.TrimSpace(s[k]) }
	return Config{
		Config: autopost.ConfigFrom(s),
		OAuth1: OAuth1{
			ConsumerKey:    get("api_key"),
			ConsumerSecret: get("api_key_secret"),
			Token:          get("access_token"),
			TokenSecret:    get("access_token_secret"),
		},
	}
}

// Ready reports whether the four credentials are set.
func (c Config) Ready() bool {
	return c.ConsumerKey != "" && c.ConsumerSecret != "" && c.Token != "" && c.TokenSecret != ""
}

// Test hooks.
var (
	now   = time.Now
	sleep = time.Sleep
)

func handle(req *pluginsdk.Request) (any, error) {
	c := ConfigFrom(req.Plugin.Settings)
	p := &autopost.Poster{
		Config:        c.Config,
		NotConfigured: "X is not configured (settings “API key”, “API key secret”, “Access token” and “Access token secret”)",
		Now:           func() time.Time { return now() },
		Sleep:         func(d time.Duration) { sleep(d) },
		Networks: []autopost.Network{{
			Key: "x", Name: "X", Cost: 1,
			Ready:  c.Ready(),
			Limit:  xText,
			Target: "POST " + tweetsURL,
			Send: func(_ pluginsdk.Post, text string) autopost.Result {
				return postX(c.OAuth1, text)
			},
		}},
	}
	return p.Handle(req)
}
