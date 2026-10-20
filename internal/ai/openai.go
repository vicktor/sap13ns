package ai

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// OpenAI-compatible Chat Completions and Images API. The official reference
// (developers.openai.com) blocks automated fetches; checked against the
// guides at https://developers.openai.com/api/docs/guides/image-generation,
// https://developers.openai.com/api/docs/guides/reasoning and
// https://developers.openai.com/api/docs/models. Assumptions, marked where
// they matter:
//   - api.openai.com gets max_completion_tokens (max_tokens is deprecated and
//     rejected by reasoning models); other servers (OpenRouter, Ollama,
//     llama.cpp, LM Studio) get the widely implemented max_tokens, with the
//     same reasoning headroom;
//   - JSON mode sends response_format {type: "json_schema"} when the task has
//     a schema (OpenAI, LM Studio, llama.cpp, Ollama and vLLM enforce it),
//     else {type: "json_object"}. Servers that reject the requested type
//     (LM Studio refuses json_object) are asked once more without
//     response_format: the system prompt already demands JSON;
//   - GPT Image models always answer with b64_json and reject
//     response_format; DALL·E models and some compatible servers may answer
//     with a URL, which is downloaded over https.
type openAI struct {
	t          *transport
	base       string
	model      string
	imageModel string
	official   bool
	noThinking bool
	embedModel string
	embedDims  int
}

type openAIChatRequest struct {
	Model               string          `json:"model"`
	Messages            []Message       `json:"messages"` // "system" role included
	MaxTokens           int             `json:"max_tokens,omitempty"`
	MaxCompletionTokens int             `json:"max_completion_tokens,omitempty"`
	Temperature         *float64        `json:"temperature,omitempty"`
	ResponseFormat      *openAIRespType `json:"response_format,omitempty"`
	ChatTemplateKwargs  map[string]any  `json:"chat_template_kwargs,omitempty"`
}

type openAIRespType struct {
	Type       string            `json:"type"`
	JSONSchema *openAIJSONSchema `json:"json_schema,omitempty"`
}

type openAIJSONSchema struct {
	Name   string          `json:"name"`
	Schema json.RawMessage `json:"schema"`
	Strict bool            `json:"strict"`
}

type openAIChatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content json.RawMessage `json:"content"` // string, null or an array of parts
			Refusal string          `json:"refusal"`
			// Thinking of open models (LM Studio, llama.cpp, vLLM; OpenRouter
			// and Ollama call it "reasoning").
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func (o *openAI) Name() string { return ProviderOpenAI }

func (o *openAI) header() http.Header {
	hdr := http.Header{}
	if o.t.key != "" {
		hdr.Set("Authorization", "Bearer "+o.t.key)
	}
	return hdr
}

