package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// Passkey is a WebAuthn credential. Data holds the go-webauthn Credential
// (public key, sign count, flags, attestation) as JSON; the store does not
// interpret it.
type Passkey struct {
	ID           int64      `db:"id"`
	UserID       int64      `db:"user_id"`
	CredentialID []byte     `db:"credential_id"`
	Name         string     `db:"name"`
	Data         []byte     `db:"data"`
	CreatedAt    time.Time  `db:"created_at"`
	LastUsedAt   *time.Time `db:"last_used_at"`
}

const passkeyCols = `id, user_id, credential_id, name, data, created_at, last_used_at`

// ListPasskeys returns the passkeys of a user, oldest first.
func (s *Store) ListPasskeys(ctx context.Context, userID int64) ([]Passkey, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+passkeyCols+` FROM passkeys WHERE user_id = $1 ORDER BY created_at, id`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[Passkey])
}

// PasskeyByCredentialID looks a credential up by its WebAuthn credential ID.
func (s *Store) PasskeyByCredentialID(ctx context.Context, credentialID []byte) (*Passkey, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+passkeyCols+` FROM passkeys WHERE credential_id = $1`, credentialID)
	if err != nil {
		return nil, err
	}
	p, err := pgx.CollectExactlyOneRow(rows, pgx.RowToAddrOfStructByName[Passkey])
	return p, mapErr(err)
}

// AddPasskey stores a new credential. It returns ErrConflict when the
// credential ID is already registered (to anyone).
func (s *Store) AddPasskey(ctx context.Context, userID int64, credentialID []byte, name string, data []byte) (int64, error) {
	var id int64
	err := s.DB.QueryRow(ctx, `INSERT INTO passkeys (user_id, credential_id, name, data) VALUES ($1, $2, $3, $4) RETURNING id`,
		userID, credentialID, name, data).Scan(&id)
	return id, mapErr(err)
}

// RenamePasskey changes the label of one of the user's passkeys.
func (s *Store) RenamePasskey(ctx context.Context, userID, id int64, name string) error {
	tag, err := s.DB.Exec(ctx, `UPDATE passkeys SET name = $3 WHERE id = $1 AND user_id = $2`, id, userID, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeletePasskey removes one of the user's passkeys and returns it.
func (s *Store) DeletePasskey(ctx context.Context, userID, id int64) (*Passkey, error) {
	rows, err := s.DB.Query(ctx, `DELETE FROM passkeys WHERE id = $1 AND user_id = $2 RETURNING `+passkeyCols, id, userID)
	if err != nil {
		return nil, err
	}
	p, err := pgx.CollectExactlyOneRow(rows, pgx.RowToAddrOfStructByName[Passkey])
	return p, mapErr(err)
}

// DeleteUserPasskeys removes every passkey of a user (administrator reset).
func (s *Store) DeleteUserPasskeys(ctx context.Context, userID int64) (int64, error) {
	tag, err := s.DB.Exec(ctx, `DELETE FROM passkeys WHERE user_id = $1`, userID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// RecordPasskeyUse saves the credential after a successful assertion (new
// sign count, backup state and clone warning) and stamps last_used_at.
func (s *Store) RecordPasskeyUse(ctx context.Context, id int64, data []byte) error {
	tag, err := s.DB.Exec(ctx, `UPDATE passkeys SET data = $2, last_used_at = now() WHERE id = $1`, id, data)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
