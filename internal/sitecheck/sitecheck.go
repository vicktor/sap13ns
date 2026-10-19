// Package sitecheck holds the rules of the site's settings pages. The
// admin forms check what they save with them, and a restore checks what a
// backup brings back with the same rules, resetting what fails to the
// site's current value or the default (Clean*), so that a backup can
// never set what the forms refuse.
package sitecheck

import (
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"sap13ns/internal/auth"
)

// Languages are the languages a site can be set to, with their endonyms.
var Languages = map[string]string{
	"ar": "العربية", "ca": "Català", "cs": "Čeština", "da": "Dansk", "de": "Deutsch", "el": "Ελληνικά",
	"en": "English", "es": "Español", "eu": "Euskara", "fi": "Suomi", "fr": "Français", "gl": "Galego",
	"he": "עברית", "hi": "हिन्दी", "it": "Italiano", "ja": "日本語", "ko": "한국어", "nb": "Norsk bokmål",
	"nl": "Nederlands", "pl": "Polski", "pt": "Português", "pt-br": "Português (Brasil)", "ro": "Română",
	"ru": "Русский", "sv": "Svenska", "tr": "Türkçe", "uk": "Українська", "zh": "中文",
}

// ValidLang reports whether code is one of Languages.
func ValidLang(code string) bool { _, ok := Languages[code]; return ok }

// ValidZone reports whether z is a time zone of the system's database.
func ValidZone(z string) bool {
	if z == "" || len(z) > 64 {
		return false
	}
	_, err := time.LoadLocation(z)
	return err == nil
}

// HexColor matches a colour like #3f068b.
var HexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// Username matches the usernames sap13ns accepts.
var Username = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{2,31}$`)

// Slug matches the names of themes, menu locations and widget areas.
var Slug = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// TooLong reports whether s has more than max characters.
func TooLong(s string, max int) bool { return utf8.RuneCountInString(s) > max }

// HTTPURL reports whether raw is an absolute http(s) address of at most
// 300 bytes.
func HTTPURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && len(raw) <= 300
}

// MenuURL accepts what a menu item may link to: a path of the site, a
// fragment, or an http(s) address. Nothing with spaces, nothing else
// (javascript:, data:, //host).
func MenuURL(u string) bool {
	if u == "" || len(u) > 1000 || strings.ContainsAny(u, "\r\n\t ") {
		return false
	}
	if strings.HasPrefix(u, "/") && !strings.HasPrefix(u, "//") || strings.HasPrefix(u, "#") {
		return true
	}
	p, err := url.Parse(u)
	return err == nil && (p.Scheme == "https" || p.Scheme == "http") && p.Host != ""
}

// ServiceURL is the rule of the base addresses of AI and embedding
// providers: https, or http when local models are allowed, with a host
// and no user.
func ServiceURL(raw string, allowLocal bool) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http" && allowLocal) && u.Host != "" && u.User == nil
}

// Email reports whether s is one plain e-mail address.
func Email(s string) bool {
	a, err := mail.ParseAddress(s)
	return err == nil && a.Address == s && len(s) <= 254
}

// Profile checks the profile of a user: the field that fails, or "".
func Profile(email, name, bio, website string) string {
	switch {
	case !Email(email):
		return "email"
	case TooLong(name, 100):
		return "display_name"
	case TooLong(bio, 2000):
		return "bio"
	case website != "" && !HTTPURL(website):
		return "website"
	}
	return ""
}

// AIRoles are the roles the writing assistant can be limited to, lowest
// first.
var AIRoles = []string{auth.RoleContributor, auth.RoleAuthor, auth.RoleEditor, auth.RoleAdmin}

// field is one checked value of a settings struct T: ok reports whether
// it passes, copy sets it from another value of T.
type field[T any] struct {
	name string
	ok   func(*T) bool
	copy func(dst, src *T)
}

// clean resets every failing field of in to cur's value, or when that
// fails too, to def's, and names the fields reset.
func clean[T any](fields []field[T], in, cur, def T) (T, []string) {
	out := in
	var reset []string
	for _, f := range fields {
		if f.ok(&out) {
			continue
		}
		reset = append(reset, f.name)
		if f.copy(&out, &cur); f.ok(&out) {
			continue
		}
		f.copy(&out, &def)
	}
	return out, reset
}

// check names the failing fields, in order.
func check[T any](fields []field[T], s T) []string {
	var bad []string
	for _, f := range fields {
		if !f.ok(&s) {
			bad = append(bad, f.name)
		}
	}
	return bad
}
