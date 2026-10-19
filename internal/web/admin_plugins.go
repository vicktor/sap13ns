package web

import (
	"cmp"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"

	"sap13ns/internal/media"
	"sap13ns/internal/plugin"
)

// Plugins are installed from a .zip package (or copied into the plugins
// directory by hand), scanned, and always arrive disabled. Switching one on
// is the approval: it pins the module hash, the assets hash and the
// permissions, with a review the first time or whenever they changed.

// pluginUploadLimit caps the request body of a package upload.
const pluginUploadLimit = plugin.MaxPackageBytes + 1<<20

type pluginPerm struct {
	Name    string
	Risk    string
	Granted bool
	Help    string
}

type pluginRow struct {
	*plugin.Plugin
	QuickEnable bool // can be switched on from the list without a review
}

type pluginsData struct {
	Plugins []pluginRow
	Plugin  *plugin.Plugin
	Perms   []pluginPerm
	Values  map[string]string
	Logs    []plugin.LogLine
	Dir     string
	// Detail page.
	Report       *plugin.Report
	ReportStale  bool // made for another build than the one on disk
	NeedsConfirm bool // high findings or high-risk permissions
	Assets       []string
	MaxMB        int64
	// Refused is the report of an upload that was not installed.
	Refused *plugin.Report
	// A site of a network: the network's catalogue, and whether its plan
	// lets it upload its own plugins (held for the operator's review).
	Tenant  bool
	Uploads bool
	Plan    string
	Catalog []CatalogPlugin
	// Inert: a suspended site opened for an emergency access, whose
	// plugins do not load (plugin.HostOptions.Inert).
	Inert bool
}

// pluginsPage is the list of plugins, with the upload form and, on a site
// of a network, the catalogue.
func (s *Server) pluginsPage(r *http.Request) (pluginsData, error) {
	d := pluginsData{Plugins: s.pluginsList(), Dir: s.cfg.PluginsDir, MaxMB: plugin.MaxPackageBytes >> 20, Uploads: true,
		Inert: s.plugins.Inert()}
	if s.limits == nil {
		return d, nil
	}
	pol, err := s.limits.PluginPolicy(r.Context())
	if err != nil {
		return d, err
	}
	d.Tenant, d.Uploads, d.Plan = true, pol.Uploads, pol.Plan
	if d.Catalog, err = s.limits.PluginCatalog(r.Context()); err != nil {
		s.log.Error("the network's plugin catalogue", "err", err) // the page works without it
	}
	// A plugin the site uploaded with the same id can be replaced by the
	// network's.
	for i := range d.Catalog {
		if p := s.plugins.Get(d.Catalog[i].ID); p != nil && p.Record != nil && p.Record.Source == "catalog" {
			d.Catalog[i].Installed = cmp.Or(p.Version, "?")
		}
	}
	return d, nil
}

func (s *Server) pluginsList() []pluginRow {
	var out []pluginRow
	for _, p := range s.plugins.List() {
		out = append(out, pluginRow{Plugin: p, QuickEnable: quickEnable(p)})
	}
	return out
}

