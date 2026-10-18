# Announcement

A widget that shows a short announcement (an offer, a maintenance window, an
event) between two dates, with an optional button. Add it in Admin →
Appearance → Widgets as **Announcement — Announcement** in any widget area.

- The message accepts a Markdown-lite subset: `**bold**` and
  `[links](https://…)`. Everything else is shown as plain text (HTML is
  escaped), and links only accept `https://`, `http://`, `mailto:`, site paths
  (`/blog/sale`) and fragments (`#signup`).
- Spanish and English pages can have their own message and button label.
- Outside its time window, or without a message, the widget renders nothing,
  so the server hides it completely (title included).
- Three styles reuse the theme's alert component: `info`, `success` and
  `warning`, each with its icon.
- The announcement is a `<section>` labelled "Announcement" ("Anuncio" on
  Spanish pages), which assistive technology exposes as a region.

It needs no permissions and stores nothing. There is no close button: dismissing
it would need JavaScript, which plugins cannot add.

## Settings

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `message` | textarea | empty | The message. Used when there is no message for the page's language. Empty hides the widget. |
| `message_es` | textarea | empty | Message for Spanish pages. |
| `message_en` | textarea | empty | Message for English pages. |
| `link_url` | text | empty | Button link: `https://…`, `http://…` or a site path such as `/blog/sale`. Empty shows no button. |
| `link_label` | text | empty | Button label. Empty uses "Learn more" ("Más información" on Spanish pages). |
| `link_label_es` | text | empty | Button label for Spanish pages. |
| `link_label_en` | text | empty | Button label for English pages. |
| `starts_at` | text | empty | Shown from this moment (inclusive). Empty: right away. |
| `ends_at` | text | empty | Hidden from this moment (exclusive). A date alone includes that whole day. Empty: no end. |
| `timezone` | text | `UTC` | IANA time zone (for example `Europe/Madrid`) for times written without an offset. |
| `style` | select | `info` | `info`, `success` or `warning`. |

Times can be written as `2026-11-01 09:00`, `2026-11-01T09:00`,
`2026-11-01 09:00:30`, `2026-11-01` or RFC 3339 with an offset
(`2026-11-01T09:00:00+01:00`, `2026-11-01T08:00Z`); an explicit offset wins
over `timezone`. Daylight saving time is handled by the time zone database
embedded in the plugin. Problems are written to the plugin log in Admin →
Plugins: an unreadable date, or `ends_at` not later than `starts_at`, hides the
announcement; an unknown time zone falls back to UTC.

### Language

Widgets do not receive the language of the page, so the plugin reads it from
the page URL: paths under `/es/` use the Spanish texts, paths under `/en/` the
English ones, and every other page uses the site's main language. Pages in
other languages use `message` and `link_label`. A widget can also be limited
to one language in Admin → Appearance → Widgets, for example to show the
announcement on Spanish pages only.

## Example

```
message_en  = Black Friday: **30% off** every plan. [See the plans](/hosting)
message_es  = Black Friday: **30 % de descuento** en todos los planes.
link_url    = /hosting
starts_at   = 2026-11-27
ends_at     = 2026-11-30
timezone    = Europe/Madrid
style       = success
```

On an English page between 27 November 00:00 and 1 December 00:00 (Madrid
time) the widget renders:

```html
<section class="alert announcement" data-category="success" aria-label="Announcement"><span data-icon="circle-check"></span><section><p>Black Friday: <strong>30% off</strong> every plan. <a href="/hosting"><u>See the plans</u></a></p><p><a href="/hosting"><span class="btn">Learn more</span></a></p></section></section>
```

The server replaces `<span data-icon="…">` with the Lucide icon. Line breaks in
the message become `<br>`.

## Host compatibility

- No theme has a widget area at the top of the page yet (the bundled themes
  offer `sidebar` and/or `footer`), so the announcement appears where those
  areas are. A theme that adds a header area gets a real top bar with no
  change to the plugin.
- `role="region"` is not allowed by the plugin sanitizer, so the plugin relies
  on the implicit region role of a labelled `<section>`.
- Widget areas style links as plain text (`.widget a`), and some themes
  recolour every link in a footer widget. So links in the message are wrapped
  in `<u>` to stay recognisable, and the button's `btn` class sits on a
  `<span>` inside the link so the theme's link colour cannot override it.
- Without a widget title, the server labels the widget's wrapper with its
  kind (`plugin:announcement:bar`); give the widget a title, or leave it as is
  and rely on the plugin's own "Announcement" label.
- Anonymous pages are cached for up to two minutes, so the announcement can
  appear or disappear up to two minutes late.
- The server does not send the site's time zone to plugins, hence the
  `timezone` setting.

## Build and install

```sh
make build                               # plugin.wasm (GOOS=wasip1 GOARCH=wasm)
make test vet                            # native unit tests and vet
make install PLUGINS_DIR=/path/to/data/plugins
```

Then open Admin → Plugins, use **Reload**, enable the plugin (approving pins
the hash of `plugin.wasm`), fill in the settings and add the widget to an area.
