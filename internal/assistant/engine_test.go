package assistant

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"hash/fnv"
	"io"
	"log/slog"
	"math"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"

	"sap13ns/internal/ai"
	"sap13ns/internal/auth"
	"sap13ns/internal/db"
	"sap13ns/internal/store"
)

// fakeEmbedder hashes words into a normalized bag-of-words vector, so that
// texts sharing words are close.
type fakeEmbedder struct {
	dims  int
	calls atomic.Int32
	texts atomic.Int32
}

func (f *fakeEmbedder) Embed(_ context.Context, req ai.EmbedRequest) (*ai.EmbedResponse, error) {
	f.calls.Add(1)
	f.texts.Add(int32(len(req.Inputs)))
	res := &ai.EmbedResponse{Model: "fake"}
	for _, in := range req.Inputs {
		v := make([]float32, f.dims)
		for _, w := range strings.FieldsFunc(strings.ToLower(in), func(r rune) bool { return !unicode.IsLetter(r) }) {
			h := fnv.New32a()
			h.Write([]byte(w))
			v[int(h.Sum32())%f.dims]++
		}
		var n float64
		for _, x := range v {
			n += float64(x * x)
		}
		for i := range v {
			v[i] = float32(float64(v[i])/math.Sqrt(n)) + 1e-4
		}
		res.Vectors = append(res.Vectors, v)
	}
	return res, nil
}

