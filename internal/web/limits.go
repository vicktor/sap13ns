package web

// The limits of a site of a network under its plan (internal/network):
// its storage, and how plugins reach it. Every plan installs from the
// network's catalogue, the plugins the operator installed on the portal;
// a plan with plugin uploads also lets the site upload its own, which the
// network operator reviews before they can run. A single-site install and
// the portal have no limits.

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"sap13ns/internal/media"
	"sap13ns/internal/plugin"
	"sap13ns/internal/store"
)

// SiteLimits are what the plan of a site of a network allows.
type SiteLimits interface {
	// The storage of the plan: Reserve fails with media.ErrQuota when the
	// bytes do not fit.
	media.Quota
	// StorageQuota is the storage of the plan, in bytes. A background
	// export larger than it is refused (Tools → Export).
	StorageQuota(ctx context.Context) (int64, error)
	// PluginPolicy is what the plan allows now: a downgrade applies at
	// once, to additions only.
	PluginPolicy(ctx context.Context) (PluginPolicy, error)
	// RequestPluginReview queues an uploaded build for the network
	// operator: pin is its plugin.InstallResult.Pin (module, assets and
	// pinned permissions), sha its module's SHA-256. It reports whether
	// the operator already approved that very build.
	RequestPluginReview(ctx context.Context, pluginID, sha, pin string) (bool, error)
	// PluginCatalog lists the plugins the network offers its sites;
	// CatalogPackage packs one of them (store.ErrNotFound for any other).
	PluginCatalog(ctx context.Context) ([]CatalogPlugin, error)
	CatalogPackage(ctx context.Context, id string) ([]byte, error)
}

// PluginPolicy is the plugin policy of a site's plan.
type PluginPolicy struct {
	Plan    string // its name
	Uploads bool   // the site may upload its own plugins, held for the operator's review
}

// A CatalogPlugin is a plugin of the network's catalogue.
type CatalogPlugin struct {
	ID, Name, Version, Description string
	Installed                      string // the version the site has from the catalogue, if any
}

// storageQuota is the quota of a site, or nil for none.
func storageQuota(l SiteLimits) media.Quota {
	if l == nil {
		return nil
	}
	return l
}

// tenantReview queues an upload of a site of a network for the
// operator's review: it stays held unless the operator approved that very
// build before.
func (s *Server) tenantReview(ctx context.Context, res *plugin.InstallResult) (held bool, err error) {
	approved, err := s.limits.RequestPluginReview(ctx, res.ID, res.Report.WasmSHA256, res.Pin)
	if err != nil || !approved {
		return true, err
	}
	cleared, err := s.plugins.ClearReview(ctx, res.ID, res.Pin)
	return !cleared, err
}

var catalogIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// adminPluginCatalogInstall installs (or updates) a plugin from the
// network's catalogue. It arrives disabled, like any other: the site's
// administrator reviews its permissions and switches it on.
func (s *Server) adminPluginCatalogInstall(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !catalogIDRe.MatchString(id) {
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return
	}
	pkg, err := s.limits.CatalogPackage(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return
	} else if err != nil {
		s.log.Error("pack a catalogue plugin", "plugin", id, "err", err)
		s.redirect(w, r, "/admin/plugins", "error", s.t("The network's catalogue is not available. Try again later."))
		return
	}
	// The catalogue's build needs no review, and clears a review an
	// earlier upload of the same plugin waited for.
	res, err := s.plugins.Install(r.Context(), bytes.NewReader(pkg), int64(len(pkg)), plugin.InstallOptions{UserID: uid(r),
		Source: "catalog", AllowDowngrade: true, Reviewed: true})
	switch {
	case errors.Is(err, media.ErrQuota):
		s.adminError(w, r, http.StatusRequestEntityTooLarge, s.storageFull())
		return
	case errors.Is(err, plugin.ErrRefused):
		s.redirect(w, r, "/admin/plugins", "error", s.t("The scanner found critical problems; this plugin cannot be enabled."))
		return
	case err != nil:
		s.log.Error("install a catalogue plugin", "plugin", id, "err", err)
		s.redirect(w, r, "/admin/plugins", "error", s.t("The plugin could not be installed: %s", err.Error()))
		return
	}
	if res.ID != id {
		s.log.Error("a catalogue package installed another plugin", "plugin", id, "installed", res.ID)
	}
	s.pluginsChanged()
	s.audit(r, uid(r), "plugin.installed", res.ID, map[string]string{"version": res.Version, "sha256": res.Report.WasmSHA256,
		"source": "catalog", "previous": res.Previous})
	s.redirect(w, r, "/admin/plugins/"+res.ID, "success", s.t("Plugin installed. It is disabled: review the scan report and the permissions, then switch it on."))
}

