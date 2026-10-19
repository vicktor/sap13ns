package plugin

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/sys"

	"golang.org/x/net/publicsuffix"
)

// The scanner inspects a plugin before it is installed: manifest, module
// structure, permissions against imports, embedded strings, assets, a list of
// known-bad hashes, and a dry run of every hook in the sandbox with storage,
// network and e-mail switched off. Critical findings refuse the package;
// high findings make enabling it require an explicit confirmation.

// Severities.
const (
	SevCritical = "critical"
	SevHigh     = "high"
	SevMedium   = "medium"
	SevInfo     = "info"
)

// Verdicts.
const (
	VerdictBlocked = "blocked" // at least one critical finding: never installed
	VerdictReview  = "review"  // high findings: enabling needs a confirmation
	VerdictClean   = "clean"
)

// ScannerVersion changes whenever rules change.
const ScannerVersion = 1

var sevRank = map[string]int{SevCritical: 0, SevHigh: 1, SevMedium: 2, SevInfo: 3}

// Finding is one scanner observation.
type Finding struct {
	Severity string `json:"severity"`
	Rule     string `json:"rule"`
	Message  string `json:"message"`
	Location string `json:"location,omitempty"`
}

// Report is the result of a scan, stored with the plugin and shown in the
// admin panel.
type Report struct {
	PluginID      string    `json:"plugin_id"`
	Version       string    `json:"version"`
	WasmSHA256    string    `json:"wasm_sha256"`
	AssetsSHA256  string    `json:"assets_sha256,omitempty"`
	PackageSHA256 string    `json:"package_sha256,omitempty"`
	Verdict       string    `json:"verdict"`
	Findings      []Finding `json:"findings"`
	Imports       []string  `json:"imports"` // sap13ns host functions the module imports
	ScannedAt     time.Time `json:"scanned_at"`
	Scanner       int       `json:"scanner"`
}

func (r *Report) add(sev, rule, loc, format string, args ...any) {
	if len(r.Findings) >= 200 {
		return
	}
	r.Findings = append(r.Findings, Finding{Severity: sev, Rule: rule, Message: fmt.Sprintf(format, args...), Location: loc})
}

func (r *Report) finish() *Report {
	sort.SliceStable(r.Findings, func(i, j int) bool {
		return sevRank[r.Findings[i].Severity] < sevRank[r.Findings[j].Severity]
	})
	switch {
	case r.Count(SevCritical) > 0:
		r.Verdict = VerdictBlocked
	case r.Count(SevHigh) > 0:
		r.Verdict = VerdictReview
	default:
		r.Verdict = VerdictClean
	}
	if r.Findings == nil {
		r.Findings = []Finding{}
	}
	if r.Imports == nil {
		r.Imports = []string{}
	}
	return r
}

// Count returns the number of findings with severity sev.
func (r *Report) Count(sev string) int {
	n := 0
	for _, f := range r.Findings {
		if f.Severity == sev {
			n++
		}
	}
	return n
}

// Blocked reports whether the package must not be installed.
func (r *Report) Blocked() bool { return r.Verdict == VerdictBlocked }

// NeedsConfirmation reports high findings.
func (r *Report) NeedsConfirmation() bool { return r.Count(SevHigh) > 0 || r.Blocked() }

// Has reports whether any finding has the rule id.
func (r *Report) Has(rule string) bool {
	return slices.ContainsFunc(r.Findings, func(f Finding) bool { return f.Rule == rule })
}

// JSON encodes the report for storage.
func (r *Report) JSON() []byte {
	b, _ := json.Marshal(r)
	return b
}

// ScanOptions give the scanner context about the site.
type ScanOptions struct {
	// Installed are the manifests of the other installed plugins (id and
	// shortcode collisions).
	Installed []Manifest
	// Replacing is the installed manifest an update would replace.
	Replacing *Manifest
	// Retained is true when a plugin with the same id was uninstalled
	// without deleting its data, which this package would inherit.
	Retained bool
	// PackageSHA256 is the hash of the uploaded archive, checked against
	// the blocklist.
	PackageSHA256 string
	// SkipDryRun disables running the hooks (structure and string rules
	// still apply).
	SkipDryRun bool
}

// Scan checks an extracted plugin directory without a running site, with a
// private sandbox runtime (sap13ns plugin check / pack). blocklistFile may be
// empty.
func Scan(ctx context.Context, dir string, opts ScanOptions, blocklistFile string) (*Report, error) {
	h, err := NewHost(ctx, "", "", nil, slog.New(slog.NewTextHandler(io.Discard, nil)),
		func() SiteInfo { return SiteInfo{URL: "https://example.test", Title: "Example", Language: "en"} }, nil)
	if err != nil {
		return nil, err
	}
	defer h.Close(context.WithoutCancel(ctx))
	h.blocklist = blocklistFile
	return h.ScanDir(ctx, dir, opts), nil
}

