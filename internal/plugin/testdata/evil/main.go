//go:build wasip1

// A hostile plugin used by the sandbox tests. Each shortcode tries to escape.
package main

import (
	"net"
	"os"
	"strings"

	"github.com/vicktor/sap13ns/pluginsdk"
)

func init() {
	pluginsdk.Handle(func(req *pluginsdk.Request) (any, error) {
		switch req.Hook {
		case pluginsdk.HookEvent:
			for _, u := range []string{"https://127.0.0.1/", "https://localhost/", "https://169.254.169.254/latest/meta-data/", "http://example.com/"} {
				_, err := pluginsdk.Fetch(pluginsdk.HTTPRequest{Method: "GET", URL: u})
				pluginsdk.Log(pluginsdk.Info, "fetch "+u+": "+errString(err))
			}
			return nil, nil
		case pluginsdk.HookShortcode:
			var sc pluginsdk.Shortcode
			req.Decode(&sc)
			switch sc.Name {
			case "readfile":
				b, err := os.ReadFile("/etc/passwd")
				return pluginsdk.HTML("read:" + errString(err) + ":" + pluginsdk.Escape(string(b))), nil
			case "env":
				return pluginsdk.HTML("env:" + strings.Join(os.Environ(), ",")), nil
			case "dial":
				_, err := net.Dial("tcp", "127.0.0.1:5432")
				return pluginsdk.HTML("dial:" + errString(err)), nil
			case "loop":
				for {
				}
			case "mem":
				var hog [][]byte
				for i := 0; i < 4096; i++ {
					hog = append(hog, make([]byte, 1<<20))
				}
				return pluginsdk.HTML("allocated " + string(rune(len(hog)))), nil
			case "kv":
				err := pluginsdk.KVSet("k", []byte("v"))
				return pluginsdk.HTML("kv:" + errString(err)), nil
			case "posts":
				_, err := pluginsdk.QueryPosts(pluginsdk.PostsQuery{Limit: 1})
				return pluginsdk.HTML("posts:" + errString(err)), nil
			case "fetch":
				_, err := pluginsdk.Fetch(pluginsdk.HTTPRequest{URL: "https://example.com/"})
				return pluginsdk.HTML("fetch:" + errString(err)), nil
			case "xss":
				return pluginsdk.HTML(`<script>alert(1)</script><img src=x onerror="alert(1)"><a href="javascript:alert(1)">x</a><div class="alert" onclick="x()">ok</div><iframe src="https://evil"></iframe>`), nil
			case "panic":
				panic("boom")
			}
		}
		return nil, nil
	})
}

func errString(err error) string {
	if err == nil {
		return "ok"
	}
	return err.Error()
}

func main() {}
