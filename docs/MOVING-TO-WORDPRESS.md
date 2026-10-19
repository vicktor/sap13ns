# Moving a site to WordPress

Every backup of a sap13ns site is also a WordPress import. Its `export.xml` is a WordPress export file (WXR 1.2), and its `wp-content/uploads/` folder is laid out as WordPress keeps its files. No plugin or conversion is needed: WordPress's own importer reads it.

This is tested. An automated test (`SAP13NS_WP_TEST=1`, in `internal/sitebackup`) imports a backup into a real WordPress in Docker, in both of the ways below, and checks the result with WP-CLI. It was last run with the current WordPress and WordPress Importer 0.9.6.

## Contents

- [Choose how](#choose-how)
- [1. Make the backup](#1-make-the-backup)
- [2a. Import while the old site is online](#2a-import-while-the-old-site-is-online)
- [2b. Import at the new address](#2b-import-at-the-new-address)
- [With WP-CLI](#with-wp-cli)
- [3. After the import](#3-after-the-import)
- [What moves, and what does not](#what-moves-and-what-does-not)
- [Coming back](#coming-back)

## Choose how

There are two ways. What differs is where WordPress gets the files from.

- **a. The old site stays online while you import.** This is a normal backup. WordPress downloads every file from the old site, so it must still answer at its address until the import has finished. This is the easy way when WordPress gets a new address.
- **b. The backup is made for the new address.** The old site may be gone, or WordPress may take over its address. You give the address of the WordPress site when you make the backup. Its links and images then point there, and you copy the files to WordPress yourself before importing.

## 1. Make the backup

In the site's admin, open **Tools → Backups**:
- **Way a:** download the latest backup from **Saved backups**, or make one with **Back up now** and download it.
- **Way b:** enter the address of the WordPress site (for example `https://new.example`) in **Address of the WordPress site**, then **Back up now**, and download it.

Downloads are decrypted on the way, even when the server keeps the backups encrypted. The link works for you only, for an hour.

From the command line, as the server's administrator:

```sh
sap13ns backup create [-site SLUG] [-wp-base https://new.example]
# encrypted archives (.zip.enc):
sap13ns backup decrypt ARCHIVE.zip.enc
```

Unzip the archive. It holds `export.xml`, `wp-content/uploads/`, a `sap13ns/` folder that WordPress ignores, and `README.txt` with these steps.

## 2a. Import while the old site is online

1. Install WordPress and sign in as an administrator.
2. **Tools → Import → WordPress**: install the importer if WordPress asks, then **Run Importer**.
3. Choose `export.xml`. Assign the authors to existing users, or let WordPress create them. Tick **Download and import file attachments**, and submit. WordPress downloads every file from the old site.
4. Go on with [After the import](#3-after-the-import).

Links between posts still point at the old site. That is what WordPress's own exports do, and the [permalinks](#3-after-the-import) can match the old addresses.

## 2b. Import at the new address

1. Install WordPress at the address you gave, and sign in as an administrator.
2. Copy the folder `wp-content/uploads` of the archive to the WordPress site as **`wp-content/uploads/sap13ns-import`**, with SFTP or your host's file manager. The year folders go inside `sap13ns-import` (for example `wp-content/uploads/sap13ns-import/2026/03/photo.png`).
3. **Tools → Import → WordPress**: install the importer if WordPress asks, then **Run Importer**.
4. Choose `export.xml`, assign or create the authors, tick **Download and import file attachments**, and submit. WordPress copies each file from `sap13ns-import` into its own uploads, keeping its name, and points the posts at its copy.
5. Delete the folder `wp-content/uploads/sap13ns-import`.
6. Go on with [After the import](#3-after-the-import).

Why not copy the files straight into `wp-content/uploads`? The importer always copies the files it imports. A copy in the same folder as the original gets a new name (`photo-1.png`), and the importer then rewrites the image addresses in the posts twice, ending at `photo-1-1.png`, which does not exist. From another folder, the copy keeps its name and the posts point at it.

In this way, links between posts, images and the site's own links in menus (such as Home) point at the new address.

## With WP-CLI

```sh
wp plugin install wordpress-importer --activate
wp import export.xml --authors=create --user=ADMIN
```

`ADMIN` is the username of a WordPress administrator. An import from the admin pages runs as one too. Without `--user`, WordPress drops the address (slug) of pending posts and filters the HTML of the content. For way b, copy the files to `wp-content/uploads/sap13ns-import` first, as above.

## 3. After the import

- **Menus.** Each menu of the site arrives as a WordPress menu, one per location and language: *Primary*, *Primary (en)*, *Footer*… Links to posts, pages, categories and tags point at the imported ones. Under **Appearance → Menus** (or the site editor), assign each one to a location of your theme.
- **Users.** WordPress creates the authors without a password and with its default role, as its importer says when it finishes: set their roles under **Users**, and they set a password with **Lost your password?**. Users who wrote nothing are not in the export file. Their list is in `sap13ns/users.json`.
- **Addresses.** On sap13ns, posts live at `/blog/<slug>`, pages at `/<slug>`, and the other languages under their code (`/en/blog/<slug>`). To keep the addresses of posts, choose **Settings → Permalinks → Custom structure** `/blog/%postname%/`. Pages are already at `/<slug>/`.
- **Redirects.** WordPress does not import them. They are in `sap13ns/redirects.json`: create them again with a redirect plugin, or in the web server.
- **Then** point the domain at WordPress, if it is not there yet.

## What moves, and what does not

| Into WordPress | |
|---|---|
| Posts and pages | Published, scheduled, draft, pending, private and trashed ones, with their addresses (slugs), dates, excerpts and authors. Pages keep their parent and order. |
| Categories and tags | With the hierarchy of categories, and the terms of each post |
| Comments | With their threads. Held comments stay held. Spam is not exported. |
| Authors | Created by the importer, or assigned to existing users |
| Media | Every file of the media library, as an attachment with its file, alternative text and caption. Images in the text point at the imported files. |
| Featured images | Each post's, with its file |
| Menus | As WordPress menus, to assign to locations |
| Meta descriptions | Under the key of Yoast SEO (`_yoast_wpseo_metadesc`), which uses them once installed |
| Languages | Each translation is a post of its own. The custom fields `_sap13ns_lang` and `_sap13ns_translation_group` record its language and which posts translate each other, for a multilingual plugin. |
| Markdown | The content arrives as HTML (a Classic block in the block editor). The Markdown source stays in the custom field `_sap13ns_markdown`. |

| Not into WordPress | |
|---|---|
| Passwords, two-factor settings, passkeys, sessions | Never in a backup: users set a new password |
| Settings, the theme and its options, customiser designs, page-builder layouts, widgets | WordPress has its own. They are in `sap13ns/` for reference. |
| Redirects | In `sap13ns/redirects.json` |
| Plugins | sap13ns plugins are WebAssembly modules, which WordPress does not run. Their list is in `sap13ns/plugins.json`. |
| Analytics, the audit log, revisions | Not in a backup |
| The site assistant's index, AI settings | Not used by WordPress |

## Coming back

The same archive restores the site in sap13ns, the `sap13ns/` folder included: **Tools → Backups → Restore** for the backups the server keeps, or `sap13ns backup restore [-site SLUG] -replace ARCHIVE.zip` for a file. A backup made for WordPress restores like any other.
