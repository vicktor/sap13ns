package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	TypePost = "post"
	TypePage = "page"

	StatusDraft     = "draft"
	StatusPending   = "pending"
	StatusPublished = "published"
	StatusPrivate   = "private"
	StatusTrash     = "trash"
)

type Post struct {
	ID              int64      `db:"id"`
	Type            string     `db:"type"`
	Status          string     `db:"status"`
	Title           string     `db:"title"`
	Slug            string     `db:"slug"`
	Lang            string     `db:"lang"`
	TGroup          *int64     `db:"tgroup"` // translation group: id of the original
	Excerpt         string     `db:"excerpt"`
	Content         string     `db:"content"`
	AuthorID        *int64     `db:"author_id"`
	ParentID        *int64     `db:"parent_id"`
	MenuOrder       int        `db:"menu_order"`
	FeaturedMediaID *int64     `db:"featured_media_id"`
	CommentStatus   string     `db:"comment_status"`
	MetaDescription string     `db:"meta_description"`
	PublishedAt     *time.Time `db:"published_at"`
	CreatedAt       time.Time  `db:"created_at"`
	UpdatedAt       time.Time  `db:"updated_at"`
	AuthorUsername  string     `db:"author_username"`
	AuthorName      string     `db:"author_name"`
	// The author's photo and Gravatar copy (User.Avatar, User.Gravatar).
	AuthorAvatar   string `db:"author_avatar"`
	AuthorGravatar string `db:"author_gravatar"`
	CommentCount   int    `db:"comment_count"`
	// AssistantExclude keeps the post out of the site assistant and MCP.
	AssistantExclude bool `db:"assistant_exclude"`

	// Loaded on demand.
	Terms    []Term `db:"-"`
	Featured *Media `db:"-"`
}

// IsPublic reports whether anonymous visitors can see the post right now.
func (p *Post) IsPublic(now time.Time) bool {
	return p.Status == StatusPublished && p.PublishedAt != nil && !p.PublishedAt.After(now)
}

// IsScheduled reports a published post whose date lies in the future.
func (p *Post) IsScheduled(now time.Time) bool {
	return p.Status == StatusPublished && p.PublishedAt != nil && p.PublishedAt.After(now)
}

func (p *Post) Categories() []Term { return p.termsOf("category") }
func (p *Post) Tags() []Term       { return p.termsOf("tag") }

func (p *Post) termsOf(tax string) []Term {
	var out []Term
	for _, t := range p.Terms {
		if t.Taxonomy == tax {
			out = append(out, t)
		}
	}
	return out
}

const postSelect = `SELECT p.id, p.type, p.status, p.title, p.slug, p.lang, p.tgroup, p.excerpt, p.content, p.author_id, p.parent_id,
	p.menu_order, p.featured_media_id, p.comment_status, p.meta_description, p.published_at, p.created_at, p.updated_at,
	p.assistant_exclude,
	coalesce(u.username, '') AS author_username,
	coalesce(nullif(u.display_name, ''), u.username, '') AS author_name,
	coalesce(u.avatar, '') AS author_avatar, coalesce(u.gravatar, '') AS author_gravatar,
	(SELECT count(*) FROM comments c WHERE c.post_id = p.id AND c.status = 'approved')::int AS comment_count
	FROM posts p LEFT JOIN users u ON u.id = p.author_id`

// publicCond restricts to content visible to anonymous visitors.
const publicCond = `p.status = 'published' AND p.published_at <= now()`

type PostQuery struct {
	Type       string
	Lang       string // empty = every language
	Statuses   []string
	PublicOnly bool
	AuthorID   int64
	TermID     int64
	TermIDs    []int64 // every one must be attached (plugins: category and tag)
	Search     string
	Order      string // "published" (default), "updated", "menu", "title", "rank"
	Limit      int
	Offset     int
	WithTerms  bool
	WithMedia  bool
	ExcludeIDs []int64
}

