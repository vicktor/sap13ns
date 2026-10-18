package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/vicktor/sap13ns/pluginsdk"
)

func valid() map[string]string {
	return map[string]string{"name": "Ada Lovelace", "email": "ada@example.org", "subject": "Engines",
		"message": "I would like to know more about the analytical engine.", "consent": "true"}
}

func with(f map[string]string, kv ...string) map[string]string {
	out := map[string]string{}
	for k, v := range f {
		out[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i]] = kv[i+1]
	}
	return out
}

func TestValidate(t *testing.T) {
	cfg := Config{Settings: map[string]string{"min_length": "20", "max_links": "3"}}
	for _, tc := range []struct {
		name   string
		fields map[string]string
		lang   string
		errs   map[string]string // field -> substring of the error
	}{
		{"valid", valid(), "en", nil},
		{"valid without subject", with(valid(), "subject", ""), "en", nil},
		{"short name", with(valid(), "name", " A "), "en", map[string]string{"name": "at least 2"}},
		{"long name", with(valid(), "name", strings.Repeat("n", 101)), "en", map[string]string{"name": "too long"}},
		{"spanish short name", with(valid(), "name", "A"), "es", map[string]string{"name": "al menos 2"}},
		{"bad e-mail", with(valid(), "email", "ada@"), "en", map[string]string{"email": "valid e-mail"}},
		{"e-mail without a dotted domain", with(valid(), "email", "ada@localhost"), "en", map[string]string{"email": "valid e-mail"}},
		{"e-mail with a header", with(valid(), "email", "ada@example.org\r\nBcc: x@example.net"), "en", map[string]string{"email": "valid e-mail"}},
		{"e-mail with a display name", with(valid(), "email", "Ada <ada@example.org>"), "en", map[string]string{"email": "valid e-mail"}},
		{"two addresses", with(valid(), "email", "ada@example.org, eve@example.net"), "es", map[string]string{"email": "correo válida"}},
		{"long subject", with(valid(), "subject", strings.Repeat("s", 151)), "en", map[string]string{"subject": "150"}},
		{"short message", with(valid(), "message", "Hi there"), "en", map[string]string{"message": "at least 20"}},
		{"spanish short message", with(valid(), "message", "Hola"), "es-ES", map[string]string{"message": "al menos 20"}},
		{"long message", with(valid(), "message", strings.Repeat("m", 5001)), "en", map[string]string{"message": "too long"}},
		{"too many links", with(valid(), "message", "See https://a.test http://b.test www.c.test and https://www.d.test now"), "en", map[string]string{"message": "at most 3"}},
		{"three links are fine", with(valid(), "message", "See https://a.test http://b.test and https://www.d.test now"), "en", nil},
		{"no consent", with(valid(), "consent", "false"), "en", map[string]string{"consent": "privacy policy"}},
		{"missing consent, spanish", with(valid(), "consent", ""), "es", map[string]string{"consent": "política de privacidad"}},
		{"several errors", map[string]string{"name": "", "email": "", "message": "", "consent": ""}, "en",
			map[string]string{"name": "", "email": "", "message": "", "consent": ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, errs := Validate(pluginsdk.FormSubmission{Form: "contact", Fields: tc.fields, Lang: tc.lang, PageURL: "https://example.test/contact"}, cfg)
			if len(errs) != len(tc.errs) {
				t.Fatalf("errors = %v, want fields %v", errs, tc.errs)
			}
			for k, sub := range tc.errs {
				if !strings.Contains(errs[k], sub) {
					t.Errorf("%s: %q does not mention %q", k, errs[k], sub)
				}
			}
			if tc.errs == nil && (m.Name == "" || m.Email == "") {
				t.Errorf("message = %+v", m)
			}
		})
	}
}

func TestValidateNoLinksAllowed(t *testing.T) {
	cfg := Config{Settings: map[string]string{"max_links": "0", "min_length": "5"}}
	_, errs := Validate(pluginsdk.FormSubmission{Fields: with(valid(), "message", "visit www.spam.test today"), Lang: "es"}, cfg)
	if errs["message"] != "El mensaje no puede contener enlaces." {
		t.Errorf("errors = %v", errs)
	}
}

func TestConfigNumbers(t *testing.T) {
	for _, tc := range []struct {
		settings        map[string]string
		minLen, maxLink int
	}{
		{nil, 20, 3},
		{map[string]string{"min_length": "1", "max_links": "0"}, 1, 0},
		{map[string]string{"min_length": "0", "max_links": "-1"}, 20, 3},
		{map[string]string{"min_length": "x", "max_links": "2.5"}, 20, 3},
		{map[string]string{"min_length": "99999"}, 5000, 3},
	} {
		c := Config{Settings: tc.settings}
		if c.MinLength() != tc.minLen || c.MaxLinks() != tc.maxLink {
			t.Errorf("%v: min %d links %d", tc.settings, c.MinLength(), c.MaxLinks())
		}
	}
}

