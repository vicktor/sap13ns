---
type: post
key: multisite
title: "Multisitio: una cuenta, muchos sitios"
slug: multisitio-una-cuenta-muchos-sitios
excerpt: Cómo sap13ns gestiona muchos sitios desde una sola instalación, cada uno con su dominio, sus temas, sus plugins y su esquema de base de datos, con un único inicio de sesión.
date: 2026-10-17
categories: [Producto]
tags: [multisitio, alojamiento]
comments: closed
---
Casi todo el que publica en la web acaba teniendo más de un sitio. Un blog personal y la web de la empresa. Un sitio por cliente. Una web principal y otra pequeña para un evento que el año que viene nadie recordará. Mantener una instalación para cada uno es tedioso, y la alternativa habitual, una gran instalación en la que todos los sitios comparten tablas y plugins, convierte el sitio más débil en un problema para todos.

El multisitio de sap13ns intenta darte la comodidad de lo segundo sin su riesgo.

## Una cuenta

Tienes una sola cuenta, con una contraseña y una configuración de verificación en dos pasos. Con ella puedes pertenecer a todos los sitios que quieras, y tu rol se decide en cada sitio: puedes ser administrador de tu blog, editor en el sitio de un cliente y suscriptor en el de una amiga. Inicias sesión una vez y pasas de un sitio a otro sin volver a identificarte.

Lo que una cuenta no significa es un único juego de permisos. Ser administrador en un sitio no te da nada en otro si nadie allí te invita.

## Muchos sitios, separados de verdad

Cada sitio tiene:

- **su propio dominio**, o varios, y las peticiones se dirigen según el nombre de host;
- **sus propios temas**, incluido cualquier tema a medida subido para él;
- **sus propios plugins**, con sus propias aprobaciones y permisos;
- **su propio directorio de archivos subidos**;
- **su propio esquema de PostgreSQL**, con sus entradas, páginas, comentarios, menús, ajustes y datos de plugins.

El esquema es la parte a la que más tiempo dedicamos. La forma habitual de hacer un multisitio es añadir una columna `site_id` a todas las tablas y acordarse de filtrar por ella en todas las consultas. Funciona hasta el día en que una consulta se olvida, y entonces los borradores de un sitio aparecen en la página de búsqueda de otro.

Con un esquema por sitio, las consultas de un sitio no pueden ver las filas de otro, porque los datos de cada sitio viven en un esquema propio y sus consultas solo trabajan sobre ese esquema. No hay ningún filtro que olvidar. Un plugin activado en un sitio, aunque tenga `posts:read`, solo lee las entradas de ese sitio.

## Qué se comparte

Unas pocas cosas se comparten a propósito, porque compartirlas es precisamente la idea:

| Compartido | Propio de cada sitio |
|---|---|
| Cuentas, contraseñas y verificación en dos pasos | Roles y pertenencias |
| El binario de sap13ns y sus temas incluidos | Contenido, ajustes, menús y widgets |
| El servidor, PostgreSQL y nginx | Temas y plugins subidos, archivos subidos |

Las actualizaciones también se comparten. Sustituyes un binario y sus migraciones se aplican al arrancar en el esquema de cada sitio.

## Las contrapartidas

Los esquemas separados no salen gratis. Cada migración tiene que ejecutarse una vez por sitio, así que una instalación con cientos de sitios tarda más en actualizarse que una con un único juego de tablas compartido. Los informes que cruzan sitios, como «las últimas entradas de todos mis sitios», necesitan una consulta por sitio en lugar de una sola. Hacer la copia de un sitio es fácil; restaurarlo en otra instalación exige cuidado con las cuentas, porque las cuentas viven fuera del sitio.

Creemos que merece la pena pagar ese precio. Se nota en una actualización más lenta o en un informe algo más complejo, no en una fuga de datos.

## En qué punto está

El multisitio es como funciona el alojamiento en sap13ns.site: una cuenta para todos tus sitios, y cada sitio con su dirección, su diseño y su contenido. Si te alojas con nosotros, lo tienes desde el primer día.

Para quien instala en su propio servidor, los comandos para crear y gestionar varios sitios desde una sola instalación llegarán pronto. Preferimos publicarlos cuando las copias de seguridad, la restauración y las actualizaciones por sitio estén bien probadas que sacarlos antes de tiempo y enterarnos de los problemas por tus incidencias.[^1]

Hasta entonces, la forma fiable de tener varios sitios en tu servidor es usar varios servicios, uno por sitio, cada uno con su base de datos. Es menos cómodo, pero cada sitio ya está completamente aislado de los demás.

Si quieres seguir el trabajo, sigue el [repositorio en GitHub](https://github.com/vicktor/sap13ns). Y si hoy gestionas muchos sitios y tienes opinión sobre cómo debería funcionar esto, nos gustaría leerla en las [incidencias](https://github.com/vicktor/sap13ns/issues).

[^1]: Cuando se publiquen los comandos, estarán documentados en DEPLOY.md y en la [página de instalación](/es/instalar).
