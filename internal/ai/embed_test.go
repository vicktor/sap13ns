package ai

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func embedder(t *testing.T, p Provider) Embedder {
	t.Helper()
	e, ok := p.(Embedder)
	if !ok {
		t.Fatalf("%s does not implement Embedder", p.Name())
	}
	return e
}

func TestEmbedPrefix(t *testing.T) {
	cases := []struct {
		model string
		query bool
		want  string
	}{
		{"text-embedding-nomic-embed-text-v1.5", true, "search_query: "},
		{"nomic-embed-text:latest", false, "search_document: "},
		{"intfloat/multilingual-e5-large", true, "query: "},
		{"e5-small-v2", false, "passage: "},
		{"text-embedding-3-small", true, ""},
		{"gemini-embedding-001", false, ""},
	}
	for _, c := range cases {
		if got := embedPrefix(c.model, c.query); got != c.want {
			t.Errorf("embedPrefix(%q, %v) = %q, want %q", c.model, c.query, got, c.want)
		}
	}
}

func TestOpenAIEmbedBatchesAndPrefixes(t *testing.T) {
	var calls atomic.Int32
	var first captured
	p := newTestProvider(t, ProviderOpenAI, func(w http.ResponseWriter, r *http.Request) {
		c := capture(r)
		if calls.Add(1) == 1 {
			first = c
		}
		inputs, _ := c.body["input"].([]any)
		// Answer out of order: the adapter must sort by index.
		var data []string
		for i := len(inputs) - 1; i >= 0; i-- {
			data = append(data, fmt.Sprintf(`{"index":%d,"embedding":[%d,0.5,0.25]}`, i, i))
		}
		fmt.Fprintf(w, `{"model":"nomic-served","data":[%s],"usage":{"prompt_tokens":%d}}`, strings.Join(data, ","), len(inputs)*3)
	}, func(c *Config) { c.EmbedModel = "text-embedding-nomic-embed-text-v1.5" })
	inputs := make([]string, openAIEmbedBatch+6)
	for i := range inputs {
		inputs[i] = fmt.Sprintf("doc %d", i)
	}
	res, err := embedder(t, p).Embed(ctx, EmbedRequest{Inputs: inputs})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Errorf("%d requests, want 2 batches", calls.Load())
	}
	if first.path != "/v1/embeddings" || first.header.Get("Authorization") != "Bearer "+testKey {
		t.Errorf("request %s, auth %q", first.path, first.header.Get("Authorization"))
	}
	if got := first.body["input"].([]any)[0]; got != "search_document: doc 0" {
		t.Errorf("first input = %q, want the nomic document prefix", got)
	}
	if first.body["model"] != "text-embedding-nomic-embed-text-v1.5" || first.body["encoding_format"] != "float" {
		t.Errorf("body: %v", first.body)
	}
	if _, ok := first.body["dimensions"]; ok {
		t.Error("dimensions must not be sent when not configured")
	}
	if len(res.Vectors) != len(inputs) || res.Vectors[1][0] != 1 || res.Vectors[openAIEmbedBatch+1][0] != 1 {
		t.Errorf("vectors not in input order: %v ... %v", res.Vectors[:2], res.Vectors[openAIEmbedBatch:openAIEmbedBatch+2])
	}
	if res.Tokens != len(inputs)*3 || res.Model != "nomic-served" {
		t.Errorf("tokens %d, model %q", res.Tokens, res.Model)
	}

	// Queries get the query prefix.
	res, err = embedder(t, p).Embed(ctx, EmbedRequest{Inputs: []string{"¿qué es?"}, Query: true})
	if err != nil || len(res.Vectors) != 1 {
		t.Fatalf("query: %v %v", res, err)
	}
}

func TestOpenAIEmbedDimensions(t *testing.T) {
	var got captured
	p := newTestProvider(t, ProviderOpenAI, func(w http.ResponseWriter, r *http.Request) {
		got = capture(r)
		io.WriteString(w, `{"data":[{"index":0,"embedding":[1,2,3,4]}]}`)
	}, func(c *Config) { c.EmbedDimensions = 3 })
	_, err := embedder(t, p).Embed(ctx, EmbedRequest{Inputs: []string{"x"}})
	if !errors.Is(err, ErrBadResponse) || !strings.Contains(err.Error(), "4 dimensions, 3 were requested") {
		t.Errorf("err = %v, want a dimension mismatch", err)
	}
	if got.body["dimensions"] != float64(3) || got.body["model"] != "text-embedding-3-small" {
		t.Errorf("body: %v", got.body)
	}
}

