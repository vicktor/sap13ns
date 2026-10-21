package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Qwen-Image answers with a URL that is downloaded at once; text goes
// through the OpenAI-compatible mode; Split pairs it with another text
// provider.
func TestAlibaba(t *testing.T) {
	img := pngBytes(t, 16, 9)
	var imgBody map[string]any
	var paths []string
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/api/v1/services/aigc/multimodal-generation/generation":
			if r.Header.Get("Authorization") != "Bearer "+testKey {
				w.WriteHeader(401)
				return
			}
			json.NewDecoder(r.Body).Decode(&imgBody)
			io.WriteString(w, `{"output":{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":[{"image":"`+srv.URL+`/result.png?Expires=1"}]}}]},"usage":{"image_count":1}}`)
		case "/result.png":
			w.Write(img)
		case "/compatible-mode/v1/chat/completions":
			io.WriteString(w, `{"choices":[{"message":{"content":"hola"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	p, err := New(Config{Provider: ProviderAlibaba, APIKey: testKey, BaseURL: srv.URL, AllowInsecureLocal: true}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != ProviderAlibaba {
		t.Errorf("name %q", p.Name())
	}
	imgs, err := p.Image(context.Background(), ImageRequest{Prompt: "a junk boat at dawn", Aspect: Aspect16x9})
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != 1 || imgs[0].Width != 16 || imgs[0].MIME != "image/png" {
		t.Errorf("images: %+v", imgs)
	}
	params, _ := imgBody["parameters"].(map[string]any)
	msgs, _ := imgBody["input"].(map[string]any)["messages"].([]any)
	if imgBody["model"] != "qwen-image-2.1-pro" || params["size"] != "1664*928" || params["watermark"] != false || len(msgs) != 1 ||
		!strings.Contains(mustJSON(msgs), "a junk boat at dawn") {
		t.Errorf("request: %v", imgBody)
	}
	res, err := p.Text(context.Background(), TextRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}, MaxTokens: 10})
	if err != nil || res.Text != "hola" {
		t.Fatalf("text: %v %+v", err, res)
	}

	// Split: another provider's text, Qwen's images.
	text := &fakeText{}
	sp := Split(text, p)
	if _, err := sp.Image(context.Background(), ImageRequest{Prompt: "x"}); err != nil {
		t.Fatal(err)
	}
	if r, _ := sp.Text(context.Background(), TextRequest{}); r.Text != "fake" || sp.Name() != "fake" {
		t.Error("split text")
	}
	if Split(text, nil) != Provider(text) {
		t.Error("split without images")
	}
}

type fakeText struct{}

func (fakeText) Name() string { return "fake" }
func (fakeText) Text(context.Context, TextRequest) (*TextResponse, error) {
	return &TextResponse{Text: "fake"}, nil
}
func (fakeText) Image(context.Context, ImageRequest) ([]Image, error) { return nil, ErrUnsupported }

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
