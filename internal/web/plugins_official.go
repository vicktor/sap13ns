package web

// The official plugins ship inside the binary (internal/plugin/bundled).
// Plugins → Official plugins lists those the site has not installed, and
// those it installed from there that the binary has a newer version of.
// Installing goes through plugin.Host.Install exactly as an upload: the
// archive limits, the scanner, installed disabled, with source
// "official"; enabling is the usual approval pinned to the build. On a
// network every plan can install them (the operator chose to run this
// binary), except those the operator hides from the sites.

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"sap13ns/internal/media"
	"sap13ns/internal/plugin"
)

// An officialRow is an official plugin the site can install or update.
type officialRow struct {
	*plugin.OfficialPlugin
	Perms     []pluginPerm // what it asks for, in plain words
	Installed string       // the installed (older) version, for an update
}

// officialOffers lists the official plugins the site can install, and the
// updates of those it installed from there, without the hidden ones. A
// plugin with the same id installed some other way is left out: it may
// not be the official one. A site whose plugins do not run lists none.
func (s *Server) officialOffers(hidden []string) []officialRow {
	if s.plugins.Inert() {
		return nil
	}
	var out []officialRow
	for _, op := range s.official.List() {
		if slices.Contains(hidden, op.ID) {
			continue
		}
		row := officialRow{OfficialPlugin: op, Perms: s.permSummary(op.EffectivePermissions())}
		if p := s.plugins.Get(op.ID); p != nil {
			if p.Record == nil || p.Record.Source != plugin.SourceOfficial || !op.NewerThan(p.Version) {
				continue
			}
			row.Installed = p.Version
		}
		out = append(out, row)
	}
	return out
}

// permSummary describes permissions in plain words, as the review on a
// plugin's page does, with the hosts of http: in one line and the sources
// of each csp: directive in another.
func (s *Server) permSummary(perms []string) []pluginPerm {
	type group struct {
		at    int // its place in out
		perm  string
		items []string
	}
	var out []pluginPerm
	groups := map[string]*group{}
	var order []*group
	for _, perm := range perms {
		key, item := "", ""
		switch {
		case strings.HasPrefix(perm, plugin.PermHTTP) && perm != "http:*":
			key, item = plugin.PermHTTP, strings.TrimPrefix(perm, plugin.PermHTTP)
		case strings.HasPrefix(perm, plugin.PermCSP):
			dir, src, _ := strings.Cut(strings.TrimPrefix(perm, plugin.PermCSP), ":")
			key, item = plugin.PermCSP+dir+":", src
		}
		if key == "" {
			out = append(out, pluginPerm{Name: perm, Risk: plugin.PermissionRisk(perm), Help: s.permHelp(perm)})
			continue
		}
		g := groups[key]
		if g == nil {
			g = &group{at: len(out), perm: perm}
			groups[key] = g
			order = append(order, g)
			out = append(out, pluginPerm{})
		}
		g.items = append(g.items, item)
	}
	for _, g := range order {
		p := pluginPerm{Name: g.perm, Risk: plugin.PermissionRisk(g.perm), Help: s.permHelp(g.perm)}
		if len(g.items) > 1 {
			list := strings.Join(g.items, ", ")
			switch {
			case strings.HasPrefix(g.perm, plugin.PermHTTP):
				p.Name, p.Help = "http:", s.t("Make HTTPS requests to these hosts only: %s.", list)
			case strings.HasPrefix(g.perm, plugin.PermCSP+"frame-src:"):
				p.Name, p.Help = plugin.PermCSP+"frame-src", s.t("Embed frames from %s in your pages.", list)
			case strings.HasPrefix(g.perm, plugin.PermCSP+"img-src:"):
				p.Name, p.Help = plugin.PermCSP+"img-src", s.t("Load images from %s.", list)
			}
		}
		out[g.at] = p
	}
	return out
}

