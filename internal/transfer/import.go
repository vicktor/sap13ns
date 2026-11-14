package transfer

import (
	"archive/tar"
	"bufio"
	"cmp"
	"compress/gzip"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/netip"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"

	"sap13ns/internal/auth"
	"sap13ns/internal/backup"
	"sap13ns/internal/db"
	"sap13ns/internal/store"
)

// Staged is an archive read, checked against its manifest and unpacked
// into a private directory, ready to be applied.
type Staged struct {
	Manifest *Manifest
	dir      string
}

// Close removes the unpacked archive.
func (s *Staged) Close() error { return os.RemoveAll(s.dir) }

// Open decrypts, unpacks and checks an archive. stageParent holds the
// unpacked copy: the data directory of the install, so that the files
// move into place without being copied again.
func Open(ctx context.Context, file, passphrase, stageParent string) (*Staged, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, 1<<16)
	if !backup.IsEncrypted(br) {
		return nil, fmt.Errorf("%s is not a transfer archive: those are always encrypted", file)
	}
	if passphrase == "" {
		return nil, fmt.Errorf("%w: the archive is encrypted; give its passphrase", backup.ErrWrongPassphrase)
	}
	plain, err := backup.Decrypt(br, passphrase)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(stageParent, 0o750); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(stageParent, ".sap13ns-transfer-")
	if err != nil {
		return nil, err
	}
	s := &Staged{dir: dir}
	if err := s.unpack(ctx, plain); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Staged) unpack(ctx context.Context, plain io.Reader) error {
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return err
	}
	defer root.Close()
	gz, err := gzip.NewReader(plain)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	tr := tar.NewReader(gz)
	got := map[string]backup.Member{}
	var m *Manifest
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("%w: %v", ErrCorrupt, err)
		}
		switch {
		case m != nil:
			return fmt.Errorf("%w: %q follows the manifest", ErrCorrupt, h.Name)
		case h.Typeflag != tar.TypeReg || !backup.SafeName(h.Name) || h.Size < 0 || h.Size > maxMemberBytes:
			return fmt.Errorf("%w: unsafe member %q", ErrCorrupt, h.Name)
		case len(got) >= maxMembers:
			return fmt.Errorf("%w: more than %d members", ErrCorrupt, maxMembers)
		case got[h.Name].Name != "":
			return fmt.Errorf("%w: %q appears twice", ErrCorrupt, h.Name)
		}
		if h.Name == NameManifest {
			if h.Size > maxManifestBytes {
				return fmt.Errorf("%w: the manifest is too large", ErrCorrupt)
			}
			m = new(Manifest)
			if err := json.NewDecoder(io.LimitReader(tr, h.Size)).Decode(m); err != nil {
				return fmt.Errorf("%w: manifest: %v", ErrCorrupt, err)
			}
			continue
		}
		dirMode, fileMode, ok := memberModes(h.Name)
		if !ok {
			return fmt.Errorf("%w: unexpected member %q", ErrCorrupt, h.Name)
		}
		if err := root.MkdirAll(path.Dir(h.Name), dirMode); err != nil {
			return err
		}
		out, err := root.OpenFile(h.Name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileMode)
		if err != nil {
			return err
		}
		sum := sha256.New()
		n, err := io.Copy(io.MultiWriter(out, sum), tr)
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		if !h.ModTime.IsZero() {
			root.Chtimes(h.Name, h.ModTime, h.ModTime)
		}
		got[h.Name] = backup.Member{Name: h.Name, Size: n, SHA256: hex.EncodeToString(sum.Sum(nil))}
	}
	// The encrypted stream is authenticated only once read to its end.
	if _, err := io.Copy(io.Discard, gz); err != nil {
		return fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if _, err := io.Copy(io.Discard, plain); err != nil {
		return err
	}
	if m == nil {
		return fmt.Errorf("%w: no manifest (the archive is cut short?)", ErrCorrupt)
	}
	if err := m.check(got); err != nil {
		return err
	}
	s.Manifest = m
	return nil
}

