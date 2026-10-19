package autopost

import (
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Config holds the settings every auto-post plugin shares, normalized. A
// plugin declares them in plugin.json with these keys:
//
//	post_types             select: posts | pages | both (default posts)
//	skip_older_than_hours  number (default 48)
//	max_hashtags           number (default 5, at most 20)
//	template               textarea: the text, with placeholders
//	template_es            textarea: the text for Spanish posts
//	template_en            textarea: the text for English posts
//	dry_run                bool
type Config struct {
	PostTypes   string        // posts | pages | both
	SkipOlder   time.Duration // never post articles published longer ago
	MaxHashtags int
	DryRun      bool

	Template, TemplateES, TemplateEN string

	// Problems with the settings, logged once per call. A plugin appends
	// its own.
	Warnings []string
}

// Defaults of the shared settings (keep plugin.json in sync).
const (
	DefaultSkipHours   = 48
	DefaultMaxHashtags = 5
	MaxHashtags        = 20
)

// ConfigFrom reads the shared settings.
func ConfigFrom(s map[string]string) Config {
	get := func(k string) string { return strings.TrimSpace(s[k]) }
	c := Config{
		PostTypes:   get("post_types"),
		SkipOlder:   DefaultSkipHours * time.Hour,
		MaxHashtags: DefaultMaxHashtags,
		DryRun:      get("dry_run") == "true",
		Template:    s["template"],
		TemplateES:  s["template_es"],
		TemplateEN:  s["template_en"],
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
		c.MaxHashtags = min(n, MaxHashtags)
	}
	return c
}

// Wants reports whether posts of this type are posted.
func (c Config) Wants(postType string) bool {
	if postType == "" {
		postType = "post"
	}
	return c.PostTypes == "both" || c.PostTypes == "posts" && postType == "post" || c.PostTypes == "pages" && postType == "page"
}

// TemplateFor returns the text template for a language: the Spanish or
// English setting, then the general one, then def (DefaultTemplate when
// nil).
func (c Config) TemplateFor(lang, postType string, def func(lang, postType string) string) string {
	if IsSpanish(lang) && strings.TrimSpace(c.TemplateES) != "" {
		return c.TemplateES
	}
	if BaseLang(lang) == "en" && strings.TrimSpace(c.TemplateEN) != "" {
		return c.TemplateEN
	}
	if strings.TrimSpace(c.Template) != "" {
		return c.Template
	}
	if def == nil {
		def = DefaultTemplate
	}
	return def(lang, postType)
}

// DefaultTemplate is "New post: {title}", the link and the hashtags, in
// Spanish for Spanish posts.
func DefaultTemplate(lang, postType string) string {
	switch {
	case IsSpanish(lang) && postType == "page":
		return "Nueva página: {title}\n\n{url}\n\n{hashtags}"
	case IsSpanish(lang):
		return "Nueva entrada: {title}\n\n{url}\n\n{hashtags}"
	case postType == "page":
		return "New page: {title}\n\n{url}\n\n{hashtags}"
	}
	return "New post: {title}\n\n{url}\n\n{hashtags}"
}

// BaseURL reduces an https URL to scheme and host ("https://host").
func BaseURL(v string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(v))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return "", false
	}
	return "https://" + strings.ToLower(u.Host), true
}

// HostOf returns the host name of a URL.
func HostOf(base string) string {
	if u, err := url.Parse(base); err == nil {
		return u.Hostname()
	}
	return base
}

// IsSpanish reports whether a language code is Spanish ("es", "es-MX").
func IsSpanish(lang string) bool {
	l := strings.ToLower(strings.TrimSpace(lang))
	return l == "es" || strings.HasPrefix(l, "es-") || strings.HasPrefix(l, "es_")
}

// BaseLang is the ISO 639 code of a language tag: "es" for "es-MX", "" when
// the tag is not one.
func BaseLang(lang string) string {
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
