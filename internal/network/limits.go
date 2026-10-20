package network

// The limits of a site's plan (multisite plan, section 5): its storage,
// its custom domains, the sites per subscription or grant, and its
// plugins. A downgrade only blocks additions: what is over the new limits
// stays.
//
// Plugins: every site installs from the network's catalogue, the plugins
// the operator installed on the portal (not blocked by the scanner), and
// the official plugins in the binary, except those the operator hides
// (official_plugins_hidden). A
// plan with plugin_uploads also lets a site upload its own, which are held
// (review_required plus a plugin_reviews row) until a network admin
// approves that very build: its module, assets and pinned permissions
// (plugin.Build.Pin). Themes: a site uses the built-in themes and the
// network's themes directory (SAP13NS_THEMES_DIR of the network); a plan
// with theme_uploads also lets it upload its own into its directory
// ($SAP13NS_DATA_DIR/sites/<id>/themes). Those need no review: the
// validation of internal/theme refuses anything a theme cannot hold, and
// uploaded themes render within output and time limits. Their files count
// against the plan's storage like media.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"sap13ns/internal/db"
	"sap13ns/internal/media"
	"sap13ns/internal/plugin"
	"sap13ns/internal/siteapp"
	"sap13ns/internal/store"
	"sap13ns/internal/web"
)

// ------------------------------------------------------------- storage --

// ReserveStorage claims bytes of a site's plan storage, atomically, so
// concurrent uploads never overshoot: ErrLimit when they do not fit.
func (n *Network) ReserveStorage(ctx context.Context, siteID, bytes int64) error {
	if bytes <= 0 {
		return nil
	}
	tag, err := n.db.Exec(ctx, `UPDATE sites s SET storage_bytes = s.storage_bytes + $2
		FROM plans p WHERE s.id = $1 AND p.slug = s.plan AND s.storage_bytes + $2 <= p.storage_bytes`, siteID, bytes)
	if err == nil && tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: no room for %d bytes", ErrLimit, bytes)
	}
	return err
}

// ReleaseStorage gives bytes back to a site's storage.
func (n *Network) ReleaseStorage(ctx context.Context, siteID, bytes int64) error {
	if bytes <= 0 {
		return nil
	}
	_, err := n.db.Exec(ctx, `UPDATE sites SET storage_bytes = greatest(storage_bytes - $2, 0) WHERE id = $1`, siteID, bytes)
	return err
}

// StorageBytes is the space a site is counted to use.
func (n *Network) StorageBytes(ctx context.Context, siteID int64) (int64, error) {
	var b int64
	err := n.db.QueryRow(ctx, `SELECT storage_bytes FROM sites WHERE id = $1`, siteID).Scan(&b)
	return b, mapErr(err)
}

// --------------------------------------------------------------- hooks --

var _ web.SiteLimits = siteHooks{}

func (h siteHooks) Reserve(ctx context.Context, n int64) error {
	err := h.r.net.ReserveStorage(ctx, h.id, n)
	if errors.Is(err, ErrLimit) {
		return errors.Join(media.ErrQuota, err)
	}
	return err
}

func (h siteHooks) Release(ctx context.Context, n int64) {
	if err := h.r.net.ReleaseStorage(ctx, h.id, n); err != nil {
		// The daily measurement corrects it.
		h.r.log.Error("release storage", "site", h.id, "bytes", n, "err", err)
	}
}

func (h siteHooks) StorageQuota(ctx context.Context) (int64, error) {
	s, err := h.r.net.Site(ctx, h.id)
	if err != nil {
		return 0, err
	}
	p, err := h.r.net.Plan(ctx, s.Plan)
	if err != nil {
		return 0, err
	}
	return p.StorageBytes, nil
}

func (h siteHooks) PluginPolicy(ctx context.Context) (web.PluginPolicy, error) {
	s, err := h.r.net.Site(ctx, h.id)
	if err != nil {
		return web.PluginPolicy{}, err
	}
	p, err := h.r.net.Plan(ctx, s.Plan)
	if err != nil {
		return web.PluginPolicy{}, err
	}
	hidden, err := h.r.net.HiddenOfficialPlugins(ctx)
	if err != nil {
		return web.PluginPolicy{}, err
	}
	return web.PluginPolicy{Plan: p.Name, Uploads: p.PluginUploads, Hidden: hidden}, nil
}

func (h siteHooks) ThemePolicy(ctx context.Context) (web.ThemePolicy, error) {
	s, err := h.r.net.Site(ctx, h.id)
	if err != nil {
		return web.ThemePolicy{}, err
	}
	p, err := h.r.net.Plan(ctx, s.Plan)
	if err != nil {
		return web.ThemePolicy{}, err
	}
	return web.ThemePolicy{Plan: p.Name, Uploads: p.ThemeUploads}, nil
}

