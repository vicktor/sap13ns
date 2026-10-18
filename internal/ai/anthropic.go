package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// Claude Messages API, verified against
// https://platform.claude.com/docs/en/api/messages,
// https://platform.claude.com/docs/en/build-with-claude/structured-outputs and
// https://platform.claude.com/docs/en/models/opus-5-5/migration-guide.
// Notable for this adapter:
//   - temperature/top_p/top_k other than the defaults are rejected by Claude
//     Opus 5.5, so they are never sent;
//   - a conversation must end with a user turn (prefill is rejected);
//   - thinking is always on for Opus 5.5: responses may start with "thinking"
//     blocks and max_tokens covers thinking plus text;
//   - native structured output is output_config.format {type: "json_schema"}.
const anthropicVersion = "2023-06-01"

type anthropic struct {
	t     *transport
	base  string
	model string
}

type anthropicRequest struct {
	Model        string           `json:"model"`
	MaxTokens    int              `json:"max_tokens"`
	System       string           `json:"system,omitempty"`
	Messages     []Message        `json:"messages"`
	OutputConfig *anthropicOutput `json:"output_config,omitempty"`
}

type anthropicOutput struct {
	Format *anthropicFormat `json:"format,omitempty"`
}

type anthropicFormat struct {
	Type   string          `json:"type"`
	Schema json.RawMessage `json:"schema"`
}

type anthropicResponse struct {
	Type    string `json:"type"`
	Model   string `json:"model"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Usage      struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

func (a *anthropic) Name() string { return ProviderAnthropic }

func (a *anthropic) Text(ctx context.Context, req TextRequest) (*TextResponse, error) {
	if err := validateText(req); err != nil {
		return nil, err
	}
	if req.Messages[len(req.Messages)-1].Role != RoleUser {
		return nil, a.t.errorf("%w: the conversation must end with a user message", ErrInvalidInput)
	}
	body := anthropicRequest{
		Model:     a.model,
		MaxTokens: tokenLimit(req.MaxTokens, true),
		System:    systemWithJSON(req),
		Messages:  req.Messages,
	}
	if req.JSON && len(req.Schema) > 0 {
		body.OutputConfig = &anthropicOutput{Format: &anthropicFormat{Type: "json_schema", Schema: req.Schema}}
	}
	ctx, cancel := context.WithTimeout(ctx, a.t.textTimeout)
	defer cancel()
	hdr := http.Header{}
	hdr.Set("x-api-key", a.t.key)
	hdr.Set("anthropic-version", anthropicVersion)
	var out anthropicResponse
	if err := a.t.post(ctx, a.base+"/v1/messages", hdr, body, maxTextResponse, &out); err != nil {
		return nil, err
	}
	if out.Type != "" && out.Type != "message" || out.StopReason == "" && len(out.Content) == 0 {
		return nil, a.t.errorf("%w: not a message", ErrBadResponse)
	}
	var text strings.Builder
	for _, c := range out.Content {
		if c.Type == "text" {
			text.WriteString(c.Text)
		}
	}
	res := &TextResponse{
		Text:         text.String(),
		Model:        out.Model,
		InputTokens:  out.Usage.InputTokens + out.Usage.CacheCreationInputTokens + out.Usage.CacheReadInputTokens,
		OutputTokens: out.Usage.OutputTokens,
	}
	switch out.StopReason {
	case "end_turn", "stop_sequence":
		res.StopReason = StopEnd
	case "max_tokens", "model_context_window_exceeded":
		res.StopReason = StopLength
	case "refusal":
		res.StopReason = StopRefusal
	default:
		res.StopReason = StopOther
	}
	return finish(a.t, req, res)
}

func (a *anthropic) Image(context.Context, ImageRequest) ([]Image, error) {
	return nil, a.t.errorf("%w: Claude does not generate images", ErrUnsupported)
}
