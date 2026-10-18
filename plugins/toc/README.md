# Table of Contents

A `content.filter` plugin that adds an automatic table of contents to long
posts and pages.

- Collects the `h2`, `h3` and `h4` headings of the rendered post.
- Gives every listed heading that has no `id` a stable, unique one: the
  heading text slugified with Unicode-aware transliteration (`Instalación` →
  `instalacion`, `Straße` → `strasse`, `Почему?` → `pochemu`, `Ελληνικά` →
  `ellinika`). Duplicates get `-2`, `-3`… and ids already used anywhere in the
  post are avoided. Text with nothing to transliterate (Chinese, Arabic,
  emoji) gets `section-<hash of the text>`, which stays stable when other
  headings change.
- Existing ids are always kept, so links people already share keep working.
- Inserts the table after the first paragraph (before the first heading when
  the post does not start with a paragraph), or where the author wrote a
  `[toc]` marker.
- Never looks at headings inside code blocks, blockquotes, callouts
  (`.alert`), asides, figures, tables or `<details>`.

It needs no permissions.

## The `[toc]` marker

Write `[toc]` on a line of its own where the table should go. It is matched in
any case (`[TOC]`), as rendered by Markdown (`<p>[toc]</p>`) or as bare text.
Only the first marker is used; the others are removed. The marker chooses
*where*; the settings still decide *whether*: when the post has too few
headings or its type is not enabled, the marker is simply removed.
`` `[toc]` `` written as code is left alone.

## Settings

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `min_headings` | number | `3` | The table appears only when the post has at least this many listed headings (0 counts as 1). |
| `max_depth` | select `h2` / `h3` / `h4` | `h4` | Deepest heading level listed. Deeper headings are not listed and get no id. |
| `title` | text | empty | Visible title and accessible name. Empty uses "Contents", or "Índice" for Spanish posts. |
| `post_types` | select `posts` / `pages` / `both` | `posts` | Which content types get a table. |

The default title follows the post's language (`es` or `es-*` gives Spanish,
anything else English), falling back to the site language.

## Example

```html
<p>Intro</p>
<h2 id="setup">Setup</h2>
<h3>Requisitos</h3>
<h2>Instalación</h2>
```

becomes (with `min_headings` = 3, Spanish post)

```html
<p>Intro</p><nav class="card toc" aria-label="Índice"><header><strong>Índice</strong></header><ol><li><a href="#setup">Setup</a><ol><li><a href="#requisitos">Requisitos</a></li></ol></li><li><a href="#instalacion">Instalación</a></li></ol></nav>
<h2 id="setup">Setup</h2>
<h3 id="requisitos">Requisitos</h3>
<h2 id="instalacion">Instalación</h2>
```

A heading nests under the closest previous heading of a higher level, so a
skipped level (`h2` → `h4`) indents only once and a post that starts with an
`h3` still gets a flat first item.

The table uses Basecoat's `card` component, which every theme styles, plus the
`toc` class; no inline styles.

## Host compatibility

- The table is a `<nav>`, which hosts allow in plugin output since fa06d1a.
  Older hosts remove it and leave a bare list.
- The Markdown renderer gives every heading an id; hosts since fa06d1a fold
  accents (`Detalle técnico` → `detalle-tecnico`). Existing ids are kept, so
  the plugin's own slugs are only used for headings without one (raw HTML
  from other plugins).

## Build and install

```sh
make build                               # plugin.wasm (GOOS=wasip1 GOARCH=wasm)
make test vet                            # native unit tests and vet
make install PLUGINS_DIR=/path/to/data/plugins
```

Then open Admin → Plugins, use **Reload**, enable the plugin (approving pins
the hash of `plugin.wasm`) and adjust its settings.
