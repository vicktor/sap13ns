# Social auto-post

Posts new articles to Bluesky and Mastodon when they are published.

- **Text.** Each network gets a short text in the post's language, with its
  title, link and hashtags made from its tags. The text comes from a
  template and is fitted to each network's limit.
- **Bluesky.** The post has clickable links and hashtags (rich-text facets),
  the post's language and a link card with the title and the excerpt.
- **Mastodon.** The status is sent with the post's language and the chosen
  visibility.
- **Scheduled posts** go out when they go live: the server announces them
  within a minute of their date.
- **No duplicates.** A post is never sent twice to the same network, whether
  it is republished, edited or delivered twice by the server.
- **Retries.** A failed send is retried on the next hourly runs, three
  attempts in all.
- **Activity log.** Everything the plugin does is written to the plugin's
  activity log in Admin → Plugins.

## How it works

| Event | What the plugin does |
| --- | --- |
| `post.published` | If the post is public now, it is sent at once. The server sends this event when a post goes live, scheduled posts included. Servers before the go-live announcement sent it when a post was scheduled; the plugin then queues the post until it is public. |
| `cron.hourly` | Sends the queued posts whose time has come and are public, and retries failed sends. |
| `post.updated` | If the post is queued, its date and title are updated, so a rescheduled post goes out at its new time. Edits are never posted. |
| `post.trashed` | Removes the post from the queue. |

**Never twice.** Before any request leaves, the plugin records the post as
"sending" to that network, and once the network answers it records "done"
or "failed".

- **Failed.** Only a send the network refused (an error status) or one that
  never left (unknown host, denied host) is marked failed and retried.
- **No answer.** If the request got no answer, such as a timeout, nobody
  knows whether the post was published. The plugin does not retry it, and
  the log asks you to check the account.
- **Mastodon.** Each status also carries an `Idempotency-Key`
  (`sap13ns-<site host>-<post id>`), so Mastodon itself ignores a repeated
  request.
- **Two workers.** The server runs two event workers at once. The records
  are updated under a lock in the plugin's storage, so two events cannot both
  send the same post.

**Public check.** A post is sent only once `posts_query` returns it. That
query only ever returns public content, never drafts, private, scheduled or
trashed posts. The plugin checks the newest 20 posts of the type and
language, then searches by title.

- **Not public yet.** The post stays in the queue and is checked every hour.
- **After 48 hours.** A post still not public is dropped.

**Old posts.** A post first published more than **Skip posts older than**
hours ago (48 by default) is never posted, so unpublishing and republishing
an old article does not announce it again.

**Budget.** The server allows 4 HTTP requests per call. Bluesky needs two
(sign in, post) and Mastodon one, so one call sends at most one post to both
networks plus a second post to Mastodon. What does not fit waits for the
next hourly run, and the log says so.

## Permissions

| Permission | Why |
| --- | --- |
| `kv` | What was sent where, and the queue of scheduled posts and retries. |
| `posts:read` | To check that a post is public before announcing it, and to read its current version when a queued post goes out. |
| `http:bsky.social` | Bluesky: sign in with the app password and create the post. |
| `http:mastodon.social`, `http:mstdn.social`, `http:mastodon.world`, `http:mastodon.online`, `http:mas.to`, `http:techhub.social`, `http:infosec.exchange`, `http:universeodon.com`, `http:fosstodon.org`, `http:hachyderm.io`, `http:masto.es`, `http:social.vivaldi.net` | The Mastodon instance you configure. Only one is used, the one in **Mastodon instance**. |

**The trade-off.** A plugin can reach only the hosts its manifest lists, and
the administrator approves that list when enabling it. A plugin cannot ask
for "the instance the administrator will type later".

- **The choice.** The manifest lists bsky.social and twelve large general
  Mastodon instances. The approval page shows exactly which hosts the plugin
  could reach. In practice it sends only to the one you configure.
- **Other servers.** An instance or a Bluesky server outside the list needs
  one line in the manifest; see the next section. A configured host that is
  not covered fails with a log line naming the missing permission.

### Adding a Mastodon instance or a Bluesky server

1. **Edit the manifest.** Add the host to `permissions` in `plugin.json`, for
   example `"http:social.example.org"`, or your PDS host for Bluesky
   (`"http:pds.example.org"`).
2. **Repack.** From the plugin's directory, run
   `sap13ns plugin pack -no-build .` (or `make build` first if you changed
   code).
