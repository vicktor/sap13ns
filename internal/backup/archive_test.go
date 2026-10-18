package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// member is one entry of a hand-made archive.
type member struct {
	hdr      tar.Header
	content  string
	unlisted bool // leave it out of the manifest
}

func reg(name, content string) member {
	return member{hdr: tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o644}, content: content}
}

type craftOpts struct {
	edit         func(*Manifest) // after the manifest is computed
	manifestLast bool
	afterTar     string // bytes inside the gzip stream after the tar end marker
	pass         string
}

var craftDump = []byte("PGDMP crafted dump")

// craft writes an archive whose manifest matches its members, so that each
// test case has exactly the defect it is about.
func craft(t *testing.T, members []member, o craftOpts) string {
	t.Helper()
	m := &Manifest{Format: formatVersion, Created: testNow, DB: Member{Name: dumpName, Size: int64(len(craftDump)), SHA256: sha256Hex(craftDump)}}
	m.TotalBytes = m.DB.Size
	for _, mb := range members {
		if !mb.unlisted && mb.hdr.Typeflag == tar.TypeReg {
			m.Files = append(m.Files, Member{Name: mb.hdr.Name, Size: int64(len(mb.content)), SHA256: sha256Hex([]byte(mb.content))})
			m.TotalBytes += int64(len(mb.content))
		}
	}
	m.FileCount = len(m.Files)
	if o.edit != nil {
		o.edit(m)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "sap13ns-20261018-033000.tar.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var w io.Writer = f
	var enc io.WriteCloser
	if o.pass != "" {
		enc, _ = newEncrypter(f, o.pass, testKDF)
		w = enc
	}
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	add := func(h tar.Header, content string) {
		if h.Typeflag == tar.TypeReg && h.Size == 0 {
			h.Size = int64(len(content))
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := io.WriteString(tw, content); err != nil {
				t.Fatal(err)
			}
		}
	}
	all := []member{{hdr: tar.Header{Typeflag: tar.TypeReg, Name: dumpName, Mode: 0o600}, content: string(craftDump)}}
	all = append(all, members...)
	man := reg(manifestName, string(raw))
	if o.manifestLast {
		all = append(all, man)
	} else {
		all = append([]member{man}, all...)
	}
	for _, mb := range all {
		add(mb.hdr, mb.content)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	io.WriteString(gz, o.afterTar)
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if enc != nil {
		enc.Close()
	}
	return path
}

func TestVerifyCraftedArchive(t *testing.T) {
	path := craft(t, []member{reg("uploads/2026/10/a.jpg", "jpeg bytes"), reg("uploads/b.txt", "")}, craftOpts{})
	m, err := Verify(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if m.FileCount != 2 || m.DB.Size != int64(len(craftDump)) {
		t.Errorf("manifest = %+v", m)
	}
	// A passphrase given for an unencrypted archive is not needed and not an error.
	if _, err := Verify(path, testPassphrase); err != nil {
		t.Errorf("unencrypted archive with a passphrase: %v", err)
	}
}

func TestTarSlipRejected(t *testing.T) {
	typed := func(typ byte, name, link string) member {
		return member{hdr: tar.Header{Typeflag: typ, Name: name, Linkname: link, Mode: 0o644}}
	}
	unlisted := func(m member) member { m.unlisted = true; return m }
	cases := map[string]member{
		"dotdot":             reg("uploads/../../evil.txt", "x"),
		"dotdot unlisted":    unlisted(reg("uploads/../../evil.txt", "x")),
		"dotdot at start":    unlisted(reg("../evil.txt", "x")),
		"absolute":           unlisted(reg("/etc/cron.d/evil", "x")),
		"absolute in upload": reg("uploads//etc/evil", "x"),
		"dot segment":        reg("uploads/./a.txt", "x"),
		"backslash":          reg(`uploads/..\..\evil.txt`, "x"),
		"control character":  reg("uploads/a\nb.txt", "x"),
		"symlink":            typed(tar.TypeSymlink, "uploads/link.jpg", "/etc/passwd"),
		"relative symlink":   typed(tar.TypeSymlink, "uploads/link.jpg", "../../.."),
		"hardlink":           typed(tar.TypeLink, "uploads/hard.jpg", "/etc/shadow"),
		"char device":        typed(tar.TypeChar, "uploads/tty", ""),
		"block device":       typed(tar.TypeBlock, "uploads/sda", ""),
		"fifo":               typed(tar.TypeFifo, "uploads/pipe", ""),
		"directory":          typed(tar.TypeDir, "uploads/dir", ""),
		"global pax header":  {hdr: tar.Header{Typeflag: tar.TypeXGlobalHeader, Name: "pax_global_header", PAXRecords: map[string]string{"comment": "x"}}},
	}
	for name, mb := range cases {
		path := craft(t, []member{reg("uploads/ok.txt", "fine"), mb}, craftOpts{})
		if _, err := Verify(path, ""); !errors.Is(err, ErrUnsafeMember) {
			t.Errorf("%s: err = %v, want ErrUnsafeMember", name, err)
		}
	}
}

func TestCorruptArchivesRejected(t *testing.T) {
	ok := []member{reg("uploads/a.txt", "alpha"), reg("uploads/b.txt", "beta")}
	cases := map[string]string{
		"not in manifest": craft(t, append(ok, member{hdr: tar.Header{Typeflag: tar.TypeReg, Name: "uploads/extra.txt"}, content: "x", unlisted: true}), craftOpts{}),
		"missing member": craft(t, ok, craftOpts{edit: func(m *Manifest) {
			m.Files = append(m.Files, Member{Name: "uploads/ghost.txt", Size: 1, SHA256: sha256Hex([]byte("g"))})
			m.FileCount++
			m.TotalBytes++
		}}),
		"duplicate member": craft(t, append(ok, member{hdr: tar.Header{Typeflag: tar.TypeReg, Name: "uploads/a.txt"}, content: "alpha", unlisted: true}), craftOpts{}),
		"size mismatch": craft(t, ok, craftOpts{edit: func(m *Manifest) {
			m.Files[0].Size++
			m.TotalBytes++
		}}),
		"hash mismatch":        craft(t, ok, craftOpts{edit: func(m *Manifest) { m.Files[1].SHA256 = sha256Hex([]byte("other")) }}),
		"dump hash mismatch":   craft(t, ok, craftOpts{edit: func(m *Manifest) { m.DB.SHA256 = sha256Hex([]byte("other")) }}),
		"manifest last":        craft(t, ok, craftOpts{manifestLast: true}),
		"format version":       craft(t, ok, craftOpts{edit: func(m *Manifest) { m.Format = 2 }}),
		"file count":           craft(t, ok, craftOpts{edit: func(m *Manifest) { m.FileCount = 1 }}),
		"total bytes":          craft(t, ok, craftOpts{edit: func(m *Manifest) { m.TotalBytes = 1 }}),
		"listed twice":         craft(t, ok, craftOpts{edit: func(m *Manifest) { m.Files[1] = m.Files[0]; m.TotalBytes = m.DB.Size + 2*m.Files[0].Size }}),
		"huge declared size":   craft(t, ok, craftOpts{edit: func(m *Manifest) { m.Files[0].Size = maxFileBytes + 1; m.TotalBytes = -1 }}),
		"dump renamed":         craft(t, ok, craftOpts{edit: func(m *Manifest) { m.DB.Name = "uploads/db.dump" }}),
		"upper-case hash":      craft(t, ok, craftOpts{edit: func(m *Manifest) { m.Files[0].SHA256 = strings.ToUpper(m.Files[0].SHA256) }}),
		"junk after tar":       craft(t, ok, craftOpts{afterTar: "PWNED"}),
		"encrypted, bad hash":  craft(t, ok, craftOpts{pass: testPassphrase, edit: func(m *Manifest) { m.Files[0].SHA256 = sha256Hex(nil) }}),
		"upload outside dir":   craft(t, []member{reg("db.dump.txt", "x")}, craftOpts{}),
		"manifest member dupe": craft(t, append(ok, member{hdr: tar.Header{Typeflag: tar.TypeReg, Name: manifestName}, content: "{}", unlisted: true}), craftOpts{}),
	}
	for name, path := range cases {
		pass := ""
		if strings.HasPrefix(name, "encrypted") {
			pass = testPassphrase
		}
		if _, err := Verify(path, pass); !errors.Is(err, ErrCorrupt) && !errors.Is(err, ErrUnsafeMember) {
			t.Errorf("%s: err = %v, want ErrCorrupt", name, err)
		}
	}

	notBackup := filepath.Join(t.TempDir(), "x.tar.gz")
	os.WriteFile(notBackup, []byte("just some text"), 0o600)
	if _, err := Verify(notBackup, ""); !errors.Is(err, ErrCorrupt) {
		t.Errorf("not a backup: err = %v", err)
	}
}

func TestMemberCountCapped(t *testing.T) {
	old := maxFiles
	maxFiles = 2
	defer func() { maxFiles = old }()
	path := craft(t, []member{reg("uploads/1", "1"), reg("uploads/2", "2"), reg("uploads/3", "3")}, craftOpts{})
	if _, err := Verify(path, ""); !errors.Is(err, ErrCorrupt) {
		t.Errorf("3 files with a cap of 2: err = %v", err)
	}
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"1": "1", "2": "2", "3": "3"})
	root, _ := os.OpenRoot(dir)
	defer root.Close()
	if _, err := scanUploads(context.Background(), root, testLog(t)); err == nil {
		t.Error("Create would write an archive that Restore refuses")
	}
}

func TestDamagedArchiveRejected(t *testing.T) {
	big := string(randBytes(300 << 10)) // incompressible, several flate blocks
	for _, pass := range []string{"", testPassphrase} {
		path := craft(t, []member{reg("uploads/big.bin", big), reg("uploads/small.txt", "small")}, craftOpts{pass: pass})
		good, _ := os.ReadFile(path)
		// Every byte of an encrypted archive is authenticated. In a plain
		// one, the gzip header's mtime/OS bytes and deflate's padding bits
		// carry no content, so a flip there may leave it intact; the
		// content itself is covered by SHA-256 and the gzip CRC.
		mustFail := []int{100, len(good) / 3, len(good) / 2, len(good) - 5, len(good) - 1}
		mayPass := []int{4, 9, 10, len(good) - 9}
		if pass != "" {
			mustFail, mayPass = append(mustFail, append(mayPass, 0, headerSize)...), nil
		}
		for _, off := range append(mustFail, mayPass...) {
			bad := bytes.Clone(good)
			bad[off] ^= 0x40
			os.WriteFile(path, bad, 0o600)
			m, err := Verify(path, pass)
			switch {
			case err == nil && slices.Contains(mustFail, off):
				t.Errorf("encrypted=%v: flipped byte %d of %d went unnoticed", pass != "", off, len(good))
			case err == nil && (m.DB.SHA256 != sha256Hex(craftDump) || m.FileCount != 2):
				t.Errorf("encrypted=%v: flipped byte %d: accepted with a different manifest", pass != "", off)
			case err != nil && !errors.Is(err, ErrCorrupt) && !errors.Is(err, ErrWrongPassphrase):
				t.Errorf("encrypted=%v: flipped byte %d: err = %v", pass != "", off, err)
			}
		}
		for _, n := range []int{0, 5, 30, len(good) / 2, len(good) - 8, len(good) - 1} {
			os.WriteFile(path, good[:n], 0o600)
			if _, err := Verify(path, pass); !errors.Is(err, ErrCorrupt) {
				t.Errorf("encrypted=%v: truncated to %d of %d: err = %v", pass != "", n, len(good), err)
			}
		}
		os.WriteFile(path, append(bytes.Clone(good), "trailing"...), 0o600)
		if _, err := Verify(path, pass); !errors.Is(err, ErrCorrupt) {
			t.Errorf("encrypted=%v: trailing data: err = %v", pass != "", err)
		}
	}
}

func TestRestoreRejectsTarSlip(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "site", "uploads")
	writeTree(t, target, map[string]string{"live.jpg": "live"})
	for _, mb := range []member{
		reg("uploads/../escape.txt", "pwned"),
		reg("uploads/../../escape.txt", "pwned"),
		{hdr: tar.Header{Typeflag: tar.TypeSymlink, Name: "uploads/escape", Linkname: base}},
	} {
		path := craft(t, []member{reg("uploads/ok.txt", "ok"), mb}, craftOpts{})
		err := Restore(context.Background(), path, RestoreOptions{UploadsDir: target, SkipDB: true})
		if !errors.Is(err, ErrUnsafeMember) {
			t.Errorf("%s: err = %v, want ErrUnsafeMember", mb.hdr.Name, err)
		}
	}
	for _, p := range []string{filepath.Join(base, "escape.txt"), filepath.Join(base, "site", "escape.txt")} {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("%s was written", p)
		}
	}
	if got := readTree(t, target); len(got) != 1 || got["live.jpg"] != "live" {
		t.Errorf("uploads changed: %v", got)
	}
	if names := dirNames(t, filepath.Join(base, "site")); len(names) != 1 {
		t.Errorf("refused restore left %v", names)
	}
}

