package store

import (
	"context"
	"encoding/json"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"
)

type PluginRecord struct {
	ID                 string            `db:"id"`
	Enabled            bool              `db:"enabled"`
	WasmSHA256         string            `db:"wasm_sha256"`
	GrantedPermissions []string          `db:"granted_permissions"`
	Settings           map[string]string `db:"settings"`
	ApprovedBy         *int64            `db:"approved_by"`
	CreatedAt          time.Time         `db:"created_at"`
	UpdatedAt          time.Time         `db:"updated_at"`
}

const pluginCols = `id, enabled, wasm_sha256, granted_permissions, settings, approved_by, created_at, updated_at`

func (s *Store) ListPluginRecords(ctx context.Context) (map[string]PluginRecord, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+pluginCols+` FROM plugins`)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByName[PluginRecord])
	if err != nil {
		return nil, err
	}
	out := make(map[string]PluginRecord, len(list))
	for _, p := range list {
		out[p.ID] = p
	}
	return out, nil
}

// ApprovePlugin enables a plugin pinned to a specific wasm hash and permission set.
func (s *Store) ApprovePlugin(ctx context.Context, id, sha string, perms []string, by int64) error {
	if perms == nil {
		perms = []string{}
	}
	_, err := s.DB.Exec(ctx, `INSERT INTO plugins (id, enabled, wasm_sha256, granted_permissions, approved_by)
		VALUES ($1, true, $2, $3, $4)
		ON CONFLICT (id) DO UPDATE SET enabled = true, wasm_sha256 = EXCLUDED.wasm_sha256,
			granted_permissions = EXCLUDED.granted_permissions, approved_by = EXCLUDED.approved_by, updated_at = now()`,
		id, sha, perms, by)
	return err
}

func (s *Store) DisablePlugin(ctx context.Context, id string) error {
	_, err := s.DB.Exec(ctx, `UPDATE plugins SET enabled = false, updated_at = now() WHERE id = $1`, id)
	return err
}

func (s *Store) SavePluginSettings(ctx context.Context, id string, settings map[string]string) error {
	raw, _ := json.Marshal(settings)
	tag, err := s.DB.Exec(ctx, `UPDATE plugins SET settings = $2, updated_at = now() WHERE id = $1`, id, raw)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// Plugin key-value storage, namespaced by plugin id.

func (s *Store) PluginKVGet(ctx context.Context, pluginID, key string) ([]byte, error) {
	var v []byte
	err := s.DB.QueryRow(ctx, `SELECT value FROM plugin_kv WHERE plugin_id = $1 AND key = $2`, pluginID, key).Scan(&v)
	return v, mapErr(err)
}

// PluginKVSet stores a value while enforcing a per-plugin quota (bytes and keys).
func (s *Store) PluginKVSet(ctx context.Context, pluginID, key string, value []byte, maxBytes int64, maxKeys int) (bool, error) {
	ok := false
	err := s.InTx(ctx, func(tx pgx.Tx) error {
		var used int64
		var keys int
		if err := tx.QueryRow(ctx, `SELECT coalesce(sum(length(value)), 0)::bigint, count(*)::int FROM plugin_kv WHERE plugin_id = $1 AND key <> $2`,
			pluginID, key).Scan(&used, &keys); err != nil {
			return err
		}
		if used+int64(len(value)) > maxBytes || keys+1 > maxKeys {
			return nil
		}
		_, err := tx.Exec(ctx, `INSERT INTO plugin_kv (plugin_id, key, value) VALUES ($1, $2, $3)
			ON CONFLICT (plugin_id, key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, pluginID, key, value)
		ok = err == nil
		return err
	})
	return ok, err
}

func (s *Store) PluginKVDelete(ctx context.Context, pluginID, key string) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM plugin_kv WHERE plugin_id = $1 AND key = $2`, pluginID, key)
	return err
}

type AuditEntry struct {
	ID       int64             `db:"id"`
	At       time.Time         `db:"at"`
	UserID   *int64            `db:"user_id"`
	Username string            `db:"username"`
	Action   string            `db:"action"`
	Target   string            `db:"target"`
	IP       netip.Addr        `db:"ip"`
	Details  map[string]string `db:"details"`
}

func (s *Store) Audit(ctx context.Context, userID int64, action, target string, ip netip.Addr, details map[string]string) error {
	if details == nil {
		details = map[string]string{}
	}
	raw, _ := json.Marshal(details)
	var uid any
	if userID > 0 {
		uid = userID
	}
	_, err := s.DB.Exec(ctx, `INSERT INTO audit_log (user_id, action, target, ip, details) VALUES ($1, $2, $3, $4, $5)`,
		uid, action, target, ipArg(ip), raw)
	return err
}

func (s *Store) ListAudit(ctx context.Context, action string, limit, offset int) ([]AuditEntry, int, error) {
	var w whereBuilder
	if action != "" {
		w.add(`a.action LIKE ?`, EscapeLike(action)+"%")
	}
	var total int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM audit_log a`+w.sql(), w.args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.DB.Query(ctx, `SELECT a.id, a.at, a.user_id, coalesce(u.username, '') AS username, a.action, a.target,
		coalesce(a.ip, '0.0.0.0'::inet) AS ip, a.details FROM audit_log a LEFT JOIN users u ON u.id = a.user_id`+w.sql()+
		` ORDER BY a.at DESC, a.id DESC LIMIT `+w.arg(limit)+` OFFSET `+w.arg(offset), w.args...)
	if err != nil {
		return nil, 0, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByName[AuditEntry])
	return list, total, err
}

// PruneAudit keeps the audit log bounded.
func (s *Store) PruneAudit(ctx context.Context, keepDays int) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM audit_log WHERE at < now() - make_interval(days => $1)`, keepDays)
	return err
}
