---
type: page
key: security
title: Seguridad
slug: seguridad
excerpt: Valores seguros por defecto, pocas piezas móviles y plugins que no llegan a lo que no has permitido. Esto es lo que protege un sitio con sap13ns.
order: 4
menu: [primary, footer, landing]
menu_label: Seguridad
comments: closed
---
La mayoría de los ataques a gestores de contenidos no son ingeniosos. Van a por un plugin olvidado, una contraseña débil, una carpeta de subidas que ejecuta código o un formulario que se fía de lo que recibe. sap13ns está pensado para que esas puertas estén cerradas antes de que instales nada.

## Menos piezas móviles

**Sin Node en producción.** Las hojas de estilo y los iconos se generan mientras desarrollamos y van dentro del binario. En el servidor solo hay un proceso de Go, PostgreSQL y nginx. Ni gestor de paquetes, ni intérprete, ni un árbol de dependencias que cambia sin avisar.

**Un binario, una versión.** Una actualización sustituye el programa entero de golpe. No existe un estado en el que la mitad de los archivos son nuevos y la otra mitad, viejos.

**Un socket Unix detrás de nginx.** En producción, sap13ns escucha en un socket Unix con modo `0660`, que pertenece a un grupo al que solo se une nginx (`sap13ns-web`). nginx puede conectarse a él, pero no puede leer el archivo de configuración ni el directorio de datos. La aplicación no expone ningún puerto a la red.

## El navegador también trabaja

**Una política de seguridad de contenidos estricta.** Todas las páginas se sirven con una política que solo permite scripts y hojas de estilo del propio sitio: `script-src 'self'; style-src 'self'`. No hay scripts en línea, ni estilos en línea, ni atributos de eventos en ningún sitio, ni en el panel ni en los temas. Si alguien consigue inyectar marcado, el navegador se niega a ejecutarlo.

**Cabeceras de seguridad.** Las páginas incluyen también `X-Frame-Options: DENY` y `frame-ancestors 'none'` contra el clickjacking, `X-Content-Type-Options: nosniff`, una política de apertura entre orígenes, una política de referencias y una política de permisos que desactiva la cámara, el micrófono, la geolocalización y otras API parecidas. Los sitios con HTTPS reciben además HSTS.

## Cuentas

**Argon2id.** Las contraseñas se guardan con Argon2id, un algoritmo que exige mucha memoria y encarece cualquier intento de adivinarlas. Pedimos una longitud mínima y comprobamos que no esté en una lista de contraseñas comunes, en vez de exigir símbolos que nadie recuerda.

**Un segundo factor para el equipo.** Por defecto, todo el que tiene acceso al panel de administración debe configurar un segundo factor: códigos TOTP de cualquier aplicación de autenticación, con códigos de recuperación de un solo uso, o una passkey. Los administradores pueden limitar la obligación a administradores y editores, pero no pueden desactivarla. Las passkeys (WebAuthn) exigen siempre la verificación del usuario, como una huella o un PIN, y también sirven para entrar sin contraseña. Para añadir una passkey o quitar el último segundo factor hace falta haber iniciado sesión hace poco o escribir la contraseña.

**Límites de frecuencia.** Los intentos de inicio de sesión se limitan por cuenta y por dirección, y los fallos repetidos bloquean la cuenta durante un tiempo. El restablecimiento de contraseñas, los registros y los comentarios tienen sus propios límites.

**Sesiones.** La cookie de sesión es `HttpOnly`, `SameSite` y, con HTTPS, `Secure` con el prefijo `__Host-`. Las sesiones caducan tras un periodo de inactividad y se pueden cerrar desde el perfil.

**Registro de auditoría.** Se registran los inicios de sesión y los intentos fallidos, los bloqueos, los cambios de rol, los ajustes, las aprobaciones de plugins y los cambios de contenido, con el usuario, la hora y la dirección.

## Formularios y datos de entrada

**Protección CSRF, por partida doble.** Cada formulario que cambia algo lleva un token sincronizado con la sesión, y la protección entre orígenes de Go rechaza las peticiones que el navegador marca como procedentes de otro sitio.

**Saneado con bluemonday.** El Markdown se procesa con el HTML en bruto desactivado y el resultado pasa por una lista de permitidos de bluemonday. El HTML que devuelven los plugins pasa por una lista propia, que nunca deja pasar scripts, estilos, iframes, formularios ni manejadores de eventos. Las URL peligrosas, como `javascript:`, se eliminan.

**Spam en comentarios.** Los formularios de comentarios llevan un campo trampa oculto y una marca de tiempo firmada. Los bots que lo rellenan todo, o que envían más rápido de lo que escribe una persona, se descartan sin decirles nada.

## Archivos subidos

Las carpetas de subidas son una vía de entrada clásica. sap13ns trata cada archivo como hostil:

- el tipo se **detecta por el contenido**, nunca por el nombre ni por lo que dice el navegador;
- solo se aceptan tipos conocidos de imagen, audio, vídeo y documento, y **la extensión tiene que coincidir** con el tipo detectado;
- las imágenes JPEG y PNG se **vuelven a codificar**, lo que elimina metadatos ocultos como las coordenadas GPS;
- los archivos se sirven con `Content-Security-Policy: sandbox` y `nosniff`, y lo que no es multimedia se envía como descarga, así que un archivo subido nunca puede ejecutarse como una página de tu sitio.

## Plugins aislados

Los plugins se ejecutan como módulos de WebAssembly dentro de un sandbox sin acceso a archivos, red, variables de entorno ni base de datos. Cada capacidad adicional es un permiso que aprueba un administrador, ligado a los hashes SHA-256 del módulo y de sus recursos: si cambian los archivos o los permisos que piden, el plugin se detiene hasta que se revise de nuevo. Los paquetes subidos se analizan antes de instalarse y siempre se instalan desactivados. Las llamadas tienen límites de memoria y de tiempo, las peticiones salientes solo llegan a direcciones públicas de los dominios autorizados y el HTML devuelto se sanea. [Más sobre los plugins](/es/plugins).

## Aislamiento en el multisitio

En una red de sitios (`SAP13NS_MULTISITE=true`), cada sitio vive en su propio esquema de PostgreSQL, con sus propios usuarios, archivos subidos y plugins. Cada consulta a la base de datos trabaja sobre el esquema de un único sitio, y una consulta que no indica ningún esquema se rechaza, así que un filtro olvidado falla en lugar de leer las filas de otro sitio. Un plugin activado en un sitio no puede ver el contenido de otro, y quien edita un sitio no tiene ningún permiso en otro si no le han hecho miembro. Los administradores de la red que abren un sitio lo hacen con una cuenta de operador aparte, y eso queda en los registros de auditoría. Las passkeys de un sitio pertenecen a su propio host, así que no sirven en ningún otro sitio. Los detalles están en [SECURITY.md](https://github.com/vicktor/sap13ns/blob/main/docs/SECURITY.md#networks-of-sites) y [MULTISITE.md](https://github.com/vicktor/sap13ns/blob/main/docs/MULTISITE.md).

## Informar de una vulnerabilidad

Si encuentras un problema de seguridad, escríbenos a **victor@bauber.xyz**, o usa el botón **Report a vulnerability** de la pestaña Security del repositorio en GitHub, antes de hacerlo público. Cuéntanos qué has encontrado y cómo reproducirlo; te responderemos, te mantendremos al tanto mientras lo arreglamos y te daremos el crédito si quieres. La política completa está en [SECURITY.md](https://github.com/vicktor/sap13ns/blob/main/docs/SECURITY.md).