// ScanDir checks a plugin directory with the host's runtime and limits.
func (h *Host) ScanDir(ctx context.Context, dir string, opts ScanOptions) *Report {
	rep := &Report{ScannedAt: time.Now().UTC().Truncate(time.Second), Scanner: ScannerVersion, PackageSHA256: opts.PackageSHA256}
	raw, err := readLimited(filepath.Join(dir, "plugin.json"), 64<<10)
	if err != nil {
		rep.add(SevCritical, "manifest.missing", "plugin.json", "plugin.json cannot be read: %v", err)
		return rep.finish()
	}
	m, err := ParseManifest(raw)
	if m == nil {
		rep.add(SevCritical, "manifest.invalid", "plugin.json", "%v", err)
		return rep.finish()
	}
	rep.PluginID, rep.Version = m.ID, m.Version
	manifestOK := err == nil
	if err != nil {
		for _, line := range strings.Split(err.Error(), "\n") {
			if strings.HasPrefix(line, "abi must be") {
				rep.add(SevCritical, "manifest.abi", "plugin.json", "built for ABI %d; this host implements ABI %d", m.ABI, ABIVersion)
				continue
			}
			rep.add(SevCritical, "manifest.invalid", "plugin.json", "%s", line)
		}
	}
	h.scanManifest(rep, m, opts)

	wasm, err := readLimited(filepath.Join(dir, m.Wasm), MaxWasmBytes)
	if !wasmFileRe.MatchString(m.Wasm) || err != nil {
		rep.add(SevCritical, "wasm.missing", m.Wasm, "the WebAssembly module cannot be read: %v", err)
		return rep.finish()
	}
	sum := sha256.Sum256(wasm)
	rep.WasmSHA256 = hex.EncodeToString(sum[:])

	assets, assetsSHA, err := readAssets(dir)
	if err != nil {
		rep.add(SevCritical, "assets.invalid", "assets/", "%v", err)
	}
	rep.AssetsSHA256 = assetsSHA
	for _, a := range append(slices.Clone(m.Assets.CSS), m.Assets.JS...) {
		if _, ok := assets[a]; !ok {
			rep.add(SevHigh, "assets.missing", a, "declared in plugin.json but not in the package")
		}
	}
	for _, doc := range []string{"README.md", "LICENSE"} {
		if _, err := os.Stat(filepath.Join(dir, doc)); err != nil && (doc != "LICENSE" || !hasLicense(dir)) {
			rep.add(SevInfo, "package.no-"+strings.ToLower(strings.TrimSuffix(doc, ".md")), doc, "the package has no %s", doc)
		}
	}
	h.scanBlocklist(rep, assets)

	// Before compiling: the dry run would allocate the tables. It runs the
	// module the way the host does, with every table pinned to a maximum.
	runnable := wasm
	if parts, err := parseWasm(wasm); err == nil {
		if err := parts.checkTables(); err != nil {
			rep.add(SevCritical, "wasm.tables", m.Wasm, "the module %v", err)
			return rep.finish()
		}
		runnable = parts.pinTables(wasm)
	}
	compiled, err := h.rt.CompileModule(ctx, runnable)
	if err != nil {
		rep.add(SevCritical, "wasm.compile", m.Wasm, "the module does not compile: %v", err)
		return rep.finish()
	}
	defer compiled.Close(context.WithoutCancel(ctx))
	used := h.scanModule(rep, m, compiled)
	for name := range used {
		rep.Imports = append(rep.Imports, name)
	}
	sort.Strings(rep.Imports)
	scanPermissions(rep, m, used)

	parts, err := parseWasm(wasm)
	if err != nil {
		rep.add(SevCritical, "wasm.format", m.Wasm, "%v", err)
	} else {
		ss := newStringScan(m, used["http_fetch"])
		ss.scan(rep, m.Wasm+": data section", parts.data, true)
		for _, cs := range parts.custom {
			ss.scan(rep, fmt.Sprintf("%s: custom section %q", m.Wasm, cs.name), cs.data, true)
		}
		for _, name := range sortedKeys(assets) {
			scanAsset(rep, ss, name, assets[name].data)
		}
		ss.report(rep)
	}

	if manifestOK && rep.Count(SevCritical) == 0 && !opts.SkipDryRun {
		h.dryRun(ctx, rep, m, compiled)
	}
	return rep.finish()
}

func hasLicense(dir string) bool {
	for _, n := range []string{"LICENSE", "LICENSE.md", "LICENSE.txt"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
			return true
		}
	}
	return false
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ------------------------------------------------------------ manifest --

