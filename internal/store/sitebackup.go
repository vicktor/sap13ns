package store

// What the per-site backups of internal/sitebackup read and write beyond
// the export (export.go): the rows a restore puts back with their own ids,
// so that every reference between them (featured images, translation
// groups, parent pages, replies, the front page, the media of a design or
// a layout) holds as it was, and the whole restore in one transaction.

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

// txDB is a store handle on an open transaction. Begin opens a savepoint,
// so the store's own transactions (InTx) nest in it.
type txDB struct{ pgx.Tx }

func (t txDB) Ping(ctx context.Context) error { return t.Conn().Ping(ctx) }

// WithTx returns a store whose statements run in tx, which the caller
// commits or rolls back. On a network, tx comes from the site's scoped
// handle and stays on the site's schema.
func (s *Store) WithTx(tx pgx.Tx) *Store { return &Store{DB: txDB{tx}} }

// contentTables are the tables a restore replaces, children first. Users,
// sessions, passkeys, plugins, settings, the audit log, analytics and AI
// usage are left alone; the assistant's index is derived from the posts
// and is built again.
var contentTables = []string{
	"assistant_chunks", "assistant_docs", "post_locks", "post_autosaves", "post_revisions",
	"comments", "post_terms", "posts", "terms", "media", "menu_items", "widgets", "redirects",
	"theme_custom_revisions", "theme_custom",
}

// HasContent reports whether the site has any content a restore would
// replace: posts, terms, media, menus, widgets, redirects or a design.
func (s *Store) HasContent(ctx context.Context) (bool, error) {
	var any bool
	err := s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM posts) OR EXISTS (SELECT 1 FROM terms) OR EXISTS (SELECT 1 FROM media)
		OR EXISTS (SELECT 1 FROM menu_items) OR EXISTS (SELECT 1 FROM widgets) OR EXISTS (SELECT 1 FROM redirects)
		OR EXISTS (SELECT 1 FROM theme_custom)`).Scan(&any)
	return any, err
}

// WipeContent deletes the content tables (contentTables). Run it in the
// restore's transaction.
func (s *Store) WipeContent(ctx context.Context) error {
	for _, t := range contentTables {
		// t is a constant of this file.
		if _, err := s.DB.Exec(ctx, `DELETE FROM `+t); err != nil {
			return err
		}
	}
	return nil
}

// ResetIdentities moves the id sequence of every content table past its
// largest id, after rows were inserted with their own ids.
func (s *Store) ResetIdentities(ctx context.Context) error {
	for _, t := range []string{"posts", "post_revisions", "terms", "media", "comments", "menu_items", "widgets", "redirects", "theme_custom_revisions"} {
		// t is a constant; setval(…, 1, false) makes the next id 1.
		if _, err := s.DB.Exec(ctx, `SELECT setval(pg_get_serial_sequence('`+t+`', 'id'),
			coalesce((SELECT max(id) FROM `+t+`), 1), (SELECT max(id) FROM `+t+`) IS NOT NULL)`); err != nil {
			return err
		}
	}
	return nil
}

// RestoreTerm inserts a term with its id.
func (s *Store) RestoreTerm(ctx context.Context, id int64, in TermInput) error {
	_, err := s.DB.Exec(ctx, `INSERT INTO terms (id, taxonomy, name, slug, description, parent_id, i18n)
		OVERRIDING SYSTEM VALUE VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		id, in.Taxonomy, in.Name, in.Slug, in.Description, in.ParentID, termI18n(in.I18n))
	return mapErr(err)
}

