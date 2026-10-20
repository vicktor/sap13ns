package web

import (
	"context"
	"strings"
	"testing"
	"time"

	"sap13ns/internal/store"
)

// Search ignores accents both ways, on the public page and in the admin.
func TestSearchAccents(t *testing.T) {
	e := newCustomEnv(t)
	ctx := context.Background()
	now := time.Now().Add(-time.Hour)
	for _, p := range []store.PostInput{
		{Title: "Mediación familiar", Slug: "mediacion-familiar", Content: "Un acuerdo entre las partes."},
		{Title: "Conciliation", Slug: "conciliation", Content: "Without accents: conciliacion previa."},
		{Title: "Dret col·laboratiu", Slug: "dret", Content: "Què és la negociació?"},
	} {
		p.Type, p.Status, p.Lang, p.AuthorID, p.CommentStatus, p.PublishedAt = store.TypePost, "published", e.s.mainLang(), e.user("admin").ID, "open", &now
		if _, err := e.st.CreatePost(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	e.s.contentChanged()
	for q, want := range map[string]string{
		"mediacion":    "Mediación familiar",
		"MEDIACIÓN":    "Mediación familiar",
		"conciliación": "Conciliation",
		"negociacio":   "Dret col·laboratiu",
	} {
		posts, total, err := e.st.ListPosts(ctx, store.PostQuery{Type: store.TypePost, Search: q, Order: "rank"})
		if err != nil || total != 1 || posts[0].Title != want {
			t.Errorf("search %q: %d posts, %v", q, total, err)
		}
	}
	if body := e.get("/search?q=mediacion", nil).Body.String(); !strings.Contains(body, "Mediación familiar") {
		t.Error("the search page does not find an accented title without its accent")
	}
}
