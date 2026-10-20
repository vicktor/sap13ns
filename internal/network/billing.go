package network

// Billing of the paid plans through Stripe (docs/BILLING.md):
//
//   - At start, every configured price must be an active recurring price
//     of the key's mode (live or test), for a paid plan of the network;
//     otherwise the server does not start. Without a key there is no
//     billing: a network of Personal (and operator-granted) sites.
//   - An owner upgrades a site with a Checkout Session for the plan's
//     price, which the server maps from the plan's name: the browser never
//     sends a price. The session carries the site in client_reference_id
//     and metadata, and a pending subscription row stands for it until the
//     webhook links the Stripe subscription. "Manage billing" opens the
//     Customer Portal of the subscription's customer.
//   - The webhook only verifies the signature (with every configured
//     secret, for rotation) and stores the event once (ON CONFLICT DO
//     NOTHING); a processor goroutine applies the events in the order they
//     were created, each claimed with FOR UPDATE SKIP LOCKED. It fetches
//     the subscription from Stripe instead of trusting the event's
//     snapshot, so duplicate and out-of-order deliveries end in Stripe's
//     current state. A failed event is retried with a growing delay, up to
//     billingMaxAttempts; failed and unmapped events are listed in the
//     network admin area.
//   - The host table holds a paid site whose subscription is in grace for
//     longer than SAP13NS_BILLING_GRACE_DAYS, pending, or canceled once
//     the period it paid for is over (public 503, admin and sign-in still
//     work); invoice.paid lifts it. Nothing is ever deleted for billing.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"sap13ns/internal/billing"
	"sap13ns/internal/config"
	"sap13ns/internal/store"
)

// Processing limits of the webhook events.
const (
	billingEvery        = 30 * time.Second // the processor also wakes on every new event
	billingLease        = 5 * time.Minute  // a claimed event is retried after this if its process died
	billingEventTimeout = 2 * time.Minute
	billingMaxAttempts  = 10
	billingRetryFirst   = time.Minute
	billingRetryMax     = 6 * time.Hour
	billingKeep         = 90 * 24 * time.Hour // processed events are purged after this
)

var (
	// ErrWebhook is a webhook request that did not verify.
	ErrWebhook = errors.New("network: invalid webhook")
	// ErrLimit is an addition the site's plan does not allow.
	ErrLimit = errors.New("network: over the plan's limit")
)

// Results of a processed event.
const (
	resultApplied  = "applied"
	resultIgnored  = "ignored"  // an event type or mode billing does not act on
	resultUnmapped = "unmapped" // not about a site of this network: a prefix, with the reason
)

// A Subscription is what the network knows of a Stripe subscription.
type Subscription struct {
	ID                   int64      `db:"id"`
	AccountID            int64      `db:"account_id"`
	Plan                 string     `db:"plan"`
	StripeSubscriptionID string     `db:"stripe_subscription_id"` // "" until Checkout completes
	StripePriceID        string     `db:"stripe_price_id"`
	StripeCustomerID     string     `db:"stripe_customer_id"`
	Status               string     `db:"status"` // Stripe's
	State                string     `db:"state"`  // billing.SubscriptionState
	GraceSince           *time.Time `db:"grace_since"`
	CurrentPeriodEnd     *time.Time `db:"current_period_end"`
	PaidUntil            *time.Time `db:"paid_until"` // the end of the last period paid for (invoice.paid)
	CancelAtPeriodEnd    bool       `db:"cancel_at_period_end"`
	SyncedAt             *time.Time `db:"synced_at"`
	CheckoutSiteID       *int64     `db:"checkout_site_id"`
	Manual               bool       `db:"manual"` // granted by an operator for a year (billing_manual.go)
	CreatedAt            time.Time  `db:"created_at"`
	UpdatedAt            time.Time  `db:"updated_at"`
}

const subCols = `id, account_id, plan, coalesce(stripe_subscription_id, '') AS stripe_subscription_id, stripe_price_id,
	stripe_customer_id, status, state, grace_since, current_period_end, paid_until, cancel_at_period_end, synced_at, checkout_site_id,
	manual, created_at, updated_at`

func (n *Network) Subscription(ctx context.Context, id int64) (*Subscription, error) {
	rows, err := n.db.Query(ctx, `SELECT `+subCols+` FROM subscriptions WHERE id = $1`, id)
	if err != nil {
		return nil, err
	}
	s, err := pgx.CollectExactlyOneRow(rows, pgx.RowToAddrOfStructByName[Subscription])
	return s, mapErr(err)
}

