package theme

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Uploads. A theme archive is unpacked into a staging folder of the site's
// own themes directory, validated there (validate.go and check.go), and
// renamed into place only when nothing is wrong. Replacing a theme keeps
// the old folder until the new one has loaded, so a failure puts it back.
// Uploaded themes are never activated by the upload.

// A Quota bounds the bytes a site stores (media.Quota satisfies it):
// Reserve fails, with media.ErrQuota, when n bytes do not fit.
type Quota interface {
	Reserve(ctx context.Context, n int64) error
	Release(ctx context.Context, n int64)
}

// UploadInfo is what an upload records in its theme's folder
// (uploadFile, a hidden name no archive can carry).
type UploadInfo struct {
	SHA256     string    `json:"sha256"` // of the archive
	UploadedAt time.Time `json:"uploaded_at"`
	UserID     int64     `json:"user_id,omitempty"` // 0 for the command line
	Source     string    `json:"source"`            // "upload" | "cli" | "editor" (theme code editor)
	// ImportedFrom is the kind of theme it was converted from
	// (internal/themeimport: "wordpress-block"); "" for a sap13ns theme.
	ImportedFrom string `json:"imported_from,omitempty"`
}

const uploadFile = ".sap13ns-upload.json"

func readUploadInfo(fsys fs.FS) *UploadInfo {
	raw, err := fs.ReadFile(fsys, uploadFile)
	if err != nil || len(raw) > 4<<10 {
		return nil
	}
	var info UploadInfo
	if json.Unmarshal(raw, &info) != nil {
		return nil
	}
	return &info
}

// InstallOptions describe who installs a theme.
type InstallOptions struct {
	UserID int64
	Source string // "upload" | "cli" | "editor"
	// ImportedFrom: the archive was converted from another CMS's theme
	// (themeimport's Report.Source).
	ImportedFrom string
	// Quota counts the theme's files against the site's storage; nil on a
	// single-site install and for the command line.
	Quota Quota
}

// InstallResult describes an installed theme.
type InstallResult struct {
	Slug, Name, Version string
	Previous            string // the version it replaced; "" for a new theme
	Replaced            bool
	Bytes               int64  // on disk
	SHA256              string // of the archive
}

var (
	// ErrNoUploads: the manager has no directory of its own.
	ErrNoUploads = errors.New("this site has no themes directory of its own")
	// ErrActive: the active theme cannot be deleted.
	ErrActive = errors.New("the active theme cannot be deleted")
	// ErrNotOwn: the theme is built in, the network's, or not installed.
	ErrNotOwn = errors.New("only the themes of the site's own directory can be deleted")
)

// ownDir is the site's own themes directory.
func (m *Manager) ownDir() string {
	for i := len(m.sources) - 1; i >= 0; i-- {
		if m.sources[i].Dir != "" {
			return m.sources[i].Dir
		}
	}
	return ""
}

// taken reports whether a built-in or network theme has the slug, loaded
// or not, and which.
func (m *Manager) taken(slug string) (builtin, network bool) {
	for _, src := range m.sources {
		if src.Dir != "" {
			continue
		}
		fsys := sourceFS(src)
		if fsys == nil {
			continue
		}
		if fi, err := fs.Stat(fsys, slug); err == nil && fi.IsDir() {
			if src.Builtin {
				builtin = true
			} else {
				network = true
			}
		}
	}
	return builtin, network
}

// Install validates a theme archive and installs it into the site's own
// directory, replacing an earlier upload of the same slug. It never
// activates it. A refused archive returns a *Refusal (errors.As) and
// changes nothing; a quota error comes from opts.Quota.
func (m *Manager) Install(ctx context.Context, r io.ReaderAt, size int64, opts InstallOptions) (*InstallResult, error) {
	dir := m.ownDir()
	if dir == "" {
		return nil, ErrNoUploads
	}
	m.installMu.Lock()
	defer m.installMu.Unlock()
	sweepLeftovers(dir, m.log)
	sum := sha256.New()
	if _, err := io.Copy(sum, io.NewSectionReader(r, 0, size)); err != nil {
		return nil, err
	}
	res := &InstallResult{SHA256: hex.EncodeToString(sum.Sum(nil))}

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	staging, err := os.MkdirTemp(dir, ".staging-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(staging) // a no-op once renamed into place
	root := filepath.Join(staging, "theme")
	if err := os.Mkdir(root, 0o750); err != nil {
		return nil, err
	}
	v, err := m.validate(r, size, root)
	if err != nil {
		return nil, err
	}
	if len(v.problems) > 0 {
		return res, &Refusal{Slug: v.slug, Problems: v.problems}
	}
	res.Slug, res.Name, res.Version = v.slug, v.theme.Name, v.theme.Version

	target := filepath.Join(dir, v.slug)
	if dirExists(target) {
		res.Replaced = true
		if raw, err := os.ReadFile(filepath.Join(target, "theme.json")); err == nil {
			var prev struct {
				Version string `json:"version"`
			}
			json.Unmarshal(raw, &prev)
			res.Previous = prev.Version
		}
	}
	info, err := json.Marshal(UploadInfo{SHA256: res.SHA256, UploadedAt: time.Now().UTC().Truncate(time.Second),
		UserID: opts.UserID, Source: opts.Source, ImportedFrom: opts.ImportedFrom})
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(root, uploadFile), info, 0o640); err != nil {
		return nil, err
	}

	// The files count against the site's storage: what the theme adds
	// over the version it replaces, claimed before anything changes.
	newBytes, err := treeBytes(root)
	if err != nil {
		return nil, err
	}
	oldBytes, err := treeBytes(target)
	if err != nil {
		return nil, err
	}
	res.Bytes = newBytes
	grow := newBytes - oldBytes
	if opts.Quota != nil && grow > 0 {
		if err := opts.Quota.Reserve(ctx, grow); err != nil {
			return res, err
		}
	}
	release := func(n int64) {
		if opts.Quota != nil && n > 0 {
			opts.Quota.Release(context.WithoutCancel(ctx), n)
		}
	}

	// Into place: the old folder is kept aside until the new theme loads.
	backup := ""
	if dirExists(target) {
		backup = filepath.Join(dir, ".old-"+v.slug+"-"+randomSuffix())
		if err := os.Rename(target, backup); err != nil {
			release(grow)
			return nil, err
		}
	}
	restore := func() {
		os.RemoveAll(target)
		if backup != "" {
			os.Rename(backup, target)
		}
	}
	if err := os.Chmod(root, 0o750); err != nil {
		restore()
		release(grow)
		return nil, err
	}
	if err := os.Rename(root, target); err != nil {
		restore()
		release(grow)
		return nil, err
	}
	loadErr := m.Load()
	if t, ok := m.Get(v.slug); loadErr != nil || !ok || !t.Uploaded || t.Upload.SHA256 != res.SHA256 {
		// It validated, so this is not expected; the old version stays.
		restore()
		release(grow)
		if err := m.Load(); err != nil {
			m.log.Error("reload the themes after a failed install", "err", err)
		}
		return nil, fmt.Errorf("the theme was validated but did not load: %v", errors.Join(loadErr, m.failure(v.slug)))
	}
	if backup != "" {
		if err := os.RemoveAll(backup); err != nil {
			m.log.Warn("remove the replaced theme", "theme", v.slug, "err", err)
		}
	}
	release(-grow) // a smaller version gives bytes back
	return res, nil
}

