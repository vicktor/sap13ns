# Plugins

This is the full contract between sap13ns and its plugins: the package format, the manifest, the hooks and their payloads, the host functions, the permissions and limits, the scanner, and the install and approval flow.

- **Writing your first plugin?** Start with the skeleton: `sap13ns plugin new my-id` creates a working project, and its README walks through building, testing and packaging it. This document is the reference behind it.
- **Running a site?** Read [Installing and approving](#installing-and-approving) and [Security model](#security-model).

## Contents

1. [Security model](#security-model)
2. [Package format](#package-format)
3. [The manifest](#the-manifest)
4. [The ABI](#the-abi)
5. [Hooks](#hooks)
6. [Events](#events)
7. [Host functions and permissions](#host-functions-and-permissions)
8. [Limits](#limits)
9. [Output rules](#output-rules)
10. [Forms](#forms)
11. [Assets and the Content-Security-Policy](#assets-and-the-content-security-policy)
12. [Settings](#settings)
13. [The scanner](#the-scanner)
14. [Installing and approving](#installing-and-approving)
15. [Command line](#command-line)
16. [Developing and testing](#developing-and-testing)
17. [Versioning](#versioning)

## Security model

A plugin is untrusted code. The site administrator decides what it may do, and the host enforces that decision on every call.

- **Sandbox.** A plugin is a WebAssembly module run by [wazero](https://wazero.io) inside the sap13ns process.
  - It cannot reach the file system, environment variables, sockets, other processes or the database.
  - Its only imports are a minimal WASI preview 1 (clock, random numbers, stdout and stderr, which go to the plugin's activity log) and the `sap13ns` host functions.
- **Permissions.** Every capability beyond computing on its input is a permission declared in `plugin.json` and granted by an administrator. Host functions check the grant on every call.
- **Pinned approval.** Enabling a plugin pins the SHA-256 of its module, the SHA-256 of its assets and the exact list of permissions (CSP sources included). If any of them changes, the plugin stops until it is approved again.
- **Bounded calls.** Each call runs with a memory cap, a deadline and per-call quotas of host calls. A plugin that fails repeatedly is paused.
- **Validated output.** The host validates and sanitizes everything a plugin returns: HTML, meta tags, JSON-LD, verdicts and form results.
- **Scanned packages.** Uploaded packages are extracted with strict limits and scanned before anything is installed, and they are always installed disabled.

The model assumes the server, its operator and its administrators are trusted. Plugin authors, visitors and anything fetched from the network are not.

## Package format

A package is a `.zip` file, at most **40 MiB**. It holds:

| Path | Required | Limit |
|---|---|---|
| `plugin.json` | yes | 64 KiB |
| `plugin.wasm` (or the name in `"wasm"`) | yes | 32 MiB |
| `assets/…` | no | 2 MiB per file, 8 MiB and 100 files in total; types: `.css .js .svg .png .jpg .jpeg .gif .webp .avif .ico .woff .woff2 .json .txt` |
| `README.md`, `LICENSE`, `LICENSE.md`, `LICENSE.txt`, `CHANGELOG.md` | no (recommended) | 256 KiB each |

- **One top-level folder is accepted.** `hello-1.0/plugin.json` works like `plugin.json`.
- **macOS litter is ignored:** `__MACOSX/` and `.DS_Store`.
- **Everything else refuses the package.** Sources, tests, `Makefile`, hidden files and any other path. `sap13ns plugin pack` writes only the allowed files.

Extraction checks every entry name before writing anything. It writes into a staging directory inside the plugins directory, which is renamed into place only after the scanner accepted the package. These problems refuse the package with a critical finding:

| Rule | Cause |
|---|---|
| `package.invalid` | not a zip, unsupported compression, unreadable entry |
| `package.too-large` | the archive is over 40 MiB |
| `package.too-many-entries` | more than 200 entries |
| `package.zip-slip` | an absolute path, a drive letter, a backslash, or `..` |
| `package.bad-name` | empty segments, control characters, names over 300 bytes |
| `package.hidden-file` | a segment starting with `.` |
| `package.symlink` | a symbolic link |
| `package.special-file` | devices, pipes, sockets, setuid or setgid files |
| `package.duplicate` | two entries with the same name, compared without case |
| `package.disallowed-file` | a path outside the list above |
| `package.too-large-file` | an entry over its limit |
| `package.zip-bomb` | over 48 MiB unpacked, a compression ratio over 100:1 for entries over 1 MiB, or more data than the header declares |
| `package.no-manifest`, `package.no-wasm` | the required files are missing |

## The manifest

`plugin.json` is strict JSON. Unknown fields are an error.

```json
{
  "abi": 1,
  "id": "hello-world",
  "name": "Hello world",
  "version": "1.2.0",
  "description": "Says hello.",
  "author": "Ada Lovelace",
  "homepage": "https://example.com/hello",
  "license": "MIT",
  "wasm": "plugin.wasm",
  "hooks": ["shortcode", "widget", "form.submit"],
  "shortcodes": ["hello", "contact"],
  "widgets": [{"id": "latest", "title": "Latest posts", "description": "The newest posts.",
               "config": [{"key": "count", "label": "Number of posts", "type": "number", "default": "5"}]}],
  "events": [],
  "permissions": ["posts:read", "mail"],
  "settings": [{"key": "greeting", "label": "Greeting", "type": "text", "default": "Hello"}],
  "forms": [{"id": "contact", "title": {"en": "Contact", "es": "Contacto"}, "submit": "Send",
             "fields": [{"name": "email", "label": "E-mail", "type": "email", "required": true}]}],
  "assets": {"css": ["assets/hello.css"], "js": []},
  "csp": {"frame-src": ["https://www.youtube-nocookie.com"]}
}
```

| Field | Rules |
|---|---|
| `abi` | must be `1` |
| `id` | `^[a-z0-9][a-z0-9-]{1,49}$`, the same as the plugin's directory. Reserved: `sap13ns admin api assets core plugin plugins static system theme themes uploads www my-plugin` |
| `name` | required, at most 80 characters |
| `version` | semantic: `1.2.3`, optionally with `-pre.release` or `+build` |
| `description` | at most 500 characters |
| `author`, `homepage`, `license` | shown in the admin. An update by a different `author` is flagged |
| `wasm` | a plain file name ending in `.wasm`; default `plugin.wasm` |
| `hooks` | at least one; see [Hooks](#hooks) |
| `shortcodes` | names `^[a-z][a-z0-9_-]{0,39}$`; required with, and only with, the `shortcode` hook |
| `widgets` | `id` (same pattern as shortcodes), `title` (required), `description`, and `config`: up to 10 fields of the setting types except `secret`; required with the `widget` hook |
| `events` | names from [Events](#events); required with the `event` hook |
| `permissions` | see [permissions](#host-functions-and-permissions) |
| `settings` | up to 50; see [Settings](#settings) |
| `forms` | up to 5; required with the `form.submit` hook; see [Forms](#forms) |
| `assets` | up to 5 `css` and 5 `js` paths, each `assets/….css` or `assets/….js`; JavaScript needs `assets:js` |
| `csp` | `frame-src` and/or `img-src`, 1 to 10 sources each, `https://host` or `https://*.domain` |

Text shown to visitors, such as form titles, submit labels, field labels and placeholders, may be a string or an object by language: `{"en": "Send", "es": "Enviar"}`. The host uses the page language (or its base language: `es` for `es-MX`), then a plain string or `en`, then any other value.

## The ABI

ABI 1 is a small calling convention over linear memory. The Go SDK implements it, so you only need this section to write plugins in another language.

**Exports:**

| Export | Signature | Purpose |
|---|---|---|
| `memory` | memory | the module's own memory; importing one is refused |
| `sap13ns_alloc` | `(size i32) -> ptr i32` | a buffer the host writes the request into |
| `sap13ns_handle` | `(ptr i32, len i32) -> i64` | handles one request and returns `ptr << 32 \| len` of the JSON response |
| `sap13ns_abi` | `() -> i32` | returns `1`. Optional, but the scanner flags modules without it |

Build Go plugins as reactors: `GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared`. A `_initialize` export runs once per instance. `main` never runs.

**The request** is one JSON object:

```json
{
  "abi": 1,
  "hook": "shortcode",
  "event": "",
  "plugin": {"id": "hello-world", "version": "1.2.0", "settings": {"greeting": "Hola"}},
  "site": {"url": "https://example.com", "title": "My site", "language": "es", "timezone": "Europe/Madrid"},
  "payload": {}
}
```

- `plugin.settings` holds the effective values: the defaults merged with what the administrator saved, secrets decrypted, datetimes in RFC 3339.
- `payload` depends on the hook.

**The response** is one JSON object with the fields of the hook (below). On failure it is `{"error": "message"}`. The SDK turns a Go panic into `{"error": "plugin panic"}`.

**Host functions** live in the `sap13ns` import module. Functions that return data put it in a pending buffer and return its length. The plugin then allocates that many bytes and calls `result` to copy it.

| Import | Signature | Returns |
|---|---|---|
| `log` | `(level, ptr, len)` | nothing. Levels: 0 debug, 1 info, 2 warn, 3 error. At most 4 KiB per line |
| `result` | `(ptr, cap) -> i32` | bytes copied; the pending buffer is cleared |
| `kv_get` | `(kptr, klen) -> i32` | length of the value, or `-1` if the key is absent |
| `kv_set` | `(kptr, klen, vptr, vlen) -> i32` | `0` or an error code |
| `kv_delete` | `(kptr, klen) -> i32` | `0` or an error code |
| `posts_query` | `(qptr, qlen) -> i32` | length of a JSON array of posts |
| `http_fetch` | `(rptr, rlen) -> i32` | length of a JSON response |
| `mail_send` | `(ptr, len) -> i32` | `0` or an error code |

Error codes: `-1` not found, `-2` permission denied, `-3` invalid argument, `-4` storage quota exceeded, `-5` host call failed, `-6` too many host calls in this request, `-7` rate limited. The SDK maps them to `ErrNotFound`, `ErrDenied`, `ErrInvalid`, `ErrQuota`, `ErrFailed`, `ErrLimit` and `ErrRateLimited`.

## Hooks

Render hooks run while a page is served. They never get the network or e-mail, and their output is cached with the page.

| Hook | Called | Timeout | Network | E-mail |
|---|---|---|---|---|
| `shortcode` | for each `[name …]` of the plugin in a post or page | 300 ms | no | no |
| `content.filter` | on the rendered HTML of every post and page, plugins in id order | 300 ms | no | no |
| `widget` | for each placed widget of the plugin | 300 ms | no | no |
| `head.meta` | on every public page | 300 ms | no | no |
| `comment.check` | before a new comment is stored (not for moderators) | 2 s for all plugins together | yes | no |
| `form.submit` | when a visitor sends one of the plugin's forms | 5 s | yes | yes |
| `event` | in the background, after something happened | 15 s | yes | yes |

The payload objects below use these shapes:

- **`post`** (a published post or page): `id`, `type` (`post`, `page`), `title`, `slug`, `lang`, `url` (absolute), `excerpt`, `author`, `categories` and `tags` (names), `terms` (`taxonomy`, `slug`, `name`, `url`), `image` (absolute URL of the featured image), `published_at` (RFC 3339) and `word_count`.
- **`page`** (the page being served): `kind` (`home`, `single`, `page`, `archive`, `search`, `404`), `url` (canonical, absolute), `lang` (the language the page is shown in), `title`, `description`, and `post` (when the page shows one).

### shortcode

- **Payload:** `{"name": "hello", "attrs": {"name": "Ada"}, "inner": "text between the tags", "post": {…}}`
  - `inner` is raw text, not HTML.
- **Response:** `{"html": "…"}`
  - The output is sanitized; see [Output rules](#output-rules).
  - Shortcodes inside code blocks are not expanded.
  - If two enabled plugins declare the same shortcode, only one of them gets it. The scanner warns about it.

### content.filter

- **Payload:** `{"html": "…", "post": {…}}`
- **Response:** `{"html": "…"}`
  - The output replaces the HTML and is sanitized again.
  - Form placeholders that were already in the input are kept. A filter can only add its own forms.

### widget

- **Payload:** `{"widget": "latest", "config": {"count": "5"}, "page": {…}}`
  - `config` holds the values saved for this placement in **Appearance → Widgets**.
  - A config field added in a later version arrives with its default.
- **Response:** `{"html": "…"}`

### head.meta

- **Payload:** the `page` object.
- **Response:** `{"meta": [{"name": "author", "content": "Ada"}], "jsonld": [{"@context": "https://schema.org", "@type": "BlogPosting", …}]}`
  - **Meta tags:** up to 20 per plugin. Each needs exactly one of `name` or `property`, matching `^[a-zA-Z][a-zA-Z0-9:_.-]{0,63}$`, with `content` of at most 1000 bytes. `http-equiv` is never allowed.
  - **JSON-LD:** up to 5 objects per plugin. Each must be a JSON object of at most 16 KiB, and a page takes at most 64 KiB of plugin JSON-LD.
  - **How JSON-LD is written:** each object is compacted and written in its own `<script type="application/ld+json">` with `<`, `>` and `&` escaped as `\u003c`, `\u003e` and `\u0026`, so it can never close the element. Invalid objects are dropped and logged.

### comment.check

- **Payload:** `{"author": "…", "url": "…", "content": "…", "post": {…}, "parent_id": 0, "registered": false}`
  - The plugin receives `email`, `ip` and `user_agent` as well only if it was granted `comments:pii`.
- **Response:** `{"verdict": "ok" | "pending" | "spam", "reason": "…"}`
  - **The strictest verdict of all plugins wins.** `pending` holds the comment for moderation; `spam` stores it as spam.
  - **The check fails open.** Errors, timeouts and unknown verdicts count as `ok`, and the site's normal moderation applies.
  - **The budget is shared.** All plugins together get 2 seconds. Plugins that do not get a turn are skipped and logged.
  - **The reason is private.** It is written to the plugin's activity log and the server log, and never shown to the visitor.

### form.submit

- **Payload:** `{"form": "contact", "fields": {"email": "…"}, "lang": "es", "page_url": "https://…"}`
  - `fields` has only the declared fields, already validated (see [Forms](#forms)).
- **Response:** `{"ok": true, "message": "Thanks!"}` or `{"ok": false, "message": "…", "errors": {"email": "…"}}`
  - The message is shown to the visitor, at most 500 bytes.
  - Field errors are kept only for declared fields, at most 200 bytes each.
  - Write messages in the language in `lang`.

### event

- **Payload:** depends on the event. The request also carries `"event": "post.published"`.
- **Response:** ignored, except `error`, which is logged.
- **Delivery:** events are queued (256) and delivered by background workers. When the queue is full, events are dropped and the server logs a warning.

## Events

| Event | Payload |
|---|---|
| `post.published` | the `post` |
| `post.updated` | the `post` |
| `post.trashed` | the `post` |
| `comment.created` | `{"id", "post_id", "post", "author", "status", "content"}` (`status`: `approved`, `pending`, `spam`) |
| `comment.approved` | `{"id", "post_id", "author"}` |
| `user.registered` | `{"username", "role"}` |
| `cron.hourly` | `{"time"}` (RFC 3339, UTC) |

## Host functions and permissions

| Permission | Risk | Grants |
|---|---|---|
| *(none)* | low | computing on the payload, `log`, clock and random numbers |
| `kv` | low | `kv_get`, `kv_set`, `kv_delete` on the plugin's private store |
| `posts:read` | low | `posts_query` over **published** posts and pages only |
| `http:api.example.com` | medium | `http_fetch` to that host (`http:*.example.com` covers its subdomains, not the domain itself) |
| `http:*` | **high** | `http_fetch` to any public host |
| `mail` | medium | `mail_send` to the site administrator or addresses saved in the plugin's `email` settings |
| `comments:pii` | **high** | commenters' e-mail, IP address and browser in `comment.check` |
| `assets:js` | **high** | linking the plugin's JavaScript on every public page |
| `csp:<directive>:<source>` | **high** | derived from `"csp"`; not written in `permissions` |

High-risk permissions need an explicit confirmation when the plugin is enabled.

**Key-value store (`kv`).**

- Keys are 1 to 200 printable ASCII characters. Values are at most 256 KiB.
- Each plugin may store 1 MiB and 1000 keys.
- Data is kept across updates and is deleted only when the plugin is uninstalled with its data.

**Posts (`posts:read`).** The query is JSON:

```json
{"limit": 5, "category": "news", "tag": "go", "search": "text", "type": "post",
 "exclude": 12, "exclude_ids": [3, 4], "lang": "es"}
```

- `limit` is 1 to 20 (default 5).
- `category` and `tag` are slugs; given together, both must match. An unknown slug gives an empty list.
- `type` is `post` (default) or `page`.
- `exclude_ids` takes at most 50 ids.
- `lang` is a language code; empty means every language. Use the page's `lang` to list posts in the reader's language.
- Results are `post` objects. Drafts, private, scheduled and trashed content never appear.

**HTTP (`http:<host>`).**

- **Request:** `{"method": "GET", "url": "https://…", "headers": {…}, "body": "…"}`
- **Response:** `{"status": 200, "headers": {…}, "body": "…", "body_base64": "…", "error": "…"}`
- **Where it is available:** only in `event`, `comment.check` and `form.submit`.
- **What is allowed:**
  - `https://` URLs only, without credentials, to granted hosts;
  - methods `GET`, `HEAD`, `POST`, `PUT`, `PATCH` and `DELETE`;
  - up to 20 request headers, but not `Host`, `Cookie`, `Connection`, forwarding or proxy headers.
- **Limits:** a 256 KiB request body, a 1 MiB response, and a 10 s timeout.
- **What the host adds:** the user agent `sap13ns-plugin/<id>`. Response header names arrive in lower case.
- **Private addresses are never reached.** Loopback, private, link-local, CGNAT and cloud metadata addresses are refused after DNS resolution, whatever the permission says, and every redirect is checked again against the grants.
- **Binary bodies** arrive in `body_base64`.

**E-mail (`mail`).**

- **Request:** `{"to": "admin", "subject": "…", "body": "…", "reply_to": "visitor@example.com"}`
- **Where it is available:** only in `event` and `form.submit`.
- **Recipients:**
  - `"admin"` is the site's oldest active administrator;
  - any other recipient must be an address saved in one of the plugin's `email` settings.
  - There is no open relay: put a visitor's address in `reply_to`, never in `to`.
- **Message:** the subject is one line of at most 200 characters, and the body is plain text of at most 64 KiB.
- **Provenance:** the host appends a footer naming the plugin and the site.
- **Limits:** at most 5 messages per call and 20 per plugin per hour. Delivery is asynchronous.

## Limits

| What | Limit |
|---|---|
| Module | 32 MiB; compiled once and cached |
| Memory | 128 MiB per instance; up to 4 instances per plugin |
| Request | 4 MiB of JSON |
| Response | 2 MiB |
| Host calls per request | 200 KV operations, 10 post queries, 4 HTTP requests, 5 e-mails |
| Timeouts | see [Hooks](#hooks); instantiation 10 s |
| Failures | after 5 failures in a row a plugin is paused for 1 minute |
| Activity log | the latest 200 lines (2000 characters each) are kept in memory and shown on the plugin's page |

## Output rules

All HTML from `shortcode`, `content.filter` and `widget` passes through the plugin sanitizer:

- **What it allows:**
  - the usual content elements, plus `section header footer aside figure figcaption time progress meter details summary mark small nav kbd`;
  - `class` with safe class names, so Basecoat components work;
  - `data-variant`, `data-size`, `data-category` and `data-icon`;
  - limited `role` and `aria-*` values;
  - `rel` and `target="_blank"` on links;
  - `http`, `https`, `mailto` and `tel` URLs.
- **What it removes:** `<script>`, `<style>`, `style` attributes, event handlers, `<iframe>`, `<form>`, `<input>` and `javascript:` URLs. They never survive.
- **Icons:** `<span data-icon="name"></span>` (optionally with `class`) is replaced with the site's inline Lucide icon.
- **Forms:** placeholders are described in [Forms](#forms). Any other `data-form` attribute is neutralized, so a plugin cannot place another plugin's form.
- **Escape anyway.** Escape every value you insert (`pluginsdk.Escape`). The sanitizer is a safety net, not an encoder.

## Forms

Plugins never write `<form>` HTML. They declare forms in the manifest, and the host renders and protects them.

```json
{"id": "contact", "title": {"en": "Contact us"}, "submit": {"en": "Send", "es": "Enviar"},
 "fields": [
   {"name": "name", "label": "Name", "type": "text", "required": true, "max_length": 100},
   {"name": "email", "label": "E-mail", "type": "email", "required": true},
   {"name": "topic", "label": "Topic", "type": "select", "choices": ["Sales", "Support"]},
   {"name": "message", "label": "Message", "type": "textarea", "required": true, "placeholder": "…"},
   {"name": "consent", "label": "I accept the privacy policy", "type": "checkbox", "required": true}]}
```

**Declaring fields.**

- **Field types:** `text email url tel number textarea select checkbox`.
- **Field names:** `^[a-z][a-z0-9_]{0,39}$`. The host's own fields start with `_`.
- **Size:** 1 to 20 fields. `max_length` defaults to 200 (5000 for `textarea`), and the maximum is 10000. `select` takes 1 to 50 choices.

**Placing a form.** Return `pluginsdk.FormPlaceholder("contact")`, which is `<div data-form="contact"></div>`, from a shortcode, a widget or a content filter. The host qualifies it as `<div data-form="<plugin>.contact"></div>`. On every response, even from the page cache, it replaces it with the form:

- a signed, single-use token, valid for 24 hours;
- a honeypot field;
- the language and the page to come back to.

**Validating a submission.** `POST /plugin-forms/<plugin>/<form>` checks, in order:

1. the cross-origin protection (`Sec-Fetch-Site` and `Origin`);
2. `application/x-www-form-urlencoded`, at most 64 KiB;
3. 10 submissions per IP every 10 minutes;
4. the honeypot (bots get a fake success);
5. the token (expired or reused tokens are refused);
6. a minimum of 3 seconds between rendering and sending;
7. every field: required, length, e-mail, `http(s)` URL, phone, number, choice, and checkbox.

Values are trimmed and control characters are removed (newlines are kept in `textarea`). Only then does `form.submit` run.

**After a submission.** The visitor is redirected back to the page (`?pf=<plugin>.<form>#form-<plugin>-<form>`). The result, field errors and entered values travel in a short-lived encrypted cookie. The page with `?pf=` is not cached.

## Assets and the Content-Security-Policy

- **Declaring assets.** `"assets": {"css": [...], "js": [...]}` links files from `assets/` on every public page while the plugin is enabled.
  - CSS is always allowed. JavaScript needs `assets:js` (high risk).
  - The admin panel never loads plugin assets.
- **Serving.** Assets are served from the site at `/plugin-assets/<id>/<hash>/assets/…`. The hash changes with the content, so responses are `Cache-Control: public, max-age=31536000, immutable`, with `nosniff`, `Cross-Origin-Resource-Policy: same-origin` and a sandboxing CSP.
- **The CSP still applies.** The site's CSP stays in force: `script-src 'self'`, no inline scripts or styles, no inline event handlers, and `connect-src 'self'`.
  - The scanner flags `eval`, `new Function`, `document.cookie`, storage, inline handlers, `document.write` and external URLs in JavaScript.
  - In CSS it flags `@import`, external `url()` and script-like constructs.
  - In SVG it flags scripts and handlers.
- **Extending the CSP.** `"csp"` adds exact `https://` sources to `frame-src` (for embeds such as video players) or `img-src`.
  - A `frame-src` set to `'none'` is replaced by the plugin sources.
  - The additions apply to public pages only, never to `/admin`, and only while the plugin is enabled.
  - The base policy already allows `https:` images, so `img-src` additions only matter on sites with a stricter policy.

## Settings

| Type | Stored as | Admin field | Notes |
|---|---|---|---|
| `text`, `textarea` | text | input, textarea | at most 4000 bytes |
| `number` | decimal text | number | empty means "use the default" |
| `bool` | `true` / `false` | checkbox | |
| `select` | one of `choices` | select | 1 to 100 choices; the default must be one of them; empty means "use the default" |
| `url` | `https://` URL | url | |
| `email` | one address | email | no default allowed; an address an administrator saves also allows `mail_send` to it |
| `datetime` | local site time `YYYY-MM-DDTHH:MM` | datetime-local | plugins receive RFC 3339 with the site offset, e.g. `2026-07-01T10:00:00+02:00` |
| `secret` | encrypted | password | see below |

- **Declaring a setting.** It needs `key` (`^[a-z][a-z0-9_]{0,39}$`), `label` and `type`. `default`, `help` and `required` are optional.
- **Saving.** Only the submitted keys change. A key added in a later version uses its default until it is saved.
- **Secrets.**
  - They are encrypted at rest with AES-256-GCM, bound to the plugin and the key.
  - They are never shown again in the admin panel. An empty field keeps the stored value, and a checkbox clears it.
  - They are decrypted only in the requests sent to the plugin. Secrets cannot have defaults.
- **Times.** `req.SettingTime(key)` in the SDK parses datetime settings. `Site.Timezone` is the site's IANA time zone.

## The scanner

Every upload, `sap13ns plugin check`, `sap13ns plugin pack` and the **Scan again** button run the same scanner. It reports findings with a severity, and the severities give a verdict:

| Verdict | When | Effect |
|---|---|---|
| `blocked` | any critical finding | the package is refused; an installed plugin cannot be enabled; `check` and `pack` exit 1 |
| `review` | high findings | enabling needs the confirmation box |
| `clean` | only medium and info | nothing else is needed |

The scanner checks, in order:

1. the package (see [Package format](#package-format));
2. the manifest;
3. the module and asset hashes;
4. the documentation files;
5. the blocklist;
6. compilation, imports and exports;
7. permissions against imports;
8. strings in the data and custom sections and in assets;
9. a dry run, which happens only when nothing is critical.

| Rule | Severity | Meaning |
|---|---|---|
| `manifest.missing`, `manifest.invalid`, `manifest.abi` | critical | `plugin.json` is missing, invalid, or for another ABI |
| `manifest.reserved-id` | critical | the id is reserved |
| `manifest.id-collision` | high | an update claims the id of a plugin by another author |
| `manifest.shortcode-collision` | medium | another installed plugin has the same shortcode |
| `wasm.missing`, `wasm.compile`, `wasm.format` | critical | the module cannot be read, compiled or parsed |
| `wasm.import` | critical | an import the host does not provide (only `sap13ns` and WASI preview 1 exist) |
| `wasm.import-signature` | critical | a known import with the wrong signature |
| `wasm.import-memory` | critical | the module imports its memory |
| `wasm.export` | critical | missing or wrong `sap13ns_alloc`, `sap13ns_handle` or `memory` |
| `wasm.no-abi-export` | medium | no `sap13ns_abi` |
| `wasm.wasi-sockets` | info | WASI socket imports, which always fail |
| `perm.undeclared` | high | the module imports a host function its permissions do not cover |
| `perm.http-wildcard` | high | `http:*` |
| `perm.high-risk` | medium | another high-risk permission (noted; enabling still asks for confirmation) |
| `perm.unused` | medium | a permission the module never uses |
| `perm.http-render-only`, `perm.mail-render-only` | medium | the network or e-mail are imported, but no hook can use them |
| `strings.cloud-metadata` | critical | cloud metadata addresses (`169.254.169.254`, `metadata.google.internal`, …) |
| `strings.miner` | critical | cryptocurrency miner signatures and pools |
| `strings.embedded-executable` | critical | an embedded ELF, PE, Mach-O or WebAssembly binary |
| `strings.private-host` | high | URLs to loopback, private or `.local`/`.internal` hosts |
| `strings.sensitive-path` | high | `/etc/passwd`, `/proc/self/`, SSH keys, the Docker socket, cloud credentials |
| `strings.high-entropy` | medium | 64 KiB or more of encrypted or compressed-looking data |
| `strings.undeclared-host` | medium / info | hard-coded hosts no `http:` permission covers (medium when the module makes requests) |
| `assets.invalid` | critical | `assets/` holds a link, a special file or a disallowed type |
| `assets.missing` | high | a declared asset is not in the package |
| `assets.js-eval`, `assets.js-function`, `assets.js-cookie`, `assets.css-script`, `assets.svg-script` | high | see [Assets](#assets-and-the-content-security-policy) |
| `assets.js-storage`, `assets.js-inline-handler`, `assets.js-document-write`, `assets.external-url`, `assets.css-import`, `assets.css-external-url` | medium | |
| `assets.js-network` | info | |
| `hash.blocklisted` | critical | the package, module or an asset is on the blocklist |
| `dryrun.instantiate`, `dryrun.abi` | critical | the module does not start, or `sap13ns_abi` is not 1 |
| `dryrun.trap`, `dryrun.timeout`, `dryrun.panic` | high | a hook traps, runs out of time or panics with sample data |
| `dryrun.invalid-response` | medium | a hook answers something that is not JSON |
| `dryrun.error`, `dryrun.slow`, `dryrun.skipped` | info | an error with sample data (expected for "permission denied"), over half the time budget, or not run |
| `package.no-readme`, `package.no-license` | info | missing documentation |

- **The dry run.**
  - It instantiates the module with the normal memory and time limits and calls every declared hook once with sample data.
  - Storage, network and e-mail are always denied, and nothing is written.
- **The blocklist.**
  - It is a list of SHA-256 hashes: a built-in list, plus `<data dir>/plugin-blocklist.txt` if it exists.
  - Each line holds a hash and an optional note; `#` starts a comment.
- **Reports.** The report is stored with the plugin. Approving a plugin whose report is missing or older than its files scans it again first.

## Installing and approving

**Statuses:**

| Status | Meaning |
|---|---|
| `available` | installed, not enabled (or waiting for the network operator's review) |
| `enabled` | approved and running |
| `modified` | the module, assets or requested permissions changed since approval; stopped until approved again |
| `invalid` | the manifest is invalid or does not match its directory |
| `error` | the module failed to compile |

**Installing.**

1. **Upload.** In **Plugins → Upload a plugin**, or with `sap13ns plugin install`. The package is extracted, scanned, and installed **disabled**. The uploader, the source (`upload` or `cli`) and the time are recorded.
2. **Refusal.** A blocked package is refused with its report (HTTP 422), and nothing is written.
3. **Review.** The plugin page shows the requested permissions with their risk, the assets, the SHA-256 of the module and the assets, and the scan report.
4. **Enable.** The form carries the SHA-256 the administrator saw. If the files changed in the meantime, enabling is refused. High-risk permissions or high findings need the confirmation box. Enabling pins the hashes and the permissions.
5. **Use.** Configure the settings, place widgets under **Appearance → Widgets**, and use shortcodes in content.

**Updating** means uploading a package with the same id.

- **What is kept:** settings and key-value data.
- **When it stops:** if the module, assets or permissions changed, the plugin becomes `modified` and must be enabled again. A new version with the same build stays enabled.
- **Older versions** are refused unless **Allow an older version** is ticked (`-allow-downgrade` on the command line).
- **Atomic swap:** the update replaces the directory in two renames.

**Disabling** takes effect immediately. Shortcodes of a disabled plugin show as plain text.

**Uninstalling** is possible only while the plugin is disabled. It removes the files. With **Delete its data**, it also removes the settings, the approval record and the key-value data; otherwise they are kept for a later reinstall.

**Audit.** Every step is written to the audit log with the hashes, versions, verdicts and permissions: `plugin.installed`, `plugin.updated`, `plugin.refused`, `plugin.scanned`, `plugin.approved`, `plugin.disabled`, `plugin.settings_saved` and `plugin.uninstalled`.

**Networks of sites.** A package can be marked *review required* when it is installed for a tenant. It cannot be enabled until the operator clears the review.

## Command line

```sh
sap13ns plugin new <id> [-dir D] [-hooks shortcode,widget,...] [-sdk PATH] [-module M] [-name N]
sap13ns plugin pack [-o DIR] [-no-build] <dir>      # build, pack and scan; writes <id>-<version>.zip and .zip.sha256
sap13ns plugin check [-json] [-skip-dry-run] <dir|file.zip>   # exit status 1 when blocked
sap13ns plugin list [-env FILE]                     # installed plugins of a site
sap13ns plugin install [-env FILE] [-allow-downgrade] <file.zip>   # installs disabled
sap13ns plugin hash <dir>                           # SHA-256 of the module and assets
```

- **`new`** copies the skeleton and keeps only the examples of the hooks you list (`shortcode widget content.filter head.meta event comment.check form.submit`). It rewrites the id, name and Go module. The `go.mod` `replace` points at the SDK found in a parent sap13ns checkout, or at `-sdk`.
- **`pack`** produces a reproducible archive: the same files give the same `.zip`.

## Developing and testing

- **The SDK.** `github.com/vicktor/sap13ns/pluginsdk` registers the handler (`pluginsdk.Handle`), decodes payloads (`req.Decode`), builds responses (`HTML`, `Head`, `MetaTags`, `Verdict`, `FormOK`, `FormErrors`, `FormPlaceholder`) and wraps the host functions (`Log`, `KVGet`, `KVSet`, `KVDelete`, `QueryPosts`, `Fetch` and `SendMail`).
- **Native builds.** The SDK also compiles natively, where host functions return `ErrUnavailable`.
- **Testing without WebAssembly.** `pluginsdk/sdktest` provides a fake host for `go test`. `env := sdktest.New(t)` installs it, and the plugin's handler then runs through the same JSON envelope as on the server.
  - **Calls:** `env.Shortcode`, `env.Widget`, `env.Filter`, `env.Head`, `env.Event`, `env.CheckComment` and `env.SubmitForm`.
  - **Setup:** `env.Plugin.Settings`, `env.Posts`, `env.KV`, `env.HTTP` and `env.MailRecipients`.
  - **Results:** `env.KV`, `env.Mails` and `env.Logs`.
  - **It enforces the server's rules:** missing permissions (`env.Denied`), unknown recipients and non-https URLs fail as they would in production.
- **The skeleton** (`plugins/skeleton`) has one example per hook, tests for each, and a `Makefile` with `build`, `test`, `vet`, `check` and `pack`.

## Versioning

- **The ABI** is `1`. Changes to ABI 1 are additive only:
  - new optional payload or response fields;
  - new hooks, events, setting types or host functions.
  
  Plugins must ignore fields they do not know. A change that would break existing modules gets a new ABI number, and the host refuses modules built for an ABI it does not implement.
- **The SDK** follows the ABI. Its public API stays backward compatible within ABI 1.
- **The scanner** has its own version (`scanner` in reports). Rules can become stricter between releases, so an installed plugin may get new findings on **Scan again**.
- **Plugins** use semantic versions. Bump the version on every release and keep a `CHANGELOG.md`.
