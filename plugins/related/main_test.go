package main

import (
	"os"
	"testing"

	"github.com/vicktor/sap13ns/pluginsdk"
)

// TestMain registers the handler, which main.go only does in the wasip1
// build, so sdktest can call it through the host's JSON envelope.
func TestMain(m *testing.M) {
	pluginsdk.Handle(handle)
	os.Exit(m.Run())
}
