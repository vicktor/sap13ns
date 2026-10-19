---
type: page
key: themes
title: Temas
slug: temas
excerpt: Seis temas terminados, cada uno con modo claro y oscuro, y un contrato sencillo para crear el tuyo, sea un blog, una landing o una web de una sola página.
order: 2
menu: [primary, footer, landing]
menu_label: Temas
comments: closed
---
Un tema decide el aspecto de un sitio, nunca su seguridad. Todos los temas de sap13ns generan el mismo marcado accesible para menús, entradas, comentarios y búsqueda, siguen la misma política de seguridad estricta y traen de serie un modo claro y otro oscuro.

Las capturas están en camino. Mientras tanto, las vistas previas de esta página dibujan cada tema en miniatura solo con CSS, en su modo claro y en su modo oscuro.

## Los seis temas

### Apple

La elegancia tranquila de una página de producto: barra de navegación esmerilada, titulares enormes, rejillas con aire y esquinas muy redondeadas. Ideal para un estudio, un producto o alguien que escribe y quiere que mande el texto. Hay seis colores de acento para elegir.

### Neumorphism

Interfaz suave. Tarjetas, botones y campos parecen salir de una misma superficie o estar tallados en ella. Las sombras están ajustadas para que el texto mantenga un contraste accesible y el foco se vea con claridad en los dos modos, que es justo donde fallan casi todos los diseños de este estilo. Puedes elegir el acento y la profundidad de las sombras.

### Glassmorphism

Paneles translúcidos y esmerilados que flotan sobre una aurora de color que se mueve despacio: pasteles luminosos de día y neón sobre índigo profundo de noche. Eliges la paleta y lo esmerilado del cristal, y el movimiento se puede desactivar. Encaja en portfolios y páginas de lanzamiento que buscan ambiente sin renunciar a un texto legible.

### Bento Grid

Losetas redondeadas de distintos tamaños, como una caja bento: una portada asimétrica con la última historia, estadísticas del sitio, categorías, etiquetas y feeds, y páginas de lectura hechas con las mismas piezas. Perfecto para revistas, blogs de enlaces y quien publica en varios formatos.

### Dashboard / SaaS

La precisión de un panel de producto: navegación lateral fija, barra superior con migas de pan y búsqueda, tarjetas de indicadores y listados de entradas con aire de tabla, con bordes nítidos, un único color de acento y un modo compacto. Va bien para documentación, registros de cambios, bases de conocimiento y productos de software.

### Onepage

Una web de una sola página con voz editorial: papel cálido y tinta, titulares grandes con serifa (o sin ella) y secciones numeradas para características, cifras, testimonios, precios y preguntas, rematadas por una llamada a la acción contundente. El menú fijo lleva a cada sección y marca la que estás leyendo. El blog vive en `/blog`, y cada sección se puede desactivar. Cinco colores de acento.

## Claro y oscuro, sin parpadeos

Cada tema define sus colores para los dos modos. Un script mínimo que viene con el núcleo aplica la elección guardada del visitante, o la preferencia de su sistema, antes del primer pintado, así que la página nunca destella en blanco de noche. El interruptor recuerda la elección para la próxima vez.

## Opciones sin código

Los temas declaran sus opciones en un manifiesto y el panel construye el formulario: colores de acento, si se muestra la presentación grande en la portada, imágenes destacadas en las tarjetas, barra lateral, etcétera. Los menús y las zonas de widgets se declaran igual y se rellenan desde la sección **Appearance** (Apariencia) del panel.

Dos herramientas van más allá sin editar el tema. El **personalizador** cambia sus colores, sus fuentes, el redondeo de las esquinas, la densidad, la cabecera y el pie, con una vista previa en directo en modo claro y oscuro y avisos de contraste. El **constructor de páginas** monta la cabecera, el pie y cada tipo de página con secciones, columnas y bloques, también bloques con tu propio HTML, y edita el CSS personalizado del tema; un tipo de página que no hayas montado conserva la plantilla del tema. Los dos guardan los cambios como borrador hasta que los publicas, conservan las diez últimas versiones publicadas y exportan e importan su trabajo en un solo archivo.

