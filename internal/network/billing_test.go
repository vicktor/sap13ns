package network

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"sap13ns/internal/billing"
	"sap13ns/internal/config"
	"sap13ns/internal/mail"
	"sap13ns/internal/testutil"
)

// fakeStripe is the part of the Stripe API the network's billing uses. No
// test talks to the real Stripe.
type fakeStripe struct {
	t   *testing.T
	srv *httptest.Server
	key string

	mu        sync.Mutex
	prices    []string // the price objects GET /v1/prices lists
	subs      map[string]*fakeSub
	down      bool // GET /v1/subscriptions/{id} fails
	checkouts []url.Values
	keys      []string // the Idempotency-Key of each Checkout
	portals   []url.Values
	fetched   map[string]int // GET /v1/subscriptions/{id}
}

type fakeSub struct {
	status, price, customer string
	cancelAtEnd             bool
	end                     time.Time
}

const (
	testStripeKey = "sk_test_fake0001"
	hookSecret    = "whsec_current"
	hookOld       = "whsec_previous" // still configured during a rotation
)

func newFakeStripe(t *testing.T, key string) *fakeStripe {
	f := &fakeStripe{t: t, key: key, subs: map[string]*fakeSub{}, fetched: map[string]int{},
		prices: []string{
			taxedPriceJSON("price_pro_month", 900, "month", "inclusive"),
			taxedPriceJSON("price_pro_year", 9900, "year", "inclusive"),
			taxedPriceJSON("price_biz_month", 3000, "month", "exclusive"),
			priceJSON("price_other", false, 100, "month"),
		}}
	f.srv = httptest.NewServer(f)
	t.Cleanup(f.srv.Close)
	return f
}

func priceJSON(id string, live bool, amount int, interval string) string {
	return fmt.Sprintf(`{"id":%q,"object":"price","active":true,"livemode":%t,"unit_amount":%d,"currency":"eur","type":"recurring",`+
		`"tax_behavior":"unspecified","recurring":{"interval":%q,"interval_count":1},"product":{"id":"prod_test","object":"product","name":"Plan"}}`,
		id, live, amount, interval)
}

// taxedPriceJSON is a test-mode price with a tax behavior.
func taxedPriceJSON(id string, amount int, interval, tax string) string {
	return strings.Replace(priceJSON(id, false, amount, interval), `"tax_behavior":"unspecified"`, `"tax_behavior":"`+tax+`"`, 1)
}

func (f *fakeStripe) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+f.key {
		http.Error(w, `{"error":{"type":"invalid_request_error","message":"bad key"}}`, http.StatusUnauthorized)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == "GET" && r.URL.Path == "/v1/prices":
		if q := r.URL.Query(); q.Get("active") != "true" || q.Get("type") != "recurring" {
			http.Error(w, `{"error":{"type":"invalid_request_error","message":"filters"}}`, http.StatusBadRequest)
			return
		}
		fmt.Fprintf(w, `{"object":"list","data":[%s],"has_more":false}`, strings.Join(f.prices, ","))
	case r.Method == "POST" && r.URL.Path == "/v1/checkout/sessions":
		if err := r.ParseForm(); err != nil {
			f.t.Error(err)
		}
		f.checkouts = append(f.checkouts, r.PostForm)
		f.keys = append(f.keys, r.Header.Get("Idempotency-Key"))
		id := "cs_test_" + strconv.Itoa(len(f.checkouts))
		fmt.Fprintf(w, `{"id":%q,"object":"checkout.session","url":"https://checkout.stripe.com/c/pay/%s","mode":"subscription","livemode":false}`, id, id)
	case r.Method == "POST" && r.URL.Path == "/v1/billing_portal/sessions":
		if err := r.ParseForm(); err != nil {
			f.t.Error(err)
		}
		f.portals = append(f.portals, r.PostForm)
		fmt.Fprint(w, `{"id":"bps_test_1","object":"billing_portal.session","url":"https://billing.stripe.com/p/session/test_1"}`)
	case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/subscriptions/"):
		id := strings.TrimPrefix(r.URL.Path, "/v1/subscriptions/")
		f.fetched[id]++
		s := f.subs[id]
		switch {
		case f.down:
			w.Header().Set("Stripe-Should-Retry", "true")
			http.Error(w, `{"error":{"type":"api_error","message":"temporarily unavailable"}}`, http.StatusInternalServerError)
		case s == nil:
			http.Error(w, `{"error":{"type":"invalid_request_error","code":"resource_missing","message":"No such subscription"}}`, http.StatusNotFound)
		default:
			fmt.Fprintf(w, `{"id":%q,"object":"subscription","customer":%q,"status":%q,"cancel_at_period_end":%t,"livemode":false,"metadata":{},`+
				`"items":{"object":"list","data":[{"id":"si_%s","quantity":1,"current_period_start":%d,"current_period_end":%d,"price":{"id":%q,"product":"prod_test"}}]}}`,
				id, s.customer, s.status, s.cancelAtEnd, id, s.end.Add(-30*24*time.Hour).Unix(), s.end.Unix(), s.price)
		}
	default:
		http.Error(w, `{"error":{"type":"invalid_request_error","message":"unknown path"}}`, http.StatusNotFound)
	}
}

// set makes Stripe answer a subscription in this state.
func (f *fakeStripe) set(id, status, price string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.subs[id]
	if s == nil {
		s = &fakeSub{customer: "cus_test0001", end: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)}
		f.subs[id] = s
	}
	s.status, s.price = status, price
}

func (f *fakeStripe) setDown(down bool) {
	f.mu.Lock()
	f.down = down
	f.mu.Unlock()
}

func (f *fakeStripe) fetches(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fetched[id]
}

// checkout returns the parameters and the idempotency key of the i-th
// Checkout Session.
func (f *fakeStripe) checkout(i int) (url.Values, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i >= len(f.checkouts) {
		f.t.Fatalf("%d Checkout Sessions, want at least %d", len(f.checkouts), i+1)
	}
	return f.checkouts[i], f.keys[i]
}

func (f *fakeStripe) sessions() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.checkouts)
}

func (f *fakeStripe) options() []billing.Option {
	return []billing.Option{billing.WithBaseURL(f.srv.URL), billing.WithRetryBackoff(time.Millisecond, 2*time.Millisecond)}
}

func testStripe() config.Stripe {
	return config.Stripe{SecretKey: testStripeKey, WebhookSecrets: []string{hookSecret, hookOld},
		Prices: map[string]config.StripePrices{"pro": {Monthly: "price_pro_month", Yearly: "price_pro_year"},
			"business": {Monthly: "price_biz_month"}},
		AutomaticTax: true, TaxIDCollection: true, BillingAddress: true, TrialDays: 14, GraceDays: 7}
}

// newBillingEnv is a network with billing on a fake Stripe, whose portal
// prints its e-mail to the mailbox.
func newBillingEnv(t *testing.T) (*netEnv, *fakeStripe, *mailbox) {
	t.Helper()
	fs := newFakeStripe(t, testStripeKey)
	box := &mailbox{}
	e := newNetEnv(t, func(o *RegistryOptions) {
		o.Mailer = mail.New(config.SMTP{}, slog.New(slog.NewTextHandler(box, nil)), true)
		o.Config.Stripe = testStripe()
		o.Network.grace = graceOf(o.Config.Stripe)
		b, err := NewBilling(context.Background(), o.Network, o.Config, o.Log, fs.options()...)
		if err != nil || b == nil {
			t.Fatalf("NewBilling: %v, %v", b, err)
		}
		o.Billing = b
	})
	e.mfa(e.owner) // billing asks for the second factor, as the site's admin does
	return e, fs, box
}

