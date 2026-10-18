<p align="center">
  <img src="ui/static/brand/logo.svg" alt="sap13ns" width="96" height="96">
</p>

<h1 align="center">sap13ns</h1>

<p align="center">
  A lightweight, secure and fast content management system.<br>
  The everyday features of WordPress in one Go binary, backed by PostgreSQL.
</p>

<p align="center">
  <a href="https://sap13ns.site">sap13ns.site</a> ·
  <a href="docs/RELEASES.md">Releases</a> ·
  <a href="LICENSE">CPAL-1.0</a>
</p>

---

sap13ns is for people who want a blog or a company site with posts, pages, comments, media, menus, themes, plugins and several languages, without a PHP stack, a plugin zoo or a weekly security patch. It ships as **one statically linked executable** with its templates, assets and migrations embedded. You run it behind nginx on Linux with PostgreSQL. That's the whole stack.

> **Status:** pre-release. The core described below works and is tested. The items under [Roadmap](#roadmap) are under active development. Expect breaking changes until 1.0.

## Highlights

- **Simple to run:** one binary (about 37 MB, no CGO), PostgreSQL, nginx. No Redis, no queue, no Node at runtime.
- **Secure by default:**
  - strict Content Security Policy with no inline scripts or styles;
  - Argon2id passwords, mandatory two-factor authentication for staff, passkeys;
  - plugins run in a WebAssembly sandbox;
  - SSRF-guarded outbound requests and an audit log.
- **Fast:** a language-aware page cache serves about 5,400 requests per second for a cached page on a laptop (Apple Silicon, `ab -c 32`, without nginx). The front end is vanilla JavaScript plus a compiled Tailwind stylesheet.
- **Multilingual:** linked translations, `/<lang>/` URLs, `hreflang`, and per-language feeds, menus, widgets and categories.
- **AI where it helps:** an optional writing assistant that works with Claude, any OpenAI-compatible server (including local models in LM Studio, Ollama or llama.cpp) and Gemini. Sites are also AI-ready: `llms.txt`, Markdown twins of every post, and an AI crawler policy.
- **Themes with light and dark modes**, built on [Basecoat UI](https://basecoatui.com) and [Lucide](https://lucide.dev) icons. Themes and plugins load at runtime: no recompiling.

## Features

### Content
- **Posts, pages and taxonomy:** posts and hierarchical pages, categories and tags, scheduled publishing, private and password-protected posts, revisions.
- **Editor:** visual, Markdown and preview modes over the same Markdown source.
  - Pasting from Word, Google Docs, LibreOffice or a web page keeps headings, lists, tables, links, images and emphasis, and drops fonts, colours and stray markup.
  - Keyboard shortcuts and Markdown autoformatting, plus its own undo stack.
  - Output is sanitised HTML (goldmark + bluemonday).
  - Autosave every few seconds, an edit lock with takeover so two people never overwrite each other, and conflict detection on save.
- **Media library:** uploads are checked by content type, given image variants, and stored by hash so duplicates are detected. Uploaded files are served with a sandboxing CSP.
- **Comments:** threaded, with moderation, per-post open/closed settings and rate limiting.
- **Menus and widgets** per theme location and per language.
- **WordPress importer:** reads a WXR export as it uploads. It brings in authors, categories, tags, posts, pages, comments and media, and creates redirects from the old URLs. It runs in the background, with a dry-run mode and a report.

### Multilingual
- Each post can have linked translations, and every language lives under its own prefix (`/en/`, `/es/`…).
- `hreflang` alternates in pages and sitemaps, language switchers in every theme, and per-language RSS, Atom and JSON feeds.
- Menus, widgets and category names can be translated. Page cache keys include the language.

### Themes
Five built-in themes, each with light and dark modes:

| Theme | Style |
|---|---|
| **Apple** | Clean, typographic, generous whitespace |
| **Neumorphism** | Soft extruded surfaces |
| **Glassmorphism** | Frosted, translucent layers |
| **Bento Grid** | Modular card layout |
| **Dashboard** | SaaS-style, sidebar navigation |

Themes are Go templates plus static files. They can be previewed before you switch, and extra themes are loaded from a directory at runtime.

### SEO and sharing
- XML sitemaps with language alternates; canonical URLs; Open Graph and Twitter cards.
- **Automatic Open Graph images:** a branded 1200×630 card is rendered for every post with no featured image, cached on disk and served immutable.
- **Redirect manager:** 301, 302 and 410 rules with hit counts. A 301 is created automatically when a published post changes slug, type or language, without chains or loops.
- RSS, Atom and JSON Feed, with optional WebSub.

### AI-ready sites
- `/llms.txt` and `/llms-full.txt` describe the site to language models.
- Every post has a clean Markdown twin (`/blog/slug.md`) with front matter and a canonical link.
- **AI access policy:** *open*, *no training* or *block*. It controls `robots.txt` (AI training and answer bots, `Content-Signal`) and the AI-facing endpoints.

### AI writing assistant (optional)
- **Providers:** Claude (Anthropic), OpenAI or any OpenAI-compatible server, and Gemini. Local models work over `http://localhost`.
- **Tasks:**
  - excerpt summary; SEO title, description, slug and keywords;
  - translation into a linked draft;
  - header and inline images, where the provider supports images;
  - drafting, improving, shortening, outlining and continuing text.
- **A local style linter** flags clichés and filler phrases ("in today's world…", "it is crucial to note…"), both in your writing and in the model's.
- **Controls:**
  - the API key is encrypted at rest;
  - a minimum role, plus monthly per-user request and image budgets;
  - every request is metered and written to the audit log;
  - long tasks run as background jobs.

### Users and security
- **Roles:** administrator, editor, author, contributor and subscriber, with capability checks on every admin route.
- **Passwords:** hashed with Argon2id. Logins are rate-limited and locked out after failures; changing a password closes the user's other sessions.
- **Two-factor authentication** (TOTP) is mandatory for staff by default. **Passkeys (WebAuthn)** work for passwordless sign-in or as the second factor, with user verification required.
- **Hardening:**
  - **Security headers:** a strict CSP with no inline scripts, styles or style attributes, plus HSTS, `X-Content-Type-Options`, `X-Frame-Options`, `Referrer-Policy`, `Permissions-Policy` and COOP.
  - **Request protection:** cross-origin request protection and CSRF tokens; SSRF protection on every outbound request (private, loopback and metadata ranges are blocked).
  - **Logging:** an audit log of sensitive actions.
- **Privacy:** no third-party requests from public pages and no tracking cookies.

### Plugins
Plugins are WebAssembly modules run by [wazero](https://wazero.io) inside a sandbox:
- no filesystem, no sockets, and no environment;
- memory caps, per-call timeouts, a concurrency limit, and an automatic pause after repeated failures;
- **capability permissions:** private key-value storage, read-only access to published posts, and HTTP to named hosts only (SSRF-guarded);
- **approval** pins the module's SHA-256 and the permissions it was granted. If either changes, the plugin is disabled until reviewed again;
- **hooks:** content filters, shortcodes, widgets, head tags, asynchronous events and an hourly cron;
- **output** is sanitised before it reaches a page.

Plugins are written in Go with the [plugin SDK](pluginsdk) and compiled with the standard toolchain (`GOOS=wasip1 GOARCH=wasm`).

### Operations
- **Single binary** with embedded templates, assets and SQL migrations. Migrations run on start.
- Listens on TCP or a **Unix socket** (recommended behind nginx). Structured logs in text or JSON.
- **Encrypted backups:** `sap13ns backup create|list|verify|restore|prune` dumps the database and uploads into one archive sealed with AES-256-GCM (STREAM construction, Argon2id key derivation), with daily, weekly and monthly retention and a systemd timer.
- **Signed self-update:** administrators see a notice in the admin when a release is out. `sap13ns update` then downloads it, checks the Ed25519-signed `SHA256SUMS`, and swaps the binary atomically, keeping the previous one for rollback (see [docs/RELEASES.md](docs/RELEASES.md)).
- **Cookie-less analytics:**
  - page views, unique visitors, top pages, sources, browsers, systems, devices and languages, with a daily chart in the admin;
  - visitors are counted with a daily rotating salt that lives only in memory, so no IP address, user agent or identifier is ever stored, and no consent banner is needed;
  - bots, prefetches and signed-in staff are not counted.

## Quick start

Requirements: Go 1.27+, Node.js (only to build the CSS), PostgreSQL 16+ (18 recommended).

```sh
# Build the front-end assets and the binary
npm ci && npm run build
go build -o bin/sap13ns ./cmd/sap13ns

# Create a database and a configuration file
createdb sap13ns
cat > sap13ns.env <<EOF
SAP13NS_DATABASE_URL=postgres:///sap13ns?host=/var/run/postgresql
SAP13NS_SECRET_KEY=$(./bin/sap13ns keygen)
SAP13NS_BASE_URL=http://localhost:8080
SAP13NS_LISTEN=127.0.0.1:8080
EOF

# Create the first administrator and start the server
./bin/sap13ns user create -env sap13ns.env -username admin -email you@example.com -role admin
./bin/sap13ns serve -env sap13ns.env
```

Open `http://localhost:8080/admin`. You'll be asked to set up two-factor authentication on first sign-in.

### Configuration
All settings are environment variables, which can be given in an env file with `-env`:

| Variable | Purpose |
|---|---|
| `SAP13NS_DATABASE_URL` | PostgreSQL connection string |
| `SAP13NS_SECRET_KEY` | Instance secret (`sap13ns keygen`); encrypts and signs sessions, keys and tokens |
| `SAP13NS_BASE_URL` | Public URL of the site |
| `SAP13NS_LISTEN` | `host:port` or `unix:/run/sap13ns/sap13ns.sock` |
| `SAP13NS_DATA_DIR`, `SAP13NS_UPLOADS_DIR`, `SAP13NS_THEMES_DIR`, `SAP13NS_PLUGINS_DIR` | Storage locations |
| `SAP13NS_SMTP_HOST`, `_PORT`, `_USERNAME`, `_PASSWORD`, `_FROM` | Outgoing e-mail |
| `SAP13NS_TRUSTED_PROXIES` | Proxies whose `X-Forwarded-For` is trusted |
| `SAP13NS_MAX_UPLOAD_MB`, `SAP13NS_DB_MAX_CONNS`, `SAP13NS_LOG_LEVEL`, `SAP13NS_LOG_FORMAT` | Limits and logging |

### Commands

```
sap13ns serve            Run the web server (default)
sap13ns migrate          Apply database migrations
sap13ns keygen           Generate a secret key
sap13ns user create      Create a user
sap13ns user reset-password
sap13ns backup create|list|verify|restore|prune
sap13ns update           Install the latest signed release (-check, -yes, -force)
sap13ns plugin hash      Print the SHA-256 of a plugin module
sap13ns version
```

## Development

```sh
npm run build                     # CSS (Tailwind v4 + Basecoat) and icons
go test ./...                     # unit tests
go vet ./...
```

Some store tests need a scratch database: set `SAP13NS_TEST_DATABASE_URL`. Set `SAP13NS_DEV=true` in your env file to reload templates from disk and log e-mails instead of sending them.

Repository layout:

```
cmd/sap13ns        command-line entry point
internal/          web server, store, auth, AI, plugins, media, backup, importer…
ui/admin           admin templates and styles
ui/shared          templates shared by all themes
ui/themes          built-in themes
pluginsdk          Go SDK for plugins (separate module)
plugins            official plugins
site               the sap13ns.site theme and content, built with sap13ns
deploy             nginx and systemd files
docs               operator and developer documentation
```

## Roadmap

In active development:

- **Plugins:** upload from the admin with an automatic security scan, install disabled, on/off switch. Also a plugin starter kit (`sap13ns plugin new`) and ten official plugins: table of contents, callouts, privacy-friendly embeds, Schema.org data, auto-posting to social networks, related posts, an announcement bar, external and affiliate links, comment antispam, and contact forms.
- **Site assistant:** a chatbot for visitors that answers from your own posts using PostgreSQL + pgvector hybrid search, with cited sources and a per-post opt-out, plus a read-only MCP endpoint for external AI agents.
- **Theme builder:**
  - a visual customiser for colours, fonts, radius, density, header, footer and home layout, with live preview;
  - a block-based template builder.
- **Multisite:** one account and many isolated sites, each with its own database schema, domains, themes and plugins.
- **Hosting at sap13ns.site:** free personal sites and paid commercial plans.

## License

sap13ns is released under the [Common Public Attribution License 1.0](LICENSE) (CPAL-1.0), an OSI-approved licence. You may use, modify and redistribute it, including over a network. Keep the **"Powered by sap13ns"** attribution linking to [sap13ns.site](https://sap13ns.site), which appears by default in the site footer and on the sign-in pages.

Copyright © 2026 BAUBER.XYZ SL.
