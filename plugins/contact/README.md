# Contact form

A contact form for sap13ns sites. Visitors write their name, e-mail
address, an optional subject and a message, accept the privacy policy, and
the message is e-mailed to the site administrator (or the address set in the
settings) with **Reply-To** set to the visitor, so answering is just
replying.

The plugin sends nothing to the visitor and stores nothing.

## Fields

| Field | Type | Rules |
| --- | --- | --- |
| Name / Nombre | text, required | 2 to 100 characters. Line breaks and control characters become spaces. |
| E-mail / Correo electrónico | email, required | One plain address (`ana@example.org`, no display name, no list), with a dotted domain. |
| Subject (optional) / Asunto (opcional) | text | At most 150 characters, kept on one line. |
| Message / Mensaje | textarea, required | `min_length` (20) to 5000 characters, at most `max_links` (3) links. |
| I have read and accept the privacy policy | checkbox, required | Must be ticked. |

Labels, the submit button ("Send message" / "Enviar mensaje"), the error
messages and the confirmation follow the page's language: `es`, `es-*` and
`es_*` are Spanish, anything else English. When the page has no language, the
site language is used.

## Placing the form

- **Shortcode:** write `[contact]` in a post or page, typically a "Contact"
  page. Other shortcodes are ignored.
- **Widget:** add the "Contact form" widget to a sidebar or footer area in
  Admin → Widgets.

Both show the intro text, a line with the privacy policy link and the form:

```html
<div class="contact-form"><p class="contact-intro">Questions about the site? Write to us.</p><p class="contact-privacy"><small>How we handle your data: <a href="/privacy">Privacy policy</a>.</small></p><div data-form="contact"></div></div>
```

On a Spanish page, with `intro_es` and `privacy_url_es` set:

```html
<div class="contact-form"><p class="contact-intro">¿Dudas sobre el sitio? Escríbenos.</p><p class="contact-privacy"><small>Cómo tratamos tus datos: <a href="/es/privacidad">Política de privacidad</a>.</small></p><div data-form="contact"></div></div>
```

(Real output of the shortcode. The host replaces `<div data-form="contact">`
with the rendered form.) The intro is plain text: HTML in it is shown
escaped, and blank lines separate paragraphs. The privacy link must be an
`https://` or `http://` address or a site path such as `/privacy`. Anything
else (`javascript:`, `//host`) is not shown, and a warning is logged once.

## What the host already does

The form is declared in `plugin.json` and rendered by the host, which
before calling the plugin:

- signs every form with a single-use token;
- adds a hidden honeypot field and refuses submissions that fill it;
- refuses submissions sent less than 3 seconds after the page loaded;
- limits each IP address to 10 submissions in 10 minutes;
- checks the declared fields (required, `max_length`, e-mail syntax) and
  shows the errors next to them.

The plugin then checks the rules in the table above, which the manifest
cannot express. It also refuses messages with too many links, a common sign
of spam. On success the host hides the form and shows the confirmation
message.

## The e-mail

Real e-mail for a Spanish visitor on an English site, with
`subject_prefix` "[Contact]" and `privacy_url_es` "/es/privacidad". Labels
use the site language, because that is the language of whoever reads the
inbox:

```text
To: admin
Reply-To: lucia@example.org
Subject: [Contact] Colaboración

New message from the contact form.

Name: Lucía Pérez
E-mail: lucia@example.org
Subject: Colaboración
Page: https://example.test/es/contacto
Language: es
Privacy consent: yes (https://example.test/es/privacidad)

Message:

Hola, me gustaría proponeros un artículo sobre Go y WebAssembly.

Un saludo.

Reply to this e-mail to answer Lucía Pérez.
```

The host replaces `admin` with the administrator's address and adds a line
saying which plugin sent the e-mail. Without a subject, the subject line is
"[Contact] Message from Lucía Pérez". The visitor saw:

> ¡Gracias, Lucía Pérez! Tu mensaje se ha enviado. Te responderemos a lucia@example.org.

A rejected submission gets the errors next to the fields (real response):

```json
{"ok":false,"message":"Check the highlighted fields.","errors":{"consent":"Accept the privacy policy to send the message.","message":"Too many links (at most 3).","name":"Enter your name (at least 2 characters)."}}
```

The visitor's address is only ever the Reply-To, never the recipient, so the
form cannot be used to send mail to anyone else. Name and subject are
reduced to one line, and both the plugin and the host refuse line breaks in
headers, so no field can add a header such as `Bcc:`.

If the e-mail cannot be queued, the visitor sees "The message could not be
sent. Try again later." (or "Too many messages right now…" when the host's
limit of 20 plugin e-mails per hour is reached), and the plugin logs the
error. The log names the recipient but contains no visitor data.

**In development**, when SMTP is not configured, the server prints each
e-mail in its log ("SMTP not configured; e-mail printed instead of sent")
instead of sending it.

## Permissions

| Permission | Why |
| --- | --- |
| `mail` | To e-mail each message to the administrator or the saved `recipient`. The host only delivers to `admin` or to an address an administrator saved in an `email` setting. |

## Settings

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `recipient` | email | empty | Where messages go. Empty sends them to the site administrator. The address must be saved in this setting by an administrator. Until it is, the host refuses it and the visitor gets the "could not be sent" message. |
| `subject_prefix` | text | `[Contact]` | Starts the subject of every e-mail, for filtering. |
| `privacy_url` | text | empty | Privacy policy link shown above the form and quoted in the e-mail. |
| `privacy_url_es`, `privacy_url_en` | text | empty | Per-language links, e.g. `/es/privacidad`. |
| `intro` | textarea | empty | Plain text above the form. |
| `intro_es`, `intro_en` | textarea | empty | Per-language intro. |
| `success` | text | empty | Shown after sending. `{name}` and `{email}` are replaced. Empty uses "Thanks, {name}! Your message was sent. We will reply to {email}." or its Spanish version. At most 500 bytes are shown. |
| `success_es`, `success_en` | text | empty | Per-language confirmation. |
| `min_length` | number | `20` | Shortest accepted message, in characters (1–5000). |
| `max_links` | number | `3` | Most `http(s)://` or `www.` links in a message. `0` allows none. |

For the per-language settings, the plugin uses `key_es` or `key_en` (by the
page's language), then `key`, then the built-in text.

## Privacy

- **E-mailed:** the name, e-mail address, subject and message the visitor
  typed, the page the form was on, its language and the privacy policy
  link they accepted. Nothing else: no IP address, no browser details.
- **Stored:** nothing. The plugin has no storage permission, and the message
  only exists in the recipient's mailbox (and in the server log in
  development, see above).
- **Consent:** ticking the box is required to send. The only record of it is
  the "Privacy consent: yes" line in the e-mail. Keep the e-mails if you need
  to show consent later.
- The privacy policy itself is up to the site: link it with `privacy_url`.

## Host gaps

- **Delivery is not confirmed.** `mail_send` queues the e-mail and returns
  before it is sent. An SMTP error later only reaches the plugin's log in
  Admin → Plugins, and the visitor has already seen the confirmation. In
  production without SMTP, every message is lost this way.

## Build and install

```sh
make build                               # plugin.wasm (GOOS=wasip1 GOARCH=wasm)
make test vet                            # native unit tests and vet
make install PLUGINS_DIR=/path/to/data/plugins
```

Then open Admin → Plugins, use **Reload** and enable the plugin. Approving
pins the hash of `plugin.wasm` and grants `mail`. Set the privacy policy
link (and a recipient if the administrator's address is not the right
inbox). Then add `[contact]` to a page or place the widget.
