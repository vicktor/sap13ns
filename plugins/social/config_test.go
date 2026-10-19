package main

import (
	"testing"
	"time"
)

func TestConfigFrom(t *testing.T) {
	tests := []struct {
		name     string
		settings map[string]string
		check    func(t *testing.T, c Config)
	}{
		{"defaults", map[string]string{}, func(t *testing.T, c Config) {
			if c.PostTypes != "posts" || c.SkipOlder != 48*time.Hour || c.MaxHashtags != 5 || c.DryRun ||
				c.BlueskyService != "https://bsky.social" || c.MastodonVisibility != "public" || c.Bluesky() || c.Mastodon() || len(c.Warnings) != 0 {
				t.Errorf("%+v", c)
			}
		}},
		{"normalized", map[string]string{
			"bluesky_handle": " @alice.example.org ", "bluesky_app_password": "x", "bluesky_service": "https://PDS.example.org/xrpc/",
			"mastodon_instance": "https://Mastodon.Social/@alice", "mastodon_token": "y", "mastodon_visibility": "unlisted",
			"max_hashtags": "50", "skip_older_than_hours": "1.5", "dry_run": "true", "post_types": "both",
		}, func(t *testing.T, c Config) {
			if c.BlueskyHandle != "alice.example.org" || c.BlueskyService != "https://pds.example.org" ||
				c.MastodonInstance != "https://mastodon.social" || c.MastodonVisibility != "unlisted" ||
				c.MaxHashtags != 20 || c.SkipOlder != 90*time.Minute || !c.DryRun || c.PostTypes != "both" || !c.Bluesky() || !c.Mastodon() {
				t.Errorf("%+v", c)
			}
		}},
		{"invalid addresses", map[string]string{
			"bluesky_service": "http://bsky.social", "mastodon_instance": "https://user:pass@mastodon.social", "mastodon_token": "y",
			"mastodon_visibility": "direct",
		}, func(t *testing.T, c Config) {
			if c.BlueskyService != "https://bsky.social" || c.MastodonInstance != "" || c.Mastodon() || len(c.Warnings) != 2 ||
				c.MastodonVisibility != "public" {
				t.Errorf("%+v", c)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { tt.check(t, ConfigFrom(tt.settings)) })
	}
}

func TestIdempotencyKey(t *testing.T) {
	if got := idempotencyKey("https://blog.example.org/es", 7); got != "sap13ns-blog.example.org-7" {
		t.Errorf("%q", got)
	}
}