func (h *Host) scanManifest(rep *Report, m *Manifest, opts ScanOptions) {
	if slices.Contains(ReservedIDs, m.ID) {
		rep.add(SevCritical, "manifest.reserved-id", "plugin.json", "the id %q is reserved; choose another one", m.ID)
	}
	if r := opts.Replacing; r != nil && r.Author != "" && !strings.EqualFold(strings.TrimSpace(r.Author), strings.TrimSpace(m.Author)) {
		rep.add(SevHigh, "manifest.id-collision", "plugin.json", "the installed plugin %q is by %q; this package claims the same id but is by %q", m.ID, r.Author, m.Author)
	}
	if opts.Retained && opts.Replacing == nil {
		rep.add(SevHigh, "manifest.retained-data", "plugin.json", "a plugin with the id %q was uninstalled without deleting its data: this package gets its settings and stored data (its secrets, e-mail recipients and approval are cleared)", m.ID)
	}
	for _, o := range opts.Installed {
		if o.ID == m.ID {
			continue
		}
		for _, sc := range m.Shortcodes {
			if slices.Contains(o.Shortcodes, sc) {
				rep.add(SevMedium, "manifest.shortcode-collision", "plugin.json", "the shortcode [%s] is also provided by %q; only one of them can be active", sc, o.ID)
			}
		}
	}
	for _, p := range m.EffectivePermissions() {
		switch {
		case p == "http:*":
			rep.add(SevHigh, "perm.http-wildcard", "plugin.json", `"http:*" lets the plugin send requests to any public host; ask for the hosts it needs instead`)
		case PermissionRisk(p) == "high":
			rep.add(SevMedium, "perm.high-risk", "plugin.json", "requests the high-risk permission %q (enabling it needs a confirmation)", p)
		}
	}
}

// -------------------------------------------------------------- module --

// scanModule checks imports and exports against what the host provides and
// returns the sap13ns functions the module imports.
func (h *Host) scanModule(rep *Report, m *Manifest, c wazero.CompiledModule) map[string]bool {
	provided := map[string]map[string]api.FunctionDefinition{}
	for _, mod := range []string{"sap13ns", "wasi_snapshot_preview1"} {
		if inst := h.rt.Module(mod); inst != nil {
			provided[mod] = inst.ExportedFunctionDefinitions()
		}
	}
	used := map[string]bool{}
	var sockets []string
	for _, f := range c.ImportedFunctions() {
		mod, name, _ := f.Import()
		def, ok := provided[mod][name]
		switch {
		case !ok:
			rep.add(SevCritical, "wasm.import", m.Wasm, "imports %s.%s, which the host does not provide (allowed: the sap13ns host functions and WASI preview 1)", mod, name)
			continue
		case !slices.Equal(def.ParamTypes(), f.ParamTypes()) || !slices.Equal(def.ResultTypes(), f.ResultTypes()):
			rep.add(SevCritical, "wasm.import-signature", m.Wasm, "imports %s.%s with a signature the host does not implement", mod, name)
		}
		if mod == "sap13ns" {
			used[name] = true
		} else if strings.HasPrefix(name, "sock_") {
			sockets = append(sockets, name)
		}
	}
	if len(sockets) > 0 {
		rep.add(SevInfo, "wasm.wasi-sockets", m.Wasm, "imports WASI socket functions (%s); they always fail in the sandbox", strings.Join(slices.Compact(sockets), ", "))
	}
	if len(c.ImportedMemories()) > 0 {
		rep.add(SevCritical, "wasm.import-memory", m.Wasm, "imports a memory; plugins must define and export their own")
	}
	exports := c.ExportedFunctions()
	want := map[string][2][]api.ValueType{
		"sap13ns_alloc":  {{api.ValueTypeI32}, {api.ValueTypeI32}},
		"sap13ns_handle": {{api.ValueTypeI32, api.ValueTypeI32}, {api.ValueTypeI64}},
	}
	for _, name := range []string{"sap13ns_alloc", "sap13ns_handle"} {
		f, ok := exports[name]
		switch {
		case !ok:
			rep.add(SevCritical, "wasm.export", m.Wasm, "missing export %s (build with -buildmode=c-shared and the pluginsdk)", name)
		case !slices.Equal(f.ParamTypes(), want[name][0]) || !slices.Equal(f.ResultTypes(), want[name][1]):
			rep.add(SevCritical, "wasm.export", m.Wasm, "export %s has the wrong signature", name)
		}
	}
	if _, ok := exports["sap13ns_abi"]; !ok {
		rep.add(SevMedium, "wasm.no-abi-export", m.Wasm, "no sap13ns_abi export; rebuild with a current pluginsdk")
	}
	if len(c.ExportedMemories()) == 0 {
		rep.add(SevCritical, "wasm.export", m.Wasm, "the module does not export its memory")
	}
	return used
}

