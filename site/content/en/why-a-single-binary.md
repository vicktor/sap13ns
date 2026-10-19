---
type: post
key: single-binary
title: Why a single binary
slug: why-a-single-binary
excerpt: Templates, styles, icons, translations and migrations all live inside one executable. Here is what that buys us, and what it costs.
date: 2026-10-15
categories: [Engineering]
tags: [go, deployment, nginx]
comments: closed
---
The first decision we made about sap13ns, before any feature, was how it would be deployed. We wanted the answer to "how do I install it?" to fit in one sentence: copy a file and start it.

That decision shaped a surprising amount of the code, so it is worth explaining.

## What goes inside

Go can embed files into the program at build time. We use that for everything the server needs at run time:

| Inside the binary | Outside the binary |
|---|---|
| Admin templates and theme templates | PostgreSQL data |
| Compiled stylesheets and the small scripts | Uploaded files |
| Icons and translations | The configuration file |
| Database migrations | Extra themes and plugins you add |

The stylesheets are built with Tailwind CSS while we develop, and the result is committed. That is why there is no Node.js on the server: the only time Node runs is on a developer's machine, before the binary exists.

## What it buys

**Installation is copying a file.** No interpreter version to match, no extensions to enable, no dependency resolution on the server. The release you tested is, byte for byte, the release you run.

**Updates are atomic.** Replacing a binary and restarting a service either works or it does not. There is no half-updated state where new templates meet old code, which is a classic source of mysterious breakage on systems that update file by file.

```bash
sudo install -m 0755 sap13ns /usr/local/bin/
sudo systemctl restart sap13ns
```

**Schema changes travel with the code.** Migrations are embedded too, and the server applies pending ones when it starts, inside a transaction and behind a PostgreSQL advisory lock so that two processes starting at once do not step on each other. You never deploy code that expects a column the database does not have yet.

**Less to attack.** A server with no package manager, no build tools and no interpreter gives an intruder much less to work with. Uploads can never become code: they are checked by content, stored as data and served in a sandbox. The only code loaded at run time is plugins, and those run inside WebAssembly with nothing but the permissions you approved.

**Rollback is copying the old file back.** As long as the newer version's migrations only added things, the previous binary keeps working against the same database.

## Behind nginx, on a socket

We do not try to make sap13ns a complete web server. nginx is very good at TLS, compression, static files and slow clients, so we let it do those jobs and keep our process focused on the application.

In production sap13ns listens on a Unix socket instead of a TCP port. The socket is created with mode `0660` and owned by the service's group, so nginx can talk to it and nobody else on the machine can. There is no application port to firewall, and no way to bypass nginx by hitting the app directly.

Uploaded files are served by nginx straight from disk, with a `sandbox` Content Security Policy, so the Go process never spends time streaming images.

*Update, 19 October 2026:* two details of this setup have changed. The socket now belongs to a dedicated `sap13ns-web` group that only nginx joins, so nginx can reach sap13ns but cannot read its configuration or its data directory. For the same reason, the nginx configuration we ship lets sap13ns serve uploads itself through the socket, with the same sandboxing policy; `SAP13NS_SERVE_UPLOADS=false` still leaves them to nginx, if you give it read access to the uploads directory. Updates also got easier: `sap13ns update` installs a signed release and keeps the previous binary for a rollback.

## What it costs

It would be dishonest to pretend there are no trade-offs.

**You cannot edit a built-in template on the server.** If you want to change how the admin looks, you rebuild. For themes we kept an escape hatch: the server also loads themes from a directory on disk, so a site can have its own theme without a custom build. This website uses exactly that.

**The binary is larger than a script.** It carries everything with it. On a modern server the difference is irrelevant, but it is not a few kilobytes.

**Plugins cannot be Go packages.** Compiling plugins into the binary would mean a custom build per site, so plugins are WebAssembly modules loaded at run time instead. That turned out to be a good thing for security, but it was a consequence of this decision, not the reason for it.

**Builds have to be reproducible.** If everything lives in one artefact, the build process matters a lot. We pin our build tools and commit generated assets, so that a release can be rebuilt from the same commit with the same result.[^1]

## Would we do it again

Yes. Every time we have deployed sap13ns, the boring part has been boring, which is exactly what you want from deployment. The time we did not spend on packaging went into the parts people actually see: the editor, the themes and the security model.

If you want to try it, the [install guide](/install) takes you from download to a running site, including the systemd unit and the nginx configuration.

[^1]: The frontend tooling (Tailwind CSS, the component styles and the icon set) is pinned to exact versions in the repository.
