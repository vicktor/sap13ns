# Security

## Reporting a vulnerability

Please report vulnerabilities privately:
- through GitHub's **Report a vulnerability** button on the repository's Security tab;
- or by e-mail to **victor@bauber.xyz**, with "sap13ns security" in the subject.

Include the version (`sap13ns version`), the steps to reproduce, and the impact you see. Please don't open public issues for vulnerabilities.

We aim to acknowledge reports within three working days and to publish a fix, with a signed release and a security advisory, as soon as it is ready. Reporters are credited unless they prefer otherwise.

Supported versions: the latest release. Before 1.0, fixes are not backported.

## Security model

sap13ns assumes the server, its operator and its administrators are trusted. Everyone else is not: visitors, commenters, authors and contributors, plugin authors, AI providers and the content they return, and anything fetched from the network. The measures below enforce that boundary.

On a network of sites, a site's administrators are trusted on their own site only. They are not trusted by the other sites, the portal or the operator: see [Networks of sites](#networks-of-sites).

### Accounts and sessions
- **Passwords:**
  - hashed with Argon2id (time 2, memory 32 MiB) with a per-password random salt;
  - at least 10 characters, and the most common passwords are refused;
  - a password change or reset closes the user's other sessions.
- **Second factor:**
  - required for staff by default: TOTP, or a passkey (WebAuthn with user verification);
  - enrolling the first second factor closes the user's other sessions;
  - adding a passkey or removing the last second factor requires a recent sign-in or the password.
- **Rate limits and lockouts:**

  | Action | Limit |
  |---|---|
  | Sign-in, per IP | 20 per 15 minutes |
  | Sign-in, per account | 8 per 15 minutes |
  | Second step | 6 per 15 minutes |
  | Comments, per IP | 6 per 10 minutes |
  | Registration and password reset | 5 per hour |

  Limits per IP count an IPv6 /64 as one address. Repeated failures lock the account for a while. Every outcome is written to the audit log.
- **Sessions:**
  - random tokens stored only as hashes;
  - `HttpOnly` cookies, `Secure` over https, `SameSite=Lax`;
  - idle and absolute lifetimes;
  - users can list their sessions and end them.
- **Capabilities:** every admin route checks a capability of the user's role (administrator, editor, author, contributor, subscriber), and object-level checks keep authors to their own content.

### Requests
- **Cross-site requests:**
  - state-changing requests must pass Go's cross-origin protection (`Sec-Fetch-Site` / `Origin`) and carry the session's CSRF token;
  - JSON endpoints require `application/json`.
- **Response headers:**
  - a strict Content Security Policy: `script-src 'self'`, `style-src 'self'`, no inline scripts, styles or style attributes anywhere, `frame-ancestors 'none'`, `object-src 'none'`;
  - images and media may come from any `https:` address (`img-src 'self' data: https:`, `media-src 'self' https:`), so posts can embed external images. The other site learns that a page was viewed, by whom (IP address and browser) and, through `Referrer-Policy: strict-origin-when-cross-origin`, only from which site, not which page. Plugins can return external images too; the `csp` `img-src` permission adds nothing under this policy;
  - also HSTS (over https), `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy`, `Permissions-Policy` and `Cross-Origin-Opener-Policy`.
