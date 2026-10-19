# Importing themes from other CMSs

sap13ns converts **WordPress block themes** into sap13ns themes, so that a site can start from a design it already knows instead of from scratch. The converted theme is an ordinary uploaded theme: it goes through every check of [Uploading a theme](THEMES.md#uploading-a-theme), it can be previewed, customised and deleted like any other, and it never runs anything of the original.

This page says what is converted, with what fidelity, what is not and why, how it maps WordPress to sap13ns, and how the importer keeps a hostile theme harmless. The last section looks at the other CMSs: Ghost, Hugo and Jekyll.

1. [Using it](#using-it)
2. [What is converted](#what-is-converted)
3. [What to expect: fidelity](#what-to-expect-fidelity)
4. [What is not converted, and why](#what-is-not-converted-and-why)
5. [Mappings](#mappings)
6. [Colours and dark mode](#colours-and-dark-mode)
7. [Security model](#security-model)
8. [Licences and credits](#licences-and-credits)
9. [Other CMSs](#other-cmss)
10. [What to build next](#what-to-build-next)

## Using it

- **In the admin.** In **Appearance → Themes**, upload the theme's `.zip` as you would a sap13ns theme (the one wordpress.org offers for download works as it is). The upload is recognised as a WordPress block theme and converted, and nothing is installed yet. The page shows the report instead: what was converted, the menu locations it made, and every block, file or rule it changed or left out. **Install the converted theme** then installs it, not activated, through the same checks, plan gate (`theme_uploads`) and storage quota as any upload; **Cancel** drops it. The conversion waits 30 minutes, for the user who uploaded it only; a site keeps at most two. The admin takes archives up to 20 MB, the limit of every theme upload; the command line takes up to 32 MB, since a WordPress archive carries fonts and screenshots the conversion leaves out (the converted theme must still fit the 20 MB of an uploaded theme).
- **On the command line.** `sap13ns theme import [-site S] [-slug NAME] theme.zip` converts, prints the report and installs. `-o converted.zip` only writes the converted theme, which `sap13ns theme install` installs later, on this site or another. `sap13ns theme install` refuses a WordPress theme and points to `import`.
- **The result.** The theme is named `wp-<folder>` (`wp-twentytwentyfive`), so it never clashes with a sap13ns theme. Its theme list entry says "Converted from WordPress". It has a **Colour style** option when the original has style variations, a menu location for each navigation block (`primary`, `footer`, …), and its README.md and LICENSE.txt carry the original's credits and licence.
- **Other kinds of theme.** A classic WordPress theme (PHP templates), a Ghost, Hugo or Jekyll theme is recognised and refused with the reason, before anything is unpacked. See [Other CMSs](#other-cmss).

The code is `internal/themeimport`; `Convert` is the whole conversion and `Detect` the recognition.

## What is converted

A block theme ([Block themes](https://developer.wordpress.org/themes/block-themes/)) is HTML templates made of blocks, a `theme.json` of settings and styles ([theme.json reference](https://developer.wordpress.org/block-editor/reference-guides/theme-json-reference/theme-json-living/)), template parts, patterns, fonts and images. The importer reads all of them as data.

| WordPress | sap13ns |
|---|---|
| `templates/index.html`, `home.html`, `front-page.html`, `single.html`, `page.html`, `archive.html` (or `category`, `tag`…), `search.html`, `404.html` ([template hierarchy](https://developer.wordpress.org/themes/basics/template-hierarchy/)) | `templates/home.html`, `front.html`, `single.html`, `page.html`, `archive.html`, `search.html`, `404.html`; a missing one falls back as in WordPress (`single` → `singular` → `index`) |
| The leading header parts and trailing footer parts of the templates ([template parts](https://developer.wordpress.org/themes/templates/template-parts/)) | `templates/layout.html`: the header and footer most pages use; a page with other parts gets them through an `if .Kind` chain |
| `patterns/*.php` ([patterns](https://developer.wordpress.org/themes/patterns/)) | Written in place where a template or part names them. Their PHP is not run: [a small evaluator](#php-in-patterns) prints the literal text of translation and escaping calls |
| `theme.json` presets: colours, gradients, font sizes (fluid), font families, spacing scale, shadows, `custom` | Custom properties (`--wp--preset--color--base`…) and the preset classes (`.has-base-color`…) |
| `theme.json` styles: root, elements (links, headings, buttons, captions), blocks, block variations, `css` | Rules, as WordPress writes them (`:root :where(…)`) |
| `styles/*.json` with a palette ([style variations](https://developer.wordpress.org/themes/global-settings-and-styles/style-variations/)) | Choices of the **Colour style** option: `:root.wp-style-<slug>` rules and token overrides |
| `styles/*.json` with `blockTypes` ([section styles](https://developer.wordpress.org/themes/features/block-style-variations/)) | `is-style-<slug>` rules |
| `style.css` (after its header) | Appended to theme.css, cleaned like every other CSS |
| Fonts named in `fontFace` (`file:./assets/fonts/…`) | `static/fonts/`, `@font-face` rules; only the families the styles use |
| Images the templates and patterns use | `static/images/` (PNG, JPEG, WebP, AVIF, GIF, sniffed), or an inline `data:` URI for a plain SVG |
| `style.css` header, `readme.txt`, `LICENSE` | `theme.json` name, version, author, licence; README.md with the credits; LICENSE.txt |

Every block of the theme is mapped ([Blocks](#blocks)). A block sap13ns does not know keeps the HTML it saved, cleaned; one without saved HTML is dropped. Both are named in the report.

## What to expect: fidelity

The six themes below were converted, installed on a copy of the development site and compared with their previews on wordpress.org (wp-themes.com) at 1440 and 390 pixels. The comparison is of layout, type and colour on the same pages (home, a post, an archive), with sap13ns's own sample content.

All 72 pages (6 themes, 3 pages, 2 widths, light and dark) loaded under the site's strict CSP with no console error, no CSP violation and no sideways scroll at 390 pixels.

| Theme | Fidelity | What differs |
|---|---|---|
| **Twenty Twenty-Five** 1.5 | High: header, list, post and footer match the original's layout, type (Manrope) and spacing | Nothing visible beyond the site's own content |
| **Twenty Twenty-Four** 1.6 | High: the business front page (hero, rounded images, the asterisk and checkmark styles from `functions.php`, the service grid) matches the preview closely | The footer's "Privacy" and "Social" columns stay empty until menus are assigned to their locations; in dark mode the black testimonial band becomes light grey (its colour is inverted with the rest) |
| **Twenty Twenty-Three** 1.7 | High: the three-column post grid, the system font stack and the type scale match | The excerpt-to-date gap is a little tighter |
| **Ollie** 1.6.5 | High: the card grid, colours and type match | The menu is centred in the header (the language and dark-mode controls take the right end); social links are text, not icons |
| **Extendable** 2.1.9 | High: list, title sizes, uppercase dates and footer match | The site title link is underlined |
| **Raft** 1.1.16 | Medium: colours, header and footer bands, centred titles and the post layout come over | Its front page is made of patterns `functions.php` registers, so the site shows the blog instead; its body font is TrueType (left out, so the system sans-serif shows); its front-end stylesheet is enqueued from PHP (left out: on phones the header stacks its menu and social links, and post text has no side padding) |

What makes a converted theme look like the original is the theme.json: the palette, the type scale, the spacing and the layout widths come over exactly, because sap13ns writes the same custom properties and the same rules WordPress does. What makes it differ is what the theme shows that sap13ns does not have (WooCommerce, a site editor's custom templates), and what its PHP does (registering patterns, enqueueing stylesheets), which is never run.

## What is not converted, and why

- **PHP.** Nothing of the theme's PHP runs, ever: not `functions.php`, not the patterns, not includes. A theme's PHP can do anything, and running it would make every theme upload a code execution. The consequence: whatever `functions.php` does is lost. Themes that register their patterns there (Raft registers them from `inc/patterns`, as PHP arrays) lose those patterns, and the report names each one a template asks for. Stylesheets enqueued from PHP (per-block styles in `assets/css/blocks/*.css`) are not loaded either. One exception, because it is so common: the block styles `functions.php` registers with `register_block_style( 'core/list', array( 'name' => …, 'inline_style' => '…' ) )` (Twenty Twenty-Four's checkmark list and asterisk heading, Twenty Twenty-Five's checkmark list) keep their CSS. A scanner (`blockstyles.go`) reads the call's string literals as text, without running anything, and the CSS is cleaned like `style.css`; a style whose CSS is not a plain literal is left out.
- **Classic themes.** A classic theme ([classic themes](https://developer.wordpress.org/themes/classic-themes/)) is PHP templates (`index.php`, `single.php`, the loop): its markup only exists once PHP runs. There is nothing to convert without running it, so the upload is refused with that reason.
- **Custom templates.** Templates outside the hierarchy (`page-no-title.html`, `blank.html`), which an editor picks per page in WordPress, have no counterpart: a sap13ns page uses its kind's template. The report lists them.
- **Typography variations, and the type of colour variations.** A style variation becomes a choice of colours (its palette, background and text). Its fonts, sizes and spacing are not taken: a theme option switches CSS classes on `html`, and switching the whole type scale with them would need every rule of the theme twice.
- **Content of the site that sap13ns does not have.** WooCommerce (templates and blocks, dropped together), query loops of a hand-picked set of posts (they show the page's posts: `Posts` is all a template sees), comment templates (the site's own comments are used, which follow its moderation), the post author's biography, the calendar and archives widgets, embeds.
- **Social icons.** The social links block keeps its links, with the service's name for text: sap13ns bundles Lucide icons, which has no brand logos, and loading logos from elsewhere is not allowed. Mail and feed links get Lucide icons.
- **Fonts from elsewhere, and TrueType fonts.** A font loaded from another server (Google Fonts, a CDN) is left out; the theme falls back to the next family of its stack. sap13ns themes are self-hosted (the CSP has no font source but the site). An uploaded theme may only carry WOFF2 and WOFF fonts, so a `.ttf` face is left out too (Raft's Readex Pro: its text falls back to the system sans-serif). Converting TrueType to WOFF2 needs a Brotli encoder, which the binary does not have.
- **Child themes.** A child theme converts with its own files; the parent is not included, so templates it inherits are missing. The report says so.
- **The site editor's changes.** Changes made in WordPress's site editor live in that site's database, not in the theme's files. The importer converts the theme as it was shipped.

## Mappings

### Blocks

The block grammar is HTML with comment delimiters ([key concepts](https://developer.wordpress.org/block-editor/explanations/architecture/key-concepts/)). The parser (`blocks.go`) follows WordPress's own: freeform HTML between blocks, a closer that does not match closes the blocks opened after its opener, blocks left open close at the end. Static blocks keep their saved HTML, with the layout classes WordPress adds when it renders them ([block supports](https://developer.wordpress.org/block-editor/reference-guides/block-api/block-supports/)); dynamic ones are rendered from their attributes ([core blocks](https://developer.wordpress.org/block-editor/reference-guides/core-blocks/)).

| Block | Becomes |
|---|---|
| `core/group`, `columns`, `column`, `buttons`, `button`, `details`, `quote`, `pullquote`, `social-links` | The saved HTML plus `is-layout-flow\|constrained\|flex\|grid` and a generated class (`wpl-…`) for the block's own gap, widths and alignment |
| `core/cover`, `image`, `media-text`, `gallery`, `heading`, `paragraph`, `list`, `list-item`, `separator`, `spacer`, `table`, `code`, `preformatted`, `verse`, `html` and the other static blocks | The saved HTML, cleaned; `style=""` becomes a generated class (`wpi-…`); images of the theme are bundled, others dropped |
| `core/template-part` | The part, in place; in the header or footer it goes to the layout |
| `core/pattern` | The pattern, in place (cycles stop) |
| `core/site-title` | `<pN class="wp-block-site-title"><a href="{{link "/"}}">{{$.Site.Title}}</a>` (hidden by the customiser's **Logo only**) |
| `core/site-tagline` | `{{with $.Site.Tagline}}…{{end}}` |
| `core/site-logo` | `{{template "shared/logo" $}}` when the site has a logo |
| `core/navigation` | `{{template "shared/menu" ($.Site.Menu "primary")}}` and the shared mobile toggle; each navigation block is a menu location of the manifest (`primary`, `footer`, `footer-2`…) |
| `core/page-list` | `{{template "shared/menu" $.Site.Pages}}` |
| `core/search` | `{{template "shared/search-form" $}}` (hidden by the customiser's **Hide the search link**) |
| `core/loginout` | A link to `{{$.Site.LoginURL}}` for visitors |
| `core/categories`, `tag-cloud`, `latest-posts` | `range` over `$.Site.Categories`, `$.Site.Tags`, `$.Posts` |
| `core/social-link` | A link with the service's name; unsafe URLs dropped |
| `core/query` + `core/post-template` | `{{range $.Posts}}<li class="wp-block-post">…{{end}}`; `perPage`/`offset` of a non-inherited query become a bounded `range $i, $p` |
| `core/query-pagination` (+ previous, numbers, next) | `{{with $.Pagination}}` with `PrevURL`, `Links` and `NextURL` |
| `core/query-no-results` | `{{if not $.Posts}}…{{end}}` |
| `core/query-title`, `term-description` | `$.Archive.Title` / `$.Heading`, `$.Archive.Description` |
| `core/post-title`, `post-content`, `post-excerpt`, `post-date`, `post-featured-image`, `post-author`, `post-author-name`, `avatar`, `post-terms`, `read-more`, `post-time-to-read` | `.Title`, `.Content` (with the `prose` class), `.Summary`, `.Published`, `shared/image`, `.Author`, `.Categories`/`.Tags`, `.URL`, `.ReadingMinutes`, on `.` inside a post template and on `$.Post` in single and page |
| `core/post-navigation-link` | `{{with $.Prev}}` / `{{with $.Next}}` |
| `core/comments`, `post-comments-form`, `comments-query-loop` | `{{template "shared/comments" $}}`, in single and page only |
| `woocommerce/*` | Dropped, with the WooCommerce templates |
| Any other block | Its saved HTML, cleaned, or nothing; both reported |

### theme.json to CSS

| theme.json | theme.css |
|---|---|
| `settings.color.palette[]` | `--wp--preset--color--<slug>`; the background, text and accent presets read the customiser's variables first: `var(--sap-light-background, #fbfaf7)` |
| `settings.color.gradients[]` | `--wp--preset--gradient--<slug>`, `.has-<slug>-gradient-background` |
| `settings.typography.fontSizes[]` (and `fluid`) | `--wp--preset--font-size--<slug>`, as `clamp()` when fluid, with WordPress's formula |
| `settings.typography.fontFamilies[]` | `--wp--preset--font-family--<slug>`, `@font-face` for the faces in the theme |
| `settings.spacing.spacingSizes[]` / `spacingScale` | `--wp--preset--spacing--<slug>` |
| `settings.shadow.presets[]` | `--wp--preset--shadow--<slug>` |
| `settings.custom` | `--wp--custom--<path>` |
| `settings.layout.contentSize` / `wideSize` | `--wp--style--global--content-size` / `--wide-size` |
| `settings.useRootPaddingAwareAlignments` | `.has-global-padding` and the root padding rules |
| `styles.color`, `typography`, `spacing` | `body{…}` and `--wp--style--root--padding-*`; the body font reads `var(--sap-font-body, …)` |
| `styles.elements.link\|h1…h6\|button\|caption` (and `:hover`, `:focus`) | `:root :where(a:where(:not(.wp-element-button))){…}` and so on |
| `styles.blocks.<name>` (and its `elements`, `variations`, `css`) | `:root :where(.wp-block-<name>){…}`; a feature the block's block.json `selectors` place elsewhere goes there (an image's border and shadow on its `img`) |
| `styles.css` | Appended, cleaned |
| `var:preset\|color\|base` values | `var(--wp--preset--color--base)` |

### Template helpers of other CMSs

These are the mappings a Ghost or Hugo converter would use ([Other CMSs](#other-cmss)); none is built yet.

| Ghost (Handlebars, [helpers](https://ghost.org/docs/themes/helpers/)) | Hugo ([functions](https://gohugo.io/functions/)) | sap13ns |
|---|---|---|
| `{{@site.title}}`, `{{@site.description}}` | `.Site.Title`, `.Site.Params.description` | `{{.Site.Title}}`, `{{.Site.Tagline}}` |
| `{{#foreach posts}}…{{/foreach}}` | `range .Pages` / `.Paginator.Pages` | `{{range .Posts}}…{{end}}` |
| `{{title}}`, `{{content}}`, `{{excerpt}}` | `.Title`, `.Content`, `.Summary` | `{{.Title}}`, `{{.Content}}`, `{{.Summary}}` |
| `{{url}}`, `{{img_url feature_image size="m"}}` | `.RelPermalink`, `.Resources.GetMatch` + `.Resize` | `{{.URL}}`, `{{template "shared/image" …}}` (srcset from the media library) |
| `{{date format="D MMM YYYY"}}` | `.Date.Format "2 Jan 2006"` | `{{date .Published}}` (the site's language) |
| `{{#primary_author}}{{name}}{{/primary_author}}` | `.Params.author` | `{{.Author.Name}}` |
| `{{#foreach tags}}`, `{{primary_tag}}` | `.GetTerms "tags"` | `{{range .Tags}}`, `{{range .Categories}}` |
| `{{navigation}}` | `range .Site.Menus.main` | `{{template "shared/menu" (.Site.Menu "primary")}}` |
| `{{pagination}}` | `template "_internal/pagination.html" .` | `{{with .Pagination}}…{{end}}` |
| `{{ghost_head}}`, `{{ghost_foot}}` | `partial "head.html"` | `{{template "shared/head" .}}`, `{{template "shared/footer-meta" .}}` |
| `{{asset "css/screen.css"}}` | `resources.Get` + Hugo Pipes ([pipes](https://gohugo.io/hugo-pipes/)) | `{{.Site.Asset "theme.css"}}` (Tailwind, built ahead) |
| `{{t "Read more"}}` | `i18n "readMore"` | `{{T "Read more"}}`, the theme's `i18n/<lang>.json` |
| `{{#is "home"}}`, contexts ([contexts](https://ghost.org/docs/themes/contexts/)) | `.IsHome`, `.Kind` | `{{if .IsFront}}`, `{{.Kind}}` |
| `{{#get "posts" filter="…"}}` | `where .Site.RegularPages "Section" "blog"` | No counterpart: a template shows its page's posts |
| `{{> "partial"}}`, `{{!< default}}` | `partial`, `baseof.html` + `block` | `{{template "name" .}}`, `layout.html` + `{{define "content"}}` |

## Colours and dark mode

Every sap13ns theme works in light and dark mode, and the customiser changes its eleven colour tokens (`--background`, `--foreground`, `--primary`, `--muted`, `--border`, …). A WordPress theme has a palette and one mode. The importer (`colours.go`, `color.go`):

1. **Finds the roles.** The background and text are `styles.color.background` and `styles.color.text` (else the presets `base`/`background` and `contrast`/`foreground`); the accent is the links' colour, else the buttons' background, else a preset named `primary`, `accent` or `accent-1`.
2. **Wires them to the customiser.** Those presets read the customiser's variables first: `--wp--preset--color--base: var(--sap-light-background, #fbfaf7)`. Changing the background in the customiser changes every block that uses the preset. The other tokens are computed: muted and card are the palette colour nearest the background (a tint of it), else a 5% blend towards the text; muted text is a blend that keeps 4.5:1 contrast; the border a 16% blend.
3. **Derives the dark palette.** Each colour of the palette is mapped in OKLCH: its lightness is inverted and compressed into [0.15, 0.96] (white becomes a lightness of 0.15, black 0.96), its hue kept, and its chroma reduced by 15% when a light tint turns dark. A vivid colour (OKLCH chroma of 0.1 or more, a link or an accent) keeps a lightness of at least 0.62, about 5:1 on the dark background: a mid-tone accent would otherwise keep its lightness under the inversion and lose its contrast. Text that contrasted with its background keeps contrasting, accents keep their hue, and pale section backgrounds become dark ones instead of glowing. The same mapping is applied to the literal colours of the theme's CSS (a rule with `#fff` gets a `:root.dark` twin with its mapped colour), and to every style variation.
4. **The other way round.** When the theme's own background is dark (lightness under 0.5), its palette is the dark one and the light palette is derived with the inverse mapping. A dark style variation is dark in both modes.

The derived dark mode is an approximation: it keeps the theme's hierarchy and contrast, not a designer's hand-picked dark palette. A theme can be adjusted afterwards in the customiser, whose dark colours override the derived ones.

## Security model

Every byte of an imported theme is treated as hostile, and the importer adds nothing to what an uploaded theme may do: its output passes the same checks (`internal/theme`) as a theme written by hand. On top of them, the importer:

- **Never runs foreign code.** No PHP, no JavaScript, no build step. <a id="php-in-patterns"></a>The PHP of patterns ([using PHP in patterns](https://developer.wordpress.org/themes/patterns/using-php-in-patterns/)) is read by a lexer and an evaluator (`php.go`) that only knows string literals, concatenation, text variables (`$label = __( '…' );`) and pure functions: translation (`__`, `esc_html_e`…), escaping, `sprintf`, the theme's URL, `home_url('/')`, `bloginfo('name')` and `date('Y')`. Any other segment (a call to anything else, a loop, an include) is left out whole and counted in the report. The evaluator has bounds on tokens, depth, variables and output. The only thing read from `functions.php` is the string literals of `register_block_style` calls (`blockstyles.go`): a block name and a style name that must match strict patterns, and CSS that is cleaned like any other.
- **Stays inside the archive.** The archive is opened with the shared extraction limits (`internal/archive`: 32 MiB, 3,000 entries, 128 MiB unpacked, a compression ratio of 100, no symbolic links, no paths that leave it, hidden files skipped), each file is read with its own limit, and nothing is written to disk: the result is a new archive in memory, which `theme.Manager.Install` validates and unpacks as any upload.
- **Keeps only known files.** Templates, parts and patterns become sap13ns templates; fonts must be sniffed as WOFF or WOFF2 with a header whose length matches the file; images are sniffed (`internal/media`) and only PNG, JPEG, WebP, AVIF and GIF are copied; an SVG is kept only as a `data:` image, and only when it has no script, foreign object, entity, link or handler. Everything else stays behind.
- **Rewrites HTML through an allowlist** (`html.go`). Elements outside a list of plain content elements are dropped, `script`, `style`, `iframe`, `form`, `svg`, `math`, `template` and the like with their content. Attributes are a short allowlist; `on*` handlers never pass; `style=""` becomes a generated class, its declarations checked one by one; `href` and `src` must be a path of the site, an anchor or an `http(s)`, `mailto` or `tel` URL; links to the theme's own files become `{{.Site.Asset}}`. The classes of sap13ns's own markup (`sap-*`, `footer-meta`, `skip-link`) cannot be claimed.
- **Cannot write template actions.** Every `{` of the theme's text and attributes is written as `&#123;`, so no `{{` reaches the template parser. The evaluator's placeholders (the site's title, the year, the home URL) are private-use characters that are stripped from every foreign text first and bound to a random nonce for each conversion, so a theme cannot forge them, not with the characters themselves, not with their entities, not in a PHP literal; a pattern that is not valid UTF-8 is left out.
- **Cleans every piece of CSS** (`css.go`). theme.json values pass a strict check: no `{ } ; < > \ " ' ! @` or comments, only known functions (`var`, `calc`, `clamp`, the colour functions, gradients…), so a value cannot end its declaration, load a file or call `url()`/`image-set()`. Font family names are checked on their own. The `css` strings, the style sheets and the theme's `style.css` go through `cleanCSS`: `@import`, `@font-face` from elsewhere, `@-moz-document` and other at-rules are dropped, `url()`s are resolved to the theme's bundled files or dropped, a `css` string nested under its block cannot close it, a statement that is not a whole rule or at-rule (or holds `</` or `<!`) is dropped, and each statement passes `customize.CheckCSS`. The assembled theme.css passes `CheckCSS` once more as a whole.
- **Bounds the work.** The block parser runs in linear time (a megabyte of unclosed delimiters parses in milliseconds), with limits of 20,000 blocks and a depth of 64; templates are capped at 250 KiB, theme.css at 1 MiB, the result at 18 MiB and 280 files.
- **Leaves the decisions to the site.** In the admin a conversion is only a report until the administrator installs it, with the CSRF token, the `theme_uploads` gate of the plan, checked again at installation, and the site's storage quota. Conversions are audited (`theme.converted`, then `theme.uploaded` with `imported_from`).

The tests in `internal/themeimport` (`hostile_test.go`, `php_test.go`, `css_test.go`, `blocks_test.go`) try each of these: PHP files, scripts, handlers, style attributes, forged placeholders, CSS injections in theme.json, fonts and images that only start like one, external fonts, zip slip, bombs, too many files and oversized templates.

## Licences and credits

- **WordPress themes are GPL.** WordPress is GPLv2 or later ([licence](https://wordpress.org/about/license/)), and every theme in the wordpress.org directory must be GPL-compatible, with its bundled fonts and images too ([theme review requirements](https://make.wordpress.org/themes/handbook/review/required/)). A converted theme is a derivative of the original, so it stays under the original's licence: the importer copies the theme's own licence file, or writes the text of the GPL version the header names, as LICENSE.txt, and README.md credits the theme, its author and URI, with the original readme (which lists the licences of fonts and images) below.
- **sap13ns is CPAL-1.0.** A theme is data that sap13ns renders, as it renders WordPress themes' content, and an uploaded theme is the site's, not part of sap13ns. The GPL applies to the converted theme; the CPAL to sap13ns. The FAQ of the GPL discusses themes and plugins as separate works where they communicate at arm's length ([GPL FAQ](https://www.gnu.org/licenses/gpl-faq.html)); here nothing of the theme's code is linked or even run.
- **The attribution stays.** Every converted layout has `shared/footer-meta`, the "Powered by sap13ns" credit the CPAL asks for, and themes' own "Designed with WordPress" lines in their footers are kept as they are text of the theme.
- **Other CMSs.** Ghost's default theme Casper is MIT ([Casper](https://github.com/TryGhost/Casper)), as are most Ghost, Hugo and Jekyll themes; some commercial themes forbid redistribution, which an importer should not hide: it would carry the licence over and refuse a theme without one.

## Other CMSs

The research behind the choice of WordPress first:

| | Templates | Styles | Content model | Conversion |
|---|---|---|---|---|
| **WordPress, block themes** ([docs](https://developer.wordpress.org/themes/block-themes/)) | Static HTML with block comments; the dynamic parts are blocks with attributes | theme.json: presets and styles as data; plain CSS | Posts, pages, categories, tags, menus, like sap13ns | **Built.** The templates are data, so they can be read without running anything |
| **WordPress, classic themes** ([docs](https://developer.wordpress.org/themes/classic-themes/)) | PHP: `the_loop`, `get_header()`, template tags, any code | style.css, enqueued from PHP | Same | **Not possible** without running PHP; refused with the reason |
| **Ghost** ([docs](https://ghost.org/docs/themes/), [structure](https://ghost.org/docs/themes/structure/)) | Handlebars: `default.hbs`, `index.hbs`, `post.hbs`, `page.hbs`, `tag.hbs`, `author.hbs`, partials; about 60 helpers ([helpers](https://ghost.org/docs/themes/helpers/)) | Plain CSS (often built with PostCSS, shipped in `assets/built`) | Posts, pages, tags, authors, members; `@site`, `@custom` settings in package.json | **Feasible**: Handlebars is logic-less, its helpers map onto sap13ns's functions (table above), and the theme's `gscan` validator ([gscan](https://github.com/TryGhost/gscan)) documents what a theme may use |
| **Hugo** ([templates](https://gohugo.io/templates/), [lookup order](https://gohugo.io/templates/lookup-order/)) | Go templates, like sap13ns, with Hugo's ~200 functions ([functions](https://gohugo.io/functions/)), `baseof.html` + `block`, shortcodes | SCSS and PostCSS through Hugo Pipes ([pipes](https://gohugo.io/hugo-pipes/)), often with Node | Sections, taxonomies, front matter; themes as modules ([components](https://gohugo.io/hugo-modules/theme-components/)) | **Partly feasible**: the syntax matches, but the functions (`where`, `.Site.RegularPages`, resources, `partialCached`) assume Hugo's page graph; styles need their build |
| **Jekyll** ([themes](https://jekyllrb.com/docs/themes/), [layouts](https://jekyllrb.com/docs/layouts/)) | Liquid ([Liquid](https://shopify.github.io/liquid/)): `_layouts`, `_includes`, filters, plugins in Ruby | Sass, built by Jekyll | Collections and front matter | **Low value**: few themes are still maintained, and many depend on Ruby plugins |

## What to build next

1. **Ghost.** The best next step: Ghost themes are popular, mostly MIT, logic-less and close to sap13ns's content model. A converter would translate Handlebars to Go templates with the helper table above, take `assets/built/*.css` as they are (cleaned as here), map `@custom` settings to theme options, and drop members and newsletters, which sap13ns does not have. It needs a Handlebars parser (a small one: Ghost themes use a known subset, which gscan lists). Expect high fidelity on Casper-like themes.
2. **Hugo, for the templates only.** A Hugo theme is already Go templates, so the structure converts, but every function outside sap13ns's must be rewritten or refused, and its styles must be built first (SCSS). Worth it for a simple blog theme subset (PaperMod-like), with a clear report of what was refused, not as a general converter.
3. **More WordPress fidelity.** Typography variations as a second option (they need the theme's rules duplicated per choice), per-block stylesheets that themes enqueue from `functions.php` by convention (`assets/css/blocks/core-<block>.css`), patterns registered from PHP arrays when they are plain literals (Raft's front page), brand icons for the social links block (a small bundled set of SVG logos, which Lucide does not have), and TrueType fonts (a WOFF2 encoder).
4. **Jekyll** last, if ever.
