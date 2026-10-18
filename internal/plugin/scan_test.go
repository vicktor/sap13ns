package plugin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tetratelabs/wazero"
)

// Host imports with the signatures the sap13ns module implements.
var (
	impKVGet  = wImport{"sap13ns", "kv_get", []byte{wI32, wI32}, []byte{wI32}}
	impFetch  = wImport{"sap13ns", "http_fetch", []byte{wI32, wI32}, []byte{wI32}}
	impMail   = wImport{"sap13ns", "mail_send", []byte{wI32, wI32}, []byte{wI32}}
	impSystem = wImport{"env", "system", []byte{wI32, wI32}, []byte{wI32}}
)

const okHTML = `{"html":"<p>ok</p>"}`

// manifestFor is a valid manifest with id t1 plus the given fields.
func manifestFor(fields string) string {
	m := `{"abi": 1, "id": "t1", "name": "Test", "version": "1.0.0", "author": "Alice"`
	if fields == "" {
		fields = `"hooks": ["shortcode"], "shortcodes": ["t"]`
	}
	return m + ", " + fields + "}"
}

func with(m wModule, imports ...wImport) wModule {
	m.imports = append(m.imports, imports...)
	return m
}

func scan(t *testing.T, dir string, opts ScanOptions, blocklist string) *Report {
	t.Helper()
	rep, err := Scan(context.Background(), dir, opts, blocklist)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func finding(rep *Report, rule string) *Finding {
	for i := range rep.Findings {
		if rep.Findings[i].Rule == rule {
			return &rep.Findings[i]
		}
	}
	return nil
}

func dumpReport(rep *Report) string {
	var b strings.Builder
	b.WriteString("verdict " + rep.Verdict)
	for _, f := range rep.Findings {
		b.WriteString("\n  " + f.Severity + " " + f.Rule + " [" + f.Location + "] " + f.Message)
	}
	return b.String()
}

var docsFiles = map[string]string{"README.md": "# Test\n", "LICENSE": "MIT\n"}

func TestScanClean(t *testing.T) {
	// Every hook, a well-formed module that answers with valid JSON, and the
	// documentation files: nothing to report.
	m := manifestFor(`"hooks": ["shortcode", "widget", "content.filter", "head.meta", "event", "comment.check", "form.submit"],
		"shortcodes": ["t"], "widgets": [{"id": "w", "title": "W"}], "events": ["post.published", "comment.created"],
		"forms": [{"id": "f", "fields": [{"name": "email", "type": "email", "label": "E-mail", "required": true}]}]`)
	rep := scan(t, writePlugin(t, m, respond(okHTML).bytes(), docsFiles), ScanOptions{}, "")
	if rep.Verdict != VerdictClean || len(rep.Findings) != 0 {
		t.Fatalf("want a clean report without findings, got %s", dumpReport(rep))
	}
	if rep.PluginID != "t1" || rep.Version != "1.0.0" || len(rep.WasmSHA256) != 64 || rep.Scanner != ScannerVersion {
		t.Fatalf("report metadata: %+v", rep)
	}
}

func TestScanRules(t *testing.T) {
	ok := respond(okHTML)
	noABI := respond(okHTML)
	noABI.funcs[0].export = ""
	noHandle := respond(okHTML)
	noHandle.funcs[2].export = "handle"
	badHandle := respond(okHTML)
	badHandle.funcs[2].params = []byte{wI32}
	noMem := respond(okHTML)
	noMem.noMem, noMem.data = true, nil
	impMem := respond(okHTML)
	impMem.impMem = true
	wrongABI := respond(okHTML)
	wrongABI.funcs[0].body = append([]byte{0x41}, sleb(ABIVersion+1)...)
	badSig := with(ok, wImport{"sap13ns", "kv_get", nil, nil})
	withCustom := ok
	withCustom.custom = map[string][]byte{"producers": []byte("fetch http://169.254.169.254/latest/meta-data/")}
	hugeTable := respond(okHTML)
	hugeTable.tables = []uint64{1 << 27} // about 1 GiB when instantiated
	manyTables := respond(okHTML)
	manyTables.tables = []uint64{1, 1, 1, 1, 1}

	elf := append([]byte("\x7fELF\x02\x01\x01\x00"), make([]byte, 56)...)
	pe := append([]byte("MZ"), make([]byte, 0x3a)...)
	pe = append(pe, 0x40, 0, 0, 0)
	pe = append(pe, []byte("PE\x00\x00")...)
	macho := []byte{0xcf, 0xfa, 0xed, 0xfe, 0x07, 0x00, 0x00, 0x01, 0, 0, 0, 0}
	entropyRNG := rand.New(rand.NewChaCha8([32]byte{1}))
	random := make([]byte, 80<<10)
	for i := range random {
		random[i] = byte(entropyRNG.Uint32())
	}

	render := `"hooks": ["shortcode"], "shortcodes": ["t"]`
	event := `"hooks": ["event"], "events": ["post.published"]`

	cases := []struct {
		name     string
		manifest string // "" means no plugin.json
		wasm     []byte // nil means no module
		extra    map[string]string
		opts     ScanOptions
		rule     string
		sev      string
	}{
		{"no manifest", "", ok.bytes(), nil, ScanOptions{}, "manifest.missing", SevCritical},
		{"huge table", manifestFor(""), hugeTable.bytes(), nil, ScanOptions{}, "wasm.tables", SevCritical},
		{"many tables", manifestFor(""), manyTables.bytes(), nil, ScanOptions{}, "wasm.tables", SevCritical},
		{"broken manifest", `{"abi": 1, "id":`, ok.bytes(), nil, ScanOptions{}, "manifest.invalid", SevCritical},
		{"invalid manifest", manifestFor(`"hooks": ["shortcode"], "shortcodes": ["no spaces"]`), ok.bytes(), nil, ScanOptions{}, "manifest.invalid", SevCritical},
		{"other ABI", strings.Replace(manifestFor(""), `"abi": 1`, `"abi": 2`, 1), ok.bytes(), nil, ScanOptions{}, "manifest.abi", SevCritical},
		{"reserved id", strings.Replace(manifestFor(""), `"t1"`, `"my-plugin"`, 1), ok.bytes(), nil, ScanOptions{}, "manifest.reserved-id", SevCritical},
		{"id taken by another author", strings.Replace(manifestFor(""), "Alice", "Mallory", 1), ok.bytes(), nil,
			ScanOptions{Replacing: &Manifest{ID: "t1", Author: "Alice"}}, "manifest.id-collision", SevHigh},
		{"shortcode collision", manifestFor(""), ok.bytes(), nil,
			ScanOptions{Installed: []Manifest{{ID: "other", Shortcodes: []string{"t"}}}}, "manifest.shortcode-collision", SevMedium},

		{"http:*", manifestFor(event + `, "permissions": ["http:*"]`), with(ok, impFetch).bytes(), nil, ScanOptions{}, "perm.http-wildcard", SevHigh},
		{"high-risk permission", manifestFor(`"hooks": ["comment.check"], "permissions": ["comments:pii"]`), respond(`{"verdict":"ok"}`).bytes(), nil, ScanOptions{}, "perm.high-risk", SevMedium},
		{"undeclared permission", manifestFor(render), with(ok, impKVGet).bytes(), nil, ScanOptions{}, "perm.undeclared", SevHigh},
		{"undeclared http", manifestFor(event), with(ok, impFetch).bytes(), nil, ScanOptions{}, "perm.undeclared", SevHigh},
		{"unused permission", manifestFor(render + `, "permissions": ["kv"]`), ok.bytes(), nil, ScanOptions{}, "perm.unused", SevMedium},
		{"unused http", manifestFor(event + `, "permissions": ["http:api.example.com"]`), ok.bytes(), nil, ScanOptions{}, "perm.unused", SevMedium},
		{"unused comments:pii", manifestFor(render + `, "permissions": ["comments:pii"]`), ok.bytes(), nil, ScanOptions{}, "perm.unused", SevMedium},
		{"http in render hooks", manifestFor(render + `, "permissions": ["http:api.example.com"]`), with(ok, impFetch).bytes(), nil, ScanOptions{}, "perm.http-render-only", SevMedium},
		{"mail in render hooks", manifestFor(render + `, "permissions": ["mail"]`), with(ok, impMail).bytes(), nil, ScanOptions{}, "perm.mail-render-only", SevMedium},

		{"no module", manifestFor(""), nil, nil, ScanOptions{}, "wasm.missing", SevCritical},
		{"not a module", manifestFor(""), []byte("\x00asm\x01\x00\x00\x00\x01\xff\xff"), nil, ScanOptions{}, "wasm.compile", SevCritical},
		{"foreign import", manifestFor(""), with(ok, impSystem).bytes(), nil, ScanOptions{}, "wasm.import", SevCritical},
		{"import signature", manifestFor(render + `, "permissions": ["kv"]`), badSig.bytes(), nil, ScanOptions{}, "wasm.import-signature", SevCritical},
		{"imported memory", manifestFor(""), impMem.bytes(), nil, ScanOptions{}, "wasm.import-memory", SevCritical},
		{"missing handle", manifestFor(""), noHandle.bytes(), nil, ScanOptions{}, "wasm.export", SevCritical},
		{"handle signature", manifestFor(""), badHandle.bytes(), nil, ScanOptions{}, "wasm.export", SevCritical},
		{"no memory export", manifestFor(""), noMem.bytes(), nil, ScanOptions{}, "wasm.export", SevCritical},
		{"no ABI export", manifestFor(""), noABI.bytes(), nil, ScanOptions{}, "wasm.no-abi-export", SevMedium},

		{"cloud metadata", manifestFor(""), respondWith(okHTML, []byte("http://169.254.169.254/latest/meta-data/")).bytes(), nil, ScanOptions{}, "strings.cloud-metadata", SevCritical},
		{"metadata in a custom section", manifestFor(""), withCustom.bytes(), nil, ScanOptions{}, "strings.cloud-metadata", SevCritical},
		{"miner", manifestFor(""), respondWith(okHTML, []byte("stratum+tcp://pool.example:3333")).bytes(), nil, ScanOptions{}, "strings.miner", SevCritical},
		{"private host", manifestFor(""), respondWith(okHTML, []byte("http://192.168.1.1/admin")).bytes(), nil, ScanOptions{}, "strings.private-host", SevHigh},
		{"sensitive path", manifestFor(""), respondWith(okHTML, []byte("/etc/shadow")).bytes(), nil, ScanOptions{}, "strings.sensitive-path", SevHigh},
		{"embedded ELF", manifestFor(""), respondWith(okHTML, elf).bytes(), nil, ScanOptions{}, "strings.embedded-executable", SevCritical},
		{"embedded PE", manifestFor(""), respondWith(okHTML, pe).bytes(), nil, ScanOptions{}, "strings.embedded-executable", SevCritical},
		{"embedded Mach-O", manifestFor(""), respondWith(okHTML, macho).bytes(), nil, ScanOptions{}, "strings.embedded-executable", SevCritical},
		{"embedded module", manifestFor(""), respondWith(okHTML, wasmMagic).bytes(), nil, ScanOptions{}, "strings.embedded-executable", SevCritical},
		{"high entropy", manifestFor(""), respondWith(okHTML, random).bytes(), nil, ScanOptions{}, "strings.high-entropy", SevMedium},
		{"hard-coded host", manifestFor(""), respondWith(okHTML, []byte("see https://tracker.evil-analytics.com/c")).bytes(), nil, ScanOptions{}, "strings.undeclared-host", SevInfo},
		{"fetched host", manifestFor(event + `, "permissions": ["http:api.example.com"]`),
			with(respondWith(okHTML, []byte("https://tracker.evil-analytics.com/c")), impFetch).bytes(), nil, ScanOptions{}, "strings.undeclared-host", SevMedium},

		{"missing asset", manifestFor(render + `, "assets": {"css": ["assets/site.css"]}`), ok.bytes(), nil, ScanOptions{}, "assets.missing", SevHigh},
		{"disallowed asset", manifestFor(""), ok.bytes(), map[string]string{"assets/run.sh": "#!/bin/sh\n"}, ScanOptions{}, "assets.invalid", SevCritical},
		{"eval", manifestFor(render + `, "permissions": ["assets:js"], "assets": {"js": ["assets/a.js"]}`), ok.bytes(),
			map[string]string{"assets/a.js": "var x = 1;\neval(atob(x));\n"}, ScanOptions{}, "assets.js-eval", SevHigh},
		{"new Function", manifestFor(render + `, "permissions": ["assets:js"], "assets": {"js": ["assets/a.js"]}`), ok.bytes(),
			map[string]string{"assets/a.js": "new Function('return 1')()"}, ScanOptions{}, "assets.js-function", SevHigh},
		{"cookies", manifestFor(render + `, "permissions": ["assets:js"], "assets": {"js": ["assets/a.js"]}`), ok.bytes(),
			map[string]string{"assets/a.js": "send(document.cookie)"}, ScanOptions{}, "assets.js-cookie", SevHigh},
		{"inline handler", manifestFor(render + `, "permissions": ["assets:js"], "assets": {"js": ["assets/a.js"]}`), ok.bytes(),
			map[string]string{"assets/a.js": "el.setAttribute('onclick', 'go()')"}, ScanOptions{}, "assets.js-inline-handler", SevMedium},
		{"external URL in JS", manifestFor(render + `, "permissions": ["assets:js"], "assets": {"js": ["assets/a.js"]}`), ok.bytes(),
			map[string]string{"assets/a.js": "fetch('https://cdn.evil-analytics.com/x')"}, ScanOptions{}, "assets.external-url", SevMedium},
		{"css @import", manifestFor(render + `, "assets": {"css": ["assets/a.css"]}`), ok.bytes(),
			map[string]string{"assets/a.css": "@import 'other.css';"}, ScanOptions{}, "assets.css-import", SevMedium},
		{"css external url", manifestFor(render + `, "assets": {"css": ["assets/a.css"]}`), ok.bytes(),
			map[string]string{"assets/a.css": "body { background: url(https://evil.example/p.png) }"}, ScanOptions{}, "assets.css-external-url", SevMedium},
		{"css script", manifestFor(render + `, "assets": {"css": ["assets/a.css"]}`), ok.bytes(),
			map[string]string{"assets/a.css": "a { behavior: url(x.htc) }"}, ScanOptions{}, "assets.css-script", SevHigh},
		{"svg script", manifestFor(""), ok.bytes(), map[string]string{"assets/i.svg": `<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"></svg>`}, ScanOptions{}, "assets.svg-script", SevHigh},
		{"asset with metadata IP", manifestFor(""), ok.bytes(), map[string]string{"assets/data.json": `{"u": "http://169.254.169.254/"}`}, ScanOptions{}, "strings.cloud-metadata", SevCritical},

		{"trap", manifestFor(""), abiModule(wTrap, nil).bytes(), nil, ScanOptions{}, "dryrun.trap", SevHigh},
		{"endless loop", manifestFor(""), abiModule(wLoop, nil).bytes(), nil, ScanOptions{}, "dryrun.timeout", SevHigh},
		{"wrong ABI version", manifestFor(""), wrongABI.bytes(), nil, ScanOptions{}, "dryrun.abi", SevCritical},
		{"invalid response", manifestFor(""), respond("not json").bytes(), nil, ScanOptions{}, "dryrun.invalid-response", SevMedium},
		{"error response", manifestFor(""), respond(`{"error":"needs real data"}`).bytes(), nil, ScanOptions{}, "dryrun.error", SevInfo},
		{"panic", manifestFor(""), respond(`{"error":"plugin panic"}`).bytes(), nil, ScanOptions{}, "dryrun.panic", SevHigh},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rep := scan(t, writePlugin(t, tc.manifest, tc.wasm, tc.extra), tc.opts, "")
			f := finding(rep, tc.rule)
			if f == nil {
				t.Fatalf("no %s finding: %s", tc.rule, dumpReport(rep))
			}
			if f.Severity != tc.sev {
				t.Fatalf("%s is %s, want %s", tc.rule, f.Severity, tc.sev)
			}
			want := map[string]string{SevCritical: VerdictBlocked, SevHigh: VerdictReview}[tc.sev]
			if want != "" && rep.Verdict != want && !(want == VerdictReview && rep.Verdict == VerdictBlocked) {
				t.Fatalf("verdict %s, want %s: %s", rep.Verdict, want, dumpReport(rep))
			}
			if tc.sev == SevCritical && (rep.Has("dryrun.trap") || rep.Has("dryrun.error")) {
				t.Fatalf("a module with critical findings was run: %s", dumpReport(rep))
			}
		})
	}
}

