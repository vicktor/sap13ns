package ai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"unicode/utf8"
)

// Embeddings for the site assistant's semantic search. Adapters whose
// provider has an embeddings API implement Embedder: the OpenAI-compatible
// one (POST /embeddings, also served by LM Studio, Ollama and llama.cpp) and
// Gemini (batchEmbedContents). Anthropic has no embeddings API, so a site
// whose text provider is Claude picks a separate embeddings provider.

// Embedder turns texts into vectors.
type Embedder interface {
	Embed(ctx context.Context, req EmbedRequest) (*EmbedResponse, error)
}

type EmbedRequest struct {
	Inputs []string
	// Query marks search queries rather than documents: Gemini embeds them
	// for retrieval, and models trained with task prefixes (nomic-embed,
	// e5) get theirs.
	Query bool
}

type EmbedResponse struct {
	Vectors [][]float32 // one per input, in order
	Model   string
	Tokens  int // input tokens, when the provider reports them
}

const (
	maxEmbedDimensions = 4000 // HNSW limit of pgvector's halfvec
	maxEmbedInputs     = 2048 // per Embed call
	maxEmbedChars      = 32_000
	openAIEmbedBatch   = 64
	geminiEmbedBatch   = 100 // documented limit of batchEmbedContents
)

var embedCatalog = map[string][]Model{
	ProviderOpenAI: {
		{"text-embedding-3-small", "OpenAI text-embedding-3-small (1536)"},
		{"text-embedding-3-large", "OpenAI text-embedding-3-large (3072)"},
		{"nomic-embed-text-v1.5", "nomic-embed-text v1.5 (768, local)"},
	},
	ProviderGemini: {
		{"gemini-embedding-001", "Gemini embedding 001 (3072, or 768/1536)"},
	},
}

// EmbedModels lists known embedding models of a provider, the default first.
func EmbedModels(provider string) []Model { return embedCatalog[provider] }

// DefaultEmbedModel returns "" for providers without embeddings.
func DefaultEmbedModel(provider string) string {
	if ms := embedCatalog[provider]; len(ms) > 0 {
		return ms[0].ID
	}
	return ""
}

// NewEmbedder returns the embeddings adapter for cfg.Provider, or an error
// matching ErrUnsupported for providers without an embeddings API.
func NewEmbedder(cfg Config, client *http.Client) (Embedder, error) {
	p, err := New(cfg, client)
	if err != nil {
		return nil, err
	}
	e, ok := p.(Embedder)
	if !ok {
		return nil, fmt.Errorf("%s: %w: no embeddings API", p.Name(), ErrUnsupported)
	}
	return e, nil
}

// embedPrefix returns the task prefix that models trained with one expect
// (nomic-embed: "search_query: " / "search_document: "; e5: "query: " /
// "passage: "). Other models get none.
func embedPrefix(model string, query bool) string {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "nomic-embed"):
		if query {
			return "search_query: "
		}
		return "search_document: "
	case strings.Contains(m, "multilingual-e5"), strings.HasPrefix(m, "e5-"), strings.Contains(m, "/e5-"):
		if query {
			return "query: "
		}
		return "passage: "
	}
	return ""
}

func validateEmbed(req EmbedRequest) error {
	if len(req.Inputs) == 0 {
		return fmt.Errorf("%w: nothing to embed", ErrInvalidInput)
	}
	if len(req.Inputs) > maxEmbedInputs {
		return fmt.Errorf("%w: too many inputs", ErrInvalidInput)
	}
	for _, in := range req.Inputs {
		if strings.TrimSpace(in) == "" {
			return fmt.Errorf("%w: empty input", ErrInvalidInput)
		}
		if utf8.RuneCountInString(in) > maxEmbedChars {
			return fmt.Errorf("%w: input too long", ErrInvalidInput)
		}
	}
	return nil
}

// embedBatches splits inputs, adds the model's prefix and checks that every
// batch comes back with one vector per input, all of the same length (the
// configured one when set).
func embedBatches(t *transport, req EmbedRequest, model string, dims, size int,
	call func(batch []string) ([][]float32, int, error)) (*EmbedResponse, error) {
	if err := validateEmbed(req); err != nil {
		return nil, err
	}
	prefix := embedPrefix(model, req.Query)
	res := &EmbedResponse{Model: model, Vectors: make([][]float32, 0, len(req.Inputs))}
	for from := 0; from < len(req.Inputs); from += size {
		batch := make([]string, 0, size)
		for _, in := range req.Inputs[from:min(from+size, len(req.Inputs))] {
			batch = append(batch, prefix+in)
		}
		vecs, tokens, err := call(batch)
		if err != nil {
			return nil, err
		}
		if len(vecs) != len(batch) {
			return nil, t.errorf("%w: %d embeddings for %d inputs", ErrBadResponse, len(vecs), len(batch))
		}
		res.Tokens += tokens
		res.Vectors = append(res.Vectors, vecs...)
	}
	want := dims
	if want == 0 {
		want = len(res.Vectors[0])
	}
	for _, v := range res.Vectors {
		if len(v) == 0 || len(v) != want {
			if dims > 0 && len(v) > 0 {
				return nil, t.errorf("%w: the model returned %d dimensions, %d were requested", ErrBadResponse, len(v), dims)
			}
			return nil, t.errorf("%w: embeddings of different lengths", ErrBadResponse)
		}
	}
	if want > maxEmbedDimensions {
		return nil, t.errorf("%w: %d dimensions; at most %d can be indexed (set a lower number)", ErrUnsupported, want, maxEmbedDimensions)
	}
	return res, nil
}

