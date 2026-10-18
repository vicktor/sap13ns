package plugin

import (
	"bytes"
	"encoding/binary"
	"errors"
)

// wasmParts are the sections of a module the scanner inspects as raw
// bytes. wazero validates everything else when it compiles the module.
type wasmParts struct {
	data   []byte          // payload of the data section (segment headers included)
	custom []customSection // custom sections ("name", "producers", ...)
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
		case 11:
			out.data = append(out.data, payload...)
		}
	}
	return out, nil
}
