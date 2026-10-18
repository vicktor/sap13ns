package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Autosave is the latest unsaved editor state of one user for one post.
type Autosave struct {
	PostID  int64     `db:"post_id" json:"-"`
	UserID  int64     `db:"user_id" json:"-"`
	Title   string    `db:"title" json:"title"`
	Content string    `db:"content" json:"content"`
	Excerpt string    `db:"excerpt" json:"excerpt"`
	SavedAt time.Time `db:"saved_at" json:"saved_at"`
}

func (s *Store) SaveAutosave(ctx context.Context, a Autosave) (time.Time, error) {
	var at time.Time
	err := s.DB.QueryRow(ctx, `INSERT INTO post_autosaves (post_id, user_id, title, content, excerpt) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (post_id, user_id) DO UPDATE SET title = EXCLUDED.title, content = EXCLUDED.content,
			excerpt = EXCLUDED.excerpt, saved_at = now()
		RETURNING saved_at`, a.PostID, a.UserID, a.Title, a.Content, a.Excerpt).Scan(&at)
	return at, mapErr(err)
}

func (s *Store) GetAutosave(ctx context.Context, postID, userID int64) (*Autosave, error) {
	list, err := collect[Autosave](s.DB.Query(ctx, `SELECT post_id, user_id, title, content, excerpt, saved_at
		FROM post_autosaves WHERE post_id = $1 AND user_id = $2`, postID, userID))
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, ErrNotFound
	}
	return &list[0], nil
}

func (s *Store) DeleteAutosave(ctx context.Context, postID, userID int64) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM post_autosaves WHERE post_id = $1 AND user_id = $2`, postID, userID)
	return err
}

// LockHolder describes who is editing a post.
type LockHolder struct {
	UserID    int64
	Name      string
	ExpiresAt time.Time
}

// AcquireLock takes or renews the edit lock of a post for userID. It fails
// (returning the current holder) when another user holds a live lock,
// unless force is set.
func (s *Store) AcquireLock(ctx context.Context, postID, userID int64, ttl time.Duration, force bool) (*LockHolder, bool, error) {
	var got int64
	err := s.DB.QueryRow(ctx, `INSERT INTO post_locks (post_id, user_id, expires_at) VALUES ($1, $2, now() + $3::interval)
		ON CONFLICT (post_id) DO UPDATE SET user_id = EXCLUDED.user_id, expires_at = EXCLUDED.expires_at
			WHERE post_locks.user_id = EXCLUDED.user_id OR post_locks.expires_at < now() OR $4
		RETURNING user_id`, postID, userID, ttl.String(), force).Scan(&got)
	if err == nil {
		return nil, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, mapErr(err)
	}
	h, err := s.LockHolderOf(ctx, postID)
	if errors.Is(err, ErrNotFound) {
		return nil, true, nil // released in between
	}
	return h, false, err
}

// LockHolderOf returns the live lock of a post, or ErrNotFound.
func (s *Store) LockHolderOf(ctx context.Context, postID int64) (*LockHolder, error) {
	var h LockHolder
	err := s.DB.QueryRow(ctx, `SELECT l.user_id, coalesce(nullif(u.display_name, ''), u.username), l.expires_at
		FROM post_locks l JOIN users u ON u.id = l.user_id WHERE l.post_id = $1 AND l.expires_at > now()`, postID).
		Scan(&h.UserID, &h.Name, &h.ExpiresAt)
	if err != nil {
		return nil, mapErr(err)
	}
	return &h, nil
}

func (s *Store) ReleaseLock(ctx context.Context, postID, userID int64) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM post_locks WHERE post_id = $1 AND user_id = $2`, postID, userID)
	return err
}

// LastEditor returns the name of the user who wrote the latest revision.
func (s *Store) LastEditor(ctx context.Context, postID int64) (string, error) {
	var name string
	err := s.DB.QueryRow(ctx, `SELECT coalesce(nullif(u.display_name, ''), u.username) FROM post_revisions r
		JOIN users u ON u.id = r.author_id WHERE r.post_id = $1 ORDER BY r.created_at DESC, r.id DESC LIMIT 1`, postID).Scan(&name)
	return name, mapErr(err)
}
