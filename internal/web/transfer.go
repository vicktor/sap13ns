package web

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"sap13ns/internal/backup"
	"sap13ns/internal/export"
	"sap13ns/internal/sitebackup"
	"sap13ns/internal/store"
	"sap13ns/internal/transfer"
)

// Tools → Move site: the whole site, in one encrypted archive, to another
// install of sap13ns (internal/transfer), and, on a single site, a site
// from another install into this one.
//
// Moving out runs in the background and leaves the archive in the export
// directory (not served, not counted in a plan's storage, one waiting per
// site, swept after export.TTL), downloaded by the administrator who asked
// through a link signed for them. The archive holds the site's secrets, so
// it needs the administrator's password and a passphrase of their own.
// On a network, the network's SMTP server and AI travel only when a
// network admin makes the archive: they are the network's, not the site's.
//
// Moving in replaces this site with the archive's. The archive is uploaded
// in chunks (any size, through nginx's limit on a request), checked and
// unpacked with its passphrase, summarised, and imported once the
// administrator confirms with their password and the site's name; a
// backup of this site is made first. The importing account is kept on the
// site, and the server restarts to take up the site's key.

const (
	transferPurpose = "transfer-download"
	// transferChunk is the largest piece of an upload, below nginx's
	// default limit on a request.
	transferChunk = 8 << 20
	// transferMax bounds an archive.
	transferMax = int64(1 << 40)
	// transferStagedTTL: a checked archive not imported is dropped.
	transferStagedTTL = 2 * time.Hour
	transferTimeout   = 3 * time.Hour
)

type transferJob struct {
	mu      sync.Mutex
	running string // "", "export", "check" or "import"
	started time.Time
	user    string
	// The last failure, for the user who met it.
	err     string
	errUser int64
	// The upload being received, and the archive checked from it.
	up       *transferUpload
	staged   *transfer.Staged
	stagedBy int64
	stagedAt time.Time
	// imported: the import finished and the server restarts (or, without
	// SetRestart, must be restarted).
	imported *transfer.Report
	// freeSpace measures a disk: export.FreeSpace when nil. Tests replace it.
	freeSpace func(dir string) (int64, error)
}

type transferUpload struct {
	id        string
	userID    int64
	name      string
	size, got int64
	path      string
	touched   time.Time
}

// SetRestart gives the server a way to restart itself, after an import.
func (s *Server) SetRestart(fn func()) { s.restart = fn }

type transferData struct {
	Running      string
	Started      time.Time
	User         string
	Failed       string
	Ready        []exportLink
	NeedPassword bool
	Recent       bool // signed in a moment ago: enough without a password
	ReauthMin    int
	MinPass      int
	Network      bool // a site of a network
	Portal       bool
	NetworkAdmin bool   // may carry the network's SMTP server and AI
	ServerSMTP   string // the server's SMTP server, "" when none
	OwnSMTP      string // the site's own (Settings → E-mail), "" when none
	NetworkAI    bool   // the plan gives the network's AI
	SiteAI       string // the site's AI: "network", "own" or ""
	// Moving in (single site).
	CanImport bool
	Chunk     int
	Upload    *transferUploadView
	Staged    *transfer.Manifest
	Rows      int64
	Label     string // the name to type
	Imported  *transfer.Report
	Restarts  bool
	Record    *transfer.Record
	Error     string
}

type transferUploadView struct {
	Name      string
	Size, Got int64
	Complete  bool
}

func (s *Server) transferDir() string { return filepath.Join(s.cfg.DataDir, "transfer-incoming") }

// canImport: only a single site takes in another; a site of a network is
// created by the network.
func (s *Server) canImport() bool { return !s.cfg.Multisite && len(s.cfg.ServerKey) == 32 }

// isNetworkAdmin reports whether the user is an admin of the network the
// site is on.
func (s *Server) isNetworkAdmin(r *http.Request, u *store.User) bool {
	if s.limits == nil || u.NetworkAccountID == nil {
		return false
	}
	ok, err := s.limits.NetworkAdmin(r.Context(), *u.NetworkAccountID)
	if err != nil {
		s.log.Warn("network admin", "err", err)
	}
	return ok
}

