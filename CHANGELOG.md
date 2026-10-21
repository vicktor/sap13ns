# Changelog

What each release changes, newest first. The release workflow publishes a
release's section, and the ones before it, as its notes, and the admin's
System page shows the sections newer than the running version. Every release
needs a section here, headed `## vX.Y.Z — YYYY-MM-DD`.

## v0.7.1 — 2026-10-21

- **A clearer Design with AI chat.** The conversation is a scrolling chat:
  the assistant's messages on the left, yours on the right, each in its own
  soft colour with the corner nearest the speaker square, and a bubble while
  the assistant works. The send button sits next to the input (Enter sends,
  Shift+Enter starts a new line), and a new conversation is an icon at the
  top right of the panel.

## v0.7.0 — 2026-10-20

- **Design with an AI agent (MCP).** Agents such as Claude Code can design a
  site through `/admin/mcp`, with the tools `get_design_guide`,
  `get_design_state` and `change_design`: the same guide, catalogue and
  validation as the in-app assistant, and the changes go to the draft,
  never published. See docs/DESIGN-AGENTS.md.
- **Access tokens for AI agents** in Profile, for users who can change the
  appearance: shown once, stored as a hash, with an expiry and the last
  use, revocable at once (migration 0020). The design MCP server accepts
  only these tokens, never the session cookie.

## v0.6.0 — 2026-10-20

- **Design with AI.** The design screen has a **Design with AI** chat
  (with the site's AI provider on): ask for a one-page site about your
  subject with animations, other colours, or a text change such as
  "change «Welcome» on the front page to «Aloha!»". The model gets a guide
  to sap13ns designs, the theme's blocks, sections, texts, colours, fonts
  and animations, and the current draft with the texts each language
  shows. Its changes pass the editors' own validation (what is refused goes
  back to it, up to three tries) and go to the draft, which you check in
  the preview and publish. It needs a recent model that is good at
  structured answers; the panel says which model answers and warns about
  local ones.

## v0.5.0 — 2026-10-20

- **One design screen.** Appearance → **Design** replaces Customise and
  Page builder: two tabs, **Style and texts** (colours, fonts, the theme's
  texts and images, custom CSS, animations) and **Structure** (the
  sections and blocks of each page), with one draft. **Publish** publishes
  everything at once and **Discard draft** drops everything; before, each
  screen published only its part and the preview could show changes that
  the other screen had not published.

## v0.4.1 — 2026-10-20

- **Analytics: bots are told apart from visitors.** Search engines, AI
  crawlers and agents (GPTBot, ClaudeBot, PerplexityBot…), link previews,
  SEO and monitoring tools, other bots and vulnerability scanners (requests
  for `/wp-login.php`, `/xmlrpc.php`, `/.env`, `/.git/`, `*.php` and the
  like) no longer count as visits. They get their own daily counts by kind
  and bot name, still without cookies or addresses (migration 0019), and
  the analytics page has a **Bots** card: hits by kind and the most active
  bots for the selected range.

## v0.4.0 — 2026-10-20

- **Theme sections.** A theme can split its pages into sections of its own
  (`sections` and `front_sections` in `theme.json`, templates in
  `templates/sections/`). The page builder offers them as blocks and starts
  from **The theme's own front page**, so a hand-written theme can be
  rearranged, lose or repeat sections, mix them with the builder's blocks
  and get different texts in each copy. See "Theme sections" in
  docs/THEMES.md.
- **Type in the preview.** In the customiser, a click on a theme's text in
  the preview lets you type over it right there; it is saved to the draft
  like its field. Enter ends a one-line text and Escape undoes. Images and
  lists still open their field. The **Click to edit** button now says when
  editing is off, since pressing it turned editing off without showing it.
- **Slower animations.** Entrance animations last 1.1 s (0.8 s subtle,
  1.6 s expressive), and children of a staggered group follow each other
  every 0.15 s.

## v0.3.0 — 2026-10-20