// scanPermissions compares requested permissions with the imports: asking
// for more than the module uses, or using what it did not ask for.
func scanPermissions(rep *Report, m *Manifest, used map[string]bool) {
	needs := map[string]string{} // permission -> import that needs it
	for name := range used {
		if perm := hostImportPerms[name]; perm != "" {
			needs[perm] = name
		}
	}
	hasHTTP := slices.ContainsFunc(m.Permissions, func(p string) bool { return strings.HasPrefix(p, PermHTTP) })
	hasOAuth := slices.ContainsFunc(m.Permissions, func(p string) bool { return strings.HasPrefix(p, PermOAuth) })
	for _, perm := range sortedKeys(needs) {
		declared := slices.Contains(m.Permissions, perm) || (perm == PermHTTP && hasHTTP) || (perm == PermOAuth && hasOAuth)
		if !declared {
			name := perm
			switch perm {
			case PermHTTP:
				name = "http:<host>"
			case PermOAuth:
				name = "oauth:<provider>"
			}
			rep.add(SevHigh, "perm.undeclared", m.Wasm, "imports %s but does not request the %q permission", needs[perm], name)
		}
	}
	httpReported := false
	for _, perm := range m.Permissions {
		switch {
		case perm == PermKV || perm == PermPostsRead || perm == PermMail:
			if _, ok := needs[perm]; !ok {
				rep.add(SevMedium, "perm.unused", "plugin.json", "requests %q but never calls the host functions that need it", perm)
			}
		case strings.HasPrefix(perm, PermHTTP):
			if _, ok := needs[PermHTTP]; !ok && !httpReported {
				rep.add(SevMedium, "perm.unused", "plugin.json", "requests %q but never imports http_fetch", perm)
				httpReported = true
			}
		case strings.HasPrefix(perm, PermOAuth):
			if _, ok := needs[PermOAuth]; !ok {
				rep.add(SevMedium, "perm.unused", "plugin.json", "requests %q but never imports oauth_token", perm)
			}
		case perm == PermCommentsPII && !m.HasHook(HookCommentCheck):
			rep.add(SevMedium, "perm.unused", "plugin.json", "requests %q without the comment.check hook", perm)
		case perm == PermAssetsJS && len(m.Assets.JS) == 0:
			rep.add(SevMedium, "perm.unused", "plugin.json", "requests %q but declares no JavaScript assets", perm)
		}
	}
	netHooks := m.HasHook(HookEvent) || m.HasHook(HookCommentCheck) || m.HasHook(HookFormSubmit)
	if used["http_fetch"] && !netHooks {
		rep.add(SevMedium, "perm.http-render-only", m.Wasm, "imports http_fetch but only has render hooks, where the network is never available")
	}
	if used["oauth_token"] && !netHooks {
		rep.add(SevMedium, "perm.oauth-render-only", m.Wasm, "imports oauth_token but only has render hooks, where tokens are never available")
	}
	if used["mail_send"] && !m.HasHook(HookEvent) && !m.HasHook(HookFormSubmit) {
		rep.add(SevMedium, "perm.mail-render-only", m.Wasm, "imports mail_send but has neither the event nor the form.submit hook, where e-mail is available")
	}
}

// ------------------------------------------------------------- strings --

var (
	metadataRe   = regexp.MustCompile(`169\.254\.169\.254|169\.254\.170\.2\b|metadata\.google\.internal|100\.100\.100\.200|fd00:ec2::254|metadata\.azure\.com`)
	privateURLRe = regexp.MustCompile(`(?i)\b(?:https?|wss?|ftp)://(?:localhost\b|127\.\d{1,3}\.\d{1,3}\.\d{1,3}|10\.\d{1,3}\.\d{1,3}\.\d{1,3}|192\.168\.\d{1,3}\.\d{1,3}|172\.(?:1[6-9]|2\d|3[01])\.\d{1,3}\.\d{1,3}|0\.0\.0\.0|\[(?:::1|fc|fd|fe80)[0-9a-f:]*\]|[a-z0-9.-]+\.(?:local|internal|localhost|lan|home\.arpa)\b)`)
	sensitiveRe  = regexp.MustCompile(`/etc/(?:passwd|shadow|sudoers|ssh/)|/proc/(?:self|\d+|net|sys|kcore)/|/root/\.ssh|\.ssh/(?:id_rsa|id_ed25519|authorized_keys)|/var/run/docker\.sock|\.aws/credentials`)
	minerRe      = regexp.MustCompile(`(?i)stratum\+(?:tcp|ssl|tls)://|xmrig|coinhive|coin-hive|cryptonight|cryptoloot|webminepool|minexmr\.com|supportxmr\.com|nanopool\.org|moneroocean\.stream|hashvault\.pro|c3pool\.com|2miners\.com|f2pool\.com|nicehash\.com|minergate\.com|herominers\.com|unmineable\.com`)
	urlHostRe    = regexp.MustCompile(`(?i)\b(?:https?|wss?)://([a-z0-9][a-z0-9-]*(?:\.[a-z0-9-]+)+)`)
	peStub       = []byte("This program cannot be run in DOS mode")
)

// Hosts that appear in ordinary builds and documentation links.
var benignHosts = []string{"go.dev", "golang.org", "pkg.go.dev", "schema.org", "www.w3.org", "w3.org", "json-schema.org",
	"example.com", "example.org", "example.net", "www.example.com", "github.com"}

type stringScan struct {
	m          *Manifest
	fetches    bool
	hosts      map[string]string // undeclared host -> first location
	seen       map[string]bool   // rule+location already reported
	httpGrants []string
}

func newStringScan(m *Manifest, fetches bool) *stringScan {
	ss := &stringScan{m: m, fetches: fetches, hosts: map[string]string{}, seen: map[string]bool{}}
	for _, p := range m.Permissions {
		if strings.HasPrefix(p, PermHTTP) {
			ss.httpGrants = append(ss.httpGrants, p)
		}
	}
	return ss
}

