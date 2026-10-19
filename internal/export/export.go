// Package export writes a site's content so that it can leave: a WordPress
// eXtended RSS file (WXR 1.2) that WordPress and the sap13ns importer read,
// or a .zip with that file, the uploads directory and a Markdown copy of
// every post and page.
//
// The WXR file follows what WordPress writes (Tools → Export), so that the
// WordPress Importer creates the authors, the categories with their
// parents, the tags, the posts and pages with their featured images, the
// threaded comments, the media library and the menus (as nav_menu terms
// and nav_menu_item posts). It adds a few keys of its own that WordPress
// keeps as hidden custom fields and the sap13ns importer reads back
// (wpimport.Meta*):
//
//   - the Markdown source of each post, so that sap13ns does not convert
//     the rendered HTML back;
//   - its language and translation group, so that translations stay linked;
//   - the translated names of terms, as termmeta;
//   - the meta description, under the key Yoast SEO uses;
//   - the site's redirects, as <sap13ns:redirect> elements of the channel.
//
// Exports stream: posts, comments and files are read in batches and
// written as they come, so memory does not grow with the site. Nothing
// secret is ever written: users carry their login, display name and,
// with PersonalData, their e-mail address; never a password hash, a second
// factor, a session or a setting.
package export

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"sap13ns/internal/store"
)

// ErrTooLarge means the export went over one of its limits.
var ErrTooLarge = errors.New("export: the export is larger than its limit")

// Limits. A WXR file larger than the importer accepts (256 MiB) is still
// written, since WordPress has no such limit, but MaxXMLBytes bounds it.
const (
	DefaultMaxXMLBytes = int64(2 << 30)
	DefaultMaxFiles    = 500_000 // as backups
	DefaultMaxBytes    = int64(64 << 30)
)

// Options choose what an export holds.
type Options struct {
	Types []string // store.TypePost, store.TypePage; nil means both
	// Statuses nil means published only (scheduled posts included).
	// Trashed posts are exported only when it lists store.StatusTrash.
	Statuses []string
	// From and To bound the date of posts (published, or created for
	// drafts); zero leaves that side open. To is exclusive.
	From, To time.Time
	AuthorID int64 // 0: every author
	// Media exports the whole media library, and puts the uploads directory
	// in a .zip; otherwise only the featured images of the exported posts
	// are listed, and a .zip carries no files.
	Media bool
	// PendingComments adds the comments awaiting moderation to the
	// approved ones. Spam and trash are never exported.
	PendingComments bool
	// PersonalData adds commenters' e-mail and IP addresses and authors'
	// e-mail addresses. Without it the file has neither.
	PersonalData bool
	// NoMenus leaves the menus out; otherwise each menu of the site (one
	// per location and language) is a WordPress menu.
	NoMenus bool
	// WPBase is the address of a WordPress site the content moves to
	// (https://new.example). The uploaded files are then listed at
	// <WPBase>/wp-content/uploads/sap13ns-import/<path> (WPImportDir), in
	// the attachments and in the links of the content: the owner copies
	// the wp-content/uploads folder of a backup there before importing the
	// file, and the WordPress Importer copies each file into its uploads
	// and points the content at its copy. Links of the content and custom
	// menu links to the site's own pages point at WPBase too. "" keeps
	// them on Site.URL.
	WPBase string

	Site Site
	Now  func() time.Time

	MaxXMLBytes int64 // 0: DefaultMaxXMLBytes
	MaxFiles    int   // .zip: uploads; 0: DefaultMaxFiles
	MaxBytes    int64 // .zip: everything before compression; 0: DefaultMaxBytes
}

// Site describes the exported site.
type Site struct {
	URL         string // https://example.com, without a trailing slash
	Title       string
	Description string
	Language    string // main language, served without a prefix
	Location    *time.Location
	Generator   string // e.g. "sap13ns 1.4.0"
	UploadsDir  string // read by a .zip with Media
}

// Report counts what an export wrote.
type Report struct {
	Posts, Pages, Attachments, Comments  int
	Authors, Categories, Tags, Redirects int
	Menus, MenuItems                     int
	Files                                int   // uploads in a .zip
	Bytes                                int64 // the WXR file, before compression
}

func (o Options) withDefaults() Options {
	if len(o.Types) == 0 {
		o.Types = []string{store.TypePost, store.TypePage}
	}
	if len(o.Statuses) == 0 {
		o.Statuses = []string{store.StatusPublished}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Site.Location == nil {
		o.Site.Location = time.UTC
	}
	o.Site.URL = strings.TrimRight(o.Site.URL, "/")
	o.WPBase = strings.TrimRight(o.WPBase, "/")
	o.MaxXMLBytes = cmp.Or(o.MaxXMLBytes, DefaultMaxXMLBytes)
	o.MaxFiles = cmp.Or(o.MaxFiles, DefaultMaxFiles)
	o.MaxBytes = cmp.Or(o.MaxBytes, DefaultMaxBytes)
	return o
}

func (o Options) filter() store.ExportFilter {
	f := store.ExportFilter{Types: o.Types, Statuses: o.Statuses, AuthorID: o.AuthorID}
	if !o.From.IsZero() {
		t := o.From
		f.From = &t
	}
	if !o.To.IsZero() {
		t := o.To
		f.To = &t
	}
	return f
}

func (o Options) commentStatuses() []string {
	if o.PendingComments {
		return []string{"approved", "pending"}
	}
	return []string{"approved"}
}

// - - - files of background exports - - -

// DirName is the directory of finished exports, under a site's data
// directory. It is not served, and a network leaves it out of the storage
// a site uses.
const DirName = "exports"

// Dir is the export directory of a site's data directory.
func Dir(dataDir string) string { return filepath.Join(dataDir, DirName) }

// TTL is how long a finished export waits to be downloaded.
const TTL = 24 * time.Hour

// partTTL is how long a file being written may go untouched before it is
// taken as abandoned (the process stopped during an export).
const partTTL = 3 * time.Hour

// PartSuffix marks a file being written, ClaimSuffix one being downloaded.
const (
	PartSuffix  = ".part"
	ClaimSuffix = ".sending"
)

var nameRE = regexp.MustCompile(`^u([1-9][0-9]{0,18})-([0-9]{1,12})-([0-9a-f]{32})\.zip$`)

// NewName returns the file name of a background export by userID started
// at t: the user and the time are in the name, so that the export page and
// the download link need nothing else.
func NewName(userID int64, t time.Time) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return fmt.Sprintf("u%d-%d-%s.zip", userID, t.Unix(), hex.EncodeToString(b))
}

