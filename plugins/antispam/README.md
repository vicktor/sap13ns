# Antispam

Checks every new comment before it is stored. It holds suspicious comments
for moderation (`pending`) and stores obvious spam as spam (`spam`).
Everything else (`ok`) follows the site's normal moderation rules.

- **Local checks.** All of them run inside the plugin, with no outside
  service:
  - links;
  - a blocklist of words, phrases, domains and addresses;
  - words that mix alphabets;
  - comments in capitals;
  - repeated characters and words;
  - first-time commenters who post a link.
- **StopForumSpam** (optional, **off by default**). The plugin can also look
  up the commenter's IP address and a hash of their e-mail address in the
  StopForumSpam database.
- **Verdict.** The strictest signal decides, and the reason lists every
  signal. Moderators see it in the activity log.

The server already rejects two kinds of submissions before any plugin runs:
those that fill the hidden honeypot field, and those sent less than three
seconds after the page loaded. Comments by moderators are never checked.

## Checks

| Check | Verdict | Setting |
| --- | --- | --- |
| More links than **Links before holding** (2) in the content | pending | `max_links` |
| At least **Links for spam** (6) links | spam | `spam_links` (0: off) |
| A link from someone who is not signed in and whose e-mail address has never had a comment approved | pending | `first_time_links` |
| A blocklist entry in the content, the name, the website or the e-mail address | spam (or pending) | `blocklist`, `blocklist_action` |
| A word mixing Latin with Cyrillic or Greek letters, such as "vіagra" with a Cyrillic "і" | pending | `script_check` = `mixed` or `latin` |
| More than half of at least 10 letters outside the Latin script | pending | `script_check` = `latin` |
| At least 20 letters, 70% or more of them capitals | pending | `caps_check` |
| A character 8 times in a row ("!!!!!!!!"), or a word 6 times in a row | pending | `repeat_check` |
| Listed by StopForumSpam with at least **spam confidence** (60%) | spam | `sfs_enabled`, `sfs_threshold` |
| Listed by StopForumSpam with a lower confidence | pending | `sfs_enabled` |

**Links** are `http://…`, `https://…` and `www.…` in the comment text. The
website field does not count as a link, but the reason mentions it.

**The blocklist** has one entry per line. Lines starting with `#` are notes.

- **Matching.** Case, accents and full-width forms are ignored, so `viagra`
  matches "VÍAGRA" and "ｖｉａｇｒａ".
- **Single words.** A word, domain or address matches whole words only:
  `sex` matches "sex" but not "Essex", and `casino.example` matches
  "www.casino.example".
- **Phrases.** An entry with spaces matches anywhere, whatever the spacing
  between its words.
- **Limits.** Up to 1000 entries of 100 characters.

**Alphabet checks** are off by default.

- **`mixed`** is safe for every site: real words rarely mix alphabets.
- **`latin`** also holds whole comments in other scripts. Use it only if
  your readers write in Latin script; it would hold every comment in
  Russian, Greek, Chinese or Arabic.

**Known commenters.** A commenter whose e-mail address had a comment
approved before is known, so their comments with a link are not held as
first comments. The server checks this and tells the plugin (`returning`);
a name alone does not count, because anyone can type a regular's name.
Signed-in users always count as known.

## Permissions

| Permission | Why |
| --- | --- |
| `comments:pii` | The commenter's e-mail address and IP address, needed only for the StopForumSpam lookup. |
| `http:api.stopforumspam.org` | The StopForumSpam lookup. |

`comments:pii` is a **high-risk permission**. Enabling the plugin asks for a
confirmation, and the scan shows a medium `perm.high-risk` finding.

- **All or nothing.** The server grants permissions all together, so the
  plugin receives e-mail and IP addresses even while StopForumSpam is off.
- **While StopForumSpam is off,** it never stores them, logs them or sends
  them anywhere.
- **Blocklist.** The only other use is matching the blocklist against the
  e-mail address.

## Settings

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `max_links` | number | `2` | More links than this: pending. `0` holds every comment with a link. |
| `spam_links` | number | `6` | At least this many links: spam. `0`: never. |
| `blocklist` | textarea | empty | One word, phrase, domain or address per line. |
| `blocklist_action` | select | `spam` | `spam` or `pending`. |
| `script_check` | select | `off` | `off`, `mixed` or `latin`. |
| `caps_check` | bool | `true` | Hold comments mostly in capitals. |
| `repeat_check` | bool | `true` | Hold repeated characters and words. |
| `first_time_links` | bool | `true` | Hold first comments with links. |
| `log_ok` | bool | `true` | Also log comments that pass. |
| `sfs_enabled` | bool | `false` | Ask StopForumSpam. |
| `sfs_check_ip` | bool | `true` | Look up the IP address. |
| `sfs_check_email` | bool | `true` | Look up the MD5 hash of the e-mail address. |
| `sfs_threshold` | number | `60` | Confidence (1–100) from which a listing means spam. |

An invalid number falls back to its default, with a warning in the log.

## StopForumSpam and privacy

