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

What one account does not mean is one set of permissions. Being an administrator on one site gives you nothing on another unless someone there invites you.

## Many sites, really separate

Each site has:

- **its own domain**, or more than one, with requests routed by host name;
- **its own themes**, including any custom theme uploaded for it;
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
| The sap13ns binary and its built-in themes | Content, settings, menus, widgets |
| The server, PostgreSQL and nginx | Uploaded themes and plugins, uploads |

Upgrades are shared as well. You replace one binary, and its migrations run for every site's schema on start.

## The trade-offs

Separate schemas are not free. Every migration has to run once per site, so an installation with hundreds of sites takes longer to upgrade than one with a single shared set of tables. Cross-site reports, such as "the latest posts from all my sites", need a query per site instead of one query. Backups of one site are easy; restoring one site into a different installation needs care with accounts, because accounts live outside the site.

We think those costs are worth paying. They show up as a slower upgrade or a slightly more complex report, not as a data leak.

## Where it stands

Multisite is how hosting on sap13ns.site works: one account for all your sites, and each site with its own address, its own design and its own content. If you host with us, you get it from the first day.

For self-hosters, the commands to create and manage several sites from one installation are coming soon. We would rather release them when backup, restore and upgrades are well tested per site than ship them early and learn about the problems from your support tickets.[^1]

Until then, the reliable way to run several sites on your own server is several services, one per site, each with its own database. It is less convenient, but every site is already completely isolated from the others.

If you want to follow the work, watch the [repository on GitHub](https://github.com/vicktor/sap13ns). And if you run many sites today and have opinions about how this should work, we would like to hear them in the [issues](https://github.com/vicktor/sap13ns/issues).

[^1]: When the commands are released, they will be documented in DEPLOY.md and on the [install page](/install).
