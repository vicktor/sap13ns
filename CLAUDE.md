# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

sap13ns is a lightweight WordPress-style CMS. It ships as one Go binary (CGO off) with the whole UI embedded, and it runs on PostgreSQL behind nginx. README.md describes the features, docs/DEPLOY.md the production setup, docs/PLUGINS.md the plugin contract (manifest, hooks, host functions, permissions, scanner), docs/MULTISITE.md the optional network of sites (SSO, billing with docs/BILLING.md, plan limits, custom domains), and docs/SECURITY.md the security model that changes must keep.

## Commands

```sh
npm ci && npm run build      # Tailwind CSS (admin + every theme), Basecoat JS, Lucide icons
npm test                     # admin JS tests (node --test ui/admin/test/*.test.mjs)
go build -o bin/sap13ns ./cmd/sap13ns
go vet ./... && go test ./...
go test ./internal/web -run TestSafeNext     # a single test
go run ./scripts/devseed -env data/dev.env   # sample content; creates admin / dev-password-123
./bin/sap13ns serve -env data/dev.env        # dev server on 127.0.0.1:8137
make dev-network                             # a network: sap13ns_net_dev, data/network.env, 3 sites, admin / dev-password-123
make dev ENV=data/network.env                # portal on localhost:8139, sites on <slug>.localhost:8139; RESET=1 starts over
make i18n                                    # every UI string has a Spanish translation
make check                                   # vet + Go and JS tests + i18n + every plugin's tests
```

- **Asset outputs:** `npm run build` writes `ui/themes/*/static/theme.css`, `ui/static/admin.css`, `ui/static/vendor/basecoat/` and `internal/icons/lucide/`. These outputs are committed, because the binary embeds them and production never runs Node. Re-run the build after changing templates (Tailwind scans them for classes) or `scripts/icons.txt`. An icon must be listed there before `{{icon "name"}}` can use it.
- **Database tests:** they use `SAP13NS_TEST_DATABASE_URL` (default `postgres:///sap13ns_test?host=/tmp`). They drop and recreate the `public` schema, and are skipped when the database is unreachable (`internal/testutil`). A PostgreSQL advisory lock serialises them, also across packages.
- **Dev mode:** `SAP13NS_DEV=true` serves `ui/` from disk, so template and asset edits show up without a rebuild. Go changes still need a rebuild and restart.
- **Separate Go modules:** `pluginsdk/` and each official plugin under `plugins/<id>/` are their own modules, so `./...` at the root skips them. Build a plugin with `make` inside its directory (`GOOS=wasip1 GOARCH=wasm … -buildmode=c-shared`); test it with `go test ./...` there.

## Architecture

- **`cmd/sap13ns`:** the CLI: `serve`, `migrate`, `keygen`, `user`, `plugin`, `backup`, `assistant`, `update`, `version`, and on a network `network` and `site`. `serve` opens the site through `internal/siteapp` (config → store → plugin host → `web.New`, plus the background loops) and listens on TCP or a Unix socket (`SAP13NS_LISTEN=unix:…`, group set by `SAP13NS_SOCKET_GROUP`). With `SAP13NS_MULTISITE=true` it serves a network instead, and `user`, `plugin` and `assistant` need `-site`.
- **`internal/config`:** every setting comes from `SAP13NS_*` environment variables, or from an env file given with `-env`.
- **`internal/db`:** pgx pool plus embedded migrations `migrations/NNNN_name.sql`. They are applied in order on start, each in its own transaction. Add a new numbered file; never edit an applied one. On a network the same site migrations run in every site schema (`site_N`, plus `network` for the portal), and `internal/network/migrations` holds the network's own tables.
- **Multisite (`internal/network`, `internal/siteapp`):** off unless `SAP13NS_MULTISITE=true`; single-site keeps the plain code path.
  - Each site is an ordinary `web.Server` with its own config, plugin host and a store on a `db.Scoped` handle, which pins the site's schema on a shared pool. A connection that was never scoped fails closed (`internal/db/scoped.go`).
  - The registry loads sites on first request and unloads idle ones; the router maps the Host header to a site; the sweeps run cron, scheduled posts and maintenance for unloaded sites.
  - `web` sees the network only through nil-safe hooks (`NetworkHooks`, `SSOClient`, `Portal`, `PortalBilling`, `SiteLimits` in `server.go`).
- **`internal/store`:** all SQL lives here, one file per area. Runtime site settings, such as language, theme and AI configuration, are rows here, not config.
- **`internal/web`:** the HTTP layer, with all routes in `routes.go`.
  - **Server state:** `Server` holds the settings snapshot (`s.settings()`, swapped atomically by `ReloadSettings`), the template sets, the page cache and the subsystems (AI, assistant, analytics, updates, plugins).
  - **Admin routes** are registered with `admin(pattern, cap, handler)`, which enforces a capability from `internal/auth`. Object-level checks, such as an author editing only their own posts, happen inside the handlers.
  - **Admin pages** render with `s.renderAdmin(w, r, template, title, section, data)`. A POST redirects back with `s.redirect(w, r, to, kind, msg)`, which carries a flash message.