func (ss *stringScan) once(rep *Report, sev, rule, loc, format string, args ...any) {
	if ss.seen[rule+"\x00"+loc] {
		return
	}
	ss.seen[rule+"\x00"+loc] = true
	rep.add(sev, rule, loc, format, args...)
}

func quote(b []byte) string {
	s := strings.ToValidUTF8(string(b), "?")
	if len(s) > 80 {
		s = s[:80] + "…"
	}
	return fmt.Sprintf("%q", s)
}

// scan applies the string rules to b. binary enables the rules that only
// make sense for module data (embedded executables, entropy).
func (ss *stringScan) scan(rep *Report, loc string, b []byte, binary bool) {
	if m := metadataRe.Find(b); m != nil {
		ss.once(rep, SevCritical, "strings.cloud-metadata", loc, "contains the cloud metadata address %s, used to steal instance credentials", quote(m))
	}
	if m := minerRe.Find(b); m != nil {
		ss.once(rep, SevCritical, "strings.miner", loc, "contains a cryptocurrency miner signature %s", quote(m))
	}
	if m := privateURLRe.Find(b); m != nil {
		ss.once(rep, SevHigh, "strings.private-host", loc, "contains a URL to a private or loopback host %s", quote(m))
	}
	if m := sensitiveRe.Find(b); m != nil {
		ss.once(rep, SevHigh, "strings.sensitive-path", loc, "contains the system path %s", quote(m))
	}
	for _, sm := range urlHostRe.FindAllSubmatch(b, 500) {
		full := strings.ToLower(string(sm[1]))
		host := trimRunOn(full)
		if ss.harmlessHost(full) || ss.harmlessHost(host) || ss.grantedRunOn(full) {
			continue
		}
		if _, ok := ss.hosts[host]; !ok && len(ss.hosts) < 50 {
			ss.hosts[host] = loc
		}
	}
	if !binary {
		return
	}
	if kind, off := findExecutable(b); kind != "" {
		ss.once(rep, SevCritical, "strings.embedded-executable", loc, "contains an embedded %s at offset %d", kind, off)
	}
	if size, off := highEntropyRun(b); size > 0 {
		ss.once(rep, SevMedium, "strings.high-entropy", loc, "contains %d KiB of high-entropy data at offset %d (encrypted or compressed payload?)", size>>10, off)
	}
}

// commonTLDs are used to cut host names that run into the next string: Go
// stores string data back to back, so "https://schema.org" followed by
// "value" reads as the host "schema.orgvalue".
var commonTLDs = []string{"com", "org", "net", "io", "dev", "app", "es", "eu", "uk", "de", "fr", "it", "nl", "co", "us",
	"info", "biz", "me", "ai", "cloud", "xyz", "site", "online", "tech", "gov", "edu", "ru", "cn", "jp", "br", "in"}

// harmlessHost: documentation hosts, reserved names and granted hosts.
func (ss *stringScan) harmlessHost(host string) bool {
	return slices.Contains(benignHosts, host) || strings.HasSuffix(host, ".example") || strings.HasSuffix(host, ".test") ||
		strings.HasSuffix(host, ".invalid") || hostAllowed(ss.httpGrants, host)
}

// grantedRunOn reports whether host is a granted host followed directly by
// the next string in the module ("bsky.social" + "embedded"), which
// trimRunOn cannot cut when the top-level domain is not a common one. The
// cut is made inside the last label and checked with hostAllowed. It is
// refused when the whole last label is a real top-level domain:
// "example.co" + "m" is example.com, another host.
func (ss *stringScan) grantedRunOn(host string) bool {
	dot := strings.LastIndexByte(host, '.')
	if dot < 0 || len(ss.httpGrants) == 0 {
		return false
	}
	if _, icann := publicsuffix.PublicSuffix(host[dot+1:]); icann {
		return false
	}
	for k := len(host) - 1; k > dot+1; k-- {
		if hostAllowed(ss.httpGrants, host[:k]) {
			return true
		}
	}
	return false
}

func trimRunOn(host string) string {
	i := strings.LastIndexByte(host, '.')
	if i < 0 {
		return host
	}
	last := host[i+1:]
	if slices.Contains(commonTLDs, last) {
		return host
	}
	best := ""
	for _, tld := range commonTLDs {
		if strings.HasPrefix(last, tld) && len(tld) > len(best) {
			best = tld
		}
	}
	if best == "" {
		return host
	}
	return host[:i+1] + best
}

// report adds the aggregated finding for hard-coded hosts.
func (ss *stringScan) report(rep *Report) {
	if len(ss.hosts) == 0 {
		return
	}
	hosts := sortedKeys(ss.hosts)
	sev, why := SevInfo, "links or text only: the plugin cannot reach them"
	if ss.fetches {
		sev, why = SevMedium, "the plugin makes HTTP requests, but its permissions do not cover these hosts"
	}
	list := hosts
	if len(list) > 10 {
		list = append(list[:10:10], fmt.Sprintf("and %d more", len(hosts)-10))
	}
	rep.add(sev, "strings.undeclared-host", ss.hosts[hosts[0]], "hard-coded hosts not covered by http permissions (%s): %s", why, strings.Join(list, ", "))
}

