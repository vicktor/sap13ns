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
- **Users' photos** (the profile, or a user's page with the users capability) are JPEG, PNG, WebP or GIF images of up to 5 MB and 20 megapixels, sniffed from their bytes. They are decoded and drawn again as square 256, 128 and 64 px JPEG files (PNG when they have transparency), so no metadata survives: no EXIF, GPS or comments. They are stored as `avatars/u<id>-<random>-<size>.<ext>`, and replacing or removing one deletes the old files. Decoding is bounded to two images at a time, and uploads share the media upload rate limit.
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
- **No code in the browser.** Templates hold no `<script>`, no event handler attributes, no `<style>` or `style=""`, no `<base>`, `<meta http-equiv>`, frames, `<object>`, `<embed>` or `<math>`, and no element or attribute names written by actions. An inline `<svg>` holds only drawing elements, so that the checks' HTML tokenizer, which does not know foreign content, reads it as a browser does. `<noscript>` is refused, because a browser reads its content as text or as markup depending on whether JavaScript is on, and no single reading can check both. The stylesheet passes the custom CSS checker, which only adds relative URLs, `data:` images and `.footer-meta` to what custom CSS may use. The CSP would block inline code anyway.
- **No secrets.** Templates can call only the documented functions. They cannot read a form token (`.CSRF`, `.Token`), write out a value that holds one, write the placeholders the server fills with tokens or plugin forms, or put the shared templates that carry the CSRF token (the admin bar, the comments and the comment form) inside an attribute or a comment, and they cannot call the assistant from the page data.
- **Forms stay the site's.** A theme's own forms use `GET` and send to a path of the site written in the template; the CSP's `form-action 'self'` keeps every form on the site anyway. A browser ignores a `<form>` inside another and adds its fields to the outer one, so a theme's `<form>` or `<select>` cannot hold a shared template that writes a form or form fields. The shared forms that carry a token (the admin bar's, the comment form, plugin forms) also begin with `</select></form>`, which ends any form or select a theme left open: a browser never adds their fields, tokens included, to a theme's form, even through markup the checks would misread.
- **The referrer.** Templates cannot change the page's `Referrer-Policy` (`strict-origin-when-cross-origin`): no `referrerpolicy` attribute, no `<meta name="referrer">` nor a `<meta name>` an action writes, no `rel` an action writes or with `opener`.
- **Bounded work.** Templates cannot recurse, loop only over lists of the page data (never over a number or a list they make), keep loops that may write nothing cheap, and expand to at most 100,000 steps per page. When rendering, a page of an uploaded theme gets at most 8 MB of output and 5 seconds, and ends with the request. Its templates run with copies of the functions bound to that render: each call checks the limits first, so the page stops even between writes, and counts the text it returns, at most 16 MB per page. The formatting functions compute their text's length before they make it, from the format actually used (a translation, not its key; `%[1]s` repeated; widths up to 99) and the arguments, and refuse more than 1 MB; they format only texts, numbers, booleans and dates, and a `dict` or `list` prints as a fixed text, so a list that holds itself cannot print in exponential space. The template sets as loaded have their functions turned off, so nothing runs them outside these copies, the upload's probe included: it renders every page once without data within the same limits, and refuses a page that goes over them. A page over a limit is a 500 and a log line (`uploaded theme refused to render`), never a partial page. Built-in and hand-installed themes keep their unbounded path.
- **The credit.** The layout must render `shared/footer-meta` with the page data, in the page's text, on every path through its conditions, and the rendered page must contain the credit or it is not sent.
- **Its own name.** An upload cannot take the slug of a built-in or network theme, and an uploaded theme never replaces one: if one with its slug appears later, the upload is not loaded.
- **Audit.** Uploads, replacements, refusals (with the rules broken) and deletions are written to the audit log with the archive's SHA-256.

**Known risks of uploaded themes:**
- **CPU.** The step count bounds the work between writes, and the time limit is checked on every write and every function call, but one call can be a function over a long text. A crafted theme can make one uncached page cost seconds of CPU. The page cache serves visitors, and the work runs inside the uploading site's own limits.
- **Memory per render.** A render holds its 8 MB buffer and up to 16 MB of text its functions made, and an uploaded theme keeps up to four copies of each page's templates per language, for renders that run at the same time; a fifth waits for one, up to the time limit.
- **The checks read HTML with a tokenizer, not a browser's parser.** Where a browser builds a different tree (a `</form>` inside a `<select>`, foreign content), the checks err on the side of refusing; the `</select></form>` at the start of the token-bearing shared forms is what holds if they do not.
- **The credit can be hidden** with CSS, as with custom CSS. That breaches the licence, and sap13ns does not try to prevent it.
- **CSS selectors** can make the browser fetch site URLs that depend on what a page holds, such as an attribute's value, as custom CSS already can. The requests stay on the site, which is the uploader's own, and the one secret on its pages, a session's CSRF token, only works with that session's cookie.

### Outbound requests
Plugin HTTP calls, imported media, AI image downloads and WebSub pings go through one SSRF guard (`internal/netguard`). It resolves the host itself, connects only to the public address it checked (which defeats DNS rebinding), and does so again on every redirect. It refuses:

- loopback, private, link-local, CGNAT, multicast, cloud metadata, documentation, benchmarking and reserved addresses;
- IPv6 ranges that embed or translate to IPv4 (IPv4-mapped and -compatible, NAT64, 6to4, Teredo), as well as site-local and unique-local addresses.

The AI API endpoint is chosen by an administrator and is not guarded. Plain `http://` is accepted only to this machine, with **Allow plain http:// to this machine** (for LM Studio and similar). With that option, image downloads may reach loopback too.

Update checks and release downloads are restricted to GitHub hosts over https with certificate validation. They honour the environment's proxy settings, and they do not use the guard.

**Gravatar.** With **Use Gravatar for users without a photo** (Settings → Discussion, on by default), the server looks up users who have not uploaded a photo. It asks `https://gravatar.com/avatar/<hash>?s=256&d=404`, where the hash is the SHA-256 of the trimmed, lower-cased e-mail address. The address itself is never sent, and neither is any cookie or other detail of the visitor.
- **Through the guard.** The request goes through the SSRF guard, ignores proxies, follows no redirects, times out after 10 seconds and reads at most 1 MB. A 404 means there is no Gravatar.
- **Re-encoded and served locally.** An image is checked and re-encoded like an upload, then served from the site's own uploads. Visitors' browsers never contact Gravatar, and no page waits for it.
- **When.** A lookup happens when a user is created or changes e-mail address, when someone presses **Refresh from Gravatar**, and in a background pass. The pass rechecks each user about once a week, at most 10 users per pass with pauses between them, and retries a failed lookup after a day. Addresses on reserved domains (`example.com`, `.test`, `.invalid`, `.localhost` and the like) are never looked up.
- **Commenters.** **Show Gravatar images for commenters** is off by default and needs the first option. It looks up the hash of a commenter's address in the background, never while a page renders, and never for spam or trashed comments. The copies are kept for at most 1000 addresses and 32 MB, rechecked after 14 days and dropped after 60 days unused, and lookups are limited to 120 an hour. A registered user who comments shows their own photo.
- **Turning it off.** Switching either option off sends nothing more and deletes the copies at once.
- **Development.** `SAP13NS_DEV_GRAVATAR_URL`, read only in development mode (`SAP13NS_DEV=true`), points the lookups at a fake server, which may be on this machine.

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
- **Site backups** (Tools → Backups, `sap13ns backup -format site`, and a network's nightly run):
  - **No secrets.** They never hold password hashes, two-factor secrets or recovery codes, passkeys, sessions, plugin secrets, AI or embedding keys, the MCP token, or the instance and site secrets. They do hold personal data (users' and commenters' e-mail addresses, commenters' IP addresses, the photos users uploaded), so they are written with mode 0600 in 0700 directories that are never served, and encrypted when a passphrase is set.
  - **Administrators only.** The page needs the settings capability. A download link is signed for the administrator's session and expires after an hour, and each download is audited.
  - **Restores** need the site's name typed and the administrator's current password, or a sign-in of the last few minutes with single sign-on. They are spaced out (one every 10 minutes, 3 an hour, recorded with the backups so a reload does not reset it) and audited. A restore makes a safety backup first, changes the database in one transaction, and swaps the staged files in after the commit, so a failed restore leaves the site as it was. It writes only the site's own schema and directory.
  - **Nothing in an archive is trusted.** Every member is checked against the manifest (size and SHA-256) and the extraction limits. Content goes through the sanitising pipeline. Settings, menus, widgets, theme options, designs, redirects and plugin settings go through the checks of their admin pages, and what fails is reset to the site's value and reported. A restore never takes a secret from an archive, never keeps a key for another provider or address, and never switches local (plain http) models on.
  - **Bounded.** A backup, or what a restore writes, never takes more than half of the free disk. On a network a backup is at most the storage of the site's plan, a site's backups take at most twice it in all, and a site keeps at most 3 safety backups. Backups are not counted toward the site's storage.
- **Analytics** store only daily aggregates. Visitors are counted with a salt that rotates daily and lives only in memory: no IP address, user agent or identifier is persisted.
- **Public pages** make no third-party requests and set no tracking cookies. Gravatar images are copies served by the site ([Outbound requests](#outbound-requests)).
- **Photos and Gravatar copies** are left out of content exports. Backups hold the uploaded photos, re-encoded again on restore, but not the Gravatar copies, which a restored site looks up again. Deleting a user deletes their files.

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
  - every portal account needs a second factor, whatever its role (`SAP13NS_DEV_SKIP_2FA` lifts it in development mode only); a new account sets one up before it sees its sites;
  - a site's user is never matched to a network account by e-mail.

  Removing a membership, or disabling an account, ends the linked sessions on every site.
- **The site switcher** of the admin lists only the sites the account is a member of, with their titles, read by the network. On a site, the account is the one linked to the user of a session that came through the portal; nothing in the request names it. Its entries are links to each site's `/sso/start`, so every switch is a new sign-on with the checks above, never a session or token carried from one site to another.
- **Operators.** A network administrator who is not a member of a site opens it only in two cases, as a separate operator user. Every sign-in is written to the audit logs of both the portal and the site.
  - **Support access** is the consent of the site's owner or one of its administrators, for 24 hours, 7 days or until revoked, granted on the portal (`/admin/sites/<id>/support`). Without it the portal issues no code, and the redemption checks the grant again under the same row locks as the membership. Every request of an operator session checks the grant, cached for at most 30 seconds and never past its end; a revocation also deletes the operators' sessions at once. No grant opens on a suspended site.
  - **Emergency access**, without that consent, is only for a legal order or a serious security incident. The network administrator gives the reason and a justification of up to 500 characters, and passes a fresh second factor: a portal sign-in less than 10 minutes old, or an authenticator code, never a password. It lasts 4 hours at most and opens the site to them alone. Its sessions have their own authentication method (`sso-emergency`) and end with it. The owner is e-mailed at once with the reason, the justification, who and until when. On a suspended site, the router serves the site to that session only; everything else still gets the 503 page. The site is then opened inert: none of the tenant's plugins loads (no hook, filter, shortcode, form, plugin asset or fetch runs, and none can be installed or switched on) and none of its background jobs runs. That instance is unloaded when the emergency ends or expires and when the site is resumed, and a site suspended while loaded is unloaded at once, so no normal instance stays loaded for a suspended site.
- **Plugins.**
  - On a paid plan, an uploaded plugin is held until a network administrator approves that exact build: its module, its assets and the permissions it pins. A build that asks for more is reviewed again.
  - Personal sites can only install the operator's catalogue and the official plugins the operator does not hide. Both come from the operator; an official plugin is still checked and scanned like any upload before it is installed.
  - Either way, the site's administrator still approves the permissions before enabling the plugin.
- **Themes.** Only plans with theme uploads can upload themes; on the others the form is hidden and the upload is refused with a 403. An uploaded theme goes into the site's own directory, so no other site can list, preview or load it. No operator reviews it: the checks of [Uploaded themes](#uploaded-themes) apply.
- **Plan limits.** Storage is reserved atomically before an upload is written, so concurrent uploads cannot go over the quota. Theme files count too, and deleting a theme gives its bytes back. Exports and backups are not counted, so they are bounded instead. Exports: one waiting per site, and none larger than the plan's storage ([MULTISITE.md](MULTISITE.md#exporting-one-site)). Backups: none larger than the plan's storage, twice it in all per site, at most 3 safety backups, and spaced-out restores ([MULTISITE.md](MULTISITE.md#backups)).
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
- **Backups.** Site backups cover the sites, not the network: the portal's accounts, plans, domains and every site's sealed secret are only in the database. Back up the database and the data directory too, and protect those copies as you would the server, because they hold every site's sealed secrets with `SAP13NS_SECRET_KEY`. `site purge` deletes a site's backups with it, unless the operator passes `-keep-backups`.

### Deployment
The systemd unit in [`deploy/systemd`](../deploy/systemd/sap13ns.service) runs sap13ns unprivileged and sandboxed (see [DEPLOY.md](DEPLOY.md#hardening-notes)). nginx terminates TLS and talks to sap13ns over a Unix socket (`0660`, owned by the `sap13ns-web` group, which nginx joins); nginx cannot read the env file or the data directory.