func (e *netEnv) billing() *Billing { return e.reg.o.Billing }

// personal creates an open Personal site of the owner.
func (e *netEnv) personal(slug string) *Site {
	e.t.Helper()
	s := e.site(slug, e.owner, PlanPersonal)
	if err := e.n.ApproveSite(e.ctx, s.ID, 0); err != nil {
		e.t.Fatal(err)
	}
	e.refresh()
	return e.row(s.ID)
}

// hook posts a Stripe event about obj to the portal's webhook, signed now
// with secret.
func (e *netEnv) hook(secret, id, typ string, created int, obj string) *httptest.ResponseRecorder {
	e.t.Helper()
	payload := fmt.Sprintf(`{"id":%q,"object":"event","api_version":"2026-09-30.endive","created":%d,"livemode":false,"type":%q,"data":{"object":%s}}`,
		id, e.clock.now().Unix()-1000+int64(created), typ, obj)
	return e.postHook(portalURL, payload, billing.SignPayload([]byte(payload), secret, e.clock.now()), "application/json; charset=utf-8")
}

func (e *netEnv) postHook(origin, payload, signature, contentType string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", origin+"/billing/webhook", strings.NewReader(payload))
	req.RemoteAddr = "192.0.2.50:4000"
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", "Stripe/1.0 (+https://stripe.com/docs/webhooks)")
	if signature != "" {
		req.Header.Set(billing.SignatureHeader, signature)
	}
	rec := httptest.NewRecorder()
	e.reg.ServeHTTP(rec, req)
	return rec
}

// mustHook posts an event that must be accepted.
func (e *netEnv) mustHook(id, typ string, created int, obj string) {
	e.t.Helper()
	if rec := e.hook(hookSecret, id, typ, created, obj); rec.Code != http.StatusOK || rec.Body.String() != `{"received":true}`+"\n" {
		e.t.Fatalf("webhook %s: %d %q", id, rec.Code, rec.Body.String())
	}
}

// result waits for an event to be processed and returns its result.
func (e *netEnv) result(id string) string {
	e.t.Helper()
	var res string
	waitFor(e.t, "event "+id, func() bool {
		var done bool
		err := e.n.db.QueryRow(e.ctx, `SELECT processed_at IS NOT NULL, result FROM billing_events WHERE id = $1`, id).Scan(&done, &res)
		return err == nil && done
	})
	return res
}

// failed waits for an event to fail attempts times.
func (e *netEnv) failed(id string, attempts int) BillingEvent {
	e.t.Helper()
	var ev BillingEvent
	waitFor(e.t, fmt.Sprintf("event %s to fail %d times", id, attempts), func() bool {
		evs, _, err := e.n.BillingProblems(e.ctx, 50, 0)
		if err != nil {
			e.t.Fatal(err)
		}
		for _, x := range evs {
			if x.ID == id && x.Attempts == attempts && x.Result != "" && x.ProcessedAt == nil {
				ev = x
				return true
			}
		}
		return false
	})
	return ev
}

func (e *netEnv) events() int {
	e.t.Helper()
	var n int
	if err := e.n.db.QueryRow(e.ctx, `SELECT count(*) FROM billing_events`).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *netEnv) subscription(siteID int64) *Subscription {
	e.t.Helper()
	sub, err := e.n.SiteSubscription(e.ctx, e.row(siteID))
	if err != nil {
		e.t.Fatal(err)
	}
	return sub
}

func metadataJSON(form url.Values) string {
	md := map[string]string{}
	for k, v := range form {
		if name, ok := strings.CutPrefix(k, "metadata["); ok {
			md[strings.TrimSuffix(name, "]")] = v[0]
		}
	}
	b, _ := json.Marshal(md)
	return string(b)
}

func sessionJSON(id, sub string, siteID int64, md string) string {
	return fmt.Sprintf(`{"id":%q,"object":"checkout.session","mode":"subscription","status":"complete","payment_status":"paid",`+
		`"customer":"cus_test0001","subscription":%q,"client_reference_id":"site:%d","metadata":%s,"livemode":false}`, id, sub, siteID, md)
}

func subJSON(id, status, md string) string {
	return fmt.Sprintf(`{"id":%q,"object":"subscription","customer":"cus_test0001","status":%q,"metadata":%s,"livemode":false}`, id, status, md)
}

func invoiceJSON(id, sub, md string) string {
	return fmt.Sprintf(`{"id":%q,"object":"invoice","customer":"cus_test0001","status":"open","livemode":false,`+
		`"parent":{"type":"subscription_details","subscription_details":{"subscription":%q,"metadata":%s}}}`, id, sub, md)
}

// paidInvoiceJSON is a paid invoice of total cents for the period ending
// at end.
func paidInvoiceJSON(id, sub, md string, total int, end time.Time) string {
	return fmt.Sprintf(`{"id":%q,"object":"invoice","customer":"cus_test0001","status":"paid","total":%d,"amount_paid":%d,`+
		`"livemode":false,"lines":{"object":"list","data":[{"period":{"start":%d,"end":%d}}]},`+
		`"parent":{"type":"subscription_details","subscription_details":{"subscription":%q,"metadata":%s}}}`,
		id, total, total, end.Add(-30*24*time.Hour).Unix(), end.Unix(), sub, md)
}

// periodEnd makes Stripe answer a subscription's current period as
// ending at end.
func (f *fakeStripe) periodEnd(id string, end time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subs[id].end = end
}

// upgrade sends the owner's upgrade form and returns the response.
func (b *browser) upgrade(csrf string, siteID int64, offer string, extra ...string) *httptest.ResponseRecorder {
	b.e.t.Helper()
	form := url.Values{"csrf": {csrf}, "offer": {offer}}
	for i := 0; i+1 < len(extra); i += 2 {
		form.Set(extra[i], extra[i+1])
	}
	return b.do("POST", portalURL+"/admin/sites/"+strconv.FormatInt(siteID, 10)+"/upgrade", form)
}

// subscribe takes a site through Checkout to an active subscription sub
// and returns the metadata Stripe copies to it.
func (e *netEnv) subscribe(fs *fakeStripe, s *Site, offer, sub, price string) string {
	e.t.Helper()
	b := e.browser()
	csrf := b.portalSignIn(s.OwnerID)
	n := fs.sessions()
	if rec := b.upgrade(csrf, s.ID, offer); rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "https://checkout.stripe.com/") {
		e.t.Fatalf("upgrade %s: %d %s", s.Slug, rec.Code, rec.Header().Get("Location"))
	}
	form, _ := fs.checkout(n)
	md := metadataJSON(form)
	fs.set(sub, "active", price)
	e.mustHook("evt_done_"+sub, "checkout.session.completed", 10, sessionJSON("cs_"+sub, sub, s.ID, md))
	if res := e.result("evt_done_" + sub); res != resultApplied {
		e.t.Fatalf("checkout of %s: %s", s.Slug, res)
	}
	return md
}

