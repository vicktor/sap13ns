package web

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"sap13ns/internal/auth"
	"sap13ns/internal/sitebackup"
	"sap13ns/internal/store"
)

// Tools → Backups: the site's own backups (internal/sitebackup), for the
// administrators of the site. A backup or a restore runs in the
// background, one at a time per site (the archives' directory is locked,
// also against the command line and the network's nightly backups).
// Downloads go through a link signed for the session that asked for it,
// and encrypted archives are decrypted on the way out. A restore needs
// the administrator's password (or, without one, a sign-in of the last
// few minutes) and the site's name typed in; it replaces the site's
// content after a safety backup, and is audited.

const (
	backupPurpose = "site-backup-download"
	backupLinkTTL = time.Hour
	// backupTimeout bounds one backup or restore started from the page.
	backupTimeout = 3 * time.Hour
)

// BackupHooks connect a site's backups to the network that runs it. The
// hooks of every site of a network implement them; a single site has none.
type BackupHooks interface {
	BackupIdentity() BackupIdentity
	// SiteRestored tells the network that a restore replaced the site's
	// content: it loads the site again once idle.
	SiteRestored(ctx context.Context)
}

// BackupIdentity names a site of a network in its backups.
type BackupIdentity struct {
	ID      int64
	Label   string // the slug
	Network string // the portal's host
	Portal  bool   // the portal: it holds the network's accounts, and is never restored from a site backup
}

// NetworkBackups is the network's side of the nightly backups, for the
// network administration on the portal.
type NetworkBackups interface {
	NetworkBackups(ctx context.Context) (*NetBackups, error)
}

// NetBackups are the nightly backups of a network.
type NetBackups struct {
	Nightly               bool
	At                    string // the time of day, "03:30"
	Dir                   string // where they are, on the server
	KeepDaily, KeepWeekly int
	Last                  *BackupRun      // the latest nightly run since the server started; nil before
	Failing               []NetSiteBackup // the sites whose latest backup failed or was skipped
}

// BackupRun is one nightly run.
type BackupRun struct {
	Started, Finished           time.Time
	Done, Failed, Skipped, Left int    // Left: not reached within the time budget, the first of the next night
	Error                       string // why the run made no backup at all
}

// NetSiteBackup is the latest backup of a site of the network.
type NetSiteBackup struct {
	ID     int64
	Slug   string
	Portal bool
	Status sitebackup.Status
}

type backupJob struct {
	mu      sync.Mutex
	running bool
	restore bool // the job is a restore, not a backup
	started time.Time
	user    string
	// The latest finished job, shown to the user who started it.
	last *backupResult
}

type backupResult struct {
	UserID   int64
	Restore  bool
	Finished time.Time
	Error    string
	Archive  string // the backup made
	Report   *sitebackup.RestoreReport
}

type backupRow struct {
	sitebackup.Archive
	URL string // the download link
}

type backupsData struct {
	List                   []backupRow
	Status                 sitebackup.Status
	Running, Restoring     bool
	Started                time.Time
	User                   string
	Result                 *backupResult
	Label                  string // the name a restore asks for
	Encrypted              bool   // archives are encrypted
	Network, Portal        bool
	KeepDaily, KeepWeekly  int
	NeedPassword           bool  // a restore asks for the password (else a recent sign-in)
	Quota                  int64 // the storage of the site's plan; 0: none
	SafetyKeep, SafetyDays int
	CooldownMinutes        int
	PerHour                int
	WPBase                 string
	Error                  string
}

// backupOptions are the options of this site's backups.
func (s *Server) backupOptions() (sitebackup.Options, error) {
	pass, err := s.cfg.SiteBackups.ReadPassphrase()
	o := sitebackup.Options{Dir: s.cfg.SiteBackups.Dir, UploadsDir: s.cfg.UploadsDir, Passphrase: pass, AppVersion: Version,
		Site: sitebackup.Site{URL: s.cfg.BaseURL.String(), Title: s.settings().SiteTitle, Label: s.cfg.BaseURL.Hostname()}}
	if s.backupHooks != nil {
		id := s.backupHooks.BackupIdentity()
		o.Site.ID, o.Site.Label, o.Site.Network = id.ID, id.Label, id.Network
	}
	return o, err
}

