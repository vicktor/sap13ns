package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// UserToken is a personal access token for an AI agent (/admin/mcp).
type UserToken struct {
	ID         int64      `db:"id"`
	UserID     int64      `db:"user_id"`
	Name       string     `db:"name"`
	CreatedAt  time.Time  `db:"created_at"`
	LastUsedAt *time.Time `db:"last_used_at"`
	ExpiresAt  *time.Time `db:"expires_at"`
}

// MaxUserTokens is how many tokens a user can have at once.
const MaxUserTokens = 10

// ErrTooManyTokens: the user already has MaxUserTokens.
var ErrTooManyTokens = errors.New("too many tokens")

const userTokenCols = `id, user_id, name, created_at, last_used_at, expires_at`

// CreateUserToken stores a token by its hash. It fails with ErrTooManyTokens when
// the user already has MaxUserTokens.
func (s *Store) CreateUserToken(ctx context.Context, userID int64, name string, hash []byte, expires *time.Time) (*UserToken, error) {
	rows, err := s.DB.Query(ctx, `INSERT INTO user_tokens (user_id, name, token_hash, expires_at)
		SELECT $1, $2, $3, $4 WHERE (SELECT count(*) FROM user_tokens WHERE user_id = $1) < $5
		RETURNING `+userTokenCols, userID, name, hash, expires, MaxUserTokens)
	if err != nil {
		return nil, err
	}
	t, err := pgx.CollectExactlyOneRow(rows, pgx.RowToAddrOfStructByName[UserToken])
	if err != nil {
		if mapErr(err) == ErrNotFound {
			return nil, ErrTooManyTokens
		}
		return nil, err
	}
	return t, nil
}

// UserTokens lists a user's tokens, newest first.
func (s *Store) UserTokens(ctx context.Context, userID int64) ([]UserToken, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+userTokenCols+` FROM user_tokens WHERE user_id = $1 ORDER BY created_at DESC, id DESC`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[UserToken])
}

// DeleteUserToken revokes one of the user's tokens.
func (s *Store) DeleteUserToken(ctx context.Context, userID, id int64) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM user_tokens WHERE user_id = $1 AND id = $2`, userID, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// UserTokenByHash returns an unexpired token, noting its use (at most once
// a minute).
func (s *Store) UserTokenByHash(ctx context.Context, hash []byte) (*UserToken, error) {
	rows, err := s.DB.Query(ctx, `UPDATE user_tokens SET last_used_at = CASE
			WHEN last_used_at IS NULL OR last_used_at < now() - interval '1 minute' THEN now() ELSE last_used_at END
		WHERE token_hash = $1 AND (expires_at IS NULL OR expires_at > now())
		RETURNING `+userTokenCols, hash)
	if err != nil {
		return nil, err
	}
	t, err := pgx.CollectExactlyOneRow(rows, pgx.RowToAddrOfStructByName[UserToken])
	return t, mapErr(err)
}
