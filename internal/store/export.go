package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// Queries of the content export (internal/export). They read in batches
// with keyset pagination, so an export of a large site never loads the
// whole site at once, and they select only what an export may contain:
// users come back without password hashes, second factors or sessions.

// ExportFilter selects the posts and pages of an export.
type ExportFilter struct {
	Types    []string // TypePost, TypePage
	Statuses []string // trashed posts are never exported
	AuthorID int64    // 0: every author
	// From and To bound coalesce(published_at, created_at); To is
	// exclusive. Nil leaves that side open.
	From, To *time.Time
}

func (f ExportFilter) where(w *whereBuilder) {
	w.add(`p.type = ANY(?)`, f.Types)
	w.add(`p.status = ANY(?)`, f.Statuses)
	w.add(`p.status <> 'trash'`)
	if f.AuthorID > 0 {
		w.add(`p.author_id = ?`, f.AuthorID)
	}
	if f.From != nil {
		w.add(`coalesce(p.published_at, p.created_at) >= ?`, *f.From)
	}
	if f.To != nil {
		w.add(`coalesce(p.published_at, p.created_at) < ?`, *f.To)
	}
}

// ExportPosts returns the next posts of f after afterID, by id, with their
// terms and featured images: at most limit posts, and no more than
// maxBytes of content unless a single post is larger.
func (s *Store) ExportPosts(ctx context.Context, f ExportFilter, afterID int64, limit int, maxBytes int64) ([]Post, error) {
	w := &whereBuilder{}
	f.where(w)
	w.add(`p.id > ?`, afterID)
	rows, err := s.DB.Query(ctx, `SELECT p.id, octet_length(p.content) FROM posts p`+w.sql()+` ORDER BY p.id LIMIT `+w.arg(limit), w.args...)
	if err != nil {
		return nil, err
	}
	var ids []int64
	var total int64
	err = forEachRow(rows, func(r pgx.Rows) (bool, error) {
		var id, size int64
		if err := r.Scan(&id, &size); err != nil {
			return false, err
		}
		if len(ids) > 0 && total+size > maxBytes {
			return false, nil
		}
		ids, total = append(ids, id), total+size
		return true, nil
	})
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	rows, err = s.DB.Query(ctx, postSelect+` WHERE p.id = ANY($1) ORDER BY p.id`, ids)
	if err != nil {
		return nil, err
	}
	posts, err := pgx.CollectRows(rows, pgx.RowToStructByName[Post])
	if err != nil {
		return nil, err
	}
	if err := s.attachTerms(ctx, posts); err != nil {
		return nil, err
	}
	return posts, s.attachMedia(ctx, posts)
}

// forEachRow calls fn for each row until it returns false or an error,
// and closes rows.
func forEachRow(rows pgx.Rows, fn func(pgx.Rows) (bool, error)) error {
	defer rows.Close()
	for rows.Next() {
		more, err := fn(rows)
		if err != nil {
			return err
		}
		if !more {
			return nil
		}
	}
	return rows.Err()
}

// CountExportPosts counts the posts and pages of f.
func (s *Store) CountExportPosts(ctx context.Context, f ExportFilter) (int, error) {
	w := &whereBuilder{}
	f.where(w)
	var n int
	err := s.DB.QueryRow(ctx, `SELECT count(*)::int FROM posts p`+w.sql(), w.args...).Scan(&n)
	return n, err
}

// MaxPostID is the highest post id, 0 without posts. Exports number media
// items after it, since WXR shares one id space between posts and files.
func (s *Store) MaxPostID(ctx context.Context) (int64, error) {
	var id int64
	err := s.DB.QueryRow(ctx, `SELECT coalesce(max(id), 0) FROM posts`).Scan(&id)
	return id, err
}

// ExportCommentCounts counts the comments with one of statuses of each
// post; posts without any are absent.
func (s *Store) ExportCommentCounts(ctx context.Context, postIDs []int64, statuses []string) (map[int64]int, error) {
	rows, err := s.DB.Query(ctx, `SELECT post_id, count(*)::int FROM comments WHERE post_id = ANY($1) AND status = ANY($2) GROUP BY post_id`,
		postIDs, statuses)
	if err != nil {
		return nil, err
	}
	out := map[int64]int{}
	err = forEachRow(rows, func(r pgx.Rows) (bool, error) {
		var id int64
		var n int
		err := r.Scan(&id, &n)
		out[id] = n
		return true, err
	})
	return out, err
}

// ExportComments returns the next comments of a post after afterID, by id.
func (s *Store) ExportComments(ctx context.Context, postID int64, statuses []string, afterID int64, limit int) ([]Comment, error) {
	rows, err := s.DB.Query(ctx, commentSelect+` WHERE c.post_id = $1 AND c.status = ANY($2) AND c.id > $3 ORDER BY c.id LIMIT $4`,
		postID, statuses, afterID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[Comment])
}

// ExportUser is what an export may say about a user.
type ExportUser struct {
	ID          int64  `db:"id"`
	Username    string `db:"username"`
	Email       string `db:"email"`
	DisplayName string `db:"display_name"`
}

// ExportAuthors returns the authors of the posts of f, by id.
func (s *Store) ExportAuthors(ctx context.Context, f ExportFilter) ([]ExportUser, error) {
	w := &whereBuilder{}
	f.where(w)
	return collect[ExportUser](s.DB.Query(ctx, `SELECT u.id, u.username, u.email, u.display_name FROM users u
		WHERE u.id IN (SELECT p.author_id FROM posts p`+w.sql()+`) ORDER BY u.id`, w.args...))
}

// ExportMedia returns the next files after afterID, by id: the whole
// library, or with featuredOf only the featured images of its posts.
func (s *Store) ExportMedia(ctx context.Context, featuredOf *ExportFilter, afterID int64, limit int) ([]Media, error) {
	w := &whereBuilder{}
	w.add(`m.id > ?`, afterID)
	if featuredOf != nil {
		pw := &whereBuilder{args: w.args}
		featuredOf.where(pw)
		w.args = pw.args
		w.conds = append(w.conds, `m.id IN (SELECT p.featured_media_id FROM posts p`+pw.sql()+`)`)
	}
	return collect[Media](s.DB.Query(ctx, `SELECT m.id, m.uploader_id, m.path, m.original_name, m.mime, m.size, m.width, m.height,
		m.alt, m.caption, m.variants, m.sha256, m.created_at FROM media m`+w.sql()+` ORDER BY m.id LIMIT `+w.arg(limit), w.args...))
}

// ExportTerms returns every category and tag, by id, without post counts.
func (s *Store) ExportTerms(ctx context.Context) ([]Term, error) {
	return collect[Term](s.DB.Query(ctx, `SELECT id, taxonomy, name, slug, description, parent_id, created_at, i18n, 0 AS count
		FROM terms ORDER BY id`))
}
