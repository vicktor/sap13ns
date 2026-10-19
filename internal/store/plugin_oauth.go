package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// PluginOAuth is the OAuth connection of a plugin to one of its providers.
// Tokens is sealed by the plugin host; the store never sees the tokens.
type PluginOAuth struct {
	PluginID         string     `db:"plugin_id"`
	Provider         string     `db:"provider"`
	Tokens           string     `db:"tokens"`
	AccountID        string     `db:"account_id"`
	AccountName      string     `db:"account_name"`
	Scopes           string     `db:"scopes"`
	ExpiresAt        *time.Time `db:"expires_at"`
	RefreshExpiresAt *time.Time `db:"refresh_expires_at"`
	Refreshable      bool       `db:"refreshable"`
	ConnectedBy      *int64     `db:"connected_by"`
	ConnectedByName  string     `db:"connected_by_name"`
	ConnectedAt      time.Time  `db:"connected_at"`
	RefreshedAt      *time.Time `db:"refreshed_at"`
	RefreshError     string     `db:"refresh_error"`
}

const pluginOAuthCols = `o.plugin_id, o.provider, o.tokens, o.account_id, o.account_name, o.scopes, o.expires_at,
	o.refresh_expires_at, o.refreshable, o.connected_by, coalesce(u.username, '') AS connected_by_name, o.connected_at,
	o.refreshed_at, o.refresh_error`

// PluginOAuthConnections lists the connections of a plugin ("" for every
// plugin).
func (s *Store) PluginOAuthConnections(ctx context.Context, pluginID string) ([]PluginOAuth, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+pluginOAuthCols+` FROM plugin_oauth o LEFT JOIN users u ON u.id = o.connected_by
		WHERE $1 = '' OR o.plugin_id = $1 ORDER BY o.plugin_id, o.provider`, pluginID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[PluginOAuth])
}

// PluginOAuthConnection returns one connection, or ErrNotFound.
func (s *Store) PluginOAuthConnection(ctx context.Context, pluginID, provider string) (*PluginOAuth, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+pluginOAuthCols+` FROM plugin_oauth o LEFT JOIN users u ON u.id = o.connected_by
		WHERE o.plugin_id = $1 AND o.provider = $2`, pluginID, provider)
	if err != nil {
		return nil, err
	}
	c, err := pgx.CollectExactlyOneRow(rows, pgx.RowToAddrOfStructByName[PluginOAuth])
	return c, mapErr(err)
}

// SavePluginOAuth stores a new connection, replacing the previous one.
func (s *Store) SavePluginOAuth(ctx context.Context, c PluginOAuth) error {
	_, err := s.DB.Exec(ctx, `INSERT INTO plugin_oauth (plugin_id, provider, tokens, account_id, account_name, scopes,
			expires_at, refresh_expires_at, refreshable, connected_by, connected_at, refreshed_at, refresh_error)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now(), NULL, '')
		ON CONFLICT (plugin_id, provider) DO UPDATE SET tokens = EXCLUDED.tokens, account_id = EXCLUDED.account_id,
			account_name = EXCLUDED.account_name, scopes = EXCLUDED.scopes, expires_at = EXCLUDED.expires_at,
			refresh_expires_at = EXCLUDED.refresh_expires_at, refreshable = EXCLUDED.refreshable,
			connected_by = EXCLUDED.connected_by, connected_at = now(), refreshed_at = NULL, refresh_error = ''`,
		c.PluginID, c.Provider, c.Tokens, c.AccountID, c.AccountName, c.Scopes, c.ExpiresAt, c.RefreshExpiresAt,
		c.Refreshable, c.ConnectedBy)
	return err
}

// RefreshPluginOAuth stores renewed tokens, only if the stored ones are
// still oldTokens: of two renewals at once, the first one wins and the
// other reports false.
func (s *Store) RefreshPluginOAuth(ctx context.Context, c PluginOAuth, oldTokens string) (bool, error) {
	tag, err := s.DB.Exec(ctx, `UPDATE plugin_oauth SET tokens = $3, expires_at = $4, refresh_expires_at = $5,
			refreshable = $6, scopes = CASE WHEN $7 = '' THEN scopes ELSE $7 END, refreshed_at = now(), refresh_error = ''
		WHERE plugin_id = $1 AND provider = $2 AND tokens = $8`,
		c.PluginID, c.Provider, c.Tokens, c.ExpiresAt, c.RefreshExpiresAt, c.Refreshable, c.Scopes, oldTokens)
	return err == nil && tag.RowsAffected() == 1, err
}