func TestScanNoFalsePositives(t *testing.T) {
	// Granted hosts, documentation hosts and Go's back-to-back strings are
	// not reported.
	data := []byte("https://api.example.com/v1 https://schema.orgvalue https://www.w3.org/2000/svg https://cdn.service.test/x")
	m := manifestFor(`"hooks": ["event"], "events": ["post.published"], "permissions": ["http:api.example.com"]`)
	rep := scan(t, writePlugin(t, m, with(respondWith(okHTML, data), impFetch).bytes(), docsFiles), ScanOptions{}, "")
	if len(rep.Findings) != 0 {
		t.Fatalf("unexpected findings: %s", dumpReport(rep))
	}

	// A granted host with an uncommon top-level domain, run into the next
	// string, is still the granted host; a subdomain of it is not.
	ss := newStringScan(&Manifest{Permissions: []string{"http:bsky.social", "http:*.masto.host", "http:example.co"}}, true)
	for host, want := range map[string]bool{"bsky.socialembedded": true, "bsky.social-x": true, "bsky.social.evil.net": false,
		"bsky.socia": false, "a.masto.hostdata": true, "a.masto.host.evil.net": false, "evil.masto.hosting.net": false,
		"example.com": false, "example.comx": true, "x.masto.hostel.com": false} {
		if got := ss.grantedRunOn(host); got != want {
			t.Errorf("grantedRunOn(%q) = %v, want %v", host, got, want)
		}
	}

	for in, want := range map[string]string{"schema.orgvalue": "schema.org", "api.example.comdata": "api.example.com",
		"cdn.example.io": "cdn.example.io", "site.local": "site.local", "x.esp": "x.es"} {
		if got := trimRunOn(in); got != want {
			t.Errorf("trimRunOn(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestScanBlocklist(t *testing.T) {
	wasm := respond(okHTML).bytes()
	sum := sha256.Sum256(wasm)
	list := filepath.Join(t.TempDir(), "blocklist.txt")
	os.WriteFile(list, []byte("# known bad\n"+strings.ToUpper(hex.EncodeToString(sum[:]))+" credential stealer\n"), 0o644)
	dir := writePlugin(t, manifestFor(""), wasm, nil)
	rep := scan(t, dir, ScanOptions{}, list)
	if f := finding(rep, "hash.blocklisted"); f == nil || f.Severity != SevCritical || !strings.Contains(f.Message, "credential stealer") || !rep.Blocked() {
		t.Fatalf("blocklisted module: %s", dumpReport(rep))
	}
	if rep := scan(t, dir, ScanOptions{}, ""); rep.Has("hash.blocklisted") {
		t.Fatal("the module is blocklisted without the site list")
	}
	// The archive hash is checked too.
	rep = scan(t, dir, ScanOptions{PackageSHA256: hex.EncodeToString(sum[:])}, list)
	if !rep.Has("hash.blocklisted") {
		t.Fatal("package hash not checked")
	}
}

func TestReportVerdicts(t *testing.T) {
	for _, tc := range []struct {
		sevs    []string
		verdict string
		confirm bool
	}{
		{nil, VerdictClean, false},
		{[]string{SevInfo, SevMedium}, VerdictClean, false},
		{[]string{SevMedium, SevHigh}, VerdictReview, true},
		{[]string{SevHigh, SevCritical}, VerdictBlocked, true},
	} {
		rep := &Report{}
		for _, s := range tc.sevs {
			rep.add(s, "r."+s, "", "x")
		}
		rep.finish()
		if rep.Verdict != tc.verdict || rep.NeedsConfirmation() != tc.confirm || rep.Findings == nil || rep.Imports == nil {
			t.Errorf("%v: verdict %s confirm %v", tc.sevs, rep.Verdict, rep.NeedsConfirmation())
		}
		if len(rep.Findings) > 1 && sevRank[rep.Findings[0].Severity] > sevRank[rep.Findings[1].Severity] {
			t.Errorf("%v: findings not sorted by severity", tc.sevs)
		}
	}
}

// TestScanFixtures builds the hostile Go plugins in testdata and checks the
// scanner catches each one, and that an ordinary plugin scans clean.
func TestScanFixtures(t *testing.T) {
	if testing.Short() {
		t.Skip("builds wasm modules")
	}
	dir := t.TempDir()
	for _, tc := range []struct {
		id, rule, verdict string
	}{
		{"callout", "", VerdictClean},
		{"badimport", "wasm.import", VerdictBlocked},
		{"wildcard", "perm.http-wildcard", VerdictReview},
		{"metadata", "strings.cloud-metadata", VerdictBlocked},
		{"elfdrop", "strings.embedded-executable", VerdictBlocked},
		{"evil", "strings.cloud-metadata", VerdictBlocked},
	} {
		t.Run(tc.id, func(t *testing.T) {
			pdir := installPlugin(t, dir, "./internal/plugin/testdata/"+tc.id, tc.id)
			rep := scan(t, pdir, ScanOptions{}, "")
			if rep.Verdict != tc.verdict || (tc.rule != "" && !rep.Has(tc.rule)) {
				t.Fatalf("want %s with %q: %s", tc.verdict, tc.rule, dumpReport(rep))
			}
			if tc.verdict == VerdictClean && rep.Count(SevMedium) > 0 {
				t.Fatalf("ordinary plugin has medium findings: %s", dumpReport(rep))
			}
			t.Log(dumpReport(rep))
		})
	}
}

func TestTablesNormalModule(t *testing.T) {
	m := respond(okHTML)
	m.tables = []uint64{8000} // a Go program: about one entry per function
	parts, err := parseWasm(m.bytes())
	if err != nil || len(parts.tables) != 1 || parts.tables[0].min != 8000 || parts.checkTables() != nil {
		t.Fatalf("tables %v err %v", parts, err)
	}
	imp := respond(okHTML)
	imp.impMem = true
	if parts, err := parseWasm(imp.bytes()); err != nil || len(parts.tables) != 0 {
		t.Fatalf("imports: %v %v", parts, err)
	}
}

// growModule exports grow(), which runs table.grow on a one-entry table
// without a maximum and returns the old size, or -1 when the table cannot
// grow. limits overrides the table's limits.
func growModule(limits ...byte) []byte {
	if limits == nil {
		limits = []byte{0x00, 0x01}
	}
	body := append([]byte{0x00, 0xd0, 0x70, 0x41}, sleb(1<<20)...) // no locals; ref.null func; i32.const 2^20
	body = append(body, 0xfc, 0x0f, 0x00, 0x0b)                    // table.grow 0; end
	out := append([]byte{}, wasmMagic...)
	out = append(out, wSection(1, wVec([][]byte{{0x60, 0x00, 0x01, wI32}}))...)
	out = append(out, wSection(3, wVec([][]byte{{0x00}}))...)
	out = append(out, wSection(4, wVec([][]byte{append([]byte{0x70}, limits...)}))...)
	out = append(out, wSection(7, wVec([][]byte{append(wName("grow"), 0x00, 0x00)}))...)
	return append(out, wSection(10, wVec([][]byte{append(uleb(uint64(len(body))), body...)}))...)
}

func TestPinTables(t *testing.T) {
	ctx := context.Background()
	rt := wazero.NewRuntime(ctx)
	defer rt.Close(ctx)
	grow := func(b []byte) int32 {
		t.Helper()
		mod, err := rt.Instantiate(ctx, b)
		if err != nil {
			t.Fatal(err)
		}
		defer mod.Close(ctx)
		res, err := mod.ExportedFunction("grow").Call(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return int32(res[0])
	}
	raw := growModule()
	if got := grow(raw); got != 1 {
		t.Fatalf("unpinned table did not grow: %d", got)
	}
	parts, err := parseWasm(raw)
	if err != nil || parts.checkTables() != nil {
		t.Fatalf("parse: %v", err)
	}
	pinned := parts.pinTables(raw)
	if got := grow(pinned); got != -1 {
		t.Fatalf("pinned table grew: %d", got)
	}
	again, err := parseWasm(pinned)
	if err != nil || len(again.tables) != 1 || !again.tables[0].hasMax || again.tables[0].max != 1 {
		t.Fatalf("pinned tables %+v err %v", again, err)
	}
	if !bytes.Equal(again.pinTables(pinned), pinned) {
		t.Fatal("a pinned module changed again")
	}

	for name, limits := range map[string][]byte{
		"max above the cap": append([]byte{0x01, 0x01}, uleb(maxTableEntries+1)...),
		"imported":          nil,
	} {
		b := growModule(limits...)
		if name == "imported" {
			b = append(append([]byte{}, wasmMagic...), wSection(2, wVec([][]byte{
				append(append(wName("env"), wName("t")...), 0x01, 0x70, 0x00, 0x01)}))...)
		}
		parts, err := parseWasm(b)
		if err != nil || parts.checkTables() == nil {
			t.Errorf("%s: accepted (parse error %v)", name, err)
		}
	}
	if _, err := parseWasm(growModule(0x01, 0x05, 0x01)); err == nil {
		t.Error("max below min parsed")
	}
}
