---
type: post
key: wasm-plugins
title: Plugins in a WebAssembly sandbox
slug: plugins-in-a-webassembly-sandbox
excerpt: Why sap13ns plugins start with no access to anything, how permissions are approved, and why the approval is tied to the exact file you reviewed.
date: 2026-10-16
categories: [Security]
tags: [plugins, webassembly, security]
comments: closed
---
If you have looked after a WordPress site for a few years, you probably know the pattern. A plugin that was fine for a long time changes hands, or ships one bad update, and suddenly it is reading the options table, creating admin accounts or injecting links into every page. The plugin did nothing the platform did not allow. The platform simply allowed everything.

When we designed plugins for sap13ns, we started from the opposite assumption: a plugin is code we did not write, and it should be able to do only what someone explicitly agreed to.

## Why WebAssembly

We needed a way to run third-party code inside our Go process without giving it the process's powers. WebAssembly fits that well. A WebAssembly module has no access to anything by default: no files, no network, no environment variables, no system calls. It can only call the functions the host chooses to give it.

We run modules with [wazero](https://wazero.io), a WebAssembly runtime written in pure Go, so there is no C toolchain involved and the sandbox is part of the same binary as everything else.

Plugins can be written in any language that compiles to WebAssembly with WASI. We provide an SDK for Go, because that is what we use:

```bash
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o plugin.wasm .
```

## What a plugin can see

The conversation between sap13ns and a plugin is plain JSON. When a hook fires, the host sends the plugin a message with the hook name, the plugin's settings, a few facts about the site and the data for that hook: the HTML of a post, the attributes of a shortcode, the widget being rendered. The plugin answers with JSON, usually a piece of HTML.

That HTML is then sanitised by the host. A plugin cannot add a `<script>`, an inline style, an iframe or an `onclick` to your pages, even if it tries. The strict Content Security Policy on every page would block them anyway; we remove them before they get that far.

## Permissions, one by one

A plugin that only transforms text needs nothing else. Anything more is a permission, declared in its manifest:

```json
{
  "abi": 1,
  "id": "mastodon-share",
  "name": "Mastodon share counts",
  "version": "0.3.0",
  "hooks": ["widget", "event"],
  "events": ["post.published"],
  "permissions": ["kv", "posts:read", "http:mastodon.social"]
}
```

There are only three kinds:

| Permission | What it allows |
|---|---|
| `kv` | A small private key-value store that no other plugin can read |
| `posts:read` | Reading published posts; drafts and private content stay invisible |
| `http:<host>` | HTTPS requests to that host, or `*.host` for its subdomains |

The manifest above is an illustration, not a plugin we ship. It shows the idea: an administrator sees exactly which host the plugin wants to talk to, and can say no.

Even an approved `http:` permission only reaches public addresses. Requests to loopback, private ranges, link-local addresses or cloud metadata services are refused, and the check happens on the address actually being dialled, so a DNS record that changes between lookup and connection does not get around it.

*Update, 19 October 2026:* the contract has grown since we wrote this. Besides these three, a plugin can now ask for `http:*` (any public host), `mail` (e-mail to the administrator or to addresses saved in its settings), `comments:pii` (a commenter's e-mail, IP address and browser while a comment is checked), `assets:js` (its own script on public pages) and extra sources for the page's `frame-src` or `img-src`. The ones that reach furthest are marked high risk and need an explicit confirmation. There are new hooks for checking comments and handling forms, plugins are uploaded as `.zip` packages that are scanned before they are installed, and the approval now pins the hashes of the module and its assets. [PLUGINS.md](https://github.com/vicktor/sap13ns/blob/main/docs/PLUGINS.md) has the current list.

## Approval is pinned to a hash

Permissions on their own are not enough. If you approve a plugin today and its file is replaced tomorrow, the new code would inherit the old approval. That is exactly the "trusted plugin goes bad" story we wanted to rule out.

So when an administrator approves a plugin, sap13ns stores the SHA-256 hash of its `.wasm` file together with the granted permissions. On every load the hash is checked. If it differs, the plugin does not run; it appears in the admin as changed and waiting for review. You can compute the hash yourself before uploading:

```bash
sap13ns plugin hash ./my-plugin
```

## Limits

A sandbox that lets a plugin run forever is still a problem. Every call has a deadline and a memory cap:

- 300 ms to render, 15 seconds to handle a background event;
- 128 MiB of memory per instance, at most four instances per plugin;
- a maximum module size and maximum input and output sizes;
- after repeated failures a plugin is paused for a minute.

Rendered output is cached with the page, so even a slow plugin does not run on every visit.

## What you give up

This model is more restrictive than what many plugin authors are used to. A sap13ns plugin cannot add arbitrary database tables, cannot rewrite the admin panel and cannot run shell commands. Some ideas simply do not fit, and those belong in the core or nowhere.

There is also a cost in convenience: JSON in and JSON out is slower than calling a function in the same language. In practice the cache hides most of it, and we think that overhead is a fair price for knowing that a widget cannot read your users table.[^1]

## Try it

The repository has fourteen official plugins in `plugins/`. One of them, `callouts`, turns Markdown alerts into styled notes, adds two shortcodes for notes and keyboard keys and needs no permissions at all. It is a good starting point. The [plugins page](/plugins) has an overview, and [PLUGINS.md](https://github.com/vicktor/sap13ns/blob/main/docs/PLUGINS.md) the full contract.

[^1]: The sandbox protects your site from a plugin. It does not make a plugin's own output correct; a plugin with `http:` permission can still show wrong data from the service it calls.
