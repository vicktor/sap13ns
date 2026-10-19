---
type: page
key: docs
title: Documentation
slug: docs
excerpt: The reference documentation lives next to the code on GitHub. Start here to find the right guide.
order: 6
menu: [primary, footer]
menu_label: Docs
comments: closed
---
The documentation is kept in the repository, next to the code it describes, so it changes in the same commit as the behaviour. These are the guides, and what each one is for.

## Guides

### Deploying — DEPLOY.md

Running sap13ns in production: the installer, the configuration, PostgreSQL, the systemd units, nginx with a Unix socket, HTTPS, backups and updates, and the proxy setup for a network of sites. The [install page](/install) is a short version of it.

[Read DEPLOY.md](https://github.com/vicktor/sap13ns/blob/main/docs/DEPLOY.md)

### Themes — THEMES.md

How a theme is put together: the `theme.json` manifest, menus, widget areas and options, the templates and the data each one receives, the shared partials, translations of theme strings and how to build the stylesheet with Tailwind CSS. It also covers what operators can change without touching a theme: the customiser and the page builder.

[Read THEMES.md](https://github.com/vicktor/sap13ns/blob/main/docs/THEMES.md)

### Plugins — PLUGINS.md

The plugin contract (ABI 1): the package format, the manifest, hooks and events, permissions, limits, the host functions, the scanner, the Go SDK, how approval and hash pinning work, and the review of uploads on a network of sites.

[Read PLUGINS.md](https://github.com/vicktor/sap13ns/blob/main/docs/PLUGINS.md)

### Networks of sites — MULTISITE.md

Running many sites from one installation: the portal, single sign-on, the network admin area, plans and limits, custom domains, TLS, the command-line reference, backups and what is not available yet.

[Read MULTISITE.md](https://github.com/vicktor/sap13ns/blob/main/docs/MULTISITE.md)

### Moving to WordPress — MOVING-TO-WORDPRESS.md

How to take a site to WordPress with one of its backups: the two ways of doing it, the steps in the admin and with WP-CLI, what to do after the import, and what moves and what does not.

[Read MOVING-TO-WORDPRESS.md](https://github.com/vicktor/sap13ns/blob/main/docs/MOVING-TO-WORDPRESS.md)

### Billing — BILLING.md

Setting up Stripe for a network of sites: products and prices, the customer portal, the webhook, the environment variables and what happens to a site whose payment lapses.

[Read BILLING.md](https://github.com/vicktor/sap13ns/blob/main/docs/BILLING.md)

### Releases and updates — RELEASES.md

How releases are built and signed, how a site learns about a new one, and how `sap13ns update` installs it and rolls it back.

[Read RELEASES.md](https://github.com/vicktor/sap13ns/blob/main/docs/RELEASES.md)

### Security — SECURITY.md

The threat model, the defences in each layer and how to report a vulnerability.

[Read SECURITY.md](https://github.com/vicktor/sap13ns/blob/main/docs/SECURITY.md)

## Where to start

| If you want to… | Read |
|---|---|
| try it on your machine | [Install, quick start](/install) |
| put a site online | DEPLOY.md |
| change how a site looks | THEMES.md |
| add a feature | PLUGINS.md |
| run many sites from one installation | MULTISITE.md |
| move a site to WordPress | MOVING-TO-WORDPRESS.md |
| understand the risks | SECURITY.md and the [security overview](/security) |
| skip the server entirely | [Hosting on sap13ns.site](/hosting) |

## Command line at a glance

```text
sap13ns serve                         run the web server (default)
sap13ns migrate                       apply database migrations and exit
sap13ns keygen                        print a new SAP13NS_SECRET_KEY
sap13ns user create                   create a user (-username, -email, -role, -name)
sap13ns user reset-password           set a new password for a user
sap13ns plugin new|pack|check         start, package and scan a plugin
sap13ns plugin list|install           list or install the site's plugins
sap13ns plugin hash <dir>             print the SHA-256 of a plugin's module and assets
sap13ns backup create|list|verify|restore|prune|decrypt
                                      encrypted backups of the database and uploads
sap13ns backup create|list|restore|prune -format site
                                      the site's own backups, which WordPress imports
sap13ns assistant reindex|status      the site assistant's search index
sap13ns update                        install the latest signed release
sap13ns version                       print the version

Networks of sites (SAP13NS_MULTISITE=true):
sap13ns network init                  create the network, its portal and an administrator
sap13ns site create|list|suspend|resume|delete|purge
sap13ns site domain add|verify|primary|remove
```

Every command accepts `-env <file>` to load its configuration from a file. On a network, the commands that work on one site take `-site <slug>`.

## Help and feedback

- **Source code:** [github.com/vicktor/sap13ns](https://github.com/vicktor/sap13ns)
- **Bugs and ideas:** [open an issue](https://github.com/vicktor/sap13ns/issues). Include the version (`sap13ns version`), what you did and what you expected.
- **Security problems:** please do not open a public issue; write to victor@bauber.xyz instead.
