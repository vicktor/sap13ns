---
type: page
key: docs
title: Documentación
slug: documentacion
excerpt: La documentación de referencia vive junto al código en GitHub. Empieza aquí para encontrar la guía que necesitas.
order: 6
menu: [primary, footer]
menu_label: Documentación
comments: closed
---
La documentación está en el repositorio, junto al código que describe, de modo que cambia en el mismo commit que el comportamiento. Estas son las guías y para qué sirve cada una. Por ahora están en inglés.

## Guías

### Despliegue — DEPLOY.md

Cómo poner sap13ns en producción: el instalador, la configuración, PostgreSQL, las unidades de systemd, nginx con un socket Unix, HTTPS, copias de seguridad y actualizaciones, y el proxy para una red de sitios. La [página de instalación](/es/instalar) es una versión resumida.

[Leer DEPLOY.md](https://github.com/vicktor/sap13ns/blob/main/docs/DEPLOY.md)

### Temas — THEMES.md

Cómo se construye un tema: el manifiesto `theme.json`, los menús, las zonas de widgets y las opciones, las plantillas y los datos que recibe cada una, los parciales compartidos, la traducción de los textos del tema y cómo generar la hoja de estilos con Tailwind CSS. También explica lo que se puede cambiar sin tocar un tema: el personalizador y el constructor de páginas.

[Leer THEMES.md](https://github.com/vicktor/sap13ns/blob/main/docs/THEMES.md)

### Plugins — PLUGINS.md

El contrato de los plugins (ABI 1): el formato del paquete, el manifiesto, los hooks y eventos, los permisos, los límites, las funciones del anfitrión, el analizador, el SDK de Go, cómo funcionan la aprobación y el anclaje al hash, y la revisión de los plugins subidos en una red de sitios.

[Leer PLUGINS.md](https://github.com/vicktor/sap13ns/blob/main/docs/PLUGINS.md)

### Redes de sitios — MULTISITE.md

Cómo servir muchos sitios desde una sola instalación: el portal, el inicio de sesión único, el área de administración de la red, los planes y sus límites, los dominios propios, TLS, la referencia de la línea de comandos, las copias de seguridad y lo que todavía no está disponible.

[Leer MULTISITE.md](https://github.com/vicktor/sap13ns/blob/main/docs/MULTISITE.md)

### Pasar a WordPress — MOVING-TO-WORDPRESS.md

Cómo llevar un sitio a WordPress con una de sus copias de seguridad: las dos maneras de hacerlo, los pasos en el panel y con WP-CLI, qué hacer después de importar, y qué se lleva y qué no. Está en inglés.

[Leer MOVING-TO-WORDPRESS.md](https://github.com/vicktor/sap13ns/blob/main/docs/MOVING-TO-WORDPRESS.md)

### Cobros — BILLING.md

Cómo configurar Stripe para una red de sitios: productos y precios, el portal de clientes, el webhook, las variables de entorno y qué le pasa a un sitio cuando deja de pagarse.

[Leer BILLING.md](https://github.com/vicktor/sap13ns/blob/main/docs/BILLING.md)

### Versiones y actualizaciones — RELEASES.md

Cómo se compilan y se firman las versiones, cómo se entera un sitio de que hay una nueva y cómo la instala `sap13ns update` y la deshace.

[Leer RELEASES.md](https://github.com/vicktor/sap13ns/blob/main/docs/RELEASES.md)

### Seguridad — SECURITY.md

El modelo de amenazas, las defensas de cada capa y cómo informar de una vulnerabilidad.

[Leer SECURITY.md](https://github.com/vicktor/sap13ns/blob/main/docs/SECURITY.md)

## Por dónde empezar

| Si quieres… | Lee |
|---|---|
| probarlo en tu equipo | [Instalar, inicio rápido](/es/instalar) |
| publicar un sitio | DEPLOY.md |
| cambiar el aspecto de un sitio | THEMES.md |
| añadir una función | PLUGINS.md |
| tener muchos sitios en una sola instalación | MULTISITE.md |
| pasar un sitio a WordPress | MOVING-TO-WORDPRESS.md |
| entender los riesgos | SECURITY.md y el [resumen de seguridad](/es/seguridad) |
| olvidarte del servidor | [Alojamiento en sap13ns.site](/es/alojamiento) |

## La línea de comandos de un vistazo

```text
sap13ns serve                         arranca el servidor web (por defecto)
sap13ns migrate                       aplica las migraciones y termina
sap13ns keygen                        genera una nueva SAP13NS_SECRET_KEY
sap13ns user create                   crea un usuario (-username, -email, -role, -name)
sap13ns user reset-password           cambia la contraseña de un usuario
sap13ns plugin new|pack|check         empieza, empaqueta y analiza un plugin
sap13ns plugin list|install           lista o instala los plugins del sitio
sap13ns plugin hash <dir>             muestra el SHA-256 del módulo y los recursos de un plugin
sap13ns backup create|list|verify|restore|prune|decrypt
                                      copias cifradas de la base de datos y los archivos subidos
sap13ns backup create|list|restore|prune -format site
                                      las copias propias del sitio, que WordPress importa
sap13ns assistant reindex|status      el índice de búsqueda del asistente del sitio
sap13ns update                        instala la última versión firmada
sap13ns version                       muestra la versión

Redes de sitios (SAP13NS_MULTISITE=true):
sap13ns network init                  crea la red, su portal y un administrador
sap13ns site create|list|suspend|resume|delete|purge
sap13ns site domain add|verify|primary|remove
```

Todos los comandos aceptan `-env <archivo>` para leer la configuración de un archivo. En una red, los comandos que trabajan sobre un sitio llevan `-site <slug>`.

## Ayuda y comentarios

- **Código fuente:** [github.com/vicktor/sap13ns](https://github.com/vicktor/sap13ns)
- **Errores e ideas:** [abre una incidencia](https://github.com/vicktor/sap13ns/issues). Indica la versión (`sap13ns version`), qué hiciste y qué esperabas.
- **Problemas de seguridad:** no abras una incidencia pública; escribe a victor@bauber.xyz.
