#!/usr/bin/env bash
# The guided installer of sap13ns for Debian and Ubuntu. Run it as root from
# an unpacked release, or from a clone of the repository:
#
#   sudo ./deploy/setup.sh              # dialogs (whiptail) when available
#   sudo ./deploy/setup.sh --plain      # plain questions in the terminal
#   sudo ./deploy/setup.sh --dry-run    # ask everything, change nothing
#
# It checks the server (PostgreSQL 16+, pgvector, nginx, certbot), offers to
# install what is missing, asks whether this is one site or a network of
# sites, and then installs and configures everything: the binary, the
# database, /etc/sap13ns/sap13ns.env, systemd, nginx, a TLS certificate,
# the backups, the country statistics and the first administrator.
#
# On a server that already hosts other sites it only adds its own files: no
# nginx block is a default_server, the default site stays, and the
# firewall, hostname and SSH are never touched. Run it again to upgrade the
# binary and the units; the configuration is kept.
#
# deploy/install.sh is the same installation without questions.
set -euo pipefail

VERSION_REPO=vicktor/sap13ns
ENV_FILE=/etc/sap13ns/sap13ns.env
PASS_FILE=/etc/sap13ns/backup-passphrase
NGINX_SITE=/etc/nginx/sites-available/sap13ns.conf
WEBROOT=/var/www/letsencrypt
LOG=/var/log/sap13ns-setup.log
SUMMARY=/root/sap13ns-setup.txt

DRY=0 PLAIN=0
for a in "$@"; do
    case "$a" in
    --dry-run) DRY=1 ;;
    --plain) PLAIN=1 ;;
    -h | --help) sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown option: $a" >&2; exit 2 ;;
    esac
done

# --------------------------------------------------------------- language --
ES=0
case "${LC_ALL:-${LC_MESSAGES:-${LANG:-}}}" in es*) ES=1 ;; esac
# t "English" "Español" prints the text in the installer's language.
t() { if [ "$ES" = 1 ]; then printf '%s' "$2"; else printf '%s' "$1"; fi; }

# ---------------------------------------------------------------- dialogs --
WT=0
if [ "$PLAIN" = 0 ] && command -v whiptail >/dev/null 2>&1 && [ -t 0 ] && [ -t 1 ]; then WT=1; fi
TITLE="sap13ns"

ui_msg() { # text
    if [ "$WT" = 1 ]; then whiptail --title "$TITLE" --msgbox "$1" 20 76; else printf '\n%s\n\n' "$1"; read -r -p "$(t "Press Enter to continue" "Pulsa Intro para seguir") " _ </dev/tty; fi
}
ui_text() { # text (long, scrollable)
    if [ "$WT" = 1 ]; then whiptail --title "$TITLE" --scrolltext --msgbox "$1" 22 78; else printf '\n%s\n\n' "$1"; read -r -p "$(t "Press Enter to continue" "Pulsa Intro para seguir") " _ </dev/tty; fi
}
ui_yesno() { # text [default: yes|no]
    if [ "$WT" = 1 ]; then
        if [ "${2:-yes}" = no ]; then whiptail --title "$TITLE" --defaultno --yesno "$1" 16 76; else whiptail --title "$TITLE" --yesno "$1" 16 76; fi
        return
    fi
    local def=Y/n ans
    [ "${2:-yes}" = no ] && def=y/N
    while :; do
        read -r -p "$1 [$def] " ans </dev/tty
        case "${ans:-${def:0:1}}" in [YySs]*) return 0 ;; [Nn]*) return 1 ;; esac
    done
}
ui_input() { # text default -> stdout
    if [ "$WT" = 1 ]; then whiptail --title "$TITLE" --inputbox "$1" 12 76 "${2:-}" 3>&1 1>&2 2>&3; return; fi
    local ans
    read -r -p "$1 [${2:-}] " ans </dev/tty
    printf '%s' "${ans:-${2:-}}"
}
ui_secret() { # text -> stdout
    if [ "$WT" = 1 ]; then whiptail --title "$TITLE" --passwordbox "$1" 12 76 3>&1 1>&2 2>&3; return; fi
    local ans
    read -r -s -p "$1 " ans </dev/tty
    echo >/dev/tty
    printf '%s' "$ans"
}
ui_menu() { # text default tag item [tag item...] -> stdout
    local text=$1 def=$2
    shift 2
    if [ "$WT" = 1 ]; then whiptail --title "$TITLE" --default-item "$def" --menu "$text" 18 76 6 "$@" 3>&1 1>&2 2>&3; return; fi
    printf '\n%s\n' "$text" >/dev/tty
    local -a tags=()
    local i=1
    while [ $# -gt 0 ]; do
        printf '  %d) %s\n' "$i" "$2" >/dev/tty
        tags+=("$1")
        shift 2
        i=$((i + 1))
    done
    local ans
    while :; do
        read -r -p "> " ans </dev/tty
        if [ -z "$ans" ]; then printf '%s' "$def"; return; fi
        if [[ "$ans" =~ ^[0-9]+$ ]] && [ "$ans" -ge 1 ] && [ "$ans" -le "${#tags[@]}" ]; then printf '%s' "${tags[$((ans - 1))]}"; return; fi
    done
}
cancelled() { echo; t "Setup cancelled; nothing else was changed." "Instalación cancelada; no se ha cambiado nada más."; echo; exit 1; }

# ---------------------------------------------------------------- helpers --
step() { printf '\n\033[1m==> %s\033[0m\n' "$1"; }
ok() { printf '    \033[32m✓\033[0m %s\n' "$1"; }
warn() { printf '    \033[33m!\033[0m %s\n' "$1"; }
die() { printf '\n\033[31m✗ %s\033[0m\n' "$1" >&2; [ -s "$LOG" ] && printf '%s %s\n' "$(t "Details in" "Detalles en")" "$LOG" >&2; exit 1; }

# run executes a command, logging it; in a dry run it only shows it.
run() {
    if [ "$DRY" = 1 ]; then printf '    + %s\n' "$*"; return 0; fi
    printf '+ %s\n' "$*" >>"$LOG"
    "$@" >>"$LOG" 2>&1
}
# write_file PATH MODE OWNER:GROUP < content
write_file() {
    if [ "$DRY" = 1 ]; then printf '    + write %s (%s %s)\n' "$1" "$2" "$3"; cat >/dev/null; return 0; fi
    local tmp
    tmp=$(mktemp "$(dirname "$1")/.sap13ns-setup.XXXXXX")
    cat >"$tmp"
    chmod "$2" "$tmp"
    chown "$3" "$tmp"
    mv -f "$tmp" "$1"
}
# set_env FILE KEY VALUE: sets KEY=VALUE, uncommenting the template's line
# or appending one. Values are written as they are (no quoting).
set_env() {
    case "$3" in *$'\n'*) die "invalid value for $2" ;; esac
    if [ "$DRY" = 1 ]; then printf '    + %s: %s=%s\n' "$1" "$2" "$( [[ "$2" == *PASSWORD* || "$2" == *SECRET* ]] && echo '…' || echo "$3")"; return 0; fi
    K="$2" V="$3" awk '
        BEGIN { k = ENVIRON["K"]; v = ENVIRON["V"]; done = 0 }
        !done && ($0 ~ "^#?[ ]?" k "=") { print k "=" v; done = 1; next }
        { print }
        END { if (!done) print k "=" v }
    ' "$1" >"$1.new" && cat "$1.new" >"$1" && rm -f "$1.new"
}
valid_domain() { [[ "$1" =~ ^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$ ]]; }
valid_email() { [[ "$1" =~ ^[^[:space:]@]+@[^[:space:]@]+\.[^[:space:]@]+$ ]]; }
valid_user() { [[ "$1" =~ ^[a-z0-9][a-z0-9._-]{1,31}$ ]]; }
psql_q() { runuser -u postgres -- psql -tAXc "$1" 2>/dev/null; }

