// Package archive unpacks the .zip packages people upload, plugins and
// themes, under strict limits. Names are checked before anything is read:
// absolute paths, "..", hidden files, symbolic links and special files are
// refused. Then every file must be on the package's allowlist, within its
// size limit, the total and the entry count, and under a compression
// ratio cap (zip bombs). Extraction enforces the sizes again while it
// reads, because the sizes in the archive's directory are only claims.
package archive

import (
	"archive/zip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
)

// Error is a reason to refuse an archive. Rule is a stable id, such as
// "package.zip-slip"; the plugin scanner reports it.
type Error struct {
	Rule  string
	Entry string
	Msg   string
}

func (e *Error) Error() string {
	if e.Entry == "" {
		return e.Msg
	}
	return fmt.Sprintf("%s: %s", e.Entry, e.Msg)
}

// Errf builds an *Error.
func Errf(rule, entry, format string, args ...any) *Error {
	return &Error{Rule: rule, Entry: entry, Msg: fmt.Sprintf(format, args...)}
}

// Limits bound an archive.
type Limits struct {
	MaxBytes    int64  // the archive itself
	MaxEntries  int    // entries in its directory, folders included
	MaxUnpacked int64  // all files together, unpacked
	MaxRatio    uint64 // uncompressed/compressed, for files over 1 MiB
}

// Zip is an archive whose names were checked.
type Zip struct {
	limits  Limits
	entries []entry
	// Prefix is the top-level folder the package sits in, with its
	// trailing slash, or "" for a package at the root of the archive.
	Prefix  string
	checked bool
}

type entry struct {
	f    *zip.File
	name string // cleaned, relative to the package root
	dir  bool
}

// Open reads the directory of an archive and checks every name. root is
// the file that marks the package (plugin.json, theme.json): the package
// sits at the root of the archive or inside one top-level folder.
func Open(r io.ReaderAt, size int64, root string, l Limits) (*Zip, error) {
	if size > l.MaxBytes {
		return nil, Errf("package.too-large", "", "the archive is larger than %d MiB", l.MaxBytes>>20)
	}
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, Errf("package.invalid", "", "not a valid .zip archive: %v", err)
	}
	if len(zr.File) > l.MaxEntries {
		return nil, Errf("package.too-many-entries", "", "the archive has %d entries (at most %d)", len(zr.File), l.MaxEntries)
	}
	z := &Zip{limits: l}
	for _, f := range zr.File {
		name := f.Name
		if strings.HasPrefix(name, "__MACOSX/") || path.Base(name) == ".DS_Store" {
			continue // macOS archive litter
		}
		if err := CheckName(name); err != nil {
			return nil, err
		}
		mode := f.Mode()
		switch {
		case mode&fs.ModeSymlink != 0:
			return nil, Errf("package.symlink", name, "symbolic links are not allowed")
		case mode&(fs.ModeDevice|fs.ModeCharDevice|fs.ModeNamedPipe|fs.ModeSocket|fs.ModeIrregular) != 0:
			return nil, Errf("package.special-file", name, "special files are not allowed")
		case mode&(fs.ModeSetuid|fs.ModeSetgid) != 0:
			return nil, Errf("package.special-file", name, "setuid and setgid files are not allowed")
		}
		z.entries = append(z.entries, entry{f: f, name: strings.TrimSuffix(name, "/"), dir: f.FileInfo().IsDir()})
	}

	// Accept archives whose content sits inside a single top-level folder.
	hasRoot := slices.ContainsFunc(z.entries, func(e entry) bool { return e.name == root })
	if !hasRoot && len(z.entries) > 0 {
		top, _, _ := strings.Cut(z.entries[0].name, "/")
		if slices.ContainsFunc(z.entries, func(e entry) bool { return e.name == top+"/"+root }) &&
			!slices.ContainsFunc(z.entries, func(e entry) bool { return e.name != top && !strings.HasPrefix(e.name, top+"/") }) {
			z.Prefix = top + "/"
			hasRoot = true
		}
	}
	if !hasRoot {
		return nil, Errf("package.no-manifest", "", "%s is missing at the root of the archive", root)
	}
	kept := z.entries[:0]
	for _, e := range z.entries {
		e.name = strings.TrimPrefix(e.name, z.Prefix)
		if e.name == "" || e.name+"/" == z.Prefix {
			continue // the top-level folder itself
		}
		kept = append(kept, e)
	}
	z.entries = kept
	return z, nil
}

// Files lists the files of the package (names relative to its root),
// folders left out.
func (z *Zip) Files() []string {
	var out []string
	for _, e := range z.entries {
		if !e.dir {
			out = append(out, e.name)
		}
	}
	return out
}

// Has reports whether the package holds the file name.
func (z *Zip) Has(name string) bool {
	return slices.ContainsFunc(z.entries, func(e entry) bool { return !e.dir && e.name == name })
}

