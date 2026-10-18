package billing

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

// This file holds the exported, trimmed-down views of Stripe objects and the
// raw shapes they are decoded from. Only fields the hosting layer needs are
// kept; everything is decoded leniently so new Stripe fields never break us.

// Address is a postal address as Stripe stores it.
type Address struct {
	Line1      string `json:"line1"`
	Line2      string `json:"line2"`
	PostalCode string `json:"postal_code"`
	City       string `json:"city"`
	State      string `json:"state"`
	Country    string `json:"country"` // ISO 3166-1 alpha-2
}

// TaxID is a customer tax ID collected by Checkout (e.g. type eu_vat).
type TaxID struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// CheckoutSession is a Checkout Session in subscription mode.
type CheckoutSession struct {
	ID string
	// URL is the hosted page to redirect the customer to. Only present
	// while the session is open.
	URL string
	// Customer and Subscription are Stripe IDs (cus_..., sub_...). Both are
	// set once the session is complete.
	Customer     string
	Subscription string
	// Status is open, complete or expired.
	Status string
	// PaymentStatus is paid, unpaid or no_payment_required.
	PaymentStatus     string
	ClientReferenceID string
	Metadata          map[string]string
	Mode              string
	CustomerEmail     string // customer_details.email, falling back to customer_email
	CustomerName      string
	CustomerAddress   *Address
	TaxIDs            []TaxID
	Invoice           string
	AmountTotal       int64
	Currency          string
	Livemode          bool
	Created           time.Time
	ExpiresAt         time.Time
}

type rawCheckoutSession struct {
	ID                string            `json:"id"`
	Object            string            `json:"object"`
	URL               string            `json:"url"`
	Customer          expandable        `json:"customer"`
	Subscription      expandable        `json:"subscription"`
	Invoice           expandable        `json:"invoice"`
	Status            string            `json:"status"`
	PaymentStatus     string            `json:"payment_status"`
	ClientReferenceID string            `json:"client_reference_id"`
	Metadata          map[string]string `json:"metadata"`
	Mode              string            `json:"mode"`
	CustomerEmail     string            `json:"customer_email"`
	CustomerDetails   *struct {
		Email   string   `json:"email"`
		Name    string   `json:"name"`
		Address *Address `json:"address"`
		TaxIDs  []TaxID  `json:"tax_ids"`
	} `json:"customer_details"`
	AmountTotal int64  `json:"amount_total"`
	Currency    string `json:"currency"`
	Livemode    bool   `json:"livemode"`
	Created     int64  `json:"created"`
	ExpiresAt   int64  `json:"expires_at"`
}

func (r *rawCheckoutSession) export() *CheckoutSession {
	s := &CheckoutSession{
		ID:                r.ID,
		URL:               r.URL,
		Customer:          r.Customer.ID,
		Subscription:      r.Subscription.ID,
		Status:            r.Status,
		PaymentStatus:     r.PaymentStatus,
		ClientReferenceID: r.ClientReferenceID,
		Metadata:          r.Metadata,
		Mode:              r.Mode,
		CustomerEmail:     r.CustomerEmail,
		Invoice:           r.Invoice.ID,
		AmountTotal:       r.AmountTotal,
		Currency:          r.Currency,
		Livemode:          r.Livemode,
		Created:           unixTime(r.Created),
		ExpiresAt:         unixTime(r.ExpiresAt),
	}
	if d := r.CustomerDetails; d != nil {
		if d.Email != "" {
			s.CustomerEmail = d.Email
		}
		s.CustomerName = d.Name
		s.CustomerAddress = d.Address
		s.TaxIDs = d.TaxIDs
	}
	return s
}

// Subscription is a Stripe Billing subscription.
type Subscription struct {
	ID       string
	Customer string
	// Status is Stripe's raw status: incomplete, incomplete_expired,
	// trialing, active, past_due, canceled, unpaid or paused. Use State()
	// for the hosting view.
	Status            string
	CancelAtPeriodEnd bool
	CancelAt          time.Time
	CanceledAt        time.Time
	EndedAt           time.Time
	TrialStart        time.Time
	TrialEnd          time.Time
	StartDate         time.Time
	Created           time.Time
	// CurrentPeriodStart/End are taken from the subscription items (where
	// Stripe has kept them since 2025-03-31.basil): the latest end across
	// items, which for a single-price plan is simply that item's period.
	CurrentPeriodStart time.Time
	CurrentPeriodEnd   time.Time
	Items              []SubscriptionItem
	Metadata           map[string]string
	LatestInvoice      string
	Currency           string
	// CancellationReason is cancellation_details.reason
	// (cancellation_requested, payment_failed, payment_disputed, ...).
	CancellationReason string
	Livemode           bool
}

