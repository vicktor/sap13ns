package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// AssistantSettings configure the site assistant: the chat visitors use, its
// embeddings backend and the read-only MCP endpoint. The text answers come
// from the AI provider of AISettings. Like AISettings they hold an API key,
// encrypted with the server secret.
type AssistantSettings struct {
	Enabled     bool     `json:"enabled"`
	Name        string   `json:"name"`
	Greeting    string   `json:"greeting"`
	Suggestions []string `json:"suggestions"`
	// Text translates the greeting, suggestions and bubble label per extra
	// language.
	Text map[string]AssistantText `json:"text,omitempty"`
	// The chat button ("bubble"): the bottom corner it sits in, how high
	// above the bottom edge, and an optional short label shown beside it.
	// An empty place is left out of the JSON, so it reads back as the
	// default.
	BubblePosition string `json:"bubble_position,omitempty"` // BubbleRight or BubbleLeft
	BubbleOffset   string `json:"bubble_offset,omitempty"`   // BubbleNormal, BubbleRaised or BubbleHigh
	BubbleLabel    string `json:"bubble_label,omitempty"`
	// Limits: questions per visitor IP and hour, and site-wide questions per
	// day and per month (0 means unlimited).
	PerIPHour       int `json:"per_ip_hour"`
	DailyRequests   int `json:"daily_requests"`
	MonthlyRequests int `json:"monthly_requests"`
	// Embeddings: "same" (the AI text provider), "openai" (any
	// OpenAI-compatible server), "gemini" or "none" (full-text search only).
	EmbedProvider   string `json:"embed_provider"`
	EmbedBaseURL    string `json:"embed_base_url"`
	EmbedKeyEnc     []byte `json:"embed_key_enc,omitempty"`
	EmbedModel      string `json:"embed_model"`
	EmbedDimensions int    `json:"embed_dimensions"` // 0: the model's own
	EmbedAllowLocal bool   `json:"embed_allow_local"`
	// MCP serves search_posts and get_post at /mcp. The optional bearer
	// token is kept as its SHA-256 (hex).
	MCPEnabled   bool   `json:"mcp_enabled"`
	MCPTokenHash string `json:"mcp_token_hash,omitempty"`
}

// The places of the chat button.
const (
	BubbleRight = "right"
	BubbleLeft  = "left"

	BubbleNormal = "normal" // in the corner
	BubbleRaised = "raised" // above a cookie bar or a bottom banner
	BubbleHigh   = "high"   // well above the bottom edge
)

// BubblePositions and BubbleOffsets are the valid values, the default first.
var (
	BubblePositions = []string{BubbleRight, BubbleLeft}
	BubbleOffsets   = []string{BubbleNormal, BubbleRaised, BubbleHigh}
)

// AssistantText is the translatable part of the assistant settings.
type AssistantText struct {
	Greeting    string   `json:"greeting"`
	Suggestions []string `json:"suggestions"`
	BubbleLabel string   `json:"bubble_label,omitempty"`
}

// In returns the greeting, suggestions and bubble label for lang, falling
// back to the main-language values.
func (s AssistantSettings) In(lang string) AssistantText {
	out := AssistantText{Greeting: s.Greeting, Suggestions: s.Suggestions, BubbleLabel: s.BubbleLabel}
	if t, ok := s.Text[lang]; ok {
		if t.Greeting != "" {
			out.Greeting = t.Greeting
		}
		if len(t.Suggestions) > 0 {
			out.Suggestions = t.Suggestions
		}
		if t.BubbleLabel != "" {
			out.BubbleLabel = t.BubbleLabel
		}
	}
	return out
}

const assistantSettingsKey = "assistant"

// DefaultAssistantSettings are the settings of a site that never saved
// them.
func DefaultAssistantSettings() AssistantSettings {
	return AssistantSettings{PerIPHour: 20, DailyRequests: 300, MonthlyRequests: 3000, EmbedProvider: "same",
		BubblePosition: BubbleRight, BubbleOffset: BubbleNormal}
}