// RestorePost inserts a post with its id, as CreatePost does (translation
// group, terms), and with its dates: updated is when it last changed
// (nil: now); it was created then too, or when published if earlier.
func (s *Store) RestorePost(ctx context.Context, id int64, in PostInput, updated *time.Time) error {
	return s.InTx(ctx, func(tx pgx.Tx) error {
		var group *int64
		if in.TranslationOf > 0 {
			var g int64
			if err := tx.QueryRow(ctx, `UPDATE posts SET tgroup = coalesce(tgroup, id) WHERE id = $1 RETURNING tgroup`,
				in.TranslationOf).Scan(&g); err != nil {
				return mapErr(err)
			}
			var taken bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM posts WHERE (tgroup = $1 OR id = $1) AND lang = $2)`,
				g, in.Lang).Scan(&taken); err != nil {
				return err
			}
			if taken {
				return ErrTranslationExists
			}
			group = &g
		}
		_, err := tx.Exec(ctx, `INSERT INTO posts (id, type, status, title, slug, lang, tgroup, excerpt, content, author_id, parent_id,
			menu_order, featured_media_id, comment_status, meta_description, published_at, assistant_exclude, created_at, updated_at)
			OVERRIDING SYSTEM VALUE
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17,
				least(coalesce($18::timestamptz, now()), coalesce($16, $18::timestamptz, now())), coalesce($18::timestamptz, now()))`,
			id, in.Type, in.Status, in.Title, in.Slug, in.Lang, group, in.Excerpt, in.Content, in.AuthorID, in.ParentID,
			in.MenuOrder, in.FeaturedMediaID, in.CommentStatus, in.MetaDescription, in.PublishedAt, in.AssistantExclude, updated)
		if err != nil {
			return mapErr(err)
		}
		return setTerms(ctx, tx, id, in.TermIDs)
	})
}

// RestoreComment inserts a comment with its id.
func (s *Store) RestoreComment(ctx context.Context, id int64, c NewComment) error {
	_, err := s.DB.Exec(ctx, `INSERT INTO comments (id, post_id, parent_id, user_id, author_name, author_email, author_url, content,
			status, ip, user_agent, created_at, held_by, held_reason)
		OVERRIDING SYSTEM VALUE VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, coalesce($12, now()), $13, $14)`,
		id, c.PostID, c.ParentID, c.UserID, c.AuthorName, c.AuthorEmail, c.AuthorURL, c.Content, c.Status, ipArg(c.IP), c.UserAgent,
		c.CreatedAt, c.HeldBy, c.HeldReason)
	return mapErr(err)
}

// RestoreMedia inserts a media item with its id and date.
func (s *Store) RestoreMedia(ctx context.Context, m *Media) error {
	variants := m.Variants
	if variants == nil {
		variants = map[string]MediaVariant{}
	}
	_, err := s.DB.Exec(ctx, `INSERT INTO media (id, uploader_id, path, original_name, mime, size, width, height, alt, caption,
			variants, sha256, created_at)
		OVERRIDING SYSTEM VALUE VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		m.ID, m.UploaderID, m.Path, m.OriginalName, m.MIME, m.Size, m.Width, m.Height, m.Alt, m.Caption, variants, m.SHA256, m.CreatedAt)
	return mapErr(err)
}

// AllMedia returns every media item, by id.
func (s *Store) AllMedia(ctx context.Context, afterID int64, limit int) ([]Media, error) {
	return s.ExportMedia(ctx, nil, afterID, limit)
}

// RestoreMenuItem inserts a menu item with its id.
func (s *Store) RestoreMenuItem(ctx context.Context, it MenuItem) error {
	_, err := s.DB.Exec(ctx, `INSERT INTO menu_items (id, location, lang, label, url, position, created_at)
		OVERRIDING SYSTEM VALUE VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		it.ID, it.Location, it.Lang, it.Label, it.URL, it.Position, it.CreatedAt)
	return mapErr(err)
}

// RestoreWidget inserts a widget with its id.
func (s *Store) RestoreWidget(ctx context.Context, w Widget) error {
	config := w.Config
	if config == nil {
		config = map[string]string{}
	}
	_, err := s.DB.Exec(ctx, `INSERT INTO widgets (id, area, lang, kind, title, config, position, created_at)
		OVERRIDING SYSTEM VALUE VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		w.ID, w.Area, w.Lang, w.Kind, w.Title, config, w.Position, w.CreatedAt)
	return mapErr(err)
}

