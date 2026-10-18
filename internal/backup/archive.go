package backup

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	formatVersion = 1
	manifestName  = "manifest.json"
	dumpName      = "db.dump"
	uploadsPrefix = "uploads/"
)

// Limits enforced when reading an archive, and when writing one so that
// Create never produces an archive that Restore would refuse. They are
// variables so that tests can lower them.
var (
	maxFiles         = 500_000
	maxManifestBytes = int64(128 << 20)
	maxFileBytes     = int64(64 << 30) // one upload
	maxDumpBytes     = int64(4 << 40)
	maxNameLen       = 1024
	maxTrailingBytes = int64(64 << 10) // zero padding after the tar end marker
)

// Manifest describes an archive. It is always the first member, so that List
// can read it without decompressing the rest.
type Manifest struct {
	Format     int       `json:"format"`
	AppVersion string    `json:"app_version"`
	Created    time.Time `json:"created"`
	Database   string    `json:"database"`         // name only, never credentials
	PGVersion  string    `json:"postgres_version"` // server_version at dump time
	DBSize     int64     `json:"db_size"`          // pg_database_size at dump time
	FileCount  int       `json:"file_count"`       // uploads
	TotalBytes int64     `json:"total_bytes"`      // db.dump plus uploads: what a restore writes
	DB         Member    `json:"db"`
	Files      []Member  `json:"files"`
}

// Member is the size and SHA-256 of one archive member.
type Member struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func (m *Manifest) validate() error {
	if m.Format != formatVersion {
		return fmt.Errorf("%w: unsupported manifest format %d", ErrCorrupt, m.Format)
	}
	if m.DB.Name != dumpName {
		return fmt.Errorf("%w: manifest names the dump %q", ErrCorrupt, m.DB.Name)
	}
	if err := m.DB.check(maxDumpBytes); err != nil {
		return err
	}
	if len(m.Files) > maxFiles {
		return fmt.Errorf("%w: %d files, at most %d are accepted", ErrCorrupt, len(m.Files), maxFiles)
	}
	if m.FileCount != len(m.Files) {
		return fmt.Errorf("%w: manifest counts %d files but lists %d", ErrCorrupt, m.FileCount, len(m.Files))
	}
	total := m.DB.Size
	seen := make(map[string]bool, len(m.Files))
	for _, f := range m.Files {
		if !strings.HasPrefix(f.Name, uploadsPrefix) || !safeName(f.Name) {
			return fmt.Errorf("%w: unsafe path %q in manifest", ErrUnsafeMember, f.Name)
		}
		if seen[f.Name] {
			return fmt.Errorf("%w: %q is listed twice", ErrCorrupt, f.Name)
		}
		seen[f.Name] = true
		if err := f.check(maxFileBytes); err != nil {
			return err
		}
		total += f.Size
	}
	if total != m.TotalBytes {
		return fmt.Errorf("%w: manifest total is %d bytes, members add up to %d", ErrCorrupt, m.TotalBytes, total)
	}
	return nil
}

func (e Member) check(max int64) error {
	if e.Size < 0 || e.Size > max {
		return fmt.Errorf("%w: %q has an invalid size %d", ErrCorrupt, e.Name, e.Size)
	}
	if b, err := hex.DecodeString(e.SHA256); err != nil || len(b) != sha256.Size || strings.ToLower(e.SHA256) != e.SHA256 {
		return fmt.Errorf("%w: %q has an invalid SHA-256", ErrCorrupt, e.Name)
	}
	return nil
}

// safeName reports whether name is a canonical, relative, slash-separated
// path that cannot climb out of the directory it is extracted to.
func safeName(name string) bool {
	if name == "" || len(name) > maxNameLen || !utf8.ValidString(name) ||
		path.IsAbs(name) || path.Clean(name) != name {
		return false
	}
	for _, el := range strings.Split(name, "/") {
		if el == ".." || el == "." {
			return false
		}
	}
	return !strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f || r == '\\' })
}

