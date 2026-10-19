package main

import (
	"time"

	"github.com/vicktor/sap13ns/pluginsdk"
	"github.com/vicktor/sap13ns/pluginsdk/autopost"
)

// Text limits of the networks.
var (
	// 300 graphemes; runes are a safe upper bound.
	blueskyLimit = autopost.Limit{Max: 300, Len: autopost.Runes}
	// 500 characters, every URL counting as 23.
	mastodonLimit = autopost.Limit{Max: 500, Len: autopost.URLsCountAs(23, autopost.Runes)}
)

// Test hooks.
var (
	now   = time.Now
	sleep = time.Sleep
)

// handle answers the events declared in plugin.json through
// autopost.Poster, with Bluesky and Mastodon as its networks.
func handle(req *pluginsdk.Request) (any, error) {
	c := ConfigFrom(req.Plugin.Settings)
	p := poster(c, req.Site.URL)
	return p.Handle(req)
}

func poster(c Config, siteURL string) *autopost.Poster {
	who := c.BlueskyHandle
	if who == "" {
		who = "(no handle configured)"
	}
	inst := c.MastodonInstance
	if inst == "" {
		inst = "(no instance configured)"
	}
	return &autopost.Poster{
		Config:        c.Config,
		NotConfigured: "neither Bluesky nor Mastodon is configured",
		Now:           func() time.Time { return now() },
		Sleep:         func(d time.Duration) { sleep(d) },
		Networks: []autopost.Network{
			{
				Key: "b", Name: "Bluesky", Cost: 2, // sign in, then post
				Ready:  c.Bluesky(),
				Limit:  blueskyLimit,
				Target: "POST " + c.BlueskyService + "/xrpc/com.atproto.repo.createRecord as " + who,
				Send: func(p pluginsdk.Post, text string) autopost.Result {
					return postBluesky(c, text, p, now())
				},
			},
			{
				Key: "m", Name: "Mastodon", Cost: 1,
				Ready:  c.Mastodon(),
				Limit:  mastodonLimit,
				Target: "POST " + inst + "/api/v1/statuses, visibility " + c.MastodonVisibility,
				Send: func(p pluginsdk.Post, text string) autopost.Result {
					return postMastodon(c, siteURL, text, p)
				},
			},
		},
	}
}
