package web

import (
	"context"
	"net/http"
	"sync"
	"time"

	"sap13ns/internal/license"
)

// The "Powered by sap13ns" credit (shared/footer-meta) is on every public
// page, as the CPAL-1.0 licence (Exhibit B) asks of whoever runs the code.
// On a network, a super administrator may let a paid plan leave it out
// (Network → Plans): its sites do while their subscription holds them, and
// a held site (unpaid, cancelled, grace over) shows it again, like a free
// plan or the portal. A single-site install leaves it out while it holds a
// white-label licence from the licensor for its domain (internal/license,
// SAP13NS_LICENSE_FILE); once the licence expires, the credit is back.

// creditPlan is the part of SiteLimits that tells whether the site's plan
// leaves out the credit. A SiteLimits without it keeps the credit.
type creditPlan interface {
	HideCredit(ctx context.Context) (bool, error)
}

// creditCache keeps the plan's answer for a minute: a change in Network →
// Plans, or a lapsed subscription, shows within that.
type creditCache struct {
	mu   sync.Mutex
	hide bool
	at   time.Time
}

const creditCacheTTL = time.Minute

// Credit values in reqCtx: not decided yet, shown, left out.
const (
	creditUnknown int8 = iota
	creditShown
	creditHidden
)

// hideCredit reports whether the request's pages leave the credit out. It
// is decided once per request, so that the page cache keeps the page under
// the key it was rendered for; a failed plan lookup shows it.
func (s *Server) hideCredit(r *http.Request) bool {
	cp, plan := s.limits.(creditPlan)
	if !plan && s.license == nil {
		return false
	}
	c, _ := r.Context().Value(ctxKey{}).(*reqCtx)
	if c != nil && c.Credit != creditUnknown {
		return c.Credit == creditHidden
	}
	var hide bool
	if plan {
		hide = s.planHidesCredit(r.Context(), cp)
	} else {
		hide = s.license.Current(time.Now()).HidesCredit(s.cfg.BaseURL.Host, time.Now())
	}
	if c != nil {
		c.Credit = creditShown
		if hide {
			c.Credit = creditHidden
		}
	}
	return hide
}

func (s *Server) planHidesCredit(ctx context.Context, cp creditPlan) bool {
	s.credit.mu.Lock()
	hide, at := s.credit.hide, s.credit.at
	s.credit.mu.Unlock()
	if !at.IsZero() && time.Since(at) < creditCacheTTL {
		return hide
	}
	hide, err := cp.HideCredit(ctx)
	if err != nil {
		return false // shown, and asked again on the next request
	}
	s.credit.mu.Lock()
	s.credit.hide, s.credit.at = hide, time.Now()
	s.credit.mu.Unlock()
	return hide
}

// openLicense watches the licence file of a single-site install, and logs
// what it holds when the server starts and whenever it changes.
func (s *Server) openLicense(path string) *license.File {
	f := license.NewFile(path)
	if f == nil {
		return nil
	}
	host := s.cfg.BaseURL.Host
	f.OnChange = func(l *license.License, err error) {
		switch {
		case err != nil:
			s.log.Error("licence file not valid: the credit is shown", "file", f.Path(), "err", err)
		case l == nil:
			s.log.Info("no licence file: the credit is shown", "file", f.Path())
		case !l.Covers(host):
			s.log.Warn("the licence is for other domains: the credit is shown", "file", f.Path(), "licensee", l.Licensee, "domains", l.Domains, "host", host)
		case !l.InForce(time.Now()):
			s.log.Warn("the licence is not in force: the credit is shown", "file", f.Path(), "licensee", l.Licensee, "expires", l.Expires)
		case l.Type != license.TypeWhiteLabel:
			s.log.Info("licence", "licensee", l.Licensee, "type", l.Type, "expires", l.Expires)
		default:
			s.log.Info("white-label licence: the credit is left out", "licensee", l.Licensee, "expires", l.Expires)
		}
	}
	f.Current(time.Now())
	return f
}
