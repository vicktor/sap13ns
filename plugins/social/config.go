// The social plugin posts new articles to Bluesky and Mastodon when they
// are published.
//
// Events drive everything (handler.go): post.published sends the post, or
// queues it when it is scheduled for later; cron.hourly sends queued posts
// once they are public and retries failed sends; post.updated and
// post.trashed keep the queue up to date. text.go builds the text,
// bluesky.go and mastodon.go the requests, and state.go keeps, in the
// plugin's key-value store, what was posted where, so that a post is never
// sent twice.
//
// Everything except main.go is plain Go, tested natively with sdktest.
package main

import (
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Config holds the plugin settings, normalized.
type Config struct {
	PostTypes   string        // posts | pages | both
	SkipOlder   time.Duration // never post articles published longer ago
	MaxHashtags int
	DryRun      bool

	Template, TemplateES, TemplateEN string

	BlueskyHandle   string
	BlueskyPassword string
	BlueskyService  string // https://host, no path

	MastodonInstance   string // https://host, no path
	MastodonToken      string
	MastodonVisibility string

	// Problems with the settings, logged once per call.
	Warnings []string
}

// Settings defaults (keep in sync with plugin.json). The trailing slash
// ends the host for the package scanner, which otherwise reads the string
// stored next to it in the binary as part of the host name.
const (
	defaultService     = "https://bsky.social/"
	defaultSkipHours   = 48
	defaultMaxHashtags = 5
)

// ConfigFrom reads the plugin settings.
func ConfigFrom(s map[string]string) Config {
	get := func(k string) string { return strings.TrimSpace(s[k]) }
	c := Config{
		PostTypes:          get("post_types"),
		SkipOlder:          defaultSkipHours * time.Hour,
		MaxHashtags:        defaultMaxHashtags,
		DryRun:             get("dry_run") == "true",
		Template:           s["template"],
		TemplateES:         s["template_es"],
		TemplateEN:         s["template_en"],
		BlueskyHandle:      strings.TrimPrefix(get("bluesky_handle"), "@"),
		BlueskyPassword:    get("bluesky_app_password"),
		MastodonToken:      get("mastodon_token"),
		MastodonVisibility: get("mastodon_visibility"),
	}
	switch c.PostTypes {
	case "posts", "pages", "both":
	default:
		c.PostTypes = "posts"
	}
	if h, err := strconv.ParseFloat(get("skip_older_than_hours"), 64); err == nil && h > 0 {
		c.SkipOlder = time.Duration(h * float64(time.Hour))
	}
	if n, err := strconv.Atoi(get("max_hashtags")); err == nil && n >= 0 {
		c.MaxHashtags = min(n, 20)
	}
	if c.MastodonVisibility != "unlisted" {
		c.MastodonVisibility = "public"
	}

	c.BlueskyService, _ = baseURL(defaultService)
	if v := get("bluesky_service"); v != "" {
		if base, ok := baseURL(v); ok {
			c.BlueskyService = base
		} else {
			c.Warnings = append(c.Warnings, "Bluesky server must be an https:// address such as https://bsky.social/; using the default, bsky.social")
		}
	}
	if v := get("mastodon_instance"); v != "" {
		if base, ok := baseURL(v); ok {
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

// Wants reports whether posts of this type are posted.
func (c Config) Wants(postType string) bool {
	if postType == "" {
		postType = "post"
	}
	return c.PostTypes == "both" || c.PostTypes == "posts" && postType == "post" || c.PostTypes == "pages" && postType == "page"
}

// TemplateFor returns the text template for a language: the Spanish or
// English setting, then the general one, then the built-in default.
func (c Config) TemplateFor(lang, postType string) string {
	if isSpanish(lang) && strings.TrimSpace(c.TemplateES) != "" {
		return c.TemplateES
	}
	if baseLang(lang) == "en" && strings.TrimSpace(c.TemplateEN) != "" {
		return c.TemplateEN
	}
	if strings.TrimSpace(c.Template) != "" {
		return c.Template
	}
	return defaultTemplate(lang, postType)
}

func defaultTemplate(lang, postType string) string {
	switch {
	case isSpanish(lang) && postType == "page":
		return "Nueva página: {title}\n\n{url}\n\n{hashtags}"
	case isSpanish(lang):
		return "Nueva entrada: {title}\n\n{url}\n\n{hashtags}"
	case postType == "page":
		return "New page: {title}\n\n{url}\n\n{hashtags}"
	}
	return "New post: {title}\n\n{url}\n\n{hashtags}"
}

// baseURL reduces an https URL to scheme and host ("https://host").
func baseURL(v string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(v))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return "", false
	}
	return "https://" + strings.ToLower(u.Host), true
}

func hostOf(base string) string {
	if u, err := url.Parse(base); err == nil {
		return u.Hostname()
	}
	return base
}

func isSpanish(lang string) bool {
	l := strings.ToLower(strings.TrimSpace(lang))
	return l == "es" || strings.HasPrefix(l, "es-") || strings.HasPrefix(l, "es_")
}

// baseLang is the ISO 639 code Mastodon expects: "es" for "es-MX".
func baseLang(lang string) string {
	l := strings.ToLower(strings.TrimSpace(lang))
	if i := strings.IndexAny(l, "-_"); i >= 0 {
		l = l[:i]
	}
	if len(l) < 2 || len(l) > 3 {
		return ""
	}
	for _, r := range l {
		if r < 'a' || r > 'z' {
			return ""
		}
	}
	return l
}
