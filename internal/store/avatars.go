package store

// Profile photos and Gravatar copies (internal/web/avatars.go). The
// columns hold avatar keys (media.AvatarFile); the files are the caller's.

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// SetUserAvatar sets the photo a user uploaded ("" removes it) and
// returns the previous one, whose files the caller deletes.
func (s *Store) SetUserAvatar(ctx context.Context, id int64, key string) (string, error) {
	var old string
	err := s.InTx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT avatar FROM users WHERE id = $1 FOR UPDATE`, id).Scan(&old); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE users SET avatar = $2, updated_at = now() WHERE id = $1`, id, key)
		return err
	})
	return old, mapErr(err)
}

// SetUserGravatar records the lookup of the Gravatar of email for a user:
// key is the copy found, "" when Gravatar has none. It is recorded only
// while the user still has that address (ok); otherwise the caller
// deletes key's files. It returns the previous copy, whose files the
// caller deletes when it differs from key.
func (s *Store) SetUserGravatar(ctx context.Context, id int64, email, key string) (old string, ok bool, err error) {
	err = s.InTx(ctx, func(tx pgx.Tx) error {
		var cur string
		if err := tx.QueryRow(ctx, `SELECT email, gravatar FROM users WHERE id = $1 FOR UPDATE`, id).Scan(&cur, &old); err != nil {
			return err
		}
		if !strings.EqualFold(strings.TrimSpace(cur), strings.TrimSpace(email)) {
			old = ""
			return nil
		}
		ok = true
		_, err := tx.Exec(ctx, `UPDATE users SET gravatar = $2, gravatar_checked_at = now() WHERE id = $1`, id, key)
		return err
	})
	return old, ok, mapErr(err)
}

// GravatarRefresh is how often a user's Gravatar is looked up again.
const GravatarRefresh = 7 * 24 * time.Hour

// RetryGravatar records a lookup that failed (Gravatar unreachable, an
// image that would not decode): the user is due again at retryAt.
func (s *Store) RetryGravatar(ctx context.Context, id int64, retryAt time.Time) error {
	_, err := s.DB.Exec(ctx, `UPDATE users SET gravatar_checked_at = $2 WHERE id = $1`, id, retryAt.Add(-GravatarRefresh))
	return err
}

// ForgetGravatar removes a user's Gravatar copy and its check, so that it
// is looked up again (a photo removed, an address changed); it returns
// the copy, whose files the caller deletes.
func (s *Store) ForgetGravatar(ctx context.Context, id int64) (string, error) {
	var old string
	err := s.InTx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT gravatar FROM users WHERE id = $1 FOR UPDATE`, id).Scan(&old); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE users SET gravatar = '', gravatar_checked_at = NULL WHERE id = $1`, id)
		return err
	})
	return old, mapErr(err)
}

// A GravatarUser is a user whose Gravatar is looked up.
type GravatarUser struct {
	ID       int64  `db:"id"`
	Email    string `db:"email"`
	Gravatar string `db:"gravatar"`
}

// GravatarDue lists at most limit users without a photo of their own
// whose Gravatar was never looked up, or last before before: never
// first, then the oldest.
func (s *Store) GravatarDue(ctx context.Context, before time.Time, limit int) ([]GravatarUser, error) {
	rows, err := s.DB.Query(ctx, `SELECT id, email, gravatar FROM users
		WHERE avatar = '' AND (gravatar_checked_at IS NULL OR gravatar_checked_at < $1)
		ORDER BY gravatar_checked_at NULLS FIRST, id LIMIT $2`, before, limit)
	return collect[GravatarUser](rows, err)
}

// ClearGravatars forgets every user's Gravatar copy and lookup (the
// setting was switched off) and returns the copies, whose files the
// caller deletes.
func (s *Store) ClearGravatars(ctx context.Context) ([]string, error) {
	var keys []string
	err := s.InTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT gravatar FROM users WHERE gravatar <> '' ORDER BY id FOR UPDATE`)
		if err != nil {
			return err
		}
		if keys, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE users SET gravatar = '', gravatar_checked_at = NULL
			WHERE gravatar <> '' OR gravatar_checked_at IS NOT NULL`)
		return err
	})
	return keys, err
}

// A CommenterGravatar is the lookup of a commenter's Gravatar: Path is the
// copy found, "" when Gravatar has none.
type CommenterGravatar struct {
	Hash      string    `db:"hash"`
	Path      string    `db:"path"`
	CheckedAt time.Time `db:"checked_at"`
	UsedAt    time.Time `db:"used_at"`
}