func (q *PostQuery) where() *whereBuilder {
	w := &whereBuilder{}
	if q.Type != "" {
		w.add(`p.type = ?`, q.Type)
	}
	if q.Lang != "" {
		w.add(`p.lang = ?`, q.Lang)
	}
	if q.PublicOnly {
		w.add(publicCond)
	} else if len(q.Statuses) > 0 {
		w.add(`p.status = ANY(?)`, q.Statuses)
	} else {
		w.add(`p.status <> 'trash'`)
	}
	if q.AuthorID > 0 {
		w.add(`p.author_id = ?`, q.AuthorID)
	}
	if q.TermID > 0 {
		w.add(`EXISTS (SELECT 1 FROM post_terms pt WHERE pt.post_id = p.id AND pt.term_id = ?)`, q.TermID)
	}
	for _, id := range q.TermIDs {
		w.add(`EXISTS (SELECT 1 FROM post_terms pt WHERE pt.post_id = p.id AND pt.term_id = ?)`, id)
	}
	if q.Search != "" {
		w.add(`p.search @@ websearch_to_tsquery('simple', `+searchFold("?")+`)`, q.Search)
	}
	if len(q.ExcludeIDs) > 0 {
		w.add(`NOT (p.id = ANY(?))`, q.ExcludeIDs)
	}
	return w
}

// searchFold folds the text of a search as the posts.search column folds
// the posts (migration 0018): lower case, accents off, so that "mediacion"
// and "mediación" find each other.
func searchFold(expr string) string {
	return `translate(lower(` + expr + `), 'áàâäãåéèêëíìîïóòôöõúùûüýÿñç', 'aaaaaaeeeeiiiiooooouuuuyync')`
}

func (s *Store) ListPosts(ctx context.Context, q PostQuery) ([]Post, int, error) {
	w := q.where()
	var total int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM posts p`+w.sql(), w.args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if q.Limit <= 0 {
		q.Limit = 10
	}
	order := ` ORDER BY coalesce(p.published_at, p.created_at) DESC, p.id DESC`
	switch q.Order {
	case "updated":
		order = ` ORDER BY p.updated_at DESC, p.id DESC`
	case "menu":
		order = ` ORDER BY p.menu_order ASC, p.title ASC`
	case "title":
		order = ` ORDER BY lower(p.title) ASC, p.id ASC`
	case "rank":
		if q.Search != "" {
			order = ` ORDER BY ts_rank(p.search, websearch_to_tsquery('simple', ` + searchFold(w.arg(q.Search)) + `)) DESC, p.published_at DESC`
		}
	}
	sql := postSelect + w.sql() + order + ` LIMIT ` + w.arg(q.Limit) + ` OFFSET ` + w.arg(q.Offset)
	rows, err := s.DB.Query(ctx, sql, w.args...)
	if err != nil {
		return nil, 0, err
	}
	posts, err := pgx.CollectRows(rows, pgx.RowToStructByName[Post])
	if err != nil {
		return nil, 0, err
	}
	if q.WithTerms {
		if err := s.attachTerms(ctx, posts); err != nil {
			return nil, 0, err
		}
	}
	if q.WithMedia {
		if err := s.attachMedia(ctx, posts); err != nil {
			return nil, 0, err
		}
	}
	return posts, total, nil
}

func (s *Store) onePost(ctx context.Context, where string, args ...any) (*Post, error) {
	rows, err := s.DB.Query(ctx, postSelect+` WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	p, err := pgx.CollectExactlyOneRow(rows, pgx.RowToAddrOfStructByName[Post])
	if err != nil {
		return nil, mapErr(err)
	}
	list := []Post{*p}
	if err := s.attachTerms(ctx, list); err != nil {
		return nil, err
	}
	if err := s.attachMedia(ctx, list); err != nil {
		return nil, err
	}
	return &list[0], nil
}

func (s *Store) PostByID(ctx context.Context, id int64) (*Post, error) {
	return s.onePost(ctx, `p.id = $1`, id)
}

// PostBySlug finds a non-trashed post of the given type and language.
func (s *Store) PostBySlug(ctx context.Context, typ, lang, slug string) (*Post, error) {
	return s.onePost(ctx, `p.type = $1 AND p.lang = $2 AND p.slug = $3 AND p.status <> 'trash'`, typ, lang, slug)
}

// Translation is a sibling of a post in another language.
type Translation struct {
	ID          int64
	Type        string
	Lang        string
	Slug        string
	Title       string
	Status      string
	PublishedAt *time.Time
}

// IsPublic reports whether anonymous visitors can see the translation now.
func (t *Translation) IsPublic(now time.Time) bool {
	return t.Status == StatusPublished && t.PublishedAt != nil && !t.PublishedAt.After(now)
}

