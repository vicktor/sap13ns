package web

// Emergency access (docs/MULTISITE.md): without the consent of a site's
// owner, a network admin opens its administration only for a legal order
// or a serious security incident (malware, phishing on the site). They
// start it from the site's page in the network admin area, with the
// reason, a justification and a fresh second factor; it lasts four hours
// at most, for them only, and its owner is e-mailed at once. Its sessions
// sign in with their own amr (store.AMRSSOEmergency), and the site's audit
// log tells them apart from a consented operator's. On a suspended site,
// the network serves the site to those sessions only (EmergencyOnly).

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"sap13ns/internal/auth"
	"sap13ns/internal/store"
)

// emergencyCategories are the reasons an emergency access may have.
var emergencyCategories = []string{"legal", "security"}

const emergencyJustificationMax = 500

// emergencyForm is the form that starts an emergency access, as it was
// sent, with what the page needs to ask for the second factor.
type emergencyForm struct {
	Category      string
	Justification string
	Error         string
	Reauth        bool // the session is recent enough: no code needed
	TOTP          bool // the account has an authenticator app
}

type emergencyOnlyKey struct{}

// EmergencyOnly marks a request to a suspended site that the network
// serves only because an emergency access to it is live: deny answers it
// unless it comes with an emergency session, or belongs to the sign-in
// that opens one (or is a static asset of its pages).
func EmergencyOnly(ctx context.Context, deny http.Handler) context.Context {
	return context.WithValue(ctx, emergencyOnlyKey{}, deny)
}

// emergencyGate applies EmergencyOnly, once the session is known.
func (s *Server) emergencyGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deny, ok := r.Context().Value(emergencyOnlyKey{}).(http.Handler)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		c := rctx(r)
		if c.User != nil && c.Session != nil && c.Session.AMR == store.AMRSSOEmergency || emergencyServed(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		deny.ServeHTTP(w, r)
	})
}

// emergencyServed are the paths a suspended site answers to anyone while
// an emergency access is live: the single sign-on that opens the session,
// and the assets of its pages.
func emergencyServed(path string) bool {
	return path == "/sso/start" || path == ssoCallback || strings.HasPrefix(path, "/static/")
}

// emergencyReauth checks the fresh second factor an emergency access
// needs: a sign-in younger than the re-authentication window of the
// profile's sensitive actions (every portal sign-in has a second factor),
// or a code from the account's authenticator app. A password is not one.
// It returns "" when the account may go on, or a translated error.
func (s *Server) emergencyReauth(r *http.Request, u *store.User, code string) (int, string) {
	if recentLogin(r) {
		return 0, ""
	}
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if code == "" || !u.TOTPEnabled {
		return http.StatusUnauthorized, s.t("Confirm it is you: enter a code from your authenticator app, or sign in again.")
	}
	if !s.mfaUser.Allow(u.Username) {
		return http.StatusTooManyRequests, s.t("Too many attempts. Wait a few minutes and try again.")
	}
	if len(code) == 6 {
		if secret, err := s.keys.Decrypt(u.TOTPSecret, []byte(u.Username)); err == nil {
			if step := auth.ValidateTOTP(secret, code, time.Now()); step > 0 {
				if fresh, _ := s.store.UseTOTPStep(r.Context(), u.ID, step); fresh {
					s.mfaUser.Reset(u.Username)
					return 0, ""
				}
			}
		}
	}
	s.audit(r, u.ID, "2fa.failed", u.Username, map[string]string{"for": "emergency access"})
	return http.StatusUnauthorized, s.t("The code is not valid.")
}

// emergencyReason is the reason of an emergency access, for the owner.
func (s *Server) emergencyReason(category string) string {
	if category == "legal" {
		return s.t("a legal order")
	}
	return s.t("a serious security incident")
}

func netSitePath(siteID int64, anchor string) string {
	return fmt.Sprintf("/admin/network/sites/%d#%s", siteID, anchor)
}

