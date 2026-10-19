package plugin

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// What a plugin stores in the visitor's browser, and the other sites it
// loads content from, declared in plugin.json "privacy". The site's cookie
// consent (internal/web/consent.go) lists the declarations of the enabled
// plugins next to the core's own, and asks the visitor for the categories
// that need consent. The declarations are information, not permissions:
// the host cannot see what a plugin's script stores, so the author must
// declare it (docs/PLUGINS.md, "Cookies and other sites").

// PrivacyDecl is one entry of "privacy".
type PrivacyDecl struct {
	// Type is what the entry is: PrivacyCookie, PrivacyStorage,
	// PrivacyService or PrivacyResource.
	Type string `json:"type"`
	// Name is the cookie's or the storage key's name ("prefix*" for a
	// family of them), or the name of the service ("YouTube").
	Name string `json:"name"`
	// Service names a service for the visitor's consent: the same id
	// across plugins ("youtube", "vimeo"), so that allowing YouTube once
	// allows it everywhere. Services only.
	Service string `json:"service"`
	// Category is one of PrivacyCategories.
	Category string `json:"category"`
	// Provider is who sets the cookie or runs the service ("Google
	// Ireland Limited"); empty for the site itself.
	Provider string `json:"provider"`
	// Purpose says what it is for, and Lifetime how long it lasts, in the
	// visitor's words: "Remembers…", {"en": "1 year", "es": "1 año"}.
	Purpose  LocalText `json:"purpose"`
	Lifetime LocalText `json:"lifetime"`
	// Policy is the provider's privacy policy (https).
	Policy string `json:"policy"`
	// ClickToLoad says that nothing of the service loads until the visitor
	// presses its placeholder's button, which asks for consent right there
	// (as the video players do). Services only.
	ClickToLoad bool `json:"click_to_load"`
}

// The types of a privacy declaration.
const (
	// PrivacyCookie is a cookie the plugin's script (or the provider's)
	// sets on the site.
	PrivacyCookie = "cookie"
	// PrivacyStorage is a localStorage, sessionStorage or IndexedDB entry.
	PrivacyStorage = "storage"
	// PrivacyService is content from another site that may store data in
	// the browser once it loads (a player, a map, a script): it must wait
	// for consent.
	PrivacyService = "service"
	// PrivacyResource is a file from another site loaded with the page
	// (an image, a font) that stores nothing in the browser; the provider
	// still sees the visitor's IP address. It is listed, never blocked.
	PrivacyResource = "resource"
)

// The consent categories, in the order the visitor sees them. Necessary
// entries need no consent and are always on.
const (
	CategoryNecessary   = "necessary"
	CategoryPreferences = "preferences"
	CategoryAnalytics   = "analytics"
	CategoryMedia       = "media" // external media and marketing
)

var (
	PrivacyCategories = []string{CategoryNecessary, CategoryPreferences, CategoryAnalytics, CategoryMedia}
	privacyTypes      = []string{PrivacyCookie, PrivacyStorage, PrivacyService, PrivacyResource}
	// ServiceRe is the form of a service id.
	ServiceRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,39}$`)
	// storageNameRe is the form of a cookie or storage key: the characters
	// a cookie name can have that need no quoting, and a final * for a
	// family of names.
	storageNameRe = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,79}\*?$`)
)

const maxPrivacyDecls = 20

func (m *Manifest) validatePrivacy() []error {
	var errs []error
	if len(m.Privacy) > maxPrivacyDecls {
		errs = append(errs, fmt.Errorf("privacy: at most %d entries", maxPrivacyDecls))
	}
	for i, d := range m.Privacy {
		bad := func(format string, a ...any) {
			errs = append(errs, fmt.Errorf("privacy[%d]: "+format, append([]any{i}, a...)...))
		}
		if !slices.Contains(privacyTypes, d.Type) {
			bad("type must be one of %s", strings.Join(privacyTypes, ", "))
			continue
		}
		if !slices.Contains(PrivacyCategories, d.Category) {
			bad("category must be one of %s", strings.Join(PrivacyCategories, ", "))
		}
		switch d.Type {
		case PrivacyCookie, PrivacyStorage:
			if !storageNameRe.MatchString(d.Name) {
				bad("name must be the cookie or key name: letters, digits, _ . : - (at most 79), optionally ending in *")
			}
		default:
			if !plainText(d.Name, 80) {
				bad("name is required (at most 80 characters, no control characters)")
			}
			if d.Provider == "" {
				bad("a %s needs its provider", d.Type)
			}
		}
		if (d.Type == PrivacyService) != (d.Service != "") {
			bad(`"service" is required for a service, and only for one`)
		} else if d.Service != "" && !ServiceRe.MatchString(d.Service) {
			bad("service must match ^[a-z][a-z0-9-]{1,39}$")
		}
		if d.ClickToLoad && d.Type != PrivacyService {
			bad(`"click_to_load" is for services only`)
		}
		if d.Provider != "" && !plainText(d.Provider, 100) {
			bad("provider must be at most 100 characters, without control characters")
		}
		if !localTextOK(d.Purpose, 300, true) {
			bad("purpose is required (at most 300 characters per language)")
		}
		needsLifetime := d.Type == PrivacyCookie || d.Type == PrivacyStorage
		if !localTextOK(d.Lifetime, 60, needsLifetime) {
			if needsLifetime {
				bad("lifetime is required for a %s (at most 60 characters per language)", d.Type)
			} else {
				bad("lifetime must be at most 60 characters per language")
			}
		}
		if d.Policy != "" && !httpsURL(d.Policy, 300) {
			bad("policy must be an https:// address (at most 300 characters)")
		}
	}
	return errs
}

// plainText reports whether s is 1 to max characters with no control
// characters and none of < > ".
func plainText(s string, max int) bool {
	return strings.TrimSpace(s) != "" && utf8.ValidString(s) && utf8.RuneCountInString(s) <= max &&
		!strings.ContainsFunc(s, func(r rune) bool { return unicode.IsControl(r) || r == '<' || r == '>' || r == '"' })
}

// localTextOK checks a translatable text: language keys of 12 bytes at
// most, and every value plain text of at most max characters.
func localTextOK(t LocalText, max int, required bool) bool {
	if len(t) == 0 {
		return !required
	}
	for k, v := range t {
		if len(k) > 12 || !plainText(v, max) {
			return false
		}
	}
	return true
}

func httpsURL(s string, max int) bool {
	if len(s) > max {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && !strings.ContainsAny(s, " \t\r\n<>\"")
}

// PluginPrivacy is what an enabled plugin declares.
type PluginPrivacy struct {
	ID, Name string
	Decls    []PrivacyDecl
	Forms    bool // the plugin has public forms (the host sets a cookie)
}

// Privacy returns the declarations of the enabled plugins, in id order;
// plugins that declare nothing and have no form are left out.
func (h *Host) Privacy() []PluginPrivacy {
	if h == nil {
		return nil
	}
	var out []PluginPrivacy
	for _, p := range h.enabled() {
		if len(p.Privacy) > 0 || len(p.Forms) > 0 {
			out = append(out, PluginPrivacy{ID: p.ID, Name: p.Name, Decls: p.Privacy, Forms: len(p.Forms) > 0})
		}
	}
	return out
}