# ------------------------------------------------------------- the binary --
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
root=$(cd "$here/.." && pwd)
BIN=""
for b in "$root/sap13ns" "$root/bin/sap13ns"; do [ -x "$b" ] && BIN=$b && break; done

arch() {
    case "$(uname -m)" in x86_64 | amd64) echo amd64 ;; aarch64 | arm64) echo arm64 ;; *) return 1 ;; esac
}

# fetch_release downloads the latest release for this machine, checks its
# SHA256SUMS and unpacks it into a temporary directory.
fetch_release() {
    local a api url sums dir
    a=$(arch) || die "$(t "Unsupported architecture" "Arquitectura no compatible"): $(uname -m)"
    api=$(curl -fsSL "https://api.github.com/repos/$VERSION_REPO/releases/latest") ||
        die "$(t "No release could be read from GitHub. Build the binary with: go build -o sap13ns ./cmd/sap13ns" "No se ha podido leer ninguna release de GitHub. Compila el binario con: go build -o sap13ns ./cmd/sap13ns")"
    url=$(printf '%s' "$api" | grep -o "https://[^\"]*_linux_${a}\.tar\.gz" | head -n1)
    sums=$(printf '%s' "$api" | grep -o 'https://[^"]*/SHA256SUMS"' | tr -d '"' | head -n1)
    [ -n "$url" ] && [ -n "$sums" ] || die "$(t "The latest release has no linux/$a package." "La última release no tiene paquete linux/$a.")"
    dir=$(mktemp -d)
    curl -fsSL -o "$dir/$(basename "$url")" "$url" || die "$(t "Download failed" "Falló la descarga")"
    curl -fsSL -o "$dir/SHA256SUMS" "$sums" || die "$(t "Download failed" "Falló la descarga")"
    (cd "$dir" && grep " $(basename "$url")\$" SHA256SUMS | sha256sum -c --quiet -) ||
        die "$(t "The package does not match SHA256SUMS" "El paquete no coincide con SHA256SUMS")"
    tar -C "$dir" -xzf "$dir/$(basename "$url")"
    BIN="$dir/sap13ns"
    [ -x "$BIN" ] || die "$(t "The package has no sap13ns binary" "El paquete no trae el binario sap13ns")"
    here="$dir/deploy"
}

