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
	"bufio"
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
	// ErrTooLarge refuses a backup larger than the site may make
	// (Options.MaxBytes).
	ErrTooLarge = errors.New("the backup would be larger than the storage of the site's plan")
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
	// MaxBytes is the largest backup the site may make, before
	// compression: the storage of its plan on a network (Limits). 0: no
	// limit.
	MaxBytes int64
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
// kept, whatever the schedule, and SafetyKeep how many of them at most.
const (
	PreRestoreDays = 7
	SafetyKeep     = 3
)

// QuotaMultiple bounds the backups a site keeps: this many times the
// storage of its plan, in all (Limits).
const QuotaMultiple = 2

// Limits are the bounds of the backups of a site whose plan has quota
// bytes of storage (0: no limit): the largest backup it may make
// (Options.MaxBytes) and the most its backups may take in all
// (Retention.MaxBytes).
func Limits(quota int64) (maxBackup, maxTotal int64) {
	if quota <= 0 {
		return 0, 0
	}
	return quota, QuotaMultiple * quota
}

// Retention is what Prune keeps.
type Retention struct {
	// The newest backup of each of the last Daily days and Weekly ISO
	// weeks (UTC), and the newest of all.
	Daily, Weekly int
	// MaxBytes bounds what the kept backups take on disk, safety backups
	// included: the oldest go first, never the newest backup. 0: no
	// bound.
	MaxBytes int64
}

// Prune removes the backups that r does not keep. Safety backups taken
// before a restore do not count toward the schedule: the newest
// SafetyKeep are kept, for PreRestoreDays. It refuses with ErrBusy while
// a backup or restore of the site runs.
func Prune(dir string, r Retention, now time.Time) (removed []string, err error) {
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	unlock, err := lock(dir)
	if err != nil {
		return nil, err
	}
	defer unlock()
	return prune(dir, r, now)
}

