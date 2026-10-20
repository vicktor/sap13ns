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
  <a href="docs/SOCIAL.md">Social</a> ·
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
- **Free to leave:** a site's backup is also a WordPress import, files included. WordPress's own importer reads it as it is ([docs/MOVING-TO-WORDPRESS.md](docs/MOVING-TO-WORDPRESS.md)).

## Features

### Content
- **Posts, pages and taxonomy:** posts and hierarchical pages, categories and tags, scheduled publishing, private and password-protected posts, revisions.
- **Editor:** visual, Markdown and preview modes over the same Markdown source.
  - Pasting from Word, Google Docs, LibreOffice or a web page keeps headings, lists, tables, links, images and emphasis, and drops fonts, colours and stray markup.
  - Keyboard shortcuts and Markdown autoformatting, plus its own undo stack.
  - **Videos:** a YouTube or Vimeo link alone on a line (or the **Video** button, or the `[video]` shortcode) becomes a responsive click-to-load player with its poster and title. Nothing loads from YouTube or Vimeo until the visitor presses play, and then from youtube-nocookie.com or Vimeo with do not track. No plugin needed.
  - **Images:** drag a corner to resize an image (in tenths of the column, keeping its shape), and float it left or right with the text around it, centre it or make it full width: `![alt](src){width=480 align=left}` in Markdown.
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
  - the featured images and the media library, as attachments with their URLs, each attached to the post it illustrates;
  - the menus, as WordPress menus (one per location and language): links to exported posts, pages, categories and tags point at the imported ones;
  - replies to a comment that is left out (pending, spam) hang from the nearest exported one.
- **What WordPress ignores, sap13ns keeps.** Extra fields carry the Markdown source, each post's language and its links to its translations, term translations, meta descriptions and redirects. A move between two sap13ns sites keeps all of them.
- **A `.zip`** with that file, the uploads directory, and a Markdown copy of every post and page (YAML front matter, then the stored Markdown).
- **No personal data by default.** Commenters' e-mail and IP addresses and authors' e-mail addresses are only exported with **Include commenters' personal data**. Passwords, two-factor secrets, passkeys, sessions, plugin secrets, API keys and the site secret are never exported.
- **Any size:** exports stream, so a site is never held in memory. A large `.zip` is prepared in the background and downloaded from a link that works once, for the administrator who asked, within 24 hours. A site keeps one such export waiting, and one starts only when the disk, and on a network the plan's storage, has room for it. Every export is in the audit log.
- **Not exported:** widgets, settings, themes, designs (the customiser exports those), plugins, users' photos and revisions. A [backup](#operations) holds all of the site.

### Multilingual
- Each post can have linked translations, and every language lives under its own prefix (`/en/`, `/es/`…).
- `hreflang` alternates in pages and sitemaps, language switchers in every theme, and per-language RSS, Atom and JSON feeds.
- Menus, widgets and category names can be translated. Page cache keys include the language.
- **The visitor's language first:** the home page opens in the browser's language when the site has it, and in the one the visitor last picked in the language switch after that (a one-year `sap13ns-lang` cookie that holds only the code, listed in the cookie settings). Other pages keep their address.
- The admin panel is in English and Spanish. It follows the browser until each user picks a language from the menu beside the light/dark toggle, or on their profile; it changes nothing on the public site. The sign-in pages follow the page that links to them (`?lang=`), else the browser.

### Themes
Six built-in themes, each with light and dark modes:

| Theme | Style |
|---|---|
| **Apple** | Clean, typographic, generous whitespace |
| **Neumorphism** | Soft extruded surfaces |
| **Glassmorphism** | Frosted, translucent layers |
| **Bento Grid** | Modular card layout |
| **Dashboard** | SaaS-style, sidebar navigation |
| **Onepage** | A one-page site: sticky header with a section menu, editorial serif type, the blog at `/blog` |

Themes are Go templates plus static files. They can be previewed before you switch, and extra themes are loaded from a directory at runtime.

