package web

import (
	"bytes"
	"net/http"
	"net/url"
	"strings"
	"time"

	"sap13ns/internal/auth"
	"sap13ns/internal/content"
	"sap13ns/internal/feed"
	"sap13ns/internal/netguard"
	"sap13ns/internal/plugin"
	"sap13ns/internal/store"
)

var feedTypes = map[string]string{
	"feed.xml":  "application/rss+xml; charset=utf-8",
	"feed.atom": "application/atom+xml; charset=utf-8",
	"feed.json": "application/feed+json; charset=utf-8",
}

// handleSiteFeed serves /feed.xml, /feed.atom and /feed.json.
func (s *Server) handleSiteFeed(file string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		text := s.settings().Text(s.reqLang(r))
		s.writeFeed(w, r, file, text.SiteTitle, text.Tagline, "/", store.PostQuery{})
	}
}

// handleScopedFeed serves /{category|tag|author}/{slug}/feed.{xml,atom,json}.
func (s *Server) handleScopedFeed(scope string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		file := r.PathValue("file")
		if _, ok := feedTypes[file]; !ok {
			s.notFound(w, r)
			return
		}
		lang := s.reqLang(r)
		site := s.settings().Text(lang).SiteTitle
		ctx := r.Context()
		switch scope {
		case "author":
			u, err := s.store.UserByUsername(ctx, r.PathValue("slug"))
			if err != nil || u.Role == auth.RoleSubscriber {
				s.notFound(w, r)
				return
			}
			s.writeFeed(w, r, file, u.Name()+" · "+site, u.Bio, authorURL(u.Username), store.PostQuery{AuthorID: u.ID})
		default:
			term, err := s.store.TermBySlug(ctx, scope, r.PathValue("slug"))
			if err != nil {
				s.notFound(w, r)
				return
			}
			t := term.In(lang)
			s.writeFeed(w, r, file, t.Name+" · "+site, t.Description, termURL(&t), store.PostQuery{TermID: t.ID})
		}
	}
}

// writeFeed renders a feed of the request language; sitePath is unprefixed.
func (s *Server) writeFeed(w http.ResponseWriter, r *http.Request, file, title, desc, sitePath string, q store.PostQuery) {
	st := s.settings()
	lang := s.reqLang(r)
	sitePath = s.langPath(lang, sitePath)
	q.Type, q.Lang, q.PublicOnly, q.Limit, q.WithTerms, q.WithMedia = store.TypePost, lang, true, st.FeedItems, true, true
	if q.Limit <= 0 || q.Limit > 100 {
		q.Limit = 20
	}
	posts, _, err := s.store.ListPosts(r.Context(), q)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	selfPath := strings.TrimSuffix(sitePath, "/") + "/" + file
	f := &feed.Feed{
		Title: title, Description: desc, Language: lang, HubURL: st.WebSubHub,
		SiteURL: s.cfg.AbsURL(sitePath), FeedURL: s.cfg.AbsURL(selfPath),
	}
	if f.Description == "" {
		f.Description = st.Text(lang).Tagline
	}
	base := s.cfg.BaseURL.String()
	for i := range posts {
		p := &posts[i]
		pv := s.postView(p, nil)
		it := feed.Item{
			ID: pv.AbsURL, Title: p.Title, URL: pv.AbsURL, Summary: content.Summary(p.Excerpt, p.Content, 400),
			AuthorName: p.AuthorName, Published: *p.PublishedAt, Updated: p.UpdatedAt,
		}
		if p.AuthorUsername != "" {
			it.AuthorURL = s.cfg.AbsURL(s.langPath(lang, authorURL(p.AuthorUsername)))
		}
		for _, t := range p.Terms {
			it.Categories = append(it.Categories, t.In(lang).Name)
		}
		if pv.Image != nil {
			it.ImageURL = base + pv.Image.Large
		}
		if st.FeedFullText {
			it.ContentHTML = absolutize(string(s.renderBody(r.Context(), p, plugin.FilterFeed)), base)
			if pv.Image != nil {
				it.ContentHTML = `<p><img src="` + base + pv.Image.Large + `" alt="` + escapeAttr(pv.Image.Alt) + `"></p>` + it.ContentHTML
			}
		}
		if it.Updated.After(f.Updated) {
			f.Updated = it.Updated
		}
		f.Items = append(f.Items, it)
	}
	if f.Updated.IsZero() {
		f.Updated = time.Now()
	}

	var buf bytes.Buffer
	switch file {
	case "feed.atom":
		err = f.WriteAtom(&buf)
	case "feed.json":
		err = f.WriteJSON(&buf)
	default:
		err = f.WriteRSS(&buf)
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", feedTypes[file])
	w.Header().Set("Last-Modified", f.Updated.UTC().Format(http.TimeFormat))
	if f.HubURL != "" {
		w.Header().Add("Link", `<`+f.HubURL+`>; rel="hub"`)
		w.Header().Add("Link", `<`+f.FeedURL+`>; rel="self"`)
	}
	w.Write(buf.Bytes())
}

// absolutize turns root-relative src/href attributes into absolute URLs so
// that feed readers can load images and follow links.
func absolutize(html, base string) string {
	r := strings.NewReplacer(` src="/`, ` src="`+base+`/`, ` href="/`, ` href="`+base+`/`, ` srcset="/`, ` srcset="`+base+`/`)
	html = r.Replace(html)
	// Protocol-relative URLs were turned into base + "//..."; undo that.
	return strings.ReplaceAll(html, base+"//", "//")
}

func escapeAttr(s string) string {
	return strings.NewReplacer(`&`, "&amp;", `"`, "&quot;", `<`, "&lt;", `>`, "&gt;").Replace(s)
}

// webSubClient pings WebSub hubs. The hub is a setting, so it only dials
// public addresses (netguard, as plugin fetches do), over https, and
// follows no redirect.
var webSubClient = &http.Client{
	Timeout: 10 * time.Second,
	Transport: &http.Transport{
		DialContext:           netguard.Dialer(nil, nil, 5*time.Second),
		Proxy:                 nil,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 8 * time.Second,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
	},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// pingWebSub notifies the configured hub that the feeds changed.
func (s *Server) pingWebSub() {
	hub := s.settings().WebSubHub
	if hub == "" || !strings.HasPrefix(hub, "https://") || !validHTTPURL(hub) {
		return
	}
	s.goJob(func() {
		client := s.webSub
		if client == nil {
			client = webSubClient
		}
		var feeds []string
		for _, l := range s.siteLangs() {
			for _, f := range []string{"/feed.xml", "/feed.atom", "/feed.json"} {
				feeds = append(feeds, s.langPath(l, f))
			}
		}
		for _, f := range feeds {
			form := "hub.mode=publish&hub.url=" + url.QueryEscape(s.cfg.AbsURL(f))
			resp, err := client.Post(hub, "application/x-www-form-urlencoded", strings.NewReader(form))
			if err != nil {
				s.log.Warn("websub ping failed", "err", err)
				return
			}
			resp.Body.Close()
		}
	})
}
