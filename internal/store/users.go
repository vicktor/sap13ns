package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

type User struct {
	ID            int64      `db:"id"`
	Username      string     `db:"username"`
	Email         string     `db:"email"`
	DisplayName   string     `db:"display_name"`
	Bio           string     `db:"bio"`
	Website       string     `db:"website"`
	Role          string     `db:"role"`
	Status        string     `db:"status"`
	PasswordHash  string     `db:"password_hash"`
	TOTPSecret    []byte     `db:"totp_secret"`
	TOTPEnabled   bool       `db:"totp_enabled"`
	TOTPLastStep  int64      `db:"totp_last_step"`
	RecoveryCodes []string   `db:"recovery_codes"`
	FailedLogins  int        `db:"failed_logins"`
	LockedUntil   *time.Time `db:"locked_until"`
	CreatedAt     time.Time  `db:"created_at"`
	UpdatedAt     time.Time  `db:"updated_at"`
	LastLoginAt   *time.Time `db:"last_login_at"`
	PasskeyCount  int        `db:"passkey_count"`
	// NetworkAccountID links the user to an account of the network portal
	// (multisite only); nil is a local user.
	NetworkAccountID *int64 `db:"network_account_id"`
}

// SSOPasswordHash is the password hash of users created by single sign-on.
// It is not a PHC string, so auth.VerifyPassword never accepts it.
const SSOPasswordHash = "!sso"

// Name returns the public name of the user.
func (u *User) Name() string {
	if u.DisplayName != "" {
		return u.DisplayName
	}
	return u.Username
}

// SSOOnly reports whether the user can only sign in through the network
// portal: they have no password of their own to reset.
func (u *User) SSOOnly() bool { return u.PasswordHash == SSOPasswordHash }

const userCols = `id, username, email, display_name, bio, website, role, status, password_hash,
	totp_secret, totp_enabled, totp_last_step, recovery_codes, failed_logins, locked_until,
	created_at, updated_at, last_login_at,
	(SELECT count(*)::int FROM passkeys k WHERE k.user_id = users.id) AS passkey_count,
	network_account_id`

func (s *Store) userBy(ctx context.Context, where string, arg any) (*User, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+userCols+` FROM users WHERE `+where, arg)
	if err != nil {
		return nil, err
	}
	u, err := pgx.CollectExactlyOneRow(rows, pgx.RowToAddrOfStructByName[User])
	return u, mapErr(err)
}

func (s *Store) UserByID(ctx context.Context, id int64) (*User, error) {
	return s.userBy(ctx, `id = $1`, id)
}

func (s *Store) UserByUsername(ctx context.Context, username string) (*User, error) {
	return s.userBy(ctx, `username = $1`, username)
}

func (s *Store) UserByEmail(ctx context.Context, email string) (*User, error) {
	return s.userBy(ctx, `lower(email) = lower($1)`, email)
}

// UserByNetworkAccount returns the user linked to a network account.
func (s *Store) UserByNetworkAccount(ctx context.Context, accountID int64) (*User, error) {
	return s.userBy(ctx, `network_account_id = $1`, accountID)
}

// UserByLogin accepts either a username or an e-mail address.
func (s *Store) UserByLogin(ctx context.Context, login string) (*User, error) {
	return s.userBy(ctx, `username = lower($1) OR lower(email) = lower($1)`, login)
}

func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.DB.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

func (s *Store) CountActiveAdmins(ctx context.Context) (int, error) {
	var n int
	err := s.DB.QueryRow(ctx, `SELECT count(*) FROM users WHERE role = 'admin' AND status = 'active'`).Scan(&n)
	return n, err
}

type UserListQuery struct {
	Role   string
	Search string
	Limit  int
	Offset int
}

