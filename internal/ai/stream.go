package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
)

// Streaming text answers for the site assistant. The three adapters
// implement Streamer over Server-Sent Events:
//   - OpenAI-compatible: "stream": true, chunks with choices[0].delta and,
//     with stream_options.include_usage, a final usage chunk, then [DONE];
//   - Anthropic: message_start / content_block_delta (text_delta,
//     thinking_delta) / message_delta / message_stop events
//     (https://platform.claude.com/docs/en/build-with-claude/streaming);
//   - Gemini: streamGenerateContent?alt=sse, each event a partial
//     GenerateContentResponse.
// Metered.TextStream falls back to Text for providers without streaming.

// Delta is a piece of a streamed answer. Thinking deltas carry no text:
// they only tell the caller that a reasoning model is still working.
type Delta struct {
	Text     string
	Thinking bool
}

// Streamer is implemented by providers that can stream text answers. The
// returned response holds the whole answer, as Text would. JSON mode is not
// supported while streaming.
type Streamer interface {
	TextStream(ctx context.Context, req TextRequest, fn func(Delta)) (*TextResponse, error)
}

// TextStream streams through the wrapped provider when it can, and
// otherwise answers with Text and passes the whole answer as one delta.
func (m *Metered) TextStream(ctx context.Context, req TextRequest, fn func(Delta)) (*TextResponse, error) {
	var res *TextResponse
	var err error
	if s, ok := m.Provider.(Streamer); ok && !req.JSON {
		res, err = s.TextStream(ctx, req, fn)
	} else {
		res, err = m.Provider.Text(ctx, req)
		if err == nil && res.Text != "" {
			fn(Delta{Text: res.Text})
		}
	}
	m.mu.Lock()
	m.u.Requests++
	if res != nil {
		m.u.InputTokens += res.InputTokens
		m.u.OutputTokens += res.OutputTokens
		if res.Model != "" {
			m.u.TextModel = res.Model
		}
	}
	m.mu.Unlock()
	return res, err
}

