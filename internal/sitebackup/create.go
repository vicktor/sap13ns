package sitebackup

import (
	"archive/zip"
	"bufio"
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"sap13ns/internal/backup"
	"sap13ns/internal/export"
	"sap13ns/internal/store"
)

// Limits, as the database backups have them (internal/backup), so that
// Create never writes what Restore refuses. Variables so tests can lower
// them.
var (
	maxFiles      = 500_000
	maxFileBytes  = int64(64 << 30)  // one upload
	maxWXRBytes   = int64(2 << 30)   // export.xml (export.DefaultMaxXMLBytes)
	maxJSONBytes  = int64(256 << 20) // one sidecar file
	maxTotalBytes = int64(4 << 40)
	freeSpace     = export.FreeSpace
)

// Manifest describes an archive (sap13ns/manifest.json).
type Manifest struct {
	Format     int             `json:"format"`
	Kind       string          `json:"kind"` // "sap13ns-site-backup"
	AppVersion string          `json:"app_version"`
	Created    time.Time       `json:"created"`
	Site       ManifestSite    `json:"site"`
	PreRestore bool            `json:"pre_restore,omitempty"`
	WPBase     string          `json:"wp_base,omitempty"`
	Counts     Counts          `json:"counts"`
	Files      []backup.Member `json:"files"` // every other member
	TotalBytes int64           `json:"total_bytes"`
}

// ManifestSite is the site a backup was made of.
type ManifestSite struct {
	ID       int64    `json:"id,omitempty"`
	Label    string   `json:"label"`
	URL      string   `json:"url"`
	Title    string   `json:"title"`
	Language string   `json:"language"`
	Langs    []string `json:"languages,omitempty"`
	Network  string   `json:"network,omitempty"`
}

// Counts are what a backup holds.
type Counts struct {
	Posts, Pages, Attachments, Comments int
	Categories, Tags, Menus, MenuItems  int
	Users, Widgets, Redirects, Files    int
}

const manifestKind = "sap13ns-site-backup"

// The sidecar files.
type (
	sideSettings struct {
		Site         json.RawMessage              `json:"site"`
		AI           json.RawMessage              `json:"ai"`
		Assistant    json.RawMessage              `json:"assistant"`
		MCPToken     bool                         `json:"mcp_token,omitempty"` // the assistant had a token, left out
		ThemeOptions map[string]map[string]string `json:"theme_options"`
	}
	sideUser struct {
		ID               int64     `json:"id"`
		Username         string    `json:"username"`
		Email            string    `json:"email"`
		DisplayName      string    `json:"display_name"`
		Bio              string    `json:"bio,omitempty"`
		Website          string    `json:"website,omitempty"`
		Role             string    `json:"role"`
		Status           string    `json:"status"`
		SSOOnly          bool      `json:"sso_only,omitempty"`
		NetworkAccountID *int64    `json:"network_account_id,omitempty"`
		NetworkOperator  bool      `json:"network_operator,omitempty"`
		CreatedAt        time.Time `json:"created_at"`
	}
	sideAppearance struct {
		Custom    []sideCustom                `json:"custom"`
		Revisions []store.ThemeCustomRevision `json:"revisions"`
	}
	sideCustom struct {
		Theme       string          `json:"theme"`
		Kind        string          `json:"kind"`
		Published   json.RawMessage `json:"published"`
		Draft       json.RawMessage `json:"draft,omitempty"`
		PublishedAt *time.Time      `json:"published_at,omitempty"`
		DraftAt     *time.Time      `json:"draft_at,omitempty"`
		UpdatedBy   *int64          `json:"updated_by,omitempty"`
	}
	sidePlugin struct {
		ID       string            `json:"id"`
		Version  string            `json:"version"`
		Enabled  bool              `json:"enabled"`
		Source   string            `json:"source"`
		Settings map[string]string `json:"settings"`
	}
)

