package billing

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"
)

// CheckoutParams describes a subscription Checkout Session for one site.
type CheckoutParams struct {
	// Customer is an existing Stripe customer (cus_...). Set it when the
	// site owner already has one so all their sites share one customer and
	// portal. Mutually exclusive with CustomerEmail.
	Customer string
	// CustomerEmail prefills the email of the customer Checkout creates.
	CustomerEmail string

	PriceID  string // required, price_...
	Quantity int    // defaults to 1

	// SuccessURL is required. It may contain the literal {CHECKOUT_SESSION_ID}
	// placeholder, which Stripe replaces with the session ID.
	SuccessURL string
	CancelURL  string

	// ClientReferenceID ties the session back to sap13ns, e.g. the site ID
	// (max 200 characters).
	ClientReferenceID string
	// Metadata is set on both the session and the subscription it creates
	// (subscription_data[metadata]) so later subscription and invoice events
	// can be mapped to the site. Put "site_id" here.
	Metadata map[string]string

	TrialDays           int  // subscription_data[trial_period_days]; 0 = no trial
	AllowPromotionCodes bool // allow_promotion_codes
	// Locale is a BCP 47 tag such as "es-ES"; it is mapped to the closest
	// locale Checkout supports (see CheckoutLocale). Empty lets Stripe use
	// the browser locale.
	Locale string

	// TaxIDCollection shows the business tax ID (e.g. EU VAT) form.
	TaxIDCollection bool
	// AutomaticTax enables Stripe Tax on the session and subscription.
	AutomaticTax bool
	// BillingAddressRequired always collects a billing address.
	BillingAddressRequired bool
}

// CreateCheckoutSession creates a hosted Checkout Session in subscription
// mode. Pass an idempotency key (see NewIdempotencyKey) to make the call
// safe to retry and to deduplicate double submissions; with an empty key the
// POST is sent once and never retried.
func (c *Client) CreateCheckoutSession(ctx context.Context, p CheckoutParams, idempotencyKey string) (*CheckoutSession, error) {
	if err := checkID("price", p.PriceID); err != nil {
		return nil, err
	}
	if p.Customer != "" && p.CustomerEmail != "" {
		return nil, errors.New("billing: set Customer or CustomerEmail, not both")
	}
	if p.Customer != "" {
		if err := checkID("customer", p.Customer); err != nil {
			return nil, err
		}
	}
	if p.CustomerEmail != "" && (utf8.RuneCountInString(p.CustomerEmail) > 800 || !strings.Contains(p.CustomerEmail, "@")) {
		return nil, errors.New("billing: invalid customer email")
	}
	if err := checkRedirectURL("success", p.SuccessURL, true); err != nil {
		return nil, err
	}
	if err := checkRedirectURL("cancel", p.CancelURL, false); err != nil {
		return nil, err
	}
	if utf8.RuneCountInString(p.ClientReferenceID) > 200 {
		return nil, errors.New("billing: client reference ID longer than 200 characters")
	}
	if err := checkMetadata(p.Metadata); err != nil {
		return nil, err
	}
	qty := p.Quantity
	switch {
	case qty == 0:
		qty = 1
	case qty < 0:
		return nil, errors.New("billing: negative quantity")
	}
	if p.TrialDays < 0 {
		return nil, errors.New("billing: negative trial days")
	}

	params := map[string]any{
		"mode":        "subscription",
		"success_url": p.SuccessURL,
		"line_items": []any{
			map[string]any{"price": p.PriceID, "quantity": qty},
		},
	}
	if p.CancelURL != "" {
		params["cancel_url"] = p.CancelURL
	}
	if p.Customer != "" {
		params["customer"] = p.Customer
	} else if p.CustomerEmail != "" {
		params["customer_email"] = p.CustomerEmail
	}
	if p.ClientReferenceID != "" {
		params["client_reference_id"] = p.ClientReferenceID
	}
	sub := map[string]any{}
	if len(p.Metadata) > 0 {
		params["metadata"] = p.Metadata
		sub["metadata"] = p.Metadata
	}
	if p.TrialDays > 0 {
		sub["trial_period_days"] = p.TrialDays
	}
	if len(sub) > 0 {
		params["subscription_data"] = sub
	}
	if p.AllowPromotionCodes {
		params["allow_promotion_codes"] = true
	}
	if loc := CheckoutLocale(p.Locale); p.Locale != "" {
		params["locale"] = loc
	}
	// With an existing customer, Checkout only writes collected details back
	// to the Customer when customer_update allows it; Stripe needs the name
	// for tax ID collection and the address for automatic tax.
	update := map[string]any{}
	if p.TaxIDCollection {
		params["tax_id_collection"] = map[string]any{"enabled": true}
		if p.Customer != "" {
			update["name"] = "auto"
		}
	}
	if p.AutomaticTax {
		params["automatic_tax"] = map[string]any{"enabled": true}
		if p.Customer != "" {
			update["address"] = "auto"
		}
	}
	if p.BillingAddressRequired {
		params["billing_address_collection"] = "required"
		if p.Customer != "" {
			update["address"] = "auto"
		}
	}
	if len(update) > 0 {
		params["customer_update"] = update
	}

	var raw rawCheckoutSession
	if err := c.do(ctx, http.MethodPost, "/v1/checkout/sessions", params, idempotencyKey, &raw); err != nil {
		return nil, err
	}
	if err := checkObject(raw.Object, "checkout.session"); err != nil {
		return nil, err
	}
	s := raw.export()
	if s.URL == "" {
		return nil, fmt.Errorf("billing: checkout session %s has no URL", s.ID)
	}
	return s, nil
}

