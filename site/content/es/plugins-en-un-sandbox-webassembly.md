---
type: post
key: wasm-plugins
title: Plugins en un sandbox de WebAssembly
slug: plugins-en-un-sandbox-webassembly
excerpt: Por qué los plugins de sap13ns empiezan sin acceso a nada, cómo se aprueban los permisos y por qué la aprobación queda ligada al archivo exacto que revisaste.
date: 2026-10-16
categories: [Seguridad]
tags: [plugins, webassembly, seguridad]
comments: closed
---
Si has cuidado de un sitio con WordPress durante unos años, probablemente conoces la historia. Un plugin que funcionó bien mucho tiempo cambia de manos, o publica una mala actualización, y de repente lee la tabla de opciones, crea cuentas de administrador o inyecta enlaces en todas las páginas. El plugin no hizo nada que la plataforma no permitiera. La plataforma, sencillamente, lo permitía todo.

Al diseñar los plugins de sap13ns partimos de la idea contraria: un plugin es código que no hemos escrito nosotros, y solo debería poder hacer aquello que alguien haya aceptado de forma explícita.

## Por qué WebAssembly

Necesitábamos ejecutar código de terceros dentro de nuestro proceso de Go sin darle los poderes del proceso. WebAssembly encaja muy bien. Un módulo de WebAssembly no tiene acceso a nada por defecto: ni archivos, ni red, ni variables de entorno, ni llamadas al sistema. Solo puede llamar a las funciones que el anfitrión decida ofrecerle.

Ejecutamos los módulos con [wazero](https://wazero.io), un entorno de WebAssembly escrito en Go puro, así que no interviene ninguna cadena de herramientas de C y el sandbox forma parte del mismo binario que todo lo demás.

Los plugins se pueden escribir en cualquier lenguaje que compile a WebAssembly con WASI. Ofrecemos un SDK para Go, porque es lo que usamos nosotros:

```bash
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o plugin.wasm .
```

## Qué ve un plugin

La conversación entre sap13ns y un plugin es JSON sin más. Cuando se dispara un hook, el anfitrión envía al plugin un mensaje con el nombre del hook, los ajustes del plugin, unos pocos datos del sitio y la información de ese hook: el HTML de una entrada, los atributos de un shortcode, el widget que se va a mostrar. El plugin responde con JSON, normalmente un fragmento de HTML.

Ese HTML lo sanea después el anfitrión. Un plugin no puede añadir un `<script>`, un estilo en línea, un iframe ni un `onclick` a tus páginas, aunque lo intente. La política de seguridad estricta de cada página los bloquearía de todos modos; nosotros los quitamos antes de que lleguen tan lejos.

## Permisos, uno a uno

Un plugin que solo transforma texto no necesita nada más. Todo lo demás es un permiso, declarado en su manifiesto:

```json
{
  "abi": 1,
  "id": "mastodon-share",
  "name": "Mastodon share counts",
  "version": "0.3.0",
  "hooks": ["widget", "event"],
  "events": ["post.published"],
  "permissions": ["kv", "posts:read", "http:mastodon.social"]
}
```

Solo hay tres tipos:

| Permiso | Qué permite |
|---|---|
| `kv` | Un pequeño almacén clave-valor privado que ningún otro plugin puede leer |
| `posts:read` | Leer entradas publicadas; los borradores y el contenido privado siguen siendo invisibles |
| `http:<dominio>` | Peticiones HTTPS a ese dominio, o `*.dominio` para sus subdominios |

El manifiesto de arriba es un ejemplo ilustrativo, no un plugin que distribuyamos. Sirve para ver la idea: un administrador ve exactamente con qué dominio quiere hablar el plugin, y puede decir que no.

Incluso un permiso `http:` aprobado solo llega a direcciones públicas. Se rechazan las peticiones a direcciones locales, rangos privados, direcciones de enlace local o servicios de metadatos de la nube, y la comprobación se hace sobre la dirección a la que realmente se conecta, así que un registro DNS que cambie entre la consulta y la conexión no sirve para saltársela.

## La aprobación va ligada a un hash

Los permisos por sí solos no bastan. Si apruebas hoy un plugin y mañana alguien sustituye su archivo, el código nuevo heredaría la aprobación antigua. Es justo la historia del «plugin de confianza que se tuerce» que queríamos descartar.

Por eso, cuando un administrador aprueba un plugin, sap13ns guarda el hash SHA-256 de su archivo `.wasm` junto con los permisos concedidos. En cada carga se comprueba el hash. Si no coincide, el plugin no se ejecuta y aparece en el panel como modificado y pendiente de revisión. Puedes calcular el hash tú mismo antes de subirlo:

```bash
sap13ns plugin hash ./mi-plugin
```

## Límites

Un sandbox que deja a un plugin funcionar para siempre sigue siendo un problema. Cada llamada tiene un plazo y un tope de memoria:

- 300 ms para renderizar y 15 segundos para atender un evento en segundo plano;
- 128 MiB de memoria por instancia y como mucho cuatro instancias por plugin;
- un tamaño máximo de módulo y tamaños máximos de entrada y salida;
- tras varios fallos seguidos, el plugin se pausa durante un minuto.

El resultado se guarda en caché con la página, así que ni siquiera un plugin lento se ejecuta en cada visita.

## A qué renuncias

Este modelo es más restrictivo de lo que acostumbran muchos autores de plugins. Un plugin de sap13ns no puede crear tablas a su antojo, ni reescribir el panel de administración, ni ejecutar órdenes de la shell. Algunas ideas simplemente no encajan, y esas tienen su sitio en el núcleo o en ninguna parte.

También hay un coste en comodidad: enviar y recibir JSON es más lento que llamar a una función en el mismo lenguaje. En la práctica la caché oculta casi todo, y creemos que ese sobrecoste es un precio razonable por saber que un widget no puede leer tu tabla de usuarios.[^1]

## Pruébalo

El repositorio incluye cuatro plugins oficiales en `plugins/`. Uno de ellos, `callouts`, convierte los avisos de Markdown en notas con estilo, añade dos shortcodes para notas y teclas y no necesita ningún permiso. Es un buen punto de partida. La [página de plugins](/es/plugins) tiene un resumen, y [PLUGINS.md](https://github.com/vicktor/sap13ns/blob/main/docs/PLUGINS.md), el contrato completo.

[^1]: El sandbox protege tu sitio de un plugin. No hace que lo que muestra el plugin sea correcto: un plugin con permiso `http:` puede seguir mostrando datos erróneos del servicio al que llama.