3. **Upload.** Upload the zip in Admin → Plugins → Upload a plugin.
4. **Enable again.** The permissions changed, so the plugin becomes
   **modified** and stops until you enable it again. The approval page shows
   the new host. Settings and records are kept.

## Settings

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `bluesky_handle` | text | empty | For example `alice.bsky.social` (a leading `@` is removed). Empty: nothing goes to Bluesky. |
| `bluesky_app_password` | secret | — | An **app password**, from Bluesky → Settings → Privacy and security → App passwords. Never your account password. |
| `bluesky_service` | url | `https://bsky.social` | The server your account lives on. Change it only for your own PDS, whose host must then be in the permissions. |
| `mastodon_instance` | url | empty | For example `https://mastodon.social`. Only the host is used. Empty: nothing goes to Mastodon. |
| `mastodon_token` | secret | — | An access token: Preferences → Development → New application, with only the `write:statuses` scope. |
| `mastodon_visibility` | select | `public` | `public` or `unlisted` (kept out of the public timelines). |
| `template` | textarea | empty | The text. Empty uses the built-in text below. |
| `template_es` | textarea | empty | Text for posts in Spanish (`es`, `es-MX`…). |
| `template_en` | textarea | empty | Text for posts in English (`en`, `en-GB`…). |
| `max_hashtags` | number | `5` | How many of the post's tags become hashtags (at most 20). `0` adds none. |
| `post_types` | select | `posts` | `posts`, `pages` or `both`. |
| `skip_older_than_hours` | number | `48` | Posts first published longer ago are never posted. |
| `dry_run` | bool | `false` | Write the text that would be posted to the activity log instead of posting it. Nothing is recorded or queued. |

**Secrets.** They are encrypted at rest, never shown again in the admin
panel, and never written to the log. That includes the Bluesky session token
the plugin gets on each send. The plugin needs nothing else from your
accounts.

**Language.** The post's language decides the text, with the site's
language as a fallback:

1. Spanish posts use `template_es`, English posts use `template_en`.
2. Any other language, or an empty per-language setting, uses `template`.
3. If that is empty too, the built-in text is used:

```
New post: {title}          Nueva entrada: {title}

{url}                      {url}

{hashtags}                 {hashtags}
```

Pages say "New page:" / "Nueva página:".

### The text

**Placeholders.**

| Placeholder | Value |
| --- | --- |
| `{title}` | The post's title. |
| `{url}` | The post's address. |
| `{excerpt}` | Its excerpt. |
| `{hashtags}` | The hashtags, separated by spaces. |
| `{site}` | The site's title. |

**Layout.** The title and the excerpt are kept on one line each. A line left
empty by a placeholder disappears, and at most one blank line is kept
between paragraphs.

**Limits.** Bluesky allows 300 characters. Mastodon allows 500, and every
link counts as 23 there, however long it is. When the text is too long the
plugin shortens it in this order:

1. the excerpt, at a word boundary with "…", and then drops it;
2. the title;
3. hashtags, from the end.

The link and the hashtags are never cut in half.

**Hashtags** come from the post's tags:

- Multi-word tags are CamelCased: `open source` → `#OpenSource`,
  `inteligencia artificial` → `#InteligenciaArtificial`.
- Single words keep their case (`#iOS`).
- Accents and non-Latin scripts are kept (`#Café`, `#日本語`). Punctuation is
  removed.
- Tags that are only digits, longer than 64 characters or repeated (ignoring
  case) are skipped.

## Example

A Spanish post, "Hola, ¿qué tal?", tagged `open source`, `Go` and `go`, with
the default settings. With **Dry run** on, the activity log shows:

```
post 12 “Hola, ¿qué tal?”: dry run, Bluesky (POST https://bsky.social/xrpc/com.atproto.repo.createRecord as alice.bsky.social, 77 characters): "Nueva entrada: Hola, ¿qué tal?\n\nhttps://example.test/es/hola\n\n#OpenSource #Go"
post 12 “Hola, ¿qué tal?”: dry run, Mastodon (POST https://mastodon.social/api/v1/statuses, visibility public, 72 characters): "Nueva entrada: Hola, ¿qué tal?\n\nhttps://example.test/es/hola\n\n#OpenSource #Go"
```

The Bluesky record the plugin creates (facet offsets are UTF-8 bytes, as the
AT Protocol requires):