// findExecutable looks for ELF, PE, Mach-O or WebAssembly binaries.
func findExecutable(b []byte) (string, int) {
	for off := 0; ; {
		i := bytes.Index(b[off:], []byte("\x7fELF"))
		if i < 0 {
			break
		}
		i += off
		if i+7 <= len(b) && (b[i+4] == 1 || b[i+4] == 2) && (b[i+5] == 1 || b[i+5] == 2) && b[i+6] == 1 {
			return "ELF executable", i
		}
		off = i + 1
	}
	if i := bytes.Index(b, peStub); i >= 0 {
		return "Windows (PE) executable", i
	}
	for off := 0; ; {
		i := bytes.Index(b[off:], []byte("MZ"))
		if i < 0 {
			break
		}
		i += off
		if i+0x40 <= len(b) {
			lfanew := int(binary.LittleEndian.Uint32(b[i+0x3c:]))
			if lfanew >= 0x40 && lfanew < 1024 && i+lfanew+4 <= len(b) && bytes.Equal(b[i+lfanew:i+lfanew+4], []byte("PE\x00\x00")) {
				return "Windows (PE) executable", i
			}
		}
		off = i + 1
	}
	machO := []struct {
		magic []byte
		le    bool
	}{{[]byte{0xfe, 0xed, 0xfa, 0xce}, false}, {[]byte{0xfe, 0xed, 0xfa, 0xcf}, false}, {[]byte{0xce, 0xfa, 0xed, 0xfe}, true}, {[]byte{0xcf, 0xfa, 0xed, 0xfe}, true}}
	cpus := []uint32{7, 0x01000007, 12, 0x0100000c, 18, 0x01000012}
	for _, mo := range machO {
		for off := 0; ; {
			i := bytes.Index(b[off:], mo.magic)
			if i < 0 {
				break
			}
			i += off
			if i+8 <= len(b) {
				cpu := binary.BigEndian.Uint32(b[i+4:])
				if mo.le {
					cpu = binary.LittleEndian.Uint32(b[i+4:])
				}
				if slices.Contains(cpus, cpu) {
					return "Mach-O executable", i
				}
			}
			off = i + 1
		}
	}
	if i := bytes.Index(b, wasmMagic); i >= 0 {
		return "WebAssembly module", i
	}
	return "", 0
}

// highEntropyRun returns the size and offset of the largest run of 4 KiB
// windows above 7.5 bits per byte, if it reaches 64 KiB. Ordinary Go builds
// have at most a few such windows (hash seeds, Unicode tables).
func highEntropyRun(b []byte) (int, int) {
	const window, threshold, minRun = 4096, 7.5, 64 << 10
	best, bestOff, run, runOff := 0, 0, 0, 0
	for off := 0; off+window <= len(b); off += window {
		if entropy(b[off:off+window]) > threshold {
			if run == 0 {
				runOff = off
			}
			run += window
			if run > best {
				best, bestOff = run, runOff
			}
		} else {
			run = 0
		}
	}
	if best < minRun {
		return 0, 0
	}
	return best, bestOff
}

func entropy(b []byte) float64 {
	var counts [256]int
	for _, c := range b {
		counts[c]++
	}
	e, n := 0.0, float64(len(b))
	for _, c := range counts {
		if c > 0 {
			p := float64(c) / n
			e -= p * math.Log2(p)
		}
	}
	return e
}

// -------------------------------------------------------------- assets --

type textRule struct {
	sev, rule, msg string
	re             *regexp.Regexp
}

var jsRules = []textRule{
	{SevHigh, "assets.js-eval", "calls eval()", regexp.MustCompile(`\beval\s*\(`)},
	{SevHigh, "assets.js-function", "builds code with new Function()", regexp.MustCompile(`\bnew\s+Function\s*\(|\bFunction\s*\(\s*["'\x60]`)},
	{SevHigh, "assets.js-cookie", "reads or writes document.cookie", regexp.MustCompile(`document\s*\.\s*cookie`)},
	{SevMedium, "assets.js-storage", "uses localStorage or sessionStorage", regexp.MustCompile(`\b(?:localStorage|sessionStorage)\b`)},
	{SevMedium, "assets.js-inline-handler", "creates inline event handlers or javascript: URLs (blocked by the CSP)", regexp.MustCompile(`(?i)setAttribute\s*\(\s*["']on[a-z]+|["'\x60][^"'\x60]*<[a-z][^>]*\son[a-z]{3,20}\s*=|javascript:`)},
	{SevMedium, "assets.js-document-write", "uses document.write", regexp.MustCompile(`document\s*\.\s*write(?:ln)?\s*\(`)},
	{SevMedium, "assets.external-url", "references an external URL", regexp.MustCompile(`(?i)\bhttps?://[a-z0-9-]+(?:\.[a-z0-9-]+)+`)},
	{SevInfo, "assets.js-network", "makes network requests (the CSP limits them to this site)", regexp.MustCompile(`\bfetch\s*\(|XMLHttpRequest|new\s+WebSocket|sendBeacon`)},
}