// Translations lists every post in the same translation group, the post
// itself included, ordered by language.
func (s *Store) Translations(ctx context.Context, p *Post) ([]Translation, error) {
	group := p.ID
	if p.TGroup != nil {
		group = *p.TGroup
	}
	rows, err := s.DB.Query(ctx, `SELECT id, type, lang, slug, title, status, published_at FROM posts
		WHERE (tgroup = $1 OR id = $1 OR id = $2) AND status <> 'trash' ORDER BY lang`, group, p.ID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Translation, error) {
		var t Translation
		err := r.Scan(&t.ID, &t.Type, &t.Lang, &t.Slug, &t.Title, &t.Status, &t.PublishedAt)
		return t, err
	})
}

// TranslationsOf returns the translation siblings of many posts at once,
// keyed by post id; posts without translations are absent.
func (s *Store) TranslationsOf(ctx context.Context, ids []int64) (map[int64][]Translation, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.DB.Query(ctx, `SELECT src.id, t.id, t.type, t.lang, t.slug, t.title, t.status, t.published_at
		FROM posts src JOIN posts t ON t.tgroup = coalesce(src.tgroup, src.id) OR t.id = src.tgroup
		WHERE src.id = ANY($1) AND t.id <> src.id AND t.status <> 'trash' ORDER BY t.lang`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]Translation{}
	for rows.Next() {
		var src int64
		var t Translation
		if err := rows.Scan(&src, &t.ID, &t.Type, &t.Lang, &t.Slug, &t.Title, &t.Status, &t.PublishedAt); err != nil {
			return nil, err
		}
		out[src] = append(out[src], t)
	}
	return out, rows.Err()
}

func (s *Store) attachTerms(ctx context.Context, posts []Post) error {
	if len(posts) == 0 {
		return nil
	}
	ids := make([]int64, len(posts))
	idx := make(map[int64]int, len(posts))
	for i, p := range posts {
		ids[i] = p.ID
		idx[p.ID] = i
	}
	rows, err := s.DB.Query(ctx, `SELECT pt.post_id, t.id, t.taxonomy, t.name, t.slug, t.description, t.parent_id, t.created_at, t.i18n
		FROM post_terms pt JOIN terms t ON t.id = pt.term_id WHERE pt.post_id = ANY($1) ORDER BY t.name`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var pid int64
		var t Term
		if err := rows.Scan(&pid, &t.ID, &t.Taxonomy, &t.Name, &t.Slug, &t.Description, &t.ParentID, &t.CreatedAt, &t.I18n); err != nil {
			return err
		}
		i := idx[pid]
		posts[i].Terms = append(posts[i].Terms, t)
	}
	return rows.Err()
}

