# My plugin

This folder is a working sap13ns plugin with one small example of every hook. Start from it, keep what you need, and delete the rest.

> Before you publish, replace this README with one about your plugin. The README is packed into the `.zip` along with `LICENSE` and `CHANGELOG.md`.

The full contract for manifest fields, payloads, host functions, limits and the scanner is in [docs/PLUGINS.md](../../docs/PLUGINS.md) of the sap13ns repository.

## What a plugin is

A sap13ns plugin is a WebAssembly module (`plugin.wasm`) plus a manifest (`plugin.json`). The CMS runs it in a sandbox inside its own process:

- **What it cannot reach:** the file system, environment variables, sockets and the database.
- **What it can always do:** compute, read the clock and random numbers, and write to its log.
- **Everything else is a permission.** Each permission is declared in `plugin.json` and approved by an administrator: its own key-value storage, reading published posts, HTTPS to listed hosts, sending e-mail to the site's own addresses, and a few others.
- **Plugins only answer hooks.** The host calls the plugin when a post is rendered, a widget is shown, a comment arrives, a form is sent or an event happens. The plugin returns data such as HTML, meta tags or a verdict, which the host validates and sanitizes.

You write plugins in Go with the SDK, `github.com/vicktor/sap13ns/pluginsdk`.

## 1. Requirements

- **Go 1.24 or newer.** Check with `go version`. Go 1.24 added `//go:wasmexport`, which the SDK needs.
- **The plugin SDK.** It lives in the `pluginsdk/` folder of a sap13ns checkout: `git clone https://github.com/vicktor/sap13ns`.
- **The `sap13ns` binary.** You need it for `check` and `pack` (`go build -o ~/bin/sap13ns ./cmd/sap13ns` in the checkout). Building and testing work without it.

## 2. Create your project

The easiest way is to let sap13ns copy this skeleton under a new id:

```sh
sap13ns plugin new hello-world                         # every example
sap13ns plugin new hello-world -hooks shortcode,widget # only these examples
sap13ns plugin new hello-world -dir ~/code/hello -sdk ~/src/sap13ns/pluginsdk
```

- **What it rewrites:** the id, the name and the Go module path. It also points `go.mod` at the SDK.
- **`-sdk`:** use it when the new folder is not inside a sap13ns checkout.
- **Hooks you can name with `-hooks`:** `shortcode`, `widget`, `content.filter`, `head.meta`, `event`, `comment.check` and `form.submit`.

You can also copy this folder by hand. If you do:

1. Change `"id"` and `"name"` in `plugin.json`. The skeleton's placeholder id is reserved, and the scanner refuses it.
2. Change the `module` line in `go.mod`.
3. Fix the `replace` line in `go.mod` so it points at your `pluginsdk` folder.

## 3. What is in the folder

| File | Purpose |
|---|---|
| `plugin.json` | The manifest: id, version, hooks, shortcodes, widgets, events, forms, permissions and settings. The host trusts nothing that is not declared here. |
| `main.go` | Receives every call from the host and dispatches it by hook. |
| `hello.go` | `shortcode`: `[hello name="Ada"]` |
| `latest.go` | `widget`: the newest posts, with a per-widget "Number of posts" field |
| `reading.go` | `content.filter`: a reading-time note under posts |
| `head.go` | `head.meta`: a `<meta>` tag and a schema.org JSON-LD object |
| `events.go` | `event`: counts published posts in the key-value store |
| `comments.go` | `comment.check`: holds comments with too many links for moderation |
| `contact.go` | `form.submit`: a contact form, placed with `[contact]`, that e-mails the site administrator |
| `*_test.go` | Tests that run natively with `go test`, using the fakes in `pluginsdk/sdktest` |
| `Makefile` | `build`, `test`, `vet`, `check`, `pack` |

Each example registers itself in `init()`. To drop one, delete its `.go` and `_test.go` files. Then remove its hook, its shortcode, widget or form, and the permissions and settings only it uses from `plugin.json`.

