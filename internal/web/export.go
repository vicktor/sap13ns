package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"sap13ns/internal/export"
	"sap13ns/internal/store"
)

// Content export (Tools → Export). A WXR file downloads at once, and so
// does a .zip whose uploads are small. A larger .zip is written in the
// background to the site's export directory, one at a time, and is then
// downloaded once from a signed link that expires after export.TTL. The
// export directory is not served, a network does not count it in a
// site's storage, and the maintenance sweeps remove what expired.

// exportDirectBytes is the most uploads a .zip may carry and still be
// written during the request. Tests lower it.
var exportDirectBytes int64 = 64 << 20

// exportPurpose signs download links.
const exportPurpose = "export-download"

type exportJob struct {
	mu       sync.Mutex
	running  bool
	started  time.Time
	finished time.Time
	user     string
	userID   int64
	report   *export.Report
	err      string
}

type exportForm struct {
	Posts, Pages, Media                 bool
	Published, Drafts, Pending, Private bool
	PendingComments, PersonalData       bool
	From, To                            string // YYYY-MM-DD, site time zone
	AuthorID                            int64
	Format                              string // "xml" or "zip"
}

type exportLink struct {
	URL     string
	Created time.Time
	Expires time.Time
	Size    int64
}

type exportData struct {
	Running bool
	Started time.Time
	User    string
	Ready   []exportLink
	Failed  string // the last background export of this user failed
	Authors []store.User
	Form    exportForm
	Error   string
}

func (s *Server) exportDir() string { return export.Dir(s.cfg.DataDir) }

func (s *Server) adminExport(w http.ResponseWriter, r *http.Request) {
	s.renderExport(w, r, http.StatusOK, exportForm{Posts: true, Pages: true, Media: true, Published: true, Format: "xml"}, "")
}

