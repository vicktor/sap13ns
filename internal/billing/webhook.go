package billing

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

const (
	// SignatureHeader is the HTTP header carrying the webhook signature.
	SignatureHeader = "Stripe-Signature"

	// DefaultWebhookTolerance is the maximum age (and clock skew) accepted
	// for a signed webhook, matching Stripe's official libraries.
	DefaultWebhookTolerance = 5 * time.Minute

	// MaxWebhookPayload caps the accepted webhook body size.
	MaxWebhookPayload = 256 << 10

	maxSignatureHeader = 4 << 10
	maxSignatures      = 16
)

// Event types the hosting integration subscribes to.
const (
	EventCheckoutSessionCompleted = "checkout.session.completed"
	EventSubscriptionCreated      = "customer.subscription.created"
	EventSubscriptionUpdated      = "customer.subscription.updated"
	EventSubscriptionDeleted      = "customer.subscription.deleted"
	EventInvoicePaid              = "invoice.paid"
	EventInvoicePaymentFailed     = "invoice.payment_failed"

	// Optional extras: pausing (trial without payment method, on-demand or
	// after payment failures) and the trial reminder.
	EventSubscriptionPaused       = "customer.subscription.paused"
	EventSubscriptionResumed      = "customer.subscription.resumed"
	EventSubscriptionTrialWillEnd = "customer.subscription.trial_will_end"
)

// WebhookEvents is the event list to enable on the webhook endpoint.
var WebhookEvents = []string{
	EventCheckoutSessionCompleted,
	EventSubscriptionCreated,
	EventSubscriptionUpdated,
	EventSubscriptionDeleted,
	EventInvoicePaid,
	EventInvoicePaymentFailed,
}

var (
	ErrWebhookPayloadTooLarge = errors.New("billing: webhook payload too large")
	ErrWebhookNoSecret        = errors.New("billing: no webhook signing secret configured")
	ErrWebhookHeader          = errors.New("billing: malformed Stripe-Signature header")
	ErrWebhookNoSignature     = errors.New("billing: Stripe-Signature has no v1 signature")
	ErrWebhookSignature       = errors.New("billing: webhook signature mismatch")
	ErrWebhookTimestamp       = errors.New("billing: webhook timestamp outside tolerance")
	ErrWebhookPayload         = errors.New("billing: invalid webhook payload")
	ErrThinEvent              = errors.New("billing: thin event payloads are not supported; configure the endpoint with snapshot payloads")
	ErrEventType              = errors.New("billing: event type does not match the requested object")
)

// Event is a verified Stripe snapshot event.
type Event struct {
	ID         string // evt_..., the deduplication key
	Type       string // e.g. customer.subscription.updated
	Created    time.Time
	Livemode   bool
	APIVersion string // version used to render Data
	Account    string // connected account, empty for the platform
	// Data is data.object: a snapshot of the object when the event fired.
	Data json.RawMessage
	// PreviousAttributes is data.previous_attributes on *.updated events.
	PreviousAttributes json.RawMessage
}

// ReadWebhookBody reads a webhook request body, enforcing MaxWebhookPayload.
// Pass the bytes unmodified to VerifyWebhook.
func ReadWebhookBody(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, MaxWebhookPayload+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxWebhookPayload {
		return nil, ErrWebhookPayloadTooLarge
	}
	return b, nil
}

// VerifyWebhook checks the Stripe-Signature header of payload against the
// endpoint signing secret (whsec_...) and decodes the event.
//
// The signed message is "<t>.<payload>", HMAC-SHA256 keyed with the secret;
// any v1 signature in the header may match (Stripe sends one per active
// secret while a secret is being rolled), v0 and unknown schemes are
// ignored. tolerance <= 0 means DefaultWebhookTolerance (a zero tolerance
// would disable replay protection, so it is never honoured); a zero now
// means time.Now(). Timestamps older than tolerance, or further than
// tolerance in the future, are rejected with ErrWebhookTimestamp, which is
// only returned for otherwise authentic payloads.
func VerifyWebhook(payload []byte, sigHeader, secret string, tolerance time.Duration, now time.Time) (*Event, error) {
	return VerifyWebhookWithSecrets(payload, sigHeader, []string{secret}, tolerance, now)
}

// VerifyWebhookWithSecrets is VerifyWebhook accepting several secrets, for
// rotation: configure both the new and the old secret until the old one
// expires.
func VerifyWebhookWithSecrets(payload []byte, sigHeader string, secrets []string, tolerance time.Duration, now time.Time) (*Event, error) {
	if len(payload) > MaxWebhookPayload {
		return nil, ErrWebhookPayloadTooLarge
	}
	var keys [][]byte
	for _, s := range secrets {
		if s = strings.TrimSpace(s); s != "" {
			keys = append(keys, []byte(s))
		}
	}
	if len(keys) == 0 {
		return nil, ErrWebhookNoSecret
	}
	ts, sigs, err := parseSignatureHeader(sigHeader)
	if err != nil {
		return nil, err
	}

	matched := false
	for _, key := range keys {
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(strconv.FormatInt(ts, 10)))
		mac.Write([]byte{'.'})
		mac.Write(payload)
		expected := mac.Sum(nil)
		for _, sig := range sigs {
			if hmac.Equal(expected, sig) {
				matched = true
			}
		}
	}
	if !matched {
		return nil, ErrWebhookSignature
	}

	if tolerance <= 0 {
		tolerance = DefaultWebhookTolerance
	}
	if now.IsZero() {
		now = time.Now()
	}
	signedAt := time.Unix(ts, 0)
	if age := now.Sub(signedAt); age > tolerance || age < -tolerance {
		return nil, fmt.Errorf("%w: signed at %s, now %s", ErrWebhookTimestamp,
			signedAt.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339))
	}

	return parseEvent(payload)
}

