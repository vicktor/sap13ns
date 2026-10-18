package billing

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Fake signing secrets; real ones come from the Stripe Dashboard or CLI.
const (
	testSecret    = "whsec_test_DUMMYsecretForUnitTests0000"
	testOldSecret = "whsec_test_DUMMYoldSecretForRotation00"
)

// fixtureTime is shortly after every fixture's creation.
var fixtureTime = time.Unix(1790813100, 0)

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func v1(payload []byte, secret string, ts int64) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.%s", ts, payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyWebhookValid(t *testing.T) {
	payload := loadFixture(t, "checkout_session_completed.json")
	header := SignPayload(payload, testSecret, fixtureTime)
	ev, err := VerifyWebhook(payload, header, testSecret, 0, fixtureTime.Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if ev.ID != "evt_1TestCheckoutCompleted0001" || ev.Type != EventCheckoutSessionCompleted ||
		ev.APIVersion != DefaultAPIVersion || ev.Livemode || ev.Created.Unix() != 1790813012 {
		t.Errorf("unexpected event: %+v", ev)
	}
	if !bytes.HasPrefix(ev.Data, []byte("{")) || ev.ObjectID() != "cs_test_a1TestSessionCompleted0000000000000000000000000000" {
		t.Errorf("data.object not captured: %s", ev.ObjectID())
	}
	if ev.PreviousAttributes != nil {
		t.Errorf("unexpected previous attributes: %s", ev.PreviousAttributes)
	}
}

func TestVerifyWebhookManualHeader(t *testing.T) {
	// Built by hand to pin the scheme: HMAC-SHA256(secret, "<t>.<payload>").
	payload := []byte(`{"id":"evt_1","object":"event","type":"invoice.paid","created":1,"data":{"object":{"id":"in_1","object":"invoice"}}}`)
	ts := fixtureTime.Unix()
	header := fmt.Sprintf(" t=%d , v1=%s ", ts, v1(payload, testSecret, ts))
	if _, err := VerifyWebhook(payload, header, testSecret, time.Minute, fixtureTime); err != nil {
		t.Fatal(err)
	}
	if got := SignPayload(payload, testSecret, fixtureTime); got != fmt.Sprintf("t=%d,v1=%s", ts, v1(payload, testSecret, ts)) {
		t.Errorf("SignPayload = %s", got)
	}
}

func TestVerifyWebhookTampered(t *testing.T) {
	payload := loadFixture(t, "invoice_paid.json")
	header := SignPayload(payload, testSecret, fixtureTime)
	tampered := bytes.Replace(payload, []byte(`"amount_paid": 2299`), []byte(`"amount_paid": 9999`), 1)
	if bytes.Equal(tampered, payload) {
		t.Fatal("fixture did not change")
	}
	if _, err := VerifyWebhook(tampered, header, testSecret, 0, fixtureTime); !errors.Is(err, ErrWebhookSignature) {
		t.Errorf("tampered payload: %v", err)
	}
	// Changing the timestamp in the header invalidates the signature too.
	forged := strings.Replace(header, fmt.Sprintf("t=%d", fixtureTime.Unix()), fmt.Sprintf("t=%d", fixtureTime.Unix()+60), 1)
	if _, err := VerifyWebhook(payload, forged, testSecret, 0, fixtureTime); !errors.Is(err, ErrWebhookSignature) {
		t.Errorf("forged timestamp: %v", err)
	}
	if _, err := VerifyWebhook(payload, header, "whsec_wrong", 0, fixtureTime); !errors.Is(err, ErrWebhookSignature) {
		t.Errorf("wrong secret: %v", err)
	}
}