// AllThemeCustom returns the customisations of every theme: designs and
// layouts, published and draft.
func (s *Store) AllThemeCustom(ctx context.Context) ([]ThemeCustom, error) {
	rows, err := s.DB.Query(ctx, `SELECT theme, kind, draft, published, draft_at, published_at, updated_by
		FROM theme_custom ORDER BY theme, kind`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ThemeCustom
	for rows.Next() {
		var c ThemeCustom
		if err := rows.Scan(&c.Theme, &c.Kind, &c.Draft, &c.Published, &c.DraftAt, &c.PublishedAt, &c.UpdatedBy); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ThemeCustomRevision is a published version of a theme's customisation.
type ThemeCustomRevision struct {
	ID        int64           `db:"id" json:"id"`
	Theme     string          `db:"theme" json:"theme"`
	Kind      string          `db:"kind" json:"kind"`
	Data      json.RawMessage `db:"data" json:"data"`
	CreatedAt time.Time       `db:"created_at" json:"created_at"`
	UserID    *int64          `db:"user_id" json:"user_id,omitempty"`
}

// AllThemeCustomRevisions returns the published versions of every theme.
func (s *Store) AllThemeCustomRevisions(ctx context.Context) ([]ThemeCustomRevision, error) {
	return collect[ThemeCustomRevision](s.DB.Query(ctx, `SELECT id, theme, kind, data, created_at, user_id
		FROM theme_custom_revisions ORDER BY id`))
}

// RestoreThemeCustom inserts a theme's customisation of one kind.
func (s *Store) RestoreThemeCustom(ctx context.Context, c ThemeCustom) error {
	published := c.Published
	if len(published) == 0 {
		published = json.RawMessage(`{}`)
	}
	var draft any
	if len(c.Draft) > 0 && string(c.Draft) != "null" {
		draft = c.Draft
	}
	_, err := s.DB.Exec(ctx, `INSERT INTO theme_custom (theme, kind, draft, published, draft_at, published_at, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, c.Theme, c.Kind, draft, published, c.DraftAt, c.PublishedAt, c.UpdatedBy)
	return mapErr(err)
}

// RestoreThemeCustomRevision inserts a published version with its id.
func (s *Store) RestoreThemeCustomRevision(ctx context.Context, r ThemeCustomRevision) error {
	_, err := s.DB.Exec(ctx, `INSERT INTO theme_custom_revisions (id, theme, kind, data, created_at, user_id)
		OVERRIDING SYSTEM VALUE VALUES ($1, $2, $3, $4, $5, $6)`, r.ID, r.Theme, r.Kind, r.Data, r.CreatedAt, r.UserID)
	return mapErr(err)
}

// AllThemeOptions returns the options of every theme, by theme slug.
func (s *Store) AllThemeOptions(ctx context.Context) (map[string]map[string]string, error) {
	rows, err := s.DB.Query(ctx, `SELECT substr(key, 7), value FROM settings WHERE key LIKE 'theme:%' ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]string{}
	for rows.Next() {
		var theme string
		var raw []byte
		if err := rows.Scan(&theme, &raw); err != nil {
			return nil, err
		}
		opts := map[string]string{}
		if json.Unmarshal(raw, &opts) == nil {
			out[theme] = opts
		}
	}
	return out, rows.Err()
}

// AllUsers returns every user, by id.
func (s *Store) AllUsers(ctx context.Context) ([]User, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+userCols+` FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[User])
}

// RestoredUser is a user a restore creates: without a password (they reset
// it) or, linked to a network account, signing in through the portal.
type RestoredUser struct {
	Username, Email, DisplayName, Bio, Website, Role, Status string
	NetworkAccountID                                         *int64
	NetworkOperator                                          bool
	CreatedAt                                                time.Time
}

// CreateRestoredUser creates a user of a restored backup.
func (s *Store) CreateRestoredUser(ctx context.Context, u RestoredUser) (int64, error) {
	hash := ""
	if u.NetworkAccountID != nil {
		hash = SSOPasswordHash
	}
	var id int64
	err := s.DB.QueryRow(ctx, `INSERT INTO users (username, email, display_name, bio, website, role, status, password_hash,
			network_account_id, network_operator, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING id`,
		u.Username, u.Email, u.DisplayName, u.Bio, u.Website, u.Role, u.Status, hash, u.NetworkAccountID, u.NetworkOperator,
		u.CreatedAt).Scan(&id)
	return id, mapErr(err)
}

// RestoreUserProfile puts back a user's name, biography and website, and,
// when role is not "", their role and status.
func (s *Store) RestoreUserProfile(ctx context.Context, id int64, displayName, bio, website, role, status string) error {
	_, err := s.DB.Exec(ctx, `UPDATE users SET display_name = $2, bio = $3, website = $4,
		role = coalesce(nullif($5, ''), role), status = coalesce(nullif($6, ''), status), updated_at = now() WHERE id = $1`,
		id, displayName, bio, website, role, status)
	return mapErr(err)
}

// RestoreRedirect inserts a redirect rule unless its source has one, with
// its hit count. It reports whether it added it.
func (s *Store) RestoreRedirect(ctx context.Context, r Redirect) (bool, error) {
	tag, err := s.DB.Exec(ctx, `INSERT INTO redirects (source, target, status, auto, hits, last_hit_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT (source) DO NOTHING`,
		r.Source, r.Target, r.Status, r.Auto, r.Hits, r.LastHitAt, r.CreatedAt)
	if err != nil {
		return false, mapErr(err)
	}
	return tag.RowsAffected() == 1, nil
}
