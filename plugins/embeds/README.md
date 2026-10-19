# Privacy-friendly embeds

A `content.filter` plugin that turns standalone YouTube, Vimeo and TikTok
links into click-to-load video players.

- Write the link on a line of its own, either as a bare URL
  (`https://youtu.be/dQw4w9WgXcQ`) or as a titled link
  (`[Our keynote](https://youtu.be/dQw4w9WgXcQ)`). The title names the video
  for screen readers and on the play button.
- **Nothing is requested from the video site until the visitor presses
  play.** No thumbnails are hot-linked, no scripts or iframes are loaded, and
  no third-party CSS is used. The play button and the placeholder are drawn
  with the theme's own colours.
- After the click the player loads from **youtube-nocookie.com** (YouTube's
  privacy-enhanced mode), from **player.vimeo.com with `dnt=1`** (Vimeo's
  Do Not Track mode), or from **TikTok's embed player** on www.tiktok.com,
  which has no privacy mode (see [Privacy](#privacy)).
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
| TikTok | `tiktok.com/@NAME/video/ID` (also `www.`, `m.`, with any query such as `?is_from_webapp=1`), `tiktok.com/@NAME/photo/ID` (photo posts), `m.tiktok.com/v/ID.html`, `tiktok.com/embed/v2/ID`, `tiktok.com/embed/ID`, `tiktok.com/player/v1/ID` (shown in portrait, 9:16) | `www.tiktok.com/player/v1/ID?autoplay=1&description=1&rel=0` |

- Start times are kept: `t=90`, `t=90s`, `t=1m30s`, `t=1h2m3s` or `start=90`
  on YouTube become `start=90`; `#t=1m30s` on Vimeo becomes `#t=90s`.
- Anything else (channels, playlists, profiles, malformed ids, look-alike
  domains such as `youtube.com.evil.test`, URLs with credentials) stays a
  link.
- **TikTok short links stay links.** `vm.tiktok.com/…`, `vt.tiktok.com/…` and
  `tiktok.com/t/…` hide the post id behind a redirect. A content filter runs
  while the page is rendered, without network access, so it cannot follow it.
  Open the short link in a browser and paste the full
  `tiktok.com/@name/video/…` address instead.
- TikTok's player options (from [TikTok's embed player
  documentation](https://developers.tiktok.com/doc/embed-player)):
  `description=1` shows the caption, which the page does not know without
  asking TikTok, and `rel=0` suggests more videos by the same author instead
  of TikTok's recommendations.
- `autoplay=1` only means the video starts when the player has loaded after
  the visitor pressed play, so one click is enough.

## Title of the video

1. The link text, when it is not just the URL.
2. Otherwise the link's title (`[https://youtu.be/…](https://youtu.be/… "Keynote")`
   in Markdown).
3. Otherwise "YouTube video" / "Vídeo de YouTube" ("Vimeo video" / "Vídeo de
   Vimeo"), numbered "YouTube video 2", "YouTube video 3" when a post has
   several untitled videos. A TikTok address names its author, so an untitled
   TikTok is "TikTok video by @name" / "Vídeo de TikTok de @name".

## Permissions

| Permission | Risk | Why |
| --- | --- | --- |
| `assets:js` | high | `assets/embeds.js` adds the play button and creates the player iframe after the click. Plugins cannot output `<button>` or `<iframe>` (the host sanitizer removes them), so this needs a script. |
| `csp:frame-src:https://www.youtube-nocookie.com` | high | Lets the page frame YouTube's privacy-enhanced player (declared under `"csp"`). |
| `csp:frame-src:https://player.vimeo.com` | high | Lets the page frame Vimeo's player. |
| `csp:frame-src:https://www.tiktok.com` | high | Lets the page frame TikTok's embed player. |

The CSP sources are added to public pages only, never to the admin panel, and
only while the plugin is enabled. They are pinned when the plugin is approved:
a package that asks for another source stops until it is approved again.
`assets/embeds.css` and `assets/tiktok.svg` need no permission.

`sap13ns plugin pack` reports the package as **CLEAN** with four
`perm.high-risk` medium findings (one per permission above). Enabling the
plugin asks for the high-risk confirmation. Version 1.1.0 adds the TikTok
source to `frame-src`, so a site that approved 1.0.0 must approve the update
again.

**No oEmbed.** TikTok's [oEmbed endpoint](https://developers.tiktok.com/doc/embed-videos)
would give the caption and a thumbnail, but it would need three more
permissions (`kv`, `http:www.tiktok.com` and the `event` hook): a content
filter has no network, the events do not carry a post's text, and a
rendered body is cached by the host for up to a day, so a caption fetched
later would show up late. The address already names the author, the link
text can give the title, and the player shows the caption once it loads.

## Settings

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `youtube` | bool | `true` | Embed YouTube links. Off leaves them as links. |
| `vimeo` | bool | `true` | Embed Vimeo links. |
| `tiktok` | bool | `true` | Embed TikTok links. |
| `notice` | text | empty | Privacy note under every embed; `{provider}` becomes YouTube, Vimeo or TikTok. Empty uses the built-in note in the post's language. |
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
  `embeds.css`, `embeds.js` and, for a TikTok placeholder, `tiktok.svg` from
  `/plugin-assets/embeds/…`. The browser makes no request to YouTube,
  Google, Vimeo or TikTok.
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
- **TikTok has no privacy-enhanced mode.** After the click the player loads
  from www.tiktok.com, which sees the visitor like any visit to TikTok and
  may set cookies or use the browser's storage. TikTok's embed documentation
  says nothing about cookies or data collection, so the click is the only
  protection: before it, nothing is requested from TikTok. In a test from
  Spain (October 2026) the player first showed TikTok's own "Allow cookies
  from TikTok on this browser?" dialog over the video. TikTok also says it
  "may apply usage limits to embedded players on a per-domain basis".
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
<div class="embed-frame"><button type="button" class="embed-play" aria-label="Play video: Our keynote (YouTube)"><span class="embed-play-mark" aria-hidden="true"></span><span class="embed-play-icon" aria-hidden="true"><svg …></svg></span><span class="embed-play-text"><span class="embed-play-title">Our keynote</span><span class="embed-play-provider">YouTube</span></span></button></div>
```

A bare TikTok URL in an English post:

```html
<figure class="card embed embed-tiktok embed-portrait" data-category="tiktok"><a class="embed-link" href="https://www.tiktok.com/@scout2015/video/6718335390845095173"><span class="embed-icon" aria-hidden="true"><span data-icon="play"></span></span><span class="embed-text"><strong class="embed-title">TikTok video by @scout2015</strong><small class="embed-source">Watch on TikTok</small></span></a><a class="embed-player" href="https://www.tiktok.com/player/v1/6718335390845095173?autoplay=1&amp;description=1&amp;rel=0" aria-label="Play TikTok video by @scout2015"><span class="embed-provider">TikTok</span></a><figcaption class="embed-notice">Nothing is loaded from TikTok until you press play. Playing the video connects to TikTok, which may collect data about your visit.</figcaption></figure>
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
its 16:9 ratio at any width (9:16 and at most 22rem wide for Shorts and
TikTok).

A TikTok placeholder has no thumbnail, since that would need a request to
TikTok. It shows TikTok's cyan and red glow and the TikTok note in the
corner instead, from `assets/tiktok.svg` (the glyph of
[Simple Icons](https://simpleicons.org), CC0 1.0), so the visitor knows
where the video comes from before pressing play.

## Adding a provider

Add an entry to `providers` in `providers.go` (id, name, URL parser that
returns the player URL), a bool setting with the same id, and the player's
origin under `"csp"` → `"frame-src"` in `plugin.json` (`TestManifestCoversProviders`
checks both). Prefer providers with a privacy mode; Dailymotion was left
out because it has none. TikTok has none either, but it was asked for and
it is embedded only after the click, like the others.

## References

- YouTube: [Embed videos and playlists](https://support.google.com/youtube/answer/171780)
  (privacy-enhanced mode) and [player parameters](https://developers.google.com/youtube/player_parameters).
- Vimeo: [Player parameters overview](https://help.vimeo.com/hc/en-us/articles/12426260232977-Player-parameters-overview) (`dnt`).
- TikTok: [Embed Player](https://developers.tiktok.com/doc/embed-player)
  (URL, parameters, postMessage events, per-domain limits) and
  [Embed videos](https://developers.tiktok.com/doc/embed-videos) (oEmbed).

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
