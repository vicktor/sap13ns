package ai

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func sse(events ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range events {
			io.WriteString(w, e+"\n\n")
			w.(http.Flusher).Flush()
		}
	}
}

func streamer(t *testing.T, p Provider) Streamer {
	t.Helper()
	s, ok := p.(Streamer)
	if !ok {
		t.Fatalf("%s does not implement Streamer", p.Name())
	}
	return s
}

// collect runs a stream and returns the visible deltas and how many
// thinking signals arrived.
func collect(t *testing.T, s Streamer, req TextRequest) (*TextResponse, []string, int, error) {
	t.Helper()
	var texts []string
	thinking := 0
	res, err := s.TextStream(ctx, req, func(d Delta) {
		if d.Thinking {
			thinking++
		} else {
			texts = append(texts, d.Text)
		}
	})
	return res, texts, thinking, err
}

func TestOpenAIStream(t *testing.T) {
	var got captured
	h := sse(
		`data: {"model":"qwen","choices":[{"delta":{"reasoning_content":"hmm"}}]}`,
		`data: {"choices":[{"delta":{"content":"Hola"}}]}`,
		`: keep-alive comment`,
		`data: {"choices":[{"delta":{"content":" mundo [1]"},"finish_reason":"stop"}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":11,"completion_tokens":4}}`,
		`data: [DONE]`,
	)
	p := newTestProvider(t, ProviderOpenAI, func(w http.ResponseWriter, r *http.Request) { got = capture(r); h(w, r) },
		func(c *Config) { c.NoThinking = true })
	res, texts, thinking, err := collect(t, streamer(t, p), TextRequest{System: "Be brief.", Messages: userMsg("hi")})
	if err != nil {
		t.Fatal(err)
	}
	if got.body["stream"] != true || get(got.body, "stream_options", "include_usage") != true {
		t.Errorf("stream not requested: %v", got.body)
	}
	if get(got.body, "chat_template_kwargs", "enable_thinking") != false {
		t.Errorf("no_thinking not forwarded: %v", got.body)
	}
	if got.header.Get("Accept") != "text/event-stream" {
		t.Errorf("Accept = %q", got.header.Get("Accept"))
	}
	if strings.Join(texts, "|") != "Hola| mundo [1]" || thinking != 1 {
		t.Errorf("deltas %q, thinking %d", texts, thinking)
	}
	if res.Text != "Hola mundo [1]" || res.Model != "qwen" || res.InputTokens != 11 || res.OutputTokens != 4 || res.StopReason != StopEnd {
		t.Errorf("response: %+v", res)
	}
}

