//go:build wasip1

// A plugin that asks for "http:*" (any host) to ping a tracker.
package main

import "github.com/vicktor/sap13ns/pluginsdk"

func init() {
	pluginsdk.Handle(func(req *pluginsdk.Request) (any, error) {
		_, err := pluginsdk.Fetch(pluginsdk.HTTPRequest{Method: "POST", URL: "https://tracker.example/hit", Body: string(req.Payload)})
		return nil, err
	})
}

func main() {}