func (s *Store) attachMedia(ctx context.Context, posts []Post) error {
	var ids []int64
	for _, p := range posts {
		if p.FeaturedMediaID != nil {
			ids = append(ids, *p.FeaturedMediaID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	media, err := s.MediaByIDs(ctx, ids)
	if err != nil {
		return err
	}
	for i := range posts {
		if posts[i].FeaturedMediaID != nil {
			if m, ok := media[*posts[i].FeaturedMediaID]; ok {
				posts[i].Featured = m
			}
		}
	}
	return nil
}

type PostInput struct {
	Type            string
	Status          string
	Title           string
	Slug            string
	Lang            string
	TranslationOf   int64 // on create: id of the post this one translates
	Excerpt         string
	Content         string
	AuthorID        int64
	ParentID        *int64
	MenuOrder       int
	FeaturedMediaID *int64
	CommentStatus   string
	MetaDescription string
	PublishedAt     *time.Time
	TermIDs         []int64
	// AssistantExclude hides the post from the site assistant.
	AssistantExclude bool
}

// ErrTranslationExists means the translation group already has that language.
var ErrTranslationExists = errors.New("store: a translation in that language already exists")

func (s *Store) CreatePost(ctx context.Context, in PostInput) (int64, error) {
	var id int64
	err := s.InTx(ctx, func(tx pgx.Tx) error {
		var group *int64
		if in.TranslationOf > 0 {
			// The original becomes the group's anchor the first time it is translated.
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
		err := tx.QueryRow(ctx, `INSERT INTO posts (type, status, title, slug, lang, tgroup, excerpt, content, author_id, parent_id,
			menu_order, featured_media_id, comment_status, meta_description, published_at, assistant_exclude)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16) RETURNING id`,
			in.Type, in.Status, in.Title, in.Slug, in.Lang, group, in.Excerpt, in.Content, in.AuthorID, in.ParentID,
			in.MenuOrder, in.FeaturedMediaID, in.CommentStatus, in.MetaDescription, in.PublishedAt, in.AssistantExclude).Scan(&id)
		if err != nil {
			return mapErr(err)
		}
		return setTerms(ctx, tx, id, in.TermIDs)
	})
	return id, err
}

// UnlinkTranslation removes a post from its translation group.
func (s *Store) UnlinkTranslation(ctx context.Context, id int64) error {
	return s.InTx(ctx, func(tx pgx.Tx) error {
		var group *int64
		if err := tx.QueryRow(ctx, `SELECT tgroup FROM posts WHERE id = $1 FOR UPDATE`, id).Scan(&group); err != nil {
			return mapErr(err)
		}
		if group == nil {
			return nil
		}
		remaining := *group
		if *group == id {
			// The anchor leaves: the oldest remaining member takes its place.
			var next *int64
			if err := tx.QueryRow(ctx, `SELECT min(id) FROM posts WHERE tgroup = $1 AND id <> $1`, id).Scan(&next); err != nil {
				return err
			}
			if next == nil {
				_, err := tx.Exec(ctx, `UPDATE posts SET tgroup = NULL WHERE id = $1`, id)
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE posts SET tgroup = $2 WHERE tgroup = $1 AND id <> $1`, id, *next); err != nil {
				return err
			}
			remaining = *next
		}
		if _, err := tx.Exec(ctx, `UPDATE posts SET tgroup = NULL WHERE id = $1`, id); err != nil {
			return err
		}
		// A group of one is no group.
		_, err := tx.Exec(ctx, `UPDATE posts SET tgroup = NULL WHERE tgroup = $1
			AND (SELECT count(*) FROM posts q WHERE q.tgroup = $1) = 1`, remaining)
		return err
	})
}

// UpdatePost saves a new version, keeping the previous one as a revision
// when title, excerpt or content changed.
func (s *Store) UpdatePost(ctx context.Context, id int64, in PostInput, editorID int64, keepRevisions int) error {
	return s.InTx(ctx, func(tx pgx.Tx) error {
		var oldTitle, oldExcerpt, oldContent string
		if err := tx.QueryRow(ctx, `SELECT title, excerpt, content FROM posts WHERE id = $1 FOR UPDATE`, id).
			Scan(&oldTitle, &oldExcerpt, &oldContent); err != nil {
			return mapErr(err)
		}
		if oldTitle != in.Title || oldExcerpt != in.Excerpt || oldContent != in.Content {
			if _, err := tx.Exec(ctx, `INSERT INTO post_revisions (post_id, title, excerpt, content, author_id) VALUES ($1, $2, $3, $4, $5)`,
				id, oldTitle, oldExcerpt, oldContent, editorID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM post_revisions WHERE post_id = $1 AND id NOT IN (
				SELECT id FROM post_revisions WHERE post_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2)`, id, keepRevisions); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `UPDATE posts SET status = $2, title = $3, slug = $4, excerpt = $5, content = $6, author_id = $7,
			parent_id = $8, menu_order = $9, featured_media_id = $10, comment_status = $11, meta_description = $12,
			published_at = $13, lang = $14, assistant_exclude = $15, updated_at = now() WHERE id = $1`,
			id, in.Status, in.Title, in.Slug, in.Excerpt, in.Content, in.AuthorID, in.ParentID, in.MenuOrder,
			in.FeaturedMediaID, in.CommentStatus, in.MetaDescription, in.PublishedAt, in.Lang, in.AssistantExclude)
		if err != nil {
			return mapErr(err)
		}
		return setTerms(ctx, tx, id, in.TermIDs)
	})
}

func setTerms(ctx context.Context, tx pgx.Tx, postID int64, termIDs []int64) error {
	if _, err := tx.Exec(ctx, `DELETE FROM post_terms WHERE post_id = $1`, postID); err != nil {
		return err
	}
	if len(termIDs) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO post_terms (post_id, term_id)
		SELECT $1, t.id FROM terms t WHERE t.id = ANY($2) ON CONFLICT DO NOTHING`, postID, termIDs)
	return err
}

func (s *Store) SetPostStatus(ctx context.Context, id int64, status string) error {
	tag, err := s.DB.Exec(ctx, `UPDATE posts SET status = $2, updated_at = now() WHERE id = $1`, id, status)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// SetAnnouncePending marks whether a post still owes its post.published
// announcement: true for a scheduled post, false once announced or no
// longer published.
func (s *Store) SetAnnouncePending(ctx context.Context, id int64, pending bool) error {
	_, err := s.DB.Exec(ctx, `UPDATE posts SET announce_pending = $2 WHERE id = $1`, id, pending)
	return err
}

// ClaimAnnouncement clears the pending announcement of one post and reports
// whether there was one. Only one caller can claim it.
func (s *Store) ClaimAnnouncement(ctx context.Context, id int64) (bool, error) {
	tag, err := s.DB.Exec(ctx, `UPDATE posts SET announce_pending = false WHERE id = $1 AND announce_pending`, id)
	return tag.RowsAffected() > 0, err
}

// ClaimDueAnnouncements clears and returns the published posts whose
// scheduled date has arrived, oldest first.
func (s *Store) ClaimDueAnnouncements(ctx context.Context) ([]int64, error) {
	rows, err := s.DB.Query(ctx, `WITH due AS (
			UPDATE posts SET announce_pending = false
			WHERE announce_pending AND status = 'published' AND published_at <= now()
			RETURNING id, published_at)
		SELECT id FROM due ORDER BY published_at, id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int64])
}

// NextAnnouncement returns when the next scheduled post of the site is
// due, or nil if none is waiting.
func (s *Store) NextAnnouncement(ctx context.Context) (*time.Time, error) {
	var at *time.Time
	err := s.DB.QueryRow(ctx, `SELECT min(published_at) FROM posts WHERE announce_pending AND status = 'published'`).Scan(&at)
	return at, err
}

func (s *Store) DeletePost(ctx context.Context, id int64) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM posts WHERE id = $1`, id)
	return err
}

func (s *Store) EmptyTrash(ctx context.Context, typ string) (int64, error) {
	tag, err := s.DB.Exec(ctx, `DELETE FROM posts WHERE type = $1 AND status = 'trash'`, typ)
	return tag.RowsAffected(), err
}

// SlugExists checks whether a slug is taken by another post of the same type
// and language.
func (s *Store) SlugExists(ctx context.Context, typ, lang, slug string, exceptID int64) (bool, error) {
	var ok bool
	err := s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM posts WHERE type = $1 AND lang = $2 AND slug = $3 AND id <> $4)`,
		typ, lang, slug, exceptID).Scan(&ok)
	return ok, err
}