// CommenterGravatars returns the lookups of the given e-mail hashes that
// the cache has.
func (s *Store) CommenterGravatars(ctx context.Context, hashes []string) (map[string]CommenterGravatar, error) {
	out := map[string]CommenterGravatar{}
	if len(hashes) == 0 {
		return out, nil
	}
	rows, err := s.DB.Query(ctx, `SELECT hash, path, checked_at, used_at FROM gravatar_cache WHERE hash = ANY($1)`, hashes)
	list, err := collect[CommenterGravatar](rows, err)
	for _, g := range list {
		out[g.Hash] = g
	}
	return out, err
}

// TouchCommenterGravatars marks lookups as shown now, at most once an
// hour each, which keeps them from being evicted first.
func (s *Store) TouchCommenterGravatars(ctx context.Context, hashes []string) error {
	if len(hashes) == 0 {
		return nil
	}
	_, err := s.DB.Exec(ctx, `UPDATE gravatar_cache SET used_at = now()
		WHERE hash = ANY($1) AND used_at < now() - interval '1 hour'`, hashes)
	return err
}

// PutCommenterGravatar records the lookup of a commenter's Gravatar (path
// "" when there is none) and returns the copy it replaces, whose files the
// caller deletes.
func (s *Store) PutCommenterGravatar(ctx context.Context, hash, path string, bytes int64) (string, error) {
	var old string
	err := s.InTx(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT path FROM gravatar_cache WHERE hash = $1 FOR UPDATE`, hash).Scan(&old)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO gravatar_cache (hash, path, bytes) VALUES ($1, $2, $3)
			ON CONFLICT (hash) DO UPDATE SET path = EXCLUDED.path, bytes = EXCLUDED.bytes, checked_at = now()`, hash, path, bytes)
		return err
	})
	return old, err
}

// EvictCommenterGravatars keeps the cache within maxRows lookups and
// maxBytes of copies, the most recently shown first, and drops the
// lookups not shown since unusedSince. It returns the copies removed,
// whose files the caller deletes.
func (s *Store) EvictCommenterGravatars(ctx context.Context, maxRows int, maxBytes int64, unusedSince time.Time) ([]string, error) {
	rows, err := s.DB.Query(ctx, `DELETE FROM gravatar_cache g WHERE g.used_at < $3 OR g.hash IN (
			SELECT hash FROM (SELECT hash, row_number() OVER w AS n, sum(bytes) OVER w AS total
				FROM gravatar_cache WINDOW w AS (ORDER BY used_at DESC, hash)) r
			WHERE r.n > $1 OR r.total > $2)
		RETURNING g.path`, maxRows, maxBytes, unusedSince)
	paths, err := collectStrings(rows, err)
	return nonEmpty(paths), err
}

// ClearCommenterGravatars empties the cache (the setting was switched off)
// and returns the copies, whose files the caller deletes.
func (s *Store) ClearCommenterGravatars(ctx context.Context) ([]string, error) {
	rows, err := s.DB.Query(ctx, `DELETE FROM gravatar_cache RETURNING path`)
	paths, err := collectStrings(rows, err)
	return nonEmpty(paths), err
}

// AvatarKeys returns every avatar the site records: users' photos and
// Gravatar copies, and commenters' copies.
func (s *Store) AvatarKeys(ctx context.Context) (map[string]bool, error) {
	rows, err := s.DB.Query(ctx, `SELECT avatar FROM users WHERE avatar <> ''
		UNION ALL SELECT gravatar FROM users WHERE gravatar <> ''
		UNION ALL SELECT path FROM gravatar_cache WHERE path <> ''`)
	keys, err := collectStrings(rows, err)
	out := make(map[string]bool, len(keys))
	for _, k := range keys {
		out[k] = true
	}
	return out, err
}

// ResetAvatars forgets every photo, Gravatar copy and lookup, in the
// transaction of a restore, which replaces the files they name.
func (s *Store) ResetAvatars(ctx context.Context) error {
	if _, err := s.DB.Exec(ctx, `UPDATE users SET avatar = '', gravatar = '', gravatar_checked_at = NULL
		WHERE avatar <> '' OR gravatar <> '' OR gravatar_checked_at IS NOT NULL`); err != nil {
		return err
	}
	_, err := s.DB.Exec(ctx, `DELETE FROM gravatar_cache`)
	return err
}

func collectStrings(rows pgx.Rows, err error) ([]string, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func nonEmpty(list []string) []string {
	out := list[:0]
	for _, v := range list {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}