// ---------------------------------------------------------- the portal --

// PortalPlugins is the operator's review of the plugins sites upload.
type PortalPlugins interface {
	// PluginReviews lists the uploads waiting for a review, oldest first;
	// PendingPluginReviews counts them.
	PluginReviews(ctx context.Context) ([]NetPluginReview, error)
	PendingPluginReviews(ctx context.Context) (int, error)
	// ReviewPlugin approves or rejects one build (its pin) of a plugin of
	// a site: store.ErrNotFound when it is not waiting, ErrNetworkState
	// when the site has another build of it now.
	ReviewPlugin(ctx context.Context, siteID int64, pluginID, pin string, approve bool, by int64) error
}

// A NetPluginReview is a build of a plugin a site uploaded.
type NetPluginReview struct {
	SiteID      int64
	Slug, Host  string
	SiteURL     string // the plugin's page in the site's admin, through single sign-on
	PluginID    string
	Pin         string // the build an approval is for: module, assets and pinned permissions
	SHA256      string // of the module
	Version     string
	Verdict     string // of the scanner
	Findings    int
	Imports     []string // host functions the module imports
	Permissions []string // what it asks for
	RequestedAt time.Time
}

func (s *Server) adminNetPlugins(w http.ResponseWriter, r *http.Request) {
	reviews, err := s.reviews.PluginReviews(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderAdmin(w, r, "network_plugins", s.t("Plugin reviews"), "net-plugins", map[string]any{"Reviews": reviews})
}

var pinRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (s *Server) adminNetPluginReview(w http.ResponseWriter, r *http.Request) {
	const back = "/admin/network/plugins"
	siteID, err := strconv.ParseInt(r.FormValue("site"), 10, 64)
	pluginID, pin, verdict := r.FormValue("plugin"), r.FormValue("pin"), r.FormValue("verdict")
	if err != nil || siteID < 1 || !catalogIDRe.MatchString(pluginID) || !pinRe.MatchString(pin) || verdict != "approve" && verdict != "reject" {
		s.redirect(w, r, back, "error", s.t("Invalid request."))
		return
	}
	switch err := s.reviews.ReviewPlugin(r.Context(), siteID, pluginID, pin, verdict == "approve", uid(r)); {
	case errors.Is(err, store.ErrNotFound):
		s.redirect(w, r, back, "error", s.t("Not found."))
		return
	case errors.Is(err, ErrNetworkState):
		s.redirect(w, r, back, "error", s.t("The site no longer has this build of %s.", pluginID))
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	details := map[string]string{"site": strconv.FormatInt(siteID, 10), "pin": pin}
	if verdict == "approve" {
		s.audit(r, uid(r), "network.plugin_approved", pluginID, details)
		s.redirect(w, r, back, "success", s.t("%s was approved: the site's administrators can switch it on.", pluginID))
		return
	}
	s.audit(r, uid(r), "network.plugin_rejected", pluginID, details)
	s.redirect(w, r, back, "success", s.t("%s was rejected: it cannot run on the site.", pluginID))
}
