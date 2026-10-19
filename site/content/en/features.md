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

### Visual and Markdown editing, with a preview

Authors write in a visual editor or straight in Markdown, with tables, footnotes, task lists and fenced code blocks. Both modes edit the same Markdown source, and pasting from Word, Google Docs or a web page keeps the headings, lists, tables and links while dropping the stray formatting. The preview mode uses the same renderer as the public site, so what you see is what gets published. Drafts are saved automatically, and an edit lock keeps two people from overwriting each other.

### Media with responsive variants

Uploads are checked by their actual content, not by their file name. JPEG and PNG images are re-encoded, which strips metadata such as GPS coordinates, and resized into 480, 1024 and 1920 pixel wide variants. Themes get a ready-made `srcset`, so phones download small images and large screens get sharp ones.

### Multilingual content with hreflang

A site has a main language at the root and any number of extra languages under their own prefix, such as `/es/`. A post and its translations are linked to each other, the language switcher takes visitors to the same page in another language, and every page announces its alternates with `hreflang` so search engines show the right version.

### RSS, Atom and JSON Feed, plus WebSub

The whole site, every category, every tag and every author has feeds in all three formats. Feeds can carry full text or excerpts. If you configure a WebSub hub, readers that support it learn about new posts within seconds instead of polling.

### Comments with anti-spam

Comments are optional and can be moderated always, only for first-time commenters, or not at all. Forms carry a hidden honeypot field and a signed timestamp, so bots that fill every field or post instantly are dropped quietly. Commenting is rate limited per address.

### Redirects

When you move content, old links should keep working. Redirects are managed from the admin panel, and when a published post changes its slug, a permanent redirect is created automatically so nobody hits a dead end.

## Looks

### Six themes, light and dark

sap13ns ships with Apple, Neumorphism, Glassmorphism, Bento Grid, Dashboard / SaaS and Onepage. Every theme has a light and a dark mode that follows the visitor's system setting, with a toggle that remembers their choice and no flash of the wrong colours on load. [See the themes](/themes).

### Landing pages and one-page sites

The front page does not have to list posts. It can be a landing page or a one-page site, with the blog at `/blog`: the theme's own, or one you build from features, figures, testimonials, pricing, questions and answers, logos and a call to action, starting from a ready-made page. Menu links scroll to each section and mark the one in view.

### Customiser and page builder

The customiser changes a theme's colours, fonts, corner radius, density, header and footer without touching its files, with a live preview in light and dark. The page builder lays out the header, the footer and each type of page from sections, columns and blocks. Both keep your changes as a draft until you publish them, and keep the last ten published versions.

### Automatic OG images

Posts and pages without a featured image get a social preview card generated from their title and the site name, with no design tool and no AI involved. Links shared on social networks and chat apps look intentional instead of showing a blank grey box.

## Extending

### Plugins in a WebAssembly sandbox

Plugins are WebAssembly modules. By default a plugin cannot read files, open network connections or see the database; it receives the data the core hands it and returns HTML that is sanitised before it reaches a page. Anything more, such as private storage, reading published posts, calling one specific external API or e-mailing the administrator, is a permission declared by the plugin and approved by an administrator.

Plugins are uploaded as `.zip` packages, scanned before they are installed, and installed disabled. Approval is tied to the SHA-256 hashes of the module and its assets and to the permissions granted. If any of them changes, the plugin stops until you review and approve it again. [How plugins work](/plugins).

## Teams and accounts

### Roles

Five roles cover the usual newsroom: administrators run the site, editors publish and moderate everyone's work, authors publish their own posts, contributors write drafts for review, and subscribers can sign in to comment.

### Two-factor authentication and passkeys

A second factor is mandatory for staff by default: TOTP codes from any authenticator app, with single-use recovery codes for the day the phone goes missing, or a passkey. Administrators can limit the requirement to administrators and editors, but they cannot switch it off. Passkeys (WebAuthn) also work for passwordless sign-in, always with user verification.

### Audit log

Sign-ins, publishing, user and role changes, settings changes and plugin approvals are written to an audit log with who, what, when and from which address. When something looks wrong, you can find out what happened.

## Moving in, and out

### WordPress importer

Bring an existing WordPress site over from its export file (WXR): authors, posts, pages, categories, tags, comments and media, with redirects from the old URLs. Content is converted to clean Markdown on the way in, and the import runs in the background, with a dry run and a report.

### Backups that WordPress imports

