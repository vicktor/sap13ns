---
type: post
key: introducing
title: Presentamos sap13ns
slug: presentamos-sap13ns
excerpt: Un gestor de contenidos al estilo de WordPress, escrito en Go, que se distribuye como un único binario y parte de la idea de que internet es un sitio hostil.
date: 2026-10-18
categories: [Lanzamientos]
tags: [lanzamiento, go, código abierto]
comments: closed
---
Hoy publicamos sap13ns: un gestor de contenidos para blogs, revistas, documentación y webs de empresa. Está escrito en Go, guarda los datos en PostgreSQL, funciona detrás de nginx y se distribuye como un único ejecutable. El código está en [GitHub](https://github.com/vicktor/sap13ns), y esta web funciona con él.

## ¿Por qué otro CMS?

WordPress acertó en muchas cosas. Entradas y páginas, categorías y etiquetas, menús y widgets, temas y plugins: es un modelo fácil de explicar a cualquier editor y ha sobrevivido a veinte años de modas. Lo hemos mantenido casi entero.

Lo que queríamos cambiar es todo lo que lo rodea. Un sitio típico hoy es un servidor de aplicaciones, un intérprete, un montón de plugins con acceso total a la base de datos, una capa de caché añadida a posteriori y una lista de actualizaciones pendientes que nunca llega a cero. Cada pieza tiene sentido por separado. Juntas forman un sitio lento de cargar, pesado de mantener y fácil de asaltar.

Así que empezamos con unas pocas reglas e intentamos no saltárnoslas.

## Las reglas

**Un archivo que desplegar.** Las plantillas, los estilos, los iconos, las traducciones y las migraciones van compilados dentro del binario. Instalar es copiar un archivo. Actualizar es sustituirlo y reiniciar. En el servidor no hay Node.js ni gestor de paquetes.

**Rápido sin ajustes.** Las páginas públicas se guardan en memoria y se sirven sin tocar la base de datos hasta que algo cambia. La búsqueda usa el motor de texto completo del propio PostgreSQL. Las imágenes se redimensionan en variantes adaptables al subirlas, así que los móviles no descargan fotos pensadas para una pantalla 4K.

**Seguro por defecto, no por lista de tareas.** Todas las páginas se sirven con una política de seguridad de contenidos estricta: nada de scripts ni estilos en línea, en ningún sitio. Las contraseñas se guardan con Argon2id. Los formularios llevan tokens CSRF. La verificación en dos pasos con códigos de recuperación viene integrada. Los archivos subidos se identifican por su contenido y se sirven aislados.

**Plugins que no pueden hacerte daño.** Es la regla de la que estamos más orgullosos. Los plugins son módulos de WebAssembly que empiezan sin acceso a archivos, red ni base de datos. Cada capacidad adicional es un permiso que tú apruebas, y la aprobación queda ligada al archivo exacto que revisaste. Lo contamos con más detalle en [Plugins en un sandbox de WebAssembly](/es/blog/plugins-en-un-sandbox-webassembly).

## Qué incluye

Un editor Markdown con vista previa en directo. Cinco temas (Apple, Neumorphism, Glassmorphism, Bento Grid y Dashboard / SaaS), todos con modo claro y oscuro. Contenido en varios idiomas, enlazado como traducciones, con `hreflang` para los buscadores. RSS, Atom y JSON Feed para todo el sitio y para cada categoría, etiqueta y autor. Comentarios con moderación y un filtro de spam discreto. Cinco roles, de administrador a suscriptor, y un registro de auditoría de quién hizo qué.

También hay un importador de archivos de exportación de WordPress, imágenes automáticas para redes sociales, redirecciones, estadísticas sin cookies y copias de seguridad de la base de datos y los archivos subidos en un solo paquete.

Y hay un asistente de IA, sobre el que queremos ser precisos. Puede redactar un extracto, sugerir un título SEO, traducir una entrada o proponer una idea de imagen, usando Claude, cualquier API compatible con OpenAI, un modelo local o Gemini. Solo actúa cuando un editor se lo pide y nunca publica nada por su cuenta. Siempre hay una persona que lee el resultado antes.[^1]

## Qué falta

El multisitio, una cuenta que gestiona muchos sitios con su propio dominio y su propio esquema de base de datos cada uno, es como funciona nuestro alojamiento, y también puedes usarlo en tu propio servidor. Le faltan algunas piezas. `sap13ns backup` todavía no sabe copiar ni restaurar un solo sitio de una red, así que una red se copia entera con `pg_dump` y una copia del directorio de datos. Una instalación de un solo sitio no se puede convertir en un sitio de una red, y los sitios no pueden subir sus propios temas. La documentación seguirá creciendo a medida que la gente pregunte cosas en las que no habíamos pensado.

*Actualización, 19 de octubre de 2026:* cuando publicamos esta entrada, las passkeys estaban previstas y los comandos para usar el multisitio en tu propio servidor llegarían pronto. Las dos cosas ya están disponibles. Las passkeys sirven para entrar sin contraseña y como segundo factor, siempre con verificación del usuario. `SAP13NS_MULTISITE=true`, `sap13ns network init` y los comandos `sap13ns site` ponen en marcha una red con inicio de sesión único, un área de administración de la red, cobro con Stripe, límites por plan y dominios propios verificados por DNS y servidos con TLS. [MULTISITE.md](https://github.com/vicktor/sap13ns/blob/main/docs/MULTISITE.md) lo explica.

Preferimos contártelo ahora a que lo descubras en mitad de una migración.

## Cómo probarlo

Si tienes un equipo con PostgreSQL, el [inicio rápido](/es/instalar) se hace en unos minutos: descargas el binario, creas una base de datos, escribes un archivo de configuración de cuatro líneas y arrancas el servidor. El primer arranque muestra un enlace de un solo uso para crear tu cuenta de administrador.

Si prefieres no tener ningún servidor, ofrecemos [alojamiento](/es/alojamiento). Los sitios personales y no comerciales son gratis después de revisarlos, y los comerciales pagan un plan mensual.

## La licencia

sap13ns es de código abierto con licencia CPAL-1.0. Puedes usarlo para cualquier cosa, también con fines comerciales, sin pagar nada. A cambio, los sitios mantienen un enlace visible «Powered by sap13ns» y, si ofreces una versión modificada a otras personas a través de una red, publicas tus cambios. La página de [código abierto](/es/codigo-abierto) lo explica en palabras sencillas.

## Lo que viene

Las próximas semanas las vamos a dedicar a escuchar: leer incidencias, arreglar aquello con lo que la gente tropiece y escribir las guías que resulten faltar. Si construyes algo con sap13ns, o si algo se rompe, [cuéntanoslo en GitHub](https://github.com/vicktor/sap13ns/issues). Ahora mismo es lo más útil que puedes hacer por el proyecto.

[^1]: El asistente es opcional. No hace nada hasta que un administrador configura un proveedor, ya sea una clave de API o la dirección de un modelo local.