func (s *Server) renderExport(w http.ResponseWriter, r *http.Request, status int, form exportForm, msg string) {
	authors, err := s.authorChoices(r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	u := currentUser(r)
	d := exportData{Authors: authors, Form: form, Error: msg}
	j := &s.exports
	j.mu.Lock()
	d.Running, d.Started, d.User = j.running, j.started, j.user
	if !j.running && j.userID == u.ID {
		d.Failed = j.err
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
		if e.UserID == u.ID {
			d.Ready = append(d.Ready, exportLink{URL: s.exportURL(e.Name, e.Created), Created: e.Created, Expires: e.Expires, Size: e.Size})
		}
	}
	s.renderAdminStatus(w, r, status, "export", s.t("Export"), "export", d)
}

// exportURL is the one-time download link of a finished export.
func (s *Server) exportURL(name string, created time.Time) string {
	return "/admin/tools/export/download/" + name + "?t=" + s.keys.Sign(exportPurpose, name, created)
}

func (s *Server) adminExportStart(w http.ResponseWriter, r *http.Request) {
	form := exportForm{
		Posts: r.FormValue("posts") == "1", Pages: r.FormValue("pages") == "1", Media: r.FormValue("media") == "1",
		Published: r.FormValue("published") == "1", Drafts: r.FormValue("drafts") == "1",
		Pending: r.FormValue("pending") == "1", Private: r.FormValue("private") == "1",
		PendingComments: r.FormValue("pending_comments") == "1", PersonalData: r.FormValue("personal_data") == "1",
		From: trimmed(r, "from"), To: trimmed(r, "to"), Format: r.FormValue("format"),
	}
	if id := formInt64Ptr(r, "author_id"); id != nil {
		form.AuthorID = *id
	}
	opts, msg := s.exportOptions(r, form)
	if msg != "" {
		s.renderExport(w, r, http.StatusUnprocessableEntity, form, msg)
		return
	}
	u := currentUser(r)
	background := false
	if form.Format == "zip" && opts.Media {
		size, err := export.UploadsSize(s.cfg.UploadsDir, exportDirectBytes)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		background = size > exportDirectBytes
	}
	details := map[string]string{
		"format": form.Format, "types": strings.Join(opts.Types, ","), "statuses": strings.Join(opts.Statuses, ","),
		"media": strconv.FormatBool(opts.Media), "pending_comments": strconv.FormatBool(opts.PendingComments),
		"personal_data": strconv.FormatBool(opts.PersonalData), "delivery": "download",
	}
	if form.From != "" {
		details["from"] = form.From
	}
	if form.To != "" {
		details["to"] = form.To
	}
	if form.AuthorID > 0 {
		details["author_id"] = strconv.FormatInt(form.AuthorID, 10)
	}
	if background {
		details["delivery"] = "background"
		s.startExportJob(w, r, form, opts, details)
		return
	}
	s.audit(r, u.ID, "export.created", form.Format, details)
	s.streamExport(w, r, form, opts)
}

// exportOptions validates the form; msg explains what is wrong.
func (s *Server) exportOptions(r *http.Request, form exportForm) (opts export.Options, msg string) {
	if form.Posts {
		opts.Types = append(opts.Types, store.TypePost)
	}
	if form.Pages {
		opts.Types = append(opts.Types, store.TypePage)
	}
	for _, st := range []struct {
		on     bool
		status string
	}{{form.Published, store.StatusPublished}, {form.Drafts, store.StatusDraft}, {form.Pending, store.StatusPending},
		{form.Private, store.StatusPrivate}} {
		if st.on {
			opts.Statuses = append(opts.Statuses, st.status)
		}
	}
	switch {
	case len(opts.Types) == 0:
		return opts, s.t("Choose posts, pages or both.")
	case len(opts.Statuses) == 0:
		return opts, s.t("Choose at least one status.")
	case form.Format != "xml" && form.Format != "zip":
		return opts, s.t("Choose the format of the export.")
	}
	day := func(v string) (time.Time, bool) {
		if v == "" {
			return time.Time{}, true
		}
		t, err := time.ParseInLocation("2006-01-02", v, s.loc())
		return t, err == nil
	}
	from, ok1 := day(form.From)
	to, ok2 := day(form.To)
	if !ok1 || !ok2 || (!from.IsZero() && !to.IsZero() && to.Before(from)) {
		return opts, s.t("Check the dates: the end cannot come before the start.")
	}
	if !to.IsZero() {
		to = to.AddDate(0, 0, 1) // the whole last day
	}
	if form.AuthorID > 0 {
		if _, err := s.store.UserByID(r.Context(), form.AuthorID); err != nil {
			return opts, s.t("Choose an author from the list.")
		}
	}
	st := s.settings()
	opts.From, opts.To, opts.AuthorID = from, to, form.AuthorID
	opts.Media, opts.PendingComments, opts.PersonalData = form.Media, form.PendingComments, form.PersonalData
	opts.Site = export.Site{URL: s.cfg.BaseURL.String(), Title: st.SiteTitle, Description: st.Tagline, Language: st.Language,
		Location: s.loc(), Generator: "sap13ns " + Version, UploadsDir: s.cfg.UploadsDir}
	return opts, ""
}

// exportFilename names a download after the site's host and the time.
func (s *Server) exportFilename(ext string, at time.Time) string {
	host := strings.NewReplacer(":", "-", "/", "-").Replace(s.cfg.BaseURL.Host)
	return fmt.Sprintf("sap13ns-%s-%s.%s", host, at.UTC().Format("20060102-150405"), ext)
}

// sentWriter records whether anything reached the client.
type sentWriter struct {
	w    io.Writer
	sent bool
}

func (c *sentWriter) Write(p []byte) (int, error) {
	if len(p) > 0 {
		c.sent = true
	}
	return c.w.Write(p)
}

func exportHeaders(w http.ResponseWriter, ctype, filename string) {
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no") // nginx: stream, do not spool to disk
}

// streamExport writes the export as the response.
func (s *Server) streamExport(w http.ResponseWriter, r *http.Request, form exportForm, opts export.Options) {
	ctype, ext := "application/xml; charset=utf-8", "xml"
	if form.Format == "zip" {
		ctype, ext = "application/zip", "zip"
	}
	exportHeaders(w, ctype, s.exportFilename(ext, time.Now()))
	// A large site takes longer than the server's write timeout.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(time.Hour))
	sw := &sentWriter{w: w}
	var err error
	if form.Format == "zip" {
		_, err = export.WriteZip(r.Context(), s.store, sw, opts)
	} else {
		_, err = export.WriteWXR(r.Context(), s.store, sw, opts)
	}
	if err == nil {
		return
	}
	if r.Context().Err() != nil {
		return // the browser went away
	}
	s.log.Error("export", "format", form.Format, "err", err)
	if !sw.sent {
		h := w.Header()
		h.Del("Content-Disposition")
		h.Del("X-Accel-Buffering")
		msg := s.t("The export failed. The error has been logged.")
		if errors.Is(err, export.ErrTooLarge) {
			msg = s.t("The export is larger than the export limit. Leave out the media library, or export part of the content.")
		}
		s.renderExport(w, r, http.StatusInternalServerError, form, msg)
		return
	}
	// Part of the file is out: break the connection, so that the browser
	// reports a failed download instead of keeping a truncated file.
	panic(http.ErrAbortHandler)
}

func (s *Server) startExportJob(w http.ResponseWriter, r *http.Request, form exportForm, opts export.Options, details map[string]string) {
	dir := s.exportDir()
	if pathInside(dir, s.cfg.UploadsDir) {
		// SAP13NS_UPLOADS_DIR is the data directory: the file would be public.
		s.renderExport(w, r, http.StatusConflict, form, s.t("Large exports need an export directory outside the uploads directory. Ask the server administrator."))
		return
	}
	u := currentUser(r)
	j := &s.exports
	j.mu.Lock()
	if j.running {
		j.mu.Unlock()
		s.renderExport(w, r, http.StatusConflict, form, s.t("An export is already running. Try again when it has finished."))
		return
	}
	now := time.Now()
	j.running, j.started, j.finished, j.user, j.userID, j.report, j.err = true, now, time.Time{}, u.Name(), u.ID, nil, ""
	j.mu.Unlock()
	s.audit(r, u.ID, "export.created", form.Format, details)
	name := export.NewName(u.ID, now)
	s.goJob(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Hour)
		defer cancel()
		rep, err := s.writeExportFile(ctx, dir, name, u.ID, opts)
		j.mu.Lock()
		j.running, j.finished, j.report = false, time.Now(), rep
		if err != nil {
			j.err = s.t("The export failed. The error has been logged.")
			if errors.Is(err, export.ErrTooLarge) {
				j.err = s.t("The export is larger than the export limit. Leave out the media library, or export part of the content.")
			}
		}
		j.mu.Unlock()
		if err != nil {
			s.log.Error("background export", "err", err)
		} else {
			s.log.Info("export ready", "files", rep.Files, "posts", rep.Posts, "pages", rep.Pages)
		}
	})
	s.redirect(w, r, "/admin/tools/export", "success", s.t("The export is being prepared. This page shows the download link when it is ready."))
}

