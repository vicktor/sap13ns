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
	tables []wasmTable     // every table, imported or defined
	// tableSec is where the table section sits in the module, [start, end)
	// including its id and size (0, 0 when there is none).
	tableSec [2]int
}

type wasmTable struct {
	ref      byte // reference type
	min, max uint64
	hasMax   bool
	imported bool
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
		start := i
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
				out.tables = append(out.tables, r.table())
			}
			if r.err != nil || len(r.b) > 0 {
				return nil, errors.New("malformed table section")
			}
			out.tableSec = [2]int{start, i}
		case 11:
			out.data = append(out.data, payload...)
		}
	}
	return out, nil
}

// Tables live outside the memory limit: they are allocated in full when a
// module is instantiated, and table.grow can extend one without a maximum
// to 2^32 entries (32 GiB). Go programs need one table of about one entry
// per function and never grow it.
const (
	maxTables       = 4
	maxTableEntries = 1 << 18
)

// size is the most entries the table can hold once pinTables has run.
func (t wasmTable) size() uint64 {
	if t.hasMax {
		return t.max
	}
	return t.min
}

// checkTables refuses modules whose tables could grow too large to
// allocate, counting each table at its maximum (its minimum when it has
// none, as pinTables fixes it there).
func (w *wasmParts) checkTables() error {
	if len(w.tables) > maxTables {
		return fmt.Errorf("declares %d tables (at most %d)", len(w.tables), maxTables)
	}
	var total uint64
	for _, t := range w.tables {
		if t.imported {
			return errors.New("imports a table (the host provides none)")
		}
		total += t.size()
	}
	if total > maxTableEntries {
		return fmt.Errorf("tables can hold %d entries (at most %d)", total, maxTableEntries)
	}
	return nil
}

// pinTables gives every table without a maximum one equal to its minimum,
// so table.grow cannot extend it. It returns b unchanged when every table
// already has a maximum.
func (w *wasmParts) pinTables(b []byte) []byte {
	pin := false
	for _, t := range w.tables {
		pin = pin || !t.hasMax
	}
	if !pin {
		return b
	}
	payload := binary.AppendUvarint(nil, uint64(len(w.tables)))
	for _, t := range w.tables {
		payload = append(payload, t.ref, 0x01)
		payload = binary.AppendUvarint(payload, t.min)
		payload = binary.AppendUvarint(payload, t.size())
	}
	out := make([]byte, 0, len(b)+len(w.tables)*binary.MaxVarintLen64)
	out = append(out, b[:w.tableSec[0]]...)
	out = append(out, 4)
	out = binary.AppendUvarint(out, uint64(len(payload)))
	out = append(out, payload...)
	return append(out, b[w.tableSec[1]:]...)
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
			t := r.table()
			t.imported = true
			w.tables = append(w.tables, t)
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

// limits reads the minimum and the optional maximum. Flags 0x02 and 0x03
// are shared memories.
func (r *wasmReader) limits() (min, max uint64, hasMax bool) {
	flag := r.byte()
	if flag > 0x03 {
		r.err = errors.New("unknown limits")
		return 0, 0, false
	}
	min = r.uint()
	if flag&0x01 != 0 {
		max, hasMax = r.uint(), true
	}
	return min, max, hasMax
}

// table reads a table type: a reference type, then limits that cannot be
// shared.
func (r *wasmReader) table() wasmTable {
	t := wasmTable{ref: r.byte()}
	if len(r.b) > 0 && r.b[0] > 0x01 {
		r.err = errors.New("unknown table limits")
		return t
	}
	t.min, t.max, t.hasMax = r.limits()
	if t.hasMax && t.max < t.min {
		r.err = errors.New("table maximum below its minimum")
	}
	return t
}