- **Redirects:** after sign-in, only local paths are accepted. They are parsed, and control characters and backslashes are rejected.
- **Redirect rules** (Tools → Redirects, and imports) never take an address the site serves itself: the home page and the language roots, sign-in and the other account pages, the admin, `/sso`, `/api`, `/mcp`, feeds, sitemaps, `robots.txt`, `llms*.txt`, `/.well-known/`, static files, themes, uploads, `/healthz`, search and the assistant. Creating a rule never replaces an existing one.
  - **Imported rules** are an option of the import, off by default. A rule is kept only when its target is a path of this site (an absolute URL on the exported site's own host is rewritten to its path) and its source is not the address of a post or page; every rule is listed in the report, with the reason for each one that was skipped.
  - The redirects that an import makes from the old addresses of posts and files pass the same checks.
- **Forwarded headers:** `X-Forwarded-For` is honoured only from configured proxies or over the Unix socket.

### Content
- Markdown is rendered with goldmark and sanitised with bluemonday. The same applies to plugin output, AI answers and imported WordPress content.
- **Uploads:**
  - the content type is detected from the bytes and must match the file extension;
  - files are stored under random names and served with `Content-Security-Policy: sandbox` and `nosniff`;
  - anything other than images, audio, video and PDF is served as a download.
- Archive extraction (backups, the importer, plugin packages, theme uploads) refuses absolute paths, `..`, links and special files, and enforces size limits.

### Appearance (customiser and page builder)
- **Drafts** are rendered only for signed-in users with the appearance capability who have set up the second factor the admin requires, and only when they ask for them (`?customize=draft`). Such requests carry a session, so the page cache never stores them, and the responses are `no-store`.
- **Designs and layouts** are validated against a schema:
  - colours are hex values, while fonts, scales, section styles and block appearance are named choices, so a page only ever gets known classes;
  - links in blocks accept `https:`, `http:`, site paths, `#…`, `mailto:` and `tel:`, never `javascript:` or `data:`;
  - Markdown in blocks goes through the content pipeline.
- **Custom CSS** is tokenised the way browsers read it. `@import`, external `url()`, resource-building functions and legacy script hooks are refused. It is served as a same-origin stylesheet under the strict CSP, never inlined.
- **Fonts:** only users with the appearance capability can upload WOFF2 files, because uploaded fonts end up in the site's stylesheet.
- **Attribution:** the page builder always puts the footer credit back, and custom CSS cannot name `footer-meta`. CSS can still hide the credit in other ways, and sap13ns does not try to prevent that. Keeping the "Powered by sap13ns" attribution visible is the operator's obligation under CPAL-1.0 (Exhibit B).

### Uploaded themes
Administrators can upload a theme as a `.zip` (**Appearance → Themes**, or `sap13ns theme install`). A theme is code that runs on the server for every visitor, so an upload is checked before anything is installed, and it renders within limits. No operator reviews uploaded themes; these guarantees are what the checks enforce. The full list of rules is in [THEMES.md](THEMES.md#uploading-a-theme).

- **Atomic install.** The archive is extracted under the shared archive limits (20 MB packed, 40 MB unpacked, 300 entries, a ratio of 100 to 1) into a staging directory, checked there, and moved into place only once it loads. A refused or failed upload leaves nothing behind, and a replacement that fails keeps the version that was there, so the active theme survives a bad upload.
- **Passive files only.** `static/` holds stylesheets, fonts and raster images, each checked by its extension and by the type sniffed from its bytes. Scripts, HTML, XML, WebAssembly and SVG are refused (an SVG opened at its own URL is a document that can run scripts). Only `static/` is served, with `nosniff`.
- **No code in the browser.** Templates hold no `<script>`, no event handler attributes, no `<style>` or `style=""`, no `<base>`, `<meta http-equiv>`, frames, `<object>` or `<embed>`, and no element or attribute names written by actions. The stylesheet passes the custom CSS checker, which only adds relative URLs, `data:` images and `.footer-meta` to what custom CSS may use. The CSP would block inline code anyway.
- **No secrets.** Templates can call only the documented functions. They cannot read the signed-in user's CSRF token, write out a value that holds it, or put the shared templates that carry it (the admin bar, the comments and the comment form) inside an attribute or a comment, and they cannot call the assistant from the page data. A theme's own forms use `GET`, and the CSP's `form-action 'self'` keeps every form on the site.
- **Bounded work.** Templates cannot recurse, loop only over lists of the page data (never over a number or a list they make), keep loops that may write nothing cheap, and expand to at most 100,000 steps per page. When rendering, a page of an uploaded theme gets at most 8 MB of output and 5 seconds, ends with the request, and its text functions refuse to make more than 1 MB. A page over a limit is a 500 and a log line (`uploaded theme refused to render`), never a partial page. Built-in and hand-installed themes keep their unbounded path.
- **The credit.** The layout must render `shared/footer-meta` with the page data, in the page's text, on every path through its conditions, and the rendered page must contain the credit or it is not sent.
- **Its own name.** An upload cannot take the slug of a built-in or network theme, and an uploaded theme never replaces one: if one with its slug appears later, the upload is not loaded.
- **Audit.** Uploads, replacements, refusals (with the rules broken) and deletions are written to the audit log with the archive's SHA-256.

**Known risks of uploaded themes:**
- **CPU.** The step count bounds the work between writes, and the time limit is checked on every write, but one step can be a function over a long text. A crafted theme can make one uncached page cost seconds of CPU. The page cache serves visitors, and the work runs inside the uploading site's own limits.
- **The credit can be hidden** with CSS, as with custom CSS. That breaches the licence, and sap13ns does not try to prevent it.
- **CSS selectors** can make the browser fetch site URLs that depend on what a page holds, such as an attribute's value, as custom CSS already can. The requests stay on the site, which is the uploader's own, and the one secret on its pages, a session's CSRF token, only works with that session's cookie.

### Outbound requests
Plugin HTTP calls, imported media and AI image downloads go through one SSRF guard (`internal/netguard`). It resolves the host itself, connects only to the public address it checked (which defeats DNS rebinding), and does so again on every redirect. It refuses:

- loopback, private, link-local, CGNAT, multicast, cloud metadata, documentation, benchmarking and reserved addresses;
- IPv6 ranges that embed or translate to IPv4 (IPv4-mapped and -compatible, NAT64, 6to4, Teredo), as well as site-local and unique-local addresses.

The AI API endpoint is chosen by an administrator and is not guarded. Plain `http://` is accepted only to this machine, with **Allow plain http:// to this machine** (for LM Studio and similar). With that option, image downloads may reach loopback too.

Update checks and release downloads are restricted to GitHub hosts over https with certificate validation. They honour the environment's proxy settings, and they do not use the guard.

### Plugins
- Plugins are WebAssembly modules run by wazero:
  - no filesystem, sockets, environment or clock beyond what WASI provides;
  - memory caps, per-call timeouts and a limit on concurrent instances;
  - a plugin is paused automatically after repeated failures.
- **Permissions** are explicit and shown at approval: private key-value storage, read-only published posts, HTTP to named hosts only.
- **Approval** pins the module's SHA-256 and the granted permissions. Any change disables the plugin until it is reviewed again.
- **Output** is sanitised before it reaches a page. Head tags are limited to safe `<meta>` names; `http-equiv` is never allowed.
- **Uploaded packages** are extracted with strict limits into a staging directory and scanned before anything is installed:
  - critical findings refuse the package;
  - plugins are always installed disabled;
  - high-risk permissions or findings need an explicit confirmation to enable.
- **Forms, e-mail and assets** are mediated by the host:
  - forms are rendered by the host with single-use tokens, a honeypot and rate limits;
  - e-mail only reaches the site's own addresses, at 20 messages per plugin per hour;
  - plugin JavaScript and CSP additions are high-risk permissions.

  The full contract is in [PLUGINS.md](PLUGINS.md).

### Secrets and data
- The instance secret (`SAP13NS_SECRET_KEY`) signs tokens and encrypts stored secrets, such as AI provider keys, with AES-256-GCM. Secrets are never shown again in the admin.
- **Backups** can be encrypted with AES-256-GCM (STREAM construction, Argon2id key derivation). The passphrase is read from a file or the environment, never from a command-line flag.
- **Updates** install only releases whose `SHA256SUMS` carries a valid Ed25519 signature from the key compiled into the binary.
- **Content exports** (Tools → Export) need the settings capability, and each one is written to the audit log.
  - They never contain password hashes, two-factor secrets, passkeys, sessions, plugin secrets, AI keys or the instance and site secrets.
  - E-mail and IP addresses are only included when the administrator asks for them.
  - A `.zip` prepared in the background sits in a directory that is never served. It is downloaded once, by the administrator who asked, through a signed link valid for 24 hours, and then deleted.
  - Background exports cannot fill the disk. A site keeps one waiting, and a new one deletes it. One starts only when its estimate fits the storage of the site's plan (on a network) and the disk keeps twice the estimate free; otherwise it is refused with 413.
- **Analytics** store only daily aggregates. Visitors are counted with a salt that rotates daily and lives only in memory: no IP address, user agent or identifier is persisted.
- **Public pages** make no third-party requests and set no tracking cookies.

### Networks of sites
A network (`SAP13NS_MULTISITE=true`, see [MULTISITE.md](MULTISITE.md)) runs many sites in one process and one database. Each site's administrators control their own site, and nothing else.

- **Data isolation.** Every site has its own PostgreSQL schema. The shared pool points a connection at one schema before each statement, so a site's queries see only its own tables, and the caller cannot override that:
  - the schema comes from the site's database handle, never from the request;
  - `search_path` is exactly that schema, without `public`;
  - a statement without a schema is refused, and a fresh connection starts on a schema that does not exist, so a forgotten scope fails instead of reading the previous site's data;
  - a lint test rejects SQL that would change session state (`SET` without `LOCAL`, `RESET ALL`, `DISCARD`, `SET ROLE`).

  Every schema is migrated before anything is served, so no site runs against another version of the tables.
- **Files and processes.** Each site has its own data directory, plugin host and caches. A plugin's `posts:read` and key-value storage reach its own site only.
- **Site secrets.** Each site has a random 32-byte secret, which signs its sessions and encrypts its stored keys. It is sealed with AES-256-GCM, under a key derived from `SAP13NS_SECRET_KEY`, with the site's ID as associated data, so sealed secrets cannot be swapped between sites.
- **Cookies.** Session cookies are host-only (`__Host-`), so a site cannot set or read another host's.
- **Single sign-on** through the portal is an authorization-code exchange with PKCE:
  - a top-level navigation only;
  - the callback must be exactly one of the site's own hosts;
  - the code is stored as a hash, valid for 60 seconds, single-use, and bound to the site and the callback;
  - the verifier never leaves the site's host-only cookie, and a state cookie prevents login CSRF;
  - staff roles need a second factor on the portal;
  - a site's user is never matched to a network account by e-mail.

  Removing a membership, or disabling an account, ends the linked sessions on every site.
- **Operators.** Network administrators need a second factor. When one opens a site they are not a member of, they get a separate operator user, and the sign-in is written to the audit logs of both the portal and the site.
- **Plugins.**
  - On a paid plan, an uploaded plugin is held until a network administrator approves that exact build: its module, its assets and the permissions it pins. A build that asks for more is reviewed again.
  - Personal sites can only install the operator's catalogue.
  - Either way, the site's administrator still approves the permissions before enabling the plugin.
- **Themes.** Only plans with theme uploads can upload themes; on the others the form is hidden and the upload is refused with a 403. An uploaded theme goes into the site's own directory, so no other site can list, preview or load it. No operator reviews it: the checks of [Uploaded themes](#uploaded-themes) apply.
- **Plan limits.** Storage is reserved atomically before an upload is written, so concurrent uploads cannot go over the quota. Theme files count too, and deleting a theme gives its bytes back. Exports are temporary and not counted, so they are bounded instead: one waiting per site, and none larger than the plan's storage ([MULTISITE.md](MULTISITE.md#exporting-one-site)).
- **Custom domains.**
  - **Proving control.** A host is routed only after a DNS check finds its TXT record with a token derived for that site and host, and finds that the host points to the network. Unverified hosts get a 404.
  - **Untrusted answers.** DNS answers are untrusted: names are normalised, only the exact value counts, and nothing from an answer is stored.
  - **Claims.** A tenant can never claim another site's verified host, the portal's host, or a host under `SAP13NS_SITES_DOMAIN`. A host that another site added but never verified moves only to a site that proves control of it.
  - **Lapses.** Verified domains are checked daily. One that keeps failing stops being served. Resolver errors are never counted.
- **HSTS** carries `includeSubDomains` only on the network's own hosts: the portal, and a site's default host while it is the primary one. A custom domain never gets it, whether it is the primary host or an alias, because forcing HTTPS on a customer's other subdomains is not the network's call.
- **Passkeys** of a site keep its default host as RP ID, never the network's base domain, so a passkey for one site is useless on another. A custom primary domain is listed as a related origin by the RP ID host.
- **TLS.**
  - **Caddy's ask endpoint** (`/_sap13ns/tls-ask`) answers only requests that come straight from loopback or a trusted proxy, without forwarded headers. Anything else gets the same 404, so the endpoint cannot tell the internet which hosts exist.
  - **The built-in ACME client** asks the CA only for verified custom domains, so nobody can make the network spend its certificate rate limits on arbitrary names.
- **Billing.** The webhook accepts only events with a valid Stripe signature and stores each event once. The processor then fetches the subscription from Stripe instead of trusting the event. The browser never chooses a price: the server maps the plan to its configured price.

**Known risks on a network:**
- **Wrong-schema SQL** is the main leak risk. It is contained by the scoped handles, the fail-closed pool, the non-existent default schema, the lint test and the interleaving tests in `internal/db`. New SQL must go through a site's store, never the raw pool.
- **A shared registrable domain.** When `SAP13NS_SITES_DOMAIN` is the portal's domain, or its parent, the sites are *same-site* with the portal:
  - **What already protects it:** Go's cross-origin protection rejects same-site requests that are not same-origin, and `__Host-` cookies cannot be overwritten from a subdomain.
  - **What does not:** a site that runs scripts (a plugin with the high-risk `assets:js` permission) can still set cookies for the parent domain and make every request to it too large ("cookie bombing").
  - **What to do:** put the sites under a domain of their own (`SAP13NS_SITES_DOMAIN=example.net` with the portal on `example.com`), or add that domain to the Public Suffix List.
- **HSTS and customer domains.** `includeSubDomains` from a customer's apex domain would force HTTPS on all of the customer's other hosts, so it is never sent there.
- **Memory.** Every loaded site has its own templates, caches and plugin instances. `SAP13NS_MAX_LOADED_SITES`, `SAP13NS_MEMORY_LIMIT_MB` and the lower per-plugin limits of a network bound them. When more sites are active than the limit, sites keep loading and unloading each other.
- **State lost on eviction.** An unloaded site loses its in-memory per-IP rate limits and its analytics visitor set. Account lockouts are kept in the database and survive. Unloading sites by loading many others resets a site's per-IP sign-in limits, but not the lockout of the account.
- **Stale custom domains.** A domain whose owner moved it elsewhere is caught by the daily re-check, and stops being served after its grace period: 3 failed checks over at least 72 hours.
- **Backups.** The encrypted backup command does not cover networks yet. Back up the database and the data directory yourself, and protect those copies as you would the server, because they hold every site's sealed secrets with `SAP13NS_SECRET_KEY`.

### Deployment
The systemd unit in [`deploy/systemd`](../deploy/systemd/sap13ns.service) runs sap13ns unprivileged and sandboxed (see [DEPLOY.md](DEPLOY.md#hardening-notes)). nginx terminates TLS and talks to sap13ns over a Unix socket (`0660`, owned by the `sap13ns-web` group, which nginx joins); nginx cannot read the env file or the data directory.