// SiteSubscription returns the subscription that pays for a site, or nil.
func (n *Network) SiteSubscription(ctx context.Context, s *Site) (*Subscription, error) {
	if s.SubscriptionID == nil {
		return nil, nil
	}
	sub, err := n.Subscription(ctx, *s.SubscriptionID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	return sub, err
}

// Held reports whether a site is not served to the public for its billing:
// its plan is paid and the subscription paying for it is pending, in grace
// for longer than the grace period, or canceled once the period it paid
// for is over. A paid site without a subscription was granted by an
// operator and is never held.
func (n *Network) Held(paid bool, sub *Subscription, now time.Time) bool {
	if !paid || sub == nil {
		return false
	}
	if sub.Manual && sub.State == string(billing.StateActive) {
		return manualHeld(sub.PaidUntil, now)
	}
	return held(sub.State, sub.GraceSince, sub.PaidUntil, now, n.grace)
}

// held is the rule of Network.Held, which the host table applies too. A
// subscription canceled before the end of a period it paid for (at once,
// from the Stripe Dashboard) keeps its sites served until that end, as a
// cancellation at the end of the period does; the host table is reloaded
// every minute (reloadEvery), so the hold starts within a minute of it.
// A refund that should take the site offline at once is the operator's
// suspension.
func held(state string, graceSince, paidUntil *time.Time, now time.Time, grace time.Duration) bool {
	if state == string(billing.StateCanceled) && paidUntil != nil && now.Before(*paidUntil) {
		return false
	}
	var since time.Time
	if graceSince != nil {
		since = *graceSince
	}
	return billing.ShouldSuspend(billing.SubscriptionState(state), since, now, grace)
}

// graceOf is the grace period of a configuration: 0 days means none (a
// failed renewal holds the site at once), and without billing the default.
func graceOf(s config.Stripe) time.Duration {
	if s.Enabled() && s.GraceDays == 0 {
		return time.Nanosecond
	}
	return time.Duration(s.GraceDays) * 24 * time.Hour // 0: billing.DefaultGracePeriod
}

// AccountCustomer returns the Stripe customer of an account, or "".
func (n *Network) AccountCustomer(ctx context.Context, accountID int64) (string, error) {
	var c string
	err := n.db.QueryRow(ctx, `SELECT stripe_customer_id FROM account_billing WHERE account_id = $1`, accountID).Scan(&c)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return c, err
}

// A Price is a configured Stripe price of a plan.
type Price struct {
	Plan     string
	Yearly   bool
	ID       string
	Amount   int64 // in the currency's smallest unit
	Currency string
	// TaxBehavior is the Stripe price's: inclusive (VAT included),
	// exclusive (VAT added on top) or unspecified.
	TaxBehavior string
}

// A BillingNotice is something to tell an account about its billing.
type BillingNotice struct {
	Kind      string // "payment_failed"
	AccountID int64
	SiteID    int64 // 0 when the subscription pays for no site
	Slug      string
	Host      string
	GraceDays int
}

// NoticePaymentFailed: a renewal could not be charged.
const NoticePaymentFailed = "payment_failed"

// Billing connects a network to Stripe. Create it with NewBilling; a nil
// *Billing means billing is off.
type Billing struct {
	n      *Network
	client *billing.Client
	stripe config.Stripe
	base   *url.URL          // the portal: return and redirect URLs
	plans  map[string]string // price ID -> plan
	prices []Price           // configured, in the plans' order
	log    *slog.Logger

	now    func() time.Time
	notify func(context.Context, BillingNotice) // set by the registry
	wake   chan struct{}
}

// NewBilling returns the billing of a network, or nil when
// SAP13NS_STRIPE_SECRET_KEY is unset. It checks that every configured
// price is an active recurring price of the key's mode for a paid plan of
// the network: an error means the server must not start. opts are for
// tests (billing.WithBaseURL on a fake Stripe); the real Stripe API is the
// default.
func NewBilling(ctx context.Context, n *Network, cfg *config.Config, log *slog.Logger, opts ...billing.Option) (*Billing, error) {
	if !cfg.Stripe.Enabled() {
		return nil, nil
	}
	// One retry inside a call: an event that fails is retried later anyway.
	all := []billing.Option{billing.WithMaxRetries(1)}
	if v := cfg.Stripe.APIVersion; v != "" {
		all = append(all, billing.WithAPIVersion(v))
	}
	c, err := billing.New(cfg.Stripe.SecretKey, append(all, opts...)...)
	if err != nil {
		return nil, err
	}
	b := &Billing{n: n, client: c, stripe: cfg.Stripe, base: cfg.BaseURL, plans: map[string]string{}, log: log,
		now: time.Now, wake: make(chan struct{}, 1)}
	if err := b.check(ctx); err != nil {
		return nil, err
	}
	return b, nil
}

// check verifies the configured prices against the network's plans and
// against Stripe.
func (b *Billing) check(ctx context.Context) error {
	plans, err := b.n.Plans(ctx)
	if err != nil {
		return err
	}
	for slug := range b.stripe.Prices {
		if !slices.ContainsFunc(plans, func(p Plan) bool { return p.Slug == slug && p.Paid }) {
			return fmt.Errorf("billing: SAP13NS_STRIPE_PRICE_%s: the network has no paid plan %q", strings.ToUpper(slug), slug)
		}
	}
	listed, err := b.client.ListPrices(ctx, true)
	if err != nil {
		return fmt.Errorf("billing: list the Stripe prices: %w", err)
	}
	found := map[string]billing.Price{}
	for _, p := range listed {
		found[p.ID] = p
	}
	mode := "test"
	if b.client.Livemode() {
		mode = "live"
	}
	for _, plan := range plans {
		cp, ok := b.stripe.Prices[plan.Slug]
		if !ok {
			continue
		}
		for _, x := range []struct {
			id     string
			yearly bool
		}{{cp.Monthly, false}, {cp.Yearly, true}} {
			if x.id == "" {
				continue
			}
			p, ok := found[x.id]
			switch {
			case !ok:
				return fmt.Errorf("billing: %s (plan %s) is not an active recurring price of the Stripe account in %s mode; "+
					"a live key needs live prices and a test key test prices", x.id, plan.Slug, mode)
			case p.Livemode != b.client.Livemode():
				return fmt.Errorf("billing: %s (plan %s) is not a %s-mode price", x.id, plan.Slug, mode)
			case b.plans[x.id] != "":
				return fmt.Errorf("billing: %s is configured for both %s and %s", x.id, b.plans[x.id], plan.Slug)
			}
			if want := map[bool]string{false: "month", true: "year"}[x.yearly]; p.Interval != want || p.IntervalCount != 1 {
				b.log.Warn("a Stripe price does not renew as its variable says", "price", x.id, "plan", plan.Slug,
					"interval", p.Interval, "count", p.IntervalCount, "expected", want)
			}
			b.plans[x.id] = plan.Slug
			b.prices = append(b.prices, Price{Plan: plan.Slug, Yearly: x.yearly, ID: x.id, Amount: p.UnitAmount, Currency: p.Currency,
				TaxBehavior: p.TaxBehavior})
		}
	}
	return nil
}

// Prices lists the prices an owner can subscribe to.
func (b *Billing) Prices() []Price { return slices.Clone(b.prices) }

func (b *Billing) price(plan string, yearly bool) (Price, bool) {
	for _, p := range b.prices {
		if p.Plan == plan && p.Yearly == yearly {
			return p, true
		}
	}
	return Price{}, false
}

// ------------------------------------------------------------- checkout --

type CheckoutRequest struct {
	AccountID int64 // signed in on the portal; must own the site
	SiteID    int64
	Plan      string
	Yearly    bool
	Email     string // the account's, for a new Stripe customer
	Locale    string
}

type CheckoutResult struct {
	URL      string // the Checkout page to send the owner to
	Attached bool   // no payment needed: the site joined a subscription with room (sites_per_grant)
}

// ownedSite returns a tenant site of the account: ErrNotFound for any
// other, so nobody learns about sites they do not own.
func (b *Billing) ownedSite(ctx context.Context, accountID, siteID int64) (*Site, error) {
	s, err := b.n.Site(ctx, siteID)
	if err != nil {
		return nil, err
	}
	if s.Kind != KindTenant || s.OwnerID != accountID || accountID == 0 {
		return nil, store.ErrNotFound
	}
	return s, nil
}

// Checkout starts the subscription of a site to a paid plan: a Checkout
// Session, unless an active subscription of the account to the plan has
// room for another site. Only an open site without a live subscription
// can subscribe (ErrState).
func (b *Billing) Checkout(ctx context.Context, req CheckoutRequest) (*CheckoutResult, error) {
	site, err := b.ownedSite(ctx, req.AccountID, req.SiteID)
	if err != nil {
		return nil, err
	}
	if site.Status != StatusActive {
		return nil, fmt.Errorf("%w: the site is %s", ErrState, site.Status)
	}
	plan, err := b.n.Plan(ctx, req.Plan)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("%w: no plan %q", ErrInvalid, req.Plan)
	} else if err != nil {
		return nil, err
	}
	price, ok := b.price(plan.Slug, req.Yearly)
	if !plan.Paid || !ok {
		return nil, fmt.Errorf("%w: the plan %s cannot be bought that way", ErrInvalid, plan.Slug)
	}
	cur, err := b.n.SiteSubscription(ctx, site)
	if err != nil {
		return nil, err
	}
	if cur != nil && cur.State != string(billing.StateCanceled) && !cur.Manual {
		return nil, fmt.Errorf("%w: the site has a subscription; change it in the billing portal", ErrState)
	}
	if plan.SitesPerGrant > 1 {
		if ok, err := b.attachToGrant(ctx, site, plan); err != nil || ok {
			return &CheckoutResult{Attached: ok}, err
		}
	}

	// The pending row the session stands for; a resubmission reuses it.
	rowID, paidBefore, err := b.pendingRow(ctx, req.AccountID, site.ID, plan.Slug)
	if err != nil {
		return nil, err
	}
	customer, err := b.n.AccountCustomer(ctx, req.AccountID)
	if err != nil {
		return nil, err
	}
	p := billing.CheckoutParams{
		PriceID:           price.ID,
		SuccessURL:        b.base.String() + "/admin/sites?billing=success",
		CancelURL:         b.base.String() + "/admin/sites?billing=canceled",
		ClientReferenceID: "site:" + strconv.FormatInt(site.ID, 10),
		Metadata: map[string]string{"site_id": strconv.FormatInt(site.ID, 10), "account_id": strconv.FormatInt(req.AccountID, 10),
			"plan": plan.Slug, "subscription": strconv.FormatInt(rowID, 10)},
		Locale:                 req.Locale,
		TaxIDCollection:        b.stripe.TaxIDCollection,
		AutomaticTax:           b.stripe.AutomaticTax,
		BillingAddressRequired: b.stripe.BillingAddress,
	}
	if !paidBefore {
		p.TrialDays = b.stripe.TrialDays // a trial for an account's first subscription only
	}
	if customer != "" {
		p.Customer = customer
	} else {
		p.CustomerEmail = req.Email
	}
	// The same request again (a double click, a return from Checkout) gets
	// the same session for 24 hours; other parameters get another key.
	sum := sha256.Sum256([]byte(fmt.Sprintf("checkout|%d|%d|%s|%s|%s|%d", rowID, site.ID, price.ID, customer, req.Email, p.TrialDays)))
	key := "sap13ns-co-" + hex.EncodeToString(sum[:16])
	cs, err := b.client.CreateCheckoutSession(ctx, p, key)
	if billing.IsIdempotencyError(err) {
		cs, err = b.client.CreateCheckoutSession(ctx, p, billing.NewIdempotencyKey())
	}
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(cs.URL, "https://") {
		return nil, errors.New("billing: the Checkout Session URL is not https")
	}
	b.log.Info("checkout started", "site", site.ID, "plan", plan.Slug, "yearly", req.Yearly, "subscription", rowID)
	return &CheckoutResult{URL: cs.URL}, nil
}

