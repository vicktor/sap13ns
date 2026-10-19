---
type: page
key: plugins
title: Plugins
slug: plugins
excerpt: Amplía sap13ns con plugins de WebAssembly que solo pueden hacer lo que autorices, y solo en la versión que revisaste.
order: 3
menu: [primary, footer, landing]
menu_label: Plugins
comments: closed
---
Los plugins son el punto débil de casi todos los gestores de contenidos. Lo normal es que un plugin se ejecute con los mismos permisos que la aplicación: puede leer la configuración, consultar cualquier tabla, escribir archivos y hablar con cualquier servidor. Basta una actualización descuidada o malintencionada para que el sitio entero pase a ser de otro.

sap13ns parte de la idea contraria. Un plugin empieza sin acceso a nada y gana cada capacidad mediante un permiso explícito que se puede revisar.

## Qué es un plugin

Un plugin es un paquete `.zip` con dos archivos que importan:

- **`plugin.wasm`**, un módulo de WebAssembly. Puedes escribirlo en Go con nuestro SDK o en cualquier lenguaje que compile a WebAssembly con WASI.
- **`plugin.json`**, un manifiesto que dice quién lo ha hecho, qué hooks quiere, qué shortcodes, widgets y formularios ofrece, qué ajustes tiene y qué permisos necesita.

Un paquete también puede llevar recursos en `assets/`, como hojas de estilo, scripts, imágenes y fuentes, además de un README, una licencia y un registro de cambios. No se acepta nada más.

```json
{
  "abi": 1,
  "id": "callout",
  "name": "Callouts",
  "version": "1.0.0",
  "hooks": ["shortcode"],
  "shortcodes": ["callout", "kbd"],
  "permissions": []
}
```

## La ABI, en palabras sencillas

El contrato entre sap13ns y un plugin se llama ABI 1. En la práctica funciona así:

1. Cuando ocurre algo a lo que el plugin está suscrito, sap13ns le envía un mensaje JSON pequeño: qué hook es, los ajustes del plugin, datos básicos del sitio y la información de ese hook.
2. El plugin hace su trabajo y responde con JSON, normalmente un fragmento de HTML.
3. sap13ns sanea ese HTML antes de que se acerque a una página. Se eliminan scripts, estilos, iframes, formularios y manejadores de eventos.

Hay siete hooks:

| Hook | Para qué sirve |
|---|---|
| `content.filter` | Transformar el HTML de una entrada o página antes de mostrarla |
| `shortcode` | Generar shortcodes como `[callout]` dentro de las entradas |
| `widget` | Generar los widgets que los editores colocan en las zonas del tema |
| `head.meta` | Añadir etiquetas `<meta>` y JSON-LD de Schema.org a la cabecera de la página |
| `comment.check` | Retener o rechazar un comentario nuevo antes de guardarlo |
| `form.submit` | Atender un formulario que declara el plugin; el anfitrión genera y protege el formulario |
| `event` | Reaccionar en segundo plano a `post.published`, `post.updated`, `post.trashed`, `comment.created`, `comment.approved`, `user.registered` y `cron.hourly` |

El resultado de los cuatro primeros se guarda en caché junto con la página, así que un plugin no se ejecuta en cada visita.

## Permisos

Sin permisos, un plugin solo puede calcular: recibir datos y devolver un resultado. Todo lo demás tiene que declararse en el manifiesto y aprobarlo un administrador:

| Permiso | Qué permite |
|---|---|
| `kv` | Un pequeño almacén clave-valor privado, visible solo para ese plugin |
| `posts:read` | Consultar entradas publicadas, nunca borradores ni contenido privado |
| `http:example.com` | Peticiones HTTPS solo a ese dominio; `http:*.example.com` permite sus subdominios |
| `http:*` | Peticiones HTTPS a cualquier dominio público (riesgo alto) |
| `mail` | Correos al administrador del sitio o a direcciones guardadas en los ajustes del plugin |
| `comments:pii` | El correo, la dirección IP y el navegador de quien comenta, al revisar un comentario (riesgo alto) |
| `assets:js` | Enlazar el script propio del plugin en las páginas públicas (riesgo alto) |

Un plugin también puede pedir que se añadan orígenes `https://` concretos a `frame-src` o `img-src` de la página, por ejemplo para un reproductor de vídeo; eso también cuenta como permiso de riesgo alto. Los permisos de riesgo alto exigen una confirmación expresa al activar el plugin. La red y el correo nunca están disponibles mientras se genera una página: las peticiones solo funcionan en los eventos, la revisión de comentarios y el envío de formularios, y el correo, solo en los eventos y los formularios.

