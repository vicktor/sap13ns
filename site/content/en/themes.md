---
type: page
key: themes
title: Themes
slug: themes
excerpt: Five finished themes, each with a light and a dark mode, and a plain contract for building your own.
order: 2
menu: [primary, footer, landing]
menu_label: Themes
comments: closed
---
A theme decides how a site looks. It never decides how safe it is. Every sap13ns theme renders the same accessible markup for menus, posts, comments and search, follows the same strict security policy, and ships with a light and a dark mode out of the box.

Screenshots are on their way. Until they are ready, the previews on this page draw each theme in miniature with nothing but CSS, in its light and dark mode.

## The five themes

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

## Light and dark, without the flash

Each theme defines its colours for both modes. A tiny script that ships with the core applies the visitor's saved choice, or their system preference, before the first paint, so the page never flashes white at night. The toggle remembers the choice for next time.

## Options without code

Themes declare their options in a manifest and the admin panel builds the form: accent colours, whether to show the large introduction on the home page, featured images in cards, a sidebar, and so on. Menus and widget areas are declared the same way, and you fill them from **Appearance** in the admin.

## Build your own

A theme is a folder with a small, documented contract:

```text
my-theme/
├── theme.json          name, menus, widget areas, options, preview colours
├── templates/
│   ├── layout.html     the page frame
│   ├── home.html       latest posts or the front page
│   ├── single.html     one post
│   ├── page.html       one page (optional)
│   ├── archive.html    categories, tags, authors, dates (optional)
│   ├── search.html     search results (optional)
│   ├── 404.html        not found (optional)
│   └── partials/       anything you want to reuse
├── i18n/es.json        translations of the theme's own strings
└── static/theme.css    the compiled stylesheet
```

Templates use Go's `html/template`, which escapes output by context, and a set of shared partials for the parts every site needs: the document head with canonical and `hreflang` links, menus, the language switcher, the light and dark toggle, comments, pagination and the search form. A theme can override any of them.

Styles are written with Tailwind CSS v4 and compiled into one minified file **while you develop**. The result is committed with the theme, so production servers never run Node.js.

Strings in templates are written in the main language and passed through the translation function. A theme can include its own translations in `i18n/<lang>.json`, a simple key-to-text map that is checked before the core catalogue.

### The rules every theme follows

- **No inline scripts or styles.** The Content Security Policy only allows scripts and stylesheets served by the site itself. No `<script>` blocks, no `style` attributes, no `onclick`.
- **No theme JavaScript.** The core script already handles the colour toggle, the mobile menu, the language switcher and comment replies.
- **No external fonts or CDNs.** System font stacks are fast, private and look right on every platform.
- **Accessible by default.** Landmarks, a skip link, visible focus, reduced motion and readable contrast in both modes.

The full contract is in [THEMES.md](https://github.com/vicktor/sap13ns/blob/main/docs/THEMES.md).