- **Page cache:** public GET routes are wrapped in `s.cached`, an in-memory cache keyed by language and path.
  - Requests with a session bypass it, and so does any query parameter not listed in `cacheableParams`.
  - Any write that affects public pages must call `s.contentChanged()`.
  - Per-visitor data must not end up in a cached body. The comment-form token is a placeholder filled in on every response (`fillTokens`).
- **Themes:** `internal/theme` loads `ui/themes/<slug>/` (built in) and `$SAP13NS_DATA_DIR/themes` (uploaded at runtime, no recompilation).
  - A theme is `theme.json` (options, menus, widget areas, swatches) plus `templates/` (`layout`, `home`, `single`, `page`, `archive`, `search`, `ask`, `404` and partials) and `src/theme.css` → `static/theme.css`.
  - Templates every theme shares are in `ui/shared/templates`; `head.html` and `widgets.html` are among them.
  - Every theme must work in light and dark mode.
- **i18n:**
  - **UI strings:** source strings are English. Translations go in `ui/i18n/<lang>.json` (English → translation). Call `{{T "..."}}` in templates and `s.t(...)` in Go. `scripts/i18ncheck` extracts every key (templates, Go calls, label tables) and `make i18n` fails when a translation is missing. Add the Spanish text to `es.json` (sorted, 2-space JSON; `internal/i18n/catalog_test.go` checks the format, placeholders and tags). A string the extractor cannot see goes in `scripts/i18ncheck/hand.go`.
  - **Content:** posts, terms and menus have per-language translations in the database. Public URLs carry a language prefix for non-default languages (`lang.go`).
- **Content pipeline:** `internal/content` renders Markdown with goldmark and sanitises it with bluemonday. Anything that reaches a page goes through this pipeline: posts, comments, plugin output, AI answers, WordPress imports.
- **Plugins:** see `internal/plugin` and `pluginsdk`.
  - Plugins are WebAssembly modules (wasip1) run in-process by wazero, with explicit permissions and approval pinned to the module's SHA-256.
  - Host functions are in `hostfuncs.go`; the upload-time scanner is in `scan.go`. Plugin fetches, imported media and AI image downloads go through the shared SSRF guard (`internal/netguard`); never write another address list.
- **Other subsystems:**

  | Package | What it does |
  |---|---|
  | `internal/ai` | Writing assistant. Adapters: Anthropic, OpenAI-compatible (also used for LM Studio, Ollama and llama.cpp), Gemini |
  | `internal/assistant` | Public site chatbot: chunking, hybrid pgvector and full-text search, cited answers. Exposed at `/api/assistant`, `/ask` and a read-only `/mcp` |
  | `internal/analytics` | Cookie-less daily aggregates |
  | `internal/update` | Signed self-update from GitHub releases |
  | `internal/backup` | Encrypted backups |
  | `internal/wpimport` | WordPress WXR import |
  | `internal/contentpack` | Loads `site/content` (the sap13ns.site corporate site) through `scripts/sitepack` |

## Rules that are easy to break

- **Strict CSP** (`script-src 'self'; style-src 'self'`):
  - no inline `<script>`, no `<style>` blocks, no `style=""` attributes, no inline event handlers;
  - behaviour goes in `ui/static/*.js` (vanilla JS, no frameworks), looks go in classes;
  - setting properties through CSSOM from JS is allowed;
  - fonts and assets are self-hosted, never loaded from a CDN.
- **Basecoat UI 1.x:** components are classes plus `data-variant` / `data-size`.
  - Button variants: `primary` (the default), `secondary`, `destructive`, `outline`, `ghost`, `link`. `default` is not a variant.
  - Alerts: `<div class="alert" data-variant="…">{{icon …}}<h2>…</h2><section>…</section></div>`.
- **Attribution:** `shared/footer-meta` renders the "Powered by sap13ns" attribution that the CPAL-1.0 licence (Exhibit B) requires. Every theme and layout must keep it.
- **Security-sensitive helpers:** use the existing ones instead of rolling new ones:
  - `safeNext` for redirects;
  - the CSRF field (`{{.CSRFField}}`) on every admin form; JSON endpoints require `application/json`;
  - `netguard` for outbound HTTP;
  - the media type sniffing in `internal/media`;
  - archive extraction limits.
- **Shared connections on a network:** SQL never changes session state (`SET search_path`, `SET ROLE`, `set_config` outside `internal/db`); `internal/db/lint_test.go` checks it. A site's data is read only through that site's scoped store, never the raw pool.
- **Dev server ports:** the dev server listens on 8137, and a local llama-server may be running on 8080. Leave both alone when testing, and use a separate port and a database copy instead.