// CreatePortalSession creates a Customer Portal session and returns its URL.
// The portal's features (cancel, switch plan, update card, invoices) are
// configured in the Stripe Dashboard.
func (c *Client) CreatePortalSession(ctx context.Context, customerID, returnURL string) (string, error) {
	if err := checkID("customer", customerID); err != nil {
		return "", err
	}
	if err := checkRedirectURL("return", returnURL, true); err != nil {
		return "", err
	}
	var out struct {
		Object string `json:"object"`
		URL    string `json:"url"`
	}
	params := map[string]any{"customer": customerID, "return_url": returnURL}
	// A fresh key per call makes network retries safe without deduplicating
	// separate clicks.
	if err := c.do(ctx, http.MethodPost, "/v1/billing_portal/sessions", params, NewIdempotencyKey(), &out); err != nil {
		return "", err
	}
	if err := checkObject(out.Object, "billing_portal.session"); err != nil {
		return "", err
	}
	if out.URL == "" {
		return "", errors.New("billing: portal session has no URL")
	}
	return out.URL, nil
}

// GetSubscription retrieves a subscription. Use it to refresh state from the
// source of truth, e.g. before acting on an out-of-order webhook.
func (c *Client) GetSubscription(ctx context.Context, id string) (*Subscription, error) {
	if err := checkID("subscription", id); err != nil {
		return nil, err
	}
	var raw rawSubscription
	if err := c.do(ctx, http.MethodGet, "/v1/subscriptions/"+id, nil, "", &raw); err != nil {
		return nil, err
	}
	if err := checkObject(raw.Object, "subscription"); err != nil {
		return nil, err
	}
	return raw.export(), nil
}

// CancelSubscription cancels a subscription. With atPeriodEnd the
// subscription stays active until the end of the paid period
// (cancel_at_period_end=true) and Stripe sends customer.subscription.deleted
// then; otherwise it is canceled immediately without proration.
func (c *Client) CancelSubscription(ctx context.Context, id string, atPeriodEnd bool) (*Subscription, error) {
	if err := checkID("subscription", id); err != nil {
		return nil, err
	}
	var raw rawSubscription
	var err error
	if atPeriodEnd {
		err = c.do(ctx, http.MethodPost, "/v1/subscriptions/"+id,
			map[string]any{"cancel_at_period_end": true}, NewIdempotencyKey(), &raw)
	} else {
		err = c.do(ctx, http.MethodDelete, "/v1/subscriptions/"+id, nil, "", &raw)
	}
	if err != nil {
		return nil, err
	}
	if err := checkObject(raw.Object, "subscription"); err != nil {
		return nil, err
	}
	return raw.export(), nil
}

