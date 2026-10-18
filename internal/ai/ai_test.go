package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

const testKey = "sk-test-0123456789abcdef"

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// captured is what a fake provider endpoint received.
type captured struct {
	method, path string
	header       http.Header
	body         map[string]any
}

func capture(r *http.Request) captured {
	c := captured{method: r.Method, path: r.URL.Path, header: r.Header.Clone()}
	json.NewDecoder(r.Body).Decode(&c.body)
	return c
}

// basePath is where each provider's API lives below the server root.
var basePath = map[string]string{ProviderAnthropic: "", ProviderOpenAI: "/v1", ProviderGemini: "/v1beta"}

func newTestProvider(t *testing.T, name string, h http.HandlerFunc, mod ...func(*Config)) Provider {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	cfg := Config{Provider: name, APIKey: testKey, BaseURL: srv.URL + basePath[name], AllowInsecureLocal: true}
	for _, m := range mod {
		m(&cfg)
	}
	p, err := New(cfg, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func reply(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
	}
}

func get(m map[string]any, path ...string) any {
	var v any = m
	for _, k := range path {
		obj, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = obj[k]
	}
	return v
}

var ctx = context.Background()

func TestAnthropicText(t *testing.T) {
	var got captured
	p := newTestProvider(t, ProviderAnthropic, func(w http.ResponseWriter, r *http.Request) {
		got = capture(r)
		io.WriteString(w, `{"type":"message","model":"claude-opus-5-5","content":[
			{"type":"thinking","thinking":"","signature":"x"},{"type":"text","text":"Hola "},{"type":"text","text":"mundo"}],
			"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":2}}`)
	})
	temp := 0.2
	res, err := p.Text(ctx, TextRequest{System: "Be brief.", Messages: userMsg("hi"), MaxTokens: 100, Temperature: &temp})
	if err != nil {
		t.Fatal(err)
	}
	if got.method != "POST" || got.path != "/v1/messages" {
		t.Errorf("request %s %s", got.method, got.path)
	}
	if got.header.Get("x-api-key") != testKey || got.header.Get("anthropic-version") != "2023-06-01" {
		t.Errorf("auth headers: %v", got.header)
	}
	if got.header.Get("Authorization") != "" {
		t.Error("Anthropic must not get a bearer token")
	}
	if got.body["system"] != "Be brief." || got.body["model"] != "claude-opus-5-5" {
		t.Errorf("body: %v", got.body)
	}
	if _, ok := got.body["temperature"]; ok {
		t.Error("temperature must not be sent to Claude")
	}
	if got.body["max_tokens"] != float64(100+reasoningHeadroom) {
		t.Errorf("max_tokens = %v", got.body["max_tokens"])
	}
	msgs, _ := got.body["messages"].([]any)
	if len(msgs) != 1 || get(msgs[0].(map[string]any), "role") != "user" {
		t.Errorf("messages: %v", got.body["messages"])
	}
	if res.Text != "Hola mundo" || res.Model != "claude-opus-5-5" || res.InputTokens != 12 || res.OutputTokens != 5 || res.StopReason != StopEnd {
		t.Errorf("response: %+v", res)
	}
}

func TestAnthropicJSON(t *testing.T) {
	var got captured
	p := newTestProvider(t, ProviderAnthropic, func(w http.ResponseWriter, r *http.Request) {
		got = capture(r)
		io.WriteString(w, `{"type":"message","content":[{"type":"text","text":"Claro:\n`+"```json"+`\n{\"a\": \"}\", \"b\": [1]}\n`+"```"+`"}],"stop_reason":"end_turn","usage":{}}`)
	})
	res, err := p.Text(ctx, TextRequest{Messages: userMsg("x"), JSON: true, Schema: json.RawMessage(`{"type":"object"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != `{"a": "}", "b": [1]}` {
		t.Errorf("extracted %q", res.Text)
	}
	if get(got.body, "output_config", "format", "type") != "json_schema" {
		t.Errorf("structured output not requested: %v", got.body)
	}
	if !strings.Contains(got.body["system"].(string), "JSON object") {
		t.Error("JSON instruction missing from the system prompt")
	}
}

func TestAnthropicRejectsPrefillAndImages(t *testing.T) {
	var calls atomic.Int32
	p := newTestProvider(t, ProviderAnthropic, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	_, err := p.Text(ctx, TextRequest{Messages: []Message{{RoleUser, "a"}, {RoleAssistant, "b"}}})
	if !errors.Is(err, ErrInvalidInput) {
		t.Errorf("prefill: %v", err)
	}
	if _, err := p.Image(ctx, ImageRequest{Prompt: "x"}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("image: %v", err)
	}
	if calls.Load() != 0 {
		t.Error("no request should have been sent")
	}
}

func TestAnthropicTruncatedJSON(t *testing.T) {
	p := newTestProvider(t, ProviderAnthropic, reply(200, `{"type":"message","content":[{"type":"text","text":"{\"a\": 1, \"b"}],"stop_reason":"max_tokens","usage":{}}`))
	if _, err := p.Text(ctx, TextRequest{Messages: userMsg("x"), JSON: true}); !errors.Is(err, ErrTruncated) {
		t.Errorf("err = %v, want ErrTruncated", err)
	}
}

// Every adapter maps HTTP failures and bad bodies to the same errors.
func TestErrorsAllProviders(t *testing.T) {
	long := strings.Repeat("x", 1000)
	cases := []struct {
		name    string
		h       http.HandlerFunc
		wantErr error
		check   func(t *testing.T, err error)
	}{
		{"401", reply(401, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`), ErrAuth, nil},
		{"403", reply(403, `{"error":{"code":403,"message":"denied","status":"PERMISSION_DENIED"}}`), ErrAuth, nil},
		{"429", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "7")
			reply(429, `{"error":{"message":"slow down","type":"rate_limit_error"}}`)(w, r)
		}, ErrRateLimited, func(t *testing.T, err error) {
			var ae *APIError
			if !errors.As(err, &ae) || ae.Status != 429 || ae.RetryAfter != 7*time.Second || ae.Type != "rate_limit_error" || ae.Message != "slow down" {
				t.Errorf("APIError = %+v", ae)
			}
		}},
		{"400 echoes key", reply(400, `{"error":{"message":"bad key `+testKey+` `+long+`","type":"invalid_request_error"}}`), nil, func(t *testing.T, err error) {
			var ae *APIError
			if !errors.As(err, &ae) || ae.Status != 400 {
				t.Fatalf("err = %v", err)
			}
			if strings.Contains(err.Error(), testKey) {
				t.Error("API key leaked into the error")
			}
			if n := utf8.RuneCountInString(ae.Message); n > maxProviderMessage {
				t.Errorf("message has %d characters", n)
			}
			if errors.Is(err, ErrAuth) || errors.Is(err, ErrRateLimited) {
				t.Error("400 is neither auth nor rate limit")
			}
		}},
		{"ollama string error", reply(500, `{"error":"model not found"}`), nil, func(t *testing.T, err error) {
			var ae *APIError
			if !errors.As(err, &ae) || ae.Message != "model not found" {
				t.Errorf("err = %v", err)
			}
		}},
		{"oversized", func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, `{"pad":"`+strings.Repeat("a", maxTextResponse)+`"}`)
		}, ErrBadResponse, nil},
		{"malformed", reply(200, `{"choices":[{"message":`), ErrBadResponse, nil},
		{"wrong shape", reply(200, `{"content":"nope","choices":"nope","candidates":"nope"}`), ErrBadResponse, nil},
	}
	for _, name := range Providers {
		for _, c := range cases {
			t.Run(name+"/"+c.name, func(t *testing.T) {
				p := newTestProvider(t, name, c.h)
				_, err := p.Text(ctx, TextRequest{Messages: userMsg("hi")})
				if err == nil {
					t.Fatal("expected an error")
				}
				if c.wantErr != nil && !errors.Is(err, c.wantErr) {
					t.Errorf("err = %v, want %v", err, c.wantErr)
				}
				if strings.Contains(err.Error(), testKey) {
					t.Error("API key leaked into the error")
				}
				if c.check != nil {
					c.check(t, err)
				}
			})
		}
	}
}