func TestVerifyWebhookTolerance(t *testing.T) {
	payload := loadFixture(t, "invoice_paid.json")
	signedAt := fixtureTime
	header := SignPayload(payload, testSecret, signedAt)

	cases := []struct {
		name      string
		now       time.Time
		tolerance time.Duration
		wantErr   error
	}{
		{"fresh", signedAt.Add(4 * time.Minute), 0, nil},
		{"expired default", signedAt.Add(6 * time.Minute), 0, ErrWebhookTimestamp},
		{"zero tolerance means default", signedAt.Add(5*time.Minute + time.Second), 0, ErrWebhookTimestamp},
		{"negative tolerance means default", signedAt.Add(6 * time.Minute), -time.Second, ErrWebhookTimestamp},
		{"custom tolerance", signedAt.Add(6 * time.Minute), 10 * time.Minute, nil},
		{"far future", signedAt.Add(-6 * time.Minute), 0, ErrWebhookTimestamp},
		{"small clock skew", signedAt.Add(-30 * time.Second), 0, nil},
	}
	for _, tc := range cases {
		_, err := VerifyWebhook(payload, header, testSecret, tc.tolerance, tc.now)
		if !errors.Is(err, tc.wantErr) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.wantErr)
		}
	}
	// An expired payload with a bad signature reports the signature, so
	// ErrWebhookTimestamp always means "authentic but too old".
	if _, err := VerifyWebhook(append(payload, ' '), header, testSecret, 0, signedAt.Add(time.Hour)); !errors.Is(err, ErrWebhookSignature) {
		t.Errorf("expired+tampered: %v", err)
	}
	// A zero now uses the wall clock: a 2026 fixture signature far from now
	// (or in the future) must be rejected rather than accepted.
	old := SignPayload(payload, testSecret, time.Now().Add(-time.Hour))
	if _, err := VerifyWebhook(payload, old, testSecret, 0, time.Time{}); !errors.Is(err, ErrWebhookTimestamp) {
		t.Errorf("wall clock: %v", err)
	}
}

func TestVerifyWebhookMultipleSignatures(t *testing.T) {
	payload := loadFixture(t, "customer_subscription_updated.json")
	ts := fixtureTime.Unix()
	good := v1(payload, testSecret, ts)
	bad := v1(payload, "whsec_other", ts)

	for name, header := range map[string]string{
		"good last":      fmt.Sprintf("t=%d,v1=%s,v1=%s", ts, bad, good),
		"good first":     fmt.Sprintf("t=%d,v1=%s,v1=%s,v0=%s", ts, good, bad, bad),
		"with garbage":   fmt.Sprintf("t=%d,v1=nothex,v1=%s,x=y,novalue", ts, good),
		"t after sigs":   fmt.Sprintf("v1=%s,t=%d", good, ts),
		"short sig skip": fmt.Sprintf("t=%d,v1=abcd,v1=%s", ts, good),
	} {
		if _, err := VerifyWebhook(payload, header, testSecret, 0, fixtureTime); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}

	// v0 is a test-mode decoy and must never be accepted on its own.
	onlyV0 := fmt.Sprintf("t=%d,v0=%s", ts, good)
	if _, err := VerifyWebhook(payload, onlyV0, testSecret, 0, fixtureTime); !errors.Is(err, ErrWebhookNoSignature) {
		t.Errorf("v0 only: %v", err)
	}
	onlyBad := fmt.Sprintf("t=%d,v1=%s,v1=%s", ts, bad, bad)
	if _, err := VerifyWebhook(payload, onlyBad, testSecret, 0, fixtureTime); !errors.Is(err, ErrWebhookSignature) {
		t.Errorf("only bad: %v", err)
	}
}

