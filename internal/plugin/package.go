package plugin

import (
	"archive/zip"
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"sap13ns/internal/archive"
	"sap13ns/internal/store"
)

// A plugin package is a .zip with, at its root (or inside one top-level
// folder):
//
//	plugin.json        manifest (required)
//	plugin.wasm        the module named by "wasm" (required)
//	assets/...         optional static files (css, js, images, fonts)
//	README.md, LICENSE[.md|.txt], CHANGELOG.md
//
// Nothing else is accepted. Extraction refuses absolute paths, "..",
// symbolic links and special files, enforces entry, size and compression
// ratio caps (zip bombs), and writes into a staging directory that is
// renamed into place only after the scanner accepted the package.

// Package limits.
const (
	MaxPackageBytes   = 40 << 20 // the uploaded archive
	maxPackageEntries = 200
	maxUnpackedBytes  = 48 << 20
	maxDocBytes       = 256 << 10
	maxRatio          = 100 // uncompressed/compressed, for entries over 1 MiB
)

var packageLimits = archive.Limits{MaxBytes: MaxPackageBytes, MaxEntries: maxPackageEntries, MaxUnpacked: maxUnpackedBytes, MaxRatio: maxRatio}

var docFiles = []string{"README.md", "LICENSE", "LICENSE.md", "LICENSE.txt", "CHANGELOG.md"}

// PackageError is a reason to refuse an archive before scanning it.
type PackageError = archive.Error

func pkgErr(rule, entry, format string, args ...any) *PackageError {
	return archive.Errf(rule, entry, format, args...)
}

// ExtractPackage safely unpacks a plugin archive into dest, which must be an
// empty directory. Errors are *PackageError for refused archives.
func ExtractPackage(r io.ReaderAt, size int64, dest string) error {
	z, err := archive.Open(r, size, "plugin.json", packageLimits)
	if err != nil {
		return err
	}
	wasmName := "plugin.wasm"
	if raw, err := z.ReadFile("plugin.json", 64<<10); err != nil {
		return pkgErr("package.invalid", "plugin.json", "%v", err)
	} else {
		var m struct {
			Wasm string `json:"wasm"`
		}
		if json.Unmarshal(raw, &m) == nil && wasmFileRe.MatchString(m.Wasm) {
			wasmName = m.Wasm
		}
	}
	allow := func(name string) (int64, error) { return entryLimit(name, wasmName) }
	if err := z.Check(allow); err != nil {
		return err
	}
	if !z.Has(wasmName) {
		return pkgErr("package.no-wasm", wasmName, "the WebAssembly module named in plugin.json is missing")
	}
	_, err = z.Extract(dest, allow)
	return err
}

// entryLimit enforces the file allowlist and returns the size cap.
func entryLimit(name, wasmName string) (int64, error) {
	switch {
	case name == "plugin.json":
		return 64 << 10, nil
	case name == wasmName:
		return MaxWasmBytes, nil
	case slices.Contains(docFiles, name):
		return maxDocBytes, nil
	case strings.HasPrefix(name, "assets/"):
		if _, ok := assetTypes[strings.ToLower(path.Ext(name))]; !ok {
			return 0, pkgErr("package.disallowed-file", name, "file type not allowed in assets/ (allowed: %s)", strings.Join(sortedKeys(assetTypes), " "))
		}
		return maxAssetBytes, nil
	}
	return 0, pkgErr("package.disallowed-file", name, "not part of a plugin package (plugin.json, %s, assets/, README.md, LICENSE, CHANGELOG.md)", wasmName)
}

// --------------------------------------------------------------- install --

// InstallOptions describe who installs a package and how.
type InstallOptions struct {
	UserID         int64  // 0 for the command line
	Source         string // "upload" | "cli" | "catalog" (the network's, on a site of a network) | "official" (the bundle's)
	AllowDowngrade bool
	// ReviewRequired marks packages uploaded by tenants of a network: they
	// cannot be enabled until the operator reviews them.
	ReviewRequired bool
	// Reviewed marks a package the network operator vouches for (its
	// catalogue): it clears the review an earlier upload waited for.
	Reviewed bool
}

// InstallResult describes an installed package (or the refused one).
type InstallResult struct {
	ID       string
	Version  string
	Previous string // version replaced by an update; "" for a new plugin
	Report   *Report
	// Pin is the ReviewPin of the installed build: its module, assets and
	// pinned permissions (see ClearReview).
	Pin string
}

