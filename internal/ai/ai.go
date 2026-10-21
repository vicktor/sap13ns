// Package ai asks large language model providers for help on behalf of
// editors.
//
// Nothing here runs on its own: every request starts from an explicit action
// in the post editor, and the web layer decides who may ask, how often and at
// what cost. The package provides:
//   - a Provider interface with adapters for the Anthropic Messages API, the
//     OpenAI-compatible Chat Completions and Images API (OpenAI, OpenRouter,
//     Ollama, llama.cpp) and the Gemini API;
//   - task helpers (excerpt, SEO fields, translation, image prompt, writing)
//     that build the prompts and validate what comes back;
//   - Lint, a local and deterministic detector of AI clichés.
//
// Transport rules shared by every adapter: HTTPS only (plain HTTP only to a
// loopback host when explicitly allowed), a deadline on every request,
// response bodies read through io.LimitReader, no redirects on API calls
// (custom auth headers such as x-api-key would follow them to another host),
// the API key never appears in errors, and image bytes are sniffed and
// decoded before they are returned.
package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Provider names accepted in Config.Provider.
const (
	ProviderAnthropic = "anthropic"
	ProviderOpenAI    = "openai" // any OpenAI-compatible server
	ProviderGemini    = "gemini"
	ProviderAlibaba   = "alibaba" // Alibaba Cloud Model Studio: Qwen
)

var Providers = []string{ProviderAnthropic, ProviderOpenAI, ProviderGemini, ProviderAlibaba}

// Provider is one configured AI backend.
type Provider interface {
	Name() string
	Text(ctx context.Context, req TextRequest) (*TextResponse, error)
	// Image returns ErrUnsupported when the provider or model cannot generate images.
	Image(ctx context.Context, req ImageRequest) ([]Image, error)
}

// Message roles.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type TextRequest struct {
	System   string
	Messages []Message
	// MaxTokens is the budget for the visible answer (0 means 1024). Where
	// the provider counts reasoning tokens against the same limit, the
	// adapter adds headroom for them.
	MaxTokens int
	// Temperature is sent only when set. The Anthropic adapter never sends
	// it: current Claude models reject non-default sampling parameters.
	Temperature *float64
	// JSON asks for a single JSON object; Text then holds only that object.
	JSON bool
	// Schema optionally describes the JSON object (JSON Schema, objects with
	// "additionalProperties": false, no string length or array size
	// keywords). Providers with native structured output enforce it.
	Schema json.RawMessage
}

// Normalized stop reasons.
const (
	StopEnd      = "end"
	StopLength   = "max_tokens"
	StopRefusal  = "refusal"  // the model declined
	StopFiltered = "filtered" // the provider's safety filter blocked the prompt or answer
	StopOther    = "other"
)

type TextResponse struct {
	Text         string
	Model        string
	InputTokens  int
	OutputTokens int // includes reasoning tokens when the provider reports them
	StopReason   string
}

// Image aspect ratios understood by every adapter (mapped to the closest
// size the model supports).
const (
	Aspect16x9 = "16:9"
	AspectOG   = "1.91:1" // Open Graph cards (1200×630)
	Aspect1x1  = "1:1"
	Aspect4x3  = "4:3"
	Aspect3x2  = "3:2"
)

var aspectRatios = map[string]float64{
	Aspect16x9: 16.0 / 9, AspectOG: 1.91, Aspect1x1: 1, Aspect4x3: 4.0 / 3, Aspect3x2: 1.5,
}

const maxImagesPerRequest = 4

type ImageRequest struct {
	Prompt string
	Aspect string // one of the Aspect constants; empty means 16:9
	N      int    // 0 means 1; at most 4
}

type Image struct {
	Data          []byte
	MIME          string // sniffed: image/png, image/jpeg or image/webp
	Width, Height int
	RevisedPrompt string // the prompt as rewritten by the provider, if it says
}

type Config struct {
	Provider   string
	APIKey     string
	BaseURL    string // empty: the provider's public endpoint
	TextModel  string // empty: DefaultTextModel(Provider)
	ImageModel string // empty: DefaultImageModel(Provider)
	// EmbedModel and EmbedDimensions configure Embed (empty model:
	// DefaultEmbedModel(Provider); 0 dimensions: the model's default).
	EmbedModel      string
	EmbedDimensions int
	// Timeout bounds text requests (0 means 90 s); ImageTimeout bounds image
	// requests, including the download of a returned URL (0 means 180 s).
	Timeout      time.Duration
	ImageTimeout time.Duration
	// AllowInsecureLocal permits plain http:// to a loopback host, for Ollama
	// or llama.cpp running on the same machine.
	AllowInsecureLocal bool
	// NoThinking asks OpenAI-compatible servers to switch the model's
	// reasoning off (chat_template_kwargs enable_thinking=false, honoured
	// by llama.cpp, vLLM and SGLang). api.openai.com is never sent it.
	NoThinking bool
}

