package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"sap13ns/internal/ai"
	"sap13ns/internal/auth"
	"sap13ns/internal/store"
)

const (
	// ReconcileEvery is how often the worker checks the whole index.
	ReconcileEvery = 10 * time.Minute
	// halfvecAbove: pgvector's HNSW index takes vector up to 2,000
	// dimensions and halfvec up to 4,000.
	halfvecAbove = 2000
	// maxDistance is the cosine distance beyond which a chunk is not
	// considered related at all (similarity below 0.25).
	maxDistance       = 0.75
	searchK           = 20 // candidates from each search
	batchSize         = 50 // posts per pending query
	queryEmbedTimeout = 20 * time.Second
	// Key derivation contexts (associated data) of the encrypted API keys.
	EmbedKeyAD = "assistant-embed-key"
	aiKeyAD    = "ai-api-key"
)

// Engine indexes the site's content and searches it. One Engine serves a
// site (one database); it is safe for concurrent use.
type Engine struct {
	st    *store.Store
	keys  *auth.Keys
	log   *slog.Logger
	nudge chan struct{}
	now   func() time.Time
	// newEmbedder builds the embeddings adapter (replaced in tests).
	newEmbedder func(ai.Config) (ai.Embedder, error)

	pass sync.Mutex // one indexing pass at a time

	mu      sync.Mutex
	backend *backend
	probed  map[string]int // dimensions measured per embeddings configuration
}

// backend is the resolved search setup: the embedder (nil for full-text
// only) and the vector column it fills (nil without pgvector).
type backend struct {
	key         string
	embedder    ai.Embedder
	model       string // configured embedding model
	vec         *store.AssistantVector
	pgvector    string // version and schema, or why it is missing
	hasPgvector bool
	problem     string // why semantic search is off, for the status page
	checked     time.Time
}

// model is the embedding model whose vectors the index holds right now.
func (b *backend) indexModel() string {
	if b.vec == nil {
		return ""
	}
	return b.vec.Model
}

func New(st *store.Store, keys *auth.Keys, log *slog.Logger) *Engine {
	return &Engine{st: st, keys: keys, log: log, nudge: make(chan struct{}, 1), now: time.Now, probed: map[string]int{},
		newEmbedder: func(cfg ai.Config) (ai.Embedder, error) { return ai.NewEmbedder(cfg, nil) }}
}

// Nudge asks the worker for a pass soon (content or settings changed).
func (e *Engine) Nudge() {
	select {
	case e.nudge <- struct{}{}:
	default:
	}
}

// Reset forgets the resolved backend after the settings changed.
func (e *Engine) Reset() {
	e.mu.Lock()
	e.backend = nil
	e.mu.Unlock()
	e.Nudge()
}

// Run indexes in the background until ctx ends: shortly after start, after
// every nudge (debounced) and every ReconcileEvery. It does nothing while
// both the assistant and MCP are off.
func (e *Engine) Run(ctx context.Context) {
	t := time.NewTicker(ReconcileEvery)
	defer t.Stop()
	wake := time.After(15 * time.Second)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-wake:
		case <-e.nudge:
			// Let a burst of saves settle.
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}
		set, err := e.st.AssistantSettings(ctx)
		if err != nil {
			e.log.Error("assistant settings", "err", err)
			continue
		}
		if !set.Enabled && !set.MCPEnabled {
			continue
		}
		if _, err := e.Reconcile(ctx, ReconcileOptions{}); err != nil && ctx.Err() == nil {
			e.log.Error("assistant index", "err", err)
		}
	}
}

// Forget removes a post from the index at once if it may no longer be
// used (unpublished, excluded, made private, trashed), then schedules a
// pass for its new content.
func (e *Engine) Forget(ctx context.Context, postID int64) {
	if _, err := e.st.DropIneligibleAssistantChunks(ctx, postID); err != nil {
		e.log.Error("assistant drop", "post", postID, "err", err)
	}
	e.Nudge()
}

// ------------------------------------------------------------- embedder --