Keep the logic in plain functions, such as `Hello`, `ReadingNote` and `CheckLinks`. Plain functions are easy to test, and `main.go` stays a thin adapter.

## 4. Test

```sh
make test        # or: go test ./...
```

- **No WebAssembly needed.** The tests run natively, because the SDK compiles for your machine as well as for wasm.
- **The real dispatch path.** `sdktest.New(t)` installs a fake host. Calls like `env.Shortcode`, `env.Widget`, `env.Filter`, `env.Head`, `env.Event`, `env.CheckComment` and `env.SubmitForm` go through the same JSON envelope the server sends.
- **Setting up a test:** put values in `env.Plugin.Settings`, `env.Posts`, `env.KV`, `env.HTTP` (a fake fetch) and `env.MailRecipients`.
- **Checking results:** inspect `env.KV`, `env.Mails` and `env.Logs`.
- **The fakes follow the server's rules.** A missing permission (`env.Denied["posts:read"] = true`), a mail recipient that is not allowed, or an `http://` URL fails in the test just as it would in production.

`make vet` also vets the code for `GOOS=wasip1`.

## 5. Build

```sh
make build
# which runs:
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -trimpath -o plugin.wasm .
```

- **`-buildmode=c-shared`** makes a reactor module: the host calls its exported functions, and `main` never runs.
- **`-trimpath`** keeps your local paths out of the binary.

## 6. Check and package

```sh
make check       # sap13ns plugin check .   — the same scan the server runs on upload
make pack        # test, build, scan, then write hello-world-0.1.0.zip and .zip.sha256
```

The scan gives a verdict:

| Verdict | Meaning | Effect |
|---|---|---|
| **blocked** | At least one critical finding | The upload is refused, and `check` and `pack` exit with status 1. |
| **review** | High findings | The administrator must tick a confirmation box to enable the plugin. |
| **clean** | Only medium findings and notes | Shown in the report; nothing else is needed. |

Read the findings even when the verdict is clean. The usual ones are:

- **Undeclared permissions:** the module imports a host function its permissions do not cover. This is high.
- **Unused permissions:** a declared permission is never used. This is medium; remove it.
- **Hard-coded hosts** that no `http:` permission covers.
- **Dry-run errors:** the scanner calls every hook once with sample data and storage, network and e-mail switched off. "Permission denied" here is expected for features that need them.

The `.zip` contains only `plugin.json`, `plugin.wasm`, `assets/`, `README.md`, `LICENSE` and `CHANGELOG.md`. Sources and tests stay out.

## 7. Install

1. In the admin panel, open **Plugins → Upload a plugin**, choose the `.zip` and press **Upload and scan**.
2. The plugin is installed **disabled**. Open it and read the scan report and the requested permissions. High-risk ones are marked.
3. Switch it on. Enabling pins the exact build: if the module, its assets or its permissions change later, the plugin stops until it is approved again.
4. Configure its settings, place its widgets under **Appearance → Widgets**, and use its shortcodes in posts.

Operators can also install from the command line with `sap13ns plugin install -env /etc/sap13ns/sap13ns.env hello-world-0.1.0.zip`. The plugin still arrives disabled.

**Updating:** upload a new `.zip` with the same id and a higher `"version"`.

- Settings and stored data are kept.
- If the module, assets or permissions changed, the plugin must be enabled again.
- Installing an older version needs an explicit confirmation.

## 8. Permissions

