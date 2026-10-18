package plugin

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

// wasmParts are the sections of a module the scanner inspects as raw
// bytes. wazero validates everything else when it compiles the module.
type wasmParts struct {
	data   []byte          // payload of the data section (segment headers included)
	custom []customSection // custom sections ("name", "producers", ...)
	tables []uint64        // minimum size of every table, imported or defined
}

type customSection struct {
	name string
	data []byte
}

var wasmMagic = []byte{0x00, 'a', 's', 'm', 0x01, 0x00, 0x00, 0x00}

// parseWasm splits a binary module into sections, checking only the framing.
func parseWasm(b []byte) (*wasmParts, error) {
	if !bytes.HasPrefix(b, wasmMagic) {
		return nil, errors.New("not a WebAssembly 1.0 module")
	}
	out := &wasmParts{}
	i := len(wasmMagic)
	for i < len(b) {
		id := b[i]
		i++
		size, n := binary.Uvarint(b[i:])
		if n <= 0 || size > uint64(len(b)-i-n) {
			return nil, errors.New("truncated section")
		}
		i += n
		payload := b[i : i+int(size)]
		i += int(size)
		switch id {
		case 0:
			nameLen, n := binary.Uvarint(payload)
			if n <= 0 || nameLen > uint64(len(payload)-n) {
				return nil, errors.New("malformed custom section")
			}
			out.custom = append(out.custom, customSection{name: string(payload[n : n+int(nameLen)]), data: payload[n+int(nameLen):]})
		case 2:
			if err := out.readImports(payload); err != nil {
				return nil, err
			}
		case 4:
			r := wasmReader{b: payload}
			for n := r.uint(); n > 0 && r.err == nil; n-- {
				r.byte() // reference type
				out.tables = append(out.tables, r.limits())
			}
			if r.err != nil {
				return nil, errors.New("malformed table section")
			}
		case 11:
			out.data = append(out.data, payload...)
		}
	}
	return out, nil
}

// Tables are allocated in full when a module is instantiated, outside the
// memory limit: one table of 2^27 entries costs about 1 GiB. Go programs
// need one table of about one entry per function.
const (
	maxTables       = 4
	maxTableEntries = 1 << 18
)

// checkTables refuses modules whose tables would be too large to allocate.
func (w *wasmParts) checkTables() error {
	if len(w.tables) > maxTables {
		return fmt.Errorf("declares %d tables (at most %d)", len(w.tables), maxTables)
	}
	var total uint64
	for _, t := range w.tables {
		total += t
	}
	if total > maxTableEntries {
		return fmt.Errorf("tables need %d entries (at most %d)", total, maxTableEntries)
	}
	return nil
}

func (w *wasmParts) readImports(b []byte) error {
	r := wasmReader{b: b}
	for n := r.uint(); n > 0 && r.err == nil; n-- {
		r.name()
		r.name()
		switch r.byte() {
		case 0x00: // function: type index
			r.uint()
		case 0x01: // table: reference type, limits
			r.byte()
			w.tables = append(w.tables, r.limits())
		case 0x02: // memory
			r.limits()
		case 0x03: // global: value type, mutability
			r.byte()
			r.byte()
		default:
			r.err = errors.New("unknown import kind")
		}
	}
	if r.err != nil {
		return errors.New("malformed import section")
	}
	return nil
}

// wasmReader reads LEB128 numbers, names and limits, remembering the first
// error.
type wasmReader struct {
	b   []byte
	err error
}

func (r *wasmReader) byte() byte {
	if r.err != nil || len(r.b) == 0 {
		r.err = errors.New("truncated")
		return 0
	}
	c := r.b[0]
	r.b = r.b[1:]
	return c
}

func (r *wasmReader) uint() uint64 {
	if r.err != nil {
		return 0
	}
	v, n := binary.Uvarint(r.b)
	if n <= 0 {
		r.err = errors.New("bad number")
		return 0
	}
	r.b = r.b[n:]
	return v
}

func (r *wasmReader) name() {
	n := r.uint()
	if r.err == nil && n > uint64(len(r.b)) {
		r.err = errors.New("truncated name")
	}
	if r.err == nil {
		r.b = r.b[n:]
	}
}

// limits returns the minimum. Flags 0x02 and 0x03 are shared memories.
func (r *wasmReader) limits() uint64 {
	flag := r.byte()
	if flag > 0x03 {
		r.err = errors.New("unknown limits")
		return 0
	}
	min := r.uint()
	if flag&0x01 != 0 {
		r.uint()
	}
	return min
}