func (h siteHooks) NetworkAdmin(ctx context.Context, accountID int64) (bool, error) {
	return h.r.net.IsNetworkAdmin(ctx, accountID)
}

func (h siteHooks) RequestPluginReview(ctx context.Context, pluginID, sha, pin string) (bool, error) {
	return h.r.net.RequestPluginReview(ctx, h.id, pluginID, sha, pin)
}

func (h siteHooks) PluginCatalog(ctx context.Context) ([]web.CatalogPlugin, error) {
	var out []web.CatalogPlugin
	err := h.r.withPortal(ctx, func(app *siteapp.App) error {
		for _, p := range catalog(app.Plugins()) {
			out = append(out, web.CatalogPlugin{ID: p.ID, Name: p.Name, Version: p.Version, Description: p.Description})
		}
		return nil
	})
	return out, err
}

func (h siteHooks) CatalogPackage(ctx context.Context, id string) ([]byte, error) {
	var buf bytes.Buffer
	err := h.r.withPortal(ctx, func(app *siteapp.App) error {
		for _, p := range catalog(app.Plugins()) {
			if p.ID == id {
				_, err := plugin.Pack(p.Dir, &buf)
				return err
			}
		}
		return store.ErrNotFound
	})
	return buf.Bytes(), err
}

// catalog lists the plugins of the portal a site can install: those the
// scanner did not block, as they are on disk.
func catalog(h *plugin.Host) []*plugin.Plugin {
	var out []*plugin.Plugin
	for _, p := range h.List() {
		rep := p.Report
		if p.Status == plugin.StatusInvalid || p.SHA256 == "" || rep == nil || rep.Blocked() ||
			rep.WasmSHA256 != p.SHA256 || rep.AssetsSHA256 != p.AssetsSHA256 {
			continue
		}
		out = append(out, p)
	}
	return out
}

// withPortal runs fn on the portal, loading it if needed.
func (r *Registry) withPortal(ctx context.Context, fn func(*siteapp.App) error) error {
	t := r.table.Load()
	if t == nil || t.network == nil {
		return errNotServed
	}
	e, err := r.acquireFor(ctx, t.network, loadTimeout, false)
	if err != nil {
		return err
	}
	defer r.releaseFor(e, false)
	return fn(e.app)
}

// ---------------------------------------------------- official plugins --

// HiddenOfficialPlugins lists the official plugins the operator hides
// from the sites, by id.
func (n *Network) HiddenOfficialPlugins(ctx context.Context) ([]string, error) {
	rows, err := n.db.Query(ctx, `SELECT plugin_id FROM official_plugins_hidden ORDER BY plugin_id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// HideOfficialPlugin hides an official plugin from the sites, or offers
// it again. The copies sites installed stay.
func (n *Network) HideOfficialPlugin(ctx context.Context, id string, hidden bool, by int64) error {
	if !plugin.ValidID(id) {
		return fmt.Errorf("%w: plugin id %q", ErrInvalid, id)
	}
	if !hidden {
		_, err := n.db.Exec(ctx, `DELETE FROM official_plugins_hidden WHERE plugin_id = $1`, id)
		return err
	}
	var hider *int64
	if by > 0 {
		hider = &by
	}
	_, err := n.db.Exec(ctx, `INSERT INTO official_plugins_hidden (plugin_id, hidden_by) VALUES ($1, $2) ON CONFLICT (plugin_id) DO NOTHING`, id, hider)
	return err
}

func (h portalHooks) HiddenOfficialPlugins(ctx context.Context) ([]string, error) {
	return h.r.net.HiddenOfficialPlugins(ctx)
}

func (h portalHooks) HideOfficialPlugin(ctx context.Context, id string, hidden bool, by int64) error {
	return webErr(h.r.net.HideOfficialPlugin(ctx, id, hidden, by))
}

// ------------------------------------------------------ plugin reviews --

// RequestPluginReview queues a build of a plugin a site uploaded (its
// pin; sha is its module's) for the operator, replacing the request for
// any other build of it. It reports whether the operator approved this
// very build before; a build rejected before is asked about again.
func (n *Network) RequestPluginReview(ctx context.Context, siteID int64, pluginID, sha, pin string) (bool, error) {
	tx, err := n.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM plugin_reviews WHERE site_id = $1 AND plugin_id = $2 AND pin <> $3 AND verdict = ''`,
		siteID, pluginID, pin); err != nil {
		return false, err
	}
	var approved bool
	if err := tx.QueryRow(ctx, `INSERT INTO plugin_reviews (site_id, plugin_id, pin, wasm_sha256) VALUES ($1, $2, $3, $4)
		ON CONFLICT (site_id, plugin_id, pin) DO UPDATE SET
			requested_at = CASE WHEN plugin_reviews.verdict = 'approved' THEN plugin_reviews.requested_at ELSE now() END,
			reviewed_by = CASE WHEN plugin_reviews.verdict = 'approved' THEN plugin_reviews.reviewed_by END,
			reviewed_at = CASE WHEN plugin_reviews.verdict = 'approved' THEN plugin_reviews.reviewed_at END,
			verdict = CASE WHEN plugin_reviews.verdict = 'approved' THEN 'approved' ELSE '' END
		RETURNING verdict = 'approved'`, siteID, pluginID, pin, sha).Scan(&approved); err != nil {
		return false, err
	}
	return approved, tx.Commit(ctx)
}