// memberModes are the modes a member is unpacked with: uploads are served
// by nginx, plugins and themes by sap13ns only, tables are private. ok is
// false for a member an archive cannot have.
func memberModes(name string) (dir, file fs.FileMode, ok bool) {
	if strings.HasPrefix(name, dirDB) {
		base := strings.TrimPrefix(name, dirDB)
		return 0o700, 0o600, !strings.Contains(base, "/") && strings.HasSuffix(base, copySuffix)
	}
	for _, d := range fileDirs {
		if strings.HasPrefix(name, dirFiles+d+"/") {
			if d == DirUploads {
				return 0o755, 0o644, true
			}
			return 0o750, 0o640, true
		}
	}
	return 0, 0, false
}

// check compares the manifest with the members read.
func (m *Manifest) check(got map[string]backup.Member) error {
	if m.Format != FormatVersion {
		return fmt.Errorf("%w: archive format %d, this sap13ns reads %d", ErrNewer, m.Format, FormatVersion)
	}
	if k, err := hex.DecodeString(m.SecretKey); err != nil || len(k) != 32 {
		return fmt.Errorf("%w: the manifest has no valid secret key", ErrCorrupt)
	}
	if len(m.Migrations) == 0 {
		return fmt.Errorf("%w: the manifest lists no migrations", ErrCorrupt)
	}
	want := map[string]backup.Member{}
	for _, t := range m.Tables {
		if t.Data.Name != dirDB+t.Name+copySuffix || len(t.Columns) == 0 {
			return fmt.Errorf("%w: table %q", ErrCorrupt, t.Name)
		}
		want[t.Data.Name] = t.Data
	}
	for _, f := range m.Files {
		if _, _, ok := memberModes(f.Name); !ok || strings.HasPrefix(f.Name, dirDB) {
			return fmt.Errorf("%w: file %q", ErrCorrupt, f.Name)
		}
		want[f.Name] = f
	}
	if len(want) != len(m.Tables)+len(m.Files) {
		return fmt.Errorf("%w: the manifest lists a member twice", ErrCorrupt)
	}
	for name, w := range want {
		if g, ok := got[name]; !ok || g != w {
			return fmt.Errorf("%w: %s does not match the manifest", ErrCorrupt, name)
		}
	}
	if len(got) != len(want) {
		return fmt.Errorf("%w: members the manifest does not list", ErrCorrupt)
	}
	return nil
}

// ApplyOptions say where the site goes.
type ApplyOptions struct {
	UploadsDir, PluginsDir, ThemesDir string
	// ServerKey is the SAP13NS_SECRET_KEY of this install, which seals the
	// site's own key (SealSiteKey).
	ServerKey []byte
	// Replace drops a site the database already has, and moves aside
	// directories that are not empty.
	Replace bool
	// Keep is the account of the administrator importing from the admin
	// pages, kept as an administrator of the site moved here, so that
	// they can sign in after the import: matched by e-mail address to an
	// account that has no password here (it signed in through a network),
	// or added. nil keeps none.
	Keep *KeepUser
	Log  *slog.Logger
	Now  func() time.Time
}

// KeepUser is an account carried over an import.
type KeepUser struct {
	Username, Email, DisplayName, PasswordHash string
}

// Record is what the site keeps of the import that brought it here
// (settings key "transfer_import"), for Tools → Move site.
type Record struct {
	Imported time.Time `json:"imported"`
	From     string    `json:"from"`              // the site's address before
	Network  string    `json:"network,omitempty"` // the network it left
	Made     time.Time `json:"made"`              // when the archive was made
	Version  string    `json:"version"`           // by which sap13ns
	// NoPassword are the accounts that still have no password here.
	NoPassword []User `json:"no_password,omitempty"`
	// What is left to set up: the import ends every session, the
	// importer's too, so the page shows it from here once they sign in.
	MailLocal        string `json:"mail_local,omitempty"`
	AIFromNetwork    bool   `json:"ai_from_network,omitempty"`
	AssistantSkipped bool   `json:"assistant_skipped,omitempty"`
}

