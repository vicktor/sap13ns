---
type: page
key: themes
title: Themes
slug: themes
excerpt: Six finished themes, each with a light and a dark mode, and a plain contract for building your own, whether it is a blog, a landing page or a one-page site.
order: 2
menu: [primary, footer, landing]
menu_label: Themes
comments: closed
---
A theme decides how a site looks. It never decides how safe it is. Every sap13ns theme renders the same accessible markup for menus, posts, comments and search, follows the same strict security policy, and ships with a light and a dark mode out of the box.

Screenshots are on their way. Until they are ready, the previews on this page draw each theme in miniature with nothing but CSS, in its light and dark mode.

## The six themes

### Apple

Quiet product-page elegance: a frosted navigation bar, oversized headlines, airy grids and generous rounded corners. Good for a studio, a product or a writer who wants the words to carry the page. Six accent colours to choose from.

### Neumorphism

Soft UI. Cards, buttons and fields are pressed out of, or carved into, one continuous surface. The shadows are tuned so that text keeps accessible contrast and focus rings stay clearly visible in both modes, which is where most soft designs fall apart. You can pick the accent and how deep the shadows go.

### Glassmorphism

Frosted, translucent panels floating over a slow-drifting aurora: luminous pastels by day, neon on deep indigo by night. You choose the palette and how frosted the glass is, and the motion can be switched off. It suits portfolios and launch pages that want atmosphere without giving up readable body text.

### Bento Grid

Rounded tiles of different sizes, like a bento box: an asymmetric home grid with the latest story, site statistics, categories, tags and feeds, and reading pages built from the same tiles. Great for magazines, link blogs and anyone who publishes in several formats.

### Dashboard / SaaS

The precision of a product dashboard: persistent sidebar navigation, a top bar with breadcrumbs and search, KPI cards and table-like post lists, with crisp borders, one accent colour and a compact mode. It fits documentation, changelogs, knowledge bases and software products.

### Onepage

A one-page site with an editorial voice: warm paper and ink, large serif headlines (or sans-serif ones), and numbered sections for features, figures, testimonials, pricing and questions, closed by a bold call to action. The sticky menu scrolls to each section and marks the one you are reading. The blog lives at `/blog`, and each section can be switched off. Five accent colours.

## Light and dark, without the flash

Each theme defines its colours for both modes. A tiny script that ships with the core applies the visitor's saved choice, or their system preference, before the first paint, so the page never flashes white at night. The toggle remembers the choice for next time.

## Options without code

Themes declare their options in a manifest and the admin panel builds the form: accent colours, whether to show the large introduction on the home page, featured images in cards, a sidebar, and so on. Menus and widget areas are declared the same way, and you fill them from **Appearance** in the admin.

Two tools go further without editing a theme. The **customiser** changes its colours, fonts, corner radius, density, header and footer, with a live preview in light and dark mode and contrast warnings. The **page builder** lays out the header, the footer and each type of page from sections, columns and blocks, including blocks of your own HTML, and edits the theme's custom CSS; a page type you have not built keeps the theme's own template. Both keep changes as a draft until you publish them, keep the last ten published versions, and export and import their work as one file.

## Landing pages and one-page sites

A site does not have to look like a blog. In **Settings → Reading**, the front page can be the theme's own: a landing page or a one-page site, with the posts moved to `/blog`. Any theme can have one, built in the page builder from seven landing blocks: features, figures, testimonials, pricing you write yourself, questions and answers, logos and a call to action. Start from the **Landing** or **One-page site** preset and change the texts, in every language of the site.

Sections can have an anchor such as `#pricing`. A menu link to it scrolls there, smoothly unless the visitor has asked for less motion. From any other page it leads back to that section of the front page, and while you scroll, the menu marks the section in view.

## Build your own

A theme is a folder with a small, documented contract:

```text
my-theme/
├── theme.json          name, menus, widget areas, options, preview colours
├── templates/
│   ├── layout.html     the page frame
│   ├── front.html      a landing page or a one-page site
│   ├── home.html       latest posts (this, front.html or both)
│   ├── single.html     one post (optional)
│   ├── page.html       one page (optional)
│   ├── archive.html    categories, tags, authors, dates (optional)
│   ├── search.html     search results (optional)
│   ├── 404.html        not found
│   ├── ask.html        the site assistant without JavaScript (optional)
│   └── partials/       anything you want to reuse
├── i18n/es.json        translations of the theme's own strings
├── src/theme.css       the stylesheet source
└── static/theme.css    the compiled stylesheet
```

A theme that leaves out the blog pages still has a blog: the posts, pages, archives and search results it does not draw use plain shared templates inside its own frame and colours.

To install a theme, upload its `.zip` in **Appearance → Themes**, where it is checked before it is installed, or copy its folder into the themes directory (`SAP13NS_THEMES_DIR`, by default `themes` in the data directory) and restart sap13ns: there is no recompiling.

Templates use Go's `html/template`, which escapes output by context, and a set of shared partials for the parts every site needs: the document head with canonical and `hreflang` links, menus, the language switcher, the light and dark toggle, comments, pagination and the search form. A theme can override any of them.

Styles are written with Tailwind CSS v4 and compiled into one minified file **while you develop**. The result is committed with the theme, so production servers never run Node.js.

Strings in templates are written in the main language and passed through the translation function. A theme can include its own translations in `i18n/<lang>.json`, a simple key-to-text map that is checked before the core catalogue.

### The rules every theme follows

- **No inline scripts or styles.** The Content Security Policy only allows scripts and stylesheets served by the site itself. No `<script>` blocks, no `style` attributes, no `onclick`.
- **No theme JavaScript.** The core script already handles the colour toggle, the mobile menu, the language switcher and comment replies.
- **No external fonts or CDNs.** Fonts are system stacks or WOFF2 files served by the site itself, so they stay fast and private.
- **Accessible by default.** Landmarks, a skip link, visible focus, reduced motion and readable contrast in both modes.

The full contract is in [THEMES.md](https://github.com/vicktor/sap13ns/blob/main/docs/THEMES.md).