func (s *Store) AssistantSettings(ctx context.Context) (AssistantSettings, error) {
	st := DefaultAssistantSettings()
	if err := s.GetJSON(ctx, assistantSettingsKey, &st); err != nil && !errors.Is(err, ErrNotFound) {
		return st, err
	}
	return st, nil
}

func (s *Store) SaveAssistantSettings(ctx context.Context, st AssistantSettings) error {
	return s.SetJSON(ctx, assistantSettingsKey, st)
}

// AssistantIndexState records the last indexing pass, for the status panel
// and `sap13ns assistant status` (which runs in another process).
type AssistantIndexState struct {
	LastRun   time.Time `json:"last_run"`
	Duration  string    `json:"duration"`
	Indexed   int       `json:"indexed"` // posts (re)indexed in that pass
	Removed   int64     `json:"removed"` // posts dropped from the index
	LastError string    `json:"last_error"`
	Vector    string    `json:"vector"` // embedding column type, e.g. "vector(768)"; "" without one
	Model     string    `json:"model"`  // embedding model; "" for full-text only
	Pgvector  string    `json:"pgvector"`
}

const assistantIndexKey = "assistant_index"

func (s *Store) AssistantIndexState(ctx context.Context) (AssistantIndexState, error) {
	var st AssistantIndexState
	if err := s.GetJSON(ctx, assistantIndexKey, &st); err != nil && !errors.Is(err, ErrNotFound) {
		return st, err
	}
	return st, nil
}

func (s *Store) SaveAssistantIndexState(ctx context.Context, st AssistantIndexState) error {
	return s.SetJSON(ctx, assistantIndexKey, st)
}

// ------------------------------------------------------------------ usage --

// AssistantUsage is the site-wide use of the assistant over a period.
type AssistantUsage struct {
	Day          time.Time `db:"day"`
	Requests     int       `db:"requests"`
	InputTokens  int64     `db:"input_tokens"`
	OutputTokens int64     `db:"output_tokens"`
	Embeddings   int       `db:"embeddings"`
}