// prune is Prune under the lock. It never removes the archives named by
// protect (the one a restore uses, and its safety backup).
func prune(dir string, r Retention, now time.Time, protect ...string) (removed []string, err error) {
	if r.Daily < 0 || r.Weekly < 0 || r.MaxBytes < 0 {
		return nil, errors.New("prune: keep counts must not be negative")
	}
	list, err := List(dir) // newest first
	if err != nil {
		return nil, err
	}
	keep := make(map[string]bool, len(protect))
	for _, p := range protect {
		if abs, err := filepath.Abs(p); err == nil {
			keep[abs] = true
		}
	}
	protected := func(a Archive) bool {
		abs, err := filepath.Abs(a.Path)
		return err != nil || keep[abs]
	}
	var regular, kept, drop []Archive
	safety := 0
	for _, a := range list {
		switch {
		case !a.PreRestore:
			regular = append(regular, a)
		case !protected(a) && (safety >= SafetyKeep || now.Sub(a.Created) > PreRestoreDays*24*time.Hour):
			drop = append(drop, a)
		default:
			safety++
			kept = append(kept, a)
		}
	}
	created := make([]time.Time, len(regular))
	for i, a := range regular {
		created[i] = a.Created
	}
	for i, keep := range backup.Keep(created, r.Daily, r.Weekly, 0) {
		switch {
		case keep || protected(regular[i]):
			kept = append(kept, regular[i])
		default:
			drop = append(drop, regular[i])
		}
	}
	if r.MaxBytes > 0 {
		// Within the bound: the oldest go first, but never the newest
		// backup, nor the one a restore uses.
		slices.SortFunc(kept, func(a, b Archive) int { return b.Created.Compare(a.Created) })
		var total int64
		for _, a := range kept {
			total += a.Size
		}
		var newest string
		if len(regular) > 0 {
			newest = regular[0].Path
		}
		for i := len(kept) - 1; i >= 0 && total > r.MaxBytes; i-- {
			if a := kept[i]; a.Path != newest && !protected(a) {
				drop, total = append(drop, a), total-a.Size
			}
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
	// The decrypted copy of an archive, and the archive being written,
	// are removed when the restore, check or backup that made them ends;
	// one a crash left behind goes now.
	stale, _ := filepath.Glob(filepath.Join(dir, decryptedPattern))
	partial, _ := filepath.Glob(filepath.Join(dir, partialPattern))
	stale = append(stale, partial...)
	for _, p := range stale {
		if fi, err := os.Lstat(p); err == nil && fi.Mode().IsRegular() && now.Sub(fi.ModTime()) > staleDecrypted {
			if err := os.Remove(p); err == nil {
				removed = append(removed, p)
			}
		}
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
	Reason  string    `json:"reason,omitempty"`  // why it was skipped: ReasonDisk or ReasonQuota
	Error   string    `json:"error,omitempty"`   // why it failed or was skipped
	Archive string    `json:"archive,omitempty"` // the latest backup made
	Bytes   int64     `json:"bytes,omitempty"`
	// Restores are when the latest restores started, oldest first (Pace):
	// on disk, so that they hold across reloads of the site and servers.
	Restores []time.Time `json:"restores,omitempty"`
}

// Pace spaces out the restores of a site, each of which makes a safety
// backup. The zero Pace allows any.
type Pace struct {
	Cooldown time.Duration // the least time between two restores
	PerHour  int           // at most this many start in an hour; 0: no limit
}

// ErrTooSoon refuses a restore that RestoreOptions.Pace does not allow yet.
var ErrTooSoon = errors.New("a restore of this site started too recently")

// wait is how long until p allows the next restore after those that
// started at starts (oldest first).
func (p Pace) wait(starts []time.Time, now time.Time) time.Duration {
	var wait time.Duration
	if n := len(starts); p.Cooldown > 0 && n > 0 {
		wait = max(wait, starts[n-1].Add(p.Cooldown).Sub(now))
	}
	if p.PerHour > 0 {
		var hour []time.Time
		for _, t := range starts {
			if now.Sub(t) < time.Hour {
				hour = append(hour, t)
			}
		}
		if len(hour) >= p.PerHour {
			wait = max(wait, hour[len(hour)-p.PerHour].Add(time.Hour).Sub(now))
		}
	}
	return max(wait, 0)
}

// RestoreWait is how long until pace allows the next restore of the site
// whose backups are in dir: 0 for now.
func RestoreWait(dir string, pace Pace, now time.Time) (time.Duration, error) {
	s, err := ReadStatus(dir)
	if err != nil {
		return 0, err
	}
	return pace.wait(s.Restores, now), nil
}

// startRestore checks the pace of the restores of the site, under the
// lock, and records this one: it counts from its start, whatever its
// outcome.
func startRestore(dir string, pace Pace, now time.Time) error {
	s, err := ReadStatus(dir)
	if err != nil {
		return err
	}
	if wait := pace.wait(s.Restores, now); wait > 0 {
		return fmt.Errorf("%w: try again in %s", ErrTooSoon, wait.Round(time.Second))
	}
	keep := s.Restores[:0]
	for _, t := range s.Restores {
		if now.Sub(t) < 24*time.Hour {
			keep = append(keep, t)
		}
	}
	s.Restores = append(keep[max(len(keep)-19, 0):], now)
	return writeStatus(dir, s)
}

// Results of a backup.
const (
	ResultOK      = "ok"
	ResultFailed  = "failed"
	ResultSkipped = "skipped" // not enough disk space, or larger than the plan allows

	ReasonDisk  = "disk"  // ErrLowDisk
	ReasonQuota = "quota" // ErrTooLarge
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
		s.Result, s.Reason, s.Error, s.OK = ResultOK, "", "", at
		if a != nil {
			s.Archive, s.Bytes = a.Name, a.Size
		}
	case errors.Is(cause, ErrLowDisk):
		s.Result, s.Reason, s.Error = ResultSkipped, ReasonDisk, truncate(cause.Error(), 500)
	case errors.Is(cause, ErrTooLarge):
		s.Result, s.Reason, s.Error = ResultSkipped, ReasonQuota, truncate(cause.Error(), 500)
	default:
		s.Result, s.Reason, s.Error = ResultFailed, "", truncate(cause.Error(), 500)
	}
	return writeStatus(dir, s)
}

func writeStatus(dir string, s Status) error {
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

// decryptedPattern names the decrypted copy of an encrypted archive
// (open), staleDecrypted the age at which Prune removes one left behind.
const (
	decryptedPattern = ".decrypted-*"
	partialPattern   = ".partial-*" // an archive being written (create)
	staleDecrypted   = 24 * time.Hour
)

func (o Options) freeSpace(dir string) (int64, error) {
	if o.FreeSpace != nil {
		return o.FreeSpace(dir)
	}
	return freeSpace(dir)
}

// fits returns ErrLowDisk unless dir's file system has twice need free:
// what a backup or restore writes takes at most half the free space.
func (o Options) fits(dir string, need int64) error {
	if need <= 0 {
		return nil
	}
	// The directory may not exist yet: its file system is its nearest
	// parent's.
	for {
		if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) || filepath.Dir(dir) == dir {
			break
		}
		dir = filepath.Dir(dir)
	}
	free, err := o.freeSpace(dir)
	if err != nil {
		return err
	}
	if free >= 0 && free < 2*need {
		return fmt.Errorf("%w: %s free, %s needed", ErrLowDisk, size(free), size(2*need))
	}
	return nil
}

// size writes n bytes for a log line.
func size(n int64) string {
	switch {
	case n >= 10<<20:
		return fmt.Sprintf("%d MiB", n>>20)
	case n >= 10<<10:
		return fmt.Sprintf("%d KiB", n>>10)
	}
	return fmt.Sprintf("%d bytes", n)
}

// decryptFits checks the room for the decrypted copy of file, if it is
// encrypted: about its size, in o.Dir.
func (o Options) decryptFits(file string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if !backup.IsEncrypted(bufio.NewReader(f)) {
		return nil
	}
	return o.fits(o.Dir, fi.Size())
}

// uploadBytes is what the uploaded files of the archive take.
func (m *Manifest) uploadBytes() int64 {
	var n int64
	for _, f := range m.Files {
		if strings.HasPrefix(f.Name, DirUploads) {
			n += f.Size
		}
	}
	return n
}

// ensureDir creates a backups directory (0700) and tightens one that is
// more open.
func ensureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700)
}