// A PluginReview is a build of a plugin waiting for the operator, with
// what the site's scanner found and the permissions it asks for.
type PluginReview struct {
	SiteID      int64
	Slug, Host  string
	PluginID    string
	Pin         string // what an approval is for: see plugin.Build
	SHA256      string // of the module
	RequestedAt time.Time
	Version     string
	Verdict     string
	Findings    int
	Imports     []string
	Permissions []string
}

// PluginReviews lists the builds waiting for a review that their active
// sites still have, oldest first.
func (n *Network) PluginReviews(ctx context.Context) ([]PluginReview, error) {
	rows, err := n.db.Query(ctx, `SELECT r.site_id, s.slug, s.schema_name, coalesce(d.host, ''), r.plugin_id, r.pin, r.wasm_sha256, r.requested_at
		FROM plugin_reviews r JOIN sites s ON s.id = r.site_id
			LEFT JOIN domains d ON d.site_id = s.id AND d.is_primary
		WHERE r.verdict = '' AND s.kind = 'tenant' AND s.status = 'active'
		ORDER BY r.requested_at, r.site_id, r.plugin_id LIMIT 500`)
	if err != nil {
		return nil, err
	}
	type row struct {
		PluginReview
		schema string
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		return x, r.Scan(&x.SiteID, &x.Slug, &x.schema, &x.Host, &x.PluginID, &x.Pin, &x.SHA256, &x.RequestedAt)
	})
	if err != nil {
		return nil, err
	}
	records := map[int64]map[string]store.PluginRecord{}
	var out []PluginReview
	for _, x := range list {
		// The build on the site's disk, which an approval checks again.
		b, err := plugin.ReadBuild(filepath.Join(n.Dir(x.SiteID), "plugins"), x.PluginID)
		if errors.Is(err, os.ErrNotExist) || err == nil && b.Pin != x.Pin {
			// Replaced (from the catalogue) or uninstalled since: an
			// upload of it asks again.
			if _, err := n.db.Exec(ctx, `DELETE FROM plugin_reviews WHERE site_id = $1 AND plugin_id = $2 AND pin = $3 AND verdict = ''`,
				x.SiteID, x.PluginID, x.Pin); err != nil {
				return nil, err
			}
			continue
		} else if err != nil {
			n.log.Warn("read a plugin waiting for a review", "site", x.SiteID, "plugin", x.PluginID, "err", err)
			continue
		}
		recs, ok := records[x.SiteID]
		if !ok {
			h, err := db.Scope(n.pool, x.schema)
			if err != nil {
				return nil, err
			}
			if recs, err = store.New(h).ListPluginRecords(ctx); err != nil {
				return nil, err
			}
			records[x.SiteID] = recs
		}
		var rep plugin.Report
		if rec, ok := recs[x.PluginID]; ok && json.Unmarshal(rec.ScanReport, &rep) == nil && rep.WasmSHA256 == b.SHA256 {
			x.Verdict, x.Findings, x.Imports = rep.Verdict, len(rep.Findings), rep.Imports
		}
		x.Version, x.Permissions = b.Manifest.Version, b.Manifest.EffectivePermissions()
		out = append(out, x.PluginReview)
	}
	return out, nil
}

// PendingPluginReviews counts the builds waiting for a review on active
// sites (PluginReviews drops those the sites replaced).
func (n *Network) PendingPluginReviews(ctx context.Context) (int, error) {
	var c int
	err := n.db.QueryRow(ctx, `SELECT count(*) FROM plugin_reviews r JOIN sites s ON s.id = r.site_id
		WHERE r.verdict = '' AND s.kind = 'tenant' AND s.status = 'active'`).Scan(&c)
	return c, err
}

