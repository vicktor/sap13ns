#!/bin/sh
# Installs sap13ns on a Debian or Ubuntu server with PostgreSQL and nginx
# already installed. Run as root from an unpacked release (or the repository
# after `go build -o sap13ns ./cmd/sap13ns`):
#
#   sudo ./deploy/install.sh example.com
#
# It is idempotent: an existing env file, database or role is left as is.
# Afterwards: get a TLS certificate, create the first administrator, start.
set -eu

DOMAIN=${1:-}
[ -n "$DOMAIN" ] || { echo "usage: $0 DOMAIN" >&2; exit 2; }
[ "$(id -u)" -eq 0 ] || { echo "run as root" >&2; exit 1; }
case "$DOMAIN" in *[!a-zA-Z0-9.-]*|"") echo "invalid domain: $DOMAIN" >&2; exit 2;; esac

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/.." && pwd)
bin="$root/sap13ns"
[ -x "$bin" ] || bin="$root/bin/sap13ns"
[ -x "$bin" ] || { echo "sap13ns binary not found next to deploy/" >&2; exit 1; }

echo "==> user and directories"
id sap13ns >/dev/null 2>&1 || useradd --system --home-dir /var/lib/sap13ns --shell /usr/sbin/nologin sap13ns
install -d -o sap13ns -g sap13ns -m 0750 /var/lib/sap13ns /var/lib/sap13ns/uploads /var/lib/sap13ns/plugins
install -d -o sap13ns -g sap13ns -m 0700 /var/lib/sap13ns/backups
install -d -o root -g sap13ns -m 0750 /etc/sap13ns
if id www-data >/dev/null 2>&1; then usermod -aG sap13ns www-data; fi

echo "==> binary"
install -o root -g root -m 0755 "$bin" /usr/local/bin/sap13ns
/usr/local/bin/sap13ns version

echo "==> database"
if ! su postgres -c "psql -tAc \"SELECT 1 FROM pg_roles WHERE rolname='sap13ns'\"" | grep -q 1; then
    su postgres -c "createuser sap13ns"
fi
if ! su postgres -c "psql -tAc \"SELECT 1 FROM pg_database WHERE datname='sap13ns'\"" | grep -q 1; then
    su postgres -c "createdb -O sap13ns sap13ns"
fi
# Optional, for the site assistant's semantic search (needs postgresql-NN-pgvector).
su postgres -c "psql -d sap13ns -c 'CREATE EXTENSION IF NOT EXISTS vector'" >/dev/null 2>&1 ||
    echo "    pgvector not installed: the site assistant will use full-text search only"

echo "==> configuration"
env=/etc/sap13ns/sap13ns.env
if [ ! -e "$env" ]; then
    secret=$(/usr/local/bin/sap13ns keygen)
    sed -e "s|^SAP13NS_SECRET_KEY=.*|SAP13NS_SECRET_KEY=$secret|" \
        -e "s|https://example.com|https://$DOMAIN|" \
        "$here/sap13ns.env.example" > "$env"
    chown root:sap13ns "$env"
    chmod 0640 "$env"
    echo "    wrote $env (keep a copy of SAP13NS_SECRET_KEY somewhere safe)"
else
    echo "    $env exists, left unchanged"
fi

echo "==> systemd"
install -m 0644 "$here/systemd/sap13ns.service" "$here/systemd/sap13ns-backup.service" \
    "$here/systemd/sap13ns-backup.timer" /etc/systemd/system/
systemctl daemon-reload
systemctl enable sap13ns sap13ns-backup.timer

echo "==> nginx"
site=/etc/nginx/sites-available/sap13ns.conf
if [ ! -e "$site" ]; then
    sed "s/example\.com/$DOMAIN/g" "$here/nginx/sap13ns.conf" > "$site"
    ln -sf ../sites-available/sap13ns.conf /etc/nginx/sites-enabled/sap13ns.conf
    install -d /var/www/letsencrypt
    echo "    wrote $site"
else
    echo "    $site exists, left unchanged"
fi

cat <<EOF

Done. Next steps:
  1. TLS:    certbot certonly --webroot -w /var/www/letsencrypt -d $DOMAIN
             (until then, comment out the 443 server block), then: nginx -t && systemctl reload nginx
  2. Admin:  sudo -u sap13ns sap13ns user create -env $env -username admin -email you@$DOMAIN -role admin
  3. Start:  systemctl start sap13ns && journalctl -u sap13ns -f
  4. Open:   https://$DOMAIN/admin (you will set up two-factor authentication)
EOF