func dayOf(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// AddAssistantUsage counts questions (requests), tokens and embedded texts
// on the UTC day of at.
func (s *Store) AddAssistantUsage(ctx context.Context, at time.Time, requests int, in, out int64, embeddings int) error {
	_, err := s.DB.Exec(ctx, `INSERT INTO assistant_usage (day, requests, input_tokens, output_tokens, embeddings)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (day) DO UPDATE SET requests = assistant_usage.requests + EXCLUDED.requests,
			input_tokens = assistant_usage.input_tokens + EXCLUDED.input_tokens,
			output_tokens = assistant_usage.output_tokens + EXCLUDED.output_tokens,
			embeddings = assistant_usage.embeddings + EXCLUDED.embeddings`,
		dayOf(at), requests, in, out, embeddings)
	return err
}

// AssistantUsageTotals returns the usage of the day and of the month of at.
func (s *Store) AssistantUsageTotals(ctx context.Context, at time.Time) (day, month AssistantUsage, err error) {
	d := dayOf(at)
	err = s.DB.QueryRow(ctx, `SELECT
		coalesce(sum(requests) FILTER (WHERE day = $1), 0)::int, coalesce(sum(input_tokens) FILTER (WHERE day = $1), 0)::bigint,
		coalesce(sum(output_tokens) FILTER (WHERE day = $1), 0)::bigint, coalesce(sum(embeddings) FILTER (WHERE day = $1), 0)::int,
		coalesce(sum(requests), 0)::int, coalesce(sum(input_tokens), 0)::bigint,
		coalesce(sum(output_tokens), 0)::bigint, coalesce(sum(embeddings), 0)::int
		FROM assistant_usage WHERE day >= $2 AND day <= $1`, d, monthOf(at)).
		Scan(&day.Requests, &day.InputTokens, &day.OutputTokens, &day.Embeddings,
			&month.Requests, &month.InputTokens, &month.OutputTokens, &month.Embeddings)
	day.Day, month.Day = d, monthOf(at)
	return day, month, err
}

// AssistantUsageDays lists the last n days with any use, newest first.
func (s *Store) AssistantUsageDays(ctx context.Context, n int) ([]AssistantUsage, error) {
	return collect[AssistantUsage](s.DB.Query(ctx, `SELECT day, requests, input_tokens, output_tokens, embeddings
		FROM assistant_usage ORDER BY day DESC LIMIT $1`, n))
}

// ----------------------------------------------------------------- index --

// AssistantEligibleCond selects the posts the assistant may use: public
// right now (published, not scheduled, not private) and not excluded by
// their editor. sap13ns has no password-protected posts. Post.AssistantEligible
// is the same rule in Go.
const AssistantEligibleCond = publicCond + ` AND NOT p.assistant_exclude AND p.type IN ('post', 'page')`

// AssistantEligible reports whether the assistant may index and cite p.
func (p *Post) AssistantEligible(now time.Time) bool {
	return p.IsPublic(now) && !p.AssistantExclude && (p.Type == TypePost || p.Type == TypePage)
}

// AssistantVector describes the embedding column added at runtime. Type is
// the schema-qualified pgvector type, so queries work whatever the
// search_path of a site's schema is.
type AssistantVector struct {
	Schema string // where the extension lives, normally public
	Kind   string // "vector" or "halfvec"
	Dims   int
	Model  string
}

func (v *AssistantVector) typ() string {
	return pgx.Identifier{v.Schema, v.Kind}.Sanitize()
}

func (v *AssistantVector) op() string {
	return "OPERATOR(" + pgx.Identifier{v.Schema}.Sanitize() + ".<=>)"
}

// String is the column type as PostgreSQL prints it.
func (v *AssistantVector) String() string { return v.Kind + "(" + strconv.Itoa(v.Dims) + ")" }

// Pgvector reports where and in which version pgvector is installed; ok is
// false when the extension does not exist in this database.
func (s *Store) Pgvector(ctx context.Context) (schema, version string, ok bool, err error) {
	err = s.DB.QueryRow(ctx, `SELECT n.nspname, e.extversion FROM pg_extension e
		JOIN pg_namespace n ON n.oid = e.extnamespace WHERE e.extname = 'vector'`).Scan(&schema, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", false, nil
	}
	return schema, version, err == nil, err
}

// CreatePgvector tries to create the extension in public. It fails without
// the privilege to do so (pgvector is not a trusted extension); callers
// treat that as "full-text search only".
func (s *Store) CreatePgvector(ctx context.Context) error {
	_, err := s.DB.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS vector SCHEMA public`)
	return err
}

// AssistantEmbeddingColumn returns the type and dimension of
// assistant_chunks.embedding, or "" when the column does not exist.
func (s *Store) AssistantEmbeddingColumn(ctx context.Context) (kind string, dims int, err error) {
	err = s.DB.QueryRow(ctx, `SELECT t.typname, a.atttypmod FROM pg_attribute a JOIN pg_type t ON t.oid = a.atttypid
		WHERE a.attrelid = 'assistant_chunks'::regclass AND a.attname = 'embedding' AND NOT a.attisdropped`).Scan(&kind, &dims)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", 0, nil
	}
	return kind, dims, err
}

// assistantLockID serializes changes to the embedding column between
// processes (the server and `sap13ns assistant reindex`).
const assistantLockID = 7_201_314

// SetAssistantEmbeddingColumn (re)creates the embedding column with an HNSW
// cosine index. Every existing embedding is lost, so chunks and posts are
// marked as not embedded. Only catalog names and an integer are spliced
// into the DDL.
func (s *Store) SetAssistantEmbeddingColumn(ctx context.Context, v *AssistantVector) error {
	if v.Kind != "vector" && v.Kind != "halfvec" || v.Dims < 1 || v.Dims > 4000 {
		return fmt.Errorf("invalid embedding column %s", v)
	}
	return s.InTx(ctx, func(tx pgx.Tx) error {
		stmts := []string{
			`SET LOCAL statement_timeout = '120s'`,
			`SELECT pg_advisory_xact_lock(` + strconv.Itoa(assistantLockID) + `)`,
			`ALTER TABLE assistant_chunks DROP COLUMN IF EXISTS embedding`,
			`ALTER TABLE assistant_chunks ADD COLUMN embedding ` + v.typ() + `(` + strconv.Itoa(v.Dims) + `)`,
			`CREATE INDEX assistant_chunks_embedding_idx ON assistant_chunks USING hnsw (embedding ` +
				pgx.Identifier{v.Schema, v.Kind + "_cosine_ops"}.Sanitize() + `)`,
			`UPDATE assistant_chunks SET model = '' WHERE model <> ''`,
			`UPDATE assistant_docs SET model = '' WHERE model <> ''`,
		}
		for _, q := range stmts {
			if _, err := tx.Exec(ctx, q); err != nil {
				return err
			}
		}
		return nil
	})
}

// ForgetAssistantEmbeddings clears embeddings made by another model than
// the current one (a model switch with the same dimension), so that the
// vector index never mixes two embedding spaces.
func (s *Store) ForgetAssistantEmbeddings(ctx context.Context, v *AssistantVector) error {
	return s.InTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE assistant_chunks SET embedding = NULL, model = '' WHERE model <> '' AND model <> $1`, v.Model); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE assistant_docs SET model = '' WHERE model <> '' AND model <> $1`, v.Model)
		return err
	})
}

// DropIneligibleAssistantChunks removes from the index every post that is
// no longer public or was excluded, and returns how many posts it dropped.
// With postID > 0 only that post is checked.
func (s *Store) DropIneligibleAssistantChunks(ctx context.Context, postID int64) (int64, error) {
	var n int64
	err := s.InTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM assistant_docs d WHERE ($1 = 0 OR d.post_id = $1)
			AND NOT EXISTS (SELECT 1 FROM posts p WHERE p.id = d.post_id AND `+AssistantEligibleCond+`)`, postID)
		if err != nil {
			return err
		}
		n = tag.RowsAffected()
		_, err = tx.Exec(ctx, `DELETE FROM assistant_chunks c WHERE ($1 = 0 OR c.post_id = $1)
			AND NOT EXISTS (SELECT 1 FROM posts p WHERE p.id = c.post_id AND `+AssistantEligibleCond+`)`, postID)
		return err
	})
	return n, err
}

