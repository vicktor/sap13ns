# Callouts

Notes, tips and warnings rendered with Basecoat's `alert` component, which
every theme styles in light and dark mode. It replaces the former
`plugins/examples/callout` and keeps its shortcodes.

- **Markdown alerts** (`content.filter`): GitHub-style alerts written as a
  blockquote whose first line is `[!NOTE]`, `[!TIP]`, `[!IMPORTANT]`,
  `[!WARNING]` or `[!CAUTION]`.
- **`[callout]` and `[kbd]` shortcodes**, backward compatible with the example
  plugin.

It needs no permissions.

## Markdown alerts

```markdown
> [!WARNING]
> Stop the service before replacing the binary.
```

becomes

```html
<div class="alert markdown-alert markdown-alert-warning" role="note" data-category="warning"><span data-icon="triangle-alert"></span><strong>Warning</strong><section>
<p>Stop the service before replacing the binary.</p>
</section></div>
```

The server turns `<span data-icon>` into a Lucide icon. The title follows the
post's language (`es` or `es-*` gives Spanish, anything else English),
falling back to the site language:

| Marker | Title (en / es) | `data-category` | Icon |
| --- | --- | --- | --- |
| `[!NOTE]` | Note / Nota | `info` | `info` |
| `[!TIP]` | Tip / Consejo | `success` | `sparkles` |
| `[!IMPORTANT]` | Important / Importante | `important` | `message-square` |
| `[!WARNING]` | Warning / Advertencia | `warning` | `triangle-alert` |
| `[!CAUTION]` | Caution / Precaución | `error` (+ `data-variant="destructive"`) | `circle-alert` |

The themes colour the icon from `data-category` (`success`, `warning`,
`error`); the `markdown-alert-*` classes match GitHub's for custom CSS.

- The marker is matched in any case and may be followed by text on the same
  line, which then becomes the first line of the body.
- Everything else in the blockquote (paragraphs, lists, code) moves into the
  alert's `<section>`.
- A blockquote with only the marker, an unknown type, or a marker that is not
  the very first thing stays a normal quote. Blocks inside `<pre>`/`<code>`
  are never touched.
- `role="note"` is the right role for static asides; `role="alert"` would make
  screen readers interrupt the visitor on page load.

## Shortcodes

```text
[callout type="warning" title="Careful"]Text.

Second paragraph.[/callout]
[kbd]Ctrl+K[/kbd]
```

- `type` is `info` (default), `success`, `warning` or `error`; `title` is
  optional. The inner text is escaped and blank lines separate paragraphs.
- Output: `<div class="alert sc-callout" role="note" data-category="warning"><span data-icon="triangle-alert"></span><strong>Careful</strong><section><p>Text.</p><p>Second paragraph.</p></section></div>`.
- `[kbd]Ctrl+K[/kbd]` gives `<span class="kbd"><kbd>Ctrl+K</kbd></span>`.

Changes from the example plugin: the callout title is a `<strong>` instead of
an `<h2>` (themes style both the same way, and callout titles no longer show up
in the document outline or in a table of contents), empty paragraphs are
skipped, and `[kbd]` wraps the key in `<span class="kbd">` (see below).

## Settings

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `markdown_alerts` | bool | `true` | Convert `[!NOTE]`… blockquotes. The shortcodes work either way. |

## Host compatibility

- The sanitizer does not allow `<kbd>` in plugin output and keeps only its
  text, so the styled `kbd` class sits on a `<span>` around it; the inner
  `<kbd>` keeps its meaning once the policy allows it.
- The whole document is sanitized again after every `content.filter` plugin,
  and `<svg>` is not allowed, so icons expanded earlier (by shortcodes or other
  filters) would be lost. When the plugin returns HTML it turns those Lucide
  SVGs back into `<span data-icon="…">` placeholders, which the server expands
  again.
- The Lucide set embedded in the server has no `lightbulb` or `octagon-alert`
  (GitHub's TIP and CAUTION icons), hence `sparkles` and `circle-alert`.

## Build and install

```sh
make build                               # plugin.wasm (GOOS=wasip1 GOARCH=wasm)
make test vet                            # native unit tests and vet
make install PLUGINS_DIR=/path/to/data/plugins
```

Then open Admin → Plugins, use **Reload** and enable the plugin (approving
pins the hash of `plugin.wasm`).
