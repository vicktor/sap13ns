package transfer

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"sap13ns/internal/backup"
	"sap13ns/internal/db"
)

// DB is the database of the site to export: a pool on a single site, the
// site's scoped handle (db.Scoped) on a network.
type DB interface {
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
}

// ExportOptions describe the site to export.
type ExportOptions struct {
	Source    Source
	SecretKey []byte // the site's key (config.Config.SecretKey of the site)
	SMTP      *SMTP
	NetworkAI *NetworkAI
	// The site's directories, and the themes of its network.
	UploadsDir, PluginsDir, ThemesDir string
	NetworkThemesDir                  string
	Passphrase                        string
	// TmpDir holds the tables while the archive is written, encrypted
	// with a key of the moment; "" is the system's.
	TmpDir     string
	AppVersion string
	Log        *slog.Logger
	Now        func() time.Time
}

// schemaMigrations is the tracking table of the site migrations; the
// manifest lists its versions instead of its rows.
const schemaMigrations = "schema_migrations"

// Export writes the archive of a site to w. The tables are read in one
// snapshot; the files as they are while the archive is written, so the
// site is best left quiet (suspended on a network) meanwhile.
func Export(ctx context.Context, src DB, w io.Writer, o ExportOptions) (*Manifest, error) {
	if err := backup.CheckPassphrase(o.Passphrase); err != nil {
		return nil, fmt.Errorf("a transfer archive holds the site's secret key and passwords and is always encrypted: %w", err)
	}
	if len(o.SecretKey) != 32 {
		return nil, errors.New("transfer: the site's secret key is missing")
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	m := &Manifest{Format: FormatVersion, AppVersion: o.AppVersion, Created: now().UTC().Truncate(time.Second), Source: o.Source,
		SecretKey: hex.EncodeToString(o.SecretKey), SMTP: o.SMTP, NetworkAI: o.NetworkAI}

	sp, err := newSpool(o.TmpDir)
	if err != nil {
		return nil, err
	}
	defer sp.remove()
	if err := dumpTables(ctx, src, m, sp); err != nil {
		return nil, err
	}
	files, themes, err := listFiles(o)
	if err != nil {
		return nil, err
	}
	m.NetworkThemes = themes

	enc, err := backup.Encrypt(w, o.Passphrase)
	if err != nil {
		return nil, err
	}
	gz := gzip.NewWriter(enc)
	tw := tar.NewWriter(gz)
	r, err := sp.reader()
	if err != nil {
		return nil, err
	}
	for _, t := range m.Tables {
		if err := tw.WriteHeader(header(t.Data.Name, t.Data.Size, m.Created, 0o600)); err != nil {
			return nil, err
		}
		if _, err := io.CopyN(tw, r, t.Data.Size); err != nil {
			return nil, fmt.Errorf("table %s: %w", t.Name, err)
		}
		m.TotalBytes += t.Data.Size
	}
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		mem, err := writeFile(tw, f)
		if err != nil {
			return nil, err
		}
		m.Files = append(m.Files, mem)
		m.TotalBytes += mem.Size
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := tw.WriteHeader(header(NameManifest, int64(len(b)), m.Created, 0o600)); err != nil {
		return nil, err
	}
	if _, err := tw.Write(b); err != nil {
		return nil, err
	}
	for _, c := range []io.Closer{tw, gz, enc} {
		if err := c.Close(); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// Estimate is about the size of the archive of a site: its tables as
// they take on disk, and its files (compression is left as a margin).
func Estimate(ctx context.Context, src DB, o ExportOptions) (int64, error) {
	tx, err := src.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	var n int64
	if err := tx.QueryRow(ctx, `SELECT coalesce(sum(pg_total_relation_size(c.oid)), 0)::bigint FROM pg_class c
		WHERE c.relnamespace = current_schema()::regnamespace AND c.relkind = 'r'`).Scan(&n); err != nil {
		return 0, err
	}
	o.Log = slog.New(slog.DiscardHandler)
	files, _, err := listFiles(o)
	if err != nil {
		return 0, err
	}
	for _, f := range files {
		if st, err := os.Stat(f.path); err == nil {
			n += st.Size()
		}
	}
	return n, nil
}

// dumpTables writes every table of the site's schema to the spool, in one
// read-only snapshot, and lists them in m.
func dumpTables(ctx context.Context, src DB, m *Manifest, sp *spool) error {
	tx, err := src.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	set, err := db.SiteMigrations()
	if err != nil {
		return err
	}
	if m.Migrations, err = set.Applied(ctx, tx); err != nil {
		return err
	}
	if len(m.Migrations) == 0 {
		return errors.New("transfer: the site's schema has no migrations")
	}
	if err := tx.QueryRow(ctx, `SELECT current_setting('server_version')`).Scan(&m.PGVersion); err != nil {
		return err
	}
	if m.Embedding, err = embeddingType(ctx, tx); err != nil {
		return err
	}
	tables, err := listTables(ctx, tx)
	if err != nil {
		return err
	}
	w, err := sp.writer()
	if err != nil {
		return err
	}
	for _, t := range tables {
		if t.Name == schemaMigrations {
			continue
		}
		cw := &counter{w: w, h: sha256.New()}
		tag, err := tx.Conn().PgConn().CopyTo(ctx, cw, `COPY `+pgx.Identifier{t.Name}.Sanitize()+` (`+columnList(t.Columns)+`) TO STDOUT`)
		if err != nil {
			return fmt.Errorf("table %s: %w", t.Name, err)
		}
		t.Rows = tag.RowsAffected()
		t.Data = backup.Member{Name: dirDB + t.Name + copySuffix, Size: cw.n, SHA256: hex.EncodeToString(cw.h.Sum(nil))}
		m.Tables = append(m.Tables, t)
	}
	return w.Close()
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// listTables lists the tables of the current schema that are not part of
// an extension, by name, with their stored columns in order (generated
// columns are computed again where the rows are loaded).
func listTables(ctx context.Context, q querier) ([]Table, error) {
	rows, err := q.Query(ctx, `SELECT c.relname::text,
			coalesce(array_agg(a.attname::text ORDER BY a.attnum) FILTER (WHERE a.attnum > 0), '{}')
		FROM pg_class c
		LEFT JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped AND a.attgenerated = ''
		WHERE c.relnamespace = current_schema()::regnamespace AND c.relkind = 'r'
			AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_class'::regclass AND d.objid = c.oid AND d.deptype = 'e')
		GROUP BY c.relname ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Table, error) {
		var t Table
		err := r.Scan(&t.Name, &t.Columns)
		return t, err
	})
}

// embeddingType is the type of assistant_chunks.embedding, such as
// "vector(768)", or "" when the column does not exist.
func embeddingType(ctx context.Context, q querier) (string, error) {
	var kind string
	var dims int
	err := q.QueryRow(ctx, `SELECT t.typname::text, a.atttypmod FROM pg_attribute a JOIN pg_type t ON t.oid = a.atttypid
		WHERE a.attrelid = to_regclass('assistant_chunks') AND a.attname = 'embedding' AND NOT a.attisdropped`).Scan(&kind, &dims)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return kind + "(" + strconv.Itoa(dims) + ")", nil
}

func columnList(cols []string) string {
	q := make([]string, len(cols))
	for i, c := range cols {
		q[i] = pgx.Identifier{c}.Sanitize()
	}
	return strings.Join(q, ", ")
}

// fileEntry is a file of the site and its name in the archive.
type fileEntry struct {
	name, path string
}

// listFiles lists the site's uploads, plugins and themes, and the themes
// of the network the site does not have a copy of its own of.
func listFiles(o ExportOptions) ([]fileEntry, []string, error) {
	var files []fileEntry
	for _, d := range []struct{ dir, prefix string }{{o.UploadsDir, DirUploads}, {o.PluginsDir, DirPlugins}, {o.ThemesDir, DirThemes}} {
		if err := walk(d.dir, dirFiles+d.prefix, o.Log, &files); err != nil {
			return nil, nil, err
		}
	}
	var themes []string
	if o.NetworkThemesDir == "" {
		return files, nil, nil
	}
	entries, err := os.ReadDir(o.NetworkThemesDir)
	if errors.Is(err, fs.ErrNotExist) {
		return files, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	for _, e := range entries {
		slug := e.Name()
		if !e.IsDir() || strings.HasPrefix(slug, ".") {
			continue
		}
		if _, err := os.Stat(filepath.Join(o.NetworkThemesDir, slug, "theme.json")); err != nil {
			continue
		}
		if o.ThemesDir != "" {
			if _, err := os.Stat(filepath.Join(o.ThemesDir, slug)); err == nil {
				continue // the site's own copy wins, as it does when it is served
			}
		}
		if err := walk(filepath.Join(o.NetworkThemesDir, slug), dirFiles+DirThemes+"/"+slug, o.Log, &files); err != nil {
			return nil, nil, err
		}
		themes = append(themes, slug)
	}
	return files, themes, nil
}

// walk adds the regular files under root to files, named prefix/<path>.
// A missing root has no files; links and other special files are left out.
func walk(root, prefix string, log *slog.Logger, files *[]fileEntry) error {
	if root == "" {
		return nil
	}
	if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		name := path.Join(prefix, filepath.ToSlash(rel))
		switch {
		case !d.Type().IsRegular():
			log.Warn("transfer: not a regular file, left out", "path", p)
		case !backup.SafeName(name):
			log.Warn("transfer: file name not portable, left out", "path", p)
		default:
			*files = append(*files, fileEntry{name: name, path: p})
		}
		return nil
	})
}

func writeFile(tw *tar.Writer, f fileEntry) (backup.Member, error) {
	fh, err := os.Open(f.path)
	if err != nil {
		return backup.Member{}, err
	}
	defer fh.Close()
	st, err := fh.Stat()
	if err != nil {
		return backup.Member{}, err
	}
	if !st.Mode().IsRegular() {
		return backup.Member{}, fmt.Errorf("%s is no longer a regular file", f.path)
	}
	if err := tw.WriteHeader(header(f.name, st.Size(), st.ModTime(), 0o644)); err != nil {
		return backup.Member{}, err
	}
	h := sha256.New()
	if _, err := io.CopyN(io.MultiWriter(tw, h), fh, st.Size()); err != nil {
		return backup.Member{}, fmt.Errorf("%s changed while it was exported: %w", f.path, err)
	}
	return backup.Member{Name: f.name, Size: st.Size(), SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

func header(name string, size int64, mtime time.Time, mode int64) *tar.Header {
	return &tar.Header{Typeflag: tar.TypeReg, Name: name, Size: size, Mode: mode, ModTime: mtime.UTC().Truncate(time.Second), Format: tar.FormatPAX}
}

type counter struct {
	w io.Writer
	h hash.Hash
	n int64
}

func (c *counter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.h.Write(p[:n])
	c.n += int64(n)
	return n, err
}

// spool keeps the tables between the snapshot and the archive, in a
// temporary file encrypted with a passphrase of the moment: the rows hold
// password hashes and must not lie on disk in clear.
type spool struct {
	f    *os.File
	pass string
}

func newSpool(dir string) (*spool, error) {
	f, err := os.CreateTemp(dir, ".sap13ns-transfer-*.spool")
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, err
	}
	return &spool{f: f, pass: rand.Text() + rand.Text()}, nil
}

func (s *spool) writer() (io.WriteCloser, error) { return backup.Encrypt(s.f, s.pass) }

func (s *spool) reader() (io.Reader, error) {
	if _, err := s.f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return backup.Decrypt(s.f, s.pass)
}

func (s *spool) remove() {
	s.f.Close()
	os.Remove(s.f.Name())
}
