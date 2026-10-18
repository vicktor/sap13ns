# Schema.org structured data

A `head.meta` plugin that describes every public page with Schema.org JSON-LD,
so search engines and other readers know what the page is, who published it
and where it sits in the site.

Each page gets one `<script type="application/ld+json">` with an `@graph` of
nodes linked by `@id`:

| Node | Where | What it says |
| --- | --- | --- |
| `WebSite` | every page | The site of the page's language (`/` or `/<lang>/`), its name and publisher. On the home page it adds a `SearchAction` pointing at the site search (`/search?q=…`, `/es/search?q=…`). |
| `Organization` or `Person` | every page | The publisher: name, address, logo (or photo) and official profiles (`sameAs`). |
| `WebPage` | every page | `CollectionPage` for the home listing and archives, `SearchResultsPage` for search, `WebPage` otherwise. Name, description, language, primary image and publication date. |
| `BlogPosting`, `Article` or `NewsArticle` | posts | Headline, description, dates, author, image, first category (`articleSection`), tags (`keywords`) and word count. Pages get no article node: they are a `WebPage`. |
| `ImageObject` | posts and pages with a featured image | The image, referenced by the page and the article. |
| `BreadcrumbList` | everything but the home page and search | Home → category → post (the post's first category), or Home → page. |

404 pages get nothing. A static front page is treated as the home page.

Text values are cut to what search engines use (headlines at 110 characters,
descriptions at 300), whitespace is collapsed, tags are deduplicated
regardless of case and at most 10 are listed. The host allows 16 KiB per
JSON-LD object; if a page would go over (very long URLs or titles), the
plugin drops the least useful properties first (keywords, profiles, long
descriptions, the category, images) and logs a warning if even that is not
enough.

## Permissions

| Permission | Why |
| --- | --- |
| `posts:read` | To read the publication date, featured image, categories, tags and word count of the post being rendered. The host currently passes `head.meta` a shortened post without them (see [Host gaps](#host-gaps)), so the plugin finds the full post with one title search and matches it by id. |

Without `posts:read` the plugin still works: dates, images, breadcrumb
categories and keywords are left out, and it logs once "posts:read is not
granted". When a future host sends the complete post, the search is skipped.

## Settings

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `article_type` | select `BlogPosting` / `Article` / `NewsArticle` | `BlogPosting` | The type used for posts. |
| `publisher_type` | select `Organization` / `Person` | `Organization` | Whether the site belongs to an organization or to one person. |
| `publisher_name` | text | empty | Empty uses the site title. |
| `publisher_url` | text | empty | An `http(s)://` address. Empty, or anything else (logged), uses the site address. |
| `publisher_logo` | url | empty | An `https://` address of a square logo (`logo` of an Organization) or a photo (`image` of a Person), at least 112×112 pixels. |
| `same_as` | textarea | empty | Official profiles, one `https://` address per line. Duplicates are removed, other values are logged and ignored, at most 10 are used. |
| `breadcrumbs` | bool | `true` | Emit the `BreadcrumbList`. |
| `search_action` | bool | `true` | Emit the `SearchAction` on the home page. |
| `home_label` | text | empty | Name of the home page in breadcrumbs. Empty uses "Home", or "Inicio" on Spanish pages. |
| `home_label_es`, `home_label_en` | text | empty | Per-language overrides of `home_label`. |

Language-dependent values follow the page's language: `es`, `es-*` and
`es_*` are Spanish, anything else English. Settings warnings are logged once
per plugin instance.

## Privacy

The plugin only describes what the page already shows publicly: titles,
excerpts, the author's display name, dates, the featured image, categories
and tags. It stores nothing, makes no network requests and reads no visitor
data.

## Example

Real output (`go test`, pretty-printed; the host compacts it) for a post
with a featured image, a category and two tags, with `publisher_name`,
`publisher_logo` and two `same_as` profiles set. The host had sent the
shortened post, so the date, image and terms come from the lookup:

```json
{
  "@context": "https://schema.org",
  "@graph": [
    {
      "@type": "WebSite",
      "@id": "https://example.test/#website",
      "url": "https://example.test/",
      "name": "Example",
      "inLanguage": "en",
      "publisher": {
        "@id": "https://example.test/#publisher"
      }
    },
    {
      "@type": "Organization",
      "@id": "https://example.test/#publisher",
      "name": "Example Press",
      "url": "https://example.test/",
      "logo": {
        "@type": "ImageObject",
        "@id": "https://example.test/#logo",
        "url": "https://example.test/uploads/logo.png",
        "contentUrl": "https://example.test/uploads/logo.png"
      },
      "sameAs": [
        "https://mastodon.social/@example",
        "https://github.com/example"
      ]
    },
    {
      "@type": "WebPage",
      "@id": "https://example.test/blog/hello-world#webpage",
      "url": "https://example.test/blog/hello-world",
      "name": "Hello world",
      "description": "A first post.",
      "inLanguage": "en",
      "isPartOf": {
        "@id": "https://example.test/#website"
      },
      "primaryImageOfPage": {
        "@id": "https://example.test/blog/hello-world#primaryimage"
      },
      "datePublished": "2026-05-01T10:00:00Z",
      "breadcrumb": {
        "@id": "https://example.test/blog/hello-world#breadcrumb"
      }
    },
    {
      "@type": "ImageObject",
      "@id": "https://example.test/blog/hello-world#primaryimage",
      "url": "https://example.test/uploads/hello.jpg",
      "contentUrl": "https://example.test/uploads/hello.jpg"
    },
    {
      "@type": "BlogPosting",
      "@id": "https://example.test/blog/hello-world#article",
      "headline": "Hello world",
      "description": "A first post.",
      "url": "https://example.test/blog/hello-world",
      "mainEntityOfPage": {
        "@id": "https://example.test/blog/hello-world#webpage"
      },
      "isPartOf": {
        "@id": "https://example.test/blog/hello-world#webpage"
      },
      "inLanguage": "en",
      "datePublished": "2026-05-01T10:00:00Z",
      "publisher": {
        "@id": "https://example.test/#publisher"
      },
      "author": {
        "@type": "Person",
        "name": "Ada Lovelace"
      },
      "image": {
        "@id": "https://example.test/blog/hello-world#primaryimage"
      },
      "articleSection": "News",
      "keywords": [
        "go",
        "cms"
      ],
      "wordCount": 420
    },
    {
      "@type": "BreadcrumbList",
      "@id": "https://example.test/blog/hello-world#breadcrumb",
      "itemListElement": [
        {
          "@type": "ListItem",
          "position": 1,
          "name": "Home",
          "item": "https://example.test/"
        },
        {
          "@type": "ListItem",
          "position": 2,
          "name": "News",
          "item": "https://example.test/category/news"
        },
        {
          "@type": "ListItem",
          "position": 3,
          "name": "Hello world",
          "item": "https://example.test/blog/hello-world"
        }
      ]
    }
  ]
}
```

The Spanish home page (`/es/`) with the default settings:

```json
{
  "@context": "https://schema.org",
  "@graph": [
    {
      "@type": "WebSite",
      "@id": "https://example.test/es/#website",
      "url": "https://example.test/es/",
      "name": "Example",
      "inLanguage": "es",
      "publisher": {
        "@id": "https://example.test/#publisher"
      },
      "potentialAction": {
        "@type": "SearchAction",
        "target": {
          "@type": "EntryPoint",
          "urlTemplate": "https://example.test/es/search?q={search_term_string}"
        },
        "query-input": "required name=search_term_string"
      }
    },
    {
      "@type": "Organization",
      "@id": "https://example.test/#publisher",
      "name": "Example",
      "url": "https://example.test/"
    },
    {
      "@type": "CollectionPage",
      "@id": "https://example.test/es/#webpage",
      "url": "https://example.test/es/",
      "name": "Example",
      "description": "Notas sobre Go.",
      "inLanguage": "es",
      "isPartOf": {
        "@id": "https://example.test/es/#website"
      }
    }
  ]
}
```

## Host gaps

- **Shortened post in `head.meta`.** `internal/web/public.go` (`renderPage`)
  builds the post with only id, type, title, slug, lang, URL, excerpt and
  author, while docs/PLUGINS.md promises the full post object. The plugin
  works around it with one `posts_query` per post page (title search, matched
  by id, at most 20 results). If the post is not found (a title made only of
  symbols, or more than 20 newer posts containing all of its title words),
  the JSON-LD is still written without the missing fields.
- **The host writes its own JSON-LD.** The theme already gets a `WebSite`
  with a `SearchAction` on the home page and a `BlogPosting` on posts. With
  this plugin enabled those entities appear twice (with different `@id`s).
  Search engines tolerate it, but the host should leave its own JSON-LD out
  when a plugin provides some.
- **No featured image in practice.** The `posts_query` host function
  (`internal/plugin/hostfuncs.go`) loads terms but not featured images, so
  the lookup never finds an image and no `ImageObject` is written yet. The
  plugin needs no change once the host fills `image` (in `head.meta` or in
  `posts_query`).
- **No modification date.** The post object has no `updated_at`, so
  `dateModified` is not written.

## Build and install

```sh
make build                               # plugin.wasm (GOOS=wasip1 GOARCH=wasm)
make test vet                            # native unit tests and vet
make install PLUGINS_DIR=/path/to/data/plugins
```

Then open Admin → Plugins, use **Reload**, enable the plugin (approving pins
the hash of `plugin.wasm` and grants `posts:read`) and fill in the publisher
settings. Check the result with the Schema.org validator or Google's Rich
Results Test on a public page.