// ParseName reads a name from NewName.
func ParseName(name string) (userID int64, created time.Time, ok bool) {
	m := nameRE.FindStringSubmatch(name)
	if m == nil {
		return 0, time.Time{}, false
	}
	id, err1 := strconv.ParseInt(m[1], 10, 64)
	sec, err2 := strconv.ParseInt(m[2], 10, 64)
	if err1 != nil || err2 != nil {
		return 0, time.Time{}, false
	}
	return id, time.Unix(sec, 0), true
}

// Ready is a finished export waiting in the export directory.
type Ready struct {
	Name    string
	UserID  int64
	Created time.Time
	Expires time.Time
	Size    int64
}

// List returns the finished exports in dir that have not expired, newest
// first. A missing directory has none.
func List(dir string, now time.Time) ([]Ready, error) {
	des, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Ready
	for i := len(des) - 1; i >= 0; i-- {
		d := des[i]
		uid, created, ok := ParseName(d.Name())
		if !ok || !d.Type().IsRegular() || !now.Before(created.Add(TTL)) {
			continue
		}
		info, err := d.Info()
		if err != nil {
			continue // removed meanwhile
		}
		out = append(out, Ready{Name: d.Name(), UserID: uid, Created: created, Expires: created.Add(TTL), Size: info.Size()})
	}
	return out, nil
}

// Sweep removes from dir the exports that expired, partial files left by
// an interrupted export and claims left by an interrupted download. It
// returns how many files it removed.
func Sweep(dir string, now time.Time) (int, error) {
	des, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n := 0
	var errs []error
	for _, d := range des {
		name := d.Name()
		info, err := d.Info()
		if err != nil || d.IsDir() {
			continue
		}
		stale := false
		switch {
		case strings.HasSuffix(name, PartSuffix):
			stale = now.Sub(info.ModTime()) > partTTL
		case strings.HasSuffix(name, ClaimSuffix):
			// A download removes its claim as soon as it has opened it.
			stale = now.Sub(info.ModTime()) > 10*time.Minute
		default:
			_, created, ok := ParseName(name)
			stale = ok && !now.Before(created.Add(TTL))
		}
		if !stale {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
			continue
		}
		n++
	}
	return n, errors.Join(errs...)
}

// RemoveReady removes every finished export in dir, whoever asked for it:
// a site keeps one export waiting for its download. Partial files and
// claims are left to Sweep. It returns how many files it removed.
func RemoveReady(dir string) (int, error) {
	des, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n := 0
	var errs []error
	for _, d := range des {
		if _, _, ok := ParseName(d.Name()); !ok || !d.Type().IsRegular() {
			continue
		}
		if err := os.Remove(filepath.Join(dir, d.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
			continue
		}
		n++
	}
	return n, errors.Join(errs...)
}

// The estimate gives itemBytes to the markup of each post, page, comment
// and media item, and headBytes to the rest of the WXR file (channel,
// authors, terms, redirects).
const (
	itemBytes = 2 << 10
	headBytes = 256 << 10
)

// Estimate is about what a .zip export of o takes before compression,
// erring on the large side: with Media the uploads directory, plus three
// copies of the text of the posts and pages (the HTML and the Markdown in
// the WXR file, and the Markdown copy), their comments, and the markup of
// each item. It stops counting uploads once the total passes the export's
// MaxBytes, which such an export could not be written within anyway.
func Estimate(ctx context.Context, st *store.Store, o Options) (int64, error) {
	o = o.withDefaults()
	text, items, err := st.ExportTextBytes(ctx, o.filter(), o.commentStatuses())
	if err != nil {
		return 0, err
	}
	n := headBytes + 3*text + items*itemBytes
	if o.Media {
		up, err := UploadsSize(o.Site.UploadsDir, o.MaxBytes)
		if err != nil {
			return 0, err
		}
		n += up
	}
	return n, nil
}

// UploadsSize adds up the sizes of the regular files under dir, stopping
// once the total passes stop. A missing directory is empty.
func UploadsSize(dir string, stop int64) (int64, error) {
	var n int64
	errStop := errors.New("stop")
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
			if n > stop {
				return errStop
			}
		}
		return nil
	})
	if errors.Is(err, errStop) {
		err = nil
	}
	return n, err
}
