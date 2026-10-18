// Package backup creates, verifies, restores and prunes backups of a site:
// the PostgreSQL database and the uploads directory, in one tar.gz archive
// that is optionally encrypted with a passphrase.
//
// An archive is named sap13ns-YYYYMMDD-HHMMSS.tar.gz (UTC), or .tar.gz.enc
// when encrypted (the format is described in crypt.go). Its members are, in
// this order:
//
//	manifest.json  format and app versions, PostgreSQL version and database
//	               size, and the size and SHA-256 of every other member
//	db.dump        pg_dump --format=custom --no-owner --no-privileges
//	uploads/...    the regular files of the uploads directory
//
// An archive is written to a temporary file in the output directory, fsynced
// and renamed into place with mode 0600. Readers trust nothing in an archive.
// Every member must be a regular file with a canonical relative name. It
// must also be listed in the manifest with the same size, and its SHA-256
// must match before anything is restored.
package backup

import (
	"bufio"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"time"
	"unicode/utf8"
)

var (
	ErrCorrupt            = errors.New("archive is corrupt or has been tampered with")
	ErrUnsafeMember       = errors.New("archive contains an unsafe member")
	ErrPassphraseRequired = errors.New("archive is encrypted; a passphrase is required")
	ErrWrongPassphrase    = errors.New("wrong passphrase, or the archive header is damaged")
	ErrWeakPassphrase     = fmt.Errorf("backup passphrase must have at least %d characters", minPassphraseLen)
	ErrNotEmpty           = errors.New("target database is not empty")
)

// Options configures Create.
type Options struct {
	DatabaseURL string
	UploadsDir  string
	OutDir      string // where archives go; created with mode 0700 if missing
	Passphrase  string // optional; empty writes an unencrypted archive
	AppVersion  string // recorded in the manifest
	PgDump      string // default "pg_dump" from PATH
	PgRestore   string // default "pg_restore" from PATH; unused by Create
	Now         func() time.Time
	Log         *slog.Logger

	probe probeFunc // tests replace the queries sent to PostgreSQL
	kdf   kdfParams // tests use cheap Argon2id parameters
}