func TestCountLinks(t *testing.T) {
	for in, want := range map[string]int{
		"":                                  0,
		"no links here":                     0,
		"https://a.test":                    1,
		"HTTPS://WWW.A.TEST and www.b.test": 2,
		"http://a.test/x?y=https://b.test":  2,
		"email me at a.www@b.test":          0,
		"www.a.test, www.b.test www.c.test": 3,
	} {
		if got := CountLinks(in); got != want {
			t.Errorf("CountLinks(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestBuildMail(t *testing.T) {
	cfg := Config{Settings: map[string]string{"subject_prefix": "[Contact]", "privacy_url": "/privacy", "privacy_url_es": "/es/privacidad"}}
	m := Message{Name: "Ada", Email: "ada@example.org", Subject: "Engines", Body: "Line one\nLine two", PageURL: "https://example.test/es/contacto", Lang: "es"}
	mail := BuildMail(m, cfg, "en")
	if mail.To != pluginsdk.MailToAdmin || mail.ReplyTo != "ada@example.org" || mail.Subject != "[Contact] Engines" {
		t.Errorf("mail = %+v", mail)
	}
	for _, want := range []string{"Name: Ada\n", "E-mail: ada@example.org\n", "Subject: Engines\n", "Page: https://example.test/es/contacto\n",
		"Language: es\n", "Privacy consent: yes (https://example.test/es/privacidad)\n", "Message:\n\nLine one\nLine two\n", "Reply to this e-mail to answer Ada."} {
		if !strings.Contains(mail.Body, want) {
			t.Errorf("body lacks %q:\n%s", want, mail.Body)
		}
	}
	// Spanish inbox, no subject, no privacy link.
	m.Subject = ""
	mail = BuildMail(m, Config{Settings: map[string]string{"subject_prefix": "[Web]", "recipient": "info@example.test"}}, "es")
	if mail.To != "info@example.test" || mail.Subject != "[Web] Mensaje de Ada" || !strings.Contains(mail.Body, "Consentimiento de privacidad: sí\n") ||
		strings.Contains(mail.Body, "Asunto:") {
		t.Errorf("spanish mail = %+v\n%s", mail, mail.Body)
	}
}

func TestBuildMailHeaders(t *testing.T) {
	m := Message{Name: "Eve\r\nBcc: victim@example.net", Email: "eve@example.org", Subject: "Hi\nBcc: x@example.net",
		Body: "Body text that is long enough.", Lang: "en"}
	// Validate cleans the name and subject; BuildMail must hold even on raw input.
	mail := BuildMail(m, Config{Settings: map[string]string{"subject_prefix": "[C]\r\nX-Evil: 1", "recipient": "a@example.test\r\nb@example.net"}}, "en")
	for _, v := range []string{mail.Subject, mail.To, mail.ReplyTo} {
		if strings.ContainsAny(v, "\r\n") {
			t.Errorf("header value with a line break: %q", v)
		}
	}
	if mail.To != pluginsdk.MailToAdmin {
		t.Errorf("a recipient with a line break must not be used: %q", mail.To)
	}
	clean, errs := Validate(pluginsdk.FormSubmission{Fields: map[string]string{"name": "Eve\r\nBcc: v@example.net", "email": "eve@example.org",
		"subject": "Hi\u2028Bcc: x", "message": "A long enough message body.", "consent": "true"}, Lang: "en"}, Config{})
	if errs != nil || clean.Name != "Eve Bcc: v@example.net" || clean.Subject != "Hi Bcc: x" {
		t.Errorf("clean = %+v, %v", clean, errs)
	}
}

func TestSubjectLength(t *testing.T) {
	m := Message{Name: "Ñandú", Email: "n@example.org", Subject: strings.Repeat("ñ", 150), Body: "x", Lang: "es"}
	mail := BuildMail(m, Config{Settings: map[string]string{"subject_prefix": "[Contacto]"}}, "es")
	if len(mail.Subject) > 200 || !strings.HasSuffix(mail.Subject, "…") || !strings.HasPrefix(mail.Subject, "[Contacto] ñ") {
		t.Errorf("subject of %d bytes: %q", len(mail.Subject), mail.Subject)
	}
}

func TestSuccess(t *testing.T) {
	m := Message{Name: "Ada", Email: "ada@example.org", Lang: "en"}
	for _, tc := range []struct {
		settings map[string]string
		lang     string
		want     string
	}{
		{nil, "en", "Thanks, Ada! Your message was sent. We will reply to ada@example.org."},
		{nil, "es", "¡Gracias, Ada! Tu mensaje se ha enviado. Te responderemos a ada@example.org."},
		{map[string]string{"success": "Got it, {name}."}, "es", "Got it, Ada."},
		{map[string]string{"success": "Got it.", "success_es": "Recibido, {name}."}, "es", "Recibido, Ada."},
		{map[string]string{"success_es": "Recibido."}, "en", "Thanks, Ada! Your message was sent. We will reply to ada@example.org."},
		{map[string]string{"success": strings.Repeat("é", 400)}, "en", strings.Repeat("é", 248) + "…"},
	} {
		m.Lang = tc.lang
		if got := Success(m, Config{Settings: tc.settings}); got != tc.want {
			t.Errorf("Success(%v, %s) = %q, want %q", tc.settings, tc.lang, got, tc.want)
		}
	}
}

func TestSendError(t *testing.T) {
	for _, tc := range []struct {
		err  error
		lang string
		want string
	}{
		{pluginsdk.ErrRateLimited, "en", "Too many messages right now. Try again later."},
		{pluginsdk.ErrRateLimited, "es", "Ahora mismo hay demasiados mensajes. Inténtalo más tarde."},
		{pluginsdk.ErrLimit, "en", "Too many messages right now. Try again later."},
		{pluginsdk.ErrDenied, "en", "The message could not be sent. Try again later."},
		{errors.New("boom"), "es", "No se pudo enviar el mensaje. Inténtalo más tarde."},
	} {
		if got := SendError(tc.err, tc.lang); got != tc.want {
			t.Errorf("SendError(%v, %s) = %q", tc.err, tc.lang, got)
		}
	}
}

func TestFormHTML(t *testing.T) {
	for _, tc := range []struct {
		name     string
		settings map[string]string
		lang     string
		want     string
		warn     bool
	}{
		{"bare", nil, "en", `<div class="contact-form"><div data-form="contact"></div></div>`, false},
		{"intro and privacy", map[string]string{"intro": "Write to us.\n\nWe answer <b>fast</b> & well.", "privacy_url": "/privacy"}, "en",
			`<div class="contact-form"><p class="contact-intro">Write to us.</p><p class="contact-intro">We answer &lt;b&gt;fast&lt;/b&gt; &amp; well.</p>` +
				`<p class="contact-privacy"><small>How we handle your data: <a href="/privacy">Privacy policy</a>.</small></p><div data-form="contact"></div></div>`, false},
		{"spanish settings", map[string]string{"intro": "Hi", "intro_es": "Escríbenos.", "privacy_url": "/privacy", "privacy_url_es": "https://example.test/es/privacidad?x=1&y=2"}, "es",
			`<div class="contact-form"><p class="contact-intro">Escríbenos.</p>` +
				`<p class="contact-privacy"><small>Cómo tratamos tus datos: <a href="https://example.test/es/privacidad?x=1&amp;y=2">Política de privacidad</a>.</small></p><div data-form="contact"></div></div>`, false},
		{"unsafe privacy link", map[string]string{"privacy_url": "javascript:alert(1)"}, "en",
			`<div class="contact-form"><div data-form="contact"></div></div>`, true},
		{"protocol-relative link", map[string]string{"privacy_url": "//evil.test/x"}, "en",
			`<div class="contact-form"><div data-form="contact"></div></div>`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, warn := FormHTML(Config{Settings: tc.settings}, tc.lang)
			if got != tc.want {
				t.Errorf("got\n%s\nwant\n%s", got, tc.want)
			}
			if (warn != "") != tc.warn {
				t.Errorf("warn = %q", warn)
			}
		})
	}
}

func TestSafeLink(t *testing.T) {
	for in, want := range map[string]bool{
		"https://example.test/privacy": true,
		"http://example.test/p":        true,
		"/privacy":                     true,
		"/es/privacidad#datos":         true,
		"https://":                     false,
		"//evil.test":                  false,
		"/\\evil.test":                 false,
		"privacy":                      false,
		"mailto:a@b.test":              false,
		"javascript:alert(1)":          false,
		"/a b":                         false,
		`/a"onmouseover="x`:            false,
	} {
		if got := safeLink(in); got != want {
			t.Errorf("safeLink(%q) = %v", in, got)
		}
	}
}

func TestAbsolute(t *testing.T) {
	for _, tc := range []struct{ link, page, want string }{
		{"/privacy", "https://example.test/es/contacto", "https://example.test/privacy"},
		{"/privacy", "https://example.test", "https://example.test/privacy"},
		{"https://other.test/p", "https://example.test/x", "https://other.test/p"},
		{"/privacy", "", "/privacy"},
	} {
		if got := absolute(tc.link, tc.page); got != tc.want {
			t.Errorf("absolute(%q, %q) = %q", tc.link, tc.page, got)
		}
	}
}
