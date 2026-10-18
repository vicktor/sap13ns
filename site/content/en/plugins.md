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

A plugin is two files in a folder:

- **`plugin.wasm`**, a WebAssembly module. You can write it in Go with our SDK, or in any language that compiles to WebAssembly with WASI.
- **`plugin.json`**, a manifest that says who made it, which hooks it wants, which shortcodes and widgets it provides, which settings it has and which permissions it needs.

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

There are five hooks:

| Hook | What it is for |
|---|---|
| `content.filter` | Transform the HTML of a post or page before it is shown |
| `shortcode` | Render shortcodes such as `[callout]` inside posts |
| `widget` | Render widgets that editors place in theme areas |
| `head.meta` | Add `<meta>` tags to the page head |
| `event` | React in the background to `post.published`, `post.updated`, `post.trashed`, `comment.created`, `comment.approved`, `user.registered` and `cron.hourly` |

Rendered output is cached together with the page, so a plugin does not run on every visit.

## Permissions

Without permissions a plugin can only compute: take input, return output. Everything else must be declared in the manifest and approved by an administrator:

| Permission | What it allows |
|---|---|
| `kv` | A small private key-value store, visible only to that plugin |
| `posts:read` | Querying published posts, never drafts or private content |
| `http:example.com` | HTTPS requests to that host only; `http:*.example.com` allows its subdomains |

Even with an `http:` permission, a plugin can only reach public addresses. Loopback, private networks, link-local addresses and cloud metadata endpoints are blocked, and the address is checked at the moment of connecting so DNS tricks do not get around it.

## Approval is pinned to the module

When you approve a plugin, sap13ns records the SHA-256 hash of its `.wasm` file next to the permissions you granted. If the file changes, because of an update, a compromised deploy or a curious colleague, the plugin stops loading and shows up as needing review. Nothing new runs until you approve it again.

You can check the hash yourself before uploading:

```bash
sap13ns plugin hash ./callout
```

## Limits

Every call runs with hard limits, so a buggy plugin slows nothing down for long:

- 128 MiB of memory per instance and at most four instances per plugin at once;
- 300 ms for each render call and 15 seconds for each background event;
- modules up to 32 MiB, with capped input and output sizes;
- a plugin that keeps failing is paused for a minute before it is tried again.

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

## Example plugins

The repository includes four official plugins in `plugins/`, each one its own Go module with tests: **callouts** (GitHub-style Markdown alerts plus the `callout` and `kbd` shortcodes), **toc** (an automatic table of contents), **external-links** (UTM parameters and link attributes for links to other sites) and **announcement** (a scheduled announcement widget). They use the shared component styles, so they look at home in every theme, and none of them needs any permission.

## Review

On a server you run yourself, you are the reviewer: read the manifest, check the permissions and approve. On sap13ns.site hosting, plugins uploaded by customers are reviewed by us before they can be enabled, and the same hash pinning applies: an approved plugin cannot be swapped for a different file.

The complete reference is in [PLUGINS.md](https://github.com/vicktor/sap13ns/blob/main/docs/PLUGINS.md).
