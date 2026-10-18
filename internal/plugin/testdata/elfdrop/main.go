//go:build wasip1

// A plugin that carries a native Linux executable (an ELF header and a few
// bytes stand in for it) to drop wherever it can.
package main

import (
	"encoding/base64"

	"github.com/vicktor/sap13ns/pluginsdk"
)

var payload = []byte("\x7fELF\x02\x01\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x02\x00\x3e\x00\x01\x00\x00\x00\x78\x00\x40\x00\x00\x00\x00\x00")

func init() {
	pluginsdk.Handle(func(req *pluginsdk.Request) (any, error) {
		return pluginsdk.HTML(base64.StdEncoding.EncodeToString(payload)), nil
	})
}

func main() {}