// SubscriptionItem is one price on a subscription.
type SubscriptionItem struct {
	ID                 string
	PriceID            string
	ProductID          string
	LookupKey          string
	Quantity           int64
	CurrentPeriodStart time.Time
	CurrentPeriodEnd   time.Time
}

// State maps the Stripe status to the hosting state.
func (s *Subscription) State() SubscriptionState { return StateFromStatus(s.Status) }

// PriceID returns the price of the first item, which identifies the plan
// for single-price subscriptions.
func (s *Subscription) PriceID() string {
	if len(s.Items) == 0 {
		return ""
	}
	return s.Items[0].PriceID
}

type rawSubscription struct {
	ID                  string            `json:"id"`
	Object              string            `json:"object"`
	Customer            expandable        `json:"customer"`
	Status              string            `json:"status"`
	CancelAtPeriodEnd   bool              `json:"cancel_at_period_end"`
	CancelAt            int64             `json:"cancel_at"`
	CanceledAt          int64             `json:"canceled_at"`
	EndedAt             int64             `json:"ended_at"`
	TrialStart          int64             `json:"trial_start"`
	TrialEnd            int64             `json:"trial_end"`
	StartDate           int64             `json:"start_date"`
	Created             int64             `json:"created"`
	Currency            string            `json:"currency"`
	Livemode            bool              `json:"livemode"`
	Metadata            map[string]string `json:"metadata"`
	LatestInvoice       expandable        `json:"latest_invoice"`
	CancellationDetails *struct {
		Reason string `json:"reason"`
	} `json:"cancellation_details"`
	Items struct {
		Data []struct {
			ID                 string `json:"id"`
			Quantity           int64  `json:"quantity"`
			CurrentPeriodStart int64  `json:"current_period_start"`
			CurrentPeriodEnd   int64  `json:"current_period_end"`
			Price              struct {
				ID        string     `json:"id"`
				Product   expandable `json:"product"`
				LookupKey string     `json:"lookup_key"`
			} `json:"price"`
		} `json:"data"`
	} `json:"items"`
	// Pre-basil API versions had the period on the subscription itself.
	LegacyPeriodStart int64 `json:"current_period_start"`
	LegacyPeriodEnd   int64 `json:"current_period_end"`
}

func (r *rawSubscription) export() *Subscription {
	s := &Subscription{
		ID:                r.ID,
		Customer:          r.Customer.ID,
		Status:            r.Status,
		CancelAtPeriodEnd: r.CancelAtPeriodEnd,
		CancelAt:          unixTime(r.CancelAt),
		CanceledAt:        unixTime(r.CanceledAt),
		EndedAt:           unixTime(r.EndedAt),
		TrialStart:        unixTime(r.TrialStart),
		TrialEnd:          unixTime(r.TrialEnd),
		StartDate:         unixTime(r.StartDate),
		Created:           unixTime(r.Created),
		Metadata:          r.Metadata,
		LatestInvoice:     r.LatestInvoice.ID,
		Currency:          r.Currency,
		Livemode:          r.Livemode,
	}
	if r.CancellationDetails != nil {
		s.CancellationReason = r.CancellationDetails.Reason
	}
	var start, end int64
	for _, it := range r.Items.Data {
		s.Items = append(s.Items, SubscriptionItem{
			ID:                 it.ID,
			PriceID:            it.Price.ID,
			ProductID:          it.Price.Product.ID,
			LookupKey:          it.Price.LookupKey,
			Quantity:           it.Quantity,
			CurrentPeriodStart: unixTime(it.CurrentPeriodStart),
			CurrentPeriodEnd:   unixTime(it.CurrentPeriodEnd),
		})
		if it.CurrentPeriodEnd > end {
			start, end = it.CurrentPeriodStart, it.CurrentPeriodEnd
		}
	}
	if end == 0 {
		start, end = r.LegacyPeriodStart, r.LegacyPeriodEnd
	}
	s.CurrentPeriodStart, s.CurrentPeriodEnd = unixTime(start), unixTime(end)
	return s
}

// Customer is a Stripe customer.
type Customer struct {
	ID       string
	Email    string
	Name     string
	Metadata map[string]string
	Address  *Address
	// TaxExempt is none, exempt or reverse.
	TaxExempt string
	// Deleted is true when the customer was deleted in Stripe; only ID is
	// meaningful then.
	Deleted  bool
	Livemode bool
	Created  time.Time
}

