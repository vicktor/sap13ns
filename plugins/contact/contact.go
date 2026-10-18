// The contact plugin is a contact form built on the host's declared forms.
//
// The form itself is declared in plugin.json; the host renders it, adds a
// signed single-use token and a honeypot, rate-limits it, checks the field
// declarations and only then calls form.submit. The plugin places the form
// ([contact] shortcode or widget) with an optional intro and a link to the
// privacy policy, validates the message further and e-mails it to the site
// administrator or the configured address, with Reply-To set to the
// visitor. It never sends mail to the visitor and stores nothing.
//
// This file and text.go are plain Go so they can be tested natively;
// handler.go adapts the SDK and main.go registers it (wasip1 only).
package main

import (
	"fmt"
	"html"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/vicktor/sap13ns/pluginsdk"
)

// FormID is the form declared in plugin.json.
const FormID = "contact"

// Field limits (the host enforces the max_length of plugin.json too).
const (
	minName          = 2
	maxName          = 100
	maxSubject       = 150
	maxMessage       = 5000
	maxSubjectBytes  = 200 // the host refuses longer subject lines
	maxResultBytes   = 500 // the host shows at most 500 bytes
	defaultMinLength = 20
	defaultMaxLinks  = 3
)

// Config holds the plugin settings.
type Config struct {
	Settings map[string]string
}

func (c Config) get(key string) string { return strings.TrimSpace(c.Settings[key]) }

// local returns the per-language value of a text setting: key_<es|en>, then
// key, then "".
func (c Config) local(key, lang string) string {
	if v := c.get(key + "_" + langKey(lang)); v != "" {
		return v
	}
	return c.get(key)
}

// Recipient is the To address: the saved recipient or the administrator.
// The host only accepts an address that an administrator saved in the
// "recipient" setting, so the plugin cannot be turned into a relay.
func (c Config) Recipient() string {
	if r := c.get("recipient"); r != "" && !strings.ContainsAny(r, "\r\n") {
		return r
	}
	return pluginsdk.MailToAdmin
}

// PrivacyURL is the privacy policy link for lang, or "" when none is set
// or the value is not a safe link.
func (c Config) PrivacyURL(lang string) (string, bool) {
	u := c.local("privacy_url", lang)
	if u == "" {
		return "", true
	}
	return u, safeLink(u)
}

func (c Config) number(key string, def, lo, hi int) int {
	n, err := strconv.Atoi(c.get(key))
	if err != nil || n < lo {
		return def
	}
	return min(n, hi)
}

// MinLength is the shortest accepted message, in characters.
func (c Config) MinLength() int { return c.number("min_length", defaultMinLength, 1, maxMessage) }

// MaxLinks is the most links a message may contain.
func (c Config) MaxLinks() int { return c.number("max_links", defaultMaxLinks, 0, 1000) }

// safeLink accepts http(s) URLs and site paths ("/privacy", not "//host").
func safeLink(u string) bool {
	if u == "" || strings.ContainsAny(u, " \t\r\n\x00<>\"'") {
		return false
	}
	l := strings.ToLower(u)
	switch {
	case strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "http://"):
		return len(u) > len("https://")
	case strings.HasPrefix(u, "/"):
		return !strings.HasPrefix(u, "//") && !strings.HasPrefix(u, "/\\")
	}
	return false
}

// FormHTML is the output of the shortcode and the widget: the intro, the
// privacy line and the form placeholder the host replaces with the form.
// warn is set when the privacy link setting could not be used.
func FormHTML(c Config, lang string) (out, warn string) {
	t := texts(lang)
	var b strings.Builder
	b.WriteString(`<div class="contact-form">`)
	for _, p := range paragraphs(c.local("intro", lang)) {
		b.WriteString(`<p class="contact-intro">` + html.EscapeString(p) + `</p>`)
	}
	if u, ok := c.PrivacyURL(lang); u != "" && ok {
		b.WriteString(`<p class="contact-privacy"><small>` + t.privacyLead + ` <a href="` + html.EscapeString(u) + `">` + t.privacyLink + `</a>.</small></p>`)
	} else if u != "" {
		warn = fmt.Sprintf("privacy_url %q is not an http(s) address or a site path; the privacy link is not shown", truncate(u, 80))
	}
	b.WriteString(pluginsdk.FormPlaceholder(FormID))
	b.WriteString(`</div>`)
	return b.String(), warn
}