func (s *Store) ListUsers(ctx context.Context, q UserListQuery) ([]User, int, error) {
	var w whereBuilder
	if q.Role != "" {
		w.add(`role = ?`, q.Role)
	}
	if q.Search != "" {
		p := "%" + EscapeLike(q.Search) + "%"
		w.add(`(username ILIKE ? OR email ILIKE ? OR display_name ILIKE ?)`, p, p, p)
	}
	var total int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM users`+w.sql(), w.args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	sql := `SELECT ` + userCols + ` FROM users` + w.sql() + ` ORDER BY created_at ASC LIMIT ` + w.arg(q.Limit) + ` OFFSET ` + w.arg(q.Offset)
	rows, err := s.DB.Query(ctx, sql, w.args...)
	if err != nil {
		return nil, 0, err
	}
	users, err := pgx.CollectRows(rows, pgx.RowToStructByName[User])
	return users, total, err
}

type NewUser struct {
	Username     string
	Email        string
	DisplayName  string
	Role         string
	PasswordHash string
}

func (s *Store) CreateUser(ctx context.Context, n NewUser) (int64, error) {
	var id int64
	err := s.DB.QueryRow(ctx, `INSERT INTO users (username, email, display_name, role, password_hash)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		n.Username, n.Email, n.DisplayName, n.Role, n.PasswordHash).Scan(&id)
	return id, mapErr(err)
}

// NewSSOUser is a user created on a site at their first single sign-on.
type NewSSOUser struct {
	NetworkAccountID int64
	Username         string
	Email            string
	DisplayName      string
	Role             string
}

