# Themes

This is the contract between sap13ns and its themes: the folder layout, the manifest, the templates and the data they receive, the CSS pipeline and the design tokens, and the rules every theme follows. It also covers what operators can change without touching a theme: the customiser and the page builder.

- **Writing a theme?** Copy a built-in theme from `ui/themes/` and read [Folder layout](#folder-layout), [Templates](#templates) and [CSS and design tokens](#css-and-design-tokens).
- **Running a site?** Read [Installing a theme](#installing-a-theme), [Uploading a theme](#uploading-a-theme), [The customiser](#the-customiser) and [The page builder](#the-page-builder).

## Contents

1. [Folder layout](#folder-layout)
2. [Installing a theme](#installing-a-theme)
3. [Uploading a theme](#uploading-a-theme)
4. [theme.json](#themejson)
5. [Templates](#templates)
6. [Shared templates](#shared-templates)
7. [Template data](#template-data)
8. [Translations](#translations)
9. [CSS and design tokens](#css-and-design-tokens)
10. [Light and dark](#light-and-dark)
11. [Content Security Policy](#content-security-policy)
12. [Accessibility](#accessibility)
13. [The customiser](#the-customiser)
14. [The page builder](#the-page-builder)
15. [Checklist](#checklist)

## Folder layout

A theme is a folder named after its slug. The slug is 1 to 40 lower-case letters, digits and hyphens, and it starts with a letter or a digit.

```text
my-theme/
├── theme.json            manifest: name, menus, widget areas, options, colours
├── templates/
│   ├── layout.html       the page frame (required)
│   ├── home.html         latest posts (required)
│   ├── single.html       one post (required)
│   ├── page.html         one page
│   ├── archive.html      categories, tags, authors and dates
│   ├── search.html       search results
│   ├── 404.html          not found
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

`theme.json`, `templates/layout.html`, `templates/home.html`, `templates/single.html` and `static/theme.css` are required. Anything else is refused.

**Static files.** By extension and by content: `.css`, `.woff2`, `.woff`, `.png`, `.jpg`, `.jpeg`, `.webp`, `.avif`, `.gif` and `.ico`. Each file's content is sniffed with the media library's checks (`internal/media`) and must match its extension, so an HTML page cannot pass as a `.png`.

- **No scripts, pages or WebAssembly** (`.js`, `.mjs`, `.html`, `.xml`, `.wasm`, `.php` …). The site's own `site.js` runs the toggles and menus.
- **No SVG.** An SVG opened at its own URL is a document that can run scripts, and the CSP of the site's origin would be its only defence. Use PNG, WebP or AVIF, an `{{icon}}`, or a `data:image/…` URL in the stylesheet, which the CSP allows for images and which never runs when CSS draws it.
- **Stylesheets** pass the customiser's CSS checker, with three relaxations for compiled stylesheets: `url()` may be relative to the stylesheet (`fonts/x.woff2`) or a fragment, `data:image/…` URLs are allowed, and rules may name `.footer-meta`. Everything else holds: no `@import`, no URLs with a scheme or a host, no `image()`, `image-set()`, `expression()`, `behavior` or `-moz-binding`, balanced and terminated, UTF-8 without NUL bytes.

**theme.json.**

- Valid JSON with the fields of [theme.json](#themejson). The slug, the folder's name or `"slug"`, is a valid slug, the two agree when both are given, and it is not the slug of a built-in theme or of a network theme.
- `name` is required. Names, descriptions, labels, help and defaults have length limits and no control characters.
- Menu, widget area and option ids are 1 to 40 lower-case letters, digits, hyphens and underscores, each declared once; at most 10 menus, 20 widget areas, 40 options and 12 swatches.
- Option types are `text`, `textarea`, `select` or `bool`; a select has 1 to 50 choices and its default is one of them.
- Colours are hexadecimal; tokens are the customiser's.

**Translations.** `i18n/<lang>.json` is named after a language code (`es`, `pt-BR`), and holds a JSON object of strings. Formats in it take widths and precisions up to 99, and none computed at run time.

**Templates.** Every template file is UTF-8. The theme is parsed with the server's real functions and the shared templates, as it runs, and an unknown function or template fails. Then each page, as it renders (the layout with the page's templates and the partials), is checked with the types of the page data:

- **Functions.** The functions of [Template data](#template-data) except `seq`, and Go's built-in ones except `call`. The other functions of the server (the admin's helpers, `toJSON`) are refused. `printf`, `T` and `plural` take a literal format, with widths and precisions up to 99 and none computed at run time.
- **Fields.** Every field and method must exist on the data it is used on. Templates cannot read the signed-in user's CSRF token (`.CSRF`), nor write out whole values that hold it, and cannot call `.Site.Assistant`, which does work on the server; the shared templates render the forms and the assistant.
- **Loops.** `range` goes only over lists of the page data (`.Posts`, `.Site.Categories`, `index`, `slice` of them…), never over a number, a literal, a list the template makes with `list`, or the result of arithmetic. The page data is built by the server and bounded by the site's content, so every loop is.
- **No recursion.** A template cannot call itself, directly or through other templates, including shared ones.
- **Silent loops.** A loop whose passes may write nothing must stay cheap: at most 400 steps per pass, no function that reads a whole text (`contains`, `printf`…), and no other such loop inside it. A page that keeps writing is stopped by the output limit; one that writes nothing would not be.
- **Size.** Each page expands to at most 100,000 steps.
- **Protected templates.** A theme cannot redefine `shared/head`, `shared/footer-meta`, `shared/blocks-footer`, `shared/blocks-sections`, `shared/block` or `blocks/footer-meta`, nor name a template with `$`. It can still redefine the other shared templates.
- **The credit.** The layout must render `{{template "shared/footer-meta" .}}` on every page, on every path through its conditions (`shared/blocks-footer` renders it when the page builder's footer is on), with the page data, in the page's text: not in an attribute, a comment, a `<title>` or a `<textarea>`. `shared/admin-bar`, `shared/comments` and `shared/comment-form`, which carry the CSRF token, must be in the page's text too.
- **HTML.** No `<script>` (the JSON-LD of `shared/head` is the core's), no `<style>`, no `style=""`, no `on*=` event handlers, no `<meta http-equiv>`, no `<base>`, `<iframe>`, `<frame>`, `<frameset>`, `<object>`, `<embed>` or `<applet>`. A theme's own forms use `method="get"` only, and no `formaction`, `formmethod` or `form` attributes; the forms that change something are the core's. Actions cannot write the name of an element or of an attribute.
- **Escaping.** Each page is rendered once without data, so that `html/template` reports a context it cannot escape (an action in the middle of an attribute name, for example).

**Rendering limits.** An uploaded theme renders each page into a buffer of at most 8 MB, within 5 seconds, and stops when the visitor goes away. Text functions (`printf`, `print`, `join`, `paragraphs`, `T`…) refuse to make a text over 1 MB. The rendered page must hold the credit. A page over a limit is not sent: the visitor gets a 500, and the log says `uploaded theme refused to render` with the theme and the page. Built-in and hand-installed themes render as before.

**What the checks do not decide.** The stylesheet can still hide the credit, as custom CSS can; the CPAL-1.0 licence requires it to stay visible, and the site answers for that. Design, accessibility and the content of the theme's own texts are the author's.

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

| Page | Used for | Without it |
|---|---|---|
| `home` | the latest posts (`/` and `/blog`) | required |
| `single` | one post | required |
| `page` | one page, including a static front page | `single` |
| `archive` | category, tag, author and date archives | `home` |
| `search` | search results | `home` |
| `404` | not found and gone | `shared/not-found` inside the layout |
| `ask` | the site assistant without JavaScript | `shared/ask` inside the layout |

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

## Shared templates

`ui/shared/templates` holds the parts every site needs. Call them with `{{template "shared/<name>" <data>}}`.

| Template | Data | Renders |
|---|---|---|
| `shared/head` | PageData | everything inside `<head>`: title, description, canonical and `hreflang` links, Open Graph, feeds, JSON-LD, the scheme script, `theme.css`, the customiser's stylesheet, plugin assets and `site.js` |
| `shared/skip-link` | none | the "Skip to content" link to `#main` |
| `shared/admin-bar` | PageData | the floating toolbar for signed-in editors |
| `shared/menu` | `[]MenuLink` | `<li>` items of a menu |
| `shared/nav-toggle` | the id of the element it opens | the small-screen menu button |
| `shared/theme-toggle` | none | the light/dark switch |
| `shared/lang-switch` | PageData | links to this page in the other languages (nothing on single-language sites) |
| `shared/logo` | PageData | the customiser's logo (nothing without one) |
| `shared/widget-area` | `dict "Page" . "Area" "<id>"` | the widgets of an area, in `.widget-area.widget-area-<id>` |
| `shared/footer-meta` | PageData | the footer line with the **"Powered by sap13ns"** credit |
| `shared/post-meta`, `shared/categories`, `shared/tags` | PostView | date, author and reading time; term links |
| `shared/image` | `dict "Image" "Sizes" "Eager" "Class"` | a responsive `<img>` with `srcset` |
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
| `.Kind` | `home`, `single`, `page`, `archive`, `search`, `404` or `ask` |
| `.Title`, `.Heading`, `.Description` | the page's title, its visible heading and its description |
| `.IsFront` | the front page (latest posts or a static front page) |
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
| `.URL`, `.Home`, `.Search` | the absolute home URL, and the home and search paths in this language |
| `.Menu "<id>"` | a menu location's links (`.Label`, `.URL`, `.Active`, `.External`) |
| `.Options` | the theme options (`index .Site.Options "<key>"`) |
| `.Categories`, `.Tags`, `.Pages`, `.Archives`, `.Stats` | lists and counts for navigation and widgets; a term or month has `.Current` on its own archive (use `aria-current="page"`) |
| `.FeedRSS`, `.FeedAtom`, `.FeedJSON` | feed URLs |
| `.FooterText` | the customiser's footer line, empty when not set |
| `.Viewer` | the signed-in user, nil for visitors |
| `.Custom` | the customiser's settings (see [The customiser](#the-customiser)) |
| `.Asset "<file>"` | the hashed URL of a file in the theme's `static/` |
| `.Static "<file>"` | the hashed URL of a core static file |

**A post** (`.Post`, each of `.Posts`) has `.Title`, `.URL`, `.Summary`, `.Content` (safe HTML), `.Published`, `.HasDate`, `.Updated`, `.Author` (`.Name`, `.URL`), `.Categories`, `.Tags`, `.Image`, `.ReadingMinutes`, `.CommentCount` and `.CommentsOpen`. An image (`.Image`) has `.URL`, `.Srcset`, `.Width`, `.Height`, `.Alt`, `.Caption`, and the `.Thumb`, `.Medium` and `.Large` variants.

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

- **Before the first paint**, the core's `theme-init.js` adds the `dark` class to `<html>` from the visitor's saved choice, or else from `prefers-color-scheme`. `?scheme=dark|light` forces one mode for one view.
- **The toggle** (`shared/theme-toggle`) switches the class and remembers the choice. With no saved choice, the page follows the system setting as it changes.
- **Define light values on `:root` and dark values on `.dark`.** Use Tailwind's `dark:` variant for anything else. `shared/head` already sets `<meta name="color-scheme" content="light dark">`.
- **Check contrast in both modes.** Body text, muted text, links, buttons and focus rings must stay readable on every surface.

## Content Security Policy

Public pages are served with a strict policy:

```text
default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: https:;
media-src 'self' https:; font-src 'self'; connect-src 'self'; frame-src 'none'; frame-ancestors 'none'; …
```

- **No inline scripts.** No `<script>` blocks with code, and no `onclick` or other event attributes. (`<script type="application/ld+json">` from `shared/head` is data, not code.)
- **No inline styles.** No `<style>` elements and no `style=""` attributes. Every look is a class in `theme.css`.
- **No JavaScript of its own.** The core's `site.js` handles the colour toggle, the small-screen menu (it toggles `.is-open` on the element a `shared/nav-toggle` controls, and `nav-open` on `<html>`; Escape, a click outside, following a menu link or growing to the width where the toggle is hidden close it), the language switch and comment replies.
- **No external fonts or stylesheets.** Fonts and styles are same-origin. Images and media may be external (`https:`).

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

**Appearance → Customise** (or **Customise** on a theme's card) changes a theme's look without editing it. Each theme has its own design.

| Panel | What the operator can change |
|---|---|
| Colours | the eleven colour tokens, separately for light and dark, with contrast warnings (WCAG AA) and dark colours derived from the light ones on request |
| Fonts | body, headings and code: a curated system stack or a WOFF2 file from the media library |
| Size and spacing | text size (90 % to 120 %), corner radius, density, shadows, content width |
| Header | the brand (title, logo, or logo and title), a centred brand, a sticky header, hiding the search link and the language switch |
| Footer | the footer widget columns (1 to 4) and a footer text, per language |
| Home page | the large introduction, the post layout (list, grid or bento) and the posts per page |
| Theme options | the options from `theme.json` |
| Custom CSS | up to 32 KB of extra rules, served as a separate file after the theme's styles |

- **Drafts.** Every change is saved as a draft and shown in the live preview (desktop or mobile, light or dark). Only users with the appearance capability see drafts, through `?customize=draft`. Visitors keep the published design.
- **Publish, discard, reset.** Publishing makes the draft live and writes its theme options to the theme's stored options. Discard drops the draft. Reset starts a draft with nothing customised, so publishing it restores the theme's own look.
- **Revisions.** The last 10 published versions are kept. Restoring one copies it into the draft.
- **Export and import.** The export is one JSON file with the design, the theme options in effect and the page builder's layouts. Importing validates the whole file before changing anything, then replaces the drafts of what the file carries: the design, the layouts, or both.
- **Inactive themes** can be customised too. What is published applies when the theme is activated.
- **Validation.** Colours must be hex values, every other setting is a named choice, and fonts and logos must be files in the media library. Custom CSS may not use `@import`, external `url()`, functions that fetch resources or legacy script hooks, and it may not name `footer-meta`. Only users with the appearance capability can upload WOFF2 fonts.

**What a theme does to support it:**

- Map the `--sap-*` variables, with fallbacks, and declare `tokens` in `theme.json`.
- Put `.Site.Custom.Class` on `<html>`. It holds `sap-sticky-on` or `sap-sticky-off`, `sap-brand-center` and `sap-has-logo`, and the theme's CSS reacts to them.
- In the header, show `shared/logo` when `.Site.Custom.Logo` is set, hide the title when `.Site.Custom.LogoOnly` is true, and leave out the search link when `.Site.Custom.HideSearch` is true. `shared/lang-switch` handles `HideLang` itself.
- On the home page, render `shared/post-list` with `.Site.Custom.HomeLayout` when it is set. Read the introduction setting from a `hero` option, which the customiser overrides, or with `.Site.Custom.HeroOn <the theme's default>`. The posts per page are applied by the server.
- Use `shared/footer-meta` and `shared/widget-area` for the footer, so the footer text and the columns apply.

## The page builder

**Appearance → Page builder** replaces a theme's regions with layouts made of sections, columns and blocks. Each theme has its own layouts.

- **Regions.** The header and the footer (on every page), and one main area per page type: home, posts, pages, archives, search results and not found.
- **Sections** have a column layout (`1`, `1-1`, `2-1`, `1-2` or `1-1-1`; columns stack on small screens) and a style: background (none, muted, card, primary, accent, inverted), spacing, width (content, narrow, wide, full) and alignment.
- **Limits.** 40 sections per region, 40 blocks per column, 600 blocks in all.

| Block | Where |
|---|---|
| Site header: brand, menu, search, language switch, light/dark toggle | header, once |
| Menu, Search, Hero, Widget area, Text (Markdown), Image, Button, Spacer or divider, Social links | anywhere |
| Breadcrumbs | posts, pages, archives, search, not found; once |
| Post list (grid, list or bento) | home, archives, search; once |
| Post content | posts and pages; once, and required there |
| Post details, Featured image, Comments | posts and pages |
| Pagination | home, archives, search, posts; once |
| Footer line and credits | footer; always there |

- **Validation.** Every block is checked against its schema: settings are typed, links accept `https:`, `http:`, site paths, `#…`, `mailto:` and `tel:`, and appearance is always a named choice.
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

## Checklist

- [ ] `theme.json` with menus, widget areas (including `top`), options, `tokens` and `swatches`.
- [ ] `layout.html`, `home.html` and `single.html`, plus the optional pages you need.
- [ ] `shared/head`, `shared/skip-link`, `shared/admin-bar`, `shared/footer-meta` and `shared/assistant` in the layout.
- [ ] Every visible string through `T`, every site link through `link`.
- [ ] Every `--sap-*` colour token mapped with fallbacks in `:root` and `.dark`, and the scales where they make sense.
- [ ] `.Site.Custom.Class` on `<html>`, the logo, the search and the home layout handled.
- [ ] `.BlocksHeader` and `.BlocksFooter` in the layout.
- [ ] No inline script or style, no external fonts or stylesheets.
- [ ] Light and dark checked at phone and desktop widths, with visible focus and reduced motion respected.
- [ ] `static/theme.css` built and committed.
- [ ] To upload it: zip the folder with only the files of [Uploading a theme](#uploading-a-theme) (no `node_modules`, scripts or SVG), and upload it to a test site first; the refusal lists every problem.
