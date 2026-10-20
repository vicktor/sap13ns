# Deploying sap13ns

sap13ns runs as one binary behind nginx, with PostgreSQL on the same machine or nearby. This guide targets Debian 13 / Ubuntu 24.04 or newer. Other Linux distributions work the same way with their own package names.

```
browser ──https──▶ nginx ──unix socket──▶ sap13ns ──▶ PostgreSQL
                  (group sap13ns-web)       └── /var/lib/sap13ns (private)
```

## Requirements

- Linux on amd64 or arm64. One vCPU and 512 MB of RAM are enough for a small site.
- PostgreSQL 16 or newer (18 recommended). The optional `pgvector` extension improves the site assistant's search.
- nginx 1.25 or newer, and a TLS certificate (for example from Let's Encrypt).

```sh
sudo apt install postgresql nginx certbot
sudo apt install postgresql-18-pgvector     # optional, for the site assistant
```

## Install

### The guided installer (recommended)
From an unpacked release, or a clone of the repository, run as root:

```sh
mkdir sap13ns && tar xzf sap13ns_<version>_linux_amd64.tar.gz -C sap13ns && cd sap13ns
sudo ./deploy/setup.sh
```

In a clone without a binary, it offers to download the latest release for the machine and checks it against `SHA256SUMS`. It speaks English or Spanish, after the system's language. It shows dialogs (`whiptail`) when they are available; `--plain` asks plain questions instead, and `--dry-run` asks everything and only shows what it would do. Nothing changes until you confirm the summary.

1. **Questions:** one site or a network of sites, the domain (and, for a network, the domain of the sites), the first administrator, how to get HTTPS, outgoing e-mail, and the visitors' countries in Analytics.
2. **Checks:** PostgreSQL 16 or newer, `pgvector`, nginx, certbot (and its Cloudflare plugin for a network's wildcard certificate), curl, openssl and gpg. It offers to install what is missing with `apt`. Where the system only has an older PostgreSQL, it adds the PostgreSQL project's repository (apt.postgresql.org). It also checks that the domain resolves, and lists the other sites nginx already serves.
3. **Installation:** the user and directories, the binary, the database and role, `/etc/sap13ns/sap13ns.env` with a fresh secret key, the backup passphrase, the systemd units and timers (nightly backups; a single site's or the whole network's), the nginx site, and the certificate:
   - one site: Let's Encrypt over HTTP;
   - a network: a wildcard certificate from Let's Encrypt through Cloudflare's DNS, with an API token;
   - or certificate files you already have, or plain HTTP for now.

   Then it creates the first administrator (or initialises the network with them), starts sap13ns and checks that it answers.
4. **The end:** the address, the administrator's password (shown once) and the next steps, which are also saved in `/root/sap13ns-setup.txt` without the password. The log of every command is `/var/log/sap13ns-setup.log`.

It only adds its own files. No nginx block is a `default_server`, the default site stays, and the firewall, hostname and SSH are left alone, so it can run on a server that already hosts other sites. Run it again to upgrade the binary and the units, or to set up HTTPS once the DNS is ready. The configuration and the database are always kept.

### The installer without questions
`deploy/install.sh` installs the same pieces for one site, with no questions and no packages. It is idempotent. It:
- creates the `sap13ns` system user, the `sap13ns-web` group (only nginx joins it, to reach the socket) and `/var/lib/sap13ns`;
- installs the binary in `/usr/local/bin`;
- creates the `sap13ns` database and role (peer authentication over the local socket) and enables `pgvector` when it is available;
- writes `/etc/sap13ns/sap13ns.env` with a fresh secret key;
- installs the systemd units and the nginx site.

```sh
sudo ./deploy/install.sh example.com
```

Then:

1. Get a certificate. Until you have one, comment out the `443` server block:

   ```sh
   sudo certbot certonly --webroot -w /var/www/letsencrypt -d example.com
   sudo nginx -t && sudo systemctl reload nginx
   ```

2. Create the first administrator and start:

   ```sh
   sudo -u sap13ns sap13ns user create -env /etc/sap13ns/sap13ns.env -username admin -email you@example.com -role admin
   sudo systemctl start sap13ns
   ```

   Open `https://example.com/admin` and sign in. You'll be asked to set up two-factor authentication or a passkey first.

### Manual install
If you prefer to do it by hand, the installer's steps are short and commented in [`deploy/install.sh`](../deploy/install.sh). The files it installs are:

| File | Installed as |
|---|---|
| `deploy/sap13ns.env.example` | `/etc/sap13ns/sap13ns.env` (root:sap13ns, 0640) |
| `deploy/systemd/sap13ns.service` | `/etc/systemd/system/` |
| `deploy/systemd/sap13ns-backup.service`, `.timer` | `/etc/systemd/system/` |
| `deploy/nginx/sap13ns.conf` | `/etc/nginx/sites-available/` |
| `deploy/nginx/sap13ns-network.conf` (a network) | `/etc/nginx/sites-available/sap13ns.conf` |
| `deploy/sap13ns-network-backup` and `deploy/systemd/sap13ns-network-backup.*` (a network) | `/usr/local/sbin/`, `/etc/systemd/system/` |
| `deploy/systemd/sap13ns-geoip.*` | `/etc/systemd/system/` |

nginx must be able to open the socket, and nothing else: `groupadd --system sap13ns-web && usermod -aG sap13ns-web www-data`. Never add `www-data` to the `sap13ns` group, which can read the env file and the data directory.

## Configuration

All settings live in `/etc/sap13ns/sap13ns.env`; the variables are listed in the [README](../README.md#configuration). Restart after a change: `sudo systemctl restart sap13ns`.

- **`SAP13NS_SECRET_KEY`** encrypts stored API keys and signs sessions and tokens. Keep a copy off the server. Changing it signs everyone out and makes stored AI keys and passkeys unusable.
- **`SAP13NS_BASE_URL`** must be the public `https://` address. Cookies are marked `Secure`, and HSTS is sent, when it is https.
- **`SAP13NS_SOCKET_GROUP=sap13ns-web`** gives that group, and only that group, access to the socket. The service joins it through `SupplementaryGroups` in the unit.
- **`SAP13NS_TRUSTED_PROXIES`** applies only to TCP listeners. Connections over the Unix socket always come from nginx, which overwrites `X-Forwarded-For`.

Everything else (site title, languages, themes, comments, two-factor policy, AI, analytics) is set in the admin.

## Operations

| Task | Command |
|---|---|
| Status and logs | `systemctl status sap13ns`, `journalctl -u sap13ns -f` |
| Health check | `curl --unix-socket /run/sap13ns/sap13ns.sock http://x/healthz` |
| Installation health | Admin → **System** (security, database, caches, updates) |
| Back up now | `sudo systemctl start sap13ns-backup` |
| List and verify backups | `sudo -u sap13ns sap13ns backup list -env /etc/sap13ns/sap13ns.env` (add `-format site` for the site's own) |
| Export the content | `sudo -u sap13ns sap13ns export -env /etc/sap13ns/sap13ns.env -o /var/lib/sap13ns/site.zip` |
| Update | `sudo sap13ns update && sudo systemctl restart sap13ns` |
| Reset a password | `sudo -u sap13ns sap13ns user reset-password -env /etc/sap13ns/sap13ns.env -username U` |
| Lost the sign-in address | `sudo -u sap13ns sap13ns settings login-path -env /etc/sap13ns/sap13ns.env` prints it; `-reset` puts back `/login` |

### Backups
`sap13ns-backup.timer` runs every night at 03:30 and makes two kinds of archive, each pruned after it is made:
- **The database and uploads** (`sap13ns backup create`): a `pg_dump` of the database and all uploads, for disaster recovery. It keeps 7 daily, 4 weekly and 12 monthly archives (`SAP13NS_BACKUP_KEEP_*`). To restore, stop the service, run `sap13ns backup restore ARCHIVE`, and start it again. Migrations run on start.
- **The site's own backup** (`sap13ns backup create -format site`): a `.zip` that WordPress imports as it is ([MOVING-TO-WORDPRESS.md](MOVING-TO-WORDPRESS.md)), and that **Tools → Backups** lists, downloads and restores. These go to `$SAP13NS_BACKUP_DIR/site/`. It keeps 7 daily and 4 weekly backups (`SAP13NS_SITE_BACKUP_KEEP_*`), plus the 3 latest safety backups made before a restore, for 7 days. [MULTISITE.md](MULTISITE.md#backups) describes the archive, the restore and its checks, which are the same on a single site.

Either kind:
- **Encryption.** To encrypt the archives, set `SAP13NS_BACKUP_PASSPHRASE_FILE` to a file readable only by `sap13ns`, and keep a copy of the passphrase elsewhere. The server reads it too, to encrypt what the Backups page makes, and decrypts downloads on the fly. `sap13ns backup decrypt ARCHIVE.enc` decrypts one by hand.
- **Disk.** A backup never starts without twice its estimated size free. A skipped one is logged, and shown on the Backups page.
- **Off the machine.** Copy the archives to another machine: a backup on the same disk doesn't protect against losing the disk.

### Exports
Administrators export the site's content from **Tools → Export**, and `sap13ns export -o FILE.xml|FILE.zip` does the same from the shell. The [README](../README.md#export) lists what an export holds.

- **Streaming.** A WXR file, and a `.zip` whose uploads add up to less than 64 MiB, stream straight to the browser. The provided nginx configuration gives `/admin/tools/export` 600-second timeouts, and the response asks nginx not to buffer it (`X-Accel-Buffering: no`). Keep both.
- **Larger `.zip` files** are written in the background to `$SAP13NS_DATA_DIR/exports/`, with mode 0600, one at a time. The directory is never served. The administrator who asked downloads the file once, from a signed link, within 24 hours. The file is deleted after the download, and the maintenance sweep removes expired ones. The data directory needs room for one copy of the uploads.
- **Uploads in the data directory.** If `SAP13NS_UPLOADS_DIR` is the data directory itself, the exports directory would be public, so background exports are refused. Use the command line, or move the uploads to their default place (`$SAP13NS_DATA_DIR/uploads`).
- **Personal data.** An export may hold e-mail and IP addresses when the administrator asks for them, and the command-line export does with `-pii`. Keep such files as safe as backups. The command writes them with mode 0600.

### Visitors by country
**Analytics** can show where visits come from, on a world map and in a list, when the server has a country database. Only the visitor's country is kept, in the daily aggregates. The address is looked up in memory and never stored or logged.

1. Choose where the database goes, in the env file: `SAP13NS_GEOIP_DB=/var/lib/sap13ns/geoip/country.mmdb`.
2. Install the monthly update from `deploy/systemd`, and run the first download:

   ```sh
   install -m 0644 sap13ns-geoip.service sap13ns-geoip.timer /etc/systemd/system/
   install -d -o sap13ns -g sap13ns -m 0750 /var/lib/sap13ns/geoip
   systemctl daemon-reload && systemctl enable --now sap13ns-geoip.timer
   systemctl start sap13ns-geoip && journalctl -u sap13ns-geoip -n 5
   ```

3. Restart sap13ns once, so that it reads the new setting. Later updates need no restart: the server loads a replaced file within a few minutes.

`sap13ns geoip update` downloads DB-IP's free "IP to Country Lite" database (CC BY 4.0; the Analytics page credits it) from `download.db-ip.com`. It checks the file before it replaces the old one. `sap13ns geoip status` shows the date of the loaded file and `sap13ns geoip lookup IP` tests an address. MaxMind's GeoLite2 Country also works: keep it up to date with MaxMind's `geoipupdate` instead of the timer, and point `SAP13NS_GEOIP_DB` at it.

Without the setting, or while the file is missing or damaged, visits are counted as before, just without a country. On a network the setting covers every site.

### Updates
Administrators see a notice in the admin when a release is out. Take a backup, run `sudo sap13ns update`, then restart. The previous binary stays as `sap13ns.old` for rollback. Details are in [RELEASES.md](RELEASES.md).

## Networks of sites

A network (`SAP13NS_MULTISITE=true`) serves the portal on the `SAP13NS_BASE_URL` host, and its sites on `<slug>.<SAP13NS_SITES_DOMAIN>` and on their custom domains. [MULTISITE.md](MULTISITE.md) explains the setup, the variables and the commands. Every host reaches the same process, so the proxy needs:
- a wildcard DNS record and a wildcard certificate for the sites;
- `Host` passed through unchanged, because sap13ns routes on it;
- a way to get certificates for custom domains.

`/healthz` answers on any host. A wildcard certificate needs a DNS-01 challenge, for example `certbot certonly --dns-<provider> -d example.com -d '*.example.com'`, with the certbot plugin for your DNS provider.

### nginx with a wildcard certificate

Keep `deploy/nginx/sap13ns.conf` as it is (the upstream, the proxy headers and the `location` blocks). Change its `server` blocks:

```nginx
# Port 80: ACME HTTP-01 challenges for every host (custom domains), and https.
server {
    listen 80 default_server;
    listen [::]:80 default_server;
    server_name _;

    location /.well-known/acme-challenge/ { root /var/www/letsencrypt; }
    location / { return 301 https://$host$request_uri; }
}

# The portal and every default host: one wildcard certificate.
server {
    listen 443 ssl;
    listen [::]:443 ssl;
    http2 on;
    server_name example.com *.example.com;

    ssl_certificate     /etc/letsencrypt/live/example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/example.com/privkey.pem;

    # ...the rest of the 443 block of deploy/nginx/sap13ns.conf...
}
```

nginx cannot get certificates on demand. A custom domain needs its own certificate and its own `server` block, once the site has verified the domain:
1. Get the certificate: `certbot certonly --webroot -w /var/www/letsencrypt -d www.example.org`.
2. Copy the 443 block, with `server_name www.example.org;` and the new certificate.
3. Reload nginx.

For more than a few custom domains, use Caddy or the built-in ACME client.

### Caddy with on-demand certificates

Caddy gets a certificate for each custom domain when its first visitor arrives, after asking sap13ns whether the network serves that host. sap13ns must listen on loopback TCP, because the ask endpoint refuses requests over the Unix socket:

```sh
# /etc/sap13ns/sap13ns.env
SAP13NS_LISTEN=127.0.0.1:8080
SAP13NS_TLS=proxy
```

```caddyfile
{
	email ops@example.com
	on_demand_tls {
		ask http://127.0.0.1:8080/_sap13ns/tls-ask
	}
}

# The portal and every default host: a wildcard certificate through DNS-01,
# which needs the Caddy module of your DNS provider (built with xcaddy).
example.com, *.example.com {
	tls {
		dns <provider> {env.DNS_API_TOKEN}
	}
	reverse_proxy 127.0.0.1:8080
}

# Custom domains: one certificate per verified domain, on demand.
https:// {
	tls {
		on_demand
	}
	reverse_proxy 127.0.0.1:8080
}
```

`reverse_proxy` passes `Host` through and sets `X-Forwarded-For`. sap13ns trusts that header from `127.0.0.1` by default (`SAP13NS_TRUSTED_PROXIES`). The ask endpoint answers only Caddy's own requests: loopback, without forwarded headers. When a site removes a domain, or the domain lapses, the endpoint answers 404 for it again, and Caddy no longer renews its certificate.

### Without a proxy: the built-in ACME client

With `SAP13NS_TLS=autocert`, sap13ns terminates TLS itself:

```sh
# /etc/sap13ns/sap13ns.env
SAP13NS_TLS=autocert
SAP13NS_LISTEN=:443
SAP13NS_HTTP_LISTEN=:80
SAP13NS_TLS_CERT=/etc/sap13ns/tls/fullchain.pem
SAP13NS_TLS_KEY=/etc/sap13ns/tls/privkey.pem
SAP13NS_ACME_EMAIL=ops@example.com
```

- **The wildcard certificate.** `SAP13NS_TLS_CERT` and `SAP13NS_TLS_KEY` cover the portal and the default hosts, and sap13ns picks up a renewal within a minute. A certbot deploy hook can copy it where the `sap13ns` user can read it:

  ```sh
  # /etc/letsencrypt/renewal-hooks/deploy/sap13ns.sh
  install -D -m 0640 -g sap13ns "$RENEWED_LINEAGE/fullchain.pem" /etc/sap13ns/tls/fullchain.pem
  install -D -m 0640 -g sap13ns "$RENEWED_LINEAGE/privkey.pem" /etc/sap13ns/tls/privkey.pem
  ```

- **Custom domains** get their certificates from Let's Encrypt, cached in `/var/lib/sap13ns/cache/autocert`. Set `SAP13NS_ACME_DIRECTORY=https://acme-staging-v02.api.letsencrypt.org/directory` to try the setup against the staging CA first.
- **Ports.** The unit drops every capability, so it needs one back to bind ports 443 and 80:

  ```ini
  # /etc/systemd/system/sap13ns.service.d/autocert.conf
  [Service]
  CapabilityBoundingSet=CAP_NET_BIND_SERVICE
  AmbientCapabilities=CAP_NET_BIND_SERVICE
  ```

  Run `systemctl daemon-reload`, then restart.

### Backups of a network

A network backs up every site by itself each night (`SAP13NS_SITE_BACKUPS`, `SAP13NS_SITE_BACKUP_TIME` and the other variables in [MULTISITE.md](MULTISITE.md#backups)), into `$SAP13NS_BACKUP_DIR/sites/<id>/`. Disable `sap13ns-backup.timer`: `sap13ns backup create` without `-site` refuses to run on a network. Back up the database and the data directory as well, with `pg_dump` and a copy of the directory, as [MULTISITE.md](MULTISITE.md#the-whole-network) shows: site backups do not hold the network's accounts, plans, domains or secrets.

Plan the space: a site's backups take at most twice the storage of its plan, and none of it counts toward the plan.

## Hardening notes
- The systemd unit runs sap13ns unprivileged with a read-only system, a private `/tmp`, no capabilities and a system-call filter. Check it with `systemd-analyze security sap13ns`.
- **Don't add `MemoryDenyWriteExecute=yes`.** The WebAssembly runtime compiles plugins to machine code, which needs executable memory.
- **With PostgreSQL on the same machine**, you can also add `IPAddressDeny=any` to the unit. This blocks outbound traffic, so only do it if you don't use SMTP, AI providers, release checks or plugins with network permissions. In that case also set `SAP13NS_UPDATE_CHECK=false`.
- **nginx can't read anything of sap13ns's:** it only reaches the socket. Uploads are served by sap13ns with a sandboxing CSP and `nosniff`; anything that isn't an image, audio, video or PDF is sent as a download.

## Troubleshooting

| Symptom | Cause |
|---|---|
| nginx returns **502** | sap13ns isn't running (`journalctl -u sap13ns`), or nginx can't open the socket: `www-data` isn't in the `sap13ns-web` group (restart nginx after `usermod`). |
| **Uploads return 413** | Raise `client_max_body_size` in nginx and `SAP13NS_MAX_UPLOAD_MB` together. |
| **Signed out after every request** | `SAP13NS_BASE_URL` is https but the site is opened over http, so the `Secure` cookie is dropped. |
| **Site assistant search is keyword-only** | `pgvector` isn't installed or enabled: install the package, then run `CREATE EXTENSION vector` as a superuser in the `sap13ns` database. |
| **An export download stops halfway** | A proxy timeout or buffering: keep the `/admin/tools/export` block of the provided nginx configuration, or export from the shell with `sap13ns export`. |
| **AI requests time out behind nginx** | The provided configuration allows 360 s for `/admin/api/ai/`; keep it if you use slow local models. |