// EmbedderConfig resolves the embeddings provider of the settings. It
// returns a nil embedder, and why, when semantic search is not configured.
func EmbedderConfig(as store.AssistantSettings, aiSt store.AISettings, keys *auth.Keys) (*ai.Config, string, error) {
	var cfg ai.Config
	switch as.EmbedProvider {
	case "", "none":
		return nil, "embeddings are off: full-text search only", nil
	case "same":
		if !aiSt.Enabled {
			return nil, "the AI provider is switched off (AI)", nil
		}
		if aiSt.Source == store.AISourceNetwork {
			return nil, "the network's AI does not make embeddings: choose an embeddings provider", nil
		}
		if aiSt.Provider == ai.ProviderAnthropic {
			return nil, "Claude has no embeddings API: choose an embeddings provider", nil
		}
		key, err := decrypt(keys, aiSt.APIKeyEnc, aiKeyAD)
		if err != nil {
			return nil, "", fmt.Errorf("decrypt AI key: %w", err)
		}
		cfg = ai.Config{Provider: aiSt.Provider, APIKey: key, BaseURL: aiSt.BaseURL, TextModel: aiSt.TextModel,
			AllowInsecureLocal: aiSt.AllowLocal}
	case ai.ProviderOpenAI, ai.ProviderGemini:
		key, err := decrypt(keys, as.EmbedKeyEnc, EmbedKeyAD)
		if err != nil {
			return nil, "", fmt.Errorf("decrypt embeddings key: %w", err)
		}
		cfg = ai.Config{Provider: as.EmbedProvider, APIKey: key, BaseURL: as.EmbedBaseURL, AllowInsecureLocal: as.EmbedAllowLocal}
	default:
		return nil, "", fmt.Errorf("unknown embeddings provider %q", as.EmbedProvider)
	}
	cfg.EmbedModel, cfg.EmbedDimensions = strings.TrimSpace(as.EmbedModel), as.EmbedDimensions
	if cfg.EmbedModel == "" {
		cfg.EmbedModel = ai.DefaultEmbedModel(cfg.Provider)
	}
	if cfg.AllowInsecureLocal {
		cfg.Timeout = 2 * time.Minute
	}
	return &cfg, "", nil
}

func decrypt(keys *auth.Keys, enc []byte, ad string) (string, error) {
	if len(enc) == 0 {
		return "", nil
	}
	b, err := keys.Decrypt(enc, []byte(ad))
	return string(b), err
}

// fingerprint identifies an embeddings configuration (the key by hash).
func fingerprint(cfg *ai.Config) string {
	h := sha256.Sum256([]byte(strings.Join([]string{cfg.Provider, cfg.BaseURL, cfg.EmbedModel,
		strconv.Itoa(cfg.EmbedDimensions), cfg.APIKey, strconv.FormatBool(cfg.AllowInsecureLocal)}, "\x00")))
	return hex.EncodeToString(h[:])
}

// resolve returns the backend, reusing the last one for maxAge. Resolving
// checks pgvector (creating the extension when allowed), measures the
// model's dimensions once, and creates or replaces the embedding column.
func (e *Engine) resolve(ctx context.Context, maxAge time.Duration) (*backend, error) {
	e.mu.Lock()
	b := e.backend
	e.mu.Unlock()
	if b != nil && e.now().Sub(b.checked) < maxAge {
		return b, nil
	}
	as, err := e.st.AssistantSettings(ctx)
	if err != nil {
		return nil, err
	}
	aiSt, err := e.st.AISettings(ctx)
	if err != nil {
		return nil, err
	}
	b = &backend{checked: e.now()}
	cfg, why, err := EmbedderConfig(as, aiSt, e.keys)
	if err != nil {
		b.problem = err.Error()
	} else if cfg == nil {
		b.problem = why
	} else {
		b.key = fingerprint(cfg)
		b.model = cfg.EmbedModel
		if emb, err := e.newEmbedder(*cfg); err != nil {
			b.problem = "embeddings: " + err.Error()
		} else {
			b.embedder = emb
		}
	}

	schema, version, ok, err := e.st.Pgvector(ctx)
	if err != nil {
		return nil, err
	}
	if !ok && b.embedder != nil {
		if cerr := e.st.CreatePgvector(ctx); cerr != nil {
			b.pgvector = "not installed (" + firstLine(cerr.Error()) + ")"
		} else if schema, version, ok, err = e.st.Pgvector(ctx); err != nil {
			return nil, err
		}
	}
	b.hasPgvector = ok
	if ok {
		b.pgvector = version + " (schema " + schema + ")"
	} else if b.pgvector == "" {
		b.pgvector = "not installed"
	}
	if ok && b.embedder != nil {
		if err := e.prepareColumn(ctx, b, schema, cfg.EmbedDimensions); err != nil {
			b.problem = err.Error()
			b.vec = nil
		}
	} else if b.embedder != nil && b.problem == "" {
		b.problem = "pgvector is not available: full-text search only"
	}
	e.mu.Lock()
	e.backend = b
	e.mu.Unlock()
	return b, nil
}