var (
	// ErrRefused means the scanner found critical problems.
	ErrRefused = errors.New("the package was refused")
	// ErrDowngrade means the package is older than the installed version.
	ErrDowngrade = errors.New("the package is older than the installed version")
	// ErrPluginEnabled is returned when uninstalling an enabled plugin.
	ErrPluginEnabled = errors.New("disable the plugin before uninstalling it")
)

// Install extracts, scans and installs a plugin package, always disabled.
// Updating an installed id replaces its files atomically and keeps its
// settings and stored data; if the module, assets or permissions changed,
// the plugin shows as modified and must be enabled again.
func (h *Host) Install(ctx context.Context, r io.ReaderAt, size int64, opts InstallOptions) (*InstallResult, error) {
	if h.inert {
		return nil, ErrInert // its scan starts the module
	}
	h.installMu.Lock()
	defer h.installMu.Unlock()
	sum := sha256.New()
	if _, err := io.Copy(sum, io.NewSectionReader(r, 0, size)); err != nil {
		return nil, err
	}
	pkgSHA := hex.EncodeToString(sum.Sum(nil))

	if err := os.MkdirAll(h.dir, 0o750); err != nil {
		return nil, err
	}
	staging, err := os.MkdirTemp(h.dir, ".staging-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(staging) // a no-op once renamed into place

	res := &InstallResult{}
	if err := ExtractPackage(r, size, staging); err != nil {
		if res.Report = packageReport(err, pkgSHA); res.Report == nil {
			return nil, err
		}
		return res, ErrRefused
	}

	// The id comes from the same parser the scanner uses (it is checked
	// against the report below).
	opt := ScanOptions{PackageSHA256: pkgSHA}
	var newM *Manifest
	var newID string
	if raw, err := readLimited(filepath.Join(staging, "plugin.json"), 64<<10); err == nil {
		if newM, _ = ParseManifest(raw); newM != nil {
			newID = newM.ID
		}
	}
	for _, p := range h.List() {
		if p.ID == newID {
			mm := p.Manifest
			opt.Replacing = &mm
			continue
		}
		opt.Installed = append(opt.Installed, p.Manifest)
	}
	// Data an uninstall kept goes to whatever package next claims the id.
	var retained map[string]string
	if opt.Replacing == nil && idRe.MatchString(newID) {
		recs, err := h.store.ListPluginRecords(ctx)
		if err != nil {
			return nil, err
		}
		if rec, ok := recs[newID]; ok {
			retained, opt.Retained = rec.Settings, true
		}
	}
	rep := h.ScanDir(ctx, staging, opt)
	res.Report, res.ID, res.Version = rep, rep.PluginID, rep.Version
	if rep.Blocked() || !idRe.MatchString(rep.PluginID) || rep.PluginID != newID {
		return res, ErrRefused
	}
	res.Pin = reviewPin(rep.WasmSHA256, rep.AssetsSHA256, newM.Pinned())

	target := filepath.Join(h.dir, rep.PluginID)
	if raw, err := readLimited(filepath.Join(target, "plugin.json"), 64<<10); err == nil {
		var old struct {
			Version string `json:"version"`
		}
		json.Unmarshal(raw, &old)
		res.Previous = old.Version
		if compareVersions(rep.Version, old.Version) < 0 && !opts.AllowDowngrade {
			return res, ErrDowngrade
		}
	}
	// The files count against the site's storage: what the package adds
	// over the version it replaces, claimed before anything changes.
	var grow int64
	if h.quota != nil {
		newBytes, err := treeBytes(staging)
		if err != nil {
			return nil, err
		}
		oldBytes, err := treeBytes(target)
		if err != nil {
			return nil, err
		}
		if grow = newBytes - oldBytes; grow > 0 {
			if err := h.quota.Reserve(ctx, grow); err != nil {
				return res, err
			}
		}
	}
	release := func(n int64) {
		if h.quota != nil && n > 0 {
			h.quota.Release(context.WithoutCancel(ctx), n)
		}
	}
	// Strip the kept data before the files go in, so a failure cannot leave
	// the new package installed with the old secrets and approval.
	if opt.Retained {
		if err := h.store.ResetRetainedPlugin(ctx, newID, inheritedSettings(retained, newM)); err != nil {
			release(grow)
			return nil, err
		}
	}
	if err := swapDir(staging, target); err != nil {
		release(grow)
		return nil, err
	}
	release(-grow) // a smaller version gives bytes back
	source := opts.Source
	if !slices.Contains([]string{"cli", "catalog", SourceOfficial}, source) {
		source = "upload"
	}
	ctx = context.WithoutCancel(ctx) // the files are in place: record them
	if err := h.store.RecordPluginInstall(ctx, store.PluginInstall{ID: rep.PluginID, Version: rep.Version, Source: source,
		UploadedBy: opts.UserID, ReviewRequired: opts.ReviewRequired && !opts.Reviewed, Reviewed: opts.Reviewed, Report: rep.JSON()}); err != nil {
		return nil, err
	}
	return res, h.Load(ctx)
}

// inheritedSettings are the settings a reinstall keeps from an uninstalled
// plugin with the same id: only values for settings the new manifest
// declares as plain (not secret or e-mail) settings, and never a sealed
// secret. The new package may not be the one the rest was entrusted to.
func inheritedSettings(old map[string]string, m *Manifest) map[string]string {
	out := map[string]string{}
	for _, s := range m.Settings {
		v, ok := old[s.Key]
		if !ok || s.Type == "secret" || s.Type == "email" || old[emailMark+s.Key] != "" || strings.HasPrefix(v, secretPrefix) {
			continue
		}
		out[s.Key] = v
	}
	return out
}

// packageReport turns a *PackageError into a blocked report (nil for any
// other error).
func packageReport(err error, pkgSHA string) *Report {
	var pe *PackageError
	if !errors.As(err, &pe) {
		return nil
	}
	rep := &Report{Scanner: ScannerVersion, PackageSHA256: pkgSHA, Findings: []Finding{}}
	rep.add(SevCritical, pe.Rule, pe.Entry, "%s", pe.Msg)
	return rep.finish()
}

// ScanPackage checks an archive the way an upload is checked, without
// installing it ("sap13ns plugin check file.zip").
func ScanPackage(ctx context.Context, r io.ReaderAt, size int64, opts ScanOptions, blocklistFile string) (*Report, error) {
	sum := sha256.New()
	if _, err := io.Copy(sum, io.NewSectionReader(r, 0, size)); err != nil {
		return nil, err
	}
	opts.PackageSHA256 = hex.EncodeToString(sum.Sum(nil))
	dir, err := os.MkdirTemp("", "sap13ns-plugin-check-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	if err := ExtractPackage(r, size, dir); err != nil {
		if rep := packageReport(err, opts.PackageSHA256); rep != nil {
			return rep, nil
		}
		return nil, err
	}
	return Scan(ctx, dir, opts, blocklistFile)
}

// Pack writes the package of a plugin directory as a .zip: plugin.json, the
// module, assets/ and the documentation files, in a fixed order with fixed
// timestamps so that the same files give the same archive. Other files
// (sources, tests, Makefile) are left out.
func Pack(dir string, w io.Writer) (*Manifest, error) {
	raw, err := readLimited(filepath.Join(dir, "plugin.json"), 64<<10)
	if err != nil {
		return nil, err
	}
	m, err := ParseManifest(raw)
	if err != nil {
		return nil, err
	}
	names := []string{"plugin.json", m.Wasm}
	for _, d := range docFiles {
		if fi, err := os.Lstat(filepath.Join(dir, d)); err == nil && fi.Mode().IsRegular() {
			names = append(names, d)
		}
	}
	assetsDir := filepath.Join(dir, "assets")
	if fi, err := os.Lstat(assetsDir); err == nil && fi.IsDir() {
		err := filepath.WalkDir(assetsDir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(dir, p)
			rel = filepath.ToSlash(rel)
			switch {
			case d.IsDir():
				return nil
			case !d.Type().IsRegular():
				return fmt.Errorf("%s: only regular files can be packed", rel)
			}
			names = append(names, rel)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	slices.Sort(names[2:])
	zw := zip.NewWriter(w)
	var total int64
	for _, name := range names {
		if err := archive.CheckName(name); err != nil {
			return nil, err
		}
		limit, err := entryLimit(name, m.Wasm)
		if err != nil {
			return nil, err
		}
		data, err := readLimited(filepath.Join(dir, filepath.FromSlash(name)), limit)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if total += int64(len(data)); total > maxUnpackedBytes {
			return nil, fmt.Errorf("the files add up to more than %d MiB", maxUnpackedBytes>>20)
		}
		f, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: packTime})
		if err != nil {
			return nil, err
		}
		if _, err := f.Write(data); err != nil {
			return nil, err
		}
	}
	return m, zw.Close()
}

// treeBytes adds up the sizes of the regular files under dir, without
// following symbolic links; a directory that does not exist is empty.
func treeBytes(dir string) (int64, error) {
	var n int64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.Type().IsRegular() {
			fi, err := d.Info()
			if err != nil {
				return err
			}
			n += fi.Size()
		}
		return nil
	})
	return n, err
}

