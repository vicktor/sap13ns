---
type: page
key: security
title: Security
slug: security
excerpt: Strict defaults, few moving parts and plugins that cannot reach what you did not allow. Here is what protects a sap13ns site.
order: 4
menu: [primary, footer, landing]
menu_label: Security
comments: closed
---
Most attacks on content management systems are not clever. They go after a forgotten plugin, a weak password, an upload folder that runs code or a form that trusts its input. sap13ns is designed so that those doors are closed before you install anything.

## Fewer moving parts

**No Node in production.** Stylesheets and icons are built while we develop and embedded in the binary. The server runs one Go process, PostgreSQL and nginx. There is no package manager on the server, no interpreter and no dependency tree that changes under your feet.

**One binary, one version.** An update replaces the whole program at once. There is no state where half the files are new and half are old.

**A Unix socket behind nginx.** In production sap13ns listens on a Unix socket with mode `0660`, owned by a group that only nginx joins (`sap13ns-web`). nginx can connect to it, but it cannot read the configuration file or the data directory. No application port is exposed to the network.

## The browser does part of the work

**A strict Content Security Policy.** Every page is served with a policy that only allows scripts and stylesheets from the site itself: `script-src 'self'; style-src 'self'`. There are no inline scripts, no inline styles and no event attributes anywhere, in the admin or in the themes. If an attacker manages to inject markup, the browser refuses to run it.

**Security headers.** Pages also carry `X-Frame-Options: DENY` and `frame-ancestors 'none'` against clickjacking, `X-Content-Type-Options: nosniff`, a cross-origin opener policy, a referrer policy and a permissions policy that turns off the camera, microphone, geolocation and similar APIs. HTTPS sites also get HSTS.

## Accounts

**Argon2id.** Passwords are hashed with Argon2id, a memory-hard algorithm that makes guessing expensive. We ask for a minimum length and check against a list of common passwords instead of demanding symbols nobody can remember.

**A second factor for staff.** By default everyone with access to the admin panel must set up a second factor: TOTP codes from any authenticator app, with single-use recovery codes, or a passkey. Administrators can limit the requirement to administrators and editors, but cannot switch it off. Passkeys (WebAuthn) always require user verification, such as a fingerprint or a PIN, and also work for passwordless sign-in. Adding a passkey or removing the last second factor asks for a recent sign-in or the password.

**Rate limits.** Sign-in attempts are throttled per account and per address, and repeated failures lock the account for a while. Password resets, registrations and comments have their own limits.

**Sessions.** The session cookie is `HttpOnly`, `SameSite` and, on HTTPS, `Secure` with the `__Host-` prefix. Sessions expire after a period of inactivity and can be revoked from the profile page.

**Audit log.** Sign-ins and failed attempts, lockouts, role changes, settings, plugin approvals and content changes are recorded with the user, time and address.

## Forms and input

**CSRF protection, twice.** Every form that changes something carries a synchronizer token tied to the session, and Go's Cross-Origin Protection rejects state-changing requests that a browser marks as coming from another site.

**Sanitising with bluemonday.** Markdown is rendered with raw HTML disabled, and the result is passed through a bluemonday allow-list. HTML returned by plugins goes through a list of its own, which never lets scripts, styles, iframes, forms or event handlers through. Dangerous URLs such as `javascript:` are removed.

**Comment spam.** Comment forms carry a hidden honeypot field and a signed timestamp. Bots that fill in everything, or submit faster than a person can type, are dropped without being told.

## Uploads

Upload folders are a classic way in. sap13ns treats every file as hostile:

- the type is **detected from the content**, never from the file name or what the browser claims;
- only a list of known image, audio, video and document types is accepted, and the **extension must match** the detected type;
- JPEG and PNG images are **re-encoded**, which removes hidden metadata such as GPS coordinates;
- files are served with `Content-Security-Policy: sandbox` and `nosniff`, and anything that is not media is sent as a download, so an uploaded file can never run as a page of your site.

## Plugins are isolated

Plugins run as WebAssembly modules inside a sandbox with no access to files, the network, environment variables or the database. Each extra capability is a permission an administrator approves, pinned to the SHA-256 hashes of the module and its assets: if the files or the permissions they ask for change, the plugin stops until it is reviewed again. Uploaded packages are scanned before they are installed, and they are always installed disabled. Calls have memory and time limits, outbound requests can only reach public addresses on hosts you allowed, and returned HTML is sanitised. [More about plugins](/plugins).

## Multisite isolation

On a network of sites (`SAP13NS_MULTISITE=true`), each site lives in its own PostgreSQL schema with its own users, uploads and plugins. Every database statement runs against exactly one site's schema, and a statement that names no schema is refused, so a forgotten filter fails instead of reading another site's rows. A plugin enabled on one site cannot see another site's content, and an editor of one site has no rights on another unless they were made a member there. Network administrators who open a site do so as a separate operator account, and that is written to the audit logs. A site's passkeys belong to its own host, so they are useless on any other site. The details are in [SECURITY.md](https://github.com/vicktor/sap13ns/blob/main/docs/SECURITY.md#networks-of-sites) and [MULTISITE.md](https://github.com/vicktor/sap13ns/blob/main/docs/MULTISITE.md).

## Reporting a vulnerability

If you find a security problem, please write to **victor@bauber.xyz**, or use the **Report a vulnerability** button on the repository's Security tab on GitHub, before making it public. Tell us what you found and how to reproduce it; we will answer, keep you informed while we fix it and credit you if you want. The full policy is in [SECURITY.md](https://github.com/vicktor/sap13ns/blob/main/docs/SECURITY.md).
