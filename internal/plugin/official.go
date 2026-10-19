package plugin

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"slices"

	"sap13ns/internal/archive"
)

// The official plugins are the ones the sap13ns team maintains (plugins/
// in the repository). `make plugins` packs each of them, as `sap13ns
// plugin pack` does, into internal/plugin/bundled, which the binary
// embeds, so they travel with the binary and with `sap13ns update`.
//
// Shipping inside the binary makes them easy to find, not trusted:
// installing one goes through Install like an upload (the archive limits,
// the scanner, installed disabled), with source "official", and enabling
// it is the same approval pinned to its build.

// SourceOfficial is the install source of an official plugin.
const SourceOfficial = "official"

// An OfficialPlugin is the package of an official plugin in the bundle.
type OfficialPlugin struct {
	*Manifest        // read from the package; the scan of an install reads it again
	SHA256    string // of the package
	Size      int64  // of the package, in bytes

	fsys fs.FS
	name string
}

// Package returns the bytes of the package, to install it.
func (o *OfficialPlugin) Package() ([]byte, error) {
	b, err := fs.ReadFile(o.fsys, o.name)
	if err != nil {
		return nil, err
	}
	if sum := sha256.Sum256(b); hex.EncodeToString(sum[:]) != o.SHA256 {
		return nil, fmt.Errorf("official plugin %s: the package changed since it was read", o.ID)
	}
	return b, nil
}

// NewerThan reports whether the bundled version is newer than version,
// the version of an installed copy.
func (o *OfficialPlugin) NewerThan(version string) bool {
	return compareVersions(o.Version, version) > 0
}

// Official is the bundle of official plugins. A nil *Official is an empty
// bundle.
type Official struct {
	list []*OfficialPlugin // by id
}

// LoadOfficial reads the bundle: the .zip packages at the root of fsys,
// each opened under the limits of an upload. A package whose manifest
// cannot be read is left out and named in the error, which comes with the
// packages that could be read. When two packages have the same id, the
// newer version is kept.
func LoadOfficial(fsys fs.FS) (*Official, error) {
	o := &Official{}
	if fsys == nil {
		return o, nil
	}
	names, err := fs.Glob(fsys, "*.zip")
	if err != nil {
		return o, err
	}
	byID := map[string]*OfficialPlugin{}
	var errs []error
	for _, name := range names {
		p, err := readOfficial(fsys, name)
		if err != nil {
			errs = append(errs, fmt.Errorf("official plugin %s: %w", name, err))
			continue
		}
		if have := byID[p.ID]; have != nil && !p.NewerThan(have.Version) {
			continue
		}
		byID[p.ID] = p
	}
	for _, p := range byID {
		o.list = append(o.list, p)
	}
	slices.SortFunc(o.list, func(a, b *OfficialPlugin) int { return cmp.Compare(a.ID, b.ID) })
	return o, errors.Join(errs...)
}

func readOfficial(fsys fs.FS, name string) (*OfficialPlugin, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > MaxPackageBytes {
		return nil, fmt.Errorf("not a package of at most %d MiB", MaxPackageBytes>>20)
	}
	// embed.FS and fstest.MapFS files read at an offset; anything else is
	// read into memory.
	ra, ok := f.(io.ReaderAt)
	if !ok {
		b, err := io.ReadAll(io.LimitReader(f, MaxPackageBytes+1))
		if err != nil {
			return nil, err
		}
		ra = bytes.NewReader(b)
	}
	sum := sha256.New()
	if _, err := io.Copy(sum, io.NewSectionReader(ra, 0, fi.Size())); err != nil {
		return nil, err
	}
	z, err := archive.Open(ra, fi.Size(), "plugin.json", packageLimits)
	if err != nil {
		return nil, err
	}
	raw, err := z.ReadFile("plugin.json", 64<<10)
	if err != nil {
		return nil, err
	}
	m, err := ParseManifest(raw)
	if err != nil {
		return nil, err
	}
	if slices.Contains(ReservedIDs, m.ID) {
		return nil, fmt.Errorf("the id %s is reserved", m.ID)
	}
	if !z.Has(m.Wasm) {
		return nil, fmt.Errorf("the module %s is missing", m.Wasm)
	}
	return &OfficialPlugin{Manifest: m, SHA256: hex.EncodeToString(sum.Sum(nil)), Size: fi.Size(), fsys: fsys, name: path.Clean(name)}, nil
}

// List returns the official plugins, by id.
func (o *Official) List() []*OfficialPlugin {
	if o == nil {
		return nil
	}
	return o.list
}

// Get returns the official plugin id, or nil.
func (o *Official) Get(id string) *OfficialPlugin {
	for _, p := range o.List() {
		if p.ID == id {
			return p
		}
	}
	return nil
}

// Len is the number of official plugins in the bundle.
func (o *Official) Len() int { return len(o.List()) }

// ValidID reports whether id is a valid plugin id.
func ValidID(id string) bool { return idRe.MatchString(id) }