// The extractor does not rely on name checks alone: os.Root refuses to leave
// the extraction directory, even through a symlink planted inside it.
func TestExtractorConfined(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "x")
	os.Mkdir(dir, 0o700)
	os.Symlink(base, filepath.Join(dir, "link"))
	x, err := newExtractor(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer x.close()
	for _, rel := range []string{"../escape.txt", "a/../../escape.txt", "link/escape.txt", "/escape.txt"} {
		if err := x.file(rel, time.Now(), strings.NewReader("pwned")); err == nil {
			t.Errorf("%s: written", rel)
		}
	}
	if _, err := os.Lstat(filepath.Join(base, "escape.txt")); err == nil {
		t.Error("escape.txt was written outside the extraction directory")
	}
	if err := x.file("2026/10/ok.txt", time.Now(), strings.NewReader("ok")); err != nil {
		t.Fatal(err)
	}
	if err := x.file("2026/10/ok.txt", time.Now(), strings.NewReader("again")); err == nil {
		t.Error("an existing file was overwritten")
	}
}

func TestSafeName(t *testing.T) {
	for name, want := range map[string]bool{
		"uploads/2026/10/a.jpg":   true,
		"uploads/a b.jpg":         true,
		"uploads/ñandú.png":       true,
		"":                        false,
		".":                       false,
		"..":                      false,
		"/abs":                    false,
		"a/../b":                  false,
		"a/./b":                   false,
		"a//b":                    false,
		"a/":                      false,
		`a\b`:                     false,
		"a\x00b":                  false,
		"a\x7fb":                  false,
		"\xff":                    false,
		strings.Repeat("a", 2000): false,
	} {
		if got := safeName(name); got != want {
			t.Errorf("safeName(%q) = %v, want %v", name, got, want)
		}
	}
}
