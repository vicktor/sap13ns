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
	WasmSHA256         string            `db:"wasm_sha256"`   // pinned at approval
	AssetsSHA256       string            `db:"assets_sha256"` // pinned at approval
	GrantedPermissions []string          `db:"granted_permissions"`
	Settings           map[string]string `db:"settings"`
	ApprovedBy         *int64            `db:"approved_by"`
	Version            string            `db:"version"` // installed package version
	Source             string            `db:"source"`  // manual | upload | cli | catalog | official
	UploadedBy         *int64            `db:"uploaded_by"`
	UploadedByName     string            `db:"uploaded_by_name"`
	UploadedAt         *time.Time        `db:"uploaded_at"`
	ReviewRequired     bool              `db:"review_required"`
	ScanReport         []byte            `db:"scan_report"` // JSON plugin.Report
	ScannedAt          *time.Time        `db:"scanned_at"`
	CreatedAt          time.Time         `db:"created_at"`
	UpdatedAt          time.Time         `db:"updated_at"`
}

const pluginCols = `p.id, p.enabled, p.wasm_sha256, p.assets_sha256, p.granted_permissions, p.settings, p.approved_by,
	p.version, p.source, p.uploaded_by, coalesce(u.username, '') AS uploaded_by_name, p.uploaded_at, p.review_required,
	p.scan_report, p.scanned_at, p.created_at, p.updated_at`

func (s *Store) ListPluginRecords(ctx context.Context) (map[string]PluginRecord, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+pluginCols+` FROM plugins p LEFT JOIN users u ON u.id = p.uploaded_by`)
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

// ApprovePlugin enables a plugin pinned to a specific wasm hash, assets hash
// and permission set.
func (s *Store) ApprovePlugin(ctx context.Context, id, sha, assetsSHA string, perms []string, by int64) error {
	if perms == nil {
		perms = []string{}
	}
	_, err := s.DB.Exec(ctx, `INSERT INTO plugins (id, enabled, wasm_sha256, assets_sha256, granted_permissions, approved_by)
		VALUES ($1, true, $2, $3, $4, $5)
		ON CONFLICT (id) DO UPDATE SET enabled = true, wasm_sha256 = EXCLUDED.wasm_sha256, assets_sha256 = EXCLUDED.assets_sha256,
			granted_permissions = EXCLUDED.granted_permissions, approved_by = EXCLUDED.approved_by, updated_at = now()`,
		id, sha, assetsSHA, perms, nullID(by))
	return err
}

// PluginInstall describes an installed (or updated) package.
type PluginInstall struct {
	ID             string
	Version        string
	Source         string // upload | cli | catalog | official
	UploadedBy     int64  // 0 = command line
	ReviewRequired bool
	Reviewed       bool   // the network operator vouches for it: clears the review flag
	Report         []byte // JSON plugin.Report
}

// RecordPluginInstall creates the plugin row (disabled) or updates an
// existing one, keeping its approval, settings and key-value data. An
// update keeps a review the plugin waits for, unless it is Reviewed.
func (s *Store) RecordPluginInstall(ctx context.Context, in PluginInstall) error {
	_, err := s.DB.Exec(ctx, `INSERT INTO plugins (id, enabled, wasm_sha256, version, source, uploaded_by, uploaded_at, review_required, scan_report, scanned_at)
		VALUES ($1, false, '', $2, $3, $4, now(), $5 AND NOT $7, $6, now())
		ON CONFLICT (id) DO UPDATE SET version = EXCLUDED.version, source = EXCLUDED.source, uploaded_by = EXCLUDED.uploaded_by,
			uploaded_at = now(), review_required = NOT $7 AND (plugins.review_required OR EXCLUDED.review_required),
			scan_report = EXCLUDED.scan_report, scanned_at = now(), updated_at = now()`,
		in.ID, in.Version, in.Source, nullID(in.UploadedBy), in.ReviewRequired, in.Report, in.Reviewed)
	return err
}

// ResetRetainedPlugin starts a reinstall over the data an uninstall kept:
// the approval is dropped, the settings are replaced, and the OAuth
// connections, which were entrusted to the package that is gone, are
// deleted.
func (s *Store) ResetRetainedPlugin(ctx context.Context, id string, settings map[string]string) error {
	raw, _ := json.Marshal(settings)
	return s.InTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM plugin_oauth WHERE plugin_id = $1`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM plugin_oauth_states WHERE plugin_id = $1`, id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE plugins SET enabled = false, wasm_sha256 = '', assets_sha256 = '', granted_permissions = '{}',
			approved_by = NULL, settings = $2, updated_at = now() WHERE id = $1`, id, raw)
		return err
	})
}

// SavePluginReport stores the latest scanner report, creating the row for
// plugins that were copied into the plugins directory by hand.
func (s *Store) SavePluginReport(ctx context.Context, id, version string, report []byte) error {
	_, err := s.DB.Exec(ctx, `INSERT INTO plugins (id, enabled, wasm_sha256, version, scan_report, scanned_at)
		VALUES ($1, false, '', $2, $3, now())
		ON CONFLICT (id) DO UPDATE SET version = EXCLUDED.version, scan_report = EXCLUDED.scan_report, scanned_at = now(), updated_at = now()`,
		id, version, report)
	return err
}

// ClearPluginReview records that the network operator reviewed the plugin.
func (s *Store) ClearPluginReview(ctx context.Context, id string) error {
	_, err := s.DB.Exec(ctx, `UPDATE plugins SET review_required = false, updated_at = now() WHERE id = $1`, id)
	return err
}

// DeletePlugin removes the plugin row with its settings and key-value data.
func (s *Store) DeletePlugin(ctx context.Context, id string) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM plugins WHERE id = $1`, id)
	return err
}

// SiteAdminEmail is the address plugins may always e-mail: the oldest
// active administrator (normally the account created at installation).
func (s *Store) SiteAdminEmail(ctx context.Context) (string, error) {
	var email string
	err := s.DB.QueryRow(ctx, `SELECT email FROM users WHERE role = 'admin' AND status = 'active' ORDER BY created_at, id LIMIT 1`).Scan(&email)
	return email, mapErr(err)
}

func nullID(id int64) any {
	if id > 0 {
		return id
	}
	return nil
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
