package main

import (
	"encoding/json"
	"testing"

	"github.com/vicktor/sap13ns/pluginsdk"
	"github.com/vicktor/sap13ns/pluginsdk/sdktest"
)

func TestHeadJSONLD(t *testing.T) {
	env := sdktest.New(t)
	resp, err := env.Head(&pluginsdk.Page{Kind: "single", Post: &pluginsdk.Post{Title: "Hi </script>", URL: "https://example.test/blog/hi", Author: "Ada"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Meta) != 1 || resp.Meta[0].Content != "Ada" || len(resp.JSONLD) != 1 {
		t.Fatalf("got %+v", resp)
	}
	var ld map[string]any
	if err := json.Unmarshal(resp.JSONLD[0], &ld); err != nil || ld["headline"] != "Hi </script>" {
		t.Fatalf("JSON-LD %s: %v", resp.JSONLD[0], err) // the host escapes "<" when it renders it
	}
	if resp, _ := env.Head(&pluginsdk.Page{Kind: "home"}); len(resp.Meta)+len(resp.JSONLD) != 0 {
		t.Fatalf("home page: %+v", resp)
	}
}