// ----------------------------------------------------- OpenAI-compatible --

type openAIEmbedRequest struct {
	Model          string   `json:"model"`
	Input          []string `json:"input"`
	Dimensions     int      `json:"dimensions,omitempty"`
	EncodingFormat string   `json:"encoding_format"`
}

type openAIEmbedResponse struct {
	Model string `json:"model"`
	Data  []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Usage struct {
		PromptTokens int `json:"prompt_tokens"`
	} `json:"usage"`
}

// Embed calls POST /embeddings. dimensions is sent only when configured
// (text-embedding-3 models honour it; other servers may ignore it, which
// embedBatches then reports).
func (o *openAI) Embed(ctx context.Context, req EmbedRequest) (*EmbedResponse, error) {
	if o.embedModel == "" {
		return nil, o.t.errorf("%w: no embedding model configured", ErrUnsupported)
	}
	var served string
	res, err := embedBatches(o.t, req, o.embedModel, o.embedDims, openAIEmbedBatch, func(batch []string) ([][]float32, int, error) {
		cctx, cancel := context.WithTimeout(ctx, o.t.textTimeout)
		defer cancel()
		var out openAIEmbedResponse
		body := openAIEmbedRequest{Model: o.embedModel, Input: batch, Dimensions: o.embedDims, EncodingFormat: "float"}
		if err := o.t.post(cctx, o.base+"/embeddings", o.header(), body, maxTextResponse*4, &out); err != nil {
			return nil, 0, err
		}
		sort.SliceStable(out.Data, func(i, j int) bool { return out.Data[i].Index < out.Data[j].Index })
		vecs := make([][]float32, len(out.Data))
		for i, d := range out.Data {
			vecs[i] = d.Embedding
		}
		served = out.Model
		return vecs, out.Usage.PromptTokens, nil
	})
	if err != nil {
		return nil, err
	}
	if served != "" {
		res.Model = o.t.clean(served, 200)
	}
	return res, nil
}

// ---------------------------------------------------------------- Gemini --

type geminiEmbedConfig struct {
	TaskType             string `json:"taskType,omitempty"`
	OutputDimensionality int    `json:"outputDimensionality,omitempty"`
}

// geminiEmbedRequest carries the options in embedContentConfig (the current
// API) or, for servers that predate it, in the deprecated top-level fields.
type geminiEmbedRequest struct {
	Model   string             `json:"model"`
	Content geminiContent      `json:"content"`
	Config  *geminiEmbedConfig `json:"embedContentConfig,omitempty"`
	geminiEmbedLegacy
}

type geminiEmbedLegacy struct {
	TaskType             string `json:"taskType,omitempty"`
	OutputDimensionality int    `json:"outputDimensionality,omitempty"`
}

type geminiEmbedResponse struct {
	Embeddings []struct {
		Values []float32 `json:"values"`
	} `json:"embeddings"`
	UsageMetadata struct {
		PromptTokenCount int `json:"promptTokenCount"`
	} `json:"usageMetadata"`
}

// Embed calls batchEmbedContents (verified against
// https://ai.google.dev/api/embeddings) with taskType RETRIEVAL_QUERY or
// RETRIEVAL_DOCUMENT and outputDimensionality when configured.
func (g *gemini) Embed(ctx context.Context, req EmbedRequest) (*EmbedResponse, error) {
	if g.embedModel == "" {
		return nil, g.t.errorf("%w: no embedding model configured", ErrUnsupported)
	}
	task := "RETRIEVAL_DOCUMENT"
	if req.Query {
		task = "RETRIEVAL_QUERY"
	}
	legacy := false
	endpoint := g.base + "/models/" + url.PathEscape(g.embedModel) + ":batchEmbedContents"
	hdr := http.Header{}
	hdr.Set("x-goog-api-key", g.t.key)
	return embedBatches(g.t, req, g.embedModel, g.embedDims, geminiEmbedBatch, func(batch []string) ([][]float32, int, error) {
		build := func() map[string]any {
			reqs := make([]geminiEmbedRequest, len(batch))
			for i, in := range batch {
				r := geminiEmbedRequest{Model: "models/" + g.embedModel, Content: geminiContent{Parts: []geminiPart{{Text: in}}}}
				if legacy {
					r.geminiEmbedLegacy = geminiEmbedLegacy{TaskType: task, OutputDimensionality: g.embedDims}
				} else {
					r.Config = &geminiEmbedConfig{TaskType: task, OutputDimensionality: g.embedDims}
				}
				reqs[i] = r
			}
			return map[string]any{"requests": reqs}
		}
		cctx, cancel := context.WithTimeout(ctx, g.t.textTimeout)
		defer cancel()
		var out geminiEmbedResponse
		err := g.t.post(cctx, endpoint, hdr, build(), maxTextResponse*4, &out)
		if !legacy && rejectsEmbedConfig(err) {
			legacy = true
			err = g.t.post(cctx, endpoint, hdr, build(), maxTextResponse*4, &out)
		}
		if err != nil {
			return nil, 0, err
		}
		vecs := make([][]float32, len(out.Embeddings))
		for i, e := range out.Embeddings {
			vecs[i] = e.Values
		}
		return vecs, out.UsageMetadata.PromptTokenCount, nil
	})
}

// rejectsEmbedConfig reports a 400 complaining about embedContentConfig.
func rejectsEmbedConfig(err error) bool {
	var e *APIError
	return errors.As(err, &e) && e.Status == http.StatusBadRequest && strings.Contains(e.Message, "embedContentConfig")
}
