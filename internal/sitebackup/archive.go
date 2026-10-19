package sitebackup

import (
	"archive/zip"
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"strings"

	"sap13ns/internal/backup"
)

// reader is an open archive: a .zip, or the plaintext of a .zip.enc
// decrypted to a temporary file first (a .zip needs random access).
// Every member is checked before anything is read: a safe name, a regular
// file, listed in the manifest with the size the .zip declares, and no
// member unlisted; reading one checks its size and SHA-256 at the end.
type reader struct {
	zr      *zip.ReadCloser
	tmp     string // decrypted copy, removed by close
	files   map[string]*zip.File
	members map[string]backup.Member
	m       *Manifest
}

func (r *reader) close() {
	if r.zr != nil {
		r.zr.Close()
	}
	if r.tmp != "" {
		os.Remove(r.tmp)
	}
}

// open opens and checks an archive. tmpDir holds the decrypted copy of an
// encrypted one (0600), on the same disk as the backups preferably.
func open(file, passphrase, tmpDir string) (*reader, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	br := bufio.NewReader(f)
	r := &reader{}
	name := file
	if backup.IsEncrypted(br) {
		if passphrase == "" {
			f.Close()
			return nil, fmt.Errorf("%w: the backup is encrypted; set the backup passphrase", backup.ErrWrongPassphrase)
		}
		plain, err := backup.Decrypt(br, passphrase)
		if err != nil {
			f.Close()
			return nil, err
		}
		tf, err := os.CreateTemp(tmpDir, ".decrypted-*")
		if err != nil {
			f.Close()
			return nil, err
		}
		r.tmp = tf.Name()
		tf.Chmod(0o600)
		_, err = io.Copy(tf, plain)
		f.Close()
		if cerr := tf.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			r.close()
			return nil, err
		}
		name = r.tmp
	} else {
		f.Close()
	}
	if r.zr, err = zip.OpenReader(name); err != nil {
		r.close()
		return nil, fmt.Errorf("%w: %v", ErrNotSiteBackup, err)
	}
	if err := r.check(); err != nil {
		r.close()
		return nil, err
	}
	return r, nil
}

func (r *reader) check() error {
	if len(r.zr.File) > maxFiles+20 {
		return fmt.Errorf("%w: %d members", backup.ErrCorrupt, len(r.zr.File))
	}
	r.files = make(map[string]*zip.File, len(r.zr.File))
	for _, f := range r.zr.File {
		mode := f.Mode()
		switch {
		case strings.HasSuffix(f.Name, "/") && mode.IsDir():
			continue // a directory entry, which a tool may add
		case !backup.SafeName(f.Name):
			return fmt.Errorf("%w: unsafe path %q", backup.ErrUnsafeMember, f.Name)
		case !mode.IsRegular() || mode&(fs.ModeSetuid|fs.ModeSetgid) != 0:
			return fmt.Errorf("%w: %q is not a regular file", backup.ErrUnsafeMember, f.Name)
		case f.Method != zip.Store && f.Method != zip.Deflate:
			return fmt.Errorf("%w: %q: compression method %d", backup.ErrCorrupt, f.Name, f.Method)
		}
		key := strings.ToLower(f.Name)
		if r.files[key] != nil {
			return fmt.Errorf("%w: %q twice", backup.ErrCorrupt, f.Name)
		}
		r.files[key] = f
	}
	mf := r.files[NameManifest]
	if mf == nil {
		return ErrNotSiteBackup
	}
	if mf.UncompressedSize64 > uint64(maxJSONBytes) {
		return fmt.Errorf("%w: manifest too large", backup.ErrCorrupt)
	}
	rc, err := mf.Open()
	if err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(rc, maxJSONBytes+1))
	rc.Close()
	if err != nil {
		return fmt.Errorf("%w: manifest: %v", backup.ErrCorrupt, err)
	}
	m := &Manifest{}
	if err := json.Unmarshal(raw, m); err != nil || m.Kind != manifestKind {
		return ErrNotSiteBackup
	}
	if m.Format != FormatVersion {
		return fmt.Errorf("%w: format %d, this version reads %d", ErrNotSiteBackup, m.Format, FormatVersion)
	}
	r.members = make(map[string]backup.Member, len(m.Files))
	var total int64
	for _, mem := range m.Files {
		key := strings.ToLower(mem.Name)
		zf := r.files[key]
		switch {
		case !backup.SafeName(mem.Name) || key == strings.ToLower(NameManifest):
			return fmt.Errorf("%w: unsafe path %q", backup.ErrUnsafeMember, mem.Name)
		case zf == nil || zf.Name != mem.Name:
			return fmt.Errorf("%w: %s is missing", backup.ErrCorrupt, mem.Name)
		case r.members[key].Name != "":
			return fmt.Errorf("%w: %s listed twice", backup.ErrCorrupt, mem.Name)
		case mem.Size < 0 || mem.Size > maxFileBytes || zf.UncompressedSize64 != uint64(mem.Size) || len(mem.SHA256) != 64:
			return fmt.Errorf("%w: %s: size or checksum", backup.ErrCorrupt, mem.Name)
		}
		if total += mem.Size; total > maxTotalBytes {
			return fmt.Errorf("%w: the backup unpacks to more than %d bytes", backup.ErrCorrupt, maxTotalBytes)
		}
		r.members[key] = mem
	}
	for key, f := range r.files {
		if key != strings.ToLower(NameManifest) && r.members[key].Name == "" {
			return fmt.Errorf("%w: %s is not in the manifest", backup.ErrCorrupt, f.Name)
		}
	}
	m.TotalBytes = total
	r.m = m
	return nil
}