// openStream posts a JSON request that asks for an event stream and returns
// the response once its status is known to be 2xx.
func (t *transport) openStream(ctx context.Context, endpoint string, hdr http.Header, in any) (*http.Response, error) {
	body, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, t.errorf("%w: cannot build request", ErrInvalidInput)
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", "sap13ns")
	resp, err := t.api.Do(req)
	if err != nil {
		return nil, t.requestError(ctx, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return nil, t.apiError(resp, raw)
	}
	return resp, nil
}

// isEventStream reports whether a response is SSE; some compatible servers
// ignore "stream": true and answer with plain JSON.
func isEventStream(resp *http.Response) bool {
	mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	return mt == "text/event-stream"
}

// readSSE calls fn for every event of an SSE stream until the stream ends,
// fn returns errStreamDone or the byte limit is reached.
func (t *transport) readSSE(ctx context.Context, r io.Reader, fn func(event string, data []byte) error) error {
	sc := bufio.NewScanner(io.LimitReader(r, maxTextResponse))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	var event string
	var data bytes.Buffer
	dispatch := func() error {
		if data.Len() == 0 {
			event = ""
			return nil
		}
		err := fn(event, bytes.TrimSuffix(data.Bytes(), []byte("\n")))
		event = ""
		data.Reset()
		return err
	}
	for sc.Scan() {
		line := sc.Bytes()
		switch {
		case len(line) == 0:
			if err := dispatch(); err != nil {
				return err
			}
		case line[0] == ':':
		default:
			field, value, _ := bytes.Cut(line, []byte(":"))
			value = bytes.TrimPrefix(value, []byte(" "))
			switch string(field) {
			case "event":
				event = string(value)
			case "data":
				data.Write(value)
				data.WriteByte('\n')
			}
		}
	}
	if err := sc.Err(); err != nil {
		if ctx.Err() != nil {
			return t.requestError(ctx, err)
		}
		return t.errorf("%w: %v", ErrBadResponse, err)
	}
	return dispatch()
}

var errStreamDone = errors.New("stream done")

// thinkFilter hides a leading <think>…</think> block from streamed content
// (local reasoning models served by llama.cpp or Ollama put it there), the
// streaming counterpart of stripThink.
type thinkFilter struct {
	state int // 0 undecided, 1 inside <think>, 2 passing through
	buf   strings.Builder
}

// push returns the visible part of a content delta and whether the model
// is still thinking.
func (f *thinkFilter) push(s string) (string, bool) {
	switch f.state {
	case 2:
		return s, false
	case 0:
		f.buf.WriteString(s)
		t := strings.TrimLeft(f.buf.String(), " \t\r\n")
		switch {
		case t == "" || strings.HasPrefix("<think>", t):
			return "", false // not decided yet
		case strings.HasPrefix(t, "<think>"):
			f.state = 1
			rest := t[len("<think>"):]
			f.buf.Reset()
			return f.push(rest)
		default:
			f.state = 2
			out := f.buf.String()
			f.buf.Reset()
			return out, false
		}
	default:
		f.buf.WriteString(s)
		b := f.buf.String()
		if i := strings.Index(b, "</think>"); i >= 0 {
			f.state = 2
			f.buf.Reset()
			return strings.TrimLeft(b[i+len("</think>"):], " \t\r\n"), false
		}
		if len(b) > 16 { // keep only what could hold a split "</think>"
			f.buf.Reset()
			f.buf.WriteString(b[len(b)-8:])
		}
		return "", true
	}
}

// flush returns content held back while undecided (an answer shorter than
// "<think>", or one that never closed its think block is dropped).
func (f *thinkFilter) flush() string {
	if f.state == 0 {
		return f.buf.String()
	}
	return ""
}

// emitter accumulates the visible answer and forwards deltas.
type emitter struct {
	fn    func(Delta)
	text  strings.Builder
	think thinkFilter
}

func (e *emitter) content(s string) {
	if s == "" {
		return
	}
	vis, thinking := e.think.push(s)
	if vis != "" {
		e.text.WriteString(vis)
		e.fn(Delta{Text: vis})
	} else if thinking {
		e.fn(Delta{Thinking: true})
	}
}

func (e *emitter) thinking() { e.fn(Delta{Thinking: true}) }

func (e *emitter) finish() string {
	if rest := e.think.flush(); rest != "" {
		e.text.WriteString(rest)
		e.fn(Delta{Text: rest})
	}
	return strings.TrimSpace(e.text.String())
}

// --------------------------------------------------------- OpenAI-compatible

type openAIStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type openAIStreamRequest struct {
	openAIChatRequest
	Stream        bool                 `json:"stream"`
	StreamOptions *openAIStreamOptions `json:"stream_options,omitempty"`
}

type openAIStreamChunk struct {
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			Refusal          string `json:"refusal"`
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error json.RawMessage `json:"error"`
}

func (o *openAI) TextStream(ctx context.Context, req TextRequest, fn func(Delta)) (*TextResponse, error) {
	if err := validateText(req); err != nil {
		return nil, err
	}
	msgs := make([]Message, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, Message{Role: "system", Content: req.System})
	}
	msgs = append(msgs, req.Messages...)
	body := openAIStreamRequest{openAIChatRequest: openAIChatRequest{Model: o.model, Messages: msgs, Temperature: req.Temperature},
		Stream: true, StreamOptions: &openAIStreamOptions{IncludeUsage: true}}
	if o.official {
		body.MaxCompletionTokens = tokenLimit(req.MaxTokens, true)
	} else {
		body.MaxTokens = tokenLimit(req.MaxTokens, true)
		if o.noThinking {
			body.ChatTemplateKwargs = map[string]any{"enable_thinking": false}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, o.t.textTimeout)
	defer cancel()
	var resp *http.Response
	err := retryModelAccess(ctx, func() (err error) {
		resp, err = o.t.openStream(ctx, o.base+"/chat/completions", o.header(), body)
		return err
	})
	if err != nil && rejectsStreamOptions(err) { // servers that do not know stream_options
		body.StreamOptions = nil
		resp, err = o.t.openStream(ctx, o.base+"/chat/completions", o.header(), body)
	}
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if !isEventStream(resp) {
		return o.decodeWhole(ctx, req, resp, fn)
	}
	em := &emitter{fn: fn}
	res := &TextResponse{Model: o.model}
	var finishReason, refusal string
	err = o.t.readSSE(ctx, resp.Body, func(_ string, data []byte) error {
		if string(data) == "[DONE]" {
			return errStreamDone
		}
		var c openAIStreamChunk
		if err := json.Unmarshal(data, &c); err != nil {
			return o.t.errorf("%w: %v", ErrBadResponse, err)
		}
		if len(c.Error) > 0 && string(c.Error) != "null" {
			return o.t.errorf("%w: %s", ErrBadResponse, o.t.clean(string(c.Error), maxProviderMessage))
		}
		if c.Model != "" {
			res.Model = c.Model
		}
		if c.Usage != nil {
			res.InputTokens, res.OutputTokens = c.Usage.PromptTokens, c.Usage.CompletionTokens
		}
		if len(c.Choices) > 0 {
			ch := c.Choices[0]
			if ch.Delta.ReasoningContent != "" || ch.Delta.Reasoning != "" {
				em.thinking()
			}
			em.content(ch.Delta.Content)
			refusal += ch.Delta.Refusal
			if ch.FinishReason != "" {
				finishReason = ch.FinishReason
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, errStreamDone) {
		return nil, err
	}
	res.Text = em.finish()
	switch {
	case refusal != "":
		res.StopReason = StopRefusal
	case finishReason == "stop":
		res.StopReason = StopEnd
	case finishReason == "length":
		res.StopReason = StopLength
	case finishReason == "content_filter":
		res.StopReason = StopFiltered
	default:
		res.StopReason = StopOther
	}
	return streamFinish(res)
}

// decodeWhole handles a server that answered a streaming request with one
// JSON body.
func (o *openAI) decodeWhole(ctx context.Context, req TextRequest, resp *http.Response, fn func(Delta)) (*TextResponse, error) {
	raw, err := readLimited(resp.Body, maxTextResponse)
	if err != nil {
		if ctx.Err() != nil {
			return nil, o.t.requestError(ctx, err)
		}
		return nil, o.t.errorf("%w", err)
	}
	var out openAIChatResponse
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Choices) == 0 {
		return nil, o.t.errorf("%w: not a chat completion", ErrBadResponse)
	}
	text, ok := openAIContent(out.Choices[0].Message.Content)
	if !ok {
		return nil, o.t.errorf("%w: unexpected content", ErrBadResponse)
	}
	res := &TextResponse{Text: text, Model: out.Model, InputTokens: out.Usage.PromptTokens, OutputTokens: out.Usage.CompletionTokens,
		StopReason: map[string]string{"stop": StopEnd, "length": StopLength, "content_filter": StopFiltered}[out.Choices[0].FinishReason]}
	if res.StopReason == "" {
		res.StopReason = StopOther
	}
	res, err = finish(o.t, req, res)
	if err == nil && res.Text != "" {
		fn(Delta{Text: res.Text})
	}
	return res, err
}

func rejectsStreamOptions(err error) bool {
	var e *APIError
	return errors.As(err, &e) && (e.Status == http.StatusBadRequest || e.Status == http.StatusUnprocessableEntity) &&
		strings.Contains(strings.ToLower(e.Message), "stream_options")
}

// streamFinish maps a streamed answer cut off before any visible text to
// the same errors as Text.
func streamFinish(res *TextResponse) (*TextResponse, error) {
	if res.StopReason == StopLength && res.Text == "" {
		return nil, ErrThinking
	}
	return res, nil
}

// ------------------------------------------------------------- Anthropic --

type anthropicStreamRequest struct {
	anthropicRequest
	Stream bool `json:"stream"`
}

type anthropicEvent struct {
	Type    string `json:"type"`
	Message struct {
		Model string `json:"model"`
		Usage struct {
			InputTokens              int `json:"input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
	Delta struct {
		Type       string `json:"type"`
		Text       string `json:"text"`
		StopReason string `json:"stop_reason"`
	} `json:"delta"`
	Usage struct {
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func (a *anthropic) TextStream(ctx context.Context, req TextRequest, fn func(Delta)) (*TextResponse, error) {
	if err := validateText(req); err != nil {
		return nil, err
	}
	if req.Messages[len(req.Messages)-1].Role != RoleUser {
		return nil, a.t.errorf("%w: the conversation must end with a user message", ErrInvalidInput)
	}
	body := anthropicStreamRequest{anthropicRequest: anthropicRequest{Model: a.model, MaxTokens: tokenLimit(req.MaxTokens, true),
		System: req.System, Messages: req.Messages}, Stream: true}
	ctx, cancel := context.WithTimeout(ctx, a.t.textTimeout)
	defer cancel()
	hdr := http.Header{}
	hdr.Set("x-api-key", a.t.key)
	hdr.Set("anthropic-version", anthropicVersion)
	resp, err := a.t.openStream(ctx, a.base+"/v1/messages", hdr, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	em := &emitter{fn: fn}
	res := &TextResponse{Model: a.model}
	var stop string
	err = a.t.readSSE(ctx, resp.Body, func(_ string, data []byte) error {
		var ev anthropicEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			return a.t.errorf("%w: %v", ErrBadResponse, err)
		}
		switch ev.Type {
		case "message_start":
			u := ev.Message.Usage
			res.InputTokens = u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
			if ev.Message.Model != "" {
				res.Model = ev.Message.Model
			}
		case "content_block_delta":
			switch ev.Delta.Type {
			case "text_delta":
				em.content(ev.Delta.Text)
			case "thinking_delta":
				em.thinking()
			}
		case "message_delta":
			stop = ev.Delta.StopReason
			res.OutputTokens = ev.Usage.OutputTokens
		case "message_stop":
			return errStreamDone
		case "error":
			e := &APIError{Provider: a.t.provider, Status: http.StatusOK, Type: a.t.clean(ev.Error.Type, 60),
				Message: a.t.clean(ev.Error.Message, maxProviderMessage)}
			if ev.Error.Type == "overloaded_error" || ev.Error.Type == "rate_limit_error" {
				e.kind = ErrRateLimited
			}
			return e
		}
		return nil
	})
	if err != nil && !errors.Is(err, errStreamDone) {
		return nil, err
	}
	res.Text = em.finish()
	switch stop {
	case "end_turn", "stop_sequence":
		res.StopReason = StopEnd
	case "max_tokens", "model_context_window_exceeded":
		res.StopReason = StopLength
	case "refusal":
		res.StopReason = StopRefusal
	default:
		res.StopReason = StopOther
	}
	return streamFinish(res)
}

// ---------------------------------------------------------------- Gemini --

func (g *gemini) TextStream(ctx context.Context, req TextRequest, fn func(Delta)) (*TextResponse, error) {
	if err := validateText(req); err != nil {
		return nil, err
	}
	body := geminiRequest{GenerationConfig: geminiGenConfig{MaxOutputTokens: tokenLimit(req.MaxTokens, true), Temperature: req.Temperature}}
	if req.System != "" {
		body.SystemInstruction = &geminiContent{Parts: []geminiPart{{Text: req.System}}}
	}
	for _, m := range req.Messages {
		role := "user"
		if m.Role == RoleAssistant {
			role = "model"
		}
		body.Contents = append(body.Contents, geminiContent{Role: role, Parts: []geminiPart{{Text: m.Content}}})
	}
	ctx, cancel := context.WithTimeout(ctx, g.t.textTimeout)
	defer cancel()
	hdr := http.Header{}
	hdr.Set("x-goog-api-key", g.t.key)
	endpoint := g.base + "/models/" + url.PathEscape(g.model) + ":streamGenerateContent?alt=sse"
	resp, err := g.t.openStream(ctx, endpoint, hdr, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	em := &emitter{fn: fn}
	res := &TextResponse{Model: g.model}
	var finishReason, block string
	err = g.t.readSSE(ctx, resp.Body, func(_ string, data []byte) error {
		var out geminiResponse
		if err := json.Unmarshal(data, &out); err != nil {
			return g.t.errorf("%w: %v", ErrBadResponse, err)
		}
		if out.ModelVersion != "" {
			res.Model = out.ModelVersion
		}
		if u := out.UsageMetadata; u.PromptTokenCount > 0 || u.CandidatesTokenCount > 0 {
			res.InputTokens, res.OutputTokens = u.PromptTokenCount, u.CandidatesTokenCount+u.ThoughtsTokenCount
		}
		if out.PromptFeedback.BlockReason != "" {
			block = out.PromptFeedback.BlockReason
		}
		for _, c := range out.Candidates[:min(1, len(out.Candidates))] {
			for _, p := range c.Content.Parts {
				if p.Thought {
					em.thinking()
				} else {
					em.content(p.Text)
				}
			}
			if c.FinishReason != "" {
				finishReason = c.FinishReason
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	res.Text = em.finish()
	res.StopReason = geminiStop(finishReason, block)
	return streamFinish(res)
}
