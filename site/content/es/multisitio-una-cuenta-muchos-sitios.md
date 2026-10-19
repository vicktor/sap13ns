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

Lo que una cuenta no significa es un único juego de permisos. Ser administrador en un sitio no te da nada en otro si allí no te hacen miembro.

## Muchos sitios, separados de verdad

Cada sitio tiene:

- **su propio dominio**, o varios, y las peticiones se dirigen según el nombre de host;
- **su propio tema**, elegido entre los incluidos, los de la red y los que se instalen solo para ese sitio;
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
| El binario de sap13ns, sus temas incluidos y los temas de la red | Contenido, ajustes, menús y widgets |
| El servidor, PostgreSQL y nginx | Los temas y plugins propios del sitio, archivos subidos |

Las actualizaciones también se comparten. Sustituyes un binario y sus migraciones se aplican al arrancar en el esquema de cada sitio.

## Las contrapartidas

Los esquemas separados no salen gratis. Cada migración tiene que ejecutarse una vez por sitio, así que una instalación con cientos de sitios tarda más en actualizarse que una con un único juego de tablas compartido. Los informes que cruzan sitios, como «las últimas entradas de todos mis sitios», necesitan una consulta por sitio en lugar de una sola. Copiar un sitio consiste en volcar un esquema y un directorio; restaurarlo en otra instalación exige cuidado con las cuentas, porque las cuentas viven fuera del sitio.

Creemos que merece la pena pagar ese precio. Se nota en una actualización más lenta o en un informe algo más complejo, no en una fuga de datos.

## En qué punto está

El multisitio es como funciona el alojamiento en sap13ns.site: una cuenta para todos tus sitios, y cada sitio con su dirección, su diseño y su contenido. Si te alojas con nosotros, lo tienes desde el primer día.

Quien instala en su propio servidor también puede usarlo. Activa `SAP13NS_MULTISITE=true`, crea la red y su portal con `sap13ns network init` y gestiona los sitios con `sap13ns site create`, `list`, `suspend`, `resume`, `delete`, `purge` y `domain`. Una red trae inicio de sesión único a través del portal, un área de administración de la red, planes con límites de almacenamiento y de dominios propios, cobro con Stripe y dominios propios que se verifican por DNS y se sirven con TLS.[^1]

Algunas piezas todavía no están:

- **Conversión.** Una instalación de un solo sitio todavía no se puede convertir en un sitio de una red, así que una red empieza con una base de datos nueva.
- **Subida de temas.** Los sitios todavía no pueden subir sus propios temas.
- **Invitaciones.** Solo los administradores de la red añaden miembros a un sitio; los propietarios todavía no pueden invitar a nadie por su cuenta.

*Actualización, 19 de octubre de 2026:* cuando publicamos esta entrada, los comandos para quien instala en su propio servidor estaban por llegar, y mientras tanto recomendábamos un servicio por sitio. Ya están publicados, con los límites de la lista anterior.

*Actualización, 19 de octubre de 2026, más tarde:* las copias por sitio también han llegado, así que salen de la lista. Una red hace cada noche la copia de cada sitio en un `.zip` propio, dentro del almacenamiento de su plan. Los administradores del sitio las descargan y las restauran en **Copias de seguridad**, y WordPress las importa tal cual. Al restaurar, las cuentas se emparejan por nombre de usuario y después por correo electrónico, y las que faltan se crean sin contraseña; nunca se borra ninguna. Ese es el cuidado con las cuentas del que hablábamos arriba. La [guía para pasar a WordPress](https://github.com/vicktor/sap13ns/blob/main/docs/MOVING-TO-WORDPRESS.md) (en inglés) tiene los detalles. La subida de temas también sale de la lista: en los planes que lo permiten, un sitio sube sus propios temas, que se comprueban automáticamente antes de publicarse.

Si quieres seguir el trabajo, sigue el [repositorio en GitHub](https://github.com/vicktor/sap13ns). Y si hoy gestionas muchos sitios y tienes opinión sobre cómo debería funcionar esto, nos gustaría leerla en las [incidencias](https://github.com/vicktor/sap13ns/issues).

[^1]: Los comandos están documentados en [MULTISITE.md](https://github.com/vicktor/sap13ns/blob/main/docs/MULTISITE.md), y la [página de instalación](/es/instalar) tiene una versión resumida.