// A <think> block in the content (llama.cpp, Ollama) is hidden, even when
// its tags are split across deltas.
func TestOpenAIStreamThinkTags(t *testing.T) {
	p := newTestProvider(t, ProviderOpenAI, sse(
		`data: {"choices":[{"delta":{"content":"<thi"}}]}`,
		`data: {"choices":[{"delta":{"content":"nk>weighing options"}}]}`,
		`data: {"choices":[{"delta":{"content":" more thoughts</th"}}]}`,
		`data: {"choices":[{"delta":{"content":"ink>\n\nAnswer"}}]}`,
		`data: {"choices":[{"delta":{"content":" here."},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	))
	res, texts, thinking, err := collect(t, streamer(t, p), TextRequest{Messages: userMsg("q")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "Answer here." || strings.Join(texts, "") != "Answer here." || thinking == 0 {
		t.Errorf("text %q, deltas %q, thinking %d", res.Text, texts, thinking)
	}
}

func TestOpenAIStreamFallbacks(t *testing.T) {
	// A server that rejects stream_options is asked again without it.
	var bodies []map[string]any
	p := newTestProvider(t, ProviderOpenAI, func(w http.ResponseWriter, r *http.Request) {
		bodies = append(bodies, capture(r).body)
		if len(bodies) == 1 {
			reply(400, `{"error":{"message":"Unrecognized request argument supplied: stream_options"}}`)(w, r)
			return
		}
		sse(`data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`, `data: [DONE]`)(w, r)
	})
	res, _, _, err := collect(t, streamer(t, p), TextRequest{Messages: userMsg("q")})
	if err != nil || res.Text != "ok" || len(bodies) != 2 || bodies[1]["stream_options"] != nil {
		t.Errorf("retry: %+v %v (%d requests)", res, err, len(bodies))
	}

	// A server that ignores "stream" and answers with one JSON body.
	whole := newTestProvider(t, ProviderOpenAI, reply(200, `{"model":"m","choices":[{"message":{"content":"<think>x</think>Entero"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`))
	res, texts, _, err := collect(t, streamer(t, whole), TextRequest{Messages: userMsg("q")})
	if err != nil || res.Text != "Entero" || len(texts) != 1 || texts[0] != "Entero" || res.InputTokens != 3 {
		t.Errorf("plain JSON answer: %+v %q %v", res, texts, err)
	}

	// Errors before the stream starts keep their meaning and redact the key.
	bad := newTestProvider(t, ProviderOpenAI, reply(429, `{"error":{"message":"slow down `+testKey+`"}}`))
	_, _, _, err = collect(t, streamer(t, bad), TextRequest{Messages: userMsg("q")})
	if !errors.Is(err, ErrRateLimited) || strings.Contains(err.Error(), testKey) {
		t.Errorf("err = %v", err)
	}

	// The whole budget spent thinking.
	starved := newTestProvider(t, ProviderOpenAI, sse(`data: {"choices":[{"delta":{"reasoning_content":"..."},"finish_reason":"length"}]}`, `data: [DONE]`))
	if _, _, _, err := collect(t, streamer(t, starved), TextRequest{Messages: userMsg("q")}); !errors.Is(err, ErrThinking) {
		t.Errorf("err = %v, want ErrThinking", err)
	}
}

func TestAnthropicStream(t *testing.T) {
	var got captured
	h := sse(
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-opus-5-5\",\"usage\":{\"input_tokens\":20,\"cache_read_input_tokens\":5}}}",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\"}}",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"secret\"}}",
		"event: ping\ndata: {\"type\":\"ping\"}",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"Sí, \"}}",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"claro [2].\"}}",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":9}}",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}",
	)
	p := newTestProvider(t, ProviderAnthropic, func(w http.ResponseWriter, r *http.Request) { got = capture(r); h(w, r) })
	res, texts, thinking, err := collect(t, streamer(t, p), TextRequest{System: "S", Messages: userMsg("¿Puedo?")})
	if err != nil {
		t.Fatal(err)
	}
	if got.path != "/v1/messages" || got.body["stream"] != true || got.body["system"] != "S" || got.header.Get("x-api-key") != testKey {
		t.Errorf("request %s %v", got.path, got.body)
	}
	if strings.Join(texts, "") != "Sí, claro [2]." || thinking != 1 || strings.Contains(strings.Join(texts, ""), "secret") {
		t.Errorf("deltas %q thinking %d", texts, thinking)
	}
	if res.Text != "Sí, claro [2]." || res.InputTokens != 25 || res.OutputTokens != 9 || res.StopReason != StopEnd || res.Model != "claude-opus-5-5" {
		t.Errorf("response: %+v", res)
	}

	overloaded := newTestProvider(t, ProviderAnthropic, sse("event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}"))
	if _, _, _, err := collect(t, streamer(t, overloaded), TextRequest{Messages: userMsg("q")}); !errors.Is(err, ErrRateLimited) {
		t.Errorf("err = %v, want ErrRateLimited", err)
	}
}

func TestGeminiStream(t *testing.T) {
	var got captured
	var query string
	h := sse(
		`data: {"candidates":[{"content":{"parts":[{"text":"pensando","thought":true}]}}]}`,
		`data: {"candidates":[{"content":{"parts":[{"text":"Hello "}]}}],"modelVersion":"gemini-3.8-flash"}`,
		`data: {"candidates":[{"content":{"parts":[{"text":"world"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":2,"thoughtsTokenCount":5}}`,
	)
	p := newTestProvider(t, ProviderGemini, func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		got = capture(r)
		h(w, r)
	})
	res, texts, thinking, err := collect(t, streamer(t, p), TextRequest{System: "S", Messages: userMsg("hi")})
	if err != nil {
		t.Fatal(err)
	}
	if got.path != "/v1beta/models/gemini-3.8-flash:streamGenerateContent" || query != "alt=sse" {
		t.Errorf("request %s?%s", got.path, query)
	}
	if get(got.body, "systemInstruction") == nil {
		t.Errorf("system instruction missing: %v", got.body)
	}
	if strings.Join(texts, "") != "Hello world" || thinking != 1 {
		t.Errorf("deltas %q thinking %d", texts, thinking)
	}
	if res.Text != "Hello world" || res.InputTokens != 8 || res.OutputTokens != 7 || res.StopReason != StopEnd {
		t.Errorf("response: %+v", res)
	}
}

// Metered falls back to Text for providers without streaming and counts
// usage either way.
type plainProvider struct{ Provider }

func TestMeteredStreamFallback(t *testing.T) {
	p := newTestProvider(t, ProviderOpenAI, reply(200, `{"choices":[{"message":{"content":"todo junto"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3}}`))
	m := Meter(plainProvider{p})
	var texts []string
	res, err := m.TextStream(ctx, TextRequest{Messages: userMsg("q")}, func(d Delta) { texts = append(texts, d.Text) })
	if err != nil || res.Text != "todo junto" || len(texts) != 1 {
		t.Fatalf("fallback: %+v %q %v", res, texts, err)
	}
	if u := m.Usage(); u.Requests != 1 || u.InputTokens != 2 || u.OutputTokens != 3 {
		t.Errorf("usage %+v", u)
	}
}

func TestThinkFilter(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"Plain answer"}, "Plain answer"},
		{[]string{"  ", "<", "b>bold</b>"}, "  <b>bold</b>"},
		{[]string{"<think>", "a", "</think>", "B"}, "B"},
		{[]string{"<thi"}, "<thi"}, // flushed at the end
		{[]string{"<think>never closed"}, ""},
	}
	for _, c := range cases {
		var f thinkFilter
		var out strings.Builder
		for _, s := range c.in {
			v, _ := f.push(s)
			out.WriteString(v)
		}
		out.WriteString(f.flush())
		if out.String() != c.want {
			t.Errorf("%q -> %q, want %q", c.in, out.String(), c.want)
		}
	}
}

// OpenAI now and then answers a model the project can use with a 403 that
// says it cannot: the request is asked again, a couple of times.
func TestOpenAIModelAccessRetry(t *testing.T) {
	old := modelAccessRetries
	modelAccessRetries = []time.Duration{time.Millisecond, time.Millisecond}
	t.Cleanup(func() { modelAccessRetries = old })
	denied := `{"error":{"message":"Project ` + "`proj_x`" + ` does not have access to model ` + "`gpt-x`" + `","type":"invalid_request_error"}}`
	var calls atomic.Int32
	flaky := func(fails int32, ok http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) <= fails {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				io.WriteString(w, denied)
				return
			}
			ok(w, r)
		}
	}
	answer := sse(`data: {"choices":[{"delta":{"content":"Hi"}}]}`, `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`, `data: [DONE]`)

	p := newTestProvider(t, ProviderOpenAI, flaky(2, answer))
	if res, _, _, err := collect(t, streamer(t, p), TextRequest{Messages: userMsg("hi")}); err != nil || res.Text != "Hi" || calls.Load() != 3 {
		t.Fatalf("stream after two 403s: %v %+v, %d calls", err, res, calls.Load())
	}

	calls.Store(0)
	p = newTestProvider(t, ProviderOpenAI, flaky(1, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"content":"Hi"},"finish_reason":"stop"}]}`)
	}))
	if res, err := p.Text(ctx, TextRequest{Messages: userMsg("hi")}); err != nil || res.Text != "Hi" || calls.Load() != 2 {
		t.Fatalf("text after a 403: %v %+v, %d calls", err, res, calls.Load())
	}

	// Denied every time: the error, after three tries.
	calls.Store(0)
	p = newTestProvider(t, ProviderOpenAI, flaky(99, answer))
	if _, _, _, err := collect(t, streamer(t, p), TextRequest{Messages: userMsg("hi")}); !isModelAccess(err) || calls.Load() != 3 {
		t.Fatalf("always denied: %v, %d calls", err, calls.Load())
	}

	// Other 403s are not asked again.
	calls.Store(0)
	p = newTestProvider(t, ProviderOpenAI, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"error":{"message":"Country, region, or territory not supported","type":"request_forbidden"}}`)
	})
	if _, err := p.Text(ctx, TextRequest{Messages: userMsg("hi")}); err == nil || calls.Load() != 1 {
		t.Fatalf("other 403: %v, %d calls", err, calls.Load())
	}
}
