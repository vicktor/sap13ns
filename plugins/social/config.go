// The social plugin posts new articles to Bluesky and Mastodon when they
// are published.
//
// The events, the queue, the text and the record of what was posted where
// (so that a post is never sent twice) come from pluginsdk/autopost, which
// the other official auto-post plugins share. handler.go describes the two
// networks to it, and bluesky.go and mastodon.go build their requests.
//
// Everything except main.go is plain Go, tested natively with sdktest.
package main

import (
	"strings"

	"github.com/vicktor/sap13ns/pluginsdk/autopost"
)

// Config holds the plugin settings, normalized: the ones every auto-post
// plugin shares (autopost.Config) and the networks'.
type Config struct {
	autopost.Config

	BlueskyHandle   string
	BlueskyPassword string
	BlueskyService  string // https://host, no path

	MastodonInstance   string // https://host, no path
	MastodonToken      string
	MastodonVisibility string
}

// The default Bluesky server (keep in sync with plugin.json). The trailing
// slash ends the host for the package scanner, which otherwise reads the
// string stored next to it in the binary as part of the host name.
const defaultService = "https://bsky.social/"

// ConfigFrom reads the plugin settings.
func ConfigFrom(s map[string]string) Config {
	get := func(k string) string { return strings.TrimSpace(s[k]) }
	c := Config{
		Config:             autopost.ConfigFrom(s),
		BlueskyHandle:      strings.TrimPrefix(get("bluesky_handle"), "@"),
		BlueskyPassword:    get("bluesky_app_password"),
		MastodonToken:      get("mastodon_token"),
		MastodonVisibility: get("mastodon_visibility"),
	}
	if c.MastodonVisibility != "unlisted" {
		c.MastodonVisibility = "public"
	}

	c.BlueskyService, _ = autopost.BaseURL(defaultService)
	if v := get("bluesky_service"); v != "" {
		if base, ok := autopost.BaseURL(v); ok {
			c.BlueskyService = base
		} else {
			c.Warnings = append(c.Warnings, "Bluesky server must be an https:// address such as https://bsky.social/; using the default, bsky.social")
		}
	}
	if v := get("mastodon_instance"); v != "" {
		if base, ok := autopost.BaseURL(v); ok {
			c.MastodonInstance = base
		} else {
			c.Warnings = append(c.Warnings, "Mastodon instance must be an https:// address such as https://mastodon.social; Mastodon is off")
		}
	}
	return c
}

// Bluesky reports whether Bluesky is configured.
func (c Config) Bluesky() bool { return c.BlueskyHandle != "" && c.BlueskyPassword != "" }

// Mastodon reports whether Mastodon is configured.
func (c Config) Mastodon() bool { return c.MastodonInstance != "" && c.MastodonToken != "" }