// CreateSSOUser creates a user linked to a network account, with no password
// of their own. A taken username or e-mail address returns ErrConflict
// naming the constraint (users_username_key, users_email_key), so the
// caller can pick another username; a local user with the same e-mail is
// never linked here.
func (s *Store) CreateSSOUser(ctx context.Context, n NewSSOUser) (int64, error) {
	var id int64
	err := s.DB.QueryRow(ctx, `INSERT INTO users (username, email, display_name, role, password_hash, network_account_id)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		n.Username, n.Email, n.DisplayName, n.Role, SSOPasswordHash, n.NetworkAccountID).Scan(&id)
	return id, mapErr(err)
}

// LinkNetworkAccount links a local user to a network account, which the
// user asked for while signed in. ErrNotFound means the user does not
// exist or is already linked; ErrConflict that the account is linked to
// another user.
func (s *Store) LinkNetworkAccount(ctx context.Context, userID, accountID int64) error {
	tag, err := s.DB.Exec(ctx, `UPDATE users SET network_account_id = $2, updated_at = now()
		WHERE id = $1 AND network_account_id IS NULL`, userID, accountID)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

type UserProfile struct {
	Email       string
	DisplayName string
	Bio         string
	Website     string
}

func (s *Store) UpdateUserProfile(ctx context.Context, id int64, p UserProfile) error {
	_, err := s.DB.Exec(ctx, `UPDATE users SET email = $2, display_name = $3, bio = $4, website = $5, updated_at = now() WHERE id = $1`,
		id, p.Email, p.DisplayName, p.Bio, p.Website)
	return mapErr(err)
}

func (s *Store) UpdateUserRoleStatus(ctx context.Context, id int64, role, status string) error {
	_, err := s.DB.Exec(ctx, `UPDATE users SET role = $2, status = $3, updated_at = now() WHERE id = $1`, id, role, status)
	return mapErr(err)
}

func (s *Store) SetPassword(ctx context.Context, id int64, hash string) error {
	_, err := s.DB.Exec(ctx, `UPDATE users SET password_hash = $2, failed_logins = 0, locked_until = NULL, updated_at = now() WHERE id = $1`, id, hash)
	return err
}

func (s *Store) RecordLoginFailure(ctx context.Context, id int64, lockAfter int, lockFor time.Duration) error {
	_, err := s.DB.Exec(ctx, `UPDATE users SET failed_logins = failed_logins + 1,
		locked_until = CASE WHEN failed_logins + 1 >= $2 THEN now() + make_interval(secs => $3) ELSE locked_until END
		WHERE id = $1`, id, lockAfter, lockFor.Seconds())
	return err
}

func (s *Store) RecordLoginSuccess(ctx context.Context, id int64) error {
	_, err := s.DB.Exec(ctx, `UPDATE users SET failed_logins = 0, locked_until = NULL, last_login_at = now() WHERE id = $1`, id)
	return err
}

// SetTOTP stores an encrypted secret. enabled=false keeps it pending until confirmed.
func (s *Store) SetTOTP(ctx context.Context, id int64, secret []byte, enabled bool, recovery []string) error {
	if recovery == nil {
		recovery = []string{}
	}
	_, err := s.DB.Exec(ctx, `UPDATE users SET totp_secret = $2, totp_enabled = $3, recovery_codes = $4, totp_last_step = 0, updated_at = now() WHERE id = $1`,
		id, secret, enabled, recovery)
	return err
}

func (s *Store) DisableTOTP(ctx context.Context, id int64) error {
	_, err := s.DB.Exec(ctx, `UPDATE users SET totp_secret = NULL, totp_enabled = false, recovery_codes = '{}', totp_last_step = 0, updated_at = now() WHERE id = $1`, id)
	return err
}

// UseTOTPStep records the last accepted time step; it fails if the step was
// already used, which prevents replaying a code inside its validity window.
func (s *Store) UseTOTPStep(ctx context.Context, id int64, step int64) (bool, error) {
	tag, err := s.DB.Exec(ctx, `UPDATE users SET totp_last_step = $2 WHERE id = $1 AND totp_last_step < $2`, id, step)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ConsumeRecoveryCode removes a recovery code hash atomically.
func (s *Store) ConsumeRecoveryCode(ctx context.Context, id int64, codeHash string) (bool, error) {
	tag, err := s.DB.Exec(ctx, `UPDATE users SET recovery_codes = array_remove(recovery_codes, $2) WHERE id = $1 AND $2 = ANY(recovery_codes)`, id, codeHash)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// DeleteUser removes a user and reassigns their content to another user.
func (s *Store) DeleteUser(ctx context.Context, id, reassignTo int64) error {
	return s.InTx(ctx, func(tx pgx.Tx) error {
		if reassignTo > 0 {
			if _, err := tx.Exec(ctx, `UPDATE posts SET author_id = $2 WHERE author_id = $1`, id, reassignTo); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
		return err
	})
}

type PasswordReset struct {
	UserID    int64
	ExpiresAt time.Time
}

func (s *Store) CreatePasswordReset(ctx context.Context, tokenHash []byte, userID int64, ttl time.Duration) error {
	// Only one active reset per user.
	return s.InTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM password_resets WHERE user_id = $1`, userID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO password_resets (token_hash, user_id, expires_at) VALUES ($1, $2, now() + make_interval(secs => $3))`,
			tokenHash, userID, ttl.Seconds())
		return err
	})
}

// ConsumePasswordReset marks a reset token used and returns its user.
func (s *Store) ConsumePasswordReset(ctx context.Context, tokenHash []byte) (int64, error) {
	var uid int64
	err := s.DB.QueryRow(ctx, `UPDATE password_resets SET used_at = now()
		WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now() RETURNING user_id`, tokenHash).Scan(&uid)
	return uid, mapErr(err)
}

// PasswordResetUser returns the user of a valid, unused reset token.
func (s *Store) PasswordResetUser(ctx context.Context, tokenHash []byte) (int64, error) {
	var uid int64
	err := s.DB.QueryRow(ctx, `SELECT user_id FROM password_resets WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()`, tokenHash).Scan(&uid)
	return uid, mapErr(err)
}

func (s *Store) PasswordResetValid(ctx context.Context, tokenHash []byte) (bool, error) {
	var ok bool
	err := s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM password_resets WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now())`, tokenHash).Scan(&ok)
	return ok, err
}