func checkHeader(h *tar.Header) error {
	if h.Typeflag != tar.TypeReg {
		return fmt.Errorf("%w: %q is not a regular file (type %q)", ErrUnsafeMember, h.Name, h.Typeflag)
	}
	if !safeName(h.Name) {
		return fmt.Errorf("%w: unsafe path %q", ErrUnsafeMember, h.Name)
	}
	if h.Size < 0 {
		return fmt.Errorf("%w: %q has a negative size", ErrCorrupt, h.Name)
	}
	return nil
}

// archiveReader is the tar stream inside a possibly encrypted gzip file.
type archiveReader struct {
	f  *os.File
	br *bufio.Reader // what gzip reads from: the file, or the decrypter
	gz *gzip.Reader
	tr *tar.Reader
}

func openArchive(name, passphrase string) (*archiveReader, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	a, err := newArchiveReader(f, passphrase)
	if err != nil {
		f.Close()
		return nil, err
	}
	return a, nil
}

func newArchiveReader(f *os.File, passphrase string) (*archiveReader, error) {
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", f.Name())
	}
	raw := bufio.NewReaderSize(f, chunkSize+tagSize)
	magic, _ := raw.Peek(len(encMagic))
	var src io.Reader = raw
	switch {
	case bytes.Equal(magic, []byte(encMagic)):
		if passphrase == "" {
			return nil, ErrPassphraseRequired
		}
		if src, err = newDecrypter(raw, passphrase); err != nil {
			return nil, err
		}
	case len(magic) >= 2 && magic[0] == 0x1f && magic[1] == 0x8b:
	default:
		return nil, fmt.Errorf("%w: not a sap13ns backup", ErrCorrupt)
	}
	// gzip reads a *bufio.Reader directly, so after its stream ends br
	// holds exactly what follows it.
	br := bufio.NewReaderSize(src, 64<<10)
	gz, err := gzip.NewReader(br)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	gz.Multistream(false)
	return &archiveReader{f: f, br: br, gz: gz, tr: tar.NewReader(gz)}, nil
}

func (a *archiveReader) close() { a.f.Close() }

// next returns the next member header, or io.EOF at the tar end marker.
func (a *archiveReader) next() (*tar.Header, error) {
	h, err := a.tr.Next()
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	return h, err
}

// manifest reads the first member, which must be the manifest, and returns
// it with the SHA-256 of its bytes.
func (a *archiveReader) manifest() (*Manifest, [32]byte, error) {
	var digest [32]byte
	h, err := a.next()
	if err == io.EOF {
		return nil, digest, fmt.Errorf("%w: empty archive", ErrCorrupt)
	}
	if err != nil {
		return nil, digest, err
	}
	if err := checkHeader(h); err != nil {
		return nil, digest, err
	}
	if h.Name != manifestName {
		return nil, digest, fmt.Errorf("%w: first member is %q, not %s", ErrCorrupt, h.Name, manifestName)
	}
	if h.Size > maxManifestBytes {
		return nil, digest, fmt.Errorf("%w: manifest too large", ErrCorrupt)
	}
	raw, err := io.ReadAll(a.tr)
	if err != nil {
		return nil, digest, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, digest, fmt.Errorf("%w: manifest: %v", ErrCorrupt, err)
	}
	if err := m.validate(); err != nil {
		return nil, digest, err
	}
	return &m, sha256.Sum256(raw), nil
}

// finish checks that only zero padding follows the tar end marker and that
// nothing follows the gzip stream. For an encrypted archive, reaching the end
// of br also authenticates the final chunk.
func (a *archiveReader) finish() error {
	pad, err := io.ReadAll(io.LimitReader(a.gz, maxTrailingBytes+1))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if int64(len(pad)) > maxTrailingBytes || !allZero(pad) {
		return fmt.Errorf("%w: data after the end of the archive", ErrCorrupt)
	}
	if _, err := a.br.ReadByte(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("%w: data after the end of the compressed stream", ErrCorrupt)
		}
		return err
	}
	return nil
}

