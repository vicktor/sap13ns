package analytics

import (
	"net"
	"net/http"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Placeholder values stored in analytics tables.
const (
	direct    = "(direct)"
	otherPath = "(other)"
)

const (
	maxSourceLen = 64  // utm_source / ref values, in characters
	maxHostLen   = 100 // referrer host names, in characters
	maxPathLen   = 256 // page paths, in bytes
	maxLangLen   = 16
)

// referrerSource returns the traffic source of a page view: the utm_source or
// ref query parameter when present, else the external referring host without
// "www.", else "(direct)". ok is false for same-site navigation, which has no
// source of its own and is not counted in the referrer dimension.
func referrerSource(r *http.Request, internal map[string]bool) (source string, ok bool) {
	if r.URL != nil && r.URL.RawQuery != "" {
		q := r.URL.Query()
		for _, k := range []string{"utm_source", "ref"} {
			if v := clean(strings.ToLower(strings.TrimSpace(q.Get(k))), maxSourceLen); v != "" {
				return v, true
			}
		}
	}
	ref := r.Header.Get("Referer")
	if ref == "" {
		return direct, true
	}
	u, err := url.Parse(ref)
	if err != nil {
		return direct, true
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "android-app":
	default:
		return direct, true
	}
	host := normalizeHost(u.Hostname())
	if host == "" {
		return direct, true
	}
	if host == normalizeHost(requestHost(r)) || internal[host] {
		return "", false
	}
	return clean(host, maxHostLen), true
}

// requestHost returns the host the visitor asked for, without the port.
func requestHost(r *http.Request) string {
	if h, _, err := net.SplitHostPort(r.Host); err == nil {
		return h
	}
	return r.Host
}

// normalizeHost lowercases a host name and strips a trailing dot, a port and
// a leading "www.".
func normalizeHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	h = strings.TrimSuffix(h, ".")
	return strings.TrimPrefix(h, "www.")
}

// clean makes s safe to store: valid UTF-8, no control characters (PostgreSQL
// rejects NUL in text, which would make every flush fail) and at most max
// characters.
func clean(s string, max int) string {
	ok := utf8.ValidString(s)
	n := 0
	for _, c := range s {
		if unicode.IsControl(c) {
			ok = false
			break
		}
		n++
	}
	if ok && n <= max {
		return s
	}
	var b strings.Builder
	n = 0
	for _, c := range s {
		if c == utf8.RuneError || unicode.IsControl(c) {
			continue
		}
		if n == max {
			break
		}
		b.WriteRune(c)
		n++
	}
	return b.String()
}

// cleanPath drops the query and fragment (they may carry personal data) and
// bounds the length. The caller passes the canonical path.
func cleanPath(p string) string {
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	p = clean(p, maxPathLen)
	for len(p) > maxPathLen {
		_, size := utf8.DecodeLastRuneInString(p)
		p = p[:len(p)-size]
	}
	if p == "" {
		return "/"
	}
	return p
}

// cleanLang normalises a language code; "" means none.
func cleanLang(l string) string {
	l = strings.ToLower(strings.TrimSpace(l))
	for _, c := range l {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return ""
		}
	}
	if len(l) > maxLangLen {
		return ""
	}
	return l
}
