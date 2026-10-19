# Related posts

Lists the posts that have the most in common with the one being read.

- **After each post** (`content.filter`), as a card titled "Related posts"
  ("Artículos relacionados" for Spanish posts), at the end of the content.
- **As a widget** ("Related posts"). It can go in any widget area and only
  appears on single-post pages.

Both look like the built-in "Recent posts" widget: title and date, and
optionally a small thumbnail. They use the same `.widget-posts` markup, so
every theme styles them in light and dark mode. Nothing is shown when no post
is related.

## How posts are chosen

1. Candidates are the published posts (never pages) **in the same language**
   as the current post that share at least one of its tags or categories. The
   current post is never listed.
2. They are ranked by the **number of shared tags**, then the **number of
   shared categories**, then **newest first** (`published_at`). Ties go to
   the higher id.
3. The first `count` are shown (4 by default, at most 10).

Posts that share no tag or category are never shown. A post without tags and
categories gets no related posts.

The plugin asks the host for one list per term: the 20 newest posts with that
tag or category. The host allows 10 queries per call, so a post with more
terms uses its first tags and keeps at least one category. Each candidate is
then scored on all of its own terms, not only on the one it was found by.
With very popular terms, an older post that would score higher can be
missed if it is not among the 20 newest posts of any queried term.

## Permissions

| Permission | Risk | Why |
| --- | --- | --- |
| `posts:read` | low | Lists published posts with a given tag or category (`QueryPosts`). The host only ever returns published, public content. |

`assets/related.css` needs no permission. `sap13ns plugin pack` reports the
package as **CLEAN**, with no findings.

## Settings

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `count` | number | `4` | How many posts to list, 1 to 10. Values outside the range are clamped. |
| `after_content` | bool | `true` | Add the card after every post. Turn it off to use only the widget. |
| `show_images` | bool | `false` | Show the featured image as a 48 px thumbnail. Off by default because the host only gives plugins the original upload, so each thumbnail downloads the full-size file. |
| `title` | text | empty | Title of the card. Empty uses "Related posts" / "Artículos relacionados". |
| `title_es` | text | empty | Title for Spanish posts. |
| `title_en` | text | empty | Title for English posts. |

The widget has one field, **Number of posts**. Empty uses `count` from the
settings. Its title is the one set in Appearance → Widgets.

The language is the post's for the card and the page's for the widget (`es`
or `es-*` gives Spanish, anything else English), falling back to the site
language. For the title, the language-specific setting wins over `title`,
which wins over the built-in text.

Dates use the site time zone, like the host's widgets: "Sep 15, 2026" or "15
sep 2026". The `datetime` attribute is UTC (RFC 3339). An unknown time zone
falls back to UTC and is logged once.

## Example

A site in `Europe/Madrid` with these posts in English:

| Post | Tags | Category | Published (UTC) |
| --- | --- | --- | --- |
| **Plugins in WebAssembly** (current) | go, wasm | engineering | 2026-10-10 09:00 |
| Sandboxing Go with wazero | go, wasm | engineering | 2026-06-02 08:00 |
| Why we chose Go | go | company | 2026-09-14 22:30 |
| Release notes 1.4 | | engineering | 2026-08-01 10:00 |

The card added after "Plugins in WebAssembly" (real output). "Why we chose
Go" shows Sep 15 because it was published after midnight Madrid time.

```html
<aside class="card related-posts" aria-label="Related posts"><header><h2 class="related-posts-title">Related posts</h2></header><section><ul class="widget-posts related-posts-list"><li><a href="https://example.test/blog/sandboxing-go-with-wazero/"><span>Sandboxing Go with wazero<time datetime="2026-06-02T08:00:00Z">Jun 2, 2026</time></span></a></li><li><a href="https://example.test/blog/why-we-chose-go/"><span>Why we chose Go<time datetime="2026-09-14T22:30:00Z">Sep 15, 2026</time></span></a></li><li><a href="https://example.test/blog/release-notes-1-4/"><span>Release notes 1.4<time datetime="2026-08-01T10:00:00Z">Aug 1, 2026</time></span></a></li></ul></section></aside>
```

The widget on the same page:

```html
<ul class="widget-posts related-widget"><li><a href="https://example.test/blog/sandboxing-go-with-wazero/"><span>Sandboxing Go with wazero<time datetime="2026-06-02T08:00:00Z">Jun 2, 2026</time></span></a></li><li><a href="https://example.test/blog/why-we-chose-go/"><span>Why we chose Go<time datetime="2026-09-14T22:30:00Z">Sep 15, 2026</time></span></a></li><li><a href="https://example.test/blog/release-notes-1-4/"><span>Release notes 1.4<time datetime="2026-08-01T10:00:00Z">Aug 1, 2026</time></span></a></li></ul>
```

A Spanish post only lists Spanish posts:

```html
<aside class="card related-posts" aria-label="Artículos relacionados"><header><h2 class="related-posts-title">Artículos relacionados</h2></header><section><ul class="widget-posts related-posts-list"><li><a href="https://example.test/es/blog/por-que-elegimos-go/"><span>Por qué elegimos Go<time datetime="2026-09-14T22:30:00Z">15 sep 2026</time></span></a></li></ul></section></aside>
```

With `show_images` on, each link starts with
`<img src="…" alt="" width="64" height="64" loading="lazy">`. The image is
decorative: the title is the link text.

## Looks and accessibility

- The card is a Basecoat `card` with a `header` and a `section`, so it gets
  each theme's card padding, radius, background and dark mode.
  `related.css` only undoes the post body's `.prose` styles inside it
  (heading size, bullets, underlined links, large rounded images) and lays
  the list out in one column on phones and two or more on wider screens. It
  uses only theme tokens (`--primary`, `--ring`, `--muted-foreground`,
  `--radius`) and has no animations.
- The widget is the same `.widget-posts` list as the built-in "Recent posts"
  widget. The host wraps plugin widgets in `.widget-html`, which some themes
  style as rich text (muted colour, smaller type, underlined links), so
  `related.css` resets those few properties for this list. Checked against
  "Recent posts" in all five built-in themes, light and dark.
- The card is an `<aside>` landmark named by its title. The table of
  contents plugin ignores headings inside `<aside>`, so "Related posts" is
  not added to it.
- Each item is one link; the date is a `<time>` element. Focus shows the
  theme's `--ring` outline.

## Privacy

The plugin reads published posts only, stores nothing, sets no cookies and
makes no outside requests. Thumbnails, when enabled, come from the site's own
media URLs.

## Host compatibility

- **Thumbnails are full-size images.** `Post.Image` is the original upload;
  the host has no thumbnail or `srcset` for plugins. Hence `show_images` is
  off by default.
- **Feed items get no card.** In full-text feeds (`context` is `feed`) the
  plugin leaves the HTML as it is.
- The card is appended to the HTML as it is; earlier filters' output is
  untouched and no HTML is parsed.

## Build and install

```sh
make build                               # plugin.wasm (GOOS=wasip1 GOARCH=wasm)
make test vet                            # native unit tests and vet
make install PLUGINS_DIR=/path/to/data/plugins
sap13ns plugin pack .                    # or build a .zip to upload
```

Then open Admin → Plugins, use **Reload** (or upload the `.zip`), review the
permission and enable the plugin. Add the widget in Appearance → Widgets.