// parseSignatureHeader extracts t and the v1 signatures from
// "t=1492774577,v1=5257a8...,v0=6ffbb5...".
func parseSignatureHeader(h string) (int64, [][]byte, error) {
	if h == "" {
		return 0, nil, fmt.Errorf("%w: missing", ErrWebhookHeader)
	}
	if len(h) > maxSignatureHeader {
		return 0, nil, fmt.Errorf("%w: too long", ErrWebhookHeader)
	}
	var (
		ts    int64
		hasTS bool
		sigs  [][]byte
	)
	for item := range strings.SplitSeq(h, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(item), "=")
		if !ok {
			continue
		}
		switch k {
		case "t":
			if hasTS {
				return 0, nil, fmt.Errorf("%w: duplicate timestamp", ErrWebhookHeader)
			}
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n <= 0 {
				return 0, nil, fmt.Errorf("%w: bad timestamp", ErrWebhookHeader)
			}
			ts, hasTS = n, true
		case "v1":
			if len(sigs) >= maxSignatures {
				continue
			}
			b, err := hex.DecodeString(v)
			if err != nil || len(b) != sha256.Size {
				continue // not a well-formed v1 signature; cannot match
			}
			sigs = append(sigs, b)
		}
	}
	if !hasTS {
		return 0, nil, fmt.Errorf("%w: missing timestamp", ErrWebhookHeader)
	}
	if len(sigs) == 0 {
		return 0, nil, ErrWebhookNoSignature
	}
	return ts, sigs, nil
}

func parseEvent(payload []byte) (*Event, error) {
	var raw struct {
		ID         string `json:"id"`
		Object     string `json:"object"`
		Type       string `json:"type"`
		APIVersion string `json:"api_version"`
		Account    string `json:"account"`
		Created    int64  `json:"created"`
		Livemode   bool   `json:"livemode"`
		Data       struct {
			Object             json.RawMessage `json:"object"`
			PreviousAttributes json.RawMessage `json:"previous_attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrWebhookPayload, err)
	}
	if strings.HasPrefix(raw.Object, "v2.") {
		return nil, ErrThinEvent
	}
	if raw.Object != "event" || raw.ID == "" || raw.Type == "" {
		return nil, fmt.Errorf("%w: not a Stripe event", ErrWebhookPayload)
	}
	if len(raw.Data.Object) == 0 || raw.Data.Object[0] != '{' {
		return nil, fmt.Errorf("%w: missing data.object", ErrWebhookPayload)
	}
	ev := &Event{
		ID:         raw.ID,
		Type:       raw.Type,
		Created:    unixTime(raw.Created),
		Livemode:   raw.Livemode,
		APIVersion: raw.APIVersion,
		Account:    raw.Account,
		Data:       raw.Data.Object,
	}
	if p := raw.Data.PreviousAttributes; len(p) > 0 && string(p) != "null" {
		ev.PreviousAttributes = p
	}
	return ev, nil
}

// SignPayload returns a Stripe-Signature header value for payload, signed
// at t with secret. It exists for tests of webhook handlers.
func SignPayload(payload []byte, secret string, t time.Time) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", t.Unix())
	mac.Write(payload)
	return fmt.Sprintf("t=%d,v1=%s", t.Unix(), hex.EncodeToString(mac.Sum(nil)))
}

// ObjectID returns data.object.id, for the secondary deduplication Stripe
// recommends (event type + object ID) when two distinct events describe the
// same change.
func (e *Event) ObjectID() string {
	var o struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(e.Data, &o)
	return o.ID
}

// CheckoutSession decodes data.object of a checkout.session.* event.
func (e *Event) CheckoutSession() (*CheckoutSession, error) {
	if !strings.HasPrefix(e.Type, "checkout.session.") {
		return nil, fmt.Errorf("%w: %s is not a checkout.session event", ErrEventType, e.Type)
	}
	var raw rawCheckoutSession
	if err := decodeObject(e.Data, "checkout.session", &raw); err != nil {
		return nil, err
	}
	return raw.export(), nil
}

// Subscription decodes data.object of a customer.subscription.* event.
func (e *Event) Subscription() (*Subscription, error) {
	if !strings.HasPrefix(e.Type, "customer.subscription.") {
		return nil, fmt.Errorf("%w: %s is not a customer.subscription event", ErrEventType, e.Type)
	}
	var raw rawSubscription
	if err := decodeObject(e.Data, "subscription", &raw); err != nil {
		return nil, err
	}
	return raw.export(), nil
}

// Invoice decodes data.object of an invoice.* event (invoice.paid,
// invoice.payment_failed, ...).
func (e *Event) Invoice() (*Invoice, error) {
	if !strings.HasPrefix(e.Type, "invoice.") {
		return nil, fmt.Errorf("%w: %s is not an invoice event", ErrEventType, e.Type)
	}
	var raw rawInvoice
	if err := decodeObject(e.Data, "invoice", &raw); err != nil {
		return nil, err
	}
	return raw.export(), nil
}