var cssRules = []textRule{
	{SevMedium, "assets.css-import", "uses @import", regexp.MustCompile(`(?i)@import`)},
	{SevMedium, "assets.css-external-url", "loads an external URL", regexp.MustCompile(`(?i)url\(\s*["']?\s*(?:https?:)?//`)},
	{SevHigh, "assets.css-script", "contains script-like CSS (expression(), behavior, -moz-binding or javascript:)", regexp.MustCompile(`(?i)expression\s*\(|(?:^|[^-\w])behavior\s*:|-moz-binding|javascript:`)},
}

var svgScriptRe = regexp.MustCompile(`(?i)<script|\son[a-z]{3,20}\s*=|javascript:|<foreignObject`)

func lineOf(b []byte, off int) int { return bytes.Count(b[:off], []byte("\n")) + 1 }

func scanAsset(rep *Report, ss *stringScan, name string, b []byte) {
	ss.scan(rep, name, b, true)
	var rules []textRule
	switch strings.ToLower(path.Ext(name)) {
	case ".js":
		rules = jsRules
	case ".css":
		rules = cssRules
	case ".svg":
		if loc := svgScriptRe.FindIndex(b); loc != nil {
			rep.add(SevHigh, "assets.svg-script", fmt.Sprintf("%s:%d", name, lineOf(b, loc[0])), "SVG with scripts or event handlers %s", quote(b[loc[0]:loc[1]]))
		}
	}
	for _, r := range rules {
		if loc := r.re.FindIndex(b); loc != nil {
			rep.add(r.sev, r.rule, fmt.Sprintf("%s:%d", name, lineOf(b, loc[0])), "%s: %s", r.msg, quote(b[loc[0]:loc[1]]))
		}
	}
}

// ----------------------------------------------------------- blocklist --

//go:embed blocklist.txt
var embeddedBlocklist string

// loadBlocklist reads the embedded list of known-bad SHA-256 hashes plus the
// optional site file.
func (h *Host) loadBlocklist() map[string]string {
	out := map[string]string{}
	parse := func(r io.Reader, src string) {
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			hash, note, _ := strings.Cut(line, " ")
			hash = strings.ToLower(hash)
			if len(hash) == 64 {
				if _, err := hex.DecodeString(hash); err == nil {
					out[hash] = strings.TrimSpace(note + " (" + src + ")")
				}
			}
		}
	}
	parse(strings.NewReader(embeddedBlocklist), "built-in list")
	if h.blocklist != "" {
		if f, err := os.Open(h.blocklist); err == nil {
			parse(io.LimitReader(f, 4<<20), filepath.Base(h.blocklist))
			f.Close()
		} else if !errors.Is(err, os.ErrNotExist) {
			h.log.Warn("plugin blocklist", "err", err)
		}
	}
	return out
}

func (h *Host) scanBlocklist(rep *Report, assets map[string]assetFile) {
	bad := h.loadBlocklist()
	check := func(hash, what string) {
		if note, ok := bad[hash]; ok && hash != "" {
			rep.add(SevCritical, "hash.blocklisted", what, "SHA-256 %s is on the list of known-bad plugins: %s", hash[:16]+"…", note)
		}
	}
	check(rep.PackageSHA256, "package")
	check(rep.WasmSHA256, "plugin.wasm")
	for _, name := range sortedKeys(assets) {
		sum := sha256.Sum256(assets[name].data)
		check(hex.EncodeToString(sum[:]), name)
	}
}

// ------------------------------------------------------------- dry run --

var (
	samplePost = &PostInfo{ID: 1, Type: "post", Title: "Sample post", Slug: "sample-post", Lang: "en",
		URL: "https://example.test/blog/sample-post", Excerpt: "A sample excerpt.", Author: "Admin",
		Categories: []string{"News"}, Tags: []string{"sample"},
		Terms: []TermInfo{{Taxonomy: "category", Slug: "news", Name: "News", URL: "https://example.test/category/news"},
			{Taxonomy: "tag", Slug: "sample", Name: "sample", URL: "https://example.test/tag/sample"}},
		Image: "https://example.test/uploads/sample.jpg", PublishedAt: "2026-01-01T00:00:00Z", WordCount: 120}
	samplePage = &PageInfo{Kind: "single", URL: samplePost.URL, Title: samplePost.Title, Description: samplePost.Excerpt, Post: samplePost}
)

func sampleEvent(name string) any {
	switch {
	case strings.HasPrefix(name, "post."):
		return samplePost
	case strings.HasPrefix(name, "comment."):
		return map[string]any{"id": 1, "post_id": 1, "post": samplePost, "author": "Sample Visitor", "status": "pending", "content": "Nice post!"}
	case name == "user.registered":
		return map[string]string{"username": "sample", "role": "subscriber"}
	default:
		return map[string]string{"time": "2026-01-01T00:00:00Z"}
	}
}

