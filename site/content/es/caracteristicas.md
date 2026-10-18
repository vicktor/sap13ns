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

### Editor Markdown con vista previa en directo

Escribes en Markdown, con tablas, notas al pie, listas de tareas y bloques de código, y ves el resultado al lado mientras tecleas. La vista previa usa el mismo motor que el sitio público: lo que ves es lo que se publica.

### Medios con variantes adaptables

Los archivos subidos se comprueban por su contenido real, no por su nombre. Las imágenes JPEG y PNG se vuelven a codificar, lo que elimina metadatos como las coordenadas GPS, y se generan variantes de 480, 1024 y 1920 píxeles de ancho. Los temas reciben un `srcset` listo para usar: los móviles descargan imágenes pequeñas y las pantallas grandes, imágenes nítidas.

### Contenido multilingüe con hreflang

Un sitio tiene un idioma principal en la raíz y tantos idiomas adicionales como quieras, cada uno con su prefijo, como `/es/`. Una entrada y sus traducciones están enlazadas entre sí, el selector de idioma lleva a la misma página en otro idioma y cada página anuncia sus alternativas con `hreflang` para que los buscadores muestren la versión adecuada.

### RSS, Atom y JSON Feed, además de WebSub

El sitio entero, cada categoría, cada etiqueta y cada autor tienen feeds en los tres formatos, con el texto completo o solo el extracto. Si configuras un hub de WebSub, los lectores compatibles se enteran de las entradas nuevas en segundos, sin tener que consultar una y otra vez.

### Comentarios con antispam

Los comentarios son opcionales y se pueden moderar siempre, solo la primera vez que alguien comenta o nunca. Los formularios llevan un campo trampa oculto y una marca de tiempo firmada, así que los bots que rellenan todos los campos o envían al instante se descartan sin hacer ruido. Además, hay un límite de comentarios por dirección.

### Redirecciones

Cuando mueves contenido, los enlaces antiguos deberían seguir funcionando. Las redirecciones se gestionan desde el panel, y al cambiar un slug puede quedar una redirección permanente para que nadie llegue a una página muerta.

## Aspecto

### Cinco temas, en claro y oscuro

sap13ns incluye Apple, Neumorphism, Glassmorphism, Bento Grid y Dashboard / SaaS. Todos tienen modo claro y oscuro, que sigue la preferencia del sistema del visitante, con un interruptor que recuerda su elección y sin parpadeos de color al cargar. [Ver los temas](/es/temas).

### Imágenes OG automáticas

Cada entrada recibe una imagen para redes sociales generada a partir de su título y del nombre del sitio, sin herramientas de diseño y sin IA. Los enlaces que se comparten en redes y en aplicaciones de mensajería se ven cuidados, no como un recuadro gris vacío.

## Ampliar

### Plugins en un sandbox de WebAssembly

Los plugins son módulos de WebAssembly. De entrada, un plugin no puede leer archivos, abrir conexiones de red ni ver la base de datos: recibe los datos que le pasa el núcleo y devuelve HTML, que se sanea antes de llegar a ninguna página. Todo lo demás, como un almacén privado, leer las entradas publicadas o llamar a una API externa concreta, es un permiso que el plugin declara y que un administrador aprueba.

La aprobación queda ligada al hash SHA-256 del módulo. Si alguien sustituye el archivo, el plugin deja de cargarse hasta que lo revises y lo apruebes de nuevo. [Cómo funcionan los plugins](/es/plugins).

## Equipos y cuentas

### Roles

Cinco roles cubren una redacción habitual: los administradores gestionan el sitio, los editores publican y moderan el trabajo de todos, los autores publican sus propias entradas, los colaboradores escriben borradores para revisión y los suscriptores pueden iniciar sesión para comentar.

### Verificación en dos pasos

Las cuentas pueden usar códigos TOTP de cualquier aplicación de autenticación, con códigos de recuperación de un solo uso para el día en que se pierde el móvil. Los administradores pueden hacerla obligatoria para administradores y editores. Las passkeys están previstas.

### Registro de auditoría

Los inicios de sesión, las publicaciones, los cambios de usuarios y roles, los ajustes y las aprobaciones de plugins quedan en un registro de auditoría con quién, qué, cuándo y desde qué dirección. Si algo no cuadra, puedes saber qué ha pasado.

## Mudarse

### Importador de WordPress

Trae un sitio de WordPress desde su archivo de exportación (WXR): entradas, páginas, categorías, etiquetas, comentarios y medios. Por el camino, el contenido se convierte a Markdown limpio.

## IA, con tus condiciones

### Un asistente que nunca publica por su cuenta

Desde el editor puedes pedir ayuda a un modelo de lenguaje: un extracto, el título y la descripción SEO, una traducción, una propuesta de imagen o una segunda opinión sobre un párrafo. Tú eliges el proveedor: Claude, cualquier API compatible con OpenAI (también modelos locales con Ollama o llama.cpp) o Gemini. No pasa nada si nadie pulsa un botón, y nada se publica sin que una persona lo revise. Un corrector local, además, señala las muletillas que hacen que un texto suene a máquina.

### llms.txt y llms-full.txt

Los sitios pueden publicar `/llms.txt` y `/llms-full.txt`, índices en texto plano de tu contenido para los modelos de lenguaje y las herramientas que los usan, generados a partir de lo que ya es público.

## Conocer a tu audiencia sin rastrearla

### Analítica sin cookies

La analítica integrada cuenta visitas, sitios de procedencia y contenido popular de forma agregada. Sin cookies, sin huellas digitales y sin scripts de terceros, así que no hace falta ningún aviso de consentimiento.

## En marcha

### Copias de seguridad

Un solo comando genera un archivo con un volcado de PostgreSQL y los archivos subidos, cifrado con una frase de paso si quieres. Ejecútalo con un temporizador de systemd y guarda unos cuantos días de historial. Los archivos se verifican antes de restaurar nada.

### Multisitio

Una sola cuenta puede gestionar muchos sitios. Cada sitio tiene su propio dominio, sus temas y plugins, sus archivos subidos y su propio esquema de PostgreSQL, de modo que el contenido y los plugins de un sitio no pueden llegar a los de otro. Inicias sesión una vez y pasas de un sitio a otro, con un rol distinto en cada uno si hace falta.

## De un vistazo

| Área | Qué incluye |
|---|---|
| Ejecución | Un binario de Go, PostgreSQL y nginx delante, sin Node.js en producción |
| Velocidad | Caché de páginas en memoria, búsqueda de texto completo en PostgreSQL, imágenes adaptables |
| Escritura | Markdown con vista previa, traducciones, feeds y comentarios |
| Diseño | Cinco temas, cada uno con modo claro y oscuro |
| Ampliación | Plugins de WebAssembly con aprobación por permiso ligada al hash del módulo |
| Cuentas | Cinco roles, 2FA TOTP con códigos de recuperación, registro de auditoría y passkeys previstas |
| Migración | Importador de WordPress y redirecciones |
| IA | Asistente opcional (Claude, compatible con OpenAI, local o Gemini) y llms.txt |
| Operación | Copias de seguridad, analítica sin cookies y multisitio |
