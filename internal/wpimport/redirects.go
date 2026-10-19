package wpimport

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strings"
	"unicode"
)

// Redirects of an import, made through Sink.AddRedirect, which never
// replaces a redirect the site has:
//   - from the old address of each imported post, page and file to its new
//     one, always;
//   - the rules a sap13ns export carries, with Options.RedirectRules only.
//
// A target is always a path of the site. A rule that points elsewhere is
// skipped, except an address on the exported site's own host, which
// becomes its path. Sources must be paths too, and neither the sink (the
// site's own addresses, existing redirects, its posts and pages) nor the
// import (the new addresses of the imported posts and pages) may lose an
// address to a redirect. Report.Redirects says why each one was skipped.

// RedirectResult is what became of one redirect of the import.
type RedirectResult struct {
	Origin string // RedirectRule, RedirectPost or RedirectFile
	Source string
	Target string // the target as made; "" for 410
	Status int
	// Reason is "" for a redirect made (in a dry run: that would be made),
	// otherwise one of the Skip constants.
	Reason string
}

// Origins of a redirect.
const (
	RedirectRule = "rule" // a rule of a sap13ns export
	RedirectPost = "post" // the old address of a post or page
	RedirectFile = "file" // the old address of a media file
)

// Reasons a redirect was not made.
const (
	SkipDisabled  = "disabled"  // a rule, and Options.RedirectRules is off
	SkipSource    = "source"    // the source is not a path of the site
	SkipStatus    = "status"    // not 301, 302, 307, 308 or 410
	SkipTarget    = "target"    // no target, or the source itself
	SkipOffSite   = "off-site"  // the target is on another site
	SkipDuplicate = "duplicate" // another redirect of the import has this source
	SkipReserved  = "reserved"  // the site serves the source itself (ErrRedirectReserved)
	SkipContent   = "content"   // a post or page has this address (ErrRedirectContent)
	SkipExists    = "exists"    // the site has a redirect for it (ErrRedirectExists)
	SkipInvalid   = "invalid"   // the sink found the rule malformed (ErrRedirectInvalid)
	SkipFailed    = "failed"    // the sink failed; the warnings say why
)

const maxRedirectResults = 1000

var redirectStatuses = []int{301, 302, 307, 308, 410}

// oldPath returns the path (and query) a visitor of the old site requested.
func oldPath(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	p := u.EscapedPath()
	if p == "" {
		p = "/"
	}
	if u.RawQuery != "" {
		p += "?" + u.RawQuery
	}
	return p
}

// importRedirects maps old permalinks of public content (a redirect for a
// draft or a scheduled post would reveal its new slug) and old media files.
func (im *importer) importRedirects(ctx context.Context) error {
	if im.opts.KeepAddresses {
		return nil
	}
	for _, p := range im.order {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !p.public {
			continue
		}
		if err := im.redirect(ctx, RedirectPost, oldPath(p.it.Link), p.url); err != nil {
			return err
		}
	}
	for i := range im.exp.Items {
		it := &im.exp.Items[i]
		if ref, ok := im.media[it.ID]; ok && it.Type == "attachment" && ref.url != "" {
			if err := im.redirect(ctx, RedirectFile, oldPath(it.AttachmentURL), ref.url); err != nil {
				return err
			}
		}
	}
	return nil
}

// redirect sends the old address of imported content to its new one. An
// address that did not change, and the home page, need none.
func (im *importer) redirect(ctx context.Context, origin, from, to string) error {
	if from == "" || from == "/" || to == "" || from == to {
		return nil
	}
	if u, err := url.Parse(to); err == nil && u.Host == "" && u.Path == from {
		return nil
	}
	return im.addRedirect(ctx, RedirectResult{Origin: origin, Source: from, Target: to, Status: 301})
}

// importRules adds the redirect rules of a sap13ns export, after the
// redirects from old addresses, which win for the same path.
func (im *importer) importRules(ctx context.Context) error {
	for _, rd := range im.exp.Redirects {
		if err := ctx.Err(); err != nil {
			return err
		}
		r := RedirectResult{Origin: RedirectRule, Source: strings.TrimSpace(rd.Source), Target: strings.TrimSpace(rd.Target), Status: rd.Status}
		if !im.opts.RedirectRules {
			im.skipRedirect(r, SkipDisabled)
			continue
		}
		if err := im.addRedirect(ctx, r); err != nil {
			return err
		}
	}
	return nil
}