func (s *Server) adminNetEmergencyStart(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadNetSite(w, r)
	if !ok {
		return
	}
	site := d.Site
	ef := &emergencyForm{Category: r.PostFormValue("category"), Justification: strings.TrimSpace(r.PostFormValue("justification"))}
	fail := func(status int, msg string) {
		ef.Error = msg
		s.renderNetSiteData(w, r, status, d, s.domainsView(d, netDomainsBase(site.ID)), ef)
	}
	u := currentUser(r)
	switch {
	case site.Status == "deleted":
		fail(http.StatusConflict, s.siteStateError(site.Status))
		return
	case d.Support.LiveEmergency(u.ID) != nil:
		s.redirect(w, r, netSitePath(site.ID, "emergency"), "info", s.t("Your emergency access to %s is already open.", site.Slug))
		return
	case !slices.Contains(emergencyCategories, ef.Category):
		fail(http.StatusUnprocessableEntity, s.t("Choose the reason: a legal order or a serious security incident."))
		return
	case ef.Justification == "":
		fail(http.StatusUnprocessableEntity, s.t("Explain why you need the access."))
		return
	case utf8.RuneCountInString(ef.Justification) > emergencyJustificationMax:
		fail(http.StatusUnprocessableEntity, s.t("The justification is too long (500 characters max)."))
		return
	}
	if status, msg := s.emergencyReauth(r, u, r.PostFormValue("code")); msg != "" {
		fail(status, msg)
		return
	}
	e, err := s.portal.StartEmergency(r.Context(), site.ID, u.ID, ef.Category, ef.Justification, clientIP(r))
	switch {
	case errors.Is(err, ErrEmergencyOpen):
		s.redirect(w, r, netSitePath(site.ID, "emergency"), "info", s.t("Your emergency access to %s is already open.", site.Slug))
		return
	case errors.Is(err, ErrNetworkState):
		fail(http.StatusConflict, s.siteStateError("deleted"))
		return
	case errors.Is(err, ErrNetworkInvalid):
		fail(http.StatusUnprocessableEntity, s.t("Explain why you need the access."))
		return
	case errors.Is(err, ErrSupportDenied), errors.Is(err, store.ErrNotFound):
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	until := s.dateTime(e.ExpiresAt)
	body := s.t("%s, an administrator of %s, opened the administration of your site %s without your consent, under the emergency access exception.",
		cmp.Or(u.DisplayName, u.Username), s.settings().SiteTitle, cmp.Or(site.Host, site.Slug)) +
		"\n\n" + s.t("Reason: %s.", s.emergencyReason(e.Category)) +
		"\n\n" + s.t("Their justification:") + "\n" + e.Justification +
		"\n\n" + s.t("The access ends %s at the latest, and can be ended earlier. Only this administrator can use it, and every sign-in is written to your site's audit log.", until) +
		"\n\n" + s.t("Emergency access is only for a legal order or a serious security incident, such as malware or phishing on the site. Any other access needs your consent.")
	notified := s.mailOwner(r.Context(), &site, s.t("Emergency access to %s", site.Slug), body)
	s.audit(r, u.ID, "network.emergency_started", site.Slug, map[string]string{"site": strconv.FormatInt(site.ID, 10),
		"category": e.Category, "justification": e.Justification, "until": e.ExpiresAt.UTC().Format(time.RFC3339),
		"owner_notified": strconv.FormatBool(notified)})
	if !notified {
		s.redirect(w, r, netSitePath(site.ID, "emergency"), "warning", s.t("Your emergency access to %s is open until %s. Its owner could not be told by e-mail: tell them another way.", site.Slug, until))
		return
	}
	s.redirect(w, r, netSitePath(site.ID, "emergency"), "success", s.t("Your emergency access to %s is open until %s. Its owner was told by e-mail.", site.Slug, until))
}

func (s *Server) adminNetEmergencyEnd(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadNetSite(w, r)
	if !ok {
		return
	}
	site := d.Site
	u := currentUser(r)
	n, err := s.portal.EndEmergency(r.Context(), site.ID, u.ID, clientIP(r))
	switch {
	case errors.Is(err, ErrSupportDenied), errors.Is(err, store.ErrNotFound):
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	back := netSitePath(site.ID, "emergency")
	if n == 0 {
		s.redirect(w, r, back, "info", s.t("No emergency access to %s was open.", site.Slug))
		return
	}
	s.audit(r, u.ID, "network.emergency_ended", site.Slug, map[string]string{"site": strconv.FormatInt(site.ID, 10), "count": strconv.Itoa(n)})
	s.mailOwner(r.Context(), &site, s.t("Emergency access to %s ended", site.Slug),
		s.t("The emergency access to your site %s was ended by %s before it expired. The administrators who used it were signed out.",
			cmp.Or(site.Host, site.Slug), cmp.Or(u.DisplayName, u.Username)))
	s.redirect(w, r, back, "success", s.t("The emergency access to %s ended. The network administrators signed in with it were signed out.", site.Slug))
}