// prepareColumn makes sure assistant_chunks.embedding fits the model.
func (e *Engine) prepareColumn(ctx context.Context, b *backend, schema string, dims int) error {
	if dims == 0 {
		e.mu.Lock()
		dims = e.probed[b.key]
		e.mu.Unlock()
	}
	if dims == 0 {
		cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		res, err := b.embedder.Embed(cctx, ai.EmbedRequest{Inputs: []string{"dimension probe"}})
		cancel()
		if err != nil {
			return fmt.Errorf("embeddings: %w", err)
		}
		dims = len(res.Vectors[0])
		e.mu.Lock()
		e.probed[b.key] = dims
		e.mu.Unlock()
	}
	kind := "vector"
	if dims > halfvecAbove {
		kind = "halfvec"
	}
	v := &store.AssistantVector{Schema: schema, Kind: kind, Dims: dims, Model: b.model}
	have, haveDims, err := e.st.AssistantEmbeddingColumn(ctx)
	if err != nil {
		return err
	}
	if have != kind || haveDims != dims {
		e.log.Info("assistant: creating the embedding column", "type", v.String(), "previous", have)
		if err := e.st.SetAssistantEmbeddingColumn(ctx, v); err != nil {
			return fmt.Errorf("embedding column: %w", err)
		}
	}
	if err := e.st.ForgetAssistantEmbeddings(ctx, v); err != nil {
		return err
	}
	b.vec = v
	return nil
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	return limitRunes(s, 200)
}

// --------------------------------------------------------------- indexer --

// ReconcileOptions modify an indexing pass.
type ReconcileOptions struct {
	Rechunk bool // reindex every post (unchanged chunks keep their embeddings)
	Reembed bool // also discard every embedding
}

// Report sums up an indexing pass.
type Report struct {
	Indexed  int
	Failed   int
	Removed  int64
	Embedded int
	Duration time.Duration
}

// Reconcile brings the index up to date: it drops posts that may no longer
// be used and (re)indexes those that are new, changed, or were embedded
// with another model. Chunks whose text did not change keep their
// embedding.
func (e *Engine) Reconcile(ctx context.Context, opt ReconcileOptions) (Report, error) {
	e.pass.Lock()
	defer e.pass.Unlock()
	start := e.now()
	var rep Report
	b, err := e.resolve(ctx, 0)
	if err != nil {
		return rep, err
	}
	if opt.Rechunk || opt.Reembed {
		if err := e.st.ResetAssistantIndex(ctx, opt.Reembed); err != nil {
			return rep, err
		}
	}
	if rep.Removed, err = e.st.DropIneligibleAssistantChunks(ctx, 0); err != nil {
		return rep, err
	}
	var lastErr string
	seen := map[int64]bool{}
	for ctx.Err() == nil {
		ids, err := e.st.AssistantPending(ctx, b.indexModel(), batchSize)
		if err != nil {
			return rep, err
		}
		fresh := 0
		for _, id := range ids {
			if seen[id] {
				continue
			}
			seen[id] = true
			fresh++
			n, err := e.indexPost(ctx, b, id)
			rep.Embedded += n
			if err != nil {
				if ctx.Err() != nil {
					break
				}
				rep.Failed++
				lastErr = err.Error()
				e.log.Warn("assistant: index post", "post", id, "err", err)
				continue
			}
			rep.Indexed++
		}
		if fresh == 0 || len(ids) < batchSize {
			break
		}
	}
	rep.Duration = e.now().Sub(start)
	if rep.Embedded > 0 {
		if err := e.st.AddAssistantUsage(ctx, e.now(), 0, 0, 0, rep.Embedded); err != nil {
			e.log.Error("assistant usage", "err", err)
		}
	}
	state := store.AssistantIndexState{LastRun: start, Duration: rep.Duration.Round(time.Millisecond).String(),
		Indexed: rep.Indexed, Removed: rep.Removed, LastError: limitRunes(lastErr, 300), Model: b.indexModel(), Pgvector: b.pgvector}
	if b.vec != nil {
		state.Vector = b.vec.String()
	}
	if err := e.st.SaveAssistantIndexState(ctx, state); err != nil {
		return rep, err
	}
	if rep.Indexed > 0 || rep.Removed > 0 || rep.Failed > 0 {
		e.log.Info("assistant index", "indexed", rep.Indexed, "failed", rep.Failed, "removed", rep.Removed,
			"embedded", rep.Embedded, "took", rep.Duration.Round(time.Millisecond))
	}
	return rep, ctx.Err()
}

