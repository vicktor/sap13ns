# X auto-post

Posts new articles to X (formerly Twitter) when they are published.

- **Text.** A short text in the post's language, with its title, link and
  hashtags made from its tags. It comes from a template and is fitted to
  X's 280 characters, counted the way X counts them.
- **Link card.** X builds the card under the post from the article's own
  page, which sap13ns fills with Open Graph and `twitter:card` tags.
- **Scheduled posts** go out once they are live. **No duplicates**: a post is
  never sent twice. **Retries**: a refused send is retried on the next
  hourly runs, three attempts in all, and a rate limit waits for the time X
  gives.
- **Activity log.** Everything the plugin does is written to the plugin's
  activity log in Admin → Plugins.

The events, the queue, the records and the retries are the ones of the
`social` plugin (Bluesky and Mastodon), shared through
[`pluginsdk/autopost`](../../pluginsdk/autopost). Its README explains them
in detail: [How it works](../social/README.md#how-it-works).

## Before you start: the X developer account and its price

Posting through the X API needs an **X developer account** and an **app**,
and every post is paid for.

- **Terms.** Signing up means accepting X's
  [Developer Agreement](https://docs.x.com/developer-terms/agreement)
  (last updated 27 April 2026), its
  [Developer Policy](https://docs.x.com/developer-terms/policy) and, for
  credits, the
  [Pay-Per-Use Agreement](https://docs.x.com/developer-terms/ppu-agreement).
  Read them first. Two points matter here:
  - The agreement describes self-serve access as meant for hobbyists,
    early-stage work and "supporting applications with a limited number of
    end-users"; beyond that "you must apply (or already subscribe to) an
    Enterprise plan". The plugin posts to one account, yours.
  - Automated actions on your account fall under X's Automation Rules,
    which the agreement incorporates.
- **Pricing.** On 19 October 2026, the
  [X API pricing page](https://docs.x.com/x-api/getting-started/pricing)
  describes pay-per-usage pricing with no subscription: you "Purchase
  credits upfront in the Developer Console", and "Credits are deducted as
  you make API requests". Its table of write operations lists:

  | Operation | Price |
  | --- | --- |
  | Post: Create | $0.015 per request |
  | Post: Create (with URL) | $0.200 per request |

  The page adds that "Prices are subject to change", so check it and the
  Developer Console before you start. The plugin's text includes the
  article's link (that is the point of announcing it), so expect the
  **with URL** price for every post. The page also lets you set a spending
  limit per billing cycle.
- **Dry run** sends nothing to X, so it costs nothing.

## Setting up

The plugin acts for one X account with **OAuth 1.0a user context**: the
app's two keys and the account's access token and secret, which the
Developer Console generates for the account that owns the app. No redirect
URI or sign-in flow is involved.

1. **Developer account.** Sign in at [console.x.com](https://console.x.com)
   with the X account the posts should come from, and accept the
   developer terms.
2. **App.** Create an app (in a project, if the console asks for one).
3. **Permissions first.** In the app's **User authentication settings**,
   set **App permissions** to **Read and write**. If the console asks for a
   callback URL and a website, enter your site's address: the plugin does
   not use them.
4. **Keys and tokens.** In **Keys and tokens**, copy the **API Key** and
   **API Key Secret**, then generate the **Access Token** and **Access Token
   Secret**. Generate the access token *after* step 3: a token made while
   the app was read-only stays read-only, and X answers 403. If you change
   the permissions later, regenerate it.
5. **Credits.** Buy credits in the Developer Console.
6. **The plugin.** In Admin → Plugins → X auto-post, enter the four values
   (they are stored encrypted, and the log never shows them), then turn
   **Dry run** on and publish a post to read the text in the activity log.
   Turn it off when the text is right.

## The text

The template has the placeholders `{title}`, `{url}`, `{excerpt}`,
`{hashtags}` and `{site}`. An empty template uses "New post: {title}" (or
"Nueva entrada: {title}" for Spanish posts), the link and the hashtags. A
Spanish and an English template can replace it for posts in those
languages.

**Counting.** X counts 280 units
([Counting characters](https://docs.x.com/fundamentals/counting-characters)):

- The letters of Latin, Greek, Cyrillic, Hebrew, Arabic and Indic scripts,
  and common punctuation, count 1. CJK characters and emoji count 2.
- Every link counts 23, whatever its length, so a long article address is
  never cut. A bare domain such as `example.com` may become a link, so the
  plugin counts it as at least 23.
- The plugin counts per code point. That is an upper bound: an emoji made
  of several code points (a flag, 👩‍💻) counts more than X counts, and the
  text may end up a little shorter than it could be. It never ends up
  longer.

**Fitting.** When the text is too long, the excerpt is shortened first,
then the title (both with "…"), then trailing hashtags are dropped. The
link and the hashtags are never cut in half.

**Hashtags.** Two by default (**Hashtags**): X readers tend to prefer few.

## The link card

The post itself carries only the text. X fetches the link with its crawler
(Twitterbot) and builds the card from the page's tags, which every sap13ns
page has:

- `og:title`, `og:description`, `og:url` and `og:image`;
- `twitter:card`: `summary_large_image` when the page has an image (the
  featured image, or the generated title card when that is on), `summary`
  otherwise.

So the card shows the article's current title, description and image. X
may keep a card it fetched for a while, so a change to a published page
can take time to show. Keep Twitterbot allowed in `robots.txt` (sap13ns
does: the AI crawler settings only concern AI training bots).

## Errors and retries

| X answers | What the plugin does |
| --- | --- |
| 201 | Posted. The log shows the post's address, `https://x.com/i/web/status/<id>`. |
| 429 Too Many Requests | Waits. The post is queued until the reset time X sends (`x-rate-limit-reset`, or the reset of a 24-hour cap that ran out), and is tried on the first hourly run after it. This does not count as an attempt. |
| 401 | Refused: the keys or tokens are wrong or were regenerated. Retried, then given up after three attempts. |
| 402 | Refused: no credits left. |
| 403 | Refused, with the reason X gives: an app that cannot use the endpoint (`client-not-enrolled`), a read-only token ("You are not permitted to perform this action"), or a text identical to a recent post. |
| 5xx | Refused for now: retried on the next hourly runs. |
| No answer (timeout) | Whether it was posted is unknown, so it is **not** retried, and the log asks you to check the account. |

**Rate limits.** The
[rate limits page](https://docs.x.com/x-api/fundamentals/rate-limits)
lists, for `POST /2/tweets`, 100 requests per 15 minutes per user and
10,000 per 24 hours per app. A blog posts far fewer.

## Permissions

| Permission | Why |
| --- | --- |
| `kv` | What was sent, and the queue of scheduled posts and retries. |
| `posts:read` | To check that a post is public before announcing it, and to read its current version when a queued post goes out. |
| `http:api.x.com` | `POST https://api.x.com/2/tweets`, nothing else. |

## Settings

| Setting | Default | Notes |
| --- | --- | --- |
| API key, API key secret | | The app's consumer key and secret. |
| Access token, Access token secret | | The account's token, generated with "Read and write". |
| Text, Text for Spanish posts, Text for English posts | empty | See [The text](#the-text). |
| Hashtags | 2 | How many of the post's tags become hashtags (at most 20). |
| Post | posts | posts, pages or both. |
| Skip posts older than (hours) | 48 | A post first published longer ago is never announced, so republishing an old article does not announce it again. |
| Dry run | off | Writes the text to the log instead of posting it. |

## Privacy

- **Sent to X:** the text, signed with the four credentials. The secrets
  themselves never leave the site: OAuth 1.0a sends only an HMAC signature
  made with them.
- **Kept in the plugin's storage:** for each post sent, its ID, the time
  and the state ("done", "failed" and the number of attempts); the queue
  (ID, type, language, title, due time; at most 100 posts); a short-lived
  lock token. Uninstalling with **Delete its data** removes all of it.

## Build and install

```sh
make build                               # plugin.wasm (GOOS=wasip1 GOARCH=wasm)
make test vet                            # native unit tests and vet
make install PLUGINS_DIR=/path/to/data/plugins
```

The plugin is one of the official plugins, so it also ships inside the
sap13ns binary: **Plugins → Official plugins → X auto-post → Install**.

The tests never call X: they use `pluginsdk/sdktest` with a fake X, and
check the signature against the example of X's
[Creating a signature](https://docs.x.com/fundamentals/authentication/oauth-1-0a/creating-a-signature)
page.

## Sources

- [Create Post, `POST /2/tweets`](https://docs.x.com/x-api/posts/create-post):
  the endpoint, the `text` field and the 201 answer with `data.id`.
- [OAuth 1.0a: Creating a signature](https://docs.x.com/fundamentals/authentication/oauth-1-0a/creating-a-signature)
  and [Authorizing a request](https://docs.x.com/fundamentals/authentication/oauth-1-0a/authorizing-a-request).
- [Counting characters](https://docs.x.com/fundamentals/counting-characters).
- [Rate limits](https://docs.x.com/x-api/fundamentals/rate-limits).
- [Pricing](https://docs.x.com/x-api/getting-started/pricing), read on
  19 October 2026.
- [Developer Agreement](https://docs.x.com/developer-terms/agreement),
  section III, self-serve use.

## License

MIT, like the SDK and the other official plugins. See [LICENSE](LICENSE).