// AssistantPending lists eligible posts whose index entry is missing, out
// of date, or made with another embedding model. With model "" (no
// embeddings) stored vectors are left alone. Failed posts are retried
// after 2^attempts minutes (at most six hours) unless they changed.
func (s *Store) AssistantPending(ctx context.Context, model string, limit int) ([]int64, error) {
	rows, err := s.DB.Query(ctx, `SELECT p.id FROM posts p LEFT JOIN assistant_docs d ON d.post_id = p.id
		WHERE `+AssistantEligibleCond+` AND (d.post_id IS NULL OR d.post_updated_at <> p.updated_at
			OR (d.error = '' AND $1 <> '' AND d.model <> $1)
			OR (d.error <> '' AND d.indexed_at < now() - make_interval(mins => least(360, power(2, d.attempts)::int))))
		ORDER BY d.post_id IS NOT NULL, p.updated_at DESC LIMIT $2`, model, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int64])
}

// AssistantDoc is the index entry of one post.
type AssistantDoc struct {
	PostID        int64     `db:"post_id"`
	SourceHash    string    `db:"source_hash"`
	PostUpdatedAt time.Time `db:"post_updated_at"`
	Model         string    `db:"model"`
	Chunks        int       `db:"chunks"`
	Error         string    `db:"error"`
	Attempts      int       `db:"attempts"`
	IndexedAt     time.Time `db:"indexed_at"`
}