// paragraphs splits plain text on blank lines; single line breaks are kept
// as spaces.
func paragraphs(s string) []string {
	var out []string
	for _, p := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n\n") {
		if p = strings.Join(strings.Fields(p), " "); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Message is a validated submission.
type Message struct {
	Name, Email, Subject, Body string
	PageURL, Lang              string
}

var linkRe = regexp.MustCompile(`(?i)https?://(?:www\.)?|\bwww\.`)

// CountLinks counts http(s):// and www. links ("https://www." counts once).
func CountLinks(s string) int { return len(linkRe.FindAllStringIndex(s, -1)) }

// Validate checks a submission beyond the host's field declarations. It
// returns the message, or field errors in the visitor's language.
func Validate(f pluginsdk.FormSubmission, c Config) (Message, map[string]string) {
	t := texts(f.Lang)
	errs := map[string]string{}
	m := Message{
		Name:    oneLine(f.Fields["name"]),
		Email:   strings.TrimSpace(f.Fields["email"]),
		Subject: oneLine(f.Fields["subject"]),
		Body:    strings.TrimSpace(strings.ReplaceAll(f.Fields["message"], "\r\n", "\n")),
		PageURL: oneLine(f.PageURL),
		Lang:    f.Lang,
	}
	switch n := utf8.RuneCountInString(m.Name); {
	case n < minName:
		errs["name"] = t.nameShort
	case n > maxName:
		errs["name"] = t.nameLong
	}
	if !validEmail(m.Email) {
		errs["email"] = t.emailInvalid
	}
	if utf8.RuneCountInString(m.Subject) > maxSubject {
		errs["subject"] = t.subjectLong
	}
	switch n := utf8.RuneCountInString(m.Body); {
	case n < c.MinLength():
		errs["message"] = fmt.Sprintf(t.messageShort, c.MinLength())
	case n > maxMessage:
		errs["message"] = t.messageLong
	case CountLinks(m.Body) > c.MaxLinks():
		if c.MaxLinks() == 0 {
			errs["message"] = t.noLinks
		} else {
			errs["message"] = fmt.Sprintf(t.tooManyLinks, c.MaxLinks())
		}
	}
	if f.Fields["consent"] != "true" {
		errs["consent"] = t.consent
	}
	if len(errs) > 0 {
		return Message{}, errs
	}
	return m, nil
}

// validEmail accepts one plain address, as the host does, and nothing that
// could add a header. The domain needs a dot: "ana@localhost" cannot be
// answered from the site's inbox.
func validEmail(s string) bool {
	if s == "" || len(s) > 254 || strings.ContainsAny(s, "\r\n,;<>\"") {
		return false
	}
	a, err := mail.ParseAddress(s)
	at := strings.LastIndexByte(s, '@')
	return err == nil && a.Address == s && at > 0 && strings.Contains(s[at+1:], ".")
}

// oneLine removes control characters (line breaks included) and collapses
// spaces, so a value can never start a new e-mail header.
func oneLine(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(s, ""))
	return strings.Join(strings.Fields(s), " ")
}

// BuildMail turns a message into the e-mail for the recipient. Labels use
// siteLang, the language of whoever reads the inbox. The visitor's address
// is only ever the Reply-To.
func BuildMail(m Message, c Config, siteLang string) pluginsdk.Mail {
	t := texts(siteLang)
	topic := m.Subject
	if topic == "" {
		topic = fmt.Sprintf(t.mailNoSubject, m.Name)
	}
	subject := oneLine(c.get("subject_prefix") + " " + topic)
	if subject == "" {
		subject = topic
	}
	consent := t.mailYes
	if u, ok := c.PrivacyURL(m.Lang); u != "" && ok {
		consent += " (" + absolute(u, m.PageURL) + ")"
	}
	var b strings.Builder
	b.WriteString(t.mailIntro + "\n\n")
	fmt.Fprintf(&b, "%s: %s\n", t.mailName, m.Name)
	fmt.Fprintf(&b, "%s: %s\n", t.mailEmail, m.Email)
	if m.Subject != "" {
		fmt.Fprintf(&b, "%s: %s\n", t.mailSubject, m.Subject)
	}
	if m.PageURL != "" {
		fmt.Fprintf(&b, "%s: %s\n", t.mailPage, m.PageURL)
	}
	if m.Lang != "" {
		fmt.Fprintf(&b, "%s: %s\n", t.mailLang, m.Lang)
	}
	fmt.Fprintf(&b, "%s: %s\n\n", t.mailConsent, consent)
	b.WriteString(t.mailMessage + ":\n\n" + m.Body + "\n\n")
	fmt.Fprintf(&b, t.mailReply+"\n", m.Name)
	return pluginsdk.Mail{To: c.Recipient(), ReplyTo: m.Email, Subject: truncateBytes(subject, maxSubjectBytes), Body: b.String()}
}

// absolute resolves a site path against the page URL's origin, so the
// e-mail carries a clickable link.
func absolute(link, pageURL string) string {
	if !strings.HasPrefix(link, "/") {
		return link
	}
	if i := strings.Index(pageURL, "://"); i > 0 {
		if j := strings.IndexByte(pageURL[i+3:], '/'); j >= 0 {
			return pageURL[:i+3+j] + link
		}
		return pageURL + link
	}
	return link
}

// Success is the message shown after sending, in the visitor's language.
func Success(m Message, c Config) string {
	msg := c.local("success", m.Lang)
	if msg == "" {
		msg = texts(m.Lang).success
	}
	msg = strings.NewReplacer("{name}", m.Name, "{email}", m.Email).Replace(msg)
	return truncateBytes(msg, maxResultBytes)
}

// SendError is the message shown when the e-mail could not be queued.
func SendError(err error, lang string) string {
	t := texts(lang)
	if err == pluginsdk.ErrRateLimited || err == pluginsdk.ErrLimit {
		return t.rateLimited
	}
	return t.sendFailed
}

// truncateBytes shortens s to at most n bytes without splitting a
// character, ending with "…" when it cuts.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimSpace(s[:cut]) + "…"
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}
