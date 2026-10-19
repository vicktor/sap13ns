---
type: page
key: privacy
title: Privacy policy
slug: privacy
excerpt: What personal data sap13ns.site handles, why, for how long and how to exercise your rights. Short version — very little.
order: 21
menu: footer
menu_label: Privacy
comments: closed
---
We built sap13ns to collect as little as possible, and we run sap13ns.site the same way. This policy explains what we handle when you visit the website and when you use our hosting.

## Data controller

| Item | Details |
|---|---|
| Company | BAUBER.XYZ SL |
| CIF (tax ID) | B93944700 |
| Registry data | Tomo / Sección 8, Hoja Registral B-662478, Inscripción 1 |
| Address | Rambla Poblenou 38, 08005 Barcelona, Spain |
| Contact for privacy matters | victor@bauber.xyz |

## Visiting the website

### No tracking cookies

We do not use advertising, tracking or social media cookies, and we do not load third-party scripts, fonts or embeds. That is why there is no cookie banner.

The only cookies are technical and strictly necessary. Each one is set only when you do what it is for:

| Cookie | What it is for | How long it lasts |
|---|---|---|
| `__Host-sap13ns` (session) | Keeps you signed in, only if you sign in. | Until you sign out, and 14 days at most; it ends sooner after 48 hours without activity. |
| `sap13ns_flash` (notice) | Carries a one-line message, such as "Saved", from one page to the next after an action in the admin panel. | 2 minutes at most; deleted once the message is shown. |
| `__Host-sap13ns-sso` (single sign-on) | Protects a sign-in to a site with your sap13ns account while it goes through our portal. | 10 minutes at most; deleted when the sign-in completes. |
| `__Host-sap13ns-wa-login`, `__Host-sap13ns-wa-reg` (passkey) | Links the steps of a sign-in with a passkey, or of adding a passkey. | 5 minutes at most; deleted when the step completes. |
| `sap_pform` (form result) | After you send a form, such as a contact form, shows you the result and, after an error, fills in again what you typed. Its content is encrypted. | 5 minutes at most; deleted once the result is shown. |

The sites we host use the same cookies; each site's administrator can change how long its sessions last.

Your choice of light or dark mode is stored in your own browser (local storage) and never sent to us. The admin panel keeps a few things there too, such as the editor's display mode and a copy of a new post until it is first saved.

### Cookie-less analytics

To know which pages are read, we count visits in aggregate, per day: page views, unique visitors, the pages viewed, the referring site (or campaign tag), and the browser, operating system, type of device and language. This is done without cookies and without building a profile of you.

To count unique visitors, your IP address and browser are combined with a random value that is kept only in memory and replaced every day at midnight. The result stays in memory for that day only, so visits cannot be linked from one day to the next. No IP address, browser string or visitor identifier is ever stored. Visits by signed-in users are not counted. The daily totals we keep contain no personal data, and they are deleted after two years.

### Server logs

Like any web server, ours records technical information about requests (IP address, date and time, page requested, browser) to keep the service secure and to investigate abuse. These logs are kept for a short period and then deleted.

### Comments

If comments are enabled on a post and you write one, we store the name, e-mail address and optional website you enter, the text, and your IP address and browser for spam control. Your e-mail address is never published.

A site can also check comments with the antispam plugin. Its checks run on our servers, except one, which is off by default: if the site's administrator turns on **StopForumSpam**, your IP address and an MD5 hash of your e-mail address (not the address itself) are sent to StopForumSpam, a third-party database of addresses known for spam. Your name, website and comment are never sent, and the plugin stores nothing.

### Profile photos and Gravatar

Users can upload a profile photo; we remove its metadata (such as the place it was taken) and store it on the site. For users without a photo, and for commenters only if the site turns it on, the server asks Gravatar (run by Automattic) whether there is an image for a SHA-256 hash of the e-mail address. The address itself is never sent, and visitors' browsers never connect to Gravatar, because the image is served as a copy from this site. Administrators can turn this off in Settings, which deletes the copies.

### Site assistant

A site, this one included, can offer an assistant that answers visitors' questions from its own pages. It is off unless the site's administrator turns it on.

When it is on and you ask it something, your question and the previous turns of the conversation are sent to the AI provider the site has configured (Anthropic, a service compatible with the OpenAI API, Google Gemini, or a model the site runs itself) to write the answer. The question may also be sent to the provider the site uses to search its pages. Please do not include personal data in your questions.

