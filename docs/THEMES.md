# Themes

This is the contract between sap13ns and its themes: the folder layout, the manifest, the templates and the data they receive, the CSS pipeline and the design tokens, and the rules every theme follows. It also covers what operators can change without touching a theme: the customiser and the page builder.

- **Writing a theme?** Copy a built-in theme from `ui/themes/` and read [Folder layout](#folder-layout), [Templates](#templates) and [CSS and design tokens](#css-and-design-tokens).
- **A landing page or a one-page site?** A theme does not have to be a blog: read [Front pages and one-page sites](#front-pages-and-one-page-sites), and start from `ui/themes/onepage`.
- **Running a site?** Read [Installing a theme](#installing-a-theme), [Uploading a theme](#uploading-a-theme), [The customiser](#the-customiser) and [The page builder](#the-page-builder).

## Contents

1. [Folder layout](#folder-layout)
2. [Installing a theme](#installing-a-theme)
3. [Uploading a theme](#uploading-a-theme)
4. [Editing a theme's code](#editing-a-themes-code)
5. [theme.json](#themejson)
6. [Templates](#templates)
7. [Front pages and one-page sites](#front-pages-and-one-page-sites)
8. [Shared templates](#shared-templates)
9. [Template data](#template-data)
10. [Translations](#translations)
11. [CSS and design tokens](#css-and-design-tokens)
12. [Light and dark](#light-and-dark)
13. [Videos and images in posts](#videos-and-images-in-posts)
14. [Cookie consent](#cookie-consent)
15. [Content Security Policy](#content-security-policy)
16. [Accessibility](#accessibility)
17. [The customiser](#the-customiser)
18. [The page builder](#the-page-builder)
19. [Animations](#animations)
20. [Checklist](#checklist)

## Folder layout

A theme is a folder named after its slug. The slug is 1 to 40 lower-case letters, digits and hyphens, and it starts with a letter or a digit.

```text
my-theme/
├── theme.json            manifest: name, menus, widget areas, options, colours
├── templates/
│   ├── layout.html       the page frame (required)
│   ├── front.html        the front page as a landing or a one-page site
│   ├── home.html         latest posts (front.html, home.html or both)
│   ├── single.html       one post
│   ├── page.html         one page
│   ├── archive.html      categories, tags, authors and dates
│   ├── search.html       search results
│   ├── 404.html          not found (required in an upload)
│   ├── ask.html          the site assistant without JavaScript
│   └── partials/*.html   templates shared by the theme's pages
├── i18n/<lang>.json      translations of the theme's own strings
├── src/theme.css         the stylesheet source (Tailwind CSS v4)
└── static/               served at /themes/<slug>/static/…
    └── theme.css         the compiled stylesheet (required)
```

- **At runtime the server reads** `theme.json`, `templates/`, `i18n/` and `static/`. `src/` is only needed to build the stylesheet.
- **Static files** are served from `static/` only, with a content hash in the URL. They are cached for a year when the hash matches.

## Installing a theme

Themes come from these places, in this order:

| Source | Where | Notes |
|---|---|---|
| Built in | `ui/themes/` | embedded in the binary |
| Network | the network's `SAP13NS_THEMES_DIR` | on a site of a network only: the themes the operator offers every site |
| Extra | `SAP13NS_THEMES_DIR` (default `$SAP13NS_DATA_DIR/themes`); on a network, `$SAP13NS_DATA_DIR/sites/<id>/themes` | one folder per theme, no recompiling; [uploads](#uploading-a-theme) go here too |

- **Adding a theme.** Upload it in **Appearance → Themes** or with `sap13ns theme install` ([Uploading a theme](#uploading-a-theme)). Or copy its folder into the themes directory and open or reload **Appearance → Themes**, which re-reads the directory; the server also reads it when it starts. In development (`SAP13NS_DEV=true`) themes are re-read on every request.
- **Overriding.** A theme copied by hand into the extra directory with the same slug as a built-in one replaces it. An uploaded theme never does: uploads refuse the slugs of built-in and network themes, and an uploaded theme whose slug a built-in or network theme takes later is not loaded.
- **Failures.** A theme whose manifest or templates do not parse is skipped and logged ("theme failed to load"), and the other themes still load. **Appearance → Themes** lists the themes of the site's directory that did not load, with a button to delete them. The server refuses to start only when no theme loads at all.
- **Fallback.** If the active theme is missing, visitors get `apple`, then `dashboard`, then any theme that loaded.
- **Preview and activate.** **Appearance → Themes** lists every theme. Users with the appearance capability can add `?preview_theme=<slug>` to any public URL to see another theme before activating it. Pagination, internal links and the search form keep the preview. Add `&scheme=dark` or `&scheme=light` to force a colour scheme for that view.

## Uploading a theme

An administrator can install a theme from a `.zip` file, without access to the server. The checks below run on every upload. A theme that fails any of them is refused, nothing is installed, and the page lists every problem found (up to 50), with its file and line, so the author can fix them in one go. No operator reviews uploaded themes: these checks and the rendering limits are what keep the site safe.

**The flow.**

- **Upload.** **Appearance → Themes → Upload a theme** takes a `.zip` of up to 20 MB, holding the theme's folder (`my-theme/theme.json`, …) or its files at the root, with `"slug"` in `theme.json`. It needs the appearance capability (administrators) and the form's CSRF token.
- **Installed, not active.** A valid theme goes into the site's themes directory. Preview it with `?preview_theme=<slug>`, then activate it.
- **Replacing.** Uploading a theme with the slug of one in the site's directory replaces it as a whole, atomically: the new version is unpacked and checked aside, and swapped in only once it loads. A version that fails the checks changes nothing, so the active theme keeps working.
- **Deleting.** A theme of the site's directory can be deleted, but not the active one. Built-in and network themes cannot.
- **Audit log.** `theme.uploaded`, `theme.updated`, `theme.refused` (with the rules it broke), `theme.deleted`, with the archive's SHA-256.
- **Command line.** `sap13ns theme list`, `sap13ns theme install <file.zip>` and `sap13ns theme remove <slug>` do the same on the server (`-site <slug>` on a network). They run the same checks and write the same audit entries (with `"source": "cli"`), and skip the plan's gate and its storage count, which the network's daily measurement corrects.
- **On a network,** the site's plan decides whether it can upload themes, and the files count against its storage ([MULTISITE.md](MULTISITE.md#plugins-and-themes)).
- **WordPress block themes.** An upload that is a WordPress block theme is converted into a sap13ns theme first, and installed through these same checks once the administrator has read the conversion's report; `sap13ns theme import` does the same on the command line ([THEME-IMPORT.md](THEME-IMPORT.md)).

**The archive.** The limits are the plugin packages' (`internal/archive`).

- At most 20 MB packed, 40 MB unpacked, 300 entries, and a compression ratio of 100 to 1 per file; only deflate or stored entries.
- No absolute paths, no `..`, no symbolic links or special files, no hidden files, no file twice. Every path segment is letters, digits, dots, hyphens and underscores.
- Per-file limits: `theme.json` 64 KB; templates, translations and docs 256 KB; `src/` and `static/*.css` 1 MB; other static files 4 MB.

**The layout.** Only what a theme needs:

- `theme.json`;
- `templates/<page>.html` for the page templates (`layout`, `home`, `single`, `page`, `archive`, `search`, `404`, `ask`), and `templates/partials/*.html`, at most 60 templates;
- `i18n/<lang>.json`;
- `src/**/*.css` (kept, never served);
- `static/**`;
- `README`, `LICENSE`, `LICENCE`, `COPYING` and `CHANGELOG` (optionally `.md` or `.txt`) at the root.

`theme.json`, `templates/layout.html`, `templates/404.html`, `static/theme.css` and a front page (`templates/front.html`, `templates/home.html` or both) are required. Anything else is refused.

**Static files.** By extension and by content: `.css`, `.woff2`, `.woff`, `.png`, `.jpg`, `.jpeg`, `.webp`, `.avif`, `.gif` and `.ico`. Each file's content is sniffed with the media library's checks (`internal/media`) and must match its extension, so an HTML page cannot pass as a `.png`.

- **No scripts, pages or WebAssembly** (`.js`, `.mjs`, `.html`, `.xml`, `.wasm`, `.php` …). The site's own scripts run the toggles and menus (see [Scripts](#scripts)).
- **No SVG.** An SVG opened at its own URL is a document that can run scripts, and the CSP of the site's origin would be its only defence. Use PNG, WebP or AVIF, an `{{icon}}`, or a `data:image/…` URL in the stylesheet, which the CSP allows for images and which never runs when CSS draws it.
- **Stylesheets** pass the customiser's CSS checker, with three relaxations for compiled stylesheets: `url()` may be relative to the stylesheet (`fonts/x.woff2`) or a fragment, `data:image/…` URLs are allowed, and rules may name `.footer-meta`. Everything else holds: no `@import`, no URLs with a scheme or a host, no `image()`, `image-set()`, `expression()`, `behavior` or `-moz-binding`, balanced and terminated, UTF-8 without NUL bytes.

**theme.json.**

- Valid JSON with the fields of [theme.json](#themejson). The slug, the folder's name or `"slug"`, is a valid slug, the two agree when both are given, and it is not the slug of a built-in theme or of a network theme.
- `name` is required. Names, descriptions, labels, help and defaults have length limits and no control characters.
- Menu, widget area and option ids are 1 to 40 lower-case letters, digits, hyphens and underscores, each declared once; at most 10 menus, 20 widget areas, 40 options and 12 swatches.
- Option types are `text`, `textarea`, `select` or `bool`; a select has 1 to 50 choices and its default is one of them.
- Colours are hexadecimal; tokens are the customiser's.

**Translations.** `i18n/<lang>.json` is named after a language code (`es`, `pt-BR`), and holds a JSON object of strings. Formats in it take widths and precisions up to 99, and none computed at run time. No string holds the placeholders the server replaces with form tokens (`sap-ctoken-`, `sap-ftoken-`).

**Templates.** Every template file is UTF-8. The theme is parsed with the server's real functions and the shared templates, as it runs, and an unknown function or template fails. Then each page, as it renders (the layout with the page's templates and the partials), is checked with the types of the page data:

- **Functions.** The functions of [Template data](#template-data) except `seq`, and Go's built-in ones except `call`. The other functions of the server (the admin's helpers, `toJSON`) are refused. `printf`, `T` and `plural` take a literal format, with widths and precisions up to 99 and none computed at run time. When the page renders, the functions that format (`printf`, `print`, `println`, `html`, `js`, `urlquery`, `T`, `plural`) take texts, numbers, booleans and dates; a list, a map or a structure is an error, since `fmt` would write it field by field. A `dict` or a `list` the template makes prints as `map[...]` or `[...]`.
- **Fields.** Every field and method must exist on the data it is used on. Templates cannot read form tokens (`.CSRF`, the signed-in user's CSRF token, and `.Token`, the comment form's), nor write out whole values that hold one, and cannot call `.Site.Assistant`, which does work on the server; the shared templates render the forms and the assistant.
- **Loops.** `range` goes only over lists of the page data (`.Posts`, `.Site.Categories`, `index`, `slice` of them…), never over a number, a literal, a list the template makes with `list`, or the result of arithmetic. The page data is built by the server and bounded by the site's content, so every loop is.
- **No recursion.** A template cannot call itself, directly or through other templates, including shared ones.
- **Silent loops.** A loop whose passes may write nothing must stay cheap: at most 400 steps per pass, no function that reads a whole text (`contains`, `printf`…), and no other such loop inside it. A page that keeps writing is stopped by the output limit; one that writes nothing would not be.
- **Size.** Each page expands to at most 100,000 steps.
- **Protected templates.** A theme cannot redefine `shared/head`, `shared/footer-meta`, `shared/blocks-footer`, `shared/blocks-sections`, `shared/block` or `blocks/footer-meta`, nor name a template with `$`. It can still redefine the other shared templates.
- **The credit.** The layout must render `{{template "shared/footer-meta" .}}` on every page, on every path through its conditions (`shared/blocks-footer` renders it when the page builder's footer is on), with the page data, in the page's text: not in an attribute, a comment, a `<title>` or a `<textarea>`. `shared/admin-bar`, `shared/comments` and `shared/comment-form`, which carry the CSRF token, must be in the page's text too.
- **HTML.** No `<script>` (the JSON-LD of `shared/head` is the core's), no `<style>`, no `style=""`, no `on*=` event handlers, no `<meta http-equiv>`, no `<base>`, `<iframe>`, `<frame>`, `<frameset>`, `<object>`, `<embed>`, `<applet>`, `<math>` or `<noscript>` (a browser reads it as text or as markup depending on JavaScript, and a theme has none). Actions cannot write the name of an element or of an attribute.
- **SVG.** An inline `<svg>` holds only drawing elements: `svg`, `g`, `path`, `circle`, `ellipse`, `line`, `polyline`, `polygon`, `rect`, `defs`, `symbol`, `use`, `linearGradient`, `radialGradient`, `stop`, `clipPath`, `mask`, `pattern`, `text` and `tspan`, and no `{{template}}`. The others (`title`, `desc`, `foreignObject`, `style`, HTML elements) make a browser read the markup in a way these checks would not.
- **Forms.** A theme's own forms use `method="get"` only, and no `formaction`, `formmethod` or `form` attributes; the forms that change something are the core's. A form's `action` is a path of the site written in the template: `action="/search"` or `action="{{link "/search"}}"`, optionally followed by more (`?x=1`), never another site, `//host` or an address an action writes. A theme's `<form>` or `<select>` cannot hold a shared template that writes a form or form fields (`shared/comments`, `shared/comment-form`, `shared/admin-bar`, the search form, the assistant…): the browser ignores a form inside a form and would add its fields, tokens included, to the theme's. Templates cannot write a plugin form's placeholder (`data-form`) nor the token placeholders (`sap-ctoken-`, `sap-ftoken-`).
- **The referrer.** The page's `Referrer-Policy` stays as the server sends it: no `referrerpolicy` attribute, no `<meta name="referrer">` (and no `<meta name>` written by an action), and `rel` is written in the template, without `opener`.
- **The probe.** Each page is rendered once without data, through the same bounded functions and limits as a visit (below), all pages within 5 seconds. `html/template` then reports a context it cannot escape (an action in the middle of an attribute name, for example), and a page that goes over a limit even without data is refused (a text that doubles at each step, an argument repeated with `%[1]s`).

**Rendering limits.** An uploaded theme renders each page into a buffer of at most 8 MB, within 5 seconds, and stops when the visitor goes away. Every function its templates call checks these limits first, so a page stops even between two writes, and counts the text it returns: at most 16 MB per page. The functions that format (`printf`, `print`, `println`, `html`, `js`, `urlquery`, `join`, `paragraphs`, `T`, `plural`) compute the length of their text before they make it, from the format actually used (for `T` and `plural`, the translation, not the key) and from their arguments, and refuse a text over 1 MB. The template sets of an uploaded theme are loaded with these functions turned off: only the bounded copies the server makes for each render run them. The rendered page must hold the credit. A page over a limit is not sent: the visitor gets a 500, and the log says `uploaded theme refused to render` with the theme and the page. Built-in and hand-installed themes render as before.

**What the checks do not decide.** The stylesheet can still hide the credit, as custom CSS can; the CPAL-1.0 licence requires it to stay visible, and the site answers for that. Design, accessibility and the content of the theme's own texts are the author's.

## Editing a theme's code

**Appearance → Themes → Edit code** opens a theme's files in the admin's code editor, as WordPress's theme file editor does: `theme.json`, the templates, the stylesheets (`static/` and `src/`) and the translations. Fonts and images are kept but not edited. It needs the appearance capability and, on a network, a plan that allows theme uploads (or a network admin).

- **Every save is an upload.** The theme is packed again with the changed file and goes through [the checks of an upload](#uploading-a-theme): a mistake refuses the save, lists the problems with their file and line, and keeps the text in the editor; nothing on disk changes. A save that passes replaces the theme at once, and the files count against the site's storage.
- **Built-in and network themes are copied.** They cannot change, so the first save installs a copy in the site's own directory, `<slug>-custom` (then `-custom-2`…), named “<Name> (custom)”, with the change. The copy starts with the original's options and Design (customiser and page builder, draft and published), is not activated, and is edited in place from then on. The original stays as it was.
- **A theme of the site's own directory** is changed in place. Uploading a new version of it later replaces the edits.
- **The compiled stylesheet.** `static/theme.css` is what pages load; a minified one opens one rule a line (only whitespace changes). It holds the Tailwind classes the theme used when it was built, so a new class has no rule: write plain CSS for what you add, or rebuild from `src/` outside the admin.
- Saves are audited as `theme.copied` or `theme.edited`, with the file and the new SHA-256.

## theme.json

`theme.json` is plain JSON. Unknown keys are ignored.

```json
{
  "name": "My theme",
  "version": "1.0.0",
  "description": "One sentence for the themes page.",
  "author": "Me",
  "license": "MIT",
  "menus": [
    {"id": "primary", "label": "Main menu"},
    {"id": "footer", "label": "Footer menu"}
  ],
  "widget_areas": [
    {"id": "top", "label": "Top bar", "description": "A full-width strip above the header."},
    {"id": "footer", "label": "Footer", "description": "Columns above the footer line."}
  ],
  "options": [
    {"key": "hero", "label": "Show the large introduction on the homepage", "type": "bool", "default": "true"},
    {"key": "accent", "label": "Accent colour", "type": "select", "default": "blue",
     "choices": [{"value": "blue", "label": "Blue"}, {"value": "green", "label": "Green"}]}
  ],
  "tokens": {
    "light": {"background": "#ffffff", "foreground": "#1d1d1f", "primary": "#0071e3", "…": "…"},
    "dark": {"background": "#000000", "foreground": "#f5f5f7", "primary": "#2997ff", "…": "…"},
    "options": {
      "accent": {"green": {"light": {"primary": "#1d8f3a"}, "dark": {"primary": "#30d158"}}}
    }
  },
  "swatches": ["#f5f5f7", "#ffffff", "#1d1d1f", "#0071e3"]
}
```

| Key | Type | Meaning |
|---|---|---|
| `name` | string | shown in the admin (the slug when empty) |
| `version`, `description`, `author`, `license` | string | shown on the themes page |
| `menus` | list of `{id, label}` | menu locations. Operators fill them in **Appearance → Menus**, per language |
| `widget_areas` | list of `{id, label, description}` | widget areas. Operators fill them in **Appearance → Widgets** |
| `options` | list | settings on the theme's **Options** page (below) |
| `tokens` | object | the theme's default colours, for the customiser (see [Design tokens](#css-and-design-tokens)) |
| `swatches` | list of colours | the preview strip on the themes page |
| `motion` | bool | the templates use the [motion library](#animations) (`data-sap-animate`), so every page loads it |

- **Menus.** `.Site.Menu "<id>"` returns a location's links. An empty `primary` location falls back to the published pages.
- **Widget areas.** Declare a `top` area for an announcement strip above the header: the page builder's header offers it as its top bar. The widgets available are recent posts, categories, tags, archives, search, feeds, text, and the widgets of enabled plugins.
- **Options.** Each option has a `key`, a `label`, a `type`, a `default` and an optional `help`. `select` options also have `choices` (`{value, label}`).

  | Type | Value in templates |
  |---|---|
  | `text`, `textarea` | the text |
  | `select` | one of the choices' values; an unknown stored value falls back to the default |
  | `bool` | `"true"` or `"false"` |

  Templates read every option with `index .Site.Options "<key>"`, and they always get a value: the stored one, or the default.
- **Labels** are plain strings: write them in English, like every source string.

## Templates

Templates are Go [`html/template`](https://pkg.go.dev/html/template), which escapes every value for its context.

- **`layout.html`** defines the template `layout`: the whole document, from `<!doctype html>` to `</html>`. It renders the page body with `{{template "content" .}}`.
- **Each page template** defines `content`.
- **`partials/*.html`** are parsed into every page. Use them for templates the theme's pages share, and give them names that cannot clash with `content` or `layout`.
- **Overriding.** Shared templates are parsed first, so a theme can redefine any of them (`shared/…` or `blocks/…`) in a partial. An uploaded theme cannot redefine the ones that render the head and the credit ([Uploading a theme](#uploading-a-theme)).

A theme needs `layout` and a front page: `front`, `home` or both. Every other page is optional. A missing page renders with another of the theme's pages, or with a neutral shared fallback inside the theme's own layout, so a landing-page theme still has a working blog.

| Page | Used for | Without it |
|---|---|---|
| `front` | the theme's own front page: a landing page or a one-page site ([below](#front-pages-and-one-page-sites)) | `/` lists the latest posts |
| `home` | the latest posts (`/`, or `/blog` when the front page is something else) | `shared/fallback-home` |
| `single` | one post | `shared/fallback-single` |
| `page` | one page, including a static front page | `single`, else `shared/fallback-single` |
| `archive` | category, tag, author and date archives | `home`, else `shared/fallback-list` |
| `search` | search results | `home`, else `shared/fallback-list` |
| `404` | not found and gone | `shared/not-found` inside the layout |
| `ask` | the site assistant without JavaScript | `shared/ask` inside the layout |

The fallbacks use the shared blocks (post list, post details, content, comments, pagination) and the neutral `.sap-fallback` styles of `shared.css`, which follow the theme's colour tokens. A theme can restyle them in its `theme.css`.

Pages laid out with the [page builder](#the-page-builder) use a page the server adds, whose `content` is `shared/blocks-main`. They still render inside the theme's `layout`.

A minimal `layout.html`:

```html
{{define "layout"}}<!doctype html>
<html lang="{{.Site.Lang}}"{{with .Site.Custom.Class}} class="{{.}}"{{end}}>
<head>{{template "shared/head" .}}</head>
<body class="kind-{{.Kind}}">
{{template "shared/skip-link"}}
{{template "shared/admin-bar" .}}
{{if .BlocksHeader}}{{template "shared/blocks-header" .}}{{else}}
{{template "shared/widget-area" (dict "Page" . "Area" "top")}}
<header>
  <a href="{{link "/"}}">{{if .Site.Custom.Logo}}{{template "shared/logo" .}}{{end}}{{if not .Site.Custom.LogoOnly}}{{.Site.Title}}{{end}}</a>
  <nav aria-label="{{T "Main menu"}}"><ul id="menu">{{template "shared/menu" (.Site.Menu "primary")}}</ul></nav>
  {{template "shared/lang-switch" .}}{{template "shared/theme-toggle"}}{{template "shared/nav-toggle" "menu"}}
</header>
{{end}}
<main id="main" tabindex="-1">{{template "content" .}}</main>
{{if .BlocksFooter}}{{template "shared/blocks-footer" .}}{{else}}
<footer>{{template "shared/widget-area" (dict "Page" . "Area" "footer")}}{{template "shared/footer-meta" .}}</footer>
{{end}}
{{template "shared/assistant" .}}
</body>
</html>{{end}}
```

## Front pages and one-page sites

A theme can be a landing page or a one-page site rather than a blog. `templates/front.html` is its front page; the blog moves to `/blog`, where the theme's `home` (or the shared fallback) lists the posts.

**When `/` shows it.** **Settings → Reading** has three choices for the front page: the latest posts, a static page, or **The theme's front page**. The last one shows the active theme's `front.html`, or a front page built in the [page builder](#the-page-builder), which any theme can have and which takes the place of `front.html`. With neither, `/` lists the posts.

- **Activating** a theme that has `front.html` while the site shows its latest posts switches the setting to the theme's front page. A static front page is left alone.
- **Previewing** a theme that has `front.html` (`?preview_theme=<slug>`) shows it at `/`, even while the site lists its posts.
- **`/?page=2`** keeps listing the posts, and the sitemap lists `/blog` (in every language) whenever `/` does not.

**Its data** is a PageData with `.Kind` `front` and `.IsFront`, the WebSite JSON-LD, and the latest posts in `.Posts` (as many as the posts per page, without pagination) for a section that shows them. Link to the rest with `.Site.Blog`.

**Sections and anchors.** Give each section the menu scrolls to an `id` and the class `sap-anchor`:

```html
<section class="sap-anchor" id="pricing" aria-labelledby="pricing-title">…</section>
<a href="{{.Site.Link "#pricing"}}">{{T "Pricing"}}</a>
```

- **`.Site.Link "#pricing"`** is `#pricing` on the front page and `/#pricing` (or `/en/#pricing`) on every other page, so the menu still works from a post. Menu items whose link is `#name` (**Appearance → Menus**), and the links of builder headers and footers, go through it on their own.
- **`onepage.js`** runs on the front page and on any page whose menu or layout has anchors (`.OnePage`: `shared/head` then marks the site bundle's script with `data-sap-onepage`). It gives the links in `header`, `nav` and `footer` that point to the section in view `aria-current="true"`. Style `a[aria-current]` to show it.
- **A sticky header** (a `header` or a `[data-sticky]` element with `position: sticky` or `fixed`) is measured into `--sap-scroll-offset`, which `.sap-anchor` uses as its `scroll-margin-top`, so a section never lands under it.
- **Scrolling is smooth** only under `prefers-reduced-motion: no-preference`. The links are ordinary links, so the page works without JavaScript.

**The landing blocks' markup.** The page builder's landing blocks (features, stats, testimonials, pricing, questions, logos, call to action, in `ui/shared/templates/blocks/landing.html`) use classes such as `.sap-feature`, `.sap-stats`, `.sap-quote`, `.sap-plan` and `.sap-faq-item`, styled by `shared.css`. A `front.html` that uses the same markup for its sample sections gets those styles, and whatever the theme adds to them also applies to a front page built from blocks.

**Onepage**, the built-in example (`ui/themes/onepage`), ships only `layout.html`, `front.html` and `404.html`:

- a sticky header whose menu, when the site has none, links to its sample sections and to the blog;
- an introduction, figures, features, testimonials, pricing, questions, the latest posts and a closing call to action, with every text through `T`;
- options for the accent, serif or sans headlines, the introduction's headline and text, the contact link, and one switch per section (its menu link goes with it);
- a note, for administrators only, that opens the page builder on the front page, whose **One-page site** preset rewrites the samples as blocks to edit.

## Shared templates

`ui/shared/templates` holds the parts every site needs. Call them with `{{template "shared/<name>" <data>}}`.

| Template | Data | Renders |
|---|---|---|
| `shared/head` | PageData | everything inside `<head>`: title, description, canonical and `hreflang` links, Open Graph, feeds, JSON-LD, the scheme script, `theme.css`, the customiser's stylesheet, plugin assets and the site's scripts (`site.min.js`) |
| `shared/skip-link` | none | the "Skip to content" link to `#main` |
| `shared/admin-bar` | PageData | the floating toolbar for signed-in editors |
| `shared/menu` | `[]MenuLink` | `<li>` items of a menu |
| `shared/nav-toggle` | the id of the element it opens | the small-screen menu button |
| `shared/theme-toggle` | none | the light/dark switch |
| `shared/lang-switch` | PageData | links to this page in the other languages (nothing on single-language sites) |
| `shared/logo` | PageData | the customiser's logo (nothing without one) |
| `shared/widget-area` | `dict "Page" . "Area" "<id>"` | the widgets of an area, in `.widget-area.widget-area-<id>` |
| `shared/footer-meta` | PageData | the footer line with the **"Powered by sap13ns"** credit, the "Cookies" link and the cookie consent ([Cookie consent](#cookie-consent)) |
| `shared/post-meta`, `shared/categories`, `shared/tags` | PostView | date, author and reading time; term links |
| `shared/image` | `dict "Image" "Sizes" "Eager" "Class"` | a responsive `<img>` with `srcset` |
| `shared/avatar` | `dict "A" "Size" "Class"` | a person's photo, or their initials without one (below) |
| `shared/featured-image` | PostView | the post's image with its caption |
| `shared/content` | PostView | the rendered body, in `.prose` |
| `shared/share` | PostView | share links, without third-party scripts |
| `shared/post-nav` | PageData | previous and next post; only the links that exist, the next one stays in the second column |
| `shared/post-list` | `dict "Posts" "Layout" "H"` | posts as a `list`, `grid` or `bento` mosaic |
| `shared/pagination` | `*Pagination` (may be nil) | page links |
| `shared/archive-header` | PageData | the title of an archive or a search |
| `shared/search-form` | PageData | the search form; its field gets an id unique in the page, so it can appear more than once |
| `shared/search-count` | PageData | "12 results for “q”" for screen readers (a visually hidden status); show your own count beside it with `aria-hidden="true"` |
| `shared/no-results` | PageData | an empty listing |
| `shared/fallback-home`, `shared/fallback-single`, `shared/fallback-list` | PageData | the stand-ins for a missing `home`, `single`/`page` and `archive`/`search` ([Templates](#templates)) |
| `shared/not-found` | PageData | the default 404 body |
| `shared/comments` | PageData | the comments and the comment form |
| `shared/subscribe` | PageData | feed links |
| `shared/assistant` | PageData | the site assistant's button and dialog; include it once, before `</body>` |
| `shared/ask` | PageData | the assistant page without JavaScript |
| `shared/blocks-header`, `shared/blocks-top`, `shared/blocks-bar`, `shared/blocks-footer`, `shared/blocks-main` | PageData | page builder regions (see [The page builder](#the-page-builder)) |
| `blocks/<type>` | BlockView | one page builder block |

## Template data

Every page template gets a **PageData** (`.`).

| Field | Meaning |
|---|---|
| `.Site` | the site (below) |
| `.Kind` | `front`, `home`, `single`, `page`, `archive`, `search`, `404` or `ask` |
| `.Title`, `.Heading`, `.Description` | the page's title, its visible heading and its description |
| `.IsFront` | the front page: the theme's front page, a static front page or the latest posts at `/` |
| `.OnePage` | the page has sections to scroll to, or menu links to them ([Front pages](#front-pages-and-one-page-sites)) |
| `.Posts`, `.Pagination`, `.Total` | listings: home, archives and search; `.Total` counts the posts of every page |
| `.Post` | single posts and pages |
| `.Prev`, `.Next` | the neighbouring posts of a single post |
| `.Comments`, `.CommentForm` | used through `shared/comments` |
| `.Archive` | archives: `.Kind` (category, tag, author, date), `.Title`, `.Description`, `.FeedURL`, `.Count` |
| `.Query` | the search terms |
| `.Translations` | this page in every site language (`.Code`, `.Name`, `.URL`, `.Current`) |
| `.Area "<id>"` | the widgets of an area |
| `.ElementID "<prefix>"` | an id unique in the page (`prefix`, then `prefix-2`…), for a partial used more than once |
| `.Preview` | a draft shown to its editor |
| `.BlocksHeader`, `.BlocksFooter` | page builder regions, nil when not built |

**`.Site`** is the same on every page:

| Field | Meaning |
|---|---|
| `.Title`, `.Tagline`, `.Lang`, `.Year` | site title and tagline, page language, current year |
| `.URL`, `.Home`, `.Search`, `.Blog` | the absolute home URL, and the home, search and posts list (`/blog`) paths in this language |
| `.Menu "<id>"` | a menu location's links (`.Label`, `.URL`, `.Active`, `.External`); the primary location falls back to the published pages |
| `.Menus` | the menus made for each location, without that fallback (`index .Site.Menus "primary"` is empty when there is none) |
| `.Link "<url>"` | a link as this page needs it: away from the front page, `#section` becomes `/#section` |
| `.Options` | the theme options (`index .Site.Options "<key>"`) |
| `.Categories`, `.Tags`, `.Pages`, `.Archives`, `.Stats` | lists and counts for navigation and widgets; a term or month has `.Current` on its own archive (use `aria-current="page"`) |
| `.FeedRSS`, `.FeedAtom`, `.FeedJSON` | feed URLs |
| `.FooterText` | the customiser's footer line, empty when not set |
| `.Viewer` | the signed-in user, nil for visitors |
| `.LoginURL` | the site's sign-in address (`/login`, or the one chosen in Settings → Accounts & security); empty when the administration is hidden from visitors, so link to it only when it is set |
| `.Custom` | the customiser's settings (see [The customiser](#the-customiser)) |
| `.Asset "<file>"` | the hashed URL of a file in the theme's `static/` |
| `.Static "<file>"` | the hashed URL of a core static file |

**A post** (`.Post`, each of `.Posts`) has `.Title`, `.URL`, `.Summary`, `.Content` (safe HTML), `.Published`, `.HasDate`, `.Updated`, `.Author` (`.Name`, `.URL`, `.Initials`, `.Avatar`), `.Categories`, `.Tags`, `.Image`, `.ReadingMinutes`, `.CommentCount` and `.CommentsOpen`. An image (`.Image`) has `.URL`, `.Srcset`, `.Width`, `.Height`, `.Alt`, `.Caption`, and the `.Thumb`, `.Medium` and `.Large` variants.

**An avatar** (`.Author.Avatar`, a comment's `.Avatar`, `.Site.Viewer.Avatar`) is the person's photo: the one they uploaded, else a copy of their Gravatar when the site uses Gravatar (Settings → Discussion). It has `.URL` (64 px), `.URL2x` (128 px), `.Large` (256 px), `.Srcset` and `.Initials`. The URLs are empty when there is no photo; show the initials then. Every URL is on the site itself, so a page never makes the visitor's browser contact Gravatar. `shared/avatar` does both:

```html
{{template "shared/avatar" (dict "A" .Author.Avatar "Size" 40 "Class" "byline-avatar")}}
```

`Size` is the size in CSS pixels (40 by default). It goes into `width`, `height` and `sizes`, and the image loads lazily. `Class` is your class for the initials. The photo carries it too, with `sap-avatar`, which crops it to fill the box and draws a thin ring that shows in light and dark mode. Without `Class`, `sap-avatar-initials` is a 40 px circle. The name goes next to the avatar, so the image has `alt=""` and the initials are `aria-hidden`.

**Functions** available in every template:

| Function | Example |
|---|---|
| `T` | `{{T "Read more"}}`, `{{T "%d comments" .CommentCount}}`: a string in the page language |
| `plural` | `{{plural .Count "%d post" "%d posts"}}`; more arguments follow the count: `{{plural .Total "%d result for “%s”" "%d results for “%s”" .Query}}` |
| `link` | `{{link "/search"}}`: a site path with the page's language prefix |
| `lang` | the page language |
| `date`, `shortDate`, `dateTime`, `ago`, `monthName`, `isoDate` | localised dates in the site's time zone; `isoDate` is RFC 3339 for `datetime` attributes |
| `icon` | `{{icon "search"}}`, `{{icon "sun" "theme-icon-light"}}`: an inline Lucide icon, hidden from assistive technology |
| `dict`, `list`, `seq` | build arguments for templates and ranges (`seq` is not available to uploaded themes) |
| `add`, `sub`, `mul`, `div` | integer arithmetic |
| `join`, `lower`, `upper`, `contains`, `hasPrefix`, `truncate`, `initials`, `paragraphs` | text helpers (`paragraphs` turns plain text into escaped `<p>` elements) |

`icon` only knows the icons compiled into the binary, the ones listed in `scripts/icons.txt`. An unknown name renders nothing.

## Translations

Source strings are English. Pass every visible string through `T`.

- **The core catalogue** (`ui/i18n/<lang>.json`) translates the strings of the core and of the shared templates.
- **A theme's own strings** go in `i18n/<lang>.json` inside the theme: a flat map from the English string to its translation. It is checked before the core catalogue, and `%s`/`%d` arguments work as in `T`.
- **Manifest strings** (the description, option labels, help and choices, menu and widget area labels in `theme.json`) are shown translated in the admin, from the theme's `i18n/<lang>.json` or the core catalogue. `make i18n` checks those of the built-in themes.
- **Links** to site paths go through `link`, so a page in English keeps its visitor in `/en/`.

## CSS and design tokens

### Building the stylesheet

Each theme has one stylesheet, `static/theme.css`, compiled from `src/theme.css` with Tailwind CSS v4. The built-in themes start like this:

```css
@import "tailwindcss" source(none);
@source "../templates";
@source "../../../shared/templates";

@import "basecoat-css/base/base.css";
/* Only the components plugins may emit. */
@import "basecoat-css/components/alert.css";
@import "basecoat-css/components/badge.css";
@import "basecoat-css/components/button.css";
@import "basecoat-css/components/card.css";
@import "basecoat-css/components/kbd.css";
@import "../../../shared/src/shared.css";
```

- **Basecoat UI** gives the base styles, the `dark:` variant (`html.dark`) and the components that plugin output may use (alerts, badges, buttons, cards, `kbd`).
- **`shared.css`** styles what the shared templates render (skip link, toggles, the language switch, prose, post lists, comments, the assistant, the page builder's blocks). Its rules are in `@layer components`, so the theme's own rules win.
- **Videos and image layout** come from `shared/src/media.css`, which `shared.css` imports **outside** the layer: themes style `.prose img` and `.prose a` without a layer, and those rules must not reach into the video player. Its selectors pair an element with a class (`figure.sap-video`, `img.align-left`), so a theme's `.prose img` loses to them while the theme's own, more specific rules still win. See [Videos and images in posts](#videos-and-images-in-posts).
- **The assistant's button** is fixed to a bottom corner. The site owner picks the corner and the height in **Settings → Site assistant**, and `shared/assistant` writes them as classes on its root: `sap-assistant-right` or `sap-assistant-left`, and `sap-assistant-offset-normal`, `-raised` or `-high`. Those classes set two custom properties, `--sap-fab-side` (the distance from the side) and `--sap-fab-bottom` (the distance from the bottom), which place both the button and the chat window. A theme with a fixed element in a bottom corner or along a side moves the button by setting them, not by restyling it. The Dashboard theme, whose sidebar is fixed on the left from 1024px, does this:

  ```css
  @media (min-width: 1024px) {
    .sap-assistant-left { --sap-fab-side: calc(var(--sidebar-w) + 1rem); }
  }
  ```

  The button hides while the small-screen menu is open (`html.nav-open`), and below 640px the chat window fills the screen.
- **`@source`** must include the theme's templates and `ui/shared/templates`, or Tailwind drops the classes they use.
- **Build.** `npm run build` compiles every theme in `ui/themes/`, and `npm run watch` rebuilds on changes. The output is committed, so production never runs Node.js. For a theme outside `ui/themes/`, run the same compiler from the repository root:

  ```sh
  node_modules/.bin/tailwindcss -i path/to/my-theme/src/theme.css -o path/to/my-theme/static/theme.css --minify
  ```

  and adjust the relative `@source` and `@import` paths to `ui/shared`.
- **Fonts and images** come from this site. No CDNs, no web font services.

### Tokens

The customiser does not touch a theme's CSS. It publishes a small stylesheet that only sets `--sap-*` variables on `:root`. A theme reads each one **with its own value as the fallback**, so an empty design changes nothing:

```css
:root {
  --background: var(--sap-light-background, #ffffff);
  --primary: var(--sap-light-primary, #0071e3);
  --radius: calc(1.125rem * var(--sap-radius-scale, 1));
}
.dark {
  --background: var(--sap-dark-background, #000000);
  --primary: var(--sap-dark-primary, #2997ff);
}
```

| Variable | Set by | Map it to |
|---|---|---|
| `--sap-light-<token>`, `--sap-dark-<token>` | Colours | the theme's colour variables, in `:root` and `.dark` |
| `--sap-light-<token>-rgb`, `--sap-dark-<token>-rgb` | Colours | the same colour as `r g b`, for `rgb(var(…) / 0.8)` |
| `--sap-light-background-hi`, `-lo`, `--sap-dark-background-hi`, `-lo` | the background colour | a highlight and a shade of the surface, for paired shadows |
| `--sap-font-body`, `--sap-font-heading`, `--sap-font-mono` | Fonts | font-family stacks |
| `--sap-text-scale` | Text size | a factor; `shared.css` scales the root font size with it |
| `--sap-radius-scale` | Corner radius | multiply radii |
| `--sap-space-scale` | Density | multiply paddings and gaps |
| `--sap-shadow-scale` | Shadows | multiply shadow offsets and blurs |
| `--sap-content-width` | Content width | the main container's width |

The colour tokens are `background`, `foreground`, `card`, `card-foreground`, `muted`, `muted-foreground`, `border`, `primary`, `primary-foreground`, `accent` and `accent-foreground`. Map every one of them in both schemes.

- **Option classes.** A theme that changes colours with an option (such as an `accent-green` class on `<html>`) maps the tokens in those rules too: `.accent-green { --primary: var(--sap-light-primary, #1d8f3a); }` and `.dark.accent-green { … }`.
- **`tokens` in theme.json** repeats the defaults: the light and dark values of every colour token, and per option value the colours it changes. The customiser shows them as the theme's colours and checks contrast against them. For the built-in themes, a test checks that the fallbacks in `src/theme.css` match `theme.json`.
- **Generated rules.** Besides the variables, the customiser's stylesheet can set the heading font on `:where(h1, h2, h3, h4, h5, h6)`, lay out the footer widget area in columns (`.widget-area.widget-area-footer`), and append the operator's custom CSS.

## Light and dark

Every theme must work in both modes.

- **Before the first paint**, the core's `theme-init.js` (a blocking script of its own, not part of the bundle) adds the `dark` class to `<html>` from the visitor's saved choice, or else from `prefers-color-scheme`. `?scheme=dark|light` forces one mode for one view.
- **The toggle** (`shared/theme-toggle`) switches the class and remembers the choice. With no saved choice, the page follows the system setting as it changes.
- **Define light values on `:root` and dark values on `.dark`.** Use Tailwind's `dark:` variant for anything else. `shared/head` already sets `<meta name="color-scheme" content="light dark">`.
- **Check contrast in both modes.** Body text, muted text, links, buttons and focus rings must stay readable on every surface.

## Videos and images in posts

Post content can hold two things a theme should check in both modes and at phone width.

**The video player.** A YouTube or Vimeo link alone on a line, or the `[video url=… start=… title=…]` shortcode, renders as a click-to-load player (the site setting **Settings → Videos** can turn it off). Without JavaScript it is a link to the video page; `video.js` makes it a button and, on a click, swaps in the provider's iframe.

```html
<figure class="sap-video sap-video-youtube" data-video-provider="youtube">        <!-- + sap-video-portrait for Shorts -->
  <a class="sap-video-link" href="https://www.youtube.com/watch?v=…" data-video-src="…">  <!-- a <button> once video.js runs -->
    <img class="sap-video-poster" src="/video-thumbs/yt-….jpg" alt="" width="1280" height="720"> <!-- when cached -->
    <span class="sap-video-play" aria-hidden="true"></span>
    <span class="sap-video-caption"><span class="sap-video-title">…</span><span class="sap-video-source">Watch on YouTube</span></span>
  </a>
</figure>
<!-- playing: figure.is-playing > iframe.sap-video-frame -->
```

- The player is 16:9 (`sap-video-portrait`: 9:16, at most 22rem wide), fills the column, and stays dark in both modes, as the posters and the players are. The play button turns `--primary` on hover and focus, and the focus ring is `--ring`; most themes need nothing else.
- Change it with selectors at least as specific as the shared ones, for example `.prose figure.sap-video { border-radius: 0; }`.
- **Feeds** get a plain figure: the poster linked to the video, and the title.

**Image size and alignment.** `![alt](src){width=480 align=left}` sets the image's `width` (and `height`, from the media library's original) and one class:

| Class | Layout |
| --- | --- |
| `align-left`, `align-right` | Floats, at most 60% of the column, with the text around it |
| `align-center` | A block, centred |
| `align-full` | The column's whole width |

- `.entry-content` (from `shared/content`) is a `flow-root`, so a floated image never hangs out of the post, and headings, rules, code, tables and videos start below a floated image.
- **Below 640px** floats stop and the image takes the column's width.
- A width without an alignment keeps the image inline. A theme that styles `.prose img` should keep `height: auto` and `max-width: 100%`, so that images with a `width` stay in proportion.

## Cookie consent

When the site's cookie consent is on (Settings → Privacy and cookies), `shared/footer-meta` adds a "Cookies" link to the footer line and renders the banner and the cookie settings dialog, and `shared/head` links `consent.css` before `theme.css`; `consent.js`, in the site bundle (see [Scripts](#scripts)), runs because the dialog is on the page. A theme needs no markup of its own: every theme that renders `shared/footer-meta` (all of them must) gets it, uploaded and imported ones included. The script moves the banner and the dialog out of the footer to the top and the end of `<body>`, so no stacking context or overflow of the theme's footer reaches them.

**Looks.** `consent.css` takes its colours from the Basecoat tokens every built-in theme defines in `:root` and `.dark` (`--popover`, `--popover-foreground`, `--muted`, `--muted-foreground`, `--border`, `--primary`, `--primary-foreground`, `--ring`, `--radius`), and falls back to the system colours (`Canvas`, `CanvasText`, `LinkText`) for a theme that defines none, so it works in light and dark without changes. To restyle it, set its custom properties on `.sap-consent-banner, .sap-consent-dialog, .sap-consent-note` (in `.dark` too, if they differ):

| Property | Default | Used for |
|---|---|---|
| `--sap-consent-bg`, `--sap-consent-fg` | `--popover`, `--popover-foreground` | the banner and the dialog |
| `--sap-consent-muted` | `--muted-foreground` | descriptions and the table's secondary text |
| `--sap-consent-border`, `--sap-consent-subtle` | `--border`, `--muted` | borders, the categories' and table's background |
| `--sap-consent-accent`, `--sap-consent-accent-fg` | `--primary`, `--primary-foreground` | the Accept and Reject buttons (equally prominent), switches that are on |
| `--sap-consent-ring` | `--ring` | focus rings |
| `--sap-consent-radius`, `--sap-consent-shadow`, `--sap-consent-font` | `--radius`, a soft shadow, `inherit` | shape and type |

```css
.sap-consent-banner, .sap-consent-dialog, .sap-consent-note {
  --sap-consent-radius: 0;
  --sap-consent-shadow: none;
  --sap-consent-font: var(--font-mono);
}
```

For more, use selectors at least as specific as the shared ones: `.sap-consent-banner`, `.sap-consent-dialog` (a native `<dialog>`, with `::backdrop`), `.sap-consent-btn` and `.sap-consent-btn-quiet`, `.sap-consent-cat`, `.sap-consent-switch` (`[aria-checked="true"]` when on), `.sap-consent-table` (stacked rows below 40rem), and `.sap-consent-note` or `.sap-consent-controls` for the "Always allow" line under a click-to-load video. `consent.css` is not in a cascade layer, so a theme's layered resets do not undo it, and a theme rule must be at least as specific to win.

**Keep it usable.**

- Do not hide the banner, the "Cookies" link or the Reject button, and keep Accept and Reject the same weight: that is what makes the consent valid.
- The banner is fixed at the bottom with `z-index: 85`, above the assistant's button (80) and below the admin bar (90). Leave room for it rather than covering it.
- Check it in light and dark, at 360 px and on desktop: the banner, the dialog with a category's details open, and a post with a video before it plays.

## Content Security Policy

Public pages are served with a strict policy:

```text
default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: https:;
media-src 'self' https:; font-src 'self'; connect-src 'self'; frame-src 'none'; frame-ancestors 'none'; …
```

- **No inline scripts.** No `<script>` blocks with code, and no `onclick` or other event attributes. (`<script type="application/ld+json">` from `shared/head` is data, not code.)
- **No inline styles.** No `<style>` elements and no `style=""` attributes. Every look is a class in `theme.css`.
- **No JavaScript of its own.** The core's `site.js` (in `site.min.js`, see [Scripts](#scripts)) handles the colour toggle, the small-screen menu (it toggles `.is-open` on the element a `shared/nav-toggle` controls, and `nav-open` on `<html>`; Escape, a click outside, following a menu link or growing to the width where the toggle is hidden close it), the language switch and comment replies.
- **No external fonts or stylesheets.** Fonts and styles are same-origin. Images and media may be external (`https:`).
- **No frames,** except the video players of the core: a page that shows one also allows `https://www.youtube-nocookie.com` and `https://player.vimeo.com` in `frame-src`, and its `video.js` (in `site.min.js`) makes the players. Plugins can add their own sources.

### Scripts

A theme writes no JavaScript and loads none: `shared/head` and `shared/assistant` bring the core's, the same in every theme (built in, uploaded or converted from WordPress). A public page makes two script requests, plus one per plugin script:

| File | Loaded | Does |
|---|---|---|
| `theme-init.js` | in `<head>`, blocking (it is tiny) | puts the `dark` class on `<html>` before the first paint |
| `site.min.js` | in `<head>`, `defer` | the bundle of `site.js` (toggles, menus, language switch, comment replies, copy link), `consent.js` (only with the cookie consent's dialog; it sets `window.sapConsent` before the plugins' scripts run), `onepage.js` (only with `data-sap-onepage`), `video.js` (only on a page with a player) and `assistant.js` (only with the assistant's widget) |
| plugin scripts | after it, `defer` | each enabled plugin's `assets.js`, as they are (docs/PLUGINS.md) |

- **The bundle** is built by `npm run build` (esbuild, `scripts/js-bundles.mjs`) and committed, so production never runs Node.js. Each source keeps its own scope and starts by looking for its elements, so a page runs only the parts it uses.
- **Editing a script** in `ui/static/`: run `npm run watch`, which rebuilds the bundle on every save (or `npm run build` once), and reload; with `SAP13NS_DEV=true` the server reads the new bundle from disk. `npm test` fails while a committed bundle is older than its sources.

## Accessibility

Themes are expected to meet WCAG 2.2 AA.

- **Skip link.** `{{template "shared/skip-link"}}` comes first in `<body>`. It targets `<main id="main" tabindex="-1">`.
- **Landmarks.** One `<header>`, one `<main>`, one `<footer>`, and `<nav>` elements with an `aria-label` (such as `{{T "Main menu"}}`).
- **Headings.** One `<h1>` per page, then headings in order.
- **Focus.** Every link, button and field shows a visible `:focus-visible` style in both modes. Never remove outlines without a replacement.
- **Reduced motion.** Animations and transitions that move things run only under `@media (prefers-reduced-motion: no-preference)`, or are switched off under `reduce`. The shared stylesheet also cuts every animation to one near-instant run under `reduce`.
- **Tables.** Post tables come wrapped in `<div class="table-scroll">`, which scrolls sideways on narrow screens. Keep the table itself `display: table`.
- **Controls.** Use the shared toggles: they carry their labels, `aria-expanded` and `aria-controls`. Icons from `icon` are hidden from screen readers, so an icon-only link needs an `aria-label`.
- **Language.** `<html lang="{{.Site.Lang}}">`.

## The customiser

**Appearance → Design** (or **Design** on a theme's card) has two tabs over one preview: **Style and texts** (the customiser, below) and **Structure** (the [page builder](#the-page-builder)). They share one draft: **Publish** publishes the style, the texts, the custom CSS and the layouts together, and **Discard draft** drops all of it.

The customiser changes a theme's look without editing it. Each theme has its own design.

| Panel | What the operator can change |
|---|---|
| Texts and images | the texts and photographs a theme declares in `theme.json` (`content`), per language, with images from the media library; a click on one in the preview opens its field |
| Colours | the eleven colour tokens, separately for light and dark, with contrast warnings (WCAG AA) and dark colours derived from the light ones on request |
| Fonts | body, headings and code: a curated system stack or a WOFF2 file from the media library |
| Size and spacing | text size (90 % to 120 %), corner radius, density, shadows, content width |
| Header | the brand (title, logo, or logo and title), a centred brand, a sticky header, hiding the search link and the language switch |
| Footer | the footer widget columns (1 to 4) and a footer text, per language |
| Home page | the large introduction, the post layout (list, grid or bento) and the posts per page |
| Theme options | the options from `theme.json` |
| Custom CSS | up to 64 KB of extra rules, served as a separate file after the theme's styles (also editable in the page builder) |

- **Drafts.** Every change is saved as a draft and shown in the live preview (desktop or mobile, light or dark). Only users with the appearance capability see drafts, through `?customize=draft`. Visitors keep the published design.
- **Publish, discard, reset.** Publishing makes the draft live and writes its theme options to the theme's stored options. Discard drops the draft. Reset starts a draft with nothing customised, so publishing it restores the theme's own look.
- **Revisions.** The last 10 published versions are kept. Restoring one copies it into the draft.
- **Export and import.** The export is one JSON file with the design, the theme options in effect and the page builder's layouts. Importing validates the whole file before changing anything, then replaces the drafts of what the file carries: the design, the layouts, or both.
- **Inactive themes** can be customised too. What is published applies when the theme is activated.
- **Validation.** Colours must be hex values, every other setting is a named choice, and fonts and logos must be files in the media library. Custom CSS may not use `@import`, `url()` to other sites (site paths, paths relative to the stylesheet, `#fragments` and `data:` images are fine), functions that fetch resources, legacy script hooks (`expression()`, `behavior`, `-moz-binding`), `javascript:` or `</style`, and it may not name `footer-meta`. Brackets must balance. Only users with the appearance capability can upload WOFF2 fonts.

**What a theme does to support it:**

- Map the `--sap-*` variables, with fallbacks, and declare `tokens` in `theme.json`.
- Put `.Site.Custom.Class` on `<html>`. It holds `sap-sticky-on` or `sap-sticky-off`, `sap-brand-center` and `sap-has-logo`, and the theme's CSS reacts to them.
- In the header, show `shared/logo` when `.Site.Custom.Logo` is set, hide the title when `.Site.Custom.LogoOnly` is true, and leave out the search link when `.Site.Custom.HideSearch` is true. `shared/lang-switch` handles `HideLang` itself.
- On the home page, render `shared/post-list` with `.Site.Custom.HomeLayout` when it is set. Read the introduction setting from a `hero` option, which the customiser overrides, or with `.Site.Custom.HeroOn <the theme's default>`. The posts per page are applied by the server.
- Use `shared/footer-meta` and `shared/widget-area` for the footer, so the footer text and the columns apply.

### Editable texts and images

A theme whose pages carry their own texts and photographs (a front page in sections, headers with pictures) declares them in `theme.json`, so the operator can change them in the customiser without editing the theme:

```json
"content": [
  {"id": "intro", "label": "Front page · Opening", "fields": [
    {"key": "intro-title", "label": "Title", "type": "text", "default": "The art of resolution"},
    {"key": "intro-lead", "label": "Introduction", "type": "textarea", "default": "Dialogue as the solution."},
    {"key": "values", "label": "Values", "type": "lines", "default": "Dialogue\nEmpathy\nTrust"},
    {"key": "intro-image", "label": "Photograph", "type": "image", "default": "img/pier-960.webp"}
  ]}
]
```

- **Types.** `text` (up to 500 characters), `textarea` (up to 5,000), `lines` (a list, one item per line, up to 60) and `image` (a file from the media library). Up to 40 groups and 400 fields; keys are ids, unique across groups.
- **Defaults.** A text's `default` is English, and the theme's `i18n/<lang>.json` translates it (a `lines` default line by line), so what was not edited keeps the theme's translations. An image's `default` is the file in `static/` the theme shows instead, for the customiser's thumbnail. Labels and help are translated by the theme's catalog too.
- **Languages.** Texts are edited per site language. A text edited only in the main language still shows the theme's own translation in the others. Images are the same in every language.
- **In templates.** `{{.Site.Text "key"}}` is the text in the page's language, `{{range .Site.Lines "key"}}` its lines, and `{{with .Site.Image "key"}}…{{else}}<the theme's image>{{end}}` the image chosen (an image view, as `shared/image` takes), or nil.
- **Click to edit.** Put `data-sap-edit="key"` on the element that holds only that text (wrap the text in a `<span>` when it shares its element with an icon), or on an image, or on the section of a background photograph. In the preview the element is outlined on hover. A click on a `text` or `textarea` lets the operator type over it right there (Enter ends a one-line text, Escape puts it back), and the text is saved into the draft like its field; a click on an image or a list opens its field. **Click to edit** in the preview's toolbar turns this off, to follow links in the preview; the customiser writes typed text into marked elements at once and reloads the preview for images and lists. On public pages the attribute only carries the animation chosen for that text or image, if any (see [Animations](#animations)).
- **Every interface text, with no declaration.** The texts a template writes with `{{T "..."}}` can be typed over in the preview of any theme, uploaded or built in. In the draft preview the server wraps each one that lands in the page's text in `<span data-sap-text="source">` (in attributes, the title, scripts and styles it only changes, unmarked). What is typed goes to the design's `texts` (language → English source → text), drafted and published with the rest of the design; emptied, the theme's own text comes back. Only texts without arguments change (`{{T "%d posts" .N}}` stays the theme's), and the licence credit ("Powered by") never does. Use `content` fields for texts that deserve a field in the sidebar, an animation or images; interface texts need nothing.

## The page builder

**Appearance → Design → Structure** replaces a theme's regions with layouts made of sections, columns and blocks. Each theme has its own layouts.

- **Starting.** A region that has no layout uses the theme's template, which is fixed: the structure panel says so and offers the ways to start, which are a ready-made page (front page only), the theme-neutral starter (a hero, the post list, the post content…) or an empty layout. Only then are there sections and blocks to add and drag. **Use the theme's design** goes back to the template. A theme with its own front page (onepage) opens the builder on the front page, and its admin note links to the One-page preset.
- **Regions.** The header and the footer (on every page), and one main area per page type: the front page (a landing page, shown when **Settings → Reading** picks the theme's front page; see [Front pages](#front-pages-and-one-page-sites)), the latest posts, posts, pages, archives, search results and not found.
- **Sections** have a column layout (`1`, `1-1`, `2-1`, `1-2` or `1-1-1`; columns stack on small screens) and a style: background (none, muted, card, primary, accent, inverted), spacing, width (content, narrow, wide, full) and alignment.
- **CSS classes.** A section and an HTML block can have up to 5 class names (`[a-z][a-z0-9-]*`, separated by spaces), added to their element for the theme's custom CSS. The section with the footer credits takes none.
- **Anchors.** A section can have an anchor (`features` makes it `#features`): 1 to 40 lower-case letters, digits and hyphens, starting with a letter, unique in its region, and not `main`, `menu`, `primary-menu`, `comments`, `respond`, `search` or `sap-…`. A menu link or a button to `#features` scrolls to it, on the front page or, through `/#features`, from any other.
- **Presets.** An empty front page offers two ready-made pages: **Landing** (hero, features, testimonials, pricing and a call to action, with `#features`, `#pricing` and `#contact`) and **One-page site** (sections with anchors: `#intro`, `#about`, `#services`, `#testimonials`, `#faq`, `#contact`). Their texts are written in the site's main language, with translations for the others, and stay editable.
- **Limits.** 40 sections per region, 40 blocks per column, 600 blocks in all, and at most 30 items per landing block (6 plans, 8 figures, 12 features or testimonials, 24 logos).

| Block | Where |
|---|---|
| Site header: brand, menu, search, language switch, light/dark toggle | header, once |
| Menu, Search, Hero, Widget area, Text (Markdown), HTML, Image, Button, Spacer or divider, Social links | anywhere, the header and footer included |
| Breadcrumbs | posts, pages, archives, search, not found; once |
| Features, Stats, Testimonials, Pricing, Questions and answers, Logos, Call to action (the landing blocks, each with its list of items) | anywhere |
| Post list (grid, list or bento) | front page, home, archives, search; once |
| Post content | posts and pages; once, and required there |
| Post details, Featured image, Comments | posts and pages |
| Pagination | home, archives, search, posts; once |
| Footer line and credits | footer; always there |

- **Validation.** Every block is checked against its schema: settings are typed, links accept `https:`, `http:`, site paths, `#…`, `mailto:` and `tel:`, and appearance is always a named choice.
- **HTML block.** Up to 50,000 characters per language of the owner's own markup, sanitised when saved and again when rendered (`content.SanitizeHTMLBlock`, bluemonday). It keeps structural and text elements, links, images, lists, tables, `figure`, `details` and `summary`, inline SVG drawings such as icons (shapes, their geometry and stroke and fill colours, without `href`, `<use>`, `<image>`, animation, `foreignObject` or `url()` paints), `class` and `id`, a fixed set of `aria-*` attributes and `role`, and `data-*` attributes other than the ones the site's scripts act on (`data-video-*`, `data-form`, `data-icon`…). Scripts, `<style>`, `style=""`, event handlers, iframes, objects, forms and `javascript:` or `data:` links are removed: the CSP would block most of them anyway. Videos keep their own path (a Markdown link in a Text block). An option applies the theme's text styles (`prose`).
- **Edit as HTML.** The `</>` button on a section's row in Structure, the first button in its settings, or **Edit its section as HTML** in a block's settings open its markup, as the preview shows it, in a code editor (CodeMirror, served by sap13ns; also used for the HTML block and Custom CSS). Applying it turns the section into one HTML block with the same id, anchor, appearance and class; Undo brings the blocks back. The markup is copied as it renders now, so what changes by itself (post lists, menus, a network's prices) stops changing, and it goes through the HTML block's sanitiser. The section with the footer credits or the post's content cannot be converted. A section that is one HTML block already opens its own markup. A part that still uses the theme's own template offers **Start from the theme's HTML**: the main part of the template, as the preview shows it, becomes one full-width section holding it as an HTML block (not for the parts that hold the post's content or the credits).
- **Custom CSS** is the customiser's (see [The customiser](#the-customiser)), edited in the builder's **Custom CSS** panel too. It is saved into the design's draft, so the preview shows it. It is published and discarded with the rest of the draft.
- **Landing blocks.** Each item has its own fields: a feature's icon, title and text; a plan's name, price, period, description, features (one per line), button and highlight; a question and its answer, in Markdown through the content pipeline. Icons come from a fixed list of Lucide icons compiled into the binary and images from the media library. Pricing is written by hand and never reads a network's plans. An item without its main text (a question without an answer, a logo without an image…) is left out of the page.
- **Languages.** Text settings have a main value and an optional translation per site language. A missing translation shows the main value.
- **Drafts and publishing** work like the customiser's: a draft shows only in the preview, publishing makes it live, the last 10 published versions are kept, and the layouts travel in the customiser's export file.

**Falling back to the theme:**

- A page type without a layout uses the theme's template. Each page type is independent: a posts layout does not apply to pages.
- Without a header or footer layout, the theme's own header or footer stays.
- A posts or pages layout without the Post content block falls back to the theme's template, so the text is never lost.
- The assistant page keeps the theme's template, inside the header and footer layouts.

**The footer credit.** The **Footer line and credits** block renders `shared/footer-meta`, with the "Powered by sap13ns" attribution that the [CPAL-1.0 licence](../LICENSE) (Exhibit B) requires. It cannot be removed: saving a footer without it puts it back, and the footer renders it even if a stored layout lost it. Custom CSS could still hide it, and sap13ns does not try to stop that, but hiding the attribution breaks the licence. Keeping it visible is the operator's obligation.

**What a theme does to support it:**

- In `layout.html`, render `shared/blocks-header` when `.BlocksHeader` is set, instead of the theme's header and top bar. Likewise render `shared/blocks-footer` when `.BlocksFooter` is set, instead of the theme's footer. A theme whose header sits apart from its top bar can call `shared/blocks-top` and `shared/blocks-bar` separately.
- Import `shared.css`, which styles the blocks from the theme's Basecoat variables (`--background`, `--foreground`, `--primary`, `--muted`, `--border`, `--radius`…) and `--sap-content-width`.
- Optionally restyle blocks in `theme.css`, or redefine a `blocks/<type>` template in a partial.
- When no block of a main layout renders the page's `<h1>`, `shared/blocks-main` adds a visually hidden one.

A theme without the `BlocksHeader` and `BlocksFooter` checks still gets main-area layouts, which render as its `content`, but its own header and footer stay.

### Theme sections

A theme's front page written by hand is fixed for the page builder unless the theme splits it into **sections**: parts of a page with the theme's own markup, which the builder offers as blocks. The operator can then reorder them, remove them, repeat them, put the builder's blocks between them and edit each copy's texts.

```json
"sections": [
  {"id": "intro", "label": "Opening", "anchor": "intro", "icon": "panel-top", "fields": [
    {"key": "title", "label": "Title", "type": "text", "content": "intro-title"},
    {"key": "steps", "label": "Steps", "type": "lines", "default": "Listen\nAgree"},
    {"key": "more", "label": "Link", "type": "url", "default": "/about"},
    {"key": "photo", "label": "Photograph", "type": "image", "content": "intro-image"}
  ]},
  {"id": "contact", "label": "Contact", "where": ["front", "page"]}
],
"front_sections": ["intro", "contact"]
```

- **Templates.** Each section is `templates/sections/<id>.html` with `{{define "section/<id>"}}…{{end}}`. Its root has no `id`: the wrapper the site adds carries the anchor and the animation. The template receives the section: `{{.Text "title"}}`, `{{range .Lines "steps"}}`, `{{.URL "more"}}` (with the page's language prefix for a site path), `{{with .Image "photo"}}…{{else}}<the theme's image>{{end}}`, and `.Page` and `.Site` as in the theme's pages.
- **Fields.** `text`, `textarea`, `lines`, `image` and `url`. A field shows, in order: the text edited in that copy of the section (per language), else the theme text named by `content` (one of `content`'s keys of the same kind, edited in the customiser), else its `default`, translated by the theme's catalog. Up to 40 sections and 60 fields each.
- **Where.** `where` lists the regions a section can go in (`front`, `home`, `single`, `page`, `archive`, `search`, `404`); the default is the front page.
- **The theme's front page.** `front.html` renders `front_sections` in order, so the theme looks the same before and after the builder:

  ```
  {{define "content"}}{{range .Site.FrontSections $}}{{template "shared/theme-section" .}}{{end}}{{end}}
  ```

  In the builder, **The theme's own front page** is the first preset: those sections, with their anchors, ready to rearrange. The builder's form shows each field with what it says now; a field left as it is keeps following the theme and the customiser.

### Design with AI

With the site's AI provider on (Settings → AI provider) and for the roles it allows, the design screen has **Design with AI**: a chat whose answers change the draft. The model gets a guide to designs and layouts, this theme's catalogue (the builder's blocks with their fields, the theme's sections and texts, colour tokens, fonts, scales, animations), the current draft and the texts each language shows, and the site's latest images (Media), and answers with JSON. The easiest answer for a model is `html_regions`: a page (the front page, the posts page, the header, the footer, the archive, search and 404 pages) as a list of full-width sections written in HTML, each an HTML block with its translations, plus `css`, the site's complete custom CSS. Smaller changes are JSON merge patches. Everything is checked exactly like the editors' changes, and the HTML goes through the HTML block's sanitiser. What is refused goes back to the model, up to three tries; on the last one, what the checks still refuse is left out, the rest is saved to the draft, and the answer says so. It never publishes. A theme that declares its texts (`content`) and its sections (`sections`) gives the model much more to work with, as it does the owner.

## Animations

sap13ns has a small motion library of its own (`ui/static/motion.css`, played by `motion.js` in the site bundle): entrance animations that play once, when an element comes into view, and two that keep moving. Everything is served by the site, so it fits the [Content Security Policy](#content-security-policy).

| Name | Effect |
| --- | --- |
| `fade`, `fade-up`, `fade-down`, `fade-left`, `fade-right` | fades in, in place or moving from one side (`fade-left` comes from the right) |
| `zoom-in`, `zoom-out` | fades in while growing to its size, or shrinking to it |
| `blur-in` | comes into focus |
| `rise`, `pop`, `flip-up`, `tilt-in` | moves up from further below while growing, grows past its size and settles, turns up from its bottom edge, comes up slightly turned |
| `reveal-up`, `reveal-right`, `reveal-circle` | uncovered from the bottom, from the left, or from the centre in a circle |
| `float`, `pulse` | keep moving: a slow up and down, a slow grow and shrink |

**Where they come from.**
- **The page builder.** Every section has **Animation** and **Animate** (the whole section, or its blocks one after another), and every block has its own **Animation**. In a section whose blocks play one after another, the blocks play the section's animation.
- **The customiser.** **Texts and images** has an **Animation** for each of the theme's texts and images. **Animations → How much things move** sets the movement for the whole site: normal, subtle, expressive, or off. **Play the animations again** plays them again in the preview.
- **Templates.** A theme animates its own elements with attributes, and declares `"motion": true` in `theme.json` so that its pages load the library:

  ```html
  <h2 data-sap-animate="fade-up">…</h2>
  <ul data-sap-stagger="fade-up">…</ul>   <!-- each child, one after another -->
  <img data-sap-animate="zoom-in" data-sap-delay="200" …>   <!-- 200 ms later -->
  ```

**What visitors get.** Nothing moves for visitors who ask their device for reduced motion, nor in print, and without JavaScript every element just shows. A page only loads `motion.css` when something on it moves: a section or a block of its layout, a text or an image the customiser animates, a motion level other than normal, or a theme that declares `motion`.

**For theme CSS.** The level is a class on `<html>`: `sap-motion-subtle`, `sap-motion-expressive` or `sap-motion-off`. `sap-motion` is set when animations play. The library's sizes are custom properties a theme may override: `--sap-motion-duration`, `--sap-motion-distance`, `--sap-motion-scale`, `--sap-motion-blur`, `--sap-motion-angle` and `--sap-motion-ease`. An element waits on the first frame of its animation until it is in view, then gets the class `sap-in`.

## Checklist

- [ ] `theme.json` with menus, widget areas (including `top`), options, `tokens` and `swatches`.
- [ ] `layout.html`, `404.html` and a front page (`front.html`, `home.html` or both), plus the optional pages you need; the others use the shared fallbacks.
- [ ] For a one-page theme: sections with an `id` and `sap-anchor`, menu links through `.Site.Link`, and a style for `a[aria-current]`.
- [ ] `shared/head`, `shared/skip-link`, `shared/admin-bar`, `shared/footer-meta` and `shared/assistant` in the layout.
- [ ] Every visible string through `T`, every site link through `link`.
- [ ] Every `--sap-*` colour token mapped with fallbacks in `:root` and `.dark`, and the scales where they make sense.
- [ ] `.Site.Custom.Class` on `<html>`, the logo, the search and the home layout handled.
- [ ] `.BlocksHeader` and `.BlocksFooter` in the layout.
- [ ] No inline script or style, no external fonts or stylesheets.
- [ ] Light and dark checked at phone and desktop widths, with visible focus and reduced motion respected.
- [ ] A post with a video and with images floated left and right looks right ([Videos and images in posts](#videos-and-images-in-posts)).
- [ ] With the cookie consent on and a video in a post (in a private window, or with `sap13ns-consent` removed from local storage, to see the banner), the banner, the dialog and the video's note look right and stay readable ([Cookie consent](#cookie-consent)).
- [ ] `static/theme.css` built and committed.
- [ ] To upload it: zip the folder with only the files of [Uploading a theme](#uploading-a-theme) (no `node_modules`, scripts or SVG), and upload it to a test site first; the refusal lists every problem.