// pendingRow returns the subscription row a Checkout for a site and plan
// stands for until the webhook links it (a resubmission reuses it), and
// whether the account ever had a Stripe subscription. It changes no
// routing, so it does not notify.
func (b *Billing) pendingRow(ctx context.Context, accountID, siteID int64, plan string) (id int64, paidBefore bool, err error) {
	tx, err := b.n.db.Begin(ctx)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback(ctx)
	// Locking the account serialises its checkouts.
	if _, err := tx.Exec(ctx, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, accountID); err != nil {
		return 0, false, err
	}
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM subscriptions WHERE account_id = $1 AND stripe_subscription_id IS NOT NULL)`,
		accountID).Scan(&paidBefore); err != nil {
		return 0, false, err
	}
	err = tx.QueryRow(ctx, `SELECT id FROM subscriptions WHERE account_id = $1 AND checkout_site_id = $2 AND plan = $3
		AND stripe_subscription_id IS NULL ORDER BY id DESC LIMIT 1`, accountID, siteID, plan).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `INSERT INTO subscriptions (account_id, plan, checkout_site_id) VALUES ($1, $2, $3) RETURNING id`,
			accountID, plan, siteID).Scan(&id)
	}
	if err != nil {
		return 0, false, err
	}
	return id, paidBefore, tx.Commit(ctx)
}

// attachToGrant adds a site to an active subscription of its owner to the
// plan that pays for fewer sites than the plan allows.
func (b *Billing) attachToGrant(ctx context.Context, site *Site, plan *Plan) (bool, error) {
	attached := false
	err := b.n.mutate(ctx, site.ID, func(tx pgx.Tx) error {
		var subID int64
		err := tx.QueryRow(ctx, `SELECT sub.id FROM subscriptions sub WHERE sub.account_id = $1 AND sub.plan = $2 AND sub.state = 'active' AND NOT sub.manual
			AND (SELECT count(*) FROM sites x WHERE x.subscription_id = sub.id) < $3
			ORDER BY sub.id LIMIT 1 FOR UPDATE`, site.OwnerID, plan.Slug, plan.SitesPerGrant).Scan(&subID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
		// Counted again under the lock.
		tag, err := tx.Exec(ctx, `UPDATE sites SET subscription_id = $2, plan = $3, updated_at = now()
			WHERE id = $1 AND kind = 'tenant' AND status = 'active'
				AND (SELECT count(*) FROM sites x WHERE x.subscription_id = $2) < $4`, site.ID, subID, plan.Slug, plan.SitesPerGrant)
		attached = err == nil && tag.RowsAffected() == 1
		return err
	})
	return attached, err
}

// PortalURL opens the Customer Portal for the billing of a site its
// account owns: the customer paying its subscription, or the account's.
func (b *Billing) PortalURL(ctx context.Context, accountID, siteID int64) (string, error) {
	site, err := b.ownedSite(ctx, accountID, siteID)
	if err != nil {
		return "", err
	}
	customer := ""
	if sub, err := b.n.SiteSubscription(ctx, site); err != nil {
		return "", err
	} else if sub != nil && sub.AccountID == accountID {
		customer = sub.StripeCustomerID
	}
	if customer == "" {
		if customer, err = b.n.AccountCustomer(ctx, accountID); err != nil {
			return "", err
		}
	}
	if customer == "" {
		return "", fmt.Errorf("%w: no billing account yet", ErrState)
	}
	u, err := b.client.CreatePortalSession(ctx, customer, b.base.String()+"/admin/sites", b.stripe.PortalConfiguration)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(u, "https://") {
		return "", errors.New("billing: the billing portal URL is not https")
	}
	return u, nil
}

// -------------------------------------------------------------- webhook --

// Receive verifies a webhook request and stores its event once. It does
// nothing slow: the processor applies the event. A request that does not
// verify is ErrWebhook.
func (b *Billing) Receive(ctx context.Context, payload []byte, signature string) error {
	ev, err := billing.VerifyWebhookWithSecrets(payload, signature, b.stripe.WebhookSecrets, 0, b.now())
	if err != nil {
		return fmt.Errorf("%w: %w", ErrWebhook, err)
	}
	if !strings.HasPrefix(ev.ID, "evt_") || len(ev.ID) > 255 {
		return fmt.Errorf("%w: event id", ErrWebhook)
	}
	created := ev.Created
	if created.IsZero() {
		created = b.now()
	}
	// Due at once ('-infinity' whatever the clocks say), oldest first.
	tag, err := b.n.db.Exec(ctx, `INSERT INTO billing_events (id, type, created, livemode, object_id, payload, next_attempt_at)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb, '-infinity') ON CONFLICT (id) DO NOTHING`,
		ev.ID, clip(ev.Type, 100), created, ev.Livemode, clip(ev.ObjectID(), 255), string(payload))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 {
		b.Wake()
	}
	return nil
}

// Wake makes the processor look for events now.
func (b *Billing) Wake() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

// ------------------------------------------------------------ processor --

// Run applies the stored events until ctx ends.
func (b *Billing) Run(ctx context.Context) {
	t := time.NewTicker(billingEvery)
	defer t.Stop()
	for {
		b.ProcessDue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-b.wake:
		case <-t.C:
		}
	}
}

// ProcessDue applies the events that are due and returns how many it
// claimed.
func (b *Billing) ProcessDue(ctx context.Context) int {
	n := 0
	for ctx.Err() == nil {
		ok, err := b.processOne(ctx)
		if err != nil {
			if ctx.Err() == nil {
				b.log.Error("billing events", "err", err)
			}
			return n
		}
		if !ok {
			return n
		}
		n++
	}
	return n
}

type storedEvent struct {
	ID       string
	Type     string
	Livemode bool
	Payload  []byte
	Attempts int
}

// processOne claims the oldest due event for billingLease and applies it.
func (b *Billing) processOne(ctx context.Context) (bool, error) {
	now := b.now()
	var ev storedEvent
	err := b.n.db.QueryRow(ctx, `UPDATE billing_events SET attempts = attempts + 1, next_attempt_at = $2
		WHERE id = (SELECT id FROM billing_events WHERE processed_at IS NULL AND next_attempt_at <= $1
			ORDER BY created, id LIMIT 1 FOR UPDATE SKIP LOCKED)
		RETURNING id, type, livemode, payload::text, attempts`, now, now.Add(billingLease)).
		Scan(&ev.ID, &ev.Type, &ev.Livemode, &ev.Payload, &ev.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	ectx, cancel := context.WithTimeout(ctx, billingEventTimeout)
	notices, err := b.apply(ectx, &ev)
	cancel()
	if err != nil {
		b.fail(context.WithoutCancel(ctx), &ev, err)
		return true, nil
	}
	if b.notify != nil {
		for _, nt := range notices {
			b.notify(ctx, nt)
		}
	}
	return true, nil
}

// fail records a failed attempt and when to try again.
func (b *Billing) fail(ctx context.Context, ev *storedEvent, err error) {
	msg := clip(strings.Join(strings.Fields(err.Error()), " "), 500)
	if msg == "" {
		msg = "error"
	}
	delay := min(billingRetryFirst<<min(ev.Attempts-1, 20), billingRetryMax)
	giveUp := ev.Attempts >= billingMaxAttempts
	if _, err := b.n.db.Exec(ctx, `UPDATE billing_events SET result = $2,
		next_attempt_at = CASE WHEN $4 THEN 'infinity'::timestamptz ELSE $3 END WHERE id = $1 AND processed_at IS NULL`,
		ev.ID, msg, b.now().Add(delay), giveUp); err != nil {
		b.log.Error("record a failed billing event", "event", ev.ID, "err", err)
	}
	b.log.Warn("billing event failed", "event", ev.ID, "type", ev.Type, "attempt", ev.Attempts, "gave_up", giveUp, "err", msg)
}

// apply runs an event in a transaction that also marks it processed.
func (b *Billing) apply(ctx context.Context, ev *storedEvent) ([]BillingNotice, error) {
	tx, err := b.n.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	a := &applyRun{b: b, ctx: ctx, tx: tx}
	result, err := a.run(ev)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE billing_events SET processed_at = now(), result = $2 WHERE id = $1`, ev.ID, clip(result, 500)); err != nil {
		return nil, err
	}
	if a.changed {
		if _, err := tx.Exec(ctx, `SELECT pg_notify($1, '')`, Channel); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	if strings.HasPrefix(result, resultUnmapped) {
		b.log.Warn("billing event not mapped to a site", "event", ev.ID, "type", ev.Type, "result", result)
	}
	return a.notices, nil
}