// memberFunc receives db.dump and each upload while an archive is walked. It
// may read as much of r as it likes; the rest is read and discarded for it.
type memberFunc func(e Member, h *tar.Header, r io.Reader) error

// walk reads a whole archive and checks its structure, and each member's
// size and SHA-256 against the manifest. Whatever fn did with the content
// must not be trusted until walk returns nil. It returns the manifest and the
// SHA-256 of its bytes.
func walk(name, passphrase string, fn memberFunc) (*Manifest, [32]byte, error) {
	a, err := openArchive(name, passphrase)
	if err != nil {
		return nil, [32]byte{}, err
	}
	defer a.close()
	m, digest, err := a.manifest()
	if err != nil {
		return nil, digest, err
	}
	want := make(map[string]Member, len(m.Files)+1)
	want[m.DB.Name] = m.DB
	for _, f := range m.Files {
		want[f.Name] = f
	}
	seen := make(map[string]bool, len(want))
	for {
		h, err := a.next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, digest, err
		}
		if err := checkHeader(h); err != nil {
			return nil, digest, err
		}
		e, ok := want[h.Name]
		switch {
		case !ok:
			return nil, digest, fmt.Errorf("%w: %q is not in the manifest", ErrCorrupt, h.Name)
		case seen[h.Name]:
			return nil, digest, fmt.Errorf("%w: %q appears twice", ErrCorrupt, h.Name)
		case h.Size != e.Size:
			return nil, digest, fmt.Errorf("%w: %q is %d bytes, the manifest says %d", ErrCorrupt, h.Name, h.Size, e.Size)
		}
		seen[h.Name] = true
		sum := sha256.New()
		r := io.TeeReader(a.tr, sum)
		if fn != nil {
			if err := fn(e, h, r); err != nil {
				return nil, digest, err
			}
		}
		if _, err := io.Copy(io.Discard, r); err != nil {
			return nil, digest, fmt.Errorf("%w: %s: %v", ErrCorrupt, h.Name, err)
		}
		if got := hex.EncodeToString(sum.Sum(nil)); got != e.SHA256 {
			return nil, digest, fmt.Errorf("%w: SHA-256 mismatch for %q", ErrCorrupt, h.Name)
		}
	}
	if len(seen) != len(want) {
		for n := range want {
			if !seen[n] {
				return nil, digest, fmt.Errorf("%w: %q is missing", ErrCorrupt, n)
			}
		}
	}
	if err := a.finish(); err != nil {
		return nil, digest, err
	}
	return m, digest, nil
}

// Verify reads a whole archive, decrypting it if needed, and checks every
// member's size and SHA-256 against the manifest without restoring anything.
func Verify(archivePath, passphrase string) (*Manifest, error) {
	m, _, err := walk(archivePath, passphrase, nil)
	return m, err
}

// readManifest returns the manifest of an unencrypted archive without
// reading the rest of it.
func readManifest(name string) (*Manifest, error) {
	a, err := openArchive(name, "")
	if err != nil {
		return nil, err
	}
	defer a.close()
	m, _, err := a.manifest()
	return m, err
}

// ------------------------------------------------------------------ writing --

var errUploadChanged = errors.New("upload changed during backup")

// upload is a file found by scanUploads.
type upload struct {
	rel  string      // slash-separated, below the uploads directory
	info fs.FileInfo // from the scan, to notice a file replaced meanwhile
	sum  Member
}

