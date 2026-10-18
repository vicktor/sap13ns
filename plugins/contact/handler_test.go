package main

import (
	"strings"
	"testing"

	"github.com/vicktor/sap13ns/pluginsdk"
	"github.com/vicktor/sap13ns/pluginsdk/sdktest"
)

func newEnv(t *testing.T) *sdktest.Env {
	env := sdktest.New(t)
	logged = map[string]bool{} // logOnce state of the previous test
	return env
}

func submitAs(t *testing.T, env *sdktest.Env, lang string, fields map[string]string) *sdktest.Response {
	t.Helper()
	r, err := env.Call(pluginsdk.HookFormSubmit, pluginsdk.FormSubmission{Form: "contact", Fields: fields, Lang: lang,
		PageURL: env.Site.URL + "/contact"})
	if err != nil {
		t.Fatalf("form.submit returned an error: %v", err)
	}
	return r
}

func TestSendsToAdmin(t *testing.T) {
	env := newEnv(t)
	res, err := env.SubmitForm("contact", valid())
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Message != "Thanks, Ada Lovelace! Your message was sent. We will reply to ada@example.org." {
		t.Errorf("result = %+v", res)
	}
	if len(env.Mails) != 1 {
		t.Fatalf("mails = %+v", env.Mails)
	}
	m := env.Mails[0]
	if m.To != pluginsdk.MailToAdmin || m.ReplyTo != "ada@example.org" || m.Subject != "Engines" {
		t.Errorf("mail = %+v", m)
	}
	if !strings.Contains(m.Body, "Page: https://example.test/contact\n") || !strings.Contains(m.Body, "analytical engine") {
		t.Errorf("body:\n%s", m.Body)
	}
	if len(env.Logs) != 0 {
		t.Errorf("logs = %+v", env.Logs)
	}
}

func TestSavedRecipient(t *testing.T) {
	env := newEnv(t)
	env.Plugin.Settings = map[string]string{"recipient": "info@example.test", "subject_prefix": "[Contact]"}
	env.MailRecipients = []string{"info@example.test"} // an administrator saved it
	if r := submitAs(t, env, "en", valid()); !r.OK {
		t.Fatalf("response = %s", r.Raw)
	}
	if len(env.Mails) != 1 || env.Mails[0].To != "info@example.test" || env.Mails[0].ReplyTo != "ada@example.org" ||
		env.Mails[0].Subject != "[Contact] Engines" {
		t.Errorf("mails = %+v", env.Mails)
	}
}

func TestUnsavedRecipient(t *testing.T) {
	// A recipient the host does not know as saved is refused, and the
	// visitor gets a retry message, never a silent success.
	env := newEnv(t)
	env.Plugin.Settings = map[string]string{"recipient": "relay@example.net"}
	r := submitAs(t, env, "es", valid())
	if r.OK || r.Message != "No se pudo enviar el mensaje. Inténtalo más tarde." || len(r.Errors) != 0 {
		t.Errorf("response = %s", r.Raw)
	}
	if len(env.Mails) != 0 {
		t.Errorf("mails = %+v", env.Mails)
	}
	if len(env.Logs) != 1 || env.Logs[0].Level != pluginsdk.Error || !strings.Contains(env.Logs[0].Text, "relay@example.net") ||
		strings.Contains(env.Logs[0].Text, "ada@example.org") || strings.Contains(env.Logs[0].Text, "Ada") {
		t.Errorf("logs = %+v", env.Logs)
	}
}

func TestMailDenied(t *testing.T) {
	env := newEnv(t)
	env.Denied["mail"] = true
	r := submitAs(t, env, "en", valid())
	if r.OK || r.Message != "The message could not be sent. Try again later." {
		t.Errorf("response = %s", r.Raw)
	}
}

func TestValidationErrors(t *testing.T) {
	for lang, want := range map[string]string{"en": "Check the highlighted fields.", "es": "Revisa los campos marcados."} {
		t.Run(lang, func(t *testing.T) { // one sdktest.Env per test
			env := newEnv(t)
			r := submitAs(t, env, lang, with(valid(), "email", "ada@example.org\nBcc: x@example.net", "consent", "false"))
			if r.OK || r.Message != want || len(r.Errors) != 2 || r.Errors["email"] == "" || r.Errors["consent"] == "" {
				t.Errorf("response = %s", r.Raw)
			}
			if len(env.Mails) != 0 {
				t.Errorf("mails = %+v", env.Mails)
			}
		})
	}
}