func (s *Server) adminPlugins(w http.ResponseWriter, r *http.Request) {
	d, err := s.pluginsPage(r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderAdmin(w, r, "plugins", s.t("Plugins"), "plugins", d)
}

// pluginsInert refuses what would load or start a plugin on a suspended
// site opened for an emergency access, which runs none of them. The
// pages of single plugins answer 404 there: none is loaded.
func (s *Server) pluginsInert(w http.ResponseWriter, r *http.Request) bool {
	if !s.plugins.Inert() {
		return false
	}
	s.redirect(w, r, "/admin/plugins", "error", s.t("Plugins do not run on this site while it is suspended."))
	return true
}

func (s *Server) adminPluginsReload(w http.ResponseWriter, r *http.Request) {
	if s.pluginsInert(w, r) {
		return
	}
	if err := s.plugins.Load(r.Context()); err != nil {
		s.redirect(w, r, "/admin/plugins", "error", err.Error())
		return
	}
	s.pluginsChanged()
	s.audit(r, uid(r), "plugins.reloaded", "", nil)
	s.redirect(w, r, "/admin/plugins", "success", s.t("Plugin directory scanned again."))
}

func (s *Server) pluginsChanged() {
	s.contentChanged()
	s.content.Invalidate()
}

func (s *Server) loadPlugin(w http.ResponseWriter, r *http.Request) (*plugin.Plugin, bool) {
	p := s.plugins.Get(r.PathValue("id"))
	if p == nil {
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return nil, false
	}
	return p, true
}

// currentReport returns the plugin's report if it describes the files on
// disk now.
func currentReport(p *plugin.Plugin) *plugin.Report {
	if p.Report == nil || p.Report.WasmSHA256 != p.SHA256 || p.Report.AssetsSHA256 != p.AssetsSHA256 {
		return nil
	}
	return p.Report
}

// needsConfirmation: enabling needs the explicit risk checkbox.
func needsConfirmation(p *plugin.Plugin, rep *plugin.Report) bool {
	return plugin.HighRisk(p.EffectivePermissions()) || rep == nil || rep.NeedsConfirmation()
}

// quickEnable reports whether the plugin can be switched back on from the
// list: the same build and permissions were approved before, its current
// report is clean of high findings, and nothing high-risk is requested.
func quickEnable(p *plugin.Plugin) bool {
	if p.Status != plugin.StatusAvailable || p.Record == nil || p.Record.ReviewRequired || p.Record.WasmSHA256 != p.SHA256 ||
		p.Record.AssetsSHA256 != p.AssetsSHA256 {
		return false
	}
	for _, perm := range p.Pinned() {
		if !slices.Contains(p.Record.GrantedPermissions, perm) {
			return false
		}
	}
	return !needsConfirmation(p, currentReport(p))
}

func (s *Server) permHelp(perm string) string {
	switch {
	case perm == plugin.PermKV:
		return s.t("Store its own small key-value data.")
	case perm == plugin.PermPostsRead:
		return s.t("Read published posts (never drafts or private data).")
	case perm == "http:*":
		return s.t("Make HTTPS requests to any public address.")
	case strings.HasPrefix(perm, plugin.PermHTTP):
		return s.t("Make HTTPS requests to this host only.")
	case perm == plugin.PermMail:
		return s.t("Send e-mail to the site administrator or to addresses you save in its settings.")
	case perm == plugin.PermCommentsPII:
		return s.t("Receive the e-mail address, IP address and browser of people who comment.")
	case perm == plugin.PermAssetsJS:
		return s.t("Run its own JavaScript on every public page.")
	case strings.HasPrefix(perm, plugin.PermCSP+"frame-src:"):
		return s.t("Embed frames from %s in your pages.", strings.TrimPrefix(perm, plugin.PermCSP+"frame-src:"))
	case strings.HasPrefix(perm, plugin.PermCSP+"img-src:"):
		return s.t("Load images from %s.", strings.TrimPrefix(perm, plugin.PermCSP+"img-src:"))
	}
	return ""
}

func (s *Server) adminPlugin(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadPlugin(w, r)
	if !ok {
		return
	}
	var granted []string
	if p.Record != nil {
		granted = p.Record.GrantedPermissions
	}
	var perms []pluginPerm
	for _, perm := range p.EffectivePermissions() {
		perms = append(perms, pluginPerm{Name: perm, Risk: plugin.PermissionRisk(perm), Granted: slices.Contains(granted, perm), Help: s.permHelp(perm)})
	}
	values := p.SettingValues()
	loc := s.state.Load().Loc
	for _, sd := range p.Settings {
		switch v := values[sd.Key]; {
		case sd.Type == "secret" && v != "":
			values[sd.Key] = "" // never echo secrets back to the browser
		case sd.Type == "datetime" && v != "":
			if t, err := plugin.ParseSettingTime(v, loc); err == nil {
				values[sd.Key] = t.In(loc).Format("2006-01-02T15:04") // what datetime-local expects
			}
		}
	}
	title := p.Name
	if title == "" {
		title = p.ID
	}
	rep := currentReport(p)
	d := pluginsData{Plugin: p, Perms: perms, Values: values, Logs: p.Logs(), Report: p.Report,
		ReportStale: p.Report != nil && rep == nil, NeedsConfirm: needsConfirmation(p, rep),
		Assets: append(slices.Clone(p.Assets.CSS), p.Assets.JS...)}
	s.renderAdmin(w, r, "plugin", title, "plugins", d)
}

// pluginBack is where toggles return: the list or the detail page.
func pluginBack(r *http.Request, id string) string {
	if r.FormValue("back") == "list" {
		return "/admin/plugins"
	}
	return "/admin/plugins/" + id
}

func (s *Server) adminPluginUpload(w http.ResponseWriter, r *http.Request) {
	if s.pluginsInert(w, r) {
		return
	}
	fail := func(msg string) { s.redirect(w, r, "/admin/plugins", "error", msg) }
	// On a site of a network, the plan decides; the upload waits for the
	// network operator's review.
	if s.limits != nil {
		pol, err := s.limits.PluginPolicy(r.Context())
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		if !pol.Uploads {
			s.adminError(w, r, http.StatusForbidden, s.t("On the %s plan, plugins come from the network's catalogue. Uploading your own needs a paid plan.", pol.Plan))
			return
		}
	}
	file, hdr, err := r.FormFile("package")
	if err != nil {
		fail(s.t("Choose a plugin package (.zip) to upload."))
		return
	}
	defer file.Close()
	if !strings.EqualFold(filepath.Ext(hdr.Filename), ".zip") || hdr.Size <= 0 {
		fail(s.t("A plugin package is a .zip file."))
		return
	}
	if hdr.Size > plugin.MaxPackageBytes {
		fail(s.t("The package is larger than %d MB.", plugin.MaxPackageBytes>>20))
		return
	}
	res, err := s.plugins.Install(r.Context(), file, hdr.Size, plugin.InstallOptions{UserID: uid(r), Source: "upload",
		AllowDowngrade: r.FormValue("allow_downgrade") == "1", ReviewRequired: s.limits != nil})
	switch {
	case errors.Is(err, media.ErrQuota):
		s.adminError(w, r, http.StatusRequestEntityTooLarge, s.storageFull())
		return
	case errors.Is(err, plugin.ErrRefused):
		details := map[string]string{"file": truncateStr(hdr.Filename, 120), "package_sha256": res.Report.PackageSHA256}
		var rules []string
		for _, f := range res.Report.Findings {
			if f.Severity == plugin.SevCritical && !slices.Contains(rules, f.Rule) {
				rules = append(rules, f.Rule)
			}
		}
		details["critical"] = strings.Join(rules, " ")
		s.audit(r, uid(r), "plugin.refused", res.ID, details)
		d, err := s.pluginsPage(r)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		d.Refused = res.Report
		s.renderAdminStatus(w, r, http.StatusUnprocessableEntity, "plugins", s.t("Plugins"), "plugins", d)
		return
	case errors.Is(err, plugin.ErrDowngrade):
		fail(s.t("Version %s is older than the installed %s. Tick “Allow an older version” to install it anyway.", res.Version, res.Previous))
		return
	case err != nil:
		s.log.Error("install plugin", "err", err)
		fail(s.t("The plugin could not be installed: %s", err.Error()))
		return
	}
	held := false
	if s.limits != nil {
		if held, err = s.tenantReview(r.Context(), res); err != nil {
			s.log.Error("request a plugin review", "plugin", res.ID, "err", err)
			s.pluginsChanged()
			fail(s.t("The plugin was installed, but its review could not be requested. Upload it again."))
			return
		}
	}
	s.pluginsChanged()
	action := "plugin.installed"
	details := map[string]string{"version": res.Version, "sha256": res.Report.WasmSHA256, "package_sha256": res.Report.PackageSHA256,
		"verdict": res.Report.Verdict, "file": truncateStr(hdr.Filename, 120)}
	msg := s.t("Plugin installed. It is disabled: review the scan report and the permissions, then switch it on.")
	if held {
		msg = s.t("Plugin installed. The network operator reviews it before it can be switched on.")
	}
	if res.Previous != "" {
		action = "plugin.updated"
		details["previous"] = res.Previous
		msg = s.t("Plugin updated from %s to %s. Its settings and data were kept.", res.Previous, res.Version)
		if p := s.plugins.Get(res.ID); p != nil && p.Status == plugin.StatusModified {
			msg += " " + s.t("Its code, assets or permissions changed, so it is stopped until you enable it again.")
		}
		if held {
			msg += " " + s.t("The network operator reviews it before it can be switched on.")
		}
	}
	s.audit(r, uid(r), action, res.ID, details)
	s.redirect(w, r, "/admin/plugins/"+res.ID, "success", msg)
}

func (s *Server) adminPluginApprove(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadPlugin(w, r)
	if !ok {
		return
	}
	back := pluginBack(r, p.ID)
	// The administrator confirms the exact build, assets, permissions and
	// capabilities they reviewed.
	pin := r.FormValue("pin")
	if pin != p.ReviewPin() {
		s.redirect(w, r, "/admin/plugins/"+p.ID, "error", s.t("The plugin changed on disk while you were reviewing it. Review it again."))
		return
	}
	// Every enabled plugin has a current report: plugins copied by hand, or
	// changed since their last scan, are scanned now.
	rep := currentReport(p)
	if rep == nil {
		var err error
		if rep, err = s.plugins.Rescan(r.Context(), p.ID); err != nil && rep == nil {
			s.redirect(w, r, "/admin/plugins/"+p.ID, "error", s.t("The plugin could not be scanned: %s", err.Error()))
			return
		}
		s.audit(r, uid(r), "plugin.scanned", p.ID, map[string]string{"verdict": rep.Verdict, "sha256": rep.WasmSHA256})
		if p = s.plugins.Get(p.ID); p == nil || p.ReviewPin() != pin {
			s.redirect(w, r, "/admin/plugins/"+r.PathValue("id"), "error", s.t("The plugin changed on disk while you were reviewing it. Review it again."))
			return
		}
	}
	if rep.Blocked() {
		s.redirect(w, r, "/admin/plugins/"+p.ID+"#report", "error", s.t("The scanner found critical problems; this plugin cannot be enabled."))
		return
	}
	if needsConfirmation(p, rep) && r.FormValue("confirm_risk") != "1" {
		s.redirect(w, r, "/admin/plugins/"+p.ID+"#review", "error", s.t("This plugin has high-risk permissions or findings. Tick the confirmation box to enable it."))
		return
	}
	if err := s.plugins.Approve(r.Context(), p.ID, pin, uid(r)); err != nil {
		msg := s.t("The plugin could not be enabled: %s", err.Error())
		switch {
		case errors.Is(err, plugin.ErrReviewRequired):
			msg = s.t("The network operator must review this plugin before it can be enabled.")
		case errors.Is(err, plugin.ErrChangedSinceReview):
			msg = s.t("The plugin changed on disk while you were reviewing it. Review it again.")
		}
		s.redirect(w, r, "/admin/plugins/"+p.ID, "error", msg)
		return
	}
	s.pluginsChanged()
	s.audit(r, uid(r), "plugin.approved", p.ID, map[string]string{"sha256": p.SHA256, "assets_sha256": p.AssetsSHA256,
		"permissions": strings.Join(p.EffectivePermissions(), " "), "version": p.Version, "verdict": rep.Verdict,
		"confirmed_risk": fmt.Sprint(r.FormValue("confirm_risk") == "1")})
	if np := s.plugins.Get(p.ID); np != nil && np.Status != plugin.StatusEnabled {
		s.redirect(w, r, "/admin/plugins/"+p.ID, "error", s.t("The plugin was approved but cannot run: %s", np.Err))
		return
	}
	s.redirect(w, r, back, "success", s.t("Plugin enabled."))
}

func (s *Server) adminPluginDisable(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadPlugin(w, r)
	if !ok {
		return
	}
	if err := s.plugins.Disable(r.Context(), p.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.pluginsChanged()
	s.audit(r, uid(r), "plugin.disabled", p.ID, nil)
	s.redirect(w, r, pluginBack(r, p.ID), "success", s.t("Plugin disabled."))
}

func (s *Server) adminPluginScan(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadPlugin(w, r)
	if !ok {
		return
	}
	rep, err := s.plugins.Rescan(r.Context(), p.ID)
	if err != nil && rep == nil {
		s.redirect(w, r, "/admin/plugins/"+p.ID, "error", s.t("The plugin could not be scanned: %s", err.Error()))
		return
	}
	s.audit(r, uid(r), "plugin.scanned", p.ID, map[string]string{"verdict": rep.Verdict, "sha256": rep.WasmSHA256})
	s.redirect(w, r, "/admin/plugins/"+p.ID+"#report", "success", s.t("Scan finished: %s.", cmp.Or(s.t(scanVerdictLabels[rep.Verdict]), rep.Verdict)))
}

// scanVerdictLabels are the interface names of the scanner verdicts, as in
// the scan-report-summary template.
var scanVerdictLabels = map[string]string{
	plugin.VerdictBlocked: "Blocked", plugin.VerdictReview: "Needs review", plugin.VerdictClean: "No problems found",
}

func (s *Server) adminPluginUninstall(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadPlugin(w, r)
	if !ok {
		return
	}
	deleteData := r.FormValue("delete_data") == "1"
	if err := s.plugins.Uninstall(r.Context(), p.ID, deleteData); err != nil {
		if errors.Is(err, plugin.ErrPluginEnabled) {
			s.redirect(w, r, "/admin/plugins/"+p.ID, "error", s.t("Disable the plugin before uninstalling it."))
			return
		}
		s.serverError(w, r, err)
		return
	}
	s.pluginsChanged()
	s.audit(r, uid(r), "plugin.uninstalled", p.ID, map[string]string{"version": p.Version, "data_deleted": fmt.Sprint(deleteData)})
	msg := s.t("Plugin uninstalled. Its settings and stored data were kept in case you install it again.")
	if deleteData {
		msg = s.t("Plugin uninstalled and its data deleted.")
	}
	s.redirect(w, r, "/admin/plugins", "success", msg)
}

func (s *Server) adminPluginSettings(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadPlugin(w, r)
	if !ok {
		return
	}
	// Only submitted fields are saved: the others keep their stored value
	// or their default.
	values := map[string]string{}
	for _, sd := range p.Settings {
		name := "set_" + sd.Key
		v := strings.TrimSpace(r.FormValue(name))
		switch {
		case sd.Type == "bool":
			values[sd.Key] = fmt.Sprint(v == "true") // an unticked box is not submitted
		case sd.Type == "secret" && r.FormValue("clear_"+sd.Key) == "1":
			values[sd.Key] = ""
		case sd.Type == "secret" && v == "":
			// keep the stored secret
		case r.PostForm.Has(name):
			values[sd.Key] = v
		}
	}
	if err := s.plugins.SaveSettings(r.Context(), p.ID, values); err != nil {
		s.redirect(w, r, "/admin/plugins/"+p.ID+"#settings", "error", err.Error())
		return
	}
	s.pluginsChanged()
	s.audit(r, uid(r), "plugin.settings_saved", p.ID, nil)
	s.redirect(w, r, "/admin/plugins/"+p.ID+"#settings", "success", s.t("Saved."))
}

// pluginWidgetFields maps plugin widget kinds to their config fields.
func (s *Server) pluginWidgetFields(kinds []widgetKind) map[string][]plugin.SettingDecl {
	out := map[string][]plugin.SettingDecl{}
	for _, k := range kinds {
		if f := s.plugins.WidgetConfig(k.Kind); len(f) > 0 {
			out[k.Kind] = f
		}
	}
	return out
}

// widgetConfigFor reads a widget's config from the form: the fields of a
// built-in kind, or the fields a plugin widget declares ("cfg_<key>"). The
// add form carries no fields, so a new plugin widget starts with defaults.
func (s *Server) widgetConfigFor(r *http.Request, kind string) (map[string]string, error) {
	decls := s.plugins.WidgetConfig(kind)
	if len(decls) == 0 {
		return widgetConfig(r, kind), nil
	}
	edit := r.FormValue("cfg_form") == "1"
	vals := map[string]string{}
	for _, d := range decls {
		name := "cfg_" + d.Key
		switch {
		case d.Type == "bool" && edit:
			vals[d.Key] = fmt.Sprint(r.FormValue(name) == "true")
		case r.Form.Has(name):
			vals[d.Key] = strings.TrimSpace(r.FormValue(name))
		}
	}
	return s.plugins.CleanWidgetConfig(kind, vals)
}