// ReviewPlugin records the operator's verdict on a build waiting for one
// (store.ErrNotFound otherwise). Approving first runs clear, which clears
// the review on the site when it still has that build; when it does not
// (false), the request is dropped and the answer is ErrState.
func (n *Network) ReviewPlugin(ctx context.Context, siteID int64, pluginID, pin string, approve bool, by int64,
	clear func(context.Context) (bool, error)) error {
	var pending bool
	if err := n.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM plugin_reviews r JOIN sites s ON s.id = r.site_id
		WHERE r.site_id = $1 AND r.plugin_id = $2 AND r.pin = $3 AND r.verdict = '' AND s.kind = 'tenant')`,
		siteID, pluginID, pin).Scan(&pending); err != nil {
		return err
	}
	if !pending {
		return store.ErrNotFound
	}
	verdict := "rejected"
	if approve {
		verdict = "approved"
		cleared, err := clear(ctx)
		if err != nil {
			return err
		}
		if !cleared {
			if _, err := n.db.Exec(ctx, `DELETE FROM plugin_reviews WHERE site_id = $1 AND plugin_id = $2 AND pin = $3 AND verdict = ''`,
				siteID, pluginID, pin); err != nil {
				return err
			}
			return fmt.Errorf("%w: site %d has another build of %s", ErrState, siteID, pluginID)
		}
	}
	var reviewer *int64
	if by > 0 {
		reviewer = &by
	}
	tag, err := n.db.Exec(ctx, `UPDATE plugin_reviews SET verdict = $4, reviewed_by = $5, reviewed_at = now()
		WHERE site_id = $1 AND plugin_id = $2 AND pin = $3 AND verdict = ''`, siteID, pluginID, pin, verdict, reviewer)
	if err == nil && tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	if err == nil {
		n.log.Info("plugin reviewed", "site", siteID, "plugin", pluginID, "pin", pin, "verdict", verdict, "by", by)
	}
	return err
}

var _ web.PortalPlugins = portalHooks{}

func (h portalHooks) PluginReviews(ctx context.Context) ([]web.NetPluginReview, error) {
	rs, err := h.r.net.PluginReviews(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]web.NetPluginReview, 0, len(rs))
	for _, x := range rs {
		v := web.NetPluginReview{SiteID: x.SiteID, Slug: x.Slug, Host: x.Host, PluginID: x.PluginID, Pin: x.Pin, SHA256: x.SHA256,
			Version: x.Version, Verdict: x.Verdict, Findings: x.Findings, Imports: x.Imports, Permissions: x.Permissions,
			RequestedAt: x.RequestedAt}
		if x.Host != "" {
			v.SiteURL = h.r.origin(x.Host) + "/sso/start?next=" + url.QueryEscape("/admin/plugins/"+x.PluginID)
		}
		out = append(out, v)
	}
	return out, nil
}

func (h portalHooks) PendingPluginReviews(ctx context.Context) (int, error) {
	return h.r.net.PendingPluginReviews(ctx)
}

func (h portalHooks) ReviewPlugin(ctx context.Context, siteID int64, pluginID, pin string, approve bool, by int64) error {
	// The site's own plugin host clears the review, under the lock its
	// installs take, when its build is still the one reviewed.
	clear := func(ctx context.Context) (cleared bool, err error) {
		verr := h.r.visit(ctx, siteID, true, func(e *entry) {
			cleared, err = e.app.Plugins().ClearReview(ctx, pluginID, pin)
		})
		if errors.Is(verr, errNotServed) {
			return false, nil // not active: not approved
		}
		return cleared, errors.Join(verr, err)
	}
	return webErr(h.r.net.ReviewPlugin(ctx, siteID, pluginID, pin, approve, by, clear))
}

// --------------------------------------------------------- the others --

// customDomainsLeft fails with ErrLimit when a site's plan has no room
// for another custom domain. tx holds the site's row lock.
func customDomainsLeft(ctx context.Context, tx pgx.Tx, siteID int64) error {
	var plan string
	var limit, have int
	if err := tx.QueryRow(ctx, `SELECT p.name, p.custom_domains, (SELECT count(*) FROM domains d WHERE d.site_id = s.id AND d.kind = 'custom')
		FROM sites s JOIN plans p ON p.slug = s.plan WHERE s.id = $1`, siteID).Scan(&plan, &limit, &have); err != nil {
		return err
	}
	if have >= limit {
		return fmt.Errorf("%w: the %s plan allows %s custom domains", ErrLimit, plan, strconv.Itoa(limit))
	}
	return nil
}