// applyRun is one event being applied.
type applyRun struct {
	b         *Billing
	ctx       context.Context
	tx        pgx.Tx
	changed   bool
	state     string     // of the subscription, after sync
	periodEnd *time.Time // and its current period's end
	notices   []BillingNotice
}

func unmapped(reason string) string { return resultUnmapped + ": " + reason }

func (a *applyRun) run(ev *storedEvent) (string, error) {
	if ev.Livemode != a.b.client.Livemode() {
		return resultIgnored + ": another mode than the key's", nil
	}
	var env struct {
		Created int64 `json:"created"`
		Data    struct {
			Object json.RawMessage `json:"object"`
		} `json:"data"`
	}
	if err := json.Unmarshal(ev.Payload, &env); err != nil || len(env.Data.Object) == 0 {
		return unmapped("the stored payload cannot be read"), nil
	}
	e := &billing.Event{ID: ev.ID, Type: ev.Type, Livemode: ev.Livemode, Data: env.Data.Object}
	switch {
	case e.Type == billing.EventCheckoutSessionCompleted:
		return a.checkout(e)
	case strings.HasPrefix(e.Type, "customer.subscription."):
		return a.subscription(e)
	case e.Type == billing.EventInvoicePaid || e.Type == billing.EventInvoicePaymentFailed:
		return a.invoice(e)
	}
	return resultIgnored, nil
}

