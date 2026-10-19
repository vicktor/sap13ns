// Package sitebackup makes and restores the backup of one site: a .zip
// that WordPress can use as it is, and that sap13ns restores exactly.
//
// The archive:
//
//	export.xml               WXR 1.2 (internal/export): posts, pages,
//	                         attachments, comments (approved and held),
//	                         authors, categories and tags with their
//	                         hierarchy, menus, featured images, and the
//	                         sap13ns extensions (Markdown, languages,
//	                         translations, redirects)
//	wp-content/uploads/...   the uploaded files, at the paths of the
//	                         attachment URLs (YYYY/MM/name.ext): WordPress's
//	                         own layout
//	sap13ns/settings.json    site settings, AI and assistant settings
//	                         without their keys, theme options
//	sap13ns/users.json       users and roles, without passwords, second
//	                         factors, passkeys or sessions
//	sap13ns/media.json       the media library (sizes, variants, captions)
//	sap13ns/menus.json       menus, per location and language
//	sap13ns/widgets.json     widgets
//	sap13ns/appearance.json  customiser designs and page-builder layouts,
//	                         published, draft and earlier versions
//	sap13ns/redirects.json   redirect rules
//	sap13ns/plugins.json     installed plugins, versions and plain settings
//	README.txt               how to move to WordPress, or restore
//	sap13ns/manifest.json    format, site, date, and the size and SHA-256
//	                         of every other member; written last
//
// WordPress ignores sap13ns/. A backup holds no secrets: no password
// hashes, TOTP secrets, recovery codes, passkeys, sessions, plugin
// secrets, AI or embedding keys, MCP token or site secret. It does hold
// personal data (e-mail and IP addresses of commenters, e-mail addresses
// of users), as the site does. Spam and trashed comments, revisions,
// autosaves, analytics and the audit log are left out.
//
// Archives may be encrypted with the passphrase and format of the
// database backups (internal/backup), and are then named .zip.enc.
//
// A restore (Restore) replaces the site's content with the archive's in
// one transaction, through the importer (internal/wpimport) and the same
// checks as the admin pages, after a safety backup of what it replaces.
package sitebackup

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"sap13ns/internal/backup"
)

// FormatVersion is the version of the archive layout (manifest "format").
const FormatVersion = 1

// Members of an archive.
const (
	NameWXR      = "export.xml"
	DirUploads   = "wp-content/uploads/"
	NameReadme   = "README.txt"
	NameManifest = "sap13ns/manifest.json"
	nameSettings = "sap13ns/settings.json"
	nameUsers    = "sap13ns/users.json"
	nameMedia    = "sap13ns/media.json"
	nameMenus    = "sap13ns/menus.json"
	nameWidgets  = "sap13ns/widgets.json"
	nameAppear   = "sap13ns/appearance.json"
	nameRedirect = "sap13ns/redirects.json"
	namePlugins  = "sap13ns/plugins.json"
)

var (
	// ErrBusy: a backup or restore of the site is running.
	ErrBusy = errors.New("a backup or restore of this site is already running")
	// ErrLowDisk: the disk has too little space for the backup.
	ErrLowDisk = errors.New("not enough free disk space for the backup")
	// ErrNotEmpty: a restore without Replace into a site that has content.
	ErrNotEmpty = errors.New("the site has content: restore with replace to replace it")
	// ErrNotSiteBackup: the file is not a site backup.
	ErrNotSiteBackup = errors.New("not a sap13ns site backup")
	// ErrNotFound: no backup has that name in the directory.
	ErrNotFound = errors.New("backup not found")
)

// Site describes the site a backup is made of or restored into.
type Site struct {
	ID    int64  // network site id; 0 on a single site
	Label string // its name in file names: the slug, or the host of a single site
	URL   string // https://example.com, no trailing slash
	Title string
	// Network is the host of the network portal ("" on a single site). A
	// restore links users to network accounts only on the same network
	// and site.
	Network string
}