// FailPluginOAuthRefresh records why a renewal failed, unless the tokens
// changed meanwhile. notRefreshable stops further attempts (the provider
// refused the refresh token itself).
func (s *Store) FailPluginOAuthRefresh(ctx context.Context, pluginID, provider, oldTokens, msg string, notRefreshable bool) error {
	_, err := s.DB.Exec(ctx, `UPDATE plugin_oauth SET refresh_error = left($4, 300), refreshable = refreshable AND NOT $5
		WHERE plugin_id = $1 AND provider = $2 AND tokens = $3`, pluginID, provider, oldTokens, msg, notRefreshable)
	return err
}

// DeletePluginOAuth removes a connection. It reports whether there was one.
func (s *Store) DeletePluginOAuth(ctx context.Context, pluginID, provider string) (bool, error) {
	tag, err := s.DB.Exec(ctx, `DELETE FROM plugin_oauth WHERE plugin_id = $1 AND provider = $2`, pluginID, provider)
	return err == nil && tag.RowsAffected() > 0, err
}

// DeletePluginOAuthAll removes every connection of a plugin and its
// authorizations in progress.
func (s *Store) DeletePluginOAuthAll(ctx context.Context, pluginID string) error {
	return s.InTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM plugin_oauth WHERE plugin_id = $1`, pluginID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM plugin_oauth_states WHERE plugin_id = $1`, pluginID)
		return err
	})
}

// PluginOAuthState is an authorization in progress.
type PluginOAuthState struct {
	StateHash   []byte    `db:"state_hash"`
	PluginID    string    `db:"plugin_id"`
	Provider    string    `db:"provider"`
	SessionHash []byte    `db:"session_hash"`
	UserID      int64     `db:"user_id"`
	Verifier    string    `db:"verifier"` // sealed by the plugin host
	RedirectURI string    `db:"redirect_uri"`
	ExpiresAt   time.Time `db:"expires_at"`
}

// maxOAuthStatesPerUser bounds the authorizations a user can have in
// progress; starting one more drops the oldest.
const maxOAuthStatesPerUser = 10

// CreatePluginOAuthState stores an authorization in progress, after
// removing the expired ones and the oldest of the user's beyond the limit.
func (s *Store) CreatePluginOAuthState(ctx context.Context, st PluginOAuthState) error {
	return s.InTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM plugin_oauth_states WHERE expires_at < now()`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM plugin_oauth_states WHERE state_hash IN (
				SELECT state_hash FROM plugin_oauth_states WHERE user_id = $1 ORDER BY expires_at DESC OFFSET $2)`,
			st.UserID, maxOAuthStatesPerUser-1); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO plugin_oauth_states (state_hash, plugin_id, provider, session_hash, user_id,
			verifier, redirect_uri, expires_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			st.StateHash, st.PluginID, st.Provider, st.SessionHash, st.UserID, st.Verifier, st.RedirectURI, st.ExpiresAt)
		return err
	})
}

// TakePluginOAuthState removes the authorization in progress with this
// state hash and returns it: a state serves one callback. It returns
// ErrNotFound for an unknown, used or expired one.
func (s *Store) TakePluginOAuthState(ctx context.Context, stateHash []byte) (*PluginOAuthState, error) {
	rows, err := s.DB.Query(ctx, `DELETE FROM plugin_oauth_states WHERE state_hash = $1 RETURNING state_hash, plugin_id,
		provider, session_hash, user_id, verifier, redirect_uri, expires_at`, stateHash)
	if err != nil {
		return nil, err
	}
	st, err := pgx.CollectExactlyOneRow(rows, pgx.RowToAddrOfStructByName[PluginOAuthState])
	if err != nil {
		return nil, mapErr(err)
	}
	if !st.ExpiresAt.After(time.Now()) {
		return nil, ErrNotFound
	}
	return st, nil
}
