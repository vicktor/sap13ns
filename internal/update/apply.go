package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// Size limits for downloads and extraction. Variables so tests can lower them.
var (
	maxAssetSize  int64 = 200 << 20 // release tarball
	maxSumsSize   int64 = 64 << 10  // SHA256SUMS
	maxSigSize    int64 = 4 << 10   // SHA256SUMS.sig
	maxBinarySize int64 = 512 << 20 // the extracted sap13ns binary
	maxTarStream  int64 = 1 << 30   // everything decompressed from the tarball
)

const (
	sumsName     = "SHA256SUMS"
	sigName      = "SHA256SUMS.sig"
	binaryName   = "sap13ns"
	runTimeout   = 20 * time.Second
	applyTimeout = 30 * time.Minute
)

// AssetName is the release tarball for a platform:
// sap13ns_<version without "v">_<goos>_<goarch>.tar.gz, for example
// AssetName("v1.4.0", "linux", "amd64") == "sap13ns_1.4.0_linux_amd64.tar.gz".
func AssetName(version, goos, goarch string) string {
	return "sap13ns_" + strings.TrimPrefix(strings.TrimSpace(version), "v") + "_" + goos + "_" + goarch + ".tar.gz"
}

// ParsePublicKey decodes a base64 (standard encoding) Ed25519 public key.
// An empty key returns ErrNoPublicKey.
func ParsePublicKey(b64 string) (ed25519.PublicKey, error) {
	b64 = strings.TrimSpace(b64)
	if b64 == "" {
		return nil, ErrNoPublicKey
	}
	b, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("update: the update public key is not %d base64-encoded bytes", ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(b), nil
}

// VerifySignature checks that sig (the contents of SHA256SUMS.sig: a base64
// Ed25519 signature, surrounding whitespace allowed) signs data with the
// base64 public key pub. It returns ErrNoPublicKey, ErrBadSignature or nil.
func VerifySignature(pub string, data, sig []byte) error {
	key, err := ParsePublicKey(pub)
	if err != nil {
		return err
	}
	return verifySig(key, data, sig)
}

func verifySig(key ed25519.PublicKey, data, sig []byte) error {
	raw, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(sig)))
	if err != nil || len(raw) != ed25519.SignatureSize {
		return fmt.Errorf("%w (%s is not a base64 Ed25519 signature)", ErrBadSignature, sigName)
	}
	if !ed25519.Verify(key, data, raw) {
		return ErrBadSignature
	}
	return nil
}

