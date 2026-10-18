package billing

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// DefaultBaseURL is the Stripe API origin.
	DefaultBaseURL = "https://api.stripe.com"

	// DefaultAPIVersion is the Stripe-Version sent with every request. It
	// was the current version when this package was written (October 2026).
	// Webhook endpoints should be created with the same version so event
	// payloads decode with the same shapes.
	DefaultAPIVersion = "2026-09-30.endive"

	// DefaultTimeout bounds a single HTTP attempt.
	DefaultTimeout = 30 * time.Second

	// MaxResponseSize caps how much of a response body is read.
	MaxResponseSize = 1 << 20

	// DefaultMaxRetries is how many times a retryable request is retried
	// after the first attempt.
	DefaultMaxRetries = 3

	userAgent = "sap13ns-billing/1"
)

// Client calls the Stripe API. Create it with New; the zero value is not
// usable. A Client is immutable after New and safe for concurrent use.
type Client struct {
	key         string
	baseURL     string
	apiVersion  string
	http        *http.Client
	maxRetries  int
	backoffBase time.Duration
	backoffMax  time.Duration
}

// Option configures a Client.
type Option func(*Client) error

// WithBaseURL points the client at another origin, typically an
// httptest.Server in tests. Plain http is only accepted for loopback hosts.
func WithBaseURL(raw string) Option {
	return func(c *Client) error {
		u, err := url.Parse(raw)
		if err != nil {
			return fmt.Errorf("billing: invalid base URL: %w", err)
		}
		if u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("billing: base URL must be scheme://host[:port][/path] without credentials, query or fragment")
		}
		switch u.Scheme {
		case "https":
		case "http":
			if !isLoopback(u.Hostname()) {
				return errors.New("billing: plain http base URL is only allowed for loopback hosts")
			}
		default:
			return fmt.Errorf("billing: unsupported base URL scheme %q", u.Scheme)
		}
		c.baseURL = strings.TrimRight(u.String(), "/")
		return nil
	}
}

// WithHTTPClient uses hc for transport. The client is copied: redirects are
// always disabled and a zero Timeout is replaced by DefaultTimeout.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) error {
		if hc == nil {
			return errors.New("billing: nil http.Client")
		}
		cp := *hc
		c.http = &cp
		return nil
	}
}

// WithAPIVersion overrides the Stripe-Version header. Changing it can change
// response shapes; prefer DefaultAPIVersion.
func WithAPIVersion(v string) Option {
	return func(c *Client) error {
		v = strings.TrimSpace(v)
		if v == "" || strings.ContainsAny(v, "\r\n") {
			return errors.New("billing: invalid API version")
		}
		c.apiVersion = v
		return nil
	}
}

// WithMaxRetries sets how many times a retryable request is retried
// (0 disables retries). Values above 5 are clamped.
func WithMaxRetries(n int) Option {
	return func(c *Client) error {
		c.maxRetries = min(max(n, 0), 5)
		return nil
	}
}

// WithRetryBackoff sets the base and maximum delay between retries. The
// delay doubles per attempt with jitter. Mostly useful in tests.
func WithRetryBackoff(base, maxDelay time.Duration) Option {
	return func(c *Client) error {
		if base < 0 || maxDelay < base {
			return errors.New("billing: invalid retry backoff")
		}
		c.backoffBase, c.backoffMax = base, maxDelay
		return nil
	}
}

// New returns a Client authenticating with secretKey, which must be a
// secret (sk_) or restricted (rk_) key. Publishable keys are rejected.
func New(secretKey string, opts ...Option) (*Client, error) {
	secretKey = strings.TrimSpace(secretKey)
	switch {
	case secretKey == "":
		return nil, errors.New("billing: empty Stripe secret key")
	case strings.HasPrefix(secretKey, "pk_"):
		return nil, errors.New("billing: publishable key given; use a secret (sk_) or restricted (rk_) key")
	case !strings.HasPrefix(secretKey, "sk_") && !strings.HasPrefix(secretKey, "rk_"):
		return nil, errors.New("billing: Stripe key must start with sk_ or rk_")
	case strings.ContainsFunc(secretKey, func(r rune) bool { return r <= ' ' || r == 0x7f }):
		return nil, errors.New("billing: Stripe key contains invalid characters")
	}
	c := &Client{
		key:         secretKey,
		baseURL:     DefaultBaseURL,
		apiVersion:  DefaultAPIVersion,
		maxRetries:  DefaultMaxRetries,
		backoffBase: 500 * time.Millisecond,
		backoffMax:  8 * time.Second,
	}
	for _, o := range opts {
		if err := o(c); err != nil {
			return nil, err
		}
	}
	if c.http == nil {
		c.http = &http.Client{}
	}
	if c.http.Timeout == 0 {
		c.http.Timeout = DefaultTimeout
	}
	c.http.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return c, nil
}