func (s *Server) adminTransfer(w http.ResponseWriter, r *http.Request) {
	s.renderTransfer(w, r, http.StatusOK, "")
}

func (s *Server) renderTransfer(w http.ResponseWriter, r *http.Request, status int, msg string) {
	ctx := r.Context()
	u := currentUser(r)
	d := transferData{NeedPassword: hasPassword(u), Recent: recentLogin(r), ReauthMin: int(passkeyReauthWindow.Minutes()), MinPass: backup.MinPassphraseLen, Network: s.cfg.Multisite,
		CanImport: s.canImport(), Chunk: transferChunk, Restarts: s.restart != nil, Error: msg}
	if s.backupHooks != nil {
		d.Portal = s.backupHooks.BackupIdentity().Portal
	}
	d.NetworkAdmin = s.isNetworkAdmin(r, u)
	if s.cfg.SMTP.Enabled() && (!d.Network || d.NetworkAdmin) {
		d.ServerSMTP = s.cfg.SMTP.Host
	}
	if ms, err := s.store.MailSettings(ctx); err == nil && ms.Enabled {
		d.OwnSMTP = ms.Host
	}
	if ai, err := s.store.AISettings(ctx); err == nil && ai.Enabled {
		d.SiteAI = "own"
		if ai.Source == store.AISourceNetwork {
			d.SiteAI = "network"
		}
	}
	if d.NetworkAdmin {
		if plan, err := s.limits.AIPlan(ctx); err == nil && plan.Text != nil {
			d.NetworkAI = true
		}
	}
	var rec transfer.Record
	if err := s.store.GetJSON(ctx, transfer.RecordKey, &rec); err == nil {
		d.Record = &rec
	}

	j := &s.transfers
	j.mu.Lock()
	s.expireTransferLocked(time.Now())
	d.Running, d.Started, d.User, d.Imported = j.running, j.started, j.user, j.imported
	if j.running == "" && j.errUser == u.ID {
		d.Failed = j.err
	}
	if up := j.up; up != nil && up.userID == u.ID {
		d.Upload = &transferUploadView{Name: up.name, Size: up.size, Got: up.got, Complete: up.got == up.size}
	}
	if j.staged != nil && j.stagedBy == u.ID {
		d.Staged = j.staged.Manifest
		d.Label = d.Staged.Source.Label
		for _, t := range d.Staged.Tables {
			d.Rows += t.Rows
		}
	}
	j.mu.Unlock()

	now := time.Now()
	if _, err := export.Sweep(s.exportDir(), now); err != nil {
		s.log.Warn("sweep exports", "err", err)
	}
	ready, err := export.List(s.exportDir(), now)
	if err != nil {
		s.log.Warn("list exports", "err", err)
	}
	for _, e := range ready {
		if e.UserID == u.ID && e.Transfer {
			d.Ready = append(d.Ready, exportLink{URL: s.transferURL(e.Name), Created: e.Created, Expires: e.Expires, Size: e.Size})
		}
	}
	s.renderAdminStatus(w, r, status, "transfer", s.tu(r, "Move site"), "transfer", d)
}

// expireTransferLocked drops an upload left half done for a day and a
// checked archive not imported in time. j.mu is held.
func (s *Server) expireTransferLocked(now time.Time) {
	j := &s.transfers
	if j.running != "" {
		return
	}
	if j.up != nil && j.up.got < j.up.size && now.Sub(j.up.touched) > 24*time.Hour {
		os.Remove(j.up.path)
		j.up = nil
	}
	if j.staged != nil && now.Sub(j.stagedAt) > transferStagedTTL {
		j.staged.Close()
		j.staged = nil
	}
}

// dropTransferLocked removes the upload and the checked archive. j.mu is held.
func (s *Server) dropTransferLocked() {
	j := &s.transfers
	if j.up != nil {
		os.Remove(j.up.path)
		j.up = nil
	}
	if j.staged != nil {
		j.staged.Close()
		j.staged = nil
	}
}