// backupLimits are the bounds of this site's backups: the largest it may
// make, and what Prune keeps. On a network they follow the storage of the
// site's plan (sitebackup.Limits); backups do not count toward it, so a
// site cannot fill the disk the network shares with them.
func (s *Server) backupLimits(ctx context.Context) (quota, maxBackup int64, keep sitebackup.Retention, err error) {
	keep = sitebackup.Retention{Daily: s.cfg.SiteBackups.KeepDaily, Weekly: s.cfg.SiteBackups.KeepWeekly}
	if s.limits == nil {
		return 0, 0, keep, nil
	}
	if quota, err = s.limits.StorageQuota(ctx); err != nil {
		return 0, 0, keep, err
	}
	maxBackup, keep.MaxBytes = sitebackup.Limits(quota)
	return quota, maxBackup, keep, nil
}

// hasPassword reports whether the user signs in with a password here.
func hasPassword(u *store.User) bool {
	return u.PasswordHash != "" && u.PasswordHash != store.SSOPasswordHash
}

func (s *Server) adminBackups(w http.ResponseWriter, r *http.Request) {
	s.renderBackups(w, r, http.StatusOK, "", "")
}

func (s *Server) renderBackups(w http.ResponseWriter, r *http.Request, status int, msg, wpBase string) {
	o, err := s.backupOptions()
	if err != nil {
		s.log.Error("backup passphrase", "err", err)
	}
	u := currentUser(r)
	d := backupsData{Label: o.Site.Label, Encrypted: o.Passphrase != "", Network: s.backupHooks != nil,
		KeepDaily: s.cfg.SiteBackups.KeepDaily, KeepWeekly: s.cfg.SiteBackups.KeepWeekly, NeedPassword: hasPassword(u),
		WPBase: wpBase, Error: msg, SafetyKeep: sitebackup.SafetyKeep, SafetyDays: sitebackup.PreRestoreDays,
		CooldownMinutes: int(restorePace.Cooldown.Minutes()), PerHour: restorePace.PerHour}
	if d.Quota, _, _, err = s.backupLimits(r.Context()); err != nil {
		s.log.Warn("backup limits", "err", err)
	}
	if s.backupHooks != nil {
		d.Portal = s.backupHooks.BackupIdentity().Portal
	}
	j := &s.backups
	j.mu.Lock()
	d.Running, d.Restoring, d.Started, d.User = j.running, j.running && j.restore, j.started, j.user
	if j.last != nil && j.last.UserID == u.ID {
		d.Result = j.last
	}
	j.mu.Unlock()
	if d.Status, err = sitebackup.ReadStatus(o.Dir); err != nil {
		s.log.Warn("backup status", "err", err)
	}
	list, err := sitebackup.List(o.Dir)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	for _, a := range list {
		d.List = append(d.List, backupRow{Archive: a, URL: s.backupURL(r, a.Name)})
	}
	s.renderAdminStatus(w, r, status, "backups", s.tu(r, "Backups"), "backups", d)
}

// backupBinding ties a download link to the session that asked for it.
func backupBinding(r *http.Request, name string) string {
	var sess []byte
	if c := rctx(r).Session; c != nil {
		sess = c.TokenHash
	}
	return name + "\x00" + hex.EncodeToString(sess)
}

func (s *Server) backupURL(r *http.Request, name string) string {
	return "/admin/tools/backups/download/" + name + "?t=" + s.keys.Sign(backupPurpose, backupBinding(r, name), time.Now())
}

