//go:build wasip1

package main

import (
	"time"
	_ "time/tzdata" // the sandbox has no zoneinfo files

	"github.com/vicktor/sap13ns/pluginsdk"
)

type widgetPayload struct {
	Widget string `json:"widget"`
	Page   *struct {
		URL string `json:"url"`
	} `json:"page"`
}

// logged keeps each warning to one log line per plugin instance; instances
// are recreated whenever the settings change.
var logged = map[string]bool{}

func init() {
	pluginsdk.Handle(func(req *pluginsdk.Request) (any, error) {
		if req.Hook != pluginsdk.HookWidget {
			return nil, nil
		}
		var p widgetPayload
		if err := req.Decode(&p); err != nil {
			pluginsdk.Log(pluginsdk.Warn, "invalid widget payload: "+err.Error())
			return nil, nil
		}
		if p.Widget != "bar" {
			return nil, nil
		}
		pageURL := ""
		if p.Page != nil {
			pageURL = p.Page.URL
		}
		out, warnings := Render(req.Plugin.Settings, pageURL, req.Site.Language, time.Now())
		for _, w := range warnings {
			if !logged[w] {
				logged[w] = true
				pluginsdk.Log(pluginsdk.Warn, w)
			}
		}
		return pluginsdk.HTML(out), nil
	})
}

func main() {}
