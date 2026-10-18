package main

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/vicktor/sap13ns/pluginsdk"
)

// logged keeps each settings warning to one log line per plugin instance;
// instances are recreated whenever the settings change.
var logged = map[string]bool{}

// handle answers the host. Only content.filter is declared.
func handle(req *pluginsdk.Request) (any, error) {
	if req.Hook != pluginsdk.HookContentFilter {
		return nil, nil
	}
	var c pluginsdk.ContentFilter
	if err := req.Decode(&c); err != nil {
		pluginsdk.Log(pluginsdk.Warn, "invalid content.filter payload: "+err.Error())
		return nil, nil
	}
	lang := req.Site.Language
	if c.Post != nil && c.Post.Lang != "" {
		lang = c.Post.Lang
	}
	opt, warnings := OptionsFrom(req.Plugin.Settings, lang)
	for _, w := range warnings {
		if !logged[w] {
			logged[w] = true
			pluginsdk.Log(pluginsdk.Warn, w)
		}
	}
	out, changed := Filter(c.HTML, opt)
	if !changed {
		return nil, nil // the host keeps the HTML as it was
	}
	return pluginsdk.HTML(out), nil
}

// OptionsFrom reads the plugin settings for content in lang, with warnings
// about values that cannot be used.
func OptionsFrom(settings map[string]string, lang string) (Options, []string) {
	es := isSpanish(lang)
	o := Options{Enabled: map[string]bool{}, Spanish: es}
	for _, p := range providers {
		// Settings missing from the request (an older manifest) count as on.
		o.Enabled[p.id] = strings.TrimSpace(settings[p.id]) != "false"
	}
	o.Notice = localized(settings, "notice", es)
	if o.Notice == "" {
		o.Notice = "Nothing is loaded from {provider} until you press play. Playing the video connects to {provider}, which may collect data about your visit."
		if es {
			o.Notice = "No se carga nada de {provider} hasta que pulsas reproducir. Al reproducir el vídeo te conectas a {provider}, que puede recoger datos de tu visita."
		}
	}
	var warnings []string
	if u := localized(settings, "privacy_url", es); safeURL(u) {
		o.PrivacyURL = u
	} else if u != "" {
		warnings = append(warnings, "privacy URL "+strconv.Quote(u)+" is not an http(s) URL or a site path; ignored")
	}
	return o, warnings
}

// localized reads key_es / key_en, then key (the announcement pattern).
func localized(settings map[string]string, key string, spanish bool) string {
	suffix := "_en"
	if spanish {
		suffix = "_es"
	}
	if v := strings.TrimSpace(settings[key+suffix]); v != "" {
		return v
	}
	return strings.TrimSpace(settings[key])
}

func isSpanish(lang string) bool {
	lang = strings.ToLower(strings.TrimSpace(lang))
	return lang == "es" || strings.HasPrefix(lang, "es-") || strings.HasPrefix(lang, "es_")
}

// safeURL accepts absolute http(s) URLs and site paths ("/privacy").
func safeURL(raw string) bool {
	if raw == "" || strings.ContainsAny(raw, " \t\r\n\x00\"<>\\") {
		return false
	}
	if strings.HasPrefix(raw, "/") {
		return !strings.HasPrefix(raw, "//")
	}
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil
}