// restorePace spaces out the restores of a site from its admin pages:
// each makes a safety backup. It is kept with the site's backups, so it
// holds when the site's server is reloaded.
var restorePace = sitebackup.Pace{Cooldown: 10 * time.Minute, PerHour: 3}

// startBackupJob claims the site's job, or reports that one runs.
func (s *Server) startBackupJob(r *http.Request, restore bool) bool {
	j := &s.backups
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.running {
		return false
	}
	j.running, j.restore, j.started, j.user = true, restore, time.Now(), currentUser(r).Name()
	return true
}

// restorePaceError explains when the next restore may start.
func (s *Server) restorePaceError(r *http.Request, dir string) string {
	wait, err := sitebackup.RestoreWait(dir, restorePace, time.Now())
	if err != nil {
		s.log.Warn("restore pace", "err", err)
	}
	return s.tu(r, "Restores of this site are spaced out: one every %d minutes, at most %d an hour. Try again in %d minutes.",
		int(restorePace.Cooldown.Minutes()), restorePace.PerHour, int(wait.Minutes())+1)
}

func (s *Server) finishBackupJob(res *backupResult) {
	j := &s.backups
	j.mu.Lock()
	j.running, j.last = false, res
	j.mu.Unlock()
}

// backupError explains a failed backup or restore to the administrator.
func (s *Server) backupError(r *http.Request, err error) string {
	switch {
	case errors.Is(err, sitebackup.ErrBusy):
		return s.tu(r, "A backup or restore of this site is already running. Try again when it has finished.")
	case errors.Is(err, sitebackup.ErrLowDisk):
		return s.tu(r, "The server does not have enough free disk space for a backup of this site. The server administrator has been told in the log.")
	case errors.Is(err, sitebackup.ErrTooLarge):
		return s.tu(r, "The backup would be larger than the storage of the site's plan. Remove media you no longer need, or move to a larger plan.")
	case errors.Is(err, sitebackup.ErrNotSiteBackup):
		return s.tu(r, "This file is not a backup of a sap13ns site.")
	case strings.Contains(err.Error(), "passphrase"):
		return s.tu(r, "The backup could not be decrypted with the server's passphrase.")
	}
	return s.tu(r, "It failed. The error has been logged.")
}

func (s *Server) adminBackupCreate(w http.ResponseWriter, r *http.Request) {
	wpBase, err := sitebackup.CheckWPBase(r.FormValue("wp_base"))
	if err != nil {
		s.renderBackups(w, r, http.StatusUnprocessableEntity,
			s.tu(r, "Enter the address of the WordPress site like https://new.example, or leave it empty."), r.FormValue("wp_base"))
		return
	}
	if !s.backupNow.Allow("now") {
		s.renderBackups(w, r, http.StatusTooManyRequests, s.tu(r, "Too many backups in a short time. Try again in an hour."), wpBase)
		return
	}
	o, err := s.backupOptions()
	if err != nil {
		s.log.Error("backup passphrase", "err", err)
		s.renderBackups(w, r, http.StatusInternalServerError, s.tu(r, "The backup passphrase of the server cannot be read. Ask the server administrator."), wpBase)
		return
	}
	o.WPBase = wpBase
	_, maxBackup, keep, err := s.backupLimits(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	o.MaxBytes = maxBackup
	if !s.startBackupJob(r, false) {
		s.renderBackups(w, r, http.StatusConflict, s.tu(r, "A backup or restore of this site is already running. Try again when it has finished."), wpBase)
		return
	}
	u := currentUser(r)
	ip := clientIP(r)
	details := map[string]string{}
	if wpBase != "" {
		details["wp_base"] = wpBase
	}
	s.goJob(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), backupTimeout)
		defer cancel()
		res := &backupResult{UserID: u.ID}
		a, _, err := sitebackup.Create(ctx, s.store, o)
		if err != nil {
			s.log.Error("site backup", "err", err)
			res.Error = s.backupError(r, err)
		} else {
			res.Archive = a.Name
			details["archive"] = a.Name
			details["bytes"] = strconv.FormatInt(a.Size, 10)
			if err := s.store.Audit(ctx, u.ID, "backup.created", a.Name, ip, details); err != nil {
				s.log.Error("audit", "err", err)
			}
			if removed, err := sitebackup.Prune(o.Dir, keep, time.Now()); err != nil {
				s.log.Error("prune site backups", "err", err)
			} else if len(removed) > 0 {
				s.log.Info("old site backups removed", "count", len(removed))
			}
		}
		res.Finished = time.Now()
		s.finishBackupJob(res)
	})
	s.redirect(w, r, "/admin/tools/backups", "success", s.tu(r, "The backup is being made. This page shows it when it is ready."))
}

