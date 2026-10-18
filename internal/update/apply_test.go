package update

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

const oldBinary = "#!/bin/sh\necho 'sap13ns v1.0.0 (old)'\n"

// installed writes a fake current binary and returns its path.
func installed(t *testing.T, mode fs.FileMode) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("self-update is not supported on Windows")
	}
	exe := filepath.Join(t.TempDir(), "sap13ns")
	if err := os.WriteFile(exe, []byte(oldBinary), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(exe, mode); err != nil {
		t.Fatal(err)
	}
	return exe
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

// assertUntouched checks that a failed Apply left only the original binary.
func assertUntouched(t *testing.T, exe string) {
	t.Helper()
	b, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != oldBinary {
		t.Errorf("binary changed to %q", b)
	}
	if names := dirNames(t, filepath.Dir(exe)); !slices.Equal(names, []string{"sap13ns"}) {
		t.Errorf("directory holds %v, want only sap13ns", names)
	}
}

func TestApplyInstallsSignedRelease(t *testing.T) {
	f := newFakeGitHub(t)
	pub, priv := newKey(t)
	rel := publish(t, f, "v1.2.0", priv, fakeBinary("v1.2.0"), runtime.GOOS, runtime.GOARCH)
	exe := installed(t, 0o751)

	if err := Apply(context.Background(), f.client(), rel, exe, pub, nil); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(exe)
	if err != nil || string(b) != fakeBinary("v1.2.0") {
		t.Fatalf("binary = %q, %v", b, err)
	}
	if fi, _ := os.Stat(exe); fi.Mode().Perm() != 0o751 {
		t.Errorf("mode %v, want 0751", fi.Mode().Perm())
	}
	old, err := os.ReadFile(exe + ".old")
	if err != nil || string(old) != oldBinary {
		t.Errorf("backup = %q, %v", old, err)
	}
	if names := dirNames(t, filepath.Dir(exe)); !slices.Equal(names, []string{"sap13ns", "sap13ns.old"}) {
		t.Errorf("directory holds %v", names)
	}
	out, err := exec.Command(exe, "version").Output()
	if err != nil || !strings.HasPrefix(string(out), "sap13ns v1.2.0 ") {
		t.Errorf("installed binary says %q, %v", out, err)
	}

	// A second update replaces the backup with the binary it replaces.
	rel = publish(t, f, "v1.3.0", priv, fakeBinary("1.3.0"), runtime.GOOS, runtime.GOARCH)
	if err := Apply(context.Background(), f.client(), rel, exe, pub, nil); err != nil {
		t.Fatal(err)
	}
	if old, _ := os.ReadFile(exe + ".old"); string(old) != fakeBinary("v1.2.0") {
		t.Errorf("backup after second update = %q", old)
	}
}

func TestApplyFollowsSymlink(t *testing.T) {
	f := newFakeGitHub(t)
	pub, priv := newKey(t)
	rel := publish(t, f, "v1.2.0", priv, fakeBinary("v1.2.0"), runtime.GOOS, runtime.GOARCH)
	exe := installed(t, 0o755)
	link := filepath.Join(t.TempDir(), "sap13ns")
	if err := os.Symlink(exe, link); err != nil {
		t.Skip(err)
	}
	if err := Apply(context.Background(), f.client(), rel, link, pub, nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != fakeBinary("v1.2.0") {
		t.Errorf("symlink target not updated: %q", b)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&fs.ModeSymlink == 0 {
		t.Errorf("symlink replaced: %v", err)
	}
}

func TestApplyRefusesWithoutPublicKey(t *testing.T) {
	f := newFakeGitHub(t)
	_, priv := newKey(t)
	rel := publish(t, f, "v1.2.0", priv, fakeBinary("v1.2.0"), runtime.GOOS, runtime.GOARCH)
	exe := installed(t, 0o755)
	for _, pub := range []string{"", "  "} {
		if err := Apply(context.Background(), f.client(), rel, exe, pub, nil); !errors.Is(err, ErrNoPublicKey) {
			t.Errorf("pub %q: err = %v, want ErrNoPublicKey", pub, err)
		}
	}
	if err := Apply(context.Background(), f.client(), rel, exe, "c2hvcnQ=", nil); err == nil {
		t.Error("short public key accepted")
	}
	if n := f.totalHits(); n != 0 {
		t.Errorf("%d requests made without a key", n)
	}
	assertUntouched(t, exe)
}

func TestApplyRejectsBadSignature(t *testing.T) {
	f := newFakeGitHub(t)
	pub, _ := newKey(t)
	_, otherPriv := newKey(t)
	rel := publish(t, f, "v1.2.0", otherPriv, fakeBinary("v1.2.0"), runtime.GOOS, runtime.GOARCH)
	exe := installed(t, 0o755)
	err := Apply(context.Background(), f.client(), rel, exe, pub, nil)
	if !errors.Is(err, ErrBadSignature) {
		t.Fatalf("err = %v, want ErrBadSignature", err)
	}
	name := AssetName("v1.2.0", runtime.GOOS, runtime.GOARCH)
	if n := f.hitCount("release-assets.githubusercontent.com/assets/" + name); n != 0 {
		t.Errorf("archive downloaded %d times before the signature was checked", n)
	}
	assertUntouched(t, exe)

	// Garbage instead of a signature.
	f.setFile(sigName, []byte("not base64!"))
	if err := Apply(context.Background(), f.client(), rel, exe, pub, nil); !errors.Is(err, ErrBadSignature) {
		t.Errorf("err = %v, want ErrBadSignature", err)
	}
	assertUntouched(t, exe)
}

func TestApplyRejectsChecksumMismatch(t *testing.T) {
	f := newFakeGitHub(t)
	pub, priv := newKey(t)
	rel := publish(t, f, "v1.2.0", priv, fakeBinary("v1.2.0"), runtime.GOOS, runtime.GOARCH)
	name := AssetName("v1.2.0", runtime.GOOS, runtime.GOARCH)
	f.setFile(name, makeTarGz(t, tarEntry{name: "sap13ns", body: fakeBinary("v1.2.0") + "# evil\n"}))
	exe := installed(t, 0o755)
	if err := Apply(context.Background(), f.client(), rel, exe, pub, nil); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("err = %v, want ErrChecksumMismatch", err)
	}
	assertUntouched(t, exe)
}

func TestApplyRejectsReplayedChecksums(t *testing.T) {
	// A validly signed SHA256SUMS from an older release does not list the
	// new archive name, so it cannot vouch for it.
	f := newFakeGitHub(t)
	pub, priv := newKey(t)
	publish(t, f, "v1.1.0", priv, fakeBinary("v1.1.0"), runtime.GOOS, runtime.GOARCH)
	f.mu.Lock()
	oldSums, oldSig := f.files[sumsName], f.files[sigName]
	f.mu.Unlock()
	rel := publish(t, f, "v1.2.0", priv, fakeBinary("v1.2.0"), runtime.GOOS, runtime.GOARCH)
	f.setFile(sumsName, oldSums)
	f.setFile(sigName, oldSig)
	exe := installed(t, 0o755)
	if err := Apply(context.Background(), f.client(), rel, exe, pub, nil); !errors.Is(err, ErrNoAsset) {
		t.Fatalf("err = %v, want ErrNoAsset (no SHA256SUMS entry)", err)
	}
	assertUntouched(t, exe)
}

func TestApplyNeedsPlatformAssetAndSums(t *testing.T) {
	f := newFakeGitHub(t)
	pub, priv := newKey(t)
	rel := publish(t, f, "v1.2.0", priv, fakeBinary("v1.2.0"), "plan9", "mips")
	exe := installed(t, 0o755)
	err := Apply(context.Background(), f.client(), rel, exe, pub, nil)
	if !errors.Is(err, ErrNoAsset) || !strings.Contains(err.Error(), AssetName("v1.2.0", runtime.GOOS, runtime.GOARCH)) {
		t.Errorf("err = %v, want ErrNoAsset naming the archive", err)
	}

	rel = publish(t, f, "v1.2.0", priv, fakeBinary("v1.2.0"), runtime.GOOS, runtime.GOARCH)
	rel.Assets = slices.DeleteFunc(rel.Assets, func(a Asset) bool { return a.Name == sigName })
	if err := Apply(context.Background(), f.client(), rel, exe, pub, nil); !errors.Is(err, ErrNoAsset) {
		t.Errorf("err = %v, want ErrNoAsset for a missing signature", err)
	}
	if n := f.totalHits(); n != 0 {
		t.Errorf("%d requests made", n)
	}
	assertUntouched(t, exe)
}

func TestApplyAllowedHostsOnly(t *testing.T) {
	f := newFakeGitHub(t)
	pub, priv := newKey(t)
	exe := installed(t, 0o755)

	for _, bad := range []string{
		"http://github.com/x/SHA256SUMS",
		"https://evil.example.com/x/SHA256SUMS",
		"https://api.github.com/x/SHA256SUMS",
		"https://github.com:444/x/SHA256SUMS",
		"https://user:pw@github.com/x/SHA256SUMS",
	} {
		rel := publish(t, f, "v1.2.0", priv, fakeBinary("v1.2.0"), runtime.GOOS, runtime.GOARCH)
		rel.Assets[1].URL = bad
		if err := Apply(context.Background(), f.client(), rel, exe, pub, nil); err == nil {
			t.Errorf("asset URL %s accepted", bad)
		}
	}
	if n := f.totalHits(); n != 0 {
		t.Errorf("%d requests made", n)
	}

	// Redirects off the allowed hosts are not followed either.
	rel := publish(t, f, "v1.2.0", priv, fakeBinary("v1.2.0"), runtime.GOOS, runtime.GOARCH)
	for _, bad := range []string{"https://evil.example.com/SHA256SUMS", "http://objects.githubusercontent.com/SHA256SUMS"} {
		f.setOverride(func(w http.ResponseWriter, r *http.Request) bool {
			if r.Host == "github.com" {
				http.Redirect(w, r, bad, http.StatusFound)
				return true
			}
			return false
		})
		err := Apply(context.Background(), f.client(), rel, exe, pub, nil)
		if err == nil {
			t.Errorf("redirect to %s followed", bad)
		}
	}
	if n := f.hitCount("evil.example.com/SHA256SUMS"); n != 0 {
		t.Errorf("disallowed host contacted %d times", n)
	}
	assertUntouched(t, exe)

	// objects.githubusercontent.com is a valid redirect target.
	f.setOverride(func(w http.ResponseWriter, r *http.Request) bool {
		if r.Host == "github.com" {
			http.Redirect(w, r, "https://objects.githubusercontent.com/o/"+filepath.Base(r.URL.Path), http.StatusFound)
			return true
		}
		return false
	})
	if err := Apply(context.Background(), f.client(), rel, exe, pub, nil); err != nil {
		t.Errorf("objects.githubusercontent.com redirect: %v", err)
	}
}

func TestApplyChecksNewBinaryVersion(t *testing.T) {
	f := newFakeGitHub(t)
	pub, priv := newKey(t)
	exe := installed(t, 0o755)
	for _, bin := range []string{
		fakeBinary("v9.9.9"),
		"#!/bin/sh\necho 'something v1.2.0'\n",
		"#!/bin/sh\nexit 3\n",
		"\x7fELF not really a program",
	} {
		rel := publish(t, f, "v1.2.0", priv, bin, runtime.GOOS, runtime.GOARCH)
		err := Apply(context.Background(), f.client(), rel, exe, pub, nil)
		if err == nil || !strings.Contains(err.Error(), "new binary") {
			t.Errorf("binary %q: err = %v", bin, err)
		}
		assertUntouched(t, exe)
	}
}

func TestApplyPermissionDenied(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	f := newFakeGitHub(t)
	pub, priv := newKey(t)
	rel := publish(t, f, "v1.2.0", priv, fakeBinary("v1.2.0"), runtime.GOOS, runtime.GOARCH)
	exe := installed(t, 0o755)
	dir := filepath.Dir(exe)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	err := Apply(context.Background(), f.client(), rel, exe, pub, nil)
	if !errors.Is(err, fs.ErrPermission) || !strings.Contains(err.Error(), "sudo sap13ns update") {
		t.Errorf("err = %v, want permission denied with sudo advice", err)
	}
	if n := f.totalHits(); n != 0 {
		t.Errorf("%d requests made before checking write access", n)
	}
}

func TestApplyRefusesConcurrentUpdate(t *testing.T) {
	f := newFakeGitHub(t)
	pub, priv := newKey(t)
	rel := publish(t, f, "v1.2.0", priv, fakeBinary("v1.2.0"), runtime.GOOS, runtime.GOARCH)
	exe := installed(t, 0o755)
	if err := os.WriteFile(exe+".new", []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := Apply(context.Background(), f.client(), rel, exe, pub, nil)
	if !errContains(err, "another update") {
		t.Errorf("err = %v", err)
	}
	if b, _ := os.ReadFile(exe + ".new"); string(b) != "partial" {
		t.Error("existing .new was touched")
	}
}

func TestApplySizeLimits(t *testing.T) {
	f := newFakeGitHub(t)
	pub, priv := newKey(t)
	exe := installed(t, 0o755)

	rel := publish(t, f, "v1.2.0", priv, fakeBinary("v1.2.0"), runtime.GOOS, runtime.GOARCH)
	rel.Assets[0].Size = maxAssetSize + 1
	if err := Apply(context.Background(), f.client(), rel, exe, pub, nil); !errContains(err, "limit") {
		t.Errorf("declared size: err = %v", err)
	}

	defer func(v int64) { maxAssetSize = v }(maxAssetSize)
	maxAssetSize = 64
	rel = publish(t, f, "v1.2.0", priv, fakeBinary("v1.2.0"), runtime.GOOS, runtime.GOARCH)
	rel.Assets[0].Size = 1 // GitHub's metadata lies; the body is still capped
	if err := Apply(context.Background(), f.client(), rel, exe, pub, nil); !errContains(err, "limit") {
		t.Errorf("actual size: err = %v", err)
	}
	assertUntouched(t, exe)
}

func TestApplyCancelled(t *testing.T) {
	f := newFakeGitHub(t)
	pub, priv := newKey(t)
	rel := publish(t, f, "v1.2.0", priv, fakeBinary("v1.2.0"), runtime.GOOS, runtime.GOARCH)
	exe := installed(t, 0o755)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Apply(ctx, f.client(), rel, exe, pub, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	assertUntouched(t, exe)
}

func TestExtractBinary(t *testing.T) {
	big := strings.Repeat("x", 101)
	tests := []struct {
		name    string
		entries []tarEntry
		want    string // binary contents, or "" for an error
		errSub  string
	}{
		{"plain", []tarEntry{{name: "sap13ns", body: "BIN"}}, "BIN", ""},
		{"with extras", []tarEntry{{name: "./", typ: tar.TypeDir}, {name: "./LICENSE", body: "L"}, {name: "./sap13ns", body: "BIN"}, {name: "README.md", body: "R"}}, "BIN", ""},
		{"missing", []tarEntry{{name: "LICENSE", body: "L"}}, "", "does not contain"},
		{"nested only", []tarEntry{{name: "dir/", typ: tar.TypeDir}, {name: "dir/sap13ns", body: "BIN"}}, "", "does not contain"},
		{"symlink", []tarEntry{{name: "sap13ns", typ: tar.TypeSymlink, linkname: "/bin/sh"}}, "", "not a regular file"},
		{"hard link", []tarEntry{{name: "x", body: "BIN"}, {name: "sap13ns", typ: tar.TypeLink, linkname: "x"}}, "", "not a regular file"},
		{"other link", []tarEntry{{name: "sap13ns", body: "BIN"}, {name: "evil", typ: tar.TypeSymlink, linkname: "/etc/passwd"}}, "", "not a regular file"},
		{"device", []tarEntry{{name: "sap13ns", body: "BIN"}, {name: "null", typ: tar.TypeChar}}, "", "not a regular file"},
		{"dotdot", []tarEntry{{name: "../sap13ns", body: "BIN"}}, "", "unsafe path"},
		{"dotdot extra", []tarEntry{{name: "sap13ns", body: "BIN"}, {name: "a/../../x", body: "X"}}, "", "unsafe path"},
		{"absolute", []tarEntry{{name: "/sap13ns", body: "BIN"}}, "", "unsafe path"},
		{"backslash", []tarEntry{{name: "..\\sap13ns", body: "BIN"}}, "", "unsafe path"},
		{"duplicate", []tarEntry{{name: "sap13ns", body: "A"}, {name: "./sap13ns", body: "B"}}, "", "twice"},
		{"too big", []tarEntry{{name: "sap13ns", body: big}}, "", "limit"},
		{"empty", []tarEntry{{name: "sap13ns", body: ""}}, "", "empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			err := extractBinary(bytes.NewReader(makeTarGz(t, tt.entries...)), &out, 100)
			if tt.want != "" {
				if err != nil || out.String() != tt.want {
					t.Errorf("got %q, %v; want %q", out.String(), err, tt.want)
				}
				return
			}
			if !errContains(err, tt.errSub) {
				t.Errorf("err = %v, want it to mention %q", err, tt.errSub)
			}
		})
	}
	if err := extractBinary(strings.NewReader("not gzip"), &bytes.Buffer{}, 100); err == nil {
		t.Error("non-gzip accepted")
	}
}

func TestLookupSum(t *testing.T) {
	a, b := sha256.Sum256([]byte("a")), sha256.Sum256([]byte("b"))
	sums := fmt.Sprintf("%x  one.tar.gz\n%x *two.tar.gz\n\n", a, b)
	if got, err := lookupSum([]byte(sums), "one.tar.gz"); err != nil || got != a {
		t.Errorf("one: %x, %v", got, err)
	}
	if got, err := lookupSum([]byte(sums), "two.tar.gz"); err != nil || got != b {
		t.Errorf("two (binary marker): %x, %v", got, err)
	}
	if _, err := lookupSum([]byte(sums), "three.tar.gz"); !errors.Is(err, ErrNoAsset) {
		t.Errorf("missing: %v", err)
	}
	if _, err := lookupSum([]byte(sums+fmt.Sprintf("%x  one.tar.gz\n", b)), "one.tar.gz"); !errContains(err, "twice") {
		t.Errorf("duplicate: %v", err)
	}
	if _, err := lookupSum([]byte("abc one.tar.gz\n"), "one.tar.gz"); err == nil {
		t.Error("short hash accepted")
	}
	if _, err := lookupSum([]byte("just garbage here\n"), "one.tar.gz"); err == nil {
		t.Error("malformed line accepted")
	}
}

func TestVerifySignature(t *testing.T) {
	pub, priv := newKey(t)
	data := []byte("abc  sap13ns_1.0.0_linux_amd64.tar.gz\n")
	sig := sign(priv, data)
	if err := VerifySignature(pub, data, sig); err != nil {
		t.Errorf("valid signature: %v", err)
	}
	if err := VerifySignature(" "+pub+"\n", data, append([]byte("  "), sig...)); err != nil {
		t.Errorf("whitespace: %v", err)
	}
	if err := VerifySignature(pub, append(data, ' '), sig); !errors.Is(err, ErrBadSignature) {
		t.Errorf("modified data: %v", err)
	}
	other, _ := newKey(t)
	if err := VerifySignature(other, data, sig); !errors.Is(err, ErrBadSignature) {
		t.Errorf("other key: %v", err)
	}
	if err := VerifySignature("", data, sig); !errors.Is(err, ErrNoPublicKey) {
		t.Errorf("no key: %v", err)
	}
	if err := VerifySignature("!!", data, sig); err == nil || errors.Is(err, ErrNoPublicKey) {
		t.Errorf("bad key: %v", err)
	}
}

func TestAssetName(t *testing.T) {
	if got := AssetName("v1.4.0", "linux", "amd64"); got != "sap13ns_1.4.0_linux_amd64.tar.gz" {
		t.Error(got)
	}
	if got := AssetName("1.4.0-rc.1", "darwin", "arm64"); got != "sap13ns_1.4.0-rc.1_darwin_arm64.tar.gz" {
		t.Error(got)
	}
}
