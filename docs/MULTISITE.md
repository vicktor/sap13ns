# Networks of sites (multisite)

With `SAP13NS_MULTISITE=true`, one sap13ns process serves a **network**: a portal where people have one account, and any number of sites. Each site is isolated from the others, with its own database schema, files, users, themes and plugins. This is how the hosting at sap13ns.site runs, and you can run your own.

A single-site install is unchanged. Nothing on this page applies to it, and the network settings are ignored without `SAP13NS_MULTISITE=true`.

## Contents

- [Concepts](#concepts)
- [Setting up a network](#setting-up-a-network)
- [Configuration](#configuration)
- [Accounts, single sign-on and operator access](#accounts-single-sign-on-and-operator-access)
- [Plans, billing and limits](#plans-billing-and-limits)
- [Plugins and themes](#plugins-and-themes)
- [Custom domains](#custom-domains)
- [TLS](#tls)
- [Command-line reference](#command-line-reference)
- [Backups](#backups)
- [Limitations](#limitations)

## Concepts

**The portal.** The portal is the site on the host of `SAP13NS_BASE_URL`. It is an ordinary sap13ns site, with its own posts, pages and theme, which makes it the network's landing page. It also has:
- the **network accounts**: its users. People register there (when **Settings → Accounts & security → Anyone can register** is on) or are created with `sap13ns user create -site network`.
- **My sites** (`/admin/sites`): every account's sites, where it requests a site, upgrades a site, manages billing and manages the custom domains of the sites it owns.
- the **network admin area** (`/admin/network/`): sites, members, network administrators, plans, billing events and plugin reviews. It is open only to network administrators who have a second factor.

The portal is never unloaded and has no custom domains.

**Tenant sites.** Every other site is a tenant. It has a name (its slug), an owner (a network account), a plan and a status.

| Status | What is served |
|---|---|
| `provisioning` | Nothing (404). The site is being created. |
| `pending` | A Personal site waiting for a network administrator's approval. The public pages show a 503 notice; sign-in and the admin work. |
| `active` | Everything. |
| `suspended` | A 503 page saying that the site is suspended, on every path. Set by a network administrator, with a reason the owner sees. |
| `deleted` | Nothing (404). Its data stays until it is purged with `sap13ns site purge`. |

A paid site whose subscription is not paid is **held**: it is served like a pending site (a 503 notice on the public pages, while sign-in and the admin work), so its owner can still pay. See [Plans, billing and limits](#plans-billing-and-limits).

**Hosts.** Every site has a **default host**, `<slug>.<SAP13NS_SITES_DOMAIN>`, for its whole life. It may add **custom domains** when its plan allows them. One verified host is the **primary** one: the site is served there, and its other hosts redirect to it (301 for GET and HEAD, 308 otherwise). ACME challenges and the WebAuthn related-origins file are answered on every host. A host the network does not route gets a 404, or a 302 to the portal with `SAP13NS_UNKNOWN_HOST=redirect`.

Some names are reserved and cannot be the slug of a site, among them `www`, `admin`, `api`, `mail`, `portal`, `sso`, `static` and `webauthn`. The full list is `reservedSlugs` in `internal/network/sites.go`.

**One schema per site.** All sites share one PostgreSQL database and one connection pool, but every site has its own schema: `network` for the portal and the network's own tables, and `site_<id>` for each tenant. The pool sets the `search_path` of a connection to exactly one site's schema before each statement. A statement that carries no schema is refused, so a forgotten scope fails instead of reading another site's rows (see [SECURITY.md](SECURITY.md#networks-of-sites)). `serve` and `migrate` migrate the network schema and then every site's schema before anything is served.

**Files.** Each site, the portal included, keeps its files under `$SAP13NS_DATA_DIR/sites/<id>/`, in `uploads/`, `plugins/` and `themes/`. Compiled plugins are cached in `$SAP13NS_DATA_DIR/cache/wasm` and shared by every site. With `SAP13NS_TLS=autocert`, certificates are cached in `$SAP13NS_DATA_DIR/cache/autocert`.

**The registry and eviction.** A site is loaded on its first request and unloaded when it is no longer needed:
- **Loading:**
  - a site loads once, however many requests arrive together;
  - at most 4 sites load at the same time;
  - a request that waits more than 10 seconds for its site gets a 503 with `Retry-After`, and the load goes on;
  - a site takes at most 32 requests at once; beyond that it answers 503.
- **Unloading:**
  - after `SAP13NS_SITE_IDLE` (15 minutes) without requests;
  - the least recently used ones, when more than `SAP13NS_MAX_LOADED_SITES` (50) are loaded;
  - more, when the heap goes over 85% of `SAP13NS_MEMORY_LIMIT_MB`, until it is back near 70%.
  
  A site with requests in flight, or busy with an import or an AI job, is never unloaded, and neither is the portal.
- **The host table** is reloaded on every change, and every minute. The command line and other processes notify the running servers through PostgreSQL (`LISTEN sap13ns_network`). A site that is suspended or deleted is unloaded at once, after its requests in flight; a site whose hosts changed is reloaded once it is idle.

**Background work.** A loaded site runs its own minute loop, as a single site does. The network also reaches the sites that are not loaded:
- **Every minute:** sites with a scheduled post that is due are loaded to publish it.
- **Every hour:** `cron.hourly` runs on the loaded sites, and on the unloaded sites whose plugins handle it.
- **Every day:** the following runs, without loading a server:
  - each schema is cleaned up (sessions, audit log, analytics);
  - storage is measured;
  - expired sign-on codes are purged;
  - the custom domains are checked again.

  The first run comes 10 minutes after a start.

## Setting up a network

The steps follow [DEPLOY.md](DEPLOY.md) for the binary, PostgreSQL and the systemd unit. The examples use `example.com` for the portal, `*.example.com` for the sites and `www.example.org` for a customer's own domain.

1. **DNS.** Point the portal host and a wildcard for the sites to the server:

   ```
   example.com.      A     192.0.2.80
   example.com.      AAAA  2001:db8::80
   *.example.com.    A     192.0.2.80
   *.example.com.    AAAA  2001:db8::80
   ```

   The sites can also live under a domain of their own, such as `*.example.net` with `SAP13NS_SITES_DOMAIN=example.net`. That keeps tenants and the portal on different registrable domains, which [SECURITY.md](SECURITY.md#networks-of-sites) recommends.
2. **TLS.** Choose how certificates are made (see [TLS](#tls)): nginx with a wildcard certificate, Caddy, or the built-in ACME client.
3. **Configuration.** Add the network settings to `/etc/sap13ns/sap13ns.env`:

   ```sh
   SAP13NS_MULTISITE=true
   SAP13NS_BASE_URL=https://example.com
   SAP13NS_SITES_DOMAIN=example.com
   SAP13NS_DB_MAX_CONNS=32
   #SAP13NS_DOMAIN_ADDRESSES=192.0.2.80,2001:db8::80
   ```

4. **Create the network.** `network init` creates the network schema, the portal on the `SAP13NS_BASE_URL` host, and a first network administrator. As with `user create`, the password is read from stdin, or a strong one is generated and printed.

   ```sh
   sudo -u sap13ns sap13ns network init -env /etc/sap13ns/sap13ns.env -username ops -email ops@example.com
   sudo systemctl start sap13ns
   ```

   `serve` refuses to start on a network that has not been created yet. Running `network init` again is harmless: without `-username` it only reports the portal and its administrators.
5. **Sign in** to `https://example.com/admin` as `ops`, and set up a second factor: every portal account needs one.
6. **The first sites.** Either let people register on the portal and request a Personal site from **My sites**, or create sites from the command line. The owner must be a network account:

   ```sh
   sudo -u sap13ns sap13ns user create -env /etc/sap13ns/sap13ns.env -site network -username owner -email owner@example.com -role subscriber
   sudo -u sap13ns sap13ns site create -env /etc/sap13ns/sap13ns.env -slug blog -owner owner@example.com -plan pro -title "Blog" -lang en
   ```

   A Personal site starts as `pending`, until a network administrator approves it under **Network → Sites**. The owner is e-mailed when it is approved or rejected.

To try a network on a development machine, `make dev-network` (in a checkout) does all of this on its own database, `sap13ns_net_dev`, with the env file `data/network.env`. The portal is on `http://localhost:8139` and the sites are on `http://<slug>.localhost:8139`, which browsers resolve to the machine itself (`SAP13NS_SITES_DOMAIN=localhost`). The accounts skip the second factor (`SAP13NS_DEV_SKIP_2FA`), so never use that env file anywhere else. Serve it with `make dev ENV=data/network.env`; the README's Development section has the details.

## Configuration

These variables are read only when `SAP13NS_MULTISITE=true`. The variables of a single site (README) apply to the whole network: one database, one data directory, one SMTP server.

| Variable | Default | Meaning |
|---|---|---|
| `SAP13NS_MULTISITE` | `false` | `true` serves a network |
| `SAP13NS_BASE_URL` | | The portal's address. Its scheme and port are those of every site. |
| `SAP13NS_SITES_DOMAIN` | the `BASE_URL` host | Default hosts are `<slug>.<SITES_DOMAIN>` |
| `SAP13NS_UNKNOWN_HOST` | `404` | What a host the network does not route gets: `404`, or `redirect` (302 to the portal) |
| `SAP13NS_MAX_LOADED_SITES` | `50` | Sites kept in memory at once |
| `SAP13NS_SITE_IDLE` | `15m` | A site is unloaded after this long without requests (at least `1m`) |
| `SAP13NS_MEMORY_LIMIT_MB` | `0` (none) | Soft memory limit of the process (at least 64). The registry unloads sites above 85% of it, and it sets the Go runtime's memory limit unless `GOMEMLIMIT` is set. |
| `SAP13NS_DB_MAX_CONNS` | `32` on a network | The pool every site shares |
| `SAP13NS_THEMES_DIR` | `$DATA_DIR/themes` | The network's themes, offered to every site |
| `SAP13NS_DOMAIN_TARGET` | the `BASE_URL` host | What custom domains point to with a CNAME |
| `SAP13NS_DOMAIN_ADDRESSES` | the addresses of `DOMAIN_TARGET` | Comma-separated public IP addresses that custom domains may point to with A/AAAA records |
| `SAP13NS_TLS` | `proxy` | `proxy`: nginx or Caddy terminate TLS. `autocert`: sap13ns does, with the built-in ACME client. |
| `SAP13NS_TLS_CERT`, `SAP13NS_TLS_KEY` | | With `autocert`: the wildcard certificate and key of the portal host and `*.<SITES_DOMAIN>` |
| `SAP13NS_ACME_EMAIL` | | With `autocert`: the contact address of the ACME account (optional) |
| `SAP13NS_ACME_DIRECTORY` | Let's Encrypt | With `autocert`: another ACME directory URL, `https` only (for example Let's Encrypt's staging) |
| `SAP13NS_HTTP_LISTEN` | `:80` | With `autocert`: the plain HTTP listener for HTTP-01 challenges and redirects to https, or `off` |
| `SAP13NS_STRIPE_*`, `SAP13NS_BILLING_GRACE_DAYS` | | Billing: see [BILLING.md](BILLING.md#6-environment-variables) |

The service refuses to start when a value is invalid: a host name with upper-case letters or a port, a private or loopback address in `SAP13NS_DOMAIN_ADDRESSES`, or `autocert` without a certificate or with a Unix-socket listener.

## Accounts, single sign-on and operator access

**One account, many sites.** A network account belongs to sites as a **member**, with a role on each one (administrator, editor, author, contributor or subscriber). A site's owner is its first administrator. Network administrators add, change and remove members on the site's page in the network admin area; owners cannot invite members themselves yet. A site can still have local users of its own, created in its admin, who sign in with a password there.

**Single sign-on.** Every tenant's sign-in page has **Sign in with your sap13ns account**. The portal and the sites share no cookies, so sign-in is an authorization-code exchange with PKCE:
1. The site sends the browser to the portal.
2. The portal signs the account in if needed, then checks:
   - the callback is one of the site's own hosts;
   - the account is a member (or a network administrator with [support access](#support-access) or an [emergency access](#emergency-access));
   - the site is served (a suspended site only for an emergency access);
   - the account has a second factor.
3. The portal sends back a single-use code that is valid for 60 seconds.
4. The site redeems the code, which checks the access again, and opens a session.

When the account is already signed in to the portal, this takes no clicks. The site's user is created the first time and follows the membership from then on: role, e-mail, name and status. It is never matched to a local user by e-mail. A local user can link their account explicitly from their profile.

Removing a member, or disabling an account, disables the linked users and ends their sessions at once, also on sites that are not loaded. A session that came through the portal does not ask for the site's second factor, because the portal has already asked for one.

**Switching sites.** The admin of the portal, and of every site, has a site switcher at the top of its sidebar. It shows the site you are on, by its title and address, and lists the sites your account is a member of, with your role on each. An entry opens that site's admin through single sign-on, so with a portal session it takes no clicks. The menu also links to **My sites**, to the network admin area for network administrators, and to **Request a site** when the account may request one.
- **Only memberships.** The switcher lists the pending and active sites the account is a member of. A site a network administrator opens with [support access](#support-access) or an [emergency access](#emergency-access) is not listed, even on that site: that access starts from the network admin area.
- **On a site,** the switcher shows only in a session that came through the portal. A local user signed in with the site's own password sees the site's name, as on a single site.
- **Freshness.** Each session reuses its list for 30 seconds. A change made on the running server (a membership, a site's status, a network administrator) shows at once; one made with the command line shows within a few seconds, when the host table is reloaded. A site's new title shows within 30 seconds.

**Every portal account has a second factor.** An account opens every site it is a member of, so the portal requires an authenticator app or a passkey of every account, whatever its role and whatever the portal's own two-factor setting. A new account is sent to set one up as soon as it first signs in, and sees **My sites** and **Request a site** only after that. `SAP13NS_DEV_SKIP_2FA=true` lifts this in development mode only (`SAP13NS_DEV=true`), as it does on a single site.

### Operator access

A network administrator who is not a member of a site can open its administration in two cases only. Either way they sign in as a separate *operator* user with the administrator role (`operator-<username>`, shown as "(network operator)"). That user is disabled when its account stops being a network administrator.

#### Support access

Support access is the site's consent. Its owner, or one of its administrators, opens it from **My sites → Support access** (`/admin/sites/<id>/support`) for **24 hours**, **7 days** or **until revoked**. A new grant replaces the open one. While it is open, every network administrator can use **Open its administration** on the site's page in the network admin area.

- **Revoking.** The owner or an administrator revokes it at any time, and a network administrator can end it from the site's page. Either way the operators signed in to the site are signed out at once.
- **Expiry.** A session ends when its grant expires or is revoked. Every request of an operator session checks the grant. The answer is cached for at most 30 seconds, and never past the grant's end. A revocation also deletes the sessions in the database at once, so the cache only delays a revocation the site did not see. The daily maintenance disables operator users that have no grant left, also on sites that are not loaded.
- **Without it.** The portal refuses to sign the operator in: "The owner of this site has not allowed support access". **Request support access** on the site's page e-mails the owner a link to the grant page, with an optional message. Nothing opens until the owner allows it.
- **Suspended sites.** Support access cannot be opened while a site is suspended; it can still be revoked.
- **Records.** Every grant, revocation and sign-in is written to the site's audit log (`support.granted`, `support.revoked`, `login.operator`) and to the portal's (`network.support_granted`, `network.support_revoked`, `sso.operator`). The site's **Users** page shows whether it is open and links to the grant page.

Members are not affected: they sign in with their role whether support access is open or not.

#### Emergency access

Without the owner's consent, a network administrator may open a site only for a **legal order** or a **serious security incident**, such as malware or phishing on the site. They start it from the **Emergency access** card on the site's page in the network admin area. They must give:

- the reason, legal order or security incident;
- a justification of up to 500 characters;
- a fresh second factor: a sign-in to the portal less than 10 minutes old (the window of the profile's sensitive actions), or a code from their authenticator app. A password alone does not do.

The access is theirs alone, and it lasts **4 hours** at most. It can be ended earlier from the same card, and its sessions then end at once. When it expires, its sessions end as with an expired grant. The owner is e-mailed at once with the reason, the justification, who opened it and until when, and again if it is ended early. If that e-mail cannot be sent, the access opens all the same: the administrator is told to contact the owner by other means, and `network.emergency_started` records `owner_notified: false`. It does not open the site to any other network administrator.

An emergency session has its own authentication method, `sso-emergency`, so the site tells it apart from a consented operator's (`sso-operator`). Its records are:

- the site's audit log: `emergency.started` (with the reason and justification), `login.emergency` and `emergency.ended`;
- the portal's audit log: `network.emergency_started`, `sso.emergency` and `network.emergency_ended`.

The site's **Users** page shows the open emergency accesses, and **My sites → Support access** lists the past ones.

**On a suspended site.** While an emergency access is live, the router serves the suspended site to its session, and to the single sign-on that opens it, and nothing else. Visitors, members and other sessions still get the 503 page.

The site is opened **inert** for it, so none of the tenant's code runs:

- none of its plugins loads, so no hook, filter, shortcode, widget, form, plugin asset or outbound fetch of theirs runs, and none can be uploaded, installed from the catalogue or switched on. Its **Plugins** page says they are not loaded;
- none of its background jobs runs: the scheduled-post announcements, the plugin cron, the analytics and redirect flushes, the assistant index and maintenance. What a request records in memory is written when the instance closes.

The inert instance is unloaded when the emergency access ends, within a minute of its expiry, and when the site is resumed; the next request then loads the site as usual, with its plugins. The other way round, a site that is suspended while loaded is unloaded at once, even while an emergency access is live, and only an inert instance serves it from then on.

**Passkeys.** Network passkeys belong to the portal host. A site's passkeys belong to its default host, the RP ID fixed when the site was created, and they keep working when the primary domain changes (see [Custom domains](#custom-domains)). The network's base domain is never an RP ID.

## Plans, billing and limits

Plans are rows of `network.plans`. **Network → Plans** lists them. The network starts with three:

| Plan | Price | Approval | Storage | Custom domains | Plugin uploads | Theme uploads |
|---|---|---|---|---|---|---|
| Personal | free, non-commercial | by a network administrator | 1 GB | 0 | no (the catalogue and the official plugins) | no |
| Pro | Stripe subscription | none | 10 GB | 2 | yes, reviewed | yes, checked, not reviewed |
| Business | Stripe subscription | none | 50 GB | 2 | yes, reviewed | yes, checked, not reviewed |

**One subscription is one site.** Each account may have one Personal site.

**Billing** is off until `SAP13NS_STRIPE_SECRET_KEY` is set. The network then has Personal sites, plus any site an operator creates on another plan with `site create -plan`. With Stripe, an owner upgrades a site from **My sites**:
1. Stripe Checkout takes the payment.
2. The webhook (`POST /billing/webhook` on the portal) gives the site its plan.
3. **Manage billing** opens Stripe's customer portal.

A site whose renewal failed stays online for `SAP13NS_BILLING_GRACE_DAYS`, then it is held. A canceled or unpaid subscription holds the site, and paying lifts the hold. Nothing is deleted for billing. [BILLING.md](BILLING.md) covers the Stripe setup.

**Limits.** A plan's limits apply to every site on it:
- **Storage** counts uploads, their image variants, plugin packages and uploaded themes, but not exports waiting for their download nor backups, which are bounded on their own ([Exporting one site](#exporting-one-site), [Backups](#where-they-are-and-how-long-they-stay)). Each upload reserves its size with one atomic update before anything is written, so concurrent uploads never go over the limit. An upload that does not fit is refused with 413. The daily measurement replaces the count with the real size of the site's schema and directory.
- **Custom domains** count verified and unverified ones.
- **A downgrade** only blocks additions: files, domains, plugins and themes over the new limits stay. A site moved to a plan without theme uploads keeps its uploaded themes, and can delete them, but cannot upload more.

## Plugins and themes

- **The catalogue.** The plugins installed on the portal (valid, and not blocked by the scanner) form the network's catalogue. Every site sees them under **Plugins → From the network** and installs them from there, disabled. The site's administrator then reviews their permissions and enables them, as on any site.
- **The official plugins.** The plugins that ship inside the binary ([PLUGINS.md](PLUGINS.md#official-plugins)) are offered to every site, on every plan, under **Plugins → Official plugins**. Like the catalogue's, they come from the operator's own binary, so they are not held for review. An install goes through the same checks and scanner as an upload, is installed disabled, and its files count against the plan's storage. A network administrator can hide any of them under **Network → Plugins → Official plugins**: the sites then neither see nor install it. Copies already installed stay, and the operator can still install it with `sap13ns plugin install -site <slug> -official <id>`.
- **Uploads.** Personal sites cannot upload plugins. On Pro and Business, an uploaded plugin is installed disabled and **held for review**: a network administrator approves or rejects it under **Network → Plugin reviews**, which shows its permissions, its scan report and its hashes. Only then can the site's administrator enable it.
- **The review pin.** The approval is pinned to the reviewed build: the module, the assets and the permissions it asks for. The same module uploaded again with a manifest that asks for more is a new build and waits again. A build approved before is approved again at once. [PLUGINS.md](PLUGINS.md#networks-of-sites) has the details.
- **Themes.** A site can use the built-in themes, the themes in the network's `SAP13NS_THEMES_DIR`, and the themes in its own directory (`$SAP13NS_DATA_DIR/sites/<id>/themes`). A site on a plan with `theme_uploads` (Pro and Business) can upload its own under **Appearance → Themes**. On Personal the form is not shown and an upload is refused with 403.
- **Theme uploads are not reviewed.** No network administrator looks at them. Every upload goes through the checks of [THEMES.md](THEMES.md#uploading-a-theme), and it is refused unless it passes all of them. Uploaded themes then render within output and time limits. What the checks guarantee is in [SECURITY.md](SECURITY.md#uploaded-themes).
- **Theme storage.** An uploaded theme's unpacked files count against the plan's storage. They are reserved atomically before the theme is moved into place, and a replacement reserves only its growth. An upload that does not fit is refused with 413 and leaves nothing behind, and deleting a theme gives its bytes back.
- **Theme isolation.** An uploaded theme lives in the site's own directory: no other site lists, previews or serves it. It cannot take the slug of a built-in or network theme. If the operator later adds a network theme with its slug, the network's theme wins, and the site's upload shows as not loaded until it is deleted.

## Custom domains

**Adding a domain.** A site owner manages the site's domains from **My sites → Domains** (`/admin/sites/<id>/domains`); a network administrator does it from the site's page in the network admin area. Both need a second factor, both are held to the plan's limit, and every change is written to the portal's audit log.

A domain is served only once it is **verified**. Until then, every request for it gets a 404. To verify `www.example.org`, the owner creates two records and presses **Verify**:

| Record | Value |
|---|---|
| `_sap13ns-verify.www.example.org.` `TXT` | `sap13ns-site-verification=<token>`. The page shows the exact value. |
| `www.example.org.` `CNAME` | `SAP13NS_DOMAIN_TARGET`, or any host of the network |

A domain without a subdomain (`example.org`) cannot have a CNAME. It uses A and AAAA records instead, and every address it has must be one of the network's: those in `SAP13NS_DOMAIN_ADDRESSES`, or the addresses of `SAP13NS_DOMAIN_TARGET`.

**Verification.** The check passes when the TXT record has exactly the expected value and the domain points to the network:
- **The token** is derived from the site and the domain, so it is the same each time the site adds that domain, and different on every site. A record proves control of the domain for one site only.
- **Names** in the answers are normalised before they are compared. Nothing from an answer is stored: the last problem is kept as a code (`txt_missing`, `not_pointing` or `dns_error`).
- **A failed lookup** (a timeout, `SERVFAIL`) is not an answer, and it never counts against a domain.

**Domains another site claimed.** A domain that another site added, but never verified, is not lost to its real owner. The page shows the TXT record for *this* site. With that record in place, adding the domain again moves it to this site. A verified domain never moves; once it lapses, it can be claimed the same way.

**The primary domain.** Any verified host can be made the primary one. The site is then served there, its base URL changes, and its other hosts redirect to it. The default host can never be removed, and the primary one cannot be removed while it is primary.

**Daily re-checks.** The daily maintenance checks every verified custom domain again, at most once every 20 hours, so a restart does not add checks:
- **A domain lapses** after at least 3 failed checks in a row whose first is at least 72 hours old: with daily checks, the fourth failed day. Resolver errors never count, and a passing check clears the failures.
- **A lapsed domain** is no longer served. If it was the primary domain, the site goes back to its default host. The owner is e-mailed, and the lapse is written to the portal's audit log (`network.domain_lapsed`).
- **Recovering:** verifying the domain again serves it again.

**Passkeys on a custom domain.** A site's passkeys keep its default host as their RP ID. When the primary domain is custom, sign-in on it accepts them as a related origin: the default host serves `/.well-known/webauthn`, listing the custom domain. Browsers that support WebAuthn related origin requests accept the passkeys there. Elsewhere, members sign in with their password and second factor, or through the portal.

**HSTS.** On a custom domain, HSTS is sent without `includeSubDomains`, because the customer's other hosts are not the network's. Only the portal, and a site's default host while it is the primary one, get `includeSubDomains`.

**Deleted sites** keep their domains reserved until they are purged.

## TLS

The portal and the default hosts are covered by **one wildcard certificate** (`example.com` and `*.example.com`). Let's Encrypt issues one only with a DNS-01 challenge, through a plugin for your DNS provider. Each custom domain needs its own certificate. There are three ways to run TLS:

| Setup | Default hosts | Custom domains |
|---|---|---|
| nginx (`SAP13NS_TLS=proxy`) | wildcard certificate | one certificate and `server` block per domain, by hand |
| Caddy (`SAP13NS_TLS=proxy`) | wildcard certificate | on demand, automatically |
| Built-in (`SAP13NS_TLS=autocert`) | wildcard certificate from files | on demand, automatically |

[DEPLOY.md](DEPLOY.md#networks-of-sites) has the nginx and Caddy configurations.

**Caddy's on-demand TLS.** Caddy asks `http://127.0.0.1:<port>/_sap13ns/tls-ask?domain=<host>` before it gets a certificate:
- **The answer** is 200 for a host the network routes, and 404 otherwise. The 404 for a host the network does not route is the same as for a refused request.
- **Who gets an answer:** only a request that comes straight from loopback or from `SAP13NS_TRUSTED_PROXIES`. A request carrying `X-Forwarded-For`, `X-Real-IP` or `Forwarded` was relayed for a client and is refused, so the endpoint cannot tell the internet which hosts the network has.
- **The listener:** Caddy must reach sap13ns over TCP (`SAP13NS_LISTEN=127.0.0.1:8080`), because a request over the Unix socket is always refused.

**Built-in ACME (`SAP13NS_TLS=autocert`).** sap13ns terminates TLS itself on `SAP13NS_LISTEN` (`:443`) and answers plain HTTP on `SAP13NS_HTTP_LISTEN` (`:80`):
- **The static certificate** comes from `SAP13NS_TLS_CERT` and `SAP13NS_TLS_KEY`, for the portal host and the default hosts. It is read again within a minute when its files change, so a renewal needs no restart. A renewed pair that does not load leaves the previous one in use.
- **ACME certificates** are requested only for verified custom domains the network routes. The portal's own names never go to the CA (Let's Encrypt allows 50 certificates per registered domain a week).
- **Challenges:** TLS-ALPN-01 on `:443` and HTTP-01 on `:80`. The HTTP listener sends routed hosts to https and answers 404 for others.
- **Binding** to ports 443 and 80 needs `CAP_NET_BIND_SERVICE` (see [DEPLOY.md](DEPLOY.md#networks-of-sites)).

## Command-line reference

Every command takes `-env FILE`. Running servers follow every change at once.

```
sap13ns network init [-username U -email E [-name N]]
        Create the network schema, the portal on the SAP13NS_BASE_URL host,
        and a network administrator (password from stdin, or generated).
sap13ns migrate [-site S]
        Migrate the network schema, then every site's (or only S's).

sap13ns site create -slug S -owner EMAIL [-plan personal|pro|business] [-title T] [-lang L]
sap13ns site list [-status S] [-json]       Every site but the deleted ones, or those
                                            with status S.
sap13ns site suspend -slug S -reason R      The owner sees the reason.
sap13ns site resume -slug S                 A site never approved goes back to pending.
sap13ns site delete -slug S -yes            Stops serving it; its data stays.
sap13ns site purge -slug S -yes             Removes a deleted site for good: schema,
                                            files, backups, members and domains;
                                            -keep-backups keeps its backups.
sap13ns site domain add -slug S HOST        Prints the records to create.
sap13ns site domain verify [-force] HOST    Runs the DNS check; -force records the
                                            domain as verified without one.
sap13ns site domain primary HOST
sap13ns site domain remove HOST

sap13ns user create|reset-password -site S ...    -site network: a network account
sap13ns plugin list|install -site S ...
sap13ns theme list|install|remove -site S ...    the site's own themes; the same checks as
                                                 an upload, without the plan's gate or quota
sap13ns assistant reindex|status -site S ...
sap13ns export -site S -o FILE.xml|FILE.zip [-drafts] [-pii] [-no-media]
        Export one site's content (see Exporting one site).
sap13ns backup create|list|prune -site S, backup restore -site S [-replace] FILE
        The site's backups (see Backups).
```

`site create` with a plan that needs approval (Personal) leaves the site pending. It is approved or rejected under **Network → Sites** in the portal.

## Backups

Every site of the network is backed up by itself, every night, into one `.zip` that sap13ns restores and that WordPress imports as it is. The site's administrators find these backups under **Tools → Backups**, where they can download one, make one, or restore one. Operators use `sap13ns backup … -site S`.

A site backup holds one site, never the network: the portal's accounts, the plans, subscriptions, domains and secrets are not in it. Keep the network's own backups as well (see [The whole network](#the-whole-network)).

### What a backup holds

| Member | What it is |
|---|---|
| `export.xml` | A WordPress export file (WXR 1.2), as Tools → Export makes it: posts and pages in every status (trash included), attachments, comments (approved and held, never spam), the authors, categories and tags with their hierarchy, menus, featured images, and the sap13ns extensions (Markdown source, languages and translations, redirects) |
| `wp-content/uploads/` | The site's files at the paths of their attachment URLs (`YYYY/MM/name.ext`): WordPress's own layout |
| `sap13ns/settings.json` | Site settings, writing assistant and site assistant settings without their keys, theme options |
| `sap13ns/users.json` | Users and their roles and profiles, without passwords, second factors, passkeys or sessions |
| `sap13ns/media.json` | The media library: sizes, resized copies, captions, alternative texts |
| `sap13ns/menus.json`, `widgets.json` | Menus per location and language, and widgets |
| `sap13ns/appearance.json` | Customiser designs and page-builder layouts: published, draft and earlier versions |
| `sap13ns/redirects.json` | Redirect rules |
| `sap13ns/plugins.json` | The plugins installed, their versions, and their plain settings |
| `README.txt` | How to move to WordPress, or to restore |
| `sap13ns/manifest.json` | Format, site, date, and the size and SHA-256 of every other member. It is written last. |

A backup never holds password hashes, two-factor secrets or recovery codes, passkeys, sessions, plugin secrets, AI or embedding keys, the MCP token or the site secret. It holds personal data, as the site does: the e-mail addresses of users and commenters, and the IP addresses of commenters. Analytics, the audit log, revisions and autosaves are left out.

With `SAP13NS_BACKUP_PASSPHRASE_FILE` (or `SAP13NS_BACKUP_PASSPHRASE`), archives are encrypted with the format of the database backups and named `.zip.enc`. The admin downloads them decrypted, and `sap13ns backup decrypt` decrypts one on the command line.

### Where they are, and how long they stay

Backups live in `$SAP13NS_BACKUP_DIR/sites/<id>/` (by default `$SAP13NS_DATA_DIR/backups`), outside the site's own directory: directories 0700, files 0600. Next to them, `status.json` records the latest attempt, the latest success, and when the latest restores started.

After each backup, and after the safety backup of a restore, the site's backups are pruned:
- the newest backup of each of the last 7 days and of the last 4 ISO weeks (`SAP13NS_SITE_BACKUP_KEEP_DAILY`, `SAP13NS_SITE_BACKUP_KEEP_WEEKLY`), and always the newest;
- the newest 3 backups made before a restore, for 7 days at most;
- in all, at most **twice the storage of the site's plan**: past that, the oldest go first, but never the newest backup, nor the archive a restore is using.

**Backups do not count toward the storage a site uses.** Counting them would let a nightly backup push a site over its quota and stop its uploads, for space the site does not control. They are bounded instead, and that space is the operator's to provide: up to twice the plan storage of every site, on the disk of `SAP13NS_BACKUP_DIR`. Copy the backups to another machine too, because a backup on the same disk does not survive the disk.

### Nightly backups

| Variable | Default | Meaning |
|---|---|---|
| `SAP13NS_SITE_BACKUPS` | `true` | `false` turns the nightly backups off |
| `SAP13NS_SITE_BACKUP_TIME` | `03:30` | When they start, server time |
| `SAP13NS_SITE_BACKUP_CONCURRENCY` | `2` | Sites backed up at once (1 to 16) |
| `SAP13NS_SITE_BACKUP_BUDGET` | `3h` | No site starts after this long (10m to 23h) |

The nightly run backs up the portal and every site that is active, held for billing or suspended. Pending and deleted sites are not backed up. It reads each site's schema and files without loading the site, so no plugin runs and a suspended site is backed up like any other. The sites least recently tried go first: the sites one night did not reach are the first of the next.

A site is **skipped** when:
- the disk does not have twice the estimated size of its backup free (a backup never takes more than half the free disk);
- the estimate is larger than the storage of its plan (`skipped: larger than the storage of its plan`).
- another backup or restore of the site is running.

A skipped or failed site is logged, listed at the top of **Network → Sites** (with a **Latest backup** column for every site), and shown on its page and on its own **Tools → Backups**. When the passphrase file cannot be read, the run makes no backup at all rather than unencrypted ones, and says so on **Network → Sites**.

### Tools → Backups

The site's administrators (only they) can:
- **Download** a backup. The link is signed for their session, and works for an hour. An encrypted backup is decrypted on the fly, and every download is in the audit log.
- **Back up now**: one at a time, 6 an hour. With the address of a WordPress site, the archive is made to move there ([MOVING-TO-WORDPRESS.md](MOVING-TO-WORDPRESS.md)).
- **Restore** a backup of the list (not an uploaded file). This needs:
  - the site's name, typed;
  - their current password (or, with single sign-on, a sign-in of the last few minutes);
  - and at most one restore every 10 minutes, 3 an hour. This is kept with the backups, so it holds when the site is reloaded.

  The portal is never restored from a site backup: it holds the network's accounts.

### Restoring

A restore replaces the site's posts, pages, comments, terms, media and files, menus, widgets, designs and layouts, redirects, settings, theme options, and the settings of the plugins the site has installed. Users are matched by username (or e-mail address) and created when missing, without a password. They are never deleted, and the administrator restoring is never demoted or disabled.

What keeps it safe:
- **All or nothing.** The archive is checked first: the manifest, every member's size and SHA-256, and the extraction limits (no absolute paths, `..`, links or special files, and bounded sizes and counts). A site with content is then backed up ("before a restore"). The database changes in one transaction. The files are staged next to the uploads and swapped in only after the commit. A restore that fails at any point leaves the site as it was.
- **The pipelines of the site.** Content goes through the same Markdown and sanitising pipeline as anything else. Settings go through the rules of their admin pages: a value that fails is reset to the site's current one, or to the default, and the report names it. Menus keep only valid links, widgets and plugin settings are checked by the code that owns them (and are left out without it), and custom CSS is sanitised.
- **Never secrets.** API and embedding keys, the MCP token, two-factor settings, passwords, plugin secrets and the site secret stay as the site has them. A key is kept only while the provider and address it was entered for stay the same, and a restore never switches local (plain http) models on.
- **Bounded.** An archive larger than the storage of the plan is not restored, and neither is a site whose safety backup would be. The decrypted copy of an encrypted archive, and the staged files, each need twice their size free. The decrypted copy is removed when the restore ends, however it ends.
- **One site.** A restore writes only the site's schema and directory. A backup of another site can be restored too, and its users are then matched by name.
- **Audited.** `backup.restore_started`, `backup.restored` or `backup.restore_failed`, and `backup.restore_refused` for a wrong password, all name the archive.

### Command line

```
sap13ns backup create -site S [-wp-base URL]     A backup now (no plan limit: the operator's)
sap13ns backup list -site S                      Its backups, and the latest attempt
sap13ns backup verify [-passphrase-file F] ARCHIVE
sap13ns backup restore -site S [-replace] [-yes] ARCHIVE
        -replace is required when the site has content; a backup of it is made first.
        The site's plugins are loaded to check their settings, and the running
        servers reload the site.
sap13ns backup prune -site S [-keep-daily N] [-keep-weekly N]
sap13ns backup decrypt [-o OUT] ARCHIVE.zip.enc
```

### The whole network

Site backups cover the sites, not the network: the portal's accounts, the plans and subscriptions, domains and certificates, and every site's sealed secret are in the database only. Back up the whole of it for disaster recovery too:

```sh
sudo systemctl stop sap13ns
sudo -u sap13ns pg_dump --format=custom sap13ns > sap13ns-network.dump
sudo tar -C /var/lib -czf sap13ns-data.tar.gz --exclude=sap13ns/backups sap13ns
sudo systemctl start sap13ns
```

(`--exclude` leaves out the site backups, which are copied on their own.)

`sap13ns backup create` without `-site` refuses to run on a network, so disable `sap13ns-backup.timer` there: it is for single sites. Keep `SAP13NS_SECRET_KEY` with these copies. It wraps every site's own secret, so without it no site can open its sessions, its stored keys or its passkeys.

### Exporting one site

An export is the lighter way out of the network, the same one as on a single site ([README](../README.md#export)):
- **The site's administrators** use **Tools → Export** on the site. This also works while the site is held for billing, because a held site keeps its admin.
- **An operator** runs `sap13ns export -site S -o S.zip`. This also works for a suspended site, whose admin no longer answers.

The `.zip` holds the WXR file, the site's uploads and a Markdown copy of each post and page. Any sap13ns site, in a network or not, and WordPress import the WXR. An export reads the site's own schema and directory, and nothing of another site's.

An export is not a backup. It leaves out users (apart from the authors' names), settings, widgets, themes, designs and plugins, and it cannot be restored over the site.

A large `.zip` waits for its download in the site's `exports/` directory. It is not counted in the site's storage, since it is temporary, and the hourly sweep deletes it after 24 hours. It is bounded instead:
- **One export waits per site.** Starting a new one deletes the site's earlier export that nobody downloaded, whoever started it, and only one runs at a time.
- **It fits the plan.** Before a background export starts, its size is estimated: the uploads plus three copies of the text of the exported posts, pages and comments, before compression. An estimate larger than the storage of the site's plan is refused with 413. The site can still download a WXR file, or a `.zip` without the media library.
- **It fits the disk.** The export is also refused when the disk of the data directory has less than twice the estimate free, counting the export it replaces. This check also applies on a single site, where no quota does.

So a site's exports take at most one finished file and one being written, each no larger than its plan's storage.

## Limitations

- **No conversion.** A single-site install cannot be turned into a network, or into a site of one, yet. Start a network with a new database.
- **What eviction loses.** Unloading a site keeps everything in the database and on disk, and flushes its pending analytics. Some state lives in memory only, and is lost:
  - the per-IP rate limits of its sign-in, comment, registration and password-reset forms (account lockouts are in the database and survive);
  - the analytics visitor set of the day, so a visitor seen before the unload is counted again after it;
  - a passkey sign-in in progress;
  - the page and content caches, which fill up again.
- **Thrashing.** When more sites are busy than `SAP13NS_MAX_LOADED_SITES`, sites keep loading and unloading each other. Raise the limit and the memory limit together.
- **Members.** Only network administrators manage the members of a site.
- **Emergency access on a suspended site.** The site is inert meanwhile (see [Emergency access](#emergency-access)): its admin shows its plugins as not loaded, and what the plugins would do (a contact form, a widget, a cron job) does not happen. The network's sweeps only visit active sites, so its scheduled-post announcements wait until it is resumed.
- **Plans.** A site's plan changes only through billing, or when it is created from the command line. The plans themselves are edited in the database.
