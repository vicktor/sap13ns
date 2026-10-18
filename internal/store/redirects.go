package store

import (
	"context"
	"time"
)

type Redirect struct {
	ID        int64      `db:"id"`
	Source    string     `db:"source"`
	Target    string     `db:"target"`
	Status    int        `db:"status"`
	Auto      bool       `db:"auto"`
	Hits      int64      `db:"hits"`
	LastHitAt *time.Time `db:"last_hit_at"`
	CreatedAt time.Time  `db:"created_at"`
}

const redirectCols = `id, source, target, status, auto, hits, last_hit_at, created_at`

// Redirects lists every redirect, newest first.
func (s *Store) Redirects(ctx context.Context) ([]Redirect, error) {
	return collect[Redirect](s.DB.Query(ctx, `SELECT `+redirectCols+` FROM redirects ORDER BY created_at DESC, id DESC`))
}

func (s *Store) RedirectByID(ctx context.Context, id int64) (*Redirect, error) {
	rs, err := collect[Redirect](s.DB.Query(ctx, `SELECT `+redirectCols+` FROM redirects WHERE id = $1`, id))
	if err != nil {
		return nil, err
	}
	if len(rs) == 0 {
		return nil, ErrNotFound
	}
	return &rs[0], nil
}

// SaveRedirect creates or replaces the redirect for source.
func (s *Store) SaveRedirect(ctx context.Context, source, target string, status int, auto bool) error {
	_, err := s.DB.Exec(ctx, `INSERT INTO redirects (source, target, status, auto) VALUES ($1, $2, $3, $4)
		ON CONFLICT (source) DO UPDATE SET target = EXCLUDED.target, status = EXCLUDED.status, auto = EXCLUDED.auto`,
		source, target, status, auto)
	return mapErr(err)
}

func (s *Store) UpdateRedirect(ctx context.Context, id int64, source, target string, status int) error {
	tag, err := s.DB.Exec(ctx, `UPDATE redirects SET source = $2, target = $3, status = $4, auto = false WHERE id = $1`,
		id, source, target, status)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteRedirect(ctx context.Context, id int64) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM redirects WHERE id = $1`, id)
	return err
}

// MovePath records that a public URL moved from oldPath to newPath: it adds
// a permanent redirect, points older redirects at the new address so there
// are no chains, and drops any redirect that would shadow the new address.
func (s *Store) MovePath(ctx context.Context, oldPath, newPath string) error {
	if oldPath == newPath {
		return nil
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM redirects WHERE source = $1`, newPath); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE redirects SET target = $2 WHERE target = $1`, oldPath, newPath); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO redirects (source, target, status, auto) VALUES ($1, $2, 301, true)
		ON CONFLICT (source) DO UPDATE SET target = EXCLUDED.target, status = 301, auto = true`, oldPath, newPath); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RedirectHits adds hit counts collected in memory.
func (s *Store) RedirectHits(ctx context.Context, hits map[int64]int64) error {
	for id, n := range hits {
		if _, err := s.DB.Exec(ctx, `UPDATE redirects SET hits = hits + $2, last_hit_at = now() WHERE id = $1`, id, n); err != nil {
			return err
		}
	}
	return nil
}