// indexPost chunks and embeds one post and returns how many chunks it
// embedded. When embedding fails the chunks are stored for full-text
// search and the error recorded; the post is retried later.
func (e *Engine) indexPost(ctx context.Context, b *backend, id int64) (int, error) {
	p, err := e.st.PostByID(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if !p.AssistantEligible(e.now()) {
		_, err := e.st.DropIneligibleAssistantChunks(ctx, id)
		return 0, err
	}
	model := b.indexModel()
	srcHash := hashOf(p.Lang, p.Title, p.Excerpt, p.Content)
	doc, err := e.st.AssistantDoc(ctx, id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return 0, err
	}
	if doc != nil && doc.SourceHash == srcHash && doc.Error == "" && (model == "" || doc.Model == model) {
		// Only the date or status changed (or embeddings are off, and the
		// stored vectors wait for them).
		return 0, e.st.TouchAssistantDoc(ctx, id, p.UpdatedAt)
	}

	pieces := Chunk(p.Title, p.Excerpt, p.Content, DefaultChunking)
	entry := store.AssistantIndexEntry{PostID: id, PostUpdatedAt: p.UpdatedAt, Lang: p.Lang, SourceHash: srcHash, Model: model}
	entry.Chunks = make([]store.AssistantChunk, len(pieces))
	for i, pc := range pieces {
		entry.Chunks[i] = store.AssistantChunk{Heading: pc.Heading, Content: pc.Content, Hash: hashOf(pc.Text())}
	}
	embedded := 0
	if b.vec != nil && len(pieces) > 0 {
		old, err := e.st.AssistantEmbeddings(ctx, id, b.vec)
		if err != nil {
			return 0, err
		}
		var need []int
		var inputs []string
		for i := range entry.Chunks {
			if v, ok := old[entry.Chunks[i].Hash]; ok {
				entry.Chunks[i].Vector = v
				continue
			}
			need = append(need, i)
			inputs = append(inputs, pieces[i].Text())
		}
		if len(inputs) > 0 {
			res, err := b.embedder.Embed(ctx, ai.EmbedRequest{Inputs: inputs})
			if err == nil && len(res.Vectors[0]) != b.vec.Dims {
				err = fmt.Errorf("the model returned %d dimensions, the index has %d", len(res.Vectors[0]), b.vec.Dims)
				e.Reset()
			}
			if err != nil {
				if ctx.Err() != nil {
					return 0, ctx.Err()
				}
				entry.Error = "embeddings: " + err.Error()
			} else {
				for j, i := range need {
					if entry.Chunks[i].Vector, err = vectorText(res.Vectors[j]); err != nil {
						entry.Error = err.Error()
						break
					}
				}
				embedded = len(need)
			}
		}
		if entry.Error != "" {
			for i := range entry.Chunks {
				entry.Chunks[i].Vector = ""
			}
			embedded = 0
		}
	}
	if _, err := e.st.ReplaceAssistantChunks(ctx, entry, b.vec); err != nil {
		return 0, err
	}
	if entry.Error != "" {
		return embedded, errors.New(entry.Error)
	}
	return embedded, nil
}

func hashOf(parts ...string) string {
	h := sha256.New()
	for i, p := range parts {
		if i > 0 {
			h.Write([]byte{0})
		}
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// vectorText writes an embedding in pgvector's text format.
func vectorText(v []float32) (string, error) {
	var b strings.Builder
	b.Grow(len(v) * 10)
	b.WriteByte('[')
	for i, f := range v {
		if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
			return "", errors.New("embeddings: the model returned a non-finite number")
		}
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String(), nil
}

// ---------------------------------------------------------------- search --

// SearchOptions shape a search.
type SearchOptions struct {
	Lang    string // the visitor's language, preferred
	Limit   int    // chunks to return (default 6)
	PerPost int    // chunks of one post (default 2)
}

// SearchResult carries the hits and what the search cost.
type SearchResult struct {
	Hits       []Hit
	Semantic   bool // the vector search ran
	Embeddings int  // query texts embedded
}

// Search runs the hybrid search: full-text and, when available, vector
// similarity, fused with RRF. Only chunks of posts the assistant may use
// are returned. A failing vector search falls back to full-text alone.
func (e *Engine) Search(ctx context.Context, query string, opt SearchOptions) (SearchResult, error) {
	var res SearchResult
	if opt.Limit <= 0 {
		opt.Limit = 6
	}
	if opt.PerPost <= 0 {
		opt.PerPost = 2
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return res, nil
	}
	b, err := e.resolve(ctx, time.Minute)
	if err != nil {
		return res, err
	}
	var text, near []store.AssistantHit
	if q := FTSQuery(query); q != "" {
		if text, err = e.st.SearchAssistantText(ctx, q, searchK); err != nil {
			return res, err
		}
	}
	if b.vec != nil {
		cctx, cancel := context.WithTimeout(ctx, queryEmbedTimeout)
		emb, err := b.embedder.Embed(cctx, ai.EmbedRequest{Inputs: []string{query}, Query: true})
		cancel()
		var vec string
		if err == nil {
			res.Embeddings = 1
			if len(emb.Vectors[0]) != b.vec.Dims {
				err = fmt.Errorf("query embedding has %d dimensions, the index %d", len(emb.Vectors[0]), b.vec.Dims)
			} else {
				vec, err = vectorText(emb.Vectors[0])
			}
		}
		if err == nil {
			near, err = e.st.SearchAssistantVector(ctx, b.vec, vec, maxDistance, searchK)
		}
		if err != nil {
			if ctx.Err() != nil {
				return res, ctx.Err()
			}
			e.log.Warn("assistant: semantic search unavailable, using full-text only", "err", err)
		} else {
			res.Semantic = true
		}
	}
	res.Hits = Fuse(FuseOptions{Lang: opt.Lang, LangBoost: 1.25, PerPost: opt.PerPost, Limit: opt.Limit}, text, near)
	return res, nil
}

// Sources numbers hits for the prompt; url gives each one's public link.
func Sources(hits []Hit, url func(Hit) string) []Source {
	out := make([]Source, len(hits))
	for i, h := range hits {
		heading := h.Heading
		if t, rest, ok := strings.Cut(heading, headingSep); ok && t == h.Title {
			heading = rest
		} else if heading == h.Title {
			heading = ""
		}
		out[i] = Source{N: i + 1, PostID: h.PostID, Title: h.Title, URL: url(h), Heading: heading,
			Snippet: snippet(h.Content, 220), Lang: h.Lang, Published: h.PublishedAt, Content: h.Content}
	}
	return out
}

// snippet is plain text from the start of a chunk, for source cards.
func snippet(md string, n int) string {
	var words []string
	for _, l := range strings.Split(md, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "```") || strings.HasPrefix(l, "~~~") || l == "" {
			continue
		}
		l = strings.TrimLeft(l, "#>*-+| ")
		words = append(words, strings.Fields(strings.NewReplacer("**", "", "__", "", "`", "").Replace(l))...)
		if len(words) > n/3 {
			break
		}
	}
	s := strings.Join(words, " ")
	if runeLen(s) <= n {
		return s
	}
	r := []rune(s)[:n]
	if i := strings.LastIndexByte(string(r), ' '); i > n/2 {
		return string(r)[:i] + "…"
	}
	return string(r) + "…"
}

// ---------------------------------------------------------------- status --

// Status describes the index for the admin page and the CLI.
type Status struct {
	store.AssistantStats
	State       store.AssistantIndexState
	Model       string // configured embedding model ("" for none)
	Vector      string // column type in use, "" without semantic search
	Pgvector    string
	HasPgvector bool
	Problem     string // why semantic search is off
	Semantic    bool
	Failures    []store.AssistantFailure
}

// Status reports the index. It resolves the backend afresh, which can
// create the pgvector extension and the embedding column.
func (e *Engine) Status(ctx context.Context) (Status, error) {
	var st Status
	b, err := e.resolve(ctx, time.Minute)
	if err != nil {
		return st, err
	}
	st.Model, st.Pgvector, st.Problem, st.Semantic = b.model, b.pgvector, b.problem, b.vec != nil
	st.HasPgvector = b.hasPgvector
	if b.vec != nil {
		st.Vector = b.vec.String()
	}
	if st.AssistantStats, err = e.st.AssistantStats(ctx, b.indexModel()); err != nil {
		return st, err
	}
	if st.State, err = e.st.AssistantIndexState(ctx); err != nil {
		return st, err
	}
	st.Failures, err = e.st.AssistantFailures(ctx, 10)
	return st, err
}