// Apply downloads rel for the running platform and installs it over the
// binary at exePath (normally os.Executable()). Steps:
//
//  1. refuse if pub (normally PublicKey) is empty: no unsigned updates;
//  2. create <exe>.new next to the binary (this also proves write access
//     and keeps two updates from running at once);
//  3. download SHA256SUMS and SHA256SUMS.sig, verify the signature, then
//     download AssetName(rel.Version, GOOS, GOARCH) and check its SHA-256;
//  4. extract the single sap13ns regular file from the tarball into
//     <exe>.new with the current binary's mode (and owner, when root), fsync;
//  5. run "<exe>.new version" and require it to print rel.Version;
//  6. hard-link (or copy) the current binary to <exe>.old, then rename
//     <exe>.new over <exe> atomically.
//
// On any failure the current binary is untouched and <exe>.new is removed.
// The running process keeps executing the old code: restart the service
// afterwards. Apply does not compare versions; callers decide whether rel is
// worth installing (see IsNewer). Downloads and redirects go only to
// github.com, objects.githubusercontent.com and
// release-assets.githubusercontent.com over https. c may be nil
// (NewHTTPClient); a client with a short Timeout will cut large downloads,
// bound the operation with ctx instead (30 minutes if ctx has no deadline).
func Apply(ctx context.Context, c *http.Client, rel *Release, exePath, pub string, log *slog.Logger) (err error) {
	if log == nil {
		log = discardLogger()
	}
	key, err := ParsePublicKey(pub)
	if err != nil {
		return err
	}
	if rel == nil {
		return errors.New("update: no release to apply")
	}
	if runtime.GOOS == "windows" {
		return errors.New("update: self-update is not supported on Windows")
	}
	if _, err := parseVersion(rel.Version); err != nil {
		return err
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, applyTimeout)
		defer cancel()
	}

	name := AssetName(rel.Version, runtime.GOOS, runtime.GOARCH)
	asset, sums, sig := rel.asset(name), rel.asset(sumsName), rel.asset(sigName)
	switch {
	case asset == nil:
		return fmt.Errorf("%w: release %s has no %s for this platform (%s/%s)", ErrNoAsset, rel.Version, name, runtime.GOOS, runtime.GOARCH)
	case sums == nil || sig == nil:
		return fmt.Errorf("%w: release %s has no %s and %s, refusing an unsigned update", ErrNoAsset, rel.Version, sumsName, sigName)
	}
	for _, a := range []*Asset{asset, sums, sig} {
		if _, err := checkRawURL(a.URL, downloadHosts); err != nil {
			return fmt.Errorf("update: asset %s: %w", a.Name, err)
		}
	}
	if asset.Size > maxAssetSize {
		return fmt.Errorf("update: %s is %d bytes, more than the %d byte limit", name, asset.Size, maxAssetSize)
	}

	exe, fi, err := resolveExe(exePath)
	if err != nil {
		return err
	}
	dir := filepath.Dir(exe)
	newPath, oldPath := exe+".new", exe+".old"

	nf, err := os.OpenFile(newPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("update: %s already exists: another update may be running; if not, remove it and retry", newPath)
		}
		return fsError("create", newPath, err)
	}
	newOpen := true
	defer func() {
		if newOpen {
			nf.Close()
		}
		if err != nil {
			os.Remove(newPath)
		}
	}()

	hc := restrictClient(c, downloadHosts)
	ua := userAgent(rel.Version)

	log.Info("downloading checksums", "version", rel.Version)
	sumsData, err := fetch(ctx, hc, sums, maxSumsSize, ua)
	if err != nil {
		return err
	}
	sigData, err := fetch(ctx, hc, sig, maxSigSize, ua)
	if err != nil {
		return err
	}
	if err := verifySig(key, sumsData, sigData); err != nil {
		return err
	}
	want, err := lookupSum(sumsData, name)
	if err != nil {
		return err
	}
	log.Info("checksums signature verified")

	tmp, err := os.CreateTemp(dir, ".sap13ns-update-*.tar.gz")
	if err != nil {
		return fsError("create a temporary file in", dir, err)
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	log.Info("downloading release", "asset", name, "bytes", asset.Size)
	got, err := fetchTo(ctx, hc, asset, maxAssetSize, ua, tmp)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("%w: %s has SHA-256 %x, SHA256SUMS lists %x", ErrChecksumMismatch, name, got, want)
	}
	log.Info("archive checksum verified", "sha256", hex.EncodeToString(got[:]))

	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("update: %w", err)
	}
	if err := extractBinary(tmp, nf, maxBinarySize); err != nil {
		return err
	}
	if err := preserveOwner(nf, fi); err != nil {
		return fsError("set the owner of", newPath, err)
	}
	if err := nf.Chmod(fi.Mode() & (fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky)); err != nil {
		return fsError("chmod", newPath, err)
	}
	if err := nf.Sync(); err != nil {
		return fsError("sync", newPath, err)
	}
	newOpen = false
	if err := nf.Close(); err != nil {
		return fsError("close", newPath, err)
	}

	if err := checkRuns(ctx, newPath, rel.Version); err != nil {
		return err
	}
	log.Info("new binary runs", "version", rel.Version)

	if err := swap(exe, newPath, oldPath); err != nil {
		return err
	}
	log.Info("binary updated", "path", exe, "backup", oldPath, "version", rel.Version)
	return nil
}

