# Deploying sap13ns

sap13ns runs as one binary behind nginx, with PostgreSQL on the same machine or nearby. This guide targets Debian 13 / Ubuntu 24.04 or newer. Other Linux distributions work the same way with their own package names.

```
browser ──https──▶ nginx ──unix socket──▶ sap13ns ──▶ PostgreSQL
                     └── /uploads/ straight from /var/lib/sap13ns/uploads
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

1. Download the release for your platform with `SHA256SUMS` and `SHA256SUMS.sig`, and check the archive (see [RELEASES.md](RELEASES.md)).
2. Unpack it and run the installer as root:

   ```sh
   mkdir sap13ns && tar xzf sap13ns_<version>_linux_amd64.tar.gz -C sap13ns && cd sap13ns
   sudo ./deploy/install.sh example.com
   ```

   The installer is idempotent. It:
   - creates the `sap13ns` system user and `/var/lib/sap13ns`;
   - installs the binary in `/usr/local/bin`;
   - creates the `sap13ns` database and role (peer authentication over the local socket) and enables `pgvector` when it is available;
   - writes `/etc/sap13ns/sap13ns.env` with a fresh secret key;
   - installs the systemd units and the nginx site.
3. Get a certificate. Until you have one, comment out the `443` server block:

   ```sh
   sudo certbot certonly --webroot -w /var/www/letsencrypt -d example.com
   sudo nginx -t && sudo systemctl reload nginx
   ```

4. Create the first administrator and start:

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

nginx must be able to open the socket: `usermod -aG sap13ns www-data`.

## Configuration

All settings live in `/etc/sap13ns/sap13ns.env`; the variables are listed in the [README](../README.md#configuration). Restart after a change: `sudo systemctl restart sap13ns`.

- **`SAP13NS_SECRET_KEY`** encrypts stored API keys and signs sessions and tokens. Keep a copy off the server. Changing it signs everyone out and makes stored AI keys and passkeys unusable.
- **`SAP13NS_BASE_URL`** must be the public `https://` address. Cookies are marked `Secure`, and HSTS is sent, when it is https.
- **`SAP13NS_SERVE_UPLOADS=false`** when nginx serves `/uploads/`, as in the provided configuration.
- **`SAP13NS_TRUSTED_PROXIES`** applies only to TCP listeners. Connections over the Unix socket always come from nginx, which overwrites `X-Forwarded-For`.

Everything else (site title, languages, themes, comments, two-factor policy, AI, analytics) is set in the admin.

## Operations

| Task | Command |
|---|---|
| Status and logs | `systemctl status sap13ns`, `journalctl -u sap13ns -f` |
| Health check | `curl --unix-socket /run/sap13ns/sap13ns.sock http://x/healthz` |
| Installation health | Admin → **System** (security, database, caches, updates) |
| Back up now | `sudo systemctl start sap13ns-backup` |
| List and verify backups | `sudo -u sap13ns sap13ns backup list -env /etc/sap13ns/sap13ns.env` |
| Update | `sudo sap13ns update && sudo systemctl restart sap13ns` |
| Reset a password | `sudo -u sap13ns sap13ns user reset-password -env /etc/sap13ns/sap13ns.env -username U` |

### Backups
`sap13ns-backup.timer` runs `sap13ns backup create` and `prune` every night at 03:30. Each archive holds a `pg_dump` of the database and all uploads.

- To encrypt the archives, set `SAP13NS_BACKUP_PASSPHRASE_FILE` to a file readable only by `sap13ns`, and keep a copy of the passphrase elsewhere.
- To restore, stop the service, run `sap13ns backup restore ARCHIVE`, and start it again. Migrations run on start.
- Copy the archives to another machine: a backup on the same disk doesn't protect against losing the disk.

### Updates
Administrators see a notice in the admin when a release is out. Take a backup, run `sudo sap13ns update`, then restart. The previous binary stays as `sap13ns.old` for rollback. Details are in [RELEASES.md](RELEASES.md).

## Hardening notes
- The systemd unit runs sap13ns unprivileged with a read-only system, a private `/tmp`, no capabilities and a system-call filter. Check it with `systemd-analyze security sap13ns`.
- **Don't add `MemoryDenyWriteExecute=yes`.** The WebAssembly runtime compiles plugins to machine code, which needs executable memory.
- **With PostgreSQL on the same machine**, you can also add `IPAddressDeny=any` to the unit. This blocks outbound traffic, so only do it if you don't use SMTP, AI providers, release checks or plugins with network permissions. In that case also set `SAP13NS_UPDATE_CHECK=false`.
- **Defense in depth for uploads:** files are served with a sandboxing CSP and `nosniff`; anything that isn't an image, audio, video or PDF is sent as a download.

## Troubleshooting

| Symptom | Cause |
|---|---|
| nginx returns **502** | sap13ns isn't running (`journalctl -u sap13ns`), or nginx can't open the socket: `www-data` isn't in the `sap13ns` group (restart nginx after `usermod`). |
| **Uploads return 413** | Raise `client_max_body_size` in nginx and `SAP13NS_MAX_UPLOAD_MB` together. |
| **Signed out after every request** | `SAP13NS_BASE_URL` is https but the site is opened over http, so the `Secure` cookie is dropped. |
| **Site assistant search is keyword-only** | `pgvector` isn't installed or enabled: install the package, then run `CREATE EXTENSION vector` as a superuser in the `sap13ns` database. |
| **AI requests time out behind nginx** | The provided configuration allows 360 s for `/admin/api/ai/`; keep it if you use slow local models. |
