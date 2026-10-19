# LinkedIn

Shares new articles on your LinkedIn profile when they are published.

- **The post.** A text in the post's language (its title, excerpt and
  hashtags made from its tags) above an **article card** with the link,
  the title and the excerpt.
- **The connection.** You connect your LinkedIn account once, on the
  plugin's page. sap13ns runs the OAuth flow and keeps the tokens
  encrypted; the plugin only receives an access token while it posts.
- **60-day tokens.** LinkedIn's access tokens last 60 days and most apps
  get no refresh token. The dashboard and the Plugins page warn a week
  before the connection ends; **Connect again** renews it.
- **Scheduled posts** go out once they are live. **No duplicates**: a post is
  never shared twice. **Retries**: a refused share is retried on the next
  hourly runs, three attempts in all.
- **Activity log.** Everything the plugin does is written to the plugin's
  activity log in Admin → Plugins.

The events, the queue, the records and the retries are the ones of the
`social` plugin (Bluesky and Mastodon), shared through
[`pluginsdk/autopost`](../../pluginsdk/autopost). Its README explains them
in detail: [How it works](../social/README.md#how-it-works).

## Setting up

You need a LinkedIn account and a LinkedIn **app** in the
[Developer Portal](https://www.linkedin.com/developers/apps). The two
products the plugin uses are self-serve: LinkedIn grants them at once, with
no review.

1. **Create the app.** In the Developer Portal, **Create app**. LinkedIn asks
   for a LinkedIn Page to associate the app with; any page you manage will
   do, it does not decide where the posts go (they go to your profile).
2. **Add the products.** In the app's **Products** tab, request:
   - **Share on LinkedIn**, which grants `w_member_social` (post on your
     behalf);
   - **Sign In with LinkedIn using OpenID Connect**, which grants `openid`
     and `profile` (the plugin reads your member id and name from
     `/v2/userinfo`, to sign the posts as you and to show who is
     connected).
3. **The redirect URI.** In the plugin's page (Admin → Plugins → LinkedIn),
   the **Connections** card shows the address to register, with a copy
   button:

   ```
   https://<your site>/admin/plugins/linkedin/oauth/linkedin/callback
   ```

   In the app's **Auth** tab, add it under **Authorized redirect URLs for
   your app**. On a network of sites, use the site's primary address (the
   one the card shows); connecting from another of its domains is refused.
4. **The credentials.** From the same **Auth** tab, copy the **Client ID**
   and the **Primary Client Secret** into the plugin's settings and save.
5. **Connect.** Enable the plugin, then **Connect LinkedIn** in the
   **Connections** card. LinkedIn asks you to allow the app; you come back
   with "Connected to LinkedIn as <your name>".
6. **Check the text.** Turn **Dry run** on, publish a post and read the
   activity log. Turn it off when the text is right.

**Scopes requested:** `openid profile w_member_social`, space-separated, in
the authorization code flow. LinkedIn does not use PKCE for web apps; the
client secret stays on the server, and the core checks the `state` it
signed for your session.

## Reconnecting every 60 days

LinkedIn issues **programmatic refresh tokens** only to approved Marketing
Developer Platform partners, so a self-serve app gets an access token that
lasts 60 days and cannot renew it alone.

- **The notice.** Seven days before the end, the dashboard and the Plugins
  page show "The connection ends soon" with a link to the plugin's
  **Connections** card.
- **Renewing.** **Connect again** runs the flow once more; while you are
  signed in to LinkedIn and the app is still allowed, it is a single click.
- **If it ends.** Nothing is lost: posts that fall due while the account is
  not connected wait in the queue (the hourly log line says so) and go out
  after you reconnect, unless they are older than **Skip posts older than**
  by then.
- **If LinkedIn does issue a refresh token** (a partner app), the core
  renews the connection by itself, and the card says so.

## Company pages

The plugin shares on **your profile** (`urn:li:person:<id>`). Posting as a
**company page** needs `w_organization_social`, which only the **Community
Management API** grants. That product is not self-serve:

- LinkedIn reviews each application (a use case and a screencast of the
  integration), and requires the product to be alone on its app;
- the member who connects must hold an administrator or content role on
  the page.

This version does not post to pages. It is a small change once you have an
approved app (the author would be `urn:li:organization:<id>`, chosen in a
setting); open an issue if you need it.

## The text

The template has the placeholders `{title}`, `{url}`, `{excerpt}`,
`{hashtags}` and `{site}`. An empty template uses "New post: {title}" (or
"Nueva entrada: {title}" for Spanish posts), the excerpt and the hashtags.
The link is not in the text: the card under it carries it. A Spanish and an
English template can replace it for posts in those languages.

- **Escaping.** LinkedIn's commentary is
  [little text](https://learn.microsoft.com/en-us/linkedin/marketing/community-management/shares/little-text-format):
  `| { } @ [ ] ( ) < > # \ * _ ~` are markup. The plugin escapes them, so a
  title such as "C++ (en 2026)" shows as written and can never mention
  someone by accident.
- **Hashtags** become LinkedIn hashtags (`{hashtag|\#|OpenSource}`). Three
  by default.
- **Length.** At most 3000 characters, counted on the escaped text. A long
  excerpt is shortened with "…".

## The card

The Posts API does not read the page: the plugin sends the card's fields
itself.

| Field | Value |
| --- | --- |
| `source` | The article's address. |
| `title` | Its title (at most 400 characters). |
| `description` | Its excerpt (at most 4000; LinkedIn's limit is 4086). |
| `thumbnail` | Not sent. LinkedIn needs it as an image uploaded first through its Images API, which the plugin host cannot do (it would need to send the image bytes). The card shows without a picture. |

## Errors and retries

| LinkedIn answers | What the plugin does |
| --- | --- |
| 201 | Shared. The log shows `https://www.linkedin.com/feed/update/<urn>/`, from the `x-restli-id` header. |
| 401 | The token was revoked or expired: connect again. |
| 403 | The app lacks **Share on LinkedIn**, or the connection lacks `w_member_social`. |
| 426, `NONEXISTENT_VERSION` | The **LinkedIn API version** setting is too old or not a version: use a recent `YYYYMM`. |
| 429 | Waits until just after midnight UTC, when LinkedIn's daily limits reset; this does not count as an attempt. |
| Other 4xx and 5xx | Retried on the next hourly runs, three attempts in all, with LinkedIn's message in the log. |
| No answer (timeout) | Whether it was shared is unknown, so it is **not** retried, and the log asks you to check the profile. |

**API version.** Every request carries `LinkedIn-Version: 202610` (the
setting). LinkedIn publishes a version every month and supports each one
for at least a year; when the log reports that the version is no longer
active, move the setting forward.

**Rate limits.** Share on LinkedIn allows 150 requests per member and
100,000 per app each day (UTC). A blog shares far fewer.

## Permissions

| Permission | Why |
| --- | --- |
| `kv` | What was shared, and the queue of scheduled posts and retries. |
| `posts:read` | To check that a post is public before sharing it, and to read its current version when a queued post goes out. |
| `oauth:linkedin` | The connection the core keeps for this plugin: the plugin receives its access token, never the refresh token or the client secret. |
| `http:www.linkedin.com` | The authorization and token endpoints, which the core calls when you connect. |
| `http:api.linkedin.com` | `/v2/userinfo` (the core, when you connect) and `POST /rest/posts` (the plugin). |

## Settings

| Setting | Default | Notes |
| --- | --- | --- |
| Client ID, Client secret | | From the app's **Auth** tab. |
| Visibility | PUBLIC | PUBLIC (anyone on LinkedIn) or CONNECTIONS. |
| LinkedIn API version | 202610 | The `LinkedIn-Version` header. |
| Text, Text for Spanish posts, Text for English posts | empty | See [The text](#the-text). |
| Hashtags | 3 | How many of the post's tags become hashtags (at most 20). |
| Post | posts | posts, pages or both. |
| Skip posts older than (hours) | 48 | A post first published longer ago is never shared. |
| Dry run | off | Writes the text to the log instead of sharing it. |

## Privacy

- **Sent to LinkedIn:** the text and the card (address, title, excerpt),
  as you. When you connect, the core also reads your member id and name.
- **Kept by sap13ns:** the connection (tokens sealed with the site's key,
  your member id and name, the scopes and the expiry), deleted when you
  disconnect or uninstall the plugin. Exports and site backups leave it
  out.
- **Kept in the plugin's storage:** for each post shared, its ID, the time
  and the state; the queue (at most 100 posts); a short-lived lock token.

## Build and install

```sh
make build                               # plugin.wasm (GOOS=wasip1 GOARCH=wasm)
make test vet                            # native unit tests and vet
make install PLUGINS_DIR=/path/to/data/plugins
```

The plugin is one of the official plugins, so it also ships inside the
sap13ns binary: **Plugins → Official plugins → LinkedIn → Install**. The
tests never call LinkedIn: they use `pluginsdk/sdktest` with a fake API and
a fake connection.

## Sources

- [Authorization Code Flow (3-legged OAuth)](https://learn.microsoft.com/en-us/linkedin/shared/authentication/authorization-code-flow):
  the endpoints, the scopes and the 60-day access tokens.
- [Programmatic refresh tokens](https://learn.microsoft.com/en-us/linkedin/shared/authentication/programmatic-refresh-tokens):
  only for approved partners.
- [Sign In with LinkedIn using OpenID Connect](https://learn.microsoft.com/en-us/linkedin/consumer/integrations/self-serve/sign-in-with-linkedin-v2):
  `/v2/userinfo` and the member id (`sub`).
- [Share on LinkedIn](https://learn.microsoft.com/en-us/linkedin/consumer/integrations/self-serve/share-on-linkedin):
  the product, `w_member_social` and its daily limits.
- [Posts API](https://learn.microsoft.com/en-us/linkedin/marketing/community-management/shares/posts-api):
  `POST /rest/posts`, article posts (no URL scraping), `x-restli-id`, the
  errors.
- [little text format](https://learn.microsoft.com/en-us/linkedin/marketing/community-management/shares/little-text-format).
- [Versioning](https://learn.microsoft.com/en-us/linkedin/marketing/versioning)
  and [Rate limits](https://learn.microsoft.com/en-us/linkedin/shared/api-guide/concepts/rate-limits).
- [Community Management API](https://learn.microsoft.com/en-us/linkedin/marketing/community-management/community-management-overview):
  company pages.

## License

MIT, like the SDK and the other official plugins. See [LICENSE](LICENSE).