// TestBillingStart: the configured prices must be the key's (mode, active,
// recurring) for paid plans, or the server does not start.
func TestBillingStart(t *testing.T) {
	f := setup(t)
	log := testutil.Log()
	try := func(key string, change func(*config.Stripe), prices ...string) (*Billing, error) {
		fs := newFakeStripe(t, key)
		if prices != nil {
			fs.prices = prices
		}
		cfg := *f.cfg
		cfg.Stripe = testStripe()
		cfg.Stripe.SecretKey = key
		if change != nil {
			change(&cfg.Stripe)
		}
		return NewBilling(f.ctx, f.n, &cfg, log, fs.options()...)
	}

	b, err := try(testStripeKey, nil)
	if err != nil || b == nil {
		t.Fatalf("a valid configuration: %v", err)
	}
	if got := b.Prices(); len(got) != 3 || got[0] != (Price{Plan: "pro", ID: "price_pro_month", Amount: 900, Currency: "eur", TaxBehavior: "inclusive"}) ||
		got[1].ID != "price_pro_year" || !got[1].Yearly || got[1].Amount != 9900 || got[2].Plan != "business" || got[2].TaxBehavior != "exclusive" {
		t.Fatalf("prices: %+v", got)
	}
	// Without a key there is no billing.
	cfg := *f.cfg
	if b, err := NewBilling(f.ctx, f.n, &cfg, log); b != nil || err != nil {
		t.Fatalf("no key: %v, %v", b, err)
	}

	for name, c := range map[string]struct {
		key    string
		change func(*config.Stripe)
		prices []string
		want   string
	}{
		// A live key with test prices: Stripe would not even list them;
		// a fake that does is refused as well.
		"live key, test prices": {key: "sk_live_fake0001", want: "is not a live-mode price"},
		"live key, prices of the test account": {key: "sk_live_fake0001", prices: []string{priceJSON("price_other", true, 100, "month")},
			want: "not an active recurring price of the Stripe account in live mode"},
		"test key, live prices": {key: testStripeKey, prices: []string{priceJSON("price_pro_month", true, 1900, "month"),
			priceJSON("price_pro_year", true, 19000, "year"), priceJSON("price_biz_month", true, 4900, "month")}, want: "is not a test-mode price"},
		"an unknown price": {key: testStripeKey, change: func(s *config.Stripe) { s.Prices["pro"] = config.StripePrices{Monthly: "price_gone"} },
			want: "price_gone (plan pro) is not an active recurring price"},
		"a free plan": {key: testStripeKey, change: func(s *config.Stripe) { s.Prices["personal"] = config.StripePrices{Monthly: "price_other"} },
			want: "no paid plan \"personal\""},
		"no such plan": {key: testStripeKey, change: func(s *config.Stripe) { s.Prices["agency"] = config.StripePrices{Monthly: "price_other"} },
			want: "no paid plan \"agency\""},
		"one price for two plans": {key: testStripeKey, change: func(s *config.Stripe) {
			s.Prices["business"] = config.StripePrices{Monthly: "price_pro_month"}
		}, want: "configured for both"},
	} {
		b, err := try(c.key, c.change, c.prices...)
		if b != nil || err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, %v; want an error with %q", name, b, err, c.want)
		}
		if err != nil && strings.Contains(err.Error(), "sk_") {
			t.Errorf("%s: the error shows the key: %v", name, err)
		}
	}
}

// TestBillingOff: without a key the portal has no billing at all.
func TestBillingOff(t *testing.T) {
	e, _ := newOpenPortal(t)
	a := e.personal("alpha")
	e.mfa(e.owner)
	b := e.browser()
	csrf := b.portalSignIn(e.owner)
	if body := b.page("/admin/sites"); strings.Contains(body, "/upgrade") || strings.Contains(body, "Mejorar un sitio") || strings.Contains(body, "checkout.stripe.com") {
		t.Fatal("My sites offers upgrades without billing")
	}
	if rec := b.get(portalURL + "/admin/sites"); strings.Contains(rec.Header().Get("Content-Security-Policy"), "stripe.com") {
		t.Fatal("the CSP allows Stripe without billing")
	}
	if rec := b.upgrade(csrf, a.ID, "pro:month"); rec.Code == http.StatusSeeOther || rec.Code == http.StatusOK {
		t.Fatalf("an upgrade without billing: %d", rec.Code)
	}
	if rec := e.postHook(portalURL, `{}`, "t=1,v1=00", "application/json"); rec.Code == http.StatusOK {
		t.Fatalf("a webhook without billing: %d", rec.Code)
	}
	_, nb, _ := e.networkAdmin("nora")
	if rec := nb.get(portalURL + "/admin/network/billing"); rec.Code != http.StatusNotFound {
		t.Fatalf("the billing events without billing: %d", rec.Code)
	}
	if body := nb.page("/admin/network/sites"); strings.Contains(body, "/admin/network/billing") {
		t.Fatal("the menu links the billing events without billing")
	}
}