# ask_tls asks how to get the certificate: TLS, CERT_DIR and CF_TOKEN.
ask_tls() {
    if [ "$MODE" = network ]; then
        TLS=$(ui_menu "$(t "HTTPS for $DOMAIN and *.$SITES_DOMAIN needs a wildcard certificate, which Let's Encrypt only issues through DNS." "HTTPS para $DOMAIN y *.$SITES_DOMAIN necesita un certificado comodín, que Let's Encrypt solo emite a través del DNS.")" cloudflare \
            cloudflare "$(t "Let's Encrypt through Cloudflare DNS (an API token)" "Let's Encrypt con el DNS de Cloudflare (un token de API)")" \
            files "$(t "I already have the certificate files" "Ya tengo los archivos del certificado")" \
            later "$(t "Later: plain HTTP for now" "Más tarde: de momento solo HTTP")") || cancelled
    else
        TLS=$(ui_menu "$(t "HTTPS for $DOMAIN:" "HTTPS para $DOMAIN:")" letsencrypt \
            letsencrypt "$(t "Let's Encrypt, checked over HTTP (port 80)" "Let's Encrypt, comprobado por HTTP (puerto 80)")" \
            files "$(t "I already have the certificate files" "Ya tengo los archivos del certificado")" \
            later "$(t "Later: plain HTTP for now" "Más tarde: de momento solo HTTP")") || cancelled
    fi
    CERT_DIR="/etc/letsencrypt/live/$DOMAIN"
    CF_TOKEN=""
    case "$TLS" in
    cloudflare)
        CF_TOKEN=$(ui_secret "$(t "Cloudflare API token with the Zone:DNS:Edit permission for $SITES_DOMAIN. It is kept in /etc/letsencrypt/sap13ns-cloudflare.ini (root, 0600) for the renewals." "Token de API de Cloudflare con el permiso Zone:DNS:Edit para $SITES_DOMAIN. Se guarda en /etc/letsencrypt/sap13ns-cloudflare.ini (root, 0600) para las renovaciones.")") || cancelled
        [ -n "$CF_TOKEN" ] || cancelled
        ;;
    files)
        while :; do
            CERT_DIR=$(ui_input "$(t "Directory with fullchain.pem and privkey.pem" "Directorio con fullchain.pem y privkey.pem")" "$CERT_DIR") || cancelled
            [ -r "$CERT_DIR/fullchain.pem" ] && [ -r "$CERT_DIR/privkey.pem" ] && break
            ui_msg "$(t "fullchain.pem and privkey.pem are not both there." "No están fullchain.pem y privkey.pem.")"
        done
        ;;
    esac
}

# setup_web writes the nginx site: first a port-80 site that answers the
# ACME challenges (and serves sap13ns over HTTP while TLS waits), then the
# full site once there is a certificate. It sets HTTPS and the BASE_URL.
setup_web() {
    step "nginx"
    names="$DOMAIN"
    [ "$MODE" = network ] && names="$DOMAIN *.$SITES_DOMAIN"
    http_site() {
        cat <<EOF
# Written by deploy/setup.sh: plain HTTP until there is a certificate.
upstream sap13ns {
    server unix:/run/sap13ns/sap13ns.sock;
    keepalive 32;
}
server {
    listen 80;
    listen [::]:80;
    server_name $names;
    client_max_body_size 20m;
    location /.well-known/acme-challenge/ { root $WEBROOT; }
    location / {
        proxy_pass http://sap13ns;
        proxy_http_version 1.1;
        proxy_set_header Connection "";
        proxy_set_header Host \$host;
        proxy_set_header X-Forwarded-For \$remote_addr;
        proxy_set_header X-Forwarded-Proto \$scheme;
        proxy_set_header X-Real-IP \$remote_addr;
    }
}
EOF
    }
    full_site() {
        if [ "$MODE" = network ]; then
            sed -e "s/\*\.example\.com/*.$SITES_DOMAIN/g" -e "s/example\.com/$DOMAIN/g" -e "s|/etc/letsencrypt/live/$DOMAIN|$CERT_DIR|g" "$here/nginx/sap13ns-network.conf"
        else
            sed -e "s/example\.com/$DOMAIN/g" -e "s|/etc/letsencrypt/live/$DOMAIN|$CERT_DIR|g" "$here/nginx/sap13ns.conf"
        fi
    }
    reload_nginx() { run nginx -t && run systemctl reload nginx; }
    [ -e "$NGINX_SITE" ] && run cp -p "$NGINX_SITE" "$NGINX_SITE.bak.$(date +%Y%m%d%H%M%S)"
    run install -d "$WEBROOT"
    http_site | write_file "$NGINX_SITE" 0644 root:root
    run ln -sf ../sites-available/sap13ns.conf /etc/nginx/sites-enabled/sap13ns.conf
    run systemctl enable --now nginx
    reload_nginx || die "nginx -t"
    ok "$NGINX_SITE (HTTP)"

    step "HTTPS"
    HTTPS=0
    case "$TLS" in
    letsencrypt)
        if run certbot certonly --non-interactive --agree-tos -m "$ADMIN_EMAIL" --webroot -w "$WEBROOT" -d "$DOMAIN" \
            --deploy-hook "systemctl reload nginx"; then HTTPS=1; else warn "$(t "certbot failed (is the DNS right?); the site stays on HTTP. Run this script again later." "certbot ha fallado (¿está bien el DNS?); el sitio sigue en HTTP. Vuelve a ejecutar este script más tarde.")"; fi
        ;;
    cloudflare)
        extra=()
        if [ "$SITES_DOMAIN" != "$DOMAIN" ]; then extra=(-d "$SITES_DOMAIN"); fi
        printf 'dns_cloudflare_api_token = %s\n' "$CF_TOKEN" | write_file /etc/letsencrypt/sap13ns-cloudflare.ini 0600 root:root
        if run certbot certonly --non-interactive --agree-tos -m "$ADMIN_EMAIL" --dns-cloudflare \
            --dns-cloudflare-credentials /etc/letsencrypt/sap13ns-cloudflare.ini --dns-cloudflare-propagation-seconds 30 \
            -d "$DOMAIN" -d "*.$SITES_DOMAIN" "${extra[@]}" \
            --cert-name "$DOMAIN" --deploy-hook "systemctl reload nginx"; then HTTPS=1; else warn "$(t "certbot failed (check the token); the network stays on HTTP." "certbot ha fallado (revisa el token); la red sigue en HTTP.")"; fi
        ;;
    files) HTTPS=1 ;;
    later) warn "$(t "HTTPS left for later: run this script again, or see docs/DEPLOY.md" "HTTPS para más tarde: vuelve a ejecutar este script, o mira docs/DEPLOY.md")" ;;
    esac
    if [ "$HTTPS" = 1 ]; then
        full_site | write_file "$NGINX_SITE" 0644 root:root
        if reload_nginx; then
            ok "https://$DOMAIN"
            set_env "$ENV_FILE" SAP13NS_BASE_URL "https://$DOMAIN"
        else
            warn "$(t "nginx rejected the HTTPS site; back to HTTP" "nginx no acepta el sitio HTTPS; se vuelve a HTTP")"
            http_site | write_file "$NGINX_SITE" 0644 root:root
            reload_nginx || true
            set_env "$ENV_FILE" SAP13NS_BASE_URL "http://$DOMAIN"
            HTTPS=0
        fi
    else
        set_env "$ENV_FILE" SAP13NS_BASE_URL "http://$DOMAIN"
    fi
}

