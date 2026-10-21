package web

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sap13ns/internal/ai"
	"sap13ns/internal/store"
)

type fakeImageAI struct{ img []byte }

func (f *fakeImageAI) Name() string { return "fake" }
func (f *fakeImageAI) Text(context.Context, ai.TextRequest) (*ai.TextResponse, error) {
	return &ai.TextResponse{Text: "A lighthouse at dawn", StopReason: ai.StopEnd}, nil
}
func (f *fakeImageAI) Image(context.Context, ai.ImageRequest) ([]ai.Image, error) {
	return []ai.Image{{Data: f.img, MIME: "image/png", Width: 64, Height: 36}}, nil
}

// An image job finishes, with the image in the library and the calls in
// the usage statistics, under who made them.
func TestAIImageJob(t *testing.T) {
	e := newPluginEnv(t)
	var buf bytes.Buffer
	png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 64, 36)))
	e.s.ai.mu.Lock()
	e.s.ai.settings = &store.AISettings{Enabled: true, Provider: "anthropic", MinRole: "author",
		ImageProvider: "alibaba", ImageModel: "qwen-image-2.1-pro"}
	e.s.ai.provider = &fakeImageAI{img: buf.Bytes()}
	e.s.ai.mu.Unlock()
	body, _ := json.Marshal(map[string]any{"title": "Faro", "content": "Un faro.", "lang": "es", "kind": "header"})
	req := httptest.NewRequest("POST", "http://example.test/admin/api/ai/image", strings.NewReader(string(body)))
	req.RemoteAddr = "192.0.2.31:4000"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-CSRF-Token", e.csrf)
	req.AddCookie(e.cookie)
	rec := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	var j struct{ Job string }
	json.Unmarshal(rec.Body.Bytes(), &j)
	for range 250 {
		e.s.ai.mu.Lock()
		job := *e.s.ai.jobs[j.Job]
		e.s.ai.mu.Unlock()
		if job.Done {
			if job.Error != "" {
				t.Fatalf("job: %s", job.Error)
			}
			st, err := e.s.store.SiteStats(context.Background(), time.Now().AddDate(0, 0, -1), time.Now().AddDate(0, 0, 1))
			if err != nil {
				t.Fatal(err)
			}
			var img, text bool
			for _, c := range st.Calls {
				img = img || c.Feature == "image" && c.Kind == store.AIKindImage && c.Provider == "alibaba" &&
					c.Model == "qwen-image-2.1-pro" && c.Source == store.AICallOwn && c.Requests == 1 && c.Images == 1
				text = text || c.Feature == "image" && c.Kind == store.AIKindText && c.Provider == "anthropic" && c.Requests == 1
			}
			if !img || !text || st.AIImages != 1 {
				t.Errorf("statistics: %+v, %d AI images", st.Calls, st.AIImages)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the image job never finished")
}