// resolveExe makes exePath absolute, follows symlinks (the real file is
// replaced) and checks that it is a regular file.
func resolveExe(exePath string) (string, fs.FileInfo, error) {
	if exePath == "" {
		return "", nil, errors.New("update: no executable path")
	}
	exe, err := filepath.Abs(exePath)
	if err != nil {
		return "", nil, fmt.Errorf("update: %w", err)
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return "", nil, fmt.Errorf("update: locating the current binary: %w", err)
	}
	fi, err := os.Stat(exe)
	if err != nil {
		return "", nil, fsError("stat", exe, err)
	}
	if !fi.Mode().IsRegular() {
		return "", nil, fmt.Errorf("update: %s is not a regular file", exe)
	}
	return exe, fi, nil
}

// fsError wraps a file system error, turning permission problems into
// advice the operator can act on.
func fsError(op, p string, err error) error {
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("update: cannot %s %s: %w; run the update as the owner of the binary, for example `sudo sap13ns update`", op, p, err)
	}
	if errors.Is(err, syscall.EROFS) {
		return fmt.Errorf("update: cannot %s %s: %w; the binary is on a read-only file system", op, p, err)
	}
	return fmt.Errorf("update: cannot %s %s: %w", op, p, err)
}

// get starts a download of a and checks the status and declared length.
func get(ctx context.Context, c *http.Client, a *Asset, max int64, ua string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("update: %w", err)
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", ua)
	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("update: downloading %s: %w", a.Name, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("update: downloading %s: server returned %s", a.Name, resp.Status)
	}
	if resp.ContentLength > max {
		resp.Body.Close()
		return nil, fmt.Errorf("update: %s is %d bytes, more than the %d byte limit", a.Name, resp.ContentLength, max)
	}
	return resp, nil
}

// fetch downloads a small asset into memory.
func fetch(ctx context.Context, c *http.Client, a *Asset, max int64, ua string) ([]byte, error) {
	resp, err := get(ctx, c, a, max, ua)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := readLimited(resp.Body, max)
	if err != nil {
		return nil, fmt.Errorf("update: downloading %s: %w", a.Name, err)
	}
	return b, nil
}

// fetchTo downloads a into w and returns its SHA-256.
func fetchTo(ctx context.Context, c *http.Client, a *Asset, max int64, ua string, w io.Writer) ([32]byte, error) {
	var sum [32]byte
	resp, err := get(ctx, c, a, max, ua)
	if err != nil {
		return sum, err
	}
	defer resp.Body.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h), io.LimitReader(resp.Body, max+1))
	if err != nil {
		return sum, fmt.Errorf("update: downloading %s: %w", a.Name, err)
	}
	if n > max {
		return sum, fmt.Errorf("update: %s is more than the %d byte limit", a.Name, max)
	}
	copy(sum[:], h.Sum(nil))
	return sum, nil
}

// lookupSum finds name in sha256sum output ("<hex>  <name>" or
// "<hex> *<name>" per line).
func lookupSum(sums []byte, name string) ([32]byte, error) {
	var sum [32]byte
	found := false
	for i, line := range strings.Split(string(sums), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return sum, fmt.Errorf("update: %s line %d is malformed", sumsName, i+1)
		}
		if strings.TrimPrefix(fields[1], "*") != name {
			continue
		}
		b, err := hex.DecodeString(fields[0])
		if err != nil || len(b) != len(sum) {
			return sum, fmt.Errorf("update: %s line %d has a bad SHA-256", sumsName, i+1)
		}
		if found {
			return sum, fmt.Errorf("update: %s lists %s twice", sumsName, name)
		}
		copy(sum[:], b)
		found = true
	}
	if !found {
		return sum, fmt.Errorf("%w: %s has no entry for %s", ErrNoAsset, sumsName, name)
	}
	return sum, nil
}

// safeTarPath rejects absolute paths, ".." elements and backslashes.
func safeTarPath(name string) bool {
	if name == "" || strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\\\x00") {
		return false
	}
	for _, el := range strings.Split(name, "/") {
		if el == ".." {
			return false
		}
	}
	return true
}