// Options make a backup.
type Options struct {
	Dir        string // where the archives of the site go (0700)
	UploadsDir string
	Site       Site
	// WPBase is the address of a WordPress site the content moves to: the
	// media addresses of export.xml point there (export.Options.WPBase).
	WPBase     string
	Passphrase string // "" leaves the archive unencrypted
	AppVersion string
	Now        func() time.Time
	// FreeSpace measures the free space of the file system of a directory
	// (-1: unknown); nil measures it with statfs.
	FreeSpace func(dir string) (int64, error)
	// PluginSettings, if set, keeps the plain settings of a plugin (it
	// knows the manifests); otherwise every sealed secret, e-mail address
	// and setting named like a secret is left out.
	PluginSettings func(id string, settings map[string]string) map[string]string
}

// CheckWPBase checks the address of a WordPress site a backup is made for
// (Options.WPBase): an absolute http or https URL, which may have a path
// (WordPress in a folder), without user, query or fragment. It returns it
// without the trailing slash.
func CheckWPBase(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" ||
		u.Fragment != "" || strings.HasSuffix(raw, "?") || strings.HasSuffix(raw, "#") || len(raw) > 500 ||
		strings.ContainsFunc(raw, func(r rune) bool { return r <= ' ' || r == '"' || r == '\'' || r == '<' || r == '>' || r == '\\' }) {
		return "", fmt.Errorf("the WordPress address must be like https://new.example or https://example.com/blog, got %q", raw)
	}
	return strings.TrimRight(u.Scheme+"://"+u.Host+u.EscapedPath(), "/"), nil
}

func (o Options) now() time.Time {
	if o.Now != nil {
		return o.Now().UTC()
	}
	return time.Now().UTC()
}

// Archive is a backup in a backups directory.
type Archive struct {
	Name       string
	Path       string
	Created    time.Time // UTC, from the name
	Size       int64
	Encrypted  bool
	PreRestore bool // taken before a restore replaced the site
}

const stampLayout = "20060102-150405"

var (
	nameRE   = regexp.MustCompile(`^sap13ns-site-([a-z0-9][a-z0-9.-]{0,99})-(\d{8}-\d{6})(-pre-restore)?\.zip(\.enc)?$`)
	labelBad = regexp.MustCompile(`[^a-z0-9.-]+`)
)

// cleanLabel makes a label safe for a file name.
func cleanLabel(s string) string {
	s = strings.Trim(labelBad.ReplaceAllString(strings.ToLower(s), "-"), "-.")
	if len(s) > 60 {
		s = strings.Trim(s[:60], "-.")
	}
	return cmp.Or(s, "site")
}

func archiveName(label string, t time.Time, preRestore, encrypted bool) string {
	name := "sap13ns-site-" + cleanLabel(label) + "-" + t.UTC().Format(stampLayout)
	if preRestore {
		name += "-pre-restore"
	}
	name += ".zip"
	if encrypted {
		name += backup.EncryptedSuffix
	}
	return name
}

// ParseName reads a backup's file name.
func ParseName(name string) (a Archive, ok bool) {
	m := nameRE.FindStringSubmatch(name)
	if m == nil {
		return a, false
	}
	t, err := time.Parse(stampLayout, m[2])
	if err != nil {
		return a, false
	}
	return Archive{Name: name, Created: t, PreRestore: m[3] != "", Encrypted: m[4] != ""}, true
}

// List returns the backups in dir, newest first. A missing directory has
// none.
func List(dir string) ([]Archive, error) {
	des, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Archive
	for _, d := range des {
		a, ok := ParseName(d.Name())
		if !ok || !d.Type().IsRegular() {
			continue
		}
		info, err := d.Info()
		if err != nil {
			continue // removed meanwhile
		}
		a.Path, a.Size = filepath.Join(dir, d.Name()), info.Size()
		out = append(out, a)
	}
	slices.SortFunc(out, func(a, b Archive) int {
		return cmp.Or(b.Created.Compare(a.Created), cmp.Compare(b.Name, a.Name))
	})
	return out, nil
}