func (s *Server) transferURL(name string) string {
	return "/admin/tools/transfer/download/" + name + "?t=" + s.keys.Sign(transferPurpose, name, time.Now())
}

// startTransferJob claims the site's transfer job.
func (s *Server) startTransferJob(r *http.Request, kind string) bool {
	j := &s.transfers
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.running != "" || j.imported != nil {
		return false
	}
	j.running, j.started, j.user, j.err, j.errUser = kind, time.Now(), currentUser(r).Name(), "", 0
	return true
}

func (s *Server) finishTransferJob(userID int64, msg string) {
	j := &s.transfers
	j.mu.Lock()
	j.running, j.err, j.errUser = "", msg, userID
	j.mu.Unlock()
}

func (s *Server) transferBusy(w http.ResponseWriter, r *http.Request) {
	s.renderTransfer(w, r, http.StatusConflict, s.tu(r, "Something else is being moved on this site. Try again when it has finished."))
}

// transferReauth is restoreReauth with the words of this page: without a
// password (single sign-on), a sign-in of the last few minutes.
func (s *Server) transferReauth(r *http.Request, u *store.User) string {
	if !hasPassword(u) && !recentLogin(r) {
		return s.tu(r, "You sign in through the network: use “Sign in again” below, then continue within %d minutes.", int(passkeyReauthWindow.Minutes()))
	}
	return s.restoreReauth(r, u)
}

// ---------------------------------------------------------------- export --

var passphraseSpaces = regexp.MustCompile(`^\s|\s$`)