- **Animations.** sap13ns has its own library of 17 animations, served by
  the site: fades, zooms, focus, rise, pop, flips, reveals, plus a float and
  a pulse that keep moving. They play when an element comes into view, and
  never for visitors who ask their device for reduced motion.
  - Page builder: an **Animation** for every section (the whole section, or
    its blocks one after another) and for every block.
  - Customiser: an **Animation** for each of the theme's texts and images,
    and **Animations → How much things move** (normal, subtle, expressive,
    off) for the whole site, with a button that plays them again in the
    preview.
  - Themes: `data-sap-animate`, `data-sap-stagger` and `data-sap-delay`,
    with `"motion": true` in `theme.json`. A page only loads the library
    when something on it moves. See "Animations" in docs/THEMES.md.

## v0.2.17 — 2026-10-20

- Customiser: when the page builder has unpublished changes, a notice says
  that the preview shows them but visitors do not, and links to the builder
  to publish or discard them. The preview could look unlike the site while
  the status said it was up to date with the published design.

## v0.2.16 — 2026-10-20

- Network: only a super admin changes a super admin's account (password,
  e-mail, sessions, two-factor authentication, status). The upgrade from an
  older version makes only the network's first admin a super admin; run
  `sap13ns network init -username U` for any other.

## v0.2.15 — 2026-10-20

- Network: a super admin can give a site a paid plan (Pro, Business) for one
  year without Stripe, from **Network → Sites → the site → Plan**, for a
  customer who paid by other means. The site behaves as a paid one, is held
  when the year ends, and the plan can be extended or canceled (back to the
  free plan). See "A plan granted by hand" in docs/BILLING.md.
- Network: **super admins** are the network administrators made from the
  command line (`network init`, which can now also promote an existing
  account). Only a super admin revokes another, and the last one stays.

## v0.2.14 — 2026-10-20

- Customiser: **Texts and images**. A theme can declare the texts and
  photographs of its own pages in `theme.json` (`content`), and the
  customiser edits them per language with the live preview: click a text or
  an image in the preview to open its field, type and see it change, or pick
  an image from the media library (with uploads). Drafts, publishing,
  revisions and export work as for the rest of the design. See "Editable
  texts and images" in docs/THEMES.md.
- The media library dialog is shared by the editor and the customiser.

## v0.2.13 — 2026-10-20

- AI providers: cheaper models in the lists. OpenAI adds GPT-5 nano and
  GPT-4.1 nano for text and GPT Image 1 mini for images; Gemini adds
  Gemini 3.1 Flash-Lite and 2.5 Flash-Lite for text and Gemini 3.1
  Flash-Lite Image. Claude Haiku 5.5 already was Anthropic's cheapest. The
  defaults do not change.

## v0.2.12 — 2026-10-20

- Search ignores accents and case: "mediacion" finds "Mediación" and
  "conciliación" finds "conciliacion", on the public search page and in the
  admin's lists. The migration rebuilds the search index of every post once.

## v0.2.11 — 2026-10-20

- Update now: the System page now stays behind a dialog while the update
  runs. It checks every 5 seconds, waits through the restart and reloads
  once the new version answers. If the update fails, it shows why, and it
  also says so when the update service never takes the request.

## v0.2.10 — 2026-10-20

- Post editor: uploading from the media dialog showed nothing, neither the
  progress nor an error, because the dialog covers the notices. The dialog
  now shows the progress bar and the percentage, then "Processing…", and
  any error inside it. The new file appears in the dialog ready to pick.

## v0.2.9 — 2026-10-20

- Update now on a network: the update stopped with "the backup
  (sap13ns-backup.service) failed" when the server still had the
  single-site backup unit from before it became a network, because that
  backup refuses to run on a network. When `sap13ns-network-backup.service`
  is installed, the update now runs only that one.
