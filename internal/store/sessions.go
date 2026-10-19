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
	AMR        string     `db:"amr"` // how it was authenticated: AMRLocal, AMRSSO, AMRSSOOperator or AMRSSOEmergency
}

// Authentication methods of a session (sessions.amr). Every one that
// starts with AMRSSO came through the network portal.
const (
	AMRLocal        = ""              // password, passkey or TOTP on this site
	AMRSSO          = "sso"           // single sign-on through the network portal
	AMRSSOOperator  = "sso-operator"  // a network admin opening the site as administrator, with the owner's consent
	AMRSSOEmergency = "sso-emergency" // a network admin's emergency access, without it
)

const sessionCols = `token_hash, user_id, csrf_token, mfa_pending, created_at, last_seen_at, expires_at, coalesce(ip, '0.0.0.0'::inet) AS ip, user_agent, amr`

func (s *Store) CreateSession(ctx context.Context, sess Session) error {
	_, err := s.DB.Exec(ctx, `INSERT INTO sessions (token_hash, user_id, csrf_token, mfa_pending, expires_at, ip, user_agent, amr)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		sess.TokenHash, sess.UserID, sess.CSRFToken, sess.MFAPending, sess.ExpiresAt, ipArg(sess.IP), sess.UserAgent, sess.AMR)
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

// DeleteNetworkSessions deletes the sessions of the user of a network
// account (operator: its operator user) that were authenticated with amr,
// and returns how many there were. The network ends one kind of access
// with it, a consented operator's or an emergency one, and leaves the
// other.
func (s *Store) DeleteNetworkSessions(ctx context.Context, accountID int64, operator bool, amr string) (int64, error) {
	tag, err := s.DB.Exec(ctx, `DELETE FROM sessions WHERE amr = $3 AND user_id IN
		(SELECT id FROM users WHERE network_account_id = $1 AND network_operator = $2)`, accountID, operator, amr)
	return tag.RowsAffected(), err
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
