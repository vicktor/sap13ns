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
  <a href="docs/DEPLOY.md">Deploy</a> ·
  <a href="docs/MULTISITE.md">Multisite</a> ·
  <a href="docs/PLUGINS.md">Plugins</a> ·
  <a href="docs/SECURITY.md">Security</a> ·
  <a href="docs/RELEASES.md">Releases</a> ·
  <a href="LICENSE">CPAL-1.0</a>
</p>

---

sap13ns is for people who want a blog or a company site with posts, pages, comments, media, menus, themes, plugins and several languages, without a PHP stack, a plugin zoo or a weekly security patch. It ships as **one statically linked executable** with its templates, assets and migrations embedded. You run it behind nginx on Linux with PostgreSQL. That's the whole stack.

> **Status:** pre-release. Everything described below works and is tested; the [Roadmap](#roadmap) lists what is still missing. Expect breaking changes until 1.0.

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
- **Many sites in one process (optional):** a network mode serves a portal and isolated sites, with single sign-on, plans, Stripe billing and custom domains ([docs/MULTISITE.md](docs/MULTISITE.md)).

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
- **WordPress importer:** reads a WXR export as it uploads. It brings in authors, categories, tags, posts, pages, comments and media, and creates redirects from the old URLs. It runs in the background, with a dry-run mode and a report. It also reads sap13ns exports, and keeps what they add (see [Export](#export)); their redirect rules are an option of the import, and only those that lead to the site's own addresses are kept.

### Export
**Tools → Export**, for the site's administrators, and `sap13ns export` take a site's content out: leaving should be as easy as arriving.
- **A WordPress export file (WXR 1.2)** that WordPress and sap13ns both import. It holds:
  - posts and pages, published and scheduled ones by default, with drafts, pending and private ones on request, narrowed by dates or an author if needed;
  - categories and tags, the authors (username, display name), approved comments (pending ones on request);
  - the featured images and the media library, as attachments with their URLs.
- **What WordPress ignores, sap13ns keeps.** Extra fields carry the Markdown source, each post's language and its links to its translations, term translations, meta descriptions and redirects. A move between two sap13ns sites keeps all of them.
- **A `.zip`** with that file, the uploads directory, and a Markdown copy of every post and page (YAML front matter, then the stored Markdown).
- **No personal data by default.** Commenters' e-mail and IP addresses and authors' e-mail addresses are only exported with **Include commenters' personal data**. Passwords, two-factor secrets, passkeys, sessions, plugin secrets, API keys and the site secret are never exported.
- **Any size:** exports stream, so a site is never held in memory. A large `.zip` is prepared in the background and downloaded from a link that works once, for the administrator who asked, within 24 hours. A site keeps one such export waiting, and one starts only when the disk, and on a network the plan's storage, has room for it. Every export is in the audit log.
- **Not exported:** menus, widgets, settings, themes, designs (the customiser exports those), plugins and revisions.

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

- **Theme uploads:** an administrator uploads a theme as a `.zip` in **Appearance → Themes** (or with `sap13ns theme install`). It is checked before it is installed: only passive static files, no scripts or SVG, stylesheets through the CSS checker, templates parsed with the real functions and checked for loops, recursion, secrets, inline code and the footer credit. It arrives inactive for a preview, a bad new version leaves the active one in place, and uploaded themes render within output and time limits ([docs/THEMES.md](docs/THEMES.md#uploading-a-theme)).

- **Customiser:** colours, fonts (self-hosted WOFF2), radius, density, header and footer, with a live preview. Changes stay a draft until published, with ten revisions and export/import.
- **Page builder:** 18 blocks (header, menu, hero, post list, content, comments, widget areas, rich text, image, button, …) arranged per region and page type. Page types you have not built keep the theme's own template.

See [docs/THEMES.md](docs/THEMES.md) for writing a theme.

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

### Site assistant (optional)
- **A chatbot for visitors** that answers from your own posts and pages and cites them. It streams its answers, and a no-JavaScript page at `/ask` works too.
- **Search:** hybrid PostgreSQL full-text and [pgvector](https://github.com/pgvector/pgvector) search over Markdown-aware passages, merged with reciprocal rank fusion and favouring the visitor's language. Without pgvector it falls back to full-text search.
- **Indexing:** drafts, scheduled and private posts are never indexed, and any post can be hidden from the assistant with one switch in the editor. The index updates as you publish.
- **Limits:** per-IP and daily or monthly budgets, with known bots refused.
- **MCP:** a read-only [Model Context Protocol](https://modelcontextprotocol.io) endpoint (`/mcp`, tools `search_posts` and `get_post`) lets external AI agents search the same index, optionally behind a bearer token.

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
- **capability permissions:**
  - private key-value storage and read-only access to published posts;
  - HTTP to named hosts only (SSRF-guarded);
  - e-mail to the administrator or configured addresses only;
  - commenter data, scripts and CSP additions, each flagged as high risk;
- **hooks:** content filters, shortcodes, widgets, head tags and JSON-LD, forms, comment checks, asynchronous events and an hourly cron;
- **output** is sanitised before it reaches a page.

**Installing:**
1. Upload a `.zip` package in Admin → Plugins.
2. The package is unpacked safely, then scanned: manifest, WebAssembly imports and exports, embedded strings, assets, and a sandboxed dry run of every hook. Critical findings block it, and high-risk ones need an explicit confirmation.
3. It is installed **disabled**. Enabling it pins the module's and assets' SHA-256 and the permissions granted. If any of them changes, the plugin stops until it is reviewed again.

Plugins are written in Go with the [plugin SDK](pluginsdk) and compiled with the standard toolchain (`GOOS=wasip1 GOARCH=wasm`). `sap13ns plugin new` starts a project from the [skeleton](plugins/skeleton), and `sap13ns plugin pack` builds, scans and packages it. See [docs/PLUGINS.md](docs/PLUGINS.md) for the full contract.

**Official plugins:**

| Plugin | What it does |
|---|---|
| [toc](plugins/toc) | Table of contents |
| [callouts](plugins/callouts) | Callouts and GitHub-style alerts |
| [external-links](plugins/external-links) | External and affiliate links |
| [announcement](plugins/announcement) | Announcement bar |
| [embeds](plugins/embeds) | Click-to-load YouTube (no-cookie) and Vimeo (do-not-track) players |
| [schema](plugins/schema) | Schema.org JSON-LD for every public page |
| [social](plugins/social) | Posts new articles to Bluesky and Mastodon |
| [related](plugins/related) | Related posts by shared tags and categories |
| [antispam](plugins/antispam) | Holds or rejects spam comments, optionally with StopForumSpam |
| [contact](plugins/contact) | Contact form e-mailed to the site, with Reply-To |

### Networks of sites (optional)
With `SAP13NS_MULTISITE=true`, one process serves a network: a portal and any number of sites. The guide is [docs/MULTISITE.md](docs/MULTISITE.md).
- **Isolated sites:** each site has its own PostgreSQL schema, users, settings, files, plugins and themes, and picks its theme from the built-in ones, the network's, or, on the plans that allow it, the ones it uploads. It is served at `<slug>.<SITES_DOMAIN>` and on its own domains. Idle sites are unloaded from memory and load again on their next request.
- **Portal and single sign-on:** people sign up on the portal (when the operator opens registration), request sites and manage them from **My sites**. They sign in to every site they belong to through the portal. Operators reach any site with a separate, audited account.
- **Plans and billing:**
  - Personal (free, approved by the operator), Pro and Business, with storage, custom-domain, plugin-upload and theme-upload limits;
  - one subscription pays for one site, through Stripe Checkout and the Customer Portal ([docs/BILLING.md](docs/BILLING.md));
  - a site whose payment lapses is held (its public pages answer 503) and never deleted.
- **Custom domains:** proven with a DNS TXT record and checked again every day. TLS comes from nginx with a wildcard certificate, from Caddy's on-demand TLS, or from the built-in ACME client.
- **Plugin catalogue and review:** sites install the plugins the operator offers. Uploads, on the plans that allow them, wait for the operator to review that exact build ([docs/PLUGINS.md](docs/PLUGINS.md#networks-of-sites)).
- **Theme uploads:** on Pro and Business, sites upload their own themes, checked automatically with no operator review. Their files count against the plan's storage ([docs/MULTISITE.md](docs/MULTISITE.md#plugins-and-themes)).

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

For a production server (systemd, nginx, TLS, backups), follow [docs/DEPLOY.md](docs/DEPLOY.md) or run `deploy/install.sh`.

### Configuration
All settings are environment variables, which can be given in an env file with `-env`:

| Variable | Purpose |
|---|---|
| `SAP13NS_DATABASE_URL` | PostgreSQL connection string |
| `SAP13NS_SECRET_KEY` | Instance secret (`sap13ns keygen`); encrypts and signs sessions, keys and tokens |
| `SAP13NS_BASE_URL` | Public URL of the site |
| `SAP13NS_LISTEN` | `host:port` or `unix:/run/sap13ns/sap13ns.sock` |
| `SAP13NS_SOCKET_GROUP` | Group that may connect to the Unix socket (nginx's, e.g. `sap13ns-web`) |
| `SAP13NS_DATA_DIR`, `SAP13NS_UPLOADS_DIR`, `SAP13NS_THEMES_DIR`, `SAP13NS_PLUGINS_DIR` | Storage locations |
| `SAP13NS_SMTP_HOST`, `_PORT`, `_USERNAME`, `_PASSWORD`, `_FROM` | Outgoing e-mail |
| `SAP13NS_TRUSTED_PROXIES` | Proxies whose `X-Forwarded-For` is trusted |
| `SAP13NS_MAX_UPLOAD_MB`, `SAP13NS_DB_MAX_CONNS`, `SAP13NS_LOG_LEVEL`, `SAP13NS_LOG_FORMAT` | Limits and logging |

A network of sites adds these. [docs/MULTISITE.md](docs/MULTISITE.md#configuration) gives their defaults and rules.

| Variable | Purpose |
|---|---|
| `SAP13NS_MULTISITE` | `true` serves a network; `SAP13NS_BASE_URL` is then the portal's address |
| `SAP13NS_SITES_DOMAIN` | Domain of the sites' default hosts, `<slug>.<SITES_DOMAIN>` |
| `SAP13NS_UNKNOWN_HOST` | `404` or `redirect` (to the portal) for hosts the network does not serve |
| `SAP13NS_MAX_LOADED_SITES`, `SAP13NS_SITE_IDLE`, `SAP13NS_MEMORY_LIMIT_MB` | How many sites stay in memory, and for how long |
| `SAP13NS_DOMAIN_TARGET`, `SAP13NS_DOMAIN_ADDRESSES` | What custom domains point to (CNAME target, A/AAAA addresses) |
| `SAP13NS_TLS`, `SAP13NS_TLS_CERT`, `SAP13NS_TLS_KEY`, `SAP13NS_ACME_EMAIL`, `SAP13NS_ACME_DIRECTORY`, `SAP13NS_HTTP_LISTEN` | `proxy` (nginx or Caddy) or `autocert` (built-in ACME client) |
| `SAP13NS_STRIPE_*`, `SAP13NS_BILLING_GRACE_DAYS` | Billing ([docs/BILLING.md](docs/BILLING.md)) |

### Commands

```
sap13ns serve            Run the web server (default)
sap13ns migrate          Apply database migrations
sap13ns keygen           Generate a secret key
sap13ns user create      Create a user
sap13ns user reset-password
sap13ns backup create|list|verify|restore|prune
sap13ns export -o FILE.xml|FILE.zip   Export the content (-drafts, -pii, -no-media)
sap13ns update           Install the latest signed release (-check, -yes, -force)
sap13ns plugin new|pack|check   Start, package and scan a plugin
sap13ns plugin list|install     List or install the site's plugins
sap13ns theme list|install|remove   List, install (checked as an upload) or remove the site's themes
sap13ns assistant reindex|status
sap13ns version

Networks of sites (SAP13NS_MULTISITE=true):
sap13ns network init     Create the network and its portal, and an administrator
sap13ns site create|list|suspend|resume|delete|purge
sap13ns site domain add|verify|primary|remove
sap13ns migrate|user|plugin|theme|assistant|export -site S   Work on one site (-site network: the portal)
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

Still missing on a network of sites:

- **Per-site backups:** `sap13ns backup create` and `restore` refuse to run on a network for now; back it up with `pg_dump` and a copy of the data directory ([docs/MULTISITE.md](docs/MULTISITE.md#backups)). A single site's content leaves the network with its [export](#export).
- **Conversion:** a single-site install cannot yet be turned into a network, or moved into one.
- **Member invitations** by site owners.

## License

sap13ns is released under the [Common Public Attribution License 1.0](LICENSE) (CPAL-1.0), an OSI-approved licence. You may use, modify and redistribute it, including over a network. Keep the **"Powered by sap13ns"** attribution linking to [sap13ns.site](https://sap13ns.site), which appears by default in the site footer and on the sign-in pages.

The plugin SDK (`pluginsdk/`), the plugin skeleton (`plugins/skeleton/`, which `sap13ns plugin new` copies) and the official plugins in `plugins/` are released under the **MIT licence** instead; each folder carries its own `LICENSE`. A plugin built on them can use any licence, including a proprietary one.

Copyright © 2026 BAUBER.XYZ SL.
