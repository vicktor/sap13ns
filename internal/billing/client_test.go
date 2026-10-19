package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeKey is obviously not a real Stripe key.
const fakeKey = "sk_test_DUMMYkeyForUnitTestsOnly000000"

func newTestClient(t *testing.T, h http.HandlerFunc, opts ...Option) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	opts = append([]Option{
		WithBaseURL(srv.URL),
		WithRetryBackoff(time.Millisecond, 2*time.Millisecond),
	}, opts...)
	c, err := New(fakeKey, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// fixtureObject returns data.object of a testdata event fixture.
func fixtureObject(t *testing.T, name string) json.RawMessage {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var ev struct {
		Data struct {
			Object json.RawMessage `json:"object"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &ev); err != nil {
		t.Fatal(err)
	}
	return ev.Data.Object
}

func readForm(t *testing.T, r *http.Request) url.Values {
	t.Helper()
	if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type = %q", ct)
	}
	// Runs inside HTTP handlers: report with Errorf, never Fatal.
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Errorf("reading body: %v", err)
		return url.Values{}
	}
	v, err := url.ParseQuery(string(b))
	if err != nil {
		t.Errorf("parsing form: %v", err)
		return url.Values{}
	}
	return v
}

func TestNewValidation(t *testing.T) {
	for _, key := range []string{"", "  ", "pk_test_abc", "abc", "sk_test_a b"} {
		if _, err := New(key); err == nil {
			t.Errorf("New(%q) accepted", key)
		}
	}
	for _, base := range []string{"http://api.example.com", "ftp://localhost", "https://user:pw@api.example.com", "https://api.example.com/?q=1", "not a url"} {
		if _, err := New(fakeKey, WithBaseURL(base)); err == nil {
			t.Errorf("base URL %q accepted", base)
		}
	}
	for _, base := range []string{"http://127.0.0.1:1234", "http://localhost:9", "https://api.stripe.com/"} {
		if _, err := New(fakeKey, WithBaseURL(base)); err != nil {
			t.Errorf("base URL %q rejected: %v", base, err)
		}
	}
	c, err := New(fakeKey)
	if err != nil {
		t.Fatal(err)
	}
	if c.http.Timeout != DefaultTimeout || c.baseURL != DefaultBaseURL || c.apiVersion != DefaultAPIVersion {
		t.Errorf("defaults not applied: %+v", c)
	}
	if c.Livemode() {
		t.Error("test key reported as live")
	}
	for _, v := range []string{"", "2026-09-30\r\nX-Evil: 1"} {
		if _, err := New(fakeKey, WithAPIVersion(v)); err == nil {
			t.Errorf("API version %q accepted", v)
		}
	}
	if c2, err := New(fakeKey, WithAPIVersion("2026-10-15.endive")); err != nil || c2.apiVersion != "2026-10-15.endive" {
		t.Errorf("WithAPIVersion: %v", err)
	}
	if live, _ := New("rk_live_DUMMYrestricted"); live == nil || !live.Livemode() {
		t.Error("restricted live key not detected as live")
	}
	// A caller-supplied http.Client is copied, not mutated.
	hc := &http.Client{}
	if _, err := New(fakeKey, WithHTTPClient(hc)); err != nil {
		t.Fatal(err)
	}
	if hc.CheckRedirect != nil || hc.Timeout != 0 {
		t.Error("caller http.Client was mutated")
	}
}

func TestClientNeverPrintsKey(t *testing.T) {
	c, err := New(fakeKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{fmt.Sprint(c), fmt.Sprintf("%v %+v %#v %s", c, c, c, c)} {
		if strings.Contains(s, "DUMMYkey") {
			t.Fatalf("key leaked: %s", s)
		}
	}
}

func TestCreateCheckoutSession(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/checkout/sessions" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+fakeKey {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("Stripe-Version"); got != DefaultAPIVersion {
			t.Errorf("Stripe-Version = %q", got)
		}
		if got := r.Header.Get("Idempotency-Key"); got != "checkout-site42-1" {
			t.Errorf("Idempotency-Key = %q", got)
		}
		f := readForm(t, r)
		want := map[string]string{
			"mode":                                 "subscription",
			"customer":                             "cus_TestCustomer0001",
			"line_items[0][price]":                 "price_TestProMonthly",
			"line_items[0][quantity]":              "1",
			"success_url":                          "https://portal.sap13ns.site/billing/success?session_id={CHECKOUT_SESSION_ID}",
			"cancel_url":                           "https://portal.sap13ns.site/billing/cancel",
			"client_reference_id":                  "site_42",
			"metadata[site_id]":                    "42",
			"metadata[plan]":                       "pro",
			"subscription_data[metadata][site_id]": "42",
			"subscription_data[metadata][plan]":    "pro",
			"subscription_data[trial_period_days]": "14",
			"allow_promotion_codes":                "true",
			"locale":                               "es",
			"tax_id_collection[enabled]":           "true",
			"automatic_tax[enabled]":               "true",
			"billing_address_collection":           "required",
			"customer_update[name]":                "auto",
			"customer_update[address]":             "auto",
		}
		for k, v := range want {
			if got := f.Get(k); got != v {
				t.Errorf("form %s = %q, want %q", k, got, v)
			}
		}
		if len(f) != len(want) {
			t.Errorf("form has %d keys, want %d: %v", len(f), len(want), f)
		}
		writeJSON(w, 200, map[string]any{
			"id":                  "cs_test_new",
			"object":              "checkout.session",
			"url":                 "https://checkout.stripe.com/c/pay/cs_test_new",
			"status":              "open",
			"payment_status":      "unpaid",
			"mode":                "subscription",
			"customer":            "cus_TestCustomer0001",
			"subscription":        nil,
			"client_reference_id": "site_42",
			"metadata":            map[string]string{"site_id": "42", "plan": "pro"},
			"expires_at":          1790899200,
		})
	})
	s, err := c.CreateCheckoutSession(context.Background(), CheckoutParams{
		Customer:               "cus_TestCustomer0001",
		PriceID:                "price_TestProMonthly",
		SuccessURL:             "https://portal.sap13ns.site/billing/success?session_id={CHECKOUT_SESSION_ID}",
		CancelURL:              "https://portal.sap13ns.site/billing/cancel",
		ClientReferenceID:      "site_42",
		Metadata:               map[string]string{"site_id": "42", "plan": "pro"},
		TrialDays:              14,
		AllowPromotionCodes:    true,
		Locale:                 "es-ES",
		TaxIDCollection:        true,
		AutomaticTax:           true,
		BillingAddressRequired: true,
	}, "checkout-site42-1")
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "cs_test_new" || s.URL == "" || s.Status != "open" || s.Customer != "cus_TestCustomer0001" ||
		s.Subscription != "" || s.Metadata["site_id"] != "42" || s.ExpiresAt.Unix() != 1790899200 {
		t.Errorf("unexpected session: %+v", s)
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d", calls.Load())
	}
}

func TestCreateCheckoutSessionMinimal(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		f := readForm(t, r)
		if f.Get("customer_email") != "owner@example.com" || f.Get("customer") != "" {
			t.Errorf("customer fields: %v", f)
		}
		for _, k := range []string{"customer_update[name]", "locale", "subscription_data[trial_period_days]", "cancel_url", "metadata[site_id]"} {
			if f.Has(k) {
				t.Errorf("unexpected %s", k)
			}
		}
		if r.Header.Get("Idempotency-Key") != "" {
			t.Error("unexpected idempotency key")
		}
		writeJSON(w, 200, map[string]any{"id": "cs_1", "object": "checkout.session", "url": "https://checkout.stripe.com/c/pay/cs_1"})
	})
	if _, err := c.CreateCheckoutSession(context.Background(), CheckoutParams{
		CustomerEmail:   "owner@example.com",
		PriceID:         "price_1",
		SuccessURL:      "https://portal.sap13ns.site/ok",
		TaxIDCollection: true, // no customer_update without an existing customer
	}, ""); err != nil {
		t.Fatal(err)
	}
}

func TestCreateCheckoutSessionValidation(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("request sent for invalid params")
	})
	ok := CheckoutParams{PriceID: "price_1", SuccessURL: "https://x.example/ok"}
	cases := map[string]func(p *CheckoutParams){
		"no price":        func(p *CheckoutParams) { p.PriceID = "" },
		"bad price":       func(p *CheckoutParams) { p.PriceID = "price_1/../x" },
		"both customers":  func(p *CheckoutParams) { p.Customer, p.CustomerEmail = "cus_1", "a@b.c" },
		"bad email":       func(p *CheckoutParams) { p.CustomerEmail = "nope" },
		"no success url":  func(p *CheckoutParams) { p.SuccessURL = "" },
		"http success":    func(p *CheckoutParams) { p.SuccessURL = "http://x.example/ok" },
		"relative cancel": func(p *CheckoutParams) { p.CancelURL = "/cancel" },
		"bad metadata":    func(p *CheckoutParams) { p.Metadata = map[string]string{"a]b": "x"} },
		"negative qty":    func(p *CheckoutParams) { p.Quantity = -1 },
		"negative trial":  func(p *CheckoutParams) { p.TrialDays = -1 },
		"long client ref": func(p *CheckoutParams) { p.ClientReferenceID = strings.Repeat("x", 201) },
	}
	for name, mod := range cases {
		p := ok
		mod(&p)
		if _, err := c.CreateCheckoutSession(context.Background(), p, ""); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestErrorParsingAndRedaction(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Request-Id", "req_TestRequest123")
		writeJSON(w, 400, map[string]any{"error": map[string]any{
			"type":    "invalid_request_error",
			"code":    "resource_missing",
			"message": "No such price: 'price_x'; key " + fakeKey + " or sk_live_51AbCdEf and whsec_abcDEF123 and sk_test_****1234",
			"param":   "line_items[0][price]",
			"doc_url": "https://docs.stripe.com/error-codes/resource-missing",
		}})
	}, WithMaxRetries(0))
	_, err := c.CreateCheckoutSession(context.Background(), CheckoutParams{PriceID: "price_x", SuccessURL: "https://x.example/ok"}, "k1")
	var se *Error
	if !errors.As(err, &se) {
		t.Fatalf("want *Error, got %T %v", err, err)
	}
	if se.Type != "invalid_request_error" || se.Code != "resource_missing" || se.Param != "line_items[0][price]" ||
		se.StatusCode != 400 || se.RequestID != "req_TestRequest123" || se.DocURL == "" {
		t.Errorf("unexpected error fields: %+v", se)
	}
	msg := err.Error()
	for _, leak := range []string{"DUMMYkey", "51AbCdEf", "abcDEF123", "1234"} {
		if strings.Contains(msg, leak) {
			t.Errorf("error leaks %q: %s", leak, msg)
		}
	}
	if !strings.Contains(msg, "No such price") || !strings.Contains(msg, "req_TestRequest123") {
		t.Errorf("error message lost detail: %s", msg)
	}
	if !IsNotFound(err) {
		t.Error("IsNotFound = false")
	}
}

func TestNonJSONErrorResponse(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusBadGateway)
		io.WriteString(w, "<html>bad gateway</html>")
	}, WithMaxRetries(0))
	_, err := c.GetCustomer(context.Background(), "cus_1")
	var se *Error
	if !errors.As(err, &se) || se.StatusCode != 502 || se.Type != "api_error" {
		t.Fatalf("got %v", err)
	}
}

func TestIdempotencyErrorHelper(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 400, map[string]any{"error": map[string]any{"type": "idempotency_error", "message": "Keys for idempotent requests can only be used with the same parameters"}})
	})
	_, err := c.CreateCheckoutSession(context.Background(), CheckoutParams{PriceID: "price_x", SuccessURL: "https://x.example/ok"}, "k1")
	if !IsIdempotencyError(err) {
		t.Fatalf("got %v", err)
	}
}

func TestRetriesWithIdempotencyKey(t *testing.T) {
	var calls atomic.Int32
	keys := make(chan string, 10)
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		keys <- r.Header.Get("Idempotency-Key")
		_ = readForm(t, r) // body is replayed on every attempt
		if calls.Add(1) < 3 {
			writeJSON(w, 500, map[string]any{"error": map[string]any{"type": "api_error", "message": "boom"}})
			return
		}
		writeJSON(w, 200, map[string]any{"id": "cs_1", "object": "checkout.session", "url": "https://checkout.stripe.com/c/pay/cs_1"})
	})
	s, err := c.CreateCheckoutSession(context.Background(), CheckoutParams{PriceID: "price_1", SuccessURL: "https://x.example/ok"}, "idem-123")
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "cs_1" || calls.Load() != 3 {
		t.Errorf("id=%s calls=%d", s.ID, calls.Load())
	}
	close(keys)
	for k := range keys {
		if k != "idem-123" {
			t.Errorf("attempt used key %q", k)
		}
	}
}

func TestNoRetryForUnkeyedPOST(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeJSON(w, 503, map[string]any{"error": map[string]any{"type": "api_error", "message": "unavailable"}})
	})
	_, err := c.CreateCheckoutSession(context.Background(), CheckoutParams{PriceID: "price_1", SuccessURL: "https://x.example/ok"}, "")
	if err == nil || calls.Load() != 1 {
		t.Errorf("err=%v calls=%d", err, calls.Load())
	}
}

func TestRetryGETOn429AndExhaustion(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Idempotency-Key") != "" {
			t.Errorf("%s with key %q", r.Method, r.Header.Get("Idempotency-Key"))
		}
		if calls.Add(1) == 1 {
			writeJSON(w, 429, map[string]any{"error": map[string]any{"type": "invalid_request_error", "code": "rate_limit", "message": "slow down"}})
			return
		}
		writeJSON(w, 200, map[string]any{"id": "cus_1", "object": "customer", "email": "a@b.c"})
	})
	cu, err := c.GetCustomer(context.Background(), "cus_1")
	if err != nil || cu.Email != "a@b.c" || calls.Load() != 2 {
		t.Fatalf("cu=%+v err=%v calls=%d", cu, err, calls.Load())
	}

	var calls2 atomic.Int32
	c2 := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls2.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	_, err = c2.GetCustomer(context.Background(), "cus_1")
	var se *Error
	if !errors.As(err, &se) || se.StatusCode != 503 {
		t.Fatalf("err=%v", err)
	}
	if got := calls2.Load(); got != 1+DefaultMaxRetries {
		t.Errorf("attempts = %d, want %d", got, 1+DefaultMaxRetries)
	}
}

func TestNoRetryOnClientError(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeJSON(w, 404, map[string]any{"error": map[string]any{"type": "invalid_request_error", "code": "resource_missing", "message": "No such subscription"}})
	})
	_, err := c.GetSubscription(context.Background(), "sub_1")
	if !IsNotFound(err) || calls.Load() != 1 {
		t.Errorf("err=%v calls=%d", err, calls.Load())
	}
}

func TestStripeShouldRetryHeader(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Stripe-Should-Retry", "false")
		writeJSON(w, 500, map[string]any{"error": map[string]any{"type": "api_error", "message": "do not retry"}})
	})
	if _, err := c.GetCustomer(context.Background(), "cus_1"); err == nil || calls.Load() != 1 {
		t.Errorf("should-retry=false: err=%v calls=%d", err, calls.Load())
	}

	var calls2 atomic.Int32
	c2 := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if calls2.Add(1) == 1 {
			w.Header().Set("Stripe-Should-Retry", "true")
			writeJSON(w, 409, map[string]any{"error": map[string]any{"type": "invalid_request_error", "code": "lock_timeout", "message": "busy"}})
			return
		}
		writeJSON(w, 200, map[string]any{"id": "cus_1", "object": "customer"})
	})
	if _, err := c2.GetCustomer(context.Background(), "cus_1"); err != nil || calls2.Load() != 2 {
		t.Errorf("should-retry=true: err=%v calls=%d", err, calls2.Load())
	}
}

func TestRetryOnNetworkError(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			conn.Close() // drop the connection without a response
			return
		}
		writeJSON(w, 200, map[string]any{"id": "sub_1", "object": "subscription", "status": "active"})
	})
	s, err := c.GetSubscription(context.Background(), "sub_1")
	if err != nil || s.Status != "active" || calls.Load() != 2 {
		t.Fatalf("s=%+v err=%v calls=%d", s, err, calls.Load())
	}
}

func TestNoRedirects(t *testing.T) {
	var followed atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/customers/cus_1", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	})
	mux.HandleFunc("/elsewhere", func(w http.ResponseWriter, r *http.Request) {
		followed.Add(1)
		writeJSON(w, 200, map[string]any{"id": "cus_1", "object": "customer"})
	})
	c := newTestClient(t, mux.ServeHTTP)
	_, err := c.GetCustomer(context.Background(), "cus_1")
	var se *Error
	if !errors.As(err, &se) || se.StatusCode != http.StatusFound {
		t.Fatalf("err=%v", err)
	}
	if followed.Load() != 0 {
		t.Error("redirect was followed")
	}
}

func TestResponseSizeCap(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"cus_1","object":"customer","name":"`)
		io.WriteString(w, strings.Repeat("a", MaxResponseSize))
		io.WriteString(w, `"}`)
	})
	_, err := c.GetCustomer(context.Background(), "cus_1")
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("err=%v", err)
	}
}

