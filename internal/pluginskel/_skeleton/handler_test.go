package main

// Tests run natively with "go test ./...": sdktest calls the handler
// registered in init() through the same JSON envelope the host sends, and
// fakes the host functions (KV, posts, fetch, mail) with the host's rules.
// Each feature has its own _test.go file next to it.

import (
	"testing"

	"github.com/vicktor/sap13ns/pluginsdk/sdktest"
)

func TestUnknownHookIsIgnored(t *testing.T) {
	env := sdktest.New(t)
	resp, err := env.Call("some.future.hook", map[string]string{})
	if err != nil || resp.HTML != "" {
		t.Fatalf("got %+v, %v", resp, err)
	}
}