// addRedirect checks r and hands it to the sink.
func (im *importer) addRedirect(ctx context.Context, r RedirectResult) error {
	from, ok := localPath(r.Source)
	if !ok || strings.Contains(from, "#") {
		im.skipRedirect(r, SkipSource)
		return nil
	}
	r.Source = from
	key := redirectKey(from)
	switch {
	case !slices.Contains(redirectStatuses, r.Status):
		im.skipRedirect(r, SkipStatus)
		return nil
	case r.Status == 410:
		r.Target = ""
	default:
		to, reason := im.localTarget(r.Target)
		if reason == "" && to == from {
			reason = SkipTarget
		}
		if reason != "" {
			im.skipRedirect(r, reason)
			return nil
		}
		r.Target = to
	}
	// A redirect from a variant of a post's address to that very address
	// (WordPress's /about/ → /about) shadows nothing: the site ignores a
	// redirect back to the path asked for.
	if im.contentURLs[key] && redirectKey(r.Target) != key {
		im.skipRedirect(r, SkipContent)
		return nil
	}
	if im.redirected[key] {
		im.skipRedirect(r, SkipDuplicate)
		return nil
	}
	im.redirected[key] = true
	var err error
	if im.opts.DryRun {
		err = im.sink.CheckRedirect(ctx, r.Source, r.Target, r.Status)
	} else {
		err = im.sink.AddRedirect(ctx, r.Source, r.Target, r.Status)
	}
	if err != nil {
		reason := sinkReason(err)
		if reason != SkipFailed {
			im.skipRedirect(r, reason)
			return nil
		}
		im.recordRedirect(r, SkipFailed)
		return im.failed(ctx, &im.rep.Counts.Redirects, "redirect %s → %s: %v", r.Source, r.Target, err)
	}
	im.rep.Counts.Redirects.Created++
	if r.Origin == RedirectRule {
		im.recordRedirect(r, "")
	}
	return nil
}

func (im *importer) skipRedirect(r RedirectResult, reason string) {
	im.rep.Counts.Redirects.Skipped++
	im.recordRedirect(r, reason)
}

func (im *importer) recordRedirect(r RedirectResult, reason string) {
	if len(im.rep.Redirects) >= maxRedirectResults {
		im.rep.RedirectsDropped++
		return
	}
	r.Reason = reason
	im.rep.Redirects = append(im.rep.Redirects, r)
}

func sinkReason(err error) string {
	for _, c := range []struct {
		err    error
		reason string
	}{
		{ErrRedirectReserved, SkipReserved}, {ErrRedirectContent, SkipContent}, {ErrRedirectExists, SkipExists},
		{ErrRedirectOffSite, SkipOffSite}, {ErrRedirectInvalid, SkipInvalid},
	} {
		if errors.Is(err, c.err) {
			return c.reason
		}
	}
	return SkipFailed
}

// localTarget returns raw as a path of the site: a local path, or an
// absolute http(s) URL on the exported site's own host, rewritten to its
// path. Anything else is on another site (SkipOffSite), or unusable.
func (im *importer) localTarget(raw string) (string, string) {
	raw = strings.TrimSpace(raw)
	switch {
	case raw == "":
		return "", SkipTarget
	case strings.ContainsFunc(raw, unicode.IsControl):
		return "", SkipTarget
	}
	if p, ok := localPath(raw); ok {
		return p, ""
	}
	if u, err := url.Parse(raw); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" &&
		!strings.ContainsRune(raw, '\\') && im.siteHosts[normHost(u.Host)] {
		p := u.EscapedPath()
		if p == "" {
			p = "/"
		}
		if u.RawQuery != "" {
			p += "?" + u.RawQuery
		}
		if u.Fragment != "" {
			p += "#" + u.EscapedFragment()
		}
		if lp, ok := localPath(p); ok {
			return lp, ""
		}
	}
	return "", SkipOffSite
}

// localPath returns raw when it is a path of the site: one leading slash
// (also once percent-decoded), no backslash, no control character, no
// scheme or host, and at most 1000 bytes.
func localPath(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	bad := func(s string) bool {
		return !strings.HasPrefix(s, "/") || strings.HasPrefix(s, "//") || strings.ContainsRune(s, '\\') ||
			strings.ContainsFunc(s, unicode.IsControl)
	}
	if len(raw) > 1000 || bad(raw) {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" || u.Opaque != "" || u.User != nil {
		return "", false
	}
	if p, err := url.PathUnescape(u.EscapedPath()); err != nil || bad(p) {
		return "", false
	}
	return raw, true
}

// redirectKey is a source the way requests match it: the path
// percent-decoded and without a trailing slash, then the query.
func redirectKey(s string) string {
	p, q, hasQuery := strings.Cut(s, "?")
	p, _, _ = strings.Cut(p, "#")
	if u, err := url.PathUnescape(p); err == nil {
		p = u
	}
	if len(p) > 1 {
		p = strings.TrimSuffix(p, "/")
	}
	if hasQuery {
		return p + "?" + q
	}
	return p
}
