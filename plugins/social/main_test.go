package main

import (
	"os"
	"testing"

	"github.com/vicktor/sap13ns/pluginsdk"
)

// main.go registers the handler only in the wasip1 build; the native tests
// register it here so that sdktest runs the real dispatch path.
func TestMain(m *testing.M) {
	pluginsdk.Handle(handle)
	os.Exit(m.Run())
}