// Create makes a backup of the site in o.Dir and records the outcome
// (ReadStatus). It refuses with ErrBusy while another backup or restore
// of the site runs, and with ErrLowDisk when the disk lacks twice the
// estimated size.
func Create(ctx context.Context, st *store.Store, o Options) (*Archive, *Manifest, error) {
	unlock, err := lock(o.Dir)
	if err != nil {
		return nil, nil, err
	}
	defer unlock()
	a, m, err := create(ctx, st, o, false)
	if rerr := record(o.Dir, o.now(), a, err); rerr != nil && err == nil {
		err = rerr
	}
	return a, m, err
}

// Estimate is about what a backup of the site takes on disk, before
// compression: the uploads, the export, the sidecar.
func Estimate(ctx context.Context, st *store.Store, uploadsDir string) (int64, error) {
	n, err := export.Estimate(ctx, st, exportOptions(Options{UploadsDir: uploadsDir}))
	return n + 1<<20, err
}

// CheckSpace returns ErrLowDisk unless dir's file system has twice the
// estimated size of a backup free.
func CheckSpace(ctx context.Context, st *store.Store, dir, uploadsDir string) error {
	need, err := Estimate(ctx, st, uploadsDir)
	if err != nil {
		return err
	}
	if err := ensureDir(dir); err != nil {
		return err
	}
	free, err := freeSpace(dir)
	if err != nil {
		return err
	}
	if free >= 0 && free < 2*need {
		return fmt.Errorf("%w: %d MiB free, %d MiB needed", ErrLowDisk, free>>20, 2*need>>20)
	}
	return nil
}

func exportOptions(o Options) export.Options {
	return export.Options{
		Statuses: []string{store.StatusPublished, store.StatusDraft, store.StatusPending, store.StatusPrivate, store.StatusTrash},
		Media:    true, PendingComments: true, PersonalData: true, WPBase: o.WPBase, Now: o.Now,
		Site:        export.Site{URL: o.Site.URL, Title: o.Site.Title, UploadsDir: o.UploadsDir, Generator: "sap13ns " + o.AppVersion},
		MaxXMLBytes: maxWXRBytes, MaxFiles: maxFiles, MaxBytes: maxTotalBytes,
	}
}

func create(ctx context.Context, st *store.Store, o Options, preRestore bool) (*Archive, *Manifest, error) {
	if o.Dir == "" || o.Site.URL == "" {
		return nil, nil, errors.New("sitebackup: the backups directory and the site address are required")
	}
	if o.Passphrase != "" {
		if err := backup.CheckPassphrase(o.Passphrase); err != nil {
			return nil, nil, err
		}
	}
	if err := CheckSpace(ctx, st, o.Dir, o.UploadsDir); err != nil {
		return nil, nil, err
	}
	// One snapshot: the archive holds the site as it was at one moment,
	// while it stays online.
	if b, ok := st.DB.(interface {
		BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
	}); ok {
		tx, err := b.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
		if err != nil {
			return nil, nil, err
		}
		defer tx.Rollback(context.WithoutCancel(ctx))
		st = st.WithTx(tx)
	}
	set, err := st.LoadSettings(ctx)
	if err != nil {
		return nil, nil, err
	}
	// Every archive of the directory has its own second, so that the
	// newest is always the latest made.
	now := o.now().Truncate(time.Second)
	existing, err := List(o.Dir)
	if err != nil {
		return nil, nil, err
	}
	for _, a := range existing { // newest first
		if !a.Created.Before(now) && a.Created.Sub(now) < time.Minute {
			now = a.Created.Add(time.Second)
		}
	}
	name := archiveName(o.Site.Label, now, preRestore, o.Passphrase != "")
	if _, err := os.Lstat(filepath.Join(o.Dir, name)); !errors.Is(err, fs.ErrNotExist) {
		return nil, nil, fmt.Errorf("sitebackup: %s exists", name)
	}
	final := filepath.Join(o.Dir, name)
	f, err := os.CreateTemp(o.Dir, ".partial-*")
	if err != nil {
		return nil, nil, err
	}
	tmp := f.Name()
	defer func() {
		if f != nil {
			f.Close()
			os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		return nil, nil, err
	}
	var out io.Writer = f
	var enc io.WriteCloser
	if o.Passphrase != "" {
		if enc, err = backup.Encrypt(f, o.Passphrase); err != nil {
			return nil, nil, err
		}
		out = enc
	}
	bw := bufio.NewWriterSize(out, 256<<10)
	w := &writer{zw: zip.NewWriter(bw), now: now}
	if o.Site.Title == "" {
		o.Site.Title = set.SiteTitle
	}
	m := &Manifest{Format: FormatVersion, Kind: manifestKind, AppVersion: o.AppVersion, Created: now, PreRestore: preRestore,
		WPBase: strings.TrimRight(o.WPBase, "/"),
		Site: ManifestSite{ID: o.Site.ID, Label: cleanLabel(o.Site.Label), URL: o.Site.URL, Title: o.Site.Title,
			Language: set.Language, Langs: set.Languages, Network: o.Site.Network}}
	if err := w.content(ctx, st, o, set, m); err != nil {
		return nil, nil, err
	}
	m.Files = w.files
	for _, f := range w.files {
		m.TotalBytes += f.Size
	}
	if err := w.json(NameManifest, m); err != nil {
		return nil, nil, err
	}
	if err := w.zw.Close(); err != nil {
		return nil, nil, err
	}
	if err := bw.Flush(); err != nil {
		return nil, nil, err
	}
	if enc != nil {
		if err := enc.Close(); err != nil {
			return nil, nil, err
		}
	}
	if err := f.Sync(); err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	if err := f.Close(); err != nil {
		return nil, nil, err
	}
	f = nil
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return nil, nil, err
	}
	syncDir(o.Dir)
	return &Archive{Name: name, Path: final, Created: now, Size: info.Size(), Encrypted: enc != nil, PreRestore: preRestore}, m, nil
}

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}