The questions and answers are not stored on our servers: we only count how many questions are asked, and your IP address is used in memory to limit how many you can ask per hour. The conversation is kept in your browser tab (session storage) and is gone when you close it.

## Using our hosting

### Account and billing

For your account and billing data, BAUBER.XYZ SL is the data controller.

When you create an account we handle your username, name, e-mail address, password (stored only as an Argon2id hash) and, if you set them up, your second factor and passkeys. For commercial plans we also keep the details of your subscription: the plan, its billing state and the references Stripe gives to you and to the subscription. We use them to provide the service, to bill you and to contact you about your account. We send these e-mails through our e-mail provider.

**Payments are processed by Stripe** (Stripe Payments Europe, Ltd., Ireland). Your card details go directly to Stripe; we never see or store your card number. At checkout Stripe also collects your billing name and address and, if you give one, your tax ID, and keeps them with your invoices; we can see them in our Stripe account. Stripe acts under its own privacy policy, and may transfer data outside the European Economic Area with appropriate safeguards.

When you sign in to a site with your sap13ns account, that site receives your username, name and e-mail address, and its administrators can see them.

### Security log

To protect accounts, every site, our portal included, keeps an audit log of sign-ins, successful or not, and of administrative actions such as changes to settings, users, plugins or domains. Each entry records the time, the account (or the username typed), the action and the IP address it came from. While you are signed in, we also keep the IP address and browser of each of your sessions, so that you can see your sessions and end them.

### Sites you publish

For the content and the visitors of a site you host with us, you are the data controller, and BAUBER.XYZ SL is your processor: we process that data on your behalf, only to provide the hosting, using our hosting and e-mail providers as sub-processors. This includes your site's comments, its audit log and the messages sent through its forms. If you turn on a feature that uses an outside service, such as the AI writing assistant, the site assistant or the StopForumSpam check, the data it needs goes to the provider you choose. If your site collects personal data, you are responsible for informing your visitors.

Our team signs in to your site's administration only with your permission: you grant support access from your site, for a limited time. Each of these sign-ins is recorded in your site's audit log. The only exception is a legal order or a serious security incident on your site, such as malware or phishing: then our team may open your administration for at most four hours, with the reason recorded in your site's audit log, and we e-mail you at once with who did it, why and until when.

## Legal bases

- **Contract:** to provide the hosting you signed up for and to manage your account.
- **Legal obligation:** to keep invoices and accounting records.
- **Legitimate interest:** to keep the website and the service secure, prevent abuse and produce aggregate statistics without personal data.
- **Consent:** where we ask for it, for example to publish a comment. You can withdraw it at any time.

## How long we keep data

- Account data: while your account exists. To close your account, write to us. We then keep it, with its sites, for 90 days, as the [terms of service](/terms) describe, and then delete it, unless you ask for earlier deletion.
- Backups of each site: we make one every night and keep up to seven daily and four weekly copies, so a copy is gone after about five weeks. A copy made just before a restore is kept for at most seven days. When a site is deleted, its backups are deleted with it.
- Invoices and billing records: for the periods required by tax and commercial law.
- Audit log: one year. Session records: deleted once the session has ended.
- Analytics: daily totals without personal data, for two years.
- Server logs: a short period, then deleted.
- Comments: while the comment is published, or until you ask us to remove it.
- Questions to the site assistant: not stored.

## Who else receives data

We do not sell personal data and do not share it for advertising. We only share it with providers that help us run the service, such as Stripe for payments and our hosting and e-mail providers, under data processing agreements, and with authorities when the law requires it. On a site that has turned them on, the site assistant sends questions to the AI provider the site uses, and the StopForumSpam check sends the data described above to StopForumSpam; these providers may be outside the European Economic Area.

## Your rights

Under the General Data Protection Regulation (GDPR) you have the right to:

- **access** the personal data we hold about you;
- **rectify** inaccurate data;
- **erase** your data;
- **restrict** how we process it;
- **data portability**, receiving your data in a common machine-readable format;
- **object** to processing based on legitimate interest;
- **withdraw your consent** at any time, without affecting earlier processing.

Write to victor@bauber.xyz to exercise any of them. We will reply within one month. If you think we have not handled your data properly, you can also complain to the Spanish data protection authority, the **Agencia Española de Protección de Datos** ([aepd.es](https://www.aepd.es)).

## Changes

If we change this policy, we will publish the new version on this page and, if the change matters to customers, tell them by e-mail.

Last updated: 19 October 2026.
