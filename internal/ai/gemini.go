package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

// Gemini generateContent, verified against https://ai.google.dev/api/generate-content
// (GenerationConfig, ImageConfig, FinishReason, BlockReason) and
// https://ai.google.dev/gemini-api/docs/generate-content/image-generation.
// Google now labels generateContent "legacy" next to the Interactions API but
// documents it as supported, including for gemini-nano-banana-2.1.
// Assumptions: maxOutputTokens covers thinking tokens (as on Gemini 2.5 and
// later), and image models accept responseModalities ["TEXT", "IMAGE"], the
// combination used by most documented examples.
type gemini struct {
	t          *transport
	base       string
	model      string
	imageModel string
	embedModel string
	embedDims  int
}

type geminiPart struct {
	Text       string      `json:"text,omitempty"`
	Thought    bool        `json:"thought,omitempty"`
	InlineData *geminiBlob `json:"inlineData,omitempty"`
}

type geminiBlob struct {
	MIMEType string `json:"mimeType"`
	Data     string `json:"data"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiRequest struct {
	SystemInstruction *geminiContent  `json:"systemInstruction,omitempty"`
	Contents          []geminiContent `json:"contents"`
	GenerationConfig  geminiGenConfig `json:"generationConfig"`
}

type geminiGenConfig struct {
	MaxOutputTokens    int                `json:"maxOutputTokens,omitempty"`
	Temperature        *float64           `json:"temperature,omitempty"`
	ResponseMIMEType   string             `json:"responseMimeType,omitempty"`
	ResponseJSONSchema json.RawMessage    `json:"responseJsonSchema,omitempty"`
	ResponseModalities []string           `json:"responseModalities,omitempty"`
	ImageConfig        *geminiImageConfig `json:"imageConfig,omitempty"`
}

type geminiImageConfig struct {
	AspectRatio string `json:"aspectRatio,omitempty"`
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text            string      `json:"text"`
				Thought         bool        `json:"thought"`
				InlineData      *geminiBlob `json:"inlineData"`
				InlineDataSnake *geminiBlob `json:"inline_data"`
			} `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	PromptFeedback struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		ThoughtsTokenCount   int `json:"thoughtsTokenCount"`
	} `json:"usageMetadata"`
	ModelVersion string `json:"modelVersion"`
}

func (g *gemini) Name() string { return ProviderGemini }