// SlugInUse reports whether a post of type typ has slug, in any language
// and status.
func (s *Store) SlugInUse(ctx context.Context, typ, slug string) (bool, error) {
	var ok bool
	err := s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM posts WHERE type = $1 AND slug = $2)`, typ, slug).Scan(&ok)
	return ok, err
}

type StatusCount struct {
	Status string
	Count  int
}

func (s *Store) CountPostsByStatus(ctx context.Context, typ string, authorID int64) (map[string]int, error) {
	rows, err := s.DB.Query(ctx, `SELECT status, count(*)::int FROM posts WHERE type = $1 AND ($2 = 0 OR author_id = $2) GROUP BY status`, typ, authorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[st] = n
	}
	return out, rows.Err()
}

// AdjacentPosts returns the previous (older) and next (newer) public posts.
func (s *Store) AdjacentPosts(ctx context.Context, p *Post) (prev, next *Post, err error) {
	if p.PublishedAt == nil {
		return nil, nil, nil
	}
	prev, err = s.onePost(ctx, `p.type = 'post' AND p.lang = $3 AND `+publicCond+` AND (p.published_at, p.id) < ($1, $2) ORDER BY p.published_at DESC, p.id DESC LIMIT 1`, *p.PublishedAt, p.ID, p.Lang)
	if err != nil && err != ErrNotFound {
		return nil, nil, err
	}
	next, err = s.onePost(ctx, `p.type = 'post' AND p.lang = $3 AND `+publicCond+` AND (p.published_at, p.id) > ($1, $2) ORDER BY p.published_at ASC, p.id ASC LIMIT 1`, *p.PublishedAt, p.ID, p.Lang)
	if err != nil && err != ErrNotFound {
		return nil, nil, err
	}
	return prev, next, nil
}

// LastPublicUpdate returns the most recent modification of public content,
// used for feed and sitemap caching headers.
func (s *Store) LastPublicUpdate(ctx context.Context) (time.Time, error) {
	var t *time.Time
	err := s.DB.QueryRow(ctx, `SELECT max(greatest(p.updated_at, p.published_at)) FROM posts p WHERE `+publicCond).Scan(&t)
	if t == nil {
		return time.Time{}, err
	}
	return *t, err
}

type SitemapEntry struct {
	ID        int64
	Type      string
	Lang      string
	Slug      string
	Group     int64 // translation group, 0 when the post has no translations
	UpdatedAt time.Time
}

func (s *Store) SitemapEntries(ctx context.Context, limit int) ([]SitemapEntry, error) {
	rows, err := s.DB.Query(ctx, `SELECT p.id, p.type, p.lang, p.slug, coalesce(p.tgroup, 0), p.updated_at FROM posts p
		WHERE `+publicCond+` ORDER BY p.updated_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (SitemapEntry, error) {
		var e SitemapEntry
		err := r.Scan(&e.ID, &e.Type, &e.Lang, &e.Slug, &e.Group, &e.UpdatedAt)
		return e, err
	})
}