// Find returns the backup named name in dir. Only a name List would show
// is looked up, so no path can be smuggled in.
func Find(dir, name string) (*Archive, error) {
	if _, ok := ParseName(name); !ok {
		return nil, ErrNotFound
	}
	list, err := List(dir)
	if err != nil {
		return nil, err
	}
	for _, a := range list {
		if a.Name == name {
			return &a, nil
		}
	}
	return nil, ErrNotFound
}

// PreRestoreDays is how long the safety backups taken before a restore are
// kept, whatever the schedule.
const PreRestoreDays = 7

// Prune removes the backups that the schedule does not keep: the newest of
// each of the last keepDaily days and keepWeekly ISO weeks (UTC), and the
// newest of all. Safety backups taken before a restore are kept for
// PreRestoreDays, and do not count.
func Prune(dir string, keepDaily, keepWeekly int, now time.Time) (removed []string, err error) {
	if keepDaily < 0 || keepWeekly < 0 {
		return nil, errors.New("prune: keep counts must not be negative")
	}
	list, err := List(dir)
	if err != nil {
		return nil, err
	}
	var regular []Archive
	var drop []Archive
	for _, a := range list {
		switch {
		case !a.PreRestore:
			regular = append(regular, a)
		case now.Sub(a.Created) > PreRestoreDays*24*time.Hour:
			drop = append(drop, a)
		}
	}
	created := make([]time.Time, len(regular))
	for i, a := range regular {
		created[i] = a.Created
	}
	for i, keep := range backup.Keep(created, keepDaily, keepWeekly, 0) {
		if !keep {
			drop = append(drop, regular[i])
		}
	}
	var errs []error
	for _, a := range drop {
		if err := os.Remove(a.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
			continue
		}
		removed = append(removed, a.Path)
	}
	if len(errs) > 0 {
		return removed, fmt.Errorf("prune: %w", errors.Join(errs...))
	}
	return removed, nil
}

// Status is the outcome of the latest backup of a site, kept next to its
// archives (status.json) for the admin pages and the network.
type Status struct {
	Tried   time.Time `json:"tried"`             // the latest attempt
	OK      time.Time `json:"ok,omitzero"`       // the latest backup made
	Result  string    `json:"result"`            // ResultOK, ResultFailed or ResultSkipped
	Error   string    `json:"error,omitempty"`   // why it failed or was skipped
	Archive string    `json:"archive,omitempty"` // the latest backup made
	Bytes   int64     `json:"bytes,omitempty"`
}

// Results of a backup.
const (
	ResultOK      = "ok"
	ResultFailed  = "failed"
	ResultSkipped = "skipped" // not enough disk space
)

const statusName = "status.json"

// ReadStatus returns the status of the backups in dir; a directory without
// one has the zero Status.
func ReadStatus(dir string) (Status, error) {
	var s Status
	b, err := os.ReadFile(filepath.Join(dir, statusName))
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if len(b) > 64<<10 {
		return s, errors.New("status file too large")
	}
	return s, json.Unmarshal(b, &s)
}

// record writes the outcome of an attempt, keeping the latest success.
func record(dir string, at time.Time, a *Archive, cause error) error {
	s, _ := ReadStatus(dir)
	s.Tried = at
	switch {
	case cause == nil:
		s.Result, s.Error, s.OK = ResultOK, "", at
		if a != nil {
			s.Archive, s.Bytes = a.Name, a.Size
		}
	case errors.Is(cause, ErrLowDisk):
		s.Result, s.Error = ResultSkipped, truncate(cause.Error(), 500)
	default:
		s.Result, s.Error = ResultFailed, truncate(cause.Error(), 500)
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, ".status.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, statusName))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }

// ensureDir creates a backups directory (0700) and tightens one that is
// more open.
func ensureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700)
}