// Livemode reports whether the client uses a live-mode key.
func (c *Client) Livemode() bool {
	return strings.HasPrefix(c.key, "sk_live_") || strings.HasPrefix(c.key, "rk_live_")
}

// String never reveals the key.
func (c *Client) String() string {
	mode := "test"
	if c.Livemode() {
		mode = "live"
	}
	return fmt.Sprintf("billing.Client{mode: %s, base: %s, version: %s}", mode, c.baseURL, c.apiVersion)
}

// GoString keeps %#v from printing the key.
func (c *Client) GoString() string { return c.String() }

// NewIdempotencyKey returns a random key suitable for the Idempotency-Key
// header. Generate it once per user action (for example when rendering the
// upgrade form) and reuse it if the action is resubmitted.
func NewIdempotencyKey() string {
	var b [16]byte
	if _, err := crand.Read(b[:]); err != nil {
		panic("billing: crypto/rand failed: " + err.Error())
	}
	return "sap13ns-" + hex.EncodeToString(b[:])
}

// do performs one API call, retrying when safe. params go in the query for
// GET and DELETE and in the form body for POST. out may be nil.
func (c *Client) do(ctx context.Context, method, path string, params map[string]any, idempotencyKey string, out any) error {
	form, err := encodeForm(params)
	if err != nil {
		return err
	}
	if len(idempotencyKey) > 255 || strings.ContainsFunc(idempotencyKey, func(r rune) bool { return r < ' ' || r == 0x7f }) {
		return errors.New("billing: idempotency key must be at most 255 printable characters")
	}
	target := c.baseURL + path
	var body []byte
	switch method {
	case http.MethodGet, http.MethodDelete:
		if len(form) > 0 {
			target += "?" + form.Encode()
		}
		idempotencyKey = "" // ignored by Stripe for GET/DELETE
	case http.MethodPost:
		body = []byte(form.Encode())
	default:
		return fmt.Errorf("billing: unsupported method %s", method)
	}
	// GET and DELETE are idempotent by definition; a POST only when keyed.
	retryable := method != http.MethodPost || idempotencyKey != ""

	var lastErr error
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			if err := sleepCtx(ctx, c.backoff(attempt)); err != nil {
				return fmt.Errorf("billing: %s %s: %w (last error: %v)", method, path, err, lastErr)
			}
		}
		retry, err := c.attempt(ctx, method, target, path, body, idempotencyKey, out)
		if err == nil {
			return nil
		}
		lastErr = err
		if !retry || !retryable || attempt >= c.maxRetries {
			return err
		}
	}
}

// attempt sends a single request. It reports whether the failure is worth
// retrying.
func (c *Client) attempt(ctx context.Context, method, target, path string, body []byte, idempotencyKey string, out any) (retry bool, err error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, rdr)
	if err != nil {
		return false, fmt.Errorf("billing: building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Stripe-Version", c.apiVersion)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return false, fmt.Errorf("billing: %s %s: %w", method, path, ctxErr)
		}
		return true, fmt.Errorf("billing: %s %s: %w", method, path, redactErr(err, c.key))
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseSize+1))
	if err != nil {
		return true, fmt.Errorf("billing: %s %s: reading response: %w", method, path, redactErr(err, c.key))
	}
	if len(data) > MaxResponseSize {
		return false, fmt.Errorf("billing: %s %s: response exceeds %d bytes", method, path, MaxResponseSize)
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if out == nil {
			return false, nil
		}
		if err := json.Unmarshal(data, out); err != nil {
			return false, fmt.Errorf("billing: %s %s: decoding response: %w", method, path, err)
		}
		return false, nil
	}

	apiErr := parseError(resp, data, c.key)
	return shouldRetry(resp), apiErr
}

// shouldRetry follows Stripe's explicit Stripe-Should-Retry hint when present,
// otherwise retries rate limiting and server errors.
func shouldRetry(resp *http.Response) bool {
	switch resp.Header.Get("Stripe-Should-Retry") {
	case "true":
		return true
	case "false":
		return false
	}
	return resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
}

// backoff returns the delay before retry number attempt (1-based): base*2^(n-1)
// capped at backoffMax, with "equal jitter" so the delay is in [d/2, d].
func (c *Client) backoff(attempt int) time.Duration {
	if c.backoffBase <= 0 {
		return 0
	}
	d := c.backoffBase << (attempt - 1)
	if d > c.backoffMax || d <= 0 {
		d = c.backoffMax
	}
	if d <= 1 {
		return d
	}
	half := d / 2
	return half + rand.N(half+1)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// validID guards path segments: Stripe IDs are [A-Za-z0-9_] only.
func validID(id string) bool {
	if id == "" || len(id) > 255 {
		return false
	}
	for _, r := range id {
		if !(r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}

func checkID(kind, id string) error {
	if !validID(id) {
		return fmt.Errorf("billing: invalid %s ID %q", kind, truncate(id, 64))
	}
	return nil
}

// truncate shortens s to at most n bytes without splitting a UTF-8 rune.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
