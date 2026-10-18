package store

import (
	"context"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"
)

type Session struct {
	TokenHash  []byte     `db:"token_hash"`
	UserID     int64      `db:"user_id"`
	CSRFToken  string     `db:"csrf_token"`
	MFAPending bool       `db:"mfa_pending"`
	CreatedAt  time.Time  `db:"created_at"`
	LastSeenAt time.Time  `db:"last_seen_at"`
	ExpiresAt  time.Time  `db:"expires_at"`
	IP         netip.Addr `db:"ip"`
	UserAgent  string     `db:"user_agent"`
}

const sessionCols = `token_hash, user_id, csrf_token, mfa_pending, created_at, last_seen_at, expires_at, coalesce(ip, '0.0.0.0'::inet) AS ip, user_agent`

func (s *Store) CreateSession(ctx context.Context, sess Session) error {
	_, err := s.DB.Exec(ctx, `INSERT INTO sessions (token_hash, user_id, csrf_token, mfa_pending, expires_at, ip, user_agent)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		sess.TokenHash, sess.UserID, sess.CSRFToken, sess.MFAPending, sess.ExpiresAt, ipArg(sess.IP), sess.UserAgent)
	return err
}

// SessionByHash returns an unexpired session.
func (s *Store) SessionByHash(ctx context.Context, hash []byte) (*Session, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+sessionCols+` FROM sessions WHERE token_hash = $1 AND expires_at > now()`, hash)
	if err != nil {
		return nil, err
	}
	sess, err := pgx.CollectExactlyOneRow(rows, pgx.RowToAddrOfStructByName[Session])
	return sess, mapErr(err)
}

func (s *Store) TouchSession(ctx context.Context, hash []byte) error {
	_, err := s.DB.Exec(ctx, `UPDATE sessions SET last_seen_at = now() WHERE token_hash = $1`, hash)
	return err
}

func (s *Store) CompleteMFA(ctx context.Context, hash []byte) error {
	_, err := s.DB.Exec(ctx, `UPDATE sessions SET mfa_pending = false WHERE token_hash = $1`, hash)
	return err
}

func (s *Store) DeleteSession(ctx context.Context, hash []byte) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, hash)
	return err
}

// DeleteUserSessions logs a user out everywhere except, optionally, one session.
func (s *Store) DeleteUserSessions(ctx context.Context, userID int64, except []byte) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1 AND ($2::bytea IS NULL OR token_hash <> $2)`, userID, except)
	return err
}

func (s *Store) ListUserSessions(ctx context.Context, userID int64) ([]Session, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+sessionCols+` FROM sessions WHERE user_id = $1 AND expires_at > now() ORDER BY last_seen_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[Session])
}

// PurgeExpired removes stale sessions and password reset tokens.
func (s *Store) PurgeExpired(ctx context.Context, idle time.Duration) (int64, error) {
	tag, err := s.DB.Exec(ctx, `DELETE FROM sessions WHERE expires_at <= now() OR last_seen_at < now() - make_interval(secs => $1)`, idle.Seconds())
	if err != nil {
		return 0, err
	}
	if _, err := s.DB.Exec(ctx, `DELETE FROM password_resets WHERE expires_at <= now() OR used_at IS NOT NULL`); err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
