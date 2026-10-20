# Changelog

What each release changes, newest first. The release workflow publishes a
release's section, and the ones before it, as its notes, and the admin's
System page shows the sections newer than the running version. Every release
needs a section here, headed `## vX.Y.Z — YYYY-MM-DD`.

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
