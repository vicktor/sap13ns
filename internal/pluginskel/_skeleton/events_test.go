package main

import (
	"testing"

	"github.com/vicktor/sap13ns/pluginsdk"
	"github.com/vicktor/sap13ns/pluginsdk/sdktest"
)

func TestPublishedCounter(t *testing.T) {
	env := sdktest.New(t)
	for range 2 {
		if _, err := env.Event(pluginsdk.EventPostPublished, pluginsdk.Post{Title: "News"}); err != nil {
			t.Fatal(err)
		}
	}
	if got := string(env.KV["published"]); got != "2" {
		t.Fatalf("counter = %q", got)
	}
	if len(env.Logs) != 2 {
		t.Fatalf("logs: %+v", env.Logs)
	}
}