// A ref is what the metadata of a session or subscription says it is for.
type ref struct{ site, account, row int64 }

// refOf reads the metadata the network gave a Checkout Session (copied to
// its subscription). clientRef, when given, must agree.
func refOf(md map[string]string, clientRef string) (ref, bool) {
	var r ref
	var err error
	for _, f := range []struct {
		key string
		dst *int64
	}{{"site_id", &r.site}, {"account_id", &r.account}, {"subscription", &r.row}} {
		if *f.dst, err = strconv.ParseInt(md[f.key], 10, 64); err != nil || *f.dst < 1 {
			return ref{}, false
		}
	}
	if clientRef != "" && clientRef != "site:"+strconv.FormatInt(r.site, 10) {
		return ref{}, false
	}
	return r, true
}

// lock reads a subscription row for update; nil if there is none.
func (a *applyRun) lock(where string, arg any) (*Subscription, error) {
	rows, err := a.tx.Query(a.ctx, `SELECT `+subCols+` FROM subscriptions WHERE `+where+` FOR UPDATE`, arg)
	if err != nil {
		return nil, err
	}
	s, err := pgx.CollectExactlyOneRow(rows, pgx.RowToAddrOfStructByName[Subscription])
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return s, err
}

// find returns the row of a Stripe subscription, linking it first through
// the metadata when it is the first event about it. note is not empty when
// the event cannot be applied to a site as expected.
func (a *applyRun) find(stripeID, customer string, md map[string]string, clientRef string) (row *Subscription, note string, err error) {
	if row, err = a.lock("stripe_subscription_id = $1", stripeID); err != nil || row != nil {
		return row, "", err
	}
	r, ok := refOf(md, clientRef)
	if !ok {
		return nil, unmapped("no site of this network in its metadata"), nil
	}
	if row, err = a.lock("id = $1", r.row); err != nil {
		return nil, "", err
	}
	if row == nil || row.AccountID != r.account {
		return nil, unmapped("its metadata names no subscription of this network"), nil
	}
	return a.link(row, stripeID, customer, r)
}

