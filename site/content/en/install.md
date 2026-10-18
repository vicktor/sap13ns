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

**1. Get the binary.** Download the build for your platform from [GitHub Releases](https://github.com/vicktor/sap13ns/releases), name it `sap13ns` and make it executable:

```bash
chmod +x sap13ns
```

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
no users yet: finish the installation at http://localhost:8080/setup?token=…
```

Open it, create the first administrator and you are in. The link only works once, and only someone who can read the server log can see it.

## Production

These steps assume a Debian or Ubuntu server with PostgreSQL and nginx installed. Adapt paths and user names to your distribution.

### 1. Install the binary

```bash
sudo install -m 0755 sap13ns /usr/local/bin/
sap13ns version
```

### 2. Create a system user and the database

```bash
sudo useradd --system --home /var/lib/sap13ns --create-home --shell /usr/sbin/nologin sap13ns
sudo -u postgres createuser sap13ns
sudo -u postgres createdb -O sap13ns sap13ns
```

The `sap13ns` system user connects to PostgreSQL over the local socket with peer authentication, so the database needs no password.

### 3. Configure

Create `/etc/sap13ns/sap13ns.env`, readable only by root and the service:

```ini
SAP13NS_DATABASE_URL=postgres:///sap13ns?host=/run/postgresql
SAP13NS_SECRET_KEY=output-of-sap13ns-keygen
SAP13NS_BASE_URL=https://example.com
SAP13NS_LISTEN=unix:/run/sap13ns/sap13ns.sock
SAP13NS_DATA_DIR=/var/lib/sap13ns
SAP13NS_SERVE_UPLOADS=false
```

```bash
sudo mkdir -p /etc/sap13ns
sap13ns keygen   # paste the result into SAP13NS_SECRET_KEY
sudo chown root:sap13ns /etc/sap13ns/sap13ns.env
sudo chmod 0640 /etc/sap13ns/sap13ns.env
```

`SAP13NS_BASE_URL` is the public address of the site. With an `https` URL, secure cookies and HSTS are switched on automatically. `SAP13NS_SERVE_UPLOADS=false` leaves uploaded files to nginx, which is faster.

### 4. Run the migrations

```bash
sudo -u sap13ns sap13ns migrate -env /etc/sap13ns/sap13ns.env
```

The server also applies pending migrations every time it starts, inside a transaction and with a lock so two processes never collide. Running them by hand first lets you see the result before the site goes live.

### 5. The systemd unit

Save this as `/etc/systemd/system/sap13ns.service`:

```ini
[Unit]
Description=sap13ns
After=network.target postgresql.service
Requires=postgresql.service

[Service]
User=sap13ns
Group=sap13ns
EnvironmentFile=/etc/sap13ns/sap13ns.env
ExecStart=/usr/local/bin/sap13ns serve
RuntimeDirectory=sap13ns
RuntimeDirectoryMode=0750
Restart=on-failure
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadWritePaths=/var/lib/sap13ns

[Install]
WantedBy=multi-user.target
```

`RuntimeDirectory` creates `/run/sap13ns` for the socket. sap13ns creates the socket with mode `0660`, so nginx must belong to the `sap13ns` group to connect:

```bash
sudo usermod -aG sap13ns www-data
sudo systemctl daemon-reload
sudo systemctl enable --now sap13ns
```

### 6. nginx

A server block with the Unix-socket upstream. nginx serves uploads directly with a sandboxing policy and passes everything else to sap13ns:

```nginx
upstream sap13ns {
    server unix:/run/sap13ns/sap13ns.sock;
}

server {
    listen 80;
    listen [::]:80;
    server_name example.com;

    client_max_body_size 16m;

    location /uploads/ {
        alias /var/lib/sap13ns/uploads/;
        add_header Content-Security-Policy "sandbox" always;
        add_header X-Content-Type-Options "nosniff" always;
        expires 30d;
    }

    location / {
        proxy_pass http://sap13ns;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

`client_max_body_size` matches the default upload limit of 16 MB (`SAP13NS_MAX_UPLOAD_MB`). Reload nginx, then add HTTPS, for example with `sudo certbot --nginx -d example.com`.

### 7. Create the first administrator

Either open the `/setup?token=…` link that the service prints in its log:

```bash
sudo journalctl -u sap13ns | grep setup
```

or create the account from the command line:

```bash
sudo -u sap13ns sap13ns user create -env /etc/sap13ns/sap13ns.env \
  -username ana -email ana@example.com -role admin
```

If you pipe a password into the command it uses that one; otherwise it generates a strong password and prints it once. Then sign in at `/login` and turn on two-factor authentication from your profile.

## Backups

`sap13ns backup create` writes one archive with a PostgreSQL dump (custom format, made with `pg_dump`, so the PostgreSQL client tools must be installed) and the uploads directory. Archives can be encrypted with a passphrase and are written with mode `0600`. Before anything is restored, every file in the archive is checked against its manifest.

Run it every night with a systemd timer. `/etc/systemd/system/sap13ns-backup.service`:

```ini
[Unit]
Description=sap13ns backup

[Service]
Type=oneshot
User=sap13ns
Group=sap13ns
EnvironmentFile=/etc/sap13ns/sap13ns.env
ExecStart=/usr/local/bin/sap13ns backup create
```

`/etc/systemd/system/sap13ns-backup.timer`:

```ini
[Unit]
Description=Nightly sap13ns backup

[Timer]
OnCalendar=daily
Persistent=true

[Install]
WantedBy=timers.target
```

```bash
sudo systemctl enable --now sap13ns-backup.timer
```

Copy the archives somewhere other than the server itself. A backup that lives on the same disk is not a backup.

## Updating

Replace the binary and restart. Migrations run on start.

```bash
sudo install -m 0755 sap13ns /usr/local/bin/
sudo systemctl restart sap13ns
```

## Multisite

Commands for creating and managing several sites from one installation are **coming soon**. Until then, run one service per site or use [hosting on sap13ns.site](/hosting).

The complete guide, including upgrades and troubleshooting, is in [DEPLOY.md](https://github.com/vicktor/sap13ns/blob/main/docs/DEPLOY.md).