# ================================================================== start ==
[ "$(id -u)" -eq 0 ] || { t "Run it as root: sudo $0" "Ejecútalo como root: sudo $0"; echo; exit 1; }
[ -r /etc/os-release ] || die "$(t "This installer needs Debian or Ubuntu." "Este instalador necesita Debian o Ubuntu.")"
# shellcheck disable=SC1091
. /etc/os-release
case " ${ID:-} ${ID_LIKE:-} " in *" debian "* | *" ubuntu "*) ;; *) die "$(t "This installer needs Debian or Ubuntu (found ${PRETTY_NAME:-unknown})." "Este instalador necesita Debian o Ubuntu (hay ${PRETTY_NAME:-desconocido}).")" ;; esac
command -v systemctl >/dev/null || die "$(t "systemd is required." "Hace falta systemd.")"
[ "$DRY" = 1 ] || : >>"$LOG"

UPGRADE=0
if [ -e "$ENV_FILE" ]; then
    ACTION=$(ui_menu "$(t "sap13ns is already installed ($ENV_FILE exists). The configuration and the database are kept in every case." "sap13ns ya está instalado (existe $ENV_FILE). La configuración y la base de datos se conservan en todos los casos.")" upgrade \
        upgrade "$(t "Upgrade the binary, the systemd units and the backup script" "Actualizar el binario, las unidades de systemd y el script de copias")" \
        https "$(t "Set up HTTPS (a certificate and the nginx site)" "Configurar HTTPS (un certificado y el sitio de nginx)")") || cancelled
    if [ "$ACTION" = https ]; then
        env_get() { sed -n "s/^$1=//p" "$ENV_FILE" | tail -n1; }
        DOMAIN=$(env_get SAP13NS_BASE_URL | sed 's|^https\{0,1\}://||; s|[:/].*$||')
        valid_domain "$DOMAIN" || die "$(t "SAP13NS_BASE_URL in $ENV_FILE has no domain" "SAP13NS_BASE_URL en $ENV_FILE no tiene dominio")"
        MODE=single SITES_DOMAIN=""
        if [ "$(env_get SAP13NS_MULTISITE)" = true ]; then MODE=network SITES_DOMAIN=$(env_get SAP13NS_SITES_DOMAIN); SITES_DOMAIN=${SITES_DOMAIN:-$DOMAIN}; fi
        while :; do
            ADMIN_EMAIL=$(ui_input "$(t "E-mail address for Let's Encrypt (expiry notices)" "Correo para Let's Encrypt (avisos de caducidad)")" "") || cancelled
            valid_email "$ADMIN_EMAIL" && break
        done
        ask_tls
        APT=()
        case "$TLS" in letsencrypt | cloudflare) command -v certbot >/dev/null || APT+=(certbot) ;; esac
        if [ "$TLS" = cloudflare ] && ! dpkg -s python3-certbot-dns-cloudflare >/dev/null 2>&1; then APT+=(python3-certbot-dns-cloudflare); fi
        if [ "${#APT[@]}" -gt 0 ]; then
            step "$(t "Packages" "Paquetes")"
            run apt-get update -qq
            run apt-get install -y -qq "${APT[@]}" || die "apt-get install ${APT[*]}"
        fi
        setup_web
        run systemctl restart sap13ns
        echo
        if [ "$HTTPS" = 1 ]; then t "Done: https://$DOMAIN" "Listo: https://$DOMAIN"; else t "HTTPS is still pending; see $LOG." "HTTPS sigue pendiente; mira $LOG."; fi
        echo
        exit 0
    fi
    UPGRADE=1
else
    ui_msg "$(t "Welcome to sap13ns.

This assistant checks the server, installs what is missing and leaves sap13ns running behind nginx, with HTTPS, backups and the first administrator.

It only adds its own files. Other sites on this nginx, the firewall, the hostname and SSH are left as they are.

Nothing changes until you confirm the summary." "Bienvenido a sap13ns.

Este asistente revisa el servidor, instala lo que falte y deja sap13ns funcionando detrás de nginx, con HTTPS, copias de seguridad y el primer administrador.

Solo añade sus propios archivos. Los otros sitios de este nginx, el cortafuegos, el nombre del servidor y SSH no se tocan.

No se cambia nada hasta que confirmes el resumen.")"
fi

if [ -z "$BIN" ]; then
    ui_yesno "$(t "There is no sap13ns binary next to this script.

Download the latest release from github.com/$VERSION_REPO?" "No hay ningún binario sap13ns junto a este script.

¿Descargo la última release de github.com/$VERSION_REPO?")" || cancelled
    command -v curl >/dev/null || { apt-get update -qq && apt-get install -y -qq curl ca-certificates; }
    fetch_release
fi

