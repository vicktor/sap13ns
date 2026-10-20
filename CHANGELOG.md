# Changelog

What each release changes, newest first. The release workflow publishes a
release's section, and the ones before it, as its notes, and the admin's
System page shows the sections newer than the running version. Every release
needs a section here, headed `## vX.Y.Z — YYYY-MM-DD`.

## v0.2.2 — 2026-10-20

- Updates: while a newer release exists, every admin page warns the
  administrators that updating is important, and the System page lists what
  each newer release changes, from this changelog.

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