func TestContextCancelDuringBackoff(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}, WithRetryBackoff(time.Hour, time.Hour))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.GetCustomer(ctx, "cus_1")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	if time.Since(start) > 5*time.Second || calls.Load() != 1 {
		t.Errorf("elapsed=%s calls=%d", time.Since(start), calls.Load())
	}
}

func TestInvalidIDsNeverSent(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("request sent: %s", r.URL)
	})
	ctx := context.Background()
	if _, err := c.GetSubscription(ctx, "../v1/charges"); err == nil {
		t.Error("GetSubscription accepted path traversal")
	}
	if _, err := c.GetCustomer(ctx, "cus_1?expand=x"); err == nil {
		t.Error("GetCustomer accepted query")
	}
	if _, err := c.CancelSubscription(ctx, "", true); err == nil {
		t.Error("CancelSubscription accepted empty ID")
	}
	if _, err := c.CreatePortalSession(ctx, "cus 1", "https://x.example", ""); err == nil {
		t.Error("CreatePortalSession accepted bad ID")
	}
}

func TestGetSubscription(t *testing.T) {
	obj := fixtureObject(t, "customer_subscription_created.json")
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/subscriptions/sub_TestSubscription0001" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(obj)
	})
	s, err := c.GetSubscription(context.Background(), "sub_TestSubscription0001")
	if err != nil {
		t.Fatal(err)
	}
	if s.State() != StateActive || s.PriceID() != "price_TestProMonthly" ||
		!s.CurrentPeriodEnd.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("unexpected subscription: %+v", s)
	}
}