func (s *Server) adminTransferExport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := currentUser(r)
	if s.backupHooks != nil && s.backupHooks.BackupIdentity().Portal {
		s.renderTransfer(w, r, http.StatusForbidden, s.tu(r, "This is the portal of the network, which does not move. Open Move site in the admin of the site you want to move."))
		return
	}
	pass := r.FormValue("passphrase")
	switch {
	case backup.CheckPassphrase(pass) != nil:
		s.renderTransfer(w, r, http.StatusUnprocessableEntity, s.tu(r, "The passphrase must have at least %d characters.", backup.MinPassphraseLen))
		return
	case pass != r.FormValue("passphrase2"):
		s.renderTransfer(w, r, http.StatusUnprocessableEntity, s.tu(r, "The two passphrases are different."))
		return
	case len(pass) > 1024 || passphraseSpaces.MatchString(pass):
		s.renderTransfer(w, r, http.StatusUnprocessableEntity, s.tu(r, "The passphrase cannot start or end with a space."))
		return
	}
	if msg := s.transferReauth(r, u); msg != "" {
		s.audit(r, u.ID, "transfer.export_refused", "", nil)
		s.renderTransfer(w, r, http.StatusUnauthorized, msg)
		return
	}
	src, ok := s.store.DB.(transfer.DB)
	if !ok {
		s.serverError(w, r, errors.New("transfer: the site's database cannot open a snapshot"))
		return
	}
	dir := s.exportDir()
	if pathInside(dir, s.cfg.UploadsDir) {
		s.renderTransfer(w, r, http.StatusConflict, s.tu(r, "Large exports need an export directory outside the uploads directory. Ask the server administrator."))
		return
	}
	netAdmin := s.isNetworkAdmin(r, u)
	o := transfer.ExportOptions{
		Source: transfer.Source{URL: s.cfg.BaseURL.String(), Title: s.settings().SiteTitle, Label: s.cfg.BaseURL.Hostname(),
			RPID: cmp.Or(s.cfg.PasskeyRPID, s.cfg.BaseURL.Hostname())},
		SecretKey: s.cfg.SecretKey, UploadsDir: s.cfg.UploadsDir, PluginsDir: s.cfg.PluginsDir, ThemesDir: s.cfg.ThemesDir,
		NetworkThemesDir: s.cfg.NetworkThemesDir, Passphrase: pass, TmpDir: dir, AppVersion: Version, Log: s.log,
	}
	if s.backupHooks != nil {
		id := s.backupHooks.BackupIdentity()
		o.Source.Label, o.Source.SiteID, o.Source.Network = id.Label, id.ID, id.Network
	}
	details := map[string]string{}
	if c := s.cfg.SMTP; c.Enabled() && r.FormValue("smtp") == "1" && (!s.cfg.Multisite || netAdmin) {
		o.SMTP = &transfer.SMTP{Host: c.Host, Port: c.Port, Username: c.Username, Password: c.Password, From: c.From}
		details["smtp"] = c.Host
	}
	if r.FormValue("network_ai") == "1" && netAdmin {
		plan, err := s.limits.AIPlan(ctx)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		if t := plan.Text; t != nil {
			o.NetworkAI = &transfer.NetworkAI{Provider: t.Provider, APIKey: t.APIKey, BaseURL: t.BaseURL, TextModel: t.TextModel, ImageModel: t.ImageModel}
			if im := plan.Image; im != nil {
				o.NetworkAI.ImageProvider, o.NetworkAI.ImageAPIKey, o.NetworkAI.ImageBaseURL, o.NetworkAI.ImageModel = im.Provider, im.APIKey, im.BaseURL, im.ImageModel
			}
			details["network_ai"] = t.Provider
		}
	}
	est, err := transfer.Estimate(ctx, src, o)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if msg, err := s.transferFits(r, dir, est); err != nil {
		s.serverError(w, r, err)
		return
	} else if msg != "" {
		s.renderTransfer(w, r, http.StatusRequestEntityTooLarge, msg)
		return
	}
	if !s.startTransferJob(r, "export") {
		s.transferBusy(w, r)
		return
	}
	details["estimate"] = strconv.FormatInt(est, 10)
	s.audit(r, u.ID, "transfer.export_started", o.Source.Label, details)
	now := time.Now()
	name := export.NewTransferName(u.ID, now)
	ip := clientIP(r)
	s.goJob(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), transferTimeout)
		defer cancel()
		size, err := s.writeTransfer(ctx, src, dir, name, o)
		msg := ""
		if err != nil {
			s.log.Error("transfer export", "err", err)
			msg = s.tu(r, "The archive could not be made. The error has been logged.")
			os.Remove(filepath.Join(dir, name))
		} else if err := s.store.Audit(ctx, u.ID, "transfer.exported", o.Source.Label, ip, map[string]string{"bytes": strconv.FormatInt(size, 10)}); err != nil {
			s.log.Error("audit", "err", err)
		}
		s.finishTransferJob(u.ID, msg)
	})
	s.redirect(w, r, "/admin/tools/transfer", "success", s.tu(r, "The archive is being made. This page shows the download link when it is ready."))
}

