package ai

import (
	"context"
	"strings"
	"testing"
)

// replies answers with each text in turn; the last repeats.
func replies(texts ...string) func(TextRequest) (*TextResponse, error) {
	n := 0
	return func(TextRequest) (*TextResponse, error) {
		t := texts[min(n, len(texts)-1)]
		n++
		return &TextResponse{Text: t, StopReason: StopEnd}, nil
	}
}

// A checked problem earns one revision, which sees the first answer and
// the problems; the better answer is kept.
func TestRevision(t *testing.T) {
	ctx := context.Background()
	f := &fakeProvider{reply: replies("En el mundo actual, afinar una guitarra es fácil.", "Afinar una guitarra lleva cinco minutos con un diapasón.")}
	s, err := Summarize(ctx, f, testArticle, "es")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.reqs) != 2 || s != "Afinar una guitarra lleva cinco minutos con un diapasón." {
		t.Fatalf("%d requests: %q", len(f.reqs), s)
	}
	rev := f.reqs[1].Messages
	if len(rev) != 3 || rev[1].Role != RoleAssistant || !strings.Contains(rev[2].Content, "en el mundo actual") && !strings.Contains(rev[2].Content, "En el mundo actual") {
		t.Fatalf("revision request: %+v", rev)
	}

	// A clean answer is not revised.
	f = &fakeProvider{reply: answer("Afinar una guitarra lleva cinco minutos.")}
	if _, err := Summarize(ctx, f, testArticle, "es"); err != nil || len(f.reqs) != 1 {
		t.Fatalf("clean answer: %d requests, %v", len(f.reqs), err)
	}

	// A worse revision, or a failed one, keeps the first answer.
	first := "En el mundo actual, afinar es fácil."
	f = &fakeProvider{reply: replies(first, "En el mundo actual, cabe destacar que afinar, en resumen, es fácil.")}
	if s, _ := Summarize(ctx, f, testArticle, "es"); s != first {
		t.Errorf("worse revision kept: %q", s)
	}
	calls := 0
	f = &fakeProvider{reply: func(TextRequest) (*TextResponse, error) {
		if calls++; calls > 1 {
			return nil, ErrRateLimited
		}
		return &TextResponse{Text: first, StopReason: StopEnd}, nil
	}}
	if s, err := Summarize(ctx, f, testArticle, "es"); err != nil || s != first {
		t.Errorf("failed revision: %q %v", s, err)
	}
}

func TestSEORevision(t *testing.T) {
	long := strings.Repeat("guitarra ", 12)
	f := &fakeProvider{reply: replies(`{"title":"`+long+`","description":"Pasos.","slug":"a","keywords":[],"og_title":"x"}`,
		`{"title":"Cómo afinar una guitarra","description":"Pasos para afinar de oído.","slug":"afinar","keywords":["afinar"],"og_title":"Afinar"}`)}
	r, err := SEO(context.Background(), f, testArticle, "es")
	if err != nil || len(f.reqs) != 2 || r.Title != "Cómo afinar una guitarra" {
		t.Fatalf("%d requests: %+v %v", len(f.reqs), r, err)
	}
	if !strings.Contains(f.reqs[1].Messages[2].Content, `"title" has 107 characters; the limit is 60`) {
		t.Errorf("feedback: %s", f.reqs[1].Messages[2].Content)
	}
}

func TestWriteRevision(t *testing.T) {
	ctx := context.Background()
	// The wrapper is asked away; when it stays, it is removed.
	body := "## Afinar\n\nUsa un diapasón de 440 Hz."
	f := &fakeProvider{reply: answer("¡Claro! Aquí tienes el borrador:\n" + body + "\n\n¿Quieres que lo amplíe?")}
	out, err := Write(ctx, f, WriteRequest{Mode: ModeDraft, Brief: "afinar", Lang: "es"})
	if err != nil || len(f.reqs) != 2 || out != body {
		t.Fatalf("%d requests: %q %v", len(f.reqs), out, err)
	}
	// Shorten must shorten.
	text := strings.Repeat("La quinta cuerda suena en La y se afina primero. ", 10)
	f = &fakeProvider{reply: replies(text, "La quinta cuerda se afina primero.")}
	if out, _ := Write(ctx, f, WriteRequest{Mode: ModeShorten, Text: text, Lang: "es"}); out != "La quinta cuerda se afina primero." {
		t.Errorf("shorten: %q", out)
	}
	if !strings.Contains(f.reqs[1].Messages[2].Content, "shorten it to about half") {
		t.Errorf("shorten feedback: %s", f.reqs[1].Messages[2].Content)
	}
}

func TestTranslateRevision(t *testing.T) {
	src := Article{Content: "## Título\n\nTexto con [enlace](/a) y `código`.", Lang: "es"}
	f := &fakeProvider{reply: func(req TextRequest) (*TextResponse, error) {
		if len(req.Messages) == 1 {
			return &TextResponse{Text: "Title\n\nText with link and code.", StopReason: StopEnd}, nil
		}
		return &TextResponse{Text: "## Title\n\nText with [link](/a) and `code`.", StopReason: StopEnd}, nil
	}}
	out, err := Translate(context.Background(), f, src, "es", "en")
	if err != nil || out.Content != "## Title\n\nText with [link](/a) and `code`." {
		t.Fatalf("%q %v", out, err)
	}
}

func TestWrapper(t *testing.T) {
	for text, want := range map[string]int{
		"¡Claro! Aquí tienes el borrador:\n## A\n\nTexto.":                1,
		"Here is the draft:\n## A\n\nText.":                               1,
		"## A\n\nTexto.\n\n¿Quieres que lo amplíe?":                       1,
		"## A\n\nText.\n\nI hope this helps!":                             1,
		"Claro que el agua fría lava peor.\n\nY más texto.":               0,
		"Here is what happened in 1998.\n\nMore.":                         0,
		"## A\n\nTexto.\n\nSi lo pruebas, cuéntamelo en los comentarios.": 0,
		"## A\n\nText.\n\nLet me know in the comments if you try it.":     0,
	} {
		if got := len(wrapperProblems(text)); got != want {
			t.Errorf("%q: %d problems, want %d", text, got, want)
		}
	}
}
