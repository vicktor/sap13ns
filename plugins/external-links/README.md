# External links

A `content.filter` plugin that marks links to other sites in posts and pages.

- Every external link gets `rel="noopener noreferrer"`.
- Links to **sponsored domains** also get `rel="sponsored nofollow"`.
- With **Open external links in a new tab** on, external links get
  `target="_blank"` and a visually hidden " (opens in a new tab)" (Spanish:
  " (se abre en una pestaña nueva)") inside the link, using the `sr-only`
  class every theme provides, so screen-reader users know what will happen.
- Links to **UTM domains** get `utm_source`, `utm_medium` and `utm_campaign`
  parameters. Parameters already in the URL are never overwritten, and the rest
  of the URL (order of parameters, fragment) is kept as written.

It needs no permissions.

## What counts as external

A link is external when it is an absolute `http://`, `https://` or
protocol-relative (`//host/path`) URL whose host differs from the host of the
site URL the server sends with every request.

- Host names are compared case-insensitively, without a trailing dot and in
  their ASCII (Punycode) form, so `bücher.example` and `xn--bcher-kva.example`
  are the same host.
- Ports are ignored: `example.com:8080` is the same site as `example.com`.
- A leading `www.` is ignored: `www.example.com` is the same site as
  `example.com`. Other subdomains (`blog.example.com`) are external.
- Fragments (`#top`), relative links (`/blog/x`, `../x`, `?page=2`),
  `mailto:`, `tel:`, `javascript:` and every other scheme are skipped, and so
  are links inside `<pre>` and `<code>`.

Internal links are never modified.

## Settings

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `new_tab` | bool | `false` | Add `target="_blank"` (only when the link has no `target`) and the hidden "(opens in a new tab)" text. |
| `sponsored_domains` | textarea | empty | Domains whose links get `rel="sponsored nofollow"`. |
| `utm_domains` | textarea | empty | Domains whose links get UTM parameters. |
| `utm_source` | text | empty | Empty uses the site's host name. |
| `utm_medium` | text | `referral` | Empty leaves the parameter out. |
| `utm_campaign` | text | empty | Empty leaves the parameter out. |

Domain lists take one domain per line, or domains separated by commas or
spaces. A domain also matches its subdomains (`amazon.com` matches
`smile.amazon.com`). Schemes, paths, ports, `*.` and `www.` are removed, so
`https://www.amazon.com/ref` is read as `amazon.com`.

## Examples

Site `https://example.com`, `sponsored_domains = amazon.com`,
`utm_domains = partner.test`, `utm_campaign = fall`, `new_tab` on:

```html
<a href="https://other.test/x">Other</a>
<a href="https://smile.amazon.com/dp/1">Book</a>
<a href="https://partner.test/p?id=7#buy">Partner</a>
<a href="https://www.example.com/about">About</a>
```

becomes

```html
<a href="https://other.test/x" rel="noopener noreferrer" target="_blank">Other<span class="sr-only"> (opens in a new tab)</span></a>
<a href="https://smile.amazon.com/dp/1" rel="noopener noreferrer sponsored nofollow" target="_blank">Book<span class="sr-only"> (opens in a new tab)</span></a>
<a href="https://partner.test/p?id=7&amp;utm_source=example.com&amp;utm_medium=referral&amp;utm_campaign=fall#buy" rel="noopener noreferrer" target="_blank">Partner<span class="sr-only"> (opens in a new tab)</span></a>
<a href="https://www.example.com/about">About</a>
```

The hidden text follows the post's language (`es` gives Spanish, anything
else English), falling back to the site language. Running the filter twice
gives the same result: existing `rel` tokens are kept and deduplicated, and a
link that already contains an `sr-only` element gets no second one.

## Host compatibility

The server sanitizes the HTML returned by every plugin. With the current
sanitizer policy (`internal/content/markdown.go`):

- `rel` and `target` are not in the list of allowed attributes, so they are
  removed from the plugin's output, and the sanitizer then adds
  `rel="nofollow"` to every link (including internal ones). Until the policy
  allows `rel` (space-separated tokens) and `target="_blank"` on `<a>`, only
  the UTM parameters and the hidden text survive. Keep `new_tab` off until
  then: the hidden text would announce a new tab that does not open.
- `tel:` links lose their `href` (the scheme is not allowed); the plugin skips
  them anyway.
- The whole document is sanitized again after every `content.filter` plugin,
  and `<svg>` is not allowed, so icons expanded earlier (by shortcodes or other
  filters) would be lost. When the plugin returns HTML it turns those Lucide
  SVGs back into `<span data-icon="…">` placeholders, which the server expands
  again.

## Build and install

```sh
make build                               # plugin.wasm (GOOS=wasip1 GOARCH=wasm)
make test vet                            # native unit tests and vet
make install PLUGINS_DIR=/path/to/data/plugins
```

Then open Admin → Plugins, use **Reload**, enable the plugin (approving pins
the hash of `plugin.wasm`) and adjust its settings.