// packTime is the timestamp of every packed file (reproducible archives).
var packTime = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

// swapDir moves staging to target, replacing an existing target with two
// renames on the same filesystem (and putting it back if the second fails).
func swapDir(staging, target string) error {
	if err := os.Chmod(staging, 0o750); err != nil {
		return err
	}
	if _, err := os.Lstat(target); errors.Is(err, os.ErrNotExist) {
		return os.Rename(staging, target)
	}
	old := filepath.Join(filepath.Dir(target), ".old-"+filepath.Base(target)+"-"+randomSuffix())
	if err := os.Rename(target, old); err != nil {
		return err
	}
	if err := os.Rename(staging, target); err != nil {
		os.Rename(old, target)
		return err
	}
	return os.RemoveAll(old)
}

func randomSuffix() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Uninstall removes a disabled plugin's files and its OAuth connections.
// With deleteData its settings and key-value data are deleted too;
// otherwise they are kept for a later reinstall, without the secrets,
// e-mail recipients and approval (Install).
func (h *Host) Uninstall(ctx context.Context, id string, deleteData bool) error {
	if !idRe.MatchString(id) {
		return errors.New("invalid plugin id")
	}
	h.installMu.Lock()
	defer h.installMu.Unlock()
	if p := h.Get(id); p != nil && (p.Status == StatusEnabled || p.Enabled()) {
		return ErrPluginEnabled
	}
	dir := filepath.Join(h.dir, id)
	if _, err := os.Lstat(dir); err == nil {
		size, _ := treeBytes(dir)
		trash := filepath.Join(h.dir, ".trash-"+id+"-"+randomSuffix())
		if err := os.Rename(dir, trash); err != nil {
			return err
		}
		if err := os.RemoveAll(trash); err != nil {
			return err
		}
		if h.quota != nil && size > 0 {
			h.quota.Release(context.WithoutCancel(ctx), size)
		}
	}
	// The tokens were entrusted to this package: they never outlive it.
	if err := h.store.DeletePluginOAuthAll(ctx, id); err != nil {
		return err
	}
	if deleteData {
		if err := h.store.DeletePlugin(ctx, id); err != nil {
			return err
		}
	}
	return h.Load(ctx)
}

