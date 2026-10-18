package main

import (
	"strings"
	"unicode/utf8"

	"github.com/vicktor/sap13ns/pluginsdk"
)

// The contact form is declared in plugin.json ("forms"); the host renders
// it, so a plugin never writes <form> HTML. Place it with the [contact]
// shortcode, which returns the form's placeholder.
func init() {
	shortcodes["contact"] = func(req *pluginsdk.Request, sc pluginsdk.Shortcode) (string, error) {
		return pluginsdk.FormPlaceholder("contact"), nil
	}
	forms["contact"] = func(req *pluginsdk.Request, f pluginsdk.FormSubmission) (any, error) {
		mail, errs := ContactMail(f, req.Setting("copy_to"))
		if errs != nil {
			return pluginsdk.FormErrors(text(f.Lang, "Please check the form.", "Revisa el formulario."), errs), nil
		}
		// SendMail needs the "mail" permission and only reaches the site
		// administrator or addresses saved in this plugin's "email"
		// settings; the visitor's address goes in Reply-To.
		if err := pluginsdk.SendMail(mail); err != nil {
			pluginsdk.Log(pluginsdk.Error, "contact form: "+err.Error())
			return pluginsdk.FormErrors(text(f.Lang, "The message could not be sent. Try again later.",
				"No se pudo enviar el mensaje. Inténtalo más tarde."), nil), nil
		}
		return pluginsdk.FormOK(text(f.Lang, "Thanks! We will get back to you soon.", "¡Gracias! Te responderemos pronto.")), nil
	}
}

// ContactMail validates a submission beyond what the host checks (types,
// lengths, required fields) and builds the e-mail.
func ContactMail(f pluginsdk.FormSubmission, copyTo string) (pluginsdk.Mail, map[string]string) {
	msg := strings.TrimSpace(f.Fields["message"])
	if utf8.RuneCountInString(msg) < 10 {
		return pluginsdk.Mail{}, map[string]string{"message": text(f.Lang, "Write at least 10 characters.", "Escribe al menos 10 caracteres.")}
	}
	to := pluginsdk.MailToAdmin
	if copyTo != "" {
		to = copyTo
	}
	return pluginsdk.Mail{
		To:      to,
		ReplyTo: f.Fields["email"],
		Subject: "Contact form: " + f.Fields["name"],
		Body:    "From: " + f.Fields["name"] + " <" + f.Fields["email"] + ">\nPage: " + f.PageURL + "\n\n" + msg + "\n",
	}, nil
}

// text picks the English or Spanish version of a message.
func text(lang, en, es string) string {
	if lang == "es" {
		return es
	}
	return en
}