func TestOpenAIText(t *testing.T) {
	var got captured
	p := newTestProvider(t, ProviderOpenAI, func(w http.ResponseWriter, r *http.Request) {
		got = capture(r)
		io.WriteString(w, `{"model":"llama3.1:8b","choices":[{"message":{"role":"assistant","content":"<think>hmm</think>\n{\"ok\": true}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":4}}`)
	}, func(c *Config) { c.TextModel = "llama3.1:8b" })
	temp := 0.3
	res, err := p.Text(ctx, TextRequest{System: "sys", Messages: userMsg("hi"), MaxTokens: 100, Temperature: &temp, JSON: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.path != "/v1/chat/completions" || got.header.Get("Authorization") != "Bearer "+testKey {
		t.Errorf("request %s auth=%q", got.path, got.header.Get("Authorization"))
	}
	if get(got.body, "response_format", "type") != "json_object" || got.body["max_tokens"] != float64(100+reasoningHeadroom) || got.body["temperature"] != 0.3 {
		t.Errorf("body: %v", got.body)
	}
	if _, ok := got.body["max_completion_tokens"]; ok {
		t.Error("compatible servers get max_tokens only")
	}
	msgs := got.body["messages"].([]any)
	if len(msgs) != 2 || get(msgs[0].(map[string]any), "role") != "system" || !strings.Contains(get(msgs[0].(map[string]any), "content").(string), "sys") {
		t.Errorf("messages: %v", msgs)
	}
	if res.Text != `{"ok": true}` || res.InputTokens != 9 || res.OutputTokens != 4 || res.StopReason != StopEnd || res.Model != "llama3.1:8b" {
		t.Errorf("response: %+v", res)
	}
}

func TestOpenAIJSONSchemaFallback(t *testing.T) {
	var formats []any
	p := newTestProvider(t, ProviderOpenAI, func(w http.ResponseWriter, r *http.Request) {
		got := capture(r)
		formats = append(formats, got.body["response_format"])
		if got.body["response_format"] != nil {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":"'response_format.type' must be 'json_schema' or 'text'"}`)
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"{\"a\":1}"},"finish_reason":"stop"}]}`)
	})
	schema := json.RawMessage(`{"type":"object","properties":{"a":{"type":"integer"}},"required":["a"],"additionalProperties":false}`)
	res, err := p.Text(ctx, TextRequest{Messages: userMsg("hi"), JSON: true, Schema: schema})
	if err != nil {
		t.Fatal(err)
	}
	if len(formats) != 2 || formats[1] != nil || res.Text != `{"a":1}` {
		t.Fatalf("formats %v, text %q", formats, res.Text)
	}
	f := formats[0].(map[string]any)
	if f["type"] != "json_schema" || get(f, "json_schema", "strict") != true || get(f, "json_schema", "schema", "type") != "object" {
		t.Errorf("first response_format: %v", f)
	}
}

func TestOpenAINoThinking(t *testing.T) {
	var got captured
	p := newTestProvider(t, ProviderOpenAI, func(w http.ResponseWriter, r *http.Request) {
		got = capture(r)
		io.WriteString(w, `{"choices":[{"message":{"content":"","reasoning_content":"hmm"},"finish_reason":"length"}]}`)
	}, func(c *Config) { c.NoThinking = true })
	_, err := p.Text(ctx, TextRequest{Messages: userMsg("hi"), JSON: true})
	if !errors.Is(err, ErrThinking) || !errors.Is(err, ErrTruncated) {
		t.Errorf("err = %v, want ErrThinking", err)
	}
	if get(got.body, "chat_template_kwargs", "enable_thinking") != false {
		t.Errorf("body: %v", got.body)
	}
}

func TestOpenAIJSONInReasoning(t *testing.T) {
	p := newTestProvider(t, ProviderOpenAI, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"content":"","reasoning_content":"{\"a\":1}"},"finish_reason":"stop"}]}`)
	})
	res, err := p.Text(ctx, TextRequest{Messages: userMsg("hi"), JSON: true})
	if err != nil || res.Text != `{"a":1}` {
		t.Fatalf("text %v, err %v", res, err)
	}
	if res, err := p.Text(ctx, TextRequest{Messages: userMsg("hi")}); err == nil && res.Text != "" {
		t.Errorf("plain text must not use the reasoning: %q", res.Text)
	}
}