Incluso con un permiso `http:`, un plugin solo llega a direcciones públicas. Las direcciones locales, las redes privadas, las direcciones de enlace local y los servicios de metadatos de la nube están bloqueados, y la dirección se comprueba en el momento de conectar, así que los trucos con DNS no sirven para saltárselo.

## La aprobación va ligada al módulo

Antes de instalar un paquete, se descomprime con límites estrictos y se analiza: el manifiesto, las importaciones y exportaciones del módulo, las cadenas que contiene, sus recursos y una prueba en seco de cada hook con datos de ejemplo. Los hallazgos críticos lo bloquean, y los altos exigen una confirmación. Un plugin siempre se instala desactivado.

Cuando activas un plugin, sap13ns guarda los hashes SHA-256 de su módulo `.wasm` y de sus recursos junto a los permisos concedidos. Si cualquiera de ellos cambia, por una actualización, un despliegue comprometido o un compañero curioso, el plugin se detiene y aparece pendiente de revisión. No se ejecuta nada nuevo hasta que lo vuelvas a aprobar.

Puedes comprobar los hashes tú mismo antes de subirlo:

```bash
sap13ns plugin hash ./callout
```

## Límites

Cada llamada tiene límites estrictos, así que un plugin con errores no ralentiza nada durante mucho tiempo:

- 128 MiB de memoria por instancia y como mucho cuatro instancias simultáneas por plugin;
- 300 ms por cada llamada de renderizado y 15 segundos por cada evento en segundo plano;
- módulos de hasta 32 MiB, con tamaños máximos de entrada y salida;
- un plugin que falla cinco veces seguidas se pausa un minuto antes de volver a intentarlo.

## Escribir un plugin en Go

El paquete `pluginsdk` se encarga de la fontanería. Registras una función, miras qué hook ha llegado y devuelves HTML:

```go
func init() {
	pluginsdk.Handle(func(req *pluginsdk.Request) (any, error) {
		if req.Hook == pluginsdk.HookShortcode {
			var sc pluginsdk.Shortcode
			req.Decode(&sc)
			return pluginsdk.HTML("<mark>" + pluginsdk.Escape(sc.Inner) + "</mark>"), nil
		}
		return nil, nil
	})
}

func main() {}
```

Compílalo con las herramientas estándar de Go:

```bash
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o plugin.wasm .
```

`sap13ns plugin new mi-plugin` empieza un proyecto a partir del esqueleto, con un ejemplo y una prueba para cada hook, y `sap13ns plugin pack` lo compila, lo analiza y lo empaqueta en el `.zip` que subes.

## Plugins de ejemplo

El repositorio incluye diez plugins oficiales en `plugins/`, cada uno un módulo de Go con sus pruebas y un README que explica cada permiso que pide:

- **callouts**: avisos de Markdown al estilo de GitHub y los shortcodes `callout` y `kbd`.
- **toc**: un índice automático.
- **external-links**: parámetros UTM y atributos para los enlaces a otros sitios.
- **announcement**: un anuncio programado como widget.
- **embeds**: los enlaces de YouTube y Vimeo se convierten en reproductores que se cargan al hacer clic. No se pide nada al sitio del vídeo hasta que el visitante pulsa reproducir, y entonces el reproductor llega de youtube-nocookie.com o de Vimeo con Do Not Track.
- **schema**: datos JSON-LD de Schema.org para entradas, páginas, migas de pan, el buscador del sitio y el editor.
- **related**: entradas relacionadas por etiquetas y categorías comunes, al final de cada entrada y como widget.
- **contact**: un formulario de contacto con consentimiento de privacidad, que se envía al administrador con el visitante como Reply-To.
- **antispam**: comprobaciones locales de los comentarios nuevos (enlaces, una lista de bloqueo, alfabetos mezclados, mayúsculas, texto repetido), con una consulta opcional a StopForumSpam desactivada por defecto.
- **social**: publica los artículos nuevos en Bluesky y Mastodon, en el idioma de la entrada y nunca dos veces.

Usan los estilos de componentes compartidos para que encajen en cualquier tema, claro u oscuro.

## Revisión

En un servidor propio, el revisor eres tú: lee el manifiesto, comprueba los permisos y aprueba. En una red de sitios, además, el operador revisa cada plugin que sube un sitio antes de que el sitio pueda activarlo, y los sitios pueden instalar los plugins que el operador ofrece en el catálogo de la red. En el alojamiento de sap13ns.site, ese operador somos nosotros: los plugins que suben los clientes los revisamos antes de que puedan activarse, y se aplica el mismo anclaje al hash: un plugin aprobado no se puede cambiar por otro archivo.

La referencia completa está en [PLUGINS.md](https://github.com/vicktor/sap13ns/blob/main/docs/PLUGINS.md).
