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
5. **Sign in** to `https://example.com/admin` as `ops`, and set up a second factor: the network admin area needs it.
6. **The first sites.** Either let people register on the portal and request a Personal site from **My sites**, or create sites from the command line. The owner must be a network account:

   ```sh
   sudo -u sap13ns sap13ns user create -env /etc/sap13ns/sap13ns.env -site network -username owner -email owner@example.com -role subscriber
   sudo -u sap13ns sap13ns site create -env /etc/sap13ns/sap13ns.env -slug blog -owner owner@example.com -plan pro -title "Blog" -lang en
   ```

   A Personal site starts as `pending`, until a network administrator approves it under **Network → Sites**. The owner is e-mailed when it is approved or rejected.

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
   - the site is served;
   - the callback is one of the site's own hosts;
   - the account is a member;
   - a staff role has a second factor.
3. The portal sends back a single-use code that is valid for 60 seconds.
4. The site redeems the code and opens a session.

When the account is already signed in to the portal, this takes no clicks. The site's user is created the first time and follows the membership from then on: role, e-mail, name and status. It is never matched to a local user by e-mail. A local user can link their account explicitly from their profile.

Removing a member, or disabling an account, disables the linked users and ends their sessions at once, also on sites that are not loaded. A session that came through the portal does not ask for the site's second factor: the portal requires one for every staff role.

**Operator access.** A network administrator can open any served site with **Open its administration** on the site's page, even without being a member. This signs them in as a separate *operator* user with the administrator role (`operator-<username>`, shown as "(network operator)"). It is written to both audit logs: `sso.operator` on the portal and `login.operator` on the site. The operator user is disabled when its account stops being a network administrator.

**Passkeys.** Network passkeys belong to the portal host. A site's passkeys belong to its default host, the RP ID fixed when the site was created, and they keep working when the primary domain changes (see [Custom domains](#custom-domains)). The network's base domain is never an RP ID.

## Plans, billing and limits

Plans are rows of `network.plans`. **Network → Plans** lists them. The network starts with three:

| Plan | Price | Approval | Storage | Custom domains | Plugin uploads |
|---|---|---|---|---|---|
| Personal | free, non-commercial | by a network administrator | 1 GB | 0 | no (the catalogue only) |
| Pro | Stripe subscription | none | 10 GB | 2 | yes, reviewed |
| Business | Stripe subscription | none | 50 GB | 2 | yes, reviewed |

**One subscription is one site.** Each account may have one Personal site.

**Billing** is off until `SAP13NS_STRIPE_SECRET_KEY` is set. The network then has Personal sites, plus any site an operator creates on another plan with `site create -plan`. With Stripe, an owner upgrades a site from **My sites**:
1. Stripe Checkout takes the payment.
2. The webhook (`POST /billing/webhook` on the portal) gives the site its plan.
3. **Manage billing** opens Stripe's customer portal.

A site whose renewal failed stays online for `SAP13NS_BILLING_GRACE_DAYS`, then it is held. A canceled or unpaid subscription holds the site, and paying lifts the hold. Nothing is deleted for billing. [BILLING.md](BILLING.md) covers the Stripe setup.

**Limits.** A plan's limits apply to every site on it:
- **Storage** counts uploads, their image variants and plugin packages. Each upload reserves its size with one atomic update before anything is written, so concurrent uploads never go over the limit. An upload that does not fit is refused with 413. The daily measurement replaces the count with the real size of the site's schema and directory.
- **Custom domains** count verified and unverified ones.
- **A downgrade** only blocks additions: files, domains and plugins over the new limits stay.

## Plugins and themes

- **The catalogue.** The plugins installed on the portal (valid, and not blocked by the scanner) form the network's catalogue. Every site sees them under **Plugins → From the network** and installs them from there, disabled. The site's administrator then reviews their permissions and enables them, as on any site.
- **Uploads.** Personal sites cannot upload plugins. On Pro and Business, an uploaded plugin is installed disabled and **held for review**: a network administrator approves or rejects it under **Network → Plugin reviews**, which shows its permissions, its scan report and its hashes. Only then can the site's administrator enable it.
- **The review pin.** The approval is pinned to the reviewed build: the module, the assets and the permissions it asks for. The same module uploaded again with a manifest that asks for more is a new build and waits again. A build approved before is approved again at once. [PLUGINS.md](PLUGINS.md#networks-of-sites) has the details.
- **Themes.** A site can use the built-in themes, the themes in the network's `SAP13NS_THEMES_DIR`, and the themes in its own directory. There is no theme upload on a network yet.

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
                                            files, members and domains.
sap13ns site domain add -slug S HOST        Prints the records to create.
sap13ns site domain verify [-force] HOST    Runs the DNS check; -force records the
                                            domain as verified without one.
sap13ns site domain primary HOST
sap13ns site domain remove HOST

sap13ns user create|reset-password -site S ...    -site network: a network account
sap13ns plugin list|install -site S ...
sap13ns assistant reindex|status -site S ...
```

`site create` with a plan that needs approval (Personal) leaves the site pending. It is approved or rejected under **Network → Sites** in the portal.

## Backups

`sap13ns backup create` and `backup restore` refuse to run on a network. `list`, `verify` and `prune` still work on existing archives. An archive holds one database dump and one uploads directory. On a network that would mean every site's schema with the network's directory only, and a restore would replace every site at once. Per-site archives (`pg_dump --schema=site_<id>` with the site's directory) will come later.

Until then, back up a network with the standard tools:

```sh
sudo systemctl stop sap13ns
sudo -u sap13ns pg_dump --format=custom sap13ns > sap13ns-network.dump
sudo tar -C /var/lib -czf sap13ns-data.tar.gz sap13ns
sudo systemctl start sap13ns
```

Keep `SAP13NS_SECRET_KEY` with the backups. It wraps every site's own secret, so without it no site can open its sessions, its stored keys or its passkeys.

## Limitations

- **No conversion.** A single-site install cannot be turned into a network, or into a site of one, yet. Start a network with a new database.
- **What eviction loses.** Unloading a site keeps everything in the database and on disk, and flushes its pending analytics. Some state lives in memory only, and is lost:
  - the per-IP rate limits of its sign-in, comment, registration and password-reset forms (account lockouts are in the database and survive);
  - the analytics visitor set of the day, so a visitor seen before the unload is counted again after it;
  - a passkey sign-in in progress;
  - the page and content caches, which fill up again.
- **Thrashing.** When more sites are busy than `SAP13NS_MAX_LOADED_SITES`, sites keep loading and unloading each other. Raise the limit and the memory limit together.
- **Members.** Only network administrators manage the members of a site.
- **Plans.** A site's plan changes only through billing, or when it is created from the command line. The plans themselves are edited in the database.
- **Backups:** see [Backups](#backups).
- **Theme uploads** do not exist yet, so the `theme_uploads` setting of a plan has no effect.