Leaving should be as easy as arriving. Every backup of a site is also a WordPress import: a WordPress export file with the posts, pages, comments, authors, categories, tags, menus and featured images, and the uploads in WordPress's own folder layout. WordPress's own importer reads it, with no plugin of ours. An automated test imports one into a real WordPress and checks the result. The [guide](https://github.com/vicktor/sap13ns/blob/main/docs/MOVING-TO-WORDPRESS.md) has the steps, for an old site that stays online during the move or for a backup made for the new address.

## AI, on your terms

### An assistant that never publishes on its own

The editor can ask a language model for help: an excerpt, SEO title and description, a translation, an image prompt, a header image where the provider can draw one, or a second opinion on a paragraph. You choose the provider: Claude, any OpenAI-compatible API (including local models through LM Studio, Ollama or llama.cpp) or Gemini. Nothing happens unless an editor clicks a button, and nothing is published without a person reviewing it. A local checker also flags the stock phrases that make text sound machine-written.

### llms.txt and llms-full.txt

Sites can publish `/llms.txt` and `/llms-full.txt`, plain-text indexes of your content for language models and the tools built on them, generated from what is already public. Every post and page also has a clean Markdown version at its own address plus `.md`. An AI access policy (open, no training, or block) decides what `robots.txt` tells AI crawlers and whether these endpoints answer at all.

### A site assistant for visitors

An optional chatbot answers visitors' questions from your own posts and pages and cites them, with a page at `/ask` that works without JavaScript. It searches with PostgreSQL full-text search, plus pgvector when the extension is installed. Drafts, scheduled and private posts are never indexed, and any post can be left out. A read-only MCP endpoint lets external AI agents search the same index.

## Knowing your audience without tracking it

### Cookie-less analytics

Built-in analytics count page views, referrers and popular content in aggregate. There are no cookies, no fingerprinting and no third-party scripts, so there is no consent banner to show.

## Running it

### Backups

Every site keeps its own backups, under **Tools → Backups**: one `.zip` with the content and files in a form WordPress reads, plus what only sap13ns uses (settings, users and roles, menus, widgets, designs, redirects, plugin settings). Administrators download them from a link tied to their session, make one when they like, and restore one after typing the site's name and their password. A restore makes a backup of what it replaces first, and changes the site in one transaction, so a restore that fails leaves the site as it was. What comes back goes through the same checks as the admin pages, and no backup ever holds a password, a second factor or a key.

For disaster recovery, one command also produces a single archive with a PostgreSQL dump and the uploaded files. Both kinds are encrypted with AES-256-GCM when you give a passphrase. The systemd timer that ships with sap13ns makes both every night and prunes old ones. Archives are verified before anything is restored.

### Signed updates

Administrators see a notice in the admin when a new release is out. `sap13ns update` downloads it, checks its Ed25519-signed checksums and swaps the binary atomically, keeping the previous one for a rollback.

### Networks of sites

Multisite is optional. With `SAP13NS_MULTISITE=true`, one process serves a network: a portal where people have one account, and any number of sites. Each site has its own PostgreSQL schema, users, settings, uploads and plugins, and picks its theme from the network's, so one site's content and plugins cannot reach another's. People sign in to every site they belong to through the portal, with a different role on each, and every account has a second factor. The network's administrators open a site they do not belong to only while its owner allows support access, or with an audited emergency access for a legal order or a serious security incident. Plans with storage and domain limits, Stripe billing, and custom domains verified through DNS and served over TLS are built in.

Every site of a network is backed up each night by itself, within the storage of its plan. On the plans that allow it, a site uploads its own themes, checked automatically. Turning an existing single-site install into a network site is not available yet. [MULTISITE.md](https://github.com/vicktor/sap13ns/blob/main/docs/MULTISITE.md) explains how a network works.

## At a glance

| Area | What you get |
|---|---|
| Runtime | One Go binary, PostgreSQL, nginx in front, no Node.js in production |
| Speed | In-memory page cache, PostgreSQL full-text search, responsive images |
| Writing | Visual and Markdown editor with preview, translations, feeds, comments |
| Design | Six themes, each with light and dark mode; landing and one-page front pages; customiser and page builder |
| Extending | WebAssembly plugins with per-permission approval pinned to the module and asset hashes |
| Accounts | Five roles, mandatory second factor (TOTP with recovery codes, or a passkey), passwordless passkey sign-in, audit log |
| Migration | WordPress importer, redirects, backups that WordPress imports |
| AI | Optional writing assistant (Claude, OpenAI-compatible, local, Gemini), site assistant for visitors, llms.txt |
| Operations | Per-site and database backups, encrypted; signed updates; cookie-less analytics; optional networks of sites |