func TestCancelSubscription(t *testing.T) {
	obj := fixtureObject(t, "customer_subscription_deleted.json")
	var gotMethod, gotKey string
	var gotForm url.Values
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotKey = r.Method, r.Header.Get("Idempotency-Key")
		if r.URL.Path != "/v1/subscriptions/sub_TestSubscription0001" {
			t.Errorf("path %s", r.URL.Path)
		}
		if r.Method == http.MethodPost {
			gotForm = readForm(t, r)
		}
		w.Write(obj)
	})
	ctx := context.Background()
	if _, err := c.CancelSubscription(ctx, "sub_TestSubscription0001", true); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotForm.Get("cancel_at_period_end") != "true" || !strings.HasPrefix(gotKey, "sap13ns-") {
		t.Errorf("at period end: %s %v key=%q", gotMethod, gotForm, gotKey)
	}
	s, err := c.CancelSubscription(ctx, "sub_TestSubscription0001", false)
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodDelete || gotKey != "" || s.State() != StateCanceled {
		t.Errorf("immediate: %s key=%q state=%s", gotMethod, gotKey, s.State())
	}
}

func TestGetCustomerDeleted(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"id": "cus_gone", "object": "customer", "deleted": true})
	})
	cu, err := c.GetCustomer(context.Background(), "cus_gone")
	if err != nil || !cu.Deleted || cu.ID != "cus_gone" {
		t.Fatalf("cu=%+v err=%v", cu, err)
	}
}