type rawCustomer struct {
	ID        string            `json:"id"`
	Object    string            `json:"object"`
	Email     string            `json:"email"`
	Name      string            `json:"name"`
	Metadata  map[string]string `json:"metadata"`
	Address   *Address          `json:"address"`
	TaxExempt string            `json:"tax_exempt"`
	Deleted   bool              `json:"deleted"`
	Livemode  bool              `json:"livemode"`
	Created   int64             `json:"created"`
}

func (r *rawCustomer) export() *Customer {
	return &Customer{
		ID:        r.ID,
		Email:     r.Email,
		Name:      r.Name,
		Metadata:  r.Metadata,
		Address:   r.Address,
		TaxExempt: r.TaxExempt,
		Deleted:   r.Deleted,
		Livemode:  r.Livemode,
		Created:   unixTime(r.Created),
	}
}

// Price is a recurring price, as shown in the admin price picker.
type Price struct {
	ID          string
	Product     string // prod_...
	ProductName string
	Nickname    string
	LookupKey   string
	// UnitAmount is in the currency's smallest unit (cents). It is 0 for
	// tiered prices (BillingScheme "tiered"), which this integration does
	// not use.
	UnitAmount    int64
	Currency      string // lowercase ISO 4217, e.g. "eur"
	Interval      string // day, week, month or year
	IntervalCount int64
	BillingScheme string
	// TaxBehavior is inclusive, exclusive or unspecified.
	TaxBehavior string
	Active      bool
	Livemode    bool
}

type rawPrice struct {
	ID            string     `json:"id"`
	Object        string     `json:"object"`
	Product       expandable `json:"product"`
	Nickname      string     `json:"nickname"`
	LookupKey     string     `json:"lookup_key"`
	UnitAmount    int64      `json:"unit_amount"`
	Currency      string     `json:"currency"`
	BillingScheme string     `json:"billing_scheme"`
	TaxBehavior   string     `json:"tax_behavior"`
	Active        bool       `json:"active"`
	Livemode      bool       `json:"livemode"`
	Recurring     *struct {
		Interval      string `json:"interval"`
		IntervalCount int64  `json:"interval_count"`
	} `json:"recurring"`
}

func (r *rawPrice) export() Price {
	p := Price{
		ID:            r.ID,
		Product:       r.Product.ID,
		Nickname:      r.Nickname,
		LookupKey:     r.LookupKey,
		UnitAmount:    r.UnitAmount,
		Currency:      r.Currency,
		BillingScheme: r.BillingScheme,
		TaxBehavior:   r.TaxBehavior,
		Active:        r.Active,
		Livemode:      r.Livemode,
	}
	if r.Recurring != nil {
		p.Interval, p.IntervalCount = r.Recurring.Interval, r.Recurring.IntervalCount
	}
	if r.Product.Raw != nil {
		var prod struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(r.Product.Raw, &prod) == nil {
			p.ProductName = prod.Name
		}
	}
	return p
}

// Invoice is the subset of a Stripe invoice used by invoice.paid and
// invoice.payment_failed handling.
type Invoice struct {
	ID            string
	Customer      string
	CustomerEmail string
	// Subscription is parent.subscription_details.subscription (or the
	// legacy top-level field on old API versions). Empty for one-off
	// invoices.
	Subscription         string
	SubscriptionMetadata map[string]string
	// Status is draft, open, paid, uncollectible or void.
	Status string
	// BillingReason is subscription_create, subscription_cycle,
	// subscription_update, manual, ...
	BillingReason   string
	AmountDue       int64
	AmountPaid      int64
	AmountRemaining int64
	Total           int64
	Currency        string
	// AttemptCount is the number of payment attempts so far; with
	// NextPaymentAttempt (zero when Stripe stopped retrying) it tells how
	// far into dunning a failed payment is.
	AttemptCount       int64
	NextPaymentAttempt time.Time
	PaidAt             time.Time
	HostedInvoiceURL   string
	InvoicePDF         string
	Number             string
	// ServicePeriodStart/End span the line items' periods: for a renewal
	// invoice this is the subscription period being paid for.
	ServicePeriodStart time.Time
	ServicePeriodEnd   time.Time
	// PriceIDs lists the prices on the (first page of) line items.
	PriceIDs []string
	Livemode bool
	Created  time.Time
}

