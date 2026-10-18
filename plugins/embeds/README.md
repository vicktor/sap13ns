# Privacy-friendly embeds

A `content.filter` plugin that turns standalone YouTube and Vimeo links into
click-to-load video players.

- Write the link on a line of its own, either as a bare URL
  (`https://youtu.be/dQw4w9WgXcQ`) or as a titled link
  (`[Our keynote](https://youtu.be/dQw4w9WgXcQ)`). The title names the video
  for screen readers and on the play button.
- **Nothing is requested from the video site until the visitor presses
  play.** No thumbnails are hot-linked, no scripts or iframes are loaded, and
  no third-party CSS is used. The play button and the placeholder are drawn
  with the theme's own colours.
- After the click the player loads from **youtube-nocookie.com** (YouTube's
  privacy-enhanced mode) or from **player.vimeo.com with `dnt=1`** (Vimeo's
  Do Not Track mode).
- **Without JavaScript the embed is a plain link** to the video page, styled
  as a card with a play icon, the title and "Watch on YouTube".
- English and Spanish texts follow the post's language.

## What is embedded

Only top-level paragraphs that contain nothing but one link to a video, or one
bare video URL. Links inside lists, quotes, tables, code or paragraphs with
other text are never touched, so `Watch [this](https://youtu.be/…) first`
stays a normal link. A link that wraps an image is left alone too.

| Provider | Recognised links | Player |
| --- | --- | --- |
| YouTube | `youtube.com/watch?v=ID` (also `www.`, `m.`, `music.`), `youtu.be/ID`, `youtube.com/shorts/ID` (shown in portrait, 9:16), `youtube.com/embed/ID`, `youtube.com/live/ID`, `youtube-nocookie.com/embed/ID` | `www.youtube-nocookie.com/embed/ID?autoplay=1` |
| Vimeo | `vimeo.com/ID`, `vimeo.com/ID/HASH` and `?h=HASH` (unlisted videos), `vimeo.com/channels/NAME/ID`, `player.vimeo.com/video/ID` | `player.vimeo.com/video/ID?autoplay=1&dnt=1` |

- Start times are kept: `t=90`, `t=90s`, `t=1m30s`, `t=1h2m3s` or `start=90`
  on YouTube become `start=90`; `#t=1m30s` on Vimeo becomes `#t=90s`.
- Anything else (channels, playlists, malformed ids, look-alike domains such
  as `youtube.com.evil.test`, URLs with credentials) stays a link.
- `autoplay=1` only means the video starts when the player has loaded after
  the visitor pressed play, so one click is enough.

## Title of the video

1. The link text, when it is not just the URL.
2. Otherwise the link's title (`[https://youtu.be/…](https://youtu.be/… "Keynote")`
   in Markdown).
3. Otherwise "YouTube video" / "Vídeo de YouTube" ("Vimeo video" / "Vídeo de
   Vimeo"), numbered "YouTube video 2", "YouTube video 3" when a post has
   several untitled videos.

## Permissions

| Permission | Risk | Why |
| --- | --- | --- |
| `assets:js` | high | `assets/embeds.js` adds the play button and creates the player iframe after the click. Plugins cannot output `<button>` or `<iframe>` (the host sanitizer removes them), so this needs a script. |
| `csp:frame-src:https://www.youtube-nocookie.com` | high | Lets the page frame YouTube's privacy-enhanced player (declared under `"csp"`). |
| `csp:frame-src:https://player.vimeo.com` | high | Lets the page frame Vimeo's player. |

The CSP sources are added to public pages only, never to the admin panel, and
only while the plugin is enabled. They are pinned when the plugin is approved:
a package that asks for another source stops until it is approved again.
`assets/embeds.css` needs no permission.

`sap13ns plugin pack` reports the package as **CLEAN** with three
`perm.high-risk` medium findings (one per permission above) and the
`package.no-license` info finding. Enabling the plugin asks for the
high-risk confirmation.

## Settings

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `youtube` | bool | `true` | Embed YouTube links. Off leaves them as links. |
| `vimeo` | bool | `true` | Embed Vimeo links. |
| `notice` | text | empty | Privacy note under every embed; `{provider}` becomes YouTube or Vimeo. Empty uses the built-in note in the post's language. |
| `notice_es` | text | empty | Note for Spanish posts. |
| `notice_en` | text | empty | Note for English posts. |
| `privacy_url` | text | empty | `https://…`, `http://…` or a site path such as `/privacy`. Adds a "Privacy policy" ("Política de privacidad") link after the note. Anything else is ignored and logged once. |
| `privacy_url_es` | text | empty | Link for Spanish posts. |
| `privacy_url_en` | text | empty | Link for English posts. |

The language is the post's (`es` or `es-*` gives Spanish, anything else
English), falling back to the site language. For each text the
language-specific setting wins over the generic one, which wins over the
built-in text.

Built-in notes:

- English: "Nothing is loaded from {provider} until you press play. Playing
  the video connects to {provider}, which may collect data about your visit."
- Spanish: "No se carga nada de {provider} hasta que pulsas reproducir. Al
  reproducir el vídeo te conectas a {provider}, que puede recoger datos de tu
  visita."

## Privacy

- **Before the click** the page only loads the site's own files:
  `embeds.css` and `embeds.js` from `/plugin-assets/embeds/…`. The browser
  makes no request to YouTube, Google or Vimeo.
- **After the click** the browser loads the player from the provider, which
  then sees the visitor's IP address, browser and the site's origin
  (`referrerpolicy="strict-origin-when-cross-origin"`: the origin, not the
  page path). YouTube refuses to play embeds requested without any referrer.
- **youtube-nocookie.com** is YouTube's privacy-enhanced mode: YouTube says
  it does not store information about visitors who do not play the video.
  Once a video plays, YouTube may store identifiers in the browser and
  collect viewing data like on youtube.com.
