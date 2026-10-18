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

Un plugin son dos archivos en una carpeta:

- **`plugin.wasm`**, un módulo de WebAssembly. Puedes escribirlo en Go con nuestro SDK o en cualquier lenguaje que compile a WebAssembly con WASI.
- **`plugin.json`**, un manifiesto que dice quién lo ha hecho, qué hooks quiere, qué shortcodes y widgets ofrece, qué ajustes tiene y qué permisos necesita.

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

Hay cinco hooks:

| Hook | Para qué sirve |
|---|---|
| `content.filter` | Transformar el HTML de una entrada o página antes de mostrarla |
| `shortcode` | Generar shortcodes como `[callout]` dentro de las entradas |
| `widget` | Generar los widgets que los editores colocan en las zonas del tema |
| `head.meta` | Añadir etiquetas `<meta>` a la cabecera de la página |
| `event` | Reaccionar en segundo plano a `post.published`, `post.updated`, `post.trashed`, `comment.created`, `comment.approved`, `user.registered` y `cron.hourly` |

El resultado se guarda en caché junto con la página, así que un plugin no se ejecuta en cada visita.

## Permisos

Sin permisos, un plugin solo puede calcular: recibir datos y devolver un resultado. Todo lo demás tiene que declararse en el manifiesto y aprobarlo un administrador:

| Permiso | Qué permite |
|---|---|
| `kv` | Un pequeño almacén clave-valor privado, visible solo para ese plugin |
| `posts:read` | Consultar entradas publicadas, nunca borradores ni contenido privado |
| `http:example.com` | Peticiones HTTPS solo a ese dominio; `http:*.example.com` permite sus subdominios |

Incluso con un permiso `http:`, un plugin solo llega a direcciones públicas. Las direcciones locales, las redes privadas, las direcciones de enlace local y los servicios de metadatos de la nube están bloqueados, y la dirección se comprueba en el momento de conectar, así que los trucos con DNS no sirven para saltárselo.

## La aprobación va ligada al módulo

Cuando apruebas un plugin, sap13ns guarda el hash SHA-256 de su archivo `.wasm` junto a los permisos concedidos. Si el archivo cambia, por una actualización, un despliegue comprometido o un compañero curioso, el plugin deja de cargarse y aparece pendiente de revisión. No se ejecuta nada nuevo hasta que lo vuelvas a aprobar.

Puedes comprobar el hash tú mismo antes de subirlo:

```bash
sap13ns plugin hash ./callout
```

## Límites

Cada llamada tiene límites estrictos, así que un plugin con errores no ralentiza nada durante mucho tiempo:

- 128 MiB de memoria por instancia y como mucho cuatro instancias simultáneas por plugin;
- 300 ms por cada llamada de renderizado y 15 segundos por cada evento en segundo plano;
- módulos de hasta 32 MiB, con tamaños máximos de entrada y salida;
- un plugin que falla una y otra vez se pausa un minuto antes de volver a intentarlo.

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

## Plugins de ejemplo

El repositorio incluye plugins de ejemplo en `plugins/examples`. El primero, **callout**, añade dos shortcodes: `callout` para notas, consejos y avisos, y `kbd` para teclas, ambos con los estilos de componentes compartidos para que encajen en cualquier tema. No necesita ningún permiso.

## Revisión

En un servidor propio, el revisor eres tú: lee el manifiesto, comprueba los permisos y aprueba. En el alojamiento de sap13ns.site, los plugins que suben los clientes los revisamos nosotros antes de que puedan activarse, y se aplica el mismo anclaje al hash: un plugin aprobado no se puede cambiar por otro archivo.

La referencia completa está en [PLUGINS.md](https://github.com/vicktor/sap13ns/blob/main/docs/PLUGINS.md).
