package plugin

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// A minimal WebAssembly assembler for scanner tests: modules that trap,
// loop forever, import forbidden functions or carry payloads in their data
// section, which the Go toolchain cannot produce on purpose.

const (
	wI32 = 0x7f
	wI64 = 0x7e
)

type wFunc struct {
	params, results []byte
	body            []byte // instructions, without the final end
	export          string
}

type wImport struct {
	module, name    string
	params, results []byte
}

type wModule struct {
	imports []wImport
	funcs   []wFunc
	data    []byte // placed at offset 2048
	custom  map[string][]byte
	noMem   bool // no memory at all
	impMem  bool // import the memory (env.memory) instead of defining it
}

func uleb(v uint64) []byte { return binary.AppendUvarint(nil, v) }

func sleb(v int64) []byte {
	var out []byte
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if (v == 0 && b&0x40 == 0) || (v == -1 && b&0x40 != 0) {
			return append(out, b)
		}
		out = append(out, b|0x80)
	}
}

func wName(s string) []byte { return append(uleb(uint64(len(s))), s...) }

func wVec(items [][]byte) []byte {
	out := uleb(uint64(len(items)))
	for _, it := range items {
		out = append(out, it...)
	}
	return out
}

func wSection(id byte, payload []byte) []byte {
	return append(append([]byte{id}, uleb(uint64(len(payload)))...), payload...)
}

func (m wModule) bytes() []byte {
	var types [][]byte
	typeOf := func(params, results []byte) uint64 {
		t := append(append(append([]byte{0x60}, uleb(uint64(len(params)))...), params...), uleb(uint64(len(results)))...)
		t = append(t, results...)
		for i, have := range types {
			if bytes.Equal(have, t) {
				return uint64(i)
			}
		}
		types = append(types, t)
		return uint64(len(types) - 1)
	}
	var imports, funcs, exports, codes [][]byte
	for _, im := range m.imports {
		imports = append(imports, append(append(append(wName(im.module), wName(im.name)...), 0x00), uleb(typeOf(im.params, im.results))...))
	}
	if m.impMem {
		imports = append(imports, append(append(wName("env"), wName("memory")...), 0x02, 0x00, 0x02))
	}
	for i, f := range m.funcs {
		funcs = append(funcs, uleb(typeOf(f.params, f.results)))
		body := append([]byte{0x00}, f.body...) // no locals
		body = append(body, 0x0b)
		codes = append(codes, append(uleb(uint64(len(body))), body...))
		if f.export != "" {
			exports = append(exports, append(append(wName(f.export), 0x00), uleb(uint64(len(m.imports)+i))...))
		}
	}
	if !m.noMem {
		exports = append(exports, append(append(wName("memory"), 0x02), 0x00))
	}
	out := []byte{0x00, 'a', 's', 'm', 0x01, 0x00, 0x00, 0x00}
	out = append(out, wSection(1, wVec(types))...)
	if len(imports) > 0 {
		out = append(out, wSection(2, wVec(imports))...)
	}
	out = append(out, wSection(3, wVec(funcs))...)
	if !m.noMem && !m.impMem {
		out = append(out, wSection(5, wVec([][]byte{{0x00, 0x02}}))...) // 2 pages, no maximum
	}
	out = append(out, wSection(7, wVec(exports))...)
	out = append(out, wSection(10, wVec(codes))...)
	if len(m.data) > 0 {
		seg := append([]byte{0x00, 0x41}, sleb(2048)...)
		seg = append(seg, 0x0b)
		seg = append(seg, uleb(uint64(len(m.data)))...)
		seg = append(seg, m.data...)
		out = append(out, wSection(11, wVec([][]byte{seg}))...)
	}
	for name, b := range m.custom {
		out = append(out, wSection(0, append(wName(name), b...))...)
	}
	return out
}

// abiModule is a well-formed plugin module whose sap13ns_handle runs body
// (which must leave an i64 on the stack). respond returns a fixed JSON
// response stored in the data section.
func abiModule(handleBody []byte, data []byte) wModule {
	return wModule{
		funcs: []wFunc{
			{results: []byte{wI32}, body: append([]byte{0x41}, sleb(ABIVersion)...), export: "sap13ns_abi"},
			{params: []byte{wI32}, results: []byte{wI32}, body: append([]byte{0x41}, sleb(8192)...), export: "sap13ns_alloc"},
			{params: []byte{wI32, wI32}, results: []byte{wI64}, body: handleBody, export: "sap13ns_handle"},
		},
		data: data,
	}
}

// respond answers every call with the JSON at the start of the data section.
func respond(json string) wModule { return respondWith(json, nil) }

// respondWith is respond with more bytes in the data section after the JSON.
func respondWith(json string, extra []byte) wModule {
	packed := int64(2048)<<32 | int64(len(json))
	return abiModule(append([]byte{0x42}, sleb(packed)...), append([]byte(json), extra...))
}

var (
	wTrap = []byte{0x00}                               // unreachable
	wLoop = []byte{0x03, 0x40, 0x0c, 0x00, 0x0b, 0x00} // loop br 0 end; unreachable
)

// writePlugin writes plugin.json (unless manifest is empty), plugin.wasm
// (unless wasm is nil) and the extra files into a new directory.
func writePlugin(t *testing.T, manifest string, wasm []byte, extra map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if manifest != "" {
		if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if wasm != nil {
		if err := os.WriteFile(filepath.Join(dir, "plugin.wasm"), wasm, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range extra {
		p := filepath.Join(dir, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}