// Rescan scans an installed plugin again and stores the report.
func (h *Host) Rescan(ctx context.Context, id string) (*Report, error) {
	if h.inert {
		return nil, ErrInert
	}
	p := h.Get(id)
	if p == nil {
		return nil, errors.New("unknown plugin")
	}
	var opt ScanOptions
	for _, o := range h.List() {
		if o.ID != id {
			opt.Installed = append(opt.Installed, o.Manifest)
		}
	}
	rep := h.ScanDir(ctx, p.Dir, opt)
	if rep.PluginID != id {
		return rep, errors.New("the manifest id does not match the plugin directory")
	}
	if err := h.store.SavePluginReport(ctx, id, rep.Version, rep.JSON()); err != nil {
		return nil, err
	}
	return rep, h.Load(ctx)
}

// compareVersions compares semantic versions (1.2.3, 1.2.3-beta.1); a
// pre-release sorts before its release. Unparsable versions compare as
// strings.
func compareVersions(a, b string) int {
	pa, okA := parseVersion(a)
	pb, okB := parseVersion(b)
	if !okA || !okB {
		return strings.Compare(a, b)
	}
	for i := range 3 {
		if pa.n[i] != pb.n[i] {
			if pa.n[i] < pb.n[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case pa.pre == pb.pre:
		return 0
	case pa.pre == "":
		return 1
	case pb.pre == "":
		return -1
	}
	return comparePrerelease(pa.pre, pb.pre)
}

// comparePrerelease orders pre-release tags as semantic versioning does:
// dot-separated identifiers, numeric ones compared as numbers and before
// alphanumeric ones, and a shorter list first when all else is equal
// (beta.2 < beta.10 < beta.10.1 < rc.1).
func comparePrerelease(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		na, errA := strconv.Atoi(as[i])
		nb, errB := strconv.Atoi(bs[i])
		switch {
		case errA == nil && errB == nil:
			if na != nb {
				return cmp.Compare(na, nb)
			}
		case errA == nil:
			return -1
		case errB == nil:
			return 1
		default:
			if c := strings.Compare(as[i], bs[i]); c != 0 {
				return c
			}
		}
	}
	return cmp.Compare(len(as), len(bs))
}

type version struct {
	n   [3]int
	pre string
}

func parseVersion(s string) (version, bool) {
	var v version
	s, _, _ = strings.Cut(s, "+") // build metadata is ignored
	core, pre, _ := strings.Cut(s, "-")
	v.pre = pre
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, false
		}
		v.n[i] = n
	}
	return v, true
}