// RecordKey is the settings key of the Record.
const RecordKey = "transfer_import"

// Report is what an import did, and what is left to the administrator.
type Report struct {
	Tables, Files int
	Rows          int64
	// SSOUsers signed in through the network's portal: they have no
	// password here and need one (sap13ns user reset-password, or
	// "Forgot your password?").
	SSOUsers []User
	// Operators are the network admins' accounts of the site, disabled.
	Operators []string
	// MovedAside are the directories that were not empty, renamed.
	MovedAside []string
	// AssistantSkipped: pgvector is missing here, so the assistant's
	// index was left out and is to be rebuilt (sap13ns assistant reindex).
	AssistantSkipped bool
	// AIFromNetwork: the site's AI came from its network and was not
	// carried; its provider is to be set up again in Settings → AI.
	AIFromNetwork bool
	// SMTP: the server the site sent through is now its own (Settings →
	// E-mail). MailLocal: it was a server of the old machine, left out.
	SMTP      bool
	MailLocal string
	// Kept is the account of ApplyOptions.Keep on the site: its username,
	// and whether it was added (else matched by e-mail address).
	Kept      string
	KeptAdded bool
	Warnings  []string
}

// User is an account of the site.
type User struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Role     string `json:"role"`
}

// Apply loads the site into the database of a single-site install, and
// its files into the given directories, which the server must not be
// using (stop it first).
func (s *Staged) Apply(ctx context.Context, pool store.DB, o ApplyOptions) (*Report, error) {
	m := s.Manifest
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if len(o.ServerKey) != 32 {
		return nil, errors.New("transfer: this install has no SAP13NS_SECRET_KEY")
	}
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	set, err := db.SiteMigrations()
	if err != nil {
		return nil, err
	}
	for _, v := range m.Migrations {
		if !slices.Contains(set.Versions(), v) {
			return nil, fmt.Errorf("%w (%s, migration %04d): update sap13ns here first", ErrNewer, m.AppVersion, v)
		}
	}
	dirs := map[string]string{DirUploads: o.UploadsDir, DirPlugins: o.PluginsDir, DirThemes: o.ThemesDir}
	for _, d := range fileDirs {
		if dirs[d] == "" {
			return nil, fmt.Errorf("transfer: no %s directory", d)
		}
		if empty, err := emptyDir(dirs[d]); err != nil {
			return nil, err
		} else if !empty && !o.Replace {
			return nil, fmt.Errorf("%w: %s is not empty", ErrNotEmpty, dirs[d])
		}
	}
	rep := &Report{Tables: len(m.Tables), Files: len(m.Files)}

	// pgvector is created outside the transaction: without the privilege,
	// the failure would abort it.
	var vec *store.AssistantVector
	if m.Embedding != "" {
		if vec, err = embeddingColumn(ctx, store.New(pool), m.Embedding); err != nil {
			return nil, err
		}
		if vec == nil {
			rep.AssistantSkipped = true
			rep.Warnings = append(rep.Warnings, "pgvector is not installed in this database: the assistant's index was left out; "+
				"install it (CREATE EXTENSION vector SCHEMA public, as a superuser) and run sap13ns assistant reindex")
		}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if err := s.load(ctx, tx, set, vec, o.Replace, rep); err != nil {
		return nil, err
	}
	if err := cleanup(ctx, tx, m, o, now(), rep); err != nil {
		return nil, err
	}

	// The files move into place before the commit, and back if it fails.
	ts := now().UTC().Format("20060102-150405")
	var undo []func()
	restore := func() {
		for _, u := range slices.Backward(undo) {
			u()
		}
	}
	for _, d := range fileDirs {
		u, aside, err := place(filepath.Join(s.dir, "files", d), dirs[d], ts, d == DirUploads)
		if err != nil {
			restore()
			return nil, err
		}
		undo = append(undo, u)
		if aside != "" {
			rep.MovedAside = append(rep.MovedAside, aside)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		restore()
		return nil, err
	}
	return rep, nil
}

// load recreates the schema at the archive's migrations and loads every
// table, in tx.
func (s *Staged) load(ctx context.Context, tx pgx.Tx, set *db.MigrationSet, vec *store.AssistantVector, replace bool, rep *Report) error {
	m := s.Manifest
	existing, err := listTables(ctx, tx)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		if !replace {
			for _, t := range []string{"users", "posts"} {
				var n int64
				if slices.ContainsFunc(existing, func(e Table) bool { return e.Name == t }) {
					if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{t}.Sanitize()).Scan(&n); err != nil {
						return err
					}
				}
				if n > 0 {
					return ErrNotEmpty
				}
			}
		}
		names := make([]string, len(existing))
		for i, t := range existing {
			names[i] = pgx.Identifier{t.Name}.Sanitize()
		}
		if _, err := tx.Exec(ctx, `DROP TABLE `+strings.Join(names, ", ")+` CASCADE`); err != nil {
			return fmt.Errorf("clear the database: %w", err)
		}
	}
	if err := set.ApplyTx(ctx, tx, m.Migrations); err != nil {
		return err
	}
	if vec != nil {
		for _, q := range store.AssistantEmbeddingDDL(vec) {
			if _, err := tx.Exec(ctx, q); err != nil {
				return fmt.Errorf("the assistant's embedding column: %w", err)
			}
		}
	}
	target, err := listTables(ctx, tx)
	if err != nil {
		return err
	}
	cols := map[string][]string{}
	var all []string
	for _, t := range target {
		cols[t.Name] = t.Columns
		if t.Name != schemaMigrations {
			all = append(all, pgx.Identifier{t.Name}.Sanitize())
		}
	}
	// What the migrations seeded makes way for the site's own rows.
	if len(all) > 0 {
		if _, err := tx.Exec(ctx, `TRUNCATE `+strings.Join(all, ", ")); err != nil {
			return err
		}
	}
	// Foreign keys are dropped while the tables load in any order, and
	// added back after, which checks every row.
	fks, err := foreignKeys(ctx, tx)
	if err != nil {
		return err
	}
	for _, fk := range fks {
		if _, err := tx.Exec(ctx, `ALTER TABLE `+fk.table+` DROP CONSTRAINT `+pgx.Identifier{fk.name}.Sanitize()); err != nil {
			return err
		}
	}
	for _, t := range m.Tables {
		if rep.AssistantSkipped && (t.Name == "assistant_chunks" || t.Name == "assistant_docs") {
			continue
		}
		have, ok := cols[t.Name]
		if !ok {
			return fmt.Errorf("%w: table %s is unknown here", ErrCorrupt, t.Name)
		}
		for _, c := range t.Columns {
			if !slices.Contains(have, c) {
				return fmt.Errorf("%w: column %s.%s is unknown here", ErrCorrupt, t.Name, c)
			}
		}
		f, err := os.Open(filepath.Join(s.dir, filepath.FromSlash(t.Data.Name)))
		if err != nil {
			return err
		}
		tag, err := tx.Conn().PgConn().CopyFrom(ctx, f, `COPY `+pgx.Identifier{t.Name}.Sanitize()+` (`+columnList(t.Columns)+`) FROM STDIN`)
		f.Close()
		if err != nil {
			return fmt.Errorf("load %s: %w", t.Name, err)
		}
		if tag.RowsAffected() != t.Rows {
			return fmt.Errorf("%w: %s has %d rows, the manifest says %d", ErrCorrupt, t.Name, tag.RowsAffected(), t.Rows)
		}
		rep.Rows += t.Rows
	}
	if err := resetSequences(ctx, tx); err != nil {
		return err
	}
	for _, fk := range fks {
		if _, err := tx.Exec(ctx, `ALTER TABLE `+fk.table+` ADD CONSTRAINT `+pgx.Identifier{fk.name}.Sanitize()+` `+fk.def); err != nil {
			return fmt.Errorf("%w: %s on %s: %v", ErrCorrupt, fk.name, fk.table, err)
		}
	}
	// The migrations this sap13ns has and the site's did not, on its rows.
	return set.ApplyTx(ctx, tx, set.Versions())
}