func (g *gemini) call(ctx context.Context, model string, body geminiRequest, limit int64) (*geminiResponse, error) {
	hdr := http.Header{}
	hdr.Set("x-goog-api-key", g.t.key)
	var out geminiResponse
	endpoint := g.base + "/models/" + url.PathEscape(model) + ":generateContent"
	if err := g.t.post(ctx, endpoint, hdr, body, limit, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func geminiStop(finish, block string) string {
	switch {
	case block != "":
		return StopFiltered
	case finish == "STOP":
		return StopEnd
	case finish == "MAX_TOKENS":
		return StopLength
	case finish == "SAFETY", finish == "RECITATION", finish == "BLOCKLIST", finish == "PROHIBITED_CONTENT",
		finish == "SPII", finish == "IMAGE_SAFETY", finish == "IMAGE_PROHIBITED_CONTENT", finish == "IMAGE_RECITATION":
		return StopFiltered
	default:
		return StopOther
	}
}

func (g *gemini) Text(ctx context.Context, req TextRequest) (*TextResponse, error) {
	if err := validateText(req); err != nil {
		return nil, err
	}
	body := geminiRequest{GenerationConfig: geminiGenConfig{
		MaxOutputTokens: tokenLimit(req.MaxTokens, true),
		Temperature:     req.Temperature,
	}}
	if sys := systemWithJSON(req); sys != "" {
		body.SystemInstruction = &geminiContent{Parts: []geminiPart{{Text: sys}}}
	}
	for _, m := range req.Messages {
		role := "user"
		if m.Role == RoleAssistant {
			role = "model"
		}
		body.Contents = append(body.Contents, geminiContent{Role: role, Parts: []geminiPart{{Text: m.Content}}})
	}
	if req.JSON {
		body.GenerationConfig.ResponseMIMEType = "application/json"
		body.GenerationConfig.ResponseJSONSchema = req.Schema
	}
	ctx, cancel := context.WithTimeout(ctx, g.t.textTimeout)
	defer cancel()
	out, err := g.call(ctx, g.model, body, maxTextResponse)
	if err != nil {
		return nil, err
	}
	res := &TextResponse{
		Model:        out.ModelVersion,
		InputTokens:  out.UsageMetadata.PromptTokenCount,
		OutputTokens: out.UsageMetadata.CandidatesTokenCount + out.UsageMetadata.ThoughtsTokenCount,
	}
	if res.Model == "" {
		res.Model = g.model
	}
	if len(out.Candidates) == 0 {
		if out.PromptFeedback.BlockReason == "" {
			return nil, g.t.errorf("%w: no candidates", ErrBadResponse)
		}
		res.StopReason = StopFiltered
		return res, nil
	}
	c := out.Candidates[0]
	var text strings.Builder
	for _, p := range c.Content.Parts {
		if !p.Thought {
			text.WriteString(p.Text)
		}
	}
	res.Text = text.String()
	res.StopReason = geminiStop(c.FinishReason, out.PromptFeedback.BlockReason)
	return finish(g.t, req, res)
}

// geminiAspect maps to the documented ImageConfig.aspectRatio values
// (1:1, 2:3, 3:2, 3:4, 4:3, 4:5, 5:4, 9:16, 16:9, 21:9 and others).
var geminiAspect = map[string]string{
	Aspect16x9: "16:9", AspectOG: "16:9", Aspect1x1: "1:1", Aspect4x3: "4:3", Aspect3x2: "3:2",
}

// Image makes one generateContent call per image: candidateCount is not
// documented for image models.
func (g *gemini) Image(ctx context.Context, req ImageRequest) ([]Image, error) {
	if g.imageModel == "" {
		return nil, g.t.errorf("%w: no image model configured", ErrUnsupported)
	}
	if err := validateImage(&req); err != nil {
		return nil, err
	}
	body := geminiRequest{
		Contents: []geminiContent{{Role: "user", Parts: []geminiPart{{Text: req.Prompt}}}},
		GenerationConfig: geminiGenConfig{
			ResponseModalities: []string{"TEXT", "IMAGE"},
			ImageConfig:        &geminiImageConfig{AspectRatio: geminiAspect[req.Aspect]},
		},
	}
	var imgs []Image
	for range req.N {
		callCtx, cancel := context.WithTimeout(ctx, g.t.imageTimeout)
		out, err := g.call(callCtx, g.imageModel, body, maxImageResponse)
		cancel()
		if err != nil {
			return nil, err
		}
		img, err := g.firstImage(out)
		if err != nil {
			return nil, err
		}
		imgs = append(imgs, img)
	}
	return imgs, nil
}

func (g *gemini) firstImage(out *geminiResponse) (Image, error) {
	if len(out.Candidates) == 0 {
		if out.PromptFeedback.BlockReason != "" {
			return Image{}, g.t.errorf("%w: prompt blocked (%s)", ErrRefused, g.t.clean(out.PromptFeedback.BlockReason, 40))
		}
		return Image{}, g.t.errorf("%w: no candidates", ErrBadResponse)
	}
	c := out.Candidates[0]
	for _, p := range c.Content.Parts {
		blob := p.InlineData
		if blob == nil {
			blob = p.InlineDataSnake
		}
		if blob == nil || p.Thought {
			continue
		}
		data, err := decodeBase64(blob.Data)
		if err != nil {
			return Image{}, g.t.errorf("%w", err)
		}
		img, err := checkImage(data)
		if err != nil {
			return Image{}, g.t.errorf("%w", err)
		}
		return img, nil
	}
	if geminiStop(c.FinishReason, "") == StopFiltered {
		return Image{}, g.t.errorf("%w: %s", ErrRefused, g.t.clean(c.FinishReason, 40))
	}
	return Image{}, g.t.errorf("%w: no image in the answer (%s)", ErrBadResponse, g.t.clean(c.FinishReason, 40))
}
