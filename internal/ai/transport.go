package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // register decoders used to validate returned images
	_ "image/png"
	"io"
	"math"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	_ "golang.org/x/image/webp"
)

const (
	defaultTextTimeout  = 90 * time.Second
	defaultImageTimeout = 180 * time.Second
	maxTextResponse     = 4 << 20
	maxImageResponse    = 25 << 20
	maxErrorBody        = 64 << 10
	maxProviderMessage  = 300
	maxPixels           = 50_000_000 // decompression-bomb guard, as in media
	defaultMaxTokens    = 1024
	// Claude Opus 5.5 always thinks, OpenAI reasoning models and Gemini
	// thinking models count reasoning against the same output limit; the
	// headroom keeps short answers from being starved by it. Only tokens
	// actually generated are billed.
	reasoningHeadroom = 16_000
	maxOutputTokens   = 64_000
)

type transport struct {
	provider     string
	api          *http.Client // API calls: redirects are refused
	download     *http.Client // image URLs: https-only redirects
	key          string
	allowLocal   bool
	textTimeout  time.Duration
	imageTimeout time.Duration
}

func newTransport(cfg Config, client *http.Client) *transport {
	if client == nil {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.MaxResponseHeaderBytes = 64 << 10
		client = &http.Client{Transport: tr}
	}
	t := &transport{
		provider:     cfg.Provider,
		key:          cfg.APIKey,
		allowLocal:   cfg.AllowInsecureLocal,
		textTimeout:  cfg.Timeout,
		imageTimeout: cfg.ImageTimeout,
	}
	api := *client
	api.Timeout, api.Jar = 0, nil
	api.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	dl := api
	dl.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("too many redirects")
		}
		return t.checkDownloadURL(req.URL)
	}
	t.api, t.download = &api, &dl
	return t
}

// checkURL enforces https, allowing http only to a loopback host when asked.
func checkURL(u *url.URL, allowLocal bool) error {
	if u.User != nil {
		return errors.New("AI URL must not contain credentials")
	}
	if u.Hostname() == "" {
		return ErrInsecureURL
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if allowLocal && isLoopback(u.Hostname()) {
			return nil
		}
	}
	return ErrInsecureURL
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	a, err := netip.ParseAddr(host)
	return err == nil && a.Unmap().IsLoopback()
}

// checkDownloadURL vets an image URL returned by a provider: https only, and
// no literal private or loopback address (best effort against SSRF through a
// hostile compatible server; DNS names are not resolved here).
func (t *transport) checkDownloadURL(u *url.URL) error {
	if u.Scheme != "https" || u.User != nil || u.Hostname() == "" {
		return fmt.Errorf("%w: image URL must use https", ErrBadResponse)
	}
	host := u.Hostname()
	if t.allowLocal && isLoopback(host) {
		return nil
	}
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return fmt.Errorf("%w: image URL points to a local address", ErrBadResponse)
	}
	if a, err := netip.ParseAddr(host); err == nil {
		a = a.Unmap()
		if a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsUnspecified() || a.IsMulticast() {
			return fmt.Errorf("%w: image URL points to a local address", ErrBadResponse)
		}
	}
	return nil
}

func (t *transport) errorf(format string, args ...any) error {
	return fmt.Errorf(t.provider+": "+format, args...)
}

// post sends a JSON request and decodes a JSON response of at most limit
// bytes into out.
func (t *transport) post(ctx context.Context, endpoint string, hdr http.Header, in any, limit int64, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return t.errorf("%w: cannot build request", ErrInvalidInput)
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "sap13ns")
	resp, err := t.api.Do(req)
	if err != nil {
		return t.requestError(ctx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return t.apiError(resp, raw)
	}
	raw, err := readLimited(resp.Body, limit)
	if err != nil {
		if ctx.Err() != nil {
			return t.requestError(ctx, err)
		}
		return t.errorf("%w", err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return t.errorf("%w: %v", ErrBadResponse, err)
	}
	return nil
}

// fetch downloads an image URL returned by a provider. No credentials are sent.
func (t *transport) fetch(ctx context.Context, raw string) ([]byte, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, t.errorf("%w: invalid image URL", ErrBadResponse)
	}
	if err := t.checkDownloadURL(u); err != nil {
		return nil, t.errorf("%w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, t.errorf("%w: invalid image URL", ErrBadResponse)
	}
	req.Header.Set("User-Agent", "sap13ns")
	resp, err := t.download.Do(req)
	if err != nil {
		return nil, t.requestError(ctx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, t.errorf("%w: image download returned HTTP %d", ErrBadResponse, resp.StatusCode)
	}
	b, err := readLimited(resp.Body, maxImageResponse)
	if err != nil {
		if ctx.Err() != nil {
			return nil, t.requestError(ctx, err)
		}
		return nil, t.errorf("%w", err)
	}
	return b, nil
}

// requestError reports a transport failure without the URL (an image URL
// may carry a signed query string).
func (t *transport) requestError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return t.errorf("request did not complete: %w", ctxErr)
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return t.errorf("request failed: %w", err)
}

func readLimited(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%w: response larger than %d bytes", ErrBadResponse, limit)
	}
	return b, nil
}

