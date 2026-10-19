---
type: page
key: features
title: Características
slug: caracteristicas
excerpt: Todo lo que hace sap13ns, desde el binario único y el sandbox de plugins hasta los feeds, las traducciones, las copias de seguridad y el multisitio.
order: 1
menu: [primary, footer, landing]
menu_label: Características
comments: closed
---
sap13ns es un gestor de contenidos al estilo de WordPress: entradas, páginas, categorías, menús, widgets, temas y plugins. La diferencia está en cómo está construido. Es un único programa en Go, guarda los datos en PostgreSQL, funciona detrás de nginx y da por hecho, desde la primera línea de código, que internet es un sitio hostil.

En esta página repasamos lo que incluye. Cada apartado es breve; los detalles están en la [documentación de GitHub](https://github.com/vicktor/sap13ns).

## Pequeño a propósito

### Un único binario de Go

El servidor, el panel de administración y todas las plantillas, hojas de estilo, iconos y traducciones se compilan en un solo ejecutable. Instalar es copiar un archivo; actualizar, sustituirlo y reiniciar el servicio. En el servidor no hay `node_modules`, ni PHP, ni una carpeta de plugins llena de código que nunca has leído.

Las migraciones de la base de datos también van dentro del binario y se aplican al arrancar, así que cada versión nueva trae consigo sus cambios de esquema.

### Búsqueda de texto completo con PostgreSQL

La búsqueda usa el propio PostgreSQL: cada entrada tiene un `tsvector` ponderado (primero el título, luego el extracto y después el cuerpo) con un índice GIN. Los visitantes pueden buscar como en un buscador web, con frases entre comillas y `-exclusiones`. Sin servicios externos y sin otro proceso que vigilar.

### Caché de páginas en memoria

Las páginas públicas se generan una vez y después se sirven desde memoria. Cualquier cambio en el contenido público (publicar, editar, un comentario nuevo, un cambio de menú) vacía la caché, de modo que nadie ve páginas obsoletas y la base de datos queda libre para las consultas que importan.

## Escribir y publicar

### Edición visual y en Markdown, con vista previa

Escribes en un editor visual o directamente en Markdown, con tablas, notas al pie, listas de tareas y bloques de código. Los dos modos editan el mismo texto Markdown, y al pegar desde Word, Google Docs o una página web se conservan los títulos, las listas, las tablas y los enlaces, y se descarta el formato sobrante. El modo de vista previa usa el mismo motor que el sitio público: lo que ves es lo que se publica. Los borradores se guardan solos, y un bloqueo de edición evita que dos personas se pisen el trabajo.

### Medios con variantes adaptables

Los archivos subidos se comprueban por su contenido real, no por su nombre. Las imágenes JPEG y PNG se vuelven a codificar, lo que elimina metadatos como las coordenadas GPS, y se generan variantes de 480, 1024 y 1920 píxeles de ancho. Los temas reciben un `srcset` listo para usar: los móviles descargan imágenes pequeñas y las pantallas grandes, imágenes nítidas.

### Contenido multilingüe con hreflang

Un sitio tiene un idioma principal en la raíz y tantos idiomas adicionales como quieras, cada uno con su prefijo, como `/es/`. Una entrada y sus traducciones están enlazadas entre sí, el selector de idioma lleva a la misma página en otro idioma y cada página anuncia sus alternativas con `hreflang` para que los buscadores muestren la versión adecuada.

### RSS, Atom y JSON Feed, además de WebSub

El sitio entero, cada categoría, cada etiqueta y cada autor tienen feeds en los tres formatos, con el texto completo o solo el extracto. Si configuras un hub de WebSub, los lectores compatibles se enteran de las entradas nuevas en segundos, sin tener que consultar una y otra vez.

### Comentarios con antispam

Los comentarios son opcionales y se pueden moderar siempre, solo la primera vez que alguien comenta o nunca. Los formularios llevan un campo trampa oculto y una marca de tiempo firmada, así que los bots que rellenan todos los campos o envían al instante se descartan sin hacer ruido. Además, hay un límite de comentarios por dirección.

### Redirecciones

Cuando mueves contenido, los enlaces antiguos deberían seguir funcionando. Las redirecciones se gestionan desde el panel, y cuando una entrada publicada cambia de slug se crea sola una redirección permanente para que nadie llegue a una página muerta.

## Aspecto

### Seis temas, en claro y oscuro

sap13ns incluye Apple, Neumorphism, Glassmorphism, Bento Grid, Dashboard / SaaS y Onepage. Todos tienen modo claro y oscuro, que sigue la preferencia del sistema del visitante, con un interruptor que recuerda su elección y sin parpadeos de color al cargar. [Ver los temas](/es/temas).

### Landings y webs de una sola página

La portada no tiene por qué listar entradas. Puede ser una landing o una web de una sola página, con el blog en `/blog`: la del propio tema, o una que montas con características, cifras, testimonios, precios, preguntas y respuestas, logotipos y una llamada a la acción, a partir de una página ya hecha. Los enlaces del menú llevan a cada sección y marcan la que está a la vista.

### Personalizador y constructor de páginas

El personalizador cambia los colores, las fuentes, el redondeo de las esquinas, la densidad, la cabecera y el pie de un tema sin tocar sus archivos, con una vista previa en directo en claro y en oscuro. El constructor de páginas monta la cabecera, el pie y cada tipo de página con secciones, columnas y bloques. Los dos guardan tus cambios como borrador hasta que los publicas y conservan las diez últimas versiones publicadas.

### Imágenes OG automáticas

Las entradas y páginas sin imagen destacada reciben una tarjeta para redes sociales generada a partir de su título y del nombre del sitio, sin herramientas de diseño y sin IA. Los enlaces que se comparten en redes y en aplicaciones de mensajería se ven cuidados, no como un recuadro gris vacío.

## Ampliar

### Plugins en un sandbox de WebAssembly

Los plugins son módulos de WebAssembly. De entrada, un plugin no puede leer archivos, abrir conexiones de red ni ver la base de datos: recibe los datos que le pasa el núcleo y devuelve HTML, que se sanea antes de llegar a ninguna página. Todo lo demás, como un almacén privado, leer las entradas publicadas, llamar a una API externa concreta o enviar un correo al administrador, es un permiso que el plugin declara y que un administrador aprueba.

Los plugins se suben como paquetes `.zip`, se analizan antes de instalarse y se instalan desactivados. La aprobación queda ligada a los hashes SHA-256 del módulo y de sus recursos, y a los permisos concedidos. Si cualquiera de ellos cambia, el plugin se detiene hasta que lo revises y lo apruebes de nuevo. [Cómo funcionan los plugins](/es/plugins).

## Equipos y cuentas

### Roles

Cinco roles cubren una redacción habitual: los administradores gestionan el sitio, los editores publican y moderan el trabajo de todos, los autores publican sus propias entradas, los colaboradores escriben borradores para revisión y los suscriptores pueden iniciar sesión para comentar.

### Verificación en dos pasos y passkeys

El segundo factor es obligatorio por defecto para el equipo: códigos TOTP de cualquier aplicación de autenticación, con códigos de recuperación de un solo uso para el día en que se pierde el móvil, o una passkey. Los administradores pueden limitar la obligación a administradores y editores, pero no pueden desactivarla. Las passkeys (WebAuthn) también sirven para entrar sin contraseña, siempre con verificación del usuario.

### Registro de auditoría

Los inicios de sesión, las publicaciones, los cambios de usuarios y roles, los ajustes y las aprobaciones de plugins quedan en un registro de auditoría con quién, qué, cuándo y desde qué dirección. Si algo no cuadra, puedes saber qué ha pasado.

## Mudarse

### Importador de WordPress

Trae un sitio de WordPress desde su archivo de exportación (WXR): autores, entradas, páginas, categorías, etiquetas, comentarios y medios, con redirecciones desde las URL antiguas. Por el camino, el contenido se convierte a Markdown limpio, y la importación se hace en segundo plano, con un modo de prueba y un informe.

## IA, con tus condiciones

### Un asistente que nunca publica por su cuenta

Desde el editor puedes pedir ayuda a un modelo de lenguaje: un extracto, el título y la descripción SEO, una traducción, una propuesta de imagen, una imagen de cabecera si el proveedor sabe dibujarla o una segunda opinión sobre un párrafo. Tú eliges el proveedor: Claude, cualquier API compatible con OpenAI (también modelos locales con LM Studio, Ollama o llama.cpp) o Gemini. No pasa nada si nadie pulsa un botón, y nada se publica sin que una persona lo revise. Un corrector local, además, señala las muletillas que hacen que un texto suene a máquina.

### llms.txt y llms-full.txt

Los sitios pueden publicar `/llms.txt` y `/llms-full.txt`, índices en texto plano de tu contenido para los modelos de lenguaje y las herramientas que los usan, generados a partir de lo que ya es público. Cada entrada y cada página tienen además una versión limpia en Markdown en su misma dirección terminada en `.md`. Una política de acceso para la IA (abierto, sin entrenamiento o bloqueado) decide qué dice `robots.txt` a los rastreadores de IA y si estas direcciones responden.

### Un asistente del sitio para los visitantes

Un chatbot opcional responde a las preguntas de los visitantes a partir de tus propias entradas y páginas, y las cita, con una página en `/ask` que funciona sin JavaScript. Busca con la búsqueda de texto completo de PostgreSQL y, si la extensión está instalada, también con pgvector. Los borradores y las entradas programadas o privadas nunca se indexan, y cualquier entrada se puede dejar fuera. Un punto de acceso MCP de solo lectura permite que agentes de IA externos busquen en el mismo índice.

## Conocer a tu audiencia sin rastrearla

### Analítica sin cookies

La analítica integrada cuenta visitas, sitios de procedencia y contenido popular de forma agregada. Sin cookies, sin huellas digitales y sin scripts de terceros, así que no hace falta ningún aviso de consentimiento.

## En marcha

### Copias de seguridad

Un solo comando genera un archivo con un volcado de PostgreSQL y los archivos subidos, cifrado con AES-256-GCM si le das una frase de paso. El temporizador de systemd que viene con sap13ns lo ejecuta cada noche y borra las copias antiguas, conservando copias diarias, semanales y mensuales. Los archivos se verifican antes de restaurar nada.

### Actualizaciones firmadas

Los administradores ven un aviso en el panel cuando sale una versión nueva. `sap13ns update` la descarga, comprueba sus sumas de verificación firmadas con Ed25519 y sustituye el binario de forma atómica, conservando el anterior por si hay que volver atrás.

### Redes de sitios

El multisitio es opcional. Con `SAP13NS_MULTISITE=true`, un solo proceso sirve una red: un portal donde cada persona tiene una cuenta y tantos sitios como quieras. Cada sitio tiene su propio esquema de PostgreSQL, sus usuarios, sus ajustes, sus archivos subidos y sus plugins, y elige su tema entre los de la red, de modo que el contenido y los plugins de un sitio no pueden llegar a los de otro. Cada persona entra en todos los sitios a los que pertenece a través del portal, con un rol distinto en cada uno, y toda cuenta tiene un segundo factor. Los administradores de la red solo abren un sitio al que no pertenecen mientras su propietario permite el acceso de soporte, o con un acceso de emergencia auditado por una orden judicial o de una autoridad, o por un incidente de seguridad grave. Vienen incluidos los planes con límites de almacenamiento y de dominios, el cobro con Stripe y los dominios propios, verificados por DNS y servidos con TLS.

Todavía no están disponibles las copias de seguridad por sitio, la conversión de una instalación de un solo sitio en un sitio de una red ni la subida de temas. [MULTISITE.md](https://github.com/vicktor/sap13ns/blob/main/docs/MULTISITE.md) explica cómo funciona una red.

## De un vistazo

| Área | Qué incluye |
|---|---|
| Ejecución | Un binario de Go, PostgreSQL y nginx delante, sin Node.js en producción |
| Velocidad | Caché de páginas en memoria, búsqueda de texto completo en PostgreSQL, imágenes adaptables |
| Escritura | Editor visual y Markdown con vista previa, traducciones, feeds y comentarios |
| Diseño | Seis temas, cada uno con modo claro y oscuro; portadas de landing y de una sola página; personalizador y constructor de páginas |
| Ampliación | Plugins de WebAssembly con aprobación por permiso ligada a los hashes del módulo y de sus recursos |
| Cuentas | Cinco roles, segundo factor obligatorio (TOTP con códigos de recuperación, o una passkey), acceso sin contraseña con passkeys y registro de auditoría |
| Migración | Importador de WordPress y redirecciones |
| IA | Asistente de escritura opcional (Claude, compatible con OpenAI, local o Gemini), asistente del sitio para los visitantes y llms.txt |
| Operación | Copias de seguridad cifradas, actualizaciones firmadas, analítica sin cookies y redes de sitios opcionales |