type foreignKey struct{ table, name, def string }

func foreignKeys(ctx context.Context, tx pgx.Tx) ([]foreignKey, error) {
	rows, err := tx.Query(ctx, `SELECT conrelid::regclass::text, conname::text, pg_get_constraintdef(oid)
		FROM pg_constraint WHERE contype = 'f' AND connamespace = current_schema()::regnamespace ORDER BY 1, 2`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (foreignKey, error) {
		var fk foreignKey
		err := r.Scan(&fk.table, &fk.name, &fk.def)
		return fk, err
	})
}

// resetSequences sets every sequence owned by a column (identity and
// serial) past the largest value loaded.
func resetSequences(ctx context.Context, tx pgx.Tx) error {
	rows, err := tx.Query(ctx, `SELECT s.oid::regclass::text, t.oid::regclass::text, a.attname::text
		FROM pg_class s
		JOIN pg_depend d ON d.classid = 'pg_class'::regclass AND d.objid = s.oid AND d.refclassid = 'pg_class'::regclass AND d.deptype IN ('a', 'i')
		JOIN pg_class t ON t.oid = d.refobjid
		JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = d.refobjsubid
		WHERE s.relkind = 'S' AND s.relnamespace = current_schema()::regnamespace`)
	if err != nil {
		return err
	}
	type seq struct{ seq, table, col string }
	seqs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (seq, error) {
		var q seq
		err := r.Scan(&q.seq, &q.table, &q.col)
		return q, err
	})
	if err != nil {
		return err
	}
	for _, q := range seqs {
		if _, err := tx.Exec(ctx, `SELECT setval($1::regclass, coalesce((SELECT max(`+pgx.Identifier{q.col}.Sanitize()+`) FROM `+q.table+`), 0) + 1, false)`, q.seq); err != nil {
			return fmt.Errorf("sequence %s: %w", q.seq, err)
		}
	}
	return nil
}