func TestOpenAILocalWithoutKey(t *testing.T) {
	var auth atomic.Value
	p := newTestProvider(t, ProviderOpenAI, func(w http.ResponseWriter, r *http.Request) {
		auth.Store(r.Header.Get("Authorization"))
		io.WriteString(w, `{"choices":[{"message":{"content":[{"type":"text","text":"ok"}]},"finish_reason":"length"}]}`)
	}, func(c *Config) { c.APIKey = "" })
	res, err := p.Text(ctx, TextRequest{Messages: userMsg("hi")})
	if err != nil {
		t.Fatal(err)
	}
	if auth.Load() != "" {
		t.Errorf("Authorization = %q", auth.Load())
	}
	if res.Text != "ok" || res.StopReason != StopLength {
		t.Errorf("response: %+v", res)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOpenAIOfficialUsesMaxCompletionTokens(t *testing.T) {
	var got captured
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		got = capture(r)
		got.header.Set("Host", r.URL.Host)
		return &http.Response{StatusCode: 200, Header: http.Header{}, Request: r,
			Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"hi","refusal":null},"finish_reason":"stop"}]}`))}, nil
	})}
	p, err := New(Config{Provider: ProviderOpenAI, APIKey: testKey}, client)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Text(ctx, TextRequest{Messages: userMsg("hi"), MaxTokens: 50}); err != nil {
		t.Fatal(err)
	}
	if got.header.Get("Host") != "api.openai.com" || got.path != "/v1/chat/completions" {
		t.Errorf("request to %s%s", got.header.Get("Host"), got.path)
	}
	if got.body["max_completion_tokens"] != float64(50+reasoningHeadroom) || got.body["model"] != "gpt-6-astra" {
		t.Errorf("body: %v", got.body)
	}
	if _, ok := got.body["max_tokens"]; ok {
		t.Error("max_tokens is deprecated on api.openai.com")
	}
	if _, err := New(Config{Provider: ProviderOpenAI}, client); err == nil {
		t.Error("api.openai.com requires a key")
	}
}

func TestOpenAIImageBase64(t *testing.T) {
	img := pngBytes(t, 32, 18)
	var got captured
	p := newTestProvider(t, ProviderOpenAI, func(w http.ResponseWriter, r *http.Request) {
		got = capture(r)
		io.WriteString(w, `{"created":1,"data":[{"b64_json":"`+base64.StdEncoding.EncodeToString(img)+`"}]}`)
	})
	imgs, err := p.Image(ctx, ImageRequest{Prompt: "a lighthouse", Aspect: Aspect16x9})
	if err != nil {
		t.Fatal(err)
	}
	if got.path != "/v1/images/generations" || got.body["model"] != "gpt-image-2.5-flare" || got.body["size"] != "1536x864" || got.body["n"] != float64(1) {
		t.Errorf("request %s %v", got.path, got.body)
	}
	if _, ok := got.body["response_format"]; ok {
		t.Error("GPT Image models reject response_format")
	}
	if len(imgs) != 1 || imgs[0].MIME != "image/png" || imgs[0].Width != 32 || imgs[0].Height != 18 || !bytes.Equal(imgs[0].Data, img) {
		t.Errorf("images: %+v", imgs)
	}
}

func TestOpenAIImageURL(t *testing.T) {
	img := pngBytes(t, 8, 8)
	var downloadAuth atomic.Value
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/img.png":
			downloadAuth.Store(r.Header.Get("Authorization"))
			w.Write(img)
		case strings.HasPrefix(r.URL.Path, "/http/"):
			io.WriteString(w, `{"data":[{"url":"http://`+r.Host+`/img.png"}]}`)
		default:
			io.WriteString(w, `{"data":[{"url":"`+srv.URL+`/img.png?sig=secret","revised_prompt":"a red lighthouse"}]}`)
		}
	}))
	defer srv.Close()

	p, err := New(Config{Provider: ProviderOpenAI, APIKey: testKey, BaseURL: srv.URL + "/v1", ImageModel: "dall-e-3", AllowInsecureLocal: true}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	imgs, err := p.Image(ctx, ImageRequest{Prompt: "lighthouse", Aspect: AspectOG, N: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != 1 || imgs[0].RevisedPrompt != "a red lighthouse" || imgs[0].MIME != "image/png" {
		t.Errorf("images: %+v", imgs)
	}
	if downloadAuth.Load() != "" {
		t.Error("the API key must not be sent to the image URL")
	}

	// Plain http image URLs are refused.
	p, _ = New(Config{Provider: ProviderOpenAI, APIKey: testKey, BaseURL: srv.URL + "/http", AllowInsecureLocal: true}, srv.Client())
	if _, err := p.Image(ctx, ImageRequest{Prompt: "x"}); !errors.Is(err, ErrBadResponse) {
		t.Errorf("http URL: %v", err)
	}
	// Without AllowInsecureLocal, a URL pointing to a loopback address is refused.
	p, _ = New(Config{Provider: ProviderOpenAI, APIKey: testKey, BaseURL: srv.URL + "/v1"}, srv.Client())
	if _, err := p.Image(ctx, ImageRequest{Prompt: "x"}); !errors.Is(err, ErrBadResponse) || strings.Contains(err.Error(), "secret") {
		t.Errorf("loopback URL: %v", err)
	}
}

func TestOpenAIImageValidation(t *testing.T) {
	svg := base64.StdEncoding.EncodeToString([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`))
	p := newTestProvider(t, ProviderOpenAI, reply(200, `{"data":[{"b64_json":"`+svg+`"}]}`))
	if _, err := p.Image(ctx, ImageRequest{Prompt: "x"}); !errors.Is(err, ErrBadResponse) {
		t.Errorf("svg: %v", err)
	}
	// A PNG signature followed by garbage is not an image either.
	fake := base64.StdEncoding.EncodeToString(append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...))
	p = newTestProvider(t, ProviderOpenAI, reply(200, `{"data":[{"b64_json":"`+fake+`"}]}`))
	if _, err := p.Image(ctx, ImageRequest{Prompt: "x"}); !errors.Is(err, ErrBadResponse) {
		t.Errorf("fake png: %v", err)
	}
	p = newTestProvider(t, ProviderOpenAI, reply(404, `{"error":{"message":"404 page not found"}}`))
	if _, err := p.Image(ctx, ImageRequest{Prompt: "x"}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("404: %v", err)
	}
	if _, err := p.Image(ctx, ImageRequest{Prompt: "x", Aspect: "2:1"}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("aspect: %v", err)
	}
}

