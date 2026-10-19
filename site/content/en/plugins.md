---
type: page
key: plugins
title: Plugins
slug: plugins
excerpt: Extend sap13ns with WebAssembly plugins that can only do what you explicitly allow, and only in the version you reviewed.
order: 3
menu: [primary, footer, landing]
menu_label: Plugins
comments: closed
---
Plugins are where most content management systems get hurt. A plugin usually runs with the same rights as the application: it can read the configuration, query any table, write files and talk to any server. One careless or malicious update and the whole site belongs to someone else.

sap13ns takes the opposite position. A plugin starts with no access to anything and earns each capability through an explicit, reviewable permission.

## What a plugin is

A plugin is a `.zip` package with two files that matter:

- **`plugin.wasm`**, a WebAssembly module. You can write it in Go with our SDK, or in any language that compiles to WebAssembly with WASI.
- **`plugin.json`**, a manifest that says who made it, which hooks it wants, which shortcodes, widgets and forms it provides, which settings it has and which permissions it needs.

A package can also carry assets such as stylesheets, scripts, images and fonts in `assets/`, plus a README, a licence and a changelog. Nothing else is accepted.

```json
{
  "abi": 1,
  "id": "callout",
  "name": "Callouts",
  "version": "1.0.0",
  "hooks": ["shortcode"],
  "shortcodes": ["callout", "kbd"],
  "permissions": []
}
```

## The ABI in plain words

The contract between sap13ns and a plugin is called ABI 1. In practice it works like this:

1. When something happens that the plugin subscribed to, sap13ns sends it a small JSON message: which hook, the plugin's settings, basic facts about the site and the data for that hook.
2. The plugin does its work and answers with JSON, usually a fragment of HTML.
3. sap13ns sanitises that HTML before it goes anywhere near a page. Scripts, styles, iframes, forms and event handlers are removed.

There are seven hooks:

| Hook | What it is for |
|---|---|
| `content.filter` | Transform the HTML of a post or page before it is shown |
| `shortcode` | Render shortcodes such as `[callout]` inside posts |
| `widget` | Render widgets that editors place in theme areas |
| `head.meta` | Add `<meta>` tags and Schema.org JSON-LD to the page head |
| `comment.check` | Hold or reject a new comment before it is stored |
| `form.submit` | Handle a form the plugin declares; the host renders and protects the form itself |
| `event` | React in the background to `post.published`, `post.updated`, `post.trashed`, `comment.created`, `comment.approved`, `user.registered` and `cron.hourly` |

The output of the first four is cached together with the page, so a plugin does not run on every visit.

## Permissions

Without permissions a plugin can only compute: take input, return output. Everything else must be declared in the manifest and approved by an administrator:

| Permission | What it allows |
|---|---|
| `kv` | A small private key-value store, visible only to that plugin |
| `posts:read` | Querying published posts, never drafts or private content |
| `http:example.com` | HTTPS requests to that host only; `http:*.example.com` allows its subdomains |
| `http:*` | HTTPS requests to any public host (high risk) |
| `mail` | E-mail to the site administrator, or to addresses saved in the plugin's settings |
| `comments:pii` | Commenters' e-mail, IP address and browser when checking a comment (high risk) |
| `assets:js` | Linking the plugin's own script on public pages (high risk) |

A plugin can also ask to add exact `https://` sources to the page's `frame-src` or `img-src`, for example for a video player; that counts as a high-risk permission too. High-risk permissions need an explicit confirmation when the plugin is enabled. Network access and e-mail are never available while a page is rendered: requests only work in events, comment checks and form submissions, and e-mail only in events and form submissions.

Even with an `http:` permission, a plugin can only reach public addresses. Loopback, private networks, link-local addresses and cloud metadata endpoints are blocked, and the address is checked at the moment of connecting so DNS tricks do not get around it.

## Approval is pinned to the module

Before a package is installed, it is unpacked with strict limits and scanned: the manifest, the module's imports and exports, the strings inside it, its assets, and a dry run of every hook with sample data. Critical findings block it, and high ones need a confirmation. A plugin is always installed disabled.

When you enable a plugin, sap13ns records the SHA-256 hashes of its `.wasm` module and its assets next to the permissions you granted. If any of them changes, because of an update, a compromised deploy or a curious colleague, the plugin stops and shows up as needing review. Nothing new runs until you approve it again.

You can check the hashes yourself before uploading:

```bash
sap13ns plugin hash ./callout
```

## Limits

Every call runs with hard limits, so a buggy plugin slows nothing down for long:

- 128 MiB of memory per instance and at most four instances per plugin at once;
- 300 ms for each render call and 15 seconds for each background event;
- modules up to 32 MiB, with capped input and output sizes;
- a plugin that fails five times in a row is paused for a minute before it is tried again.

## Writing a plugin in Go

The `pluginsdk` package takes care of the plumbing. You register a handler, switch on the hook and return HTML:

```go
func init() {
	pluginsdk.Handle(func(req *pluginsdk.Request) (any, error) {
		if req.Hook == pluginsdk.HookShortcode {
			var sc pluginsdk.Shortcode
			req.Decode(&sc)
			return pluginsdk.HTML("<mark>" + pluginsdk.Escape(sc.Inner) + "</mark>"), nil
		}
		return nil, nil
	})
}

func main() {}
```

Build it with the standard Go toolchain:

```bash
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o plugin.wasm .
```

`sap13ns plugin new my-plugin` starts a project from the skeleton, with an example and a test for each hook, and `sap13ns plugin pack` builds, scans and packages it into the `.zip` you upload.

## Example plugins

The repository includes eleven official plugins in `plugins/`, each one its own Go module with tests and a README that explains every permission it asks for:

- **callouts**: GitHub-style Markdown alerts plus the `callout` and `kbd` shortcodes.
- **toc**: an automatic table of contents.
- **external-links**: UTM parameters and link attributes for links to other sites.
- **announcement**: a scheduled announcement widget.
- **embeds**: YouTube, Vimeo and TikTok links become click-to-load players. Nothing is requested from the video site until the visitor presses play, and then the player comes from youtube-nocookie.com, from Vimeo with Do Not Track, or from TikTok.
- **youtube**: the latest videos of a YouTube channel or playlist as a grid or a carousel, in a post or as a widget. The list is refreshed every hour on the server, and a video plays from youtube-nocookie.com only after a click.
- **schema**: Schema.org JSON-LD for posts, pages, breadcrumbs, the site search box and the publisher.
- **related**: related posts by shared tags and categories, after each post and as a widget.
- **contact**: a contact form with privacy consent, sent to the administrator with the visitor as Reply-To.
- **antispam**: local checks on new comments (links, a blocklist, mixed alphabets, shouting, repeated text), with an optional StopForumSpam lookup that is off by default.
- **social**: posts new articles to Bluesky and Mastodon, in the post's language, and never twice.

They use the shared component styles, so they look at home in every theme, light and dark.

## Review

On a server you run yourself, you are the reviewer: read the manifest, check the permissions and approve. On a network of sites, the operator also reviews every plugin a site uploads before the site can enable it, and sites can install the plugins the operator offers in the network's catalogue. On sap13ns.site hosting, that operator is us: plugins uploaded by customers are reviewed by us before they can be enabled, and the same hash pinning applies: an approved plugin cannot be swapped for a different file.

The complete reference is in [PLUGINS.md](https://github.com/vicktor/sap13ns/blob/main/docs/PLUGINS.md).
