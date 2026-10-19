# YouTube gallery

Shows the latest videos of a YouTube channel or playlist as a grid or a
carousel, in posts with the `[youtube_videos]` shortcode and in any widget
area.

- Each video shows its thumbnail, its title, its date and a play button. The
  player loads in place, from **youtube-nocookie.com** (YouTube's
  privacy-enhanced mode), and **only after the visitor presses play**.
- **Without JavaScript every video is a link** to its page on YouTube. The
  carousel still scrolls by touch, trackpad or keyboard.
- The list comes from the channel's or playlist's **public feed**. No account
  or key is needed. With an optional **YouTube Data API key** the gallery
  can show more videos, with durations and live badges, and it accepts
  handles (`@name`).
- The list is stored by the plugin and refreshed every hour. A page never
  waits for YouTube, and a failed refresh keeps the last good list.
- English and Spanish texts follow the page's language.

## Quick start

1. Install and enable the plugin (Admin → Plugins), and approve its
   permissions.
2. In its settings, set **Channel** to the channel id (`UC…`). See
   [Choosing the channel](#choosing-the-channel).
3. Put `[youtube_videos]` on a line of its own in a post, or add the
   **YouTube videos** widget in Appearance → Widgets.

A gallery shows "Watch the videos on YouTube" until its first list arrives.
The default channel and playlist are fetched with the next hourly run.
Galleries with another channel or playlist are fetched when a post is
published or updated, or with the next hourly run (see
[Refresh and caching](#refresh-and-caching)).

## Choosing the channel

| You give | Example | Needs an API key |
| --- | --- | --- |
| Channel id | `UC_x5XG1OV2P6uZZ5FSM9Ttw` | no |
| Channel URL | `https://www.youtube.com/channel/UC_x5XG1OV2P6uZZ5FSM9Ttw` | no |
| Handle or handle URL | `@GoogleDevelopers`, `https://www.youtube.com/@GoogleDevelopers` | **yes** |
| Playlist id or URL | `PLOU2XLYxmsIJ8ItHmK4bRlY4GCzMgXLAJ`, `https://www.youtube.com/playlist?list=PL…` | no |

**Why handles need a key.** The feed only takes a channel id. Turning a
handle into an id without the API would mean reading the channel's HTML
page. In the EU that page redirects to a cookie-consent page, and it is
larger than the 1 MiB a plugin may download, so the plugin does not try.
With a key, the handle is looked up once with `channels.list?forHandle=`.

Old custom URLs (`youtube.com/c/Name`, `youtube.com/user/Name`) are refused
with a message, since neither the feed nor the API can read them.

**Finding a channel id:**

- Your own channel: sign in to YouTube, then profile picture → **Settings** →
  **Advanced settings**. The page shows the channel id (you must be the
  channel's primary owner).
- Any channel: on the channel's page, open the description ("…more"), then
  **Share channel** → **Copy channel ID**.
- A channel's uploads are also a playlist: its id is the channel id with
  `UU` in place of `UC` (`UU_x5XG1OV2P6uZZ5FSM9Ttw`).

## Shortcode

```text
[youtube_videos]
[youtube_videos count=4 layout=carousel title="Latest talks"]
[youtube_videos channel=UC_x5XG1OV2P6uZZ5FSM9Ttw count=8]
[youtube_videos playlist=PLOU2XLYxmsIJ8ItHmK4bRlY4GCzMgXLAJ layout=grid]
```

| Attribute | Default | Meaning |
| --- | --- | --- |
| `channel` | the settings | A channel id, channel URL or (with a key) handle. |
| `playlist` | the settings | A playlist id or URL. If both are given, the playlist wins. |
| `count` | `count` setting (6) | 1 to 24 videos. The feed has the latest 15. |
| `layout` | `layout` setting (`grid`) | `grid` or `carousel`. |
| `title` | none | A heading above the gallery. |

The settings' channel and playlist are used only when the shortcode has
neither. In a post the gallery is a Basecoat `card`.

## Widget

**YouTube videos** has the same options: channel, playlist, number of videos
and layout (a carousel by default, which suits a sidebar or a footer). Empty
fields use the plugin settings. The widget's own title is the heading.

## Settings

| Key | Type | Default | Meaning |
| --- | --- | --- | --- |
| `channel` | text | empty | The default channel: id, URL or (with a key) handle. |
| `playlist` | text | empty | A default playlist, used instead of the channel when set. |
| `count` | number | `6` | Videos per gallery, 1 to 24. |
| `layout` | select | `grid` | The shortcode's layout when it has no `layout=`. |
| `api_key` | secret | empty | Optional YouTube Data API v3 key. |
| `thumbnails` | bool | `true` | Load thumbnails from i.ytimg.com. Off shows a neutral placeholder. |
| `show_notice` | bool | `true` | The privacy note under each gallery. |
| `notice`, `notice_es`, `notice_en` | text | empty | Your own note; empty uses the built-in one in the page's language. |
| `privacy_url`, `privacy_url_es`, `privacy_url_en` | text | empty | `https://…`, `http://…` or a site path such as `/privacy`. Adds a "Privacy policy" link after the note. |

The language is the post's (for a widget, the page's), falling back to the
site language: `es` or `es-*` gives Spanish, anything else English. A
language-specific setting wins over the generic one.

## Feed or Data API

| | Public feed (no key) | Data API (with `api_key`) |
| --- | --- | --- |
| Videos | the latest 15 | up to 24 |
| Duration on the thumbnail | no | yes |
| Live and upcoming badges | no | yes |
| Shorts | linked to `/shorts/…` | linked to `/watch?v=…` (the API does not mark Shorts) |
| Handles (`@name`) | no | yes |
| Requests per refresh | 1 to www.youtube.com | 2 or 3 to www.googleapis.com |

**The API key.** In the Google Cloud console, enable the *YouTube Data API
v3* for a project, create an API key and restrict it to that API. The key is
sent in the `X-Goog-Api-Key` header, never in a URL, and never appears in the
plugin's log. The plugin stores it as a `secret` setting, so it is not shown
again in the admin panel and backups leave it out.

**Quota.** Every call the plugin makes (`channels.list`, `playlists.list`,
`playlistItems.list`, `videos.list`) costs 1 unit. A project gets 10,000
units a day by default. A refresh costs 2 units, or 3 when the channel's or
playlist's name is refreshed (once a day). Even the maximum of 16 sources
refreshed every hour stays under 1,200 units a day.

**When the API fails** for a reason other than "not found" (an exhausted
quota, a revoked key), the plugin tries the feed in the same run. Handles
need a channel id that the API found earlier.

With a key, the site is the API client. YouTube's API terms and developer
policies then apply to it, for example a link to YouTube's Terms of Service
and a privacy policy on the site.

## Refresh and caching

- **Rendering never fetches.** Shortcodes and widgets have no network access
  in sap13ns. They read the stored list, and register the channel or
  playlist they show so that the next refresh fetches it.
- **Refreshes** run on the host's `cron.hourly` event. They also run on
  `post.published` and `post.updated`, but these only fetch sources that
  never had a good fetch, so a gallery added to a post appears soon. A
  source is fetched at most every 55 minutes.
- **Failures keep the last good list.** After a failure the plugin waits
  1, 2, 4… hours (at most 24) before trying again. The first failure is
  logged as a warning; repeats are logged at debug level.
- **30 days.** A list that could not be refreshed for 30 days is no longer
  shown (the gallery falls back to the link). A source that no gallery
  showed for 30 days is forgotten. YouTube's developer policies allow API
  data to be stored for at most 30 days before it is refreshed or deleted.
- **Limits.** At most 16 channels and playlists across the site (the default
  one is always kept), 24 videos each, titles cut at 150 characters, and the
  host's 4 requests per run. Sources that do not fit in a run are fetched in
  the next one, the stalest first.
- **The host caches pages.** A widget shows a new list within about two
  minutes, because the page cache is short. A gallery inside a post is part
  of the post's rendered body, which the host caches; the shortcode gives it
  a max age of five minutes (`pluginsdk.HTMLMaxAge`), so a post shows a new
  list within about seven minutes of the fetch.

The plugin stores, per source: the video ids, titles, dates, durations and
Shorts/live flags, the channel's or playlist's name, and the time of the last
fetch. It stores nothing about visitors.

## Permissions

| Permission | Risk | Why |
| --- | --- | --- |
| `kv` | low | Stores the lists and the sources that galleries show. |
| `http:www.youtube.com` | medium | Reads the public feeds (`/feeds/videos.xml`). |
| `http:www.googleapis.com` | medium | The Data API, only when a key is set. |
| `assets:js` | high | `youtube.js` turns each video into a play button and creates the player after the click, and adds the carousel's buttons. Plugins cannot output `<button>` or `<iframe>` (the host sanitizer removes them), so this needs a script. |
| `csp:frame-src:https://www.youtube-nocookie.com` | high | Lets the page frame the privacy-enhanced player. |
| `csp:img-src:https://i.ytimg.com` | high | Lets the page show the thumbnails. |

The CSP sources apply to public pages only, and only while the plugin is
enabled. They are pinned when the plugin is approved, like the module and its
assets. HTTP goes through the host's SSRF guard, with a 6 s timeout and at
most 1 MiB per response.

`sap13ns plugin pack` reports the package as **CLEAN** with three
`perm.high-risk` medium findings: `assets:js` and the two CSP sources.

## Privacy

- **Before the click**, with `thumbnails` on (the default), the browser
  loads each thumbnail from **i.ytimg.com**, YouTube's image server. Google
  then sees the visitor's IP address, browser and the site's origin. The
  images are lazy-loaded, so only those near the screen are requested.
  Nothing else is loaded from YouTube: no script, no iframe, no cookies.
- **With `thumbnails` off**, nothing at all is requested from YouTube before
  the click. Each video shows a neutral placeholder with its title.
- **After the click** the player loads from **youtube-nocookie.com**.
  YouTube says privacy-enhanced mode does not store information about
  visitors who do not play the video. Once a video plays, YouTube may store
  identifiers in the browser and collect viewing data, as on youtube.com.
  The player gets the site's origin as the referrer
  (`referrerpolicy="strict-origin-when-cross-origin"`), not the page path.
- **The server** fetches the feed (or the API) from YouTube once an hour per
  source. These requests carry no visitor data.
- The note under each gallery says what loads when, and it changes with the
  `thumbnails` setting. Set `privacy_url` to link to your privacy policy.

Built-in notes (thumbnails on / off):

- "Thumbnails are loaded from YouTube. The player loads from
  youtube-nocookie.com only when you press play; playing a video connects to
  YouTube, which may collect data about your visit." / "Nothing is loaded
  from YouTube until you press play. Playing a video connects to YouTube,
  which may collect data about your visit."
- "Las miniaturas se cargan desde YouTube. El reproductor se carga desde
  youtube-nocookie.com solo cuando pulsas reproducir; al reproducir un vídeo
  te conectas a YouTube, que puede recoger datos de tu visita." / "No se
  carga nada de YouTube hasta que pulsas reproducir. Al reproducir un vídeo
  te conectas a YouTube, que puede recoger datos de tu visita."

### Cookie consent

Since 1.1.0 the plugin declares two entries in the `privacy` of its
`plugin.json`, both in the `media` category ("External media and
marketing"), so they appear in the site's cookie list (Settings → Privacy
and cookies, and the "Cookies" link at the bottom of every page):

- the **YouTube** player, a click-to-load service with the shared id
  `youtube`. Each gallery and widget carries `data-consent="youtube"` and its
  note `data-consent-note`: while the visitor has not allowed YouTube, the
  site's consent script adds "Always allow YouTube" and a "Cookie settings"
  link at the end of the note. Pressing play still loads that one video;
  "Always allow" or accepting the category remembers YouTube for the whole
  site (the embeds plugin and the core's video players included).
- the **thumbnails**, a resource: they load with the page (unless
  `thumbnails` is off) and set no cookies, so they are listed but not
  blocked. Turn `thumbnails` off if your site must not contact YouTube
  before the visitor chooses.

Older sap13ns versions do not know the `privacy` field and refuse the
manifest: use 1.0.0 there.

## Accessibility

- The gallery is a labelled region ("Latest videos from Google for Developers
  on YouTube"), and the videos are a list (`role="list"`, so Safari keeps
  announcing it without bullets).
- Without JavaScript each video is one link: the thumbnail is decorative
  (`alt=""`), and the link reads the title, plus the duration as words ("4
  min 13 s") when known.
- With JavaScript each thumbnail is a real `<button>` labelled "Play video:
  *title* (YouTube)". After the click the focus moves into the player, whose
  `title` names the video. Starting a video stops the other one playing in
  the same gallery.
- The carousel's buttons are labelled "Previous videos" and "Next videos".
  They use `aria-disabled` at either end and stay focusable. They are
  hidden when everything fits. Each press moves by the visible videos.
  Tabbing through the videos brings each one into view.
- With `prefers-reduced-motion` the carousel jumps instead of scrolling
  smoothly, and the hover zoom is off.
- Focus rings use the theme's `--ring` colour.

## Looks

The gallery uses the theme's tokens (`--primary`, `--ring`, `--border`,
`--muted-foreground`, `--radius`). In a post it is a Basecoat `card`, so it
follows every theme in light and dark mode. Thumbnails and the player are
dark in both modes. The grid fits as many 10rem columns as there is room
for. The carousel shows three videos, two in a narrow column such as a
sidebar or a phone, and one and a bit below 26rem. It was checked in the
apple, bento, dashboard, glassmorphism, neumorphism and onepage themes, at
1440 and 390 px, in light and dark.

## Example

`[youtube_videos count=1]` renders (no JavaScript):

```html
<section class="card ytg ytg-grid" aria-label="Latest videos from Google for Developers on YouTube">
  <ul class="ytg-list" role="list">
    <li class="ytg-item">
      <a class="ytg-link" href="https://www.youtube.com/watch?v=LJ2v4-FvPcA">
        <span class="ytg-thumb"><img src="https://i.ytimg.com/vi/LJ2v4-FvPcA/hqdefault.jpg" alt="" width="480" height="360" loading="lazy">
          <span class="ytg-badge" aria-hidden="true">…play icon…</span>
          <span class="ytg-duration" aria-hidden="true">4:13</span></span>
        <span class="ytg-body"><span class="ytg-title">Builders building with Gemini Audio</span><span class="sr-only"> (4 min 13 s)</span></span>
      </a>
      <span class="ytg-meta"><time datetime="2026-10-16T21:00:00Z">Oct 16, 2026</time></span>
      <a class="ytg-player" href="https://www.youtube-nocookie.com/embed/LJ2v4-FvPcA?autoplay=1" aria-label="Play video: Builders building with Gemini Audio (YouTube)"></a>
    </li>
  </ul>
  <footer class="ytg-footer">
    <p class="ytg-notice">Thumbnails are loaded from YouTube. … <a href="/privacy">Privacy policy</a></p>
    <a class="ytg-more" href="https://www.youtube.com/channel/UC_x5XG1OV2P6uZZ5FSM9Ttw">More videos on YouTube</a>
  </footer>
</section>
```

The hidden `a.ytg-player` gives the script the player address and the
button's label. A carousel also carries the buttons' labels and icons in a
hidden `span.ytg-ui`. After the click the script puts this in the item:

```html
<iframe src="https://www.youtube-nocookie.com/embed/LJ2v4-FvPcA?autoplay=1" title="Builders building with Gemini Audio (YouTube)" allow="accelerometer; autoplay; clipboard-write; encrypted-media; gyroscope; picture-in-picture; web-share" allowfullscreen="" referrerpolicy="strict-origin-when-cross-origin"></iframe>
```

## Limitations

- Handles need an API key. Old `/c/` and `/user/` URLs are not supported.
- The feed has no durations and no live status, and it has at most 15
  videos.
- There is no filter for Shorts or live streams. A Short's thumbnail is
  YouTube's 4:3 image, cropped to 16:9.
- A gallery in a post can lag by up to a day (see
  [Refresh and caching](#refresh-and-caching)). A new gallery shows the link
  until its first fetch.
- Private, deleted and not-yet-published videos are skipped by the API but
  may linger in the feed until YouTube updates it.

## References

- Feeds: the Atom format of the channel feed is described in
  [Subscribe to push notifications](https://developers.google.com/youtube/v3/guides/push_notifications)
  (`yt:videoId`, `yt:channelId`, `published`).
- Data API: [Getting started](https://developers.google.com/youtube/v3/getting-started)
  (quota), [Quota costs](https://developers.google.com/youtube/v3/determine_quota_cost),
  [channels.list](https://developers.google.com/youtube/v3/docs/channels/list) (`forHandle`),
  [playlists.list](https://developers.google.com/youtube/v3/docs/playlists/list),
  [playlistItems.list](https://developers.google.com/youtube/v3/docs/playlistItems/list),
  [videos.list](https://developers.google.com/youtube/v3/docs/videos/list).
- API keys: [Use API keys](https://cloud.google.com/docs/authentication/api-keys-use)
  (the `X-Goog-Api-Key` header).
- Policies: [YouTube API Services developer policies](https://developers.google.com/youtube/terms/developer-policies)
  (storage for at most 30 days, attribution).
- Channel id: [Find your channel's user and channel IDs](https://support.google.com/youtube/answer/3250431).
- Player: [Embed videos and playlists](https://support.google.com/youtube/answer/171780)
  (privacy-enhanced mode) and [player parameters](https://developers.google.com/youtube/player_parameters).

## Build and install

```sh
make build                               # plugin.wasm (GOOS=wasip1 GOARCH=wasm)
make test vet                            # native unit tests and vet
make install PLUGINS_DIR=/path/to/data/plugins
sap13ns plugin pack .                    # or build a .zip to upload
```

The tests run against an `httptest` fake of the feed and the Data API, plus
two real feeds captured on 2026-10-19 (`testdata/`). The plugin is also
bundled with sap13ns as an official plugin (`make plugins` at the root).
Then open Admin → Plugins, review the permissions, tick the confirmation for
the high-risk ones and enable the plugin.