// adminPluginOfficialInstall installs an official plugin, or updates one
// installed from there, as an upload: unpacked under the archive limits,
// scanned, and installed disabled. An update that changes the module, the
// assets or the permissions stops the plugin until it is approved again.
func (s *Server) adminPluginOfficialInstall(w http.ResponseWriter, r *http.Request) {
	if s.pluginsInert(w, r) {
		return
	}
	op := s.official.Get(r.PathValue("id"))
	if op == nil {
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return
	}
	if s.limits != nil {
		pol, err := s.limits.PluginPolicy(r.Context())
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		if slices.Contains(pol.Hidden, op.ID) {
			s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
			return
		}
	}
	if p := s.plugins.Get(op.ID); p != nil && (p.Record == nil || p.Record.Source != plugin.SourceOfficial) {
		s.redirect(w, r, "/admin/plugins/"+op.ID, "error",
			s.t("A plugin with the id %s is already installed, not from the official plugins. Uninstall it first to install the official one.", op.ID))
		return
	}
	pkg, err := op.Package()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	// The core team's code needs no review by a network operator, and
	// clears a review that kept data of an earlier upload still carries.
	res, err := s.plugins.Install(r.Context(), bytes.NewReader(pkg), int64(len(pkg)), plugin.InstallOptions{UserID: uid(r),
		Source: plugin.SourceOfficial, Reviewed: true})
	switch {
	case errors.Is(err, media.ErrQuota):
		s.adminError(w, r, http.StatusRequestEntityTooLarge, s.storageFull())
		return
	case errors.Is(err, plugin.ErrRefused):
		s.pluginRefused(w, r, res, map[string]string{"source": plugin.SourceOfficial})
		return
	case errors.Is(err, plugin.ErrDowngrade):
		s.redirect(w, r, "/admin/plugins/"+op.ID, "error", s.t("The installed version %s is newer than the official %s.", res.Previous, res.Version))
		return
	case err != nil:
		s.log.Error("install an official plugin", "plugin", op.ID, "err", err)
		s.redirect(w, r, "/admin/plugins", "error", s.t("The plugin could not be installed: %s", err.Error()))
		return
	}
	s.pluginsChanged()
	s.pluginInstalled(w, r, res, false, map[string]string{"source": plugin.SourceOfficial})
}

// ---------------------------------------------------------- the portal --

// A netOfficialRow is an official plugin on the operator's page.
type netOfficialRow struct {
	*plugin.OfficialPlugin
	Hidden bool // from the sites
}

func (s *Server) netOfficialRows(ctx context.Context) ([]netOfficialRow, error) {
	if s.official.Len() == 0 {
		return nil, nil
	}
	hidden, err := s.reviews.HiddenOfficialPlugins(ctx)
	if err != nil {
		return nil, err
	}
	var out []netOfficialRow
	for _, op := range s.official.List() {
		out = append(out, netOfficialRow{OfficialPlugin: op, Hidden: slices.Contains(hidden, op.ID)})
	}
	return out, nil
}

// adminNetOfficialPlugin hides an official plugin from the sites of the
// network, or offers it again. Hiding stops installs and updates from the
// sites' admin; the copies they installed stay.
func (s *Server) adminNetOfficialPlugin(w http.ResponseWriter, r *http.Request) {
	const back = "/admin/network/plugins#official"
	op, hidden := s.official.Get(r.FormValue("plugin")), r.FormValue("hidden")
	if op == nil || hidden != "0" && hidden != "1" {
		s.redirect(w, r, back, "error", s.t("Invalid request."))
		return
	}
	if err := s.reviews.HideOfficialPlugin(r.Context(), op.ID, hidden == "1", uid(r)); err != nil {
		s.serverError(w, r, err)
		return
	}
	if hidden == "1" {
		s.audit(r, uid(r), "network.official_plugin_hidden", op.ID, map[string]string{"version": op.Version})
		s.redirect(w, r, back, "success", s.t("%s is hidden from the sites. The copies they installed stay.", op.Name))
		return
	}
	s.audit(r, uid(r), "network.official_plugin_offered", op.ID, map[string]string{"version": op.Version})
	s.redirect(w, r, back, "success", s.t("%s is offered to the sites again.", op.Name))
}
