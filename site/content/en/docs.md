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

Running sap13ns in production: the configuration variables, PostgreSQL, the systemd unit, nginx with a Unix socket, HTTPS, backups and updates. The [install page](/install) is a short version of it.

[Read DEPLOY.md](https://github.com/vicktor/sap13ns/blob/main/docs/DEPLOY.md)

### Themes — THEMES.md

How a theme is put together: the `theme.json` manifest, menus, widget areas and options, the templates and the data each one receives, the shared partials, translations of theme strings and how to build the stylesheet with Tailwind CSS.

[Read THEMES.md](https://github.com/vicktor/sap13ns/blob/main/docs/THEMES.md)

### Plugins — PLUGINS.md

The plugin contract (ABI 1): the manifest, hooks and events, permissions, limits, the host functions, the Go SDK and how approval and hash pinning work.

[Read PLUGINS.md](https://github.com/vicktor/sap13ns/blob/main/docs/PLUGINS.md)

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
| understand the risks | SECURITY.md and the [security overview](/security) |
| skip the server entirely | [Hosting on sap13ns.site](/hosting) |

## Command line at a glance

```text
sap13ns serve                  run the web server (default)
sap13ns migrate                apply database migrations and exit
sap13ns keygen                 print a new SAP13NS_SECRET_KEY
sap13ns user create            create a user (-username, -email, -role, -name)
sap13ns user reset-password    set a new password for a user
sap13ns plugin hash <dir>      print the SHA-256 of a plugin's module
sap13ns backup create          write a backup archive
sap13ns version                print the version
```

Every command accepts `-env <file>` to load its configuration from a file.

## Help and feedback

- **Source code:** [github.com/vicktor/sap13ns](https://github.com/vicktor/sap13ns)
- **Bugs and ideas:** [open an issue](https://github.com/vicktor/sap13ns/issues). Include the version (`sap13ns version`), what you did and what you expected.
- **Security problems:** please do not open a public issue; write to victor@bauber.xyz instead.
