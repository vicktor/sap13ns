---
type: post
key: introducing
title: Introducing sap13ns
slug: introducing-sap13ns
excerpt: A content management system in the spirit of WordPress, written in Go, shipped as one binary and built on the assumption that the internet is hostile.
date: 2026-10-18
categories: [Releases]
tags: [launch, go, open source]
comments: closed
---
Today we are publishing sap13ns: a content management system for blogs, magazines, documentation and company sites. It is written in Go, keeps its data in PostgreSQL, runs behind nginx and ships as a single executable file. The source is on [GitHub](https://github.com/vicktor/sap13ns), and this website runs on it.

## Why another CMS

WordPress got many things right. Posts and pages, categories and tags, menus and widgets, themes and plugins: that model is easy to explain to an editor and it has survived twenty years of changing fashions. We kept it almost entirely.

What we wanted to change is everything around it. A typical site today is an application server, a language runtime, a pile of plugins with full access to the database, a caching layer bolted on later and a list of updates that never quite reaches zero. Each piece is reasonable on its own. Together they make a site that is slow to load, tedious to run and easy to break into.

So we started from a few rules and tried not to bend them.

## The rules

**One file to deploy.** Templates, styles, icons, translations and migrations are compiled into the binary. Installing is copying one file. Updating is replacing it and restarting. There is no Node.js and no package manager on the server.

**Fast without tuning.** Public pages are cached in memory and served without touching the database until something changes. Search uses PostgreSQL's own full-text engine. Images are resized into responsive variants when they are uploaded, so phones do not download photos meant for a 4K screen.

**Secure by default, not by checklist.** Every page is served with a strict Content Security Policy: no inline scripts, no inline styles, anywhere. Passwords are hashed with Argon2id. Forms carry CSRF tokens. Two-factor authentication with recovery codes is built in. Uploads are identified by their content and served in a sandbox.

**Plugins that cannot hurt you.** This is the rule we are proudest of. Plugins are WebAssembly modules that start with no access to files, the network or the database. Every extra capability is a permission you approve, and the approval is pinned to the exact file you reviewed. We wrote more about it in [Plugins in a WebAssembly sandbox](/blog/plugins-in-a-webassembly-sandbox).

## What is in the box

A Markdown editor with live preview. Five themes (Apple, Neumorphism, Glassmorphism, Bento Grid and Dashboard / SaaS), each with light and dark modes. Content in several languages, linked as translations, with `hreflang` for search engines. RSS, Atom and JSON Feed for the whole site and for every category, tag and author. Comments with moderation and quiet spam filtering. Five roles, from administrator to subscriber, and an audit log of who did what.

There is also an importer for WordPress export files, automatic social preview images, redirects, cookie-less statistics and backups of the database and uploads in one archive.

And there is an AI assistant, which we want to be precise about. It can draft an excerpt, suggest an SEO title, translate a post or propose an image prompt, using Claude, any OpenAI-compatible API, a local model or Gemini. It only runs when an editor asks for it, and it never publishes anything on its own. A person always reads the result first.[^1]

## What is not finished

Multisite, one account managing many sites each with its own domain and its own database schema, is how our hosting works, and you can run it on your own server too. A few parts of it are missing. `sap13ns backup` cannot yet back up or restore one site of a network, so a network is backed up as a whole with `pg_dump` and a copy of the data directory. An existing single-site install cannot be turned into a site of a network, and sites cannot upload their own themes. The documentation will keep growing as people ask questions we did not think of.

*Update, 19 October 2026:* when we first published this post, passkeys were still planned and the commands to run multisite on your own server were coming soon. Both are available now. Passkeys work for passwordless sign-in and as the second factor, always with user verification. `SAP13NS_MULTISITE=true`, `sap13ns network init` and the `sap13ns site` commands run a network with single sign-on, a network admin area, Stripe billing, plan limits, and custom domains verified through DNS and served over TLS. [MULTISITE.md](https://github.com/vicktor/sap13ns/blob/main/docs/MULTISITE.md) explains it.

We would rather tell you this now than have you find out in the middle of a migration.

## How to try it

If you have a machine with PostgreSQL, the [quick start](/install) takes a few minutes: download the binary, create a database, write a four-line configuration file and start the server. The first start prints a one-time link to create your administrator account.

If you would rather not run a server at all, we offer [hosting](/hosting). Personal, non-commercial sites are free once we have checked them, and commercial sites pay a monthly plan.

## The licence

sap13ns is open source under the CPAL-1.0. You can use it for anything, including commercial work, at no cost. In return, sites keep a visible "Powered by sap13ns" link, and if you offer a modified version to others over a network, you publish your changes. The [open-source page](/open-source) explains it in plain words.

## What comes next

We are going to spend the next weeks listening: reading issues, fixing what people trip over and writing the guides that turn out to be missing. If you build something with sap13ns, or if something breaks, [tell us on GitHub](https://github.com/vicktor/sap13ns/issues). That is the most useful thing you can do for the project right now.

[^1]: The assistant is optional. It does nothing until an administrator configures a provider, either an API key or the address of a local model.