// writer adds members to the archive and records their size and hash.
type writer struct {
	zw    *zip.Writer
	now   time.Time
	files []backup.Member
	total int64
}

type counter struct{ n int64 }

func (c *counter) Write(p []byte) (int, error) { c.n += int64(len(p)); return len(p), nil }

func (w *writer) add(name string, deflate bool, mod time.Time, limit int64, fn func(io.Writer) error) error {
	if !backup.SafeName(name) {
		return fmt.Errorf("sitebackup: unsafe member name %q", name)
	}
	h := &zip.FileHeader{Name: name, Method: zip.Store, Modified: mod}
	if deflate {
		h.Method = zip.Deflate
	}
	h.SetMode(0o644)
	zf, err := w.zw.CreateHeader(h)
	if err != nil {
		return err
	}
	sum, n := sha256.New(), &counter{}
	if err := fn(io.MultiWriter(zf, sum, n)); err != nil {
		return err
	}
	if n.n > limit {
		return fmt.Errorf("sitebackup: %s is larger than %d bytes", name, limit)
	}
	if w.total += n.n; w.total > maxTotalBytes {
		return fmt.Errorf("sitebackup: the backup is larger than %d bytes", maxTotalBytes)
	}
	if name != NameManifest {
		w.files = append(w.files, backup.Member{Name: name, Size: n.n, SHA256: hex.EncodeToString(sum.Sum(nil))})
	}
	return nil
}

func (w *writer) json(name string, v any) error {
	return w.add(name, true, w.now, maxJSONBytes, func(out io.Writer) error {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		return enc.Encode(v)
	})
}