func (o *openAI) Text(ctx context.Context, req TextRequest) (*TextResponse, error) {
	if err := validateText(req); err != nil {
		return nil, err
	}
	msgs := make([]Message, 0, len(req.Messages)+1)
	if sys := systemWithJSON(req); sys != "" {
		msgs = append(msgs, Message{Role: "system", Content: sys})
	}
	msgs = append(msgs, req.Messages...)
	body := openAIChatRequest{Model: o.model, Messages: msgs, Temperature: req.Temperature}
	// Local and hosted open models (Qwen3, GLM, DeepSeek…) think before
	// answering too, so every server gets the reasoning headroom.
	if o.official {
		body.MaxCompletionTokens = tokenLimit(req.MaxTokens, true)
	} else {
		body.MaxTokens = tokenLimit(req.MaxTokens, true)
		if o.noThinking {
			body.ChatTemplateKwargs = map[string]any{"enable_thinking": false}
		}
	}
	switch {
	case req.JSON && len(req.Schema) > 0:
		body.ResponseFormat = &openAIRespType{Type: "json_schema",
			JSONSchema: &openAIJSONSchema{Name: "result", Schema: req.Schema, Strict: true}}
	case req.JSON:
		body.ResponseFormat = &openAIRespType{Type: "json_object"}
	}
	ctx, cancel := context.WithTimeout(ctx, o.t.textTimeout)
	defer cancel()
	var out openAIChatResponse
	err := retryModelAccess(ctx, func() error {
		return o.t.post(ctx, o.base+"/chat/completions", o.header(), body, maxTextResponse, &out)
	})
	if body.ResponseFormat != nil && rejectsResponseFormat(err) {
		body.ResponseFormat = nil
		err = o.t.post(ctx, o.base+"/chat/completions", o.header(), body, maxTextResponse, &out)
	}
	if err != nil {
		return nil, err
	}
	if len(out.Choices) == 0 {
		return nil, o.t.errorf("%w: no choices", ErrBadResponse)
	}
	c := out.Choices[0]
	text, ok := openAIContent(c.Message.Content)
	if !ok {
		return nil, o.t.errorf("%w: unexpected content", ErrBadResponse)
	}
	// LM Studio returns grammar-constrained output of reasoning models in
	// reasoning_content and leaves content empty.
	if req.JSON && strings.TrimSpace(text) == "" && c.FinishReason != "length" {
		text = cmp.Or(c.Message.ReasoningContent, c.Message.Reasoning)
	}
	res := &TextResponse{
		Text:         text,
		Model:        out.Model,
		InputTokens:  out.Usage.PromptTokens,
		OutputTokens: out.Usage.CompletionTokens,
	}
	switch {
	case c.Message.Refusal != "":
		res.StopReason = StopRefusal
	case c.FinishReason == "stop":
		res.StopReason = StopEnd
	case c.FinishReason == "length":
		res.StopReason = StopLength
	case c.FinishReason == "content_filter":
		res.StopReason = StopFiltered
	default:
		res.StopReason = StopOther
	}
	return finish(o.t, req, res)
}

// modelAccessRetries are the pauses before asking again when OpenAI says
// the project has no access to a model it does have access to: the same
// key and model get answers and, now and then, this 403 (the access is not
// known to every one of its servers yet).
var modelAccessRetries = []time.Duration{500 * time.Millisecond, 1500 * time.Millisecond}

// retryModelAccess calls do again, after a pause, while it fails with a
// model access error.
func retryModelAccess(ctx context.Context, do func() error) error {
	err := do()
	for _, wait := range modelAccessRetries {
		if !isModelAccess(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(wait):
		}
		err = do()
	}
	return err
}

// isModelAccess reports OpenAI's "Project … does not have access to model
// …" (403), or its "does not exist or you do not have access to it" (404).
func isModelAccess(err error) bool {
	var e *APIError
	if !errors.As(err, &e) || (e.Status != http.StatusForbidden && e.Status != http.StatusNotFound) {
		return false
	}
	m := strings.ToLower(e.Message)
	return strings.Contains(m, "have access to model") || strings.Contains(m, "do not have access to it")
}

// rejectsResponseFormat reports a 400 or 422 that blames response_format.
func rejectsResponseFormat(err error) bool {
	var e *APIError
	if !errors.As(err, &e) || (e.Status != http.StatusBadRequest && e.Status != http.StatusUnprocessableEntity) {
		return false
	}
	m := strings.ToLower(e.Message + " " + e.Type)
	return strings.Contains(m, "response_format") || strings.Contains(m, "json_schema") || strings.Contains(m, "json_object")
}

func openAIContent(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", true
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, true
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return "", false
	}
	var b strings.Builder
	for _, p := range parts {
		if p.Type == "text" || p.Type == "output_text" {
			b.WriteString(p.Text)
		}
	}
	return b.String(), true
}

type openAIImageRequest struct {
	Model          string `json:"model"`
	Prompt         string `json:"prompt"`
	N              int    `json:"n,omitempty"`
	Size           string `json:"size,omitempty"`
	ResponseFormat string `json:"response_format,omitempty"`
}

