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

**1. Descarga el binario.** Baja el archivo para tu plataforma desde [GitHub Releases](https://github.com/vicktor/sap13ns/releases): `sap13ns_<versión>_linux_amd64.tar.gz`, `linux_arm64`, `darwin_amd64` o `darwin_arm64`, junto con `SHA256SUMS`. Compruébalo y descomprímelo:

```bash
sha256sum -c --ignore-missing SHA256SUMS
tar xzf sap13ns_<versión>_linux_amd64.tar.gz
```

El archivo contiene el binario `sap13ns` y la carpeta `deploy/`, con el instalador, las unidades de systemd y el sitio de nginx.

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

Estos pasos siguen [DEPLOY.md](https://github.com/vicktor/sap13ns/blob/main/docs/DEPLOY.md) en un servidor Debian 13 o Ubuntu 24.04, o posterior, con PostgreSQL 16 o posterior y nginx instalados. Otras distribuciones funcionan igual, con sus propios nombres de paquete.

### 1. Ejecuta el instalador

Descomprime el archivo de Linux para tu servidor, como en el inicio rápido, y ejecuta el instalador como root:

```bash
mkdir sap13ns && tar xzf sap13ns_<versión>_linux_amd64.tar.gz -C sap13ns && cd sap13ns
sudo ./deploy/install.sh example.com
```

Puedes volver a ejecutarlo sin miedo: si ya existen la configuración, la base de datos o el rol, los deja como están. El instalador:

- crea el usuario del sistema `sap13ns`, el grupo `sap13ns-web` y `/var/lib/sap13ns`;
- instala el binario en `/usr/local/bin`;
- crea la base de datos y el rol `sap13ns`, y activa `pgvector` si está instalado. El servicio se conecta por el socket local con autenticación *peer*, así que la base de datos no necesita contraseña;
- escribe `/etc/sap13ns/sap13ns.env`, legible solo por root y por el servicio, con una clave secreta nueva;
- instala las unidades de systemd del servicio y de la copia de seguridad nocturna, y el sitio de nginx.

### 2. El socket y nginx

sap13ns escucha en el socket Unix `/run/sap13ns/sap13ns.sock`, con modo `0660` y el grupo `sap13ns-web`. nginx (`www-data`) se une a ese grupo y a ningún otro, así que puede llegar al socket pero no puede leer el archivo de configuración ni el directorio de datos. Si lo montas a mano, no añadas nunca `www-data` al grupo `sap13ns`.

nginx pasa todas las peticiones a sap13ns por el socket, también las de los archivos subidos: sap13ns los sirve él mismo con una política de seguridad de contenidos que los aísla.

### 3. Configura

El instalador escribe la configuración a partir de `deploy/sap13ns.env.example`. Estas son las líneas que importan:

```ini
SAP13NS_DATABASE_URL=postgres:///sap13ns?host=/run/postgresql
SAP13NS_SECRET_KEY=salida-de-sap13ns-keygen
SAP13NS_BASE_URL=https://example.com
SAP13NS_LISTEN=unix:/run/sap13ns/sap13ns.sock
SAP13NS_SOCKET_GROUP=sap13ns-web
SAP13NS_DATA_DIR=/var/lib/sap13ns
```

`SAP13NS_BASE_URL` es la dirección pública del sitio. Con una URL `https`, las cookies se marcan como `Secure` y se envía HSTS. Guarda una copia de `SAP13NS_SECRET_KEY` fuera del servidor: si cambia, se cierran todas las sesiones y las claves guardadas y las passkeys dejan de servir. Reinicia el servicio después de cualquier cambio.

### 4. HTTPS

Consigue un certificado. Mientras no lo tengas, comenta el bloque de servidor `443` del sitio de nginx:

```bash
sudo certbot certonly --webroot -w /var/www/letsencrypt -d example.com
sudo nginx -t && sudo systemctl reload nginx
```

### 5. Crea el primer administrador y arranca

```bash
sudo -u sap13ns sap13ns user create -env /etc/sap13ns/sap13ns.env \
  -username ana -email ana@example.com -role admin
sudo systemctl start sap13ns
```

Si le pasas una contraseña por la entrada estándar, `user create` usa esa; si no, genera una contraseña fuerte y la muestra una sola vez. Si arrancas el servicio sin crear ningún usuario, en su lugar escribe en el log un enlace `/setup?token=…` de un solo uso (`sudo journalctl -u sap13ns | grep setup`).

Abre `https://example.com/admin` e inicia sesión. Antes de nada se te pedirá que configures un segundo factor: una aplicación de autenticación o una passkey.

Las migraciones se aplican cada vez que arranca el servidor, dentro de una transacción y con un bloqueo para que dos procesos nunca choquen. Para aplicarlas antes del primer arranque, ejecuta `sudo -u sap13ns sap13ns migrate -env /etc/sap13ns/sap13ns.env`.

### Instalar a mano

Los pasos del instalador son cortos y están comentados en [`deploy/install.sh`](https://github.com/vicktor/sap13ns/blob/main/deploy/install.sh). Estos son los archivos que instala:

| Archivo | Se instala como |
|---|---|
| `deploy/sap13ns.env.example` | `/etc/sap13ns/sap13ns.env` (root:sap13ns, `0640`) |
| `deploy/systemd/sap13ns.service` | `/etc/systemd/system/` |
| `deploy/systemd/sap13ns-backup.service` y `.timer` | `/etc/systemd/system/` |
| `deploy/nginx/sap13ns.conf` | `/etc/nginx/sites-available/` |

La unidad del servicio ejecuta sap13ns como el usuario `sap13ns`, con el grupo `sap13ns-web` añadido y con las opciones de aislamiento de systemd activadas.

## Copias de seguridad

`sap13ns backup create|list|verify|restore|prune` se encarga de las copias. Un archivo de copia contiene un volcado de PostgreSQL (hecho con `pg_dump`, así que necesitas las herramientas cliente de PostgreSQL) y el directorio de archivos subidos, y se escribe con modo `0600`. Cuando `SAP13NS_BACKUP_PASSPHRASE_FILE` apunta a un archivo que solo puede leer `sap13ns`, las copias se cifran con AES-256-GCM, con una clave derivada de la frase de paso con Argon2id. Guarda una copia de la frase de paso en otro sitio: sin ella no se puede restaurar. Antes de restaurar nada, cada archivo del paquete se comprueba contra su manifiesto.

El instalador activa `sap13ns-backup.timer`, que ejecuta `backup create` y después `backup prune` cada noche a las 03:30. Por defecto se conservan 7 copias diarias, 4 semanales y 12 mensuales. Para hacer una copia ahora y ver las que tienes:

```bash
sudo systemctl start sap13ns-backup
sudo -u sap13ns sap13ns backup list -env /etc/sap13ns/sap13ns.env
```

Para restaurar, para el servicio, ejecuta `sudo -u sap13ns sap13ns backup restore -env /etc/sap13ns/sap13ns.env ARCHIVO` y vuelve a arrancarlo.

El temporizador hace también la copia propia del sitio (`backup create -format site`): un `.zip` que WordPress importa tal cual y que **Copias de seguridad**, en el panel, muestra, descarga y restaura. Se conservan 7 diarias y 4 semanales. Para irte a WordPress, sigue [la guía](https://github.com/vicktor/sap13ns/blob/main/docs/MOVING-TO-WORDPRESS.md) (en inglés).

Copia los archivos a otro lugar que no sea el propio servidor. Una copia que vive en el mismo disco no es una copia.

## Actualizar

Los administradores ven un aviso en el panel cuando sale una versión nueva. Haz una copia de seguridad, instálala y reinicia:

```bash
sudo systemctl start sap13ns-backup
sudo sap13ns update
sudo systemctl restart sap13ns
```

`sap13ns update` descarga la versión para tu plataforma, comprueba sus sumas de verificación firmadas y sustituye el binario de forma atómica. El binario anterior se queda como `/usr/local/bin/sap13ns.old`, así que volver atrás es ponerlo en su sitio y reiniciar. Las migraciones se aplican al arrancar. Los detalles están en [RELEASES.md](https://github.com/vicktor/sap13ns/blob/main/docs/RELEASES.md).

## Multisitio

Una sola instalación puede servir una red de sitios entera: un portal donde cada persona tiene una cuenta y tantos sitios como quieras, cada uno con su propio esquema de PostgreSQL, sus archivos, sus usuarios y sus plugins. Está desactivado hasta que lo activas, y empieza con una base de datos nueva: una instalación de un solo sitio todavía no se puede convertir en una red.

1. Apunta al servidor el host del portal y un comodín para los sitios, y consigue un certificado comodín para ellos. Los dominios propios obtienen sus certificados con el TLS bajo demanda de Caddy, con el cliente ACME integrado o uno a uno con nginx.
2. Añade `SAP13NS_MULTISITE=true` y `SAP13NS_SITES_DOMAIN` a la configuración. `SAP13NS_BASE_URL` pasa a ser la dirección del portal. Desactiva `sap13ns-backup.timer`: una red hace cada noche la copia de cada uno de sus sitios.
3. Crea la red, su portal y un primer administrador de la red, y arranca el servicio:

   ```bash
   sudo -u sap13ns sap13ns network init -env /etc/sap13ns/sap13ns.env -username ana -email ana@example.com
   sudo systemctl start sap13ns
   ```

4. Deja que la gente se registre en el portal, si abres el registro, y pida sitios, o créalos con `sap13ns site create -slug blog -owner ana@example.com -plan pro`. `sap13ns site list`, `suspend`, `resume`, `delete`, `purge` y `domain` sirven para gestionarlos.

Una red tiene inicio de sesión único a través del portal, un área de administración de la red, planes con límites de almacenamiento y de dominios propios, cobro con Stripe y dominios propios verificados por DNS y servidos con TLS. Cada sitio tiene su copia de seguridad cada noche, y sus administradores las descargan y las restauran en **Copias de seguridad**. Esas copias guardan los sitios, no la red, así que guarda también un `pg_dump` y una copia del directorio de datos. Los sitios suben sus propios temas en los planes que lo permiten. Una cosa todavía no está disponible en una red: una instalación de un solo sitio no se puede convertir en un sitio de la red. La guía es [MULTISITE.md](https://github.com/vicktor/sap13ns/blob/main/docs/MULTISITE.md), y [DEPLOY.md](https://github.com/vicktor/sap13ns/blob/main/docs/DEPLOY.md#networks-of-sites) tiene las configuraciones de nginx y Caddy.

Si prefieres no tener ningún servidor, está el [alojamiento en sap13ns.site](/es/alojamiento).

La guía completa, con las notas de bastionado y la resolución de problemas, está en [DEPLOY.md](https://github.com/vicktor/sap13ns/blob/main/docs/DEPLOY.md).