```json
{"$type":"app.bsky.feed.post","text":"Nueva entrada: Hola, ¿qué tal?\n\nhttps://example.test/es/hola\n\n#OpenSource #Go","createdAt":"2026-10-18T12:00:00.000Z","langs":["es"],"facets":[{"index":{"byteStart":34,"byteEnd":62},"features":[{"$type":"app.bsky.richtext.facet#link","uri":"https://example.test/es/hola"}]},{"index":{"byteStart":64,"byteEnd":75},"features":[{"$type":"app.bsky.richtext.facet#tag","tag":"OpenSource"}]},{"index":{"byteStart":76,"byteEnd":79},"features":[{"$type":"app.bsky.richtext.facet#tag","tag":"Go"}]}],"embed":{"$type":"app.bsky.embed.external","external":{"uri":"https://example.test/es/hola","title":"Hola, ¿qué tal?","description":"Un saludo desde Málaga."}}}
```

The Mastodon request is `POST /api/v1/statuses` with
`{"status":"…","language":"es","visibility":"public"}`.

**Activity log lines:**

```
post 12 “Hola, ¿qué tal?”: posted to Bluesky: at://did:plc:abc/app.bsky.feed.post/3kxyz (bsky.app/profile/did:plc:abc/post/3kxyz)
post 12 “Hola, ¿qué tal?”: posted to Mastodon: https://mastodon.social/@alice/1
post 12 “Hola, ¿qué tal?”: already posted to Bluesky and Mastodon; it is never posted twice
post 12 “Hola, ¿qué tal?”: scheduled for 2026-10-18 14:00 UTC; it will be posted within an hour after it goes live
post 12 “Hola, ¿qué tal?”: Mastodon failed (attempt 1 of 3): HTTP 503: Service unavailable; it will be retried on the next hourly run
post 12 “Hola, ¿qué tal?”: Mastodon failed (attempt 1 of 3): the host social.example.org is not in this plugin's permissions (http:social.example.org); see “Adding a Mastodon instance or a Bluesky server” in the plugin's README
post 12 “Hola, ¿qué tal?”: Mastodon: no answer (Post "https://mastodon.social/api/v1/statuses": context deadline exceeded); whether it was posted is unknown, so it is not retried. Check the account
post 13 “Segunda”: Bluesky: this run used its 4 requests; it will be posted on the next hourly run
post 12 “Hola, ¿qué tal?”: not posted: it was first published on 2026-10-15 11:00 UTC, more than 48 hours ago (setting “Skip posts older than”)
```

Error answers are reduced to their `error` and `message` fields. Response
bodies, tokens and passwords never reach the log.

## Privacy

**What is sent to Bluesky and Mastodon.**

- **The text.** The title, the address, the excerpt if your template uses
  it, the hashtags and the site title.
- **Bluesky.** Also the post's language and the link card (title and
  excerpt).
- **Your credentials** go only to the server they belong to.

Nothing about readers or commenters is ever sent.

**What is stored** in the plugin's key-value storage:

- **Records.** For each post: the post ID, the state per network and when it
  last changed. At most 3000 posts are kept, the oldest are forgotten first.
- **Queue.** For each queued post: its ID, type, language, title and due
  time. At most 100 posts.
- **Lock.** A short-lived random token.

Uninstalling with **Delete its data** removes all of it.

## Host gaps

- **No compare-and-swap in the key-value store.** The plugin builds its lock
  from write, wait 250 ms and read back. That adds a quarter of a second per
  update, and it relies on both writes landing within the wait. A `kv_cas`,
  or a per-plugin lock in the host, would make it exact.
- **Hosts are fixed at approval.** An `http:` permission cannot follow a
  setting, hence the list of instances and the repack procedure above. A
  permission such as `http:setting:mastodon_instance` would close that gap,
  approved as "the host the administrator enters in this setting".
- **4 requests per call.** This limits how many queued posts one hourly run
  can send. It is fine for a blog, slow for a backlog after a long outage.

## Build and install

```sh
make build                               # plugin.wasm (GOOS=wasip1 GOARCH=wasm)
make test vet                            # native unit tests and vet
make install PLUGINS_DIR=/path/to/data/plugins
```

Then:

1. In Admin → Plugins, use **Reload** and enable the plugin. Approving pins
   the hash of `plugin.wasm` and the permissions.
2. Enter a Bluesky handle and app password, a Mastodon instance and token,
   or both.
3. To check the text first, turn **Dry run** on and publish a post, then
   read the activity log.