| Permission | Allows | Risk |
|---|---|---|
| *(none)* | Transforming what the hook receives | low |
| `kv` | A private key-value store (1 MiB, 1000 keys) | low |
| `posts:read` | Reading **published** posts with `QueryPosts` | low |
| `http:api.example.com` | HTTPS requests to that host only (exact host, or `*.example.com`) | medium |
| `http:*` | HTTPS requests to any public host | **high** |
| `mail` | `SendMail` to the site administrator or to addresses saved in this plugin's `email` settings. There is no open relay, and the limit is 20 messages per hour. | medium |
| `comments:pii` | Receiving the e-mail address, IP address and browser of commenters in `comment.check` | **high** |
| `assets:js` | Adding your own JavaScript to every public page | **high** |
| `"csp"` in the manifest | Adding exact `https://` hosts to the site's `frame-src` or `img-src` (embeds) | **high** |

Ask for as little as you can. Plugins with high-risk permissions need an explicit confirmation to be enabled, and many administrators will not grant them.

Requests never reach private, loopback or link-local addresses, whatever the permission says, and redirects are checked again.

## 9. Limits

| What | Limit |
|---|---|
| Module size | 32 MiB (the whole package: 40 MiB) |
| Memory | 128 MiB per instance |
| Rendering hooks (`shortcode`, `widget`, `content.filter`, `head.meta`) | 300 ms; no network and no e-mail |
| `comment.check` | about 2 s for all plugins together; may use the network; errors count as "ok" |
| `form.submit` | 5 s; may use the network and send e-mail |
| `event` | 15 s, in the background; may use the network and send e-mail |
| Output | 2 MiB of HTML per call; JSON-LD 16 KiB per object and 64 KiB per page |
| Host calls per hook call | 200 KV operations, 10 post queries, 4 HTTP requests |
| HTTP | HTTPS only; 256 KiB request body; 1 MiB response |
| Failures | after 5 errors in a row the plugin pauses for a minute |

## 10. Security rules

- **Escape everything** that comes from settings, attributes, posts or the network with `pluginsdk.Escape`.
  - The host sanitizes all HTML: `<script>`, `<style>`, `style=""` attributes, event handlers and `javascript:` URLs never survive.
  - Write output as if that sanitizer did not exist.
- **Never write `<form>` HTML.** Declare forms in `plugin.json` and return `pluginsdk.FormPlaceholder("id")`.
  - The host renders and protects the form: a token, a honeypot, a rate limit, a size limit and a same-origin check.
  - The host validates every field before your `form.submit` hook runs.
- **Secrets** belong in settings of type `secret`.
  - They are encrypted at rest and never shown again in the admin panel.
  - They are decrypted only in the requests the host sends to your plugin.
  - Never log them or put them in HTML.
- **E-mail** only reaches the site's own addresses.
  - Put the visitor's address in `ReplyTo`, never in `To`.
- **No obfuscation.** The scanner refuses or flags these:
  - packed binaries and embedded executables;
  - cryptocurrency miners;
  - cloud metadata addresses, private hosts and system paths.
  - In assets: `eval`, `new Function`, `document.cookie`, inline event handlers and external URLs.
- **Assets** (`"assets": {"css": [...], "js": [...]}`) go under `assets/`. They are served from the site itself with a content hash and an immutable cache.
  - JavaScript needs `assets:js`.
  - CSS must not load anything from other sites.
- **Version honestly.** Use semantic versions (`1.2.3`) and bump the version on every release. Keep a `CHANGELOG.md`.

## 11. Troubleshooting

| Problem | What to check |
|---|---|
| `go: cannot find module providing github.com/vicktor/sap13ns/pluginsdk` | The `replace` line in `go.mod` must point at the `pluginsdk` folder of a sap13ns checkout. |
| `undefined: ...` when building for wasm only | A file has a `//go:build` tag, or uses a package that does not support `wasip1`. |
| The plugin shows **Changed — review again** | The `.wasm`, its assets or its permissions changed after approval. Review the plugin and enable it again. |
| Nothing appears on the page | Check the hook, shortcode, widget or form is declared in `plugin.json`, that the plugin is enabled, and read **Activity** on the plugin page. |
| `permission denied` in Activity | The permission is missing from `plugin.json`, or it was added after approval and needs a new approval. |