func (s *Store) AssistantDoc(ctx context.Context, postID int64) (*AssistantDoc, error) {
	rows, err := s.DB.Query(ctx, `SELECT post_id, source_hash, post_updated_at, model, chunks, error, attempts, indexed_at
		FROM assistant_docs WHERE post_id = $1`, postID)
	if err != nil {
		return nil, err
	}
	d, err := pgx.CollectExactlyOneRow(rows, pgx.RowToAddrOfStructByName[AssistantDoc])
	return d, mapErr(err)
}

// AssistantEmbeddings returns the stored embeddings of a post's chunks made
// with v.Model, keyed by content hash, in pgvector's text format.
func (s *Store) AssistantEmbeddings(ctx context.Context, postID int64, v *AssistantVector) (map[string]string, error) {
	rows, err := s.DB.Query(ctx, `SELECT content_hash, embedding::text FROM assistant_chunks
		WHERE post_id = $1 AND model = $2 AND embedding IS NOT NULL`, postID, v.Model)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var h, vec string
		if err := rows.Scan(&h, &vec); err != nil {
			return nil, err
		}
		out[h] = vec
	}
	return out, rows.Err()
}

// AssistantChunk is one chunk to store. Vector is in pgvector's text format
// ("[0.1,0.2,…]"), empty when the chunk has no embedding.
type AssistantChunk struct {
	Heading string
	Content string
	Hash    string
	Vector  string
}

// AssistantIndexEntry is what ReplaceAssistantChunks stores for a post.
type AssistantIndexEntry struct {
	PostID        int64
	PostUpdatedAt time.Time
	Lang          string
	SourceHash    string
	Chunks        []AssistantChunk
	Model         string // embedding model of the vectors; "" when none
	Error         string // embedding failure: chunks are kept for full-text search
}

// ReplaceAssistantChunks swaps a post's chunks for new ones in one
// transaction. It re-checks under a row lock that the post is still
// eligible, so a post excluded or unpublished while it was being embedded
// is never written back; it then reports false. v is nil without an
// embedding column.
func (s *Store) ReplaceAssistantChunks(ctx context.Context, e AssistantIndexEntry, v *AssistantVector) (bool, error) {
	stored := false
	err := s.InTx(ctx, func(tx pgx.Tx) error {
		// Two indexers (the server and the CLI) never write one post at once.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1, ($2 % 2147483647)::int)`, assistantLockID, e.PostID); err != nil {
			return err
		}
		var eligible bool
		err := tx.QueryRow(ctx, `SELECT `+AssistantEligibleCond+` FROM posts p WHERE p.id = $1 FOR SHARE`, e.PostID).Scan(&eligible)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM assistant_chunks WHERE post_id = $1`, e.PostID); err != nil {
			return err
		}
		if !eligible {
			_, err := tx.Exec(ctx, `DELETE FROM assistant_docs WHERE post_id = $1`, e.PostID)
			return err
		}
		insert := `INSERT INTO assistant_chunks (post_id, lang, chunk_no, heading, content, content_hash, model) VALUES ($1, $2, $3, $4, $5, $6, $7)`
		if v != nil {
			insert = `INSERT INTO assistant_chunks (post_id, lang, chunk_no, heading, content, content_hash, model, embedding)
				VALUES ($1, $2, $3, $4, $5, $6, $7, nullif($8, '')::text::` + v.typ() + `)`
		}
		for i, c := range e.Chunks {
			model := ""
			if c.Vector != "" {
				model = e.Model
			}
			args := []any{e.PostID, e.Lang, i, c.Heading, c.Content, c.Hash, model}
			if v != nil {
				args = append(args, c.Vector)
			}
			if _, err := tx.Exec(ctx, insert, args...); err != nil {
				return err
			}
		}
		attempts := 0
		if e.Error != "" {
			attempts = 1
		}
		_, err = tx.Exec(ctx, `INSERT INTO assistant_docs (post_id, source_hash, post_updated_at, model, chunks, error, attempts, indexed_at)
			VALUES ($1, $2, $3, $4, $5, left($6, 500), $7, now())
			ON CONFLICT (post_id) DO UPDATE SET source_hash = EXCLUDED.source_hash, post_updated_at = EXCLUDED.post_updated_at,
				model = EXCLUDED.model, chunks = EXCLUDED.chunks, error = EXCLUDED.error, indexed_at = now(),
				attempts = CASE WHEN EXCLUDED.error = '' THEN 0
					WHEN assistant_docs.post_updated_at = EXCLUDED.post_updated_at THEN assistant_docs.attempts + 1 ELSE 1 END`,
			e.PostID, e.SourceHash, e.PostUpdatedAt, e.Model, len(e.Chunks), e.Error, attempts)
		stored = err == nil
		return err
	})
	return stored, err
}

// TouchAssistantDoc records that a post changed (status, date) without any
// change to what is indexed.
func (s *Store) TouchAssistantDoc(ctx context.Context, postID int64, updatedAt time.Time) error {
	_, err := s.DB.Exec(ctx, `UPDATE assistant_docs SET post_updated_at = $2, indexed_at = now() WHERE post_id = $1`, postID, updatedAt)
	return err
}

// ResetAssistantIndex makes every post pending. With embeddings, the
// stored vectors are discarded too; otherwise unchanged chunks keep theirs.
func (s *Store) ResetAssistantIndex(ctx context.Context, embeddings bool) error {
	return s.InTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM assistant_docs`); err != nil {
			return err
		}
		if !embeddings {
			return nil
		}
		_, err := tx.Exec(ctx, `UPDATE assistant_chunks SET model = '' WHERE model <> ''`)
		return err
	})
}

