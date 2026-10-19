package web

// Billing of the network's paid plans, on the portal only (docs/BILLING.md):
// an owner upgrades a site from "My sites" through Stripe Checkout and
// manages it in the Customer Portal; Stripe reports back to the webhook;
// network admins see the events that failed or could not be mapped. The
// network (internal/network) does the work through PortalBilling; without
// a Stripe key none of these routes exist.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"sap13ns/internal/billing"
	"sap13ns/internal/store"
)

// PortalBilling is the billing the portal gets from the network.
type PortalBilling interface {
	// BillingEnabled is false when the network has no Stripe key: the
	// portal then shows no billing at all.
	BillingEnabled() bool
	// PlanOffers lists what an owner can subscribe a site to.
	PlanOffers(ctx context.Context) ([]PlanOffer, error)
	// StartCheckout begins the subscription of a site its account owns:
	// store.ErrNotFound for a site it does not own, ErrNetworkInvalid for a
	// plan or interval that cannot be bought, ErrNetworkState when the
	// site is not open or already has a subscription.
	StartCheckout(ctx context.Context, c CheckoutRequest) (*CheckoutResult, error)
	// BillingPortal returns a Customer Portal URL for the billing of a
	// site its account owns (ErrNetworkState when it has none yet).
	BillingPortal(ctx context.Context, accountID, siteID int64) (string, error)
	// ReceiveWebhook verifies and stores a Stripe event:
	// ErrWebhookInvalid when it does not verify.
	ReceiveWebhook(ctx context.Context, payload []byte, signature string) error
	// BillingEvents lists the events that failed or were not mapped to a
	// site, newest first, and their total; FailingBillingEvents counts the
	// failed ones still waiting.
	BillingEvents(ctx context.Context, limit, offset int) ([]NetBillingEvent, int, error)
	FailingBillingEvents(ctx context.Context) (int, error)
	// RetryBillingEvent processes such an event again (store.ErrNotFound
	// for any other).
	RetryBillingEvent(ctx context.Context, id string) error
}

// ErrWebhookInvalid is a webhook request whose signature does not verify.
var ErrWebhookInvalid = errors.New("billing: invalid webhook")

// A PlanOffer is one price of a paid plan.
type PlanOffer struct {
	Plan     string
	Name     string
	Yearly   bool
	Amount   int64 // in the currency's smallest unit
	Currency string
}

// Value is the offer's form value: the plan and its interval. Prices
// never leave the server.
func (o PlanOffer) Value() string {
	if o.Yearly {
		return o.Plan + ":year"
	}
	return o.Plan + ":month"
}

// zeroDecimal are the currencies Stripe counts in whole units.
var zeroDecimal = map[string]bool{"bif": true, "clp": true, "djf": true, "gnf": true, "jpy": true, "kmf": true, "krw": true,
	"mga": true, "pyg": true, "rwf": true, "ugx": true, "vnd": true, "vuv": true, "xaf": true, "xof": true, "xpf": true}

// Price is the amount with its currency, such as "19.00 EUR".
func (o PlanOffer) Price() string {
	cur := strings.ToUpper(o.Currency)
	if zeroDecimal[strings.ToLower(o.Currency)] {
		return fmt.Sprintf("%d %s", o.Amount, cur)
	}
	return fmt.Sprintf("%d.%02d %s", o.Amount/100, o.Amount%100, cur)
}

type CheckoutRequest struct {
	AccountID int64
	SiteID    int64
	Plan      string
	Yearly    bool
	Email     string
	Locale    string
}

type CheckoutResult struct {
	URL      string // Stripe Checkout
	Attached bool   // the site joined a subscription of its owner with room for it
}

// NetBilling is the subscription that pays for a site.
type NetBilling struct {
	Plan              string
	Status            string // Stripe's
	State             string // active, pending, grace or canceled
	PeriodEnd         *time.Time
	CancelAtPeriodEnd bool
	GraceSince        *time.Time
	Held              bool // its public pages are not served
	Manageable        bool // it has a Stripe customer, so the Customer Portal opens
}

// A NetBillingEvent is a Stripe event that failed or was not mapped.
type NetBillingEvent struct {
	ID          string
	Type        string
	Created     time.Time
	ReceivedAt  time.Time
	ProcessedAt *time.Time
	Attempts    int
	Result      string
	NextAttempt *time.Time // nil once it gave up, or when processed
	Unmapped    bool
}

// BillingNotice is a billing message for an account of the portal.
type BillingNotice struct {
	Kind      string // "payment_failed"
	AccountID int64
	Slug      string // the site, if any
	Host      string
	GraceDays int
}

// NotifyBilling e-mails an account about its billing, in the portal's
// language, with a link to "My sites" (where "Manage billing" opens the
// Customer Portal).
func (s *Server) NotifyBilling(ctx context.Context, n BillingNotice) error {
	u, err := s.store.UserByID(ctx, n.AccountID)
	if err != nil {
		return err
	}
	if n.Kind != "payment_failed" {
		return fmt.Errorf("billing notice %q", n.Kind)
	}
	title := s.settings().SiteTitle
	subject := s.t("A payment for your sites failed")
	body := s.t("We could not charge the renewal of your subscription on %s.", title)
	if n.Host != "" {
		subject = s.t("The payment for %s failed", n.Host)
		body = s.t("We could not charge the renewal of the subscription of your site %s on %s.", n.Host, title)
	}
	if n.GraceDays > 0 {
		body += " " + s.t("The site stays open for %d days while the payment is retried.", n.GraceDays)
	}
	body += "\n\n" + s.t("Update your payment method from your sites, with “Manage billing”:") + "\n" + s.adminLink("/admin/sites")
	return s.mailer.Send(u.Email, subject+" · "+title, body)
}

