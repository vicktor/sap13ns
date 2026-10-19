package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

// Kinds of theme customisation.
const (
	CustomDesign = "design" // the visual customiser
	CustomLayout = "layout" // the block builder
)

// MaxCustomRevisions is how many published versions are kept per theme and kind.
const MaxCustomRevisions = 10

// ThemeCustom is one theme's customisation of one kind. Draft is nil when
// nothing is unpublished; Published is "{}" until the first publish.
type ThemeCustom struct {
	Theme       string
	Kind        string
	Draft       json.RawMessage
	Published   json.RawMessage
	DraftAt     *time.Time
	PublishedAt *time.Time
	UpdatedBy   *int64
}

// CustomRevision is a published version.
type CustomRevision struct {
	ID        int64           `db:"id"`
	Data      json.RawMessage `db:"data"`
	CreatedAt time.Time       `db:"created_at"`
	UserID    *int64          `db:"user_id"`
	UserName  string          `db:"user_name"`
}

// ThemeCustom returns the customisation of a theme, or an empty one.
func (s *Store) ThemeCustom(ctx context.Context, theme, kind string) (*ThemeCustom, error) {
	c := &ThemeCustom{Theme: theme, Kind: kind, Published: json.RawMessage(`{}`)}
	err := s.DB.QueryRow(ctx, `SELECT draft, published, draft_at, published_at, updated_by
		FROM theme_custom WHERE theme = $1 AND kind = $2`, theme, kind).
		Scan(&c.Draft, &c.Published, &c.DraftAt, &c.PublishedAt, &c.UpdatedBy)
	if err == pgx.ErrNoRows {
		return c, nil
	}
	return c, err
}

// SaveCustomDraft stores the draft (data must be valid JSON).
func (s *Store) SaveCustomDraft(ctx context.Context, theme, kind string, data []byte, userID int64) error {
	_, err := s.DB.Exec(ctx, `INSERT INTO theme_custom (theme, kind, draft, draft_at, updated_by)
		VALUES ($1, $2, $3, now(), nullif($4, 0))
		ON CONFLICT (theme, kind) DO UPDATE SET draft = excluded.draft, draft_at = now(), updated_by = excluded.updated_by`,
		theme, kind, json.RawMessage(data), userID)
	return mapErr(err)
}

// SaveCustomDraftTx is SaveCustomDraft inside a transaction (such as the fn
// of PublishCustom, to keep part of a draft unpublished).
func SaveCustomDraftTx(ctx context.Context, tx pgx.Tx, theme, kind string, data []byte, userID int64) error {
	_, err := tx.Exec(ctx, `UPDATE theme_custom SET draft = $3, draft_at = now(), updated_by = nullif($4, 0)
		WHERE theme = $1 AND kind = $2`, theme, kind, json.RawMessage(data), userID)
	return mapErr(err)
}

// DiscardCustomDraft drops the draft, keeping the published version.
func (s *Store) DiscardCustomDraft(ctx context.Context, theme, kind string) error {
	_, err := s.DB.Exec(ctx, `UPDATE theme_custom SET draft = NULL, draft_at = NULL WHERE theme = $1 AND kind = $2`, theme, kind)
	return err
}

// PublishCustom makes data the published version, clears the draft, records
// a revision and prunes revisions beyond MaxCustomRevisions. fn, when not
// nil, runs inside the same transaction (to save related settings).
func (s *Store) PublishCustom(ctx context.Context, theme, kind string, data []byte, userID int64, fn func(tx pgx.Tx) error) error {
	return s.InTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO theme_custom (theme, kind, published, published_at, updated_by)
			VALUES ($1, $2, $3, now(), nullif($4, 0))
			ON CONFLICT (theme, kind) DO UPDATE SET published = excluded.published, published_at = now(),
				draft = NULL, draft_at = NULL, updated_by = excluded.updated_by`,
			theme, kind, json.RawMessage(data), userID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO theme_custom_revisions (theme, kind, data, user_id) VALUES ($1, $2, $3, nullif($4, 0))`,
			theme, kind, json.RawMessage(data), userID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM theme_custom_revisions WHERE theme = $1 AND kind = $2 AND id NOT IN (
			SELECT id FROM theme_custom_revisions WHERE theme = $1 AND kind = $2 ORDER BY id DESC LIMIT $3)`,
			theme, kind, MaxCustomRevisions); err != nil {
			return err
		}
		if fn != nil {
			return fn(tx)
		}
		return nil
	})
}

// CustomRevisions lists the published versions, newest first.
func (s *Store) CustomRevisions(ctx context.Context, theme, kind string) ([]CustomRevision, error) {
	rows, err := s.DB.Query(ctx, `SELECT r.id, r.data, r.created_at, r.user_id,
			coalesce(nullif(u.display_name, ''), u.username, '') AS user_name
		FROM theme_custom_revisions r LEFT JOIN users u ON u.id = r.user_id
		WHERE r.theme = $1 AND r.kind = $2 ORDER BY r.id DESC LIMIT $3`, theme, kind, MaxCustomRevisions)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[CustomRevision])
}

// CustomRevision returns one published version of a theme.
func (s *Store) CustomRevision(ctx context.Context, theme, kind string, id int64) (*CustomRevision, error) {
	rows, err := s.DB.Query(ctx, `SELECT r.id, r.data, r.created_at, r.user_id,
			coalesce(nullif(u.display_name, ''), u.username, '') AS user_name
		FROM theme_custom_revisions r LEFT JOIN users u ON u.id = r.user_id
		WHERE r.theme = $1 AND r.kind = $2 AND r.id = $3`, theme, kind, id)
	if err != nil {
		return nil, err
	}
	rev, err := pgx.CollectExactlyOneRow(rows, pgx.RowToAddrOfStructByName[CustomRevision])
	return rev, mapErr(err)
}

// SaveThemeOptionsTx is SaveThemeOptions inside a transaction.
func SaveThemeOptionsTx(ctx context.Context, tx pgx.Tx, theme string, opts map[string]string) error {
	b, err := json.Marshal(opts)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO settings (key, value, updated_at) VALUES ($1, $2, now())
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = now()`, "theme:"+theme, json.RawMessage(b))
	return err
}