// cleanup cuts what tied the site to its network and its old server:
// accounts linked to the portal become local ones (those that only signed
// in through it need a password), the network admins' own accounts are
// disabled, and every session ends. The site's key is sealed for this
// server; the mail server it sent through becomes its own, and the AI of
// the network too when the archive carries it; the importing
// administrator's account is kept.
func cleanup(ctx context.Context, tx pgx.Tx, m *Manifest, o ApplyOptions, now time.Time, rep *Report) error {
	secret, _ := hex.DecodeString(m.SecretKey)
	keys := auth.NewKeys(secret)
	rows, err := tx.Query(ctx, `UPDATE users SET status = 'disabled' WHERE network_operator AND status = 'active' RETURNING username`)
	if err != nil {
		return err
	}
	if rep.Operators, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET network_account_id = NULL, network_operator = false WHERE network_account_id IS NOT NULL`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM sessions`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM site_key`); err != nil {
		return err
	}
	if !hmac.Equal(secret, o.ServerKey) {
		sealed, err := SealSiteKey(o.ServerKey, secret)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO site_key (sealed) VALUES ($1)`, sealed); err != nil {
			return err
		}
	}
	if o.Keep != nil {
		if err := keepUser(ctx, tx, o.Keep, rep); err != nil {
			return fmt.Errorf("keep your account: %w", err)
		}
	}
	rows, err = tx.Query(ctx, `SELECT username, email, role FROM users
		WHERE password_hash = $1 AND status = 'active' ORDER BY username`, store.SSOPasswordHash)
	if err != nil {
		return err
	}
	if rep.SSOUsers, err = pgx.CollectRows(rows, pgx.RowToStructByPos[User]); err != nil {
		return err
	}

	st := store.New(txDB{tx})
	if err := ownMail(ctx, st, keys, m.SMTP, rep); err != nil {
		return err
	}
	if err := ownAI(ctx, st, keys, m.NetworkAI, rep); err != nil {
		return err
	}
	return st.SetJSON(ctx, RecordKey, Record{Imported: now.UTC(), From: m.Source.URL, Network: m.Source.Network,
		Made: m.Created, Version: m.AppVersion, NoPassword: rep.SSOUsers, MailLocal: rep.MailLocal, AIFromNetwork: rep.AIFromNetwork,
		AssistantSkipped: rep.AssistantSkipped})
}

