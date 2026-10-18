package main

import (
	"testing"

	"github.com/vicktor/sap13ns/pluginsdk"
	"github.com/vicktor/sap13ns/pluginsdk/sdktest"
)

func TestCommentLinks(t *testing.T) {
	env := sdktest.New(t)
	env.Plugin.Settings["max_links"] = "1"
	verdict, reason, err := env.CheckComment(pluginsdk.CommentCheck{Content: "see https://a.example and https://b.example"})
	if err != nil || verdict != pluginsdk.VerdictPending || reason != "2 links" {
		t.Fatalf("got %q %q %v", verdict, reason, err)
	}
	if verdict, _, _ := env.CheckComment(pluginsdk.CommentCheck{Content: "Nice post!"}); verdict != pluginsdk.VerdictOK {
		t.Fatalf("plain comment: %q", verdict)
	}
}
