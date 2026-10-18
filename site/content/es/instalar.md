---
type: page
key: install
title: Instalar sap13ns
slug: instalar
excerpt: Prueba sap13ns en tu equipo en unos minutos y después ponlo en producción con systemd, nginx y un socket Unix.
order: 5
menu: [footer, cta, landing]
menu_label: Instalar
menu_label_cta: Empezar
comments: closed
---
sap13ns necesita tres cosas: el binario `sap13ns`, una base de datos PostgreSQL y, en producción, nginx delante. No hay nada que compilar ni ningún entorno de ejecución que instalar.

## Inicio rápido

Sirve para probar sap13ns en tu portátil o en un servidor de pruebas. Necesitas PostgreSQL funcionando en local.

**1. Descarga el binario.** Baja la versión para tu plataforma desde [GitHub Releases](https://github.com/vicktor/sap13ns/releases), llámalo `sap13ns` y dale permisos de ejecución:

```bash
chmod +x sap13ns
```

**2. Crea una base de datos.**

```bash
createdb sap13ns
```

**3. Escribe un pequeño archivo de configuración** llamado `sap13ns.env`. La clave secreta firma las sesiones y los tokens; genera una nueva con `./sap13ns keygen`:

```ini
SAP13NS_DATABASE_URL=postgres:///sap13ns?host=/tmp
SAP13NS_SECRET_KEY=pega-aqui-la-salida-de-sap13ns-keygen
SAP13NS_BASE_URL=http://localhost:8080
SAP13NS_LISTEN=127.0.0.1:8080
```

Ajusta la URL de la base de datos a tu instalación; también vale `postgres://usuario:contraseña@localhost/sap13ns`.

**4. Arranca el servidor.**

```bash
./sap13ns serve -env ./sap13ns.env
```

En el primer arranque, sap13ns crea sus tablas y, como todavía no hay usuarios, escribe en el log un enlace de instalación de un solo uso:

```text
no users yet: finish the installation at http://localhost:8080/setup?token=…
```

Ábrelo, crea el primer administrador y ya estás dentro. El enlace solo funciona una vez, y solo lo ve quien puede leer el log del servidor.

## Producción

Estos pasos suponen un servidor Debian o Ubuntu con PostgreSQL y nginx instalados. Adapta rutas y nombres de usuario a tu distribución.

### 1. Instala el binario

```bash
sudo install -m 0755 sap13ns /usr/local/bin/
sap13ns version
```

### 2. Crea un usuario del sistema y la base de datos

```bash
sudo useradd --system --home /var/lib/sap13ns --create-home --shell /usr/sbin/nologin sap13ns
sudo -u postgres createuser sap13ns
sudo -u postgres createdb -O sap13ns sap13ns
```

El usuario del sistema `sap13ns` se conecta a PostgreSQL por el socket local con autenticación *peer*, así que la base de datos no necesita contraseña.

### 3. Configura

Crea `/etc/sap13ns/sap13ns.env`, legible solo por root y por el servicio:

```ini
SAP13NS_DATABASE_URL=postgres:///sap13ns?host=/run/postgresql
SAP13NS_SECRET_KEY=salida-de-sap13ns-keygen
SAP13NS_BASE_URL=https://example.com
SAP13NS_LISTEN=unix:/run/sap13ns/sap13ns.sock
SAP13NS_DATA_DIR=/var/lib/sap13ns
SAP13NS_SERVE_UPLOADS=false
```

```bash
sudo mkdir -p /etc/sap13ns
sap13ns keygen   # pega el resultado en SAP13NS_SECRET_KEY
sudo chown root:sap13ns /etc/sap13ns/sap13ns.env
sudo chmod 0640 /etc/sap13ns/sap13ns.env
```

`SAP13NS_BASE_URL` es la dirección pública del sitio. Con una URL `https` se activan solas las cookies seguras y HSTS. `SAP13NS_SERVE_UPLOADS=false` deja los archivos subidos a nginx, que los sirve más rápido.

### 4. Ejecuta las migraciones

```bash
sudo -u sap13ns sap13ns migrate -env /etc/sap13ns/sap13ns.env
```

El servidor también aplica las migraciones pendientes cada vez que arranca, dentro de una transacción y con un bloqueo para que dos procesos nunca choquen. Ejecutarlas a mano la primera vez te permite ver el resultado antes de que el sitio esté en marcha.

### 5. La unidad de systemd

Guarda esto como `/etc/systemd/system/sap13ns.service`:

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

`RuntimeDirectory` crea `/run/sap13ns` para el socket. sap13ns crea el socket con modo `0660`, así que nginx tiene que pertenecer al grupo `sap13ns` para conectarse:

```bash
sudo usermod -aG sap13ns www-data
sudo systemctl daemon-reload
sudo systemctl enable --now sap13ns
```

### 6. nginx

Un bloque de servidor con el upstream en el socket Unix. nginx sirve directamente los archivos subidos con una política de aislamiento y pasa todo lo demás a sap13ns:

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

`client_max_body_size` coincide con el límite de subida por defecto, 16 MB (`SAP13NS_MAX_UPLOAD_MB`). Recarga nginx y añade HTTPS, por ejemplo con `sudo certbot --nginx -d example.com`.

### 7. Crea el primer administrador

Abre el enlace `/setup?token=…` que el servicio escribe en su log:

```bash
sudo journalctl -u sap13ns | grep setup
```

o crea la cuenta desde la línea de comandos:

```bash
sudo -u sap13ns sap13ns user create -env /etc/sap13ns/sap13ns.env \
  -username ana -email ana@example.com -role admin
```

Si le pasas una contraseña por la entrada estándar, usa esa; si no, genera una contraseña fuerte y la muestra una sola vez. Después inicia sesión en `/login` y activa la verificación en dos pasos desde tu perfil.

## Copias de seguridad

`sap13ns backup create` genera un único archivo con un volcado de PostgreSQL (en formato personalizado, hecho con `pg_dump`, así que necesitas las herramientas cliente de PostgreSQL) y el directorio de archivos subidos. Los archivos pueden cifrarse con una frase de paso y se escriben con modo `0600`. Antes de restaurar nada, cada archivo del paquete se comprueba contra su manifiesto.

Ejecútalo cada noche con un temporizador de systemd. `/etc/systemd/system/sap13ns-backup.service`:

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

Copia los archivos a otro lugar que no sea el propio servidor. Una copia que vive en el mismo disco no es una copia.

## Actualizar

Sustituye el binario y reinicia. Las migraciones se aplican al arrancar.

```bash
sudo install -m 0755 sap13ns /usr/local/bin/
sudo systemctl restart sap13ns
```

## Multisitio

Los comandos para crear y gestionar varios sitios desde una sola instalación **llegarán pronto**. Mientras tanto, ejecuta un servicio por sitio o usa el [alojamiento de sap13ns.site](/es/alojamiento).

La guía completa, con actualizaciones y resolución de problemas, está en [DEPLOY.md](https://github.com/vicktor/sap13ns/blob/main/docs/DEPLOY.md).