// member opens a member for reading; the reader fails with ErrCorrupt
// when the data does not match the manifest, at the latest at its end.
func (r *reader) member(name string) (io.ReadCloser, error) {
	key := strings.ToLower(name)
	mem, ok := r.members[key]
	if !ok {
		return nil, fmt.Errorf("%w: %s is missing", backup.ErrCorrupt, name)
	}
	rc, err := r.files[key].Open()
	if err != nil {
		return nil, err
	}
	return &checked{rc: rc, r: io.LimitReader(rc, mem.Size+1), sum: sha256.New(), m: mem}, nil
}

func (r *reader) has(name string) bool { _, ok := r.members[strings.ToLower(name)]; return ok }

// readJSON decodes a sidecar file; a missing one leaves v alone.
func (r *reader) readJSON(name string, v any) error {
	if !r.has(name) {
		return nil
	}
	rc, err := r.member(name)
	if err != nil {
		return err
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%w: %s: %v", backup.ErrCorrupt, name, err)
	}
	return nil
}

type checked struct {
	rc  io.ReadCloser
	r   io.Reader
	sum hash.Hash
	n   int64
	m   backup.Member
}

func (c *checked) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.sum.Write(p[:n])
	c.n += int64(n)
	if c.n > c.m.Size {
		return n, fmt.Errorf("%w: %s is larger than its manifest says", backup.ErrCorrupt, c.m.Name)
	}
	if errors.Is(err, io.EOF) && (c.n != c.m.Size || hex.EncodeToString(c.sum.Sum(nil)) != c.m.SHA256) {
		return n, fmt.Errorf("%w: %s does not match its checksum", backup.ErrCorrupt, c.m.Name)
	}
	return n, err
}

func (c *checked) Close() error { return c.rc.Close() }

// Verify reads a whole backup and checks every member against the
// manifest.
func Verify(file, passphrase, tmpDir string) (*Manifest, error) {
	r, err := open(file, passphrase, tmpDir)
	if err != nil {
		return nil, err
	}
	defer r.close()
	for _, mem := range r.m.Files {
		rc, err := r.member(mem.Name)
		if err != nil {
			return nil, err
		}
		_, err = io.Copy(io.Discard, rc)
		rc.Close()
		if err != nil {
			return nil, err
		}
	}
	return r.m, nil
}

// ReadManifest returns the manifest of a backup, checking the layout but
// not the data.
func ReadManifest(file, passphrase, tmpDir string) (*Manifest, error) {
	r, err := open(file, passphrase, tmpDir)
	if err != nil {
		return nil, err
	}
	defer r.close()
	return r.m, nil
}

// Plain returns a reader of the .zip of a backup: the file itself, or its
// plaintext when encrypted. The reader of an encrypted backup fails, at
// the latest at the end, when the file was modified or cut; a download
// must then be aborted rather than completed.
func Plain(file, passphrase string) (io.ReadCloser, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	br := bufio.NewReader(f)
	if !backup.IsEncrypted(br) {
		return struct {
			io.Reader
			io.Closer
		}{br, f}, nil
	}
	if passphrase == "" {
		f.Close()
		return nil, fmt.Errorf("%w: the backup is encrypted; set the backup passphrase", backup.ErrWrongPassphrase)
	}
	plain, err := backup.Decrypt(br, passphrase)
	if err != nil {
		f.Close()
		return nil, err
	}
	return struct {
		io.Reader
		io.Closer
	}{plain, f}, nil
}