// transferFits checks that an archive of about est bytes may be made: on
// a network within the storage of the site's plan, and with twice it free
// on the disk. msg explains a refusal.
func (s *Server) transferFits(r *http.Request, dir string, est int64) (string, error) {
	if s.limits != nil {
		quota, err := s.limits.StorageQuota(r.Context())
		if err != nil {
			return "", err
		}
		if quota > 0 && est > 2*quota {
			return s.tu(r, "The archive would take about %s, more than twice the storage of the site's plan (%s).", filesize(est), filesize(quota)), nil
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	free, err := s.freeSpace(dir)
	if err != nil || free < 0 {
		return "", err
	}
	if free < 2*est {
		s.log.Warn("transfer refused: not enough free disk space", "dir", dir, "estimate", est, "free", free)
		return s.tu(r, "The server does not have enough free disk space for an archive of this site, which would take about %s.", filesize(est)), nil
	}
	return "", nil
}

func (s *Server) freeSpace(dir string) (int64, error) {
	if f := s.transfers.freeSpace; f != nil {
		return f(dir)
	}
	return export.FreeSpace(dir)
}

// writeTransfer writes the archive to dir/name through a partial file; it
// replaces the archive waiting before it.
func (s *Server) writeTransfer(ctx context.Context, src transfer.DB, dir, name string, o transfer.ExportOptions) (int64, error) {
	part := filepath.Join(dir, name+export.PartSuffix)
	f, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, err
	}
	_, err = transfer.Export(ctx, src, f, o)
	if err == nil {
		err = f.Sync()
	}
	var size int64
	if st, serr := f.Stat(); serr == nil {
		size = st.Size()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(part)
		return 0, err
	}
	if _, err := export.RemoveTransfers(dir); err != nil {
		s.log.Warn("remove the earlier archives", "err", err)
	}
	return size, os.Rename(part, filepath.Join(dir, name))
}

// adminTransferDownload sends an archive to the user who made it, as many
// times as needed until it expires.
func (s *Server) adminTransferDownload(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	userID, created, ok := export.ParseName(name)
	u := currentUser(r)
	if !ok || !export.IsTransfer(name) || userID != u.ID {
		s.adminError(w, r, http.StatusNotFound, s.tu(r, "This archive does not exist."))
		return
	}
	if _, ok := s.keys.Verify(transferPurpose, name, r.URL.Query().Get("t"), export.TTL); !ok || time.Since(created) >= export.TTL {
		s.adminError(w, r, http.StatusForbidden, s.tu(r, "This download link has expired. Make a new archive."))
		return
	}
	f, err := os.Open(filepath.Join(s.exportDir(), name))
	if err != nil {
		s.adminError(w, r, http.StatusGone, s.tu(r, "This download link has expired. Make a new archive."))
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, u.ID, "transfer.downloaded", name, map[string]string{"bytes": strconv.FormatInt(info.Size(), 10)})
	label := s.cfg.BaseURL.Hostname()
	if s.backupHooks != nil {
		label = s.backupHooks.BackupIdentity().Label
	}
	exportHeaders(w, "application/octet-stream", label+"-"+created.UTC().Format("20060102-1504")+export.TransferExt)
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(transferTimeout))
	if _, err := io.Copy(w, f); err != nil && r.Context().Err() == nil {
		s.log.Warn("send transfer archive", "err", err)
	}
}

// ---------------------------------------------------------------- import --

type uploadReply struct {
	ID       string `json:"id,omitempty"`
	Received int64  `json:"received"`
	Error    string `json:"error,omitempty"`
}

func (s *Server) transferJSON(w http.ResponseWriter, status int, v uploadReply) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// adminTransferUpload receives one chunk of an archive: ?offset=N&size=S
// (&name=, on the first; &id=, on the others), the bytes in the body.
func (s *Server) adminTransferUpload(w http.ResponseWriter, r *http.Request) {
	fail := func(status int, msg string) { s.transferJSON(w, status, uploadReply{Error: msg}) }
	if !s.canImport() {
		fail(http.StatusNotFound, s.tu(r, "Sites are moved into a single-site install only."))
		return
	}
	q := r.URL.Query()
	offset, err1 := strconv.ParseInt(q.Get("offset"), 10, 64)
	size, err2 := strconv.ParseInt(q.Get("size"), 10, 64)
	if err1 != nil || err2 != nil || offset < 0 || size <= 0 || offset >= size {
		fail(http.StatusBadRequest, s.tu(r, "The upload failed. Try again."))
		return
	}
	if size > transferMax {
		fail(http.StatusRequestEntityTooLarge, s.tu(r, "The file is too large (%s max).", filesize(transferMax)))
		return
	}
	u := currentUser(r)
	j := &s.transfers
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.running != "" || j.imported != nil {
		fail(http.StatusConflict, s.tu(r, "Something else is being moved on this site. Try again when it has finished."))
		return
	}
	if offset == 0 {
		// A new upload replaces what was there.
		s.dropTransferLocked()
		dir := s.transferDir()
		if err := os.MkdirAll(dir, 0o700); err != nil {
			s.log.Error("transfer upload", "err", err)
			fail(http.StatusInternalServerError, s.tu(r, "The upload failed. Try again."))
			return
		}
		// The archive, then its unpacked copy, then the database.
		if free, err := s.freeSpace(dir); err == nil && free >= 0 && free < 3*size {
			fail(http.StatusInsufficientStorage, s.tu(r, "The server does not have enough free disk space for this archive: it needs about %s.", filesize(3*size)))
			return
		}
		b := make([]byte, 16)
		rand.Read(b)
		id := hex.EncodeToString(b)
		name := strings.TrimSpace(q.Get("name"))
		if len(name) > 200 {
			name = name[:200]
		}
		j.up = &transferUpload{id: id, userID: u.ID, name: name, size: size, path: filepath.Join(dir, id+".part")}
		s.audit(r, u.ID, "transfer.upload_started", name, map[string]string{"bytes": strconv.FormatInt(size, 10)})
	}
	up := j.up
	if up == nil || up.userID != u.ID || (offset > 0 && q.Get("id") != up.id) || offset != up.got || size != up.size {
		fail(http.StatusConflict, s.tu(r, "The upload was interrupted. Choose the file again."))
		return
	}
	flags := os.O_WRONLY | os.O_APPEND
	if offset == 0 {
		flags |= os.O_CREATE | os.O_EXCL
	}
	f, err := os.OpenFile(up.path, flags, 0o600)
	if err != nil {
		s.log.Error("transfer upload", "err", err)
		fail(http.StatusInternalServerError, s.tu(r, "The upload failed. Try again."))
		return
	}
	n, err := io.Copy(f, io.LimitReader(http.MaxBytesReader(w, r.Body, transferChunk), min(transferChunk, up.size-up.got)))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		// What was written of this chunk is cut, so that it can be sent again.
		os.Truncate(up.path, up.got)
		fail(http.StatusBadRequest, s.tu(r, "The upload failed. Try again."))
		return
	}
	up.got += n
	up.touched = time.Now()
	s.transferJSON(w, http.StatusOK, uploadReply{ID: up.id, Received: up.got})
}