// scratchStore migrates a new database next to SAP13NS_TEST_DATABASE_URL
// into the schema "site_b", reached through a search_path that does not
// include public: pgvector (created in public) must work by qualified name,
// as for a site of a multisite install.
func scratchStore(t *testing.T) *store.Store {
	t.Helper()
	base := os.Getenv("SAP13NS_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("set SAP13NS_TEST_DATABASE_URL to run against PostgreSQL")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Skipf("test database unavailable: %v", err)
	}
	t.Cleanup(func() { admin.Close(context.Background()) })
	b := make([]byte, 6)
	rand.Read(b)
	name := "sap13ns_assistant_test_" + hex.EncodeToString(b)
	ident := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+ident); err != nil {
		t.Skipf("cannot create a scratch database: %v", err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+ident+" WITH (FORCE)"); err != nil {
			t.Errorf("drop scratch database %s: %v", name, err)
		}
	})
	var dsn string
	if strings.HasPrefix(base, "postgres://") || strings.HasPrefix(base, "postgresql://") {
		u, err := url.Parse(base)
		if err != nil {
			t.Fatal(err)
		}
		u.Path = "/" + name
		q := u.Query()
		q.Set("search_path", "site_b")
		u.RawQuery = q.Encode()
		dsn = u.String()
	} else {
		dsn = base + " dbname=" + name + " search_path=site_b"
	}
	conn, err := pgx.Connect(ctx, strings.Replace(dsn, "search_path=site_b", "search_path=public", 1))
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(ctx, "CREATE SCHEMA site_b")
	conn.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.Open(ctx, dsn, 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := db.Migrate(ctx, pool, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	return store.New(pool)
}

type fixture struct {
	t   *testing.T
	ctx context.Context
	st  *store.Store
	e   *Engine
	emb *fakeEmbedder
	uid int64
}

func (f *fixture) post(in store.PostInput) int64 {
	f.t.Helper()
	if in.Type == "" {
		in.Type = store.TypePost
	}
	if in.Status == "" {
		in.Status = store.StatusPublished
	}
	if in.PublishedAt == nil && in.Status == store.StatusPublished {
		past := time.Now().Add(-time.Hour)
		in.PublishedAt = &past
	}
	if in.Lang == "" {
		in.Lang = "en"
	}
	in.CommentStatus, in.AuthorID = "open", f.uid
	id, err := f.st.CreatePost(f.ctx, in)
	if err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *fixture) settings(fn func(*store.AssistantSettings)) {
	f.t.Helper()
	as, err := f.st.AssistantSettings(f.ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	fn(&as)
	if err := f.st.SaveAssistantSettings(f.ctx, as); err != nil {
		f.t.Fatal(err)
	}
	f.e.Reset()
}

func (f *fixture) reconcile(opt ReconcileOptions) Report {
	f.t.Helper()
	rep, err := f.e.Reconcile(f.ctx, opt)
	if err != nil {
		f.t.Fatal(err)
	}
	return rep
}

func (f *fixture) search(q, lang string) SearchResult {
	f.t.Helper()
	res, err := f.e.Search(f.ctx, q, SearchOptions{Lang: lang})
	if err != nil {
		f.t.Fatal(err)
	}
	return res
}

func posts(res SearchResult) []int64 {
	var out []int64
	for _, h := range res.Hits {
		if len(out) == 0 || out[len(out)-1] != h.PostID {
			out = append(out, h.PostID)
		}
	}
	return out
}

func has(res SearchResult, id int64) bool {
	for _, h := range res.Hits {
		if h.PostID == id {
			return true
		}
	}
	return false
}

func TestEngineIndexSearchAndExclusion(t *testing.T) {
	st := scratchStore(t)
	ctx := context.Background()
	f := &fixture{t: t, ctx: ctx, st: st, emb: &fakeEmbedder{dims: 16}}
	f.e = New(st, auth.NewKeys([]byte(strings.Repeat("k", 32))), slog.New(slog.NewTextHandler(io.Discard, nil)))
	f.e.newEmbedder = func(cfg ai.Config) (ai.Embedder, error) {
		if cfg.EmbedDimensions > 0 {
			f.emb.dims = cfg.EmbedDimensions
		}
		return f.emb, nil
	}
	uid, err := st.CreateUser(ctx, store.NewUser{Username: "editor", Email: "ed@example.com", Role: auth.RoleEditor, PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	f.uid = uid

	hoursES := f.post(store.PostInput{Title: "Horario de la tienda", Slug: "horario", Lang: "es",
		Content: "Abrimos de lunes a viernes, de nueve a dos. Los sábados la tienda cierra."})
	hoursEN := f.post(store.PostInput{Title: "Shop opening hours", Slug: "hours", Lang: "en", TranslationOf: hoursES,
		Content: "We open Monday to Friday, nine to two. On Saturdays the shop is closed."})
	pricing := f.post(store.PostInput{Type: store.TypePage, Title: "Pricing", Slug: "pricing",
		Content: "## Plans\n\nThe Pro plan costs ten euros a month.\n\n## Refunds\n\nRefunds within thirty days."})
	secret := f.post(store.PostInput{Title: "Founder notes", Slug: "notes", AssistantExclude: true,
		Content: "The founder's favourite colour is ultramarine."})
	future := time.Now().Add(48 * time.Hour)
	hidden := []int64{
		f.post(store.PostInput{Title: "Draft", Slug: "draft", Status: store.StatusDraft, Content: "ultramarine draft"}),
		f.post(store.PostInput{Title: "Private", Slug: "private", Status: store.StatusPrivate, Content: "ultramarine private"}),
		f.post(store.PostInput{Title: "Soon", Slug: "soon", PublishedAt: &future, Content: "ultramarine scheduled"}),
	}
	long := f.post(store.PostInput{Title: "History", Slug: "history",
		Content: "## Origins\n\n" + strings.Repeat("The workshop started small in a garage near the river. ", 120)})

	f.settings(func(as *store.AssistantSettings) {
		as.Enabled, as.EmbedProvider, as.EmbedBaseURL, as.EmbedModel = true, "openai", "http://127.0.0.1:1/v1", "fake-embed"
	})
	if _, _, ok, _ := st.Pgvector(ctx); !ok {
		if err := st.CreatePgvector(ctx); err != nil {
			t.Skipf("pgvector unavailable: %v", err)
		}
	}
	rep := f.reconcile(ReconcileOptions{})
	if rep.Indexed != 4 || rep.Failed != 0 {
		t.Fatalf("first pass: %+v", rep)
	}
	status, err := f.e.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Semantic || status.Vector != "vector(16)" || status.Eligible != 4 || status.Excluded != 1 ||
		status.Indexed != 4 || status.Pending != 0 || status.Chunks < 5 || status.Embedded != status.Chunks {
		t.Fatalf("status: %+v", status)
	}

	// Excluded, draft, private and scheduled posts are never found.
	res := f.search("ultramarine", "en")
	if has(res, secret) || len(res.Hits) != 0 {
		t.Errorf("hidden content found: %v", posts(res))
	}
	for _, id := range hidden {
		if has(res, id) {
			t.Errorf("post %d found", id)
		}
	}
	// Each visitor gets their language's version of a translated post.
	if got := posts(f.search("horario tienda abrimos", "es")); len(got) == 0 || got[0] != hoursES || contains(got, hoursEN) {
		t.Errorf("Spanish search: %v", got)
	}
	if got := posts(f.search("shop opening hours", "en")); len(got) == 0 || got[0] != hoursEN || contains(got, hoursES) {
		t.Errorf("English search: %v", got)
	}
	res = f.search("how much is the Pro plan?", "en")
	if !res.Semantic || res.Embeddings != 1 || len(res.Hits) == 0 || res.Hits[0].PostID != pricing ||
		!strings.Contains(res.Hits[0].Heading, "Pricing") {
		t.Errorf("pricing search: semantic %v, %+v", res.Semantic, res.Hits)
	}

	// Unchanged chunks keep their embeddings on a rebuild.
	before := f.emb.texts.Load()
	if rep := f.reconcile(ReconcileOptions{Rechunk: true}); rep.Indexed != 4 || rep.Embedded != 0 {
		t.Errorf("rechunk: %+v", rep)
	}
	if f.emb.texts.Load() != before {
		t.Errorf("rechunk embedded %d texts", f.emb.texts.Load()-before)
	}

	// Excluding or unpublishing removes a post at once.
	p, _ := st.PostByID(ctx, pricing)
	in := store.PostInput{Type: p.Type, Status: p.Status, Title: p.Title, Slug: p.Slug, Lang: p.Lang, Content: p.Content,
		CommentStatus: p.CommentStatus, PublishedAt: p.PublishedAt, AuthorID: uid, AssistantExclude: true}
	if err := st.UpdatePost(ctx, pricing, in, 0, 5); err != nil {
		t.Fatal(err)
	}
	f.e.Forget(ctx, pricing)
	if res := f.search("Pro plan euros", "en"); has(res, pricing) {
		t.Error("excluded page still found")
	}
	if err := st.SetPostStatus(ctx, hoursEN, store.StatusDraft); err != nil {
		t.Fatal(err)
	}
	f.e.Forget(ctx, hoursEN)
	if res := f.search("shop opening hours", "en"); has(res, hoursEN) {
		t.Error("unpublished post still found")
	}
	// A late write for a post that is no longer eligible is refused.
	ok, err := st.ReplaceAssistantChunks(ctx, store.AssistantIndexEntry{PostID: secret, PostUpdatedAt: time.Now(), Lang: "en",
		Chunks: []store.AssistantChunk{{Heading: "Founder notes", Content: "ultramarine", Hash: "x"}}}, nil)
	if err != nil || ok {
		t.Errorf("write for an excluded post: stored %v, %v", ok, err)
	}
	if res := f.search("ultramarine", "en"); len(res.Hits) != 0 {
		t.Error("excluded post written to the index")
	}
	status, _ = f.e.Status(ctx)
	if status.Eligible != 2 || status.Excluded != 2 || status.Indexed != 2 {
		t.Errorf("status after exclusions: %+v", status.AssistantStats)
	}

	// Another model with the same dimension: everything is embedded again.
	f.settings(func(as *store.AssistantSettings) { as.EmbedModel = "fake-embed-2" })
	if rep := f.reconcile(ReconcileOptions{}); rep.Indexed != 2 || rep.Embedded == 0 {
		t.Errorf("model switch: %+v", rep)
	}
	status, _ = f.e.Status(ctx)
	if status.Embedded != status.Chunks || status.State.Model != "fake-embed-2" {
		t.Errorf("after model switch: %+v", status)
	}

	// Above 2,000 dimensions the column becomes halfvec.
	f.settings(func(as *store.AssistantSettings) { as.EmbedDimensions = 2100 })
	f.reconcile(ReconcileOptions{})
	status, _ = f.e.Status(ctx)
	if status.Vector != "halfvec(2100)" || status.Embedded != status.Chunks || status.Pending != 0 {
		t.Errorf("halfvec: %+v", status)
	}
	if res := f.search("garage near the river", "en"); !res.Semantic || len(res.Hits) == 0 || res.Hits[0].PostID != long {
		t.Errorf("halfvec search: %+v", res)
	}

	// Without embeddings, full-text search keeps working.
	f.settings(func(as *store.AssistantSettings) { as.EmbedProvider = "none" })
	calls := f.emb.calls.Load()
	res = f.search("horario tienda", "es")
	if res.Semantic || len(res.Hits) == 0 || res.Hits[0].PostID != hoursES || f.emb.calls.Load() != calls {
		t.Errorf("full-text only: %+v", res)
	}
	if rep := f.reconcile(ReconcileOptions{}); rep.Failed != 0 || rep.Indexed != 0 {
		t.Errorf("pass without embeddings: %+v", rep)
	}
	if status, _ := f.e.Status(ctx); status.Indexed != 2 || status.Pending != 0 {
		t.Errorf("status without embeddings: %+v", status.AssistantStats)
	}
	// Switching them back on finds the vectors where they were.
	f.settings(func(as *store.AssistantSettings) { as.EmbedProvider = "openai" })
	if rep := f.reconcile(ReconcileOptions{}); rep.Embedded != 0 || rep.Indexed != 0 {
		t.Errorf("embeddings back on: %+v", rep)
	}
}

func contains(ids []int64, id int64) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}
