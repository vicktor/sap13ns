package main

import (
	"github.com/vicktor/sap13ns/pluginsdk"
)

// logged keeps each settings warning to one line per plugin instance; the
// host creates new instances when the settings change.
var logged = map[string]bool{}

func logOnce(msg string) {
	if msg != "" && !logged[msg] {
		logged[msg] = true
		pluginsdk.Log(pluginsdk.Warn, msg)
	}
}

func handle(req *pluginsdk.Request) (any, error) {
	cfg := Config{Settings: req.Plugin.Settings}
	switch req.Hook {
	case pluginsdk.HookShortcode:
		var sc pluginsdk.Shortcode
		if err := req.Decode(&sc); err != nil || sc.Name != "contact" {
			return nil, nil
		}
		lang := req.Site.Language
		if sc.Post != nil && sc.Post.Lang != "" {
			lang = sc.Post.Lang
		}
		out, warn := FormHTML(cfg, lang)
		logOnce(warn)
		return pluginsdk.HTML(out), nil

	case pluginsdk.HookWidget:
		var w pluginsdk.Widget
		if err := req.Decode(&w); err != nil || w.Widget != "contact" {
			return nil, nil
		}
		lang := req.Site.Language
		if w.Page != nil && w.Page.Lang != "" {
			lang = w.Page.Lang
		}
		out, warn := FormHTML(cfg, lang)
		logOnce(warn)
		return pluginsdk.HTML(out), nil

	case pluginsdk.HookFormSubmit:
		var f pluginsdk.FormSubmission
		if err := req.Decode(&f); err != nil {
			return pluginsdk.FormErrors(texts(req.Site.Language).sendFailed, nil), nil
		}
		if f.Lang == "" {
			f.Lang = req.Site.Language
		}
		return submit(f, cfg, req.Site.Language), nil
	}
	return nil, nil
}

// submit validates a submission and e-mails it. Mail errors are logged
// (without the visitor's data) and shown to the visitor as a retry message.
func submit(f pluginsdk.FormSubmission, cfg Config, siteLang string) any {
	if f.Form != FormID {
		return pluginsdk.FormErrors(texts(f.Lang).sendFailed, nil)
	}
	m, errs := Validate(f, cfg)
	if errs != nil {
		return pluginsdk.FormErrors(texts(f.Lang).checkForm, errs)
	}
	if err := pluginsdk.SendMail(BuildMail(m, cfg, siteLang)); err != nil {
		pluginsdk.Log(pluginsdk.Error, "contact form: the e-mail to "+cfg.Recipient()+" was not sent: "+err.Error())
		return pluginsdk.FormErrors(SendError(err, f.Lang), nil)
	}
	return pluginsdk.FormOK(Success(m, cfg))
}