type rawInvoice struct {
	ID                 string     `json:"id"`
	Object             string     `json:"object"`
	Customer           expandable `json:"customer"`
	CustomerEmail      string     `json:"customer_email"`
	Status             string     `json:"status"`
	BillingReason      string     `json:"billing_reason"`
	AmountDue          int64      `json:"amount_due"`
	AmountPaid         int64      `json:"amount_paid"`
	AmountRemaining    int64      `json:"amount_remaining"`
	Total              int64      `json:"total"`
	Currency           string     `json:"currency"`
	AttemptCount       int64      `json:"attempt_count"`
	NextPaymentAttempt int64      `json:"next_payment_attempt"`
	HostedInvoiceURL   string     `json:"hosted_invoice_url"`
	InvoicePDF         string     `json:"invoice_pdf"`
	Number             string     `json:"number"`
	Livemode           bool       `json:"livemode"`
	Created            int64      `json:"created"`
	StatusTransitions  struct {
		PaidAt int64 `json:"paid_at"`
	} `json:"status_transitions"`
	Parent *struct {
		Type                string `json:"type"`
		SubscriptionDetails *struct {
			Subscription expandable        `json:"subscription"`
			Metadata     map[string]string `json:"metadata"`
		} `json:"subscription_details"`
	} `json:"parent"`
	LegacySubscription expandable `json:"subscription"`
	Lines              struct {
		Data []struct {
			Period struct {
				Start int64 `json:"start"`
				End   int64 `json:"end"`
			} `json:"period"`
			Pricing *struct {
				PriceDetails *struct {
					Price expandable `json:"price"`
				} `json:"price_details"`
			} `json:"pricing"`
		} `json:"data"`
	} `json:"lines"`
}

func (r *rawInvoice) export() *Invoice {
	inv := &Invoice{
		ID:                 r.ID,
		Customer:           r.Customer.ID,
		CustomerEmail:      r.CustomerEmail,
		Subscription:       r.LegacySubscription.ID,
		Status:             r.Status,
		BillingReason:      r.BillingReason,
		AmountDue:          r.AmountDue,
		AmountPaid:         r.AmountPaid,
		AmountRemaining:    r.AmountRemaining,
		Total:              r.Total,
		Currency:           r.Currency,
		AttemptCount:       r.AttemptCount,
		NextPaymentAttempt: unixTime(r.NextPaymentAttempt),
		PaidAt:             unixTime(r.StatusTransitions.PaidAt),
		HostedInvoiceURL:   r.HostedInvoiceURL,
		InvoicePDF:         r.InvoicePDF,
		Number:             r.Number,
		Livemode:           r.Livemode,
		Created:            unixTime(r.Created),
	}
	if p := r.Parent; p != nil && p.SubscriptionDetails != nil {
		if id := p.SubscriptionDetails.Subscription.ID; id != "" {
			inv.Subscription = id
		}
		inv.SubscriptionMetadata = p.SubscriptionDetails.Metadata
	}
	var start, end int64
	for _, l := range r.Lines.Data {
		if l.Period.Start != 0 && (start == 0 || l.Period.Start < start) {
			start = l.Period.Start
		}
		end = max(end, l.Period.End)
		if l.Pricing != nil && l.Pricing.PriceDetails != nil && l.Pricing.PriceDetails.Price.ID != "" {
			inv.PriceIDs = append(inv.PriceIDs, l.Pricing.PriceDetails.Price.ID)
		}
	}
	inv.ServicePeriodStart, inv.ServicePeriodEnd = unixTime(start), unixTime(end)
	return inv
}

// expandable decodes a field Stripe returns either as an ID string or, when
// expanded, as an object with an "id". Raw keeps the object form.
type expandable struct {
	ID  string
	Raw json.RawMessage
}

func (e *expandable) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	switch {
	case len(b) == 0 || bytes.Equal(b, []byte("null")):
		return nil
	case b[0] == '"':
		return json.Unmarshal(b, &e.ID)
	case b[0] == '{':
		var obj struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(b, &obj); err != nil {
			return err
		}
		e.ID = obj.ID
		e.Raw = append(json.RawMessage(nil), b...)
		return nil
	}
	return fmt.Errorf("billing: expected ID string or object, got %.20s", b)
}

func unixTime(sec int64) time.Time {
	if sec <= 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0).UTC()
}

// decodeObject unmarshals a Stripe object and checks its "object" field.
func decodeObject(data []byte, wantObject string, dst interface{ objectName() string }) error {
	if err := json.Unmarshal(data, dst); err != nil {
		return fmt.Errorf("billing: decoding %s: %w", wantObject, err)
	}
	if got := dst.objectName(); got != "" && got != wantObject {
		return fmt.Errorf("billing: expected %s object, got %q", wantObject, got)
	}
	return nil
}

func (r *rawCheckoutSession) objectName() string { return r.Object }
func (r *rawSubscription) objectName() string    { return r.Object }
func (r *rawInvoice) objectName() string         { return r.Object }