var (
	ErrUnsupported = errors.New("not supported by this provider or model")
	ErrAuth        = errors.New("authentication with the AI provider failed")
	ErrRateLimited = errors.New("rate limited by the AI provider")
	ErrBadResponse = errors.New("malformed response from the AI provider")
	ErrRefused     = errors.New("the model declined or the provider filtered the answer")
	ErrTruncated   = errors.New("the answer was cut off at the token limit")
	// ErrThinking is ErrTruncated with no visible answer: a reasoning model
	// spent the whole budget thinking.
	ErrThinking     = fmt.Errorf("%w before the model finished thinking", ErrTruncated)
	ErrInsecureURL  = errors.New("AI endpoint must use https (http only to a loopback host when allowed)")
	ErrInvalidInput = errors.New("invalid AI request")
)

// APIError is a non-2xx answer from a provider. It matches ErrAuth or
// ErrRateLimited with errors.Is when appropriate.
type APIError struct {
	Provider   string
	Status     int
	Type       string        // provider error type or status, e.g. "rate_limit_error"
	Message    string        // provider message, at most 300 characters, API key redacted
	RetryAfter time.Duration // from the Retry-After header, when present
	kind       error
}

func (e *APIError) Error() string {
	s := fmt.Sprintf("%s: HTTP %d", e.Provider, e.Status)
	if e.Type != "" {
		s += " " + e.Type
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	return s
}

func (e *APIError) Unwrap() error { return e.kind }

// Model is one entry of the settings dropdown.
type Model struct {
	ID    string
	Label string
}

// catalog: the default first, then the others; the last ones of OpenAI and
// Gemini are their cheapest (pricing pages checked on 2026-10-20). Claude
// Haiku 5.5 is already Anthropic's cheapest model.
var catalog = map[string]struct{ text, image []Model }{
	ProviderAnthropic: {
		text: []Model{
			{"claude-opus-5-5", "Claude Opus 5.5"},
			{"claude-sonnet-5-5", "Claude Sonnet 5.5"},
			{"claude-haiku-5-5", "Claude Haiku 5.5"},
		},
	},
	ProviderOpenAI: {
		text: []Model{
			{"gpt-6-astra", "GPT-6 Astra"},
			{"gpt-6.1-sol", "GPT-6.1 Sol"},
			{"gpt-6-luna", "GPT-6 Luna"},
			{"gpt-4.1-nano", "GPT-4.1 nano"},
			{"gpt-5-nano", "GPT-5 nano"},
		},
		image: []Model{
			{"gpt-image-2.5-flare", "GPT Image 2.5 Flare"},
			{"gpt-image-2.5-sunburst", "GPT Image 2.5 Sunburst"},
			{"gpt-image-1-mini", "GPT Image 1 mini"},
		},
	},
	ProviderGemini: {
		text: []Model{
			{"gemini-3.8-flash", "Gemini 3.8 Flash"},
			{"gemini-4-argon", "Gemini 4 Argon"},
			{"gemini-3.5-flash-lite", "Gemini 3.5 Flash-Lite"},
			{"gemini-3.1-flash-lite", "Gemini 3.1 Flash-Lite"},
			{"gemini-2.5-flash-lite", "Gemini 2.5 Flash-Lite"},
		},
		image: []Model{
			{"gemini-nano-banana-2.1", "Nano Banana 2.1"},
			{"gemini-3-pro-image", "Nano Banana Pro"},
			{"gemini-3.1-flash-image", "Nano Banana 2"},
			{"gemini-3.1-flash-lite-image", "Gemini 3.1 Flash-Lite Image"},
		},
	},
	ProviderAlibaba: {
		text: []Model{
			{"qwen-plus", "Qwen Plus"},
			{"qwen3-max", "Qwen3 Max"},
			{"qwen-flash", "Qwen Flash"},
		},
		image: []Model{
			{"qwen-image-2.0-pro", "Qwen-Image 2.0 Pro"},
			{"qwen-image-2.0", "Qwen-Image 2.0"},
		},
	},
}

// Models lists the known models of a provider, the default first. Other IDs
// are accepted too (OpenRouter and Ollama names, newer releases).
func Models(provider string) (text, image []Model) {
	c := catalog[provider]
	return c.text, c.image
}

func DefaultTextModel(provider string) string {
	if c := catalog[provider]; len(c.text) > 0 {
		return c.text[0].ID
	}
	return ""
}

// DefaultImageModel returns "" for providers without image generation.
func DefaultImageModel(provider string) string {
	if c := catalog[provider]; len(c.image) > 0 {
		return c.image[0].ID
	}
	return ""
}

// Model IDs end up in JSON bodies (and, for Gemini, in the URL path).
// OpenRouter uses "vendor/model" and Ollama "model:tag".
var (
	modelRe       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+-]{0,199}$`)
	geminiModelRe = regexp.MustCompile(`^(models/)?[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

// New returns the adapter for cfg.Provider. client may be nil; otherwise its
// Transport is reused (its Timeout is replaced by per-request deadlines and
// its redirect policy by a stricter one).
func New(cfg Config, client *http.Client) (Provider, error) {
	cfg.Provider = strings.ToLower(strings.TrimSpace(cfg.Provider))
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	for _, r := range cfg.APIKey {
		if r <= ' ' || r == 0x7f {
			return nil, errors.New("API key contains whitespace or control characters")
		}
	}
	if cfg.TextModel == "" {
		cfg.TextModel = DefaultTextModel(cfg.Provider)
	}
	if cfg.ImageModel == "" {
		cfg.ImageModel = DefaultImageModel(cfg.Provider)
	}
	if cfg.EmbedModel == "" {
		cfg.EmbedModel = DefaultEmbedModel(cfg.Provider)
	}
	if cfg.EmbedDimensions < 0 || cfg.EmbedDimensions > maxEmbedDimensions {
		return nil, fmt.Errorf("invalid embedding dimensions %d", cfg.EmbedDimensions)
	}
	for _, m := range []string{cfg.TextModel, cfg.ImageModel, cfg.EmbedModel} {
		if m != "" && (!modelRe.MatchString(m) || cfg.Provider == ProviderGemini && !geminiModelRe.MatchString(m)) {
			return nil, fmt.Errorf("invalid model name %q", m)
		}
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTextTimeout
	}
	if cfg.ImageTimeout <= 0 {
		cfg.ImageTimeout = defaultImageTimeout
	}

	var defaultBase string
	switch cfg.Provider {
	case ProviderAnthropic:
		defaultBase = "https://api.anthropic.com"
	case ProviderOpenAI:
		defaultBase = "https://api.openai.com/v1"
	case ProviderGemini:
		defaultBase = "https://generativelanguage.googleapis.com/v1beta"
	case ProviderAlibaba:
		defaultBase = alibabaBase
	default:
		return nil, fmt.Errorf("unknown AI provider %q", cfg.Provider)
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBase
	}
	u, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, errors.New("invalid AI base URL")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("AI base URL must not have a query or fragment")
	}
	if err := checkURL(u, cfg.AllowInsecureLocal); err != nil {
		return nil, err
	}
	// The key is optional only for self-hosted OpenAI-compatible servers.
	needsKey := cfg.Provider != ProviderOpenAI || u.Hostname() == "api.openai.com"
	if needsKey && cfg.APIKey == "" {
		return nil, fmt.Errorf("%s: API key is required", cfg.Provider)
	}

	t := newTransport(cfg, client)
	switch cfg.Provider {
	case ProviderAnthropic:
		return &anthropic{t: t, base: strings.TrimSuffix(cfg.BaseURL, "/v1"), model: cfg.TextModel}, nil
	case ProviderOpenAI:
		return &openAI{t: t, base: cfg.BaseURL, model: cfg.TextModel, imageModel: cfg.ImageModel,
			official: u.Hostname() == "api.openai.com", noThinking: cfg.NoThinking,
			embedModel: cfg.EmbedModel, embedDims: cfg.EmbedDimensions}, nil
	case ProviderAlibaba:
		root := alibabaRoot(cfg.BaseURL)
		return &alibaba{openAI: &openAI{t: t, base: root + "/compatible-mode/v1", model: cfg.TextModel,
			embedModel: cfg.EmbedModel, embedDims: cfg.EmbedDimensions}, root: root, imageModel: cfg.ImageModel}, nil
	default:
		return &gemini{t: t, base: cfg.BaseURL, model: strings.TrimPrefix(cfg.TextModel, "models/"),
			imageModel: strings.TrimPrefix(cfg.ImageModel, "models/"),
			embedModel: strings.TrimPrefix(cfg.EmbedModel, "models/"), embedDims: cfg.EmbedDimensions}, nil
	}
}

// Usage counts what went through a Metered provider.
type Usage struct {
	Requests     int
	InputTokens  int
	OutputTokens int
	Images       int
}

// Metered wraps a Provider and adds up usage, so that the caller can charge
// a budget for task helpers that make several requests (Translate makes one
// per section).
type Metered struct {
	Provider
	mu sync.Mutex
	u  Usage
}

func Meter(p Provider) *Metered { return &Metered{Provider: p} }

func (m *Metered) Text(ctx context.Context, req TextRequest) (*TextResponse, error) {
	res, err := m.Provider.Text(ctx, req)
	m.mu.Lock()
	m.u.Requests++
	if res != nil {
		m.u.InputTokens += res.InputTokens
		m.u.OutputTokens += res.OutputTokens
	}
	m.mu.Unlock()
	return res, err
}

func (m *Metered) Image(ctx context.Context, req ImageRequest) ([]Image, error) {
	imgs, err := m.Provider.Image(ctx, req)
	m.mu.Lock()
	m.u.Requests++
	m.u.Images += len(imgs)
	m.mu.Unlock()
	return imgs, err
}

func (m *Metered) Usage() Usage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.u
}