func (w *writer) content(ctx context.Context, st *store.Store, o Options, set store.Settings, m *Manifest) error {
	eo := exportOptions(o)
	eo.Site.Language, eo.Site.Title = set.Language, m.Site.Title
	var rep *export.Report
	if err := w.add(NameWXR, true, w.now, maxWXRBytes, func(out io.Writer) error {
		var err error
		rep, err = export.WriteWXR(ctx, st, out, eo)
		return err
	}); err != nil {
		return fmt.Errorf("export.xml: %w", err)
	}
	m.Counts = Counts{Posts: rep.Posts, Pages: rep.Pages, Attachments: rep.Attachments, Comments: rep.Comments,
		Categories: rep.Categories, Tags: rep.Tags, Menus: rep.Menus, MenuItems: rep.MenuItems}
	n, err := w.uploads(ctx, o.UploadsDir)
	if err != nil {
		return fmt.Errorf("uploads: %w", err)
	}
	m.Counts.Files = n
	if err := w.sidecar(ctx, st, o, m); err != nil {
		return err
	}
	return w.add(NameReadme, true, w.now, 1<<20, func(out io.Writer) error {
		_, err := io.WriteString(out, readme(m))
		return err
	})
}

// uploads adds the regular files of the uploads directory under
// wp-content/uploads/. Symbolic links, special files, the uploader's
// temporary files and names an archive cannot carry are left out.
func (w *writer) uploads(ctx context.Context, dir string) (int, error) {
	if dir == "" {
		return 0, nil
	}
	root, err := os.OpenRoot(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer root.Close()
	n := 0
	err = fs.WalkDir(root.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		switch t := d.Type(); {
		case rel == ".":
			return nil
		case t.IsDir():
			if rel == ".tmp" || strings.HasPrefix(path.Base(rel), ".") {
				return fs.SkipDir
			}
			return nil
		case !t.IsRegular(), strings.HasSuffix(rel, ".tmp"), strings.HasPrefix(path.Base(rel), "."), !backup.SafeName(DirUploads + rel):
			return nil
		}
		if n >= maxFiles {
			return fmt.Errorf("more than %d files", maxFiles)
		}
		info, err := d.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		f, err := root.Open(rel)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		defer f.Close()
		n++
		return w.add(DirUploads+rel, compressible(rel), info.ModTime(), maxFileBytes, func(out io.Writer) error {
			// Exactly the size seen: a file that grows meanwhile is cut.
			_, err := io.Copy(out, io.LimitReader(f, info.Size()))
			return err
		})
	})
	return n, err
}

// compressible reports whether deflating a file is worth it.
func compressible(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".avif", ".heic", ".mp4", ".m4v", ".webm", ".mov",
		".mp3", ".m4a", ".ogg", ".oga", ".opus", ".flac", ".zip", ".gz", ".woff", ".woff2", ".pdf":
		return false
	}
	return true
}

