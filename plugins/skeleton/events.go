package main

import (
	"errors"
	"strconv"

	"github.com/vicktor/sap13ns/pluginsdk"
)

// Events run in the background after something happens on the site. This
// one keeps a counter in the plugin's private key-value store ("kv").
func init() {
	events[pluginsdk.EventPostPublished] = func(req *pluginsdk.Request) error {
		var post pluginsdk.Post
		if err := req.Decode(&post); err != nil {
			return err
		}
		n, err := CountPublished()
		if err != nil {
			return err
		}
		pluginsdk.Log(pluginsdk.Info, "published "+strconv.Quote(post.Title)+"; posts published so far: "+strconv.Itoa(n))
		return nil
	}
}

// CountPublished increments and returns the "published" counter.
func CountPublished() (int, error) {
	n := 0
	raw, err := pluginsdk.KVGet("published")
	switch {
	case errors.Is(err, pluginsdk.ErrNotFound):
	case err != nil:
		return 0, err
	default:
		n, _ = strconv.Atoi(string(raw))
	}
	n++
	return n, pluginsdk.KVSet("published", []byte(strconv.Itoa(n)))
}
