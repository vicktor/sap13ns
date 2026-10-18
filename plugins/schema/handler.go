package main

import "github.com/vicktor/sap13ns/pluginsdk"

// logged keeps each warning to one line per plugin instance; the host
// creates new instances when the settings change.
var logged = map[string]bool{}

func logOnce(level pluginsdk.Level, msg string) {
	if !logged[msg] {
		logged[msg] = true
		pluginsdk.Log(level, msg)
	}
}

// handle answers head.meta with one JSON-LD object, built only from the
// page object the host sends.
func handle(req *pluginsdk.Request) (any, error) {
	if req.Hook != pluginsdk.HookHeadMeta {
		return nil, nil
	}
	var page pluginsdk.Page
	if err := req.Decode(&page); err != nil {
		logOnce(pluginsdk.Warn, "invalid head.meta payload: "+err.Error())
		return nil, nil
	}
	cfg, warnings := ConfigFrom(req.Plugin.Settings, req.Site)
	for _, w := range warnings {
		logOnce(pluginsdk.Warn, w)
	}
	ld := Build(page, cfg)
	if ld == nil {
		if page.Kind != "404" && page.URL != "" {
			logOnce(pluginsdk.Warn, "the structured data of "+page.URL+" is too large and was left out")
		}
		return nil, nil
	}
	return pluginsdk.Head(nil, ld), nil
}
