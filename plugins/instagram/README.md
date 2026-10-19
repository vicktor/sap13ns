# Instagram

Publishes your new articles to Instagram, and shows your latest Instagram
posts on the site.

- **Publishing.** When a post with a JPEG featured image is published, the
  plugin publishes that image to your Instagram professional account, with
  a caption in the post's language: its title, excerpt, "Link in bio." and
  hashtags made from its tags. Posts without a featured image are skipped,
  and the activity log says why.
- **The feed.** `[instagram_feed]` in a post, or the **Instagram feed**
  widget, shows your latest posts as a **carousel** or a **grid**. Videos
  and reels show their thumbnail with a play badge, albums a badge of their
  own. Every post links to Instagram.
- **Works without JavaScript.** The carousel is a row that scrolls and snaps
  by touch, trackpad or keyboard. A small script adds previous and next
  buttons, which scroll smoothly unless the visitor asks for reduced motion.
- **The connection.** You connect the Instagram account once, on the
  plugin's page. sap13ns runs the OAuth flow, trades Instagram's one-hour
  token for a **60-day token**, keeps it encrypted, and **renews it by
  itself** after 30 days. The plugin only receives the access token while
  it works.
- **No duplicates**: a post is never published twice. **Retries**: a refused
  post is retried on the next hourly runs, three attempts in all.
  **Scheduled posts** go out once they are live.