- Media library: uploading shows its progress, the percentage sent and then
  "Processing…" while the server makes the image sizes, and the page
  reloads with the result. Before, nothing showed until the upload ended. A
  failed upload says so and lets you choose the file again.

## v0.2.8 — 2026-10-20

- Post editor: in the Language box, long titles of the linked translations
  ran out of the card. They are now cut short with an ellipsis, and the full
  title shows on hover.

## v0.2.7 — 2026-10-20

- Update now: the button showed only after the update service had run once,
  because systemd creates its status directory then. The admin now also
  counts the installed `sap13ns-update.path`, so it appears right after
  setting up the service.
- Sites of a network have no **System** page and no update notice: the
  server's details and its updates belong to the portal. Their
  administrators no longer see the memory, connections and database size of
  the shared server.

## v0.2.6 — 2026-10-20

- Theme uploads on a network: the exception for network admins now needs a
  session that came through the portal. A local sign-in of a user linked to
  a network admin's account follows the plan, as the site switcher already
  did.

## v0.2.5 — 2026-10-20

- Themes on a network: a network admin signed in to a site, as a member or as
  an operator, can upload a theme under **Appearance → Themes** whatever the
  site's plan. The page says the plan does not include it; site
  administrators on such a plan still do not see the form. The upload goes
  through the same checks and counts against the site's storage.

## v0.2.4 — 2026-10-20

- System page: the caches show how much of the public traffic they served
  since the start, and say why they look empty to an administrator: pages
  seen while signed in are always rendered afresh, and saving anything in the
  admin empties the cache.

## v0.2.3 — 2026-10-20

- Updates from the admin: **System → Updates → Update now** installs the
  latest signed release, after a backup, and restarts the site. The web
  server still cannot write its own binary: it leaves a request that a root
  service, `sap13ns-update.path`, acts on. Run `deploy/setup.sh` from this
  release and choose the upgrade once, to install that service; later
  releases then install from the admin.
- The System page says when an update is under way, and why the last one
  failed.

## v0.2.2 — 2026-10-20

- Updates: while a newer release exists, every admin page warns the
  administrators that updating is important, and the System page lists what
  each newer release changes, from this changelog.
- Site assistant: it also answers from the front page, as it renders (a
  theme's front page or a one-page site built in the page builder, which are
  not posts), and, on a network's portal, from the hosting plans with their
  current prices.
- Site assistant with OpenAI: when OpenAI answers now and then that the
  project has no access to a model it does have access to (HTTP 403), the
  question is asked again instead of failing.

## v0.2.1 — 2026-10-20

- Admin: choosing an option of a list with the mouse works again. A click
  closed the list before it landed, so only the keyboard could choose (the
  plan to upgrade a site, the settings, the filters…).
- My sites: the table of sites takes the whole width, so every action shows
  without scrolling sideways; upgrading and requesting a site come under it.
- Networks: the light or dark mode you choose is kept on every site of the
  network and on the portal, not only on the site where you chose it.
- Privacy policy of sap13ns.site: the language and theme cookies, and the
  visitor's country in the analytics.

## v0.2.0 — 2026-10-20

The first signed release: `sap13ns update` installs the next ones.

- Analytics: visitors by country on a world map, from a country database on
  the server (`sap13ns geoip update`); only the country is kept.
- Languages: the public site and the sign-in page follow the visitor's
  browser until they choose a language.
- A guided installer (`deploy/setup.sh`) for a single site or a network:
  PostgreSQL, pgvector, nginx, HTTPS, backups and the first administrator.
- Cookie consent built from what the site really uses, with the plugins
  declaring their cookies, storage and other sites.
- Page builder: an HTML block, CSS classes on sections and custom CSS.
- WordPress block themes are converted into sap13ns themes on upload.
- Plugins can connect to OAuth providers; official plugins post new articles
  to X, LinkedIn and Instagram.
- Billing on a network: monthly and yearly prices, VAT shown, a canceled
  subscription keeps the time it paid for, one e-mail per failed renewal.