func TestCreatePortalSession(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/billing_portal/sessions" || r.Header.Get("Idempotency-Key") == "" {
			t.Errorf("%s key=%q", r.URL.Path, r.Header.Get("Idempotency-Key"))
		}
		f := readForm(t, r)
		if f.Get("customer") != "cus_1" || f.Get("return_url") != "https://portal.sap13ns.site/sites/42/billing" || f.Get("configuration") != "bpc_1abcdefgh" {
			t.Errorf("form %v", f)
		}
		writeJSON(w, 200, map[string]any{"id": "bps_1", "object": "billing_portal.session", "url": "https://billing.stripe.com/p/session/test_x"})
	})
	u, err := c.CreatePortalSession(context.Background(), "cus_1", "https://portal.sap13ns.site/sites/42/billing", "bpc_1abcdefgh")
	if err != nil || u != "https://billing.stripe.com/p/session/test_x" {
		t.Fatalf("u=%q err=%v", u, err)
	}
	if _, err := c.CreatePortalSession(context.Background(), "cus_1", "javascript:alert(1)", ""); err == nil {
		t.Error("bad return URL accepted")
	}
	if _, err := c.CreatePortalSession(context.Background(), "cus_1", "https://x.example", "cus_2"); err == nil {
		t.Error("bad configuration accepted")
	}
}

