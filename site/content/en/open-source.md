---
type: page
key: open-source
title: Open source
slug: open-source
excerpt: sap13ns is open source under the CPAL-1.0. Use it for anything, commercial work included, and keep the credit visible.
order: 8
menu: [footer, landing]
menu_label: Open source
comments: closed
---
The full source code of sap13ns is public at [github.com/vicktor/sap13ns](https://github.com/vicktor/sap13ns), under the **Common Public Attribution License 1.0 (CPAL-1.0)**, a licence approved by the Open Source Initiative.

We chose it because it lets almost everyone do almost anything, and asks for two things in return: credit, and sharing improvements with the people who use them.

## What you can do

- **Use it for anything.** Personal sites, client work, your company's website, a hosting business. Commercial use is allowed and costs nothing.
- **Change it.** Fork it, patch it, write your own themes and plugins.
- **Redistribute it**, modified or not, under the same licence.

## What we ask in return

### Keep the attribution

Every site running sap13ns shows a visible **"Powered by sap13ns"** credit that links to [https://sap13ns.site](https://sap13ns.site). Keep it in place and readable. You can style it to fit your theme, but do not hide or remove it.

This is the whole price of the software. It is how other people find the project, and it is what keeps it going.

### Share your changes when you offer them over a network

If you modify sap13ns and let other people use your modified version over a network (for example, by running it as a hosted service or for your clients' public sites), you must make the source code of your modifications available to them, under the same licence. Using an unmodified sap13ns, or modifying it only for yourself, creates no such obligation.

In our view, themes and plugins that only use the documented contracts are separate works, and you choose their licence. The plugin SDK, the plugin skeleton and the official plugins are under the MIT licence, so you can start from them for a plugin under any licence, including a closed one. The built-in themes state their own licence in their manifests.

### Credit and link back

If you write about a site built with sap13ns, or build a product on top of it, a mention and a link to the project are appreciated. The licence asks for the visible credit on each site; the rest is good manners.

## In short

| Question | Answer |
|---|---|
| Licence | CPAL-1.0 (plugin SDK, skeleton and official plugins: MIT) |
| Commercial use | Allowed, free of charge |
| Modifications | Allowed |
| Attribution | Visible "Powered by sap13ns" linking to sap13ns.site |
| Network use of modified versions | Publish the source of your changes |
| Copyright holder | BAUBER.XYZ SL |

This page is a summary in plain words, not legal advice. The licence text in the repository is what applies; if anything here seems to differ from it, the licence wins.

## Contributing

Contributions are welcome, from typo fixes to new features:

1. Open an [issue](https://github.com/vicktor/sap13ns/issues) to discuss anything larger than a small fix before you write it.
2. Keep pull requests focused, with tests for new behaviour.
3. Run `go test ./...` before you send it.
4. Report security problems privately to victor@bauber.xyz, not in a public issue.

By contributing you agree that your contribution is published under the project's licence.