// writeExportFile writes a .zip export to dir/name through a partial file,
// and keeps only the newest finished export of the user.
func (s *Server) writeExportFile(ctx context.Context, dir, name string, userID int64, opts export.Options) (*export.Report, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	part := filepath.Join(dir, name+export.PartSuffix)
	f, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	rep, err := export.WriteZip(ctx, s.store, f, opts)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(part)
		return rep, err
	}
	older, _ := export.List(dir, time.Now())
	for _, e := range older {
		if e.UserID == userID {
			os.Remove(filepath.Join(dir, e.Name))
		}
	}
	return rep, os.Rename(part, filepath.Join(dir, name))
}

// adminExportDownload sends a finished export once. The link must carry a
// valid signature less than export.TTL old, and belong to the user; the
// file is claimed by a rename, so a second request finds nothing.
func (s *Server) adminExportDownload(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	userID, created, ok := export.ParseName(name)
	u := currentUser(r)
	if !ok || userID != u.ID {
		s.adminError(w, r, http.StatusNotFound, s.t("This export does not exist."))
		return
	}
	dir := s.exportDir()
	gone := func() {
		s.adminError(w, r, http.StatusGone, s.t("This download link has expired or has already been used. Start a new export."))
	}
	if time.Since(created) >= export.TTL {
		os.Remove(filepath.Join(dir, name))
		gone()
		return
	}
	if _, ok := s.keys.Verify(exportPurpose, name, r.URL.Query().Get("t"), export.TTL); !ok {
		s.adminError(w, r, http.StatusForbidden, s.t("This download link is not valid."))
		return
	}
	claim := filepath.Join(dir, name+export.ClaimSuffix)
	if err := os.Rename(filepath.Join(dir, name), claim); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			s.log.Error("claim export", "err", err)
		}
		gone()
		return
	}
	f, err := os.Open(claim)
	os.Remove(claim) // an open file can still be read
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, u.ID, "export.downloaded", name, map[string]string{"bytes": strconv.FormatInt(info.Size(), 10)})
	exportHeaders(w, "application/zip", s.exportFilename("zip", created))
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(2 * time.Hour))
	if _, err := io.Copy(w, f); err != nil && r.Context().Err() == nil {
		s.log.Warn("send export", "err", err)
	}
}

// pathInside reports whether dir is parent or below it, lexically or once
// symlinks are resolved.
func pathInside(dir, parent string) bool {
	within := func(d, p string) bool {
		rel, err := filepath.Rel(p, d)
		return err == nil && filepath.IsLocal(rel)
	}
	if d, err1 := filepath.Abs(dir); err1 == nil {
		if p, err2 := filepath.Abs(parent); err2 == nil && within(d, p) {
			return true
		}
	}
	// The export directory may not exist yet: resolve its parent then.
	resolve := func(p string) (string, error) {
		r, err := filepath.EvalSymlinks(p)
		if errors.Is(err, fs.ErrNotExist) {
			if r, err = filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
				r = filepath.Join(r, filepath.Base(p))
			}
		}
		return r, err
	}
	d, err1 := resolve(dir)
	p, err2 := filepath.EvalSymlinks(parent)
	return err1 == nil && err2 == nil && within(d, p)
}

// SweepExports removes the expired exports of the site.
func (s *Server) SweepExports(now time.Time) {
	if n, err := export.Sweep(s.exportDir(), now); err != nil {
		s.log.Error("sweep exports", "err", err)
	} else if n > 0 {
		s.log.Info("expired exports removed", "count", n)
	}
}