type Revision struct {
	ID         int64     `db:"id"`
	PostID     int64     `db:"post_id"`
	Title      string    `db:"title"`
	Excerpt    string    `db:"excerpt"`
	Content    string    `db:"content"`
	AuthorName string    `db:"author_name"`
	CreatedAt  time.Time `db:"created_at"`
}

const revisionSelect = `SELECT r.id, r.post_id, r.title, r.excerpt, r.content,
	coalesce(nullif(u.display_name, ''), u.username, '') AS author_name, r.created_at
	FROM post_revisions r LEFT JOIN users u ON u.id = r.author_id`

func (s *Store) ListRevisions(ctx context.Context, postID int64) ([]Revision, error) {
	rows, err := s.DB.Query(ctx, revisionSelect+` WHERE r.post_id = $1 ORDER BY r.created_at DESC, r.id DESC`, postID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[Revision])
}

func (s *Store) RevisionByID(ctx context.Context, id int64) (*Revision, error) {
	rows, err := s.DB.Query(ctx, revisionSelect+` WHERE r.id = $1`, id)
	if err != nil {
		return nil, err
	}
	r, err := pgx.CollectExactlyOneRow(rows, pgx.RowToAddrOfStructByName[Revision])
	return r, mapErr(err)
}

type ArchiveMonth struct {
	Year  int
	Month int
	Count int
}

func (s *Store) ArchiveMonths(ctx context.Context, tz, lang string, limit int) ([]ArchiveMonth, error) {
	rows, err := s.DB.Query(ctx, `SELECT extract(year FROM p.published_at AT TIME ZONE $1)::int AS y,
		extract(month FROM p.published_at AT TIME ZONE $1)::int AS m, count(*)::int
		FROM posts p WHERE p.type = 'post' AND p.lang = $3 AND `+publicCond+` GROUP BY 1, 2 ORDER BY 1 DESC, 2 DESC LIMIT $2`, tz, limit, lang)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ArchiveMonth, error) {
		var a ArchiveMonth
		err := r.Scan(&a.Year, &a.Month, &a.Count)
		return a, err
	})
}

// PostsInMonth lists public posts of a calendar month in the site timezone.
func (s *Store) PostsInMonth(ctx context.Context, tz, lang string, year, month, limit, offset int) ([]Post, int, error) {
	cond := `p.type = 'post' AND p.lang = $4 AND ` + publicCond + ` AND extract(year FROM p.published_at AT TIME ZONE $1) = $2 AND extract(month FROM p.published_at AT TIME ZONE $1) = $3`
	var total int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM posts p WHERE `+cond, tz, year, month, lang).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.DB.Query(ctx, postSelect+` WHERE `+cond+` ORDER BY p.published_at DESC LIMIT $5 OFFSET $6`, tz, year, month, lang, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	posts, err := pgx.CollectRows(rows, pgx.RowToStructByName[Post])
	if err != nil {
		return nil, 0, err
	}
	if err := s.attachTerms(ctx, posts); err != nil {
		return nil, 0, err
	}
	return posts, total, s.attachMedia(ctx, posts)
}
