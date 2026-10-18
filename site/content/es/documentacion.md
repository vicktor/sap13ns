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

Cómo poner sap13ns en producción: las variables de configuración, PostgreSQL, la unidad de systemd, nginx con un socket Unix, HTTPS, copias de seguridad y actualizaciones. La [página de instalación](/es/instalar) es una versión resumida.

[Leer DEPLOY.md](https://github.com/vicktor/sap13ns/blob/main/docs/DEPLOY.md)

### Temas — THEMES.md

Cómo se construye un tema: el manifiesto `theme.json`, los menús, las zonas de widgets y las opciones, las plantillas y los datos que recibe cada una, los parciales compartidos, la traducción de los textos del tema y cómo generar la hoja de estilos con Tailwind CSS.

[Leer THEMES.md](https://github.com/vicktor/sap13ns/blob/main/docs/THEMES.md)

### Plugins — PLUGINS.md

El contrato de los plugins (ABI 1): el manifiesto, los hooks y eventos, los permisos, los límites, las funciones del anfitrión, el SDK de Go y cómo funcionan la aprobación y el anclaje al hash.

[Leer PLUGINS.md](https://github.com/vicktor/sap13ns/blob/main/docs/PLUGINS.md)

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
| entender los riesgos | SECURITY.md y el [resumen de seguridad](/es/seguridad) |
| olvidarte del servidor | [Alojamiento en sap13ns.site](/es/alojamiento) |

## La línea de comandos de un vistazo

```text
sap13ns serve                  arranca el servidor web (por defecto)
sap13ns migrate                aplica las migraciones y termina
sap13ns keygen                 genera una nueva SAP13NS_SECRET_KEY
sap13ns user create            crea un usuario (-username, -email, -role, -name)
sap13ns user reset-password    cambia la contraseña de un usuario
sap13ns plugin hash <dir>      muestra el SHA-256 del módulo de un plugin
sap13ns backup create          genera una copia de seguridad
sap13ns version                muestra la versión
```

Todos los comandos aceptan `-env <archivo>` para leer la configuración de un archivo.

## Ayuda y comentarios

- **Código fuente:** [github.com/vicktor/sap13ns](https://github.com/vicktor/sap13ns)
- **Errores e ideas:** [abre una incidencia](https://github.com/vicktor/sap13ns/issues). Indica la versión (`sap13ns version`), qué hiciste y qué esperabas.
- **Problemas de seguridad:** no abras una incidencia pública; escribe a victor@bauber.xyz.
