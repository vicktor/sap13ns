# Designing a site with an AI agent

sap13ns has two ways to change a theme's design in plain words: the
**Design with AI** chat on the design screen (Appearance → Design), which
uses the site's own AI provider, and a **design MCP server** for agents
that run elsewhere, such as Claude Code. Both work the same way: the model
reads a guide to how sap13ns designs are made and the theme's catalogue,
reads the draft, and sends changes that the site validates exactly as it
validates the editors' changes. Changes go to the **draft**; you check the
preview and publish.

Both need a recent model that is good at following a schema and writing
long JSON: a current Claude, GPT or Gemini model. Small local models often
produce changes the site refuses (nothing is saved then).

## Connecting an agent (MCP)

1. In **Profile → Access tokens for AI agents**, create a token. It is
   shown once. Only users who can change the appearance have this section.
2. Add the server to the agent. For Claude Code:

   ```sh
   claude mcp add --transport http sap13ns-design https://example.com/admin/mcp \
     --header "Authorization: Bearer sap13ns_…"
   ```

   Any MCP client that speaks Streamable HTTP works the same way: the
   address is `/admin/mcp` on the site, and the token goes in the
   `Authorization: Bearer` header. On a network, each site has its own
   address and its own tokens.
3. Ask: "make the front page a one-page site about our sailing school,
   with animations", "use a dark green palette", "change «Welcome» on the
   front page to «Aloha!»".

Revoke a token in the same place; it stops working at once.

## Tools

| Tool | What it does |
|---|---|
| `get_design_guide` | The guide (designs, layouts, rules) and the theme's catalogue: the page builder's blocks and their fields, the theme's sections and texts, colour tokens, fonts, scales and animations. |
| `get_design_state` | The draft design (`DESIGN`), the layouts (`LAYOUT`), what each of the theme's texts shows in every language (`CURRENT TEXTS`), and the preview's address. |
| `change_design` | `design_patch` and `layout_patch` (JSON merge patches, RFC 7386, over `DESIGN` and `LAYOUT`) and `block_edits` (settings merged into a block, per language). Nothing is saved unless all of it passes; otherwise the problems come back to fix. |

Every tool takes an optional `theme` (a slug); the active theme by default.
None of them publishes.

## What makes a theme easy to design with AI

The same as for the editors: a theme that declares its texts and images
(`content` in `theme.json`) and splits its pages into sections (`sections`
and `front_sections`) gives the model, and you, something to change without
touching the theme. See "Editable texts and images" and "Theme sections" in
[THEMES.md](THEMES.md).

## Security

See "Access tokens for AI agents" and "Design with AI" in
[SECURITY.md](SECURITY.md).
