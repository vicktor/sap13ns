package main

import (
	"strings"
	"testing"

	"github.com/vicktor/sap13ns/pluginsdk"
	"github.com/vicktor/sap13ns/pluginsdk/sdktest"
)

func TestContactForm(t *testing.T) {
	env := sdktest.New(t)
	fields := map[string]string{"name": "Ada", "email": "ada@example.org", "message": "Hello, I have a question."}
	res, err := env.SubmitForm("contact", fields)
	if err != nil || !res.OK {
		t.Fatalf("got %+v, %v", res, err)
	}
	if len(env.Mails) != 1 || env.Mails[0].To != pluginsdk.MailToAdmin || env.Mails[0].ReplyTo != "ada@example.org" {
		t.Fatalf("mails: %+v", env.Mails)
	}

	fields["message"] = "short"
	res, _ = env.SubmitForm("contact", fields)
	if res.OK || res.Errors["message"] == "" {
		t.Fatalf("short message accepted: %+v", res)
	}
}

func TestContactNoOpenRelay(t *testing.T) {
	env := sdktest.New(t)
	// copy_to must be saved by an administrator; the fake host, like the
	// real one, refuses any other recipient.
	env.Plugin.Settings["copy_to"] = "attacker@example.net"
	res, _ := env.SubmitForm("contact", map[string]string{"name": "x", "email": "x@example.org", "message": "0123456789 spam"})
	if res.OK || len(env.Mails) != 0 {
		t.Fatalf("mail to an unlisted address was sent: %+v", env.Mails)
	}
	env.MailRecipients = []string{"attacker@example.net"} // now it is a saved "email" setting
	if res, _ := env.SubmitForm("contact", map[string]string{"name": "x", "email": "x@example.org", "message": "0123456789 fine"}); !res.OK {
		t.Fatalf("listed recipient refused: %+v", res)
	}
}

func TestContactPlaceholder(t *testing.T) {
	env := sdktest.New(t)
	html, err := env.Shortcode("contact", nil, "")
	if err != nil || !strings.Contains(html, `data-form="contact"`) {
		t.Fatalf("got %q, %v", html, err)
	}
}