func TestListPricesPaginates(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/v1/prices" || q.Get("active") != "true" || q.Get("type") != "recurring" ||
			q.Get("limit") != "100" || q.Get("expand[0]") != "data.product" {
			t.Errorf("query %s", r.URL.RawQuery)
		}
		switch calls.Add(1) {
		case 1:
			if q.Has("starting_after") {
				t.Error("first page has cursor")
			}
			writeJSON(w, 200, map[string]any{"object": "list", "has_more": true, "data": []any{
				map[string]any{
					"id": "price_ProM", "object": "price", "active": true, "currency": "eur", "unit_amount": 1900,
					"billing_scheme": "per_unit", "tax_behavior": "exclusive", "lookup_key": "sap13ns_pro_monthly",
					"type":      "recurring",
					"recurring": map[string]any{"interval": "month", "interval_count": 1},
					"product":   map[string]any{"id": "prod_Pro", "object": "product", "name": "sap13ns Pro", "active": true},
				},
			}})
		case 2:
			if q.Get("starting_after") != "price_ProM" {
				t.Errorf("cursor %q", q.Get("starting_after"))
			}
			writeJSON(w, 200, map[string]any{"object": "list", "has_more": false, "data": []any{
				map[string]any{
					"id": "price_BizY", "object": "price", "active": true, "currency": "eur", "unit_amount": 49000,
					"recurring": map[string]any{"interval": "year", "interval_count": 1},
					"product":   "prod_Biz", // not expanded
				},
			}})
		default:
			t.Error("too many pages")
		}
	})
	prices, err := c.ListPrices(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(prices) != 2 {
		t.Fatalf("got %d prices", len(prices))
	}
	p := prices[0]
	if p.ID != "price_ProM" || p.Product != "prod_Pro" || p.ProductName != "sap13ns Pro" || p.UnitAmount != 1900 ||
		p.Currency != "eur" || p.Interval != "month" || p.IntervalCount != 1 || p.LookupKey != "sap13ns_pro_monthly" {
		t.Errorf("price 0: %+v", p)
	}
	if prices[1].Product != "prod_Biz" || prices[1].ProductName != "" || prices[1].Interval != "year" {
		t.Errorf("price 1: %+v", prices[1])
	}
}

func TestBackoffBounds(t *testing.T) {
	c := &Client{backoffBase: 100 * time.Millisecond, backoffMax: 300 * time.Millisecond}
	for attempt, maxD := range map[int]time.Duration{1: 100 * time.Millisecond, 2: 200 * time.Millisecond, 3: 300 * time.Millisecond, 10: 300 * time.Millisecond} {
		for range 50 {
			d := c.backoff(attempt)
			if d < maxD/2 || d > maxD {
				t.Fatalf("attempt %d: %s not in [%s, %s]", attempt, d, maxD/2, maxD)
			}
		}
	}
}
