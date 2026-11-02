package web

import "net/http"

// The "Powered by sap13ns" credit (shared/footer-meta) is on every public
// page, as the CPAL-1.0 licence (Exhibit B) asks of whoever runs the code.
// The licensor's network (SAP13NS_PAID_PLANS_HIDE_CREDIT, config
// PaidHideCredit) leaves it out on the sites of its paid plans while their
// subscription holds them: a held site (unpaid, cancelled, grace over)
// shows it again, like a free plan, a single-site install or the portal.

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
	if s.limits == nil || !s.cfg.PaidHideCredit {
		return false
	}
	c, _ := r.Context().Value(ctxKey{}).(*reqCtx)
	if c != nil && c.Credit != creditUnknown {
		return c.Credit == creditHidden
	}
	hide := false
	if p, err := s.aiPlan(r.Context()); err == nil {
		hide = p.Paid
	}
	if c != nil {
		c.Credit = creditShown
		if hide {
			c.Credit = creditHidden
		}
	}
	return hide
}