// apiError parses the error envelopes of all three providers:
// Anthropic {"type":"error","error":{"type","message"}}, OpenAI-compatible
// {"error":{"message","type","code"}} (Ollama also {"error":"..."}) and
// Google {"error":{"code","message","status","details":[{"reason"}]}}.
func (t *transport) apiError(resp *http.Response, body []byte) error {
	e := &APIError{Provider: t.provider, Status: resp.StatusCode}
	var env struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	var reason string
	if json.Unmarshal(body, &env) == nil {
		var obj struct {
			Type    string `json:"type"`
			Status  string `json:"status"`
			Message string `json:"message"`
			Details []struct {
				Reason string `json:"reason"`
			} `json:"details"`
		}
		var s string
		switch {
		case json.Unmarshal(env.Error, &obj) == nil:
			e.Type, e.Message = obj.Type, obj.Message
			if e.Type == "" {
				e.Type = obj.Status
			}
			for _, d := range obj.Details {
				if d.Reason != "" {
					reason = d.Reason
				}
			}
		case json.Unmarshal(env.Error, &s) == nil:
			e.Message = s
		default:
			e.Message = env.Message
		}
	}
	if e.Message == "" {
		e.Message = http.StatusText(resp.StatusCode)
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		e.Message = "unexpected redirect"
	}
	e.Type = t.clean(e.Type, 60)
	e.Message = t.clean(e.Message, maxProviderMessage)
	e.RetryAfter = retryAfter(resp.Header.Get("Retry-After"))

	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		e.kind = ErrAuth
	case resp.StatusCode == http.StatusTooManyRequests:
		e.kind = ErrRateLimited
	case reason == "API_KEY_INVALID" || strings.Contains(e.Message, "API key not valid"):
		// Gemini answers 400 INVALID_ARGUMENT to a bad key.
		e.kind = ErrAuth
	}
	return e
}

// clean makes provider text safe to show and log: valid UTF-8, no control
// characters, the API key redacted, at most max characters.
func (t *transport) clean(s string, max int) string {
	s = strings.ToValidUTF8(s, "?")
	if len(t.key) >= 6 {
		s = strings.ReplaceAll(s, t.key, "[redacted]")
	}
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
	return truncateRunes(s, max)
}

func truncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max-1]) + "…"
}

func retryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 0 {
		return time.Duration(n) * time.Second
	}
	if at, err := http.ParseTime(v); err == nil {
		if d := time.Until(at); d > 0 {
			return d
		}
	}
	return 0
}

func validateText(req TextRequest) error {
	if len(req.Messages) == 0 {
		return fmt.Errorf("%w: no messages", ErrInvalidInput)
	}
	for _, m := range req.Messages {
		if m.Role != RoleUser && m.Role != RoleAssistant {
			return fmt.Errorf("%w: unknown role %q", ErrInvalidInput, m.Role)
		}
	}
	if req.MaxTokens < 0 {
		return fmt.Errorf("%w: negative MaxTokens", ErrInvalidInput)
	}
	if len(req.Schema) > 0 && !json.Valid(req.Schema) {
		return fmt.Errorf("%w: schema is not valid JSON", ErrInvalidInput)
	}
	return nil
}

// tokenLimit returns the output limit to send for an answer budget.
func tokenLimit(answer int, reasoning bool) int {
	if answer <= 0 {
		answer = defaultMaxTokens
	}
	if reasoning {
		answer += reasoningHeadroom
	}
	return min(answer, maxOutputTokens)
}

const jsonInstruction = "Respond with a single JSON object and nothing else: no Markdown code fences, no text before or after it."

func systemWithJSON(req TextRequest) string {
	if !req.JSON {
		return req.System
	}
	if req.System == "" {
		return jsonInstruction
	}
	return req.System + "\n\n" + jsonInstruction
}

