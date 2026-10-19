---
type: page
key: install
title: Install sap13ns
slug: install
excerpt: Try sap13ns on your machine in a few minutes, then run it in production with systemd, nginx and a Unix socket.
order: 5
menu: [footer, cta, landing]
menu_label: Install
menu_label_cta: Get started
comments: closed
---
sap13ns needs three things: the `sap13ns` binary, a PostgreSQL database and, in production, nginx in front of it. There is nothing to compile and no runtime to install.

## Quick start

Use this to try sap13ns on your laptop or a test server. You need PostgreSQL running locally.

**1. Get the binary.** Download the archive for your platform from [GitHub Releases](https://github.com/vicktor/sap13ns/releases): `sap13ns_<version>_linux_amd64.tar.gz`, `linux_arm64`, `darwin_amd64` or `darwin_arm64`, together with `SHA256SUMS`. Check it and unpack it:

```bash
sha256sum -c --ignore-missing SHA256SUMS
tar xzf sap13ns_<version>_linux_amd64.tar.gz
```

The archive holds the `sap13ns` binary and the `deploy/` folder with the installer, the systemd units and the nginx site.

**2. Create a database.**

```bash
createdb sap13ns
```

**3. Write a small configuration file** called `sap13ns.env`. The secret key signs sessions and tokens; generate a fresh one with `./sap13ns keygen`:

```ini
SAP13NS_DATABASE_URL=postgres:///sap13ns?host=/tmp
SAP13NS_SECRET_KEY=paste-the-output-of-sap13ns-keygen-here
SAP13NS_BASE_URL=http://localhost:8080
SAP13NS_LISTEN=127.0.0.1:8080
```

Adjust the database URL to your setup; `postgres://user:password@localhost/sap13ns` works too.

**4. Start the server.**

```bash
./sap13ns serve -env ./sap13ns.env
```

On the first start sap13ns creates its tables and, because there are no users yet, prints a one-time installation link in the log:

```text
no users yet: finish the installation at http://localhost:8080/login/setup?token=…
```

Open it, create the first administrator and you are in. The link only works once, and only someone who can read the server log can see it.

## Production

These steps follow [DEPLOY.md](https://github.com/vicktor/sap13ns/blob/main/docs/DEPLOY.md) on a Debian 13 or Ubuntu 24.04 server, or newer, with PostgreSQL 16 or newer and nginx installed. Other distributions work the same way with their own package names.

### 1. Run the installer

Unpack the Linux archive for your server, as in the quick start, and run the installer as root:

```bash
mkdir sap13ns && tar xzf sap13ns_<version>_linux_amd64.tar.gz -C sap13ns && cd sap13ns
sudo ./deploy/install.sh example.com
```

It is safe to run again: an existing configuration, database or role is left as it is. The installer:

- creates the `sap13ns` system user, the `sap13ns-web` group and `/var/lib/sap13ns`;
- installs the binary in `/usr/local/bin`;
- creates the `sap13ns` database and role, and enables `pgvector` when it is installed. The service connects over the local socket with peer authentication, so the database needs no password;
- writes `/etc/sap13ns/sap13ns.env`, readable only by root and the service, with a fresh secret key;
- installs the systemd units of the service and of the nightly backup, and the nginx site.

### 2. The socket and nginx

sap13ns listens on the Unix socket `/run/sap13ns/sap13ns.sock`, with mode `0660` and the group `sap13ns-web`. nginx (`www-data`) joins that group and nothing else, so it can reach the socket but cannot read the configuration file or the data directory. If you set things up by hand, never add `www-data` to the `sap13ns` group.

nginx passes every request to sap13ns over the socket, uploads included: sap13ns serves them itself with a sandboxing Content Security Policy.

### 3. Configure

The installer writes the configuration from `deploy/sap13ns.env.example`. These are the lines that matter:

```ini
SAP13NS_DATABASE_URL=postgres:///sap13ns?host=/run/postgresql
SAP13NS_SECRET_KEY=output-of-sap13ns-keygen
SAP13NS_BASE_URL=https://example.com
SAP13NS_LISTEN=unix:/run/sap13ns/sap13ns.sock
SAP13NS_SOCKET_GROUP=sap13ns-web
SAP13NS_DATA_DIR=/var/lib/sap13ns
```

`SAP13NS_BASE_URL` is the public address of the site. With an `https` URL, cookies are marked `Secure` and HSTS is sent. Keep a copy of `SAP13NS_SECRET_KEY` somewhere other than the server: changing it signs everyone out and makes stored keys and passkeys unusable. Restart the service after any change.

### 4. HTTPS

Get a certificate. Until you have one, comment out the `443` server block of the nginx site:

```bash
sudo certbot certonly --webroot -w /var/www/letsencrypt -d example.com
sudo nginx -t && sudo systemctl reload nginx
```

### 5. Create the first administrator and start

```bash
sudo -u sap13ns sap13ns user create -env /etc/sap13ns/sap13ns.env \
  -username ana -email ana@example.com -role admin
sudo systemctl start sap13ns
```

If you pipe a password into `user create` it uses that one; otherwise it generates a strong password and prints it once. If you start the service without creating a user, it prints a one-time `/login/setup?token=…` link in its log instead (`sudo journalctl -u sap13ns | grep setup`).

Open `https://example.com/admin` and sign in. Before anything else you will be asked to set up a second factor: an authenticator app or a passkey.

Migrations run every time the server starts, inside a transaction and with a lock so two processes never collide. To apply them before the first start, run `sudo -u sap13ns sap13ns migrate -env /etc/sap13ns/sap13ns.env`.

### Installing by hand

The installer's steps are short and commented in [`deploy/install.sh`](https://github.com/vicktor/sap13ns/blob/main/deploy/install.sh). These are the files it installs:

| File | Installed as |
|---|---|
| `deploy/sap13ns.env.example` | `/etc/sap13ns/sap13ns.env` (root:sap13ns, `0640`) |
| `deploy/systemd/sap13ns.service` | `/etc/systemd/system/` |
| `deploy/systemd/sap13ns-backup.service` and `.timer` | `/etc/systemd/system/` |
| `deploy/nginx/sap13ns.conf` | `/etc/nginx/sites-available/` |

The service unit runs sap13ns as the `sap13ns` user with the `sap13ns-web` group added, and with systemd's sandboxing options turned on.

## Backups

`sap13ns backup create|list|verify|restore|prune` handles backups. An archive holds a PostgreSQL dump (made with `pg_dump`, so the PostgreSQL client tools must be installed) and the uploads directory, and is written with mode `0600`. When `SAP13NS_BACKUP_PASSPHRASE_FILE` points to a file readable only by `sap13ns`, archives are encrypted with AES-256-GCM, with a key derived from the passphrase with Argon2id. Keep a copy of the passphrase elsewhere: without it an archive cannot be restored. Before anything is restored, every file in the archive is checked against its manifest.

The installer enables `sap13ns-backup.timer`, which runs `backup create` and then `backup prune` every night at 03:30. Pruning keeps 7 daily, 4 weekly and 12 monthly archives by default. To make a backup now, and to see the archives you have:

```bash
sudo systemctl start sap13ns-backup
sudo -u sap13ns sap13ns backup list -env /etc/sap13ns/sap13ns.env
```

To restore, stop the service, run `sudo -u sap13ns sap13ns backup restore -env /etc/sap13ns/sap13ns.env ARCHIVE`, and start it again.

The timer also makes the site's own backup (`backup create -format site`): a `.zip` that WordPress imports as it is, and that **Tools → Backups** in the admin lists, downloads and restores. It keeps 7 daily and 4 weekly ones. To leave for WordPress, follow [the guide](https://github.com/vicktor/sap13ns/blob/main/docs/MOVING-TO-WORDPRESS.md).

Copy the archives somewhere other than the server itself. A backup that lives on the same disk is not a backup.

## Updating

Administrators see a notice in the admin when a new release is out. Take a backup, then install it and restart:

```bash
sudo systemctl start sap13ns-backup
sudo sap13ns update
sudo systemctl restart sap13ns
```

`sap13ns update` downloads the release for your platform, checks its signed checksums and replaces the binary atomically. The previous binary stays as `/usr/local/bin/sap13ns.old`, so rolling back is moving it into place and restarting. Migrations run on start. Details are in [RELEASES.md](https://github.com/vicktor/sap13ns/blob/main/docs/RELEASES.md).

## Multisite

One installation can serve a whole network of sites: a portal where people have one account, and any number of sites, each with its own PostgreSQL schema, files, users and plugins. It is off unless you turn it on, and it starts with a new database: an existing single-site install cannot be turned into a network yet.

1. Point the portal's host and a wildcard for the sites to the server, and get a wildcard certificate for them. Custom domains get their certificates from Caddy's on-demand TLS, from the built-in ACME client, or one by one with nginx.
2. Add `SAP13NS_MULTISITE=true` and `SAP13NS_SITES_DOMAIN` to the configuration. `SAP13NS_BASE_URL` becomes the portal's address. Disable `sap13ns-backup.timer`: a network backs up each of its sites by itself every night.
3. Create the network, its portal and a first network administrator, then start the service:

   ```bash
   sudo -u sap13ns sap13ns network init -env /etc/sap13ns/sap13ns.env -username ana -email ana@example.com
   sudo systemctl start sap13ns
   ```

4. Let people register on the portal, when you open registration, and request sites, or create them with `sap13ns site create -slug blog -owner ana@example.com -plan pro`. `sap13ns site list`, `suspend`, `resume`, `delete`, `purge` and `domain` manage them.

A network has single sign-on through the portal, a network admin area, plans with storage and custom-domain limits, Stripe billing, and custom domains verified through DNS and served over TLS. Every site is backed up each night, and its administrators download and restore its backups under **Tools → Backups**. Those backups hold the sites, not the network, so keep a `pg_dump` and a copy of the data directory as well. Sites upload their own themes on the plans that allow it. One thing is not available on a network yet: an existing single-site install cannot be turned into a network site. The guide is [MULTISITE.md](https://github.com/vicktor/sap13ns/blob/main/docs/MULTISITE.md), and [DEPLOY.md](https://github.com/vicktor/sap13ns/blob/main/docs/DEPLOY.md#networks-of-sites) has the nginx and Caddy configurations.

If you would rather not run a server at all, there is [hosting on sap13ns.site](/hosting).

The complete guide, including the hardening notes and troubleshooting, is in [DEPLOY.md](https://github.com/vicktor/sap13ns/blob/main/docs/DEPLOY.md).