- **`dnt=1`** asks Vimeo's player not to track the session (no analytics or
  session cookies). Vimeo still serves the video and sees the request.
- The plugin stores nothing and sets no cookies. Each video needs its own
  click; the choice is not remembered.
- Add your privacy policy with `privacy_url` so visitors can read how the site
  handles embedded content.

## Accessibility

- Without JavaScript there is one link per video: the play icon is hidden
  from assistive technology and the link reads "Our keynote, Watch on
  YouTube".
- With JavaScript the play control is a real `<button>` whose label names the
  video: "Play video: Our keynote (YouTube)" ("Reproducir vídeo: …"), or
  "Play YouTube video 2" for untitled videos. Enter and Space work natively
  and the focus ring uses the theme's `--ring` colour.
- After the click the focus moves into the player, whose `title` is "Our
  keynote (YouTube)". The "Watch on YouTube" link stays below it.
- Under `prefers-reduced-motion: reduce` the button has no hover animation.
- All visible text comes from the server in the post's language; the script
  contains no user-facing strings.

## Example

A post in English with `privacy_url` = `/privacy`:

```markdown
[Our keynote](https://youtu.be/dQw4w9WgXcQ?t=1m30s)
```

becomes

```html
<figure class="card embed embed-youtube" data-category="youtube"><a class="embed-link" href="https://youtu.be/dQw4w9WgXcQ?t=1m30s"><span class="embed-icon" aria-hidden="true"><span data-icon="play"></span></span><span class="embed-text"><strong class="embed-title">Our keynote</strong><small class="embed-source">Watch on YouTube</small></span></a><a class="embed-player" href="https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ?autoplay=1&amp;start=90" aria-label="Play video: Our keynote (YouTube)"><span class="embed-provider">YouTube</span></a><figcaption class="embed-notice">Nothing is loaded from YouTube until you press play. Playing the video connects to YouTube, which may collect data about your visit. <a href="/privacy">Privacy policy</a></figcaption></figure>
```

A bare Vimeo URL in a Spanish post:

```html
<figure class="card embed embed-vimeo" data-category="vimeo"><a class="embed-link" href="https://vimeo.com/76979871"><span class="embed-icon" aria-hidden="true"><span data-icon="play"></span></span><span class="embed-text"><strong class="embed-title">Vídeo de Vimeo</strong><small class="embed-source">Ver en Vimeo</small></span></a><a class="embed-player" href="https://player.vimeo.com/video/76979871?autoplay=1&amp;dnt=1" aria-label="Reproducir vídeo de Vimeo"><span class="embed-provider">Vimeo</span></a><figcaption class="embed-notice">No se carga nada de Vimeo hasta que pulsas reproducir. Al reproducir el vídeo te conectas a Vimeo, que puede recoger datos de tu visita.</figcaption></figure>
```

The server replaces `<span data-icon="play">` with the Lucide icon. The
`a.embed-player` link is hidden by `embeds.css`; the script reads its address
and label. Once the script has run, the figure gets `is-enhanced` and starts
with:

```html
<div class="embed-frame"><button type="button" class="embed-play" aria-label="Play video: Our keynote (YouTube)"><span class="embed-play-icon" aria-hidden="true"><svg …></svg></span><span class="embed-play-text"><span class="embed-play-title">Our keynote</span><span class="embed-play-provider">YouTube</span></span></button></div>
```

and after the click the button is replaced by

```html
<iframe src="https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ?autoplay=1&amp;start=90" title="Our keynote (YouTube)" allow="accelerometer; autoplay; clipboard-write; encrypted-media; gyroscope; picture-in-picture; web-share" allowfullscreen="" referrerpolicy="strict-origin-when-cross-origin"></iframe>
```

## Looks

The embed is a Basecoat `card`, which every theme styles in light and dark
mode. `embeds.css` only uses theme tokens (`--primary`, `--ring`,
`--muted-foreground`, `--radius`) for the play badge, the note and the focus
ring. The video placeholder is dark in both modes, like a player. It keeps
its 16:9 ratio at any width (9:16 and at most 22rem wide for Shorts).

## Adding a provider

Add an entry to `providers` in `providers.go` (id, name, URL parser that
returns the player URL), a bool setting with the same id, and the player's
origin under `"csp"` → `"frame-src"` in `plugin.json`. Prefer providers with a
privacy mode; Dailymotion was left out because it has none.

## Host compatibility

- The host links plugin scripts on every public page. `embeds.js` checks for
  `figure.embed` and stops at once when there is none.
- Rendered post bodies are cached by the host. Changing the plugin's settings
  changes the plugin generation, so posts are rendered again with the new
  settings.
- Plugins run their `content.filter` in id order. `external-links` runs after
  `embeds` and may add `rel`, `target="_blank"` and its hidden "(opens in a
  new tab)" text to the embed's links. That is harmless: the script reads the
  title from `.embed-title` and the provider from `.embed-provider`.
- Like the other official filters, the plugin turns Lucide SVGs expanded
  earlier in the pipeline back into `<span data-icon>` placeholders before it
  returns the HTML, for hosts that sanitise between filters without keeping
  icons.

## Build and install

```sh
make build                               # plugin.wasm (GOOS=wasip1 GOARCH=wasm)
make test vet                            # native unit tests and vet
make install PLUGINS_DIR=/path/to/data/plugins
sap13ns plugin pack .                    # or build a .zip to upload
```

Then open Admin → Plugins, use **Reload** (or upload the `.zip`), review the
permissions, tick the confirmation for the high-risk permissions and enable
the plugin. Approving pins the hash of `plugin.wasm`, of the assets and the
CSP sources.