// finish post-processes a successful text answer: reasoning traces of local
// models are dropped and, in JSON mode, the first JSON object is extracted.
func finish(t *transport, req TextRequest, res *TextResponse) (*TextResponse, error) {
	res.Text = strings.TrimSpace(stripThink(res.Text))
	if !req.JSON || res.StopReason == StopRefusal || res.StopReason == StopFiltered {
		return res, nil
	}
	obj, err := extractJSON(res.Text)
	if err != nil {
		if res.StopReason == StopLength {
			return nil, t.errorf("%w", truncated(res))
		}
		return nil, t.errorf("%w", err)
	}
	res.Text = obj
	return res, nil
}

func truncated(res *TextResponse) error {
	if res.Text == "" {
		return ErrThinking
	}
	return ErrTruncated
}

// stripThink removes a leading <think>…</think> block, which some local
// reasoning models served by llama.cpp or Ollama put in the content.
func stripThink(s string) string {
	t := strings.TrimLeft(s, " \t\r\n")
	if !strings.HasPrefix(t, "<think>") {
		return s
	}
	if i := strings.Index(t, "</think>"); i >= 0 {
		return t[i+len("</think>"):]
	}
	return s
}

// extractJSON returns the first complete, valid JSON object in s, skipping
// any prose or code fences around it.
func extractJSON(s string) (string, error) {
	for from := 0; from < len(s); {
		i := strings.IndexByte(s[from:], '{')
		if i < 0 {
			break
		}
		start := from + i
		if end := matchBrace(s, start); end > 0 && json.Valid([]byte(s[start:end])) {
			return s[start:end], nil
		}
		from = start + 1
	}
	return "", fmt.Errorf("%w: no JSON object in the answer", ErrBadResponse)
}

// matchBrace returns the offset just past the brace closing the one at
// start, honouring JSON strings, or -1.
func matchBrace(s string, start int) int {
	depth, inString, escaped := 0, false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		switch {
		case inString:
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
		case c == '"':
			inString = true
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}

// decodeBase64 accepts standard or URL-safe base64, padded or not, with
// line breaks, and data: URLs.
func decodeBase64(s string) ([]byte, error) {
	if strings.HasPrefix(s, "data:") {
		_, payload, ok := strings.Cut(s, ";base64,")
		if !ok {
			return nil, fmt.Errorf("%w: data URL is not base64", ErrBadResponse)
		}
		s = payload
	}
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, s)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, fmt.Errorf("%w: invalid base64 image data", ErrBadResponse)
}

// checkImage verifies that data really is a PNG, JPEG or WebP image of sane
// dimensions; the MIME type comes from the bytes, never from the provider.
func checkImage(data []byte) (Image, error) {
	if len(data) == 0 || len(data) > maxImageResponse {
		return Image{}, fmt.Errorf("%w: empty or oversized image", ErrBadResponse)
	}
	mime := http.DetectContentType(data)
	if i := strings.IndexByte(mime, ';'); i >= 0 {
		mime = mime[:i]
	}
	want := map[string]string{"image/png": "png", "image/jpeg": "jpeg", "image/webp": "webp"}[mime]
	if want == "" {
		return Image{}, fmt.Errorf("%w: image is not PNG, JPEG or WebP", ErrBadResponse)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || format != want || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxPixels {
		return Image{}, fmt.Errorf("%w: image cannot be decoded or is too large", ErrBadResponse)
	}
	return Image{Data: data, MIME: mime, Width: cfg.Width, Height: cfg.Height}, nil
}

func validateImage(req *ImageRequest) error {
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Prompt == "" {
		return fmt.Errorf("%w: empty image prompt", ErrInvalidInput)
	}
	if utf8.RuneCountInString(req.Prompt) > 4000 {
		return fmt.Errorf("%w: image prompt is too long", ErrInvalidInput)
	}
	if req.Aspect == "" {
		req.Aspect = Aspect16x9
	}
	if _, ok := aspectRatios[req.Aspect]; !ok {
		return fmt.Errorf("%w: unknown aspect ratio %q", ErrInvalidInput, req.Aspect)
	}
	if req.N <= 0 {
		req.N = 1
	}
	req.N = min(req.N, maxImagesPerRequest)
	return nil
}

// closestSize picks the "WxH" size whose ratio is nearest to the aspect.
func closestSize(aspect string, sizes []string) string {
	want := math.Log(aspectRatios[aspect])
	best, bestDiff := sizes[0], math.Inf(1)
	for _, s := range sizes {
		ws, hs, _ := strings.Cut(s, "x")
		w, _ := strconv.ParseFloat(ws, 64)
		h, _ := strconv.ParseFloat(hs, 64)
		if d := math.Abs(math.Log(w/h) - want); d < bestDiff {
			best, bestDiff = s, d
		}
	}
	return best
}