type openAIImageResponse struct {
	Data []struct {
		B64JSON       string `json:"b64_json"`
		URL           string `json:"url"`
		RevisedPrompt string `json:"revised_prompt"`
	} `json:"data"`
	Error json.RawMessage `json:"error"` // LM Studio answers unknown endpoints with 200 and an error
}

// openAISize maps an aspect ratio to a size the model accepts. GPT Image 2.5
// takes custom WIDTHxHEIGHT sizes (multiples of 16, ratio between 1:3 and
// 3:1, 655,360 to 8,294,400 pixels); earlier models only fixed sizes.
func openAISize(model, aspect string) string {
	switch {
	case strings.HasPrefix(model, "gpt-image-2.5"):
		return map[string]string{
			Aspect16x9: "1536x864", AspectOG: "1536x800", Aspect1x1: "1024x1024",
			Aspect4x3: "1536x1152", Aspect3x2: "1536x1024",
		}[aspect]
	case strings.HasPrefix(model, "dall-e-3"):
		return closestSize(aspect, []string{"1024x1024", "1792x1024", "1024x1792"})
	case strings.HasPrefix(model, "dall-e-2"):
		return "1024x1024"
	default: // gpt-image-1, 1.5, 2 and unknown compatible servers
		return closestSize(aspect, []string{"1024x1024", "1536x1024", "1024x1536"})
	}
}

func (o *openAI) Image(ctx context.Context, req ImageRequest) ([]Image, error) {
	if o.imageModel == "" {
		return nil, o.t.errorf("%w: no image model configured", ErrUnsupported)
	}
	if err := validateImage(&req); err != nil {
		return nil, err
	}
	body := openAIImageRequest{Model: o.imageModel, Prompt: req.Prompt, N: req.N, Size: openAISize(o.imageModel, req.Aspect)}
	if strings.HasPrefix(o.imageModel, "dall-e") {
		body.ResponseFormat = "b64_json"
		if strings.HasPrefix(o.imageModel, "dall-e-3") {
			body.N = 1 // DALL·E 3 generates one image per request
		}
	}
	ctx, cancel := context.WithTimeout(ctx, o.t.imageTimeout)
	defer cancel()
	var out openAIImageResponse
	if err := o.t.post(ctx, o.base+"/images/generations", o.header(), body, maxImageResponse, &out); err != nil {
		// Servers without an images endpoint (Ollama, llama.cpp, OpenRouter).
		if ae := (*APIError)(nil); errors.As(err, &ae) && (ae.Status == 404 || ae.Status == 405 || ae.Status == 501) {
			return nil, fmt.Errorf("%w: %w", ErrUnsupported, err)
		}
		return nil, err
	}
	if len(out.Data) == 0 {
		if len(out.Error) > 0 && string(out.Error) != "null" {
			return nil, o.t.errorf("%w: %s", ErrUnsupported, truncateRunes(string(out.Error), 200))
		}
		return nil, o.t.errorf("%w: no images", ErrBadResponse)
	}
	var imgs []Image
	for _, d := range out.Data[:min(len(out.Data), req.N)] {
		var data []byte
		var err error
		switch {
		case d.B64JSON != "":
			data, err = decodeBase64(d.B64JSON)
		case strings.HasPrefix(d.URL, "data:"):
			data, err = decodeBase64(d.URL)
		case d.URL != "":
			if data, err = o.t.fetch(ctx, d.URL); err != nil {
				return nil, err
			}
		default:
			err = fmt.Errorf("%w: image without data", ErrBadResponse)
		}
		if err != nil {
			return nil, o.t.errorf("%w", err)
		}
		img, err := checkImage(data)
		if err != nil {
			return nil, o.t.errorf("%w", err)
		}
		img.RevisedPrompt = o.t.clean(d.RevisedPrompt, 4000)
		imgs = append(imgs, img)
	}
	return imgs, nil
}