// adminTransferDiscard drops the upload and the checked archive.
func (s *Server) adminTransferDiscard(w http.ResponseWriter, r *http.Request) {
	j := &s.transfers
	j.mu.Lock()
	if j.running == "" {
		s.dropTransferLocked()
	}
	j.mu.Unlock()
	s.redirect(w, r, "/admin/tools/transfer", "success", s.tu(r, "The uploaded archive was removed."))
}

// adminTransferCheck decrypts, unpacks and checks the uploaded archive.
func (s *Server) adminTransferCheck(w http.ResponseWriter, r *http.Request) {
	if !s.canImport() {
		s.adminError(w, r, http.StatusNotFound, s.tu(r, "Sites are moved into a single-site install only."))
		return
	}
	u := currentUser(r)
	pass := r.FormValue("passphrase")
	j := &s.transfers
	j.mu.Lock()
	up := j.up
	ready := up != nil && up.userID == u.ID && up.got == up.size
	j.mu.Unlock()
	if !ready {
		s.renderTransfer(w, r, http.StatusUnprocessableEntity, s.tu(r, "Upload the archive first."))
		return
	}
	if pass == "" {
		s.renderTransfer(w, r, http.StatusUnprocessableEntity, s.tu(r, "Enter the passphrase of the archive."))
		return
	}
	if !s.startTransferJob(r, "check") {
		s.transferBusy(w, r)
		return
	}
	s.goJob(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), transferTimeout)
		defer cancel()
		st, err := transfer.Open(ctx, up.path, pass, s.cfg.DataDir)
		msg := ""
		switch {
		case errors.Is(err, backup.ErrWrongPassphrase):
			msg = s.tu(r, "The passphrase is not the one of this archive.")
		case errors.Is(err, transfer.ErrNewer):
			msg = s.tu(r, "The archive was made by a newer version of sap13ns: update this server first.")
		case err != nil:
			s.log.Warn("transfer check", "err", err)
			msg = s.tu(r, "This file is not a complete archive of a site moved with sap13ns.")
		}
		j.mu.Lock()
		if err == nil {
			os.Remove(up.path)
			j.up, j.staged, j.stagedBy, j.stagedAt = nil, st, u.ID, time.Now()
		}
		j.mu.Unlock()
		s.finishTransferJob(u.ID, msg)
	})
	s.redirect(w, r, "/admin/tools/transfer", "success", s.tu(r, "The archive is being checked. This page shows what it holds when it is ready."))
}

