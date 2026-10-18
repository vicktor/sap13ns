// Package abi connects the SDK to whatever carries its host calls: the
// sap13ns imports when the plugin runs as WebAssembly, or the fakes of
// pluginsdk/sdktest when its tests run natively. It is internal so that
// plugins only ever see the typed API of package pluginsdk.
package abi

// Result codes shared with the host (negative values). Non-negative values
// returned by the host are lengths of a pending result.
const (
	CodeNotFound    int32 = -1
	CodeDenied      int32 = -2
	CodeInvalid     int32 = -3
	CodeQuota       int32 = -4
	CodeFailed      int32 = -5
	CodeLimit       int32 = -6
	CodeRateLimited int32 = -7
	CodeUnavailable int32 = -8 // SDK only: no host (native build without sdktest)
)

// Host is the raw host interface. Payloads are the same JSON documents the
// sap13ns host functions exchange, so fakes behave like the real host.
type Host interface {
	Log(level uint32, msg []byte)
	KVGet(key []byte) ([]byte, int32)
	KVSet(key, value []byte) int32
	KVDelete(key []byte) int32
	PostsQuery(query []byte) ([]byte, int32)
	HTTPFetch(request []byte) ([]byte, int32)
	MailSend(message []byte) int32
}

// Current is the host used by the SDK. The wasm glue sets it at start-up;
// sdktest swaps it for fakes.
var Current Host

// Dispatch decodes one request envelope, runs the plugin handler and
// encodes its response. pluginsdk.Handle sets it.
var Dispatch func(in []byte) []byte