- **Not only blogs:** a theme can be a landing page or a one-page site. It ships `templates/front.html`, **Settings → Reading** shows it at `/`, and the posts move to `/blog`. The blog pages a theme does not ship (posts, pages, archives, search) render with shared fallbacks inside its own layout. Menu links such as `#pricing` scroll to the section with that anchor, and lead back to it (`/#pricing`) from any other page; the link of the section in view is marked as you scroll.

- **Theme uploads:** an administrator uploads a theme as a `.zip` in **Appearance → Themes** (or with `sap13ns theme install`). It is checked before it is installed: only passive static files, no scripts or SVG, stylesheets through the CSS checker, templates parsed with the real functions and checked for loops, recursion, secrets, form targets, inline code and the footer credit. It arrives inactive for a preview, a bad new version leaves the active one in place, and uploaded themes render within output and time limits ([docs/THEMES.md](docs/THEMES.md#uploading-a-theme)).
- **WordPress themes:** a WordPress block theme (Twenty Twenty-Five, Ollie…) uploads as it is: it is converted into a sap13ns theme, with a report of what was kept and left out, its palette wired to the customiser and a derived dark mode. None of its PHP runs ([docs/THEME-IMPORT.md](docs/THEME-IMPORT.md)).

- **Customiser:** colours, fonts (self-hosted WOFF2), radius, density, header and footer, with a live preview. Changes stay a draft until published, with ten revisions and export/import.
- **Page builder:** 26 blocks (header, menu, hero, post list, content, comments, widget areas, rich text, your own HTML, image, button, …) arranged per region and page type, with drag and drop, undo and a live preview. Page types you have not built keep the theme's own template until you start from a preset or an empty layout. Sections and HTML blocks take CSS classes for the theme's custom CSS, which the builder edits too. HTML is sanitised and the CSS checked, so neither can run scripts.
- **Landing pages:** seven landing blocks (features, figures, testimonials, pricing written by hand, questions and answers, logos, call to action), section anchors for one-page menus, and a front page region that starts from a **Landing** or **One-page site** preset in any theme.

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
- **Setup:** pick a provider and turn it on in **Settings → AI provider**. The same provider writes the site assistant's answers.
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
- **Turning it on:** switch on **Show the assistant on the site** in **Settings → Site assistant**. Visitors see the chat once the AI provider is on too; the page, the AI provider page and the dashboard show whether it is on, off or waiting for the provider.
- **The chat button:** a round button in a bottom corner of every public page opens the chat window. You choose the corner (right or left), its height (normal, raised above cookie bars, or high) and an optional short label beside it, translatable per language. Phones get a full-screen chat.
- **Search:** hybrid PostgreSQL full-text and [pgvector](https://github.com/pgvector/pgvector) search over Markdown-aware passages, merged with reciprocal rank fusion and favouring the visitor's language. Without pgvector it falls back to full-text search.
- **Indexing:** drafts, scheduled and private posts are never indexed, and any post can be hidden from the assistant with one switch in the editor. The index updates as you publish.
- **The front page and the plans:** a theme's own front page or the page builder's sections (a one-page or landing site) are not posts, so the assistant reads the front page as it renders, split at its headings, and uses the sections that match the question. On a network's portal it also gets the hosting plans with their current prices.
- **Limits:** per-IP and daily or monthly budgets, with known bots refused.
- **MCP:** a read-only [Model Context Protocol](https://modelcontextprotocol.io) endpoint (`/mcp`, tools `search_posts` and `get_post`) lets external AI agents search the same index, optionally behind a bearer token.

### Users and security
- **Roles:** administrator, editor, author, contributor and subscriber, with capability checks on every admin route.
- **Passwords:** hashed with Argon2id. Logins are rate-limited and locked out after failures; changing a password closes the user's other sessions.
- **Two-factor authentication** (TOTP) is mandatory for staff by default. **Passkeys (WebAuthn)** work for passwordless sign-in or as the second factor, with user verification required.
- **Sign-in address:** `/login` by default, or one the administrator chooses under **Settings → Accounts & security**, such as `/entrar` or `/team/access`. The old address then answers like a page that does not exist. **Hide the administration from visitors** answers `/admin` the same way to anyone who is not signed in. This keeps the bots that try `/login` away, but the address is not a secret: the rate limits and the second factor are what protect the sign-in. If you lose the address, `sap13ns settings login-path` prints it on the server, and `-reset` puts back `/login`.
- **Photos:** users upload a photo, which is cropped to a square and re-encoded without its metadata, or show their Gravatar. The server fetches the Gravatar with a hash of the e-mail address and serves a copy, so visitors' browsers never contact Gravatar. Commenters' Gravatars are opt-in. Without either, the initials are shown.
- **Hardening:**
  - **Security headers:** a strict CSP with no inline scripts, styles or style attributes, plus HSTS, `X-Content-Type-Options`, `X-Frame-Options`, `Referrer-Policy`, `Permissions-Policy` and COOP.
  - **Request protection:** cross-origin request protection and CSRF tokens; SSRF protection on every outbound request (private, loopback and metadata ranges are blocked).
  - **Logging:** an audit log of sensitive actions.
- **Privacy:** no third-party requests from public pages and no tracking cookies.
- **Cookie consent:** built in, and built from what the site really uses. The core lists its own cookies and storage, and plugins declare theirs in their manifest, so **Settings → Privacy and cookies** and the "Cookies" link at the bottom of every page show the same generated list, by category (necessary, preferences, analytics, external media and marketing).
  - A category appears only when something uses it. Every new visitor sees the banner: it asks when something optional needs consent, and on a site with only necessary cookies it is a notice with "Got it" and the list.
  - Accept all, reject all (just as prominent) and customise, with a toggle per category and a table of every cookie. Nothing optional loads before consent: video players stay click-to-load placeholders that name the provider, and pressing play allows that one video, or the provider everywhere with "Always allow".
  - The choice stays in the visitor's browser for six months, with its date and the version of the list; a new category asks again. Cached pages stay the same for everyone.
  - Per-language texts and a link to your cookie policy; styled by every theme in light and dark; accessible with the keyboard and screen readers.

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

**Official plugins** ship inside the binary (`make build` packs them). Install them from Admin → Plugins → **Official plugins**, or with `sap13ns plugin install -official <id>`. They go through the same steps as an upload, and an **Update** button appears when a newer binary carries a newer version.


| Plugin | What it does |
|---|---|
| [toc](plugins/toc) | Table of contents |
| [callouts](plugins/callouts) | Callouts and GitHub-style alerts |
| [external-links](plugins/external-links) | External and affiliate links |
| [announcement](plugins/announcement) | Announcement bar |
| [embeds](plugins/embeds) | Click-to-load YouTube (no-cookie), Vimeo (do-not-track) and TikTok players |
| [youtube](plugins/youtube) | The latest videos of a YouTube channel or playlist as a grid or a carousel, played only after a click |
| [schema](plugins/schema) | Schema.org JSON-LD for every public page |
| [social](plugins/social) | Posts new articles to Bluesky and Mastodon |
| [x-autopost](plugins/x-autopost) | Posts new articles to X |
| [linkedin](plugins/linkedin) | Shares new articles on your LinkedIn profile, with an article card |
| [instagram](plugins/instagram) | Publishes new articles' featured images to Instagram, and shows your latest Instagram posts as a carousel or a grid |
| [related](plugins/related) | Related posts by shared tags and categories |
| [antispam](plugins/antispam) | Holds or rejects spam comments, optionally with StopForumSpam |
| [contact](plugins/contact) | Contact form e-mailed to the site, with Reply-To |

### Networks of sites (optional)
With `SAP13NS_MULTISITE=true`, one process serves a network: a portal and any number of sites. The guide is [docs/MULTISITE.md](docs/MULTISITE.md).
- **Isolated sites:** each site has its own PostgreSQL schema, users, settings, files, plugins and themes, and picks its theme from the built-in ones, the network's, or, on the plans that allow it, the ones it uploads. It is served at `<slug>.<SITES_DOMAIN>` and on its own domains. Idle sites are unloaded from memory and load again on their next request.
- **Portal and single sign-on:** people sign up on the portal (when the operator opens registration), request sites and manage them from **My sites**. They sign in to every site they belong to through the portal, which requires a second factor of every account. Network administrators open a site they are not a member of only while its owner allows support access (24 hours, 7 days or until revoked), or with an emergency access for a legal order or a serious security incident: four hours at most, with a justification e-mailed to the owner. Either way they use a separate operator account, and every sign-in is audited.
- **Plans and billing:**
  - Personal (free, approved by the operator), Pro and Business, with storage, custom-domain, plugin-upload and theme-upload limits;
  - one subscription pays for one site, through Stripe Checkout and the Customer Portal ([docs/BILLING.md](docs/BILLING.md));
  - a site whose payment lapses is held (its public pages answer 503) and never deleted.
- **Custom domains:** proven with a DNS TXT record and checked again every day. TLS comes from nginx with a wildcard certificate, from Caddy's on-demand TLS, or from the built-in ACME client.
- **Nightly backups:** every site is backed up each night, a few at a time and within a time budget, without loading it. Its administrators download and restore them under **Tools → Backups**. Backups are bounded by the plan's storage and the free disk, and the network's sites page shows the latest one of each site ([docs/MULTISITE.md](docs/MULTISITE.md#backups)).
- **Plugin catalogue and review:** sites install the plugins the operator offers, and the official plugins the operator does not hide. Uploads, on the plans that allow them, wait for the operator to review that exact build ([docs/PLUGINS.md](docs/PLUGINS.md#networks-of-sites)).
- **Theme uploads:** on Pro and Business, sites upload their own themes, checked automatically with no operator review. Their files count against the plan's storage ([docs/MULTISITE.md](docs/MULTISITE.md#plugins-and-themes)).

### Operations
- **Single binary** with embedded templates, assets and SQL migrations. Migrations run on start.
- Listens on TCP or a **Unix socket** (recommended behind nginx). Structured logs in text or JSON.
- **Backups that WordPress can read:** every site keeps its own backups under **Tools → Backups**: one `.zip` with a WordPress export file, the uploads in WordPress's layout, and what only sap13ns uses (settings, users and roles, menus, widgets, designs, redirects, plugin settings). WordPress imports it as it is ([docs/MOVING-TO-WORDPRESS.md](docs/MOVING-TO-WORDPRESS.md)), and sap13ns restores it in one transaction, after a safety backup, with the checks of the admin pages. A network backs up every site each night.
- **Encrypted backups:** `sap13ns backup create|list|verify|restore|prune|decrypt` also dumps the database and uploads into one archive, for disaster recovery. Both kinds can be sealed with AES-256-GCM (STREAM construction, Argon2id key derivation), with daily and weekly (and for the database, monthly) retention and a systemd timer.
- **Signed self-update:** administrators see a notice in the admin when a release is out. `sap13ns update` then downloads it, checks the Ed25519-signed `SHA256SUMS`, and swaps the binary atomically, keeping the previous one for rollback (see [docs/RELEASES.md](docs/RELEASES.md)).
- **Cookie-less analytics:**
  - page views, unique visitors, top pages, sources, browsers, systems, devices and languages, with a daily chart in the admin;
  - optionally, the visitors' countries on a world map, from a local country database that `sap13ns geoip update` keeps up to date. Only the country is kept ([docs/DEPLOY.md](docs/DEPLOY.md#visitors-by-country));
  - visitors are counted with a daily rotating salt that lives only in memory, so no IP address, user agent or identifier is ever stored, and no consent banner is needed;
  - bots, prefetches and signed-in staff are not counted.

## Quick start

Requirements: Go 1.27+, Node.js (only to build the CSS and the JavaScript bundles), PostgreSQL 16+ (18 recommended).

```sh
# Build the front-end assets and the binary, with the official plugins inside
npm ci && npm run build
make build                        # or: go build -o bin/sap13ns ./cmd/sap13ns (no official plugins)

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

For a production server, run the guided installer, `sudo ./deploy/setup.sh`, from a release or a clone. It checks and installs PostgreSQL, pgvector, nginx and certbot, asks whether it is one site or a network, and sets up systemd, nginx, HTTPS, backups and the first administrator. [docs/DEPLOY.md](docs/DEPLOY.md) describes it and the manual steps.

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
sap13ns settings login-path   Print the sign-in address (-reset: back to /login, -set /PATH)
sap13ns backup create|list|verify|restore|prune|decrypt   The database and uploads
sap13ns backup create|list|prune|restore -format site      The site's own backups (-wp-base URL)
sap13ns export -o FILE.xml|FILE.zip   Export the content (-drafts, -pii, -no-media)
sap13ns update           Install the latest signed release (-check, -yes, -force)
sap13ns plugin new|pack|check   Start, package and scan a plugin
sap13ns plugin list|install     List or install the site's plugins (-official: the bundled ones)
sap13ns theme list|install|remove   List, install (checked as an upload) or remove the site's themes
sap13ns assistant reindex|status
sap13ns version

Networks of sites (SAP13NS_MULTISITE=true):
sap13ns network init     Create the network and its portal, and an administrator
sap13ns site create|list|suspend|resume|delete|purge
sap13ns site domain add|verify|primary|remove
sap13ns migrate|user|settings|plugin|theme|assistant|export|backup -site S   Work on one site (-site network: the portal)
```

## Development

```sh
npm run build                     # CSS (Tailwind v4 + Basecoat) and icons
make plugins                      # build, scan and pack the official plugins the binary embeds
go test ./...                     # unit tests
go vet ./...
```

Some store tests need a scratch database: set `SAP13NS_TEST_DATABASE_URL`. Set `SAP13NS_DEV=true` in your env file to reload templates from disk and log e-mails instead of sending them.

To try a [network of sites](docs/MULTISITE.md) on your machine (the portal, single sign-on and the admin's site switcher):

```sh
make dev-network                  # database sap13ns_net_dev, data/network.env, data/network
make dev ENV=data/network.env     # the portal on http://localhost:8139, the sites on http://<slug>.localhost:8139
```

It creates the network admin `admin` and the account `marta` (password `dev-password-123`, no second factor) and three sites with sample content: `cuaderno`, `recetas` and `studio`. Running it again only prints the addresses, and `make dev-network RESET=1` starts it over. It never touches the single site of `make dev` (`sap13ns_dev`, `data/dev.env`, port 8137). `NET_DB`, `NET_PORT`, `NET_ENV` and `NET_DATA` choose another database, port, env file and data directory.

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

- **Conversion:** a single-site install cannot yet be turned into a network, or moved into one.
- **Member invitations** by site owners.

## License

sap13ns is released under the [Common Public Attribution License 1.0](LICENSE) (CPAL-1.0), an OSI-approved licence. You may use, modify and redistribute it, including over a network. Keep the **"Powered by sap13ns"** attribution linking to [sap13ns.site](https://sap13ns.site), which appears by default in the site footer and on the sign-in pages.

The plugin SDK (`pluginsdk/`), the plugin skeleton (`plugins/skeleton/`, which `sap13ns plugin new` copies) and the official plugins in `plugins/` are released under the **MIT licence** instead; each folder carries its own `LICENSE`. A plugin built on them can use any licence, including a proprietary one.

Copyright © 2026 BAUBER.XYZ SL.
