// External links: marks links to other sites in post content.
//
// Every absolute http(s) link whose host differs from the site's host gets
// rel="noopener noreferrer"; hosts in the sponsored list also get
// rel="sponsored nofollow"; hosts in the UTM list get utm_* parameters that
// are not already present; with the new-tab option the link opens in a new
// tab and carries visually hidden text that says so. Internal, relative,
// fragment, mailto: and tel: links are never touched.
package main

import (
	"net/url"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	"golang.org/x/net/idna"
)

// Config holds the plugin settings and the request context.
type Config struct {
	SiteURL          string   // the site's base URL, from the request
	Lang             string   // post language, else site language
	NewTab           bool     // add target="_blank" to external links
	SponsoredDomains []string // normalised with ParseDomains
	UTMDomains       []string // normalised with ParseDomains
	UTMSource        string   // empty: the site's host name
	UTMMedium        string
	UTMCampaign      string
}

// Filter rewrites the external links of an HTML fragment. It reports whether
// anything changed; when it did not, src is returned unchanged.
func Filter(src string, cfg Config) (string, bool) {
	if strings.TrimSpace(src) == "" || !strings.Contains(src, "<a") && !strings.Contains(src, "<A") {
		return src, false
	}
	nodes, err := html.ParseFragment(strings.NewReader(src), &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body})
	if err != nil {
		return src, false
	}
	f := filter{cfg: cfg}
	if u, err := url.Parse(strings.TrimSpace(cfg.SiteURL)); err == nil && u.Host != "" {
		f.siteHost = strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
		f.siteKey = siteKey(f.siteHost)
	}
	if f.cfg.UTMSource == "" {
		f.cfg.UTMSource = f.siteHost
	}
	for _, n := range nodes {
		f.walk(n)
	}
	if !f.changed {
		return src, false
	}
	root := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
	for _, n := range nodes {
		root.AppendChild(n)
	}
	restoreIcons(root)
	var b strings.Builder
	for c := root.FirstChild; c != nil; c = c.NextSibling {
		if err := html.Render(&b, c); err != nil {
			return src, false
		}
	}
	return b.String(), true
}

type filter struct {
	cfg      Config
	siteHost string // lowercased host name of the site
	siteKey  string // siteHost in ASCII without "www."; "" if unknown
	changed  bool
}

func (f *filter) walk(n *html.Node) {
	if n.Type == html.ElementNode {
		switch n.DataAtom {
		case atom.Pre, atom.Code:
			return
		case atom.A:
			f.link(n)
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		f.walk(c)
	}
}

func (f *filter) link(a *html.Node) {
	href, ok := attr(a, "href")
	if !ok {
		return
	}
	host, ok := externalHost(href)
	if !ok || f.siteKey != "" && siteKey(host) == f.siteKey {
		return
	}
	rel := []string{"noopener", "noreferrer"}
	if matchDomain(host, f.cfg.SponsoredDomains) {
		rel = append(rel, "sponsored", "nofollow")
	}
	f.set(a, "rel", mergeTokens(a, rel))

	if f.cfg.NewTab {
		if _, has := attr(a, "target"); !has {
			f.set(a, "target", "_blank")
		}
	}
	if t, _ := attr(a, "target"); strings.EqualFold(strings.TrimSpace(t), "_blank") && !hasSROnly(a) {
		span := &html.Node{Type: html.ElementNode, Data: "span", DataAtom: atom.Span,
			Attr: []html.Attribute{{Key: "class", Val: "sr-only"}}}
		span.AppendChild(&html.Node{Type: html.TextNode, Data: newTabText(f.cfg.Lang)})
		a.AppendChild(span)
		f.changed = true
	}

	if matchDomain(host, f.cfg.UTMDomains) {
		f.set(a, "href", addUTM(strings.TrimSpace(href), [][2]string{
			{"utm_source", f.cfg.UTMSource},
			{"utm_medium", f.cfg.UTMMedium},
			{"utm_campaign", f.cfg.UTMCampaign},
		}))
	}
}

// set replaces or adds an attribute, recording a change only when the value
// actually differs.
func (f *filter) set(n *html.Node, key, val string) {
	for i, a := range n.Attr {
		if a.Namespace == "" && strings.EqualFold(a.Key, key) {
			if a.Val != val {
				n.Attr[i].Val = val
				f.changed = true
			}
			return
		}
	}
	n.Attr = append(n.Attr, html.Attribute{Key: key, Val: val})
	f.changed = true
}

func attr(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Namespace == "" && strings.EqualFold(a.Key, key) {
			return a.Val, true
		}
	}
	return "", false
}