func (w *writer) sidecar(ctx context.Context, st *store.Store, o Options, m *Manifest) error {
	// Settings, without secrets.
	set, err := st.LoadSettings(ctx)
	if err != nil {
		return err
	}
	ai, err := st.AISettings(ctx)
	if err != nil {
		return err
	}
	ai.APIKeyEnc = nil
	as, err := st.AssistantSettings(ctx)
	if err != nil {
		return err
	}
	token := as.MCPTokenHash != ""
	as.EmbedKeyEnc, as.MCPTokenHash = nil, ""
	themes, err := st.AllThemeOptions(ctx)
	if err != nil {
		return err
	}
	ss := sideSettings{MCPToken: token, ThemeOptions: themes}
	for _, v := range []struct {
		dst *json.RawMessage
		src any
	}{{&ss.Site, set}, {&ss.AI, ai}, {&ss.Assistant, as}} {
		if *v.dst, err = json.Marshal(v.src); err != nil {
			return err
		}
	}
	if err := w.json(nameSettings, ss); err != nil {
		return err
	}

	// Users and roles; never a password hash, second factor or passkey.
	users, err := st.AllUsers(ctx)
	if err != nil {
		return err
	}
	su := make([]sideUser, 0, len(users))
	for _, u := range users {
		su = append(su, sideUser{ID: u.ID, Username: u.Username, Email: u.Email, DisplayName: u.DisplayName, Bio: u.Bio,
			Website: u.Website, Role: u.Role, Status: u.Status, SSOOnly: u.SSOOnly(), NetworkAccountID: u.NetworkAccountID,
			NetworkOperator: u.NetworkOperator, CreatedAt: u.CreatedAt})
	}
	m.Counts.Users = len(su)
	if err := w.json(nameUsers, su); err != nil {
		return err
	}

	// The media library, row by row.
	var media []store.Media
	for after := int64(0); ; {
		list, err := st.AllMedia(ctx, after, 1000)
		if err != nil {
			return err
		}
		if len(list) == 0 {
			break
		}
		media = append(media, list...)
		after = list[len(list)-1].ID
	}
	if media == nil {
		media = []store.Media{}
	}
	if err := w.json(nameMedia, media); err != nil {
		return err
	}

	menus, err := st.AllMenus(ctx)
	if err != nil {
		return err
	}
	var items []store.MenuItem
	for _, list := range menus {
		items = append(items, list...)
	}
	sortByID(items, func(it store.MenuItem) int64 { return it.ID })
	if err := w.json(nameMenus, nonNil(items)); err != nil {
		return err
	}

	widgets, err := st.AllWidgets(ctx)
	if err != nil {
		return err
	}
	var ws []store.Widget
	for _, list := range widgets {
		ws = append(ws, list...)
	}
	sortByID(ws, func(w store.Widget) int64 { return w.ID })
	m.Counts.Widgets = len(ws)
	if err := w.json(nameWidgets, nonNil(ws)); err != nil {
		return err
	}

	custom, err := st.AllThemeCustom(ctx)
	if err != nil {
		return err
	}
	revs, err := st.AllThemeCustomRevisions(ctx)
	if err != nil {
		return err
	}
	ap := sideAppearance{Custom: []sideCustom{}, Revisions: nonNil(revs)}
	for _, c := range custom {
		ap.Custom = append(ap.Custom, sideCustom{Theme: c.Theme, Kind: c.Kind, Published: c.Published, Draft: c.Draft,
			PublishedAt: c.PublishedAt, DraftAt: c.DraftAt, UpdatedBy: c.UpdatedBy})
	}
	if err := w.json(nameAppear, ap); err != nil {
		return err
	}

	redirects, err := st.Redirects(ctx)
	if err != nil {
		return err
	}
	m.Counts.Redirects = len(redirects)
	if err := w.json(nameRedirect, nonNil(redirects)); err != nil {
		return err
	}

	recs, err := st.ListPluginRecords(ctx)
	if err != nil {
		return err
	}
	plugins := []sidePlugin{}
	for _, r := range recs {
		keep := plainSettings
		if o.PluginSettings != nil {
			keep = o.PluginSettings
		}
		plugins = append(plugins, sidePlugin{ID: r.ID, Version: r.Version, Enabled: r.Enabled, Source: r.Source,
			Settings: plainSettings(r.ID, keep(r.ID, r.Settings))})
	}
	sortByKey(plugins, func(p sidePlugin) string { return p.ID })
	return w.json(namePlugins, plugins)
}

// plainSettings keeps the settings of a plugin that are neither sealed
// secrets ("enc1:"), nor e-mail addresses an administrator saved (and
// their markers), nor named like a secret. It also runs after an
// Options.PluginSettings, as a floor.
func plainSettings(_ string, in map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		lower := strings.ToLower(k)
		switch {
		case strings.HasPrefix(v, "enc1:"), strings.HasPrefix(k, "@email:"), in["@email:"+k] != "":
		case strings.Contains(lower, "secret"), strings.Contains(lower, "password"), strings.Contains(lower, "token"),
			strings.Contains(lower, "apikey"), strings.Contains(lower, "api_key"), strings.Contains(lower, "api-key"):
		default:
			out[k] = v
		}
	}
	return out
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func sortByID[T any](s []T, id func(T) int64) {
	slices.SortFunc(s, func(a, b T) int { return cmp.Compare(id(a), id(b)) })
}

func sortByKey[T any](s []T, key func(T) string) {
	slices.SortFunc(s, func(a, b T) int { return cmp.Compare(key(a), key(b)) })
}

// randomSuffix names a staging directory.
func randomSuffix() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}