// adminTransferImport replaces this site with the checked archive's.
func (s *Server) adminTransferImport(w http.ResponseWriter, r *http.Request) {
	if !s.canImport() {
		s.adminError(w, r, http.StatusNotFound, s.tu(r, "Sites are moved into a single-site install only."))
		return
	}
	u := currentUser(r)
	j := &s.transfers
	j.mu.Lock()
	st := j.staged
	if j.stagedBy != u.ID {
		st = nil
	}
	j.mu.Unlock()
	if st == nil {
		s.renderTransfer(w, r, http.StatusUnprocessableEntity, s.tu(r, "Upload the archive first."))
		return
	}
	m := st.Manifest
	if strings.TrimSpace(r.FormValue("confirm")) != m.Source.Label {
		s.renderTransfer(w, r, http.StatusUnprocessableEntity, s.tu(r, "Type %s to confirm.", m.Source.Label))
		return
	}
	if msg := s.transferReauth(r, u); msg != "" {
		s.audit(r, u.ID, "transfer.import_refused", m.Source.Label, nil)
		s.renderTransfer(w, r, http.StatusUnauthorized, msg)
		return
	}
	var keep *transfer.KeepUser
	if r.FormValue("keep") == "1" && hasPassword(u) {
		keep = &transfer.KeepUser{Username: u.Username, Email: u.Email, DisplayName: u.DisplayName, PasswordHash: u.PasswordHash}
	}
	if !s.startTransferJob(r, "import") {
		s.transferBusy(w, r)
		return
	}
	s.audit(r, u.ID, "transfer.import_started", m.Source.Label, map[string]string{"from": m.Source.URL})
	ip := clientIP(r)
	s.goJob(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), transferTimeout)
		defer cancel()
		rep, err := s.importTransfer(ctx, st, keep)
		if err != nil {
			s.log.Error("transfer import", "err", err)
			msg := s.tu(r, "The import did not finish. The site is as it was. The error has been logged.")
			if errors.Is(err, sitebackup.ErrLowDisk) {
				msg = s.tu(r, "The server does not have enough free disk space for a backup of this site. The server administrator has been told in the log.")
			}
			s.finishTransferJob(u.ID, msg)
			return
		}
		// The rows are the site's now: the audit log is the imported one.
		details := map[string]string{"from": m.Source.URL, "made": m.Created.Format(time.RFC3339), "rows": strconv.FormatInt(rep.Rows, 10),
			"files": strconv.Itoa(rep.Files)}
		if rep.Kept != "" {
			details["account"] = rep.Kept
		}
		if err := s.store.Audit(ctx, 0, "transfer.imported", m.Source.Label, ip, details); err != nil {
			s.log.Error("audit", "err", err)
		}
		j.mu.Lock()
		j.staged.Close()
		j.staged, j.running, j.imported = nil, "", rep
		j.mu.Unlock()
		s.log.Info("site imported", "from", m.Source.URL, "rows", rep.Rows, "files", rep.Files)
		if s.restart != nil {
			// Let the page that asked see the result first.
			time.AfterFunc(2*time.Second, s.restart)
		}
	})
	s.redirect(w, r, "/admin/tools/transfer", "success", s.tu(r, "The import has started. This page shows its result when it has finished."))
}

// importTransfer backs this site up, then replaces it with the archive's.
func (s *Server) importTransfer(ctx context.Context, st *transfer.Staged, keep *transfer.KeepUser) (*transfer.Report, error) {
	o, err := s.backupOptions()
	if err != nil {
		return nil, err
	}
	if _, _, err := sitebackup.Create(ctx, s.store, o); err != nil {
		return nil, err
	}
	return st.Apply(ctx, s.store.DB, transfer.ApplyOptions{UploadsDir: s.cfg.UploadsDir, PluginsDir: s.cfg.PluginsDir, ThemesDir: s.cfg.ThemesDir,
		ServerKey: s.cfg.ServerKey, Replace: true, Keep: keep, Log: s.log})
}