## Landings y webs de una sola página

Un sitio no tiene por qué parecer un blog. En **Ajustes → Lectura**, la portada puede ser la del propio tema: una landing o una web de una sola página, con las entradas en `/blog`. Cualquier tema puede tenerla, montada en el constructor de páginas con siete bloques de landing: características, cifras, testimonios, precios que escribes tú, preguntas y respuestas, logotipos y una llamada a la acción. Empieza con la plantilla **Landing** o **Web de una sola página** y cambia los textos, en todos los idiomas del sitio.

Las secciones pueden tener un ancla como `#precios`. Un enlace de menú a ella desplaza la página hasta allí, con suavidad salvo que el visitante haya pedido menos movimiento. Desde cualquier otra página lleva de vuelta a esa sección de la portada, y mientras te desplazas, el menú marca la sección que tienes delante.

## Crea el tuyo

Un tema es una carpeta con un contrato pequeño y documentado:

```text
mi-tema/
├── theme.json          nombre, menús, zonas de widgets, opciones, colores de muestra
├── templates/
│   ├── layout.html     el marco de la página
│   ├── front.html      una landing o una web de una sola página
│   ├── home.html       últimas entradas (esta, front.html o las dos)
│   ├── single.html     una entrada (opcional)
│   ├── page.html       una página (opcional)
│   ├── archive.html    categorías, etiquetas, autores, fechas (opcional)
│   ├── search.html     resultados de búsqueda (opcional)
│   ├── 404.html        página no encontrada
│   ├── ask.html        el asistente del sitio sin JavaScript (opcional)
│   └── partials/       lo que quieras reutilizar
├── i18n/es.json        traducciones de los textos propios del tema
├── src/theme.css       el código fuente de la hoja de estilos
└── static/theme.css    la hoja de estilos compilada
```

Un tema que deja fuera las páginas del blog sigue teniendo blog: las entradas, páginas, archivos y resultados de búsqueda que no dibuja usan plantillas compartidas sencillas dentro de su propio marco y con sus colores.

Para instalar un tema, sube su `.zip` en **Apariencia → Temas**, donde se revisa antes de instalarlo, o copia su carpeta en el directorio de temas (`SAP13NS_THEMES_DIR`, por defecto `themes` dentro del directorio de datos) y reinicia sap13ns: no hay que recompilar nada.

Las plantillas usan `html/template` de Go, que escapa la salida según el contexto, y un conjunto de parciales compartidos para lo que necesita cualquier sitio: la cabecera del documento con enlaces canónicos y `hreflang`, los menús, el selector de idioma, el interruptor de claro y oscuro, los comentarios, la paginación y el formulario de búsqueda. Un tema puede sustituir cualquiera de ellos.

Los estilos se escriben con Tailwind CSS v4 y se compilan en un único archivo minificado **mientras desarrollas**. El resultado se guarda junto al tema, así que los servidores de producción nunca ejecutan Node.js.

Los textos de las plantillas se escriben en el idioma principal y pasan por la función de traducción. Un tema puede incluir sus propias traducciones en `i18n/<idioma>.json`, un mapa sencillo de clave a texto que se consulta antes que el catálogo del núcleo.

### Las reglas que cumple todo tema

- **Nada de scripts ni estilos en línea.** La política de seguridad de contenidos solo permite scripts y hojas de estilo servidos por el propio sitio. Sin bloques `<script>`, sin atributos `style`, sin `onclick`.
- **Nada de JavaScript propio.** El script del núcleo ya se encarga del interruptor de color, el menú móvil, el selector de idioma y las respuestas a comentarios.
- **Nada de fuentes externas ni CDN.** Las fuentes son las del sistema o archivos WOFF2 que sirve el propio sitio, así que siguen siendo rápidas y privadas.
- **Accesible de serie.** Regiones semánticas, enlace para saltar al contenido, foco visible, movimiento reducido y contraste legible en los dos modos.

El contrato completo está en [THEMES.md](https://github.com/vicktor/sap13ns/blob/main/docs/THEMES.md).