func sampleFields(f *FormDecl) map[string]string {
	out := map[string]string{}
	for _, fd := range f.Fields {
		switch fd.Type {
		case "email":
			out[fd.Name] = "visitor@example.test"
		case "url":
			out[fd.Name] = "https://example.test/"
		case "number":
			out[fd.Name] = "1"
		case "checkbox":
			out[fd.Name] = "true"
		case "select":
			out[fd.Name] = fd.Choices[0]
		default:
			out[fd.Name] = "Sample"
		}
	}
	return out
}

type dryCall struct {
	hook, event, label string
	payload            any
	timeout            time.Duration
}

// dryRun instantiates the module with the normal limits (memory, deadlines)
// and nothing granted, checks the ABI export and calls every declared hook
// with a synthetic payload.
func (h *Host) dryRun(ctx context.Context, rep *Report, m *Manifest, compiled wazero.CompiledModule) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	p := &Plugin{Manifest: *m, Status: StatusEnabled, compiled: compiled, maxInst: h.maxInstances,
		pool: make(chan *instance, h.maxInstances), dryRun: true}
	defer p.retire(context.Background())

	inst, err := p.acquire(ctx, h)
	if err != nil {
		rep.add(SevCritical, "dryrun.instantiate", m.Wasm, "the module cannot be started: %v", err)
		return
	}
	if f := inst.mod.ExportedFunction("sap13ns_abi"); f != nil {
		res, err := f.Call(ctx)
		if err != nil || len(res) != 1 || int32(res[0]) != ABIVersion {
			rep.add(SevCritical, "dryrun.abi", m.Wasm, "sap13ns_abi does not return %d", ABIVersion)
		}
	}
	p.release(inst, false)

	var calls []dryCall
	for _, hook := range m.Hooks {
		switch hook {
		case HookContentFilter:
			calls = append(calls, dryCall{hook, "", hook, filterPayload{HTML: "<p>Hello <strong>world</strong>.</p>", Post: samplePost}, renderTimeout})
		case HookShortcode:
			for _, sc := range m.Shortcodes {
				calls = append(calls, dryCall{hook, "", "shortcode [" + sc + "]", shortcodePayload{Name: sc, Attrs: map[string]string{}, Inner: "Sample text", Post: samplePost}, renderTimeout})
			}
		case HookWidget:
			for _, w := range m.Widgets {
				calls = append(calls, dryCall{hook, "", "widget " + w.ID, widgetPayload{Widget: w.ID, Config: map[string]string{}, Page: samplePage}, renderTimeout})
			}
		case HookHeadMeta:
			calls = append(calls, dryCall{hook, "", hook, samplePage, renderTimeout})
		case HookEvent:
			for _, e := range m.Events {
				calls = append(calls, dryCall{hook, e, "event " + e, sampleEvent(e), eventTimeout})
			}
		case HookCommentCheck:
			calls = append(calls, dryCall{hook, "", hook, commentPayload{Author: "Sample Visitor", URL: "https://visitor.example",
				Content: "Nice post, thanks!", Post: samplePost}, commentTimeout})
		case HookFormSubmit:
			for i := range m.Forms {
				f := &m.Forms[i]
				calls = append(calls, dryCall{hook, "", "form " + f.ID, formPayload{Form: f.ID, Fields: sampleFields(f), Lang: "en", PageURL: samplePost.URL}, formTimeout})
			}
		}
	}
	for _, c := range calls {
		if ctx.Err() != nil {
			rep.add(SevInfo, "dryrun.skipped", c.label, "not run: the dry run took longer than 30 seconds")
			continue
		}
		p.failMu.Lock()
		p.failures, p.pausedTil = 0, time.Time{}
		p.failMu.Unlock()
		start := time.Now()
		resp, err := h.call(ctx, p, c.hook, c.event, c.payload, c.timeout, 0)
		var exit *sys.ExitError
		switch {
		case err == nil:
		case resp != nil && resp.Error == "plugin panic":
			rep.add(SevHigh, "dryrun.panic", c.label, "the handler panicked")
		case resp != nil:
			rep.add(SevInfo, "dryrun.error", c.label, "returned an error with sample data: %s", truncate(resp.Error, 200))
		case errors.As(err, &exit) && (exit.ExitCode() == sys.ExitCodeDeadlineExceeded || exit.ExitCode() == sys.ExitCodeContextCanceled),
			errors.Is(err, context.DeadlineExceeded):
			rep.add(SevHigh, "dryrun.timeout", c.label, "did not return within %s", c.timeout)
		case strings.HasPrefix(err.Error(), "invalid response"):
			rep.add(SevMedium, "dryrun.invalid-response", c.label, "the response is not valid JSON")
		default:
			rep.add(SevHigh, "dryrun.trap", c.label, "trapped: %s", truncate(err.Error(), 200))
		}
		if err == nil && time.Since(start) > c.timeout/2 && c.timeout <= renderTimeout {
			rep.add(SevInfo, "dryrun.slow", c.label, "took %s of its %s budget", time.Since(start).Round(time.Millisecond), c.timeout)
		}
	}
}