// scanUploads lists and hashes the regular files below root. Symlinks are
// skipped, devices, sockets and pipes are refused, and the media package's
// in-progress files (the top-level .tmp directory and *.tmp) are left out.
func scanUploads(ctx context.Context, root *os.Root, log *slog.Logger) ([]upload, error) {
	var files []upload
	err := fs.WalkDir(root.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		switch t := d.Type(); {
		case rel == ".":
			return nil
		case t.IsDir():
			if rel == ".tmp" {
				return fs.SkipDir
			}
			return nil
		case t&fs.ModeSymlink != 0:
			log.Warn("backup skips symlink in uploads", "path", rel)
			return nil
		case !t.IsRegular():
			return fmt.Errorf("uploads: refusing %s: not a regular file (%s)", rel, t.Type())
		case strings.HasSuffix(rel, ".tmp"):
			return nil
		}
		name := uploadsPrefix + rel
		if !safeName(name) {
			return fmt.Errorf("uploads: refusing %q: name cannot be restored safely", rel)
		}
		if len(files) == maxFiles {
			return fmt.Errorf("uploads: more than %d files", maxFiles)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		u := upload{rel: rel, info: info, sum: Member{Name: name, Size: info.Size()}}
		if u.sum.Size > maxFileBytes {
			return fmt.Errorf("uploads: %s is larger than %d bytes", rel, maxFileBytes)
		}
		sum := sha256.New()
		if err := copyUpload(root, &u, sum); err != nil {
			return err
		}
		u.sum.SHA256 = hex.EncodeToString(sum.Sum(nil))
		files = append(files, u)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// copyUpload copies exactly the scanned size of an upload to w, failing with
// errUploadChanged if the file was replaced, resized or removed since the
// scan, or if its content no longer matches the scan's hash.
func copyUpload(root *os.Root, u *upload, w io.Writer) error {
	f, err := root.Open(u.rel)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %s was removed", errUploadChanged, u.rel)
	}
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	// SameFile also catches a regular file swapped for a symlink: the open
	// followed it to another inode.
	if !fi.Mode().IsRegular() || !os.SameFile(fi, u.info) || fi.Size() != u.info.Size() || !fi.ModTime().Equal(u.info.ModTime()) {
		return fmt.Errorf("%w: %s", errUploadChanged, u.rel)
	}
	sum := sha256.New()
	if _, err := io.CopyN(io.MultiWriter(w, sum), f, u.sum.Size); err != nil {
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("%w: %s shrank", errUploadChanged, u.rel)
		}
		return err
	}
	if u.sum.SHA256 != "" && hex.EncodeToString(sum.Sum(nil)) != u.sum.SHA256 {
		return fmt.Errorf("%w: %s", errUploadChanged, u.rel)
	}
	return nil
}

// writeTar writes the gzip-compressed tar stream: manifest, dump, uploads.
func writeTar(ctx context.Context, w io.Writer, m *Manifest, sp *spool, root *os.Root, files []upload) error {
	// The dump is compressed by pg_dump and most uploads are images or
	// video, so harder compression would cost time for little gain.
	gz, err := gzip.NewWriterLevel(w, gzip.BestSpeed)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(gz)
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if int64(len(raw)) > maxManifestBytes {
		return fmt.Errorf("manifest too large (%d bytes)", len(raw))
	}
	if err := tw.WriteHeader(fileHeader(manifestName, int64(len(raw)), m.Created)); err != nil {
		return err
	}
	if _, err := tw.Write(raw); err != nil {
		return err
	}

	r, err := sp.reader()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(fileHeader(dumpName, m.DB.Size, m.Created)); err != nil {
		return err
	}
	if n, err := io.Copy(tw, r); err != nil {
		return fmt.Errorf("copy dump: %w", err)
	} else if n != m.DB.Size {
		return fmt.Errorf("copy dump: %d bytes, expected %d", n, m.DB.Size)
	}

	for i := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		u := &files[i]
		if err := tw.WriteHeader(fileHeader(u.sum.Name, u.sum.Size, u.info.ModTime())); err != nil {
			return err
		}
		if err := copyUpload(root, u, tw); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func fileHeader(name string, size int64, mtime time.Time) *tar.Header {
	return &tar.Header{Typeflag: tar.TypeReg, Name: name, Size: size, Mode: 0o644, ModTime: mtime.UTC()}
}