[StopForumSpam](https://www.stopforumspam.com) is a free, third-party
database of addresses seen sending spam. When `sfs_enabled` is on, the
plugin sends, for each comment that passed every local check:

- **The IP address**, unless `sfs_check_ip` is off. Private and loopback
  addresses are never sent.
- **The MD5 hash of the lowercased e-mail address**, unless
  `sfs_check_email` is off. The address itself is never sent.
- **Never** the name, the website, the comment or the user agent.

**Why it is off by default.**

- **Personal data.** Both values are personal data under the GDPR. An MD5
  hash of an e-mail address is pseudonymous, not anonymous: anyone with a
  list of addresses can find which one it was.
- **Your decision.** Sending them to a third party is a decision for the
  site owner, and the privacy policy should say so. The local checks catch
  most spam without it.

**How the lookup behaves.**

- **When it runs.** Only when the local checks found nothing. A comment
  already held or rejected is not looked up.
- **The time budget.** The server gives all comment checks together two
  seconds. If a request is still running when they run out, the server
  discards the plugin's whole answer and holds the comment for moderation.
  A slow StopForumSpam can therefore hold a clean comment, but it can never
  turn a local "spam" verdict into "pending".
- **A failed lookup is ignored.** This is the plugin's own fallback, not the
  server's. When StopForumSpam answers in time with an error (no answer,
  a network error, an HTTP error or an unexpected answer), the plugin
  ignores the lookup. The local checks, which found nothing, decide, and
  the plugin answers an explicit `ok`. The log says why: "no answer in
  time", "network error", "HTTP 503" or "unexpected answer". The log line
  never contains the address or the request URL.
- **No cache.** Answers are not cached, so the plugin stores no IP
  addresses or e-mail hashes.

**What the plugin stores.** Nothing.

**What the log contains.**

- **Names.** Commenters' names, as the server's own lines do.
- **Reasons.** The signals that triggered, with at most a 30-character
  sample of the word that triggered them.
- **Never** an e-mail address, an IP address, an e-mail hash or a
  StopForumSpam URL.

## Examples

**Logged by the server** (held and spam comments):

```
comment.check: pending by "Ana Pérez": first comment with a link
comment.check: pending by "Ana Pérez": 3 links, plus a website address (more than 2)
comment.check: spam by "Viagra Shop": blocklist entry “viagra” in the name
comment.check: spam by "Ana Pérez": blocklist entry “viagra” in the content; first comment with a link; 100% capital letters; the word “viagra” 6 times or more in a row
comment.check: pending by "Ana Pérez": a word mixes Latin with Cyrillic or Greek letters (“vіagra”)
comment.check: spam by "Ana Pérez": listed by StopForumSpam: IP address (confidence 97%, seen 250 times)
comment.check: pending by "Ana Pérez": listed by StopForumSpam: e-mail address (confidence 12%, seen 2 times)
```

**Logged by the plugin:**

```
comment.check: ok by "Ana Pérez": no spam signals; first comment
comment.check: ok by "Ana Pérez": no spam signals; 1 link; signed-in user
comment.check: ok by "Ana Pérez": no spam signals; first comment; not listed by StopForumSpam
comment.check by "Ana Pérez": StopForumSpam: no answer in time; judged by the local checks only
```

**Where they appear.** The server writes held and spam comments to the
plugin's activity log itself, so the plugin logs only the comments that
pass. Turn `log_ok` off to keep the log for the held ones.

## Host gaps

- **No timing or honeypot signal.** The server rejects honeypot hits and
  very fast submissions itself, before `comment.check`
  (`internal/web/public.go`, `handleComment`). It passes nothing to plugins.
  - **Fix.** An `elapsed_ms` field (time from page load to submission)
    would let a plugin hold submissions that were fast but not fast enough
    to reject.
- **All-or-nothing permissions.** There are no optional permissions, so
  `comments:pii` is granted, and the high-risk confirmation asked, even when
  StopForumSpam stays off.
  - **Fix.** Optional permissions, granted one by one, would let the
    administrator enable the local checks alone. So would splitting
    StopForumSpam into its own plugin.
- **No per-request timeout in `http_fetch`.** Inside `comment.check` a slow
  request can use up the shared two-second budget. The server then discards
  the plugin's whole answer, whatever it had decided locally, and holds the
  comment as `pending`. Since bdef4a0 a failed check holds the comment
  rather than passing it.
  - **The workaround.** The plugin only asks when the local verdict is
    already `ok`, so a local `spam` is never lost. A slow StopForumSpam can
    still hold a clean comment.
  - **Fix.** A `timeout_ms` per request, or a fetch capped at the remaining
    budget minus a margin, would remove the restriction.

## Build and install

```sh
make build                               # plugin.wasm (GOOS=wasip1 GOARCH=wasm)
make test vet                            # native unit tests and vet
make install PLUGINS_DIR=/path/to/data/plugins
```

Then:

1. In Admin → Plugins, use **Reload** and enable the plugin. Confirm the
   high-risk `comments:pii` permission.
2. Review the settings. The defaults hold link-heavy, shouting and
   repetitive comments and first comments with links.
3. Add your blocklist.
4. Turn StopForumSpam on only after updating your privacy policy.