// extractBinary copies the single regular file named sap13ns at the root of
// the gzipped tar r into w. Other regular files and directories are
// skipped; links, devices, unsafe paths, a missing or duplicated binary and
// sizes over max are errors.
func extractBinary(r io.Reader, w io.Writer, max int64) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("update: reading archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(io.LimitReader(gz, maxTarStream))
	found := false
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("update: reading archive: %w", err)
		}
		if !safeTarPath(h.Name) {
			return fmt.Errorf("update: archive entry %q has an unsafe path", h.Name)
		}
		switch h.Typeflag {
		case tar.TypeReg:
		case tar.TypeDir, tar.TypeXGlobalHeader:
			continue
		default:
			return fmt.Errorf("update: archive entry %q is not a regular file or directory (type %q)", h.Name, h.Typeflag)
		}
		if path.Clean(h.Name) != binaryName {
			continue // LICENSE, README.md, ...
		}
		if found {
			return fmt.Errorf("update: archive contains %s twice", binaryName)
		}
		if h.Size > max {
			return fmt.Errorf("update: %s in the archive is %d bytes, more than the %d byte limit", binaryName, h.Size, max)
		}
		n, err := io.Copy(w, io.LimitReader(tr, max+1))
		if err != nil {
			return fmt.Errorf("update: extracting %s: %w", binaryName, err)
		}
		if n > max {
			return fmt.Errorf("update: %s in the archive is more than the %d byte limit", binaryName, max)
		}
		if n == 0 {
			return fmt.Errorf("update: %s in the archive is empty", binaryName)
		}
		found = true
	}
	if !found {
		return fmt.Errorf("update: archive does not contain %s", binaryName)
	}
	return nil
}

// boundedBuffer keeps the first 16 KiB written to it.
type boundedBuffer struct{ bytes.Buffer }

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if room := 16<<10 - b.Len(); room > 0 {
		b.Buffer.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// checkRuns runs "<bin> version" and requires output of the form
// "sap13ns <version> ..." for the expected version.
func checkRuns(ctx context.Context, bin, want string) error {
	ctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	var out boundedBuffer
	var err error
	for attempt := 0; ; attempt++ {
		out.Reset()
		cmd := exec.CommandContext(ctx, bin, "version")
		cmd.Stdout, cmd.Stderr = &out, &out
		cmd.Env = []string{}
		cmd.WaitDelay = 2 * time.Second
		err = cmd.Run()
		// A file just closed for writing can briefly be "busy" if another
		// goroutine forked meanwhile; retry a few times.
		if errors.Is(err, syscall.ETXTBSY) && attempt < 5 && ctx.Err() == nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		break
	}
	text := strings.TrimSpace(out.String())
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("update: the new binary did not answer `version` within %s", runTimeout)
		}
		return fmt.Errorf("update: the new binary failed to run (%v); output: %q", err, text)
	}
	fields := strings.Fields(text)
	if len(fields) < 2 || fields[0] != binaryName || !sameVersion(fields[1], want) {
		return fmt.Errorf("update: the new binary reports %q, expected %s %s", firstLine(text), binaryName, want)
	}
	return nil
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// swap keeps the current binary as oldPath and moves newPath over exe.
func swap(exe, newPath, oldPath string) error {
	tmpOld := oldPath + ".tmp"
	os.Remove(tmpOld)
	if err := os.Link(exe, tmpOld); err != nil {
		if err := copyFile(exe, tmpOld); err != nil {
			os.Remove(tmpOld)
			return fsError("back up the current binary to", tmpOld, err)
		}
	}
	if err := os.Rename(tmpOld, oldPath); err != nil {
		os.Remove(tmpOld)
		return fsError("back up the current binary to", oldPath, err)
	}
	if err := os.Rename(newPath, exe); err != nil {
		return fsError("replace", exe, err)
	}
	syncDir(filepath.Dir(exe))
	return nil
}

// copyFile copies src to a new file dst with the same mode and fsyncs it.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := preserveOwner(out, fi); err != nil {
		out.Close()
		return err
	}
	if err := out.Chmod(fi.Mode() & (fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky)); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// syncDir makes the renames durable where the platform supports it.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}
