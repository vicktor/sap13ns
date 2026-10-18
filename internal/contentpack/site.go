package contentpack

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"sap13ns/internal/store"
)

// Site is the content of site.json: the site settings applied on import and
// menu links that do not point to an item of the pack (the blog, GitHub…).
type Site struct {
	Title        string                    `json:"title"`
	Tagline      string                    `json:"tagline"`
	FooterText   string                    `json:"footer_text"`
	Language     string                    `json:"language"`  // main language, served at the root
	Languages    []string                  `json:"languages"` // extra languages, served under /<code>/
	ActiveTheme  string                    `json:"active_theme"`
	Timezone     string                    `json:"timezone"`
	PostsPerPage int                       `json:"posts_per_page"`
	SiteText     map[string]store.SiteText `json:"site_text"` // title, tagline and footer per extra language
	MenuLinks    []MenuLink                `json:"menu_links"`

	// Warnings found while loading (unknown keys); Import adds them to its report.
	Warnings []string `json:"-"`
}

// MenuLink is a menu entry declared in site.json.
type MenuLink struct {
	Menu  string `json:"menu"`  // location, e.g. primary
	Lang  string `json:"lang"`  // language code; the main language becomes the shared menu
	Label string `json:"label"` // 1 to 100 characters
	URL   string `json:"url"`
	Order int    `json:"order"`
}

var siteKeys = map[string]bool{"title": true, "tagline": true, "footer_text": true, "language": true, "languages": true,
	"active_theme": true, "timezone": true, "posts_per_page": true, "site_text": true, "menu_links": true}

// LoadSite reads and validates site.json.
func LoadSite(path string) (*Site, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var site Site
	if err := json.Unmarshal(raw, &site); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var keys map[string]json.RawMessage
	if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&keys); err == nil {
		var unknown []string
		for k := range keys {
			if !siteKeys[k] {
				unknown = append(unknown, k)
			}
		}
		sort.Strings(unknown)
		for _, k := range unknown {
			site.Warnings = append(site.Warnings, fmt.Sprintf("%s: unknown key %q ignored", path, k))
		}
	}

	var errs []error
	fail := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s: %s", path, fmt.Sprintf(format, args...)))
	}
	if strings.TrimSpace(site.Title) == "" {
		fail("title is required")
	}
	if !langRe.MatchString(site.Language) {
		fail("language must be a language code such as \"en\", got %q", site.Language)
	}
	for _, l := range site.Languages {
		if !langRe.MatchString(l) {
			fail("languages: invalid code %q", l)
		}
		if l == site.Language {
			fail("languages must not repeat the main language %q", l)
		}
	}
	for l := range site.SiteText {
		if !langRe.MatchString(l) {
			fail("site_text: invalid language code %q", l)
		}
	}
	if site.Timezone != "" {
		if _, err := time.LoadLocation(site.Timezone); err != nil {
			fail("timezone %q: %v", site.Timezone, err)
		}
	}
	if site.PostsPerPage < 0 || site.PostsPerPage > 100 {
		fail("posts_per_page must be between 1 and 100")
	}
	for i, m := range site.MenuLinks {
		where := fmt.Sprintf("menu_links[%d]", i)
		if !locationRe.MatchString(m.Menu) {
			fail("%s: invalid menu location %q", where, m.Menu)
		}
		if !langRe.MatchString(m.Lang) {
			fail("%s: lang must be a language code, got %q", where, m.Lang)
		}
		if n := len([]rune(m.Label)); n < 1 || n > 100 {
			fail("%s: label must be 1 to 100 characters", where)
		}
		if m.URL == "" || len(m.URL) > 500 {
			fail("%s: url must be 1 to 500 characters", where)
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return &site, nil
}