// adminBackupDownload sends a backup, decrypted when it is encrypted. The
// link must be signed for this session less than backupLinkTTL ago.
func (s *Server) adminBackupDownload(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := s.keys.Verify(backupPurpose, backupBinding(r, name), r.URL.Query().Get("t"), backupLinkTTL); !ok {
		s.adminError(w, r, http.StatusForbidden, s.tu(r, "This download link is not valid. Open the backups page again."))
		return
	}
	o, err := s.backupOptions()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	a, err := sitebackup.Find(o.Dir, name)
	if errors.Is(err, sitebackup.ErrNotFound) {
		s.adminError(w, r, http.StatusNotFound, s.tu(r, "This backup does not exist any more."))
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	in, err := sitebackup.Plain(a.Path, o.Passphrase)
	if err != nil {
		s.log.Error("open a site backup", "name", name, "err", err)
		s.adminError(w, r, http.StatusInternalServerError, s.backupError(r, err))
		return
	}
	defer in.Close()
	u := currentUser(r)
	s.audit(r, u.ID, "backup.downloaded", name, map[string]string{"bytes": strconv.FormatInt(a.Size, 10)})
	exportHeaders(w, "application/zip", strings.TrimSuffix(name, ".enc"))
	if !a.Encrypted {
		w.Header().Set("Content-Length", strconv.FormatInt(a.Size, 10))
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(backupTimeout))
	if _, err := io.Copy(w, in); err != nil {
		if r.Context().Err() != nil {
			return // the browser went away
		}
		// A modified or cut archive: break the connection, so that the
		// browser reports a failed download instead of keeping a bad file.
		s.log.Error("send a site backup", "name", name, "err", err)
		panic(http.ErrAbortHandler)
	}
}

func (s *Server) adminBackupRestore(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	o, err := s.backupOptions()
	if err != nil {
		s.log.Error("backup passphrase", "err", err)
		s.renderBackups(w, r, http.StatusInternalServerError, s.tu(r, "The backup passphrase of the server cannot be read. Ask the server administrator."), "")
		return
	}
	if s.backupHooks != nil && s.backupHooks.BackupIdentity().Portal {
		s.renderBackups(w, r, http.StatusForbidden, s.tu(r, "The portal holds the network's accounts: it is restored from a backup of the database, by the server administrator."), "")
		return
	}
	a, err := sitebackup.Find(o.Dir, r.FormValue("name"))
	if err != nil {
		s.renderBackups(w, r, http.StatusUnprocessableEntity, s.tu(r, "Choose a backup from the list."), "")
		return
	}
	if strings.TrimSpace(r.FormValue("confirm")) != o.Site.Label {
		s.renderBackups(w, r, http.StatusUnprocessableEntity, s.tu(r, "Type the name of the site, %s, to confirm the restore.", o.Site.Label), "")
		return
	}
	if msg := s.restoreReauth(r, u); msg != "" {
		s.audit(r, u.ID, "backup.restore_refused", a.Name, nil)
		s.renderBackups(w, r, http.StatusUnauthorized, msg, "")
		return
	}
	// Each restore makes a safety backup: a few, spaced out. Restore
	// checks it again under the site's lock.
	if wait, err := sitebackup.RestoreWait(o.Dir, restorePace, time.Now()); err != nil {
		s.serverError(w, r, err)
		return
	} else if wait > 0 {
		s.renderBackups(w, r, http.StatusTooManyRequests, s.restorePaceError(r, o.Dir), "")
		return
	}
	_, maxBackup, keep, err := s.backupLimits(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	o.MaxBytes = maxBackup
	if !s.startBackupJob(r, true) {
		s.renderBackups(w, r, http.StatusConflict, s.tu(r, "A backup or restore of this site is already running. Try again when it has finished."), "")
		return
	}
	ip := clientIP(r)
	s.audit(r, u.ID, "backup.restore_started", a.Name, nil)
	s.goJob(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), backupTimeout)
		defer cancel()
		res := &backupResult{UserID: u.ID, Restore: true}
		rep, err := sitebackup.Restore(ctx, s.store, a.Path, sitebackup.RestoreOptions{Options: o, Replace: true, ActorID: u.ID,
			CheckRedirect: CheckRestoredRedirect, SignIn: CheckRestoredSignIn, Plugins: s.pluginRules(), Retention: &keep, Pace: restorePace,
			Theme: func(slug string) bool { _, ok := s.themes.Get(slug); return ok }})
		details := map[string]string{}
		if err != nil {
			s.log.Error("site restore", "archive", a.Name, "err", err)
			res.Error = s.backupError(r, err)
			if errors.Is(err, sitebackup.ErrTooSoon) {
				res.Error = s.restorePaceError(r, o.Dir)
			}
			details["error"] = err.Error()
		} else {
			res.Report = rep
			details["made"] = rep.Manifest.Created.Format(time.RFC3339)
			details["posts"] = strconv.Itoa(rep.Posts)
			details["pages"] = strconv.Itoa(rep.Pages)
			if rep.Safety != nil {
				details["safety_backup"] = rep.Safety.Name
			}
			if len(rep.Pruned) > 0 {
				details["pruned"] = strconv.Itoa(len(rep.Pruned))
			}
			if err := s.Restored(ctx); err != nil {
				s.log.Error("reload after a restore", "err", err)
			}
			if s.backupHooks != nil {
				s.backupHooks.SiteRestored(ctx)
			}
		}
		action := "backup.restored"
		if err != nil {
			action = "backup.restore_failed"
		}
		if err := s.store.Audit(ctx, u.ID, action, a.Name, ip, details); err != nil {
			s.log.Error("audit", "err", err)
		}
		res.Finished = time.Now()
		s.finishBackupJob(res)
	})
	s.redirect(w, r, "/admin/tools/backups", "success", s.tu(r, "The restore has started. This page shows its result when it has finished."))
}

// pluginRules checks the plugin settings and widgets of a restore.
func (s *Server) pluginRules() sitebackup.PluginRules {
	if s.plugins == nil {
		return nil
	}
	return s.plugins
}

// restoreReauth checks that the administrator restoring is who they say:
// the current password, or without one (single sign-on) a sign-in of the
// last few minutes. It returns "" or a translated error.
func (s *Server) restoreReauth(r *http.Request, u *store.User) string {
	if !hasPassword(u) {
		if recentLogin(r) {
			return ""
		}
		return s.tu(r, "Sign out and in again, then restore within %d minutes.", int(passkeyReauthWindow.Minutes()))
	}
	password := r.FormValue("current_password")
	if password == "" {
		return s.tu(r, "Enter your current password to continue.")
	}
	if !s.loginUser.Allow("pw:" + u.Username) {
		return s.tu(r, "Too many attempts. Wait a few minutes and try again.")
	}
	if len(password) > auth.MaxPasswordLen || !auth.VerifyPassword(u.PasswordHash, password) {
		return s.tu(r, "The current password is not correct.")
	}
	s.loginUser.Reset("pw:" + u.Username)
	return ""
}
