# Posting to social networks

Four official plugins announce new articles on social networks, and one of
them also shows your Instagram posts on the site. They are MIT-licensed,
ship inside the binary (**Plugins → Official plugins**) and each one has a
README with the exact setup.

| Plugin | Networks | What is posted | Credentials | Cost |
| --- | --- | --- | --- | --- |
| [social](../plugins/social) | Bluesky, Mastodon | A text with the title, link and hashtags; Bluesky adds a link card | An app password (Bluesky), an access token (Mastodon) | Free |
| [x-autopost](../plugins/x-autopost) | X | A text of up to 280 characters with the link; X builds the card from the page's Open Graph tags | The four OAuth 1.0a keys of an X developer app | X charges per post, from prepaid credits |
| [linkedin](../plugins/linkedin) | LinkedIn (your profile) | A text and an article card with the link, title and excerpt | A LinkedIn app, connected with OAuth | Free; reconnect every 60 days |
| [instagram](../plugins/instagram) | Instagram (professional accounts) | The featured image (JPEG) with a caption that ends "Link in bio."; plus a feed of your latest posts as a carousel or a grid | A Meta app, connected with OAuth | Free; the token renews itself |

## What they share

The four are built on [`pluginsdk/autopost`](../pluginsdk/autopost), so they
behave the same way:

- **When.** A post is announced when it goes live, scheduled posts
  included. Edits are never announced, and a post first published longer ago
  than **Skip posts older than** (48 hours by default) never is, so
  republishing an old article stays quiet.
- **Never twice.** Before a request leaves, the post is recorded as being
  sent. A network that refused it is retried on the next hourly runs, three
  attempts in all; a request that got no answer is never retried, because
  nobody knows whether it was published.
- **Waiting.** Rate limits wait for the time the network gives (or until its
  daily limits reset) without counting as attempts. Posts that fall due
  while a network is not configured or not connected wait in the queue.
- **Text.** A template per language (`{title} {url} {excerpt} {hashtags}
  {site}`), with English and Spanish defaults, fitted to each network's
  limit and way of counting. Hashtags come from the post's tags.
- **Dry run.** Writes the text to the plugin's activity log instead of
  posting it.

**OAuth connections.** LinkedIn and Instagram use the core's
[OAuth connections](PLUGINS.md#oauth-connections): the administrator
connects the account on the plugin's page, the core keeps the tokens sealed
and renews them, and the plugin only receives an access token while it
posts. The dashboard warns a week before a connection ends.

## TikTok: why there is no plugin

TikTok was studied and not implemented. In short, its rules do not allow
the plugin a sap13ns site would need.

**What the API offers.** The
[Content Posting API](https://developers.tiktok.com/doc/content-posting-api-get-started)
posts videos and photos, never text or links:

- **Direct Post** (scope `video.publish`) publishes to the creator's account;
- **Upload** (scope `video.upload`) sends a draft to the creator's TikTok
  inbox, which they finish in the app; at most five drafts may be pending in
  24 hours;
- photos come from your server with `PULL_FROM_URL`, which needs the domain
  verified with TikTok first;
- access tokens last 24 hours and refresh tokens 365 days. The core's OAuth
  connections could run that flow with one addition: TikTok names the
  client `client_key`, not `client_id`.

**What blocks it.** TikTok's
[Content Sharing Guidelines](https://developers.tiktok.com/doc/content-sharing-guidelines):

- **Unaudited apps post privately.** Until TikTok audits an app, everything
  it posts is visible only to the creator (`SELF_ONLY`), and at most five
  users may post through it in 24 hours.
- **The audit refuses this use.** Among the apps TikTok says it will not
  approve: "a utility tool to help upload contents to the account(s) you or
  your team manages", and apps for internal or private use. Each sap13ns
  site would register its own app to post to its own account, which is
  exactly that.
- **The required interface is the plugin's opposite.** Before each post the
  app must show the creator's name, a preview, a privacy choice with no
  default, the comment, Duet and Stitch switches off by default, the
  commercial content disclosure and TikTok's music terms, and send only
  after the creator's explicit consent. An automatic announcement on
  publication cannot ask any of that.
- **No links.** Captions have no clickable links, and apps may not add
  watermarks or promotional elements to the content. A post would be the
  featured image with a caption, which is what Instagram already gets.

**What would be possible.** A manual "send to TikTok" button in the post
editor that uploads the featured image as a draft to the creator's inbox
(the Upload mode) would fit TikTok's rules better, but it is a core feature
with its own interface, not an event plugin, and it would still be limited
to private posts unless TikTok audited a shared sap13ns app. For now the
[`embeds`](../plugins/embeds) plugin shows TikTok videos in posts, loaded
only after a click.