// AssistantStats summarizes the index for the status panel. Posts count
// translations separately.
type AssistantStats struct {
	Eligible int // public posts and pages the assistant may use
	Excluded int // public, but hidden from the assistant by their editor
	Indexed  int // indexed and up to date, with the current model
	Failed   int // indexed for full-text search, embedding failed
	Pending  int
	Chunks   int
	Embedded int // chunks with an embedding of the current model
}

func (s *Store) AssistantStats(ctx context.Context, model string) (AssistantStats, error) {
	var st AssistantStats
	err := s.DB.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM posts p WHERE `+AssistantEligibleCond+`)::int,
		(SELECT count(*) FROM posts p WHERE `+publicCond+` AND p.assistant_exclude)::int,
		(SELECT count(*) FROM assistant_docs d JOIN posts p ON p.id = d.post_id WHERE `+AssistantEligibleCond+`
			AND d.post_updated_at = p.updated_at AND ($1 = '' OR d.model = $1) AND d.error = '')::int,
		(SELECT count(*) FROM assistant_docs d JOIN posts p ON p.id = d.post_id WHERE `+AssistantEligibleCond+` AND d.error <> '')::int,
		(SELECT count(*) FROM assistant_chunks)::int,
		(SELECT count(*) FROM assistant_chunks WHERE model = $1 AND model <> '')::int`, model).
		Scan(&st.Eligible, &st.Excluded, &st.Indexed, &st.Failed, &st.Chunks, &st.Embedded)
	st.Pending = max(0, st.Eligible-st.Indexed-st.Failed)
	return st, err
}

// AssistantFailure is a post whose last indexing attempt failed.
type AssistantFailure struct {
	PostID    int64     `db:"post_id"`
	Title     string    `db:"title"`
	Error     string    `db:"error"`
	Attempts  int       `db:"attempts"`
	IndexedAt time.Time `db:"indexed_at"`
}

func (s *Store) AssistantFailures(ctx context.Context, limit int) ([]AssistantFailure, error) {
	return collect[AssistantFailure](s.DB.Query(ctx, `SELECT d.post_id, p.title, d.error, d.attempts, d.indexed_at
		FROM assistant_docs d JOIN posts p ON p.id = d.post_id WHERE d.error <> '' ORDER BY d.indexed_at DESC LIMIT $1`, limit))
}

// ---------------------------------------------------------------- search --

// AssistantHit is a chunk found by a search, with its post. Group is the
// translation group (the post id when it has no translations). Score is
// the full-text rank or the cosine similarity.
type AssistantHit struct {
	ChunkID     int64      `db:"chunk_id"`
	PostID      int64      `db:"post_id"`
	ChunkNo     int        `db:"chunk_no"`
	Lang        string     `db:"lang"`
	Heading     string     `db:"heading"`
	Content     string     `db:"content"`
	Type        string     `db:"type"`
	Slug        string     `db:"slug"`
	Title       string     `db:"title"`
	Group       int64      `db:"tgroup"`
	PublishedAt *time.Time `db:"published_at"`
	UpdatedAt   time.Time  `db:"updated_at"`
	Score       float64    `db:"score"`
}

const assistantHitCols = `c.id AS chunk_id, c.post_id, c.chunk_no, c.lang, c.heading, c.content,
	p.type, p.slug, p.title, coalesce(p.tgroup, p.id) AS tgroup, p.published_at, p.updated_at`

// tsLang maps a chunk language to its text search configuration, the same
// mapping as the generated column assistant_chunks.tsv.
const tsLang = `split_part(c.lang, '-', 1)`

// SearchAssistantText runs a full-text search over the chunks of eligible
// posts. query goes through websearch_to_tsquery once per configuration,
// so each chunk is matched with its own language's stemming; terms joined
// with "or" match any of them, ranked by ts_rank_cd.
func (s *Store) SearchAssistantText(ctx context.Context, query string, limit int) ([]AssistantHit, error) {
	return collect[AssistantHit](s.DB.Query(ctx, `WITH q AS (SELECT websearch_to_tsquery('spanish', $1) AS es,
			websearch_to_tsquery('english', $1) AS en, websearch_to_tsquery('simple', $1) AS si)
		SELECT `+assistantHitCols+`, ts_rank_cd(c.tsv, CASE `+tsLang+` WHEN 'es' THEN q.es WHEN 'en' THEN q.en ELSE q.si END, 32)::float8 AS score
		FROM q CROSS JOIN assistant_chunks c JOIN posts p ON p.id = c.post_id
		WHERE ((`+tsLang+` = 'es' AND c.tsv @@ q.es) OR (`+tsLang+` = 'en' AND c.tsv @@ q.en)
			OR (`+tsLang+` NOT IN ('es', 'en') AND c.tsv @@ q.si))
			AND `+AssistantEligibleCond+`
		ORDER BY score DESC, c.post_id, c.chunk_no LIMIT $2`, query, limit))
}

// SearchAssistantVector returns the chunks nearest to an embedding (text
// format) by cosine distance, through the HNSW index, keeping those of
// eligible posts within maxDistance.
func (s *Store) SearchAssistantVector(ctx context.Context, v *AssistantVector, vec string, maxDistance float64, limit int) ([]AssistantHit, error) {
	q := `$1::text::` + v.typ()
	return collect[AssistantHit](s.DB.Query(ctx, `SELECT `+assistantHitCols+`, (1 - c.dist)::float8 AS score
		FROM (SELECT c.*, c.embedding `+v.op()+` `+q+` AS dist FROM assistant_chunks c
			WHERE c.embedding IS NOT NULL AND c.model = $2 ORDER BY c.embedding `+v.op()+` `+q+` LIMIT $3) c
		JOIN posts p ON p.id = c.post_id
		WHERE `+AssistantEligibleCond+` AND c.dist <= $4
		ORDER BY c.dist`, vec, v.Model, limit, maxDistance))
}

// AssistantPostBySlug finds a post the assistant may cite (nil when there is
// none, or it is not eligible).
func (s *Store) AssistantPostBySlug(ctx context.Context, typ, lang, slug string) (*Post, error) {
	p, err := s.PostBySlug(ctx, typ, lang, slug)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil || !p.AssistantEligible(time.Now()) {
		return nil, err
	}
	return p, nil
}