// link records the Stripe subscription and customer of a row and attaches
// the site the Checkout was for.
func (a *applyRun) link(row *Subscription, stripeID, customer string, r ref) (*Subscription, string, error) {
	ctx, tx := a.ctx, a.tx
	note := ""
	switch row.StripeSubscriptionID {
	case stripeID:
	case "":
		if _, err := tx.Exec(ctx, `UPDATE subscriptions SET stripe_subscription_id = $2, checkout_site_id = NULL, updated_at = now()
			WHERE id = $1`, row.ID, stripeID); err != nil {
			return nil, "", err
		}
		row.StripeSubscriptionID = stripeID
	default:
		// The same Checkout paid twice (two tabs): keep track of the second
		// subscription, attached to no site, for the operator to refund.
		var id int64
		if err := tx.QueryRow(ctx, `INSERT INTO subscriptions (account_id, plan, stripe_subscription_id) VALUES ($1, $2, $3) RETURNING id`,
			row.AccountID, row.Plan, stripeID).Scan(&id); err != nil {
			return nil, "", err
		}
		dup, err := a.lock("id = $1", id)
		if err != nil {
			return nil, "", err
		}
		return a.withCustomer(dup, customer, unmapped(fmt.Sprintf("a second subscription for site %d, attached to no site", r.site)))
	}
	if customer != "" {
		if _, err := tx.Exec(ctx, `INSERT INTO account_billing (account_id, stripe_customer_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			row.AccountID, customer); err != nil {
			return nil, "", err
		}
	}
	tag, err := tx.Exec(ctx, `UPDATE sites s SET subscription_id = $2, updated_at = now()
		WHERE s.id = $1 AND s.kind = 'tenant' AND s.owner_id = $3 AND s.status <> 'deleted'
			AND (s.subscription_id IS NULL OR s.subscription_id = $2
				OR (SELECT o.state = 'canceled' OR o.manual FROM subscriptions o WHERE o.id = s.subscription_id))
			AND (SELECT count(*) FROM sites x WHERE x.subscription_id = $2 AND x.id <> s.id)
				< (SELECT p.sites_per_grant FROM plans p WHERE p.slug = $4)`, r.site, row.ID, row.AccountID, row.Plan)
	if err != nil {
		return nil, "", err
	}
	if tag.RowsAffected() == 0 {
		note = unmapped(fmt.Sprintf("site %d could not be attached: it is gone, has another subscription, or the plan's sites are taken", r.site))
	}
	a.changed = true
	return a.withCustomer(row, customer, note)
}

func (a *applyRun) withCustomer(row *Subscription, customer, note string) (*Subscription, string, error) {
	if customer != "" && row.StripeCustomerID != customer {
		if _, err := a.tx.Exec(a.ctx, `UPDATE subscriptions SET stripe_customer_id = $2 WHERE id = $1`, row.ID, customer); err != nil {
			return nil, "", err
		}
		row.StripeCustomerID = customer
	}
	return row, note, nil
}

func (a *applyRun) checkout(e *billing.Event) (string, error) {
	cs, err := e.CheckoutSession()
	if err != nil {
		return unmapped("the session cannot be read"), nil
	}
	if cs.Mode != "subscription" || cs.Subscription == "" {
		return unmapped("not a subscription Checkout"), nil
	}
	row, note, err := a.find(cs.Subscription, cs.Customer, cs.Metadata, cs.ClientReferenceID)
	if err != nil || row == nil {
		return note, err
	}
	return a.sync(row, note)
}

func (a *applyRun) subscription(e *billing.Event) (string, error) {
	s, err := e.Subscription()
	if err != nil || s.ID == "" {
		return unmapped("the subscription cannot be read"), nil
	}
	row, note, err := a.find(s.ID, s.Customer, s.Metadata, "")
	if err != nil || row == nil {
		return note, err
	}
	return a.sync(row, note)
}

func (a *applyRun) invoice(e *billing.Event) (string, error) {
	inv, err := e.Invoice()
	if err != nil {
		return unmapped("the invoice cannot be read"), nil
	}
	if inv.Subscription == "" {
		return unmapped("not the invoice of a subscription"), nil
	}
	row, note, err := a.find(inv.Subscription, inv.Customer, inv.SubscriptionMetadata, "")
	if err != nil || row == nil {
		return note, err
	}
	result, err := a.sync(row, note)
	if err == nil && e.Type == billing.EventInvoicePaid {
		err = a.paid(row, inv)
	}
	// A failure Stripe has recovered from since (a late delivery) is not
	// news.
	if err != nil || e.Type != billing.EventInvoicePaymentFailed || a.state != string(billing.StateGrace) {
		return result, err
	}
	// Once per grace period: Smart Retries send this event again for
	// every attempt that fails.
	var first bool
	err = a.tx.QueryRow(a.ctx, `UPDATE subscriptions SET failure_noticed_for = grace_since
		WHERE id = $1 AND grace_since IS NOT NULL AND failure_noticed_for IS DISTINCT FROM grace_since RETURNING true`, row.ID).Scan(&first)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, nil
	} else if err != nil {
		return "", err
	}
	// The owner is told, with a link to the portal's "My sites", where
	// "Manage billing" opens a fresh Customer Portal session.
	rows, err := a.tx.Query(a.ctx, `SELECT s.id, s.slug, coalesce((SELECT d.host FROM domains d WHERE d.site_id = s.id AND d.is_primary), '')
		FROM sites s WHERE s.subscription_id = $1 AND s.status <> 'deleted' ORDER BY s.id`, row.ID)
	if err != nil {
		return "", err
	}
	notices, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (BillingNotice, error) {
		nt := BillingNotice{Kind: NoticePaymentFailed, AccountID: row.AccountID, GraceDays: a.b.stripe.GraceDays}
		return nt, r.Scan(&nt.SiteID, &nt.Slug, &nt.Host)
	})
	if err != nil {
		return "", err
	}
	if len(notices) == 0 {
		notices = []BillingNotice{{Kind: NoticePaymentFailed, AccountID: row.AccountID, GraceDays: a.b.stripe.GraceDays}}
	}
	a.notices = append(a.notices, notices...)
	return result, nil
}

// paid records the period a paid invoice paid for: its lines' service
// period, or else the subscription's current period. An invoice that
// charged nothing (a trial's) pays for no time.
func (a *applyRun) paid(row *Subscription, inv *billing.Invoice) error {
	if inv.Status != "paid" || inv.Total <= 0 {
		return nil
	}
	end := inv.ServicePeriodEnd
	if end.IsZero() && a.periodEnd != nil {
		end = *a.periodEnd
	}
	if end.IsZero() {
		return nil
	}
	_, err := a.tx.Exec(a.ctx, `UPDATE subscriptions SET paid_until = greatest(paid_until, $2) WHERE id = $1`, row.ID, end)
	return err
}

// sync fetches the subscription from Stripe, the source of truth, and
// stores its state. Grace starts the first time it is seen and ends when
// the subscription is active again; an active subscription gives its plan
// to the sites it pays for (a switch in the portal, a downgrade included:
// what is over the new limits stays, only additions are refused).
func (a *applyRun) sync(row *Subscription, note string) (string, error) {
	s, err := a.b.client.GetSubscription(a.ctx, row.StripeSubscriptionID)
	if err != nil {
		return "", err
	}
	plan := row.Plan
	if p, ok := a.b.plans[s.PriceID()]; ok {
		plan = p
	} else if s.PriceID() != "" && s.PriceID() != row.StripePriceID {
		a.b.log.Warn("a subscription's price is not configured; its plan is kept", "subscription", row.ID, "price", s.PriceID(), "plan", plan)
	}
	state := string(billing.StateFromStatus(s.Status))
	a.state = state
	var periodEnd *time.Time
	if !s.CurrentPeriodEnd.IsZero() {
		periodEnd = &s.CurrentPeriodEnd
	}
	a.periodEnd = periodEnd
	if _, err := a.tx.Exec(a.ctx, `UPDATE subscriptions SET plan = $2, stripe_price_id = $3,
			stripe_customer_id = CASE WHEN $4 = '' THEN stripe_customer_id ELSE $4 END, status = $5, state = $6,
			grace_since = CASE WHEN $6 = 'grace' THEN coalesce(grace_since, $7) WHEN $6 = 'active' THEN NULL ELSE grace_since END,
			current_period_end = $8, cancel_at_period_end = $9, synced_at = $7, updated_at = now()
		WHERE id = $1`, row.ID, plan, clip(s.PriceID(), 255), validCustomer(s.Customer), clip(s.Status, 40), state, a.b.now(),
		periodEnd, s.CancelAtPeriodEnd); err != nil {
		return "", err
	}
	if state == string(billing.StateActive) {
		if _, err := a.tx.Exec(a.ctx, `UPDATE sites SET plan = $2, updated_at = now()
			WHERE subscription_id = $1 AND kind = 'tenant' AND plan IS DISTINCT FROM $2`, row.ID, plan); err != nil {
			return "", err
		}
	}
	a.changed = true
	if note != "" {
		return note, nil
	}
	return resultApplied, nil
}

func validCustomer(c string) string {
	if strings.HasPrefix(c, "cus_") && len(c) <= 255 {
		return c
	}
	return ""
}

// clip shortens s to at most n bytes without splitting a rune.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// --------------------------------------------------------- admin views --

// A BillingEvent is a stored webhook event, for the network admin area.
type BillingEvent struct {
	ID            string     `db:"id"`
	Type          string     `db:"type"`
	Created       time.Time  `db:"created"`
	Livemode      bool       `db:"livemode"`
	ObjectID      string     `db:"object_id"`
	ReceivedAt    time.Time  `db:"received_at"`
	ProcessedAt   *time.Time `db:"processed_at"`
	Attempts      int        `db:"attempts"`
	Result        string     `db:"result"`
	NextAttemptAt time.Time  `db:"next_attempt_at"`
}

// GaveUp reports whether the processor stopped retrying the event.
func (e *BillingEvent) GaveUp() bool { return e.ProcessedAt == nil && e.NextAttemptAt.Year() > 9000 }

const problemWhere = `(processed_at IS NULL AND result <> '') OR result LIKE 'unmapped%'`

// BillingProblems lists the events that failed (and are retried or given
// up) or could not be mapped to a site, newest first, and their total.
func (n *Network) BillingProblems(ctx context.Context, limit, offset int) ([]BillingEvent, int, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var total int
	if err := n.db.QueryRow(ctx, `SELECT count(*) FROM billing_events WHERE `+problemWhere).Scan(&total); err != nil {
		return nil, 0, err
	}
	// ±infinity (due at once, given up) do not fit a time.Time.
	rows, err := n.db.Query(ctx, `SELECT id, type, created, livemode, object_id, received_at, processed_at, attempts, result,
		greatest(least(next_attempt_at, '9999-12-31 00:00:00+00'), '1970-01-01 00:00:00+00') AS next_attempt_at
		FROM billing_events WHERE `+problemWhere+` ORDER BY received_at DESC, id LIMIT $1 OFFSET $2`, limit, max(offset, 0))
	if err != nil {
		return nil, 0, err
	}
	evs, err := pgx.CollectRows(rows, pgx.RowToStructByName[BillingEvent])
	return evs, total, err
}

// FailingBillingEvents counts the events that failed and are not
// processed: what the network admin menu shows.
func (n *Network) FailingBillingEvents(ctx context.Context) (int, error) {
	var c int
	err := n.db.QueryRow(ctx, `SELECT count(*) FROM billing_events WHERE processed_at IS NULL AND result <> ''`).Scan(&c)
	return c, err
}

// RetryBillingEvent makes a failed or unmapped event due again, as new.
func (n *Network) RetryBillingEvent(ctx context.Context, id string) error {
	tag, err := n.db.Exec(ctx, `UPDATE billing_events SET processed_at = NULL, attempts = 0, result = '', next_attempt_at = '-infinity'
		WHERE id = $1 AND (`+problemWhere+`)`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return err
}

// PurgeBillingEvents deletes the processed events older than billingKeep,
// except the unmapped ones, which wait for a network admin.
func (n *Network) PurgeBillingEvents(ctx context.Context) (int64, error) {
	tag, err := n.db.Exec(ctx, `DELETE FROM billing_events WHERE processed_at < now() - $1::bigint * interval '1 second'
		AND result NOT LIKE 'unmapped%'`, int64(billingKeep/time.Second))
	return tag.RowsAffected(), err
}