func (o Options) withDefaults() Options {
	if o.PgDump == "" {
		o.PgDump = "pg_dump"
	}
	if o.PgRestore == "" {
		o.PgRestore = "pg_restore"
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.probe == nil {
		o.probe = probeServer
	}
	if o.kdf == (kdfParams{}) {
		o.kdf = defaultKDF
	}
	return o
}

// Entry is an archive found by List.
type Entry struct {
	Name      string
	Path      string
	Created   time.Time // from the file name, UTC
	Size      int64
	Encrypted bool
	Manifest  *Manifest // nil when encrypted (it needs the passphrase) or unreadable
	Err       error     // why the manifest of an unencrypted archive could not be read
}

const stampLayout = "20060102-150405"

var nameRE = regexp.MustCompile(`^sap13ns-(\d{8}-\d{6})\.tar\.gz(\.enc)?$`)

func archiveName(t time.Time, encrypted bool) string {
	name := "sap13ns-" + t.UTC().Format(stampLayout) + ".tar.gz"
	if encrypted {
		name += ".enc"
	}
	return name
}

func parseName(name string) (created time.Time, encrypted, ok bool) {
	m := nameRE.FindStringSubmatch(name)
	if m == nil {
		return time.Time{}, false, false
	}
	t, err := time.Parse(stampLayout, m[1])
	if err != nil {
		return time.Time{}, false, false
	}
	return t, m[2] != "", true
}

// archives returns the archives in dir, newest first. Only regular files
// whose names match the naming pattern are considered.
func archives(dir string) ([]Entry, error) {
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, d := range des {
		created, enc, ok := parseName(d.Name())
		if !ok || !d.Type().IsRegular() {
			continue
		}
		info, err := d.Info()
		if err != nil {
			continue // removed meanwhile
		}
		out = append(out, Entry{Name: d.Name(), Path: filepath.Join(dir, d.Name()), Created: created, Size: info.Size(), Encrypted: enc})
	}
	slices.SortFunc(out, func(a, b Entry) int {
		if c := b.Created.Compare(a.Created); c != 0 {
			return c
		}
		return cmp.Compare(b.Name, a.Name)
	})
	return out, nil
}

// List returns the archives in dir, newest first, with the manifests of the
// unencrypted ones. A missing directory holds no archives.
func List(dir string) ([]Entry, error) {
	out, err := archives(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	for i := range out {
		if !out[i].Encrypted {
			out[i].Manifest, out[i].Err = readManifest(out[i].Path)
		}
	}
	return out, nil
}

// Create dumps the database and archives it with the uploads. It returns the
// archive's path and manifest.
func Create(ctx context.Context, opts Options) (path string, info *Manifest, err error) {
	o := opts.withDefaults()
	start := time.Now()
	switch {
	case o.DatabaseURL == "":
		return "", nil, errors.New("backup: database url is required")
	case o.UploadsDir == "":
		return "", nil, errors.New("backup: uploads directory is required")
	case o.OutDir == "":
		return "", nil, errors.New("backup: output directory is required")
	case o.Passphrase != "" && utf8.RuneCountInString(o.Passphrase) < minPassphraseLen:
		return "", nil, ErrWeakPassphrase
	}
	env, dbname, err := pgEnv(o.DatabaseURL)
	if err != nil {
		return "", nil, err
	}
	if fi, err := os.Stat(o.UploadsDir); err != nil {
		return "", nil, fmt.Errorf("uploads: %w", err)
	} else if !fi.IsDir() {
		return "", nil, fmt.Errorf("uploads: %s is not a directory", o.UploadsDir)
	}
	// The web server publishes the uploads, and archive names are
	// predictable. Checked before and after creating OutDir, for symlinks.
	errPublic := errors.New("backup: the output directory must not be inside the uploads directory")
	if inside(o.OutDir, o.UploadsDir) {
		return "", nil, errPublic
	}
	if err := os.MkdirAll(o.OutDir, 0o700); err != nil {
		return "", nil, err
	}
	if inside(o.OutDir, o.UploadsDir) {
		return "", nil, errPublic
	}
	if fi, err := os.Stat(o.OutDir); err == nil && fi.Mode().Perm()&0o077 != 0 {
		o.Log.Warn("backup directory is accessible to other users", "dir", o.OutDir, "mode", fi.Mode().Perm().String())
	}

	created := o.Now().UTC().Truncate(time.Second)
	final := filepath.Join(o.OutDir, archiveName(created, o.Passphrase != ""))
	if _, err := os.Lstat(final); err == nil {
		return "", nil, fmt.Errorf("backup: %s already exists", final)
	}

	srv, err := o.probe(ctx, o.DatabaseURL)
	if err != nil {
		return "", nil, err
	}
	sp, err := newSpool(o.OutDir)
	if err != nil {
		return "", nil, err
	}
	defer sp.remove()
	db, err := dump(ctx, o, env, dbname, sp)
	if err != nil {
		return "", nil, err
	}

	root, err := os.OpenRoot(o.UploadsDir)
	if err != nil {
		return "", nil, fmt.Errorf("uploads: %w", err)
	}
	defer root.Close()
	m := &Manifest{Format: formatVersion, AppVersion: o.AppVersion, Created: created, Database: dbname,
		PGVersion: srv.Version, DBSize: srv.Size, DB: db}
	// Uploads are hashed first because the manifest, which comes first,
	// lists their hashes, then hashed again while archived. If a file
	// changes in between, the whole archive is written again.
	for attempt := 1; ; attempt++ {
		files, err := scanUploads(ctx, root, o.Log)
		if err == nil {
			m.Files = make([]Member, len(files))
			m.TotalBytes = db.Size
			for i, f := range files {
				m.Files[i] = f.sum
				m.TotalBytes += f.sum.Size
			}
			m.FileCount = len(files)
			err = writeArchive(ctx, o, final, m, sp, root, files)
		}
		if errors.Is(err, errUploadChanged) && attempt < 3 {
			o.Log.Warn("uploads changed during backup, starting again", "err", err)
			continue
		}
		if err != nil {
			return "", nil, err
		}
		break
	}
	o.Log.Info("backup created", "path", final, "encrypted", o.Passphrase != "", "db_bytes", db.Size,
		"files", m.FileCount, "total_bytes", m.TotalBytes, "duration", time.Since(start).Round(time.Millisecond))
	return final, m, nil
}

// dump streams pg_dump's output into the spool, hashing it on the way.
func dump(ctx context.Context, o Options, env []string, dbname string, sp *spool) (Member, error) {
	w, err := sp.writer()
	if err != nil {
		return Member{}, err
	}
	sum := sha256.New()
	cw := &countWriter{w: io.MultiWriter(w, sum)}
	args := []string{"--format=custom", "--no-owner", "--no-privileges", "--no-password",
		"--lock-wait-timeout=60s", conninfoDB(dbname)}
	if err := runTool(ctx, o.Log, o.PgDump, args, env, cw); err != nil {
		return Member{}, err
	}
	if err := w.Close(); err != nil {
		return Member{}, err
	}
	switch {
	case string(cw.head) != "PGDMP":
		return Member{}, errors.New("pg_dump: output is not a custom-format archive")
	case cw.n > maxDumpBytes:
		return Member{}, fmt.Errorf("pg_dump: dump larger than %d bytes", maxDumpBytes)
	}
	return Member{Name: dumpName, Size: cw.n, SHA256: hex.EncodeToString(sum.Sum(nil))}, nil
}

// countWriter counts bytes and keeps the first five, pg_dump's magic.
type countWriter struct {
	w    io.Writer
	n    int64
	head []byte
}

func (c *countWriter) Write(p []byte) (int, error) {
	if k := 5 - len(c.head); k > 0 {
		c.head = append(c.head, p[:min(k, len(p))]...)
	}
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// writeArchive writes the archive to a temporary file in OutDir and renames
// it into place once it is complete and on disk.
func writeArchive(ctx context.Context, o Options, final string, m *Manifest, sp *spool, root *os.Root, files []upload) (err error) {
	tmp, err := os.CreateTemp(o.OutDir, ".sap13ns-*.tmp")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	bw := bufio.NewWriterSize(tmp, 256<<10)
	var w io.Writer = bw
	var enc io.WriteCloser
	if o.Passphrase != "" {
		if enc, err = newEncrypter(bw, o.Passphrase, o.kdf); err != nil {
			return err
		}
		w = enc
	}
	if err := writeTar(ctx, w, m, sp, root, files); err != nil {
		return err
	}
	if enc != nil {
		if err := enc.Close(); err != nil {
			return err
		}
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if _, err := os.Lstat(final); err == nil {
		return fmt.Errorf("backup: %s already exists", final)
	}
	if err := os.Rename(tmp.Name(), final); err != nil {
		return err
	}
	return syncDir(o.OutDir)
}

// inside reports whether dir is parent or below it, lexically or after
// resolving symlinks (when both exist).
func inside(dir, parent string) bool {
	within := func(d, p string) bool {
		rel, err := filepath.Rel(p, d)
		return err == nil && filepath.IsLocal(rel) // "." included
	}
	d, err1 := filepath.Abs(dir)
	p, err2 := filepath.Abs(parent)
	if err1 == nil && err2 == nil && within(d, p) {
		return true
	}
	d, err1 = filepath.EvalSymlinks(dir)
	p, err2 = filepath.EvalSymlinks(parent)
	return err1 == nil && err2 == nil && within(d, p)
}

// syncDir makes a rename in dir durable.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