func TestEmbedErrors(t *testing.T) {
	p := newTestProvider(t, ProviderOpenAI, reply(401, `{"error":{"message":"bad key `+testKey+`"}}`))
	e := embedder(t, p)
	_, err := e.Embed(ctx, EmbedRequest{Inputs: []string{"x"}})
	if !errors.Is(err, ErrAuth) || strings.Contains(err.Error(), testKey) {
		t.Errorf("err = %v, want ErrAuth with the key redacted", err)
	}
	for _, req := range []EmbedRequest{{}, {Inputs: []string{"  "}}, {Inputs: []string{strings.Repeat("x", maxEmbedChars+1)}}} {
		if _, err := e.Embed(ctx, req); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("Embed(%d inputs) err = %v, want ErrInvalidInput", len(req.Inputs), err)
		}
	}
	short := newTestProvider(t, ProviderOpenAI, reply(200, `{"data":[{"index":0,"embedding":[1]}]}`))
	if _, err := embedder(t, short).Embed(ctx, EmbedRequest{Inputs: []string{"a", "b"}}); !errors.Is(err, ErrBadResponse) {
		t.Errorf("missing embeddings: err = %v", err)
	}
	ragged := newTestProvider(t, ProviderOpenAI, reply(200, `{"data":[{"index":0,"embedding":[1,2]},{"index":1,"embedding":[1]}]}`))
	if _, err := embedder(t, ragged).Embed(ctx, EmbedRequest{Inputs: []string{"a", "b"}}); !errors.Is(err, ErrBadResponse) {
		t.Errorf("ragged embeddings: err = %v", err)
	}
}

func TestGeminiEmbed(t *testing.T) {
	var got captured
	p := newTestProvider(t, ProviderGemini, func(w http.ResponseWriter, r *http.Request) {
		got = capture(r)
		io.WriteString(w, `{"embeddings":[{"values":[0.1,0.2]},{"values":[0.3,0.4]}],"usageMetadata":{"promptTokenCount":7}}`)
	}, func(c *Config) { c.EmbedDimensions = 2 })
	res, err := embedder(t, p).Embed(ctx, EmbedRequest{Inputs: []string{"uno", "dos"}, Query: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.path != "/v1beta/models/gemini-embedding-001:batchEmbedContents" || got.header.Get("x-goog-api-key") != testKey {
		t.Errorf("request %s, key %q", got.path, got.header.Get("x-goog-api-key"))
	}
	reqs, _ := got.body["requests"].([]any)
	if len(reqs) != 2 {
		t.Fatalf("requests: %v", got.body)
	}
	r0 := reqs[0].(map[string]any)
	if r0["model"] != "models/gemini-embedding-001" || get(r0, "embedContentConfig", "taskType") != "RETRIEVAL_QUERY" ||
		get(r0, "embedContentConfig", "outputDimensionality") != float64(2) {
		t.Errorf("request: %v", r0)
	}
	if _, legacy := r0["taskType"]; legacy {
		t.Error("deprecated taskType sent next to embedContentConfig")
	}
	if len(res.Vectors) != 2 || res.Vectors[1][1] != float32(0.4) || res.Tokens != 7 {
		t.Errorf("response: %+v", res)
	}
}

// Servers that predate embedContentConfig get the deprecated fields.
func TestGeminiEmbedLegacyFields(t *testing.T) {
	var bodies []map[string]any
	p := newTestProvider(t, ProviderGemini, func(w http.ResponseWriter, r *http.Request) {
		c := capture(r)
		bodies = append(bodies, c.body)
		if len(bodies) == 1 {
			reply(400, `{"error":{"code":400,"message":"Invalid JSON payload received. Unknown name \"embedContentConfig\" at 'requests[0]': Cannot find field.","status":"INVALID_ARGUMENT"}}`)(w, r)
			return
		}
		io.WriteString(w, `{"embeddings":[{"values":[1,2]}]}`)
	})
	if _, err := embedder(t, p).Embed(ctx, EmbedRequest{Inputs: []string{"x"}}); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 {
		t.Fatalf("%d requests, want a retry", len(bodies))
	}
	r := bodies[1]["requests"].([]any)[0].(map[string]any)
	if r["taskType"] != "RETRIEVAL_DOCUMENT" || r["embedContentConfig"] != nil {
		t.Errorf("legacy request: %v", r)
	}
}

func TestNewEmbedderAnthropic(t *testing.T) {
	_, err := NewEmbedder(Config{Provider: ProviderAnthropic, APIKey: testKey}, nil)
	if !errors.Is(err, ErrUnsupported) {
		t.Errorf("err = %v, want ErrUnsupported", err)
	}
	if _, err := NewEmbedder(Config{Provider: ProviderOpenAI, BaseURL: "http://127.0.0.1:1234/v1", AllowInsecureLocal: true, EmbedDimensions: 5000}, nil); err == nil {
		t.Error("5000 dimensions accepted")
	}
	e, err := NewEmbedder(Config{Provider: ProviderOpenAI, BaseURL: "http://127.0.0.1:1234/v1", AllowInsecureLocal: true}, nil)
	if err != nil || e == nil {
		t.Errorf("local OpenAI-compatible embedder: %v", err)
	}
	b, _ := json.Marshal(EmbedModels(ProviderGemini))
	if !strings.Contains(string(b), "gemini-embedding-001") || DefaultEmbedModel(ProviderAnthropic) != "" {
		t.Errorf("catalog: %s", b)
	}
}