// leftoverAge is how old a staging, replaced or deleted folder must be
// before an install sweeps it: younger ones may belong to an install that
// is running in another process (the command line beside the server).
const leftoverAge = time.Hour

// sweepLeftovers cleans up after an install or a removal that a crash
// interrupted: staging and deleted folders go, and a replaced version
// whose new one never arrived is put back.
func sweepLeftovers(dir string, log *slog.Logger) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || !strings.HasPrefix(name, ".") {
			continue
		}
		fi, err := e.Info()
		if err != nil || time.Since(fi.ModTime()) < leftoverAge {
			continue
		}
		full := filepath.Join(dir, name)
		switch {
		case strings.HasPrefix(name, ".staging-"), strings.HasPrefix(name, ".trash-"):
		case strings.HasPrefix(name, ".old-"):
			// .old-<slug>-<suffix>
			slug := strings.TrimPrefix(name, ".old-")
			if i := strings.LastIndexByte(slug, '-'); i > 0 {
				slug = slug[:i]
			}
			if slugRe.MatchString(slug) && !dirExists(filepath.Join(dir, slug)) {
				if os.Rename(full, filepath.Join(dir, slug)) == nil {
					log.Warn("restored a theme an interrupted install had moved aside", "theme", slug)
					continue
				}
			}
		default:
			continue
		}
		if err := os.RemoveAll(full); err != nil {
			log.Warn("remove a leftover theme folder", "folder", name, "err", err)
		}
	}
}

func (m *Manager) failure(slug string) error {
	for _, f := range m.Failures() {
		if f.Slug == slug {
			return errors.New(f.Err)
		}
	}
	return nil
}

// Remove deletes a theme of the site's own directory, unless it is the
// active one, and gives its bytes back to q (nil for none).
func (m *Manager) Remove(ctx context.Context, slug, active string, q Quota) (int64, error) {
	dir := m.ownDir()
	if dir == "" {
		return 0, ErrNoUploads
	}
	if !slugRe.MatchString(slug) {
		return 0, ErrNotOwn
	}
	m.installMu.Lock()
	defer m.installMu.Unlock()
	target := filepath.Join(dir, slug)
	fi, err := os.Lstat(target)
	if err != nil || !fi.IsDir() {
		return 0, ErrNotOwn
	}
	if slug == active {
		// Unless a built-in or network theme of that slug is the active
		// one, and this folder, which it shadows, never loaded.
		m.mu.RLock()
		th, ok := m.themes[slug]
		m.mu.RUnlock()
		if !ok || th.Own {
			return 0, ErrActive
		}
	}
	size, err := treeBytes(target)
	if err != nil {
		return 0, err
	}
	trash := filepath.Join(dir, ".trash-"+slug+"-"+randomSuffix())
	if err := os.Rename(target, trash); err != nil {
		return 0, err
	}
	if err := os.RemoveAll(trash); err != nil {
		m.log.Warn("remove a deleted theme's files", "theme", slug, "err", err)
	}
	if q != nil && size > 0 {
		q.Release(context.WithoutCancel(ctx), size)
	}
	return size, m.Load()
}

func dirExists(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.IsDir()
}

// treeBytes adds up the sizes of the regular files under dir, without
// following symbolic links; a directory that does not exist is empty.
func treeBytes(dir string) (int64, error) {
	var n int64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.Type().IsRegular() {
			fi, err := d.Info()
			if err != nil {
				return err
			}
			n += fi.Size()
		}
		return nil
	})
	return n, err
}

func randomSuffix() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}