The events, the queue, the records and the retries are the ones of the
`social` plugin (Bluesky and Mastodon), shared through
[`pluginsdk/autopost`](../../pluginsdk/autopost). Its README explains them
in detail: [How it works](../social/README.md#how-it-works).

## Setting up

You need an Instagram **professional account** (Business or Creator) and a
**Meta app**. A Facebook Page is not needed: the plugin uses the Instagram
API with Instagram Login.

1. **A professional account.** In the Instagram app, switch the account to a
   professional one if it is personal (Settings → Account type and tools →
   Switch to professional account).
2. **Create the app.** In the [Meta App Dashboard](https://developers.facebook.com/apps),
   create an app with the Instagram use case ("Manage messaging & content on
   Instagram"), or a **Business** app, and add the **Instagram** product.
3. **The redirect URI.** In the plugin's page (Admin → Plugins → Instagram),
   the **Connections** card shows the address to register, with a copy
   button:

   ```
   https://<your site>/admin/plugins/instagram/oauth/instagram/callback
   ```

   In the App Dashboard, open **Instagram → API setup with Instagram login →
   3. Set up Instagram business login → Business login settings** and add
   it under **OAuth redirect URIs**. Check the saved address: it must match
   exactly, without a trailing slash. On a network of sites, use the site's
   primary address (the one the card shows).
4. **The credentials.** The same **Business login settings** show the
   **Instagram App ID** and the **Instagram app secret**. Copy them into the
   plugin's settings and save. They are not the Meta app's ID and secret of
   App settings → Basic.
5. **Add your account to the app.** While the app is in development mode,
   only accounts with a role on it can connect. Add the Instagram account in
   **App roles → Roles → Instagram testers** and accept the invitation in
   Instagram (on the web: [Apps and websites → Tester invites](https://www.instagram.com/accounts/manage_access/)).
   Accounts you own or manage, added this way, are covered by **Standard
   Access**: there is no App Review to pass.
6. **Connect.** Enable the plugin, then **Connect Instagram** in the
   **Connections** card. Instagram asks you to sign in and to allow the app;
   you come back with "Connected to Instagram as <your username>", and the
   feed is fetched at once.
7. **Check the caption.** Turn **Dry run** on, publish a post with a JPEG
   featured image and read the activity log. Turn it off when the caption
   is right. Put the site's address in your Instagram bio, since the caption
   says "Link in bio."

**Scopes requested:** `instagram_business_basic` (the account, its media)
and `instagram_business_content_publish` (publishing), comma-separated, in
the authorization code flow. Instagram Login does not use PKCE; the app
secret stays on the server, and the core checks the `state` it signed for
your session. To use only the feed, turn **Publish new posts** off; the
publishing scope is still requested.

## The token

| Step | What happens |
| --- | --- |
| Connecting | The code is exchanged at `api.instagram.com/oauth/access_token` for a short-lived token, which is traded at once (`graph.instagram.com/access_token`, `ig_exchange_token`) for a long-lived one that lasts **60 days**. The core reads the account's id and username from `/me`. |
| Renewal | Once half of its life has passed (30 days), the core renews the token with `graph.instagram.com/refresh_access_token` (`ig_refresh_token`), from the hourly run or when the plugin asks for it. Meta renews tokens that are at least 24 hours old and still valid. |
| Failure | A failed renewal is logged in the activity log and tried again after 15 minutes while the old token lasts. A token that is not renewed within 60 days ends; the dashboard warns a week before, and **Connect again** fixes it. |
| Disconnecting | The core deletes the tokens; on its next hourly run the plugin deletes the stored feed. |

## Publishing

For each new post, the plugin:

1. checks that it is public, is a post type the plugin publishes (setting
   **Post**), is not older than **Skip posts older than**, and has a JPEG
   featured image;
2. creates an image container: `POST graph.instagram.com/v26.0/<IG_ID>/media`
   with `image_url` (the featured image's address on your site) and
   `caption`;
3. waits two seconds and reads the container's `status_code`;
4. publishes it: `POST /<IG_ID>/media_publish` with `creation_id`.

Instagram **downloads the image from your site**, so the site must be
reachable from the internet. The image must be:

- a **JPEG** (`.jpg` or `.jpeg`): PNG, WebP or AVIF featured images are
  skipped, with a log line;
- at most **8 MB**, with an aspect ratio between **4:5** (portrait) and
  **1.91:1** (landscape); Instagram scales the width to 320–1440 pixels.

The plugin sends the original upload, not a resized copy.

**The caption.** The template has the placeholders `{title}`, `{url}`,
`{excerpt}`, `{hashtags}` and `{site}`. An empty template uses:

- English: "New post: {title}", the excerpt, "Link in bio." and the hashtags;
- Spanish: "Nueva entrada: {title}", the excerpt, "Enlace en la bio." and
  the hashtags;
- "New page" / "Nueva página" for pages.

Links in captions are not clickable on Instagram, which is why the default
caption leaves the address out. The caption is fitted to Instagram's limits
of **2200 characters, 30 hashtags and 20 @ tags**: the excerpt is shortened
first, then the title, then hashtags are dropped. The **Hashtags** setting
(5 by default, at most 20) sets how many of the post's tags become
hashtags.

**Limits.** Instagram allows 100 posts published through the API per account
in a moving 24 hours (one section of Meta's documentation says 50). Each
post takes three requests, within the four a plugin may make per run.

## Errors and retries

| Instagram answers | What the plugin does |
| --- | --- |
| Published | The log shows the media id and the profile's address. The permalink appears in the feed on its next refresh. |
| Container `ERROR` or `EXPIRED` | Retried on the next hourly runs, three attempts in all, with the reason in the log. |
| Container still `IN_PROGRESS` | Waits three more seconds and publishes. If Instagram answers "media not ready" (9007/2207027), the post is tried again on the next hourly run with a new container; this does not count as an attempt. |
| Token refused (190) | Connect again on the plugin's page. |
| Permission refused (10, 200) | The connection lacks `instagram_business_content_publish`, or the account is not a professional account. |
| Image not downloaded (9004/2207052, 2207003) | The site is not reachable from the internet, or the image is not a public JPEG. |
| Image too large, wrong format or aspect ratio (2207004, 2207005, 2207009) | The log explains the image requirements. |
| Rate limits (4, 17, 32, 613) and the daily publishing limit (9/2207042) | Tried again an hour later; this does not count as an attempt. |
| No answer to `media_publish` (timeout) | Whether it was published is unknown, so it is **not** retried, and the log asks you to check the account. A container that got no answer is retried: nothing is published until `media_publish`. |

## The feed

```
[instagram_feed]
[instagram_feed count=8 layout=carousel]
[instagram_feed count=12 layout=grid title="On Instagram"]
```

| Attribute | Default | Notes |
| --- | --- | --- |
| `count` | the setting **Posts in the feed** (8) | 1 to 24. |
| `layout` | the setting **Feed layout** (carousel) | `carousel` or `grid`. |
| `title` | none | A heading above the feed. |

The **Instagram feed** widget has its own **Number of posts** and
**Layout**, and takes its title from the widget area.

- **What each post shows.** Its image, cropped to a square like the profile
  grid. Videos and reels show their thumbnail and a **play** badge; albums
  show their cover and an **images** badge. A video whose thumbnail
  Instagram withholds (for example because of licensed music) shows a
  neutral placeholder.
- **Accessibility.** Every post is a link whose text is its alt text on
  Instagram, or the start of its caption, or "Instagram post of <date>",
  plus "(video)" or "(several photos or videos)". The carousel's buttons
  have labels in the page's language and use `aria-disabled` at the ends of
  the row, so the focus stays on them. Focus rings use the theme's `--ring`.
- **Themes.** In a post the feed is a Basecoat `.card`; everything else uses
  the theme's tokens, so it follows each theme in light and dark mode.
- **English and Spanish** texts follow the page's language.

**Refresh and caching.**

- **Rendering never fetches.** Shortcodes and widgets have no network
  access in sap13ns: they read the list the plugin stored.
- The list is fetched in the background: when the account connects, and on
  every hourly run (at most every 55 minutes), in one request
  (`GET /<IG_ID>/media`, the newest 24 posts).
- **Failures keep the last good list.** After a failure the plugin waits 1,
  2, 4… hours (at most 24) before trying again. The first failure is logged
  as a warning.
- **Three days.** Instagram's image addresses are signed and expire. A list
  that could not be refreshed for three days is no longer shown; the feed
  falls back to a link to the profile.
- **The host caches pages.** A widget shows a new list within about two
  minutes. A feed inside a post is part of the post's rendered body: the
  shortcode tells the host to render it again after five minutes.
- **Captions.** Meta's reference marks the `caption` field as available with
  Facebook Login only. The plugin asks for it (and `alt_text`); if the API
  refuses the fields, it falls back to the ones every login has and
  remembers it, and the posts get the generic "Instagram post of <date>"
  text.

## Permissions

| Permission | Why |
| --- | --- |
| `kv` | The feed, what was published, and the queue of scheduled posts and retries. |
| `posts:read` | To check that a post is public before publishing it, and to read its current version when a queued post goes out. |
| `oauth:instagram` | The connection the core keeps for this plugin: the plugin receives its access token, never the app secret. |
| `http:www.instagram.com` | The authorization page, where the core sends you when you connect. |
| `http:api.instagram.com` | The token endpoint, which the core calls when you connect. |
| `http:graph.instagram.com` | The long-lived token and its renewal, `/me` (the core), and the media and publishing endpoints (the plugin). |
| `assets:js` | `instagram.js`, the carousel's previous and next buttons. Plugins cannot output `<button>` (the host sanitizer removes it), so this needs a script. |
| `csp:img-src:https://*.cdninstagram.com`, `csp:img-src:https://*.fbcdn.net` | Lets public pages show the images from Instagram's CDN. Images on other hosts are not shown, and the log names the host. |

`sap13ns plugin pack` reports the package as **CLEAN** with three
`perm.high-risk` medium findings: `assets:js` and the two CSP sources. The
CSP sources apply to public pages only, and only while the plugin is
enabled.

## Settings

| Setting | Default | Notes |
| --- | --- | --- |
| Instagram app ID, Instagram app secret | | From **Business login settings**. |
| Publish new posts | on | Off keeps only the feed. |
| Caption, Caption for Spanish posts, Caption for English posts | empty | See [Publishing](#publishing). |
| Hashtags | 5 | How many of the post's tags become hashtags (at most 20). |
| Post | posts | posts, pages or both. |
| Skip posts older than (hours) | 48 | A post first published longer ago is never published. |
| Dry run | off | Writes the caption to the log instead of publishing it. The feed is still refreshed. |
| Posts in the feed | 8 | For `[instagram_feed]` without `count=`. |
| Feed layout | carousel | For `[instagram_feed]` without `layout=`. |
| Show the privacy note | on | "Images are loaded from Instagram (Meta), which may collect data about your visit." under each feed. |
| Privacy policy link | empty | `https://…`, `http://…` or a site path such as `/privacy`. |

## Privacy

- **Visitors.** Pages with a feed load its images from Instagram's CDN, so
  Meta sees each visitor's IP address and browser. The images are
  lazy-loaded. Nothing else is loaded from Meta: no script, no iframe. The
  privacy note under each feed says so.
- **Sent to Instagram:** the featured image's address, the caption, and the
  requests for your own media, as you.
- **Kept by sap13ns:** the connection (tokens sealed with the site's key,
  the account id and username, the scopes and the expiry), deleted when you
  disconnect or uninstall the plugin. Exports and site backups leave it
  out.
- **Kept in the plugin's storage:** the feed (for each post: its id, type,
  image address, link, the start of its alt text or caption, and its date),
  deleted when the account is disconnected; for each post published, its ID,
  the time and the state; the queue (at most 100 posts); a short-lived lock
  token.
- **Cookie consent.** Since 1.1.0 the `privacy` of `plugin.json` declares
  the feed's images as a resource in the `media` category ("External media
  and marketing"), with Meta as the provider. They appear in the site's
  cookie list (Settings → Privacy and cookies, and the "Cookies" link at the
  bottom of every page). Because they load with the page and set no
  cookies, they are listed but not blocked, and they alone never show the
  consent banner. Older sap13ns versions do not know the `privacy` field
  and refuse the manifest: use 1.0.0 there.

## Build and install

```sh
make build                               # plugin.wasm (GOOS=wasip1 GOARCH=wasm)
make test vet                            # native unit tests and vet
make install PLUGINS_DIR=/path/to/data/plugins
```

The plugin is one of the official plugins, so it also ships inside the
sap13ns binary: **Plugins → Official plugins → Instagram → Install**. The
tests never call Instagram: they use `pluginsdk/sdktest` with a fake Graph
API and a fake connection.

## Sources

- [Business Login for Instagram](https://developers.facebook.com/docs/instagram-platform/instagram-api-with-instagram-login/business-login):
  the App Dashboard settings, the authorization and token endpoints, the
  scopes, long-lived tokens and their renewal, Standard and Advanced Access.
- [Content publishing](https://developers.facebook.com/docs/instagram-platform/content-publishing):
  containers, `status_code`, `media_publish`, the 24-hour limit.
- [IG User Media](https://developers.facebook.com/docs/instagram-platform/instagram-graph-api/reference/ig-user/media):
  `image_url`, `caption`, the JPEG and caption limits.
- [IG Media](https://developers.facebook.com/docs/instagram-platform/reference/instagram-media):
  the fields of the feed, `media_url` and `thumbnail_url`.
- [Error codes](https://developers.facebook.com/docs/instagram-platform/instagram-graph-api/reference/error-codes).

## License

MIT, like the SDK and the other official plugins. See [LICENSE](LICENSE).