# ---------------------------------------------------------------- upgrade --
if [ "$UPGRADE" = 1 ]; then
    step "$(t "Upgrade" "Actualización")"
    run install -o root -g root -m 0755 "$BIN" /usr/local/bin/sap13ns
    for u in sap13ns.service sap13ns-backup.service sap13ns-backup.timer sap13ns-geoip.service sap13ns-geoip.timer \
        sap13ns-network-backup.service sap13ns-network-backup.timer; do
        if [ -e "/etc/systemd/system/$u" ]; then run install -m 0644 "$here/systemd/$u" /etc/systemd/system/; fi
    done
    if [ -e /usr/local/sbin/sap13ns-network-backup ]; then run install -m 0750 "$here/sap13ns-network-backup" /usr/local/sbin/; fi
    run systemctl daemon-reload
    run systemctl restart sap13ns
    ok "$(/usr/local/bin/sap13ns version)"
    echo
    t "Done. Logs: journalctl -u sap13ns -f" "Listo. Registro: journalctl -u sap13ns -f"
    echo
    exit 0
fi

# -------------------------------------------------------------- questions --
MODE=$(ui_menu "$(t "What are you installing?" "¿Qué vas a instalar?")" single \
    single "$(t "One site (a blog, a company site)" "Un sitio (un blog, una web de empresa)")" \
    network "$(t "A network of sites (a portal and <name>.your-domain)" "Una red de sitios (un portal y <nombre>.tu-dominio)")") || cancelled

