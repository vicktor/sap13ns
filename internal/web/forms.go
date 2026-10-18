package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"sap13ns/internal/content"
	"sap13ns/internal/store"
)

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

func formInt(r *http.Request, key string, def int) int {
	v, err := strconv.Atoi(strings.TrimSpace(r.FormValue(key)))
	if err != nil {
		return def
	}
	return v
}

func formInt64Ptr(r *http.Request, key string) *int64 {
	v, err := strconv.ParseInt(strings.TrimSpace(r.FormValue(key)), 10, 64)
	if err != nil || v <= 0 {
		return nil
	}
	return &v
}

func formIDs(r *http.Request, key string) []int64 {
	var out []int64
	for _, v := range r.Form[key] {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil && id > 0 {
			out = append(out, id)
		}
	}
	return out
}

func trimmed(r *http.Request, key string) string { return strings.TrimSpace(r.FormValue(key)) }

func tooLong(s string, max int) bool { return utf8.RuneCountInString(s) > max }

// parseLocal parses a datetime-local input in the site timezone.
func (s *Server) parseLocal(v string) (*time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, nil
	}
	for _, layout := range []string{"2006-01-02T15:04", "2006-01-02T15:04:05", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, v, s.loc()); err == nil {
			return &t, nil
		}
	}
	return nil, fmt.Errorf("invalid date %q", v)
}

func listPage(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil || n < 1 {
		return 1
	}
	return n
}

// reservedSlugs cannot be used by pages because they clash with routes.
var reservedSlugs = map[string]bool{
	"admin": true, "blog": true, "category": true, "tag": true, "author": true, "archive": true, "search": true,
	"static": true, "themes": true, "uploads": true, "login": true, "logout": true, "register": true, "setup": true,
	"forgot-password": true, "reset-password": true, "healthz": true, "feed": true, "api": true, "wp-admin": true,
	"wp-login-php": true, "xmlrpc-php": true, "ask": true, "mcp": true,
}

// uniqueSlug derives a slug and appends -2, -3, ... until it is free.
func (s *Server) uniqueSlug(ctx context.Context, typ, lang, wanted, title string, exceptID int64) (string, error) {
	base := content.Slugify(wanted)
	if base == "" {
		base = content.Slugify(title)
	}
	if base == "" {
		base = "entrada"
		if typ == store.TypePage {
			base = "pagina"
		}
	}
	if len(base) > 180 {
		base = strings.Trim(base[:180], "-")
	}
	slug := base
	for i := 2; i < 1000; i++ {
		if typ == store.TypePage && (reservedSlugs[slug] || validLang(slug)) {
			slug = fmt.Sprintf("%s-%d", base, i)
			continue
		}
		taken, err := s.store.SlugExists(ctx, typ, lang, slug, exceptID)
		if err != nil {
			return "", err
		}
		if !taken {
			return slug, nil
		}
		slug = fmt.Sprintf("%s-%d", base, i)
	}
	return "", fmt.Errorf("could not find a free slug for %q", base)
}

// withQuery adds parameters to a path.
func withQuery(path string, kv ...string) string {
	q := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] != "" {
			q.Set(kv[i], kv[i+1])
		}
	}
	if enc := q.Encode(); enc != "" {
		return path + "?" + enc
	}
	return path
}

// Tab is a filter link above an admin list.
type Tab struct {
	Label   string
	URL     string
	Current bool
	Count   int
}