func TestOpenAISizes(t *testing.T) {
	cases := []struct{ model, aspect, want string }{
		{"gpt-image-2.5-flare", AspectOG, "1536x800"},
		{"gpt-image-2.5-sunburst", Aspect4x3, "1536x1152"},
		{"gpt-image-1", Aspect16x9, "1536x1024"},
		{"gpt-image-1", Aspect1x1, "1024x1024"},
		{"gpt-image-1", Aspect4x3, "1536x1024"},
		{"dall-e-3", Aspect16x9, "1792x1024"},
		{"dall-e-3", Aspect1x1, "1024x1024"},
	}
	for _, c := range cases {
		if got := openAISize(c.model, c.aspect); got != c.want {
			t.Errorf("%s %s = %s, want %s", c.model, c.aspect, got, c.want)
		}
	}
}

func TestGeminiText(t *testing.T) {
	var got captured
	p := newTestProvider(t, ProviderGemini, func(w http.ResponseWriter, r *http.Request) {
		got = capture(r)
		io.WriteString(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"pensando","thought":true},{"text":"{\"a\":1}"}]},"finishReason":"STOP"}],
			"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3,"thoughtsTokenCount":4},"modelVersion":"gemini-3.8-flash-001"}`)
	})
	res, err := p.Text(ctx, TextRequest{System: "sys", JSON: true, Schema: seoSchema,
		Messages: []Message{{RoleUser, "a"}, {RoleAssistant, "b"}, {RoleUser, "c"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got.path != "/v1beta/models/gemini-3.8-flash:generateContent" || got.header.Get("x-goog-api-key") != testKey {
		t.Errorf("request %s key=%q", got.path, got.header.Get("x-goog-api-key"))
	}
	if sys, _ := get(got.body, "systemInstruction").(map[string]any); sys == nil || !strings.Contains(get(sys["parts"].([]any)[0].(map[string]any), "text").(string), "sys") {
		t.Errorf("systemInstruction: %v", got.body["systemInstruction"])
	}
	var roles []string
	for _, c := range got.body["contents"].([]any) {
		roles = append(roles, c.(map[string]any)["role"].(string))
	}
	if strings.Join(roles, ",") != "user,model,user" {
		t.Errorf("roles %v", roles)
	}
	if get(got.body, "generationConfig", "responseMimeType") != "application/json" || get(got.body, "generationConfig", "responseJsonSchema") == nil {
		t.Errorf("generationConfig: %v", got.body["generationConfig"])
	}
	if res.Text != `{"a":1}` || res.InputTokens != 7 || res.OutputTokens != 7 || res.StopReason != StopEnd || res.Model != "gemini-3.8-flash-001" {
		t.Errorf("response: %+v", res)
	}
}

func TestGeminiBlockedAndBadKey(t *testing.T) {
	p := newTestProvider(t, ProviderGemini, reply(200, `{"promptFeedback":{"blockReason":"SAFETY"}}`))
	res, err := p.Text(ctx, TextRequest{Messages: userMsg("x")})
	if err != nil || res.StopReason != StopFiltered {
		t.Errorf("blocked: %+v %v", res, err)
	}
	p = newTestProvider(t, ProviderGemini, reply(400, `{"error":{"code":400,"message":"API key not valid. Please pass a valid API key.","status":"INVALID_ARGUMENT",
		"details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"API_KEY_INVALID"}]}}`))
	if _, err := p.Text(ctx, TextRequest{Messages: userMsg("x")}); !errors.Is(err, ErrAuth) {
		t.Errorf("bad key: %v", err)
	}
}

func TestGeminiImage(t *testing.T) {
	img := pngBytes(t, 19, 10)
	var calls atomic.Int32
	var got captured
	p := newTestProvider(t, ProviderGemini, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		got = capture(r)
		io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"Here it is"},{"inlineData":{"mimeType":"image/jpeg","data":"`+
			base64.StdEncoding.EncodeToString(img)+`"}}]},"finishReason":"STOP"}]}`)
	})
	imgs, err := p.Image(ctx, ImageRequest{Prompt: "a harbour", Aspect: AspectOG, N: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got.path != "/v1beta/models/gemini-nano-banana-2.1:generateContent" || calls.Load() != 2 {
		t.Errorf("path %s, %d calls", got.path, calls.Load())
	}
	if get(got.body, "generationConfig", "imageConfig", "aspectRatio") != "16:9" {
		t.Errorf("generationConfig: %v", got.body["generationConfig"])
	}
	if mods, _ := get(got.body, "generationConfig", "responseModalities").([]any); len(mods) != 2 || mods[1] != "IMAGE" {
		t.Errorf("responseModalities: %v", mods)
	}
	// The MIME type comes from the bytes, not from the provider's label.
	if len(imgs) != 2 || imgs[0].MIME != "image/png" || imgs[0].Width != 19 {
		t.Errorf("images: %+v", imgs)
	}

	p = newTestProvider(t, ProviderGemini, reply(200, `{"candidates":[{"content":{"parts":[{"text":"no"}]},"finishReason":"IMAGE_SAFETY"}]}`))
	if _, err := p.Image(ctx, ImageRequest{Prompt: "x"}); !errors.Is(err, ErrRefused) {
		t.Errorf("filtered image: %v", err)
	}
}

func TestNewValidatesConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		ok   bool
	}{
		{"defaults", Config{Provider: "Anthropic", APIKey: "k"}, true},
		{"https base", Config{Provider: ProviderOpenAI, BaseURL: "https://openrouter.ai/api/v1/", APIKey: "k", TextModel: "anthropic/claude-opus-5-5"}, true},
		{"ollama", Config{Provider: ProviderOpenAI, BaseURL: "http://127.0.0.1:11434/v1", AllowInsecureLocal: true}, true},
		{"llama.cpp localhost", Config{Provider: ProviderOpenAI, BaseURL: "http://localhost:8080/v1", AllowInsecureLocal: true}, true},
		{"ipv6 loopback", Config{Provider: ProviderOpenAI, BaseURL: "http://[::1]:8080/v1", AllowInsecureLocal: true}, true},
		{"http not allowed", Config{Provider: ProviderOpenAI, BaseURL: "http://127.0.0.1:11434/v1"}, false},
		{"http remote", Config{Provider: ProviderOpenAI, BaseURL: "http://example.com/v1", AllowInsecureLocal: true}, false},
		{"credentials in URL", Config{Provider: ProviderOpenAI, BaseURL: "https://user:pw@example.com/v1", APIKey: "k"}, false},
		{"query", Config{Provider: ProviderGemini, BaseURL: "https://example.com/v1beta?key=x", APIKey: "k"}, false},
		{"unknown provider", Config{Provider: "mistral", APIKey: "k"}, false},
		{"missing key", Config{Provider: ProviderGemini}, false},
		{"key with newline", Config{Provider: ProviderAnthropic, APIKey: "k\r\nX-Evil: 1"}, false},
		{"model injection", Config{Provider: ProviderGemini, APIKey: "k", TextModel: "../../x"}, false},
		{"gemini model with colon", Config{Provider: ProviderGemini, APIKey: "k", TextModel: "gemini:x"}, false},
	}
	for _, c := range cases {
		p, err := New(c.cfg, nil)
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v", c.name, err)
		}
		if err != nil && c.cfg.APIKey != "" && len(c.cfg.APIKey) > 2 && strings.Contains(err.Error(), c.cfg.APIKey) {
			t.Errorf("%s: key in error", c.name)
		}
		if p != nil && p.Name() != strings.ToLower(c.cfg.Provider) {
			t.Errorf("%s: name %s", c.name, p.Name())
		}
	}
	if _, err := New(Config{Provider: ProviderOpenAI, BaseURL: "http://example.com"}, nil); !errors.Is(err, ErrInsecureURL) {
		t.Errorf("want ErrInsecureURL, got %v", err)
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	var leaked atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "" {
			leaked.Store(true)
		}
	}))
	defer other.Close()
	p := newTestProvider(t, ProviderAnthropic, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/v1/messages", http.StatusTemporaryRedirect)
	})
	_, err := p.Text(ctx, TextRequest{Messages: userMsg("x")})
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != http.StatusTemporaryRedirect {
		t.Errorf("err = %v", err)
	}
	if leaked.Load() {
		t.Error("the API key followed the redirect")
	}
}