while :; do
    DOMAIN=$(ui_input "$(t "The domain of the site (without https://), such as example.com. Its DNS must point to this server." "El dominio del sitio (sin https://), como ejemplo.com. Su DNS debe apuntar a este servidor.")" "${DOMAIN:-}") || cancelled
    DOMAIN=$(printf '%s' "$DOMAIN" | tr '[:upper:]' '[:lower:]' | sed 's|^https\{0,1\}://||; s|/.*$||')
    valid_domain "$DOMAIN" && break
    ui_msg "$(t "That is not a valid domain name." "Eso no es un nombre de dominio válido.")"
done

SITES_DOMAIN=""
if [ "$MODE" = network ]; then
    while :; do
        SITES_DOMAIN=$(ui_input "$(t "The sites of the network get <name>.DOMAIN. Which domain? It needs a wildcard DNS record (*.DOMAIN) pointing here." "Los sitios de la red tendrán <nombre>.DOMINIO. ¿Qué dominio? Necesita un registro DNS comodín (*.DOMINIO) que apunte aquí.")" "${SITES_DOMAIN:-$DOMAIN}") || cancelled
        valid_domain "$SITES_DOMAIN" && break
        ui_msg "$(t "That is not a valid domain name." "Eso no es un nombre de dominio válido.")"
    done
fi

while :; do
    ADMIN_USER=$(ui_input "$(t "Username of the first administrator" "Nombre de usuario del primer administrador")" "${ADMIN_USER:-admin}") || cancelled
    valid_user "$ADMIN_USER" && break
    ui_msg "$(t "2 to 32 characters: lower-case letters, digits, dot, dash or underscore." "De 2 a 32 caracteres: minúsculas, números, punto, guion o guion bajo.")"
done
while :; do
    ADMIN_EMAIL=$(ui_input "$(t "E-mail address of the administrator (also used for the certificate)" "Correo del administrador (también para el certificado)")" "${ADMIN_EMAIL:-}") || cancelled
    valid_email "$ADMIN_EMAIL" && break
    ui_msg "$(t "That is not a valid e-mail address." "Eso no es una dirección de correo válida.")"
done

ask_tls

SMTP_HOST="" SMTP_PORT=587 SMTP_USER="" SMTP_PASS="" SMTP_FROM=""
if ui_yesno "$(t "Set up outgoing e-mail now? sap13ns sends password resets and notices. Without it, nobody can reset a password." "¿Configuro ahora el correo saliente? sap13ns envía los restablecimientos de contraseña y los avisos. Sin él nadie puede recuperar su contraseña.")"; then
    SMTP_HOST=$(ui_input "$(t "SMTP server" "Servidor SMTP")" "") || cancelled
    SMTP_PORT=$(ui_input "$(t "Port (587 STARTTLS, 465 TLS)" "Puerto (587 STARTTLS, 465 TLS)")" 587) || cancelled
    SMTP_USER=$(ui_input "$(t "User" "Usuario")" "no-reply@$DOMAIN") || cancelled
    SMTP_PASS=$(ui_secret "$(t "Password" "Contraseña")") || cancelled
    SMTP_FROM=$(ui_input "$(t "Sender" "Remitente")" "$DOMAIN <no-reply@$DOMAIN>") || cancelled
    [[ "$SMTP_PORT" =~ ^[0-9]+$ ]] || SMTP_PORT=587
fi

GEOIP=0
ui_yesno "$(t "Show the visitors' countries in Analytics? It downloads DB-IP's free country database every month; only the country is kept, never the address." "¿Mostrar los países de los visitantes en Estadísticas? Descarga cada mes la base de datos gratuita de países de DB-IP; solo se guarda el país, nunca la dirección.")" && GEOIP=1

# ----------------------------------------------------------------- checks --
step "$(t "Checking the server" "Revisando el servidor")"
APT=()
PG_VER=""
if command -v pg_lsclusters >/dev/null; then
    PG_VER=$(pg_lsclusters -h 2>/dev/null | awk '$4 == "online" { print $1; exit }')
    [ -n "$PG_VER" ] || PG_VER=$(pg_lsclusters -h 2>/dev/null | awk '{ print $1; exit }')
fi
PG_NEEDS_PGDG=0
if [ -n "$PG_VER" ]; then
    if [ "${PG_VER%%.*}" -ge 16 ]; then ok "PostgreSQL $PG_VER"; else warn "PostgreSQL $PG_VER: $(t "16 or newer is required" "hace falta la 16 o posterior")"; PG_NEEDS_PGDG=1; fi
else
    CAND=$(apt-cache policy postgresql 2>/dev/null | awk '/Candidate:/ { print $2 }' | grep -o '^[0-9]*' || true)
    if [ -n "$CAND" ] && [ "$CAND" -ge 16 ]; then
        warn "PostgreSQL: $(t "not installed (the system offers $CAND)" "no instalado (el sistema ofrece la $CAND)")"
        APT+=(postgresql)
        PG_VER=$CAND
    else
        warn "PostgreSQL: $(t "not installed, and this release of the system only offers ${CAND:-none}" "no instalado, y esta versión del sistema solo ofrece la ${CAND:-ninguna}")"
        PG_NEEDS_PGDG=1
    fi
fi
if command -v nginx >/dev/null; then ok "nginx $(nginx -v 2>&1 | grep -o '[0-9][0-9.]*' | head -n1)"; else warn "nginx: $(t "not installed" "no instalado")"; APT+=(nginx); fi
need_certbot=0
case "$TLS" in letsencrypt | cloudflare) need_certbot=1 ;; esac
if [ "$need_certbot" = 1 ]; then
    if command -v certbot >/dev/null; then ok "certbot"; else warn "certbot: $(t "not installed" "no instalado")"; APT+=(certbot); fi
    if [ "$TLS" = cloudflare ] && ! dpkg -s python3-certbot-dns-cloudflare >/dev/null 2>&1; then
        warn "python3-certbot-dns-cloudflare: $(t "not installed" "no instalado")"
        APT+=(python3-certbot-dns-cloudflare)
    fi
fi
for c in curl openssl gpg; do
    if command -v "$c" >/dev/null; then ok "$c"; else warn "$c: $(t "not installed" "no instalado")"; APT+=("$c"); fi
done

# DNS: a warning only, the server may be behind NAT.
addrs=$(ip -o addr show scope global 2>/dev/null | awk '{ split($4, a, "/"); print a[1] }' || true)
check_dns() {
    local name=$1 got
    got=$(getent ahosts "$name" 2>/dev/null | awk '{ print $1 }' | sort -u || true)
    if [ -z "$got" ]; then warn "$name: $(t "does not resolve yet" "aún no resuelve")"; return 1; fi
    for ip in $got; do
        if printf '%s\n' "$addrs" | grep -qx "$ip"; then ok "$name → $ip"; return 0; fi
    done
    warn "$name → $(echo "$got" | tr '\n' ' ')$(t "(not an address of this server; fine behind NAT)" "(no es una dirección de este servidor; normal detrás de NAT)")"
    return 0
}
DNS_OK=1
check_dns "$DOMAIN" || DNS_OK=0
[ "$MODE" = network ] && { check_dns "check-$RANDOM.$SITES_DOMAIN" || DNS_OK=0; }

if command -v nginx >/dev/null && [ -d /etc/nginx/sites-enabled ]; then
    others=$(find /etc/nginx/sites-enabled -mindepth 1 ! -name sap13ns.conf 2>/dev/null | wc -l)
    [ "$others" -gt 0 ] && ok "$(t "nginx already serves $others other site(s): they are left as they are" "nginx ya sirve $others sitio(s) más: se quedan como están")"
fi
if [ -e "$NGINX_SITE" ]; then warn "$NGINX_SITE $(t "exists: a copy is kept as .bak before it is replaced" "existe: se guarda una copia .bak antes de sustituirlo")"; fi

# ---------------------------------------------------------------- summary --
pkgs="${APT[*]:-}"
[ "$PG_NEEDS_PGDG" = 1 ] && pkgs="$pkgs $(t "PostgreSQL 17 from apt.postgresql.org" "PostgreSQL 17 de apt.postgresql.org")"
summary="$(t "Summary" "Resumen")

  $(t "Type" "Tipo"):          $(if [ "$MODE" = network ]; then t "network of sites" "red de sitios"; else t "one site" "un sitio"; fi)
  $(t "Address" "Dirección"):     https://$DOMAIN$([ "$MODE" = network ] && printf '\n  %s:         <%s>.%s' "$(t "Sites" "Sitios")" "$(t "name" "nombre")" "$SITES_DOMAIN")
  $(t "Administrator" "Administrador"): $ADMIN_USER <$ADMIN_EMAIL>
  HTTPS:         $TLS
  $(t "E-mail" "Correo"):        ${SMTP_HOST:-$(t "not now" "ahora no")}
  $(t "Countries" "Países"):        $(if [ "$GEOIP" = 1 ]; then t yes sí; else t no no; fi)
  $(t "Backups" "Copias"):       $(t "every night, encrypted" "cada noche, cifradas")
  $(t "Packages" "Paquetes"):      ${pkgs:-$(t "nothing to install" "nada que instalar")}
$([ "$DNS_OK" = 0 ] && printf '\n%s' "$(t "Warning: the DNS does not resolve yet; the certificate will wait." "Aviso: el DNS aún no resuelve; el certificado tendrá que esperar.")")

$(t "Install now?" "¿Instalo ahora?")"
ui_yesno "$summary" || cancelled
[ "$DRY" = 1 ] && step "$(t "Dry run: these are the steps, nothing is changed" "Simulación: estos son los pasos, no se cambia nada")"

# ================================================================ install ==
export DEBIAN_FRONTEND=noninteractive
if [ "$PG_NEEDS_PGDG" = 1 ]; then
    step "$(t "PostgreSQL from apt.postgresql.org" "PostgreSQL de apt.postgresql.org")"
    run apt-get update -qq
    run apt-get install -y -qq postgresql-common ca-certificates curl
    run /usr/share/postgresql-common/pgdg/apt.postgresql.org.sh -y
    APT+=(postgresql-17)
    PG_VER=17
fi
if [ "${#APT[@]}" -gt 0 ]; then
    step "$(t "Packages" "Paquetes")"
    run apt-get update -qq
    run apt-get install -y -qq "${APT[@]}" || die "apt-get install ${APT[*]}"
    ok "${APT[*]}"
fi
if [ "$DRY" = 0 ]; then
    run systemctl enable --now postgresql || true
    PG_VER=$(pg_lsclusters -h 2>/dev/null | awk '$4 == "online" { print $1; exit }')
    if [ -z "$PG_VER" ]; then
        # A cluster that is down (just installed, or stopped): start it.
        c=$(pg_lsclusters -h 2>/dev/null | awk '{ print $1 "/" $2; exit }')
        if [ -n "$c" ]; then run systemctl start "postgresql@${c%/*}-${c#*/}" || true; fi
        PG_VER=$(pg_lsclusters -h 2>/dev/null | awk '$4 == "online" { print $1; exit }')
    fi
    [ -n "$PG_VER" ] || die "$(t "No PostgreSQL cluster is running (pg_lsclusters)." "No hay ningún clúster de PostgreSQL en marcha (pg_lsclusters).")"
fi
if [ ! -e "/usr/share/postgresql/$PG_VER/extension/vector.control" ]; then
    if run apt-get install -y -qq "postgresql-$PG_VER-pgvector"; then ok "pgvector"; else warn "$(t "pgvector is not available: the site assistant will use full-text search only" "pgvector no está disponible: el asistente del sitio usará solo búsqueda de texto")"; fi
else
    ok "pgvector"
fi

step "$(t "User and directories" "Usuario y directorios")"
id sap13ns >/dev/null 2>&1 || run useradd --system --home-dir /var/lib/sap13ns --shell /usr/sbin/nologin sap13ns
getent group sap13ns-web >/dev/null || run groupadd --system sap13ns-web
run install -d -o sap13ns -g sap13ns -m 0750 /var/lib/sap13ns /var/lib/sap13ns/uploads /var/lib/sap13ns/plugins /var/lib/sap13ns/geoip
run install -d -o sap13ns -g sap13ns -m 0700 /var/lib/sap13ns/backups
run install -d -o root -g sap13ns -m 0750 /etc/sap13ns
# nginx joins only the group of the socket: it never reads the env file.
id www-data >/dev/null 2>&1 && run usermod -aG sap13ns-web www-data
ok "sap13ns, sap13ns-web, /var/lib/sap13ns"

step "$(t "Binary" "Binario")"
run install -o root -g root -m 0755 "$BIN" /usr/local/bin/sap13ns
ok "$("$BIN" version)"

step "$(t "Database" "Base de datos")"
if [ "$DRY" = 1 ] || [ "$(psql_q "SELECT 1 FROM pg_roles WHERE rolname = 'sap13ns'")" != 1 ]; then run runuser -u postgres -- createuser sap13ns; fi
if [ "$DRY" = 1 ] || [ "$(psql_q "SELECT 1 FROM pg_database WHERE datname = 'sap13ns'")" != 1 ]; then run runuser -u postgres -- createdb -O sap13ns sap13ns; fi
run runuser -u postgres -- psql -X -d sap13ns -c 'CREATE EXTENSION IF NOT EXISTS vector' || true
ok "sap13ns @ PostgreSQL $PG_VER"

step "$(t "Configuration" "Configuración")"
if [ "$DRY" = 1 ]; then SECRET="(sap13ns keygen)"; else SECRET=$(/usr/local/bin/sap13ns keygen); fi
write_file "$ENV_FILE" 0640 root:sap13ns <"$here/sap13ns.env.example"
set_env "$ENV_FILE" SAP13NS_SECRET_KEY "$SECRET"
set_env "$ENV_FILE" SAP13NS_BASE_URL "$([ "$TLS" = later ] && echo http || echo https)://$DOMAIN"
set_env "$ENV_FILE" SAP13NS_BACKUP_PASSPHRASE_FILE "$PASS_FILE"
if [ -n "$SMTP_HOST" ]; then
    set_env "$ENV_FILE" SAP13NS_SMTP_HOST "$SMTP_HOST"
    set_env "$ENV_FILE" SAP13NS_SMTP_PORT "$SMTP_PORT"
    set_env "$ENV_FILE" SAP13NS_SMTP_USERNAME "$SMTP_USER"
    set_env "$ENV_FILE" SAP13NS_SMTP_PASSWORD "$SMTP_PASS"
    set_env "$ENV_FILE" SAP13NS_SMTP_FROM "$SMTP_FROM"
fi
[ "$GEOIP" = 1 ] && set_env "$ENV_FILE" SAP13NS_GEOIP_DB /var/lib/sap13ns/geoip/country.mmdb
if [ "$MODE" = network ]; then
    set_env "$ENV_FILE" SAP13NS_MULTISITE true
    set_env "$ENV_FILE" SAP13NS_SITES_DOMAIN "$SITES_DOMAIN"
fi
if [ ! -e "$PASS_FILE" ]; then
    openssl rand -base64 32 | write_file "$PASS_FILE" 0640 root:sap13ns
fi
ok "$ENV_FILE, $PASS_FILE"

step "systemd"
run install -m 0644 "$here/systemd/sap13ns.service" "$here/systemd/sap13ns-geoip.service" "$here/systemd/sap13ns-geoip.timer" /etc/systemd/system/
if [ "$MODE" = network ]; then
    # Each site is backed up by sap13ns at 03:30; this is the whole network.
    run install -m 0750 "$here/sap13ns-network-backup" /usr/local/sbin/
    run install -m 0644 "$here/systemd/sap13ns-network-backup.service" "$here/systemd/sap13ns-network-backup.timer" /etc/systemd/system/
    BACKUP_TIMER=sap13ns-network-backup.timer
else
    run install -m 0644 "$here/systemd/sap13ns-backup.service" "$here/systemd/sap13ns-backup.timer" /etc/systemd/system/
    BACKUP_TIMER=sap13ns-backup.timer
fi
run systemctl daemon-reload
run systemctl enable sap13ns "$BACKUP_TIMER"
[ "$GEOIP" = 1 ] && run systemctl enable sap13ns-geoip.timer
ok "sap13ns.service, $BACKUP_TIMER$([ "$GEOIP" = 1 ] && echo ", sap13ns-geoip.timer")"


setup_web

step "$(t "First administrator" "Primer administrador")"
ADMIN_PASS=""
if [ "$DRY" = 1 ]; then
    run sudo -u sap13ns sap13ns "$([ "$MODE" = network ] && echo "network init" || echo "user create")" -env "$ENV_FILE" -username "$ADMIN_USER" -email "$ADMIN_EMAIL"
else
    if [ "$MODE" = network ]; then
        out=$(runuser -u sap13ns -- /usr/local/bin/sap13ns network init -env "$ENV_FILE" -username "$ADMIN_USER" -email "$ADMIN_EMAIL" </dev/null 2>&1) || { echo "$out" >>"$LOG"; die "network init"; }
    else
        runuser -u sap13ns -- /usr/local/bin/sap13ns migrate -env "$ENV_FILE" >>"$LOG" 2>&1 || die "migrate"
        out=$(runuser -u sap13ns -- /usr/local/bin/sap13ns user create -env "$ENV_FILE" -username "$ADMIN_USER" -email "$ADMIN_EMAIL" -role admin </dev/null 2>&1) || { echo "$out" >>"$LOG"; die "user create"; }
    fi
    printf '%s\n' "$out" | grep -v '^password: ' >>"$LOG"
    ADMIN_PASS=$(printf '%s\n' "$out" | sed -n 's/^password: //p' | head -n1)
    ok "$ADMIN_USER"
fi

step "$(t "Start" "Arranque")"
run systemctl restart sap13ns
run systemctl start "$BACKUP_TIMER"
if [ "$GEOIP" = 1 ]; then run systemctl start --no-block sap13ns-geoip.service; run systemctl start sap13ns-geoip.timer; fi
if [ "$DRY" = 0 ]; then
    for _ in $(seq 1 30); do [ -S /run/sap13ns/sap13ns.sock ] && break; sleep 1; done
    if curl -fsS --unix-socket /run/sap13ns/sap13ns.sock -H "Host: $DOMAIN" http://localhost/healthz >/dev/null 2>&1; then
        ok "$(t "sap13ns is answering" "sap13ns responde")"
    else
        warn "$(t "sap13ns does not answer yet: journalctl -u sap13ns -n 50" "sap13ns aún no responde: journalctl -u sap13ns -n 50")"
    fi
fi

# ------------------------------------------------------------------- done --
scheme=$([ "$HTTPS" = 1 ] && echo https || echo http)
next="$(t "Next steps" "Siguientes pasos")
  1. $(t "Open" "Abre") $scheme://$DOMAIN/admin $(t "and sign in; you will set up the second factor." "e inicia sesión; configurarás el segundo factor.")
  2. $(t "Keep a copy of SAP13NS_SECRET_KEY ($ENV_FILE) and of $PASS_FILE off this server: without them no backup can be opened." "Guarda una copia de SAP13NS_SECRET_KEY ($ENV_FILE) y de $PASS_FILE fuera de este servidor: sin ellas no se puede abrir ninguna copia.")
  3. $(t "Copy the backups to another machine every night (docs/DEPLOY.md)." "Copia las copias de seguridad a otra máquina cada noche (docs/DEPLOY.md).")"
[ "$MODE" = network ] && next="$next
  4. $(t "Portal: Settings → Accounts & security → «Anyone can register» lets people sign up and ask for sites; billing is in docs/BILLING.md." "Portal: Ajustes → Cuentas y seguridad → «Cualquiera puede registrarse» deja que la gente se registre y pida sitios; la facturación está en docs/BILLING.md.")"
[ -z "$SMTP_HOST" ] && next="$next
  · $(t "E-mail is not set up: add SAP13NS_SMTP_* to $ENV_FILE and restart sap13ns." "El correo no está configurado: añade SAP13NS_SMTP_* a $ENV_FILE y reinicia sap13ns.")"
[ "$HTTPS" = 0 ] && next="$next
  · $(t "HTTPS is pending: run sudo $0 again once the DNS points here." "HTTPS está pendiente: vuelve a ejecutar sudo $0 cuando el DNS apunte aquí.")"

if [ "$DRY" = 0 ]; then
    { printf 'sap13ns setup, %s\n\n%s\n' "$(date -R)" "$next"; } | write_file "$SUMMARY" 0600 root:root
fi
final="$(t "sap13ns is installed." "sap13ns está instalado.")

  $scheme://$DOMAIN/admin
  $(t "User" "Usuario"):     $ADMIN_USER
  $(t "Password" "Contraseña"): ${ADMIN_PASS:-$(t "(the one you gave)" "(la que indicaste)")}

$(t "Write the password down now: it is shown only once." "Apunta la contraseña ahora: solo se muestra esta vez.")

$next

$(t "These steps are also in $SUMMARY (without the password)." "Estos pasos también están en $SUMMARY (sin la contraseña).")"
ui_text "$final"
[ "$WT" = 1 ] && printf '%s\n' "$next"
exit 0
