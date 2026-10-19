---
type: post
key: single-binary
title: Por qué un solo binario
slug: por-que-un-solo-binario
excerpt: Plantillas, estilos, iconos, traducciones y migraciones viven dentro de un único ejecutable. Esto es lo que ganamos con ello y lo que nos cuesta.
date: 2026-10-15
categories: [Ingeniería]
tags: [go, despliegue, nginx]
comments: closed
---
La primera decisión que tomamos sobre sap13ns, antes que cualquier función, fue cómo se iba a desplegar. Queríamos que la respuesta a «¿cómo lo instalo?» cupiera en una frase: copias un archivo y lo arrancas.

Esa decisión ha marcado una parte sorprendente del código, así que merece una explicación.

## Qué va dentro

Go puede incrustar archivos en el programa al compilarlo. Lo usamos para todo lo que el servidor necesita mientras funciona:

| Dentro del binario | Fuera del binario |
|---|---|
| Plantillas del panel y de los temas | Los datos de PostgreSQL |
| Hojas de estilo compiladas y los scripts pequeños | Los archivos subidos |
| Iconos y traducciones | El archivo de configuración |
| Migraciones de la base de datos | Los temas y plugins adicionales que añadas |

Las hojas de estilo se generan con Tailwind CSS mientras desarrollamos, y el resultado se guarda en el repositorio. Por eso no hay Node.js en el servidor: Node solo se ejecuta en el equipo de quien desarrolla, antes de que exista el binario.

## Lo que ganamos

**Instalar es copiar un archivo.** No hay que cuadrar versiones de un intérprete, ni activar extensiones, ni resolver dependencias en el servidor. La versión que probaste es, byte a byte, la que ejecutas.

**Las actualizaciones son atómicas.** Sustituir un binario y reiniciar un servicio funciona o no funciona. No existe un estado a medio actualizar en el que plantillas nuevas se encuentran con código viejo, que es una fuente clásica de fallos misteriosos en los sistemas que se actualizan archivo a archivo.

```bash
sudo install -m 0755 sap13ns /usr/local/bin/
sudo systemctl restart sap13ns
```

**Los cambios de esquema viajan con el código.** Las migraciones también van incrustadas, y el servidor aplica las pendientes al arrancar, dentro de una transacción y tras un bloqueo consultivo de PostgreSQL, para que dos procesos que arrancan a la vez no se pisen. Nunca despliegas código que espera una columna que la base de datos todavía no tiene.

**Menos superficie de ataque.** Un servidor sin gestor de paquetes, sin herramientas de compilación y sin intérprete le deja muy poco con lo que trabajar a un intruso. Los archivos subidos nunca pueden convertirse en código: se comprueban por su contenido, se guardan como datos y se sirven aislados. El único código que se carga en marcha son los plugins, y esos se ejecutan dentro de WebAssembly sin más permisos que los que hayas aprobado.

**Volver atrás es copiar el archivo anterior.** Mientras las migraciones de la versión nueva solo hayan añadido cosas, el binario anterior sigue funcionando con la misma base de datos.

## Detrás de nginx, en un socket

No intentamos que sap13ns sea un servidor web completo. nginx es muy bueno con TLS, la compresión, los archivos estáticos y los clientes lentos, así que le dejamos esos trabajos y nuestro proceso se centra en la aplicación.

En producción, sap13ns escucha en un socket Unix en lugar de en un puerto TCP. El socket se crea con modo `0660` y pertenece al grupo del servicio, así que nginx puede hablar con él y nadie más en la máquina. No hay ningún puerto de la aplicación que proteger con el cortafuegos ni forma de saltarse nginx atacando directamente a la aplicación.

nginx sirve los archivos subidos directamente desde el disco, con una política de seguridad `sandbox`, para que el proceso de Go no pierda tiempo enviando imágenes.

*Actualización, 19 de octubre de 2026:* dos detalles de esta configuración han cambiado. El socket pertenece ahora a un grupo propio, `sap13ns-web`, al que solo se une nginx, así que nginx puede llegar a sap13ns pero no puede leer su configuración ni su directorio de datos. Por eso mismo, la configuración de nginx que distribuimos deja que sap13ns sirva él mismo los archivos subidos a través del socket, con la misma política de aislamiento; `SAP13NS_SERVE_UPLOADS=false` sigue dejándoselos a nginx, si le das permiso de lectura sobre el directorio de archivos subidos. Además, actualizar es más fácil: `sap13ns update` instala una versión firmada y conserva el binario anterior por si hay que volver atrás.

## Lo que nos cuesta

No sería honesto fingir que no hay contrapartidas.

**No puedes editar una plantilla incluida en el servidor.** Si quieres cambiar el aspecto del panel, hay que recompilar. Para los temas dejamos una salida: el servidor también carga temas desde un directorio del disco, así que un sitio puede tener su propio tema sin una compilación a medida. Esta web hace exactamente eso.

**El binario pesa más que un script.** Lo lleva todo consigo. En un servidor actual la diferencia es irrelevante, pero no son unos pocos kilobytes.

**Los plugins no pueden ser paquetes de Go.** Compilar los plugins dentro del binario obligaría a hacer una compilación distinta para cada sitio, así que los plugins son módulos de WebAssembly que se cargan en marcha. Resultó ser una ventaja para la seguridad, pero fue una consecuencia de esta decisión, no su motivo.

**Las compilaciones tienen que ser reproducibles.** Si todo vive en un único artefacto, el proceso de compilación importa mucho. Fijamos las versiones de nuestras herramientas y guardamos en el repositorio los recursos generados, para que una versión se pueda recompilar desde el mismo commit con el mismo resultado.[^1]

## ¿Lo volveríamos a hacer?

Sí. Cada vez que hemos desplegado sap13ns, la parte aburrida ha sido aburrida, que es justo lo que quieres de un despliegue. El tiempo que no hemos dedicado al empaquetado ha ido a lo que la gente sí ve: el editor, los temas y el modelo de seguridad.

Si quieres probarlo, la [guía de instalación](/es/instalar) te lleva de la descarga a un sitio en marcha, con la unidad de systemd y la configuración de nginx incluidas.

[^1]: Las herramientas de frontend (Tailwind CSS, los estilos de componentes y el juego de iconos) tienen versiones exactas fijadas en el repositorio.