// keepUser keeps the importing administrator's account: an account of the
// site with the same e-mail address and no password here gets theirs and
// becomes an administrator; otherwise the account is added.
func keepUser(ctx context.Context, tx pgx.Tx, k *KeepUser, rep *Report) error {
	var name string
	err := tx.QueryRow(ctx, `UPDATE users SET password_hash = $2, role = 'admin', status = 'active', failed_logins = 0, locked_until = NULL
		WHERE lower(email) = lower($1) AND password_hash = $3 RETURNING username`, k.Email, k.PasswordHash, store.SSOPasswordHash).Scan(&name)
	if err == nil {
		rep.Kept = name
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var taken bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE lower(email) = lower($1))`, k.Email).Scan(&taken); err != nil {
		return err
	}
	if taken {
		// The site has an account of that address with a password of
		// its own: it is theirs, and they sign in with it.
		return tx.QueryRow(ctx, `SELECT username FROM users WHERE lower(email) = lower($1)`, k.Email).Scan(&rep.Kept)
	}
	name = k.Username
	for i := 2; ; i++ {
		var used bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE username = $1)`, name).Scan(&used); err != nil {
			return err
		}
		if !used {
			break
		}
		name = fmt.Sprintf("%.28s-%d", k.Username, i)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO users (username, email, display_name, role, password_hash) VALUES ($1, $2, $3, 'admin', $4)`,
		name, k.Email, k.DisplayName, k.PasswordHash); err != nil {
		return err
	}
	rep.Kept, rep.KeptAdded = name, true
	return nil
}

// mailPasswordAD is the associated data of the site's SMTP password
// (internal/web, Settings → E-mail).
const mailPasswordAD = "site-smtp-password"

// ownMail makes the server the site sent through its own, unless it has
// one already or the server cannot be reached from elsewhere.
func ownMail(ctx context.Context, st *store.Store, keys *auth.Keys, s *SMTP, rep *Report) error {
	if s == nil || s.Host == "" || s.From == "" {
		return nil
	}
	cur, err := st.MailSettings(ctx)
	if err != nil || cur.Enabled {
		return err
	}
	if localHost(s.Host) {
		rep.MailLocal = s.Host
		rep.Warnings = append(rep.Warnings, "the site sent mail through "+s.Host+", a server of the old machine: set up its mail in Settings → E-mail")
		return nil
	}
	ms := store.MailSettings{Enabled: true, Host: s.Host, Port: cmp.Or(s.Port, 587), Username: s.Username, From: s.From}
	if s.Password != "" {
		if ms.PasswordEnc, err = keys.Encrypt([]byte(s.Password), []byte(mailPasswordAD)); err != nil {
			return err
		}
	}
	rep.SMTP = true
	return st.SaveMailSettings(ctx, ms)
}

// localHost reports a mail server that only the old machine reaches.
func localHost(h string) bool {
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	a, err := netip.ParseAddr(h)
	return err == nil && (a.IsLoopback() || a.IsPrivate() || a.IsUnspecified() || a.IsLinkLocalUnicast())
}

// ownAI makes the AI of the network, when the archive carries it, the
// site's own provider.
func ownAI(ctx context.Context, st *store.Store, keys *auth.Keys, n *NetworkAI, rep *Report) error {
	ai, err := st.AISettings(ctx)
	if err != nil || ai.Source != store.AISourceNetwork {
		return err
	}
	if n == nil || n.Provider == "" {
		rep.AIFromNetwork = true
		rep.Warnings = append(rep.Warnings, "the site's AI came from its network: set up a provider in Settings → AI")
		return nil
	}
	ai.Source, ai.Provider, ai.BaseURL, ai.TextModel = "", n.Provider, n.BaseURL, n.TextModel
	if ai.APIKeyEnc, err = keys.Encrypt([]byte(n.APIKey), []byte("ai-api-key")); err != nil {
		return err
	}
	ai.ImageProvider, ai.ImageBaseURL, ai.ImageKeyEnc = "", "", nil
	if n.ImageModel != "" {
		ai.ImageModel = n.ImageModel
	}
	if n.ImageProvider != "" && n.ImageProvider != n.Provider {
		ai.ImageProvider, ai.ImageBaseURL = n.ImageProvider, n.ImageBaseURL
		if ai.ImageKeyEnc, err = keys.Encrypt([]byte(n.ImageAPIKey), []byte("ai-image-key")); err != nil {
			return err
		}
	}
	return st.SaveAISettings(ctx, ai)
}

// txDB lets the store work inside the import's transaction.
type txDB struct{ pgx.Tx }

func (txDB) Ping(context.Context) error { return nil }

// embeddingColumn is the embedding column to recreate, in the schema where
// pgvector is (created when it can be), or nil when pgvector is missing.
func embeddingColumn(ctx context.Context, st *store.Store, typ string) (*store.AssistantVector, error) {
	kind, rest, ok := strings.Cut(typ, "(")
	dims, err := strconv.Atoi(strings.TrimSuffix(rest, ")"))
	if !ok || err != nil || (kind != "vector" && kind != "halfvec") || dims < 1 || dims > 4000 {
		return nil, fmt.Errorf("%w: embedding column %q", ErrCorrupt, typ)
	}
	schema, _, have, err := st.Pgvector(ctx)
	if err != nil {
		return nil, err
	}
	if !have {
		if st.CreatePgvector(ctx) != nil {
			return nil, nil
		}
		if schema, _, have, err = st.Pgvector(ctx); err != nil || !have {
			return nil, err
		}
	}
	return &store.AssistantVector{Schema: schema, Kind: kind, Dims: dims}, nil
}

// emptyDir reports whether dir is missing or empty.
func emptyDir(dir string) (bool, error) {
	f, err := os.Open(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	_, err = f.Readdirnames(1)
	if errors.Is(err, io.EOF) {
		return true, nil
	}
	return false, err
}

// place moves the unpacked directory src to dst. A dst that is not empty
// is renamed to dst.before-transfer-<ts> first (aside). undo puts things
// back as they were.
func place(src, dst, ts string, public bool) (undo func(), aside string, err error) {
	mode := fs.FileMode(0o750)
	if public {
		mode = 0o755
	}
	if _, err := os.Stat(src); errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(src, mode); err != nil {
			return nil, "", err
		}
	}
	if err := os.Chmod(src, mode); err != nil {
		return nil, "", err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return nil, "", err
	}
	empty, err := emptyDir(dst)
	if err != nil {
		return nil, "", err
	}
	removedEmpty := false
	if _, statErr := os.Stat(dst); statErr == nil {
		if empty {
			if err := os.Remove(dst); err != nil {
				return nil, "", err
			}
			removedEmpty = true
		} else {
			aside = dst + ".before-transfer-" + ts
			if err := os.Rename(dst, aside); err != nil {
				return nil, "", err
			}
		}
	}
	back := func() {
		if aside != "" {
			os.Rename(aside, dst)
		} else if removedEmpty {
			os.Mkdir(dst, mode)
		}
	}
	if err := os.Rename(src, dst); err != nil {
		if !errors.Is(err, syscall.EXDEV) {
			back()
			return nil, "", err
		}
		// Another disk: copy.
		if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
			os.RemoveAll(dst)
			back()
			return nil, "", err
		}
		return func() { os.RemoveAll(dst); back() }, aside, nil
	}
	return func() { os.Rename(dst, src); back() }, aside, nil
}
