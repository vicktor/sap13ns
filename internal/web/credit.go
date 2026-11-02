package web

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// The "Powered by sap13ns" credit (shared/footer-meta) is on every public
// page, as the CPAL-1.0 licence (Exhibit B) asks of whoever runs the code.
// On a network, a super administrator may let a paid plan leave it out
// (Network → Plans): its sites do while their subscription holds them, and
// a held site (unpaid, cancelled, grace over) shows it again, like a free
// plan, a single-site install or the portal.

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
	cp, ok := s.limits.(creditPlan)
	if !ok {
		return false
	}
	c, _ := r.Context().Value(ctxKey{}).(*reqCtx)
	if c != nil && c.Credit != creditUnknown {
		return c.Credit == creditHidden
	}
	hide := s.planHidesCredit(r.Context(), cp)
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