// ReadFile reads a file of the package, at most limit bytes.
func (z *Zip) ReadFile(name string, limit int64) ([]byte, error) {
	for _, e := range z.entries {
		if !e.dir && e.name == name {
			return readZipFile(e.f, limit)
		}
	}
	return nil, fs.ErrNotExist
}

// Check applies the package's allowlist before anything is written: allow
// returns the size limit of a file (a name relative to the package root)
// or an *Error. It also refuses duplicate names (compared without case),
// compression methods other than store and deflate, files over their
// limit, a total over MaxUnpacked and compression ratios over MaxRatio.
func (z *Zip) Check(allow func(name string) (int64, error)) error {
	seen := map[string]bool{}
	var declared uint64
	for _, e := range z.entries {
		if e.dir {
			continue
		}
		key := strings.ToLower(e.name)
		if seen[key] {
			return Errf("package.duplicate", e.name, "duplicate entry (names are compared without case)")
		}
		seen[key] = true
		limit, err := allow(e.name)
		if err != nil {
			return err
		}
		if e.f.Method != zip.Store && e.f.Method != zip.Deflate {
			return Errf("package.invalid", e.name, "unsupported compression method %d", e.f.Method)
		}
		if e.f.UncompressedSize64 > uint64(limit) {
			return Errf("package.too-large-file", e.name, "%d bytes; at most %d", e.f.UncompressedSize64, limit)
		}
		if declared += e.f.UncompressedSize64; declared > uint64(z.limits.MaxUnpacked) {
			return Errf("package.zip-bomb", e.name, "the archive unpacks to more than %d MiB", z.limits.MaxUnpacked>>20)
		}
		if e.f.UncompressedSize64 > 1<<20 && e.f.UncompressedSize64 > z.limits.MaxRatio*max(e.f.CompressedSize64, 1) {
			return Errf("package.zip-bomb", e.name, "compression ratio above %d:1", z.limits.MaxRatio)
		}
	}
	return nil
}

// Extract writes the files of a checked package into dest, which must be
// an empty directory, with the limits allow returns, and returns the bytes
// written. The archive's claimed sizes are enforced while it reads.
func (z *Zip) Extract(dest string, allow func(name string) (int64, error)) (int64, error) {
	if err := z.Check(allow); err != nil {
		return 0, err
	}
	var written int64
	for _, e := range z.entries {
		if e.dir {
			continue
		}
		limit, _ := allow(e.name)
		n, err := extractEntry(e.f, dest, e.name, limit)
		if err != nil {
			return written, err
		}
		if written += n; written > z.limits.MaxUnpacked {
			return written, Errf("package.zip-bomb", e.name, "the archive unpacks to more than %d MiB", z.limits.MaxUnpacked>>20)
		}
	}
	return written, nil
}

// CheckName refuses archive entry names that are absolute, climb out with
// "..", have empty or hidden segments, or carry control characters.
func CheckName(name string) error {
	switch {
	case name == "" || len(name) > 300:
		return Errf("package.bad-name", name, "invalid entry name")
	case strings.HasPrefix(name, "/") || strings.Contains(name, "\\") || (len(name) > 1 && name[1] == ':'):
		return Errf("package.zip-slip", name, "absolute paths are not allowed")
	}
	for _, r := range name {
		if unicode.IsControl(r) || r == 0xfffd {
			return Errf("package.bad-name", name, "control characters in the name")
		}
	}
	segs := strings.Split(strings.TrimSuffix(name, "/"), "/")
	for _, seg := range segs {
		switch {
		case seg == "..":
			return Errf("package.zip-slip", name, `".." in the path`)
		case seg == "" || seg == ".":
			return Errf("package.bad-name", name, "empty path segment")
		case strings.HasPrefix(seg, "."):
			return Errf("package.hidden-file", name, "hidden files are not allowed")
		}
	}
	return nil
}

func readZipFile(f *zip.File, limit int64) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("larger than %d bytes", limit)
	}
	return b, nil
}

func extractEntry(f *zip.File, dest, name string, limit int64) (int64, error) {
	target := filepath.Join(dest, filepath.FromSlash(name))
	if rel, err := filepath.Rel(dest, target); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return 0, Errf("package.zip-slip", name, "path escapes the package directory")
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return 0, Errf("package.invalid", name, "%v", err)
	}
	rc, err := f.Open()
	if err != nil {
		return 0, Errf("package.invalid", name, "%v", err)
	}
	defer rc.Close()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return 0, Errf("package.invalid", name, "%v", err)
	}
	n, err := io.Copy(out, io.LimitReader(rc, limit+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	switch {
	case n > limit:
		return n, Errf("package.zip-bomb", name, "unpacks to more than its declared size")
	case err != nil:
		return n, Errf("package.invalid", name, "%v", err)
	}
	return n, nil
}