// externalHost returns the lowercased host name of an absolute http(s) or
// protocol-relative URL. Every other kind of link reports false.
func externalHost(href string) (string, bool) {
	href = strings.TrimSpace(href)
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	}
	u, err := url.Parse(href)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return "", false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	return host, host != ""
}

// toASCII converts an internationalised host name to its Punycode form.
// Names that IDNA rejects are returned as they are.
func toASCII(host string) string {
	if a, err := idna.Lookup.ToASCII(host); err == nil {
		return a
	}
	if a, err := idna.Punycode.ToASCII(host); err == nil {
		return strings.ToLower(a)
	}
	return host
}

// siteKey is the form used to decide whether two hosts are the same site:
// ASCII, lowercase, without a leading "www.".
func siteKey(host string) string {
	return strings.TrimPrefix(toASCII(host), "www.")
}

// matchDomain reports whether host is one of domains or a subdomain of one.
func matchDomain(host string, domains []string) bool {
	if len(domains) == 0 {
		return false
	}
	host = toASCII(host)
	for _, d := range domains {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

// ParseDomains reads a domain list written one per line or separated by
// commas or spaces. Schemes, paths, ports, "*." and "www." are removed and
// names are converted to ASCII; invalid entries are ignored.
func ParseDomains(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, d := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	}) {
		d = strings.ToLower(d)
		if i := strings.Index(d, "://"); i >= 0 {
			d = d[i+3:]
		}
		if i := strings.IndexAny(d, "/?#"); i >= 0 {
			d = d[:i]
		}
		if i := strings.LastIndexByte(d, '@'); i >= 0 {
			d = d[i+1:]
		}
		if i := strings.LastIndexByte(d, ':'); i >= 0 && !strings.Contains(d, "]") {
			d = d[:i]
		}
		d = strings.TrimPrefix(d, "*.")
		d = strings.TrimPrefix(d, "www.")
		d = strings.Trim(d, ".")
		if d == "" || strings.ContainsAny(d, "*[]<>\"'") {
			continue
		}
		d = toASCII(d)
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

// mergeTokens returns the link's rel tokens (lowercased, without duplicates)
// followed by the missing tokens of add.
func mergeTokens(a *html.Node, add []string) string {
	cur, _ := attr(a, "rel")
	var out []string
	seen := map[string]bool{}
	for _, t := range append(strings.Fields(strings.ToLower(cur)), add...) {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return strings.Join(out, " ")
}

func hasSROnly(n *html.Node) bool {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode {
			continue
		}
		if cls, _ := attr(c, "class"); containsToken(cls, "sr-only") || hasSROnly(c) {
			return true
		}
	}
	return false
}

func containsToken(list, tok string) bool {
	for _, t := range strings.Fields(list) {
		if t == tok {
			return true
		}
	}
	return false
}

// addUTM appends the given parameters that are not empty and not already in
// the query. The rest of the URL is kept byte for byte.
func addUTM(href string, params [][2]string) string {
	base, frag, hasFrag := strings.Cut(href, "#")
	query := ""
	if i := strings.IndexByte(base, '?'); i >= 0 {
		query = base[i+1:]
	}
	present := map[string]bool{}
	for _, kv := range strings.Split(query, "&") {
		k, _, _ := strings.Cut(kv, "=")
		if uk, err := url.QueryUnescape(k); err == nil {
			k = uk
		}
		present[k] = true
	}
	var add []string
	for _, p := range params {
		if p[1] != "" && !present[p[0]] {
			add = append(add, p[0]+"="+url.QueryEscape(p[1]))
		}
	}
	if len(add) == 0 {
		return href
	}
	switch {
	case !strings.Contains(base, "?"):
		base += "?"
	case !strings.HasSuffix(base, "?") && !strings.HasSuffix(base, "&"):
		base += "&"
	}
	base += strings.Join(add, "&")
	if hasFrag {
		base += "#" + frag
	}
	return base
}

func newTabText(lang string) string {
	if isSpanish(lang) {
		return " (se abre en una pestaña nueva)"
	}
	return " (opens in a new tab)"
}

func isSpanish(lang string) bool {
	lang = strings.ToLower(strings.TrimSpace(lang))
	return lang == "es" || strings.HasPrefix(lang, "es-") || strings.HasPrefix(lang, "es_")
}