func TestHeaderInjection(t *testing.T) {
	env := newEnv(t)
	fields := with(valid(), "name", "Eve\r\nBcc: victim@example.net", "subject", "Hi\r\nBcc: victim@example.net")
	if r := submitAs(t, env, "en", fields); !r.OK {
		t.Fatalf("response = %s", r.Raw)
	}
	m := env.Mails[0]
	if m.To != pluginsdk.MailToAdmin || m.Subject != "Hi Bcc: victim@example.net" || strings.ContainsAny(m.Subject, "\r\n") {
		t.Errorf("mail = %+v", m)
	}
	if !strings.Contains(m.Body, "Name: Eve Bcc: victim@example.net\n") {
		t.Errorf("body:\n%s", m.Body)
	}
}

func TestSpanishSubmission(t *testing.T) {
	env := newEnv(t)
	env.Plugin.Settings = map[string]string{"success_es": "Recibido, {name}.", "privacy_url_es": "/es/privacidad"}
	r := submitAs(t, env, "es", with(valid(), "subject", ""))
	if !r.OK || r.Message != "Recibido, Ada Lovelace." {
		t.Errorf("response = %s", r.Raw)
	}
	// The inbox reads the site language (English), the visitor wrote in Spanish.
	m := env.Mails[0]
	if m.Subject != "Message from Ada Lovelace" || !strings.Contains(m.Body, "Language: es\n") ||
		!strings.Contains(m.Body, "Privacy consent: yes (https://example.test/es/privacidad)\n") {
		t.Errorf("mail = %+v\n%s", m, m.Body)
	}
}

func TestEmptyLangUsesSite(t *testing.T) {
	env := newEnv(t)
	env.Site.Language = "es"
	r := submitAs(t, env, "", with(valid(), "message", "corto"))
	if r.OK || r.Errors["message"] != "Escribe al menos 20 caracteres." {
		t.Errorf("response = %s", r.Raw)
	}
}

func TestOtherForm(t *testing.T) {
	env := newEnv(t)
	res, err := env.SubmitForm("newsletter", valid())
	if err != nil || res.OK || len(env.Mails) != 0 {
		t.Errorf("result = %+v, %v", res, err)
	}
}

func TestShortcodeAndWidget(t *testing.T) {
	env := newEnv(t)
	env.Plugin.Settings = map[string]string{"intro": `Say <script>hi</script>`, "privacy_url": "/privacy?a=1&b=2"}
	want := `<div class="contact-form"><p class="contact-intro">Say &lt;script&gt;hi&lt;/script&gt;</p>` +
		`<p class="contact-privacy"><small>How we handle your data: <a href="/privacy?a=1&amp;b=2">Privacy policy</a>.</small></p>` +
		`<div data-form="contact"></div></div>`
	sc, err := env.Shortcode("contact", nil, "")
	if err != nil || sc != want {
		t.Errorf("shortcode = %q, %v", sc, err)
	}
	w, err := env.Widget("contact", nil)
	if err != nil || w != want {
		t.Errorf("widget = %q, %v", w, err)
	}
	if other, _ := env.Shortcode("form", nil, ""); other != "" {
		t.Errorf("other shortcode = %q", other)
	}
	if other, _ := env.Widget("search", nil); other != "" {
		t.Errorf("other widget = %q", other)
	}
}

func TestShortcodeLanguage(t *testing.T) {
	env := newEnv(t)
	env.Plugin.Settings = map[string]string{"privacy_url": "/privacy", "privacy_url_es": "/es/privacidad"}
	r, err := env.Call(pluginsdk.HookShortcode, pluginsdk.Shortcode{Name: "contact", Attrs: map[string]string{},
		Post: &pluginsdk.Post{Lang: "es", URL: "https://example.test/es/contacto"}})
	if err != nil || !strings.Contains(r.HTML, `Cómo tratamos tus datos: <a href="/es/privacidad">Política de privacidad</a>`) {
		t.Errorf("html = %q, %v", r.HTML, err)
	}
}

func TestBadPrivacyURLLoggedOnce(t *testing.T) {
	env := newEnv(t)
	env.Plugin.Settings = map[string]string{"privacy_url": "javascript:alert(1)"}
	for range 3 {
		if html, _ := env.Shortcode("contact", nil, ""); strings.Contains(html, "javascript") {
			t.Fatalf("html = %q", html)
		}
	}
	if len(env.Logs) != 1 || env.Logs[0].Level != pluginsdk.Warn || !strings.Contains(env.Logs[0].Text, "privacy_url") {
		t.Errorf("logs = %+v", env.Logs)
	}
}

func TestOtherHooks(t *testing.T) {
	env := newEnv(t)
	r, err := env.Head(&pluginsdk.Page{Kind: "home", URL: "https://example.test/"})
	if err != nil || len(r.JSONLD) != 0 || len(r.Meta) != 0 {
		t.Errorf("head = %s, %v", r.Raw, err)
	}
}
