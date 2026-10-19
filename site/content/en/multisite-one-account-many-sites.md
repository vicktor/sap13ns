---
type: post
key: multisite
title: "Multisite: one account, many sites"
slug: multisite-one-account-many-sites
excerpt: How sap13ns runs many sites from one installation, each with its own domain, themes, plugins and database schema, while you sign in only once.
date: 2026-10-17
categories: [Product]
tags: [multisite, hosting]
comments: closed
---
Most people who publish on the web end up with more than one site. A personal blog and the company's site. A site per client. A main site and a small one for an event that will be forgotten next year. Running a separate installation for each is tedious, and the usual alternative, one big installation where every site shares tables and plugins, makes one weak site everyone's problem.

Multisite in sap13ns tries to give you the convenience of the second without the risk.

## One account

You have one account, with one password and one set of two-factor credentials. With it you can belong to any number of sites, and your role is decided per site: you might be the administrator of your own blog, an editor on a client's site and a subscriber on a friend's. Sign in once, and you can move between the sites you belong to without signing in again.

What one account does not mean is one set of permissions. Being an administrator on one site gives you nothing on another unless you are made a member there.

## Many sites, really separate

Each site has:

- **its own domain**, or more than one, with requests routed by host name;
- **its own theme**, chosen from the built-in themes, the network's and any installed for that site alone;
- **its own plugins**, with their own approvals and their own permissions;
- **its own uploads directory**;
- **its own PostgreSQL schema**, holding its posts, pages, comments, menus, settings and plugin data.

The schema is the part we spent the most time on. The common way to build multisite is to add a `site_id` column to every table and remember to filter by it in every query. That works until the day one query forgets, and then one site's drafts appear on another's search page.

With one schema per site, a site's queries simply cannot see another site's rows, because each site's data lives in a schema of its own and its queries run against that schema only. There is no filter to forget. A plugin enabled on one site, even one with `posts:read`, reads only that site's posts.

## What is shared

A few things are deliberately shared, because sharing them is the point:

| Shared | Per site |
|---|---|
| Accounts, passwords, two-factor credentials | Roles and memberships |
| The sap13ns binary, its built-in themes and the network's themes | Content, settings, menus, widgets |
| The server, PostgreSQL and nginx | The site's own themes and plugins, uploads |

Upgrades are shared as well. You replace one binary, and its migrations run for every site's schema on start.

## The trade-offs

Separate schemas are not free. Every migration has to run once per site, so an installation with hundreds of sites takes longer to upgrade than one with a single shared set of tables. Cross-site reports, such as "the latest posts from all my sites", need a query per site instead of one query. Backing up one site means dumping one schema and one directory; restoring one site into a different installation needs care with accounts, because accounts live outside the site.

We think those costs are worth paying. They show up as a slower upgrade or a slightly more complex report, not as a data leak.

## Where it stands

Multisite is how hosting on sap13ns.site works: one account for all your sites, and each site with its own address, its own design and its own content. If you host with us, you get it from the first day.

Self-hosters can run it too. Set `SAP13NS_MULTISITE=true`, create the network and its portal with `sap13ns network init`, and manage the sites with `sap13ns site create`, `list`, `suspend`, `resume`, `delete`, `purge` and `domain`. A network comes with single sign-on through the portal, a network admin area, plans with storage and custom-domain limits, Stripe billing, and custom domains that are verified through DNS and served over TLS.[^1]

Some parts are not there yet:

- **Conversion.** An existing single-site install cannot be turned into a site of a network yet, so a network starts with a new database.
- **Theme uploads.** Sites cannot upload their own themes yet.
- **Invitations.** Only the network's administrators add members to a site; owners cannot invite people themselves yet.

*Update, 19 October 2026:* when we published this post, the commands for self-hosters were still to come, and we suggested running one service per site in the meantime. They are released now, with the limits listed above.

*Update, 19 October 2026, later:* per-site backups are here too, so they are off the list. A network backs up every site each night into a `.zip` of its own, within the storage of the site's plan. The site's administrators download and restore them under **Tools → Backups**, and WordPress imports them as they are. A restore matches the accounts by username, then by e-mail address, and creates the missing ones without a password; it never deletes one. That is the care with accounts mentioned above. The [guide to moving to WordPress](https://github.com/vicktor/sap13ns/blob/main/docs/MOVING-TO-WORDPRESS.md) has the details. Theme uploads are off the list as well: on the plans that allow it, a site uploads its own themes, which are checked automatically before they go live.

If you want to follow the work, watch the [repository on GitHub](https://github.com/vicktor/sap13ns). And if you run many sites today and have opinions about how this should work, we would like to hear them in the [issues](https://github.com/vicktor/sap13ns/issues).

[^1]: The commands are documented in [MULTISITE.md](https://github.com/vicktor/sap13ns/blob/main/docs/MULTISITE.md), and the [install page](/install) has a short version.