// GetCustomer retrieves a customer. A deleted customer is returned with
// Deleted set rather than as an error.
func (c *Client) GetCustomer(ctx context.Context, id string) (*Customer, error) {
	if err := checkID("customer", id); err != nil {
		return nil, err
	}
	var raw rawCustomer
	if err := c.do(ctx, http.MethodGet, "/v1/customers/"+id, nil, "", &raw); err != nil {
		return nil, err
	}
	if err := checkObject(raw.Object, "customer"); err != nil {
		return nil, err
	}
	return raw.export(), nil
}

// maxPricePages bounds ListPrices pagination (100 prices per page).
const maxPricePages = 10

// ListPrices returns recurring prices with their product names, for the
// admin plan picker. active mirrors Stripe's filter: true lists active
// prices, false lists archived ones.
func (c *Client) ListPrices(ctx context.Context, active bool) ([]Price, error) {
	var prices []Price
	after := ""
	for page := 0; page < maxPricePages; page++ {
		params := map[string]any{
			"active": active,
			"type":   "recurring",
			"limit":  100,
			"expand": []string{"data.product"},
		}
		if after != "" {
			params["starting_after"] = after
		}
		var out struct {
			Data    []rawPrice `json:"data"`
			HasMore bool       `json:"has_more"`
		}
		if err := c.do(ctx, http.MethodGet, "/v1/prices", params, "", &out); err != nil {
			return nil, err
		}
		for i := range out.Data {
			prices = append(prices, out.Data[i].export())
		}
		if !out.HasMore || len(out.Data) == 0 {
			return prices, nil
		}
		after = out.Data[len(out.Data)-1].ID
		if !validID(after) {
			return nil, errors.New("billing: invalid pagination cursor from Stripe")
		}
	}
	return prices, nil
}

func checkObject(got, want string) error {
	if got != want {
		return fmt.Errorf("billing: expected %s object, got %q", want, truncate(got, 40))
	}
	return nil
}

// checkRedirectURL requires an absolute https URL (http only for loopback,
// for local development).
func checkRedirectURL(name, raw string, required bool) error {
	if raw == "" {
		if required {
			return fmt.Errorf("billing: %s URL is required", name)
		}
		return nil
	}
	if len(raw) > 2048 {
		return fmt.Errorf("billing: %s URL too long", name)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return fmt.Errorf("billing: %s URL must be absolute", name)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Hostname())) {
		return fmt.Errorf("billing: %s URL must use https", name)
	}
	return nil
}

// checkoutLocales are the values Checkout accepts for locale
// (docs.stripe.com/api/checkout/sessions/create, October 2026).
var checkoutLocales = map[string]string{}

func init() {
	for _, l := range []string{
		"bg", "cs", "da", "de", "el", "en", "en-GB", "es", "es-419", "et", "fi", "fil",
		"fr", "fr-CA", "hr", "hu", "id", "it", "ja", "ko", "lt", "lv", "ms", "mt", "nb",
		"nl", "pl", "pt", "pt-BR", "ro", "ru", "sk", "sl", "sv", "th", "tr", "vi", "zh",
		"zh-HK", "zh-TW",
	} {
		checkoutLocales[strings.ToLower(l)] = l
	}
}

// CheckoutLocale maps a BCP 47 language tag ("es-ES", "pt_BR", "en") to a
// locale Stripe Checkout supports, falling back to the base language and
// then to "auto".
func CheckoutLocale(tag string) string {
	t := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(tag), "_", "-"))
	if t == "" || t == "auto" {
		return "auto"
	}
	if l, ok := checkoutLocales[t]; ok {
		return l
	}
	// Latin-American Spanish variants share es-419.
	if base, region, ok := strings.Cut(t, "-"); ok {
		if base == "es" && len(region) == 2 && region != "es" {
			return "es-419"
		}
		if l, ok := checkoutLocales[base]; ok {
			return l
		}
	}
	return "auto"
}
