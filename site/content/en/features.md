---
type: page
key: features
title: Features
slug: features
excerpt: Everything sap13ns does, from the single binary and the plugin sandbox to feeds, translations, backups and multisite.
order: 1
menu: [primary, footer, landing]
menu_label: Features
comments: closed
---
sap13ns is a content management system in the spirit of WordPress: posts, pages, categories, menus, widgets, themes and plugins. The difference is in how it is built. It is one Go program, it keeps its data in PostgreSQL, it sits behind nginx and it assumes from the first line of code that the internet is hostile.

This page walks through what you get. Each section is short; the [documentation on GitHub](https://github.com/vicktor/sap13ns) has the details.

## Small by design

### One Go binary

The server, the admin panel, every template, stylesheet, icon and translation are compiled into a single executable. Installing means copying one file; updating means replacing it and restarting the service. There is no `node_modules` on the server, no PHP runtime and no plugin folder full of code you have never read.

Database migrations ship inside the binary too and run when the server starts, so a new version brings its schema changes with it.

### PostgreSQL full-text search

Search uses PostgreSQL itself: every post has a weighted `tsvector` (title first, then excerpt, then body) backed by a GIN index. Visitors can type the way they type into a search engine, with quoted phrases and `-exclusions`. No external search service, no extra process to babysit.

### An in-memory page cache

Public pages are rendered once and then served from memory. Anything that changes public content (publishing, editing, a new comment, a menu change) clears the cache, so visitors never see stale pages and the database is left alone for the reads that matter.

## Writing and publishing

### Markdown editor with live preview

Authors write in Markdown with tables, footnotes, task lists and fenced code blocks, and see the rendered result next to the text as they type. The preview uses the same renderer as the public site, so what you see is what gets published.

### Media with responsive variants

Uploads are checked by their actual content, not by their file name. JPEG and PNG images are re-encoded, which strips metadata such as GPS coordinates, and resized into 480, 1024 and 1920 pixel wide variants. Themes get a ready-made `srcset`, so phones download small images and large screens get sharp ones.

### Multilingual content with hreflang

A site has a main language at the root and any number of extra languages under their own prefix, such as `/es/`. A post and its translations are linked to each other, the language switcher takes visitors to the same page in another language, and every page announces its alternates with `hreflang` so search engines show the right version.

### RSS, Atom and JSON Feed, plus WebSub

The whole site, every category, every tag and every author has feeds in all three formats. Feeds can carry full text or excerpts. If you configure a WebSub hub, readers that support it learn about new posts within seconds instead of polling.

### Comments with anti-spam

Comments are optional and can be moderated always, only for first-time commenters, or not at all. Forms carry a hidden honeypot field and a signed timestamp, so bots that fill every field or post instantly are dropped quietly. Commenting is rate limited per address.

### Redirects

When you move content, old links should keep working. Redirects are managed from the admin panel, and changing a slug can leave a permanent redirect behind so nobody hits a dead end.

## Looks

### Five themes, light and dark

sap13ns ships with Apple, Neumorphism, Glassmorphism, Bento Grid and Dashboard / SaaS. Every theme has a light and a dark mode that follows the visitor's system setting, with a toggle that remembers their choice and no flash of the wrong colours on load. [See the themes](/themes).

### Automatic OG images

Each post gets a social preview image generated from its title and the site name, with no design tool and no AI involved. Links shared on social networks and chat apps look intentional instead of showing a blank grey box.

## Extending

### Plugins in a WebAssembly sandbox

Plugins are WebAssembly modules. By default a plugin cannot read files, open network connections or see the database; it receives the data the core hands it and returns HTML that is sanitised before it reaches a page. Anything more, such as private storage, reading published posts or calling one specific external API, is a permission declared by the plugin and approved by an administrator.

Approval is tied to the SHA-256 hash of the module. If someone replaces the file, the plugin stops loading until you review and approve it again. [How plugins work](/plugins).

## Teams and accounts

### Roles

Five roles cover the usual newsroom: administrators run the site, editors publish and moderate everyone's work, authors publish their own posts, contributors write drafts for review, and subscribers can sign in to comment.

### Two-factor authentication

Accounts can use TOTP codes from any authenticator app, with single-use recovery codes for the day the phone goes missing. Administrators can make two-factor authentication mandatory for admins and editors. Passkeys are planned.

### Audit log

Sign-ins, publishing, user and role changes, settings changes and plugin approvals are written to an audit log with who, what, when and from which address. When something looks wrong, you can find out what happened.

## Moving in

### WordPress importer

Bring an existing WordPress site over from its export file (WXR): posts, pages, categories, tags, comments and media. Content is converted to clean Markdown on the way in.

## AI, on your terms

### An assistant that never publishes on its own

The editor can ask a language model for help: an excerpt, SEO title and description, a translation, an image prompt or a second opinion on a paragraph. You choose the provider: Claude, any OpenAI-compatible API (including local models through Ollama or llama.cpp) or Gemini. Nothing happens unless an editor clicks a button, and nothing is published without a person reviewing it. A local checker also flags the stock phrases that make text sound machine-written.

### llms.txt and llms-full.txt

Sites can publish `/llms.txt` and `/llms-full.txt`, plain-text indexes of your content for language models and the tools built on them, generated from what is already public.

## Knowing your audience without tracking it

### Cookie-less analytics

Built-in analytics count page views, referrers and popular content in aggregate. There are no cookies, no fingerprinting and no third-party scripts, so there is no consent banner to show.

## Running it

### Backups

One command produces a single archive with a PostgreSQL dump and the uploaded files, optionally encrypted with a passphrase. Run it from a systemd timer and keep a few days of history. Archives are verified before anything is restored.

### Multisite

One account can manage many sites. Each site has its own domain, its own themes and plugins, its own uploads and its own PostgreSQL schema, so one site's content and plugins cannot reach another's. Sign in once and switch between the sites you belong to, with a different role on each if needed.

## At a glance

| Area | What you get |
|---|---|
| Runtime | One Go binary, PostgreSQL, nginx in front, no Node.js in production |
| Speed | In-memory page cache, PostgreSQL full-text search, responsive images |
| Writing | Markdown with live preview, translations, feeds, comments |
| Design | Five themes, each with light and dark mode |
| Extending | WebAssembly plugins with per-permission approval pinned to the module hash |
| Accounts | Five roles, TOTP 2FA with recovery codes, audit log, passkeys planned |
| Migration | WordPress importer, redirects |
| AI | Optional assistant (Claude, OpenAI-compatible, local, Gemini), llms.txt |
| Operations | Backups, cookie-less analytics, multisite |