func TestVerifyWebhookSecretRotation(t *testing.T) {
	payload := loadFixture(t, "customer_subscription_deleted.json")
	ts := fixtureTime.Unix()
	// During a roll, Stripe signs with every active secret.
	both := fmt.Sprintf("t=%d,v1=%s,v1=%s", ts, v1(payload, testSecret, ts), v1(payload, testOldSecret, ts))
	oldOnly := fmt.Sprintf("t=%d,v1=%s", ts, v1(payload, testOldSecret, ts))
	newOnly := fmt.Sprintf("t=%d,v1=%s", ts, v1(payload, testSecret, ts))

	if _, err := VerifyWebhook(payload, both, testSecret, 0, fixtureTime); err != nil {
		t.Errorf("new secret, both sigs: %v", err)
	}
	if _, err := VerifyWebhook(payload, both, testOldSecret, 0, fixtureTime); err != nil {
		t.Errorf("old secret, both sigs: %v", err)
	}
	secrets := []string{testSecret, " " + testOldSecret + " ", ""}
	for name, h := range map[string]string{"old only": oldOnly, "new only": newOnly, "both": both} {
		if _, err := VerifyWebhookWithSecrets(payload, h, secrets, 0, fixtureTime); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	// After the old secret is removed, old-only signatures fail.
	if _, err := VerifyWebhookWithSecrets(payload, oldOnly, []string{testSecret}, 0, fixtureTime); !errors.Is(err, ErrWebhookSignature) {
		t.Errorf("old after rotation: %v", err)
	}
	if _, err := VerifyWebhookWithSecrets(payload, newOnly, []string{"", "  "}, 0, fixtureTime); !errors.Is(err, ErrWebhookNoSecret) {
		t.Errorf("no secrets: %v", err)
	}
	if _, err := VerifyWebhookWithSecrets(payload, newOnly, nil, 0, fixtureTime); !errors.Is(err, ErrWebhookNoSecret) {
		t.Errorf("nil secrets: %v", err)
	}
}

func TestVerifyWebhookBadHeaders(t *testing.T) {
	payload := loadFixture(t, "invoice_paid.json")
	ts := fixtureTime.Unix()
	sig := v1(payload, testSecret, ts)
	for name, h := range map[string]string{
		"empty":         "",
		"no timestamp":  "v1=" + sig,
		"bad timestamp": "t=abc,v1=" + sig,
		"negative t":    "t=-5,v1=" + sig,
		"duplicate t":   fmt.Sprintf("t=%d,t=%d,v1=%s", ts, ts, sig),
		"huge":          "t=1," + strings.Repeat("v1=00,", 1000),
	} {
		if _, err := VerifyWebhook(payload, h, testSecret, 0, fixtureTime); !errors.Is(err, ErrWebhookHeader) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := VerifyWebhook(payload, fmt.Sprintf("t=%d", ts), testSecret, 0, fixtureTime); !errors.Is(err, ErrWebhookNoSignature) {
		t.Errorf("no v1: %v", err)
	}
}

func TestVerifyWebhookPayloadLimits(t *testing.T) {
	big := bytes.Repeat([]byte(" "), MaxWebhookPayload+1)
	h := SignPayload(big, testSecret, fixtureTime)
	if _, err := VerifyWebhook(big, h, testSecret, 0, fixtureTime); !errors.Is(err, ErrWebhookPayloadTooLarge) {
		t.Errorf("big payload: %v", err)
	}
	if _, err := ReadWebhookBody(bytes.NewReader(big)); !errors.Is(err, ErrWebhookPayloadTooLarge) {
		t.Errorf("ReadWebhookBody: %v", err)
	}
	ok := loadFixture(t, "invoice_paid.json")
	got, err := ReadWebhookBody(bytes.NewReader(ok))
	if err != nil || !bytes.Equal(got, ok) {
		t.Errorf("ReadWebhookBody small: %v", err)
	}
}

func TestVerifyWebhookRejectsNonEvents(t *testing.T) {
	for name, payload := range map[string]string{
		"not json":      `not json`,
		"not event":     `{"id":"cus_1","object":"customer"}`,
		"no type":       `{"id":"evt_1","object":"event","data":{"object":{"id":"x"}}}`,
		"no data":       `{"id":"evt_1","object":"event","type":"invoice.paid"}`,
		"data not obj":  `{"id":"evt_1","object":"event","type":"invoice.paid","data":{"object":"in_1"}}`,
		"thin event v2": `{"id":"evt_1","object":"v2.core.event","type":"v1.invoice.paid","related_object":{"id":"in_1"}}`,
	} {
		p := []byte(payload)
		_, err := VerifyWebhook(p, SignPayload(p, testSecret, fixtureTime), testSecret, 0, fixtureTime)
		want := ErrWebhookPayload
		if name == "thin event v2" {
			want = ErrThinEvent
		}
		if !errors.Is(err, want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func verifiedFixture(t *testing.T, name string) *Event {
	t.Helper()
	p := loadFixture(t, name)
	ev, err := VerifyWebhook(p, SignPayload(p, testSecret, fixtureTime), testSecret, 0, fixtureTime)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return ev
}

func TestEventCheckoutSessionCompleted(t *testing.T) {
	ev := verifiedFixture(t, "checkout_session_completed.json")
	s, err := ev.CheckoutSession()
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "cs_test_a1TestSessionCompleted0000000000000000000000000000" || s.Customer != "cus_TestCustomer0001" ||
		s.Subscription != "sub_TestSubscription0001" || s.Status != "complete" || s.PaymentStatus != "paid" ||
		s.ClientReferenceID != "site_42" || s.Metadata["site_id"] != "42" || s.Mode != "subscription" ||
		s.URL != "" || s.Invoice != "in_TestInvoiceFirst0001" || s.AmountTotal != 2299 || s.Currency != "eur" {
		t.Errorf("unexpected session: %+v", s)
	}
	if s.CustomerEmail != "owner@example.com" || s.CustomerName != "Ejemplo Studio SL" ||
		s.CustomerAddress == nil || s.CustomerAddress.Country != "ES" ||
		len(s.TaxIDs) != 1 || s.TaxIDs[0] != (TaxID{Type: "eu_vat", Value: "ESA12345674"}) {
		t.Errorf("unexpected customer details: %+v %+v", s, s.CustomerAddress)
	}
	if _, err := ev.Subscription(); !errors.Is(err, ErrEventType) {
		t.Errorf("Subscription() on checkout event: %v", err)
	}
	if _, err := ev.Invoice(); !errors.Is(err, ErrEventType) {
		t.Errorf("Invoice() on checkout event: %v", err)
	}
}

func TestEventSubscriptionLifecycle(t *testing.T) {
	nov1 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	dec1 := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)

	created := verifiedFixture(t, "customer_subscription_created.json")
	s, err := created.Subscription()
	if err != nil {
		t.Fatal(err)
	}
	if created.Type != EventSubscriptionCreated || s.ID != "sub_TestSubscription0001" || s.Customer != "cus_TestCustomer0001" ||
		s.Status != "active" || s.State() != StateActive || s.PriceID() != "price_TestProMonthly" ||
		s.Items[0].ProductID != "prod_TestPro" || s.Items[0].LookupKey != "sap13ns_pro_monthly" || s.Items[0].Quantity != 1 ||
		!s.CurrentPeriodEnd.Equal(nov1) || !s.CurrentPeriodStart.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) ||
		s.Metadata["site_id"] != "42" || s.LatestInvoice != "in_TestInvoiceFirst0001" || s.CancelAtPeriodEnd ||
		!s.CanceledAt.IsZero() || !s.TrialEnd.IsZero() {
		t.Errorf("created: %+v", s)
	}

	updated := verifiedFixture(t, "customer_subscription_updated.json")
	s, err = updated.Subscription()
	if err != nil {
		t.Fatal(err)
	}
	if s.Status != "past_due" || s.State() != StateGrace || !s.CurrentPeriodEnd.Equal(dec1) {
		t.Errorf("updated: %+v", s)
	}
	if !bytes.Contains(updated.PreviousAttributes, []byte(`"status": "active"`)) {
		t.Errorf("previous_attributes: %s", updated.PreviousAttributes)
	}

	deleted := verifiedFixture(t, "customer_subscription_deleted.json")
	s, err = deleted.Subscription()
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Type != EventSubscriptionDeleted || s.State() != StateCanceled || !s.EndedAt.Equal(nov1) ||
		s.CancellationReason != "cancellation_requested" || !s.CancelAtPeriodEnd {
		t.Errorf("deleted: %+v", s)
	}
	if _, err := deleted.CheckoutSession(); !errors.Is(err, ErrEventType) {
		t.Errorf("CheckoutSession() on subscription event: %v", err)
	}
}

func TestEventInvoices(t *testing.T) {
	paid := verifiedFixture(t, "invoice_paid.json")
	inv, err := paid.Invoice()
	if err != nil {
		t.Fatal(err)
	}
	if paid.Type != EventInvoicePaid || inv.ID != "in_TestInvoiceFirst0001" || inv.Subscription != "sub_TestSubscription0001" ||
		inv.SubscriptionMetadata["site_id"] != "42" || inv.Status != "paid" || inv.BillingReason != "subscription_create" ||
		inv.AmountPaid != 2299 || inv.Customer != "cus_TestCustomer0001" || inv.PaidAt.Unix() != 1790812805 ||
		!inv.ServicePeriodEnd.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) ||
		len(inv.PriceIDs) != 1 || inv.PriceIDs[0] != "price_TestProMonthly" || !inv.NextPaymentAttempt.IsZero() {
		t.Errorf("paid: %+v", inv)
	}

	failed := verifiedFixture(t, "invoice_payment_failed.json")
	inv, err = failed.Invoice()
	if err != nil {
		t.Fatal(err)
	}
	if failed.Type != EventInvoicePaymentFailed || inv.Status != "open" || inv.BillingReason != "subscription_cycle" ||
		inv.AttemptCount != 1 || inv.NextPaymentAttempt.Unix() != 1793754000 || inv.AmountRemaining != 2299 ||
		!inv.PaidAt.IsZero() || inv.HostedInvoiceURL == "" || inv.Subscription != "sub_TestSubscription0001" ||
		!inv.ServicePeriodEnd.Equal(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("failed: %+v", inv)
	}
}

func TestEventDecodingCompat(t *testing.T) {
	sign := func(p string) *Event {
		t.Helper()
		ev, err := VerifyWebhook([]byte(p), SignPayload([]byte(p), testSecret, fixtureTime), testSecret, 0, fixtureTime)
		if err != nil {
			t.Fatal(err)
		}
		return ev
	}
	// Pre-basil shapes: top-level invoice.subscription and subscription
	// current_period_end, plus an expanded customer object.
	inv, err := sign(`{"id":"evt_old1","object":"event","type":"invoice.paid","created":1,
		"data":{"object":{"id":"in_old","object":"invoice","subscription":"sub_old","customer":{"id":"cus_obj","object":"customer"}}}}`).Invoice()
	if err != nil || inv.Subscription != "sub_old" || inv.Customer != "cus_obj" {
		t.Errorf("legacy invoice: %+v %v", inv, err)
	}
	sub, err := sign(`{"id":"evt_old2","object":"event","type":"customer.subscription.updated","created":1,
		"data":{"object":{"id":"sub_old","object":"subscription","status":"unpaid","current_period_end":1793491200,"items":{"data":[]}}}}`).Subscription()
	if err != nil || sub.CurrentPeriodEnd.Unix() != 1793491200 || sub.State() != StateGrace {
		t.Errorf("legacy subscription: %+v %v", sub, err)
	}
	// data.object of the wrong kind is rejected even if the type matches.
	if _, err := sign(`{"id":"evt_x","object":"event","type":"invoice.paid","created":1,
		"data":{"object":{"id":"cus_1","object":"customer"}}}`).Invoice(); err == nil {
		t.Error("customer object decoded as invoice")
	}
}