func TestTimeout(t *testing.T) {
	p := newTestProvider(t, ProviderOpenAI, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}, func(c *Config) { c.Timeout = 50 * time.Millisecond })
	start := time.Now()
	_, err := p.Text(ctx, TextRequest{Messages: userMsg("x")})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Errorf("err = %v after %v", err, time.Since(start))
	}
}

func TestMetered(t *testing.T) {
	p := newTestProvider(t, ProviderAnthropic, reply(200, `{"type":"message","content":[{"type":"text","text":"x"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":2}}`))
	m := Meter(p)
	for range 2 {
		if _, err := m.Text(ctx, TextRequest{Messages: userMsg("x")}); err != nil {
			t.Fatal(err)
		}
	}
	if u := m.Usage(); u.Requests != 2 || u.InputTokens != 6 || u.OutputTokens != 4 || m.Name() != ProviderAnthropic {
		t.Errorf("usage %+v", u)
	}
}

func TestModels(t *testing.T) {
	text, img := Models(ProviderAnthropic)
	var ids []string
	for _, m := range text {
		ids = append(ids, m.ID)
	}
	if strings.Join(ids, ",") != "claude-opus-5-5,claude-sonnet-5-5,claude-haiku-5-5" || len(img) != 0 {
		t.Errorf("anthropic models %v %v", ids, img)
	}
	if DefaultTextModel(ProviderGemini) == "" || DefaultImageModel(ProviderOpenAI) == "" || DefaultImageModel(ProviderAnthropic) != "" {
		t.Error("defaults")
	}
}

func TestExtractJSON(t *testing.T) {
	cases := map[string]string{
		`{"a":1}`:                           `{"a":1}`,
		"```json\n{\"a\": {\"b\": 2}}\n```": `{"a": {"b": 2}}`,
		`Here: {not json} then {"x":"{"}`:   `{"x":"{"}`,
		`{"s":"quote \" and } brace"} tail`: `{"s":"quote \" and } brace"}`,
	}
	for in, want := range cases {
		got, err := extractJSON(in)
		if err != nil || got != want {
			t.Errorf("extractJSON(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "no json", `{"a":`, `[1,2]`} {
		if _, err := extractJSON(bad); !errors.Is(err, ErrBadResponse) {
			t.Errorf("extractJSON(%q) should fail", bad)
		}
	}
}
