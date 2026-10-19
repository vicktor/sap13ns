//go:build wasip1

// WebAssembly glue: the exports the host calls and the sap13ns imports the
// SDK uses. Only this file depends on the sandbox, so the rest of the
// package (and plugins built on it) also compiles natively for tests.

package pluginsdk

import (
	"unsafe"

	"github.com/vicktor/sap13ns/pluginsdk/internal/abi"
)

func init() { abi.Current = wasmHost{} }

var (
	inputs     = map[uintptr][]byte{}
	lastOutput []byte
)

//go:wasmexport sap13ns_abi
func abiVersion() int32 { return ABIVersion }

//go:wasmexport sap13ns_alloc
func alloc(size uint32) uint32 {
	buf := make([]byte, size)
	ptr := uintptr(unsafe.Pointer(unsafe.SliceData(buf)))
	inputs[ptr] = buf // keep alive until sap13ns_handle consumes it
	return uint32(ptr)
}

//go:wasmexport sap13ns_handle
func handle(ptr, size uint32) uint64 {
	buf := inputs[uintptr(ptr)]
	delete(inputs, uintptr(ptr))
	var out []byte
	switch {
	case abi.Dispatch == nil:
		out = errorJSON("no handler registered")
	case uint32(len(buf)) < size:
		out = errorJSON("invalid input")
	default:
		out = abi.Dispatch(buf[:size])
	}
	lastOutput = out // keep alive until the host has copied it
	p := uintptr(unsafe.Pointer(unsafe.SliceData(out)))
	return uint64(p)<<32 | uint64(len(out))
}

//go:wasmimport sap13ns log
func hostLog(level, ptr, size uint32)

//go:wasmimport sap13ns result
func hostResult(ptr, capacity uint32) int32

//go:wasmimport sap13ns kv_get
func hostKVGet(kptr, klen uint32) int32

//go:wasmimport sap13ns kv_set
func hostKVSet(kptr, klen, vptr, vlen uint32) int32

//go:wasmimport sap13ns kv_delete
func hostKVDelete(kptr, klen uint32) int32

//go:wasmimport sap13ns posts_query
func hostPostsQuery(qptr, qlen uint32) int32

//go:wasmimport sap13ns http_fetch
func hostHTTPFetch(rptr, rlen uint32) int32

//go:wasmimport sap13ns mail_send
func hostMailSend(mptr, mlen uint32) int32

//go:wasmimport sap13ns oauth_token
func hostOAuthToken(pptr, plen uint32) int32

func ptrLen(b []byte) (uint32, uint32) {
	if len(b) == 0 {
		return 0, 0
	}
	return uint32(uintptr(unsafe.Pointer(unsafe.SliceData(b)))), uint32(len(b))
}

// fetchResult copies the pending result of the previous host call.
func fetchResult(n int32) ([]byte, int32) {
	if n < 0 {
		return nil, n
	}
	buf := make([]byte, n)
	if n == 0 {
		return buf, 0
	}
	p, c := ptrLen(buf)
	if got := hostResult(p, c); got != n {
		return nil, abi.CodeFailed
	}
	return buf, 0
}

// wasmHost forwards SDK calls to the sap13ns imports. Unused methods are
// dropped by the linker, so a plugin only imports what it calls; the
// scanner compares those imports with the declared permissions.
type wasmHost struct{}

func (wasmHost) Log(level uint32, msg []byte) {
	p, n := ptrLen(msg)
	hostLog(level, p, n)
}

func (wasmHost) KVGet(key []byte) ([]byte, int32) {
	p, n := ptrLen(key)
	return fetchResult(hostKVGet(p, n))
}

func (wasmHost) KVSet(key, value []byte) int32 {
	kp, kn := ptrLen(key)
	vp, vn := ptrLen(value)
	return hostKVSet(kp, kn, vp, vn)
}

func (wasmHost) KVDelete(key []byte) int32 {
	p, n := ptrLen(key)
	return hostKVDelete(p, n)
}

func (wasmHost) PostsQuery(query []byte) ([]byte, int32) {
	p, n := ptrLen(query)
	return fetchResult(hostPostsQuery(p, n))
}

func (wasmHost) HTTPFetch(request []byte) ([]byte, int32) {
	p, n := ptrLen(request)
	return fetchResult(hostHTTPFetch(p, n))
}

func (wasmHost) MailSend(message []byte) int32 {
	p, n := ptrLen(message)
	return hostMailSend(p, n)
}

func (wasmHost) OAuthToken(provider []byte) ([]byte, int32) {
	p, n := ptrLen(provider)
	return fetchResult(hostOAuthToken(p, n))
}