// ------------------------------------------------------------- upgrade --

// stripeForms are the origins the upgrade and billing forms redirect to:
// the form-action of "My sites" allows them.
const stripeForms = "https://checkout.stripe.com https://billing.stripe.com"

func (s *Server) adminSiteUpgrade(w http.ResponseWriter, r *http.Request) {
	const back = "/admin/sites"
	u := currentUser(r)
	id, ok := pathID(r)
	if !ok {
		s.adminError(w, r, http.StatusNotFound, s.tu(r, "Not found."))
		return
	}
	plan, interval, ok := strings.Cut(r.FormValue("offer"), ":")
	if !ok || interval != "month" && interval != "year" || plan == "" || len(plan) > 32 {
		s.redirect(w, r, back, "error", s.tu(r, "Invalid request."))
		return
	}
	if !s.checkout.Allow(strconv.FormatInt(u.ID, 10)) {
		s.redirect(w, r, back, "error", s.tu(r, "Too many attempts. Try again later."))
		return
	}
	res, err := s.billing.StartCheckout(r.Context(), CheckoutRequest{AccountID: u.ID, SiteID: id, Plan: plan, Yearly: interval == "year",
		Email: u.Email, Locale: s.uiLang(r)})
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.adminError(w, r, http.StatusNotFound, s.tu(r, "Not found."))
		return
	case errors.Is(err, ErrNetworkInvalid):
		s.redirect(w, r, back, "error", s.tu(r, "That plan cannot be bought."))
		return
	case errors.Is(err, ErrNetworkState):
		s.redirect(w, r, back, "error", s.tu(r, "This site cannot subscribe now: it is not open, or it has a subscription already."))
		return
	case err != nil:
		s.log.Error("start a checkout", "site", id, "err", err)
		s.redirect(w, r, back, "error", s.tu(r, "The payment page could not be opened. Try again later."))
		return
	}
	details := map[string]string{"site": strconv.FormatInt(id, 10), "plan": plan, "interval": interval}
	if res.Attached {
		s.audit(r, u.ID, "network.subscription_attached", strconv.FormatInt(id, 10), details)
		s.redirect(w, r, back, "success", s.tu(r, "The site is now on your subscription."))
		return
	}
	s.audit(r, u.ID, "network.checkout_started", strconv.FormatInt(id, 10), details)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, res.URL, http.StatusSeeOther)
}

func (s *Server) adminSiteBilling(w http.ResponseWriter, r *http.Request) {
	const back = "/admin/sites"
	u := currentUser(r)
	id, ok := pathID(r)
	if !ok {
		s.adminError(w, r, http.StatusNotFound, s.tu(r, "Not found."))
		return
	}
	if !s.checkout.Allow(strconv.FormatInt(u.ID, 10)) {
		s.redirect(w, r, back, "error", s.tu(r, "Too many attempts. Try again later."))
		return
	}
	target, err := s.billing.BillingPortal(r.Context(), u.ID, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.adminError(w, r, http.StatusNotFound, s.tu(r, "Not found."))
		return
	case errors.Is(err, ErrNetworkState):
		s.redirect(w, r, back, "error", s.tu(r, "This site has no billing yet."))
		return
	case err != nil:
		s.log.Error("open the billing portal", "site", id, "err", err)
		s.redirect(w, r, back, "error", s.tu(r, "The billing page could not be opened. Try again later."))
		return
	}
	s.audit(r, u.ID, "network.billing_portal", strconv.FormatInt(id, 10), map[string]string{"site": strconv.FormatInt(id, 10)})
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// ------------------------------------------------------------- webhook --

// handleBillingWebhook stores a Stripe event and answers at once: 400 for
// a request that does not verify, 500 when it could not be stored (Stripe
// retries), 200 otherwise, whatever the event is about.
func (s *Server) handleBillingWebhook(w http.ResponseWriter, r *http.Request) {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(strings.ToLower(ct), "application/json") {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "application/json required"})
		return
	}
	payload, err := billing.ReadWebhookBody(http.MaxBytesReader(w, r.Body, billing.MaxWebhookPayload+1))
	if err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "payload too large"})
		return
	}
	switch err := s.billing.ReceiveWebhook(r.Context(), payload, r.Header.Get(billing.SignatureHeader)); {
	case errors.Is(err, ErrWebhookInvalid):
		s.log.Warn("billing webhook refused", "ip", clientIP(r).String(), "err", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid signature"})
		return
	case err != nil:
		s.log.Error("billing webhook", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "not stored"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"received": true})
}

// -------------------------------------------------------------- events --

func (s *Server) adminNetBilling(w http.ResponseWriter, r *http.Request) {
	page := listPage(r)
	const per = 50
	events, total, err := s.billing.BillingEvents(r.Context(), per, (page-1)*per)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderAdmin(w, r, "network_billing", s.tu(r, "Billing events"), "net-billing", map[string]any{"Events": events, "Total": total,
		"Page": newPagination("/admin/network/billing", r.URL.Query(), page, per, total)})
}

func (s *Server) adminNetBillingRetry(w http.ResponseWriter, r *http.Request) {
	const back = "/admin/network/billing"
	id := r.PathValue("event")
	if !strings.HasPrefix(id, "evt_") || len(id) > 255 {
		s.adminError(w, r, http.StatusNotFound, s.tu(r, "Not found."))
		return
	}
	switch err := s.billing.RetryBillingEvent(r.Context(), id); {
	case errors.Is(err, store.ErrNotFound):
		s.redirect(w, r, back, "error", s.tu(r, "Not found."))
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	s.audit(r, uid(r), "network.billing_event_retried", id, nil)
	s.redirect(w, r, back, "success", s.tu(r, "The event %s will be processed again.", id))
}