// TestBillingCheckout: the upgrade form starts a Checkout Session whose
// price the server picks, for a site the account owns.
func TestBillingCheckout(t *testing.T) {
	e, fs, _ := newBillingEnv(t)
	a := e.personal("alpha")
	b := e.browser()
	csrf := b.portalSignIn(e.owner)

	body := b.page("/admin/sites")
	for _, want := range []string{`action="/admin/sites/` + strconv.FormatInt(a.ID, 10) + `/upgrade"`, `value="pro:month"`, `value="pro:year"`,
		`value="business:month"`, "Pro · 9\u00a0€ al mes · IVA incluido", "Pro · 99\u00a0€ al año · IVA incluido", "Business · 30\u00a0€ al mes · &#43; IVA"} {
		if !strings.Contains(body, want) {
			t.Fatalf("My sites has no %s:\n%s", want, body)
		}
	}
	if strings.Contains(body, "price_") {
		t.Fatal("My sites shows a Stripe price ID")
	}
	if csp := b.get(portalURL + "/admin/sites").Header().Get("Content-Security-Policy"); !strings.Contains(csp, "form-action 'self' https://checkout.stripe.com https://billing.stripe.com") {
		t.Fatalf("the CSP of My sites: %s", csp)
	}
	if csp := b.get(portalURL + "/admin/profile").Header().Get("Content-Security-Policy"); strings.Contains(csp, "stripe.com") {
		t.Fatalf("the CSP of another page allows Stripe: %s", csp)
	}

	// The browser picks a plan and an interval; a price it sends is
	// ignored.
	rec := b.upgrade(csrf, a.ID, "pro:month", "price", "price_biz_month", "price_id", "price_other")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "https://checkout.stripe.com/c/pay/cs_test_1" {
		t.Fatalf("upgrade: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	form, key := fs.checkout(0)
	var row int64
	if err := e.n.db.QueryRow(e.ctx, `SELECT id FROM subscriptions WHERE checkout_site_id = $1 AND stripe_subscription_id IS NULL`, a.ID).Scan(&row); err != nil {
		t.Fatalf("no pending subscription: %v", err)
	}
	site, acct, rowID := strconv.FormatInt(a.ID, 10), strconv.FormatInt(e.owner, 10), strconv.FormatInt(row, 10)
	for k, want := range map[string]string{
		"mode": "subscription", "line_items[0][price]": "price_pro_month", "line_items[0][quantity]": "1",
		"client_reference_id": "site:" + site,
		"metadata[site_id]":   site, "metadata[account_id]": acct, "metadata[plan]": "pro", "metadata[subscription]": rowID,
		"subscription_data[metadata][site_id]": site, "subscription_data[metadata][subscription]": rowID,
		"subscription_data[trial_period_days]": "14",
		"automatic_tax[enabled]":               "true", "tax_id_collection[enabled]": "true", "billing_address_collection": "required",
		"customer_email": "owner@example.com", "locale": "es",
		"success_url": "https://sap13ns.site/admin/sites?billing=success", "cancel_url": "https://sap13ns.site/admin/sites?billing=canceled",
	} {
		if got := form.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if !strings.HasPrefix(key, "sap13ns-co-") {
		t.Fatalf("idempotency key %q", key)
	}
	// A double submission is the same request to Stripe.
	if rec := b.upgrade(csrf, a.ID, "pro:month"); rec.Code != http.StatusSeeOther {
		t.Fatalf("again: %d", rec.Code)
	}
	if form2, key2 := fs.checkout(1); key2 != key || form2.Get("metadata[subscription]") != rowID {
		t.Fatalf("a resubmission: key %s (was %s), row %s", key2, key, form2.Get("metadata[subscription]"))
	}
	// Another interval is another request.
	b.upgrade(csrf, a.ID, "pro:year")
	if form3, key3 := fs.checkout(2); key3 == key || form3.Get("line_items[0][price]") != "price_pro_year" {
		t.Fatalf("yearly: %s %s", key3, form3.Get("line_items[0][price]"))
	}

	// What cannot be bought.
	for offer, msg := range map[string]string{
		"personal:month": "Ese plan no se puede comprar.", "business:year": "Ese plan no se puede comprar.",
		"agency:month": "Ese plan no se puede comprar.", "price_pro_month:month": "Ese plan no se puede comprar.",
		"pro:week": "Solicitud no válida.", "pro": "Solicitud no válida.", "": "Solicitud no válida.",
	} {
		n := fs.sessions()
		b.post("/admin/sites/"+site+"/upgrade", "/admin/sites", url.Values{"csrf": {csrf}, "offer": {offer}})
		if !strings.Contains(b.page("/admin/sites"), msg) || fs.sessions() != n {
			t.Errorf("offer %q: no %q, or a session was created", offer, msg)
		}
	}
	// Without the CSRF token, nothing.
	if rec := b.do("POST", portalURL+"/admin/sites/"+site+"/upgrade", url.Values{"offer": {"pro:month"}}); rec.Code != http.StatusForbidden {
		t.Fatalf("without CSRF: %d", rec.Code)
	}

	// Only the owner, and only of an open tenant site.
	ann := e.account("ann")
	if err := e.n.SetMember(e.ctx, a.ID, ann, "admin", 0); err != nil {
		t.Fatal(err)
	}
	e.mfa(ann)
	ab := e.browser()
	acsrf := ab.portalSignIn(ann)
	n := fs.sessions()
	for _, id := range []int64{a.ID, e.portalID(), 99999} {
		if rec := ab.upgrade(acsrf, id, "pro:month"); rec.Code != http.StatusNotFound {
			t.Fatalf("ann upgrades site %d: %d", id, rec.Code)
		}
		if rec := ab.do("POST", portalURL+"/admin/sites/"+strconv.FormatInt(id, 10)+"/billing", url.Values{"csrf": {acsrf}}); rec.Code != http.StatusNotFound {
			t.Fatalf("ann manages the billing of site %d: %d", id, rec.Code)
		}
	}
	if strings.Contains(ab.page("/admin/sites"), "/upgrade") {
		t.Fatal("a member who does not own the site is offered the upgrade")
	}
	// A Personal site waiting for approval does not skip it by paying.
	p := e.site("waiting", e.owner, "personal")
	b.post("/admin/sites/"+strconv.FormatInt(p.ID, 10)+"/upgrade", "/admin/sites", url.Values{"csrf": {csrf}, "offer": {"pro:month"}})
	if !strings.Contains(b.page("/admin/sites"), "Este sitio no se puede suscribir ahora") || fs.sessions() != n {
		t.Fatal("a site waiting for approval was sent to Checkout")
	}

	// The second factor first: Checkout and the Customer Portal are the
	// billing of a site, which its owner administers with one.
	nob := e.bareAccount("nob")
	ns := e.site("nobsite", nob, "personal")
	nb := e.browser()
	ncsrf := nb.portalSignIn(nob)
	if rec := nb.upgrade(ncsrf, ns.ID, "pro:month"); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/profile/2fa" {
		t.Fatalf("an owner without a second factor: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if fs.sessions() != n {
		t.Fatal("a session was created")
	}

	// "Manage billing" needs a Stripe customer.
	b.post("/admin/sites/"+site+"/billing", "/admin/sites", url.Values{"csrf": {csrf}})
	if !strings.Contains(b.page("/admin/sites"), "Este sitio aún no tiene facturación.") {
		t.Fatal("Manage billing without a customer")
	}
	if got := audits(t, e.portal, "network.checkout_started"); got != 3 {
		t.Fatalf("%d checkouts audited", got)
	}
}

// TestBillingWebhook: what the webhook accepts, where, and that it stores
// every event once.
func TestBillingWebhook(t *testing.T) {
	e, fs, _ := newBillingEnv(t)
	a := e.personal("alpha")
	md := e.subscribe(fs, a, "pro:month", "sub_test0001", "price_pro_month")
	if sub := e.subscription(a.ID); sub == nil || sub.State != "active" || sub.Plan != "pro" || sub.StripeCustomerID != "cus_test0001" ||
		sub.StripePriceID != "price_pro_month" || sub.CheckoutSiteID != nil || sub.CurrentPeriodEnd == nil {
		t.Fatalf("subscription: %+v", sub)
	}
	if s := e.row(a.ID); s.Plan != "pro" {
		t.Fatalf("plan %s", s.Plan)
	}
	if c, _ := e.n.AccountCustomer(e.ctx, e.owner); c != "cus_test0001" {
		t.Fatalf("customer %q", c)
	}
	stored := e.events()

	obj := subJSON("sub_test0001", "active", md)
	payload := `{"id":"evt_sig","object":"event","created":1,"livemode":false,"type":"customer.subscription.updated","data":{"object":` + obj + `}}`
	now := e.clock.now()
	for name, c := range map[string]struct {
		sig, ct string
		code    int
	}{
		"no signature":     {"", "application/json", 400},
		"another secret":   {billing.SignPayload([]byte(payload), "whsec_wrong", now), "application/json", 400},
		"an old signature": {billing.SignPayload([]byte(payload), hookSecret, now.Add(-time.Hour)), "application/json", 400},
		"another payload":  {billing.SignPayload([]byte(payload+" "), hookSecret, now), "application/json", 400},
		"a form":           {billing.SignPayload([]byte(payload), hookSecret, now), "application/x-www-form-urlencoded", 415},
		"a garbled header": {"t=abc,v1=zz", "application/json", 400},
		"a v0 signature":   {strings.Replace(billing.SignPayload([]byte(payload), hookSecret, now), "v1=", "v0=", 1), "application/json", 400},
		"no timestamp":     {"v1=" + strings.Repeat("0", 64), "application/json", 400},
	} {
		if rec := e.postHook(portalURL, payload, c.sig, c.ct); rec.Code != c.code {
			t.Errorf("%s: %d, want %d", name, rec.Code, c.code)
		}
	}
	if rec := e.postHook(portalURL, strings.Repeat(" ", billing.MaxWebhookPayload+10), billing.SignPayload([]byte("x"), hookSecret, now), "application/json"); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("a large payload: %d", rec.Code)
	}
	// Only the portal has the webhook.
	if rec := e.postHook("https://"+a.PrimaryHost, payload, billing.SignPayload([]byte(payload), hookSecret, now), "application/json"); rec.Code == http.StatusOK {
		t.Fatalf("a tenant's webhook: %d", rec.Code)
	}
	if got := e.events(); got != stored {
		t.Fatalf("%d events stored, want %d", got, stored)
	}

	// The previous secret still works during a rotation; a duplicate is
	// stored and applied once.
	before := fs.fetches("sub_test0001")
	for range 3 {
		if rec := e.hook(hookOld, "evt_dup", "customer.subscription.updated", 20, obj); rec.Code != http.StatusOK {
			t.Fatalf("a rotated secret: %d", rec.Code)
		}
	}
	if res := e.result("evt_dup"); res != resultApplied {
		t.Fatalf("evt_dup: %s", res)
	}
	e.billing().ProcessDue(e.ctx)
	if got := e.events(); got != stored+1 {
		t.Fatalf("%d events stored after a duplicate, want %d", got, stored+1)
	}
	if got := fs.fetches("sub_test0001"); got != before+1 {
		t.Fatalf("the duplicate was applied %d times", got-before)
	}

	// An event about nothing of this network is answered 200, recorded
	// and listed for the network admins.
	e.mustHook("evt_alien", "customer.subscription.updated", 30, subJSON("sub_alien0001", "active", `{}`))
	e.mustHook("evt_forged", "customer.subscription.updated", 31,
		subJSON("sub_forged001", "active", `{"site_id":"`+strconv.FormatInt(a.ID, 10)+`","account_id":"999","subscription":"1"}`))
	e.mustHook("evt_other", "charge.refunded", 32, `{"id":"ch_1","object":"charge"}`)
	livePayload := `{"id":"evt_live","object":"event","created":1,"livemode":true,"type":"invoice.paid","data":{"object":` + invoiceJSON("in_live", "sub_test0001", md) + `}}`
	if rec := e.postHook(portalURL, livePayload, billing.SignPayload([]byte(livePayload), hookSecret, now), "application/json"); rec.Code != 200 {
		t.Fatalf("a live event: %d", rec.Code)
	}
	for id, want := range map[string]string{"evt_alien": "unmapped: no site", "evt_forged": "unmapped: its metadata names no subscription",
		"evt_other": "ignored", "evt_live": "ignored: another mode"} {
		if res := e.result(id); !strings.HasPrefix(res, want) {
			t.Errorf("%s: %q, want %q", id, res, want)
		}
	}
	if sub := e.subscription(a.ID); sub.StripeSubscriptionID != "sub_test0001" {
		t.Fatalf("a forged event moved the site: %+v", sub)
	}
	_, nb, ncsrf := e.networkAdmin("nora")
	body := nb.page("/admin/network/billing")
	if !strings.Contains(body, "evt_alien") || !strings.Contains(body, "evt_forged") || strings.Contains(body, "evt_other") ||
		strings.Contains(body, "evt_dup") || !strings.Contains(body, "Sin asignar") {
		t.Fatalf("billing events:\n%s", body)
	}
	if strings.Contains(body, "cus_test0001") || strings.Contains(body, `"site_id"`) {
		t.Fatal("the events page shows payloads")
	}
	// A retry runs it again, and it stays listed while it maps nothing.
	nb.post("/admin/network/billing/evt_alien/retry", "/admin/network/billing", url.Values{"csrf": {ncsrf}})
	if res := e.result("evt_alien"); !strings.HasPrefix(res, "unmapped") {
		t.Fatalf("retried: %s", res)
	}
	nb.post("/admin/network/billing/evt_dup/retry", "/admin/network/billing", url.Values{"csrf": {ncsrf}})
	if !strings.Contains(nb.page("/admin/network/billing"), "evt_alien") {
		t.Fatal("the retried event is not listed")
	}
	// Owners do not see the network's events.
	ob := e.browser()
	ob.portalSignIn(e.owner)
	if rec := ob.get(portalURL + "/admin/network/billing"); rec.Code == http.StatusOK {
		t.Fatalf("an owner opens the billing events: %d", rec.Code)
	}
}

// TestBillingOrder: whatever order Stripe delivers the events in, the
// network ends in Stripe's current state, which it fetches.
func TestBillingOrder(t *testing.T) {
	e, fs, _ := newBillingEnv(t)
	a := e.personal("alpha")
	b := e.browser()
	csrf := b.portalSignIn(e.owner)
	b.upgrade(csrf, a.ID, "business:month")
	form, _ := fs.checkout(0)
	md := metadataJSON(form)
	fs.set("sub_test0002", "active", "price_biz_month")

	// The newest first, then a stale snapshot, then the completion.
	e.mustHook("evt_updated", "customer.subscription.updated", 30, subJSON("sub_test0002", "active", md))
	e.mustHook("evt_created", "customer.subscription.created", 10, subJSON("sub_test0002", "incomplete", md))
	e.mustHook("evt_completed", "checkout.session.completed", 20, sessionJSON("cs_test_1", "sub_test0002", a.ID, md))
	e.mustHook("evt_failed_late", "invoice.payment_failed", 15, invoiceJSON("in_1", "sub_test0002", md))
	for _, id := range []string{"evt_updated", "evt_created", "evt_completed", "evt_failed_late"} {
		if res := e.result(id); res != resultApplied {
			t.Fatalf("%s: %s", id, res)
		}
	}
	sub := e.subscription(a.ID)
	if sub == nil || sub.State != "active" || sub.Status != "active" || sub.Plan != "business" || sub.GraceSince != nil {
		t.Fatalf("subscription: %+v", sub)
	}
	if s := e.row(a.ID); s.Plan != "business" {
		t.Fatalf("plan %s", s.Plan)
	}
	e.served(a.PrimaryHost, http.StatusOK)

	// The same Checkout paid twice (two tabs): the second subscription is
	// kept apart, for the operator, and the site stays on the first.
	fs.set("sub_test0003", "active", "price_biz_month")
	e.mustHook("evt_twice", "checkout.session.completed", 40, sessionJSON("cs_test_2", "sub_test0003", a.ID, md))
	if res := e.result("evt_twice"); !strings.HasPrefix(res, "unmapped: a second subscription") {
		t.Fatalf("paid twice: %s", res)
	}
	if got := e.subscription(a.ID); got.StripeSubscriptionID != "sub_test0002" {
		t.Fatalf("the site moved to %s", got.StripeSubscriptionID)
	}
	var dup int
	if err := e.n.db.QueryRow(e.ctx, `SELECT count(*) FROM subscriptions WHERE stripe_subscription_id = 'sub_test0003'
		AND NOT EXISTS (SELECT 1 FROM sites WHERE subscription_id = subscriptions.id)`).Scan(&dup); err != nil || dup != 1 {
		t.Fatalf("the second subscription: %d %v", dup, err)
	}

	// A plan switch in the Customer Portal (a downgrade included) gives the
	// site the new plan; nothing else changes.
	fs.set("sub_test0002", "active", "price_pro_month")
	e.mustHook("evt_switch", "customer.subscription.updated", 50, subJSON("sub_test0002", "active", md))
	if res := e.result("evt_switch"); res != resultApplied || e.row(a.ID).Plan != "pro" {
		t.Fatalf("switch: %s, plan %s", res, e.row(a.ID).Plan)
	}
}

// TestBillingGrace: a failed renewal tells the owner, holds the site after
// the grace days and invoice.paid lifts it; a canceled subscription holds
// it at once. Nothing is deleted.
func TestBillingGrace(t *testing.T) {
	e, fs, box := newBillingEnv(t)
	a := e.personal("alpha")
	publish(t, e.siteStore(a), "Hello", "hello")
	md := e.subscribe(fs, a, "pro:month", "sub_test0004", "price_pro_month")
	e.refresh()
	e.served(a.PrimaryHost, http.StatusOK)

	fs.set("sub_test0004", "past_due", "price_pro_month")
	e.mustHook("evt_fail", "invoice.payment_failed", 20, invoiceJSON("in_2", "sub_test0004", md))
	if res := e.result("evt_fail"); res != resultApplied {
		t.Fatalf("payment failed: %s", res)
	}
	sub := e.subscription(a.ID)
	if sub.State != "grace" || sub.Status != "past_due" || sub.GraceSince == nil || !sub.GraceSince.Equal(e.clock.now()) {
		t.Fatalf("in grace: %+v", sub)
	}
	waitFor(t, "the e-mail", func() bool { return strings.Contains(box.String(), "Ha fallado el pago de alpha.sap13ns.site") })
	if mails := box.String(); !strings.Contains(mails, "to=owner@example.com") || !strings.Contains(mails, "https://sap13ns.site/admin/sites") ||
		!strings.Contains(mails, "7 días") {
		t.Fatalf("the e-mail:\n%s", mails)
	}
	// Grace keeps its start when Stripe retries and fails again.
	e.clock.add(3 * 24 * time.Hour)
	e.mustHook("evt_fail2", "invoice.payment_failed", 21, invoiceJSON("in_2", "sub_test0004", md))
	e.result("evt_fail2")
	if got := e.subscription(a.ID); got.GraceSince == nil || !got.GraceSince.Equal(*sub.GraceSince) {
		t.Fatalf("grace moved: %v, was %v", got.GraceSince, sub.GraceSince)
	}
	e.refresh()
	e.served(a.PrimaryHost, http.StatusOK)

	// Held after the grace days: the public pages, not the sign-in.
	e.clock.add(4*24*time.Hour - time.Minute)
	e.refresh()
	e.served(a.PrimaryHost, http.StatusOK)
	e.clock.add(time.Minute)
	e.refresh()
	// "Paused", in the site's language (Spanish), and nothing about its
	// billing for the visitors.
	rec := e.do("GET", "https://"+a.PrimaryHost+"/", func(r *http.Request) { r.Header.Set("Accept-Language", "en-US,en;q=0.9") })
	if body := rec.Body.String(); rec.Code != http.StatusServiceUnavailable || !strings.Contains(body, "Este sitio está en pausa") ||
		!strings.Contains(body, "Volverá pronto. Si es tu sitio, inicia sesión para reactivarlo.") || !strings.Contains(body, `lang="es"`) ||
		strings.Contains(body, "suspend") || strings.Contains(body, "pago") || strings.Contains(body, "factura") {
		t.Fatalf("held: %d\n%s", rec.Code, body)
	}
	// A site in English with a Spanish version: Spanish for whoever
	// prefers it, English for the others, once the languages are read
	// again.
	st := e.siteStore(a)
	set, err := st.LoadSettings(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	set.Language, set.Languages = "en", []string{"es"}
	if err := st.SaveSettings(e.ctx, set); err != nil {
		t.Fatal(err)
	}
	e.clock.add(siteLangsTTL)
	e.refresh()
	for accept, want := range map[string]string{"": "This site is paused", "fr-FR,fr;q=0.9": "This site is paused",
		"es-ES,es;q=0.9": "Este sitio está en pausa", "de,es;q=0.5": "Este sitio está en pausa"} {
		rec := e.do("GET", "https://"+a.PrimaryHost+"/blog/x", func(r *http.Request) { r.Header.Set("Accept-Language", accept) })
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("held, Accept-Language %q: %d, want %q", accept, rec.Code, want)
		}
	}
	if rec := e.do("GET", "https://"+a.PrimaryHost+"/login"); rec.Code != http.StatusOK {
		t.Fatalf("the sign-in of a held site: %d", rec.Code)
	}
	ob := e.browser()
	ob.portalSignIn(e.owner)
	if body := ob.page("/admin/sites"); !strings.Contains(body, "Retenido por facturación") || !strings.Contains(body, "Pago fallido") ||
		!strings.Contains(body, `/billing"`) {
		t.Fatal("My sites does not show the hold")
	}
	_, nb, _ := e.networkAdmin("nora")
	if body := nb.page(siteURL(a.ID, "")); !strings.Contains(body, "Retenido por facturación") || !strings.Contains(body, "past_due") {
		t.Fatal("the network admin's site page does not show the hold")
	}

	// "Manage billing" opens the customer's portal.
	ocsrf := ob.portalSignIn(e.owner)
	rec = ob.do("POST", portalURL+"/admin/sites/"+strconv.FormatInt(a.ID, 10)+"/billing", url.Values{"csrf": {ocsrf}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "https://billing.stripe.com/p/session/test_1" {
		t.Fatalf("manage billing: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	fs.mu.Lock()
	portal := fs.portals[0]
	fs.mu.Unlock()
	if portal.Get("customer") != "cus_test0001" || portal.Get("return_url") != "https://sap13ns.site/admin/sites" {
		t.Fatalf("portal session: %v", portal)
	}

	// invoice.paid lifts the hold.
	fs.set("sub_test0004", "active", "price_pro_month")
	e.mustHook("evt_paid", "invoice.paid", 30, invoiceJSON("in_2", "sub_test0004", md))
	if res := e.result("evt_paid"); res != resultApplied {
		t.Fatalf("paid: %s", res)
	}
	if got := e.subscription(a.ID); got.State != "active" || got.GraceSince != nil {
		t.Fatalf("after invoice.paid: %+v", got)
	}
	e.refresh()
	e.served(a.PrimaryHost, http.StatusOK)

	// Canceled without a paid period on record (the invoices of this test
	// do not say they were paid): held at once, its data kept, and it can
	// subscribe again.
	fs.set("sub_test0004", "canceled", "price_pro_month")
	e.mustHook("evt_deleted", "customer.subscription.deleted", 40, subJSON("sub_test0004", "canceled", md))
	if res := e.result("evt_deleted"); res != resultApplied {
		t.Fatalf("deleted: %s", res)
	}
	e.refresh()
	if rec := e.do("GET", "https://"+a.PrimaryHost+"/"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("canceled: %d", rec.Code)
	}
	if s := e.row(a.ID); s.Status != StatusActive || s.Plan != "pro" || s.SubscriptionID == nil {
		t.Fatalf("canceled site: %+v", s)
	}
	if p, err := e.siteStore(a).PostBySlug(e.ctx, "post", "es", "hello"); err != nil || p == nil {
		t.Fatalf("the posts of a canceled site: %v", err)
	}
	if body := ob.page("/admin/sites"); !strings.Contains(body, `action="/admin/sites/`+strconv.FormatInt(a.ID, 10)+`/upgrade"`) ||
		!strings.Contains(body, "Suscripción terminada") {
		t.Fatal("a canceled site cannot subscribe again")
	}

	// Without grace days a failed renewal holds at once.
	if graceOf(config.Stripe{SecretKey: "sk_test_x"}) != time.Nanosecond || graceOf(config.Stripe{GraceDays: 3}) != 72*time.Hour {
		t.Fatal("graceOf")
	}
}

// TestBillingFailureNoticeOnce: Smart Retries send invoice.payment_failed
// for every attempt that fails; the owner is e-mailed once per grace
// period, at the first failure, whatever event started the grace.
func TestBillingFailureNoticeOnce(t *testing.T) {
	e, fs, box := newBillingEnv(t)
	a := e.personal("alpha")
	md := e.subscribe(fs, a, "pro:month", "sub_test0020", "price_pro_month")
	mails := func() int { return strings.Count(box.String(), "Ha fallado el pago de alpha.sap13ns.site") }
	// The processor applies events one after the other and sends the
	// e-mails of one before the next: once a later event is done, the
	// e-mails of the earlier ones are sent.
	n := 0
	settle := func() {
		t.Helper()
		n++
		id := "evt_settle_" + strconv.Itoa(n)
		e.mustHook(id, "customer.created", 999, `{"id":"cus_test0001","object":"customer"}`)
		e.result(id)
	}

	// The subscription's update arrives first and starts the grace; the
	// invoice's failure still tells the owner.
	fs.set("sub_test0020", "past_due", "price_pro_month")
	e.mustHook("evt_updated", "customer.subscription.updated", 20, subJSON("sub_test0020", "past_due", md))
	e.result("evt_updated")
	e.mustHook("evt_fail_1", "invoice.payment_failed", 21, invoiceJSON("in_1", "sub_test0020", md))
	e.result("evt_fail_1")
	settle()
	one := mails()
	if one == 0 {
		t.Fatalf("no e-mail for the first failure:\n%s", box.String())
	}
	// Three retries fail: no more e-mails.
	for i := range 3 {
		e.clock.add(24 * time.Hour)
		id := "evt_retry_" + strconv.Itoa(i)
		e.mustHook(id, "invoice.payment_failed", 22+i, invoiceJSON("in_1", "sub_test0020", md))
		if res := e.result(id); res != resultApplied {
			t.Fatalf("retry %d: %s", i, res)
		}
	}
	settle()
	if got := mails(); got != one {
		t.Fatalf("%d e-mails after the retries, want %d", got, one)
	}

	// Paid, then a later renewal fails: another grace, another e-mail.
	fs.set("sub_test0020", "active", "price_pro_month")
	e.mustHook("evt_paid", "invoice.paid", 30, invoiceJSON("in_1", "sub_test0020", md))
	e.result("evt_paid")
	e.clock.add(30 * 24 * time.Hour)
	fs.set("sub_test0020", "past_due", "price_pro_month")
	e.mustHook("evt_fail_2", "invoice.payment_failed", 40, invoiceJSON("in_2", "sub_test0020", md))
	e.result("evt_fail_2")
	e.mustHook("evt_fail_2b", "invoice.payment_failed", 41, invoiceJSON("in_2", "sub_test0020", md))
	e.result("evt_fail_2b")
	settle()
	if got := mails(); got != 2*one {
		t.Fatalf("%d e-mails after the second grace, want %d", got, 2*one)
	}
}

// TestBillingCanceledEarly: a subscription canceled at once (from the
// Stripe Dashboard) keeps its site served until the end of the period it
// paid for, and the host table holds it once that end passes; one that
// ends with its period, or after a failed renewal, holds it at once.
func TestBillingCanceledEarly(t *testing.T) {
	e, fs, _ := newBillingEnv(t)
	a := e.personal("alpha")
	md := e.subscribe(fs, a, "pro:month", "sub_test0010", "price_pro_month")
	end := e.clock.now().Add(30 * 24 * time.Hour)
	fs.periodEnd("sub_test0010", end)

	// A trial's invoice charges nothing and pays for no time.
	e.mustHook("evt_trial", "invoice.paid", 11, paidInvoiceJSON("in_0", "sub_test0010", md, 0, end))
	if res := e.result("evt_trial"); res != resultApplied || e.subscription(a.ID).PaidUntil != nil {
		t.Fatalf("a trial's invoice: %s, paid until %v", res, e.subscription(a.ID).PaidUntil)
	}
	e.mustHook("evt_paid", "invoice.paid", 12, paidInvoiceJSON("in_1", "sub_test0010", md, 900, end))
	if res := e.result("evt_paid"); res != resultApplied {
		t.Fatalf("paid: %s", res)
	}
	if got := e.subscription(a.ID); got.PaidUntil == nil || !got.PaidUntil.Equal(end) {
		t.Fatalf("paid until %v, want %v", got.PaidUntil, end)
	}
	// A late delivery of an older invoice does not move it back.
	e.mustHook("evt_paid_old", "invoice.paid", 13, paidInvoiceJSON("in_old", "sub_test0010", md, 900, end.Add(-30*24*time.Hour)))
	e.result("evt_paid_old")
	if got := e.subscription(a.ID); !got.PaidUntil.Equal(end) {
		t.Fatalf("an older invoice moved the paid period to %v", got.PaidUntil)
	}

	// Canceled at once, ten days into the month: still served.
	e.clock.add(10 * 24 * time.Hour)
	fs.set("sub_test0010", "canceled", "price_pro_month")
	e.mustHook("evt_deleted", "customer.subscription.deleted", 20, subJSON("sub_test0010", "canceled", md))
	if res := e.result("evt_deleted"); res != resultApplied {
		t.Fatalf("deleted: %s", res)
	}
	if sub := e.subscription(a.ID); sub.State != "canceled" || e.n.Held(true, sub, e.clock.now()) {
		t.Fatalf("canceled with time paid for: %+v", sub)
	}
	e.refresh()
	e.served(a.PrimaryHost, http.StatusOK)
	ob := e.browser()
	ob.portalSignIn(e.owner)
	body := ob.page("/admin/sites")
	if !strings.Contains(body, "Suscripción terminada") || !strings.Contains(body, "en línea hasta el") || strings.Contains(body, "Retenido por facturación") {
		t.Fatalf("My sites does not show until when the site is served:\n%s", body)
	}

	// The minute's reload holds it once the paid period is over.
	e.clock.add(end.Sub(e.clock.now()) - time.Minute)
	e.refresh()
	e.served(a.PrimaryHost, http.StatusOK)
	e.clock.add(time.Minute)
	e.refresh()
	if rec := e.do("GET", "https://"+a.PrimaryHost+"/"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("after the paid period: %d", rec.Code)
	}
	if !e.n.Held(true, e.subscription(a.ID), e.clock.now()) {
		t.Fatal("Network.Held disagrees with the host table")
	}

	// Canceled at the end of its period (the Customer Portal's way): held
	// when Stripe ends it, as before.
	b := e.personal("bravo")
	mdB := e.subscribe(fs, b, "pro:month", "sub_test0011", "price_pro_month")
	endB := e.clock.now().Add(30 * 24 * time.Hour)
	fs.periodEnd("sub_test0011", endB)
	e.mustHook("evt_paid_b", "invoice.paid", 30, paidInvoiceJSON("in_b", "sub_test0011", mdB, 900, endB))
	e.result("evt_paid_b")
	e.clock.add(endB.Sub(e.clock.now()))
	fs.set("sub_test0011", "canceled", "price_pro_month")
	e.mustHook("evt_deleted_b", "customer.subscription.deleted", 31, subJSON("sub_test0011", "canceled", mdB))
	e.result("evt_deleted_b")
	e.refresh()
	if rec := e.do("GET", "https://"+b.PrimaryHost+"/"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("canceled at the end of its period: %d", rec.Code)
	}

	// Canceled after a failed renewal: the period it is in was not paid.
	c := e.personal("charlie")
	mdC := e.subscribe(fs, c, "pro:month", "sub_test0012", "price_pro_month")
	endC := e.clock.now().Add(30 * 24 * time.Hour)
	e.mustHook("evt_paid_c", "invoice.paid", 40, paidInvoiceJSON("in_c", "sub_test0012", mdC, 900, endC))
	e.result("evt_paid_c")
	e.clock.add(endC.Sub(e.clock.now()) + time.Hour)
	fs.periodEnd("sub_test0012", endC.Add(30*24*time.Hour))
	fs.set("sub_test0012", "past_due", "price_pro_month")
	e.mustHook("evt_fail_c", "invoice.payment_failed", 41, invoiceJSON("in_c2", "sub_test0012", mdC))
	e.result("evt_fail_c")
	e.clock.add(2 * 24 * time.Hour)
	fs.set("sub_test0012", "canceled", "price_pro_month")
	e.mustHook("evt_deleted_c", "customer.subscription.deleted", 42, subJSON("sub_test0012", "canceled", mdC))
	e.result("evt_deleted_c")
	e.refresh()
	if rec := e.do("GET", "https://"+c.PrimaryHost+"/"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("canceled after a failed renewal: %d", rec.Code)
	}
}

// TestHeld is the rule itself.
func TestHeld(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { x := now.Add(d); return &x }
	grace := 7 * 24 * time.Hour
	for _, c := range []struct {
		name              string
		state             string
		graceSince, until *time.Time
		want              bool
	}{
		{"active", "active", nil, nil, false},
		{"pending", "pending", nil, nil, true},
		{"in grace", "grace", at(-time.Hour), nil, false},
		{"grace over", "grace", at(-grace), at(time.Hour), true},
		{"canceled, nothing paid", "canceled", nil, nil, true},
		{"canceled, paid for a day more", "canceled", nil, at(24 * time.Hour), false},
		{"canceled, paid period over", "canceled", nil, at(0), true},
		{"pending, paid for later", "pending", nil, at(time.Hour), true},
	} {
		if got := held(c.state, c.graceSince, c.until, now, grace); got != c.want {
			t.Errorf("%s: held = %t", c.name, got)
		}
	}
}

// TestBillingRetry: a failed event is retried with a growing delay, shown
// to the network admins, and given up after billingMaxAttempts.
func TestBillingRetry(t *testing.T) {
	e, fs, _ := newBillingEnv(t)
	a := e.personal("alpha")
	md := e.subscribe(fs, a, "pro:month", "sub_test0005", "price_pro_month")

	fs.setDown(true)
	e.mustHook("evt_retry", "customer.subscription.updated", 20, subJSON("sub_test0005", "active", md))
	ev := e.failed("evt_retry", 1)
	if !ev.NextAttemptAt.Equal(e.clock.now().Add(billingRetryFirst)) || !strings.Contains(ev.Result, "temporarily unavailable") {
		t.Fatalf("first failure: %+v", ev)
	}
	if n, _ := e.n.FailingBillingEvents(e.ctx); n != 1 {
		t.Fatalf("%d failing", n)
	}
	_, nb, ncsrf := e.networkAdmin("nora")
	body := nb.page("/admin/network/billing")
	if !strings.Contains(body, "evt_retry") || !strings.Contains(body, "Reintentando") || !strings.Contains(body, `href="/admin/network/billing"`) {
		t.Fatalf("billing events:\n%s", body)
	}
	// Not before its time; then twice the delay.
	e.clock.add(30 * time.Second)
	if n := e.billing().ProcessDue(e.ctx); n != 0 {
		t.Fatalf("processed %d before the delay", n)
	}
	e.clock.add(31 * time.Second)
	e.billing().ProcessDue(e.ctx)
	if ev := e.failed("evt_retry", 2); !ev.NextAttemptAt.Equal(e.clock.now().Add(2 * billingRetryFirst)) {
		t.Fatalf("second failure: %+v", ev)
	}
	// A network admin's retry runs it now.
	fs.setDown(false)
	nb.post("/admin/network/billing/evt_retry/retry", "/admin/network/billing", url.Values{"csrf": {ncsrf}})
	if res := e.result("evt_retry"); res != resultApplied {
		t.Fatalf("retried: %s", res)
	}
	nb.page("/admin/network/billing") // shows the flash message, with the event's ID
	if strings.Contains(nb.page("/admin/network/billing"), "evt_retry") {
		t.Fatal("an applied event is listed")
	}
	if audits(t, e.portal, "network.billing_event_retried") != 1 {
		t.Fatal("the retry is not audited")
	}
	if rec := nb.do("POST", portalURL+"/admin/network/billing/evt_retry/retry", url.Values{"csrf": {ncsrf}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("retry of an applied event: %d", rec.Code)
	}

	// Given up after the last attempt.
	fs.setDown(true)
	e.mustHook("evt_lost", "customer.subscription.updated", 30, subJSON("sub_test0005", "active", md))
	e.failed("evt_lost", 1)
	if _, err := e.n.db.Exec(e.ctx, `UPDATE billing_events SET attempts = $2, next_attempt_at = '-infinity' WHERE id = $1`,
		"evt_lost", billingMaxAttempts-1); err != nil {
		t.Fatal(err)
	}
	e.billing().ProcessDue(e.ctx)
	if ev := e.failed("evt_lost", billingMaxAttempts); !ev.GaveUp() {
		t.Fatalf("not given up: %+v", ev)
	}
	e.clock.add(30 * 24 * time.Hour)
	if n := e.billing().ProcessDue(e.ctx); n != 0 {
		t.Fatal("a given-up event was retried")
	}
	if body := nb.page("/admin/network/billing"); !strings.Contains(body, "Abandonado") {
		t.Fatal("the given-up event is not shown")
	}

	// Processed events are purged after billingKeep, unmapped ones kept.
	e.mustHook("evt_nothing", "customer.subscription.updated", 40, subJSON("sub_alien0002", "active", `{}`))
	e.result("evt_nothing")
	if _, err := e.n.db.Exec(e.ctx, `UPDATE billing_events SET processed_at = now() - interval '91 days' WHERE processed_at IS NOT NULL`); err != nil {
		t.Fatal(err)
	}
	if n, err := e.n.PurgeBillingEvents(e.ctx); err != nil || n < 2 {
		t.Fatalf("purged %d, %v", n, err)
	}
	var left []string
	rows, _ := e.n.db.Query(e.ctx, `SELECT id FROM billing_events ORDER BY id`)
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		left = append(left, id)
	}
	if strings.Join(left, ",") != "evt_lost,evt_nothing" {
		t.Fatalf("left after the purge: %v", left)
	}
}

// TestBillingSitesPerGrant: a plan that pays for several sites adds the
// owner's next site to the subscription without another payment.
func TestBillingSitesPerGrant(t *testing.T) {
	e, fs, _ := newBillingEnv(t)
	if _, err := e.n.db.Exec(e.ctx, `UPDATE plans SET sites_per_grant = 2 WHERE slug = 'business'`); err != nil {
		t.Fatal(err)
	}
	a, b2, c := e.personal("alpha"), e.personal("bravo"), e.personal("charlie")
	e.subscribe(fs, a, "business:month", "sub_test0006", "price_biz_month")
	n := fs.sessions()
	b := e.browser()
	csrf := b.portalSignIn(e.owner)
	b.post("/admin/sites/"+strconv.FormatInt(b2.ID, 10)+"/upgrade", "/admin/sites", url.Values{"csrf": {csrf}, "offer": {"business:month"}})
	if fs.sessions() != n || e.row(b2.ID).Plan != "business" || e.subscription(b2.ID).StripeSubscriptionID != "sub_test0006" {
		t.Fatal("bravo did not join the subscription")
	}
	if !strings.Contains(b.page("/admin/sites"), "El sitio está ahora en tu suscripción.") {
		t.Fatal("no message")
	}
	// Full: the third site pays.
	if rec := b.upgrade(csrf, c.ID, "business:month"); rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "https://checkout.stripe.com/") {
		t.Fatalf("charlie: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	// The default: one site per subscription.
	plans := maps.Collect(func(yield func(string, int) bool) {
		ps, _ := e.n.Plans(e.ctx)
		for _, p := range ps {
			if !yield(p.Slug, p.SitesPerGrant) {
				return
			}
		}
	})
	if plans["pro"] != 1 || plans["personal"] != 1 {
		t.Fatalf("sites per grant: %v", plans)
	}
}
