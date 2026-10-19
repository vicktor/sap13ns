// Tests for the visual editor's pure core: Markdown <-> DOM round trip and the
// paste sanitizer. Run with `npm test` (node --test ui/admin/test/*.test.mjs). No DOM library needed:
// a tiny HTML parser (dom.mjs) builds the minimal node tree the converter reads.
import { test } from "node:test";
import assert from "node:assert/strict";
import { parseMarkdown, renderMarkdown, toHTML, readNodes, htmlToBlocks, joinTop, blockSig, looksLikeMarkdown,
  toggleMark, setLink, setBlockType, toggleCodeBlock, toggleList, toggleQuote, indentList, splitBlock, joinBackward, joinForward,
  insertBlocks, newTable, tableOp, autoformat } from "../../static/editor.js";

import { parseHTML } from "./dom.mjs";

/* --------------------------------------------------------------- helpers -- */
const viaDOM = (md) => renderMarkdown(readNodes(parseHTML(toHTML(parseMarkdown(md))).childNodes));
const paste = (html) => renderMarkdown(htmlToBlocks(parseHTML(html)).blocks);

/* -------------------------------------------------------------- round trip -- */
const CANONICAL = [
  "Hello **bold** and *italic* and ~~gone~~ and `code`.",
  "## Heading two\n\n### Heading three\n\n#### Heading four",
  "A [link](https://example.com \"Title\") and ![alt text](/uploads/a.png) here.",
  "- one\n- two\n  - nested\n  - nested 2\n    1. deep\n- three",
  "3. three\n4. four",
  "> quoted **text**\n>\n> - with a list",
  "```go\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n```",
  "```\n```",
  "---",
  "| Name | Value |\n| --- | :---: |\n| a | **b** |\n| `c` |  |",
  "Line one\nline two (soft break)\\\nhard break",
  "Escapes: \\*not em\\*, 1. x, a_b\\_ snake_case, \\`tick\\`, \\<b>, [a\\](b)",
  "1\\. Not a list\n\n\\# Not a heading\n\n\\- not a bullet\n\n\\> not a quote",
  "**bold *both*** and *it **st***",
  "[**bold link**](https://x.test/a_(b))",
  "- [ ] todo\n- [x] done",
  "1. Step one:\n\n   ```bash\n   run it\n   ```\n\n2. Step two",
  "Autolink <https://example.com> and <me@example.com>.",
  "Tilde ~5 minutes",
];
test("canonical Markdown survives parse -> serialize", () => {
  for (const md of CANONICAL) assert.equal(renderMarkdown(parseMarkdown(md)), md, md);
});
test("canonical Markdown survives the full DOM round trip", () => {
  for (const md of CANONICAL) assert.equal(viaDOM(md), md, md);
});

test("unsupported content is kept verbatim as opaque blocks", () => {
  const cases = [
    "<div class=\"note\">\n  <b>raw</b> html\n</div>",
    "Text with [gallery ids=\"1,2\"] shortcode and [note]inner[/note].",
    "[embed url=\"https://x.test/a_b*c\"]",
    "A footnote[^1] reference.\n\n[^1]: The note text.\n    More note.",
    "See [the docs][docs] and [docs].\n\n[docs]: https://example.com/docs \"Docs\"",
    "Inline <kbd>Ctrl</kbd> html and &nbsp; entity and &unknown; one.",
    "<!-- more -->",
  ];
  for (const md of cases) assert.equal(viaDOM(md), md, md);
  const blocks = parseMarkdown(cases[0]);
  assert.equal(blocks[0].type, "raw");
  const html = toHTML(parseMarkdown(cases[1]));
  assert.match(html, /<span class="md-raw" contenteditable="false" data-raw="\[gallery ids=&quot;1,2&quot;\]">/);
});

test("non-canonical Markdown is preserved for untouched blocks", () => {
  const md = "Title\n=====\n\n* star list\n* item\n\n__bold__ and _em_\nwrapped\n\n    indented code\n\n- dash list";
  const blocks = parseMarkdown(md);
  const dom = parseHTML(toHTML(blocks)).childNodes;
  const preserve = new Map();
  blocks.forEach((b, k) => preserve.set(blockSig(readNodes([dom[k]])[0]), b.src));
  // Simulate editing only the third block.
  const entries = dom.map((el, k) => {
    const b = readNodes([el])[0];
    if (k === 2) b.runs.push({ t: " (edited)", m: {} });
    return { block: b, src: preserve.get(blockSig(b)) ?? null };
  });
  const { md: out } = joinTop(entries);
  assert.equal(out, "Title\n=====\n\n* star list\n* item\n\n**bold** and *em*\nwrapped (edited)\n\n    indented code\n\n- dash list");
});

test("adjacent lists keep distinct markers", () => {
  const md = "- a\n\n* b";
  assert.equal(parseMarkdown(md).length, 2);
  assert.equal(renderMarkdown(parseMarkdown(md)), "- a\n\n* b");
  const entries = parseMarkdown("* a\n\n- b").map((b, k) => ({ block: b, src: k === 1 ? b.src : null }));
  assert.equal(joinTop(entries).md, "* a\n\n- b");
  const both = parseMarkdown("* a\n\n- b").map((b) => ({ block: b, src: null }));
  assert.equal(joinTop(both).md, "- a\n\n* b");
});

test("text with Markdown characters round-trips as literal text", () => {
  const texts = ["5 * 3 = 15 and a_b_c", "# hash at start", "1. looks like a list", "use <div> tags & &amp; entities", "[x](y) is not a link",
    "C:\\Users\\path\\", "price: $5 | 10%", "~~not struck~~", "ends with backslash \\"];
  for (const t of texts) {
    const md = renderMarkdown([{ type: "p", runs: [{ t, m: {} }] }]);
    const back = parseMarkdown(md);
    assert.equal(back.length, 1, md);
    assert.deepEqual(back[0].runs.map((r) => r.t).join(""), t, md);
  }
});

test("emphasis edge cases parse like CommonMark", () => {
  const runs = (md) => parseMarkdown(md)[0].runs.map((r) => [r.t, Object.keys(r.m).sort().join("")]);
  assert.deepEqual(runs("***x***"), [["x", "es"]]);
  assert.deepEqual(runs("snake_case_word"), [["snake_case_word", ""]]);
  assert.deepEqual(runs("**a** b"), [["a", "s"], [" b", ""]]);
  assert.deepEqual(runs("*a **b** c*"), [["a ", "e"], ["b", "es"], [" c", "e"]]);
  assert.deepEqual(runs("2*3*4"), [["2", ""], ["3", "e"], ["4", ""]]);
  assert.deepEqual(runs("~~del~~ ~one~"), [["del", "d"], [" ", ""], ["one", "d"]]);
});

/* ------------------------------------------------------------------ paste -- */
const WORD = `<html xmlns:o="urn:schemas-microsoft-com:office:office"><head><meta name=Generator content="Microsoft Word 15">
<style><!-- p.MsoNormal {font-family:Calibri} --></style><!--[if gte mso 9]><xml><o:OfficeDocumentSettings></o:OfficeDocumentSettings></xml><![endif]--></head>
<body lang=EN-US style='tab-interval:.5in'><!--StartFragment-->
<p class=MsoTitle><span lang=EN-US>Quarterly report<o:p></o:p></span></p>
<h1><span lang=EN-US>Summary<o:p></o:p></span></h1>
<p class=MsoNormal><span style='font-family:"Calibri",sans-serif;color:#C00000'>Revenue grew </span><b><span style='mso-bidi-font-weight:normal'>12%</span></b><span> and costs were <i>flat</i>.<o:p></o:p></span></p>
<p class=MsoNormal><o:p>&nbsp;</o:p></p>
<p class=MsoListParagraphCxSpFirst style='text-indent:-.25in;mso-list:l0 level1 lfo1'><![if !supportLists]><span style='font-family:Symbol'><span style='mso-list:Ignore'>·<span style='font:7.0pt "Times New Roman"'>&nbsp;&nbsp;&nbsp;&nbsp; </span></span></span><![endif]>First point<o:p></o:p></p>
<p class=MsoListParagraphCxSpMiddle style='margin-left:1.0in;text-indent:-.25in;mso-list:l0 level2 lfo1'><![if !supportLists]><span style='font-family:"Courier New"'><span style='mso-list:Ignore'>o<span style='font:7.0pt "Times New Roman"'>&nbsp;&nbsp; </span></span></span><![endif]>Nested point<o:p></o:p></p>
<p class=MsoListParagraphCxSpLast style='text-indent:-.25in;mso-list:l0 level1 lfo1'><![if !supportLists]><span style='mso-list:Ignore'>·<span>&nbsp; </span></span><![endif]>Second point<o:p></o:p></p>
<p class=MsoListParagraphCxSpFirst style='text-indent:-.25in;mso-list:l1 level1 lfo2'><![if !supportLists]><span><span style='mso-list:Ignore'>1.<span>&nbsp;&nbsp; </span></span></span><![endif]>Numbered one<o:p></o:p></p>
<p class=MsoListParagraphCxSpLast style='text-indent:-.25in;mso-list:l1 level1 lfo2'><![if !supportLists]><span><span style='mso-list:Ignore'>2.<span>&nbsp;&nbsp; </span></span></span><![endif]>Numbered two<o:p></o:p></p>
<h2>Details</h2>
<table class=MsoTableGrid border=1 cellspacing=0 cellpadding=0 style='border-collapse:collapse;border:none'>
 <tr><td width=200 valign=top style='border:solid windowtext 1.0pt'><p class=MsoNormal><b>Item</b><o:p></o:p></p></td><td><p class=MsoNormal><b>Cost</b><o:p></o:p></p></td></tr>
 <tr><td><p class=MsoNormal>Hosting | VPS<o:p></o:p></p></td><td><p class=MsoNormal align=right style='text-align:right'>40 €<o:p></o:p></p></td></tr>
</table>
<p class=MsoQuote>Simplicity is prerequisite for reliability.<o:p></o:p></p>
<p class=MsoNormal>See <a href="https://example.com/report?a=1&amp;b=2">the report</a>, <a href="javascript:alert(1)">bad</a>, <a name="_GoBack"></a><a href="file:///C:/x.docx">local</a>.<o:p></o:p></p>
<p class=MsoNormal><span style='font-family:Consolas'>go test ./...<o:p></o:p></span></p>
<p class=MsoNormal><span style='font-family:Consolas'>go vet ./...<o:p></o:p></span></p>
<p class=MsoNormal><img width=100 height=50 src="file:///C:/Users/x/clip_image001.png" v:shapes="Picture_x0020_1"><o:p></o:p></p>
<!--EndFragment--></body></html>`;

test("Word HTML becomes clean structural Markdown", () => {
  const { blocks, images } = htmlToBlocks(parseHTML(WORD));
  assert.equal(images.dropped, 1);
  assert.equal(renderMarkdown(blocks), [
    "## Quarterly report",
    "## Summary",
    "Revenue grew **12%** and costs were *flat*.",
    "- First point\n  - Nested point\n- Second point",
    "1. Numbered one\n2. Numbered two",
    "### Details",
    "| **Item** | **Cost** |\n| --- | --- |\n| Hosting \\| VPS | 40 € |",
    "> Simplicity is prerequisite for reliability.",
    "See [the report](https://example.com/report?a=1&b=2), bad, local.",
    "```\ngo test ./...\ngo vet ./...\n```",
  ].join("\n\n"));
});

const GDOCS = `<meta charset="utf-8"><b style="font-weight:normal;" id="docs-internal-guid-1234"><p dir="ltr" style="line-height:1.38;margin-top:0pt;margin-bottom:0pt;"><span style="font-size:26pt;font-family:Arial;color:#000000;font-weight:400;white-space:pre;white-space:pre-wrap;">Launch plan</span></p><h1 dir="ltr" style="line-height:1.38;margin-top:20pt;"><span style="font-size:20pt;font-family:Arial;font-weight:400;">Goals</span></h1><p dir="ltr"><span style="font-size:11pt;font-weight:400;">We want </span><span style="font-size:11pt;font-weight:700;">fast</span><span style="font-size:11pt;font-weight:400;"> and </span><span style="font-size:11pt;font-style:italic;font-weight:400;">simple</span><span style="font-size:11pt;text-decoration:line-through;font-weight:400;">cheap</span><span style="font-size:11pt;font-weight:400;"> tools, see </span><a href="https://www.google.com/url?q=https://example.org/page&amp;sa=D&amp;source=editors" style="text-decoration:none;"><span style="font-size:11pt;color:#1155cc;text-decoration:underline;">this page</span></a><span style="font-size:11pt;">.</span></p><br><ul style="margin-top:0;margin-bottom:0;"><li dir="ltr" style="list-style-type:disc;font-size:11pt;" aria-level="1"><p dir="ltr" role="presentation"><span style="font-size:11pt;">Alpha</span></p></li><ul style="margin-top:0;"><li dir="ltr" style="list-style-type:circle;" aria-level="2"><p dir="ltr" role="presentation"><span style="font-size:11pt;">Alpha one</span></p></li></ul><li dir="ltr" aria-level="1"><p dir="ltr" role="presentation"><span style="font-size:11pt;">Beta</span></p></li></ul><br><ol><li aria-level="1"><p><span>First</span></p></li><li aria-level="1"><p><span>Second</span></p></li></ol><p dir="ltr"><span style="font-size:11pt;">Image: </span><span><img src="https://lh3.googleusercontent.com/abc" width="200" height="100"></span><span><img src="data:image/png;base64,iVBORw0KGgo="></span></p><div dir="ltr" align="left"><table style="border:none;border-collapse:collapse;"><colgroup><col width="100"></colgroup><tbody><tr style="height:0pt"><td style="padding:5pt"><p dir="ltr"><span style="font-weight:700;">H1</span></p></td><td><p dir="ltr"><span>H2</span></p></td></tr><tr><td><p><span>a</span></p></td><td><p><span>b</span></p></td></tr></tbody></table></div></b>`;

test("Google Docs HTML becomes clean structural Markdown", () => {
  const { blocks, images } = htmlToBlocks(parseHTML(GDOCS));
  assert.deepEqual(images.data, ["data:image/png;base64,iVBORw0KGgo="]);
  assert.equal(renderMarkdown(blocks), [
    "## Launch plan",
    "## Goals",
    "We want **fast** and *simple*~~cheap~~ tools, see [this page](https://example.org/page).",
    "- Alpha\n  - Alpha one\n- Beta",
    "1. First\n2. Second",
    "Image: ![](https://lh3.googleusercontent.com/abc)![](data:image/png;base64,iVBORw0KGgo=)",
    "| **H1** | H2 |\n| --- | --- |\n| a | b |",
  ].join("\n\n"));
});

test("web pages: scripts, styles, attributes and unsafe URLs are dropped", () => {
  const html = `<div class="post" onclick="evil()"><script>alert(1)</script><style>p{color:red}</style>
    <h3 style="color:red">Title <small>sub</small></h3><p class="lead">Some <span style="font-weight:bold">bold</span> and <u>under</u>
    text with <a href="https://ok.test/" onclick="x()">ok</a> <a href="/relative">rel</a>.</p>
    <pre><code class="language-js">let a = 1;\n  let b = 2;\n</code></pre>
    <blockquote><p>Quoted</p></blockquote><img src="javascript:alert(1)" alt="x"><img src="https://img.test/a.jpg" alt="A cat" onerror="x()">
    <ul><li><input type="checkbox" checked> done task</li></ul><hr><p>Kbd <kbd>Ctrl</kbd>+<code>C</code></p></div>`;
  assert.equal(paste(html), [
    "### Title sub",
    "Some **bold** and under text with [ok](https://ok.test/) rel.",
    "```js\nlet a = 1;\n  let b = 2;\n```",
    "> Quoted",
    "![A cat](https://img.test/a.jpg)",
    "- [x] done task",
    "---",
    "Kbd `Ctrl`+`C`",
  ].join("\n\n"));
});

test("VS Code style code pastes become a code block", () => {
  const html = `<div style="color: #d4d4d4;background-color: #1e1e1e;font-family: Menlo, Monaco, 'Courier New', monospace;white-space: pre;"><div><span style="color: #569cd6;">func</span><span> main() {</span></div><div><span>    run()</span></div><div><span>}</span></div></div>`;
  assert.equal(paste(html), "```\nfunc main() {\n    run()\n}\n```");
});

test("heading levels are clamped to h2-h4 and nested lists flatten correctly", () => {
  assert.equal(paste("<h4>a</h4><h5>b</h5><h6>c</h6>"), "#### a\n\n#### b\n\n#### c");
  assert.equal(paste("<h1>a</h1><h2>b</h2><h3>c</h3><h4>d</h4>"), "## a\n\n### b\n\n#### c\n\n#### d");
  assert.equal(paste("<ol start=3><li>three<ul><li>x</li></ul></li><li>four</li></ol>"), "3. three\n   - x\n4. four");
});

test("plain text detection", () => {
  assert.ok(looksLikeMarkdown("# Title\n\nText"));
  assert.ok(looksLikeMarkdown("some **bold** text"));
  assert.ok(!looksLikeMarkdown("Just a sentence. Another one."));
});

/* --------------------------------------------------------- edit commands -- */
// "‸" marks the caret / selection start and "⁁" the selection end.
const M1 = "\uE000", M2 = "\uE001";
const mk = (md) => parseMarkdown(md.replace(/‸/g, M1).replace(/⁁/g, M2));
function show(blocks) {
  const vis = (s) => s.replace(/\uE000/g, "‸").replace(/\uE001/g, "⁁").replace(/\u200B/g, "");
  const walk = (bs) => bs.map((b) => {
    if (b.type === "p" || b.type === "h") return { ...b, runs: b.runs.map((r) => (r.t !== undefined ? { ...r, t: vis(r.t) } : r)) };
    if (b.type === "code") return { ...b, text: vis(b.text) };
    if (b.type === "quote") return { ...b, blocks: walk(b.blocks) };
    if (b.type === "list") return { ...b, items: b.items.map((it) => ({ ...it, blocks: walk(it.blocks) })) };
    if (b.type === "table") return { ...b, rows: b.rows.map((row) => row.map((c) => c.map((r) => (r.t !== undefined ? { ...r, t: vis(r.t) } : r)))) };
    return b;
  });
  return renderMarkdown(walk(blocks));
}
const cmd = (md, fn) => show(fn(mk(md)));

test("inline marks toggle over selections", () => {
  assert.equal(cmd("a ‸bold⁁ b", (b) => toggleMark(b, "s")), "a **‸bold⁁** b");
  assert.equal(cmd("a **‸bold⁁** b", (b) => toggleMark(b, "s")), "a ‸bold⁁ b");
  assert.equal(cmd("x‸one\n\ntwo⁁y", (b) => toggleMark(b, "e")), "x*‸one*\n\n*two⁁*y");
  assert.equal(cmd("a ‸**b**c⁁", (b) => toggleMark(b, "s")), "a **‸bc⁁**");
  assert.equal(cmd("a ‸x⁁ b", (b) => toggleMark(b, "c")), "a `‸x⁁` b");
  assert.equal(cmd("a ‸x⁁ b", (b) => toggleMark(b, "d")), "a ~~‸x⁁~~ b");
  // Collapsed caret: a pending zero-width run, dropped when serialized empty.
  const b = toggleMark(mk("ab‸"), "s");
  assert.equal(b[0].runs[b[0].runs.length - 1].m.s, true);
});

test("links are set, edited and removed", () => {
  assert.equal(cmd("go ‸here⁁ now", (b) => setLink(b, { href: "https://x.test" })), "go [‸here⁁](https://x.test) now");
  assert.equal(cmd("go [he‸re](https://a.test) now", (b) => setLink(b, { href: "https://b.test" })), "go [he‸re](https://b.test) now");
  assert.equal(cmd("go [he‸re](https://a.test) now", (b) => setLink(b, null)), "go he‸re now");
  assert.equal(cmd("go ‸", (b) => setLink(b, { href: "https://x.test", text: "site" })), "go [site](https://x.test)‸");
});

test("block types, code blocks and quotes", () => {
  assert.equal(cmd("one‸\n\ntwo", (b) => setBlockType(b, 2)), "## one‸\n\ntwo");
  assert.equal(cmd("## one‸", (b) => setBlockType(b, 2)), "one‸");
  assert.equal(cmd("‸one\n\ntwo⁁", (b) => setBlockType(b, 3)), "### ‸one\n\n### two⁁");
  assert.equal(cmd("‸a\n\nb⁁", toggleCodeBlock), "```\n‸a\nb⁁\n```");
  assert.equal(cmd("```js\na‸\nb\n```", toggleCodeBlock), "a‸\n\nb");
  assert.equal(cmd("one‸", toggleQuote), "> one‸");
  assert.equal(cmd("> one‸", toggleQuote), "one‸");
});

test("lists wrap, unwrap, switch, merge and indent", () => {
  assert.equal(cmd("‸a\n\nb⁁", (b) => toggleList(b, false)), "- ‸a\n- b⁁");
  assert.equal(cmd("- a\n- b‸\n- c", (b) => toggleList(b, false)), "- a\n\nb‸\n\n- c");
  assert.equal(cmd("- a\n- b‸", (b) => toggleList(b, true)), "1. a\n2. b‸");
  assert.equal(cmd("- a\n\nb‸", (b) => toggleList(b, false)), "- a\n- b‸");
  assert.equal(cmd("- a\n- b‸\n- c", (b) => indentList(b, false)), "- a\n  - b‸\n- c");
  assert.equal(cmd("- a\n  - b‸\n- c", (b) => indentList(b, true)), "- a\n- b‸\n- c");
  assert.equal(cmd("- a\n  - b‸\n  - c\n- d", (b) => indentList(b, true)), "- a\n- b‸\n  - c\n- d");
  assert.equal(cmd("- ‸a", (b) => indentList(b, false)), "- ‸a");
});

test("enter, backspace and delete follow word-processor rules", () => {
  assert.equal(cmd("ab‸cd", splitBlock), "ab\n\n‸cd");
  assert.equal(cmd("## Title‸", splitBlock), "## Title\n\n‸");
  assert.equal(cmd("- one‸two", splitBlock), "- one\n- ‸two");
  assert.equal(cmd("- one\n- ‸", splitBlock), "- one\n\n‸");
  assert.equal(cmd("- one\n  - ‸", splitBlock), "- one\n- ‸");
  assert.equal(cmd("> quote\n>\n> ‸", (b) => splitBlock(splitBlock(mk("> quote‸")))), "> quote\n\n‸");
  assert.equal(cmd("ab\n\n‸cd", joinBackward), "ab‸cd");
  assert.equal(cmd("## ‸Title", joinBackward), "‸Title");
  assert.equal(cmd("- a\n- ‸b", joinBackward), "- a\n\n‸b");
  assert.equal(cmd("> ‸q", joinBackward), "‸q");
  assert.equal(cmd("x\n\n***\n\n‸y", joinBackward), "x\n\n‸y");
  assert.equal(cmd("ab‸\n\ncd", joinForward), "ab‸cd");
  assert.equal(cmd("- a‸\n- b", joinForward), "- a‸b");
});

test("insertion of blocks at the caret", () => {
  assert.equal(cmd("ab‸cd", (b) => insertBlocks(b, mk("**X**"))), "ab**X**‸cd");
  assert.equal(cmd("ab‸cd", (b) => insertBlocks(b, mk("## H\n\npara"))), "ab\n\n## H\n\npara‸\n\ncd");
  assert.equal(cmd("‸", (b) => insertBlocks(b, mk("one\n\ntwo"))), "one\n\ntwo‸");
  assert.equal(cmd("- a‸", (b) => insertBlocks(b, mk("- x\n- y"))), "- a\n- x\n- y‸");
  assert.equal(cmd("text‸", (b) => insertBlocks(b, [{ type: "hr" }], { blocks: true })), "text\n\n---\n\n‸");
});

test("tables: create, navigate, add and delete rows and columns", () => {
  const t = [newTable(2, 2)];
  assert.equal(show(t), "| ‸ |  |\n| --- | --- |\n|  |  |");
  assert.equal(cmd("| a‸ | b |\n| - | - |\n| c | d |", (b) => tableOp(b, "next")), "| a | b‸ |\n| --- | --- |\n| c | d |");
  assert.equal(cmd("| a | b |\n| - | - |\n| c | d‸ |", (b) => tableOp(b, "next")), "| a | b |\n| --- | --- |\n| c | d |\n| ‸ |  |");
  assert.equal(cmd("| a‸ | b |\n| - | - |", (b) => tableOp(b, "col-add")), "| a | ‸ | b |\n| --- | --- | --- |");
  assert.equal(cmd("| a | b |\n| - | - |\n| c‸ | d |", (b) => tableOp(b, "row-del")), "| a‸ | b |\n| --- | --- |");
  assert.equal(cmd("| a‸ | b |\n| - | - |", (b) => tableOp(b, "table-del")), "‸");
});

test("Markdown autoformat prefixes", () => {
  assert.equal(cmd("‸x", (b) => autoformat(b, "h", 2)), "## ‸x");
  assert.equal(cmd("‸x", (b) => autoformat(b, "ul")), "- ‸x");
  assert.equal(cmd("‸x", (b) => autoformat(b, "ol", 3)), "3. ‸x");
  assert.equal(cmd("‸x", (b) => autoformat(b, "quote")), "> ‸x");
  assert.equal(cmd("‸", (b) => autoformat(b, "code", "go")), "```go\n‸\n```");
  assert.equal(cmd("‸", (b) => autoformat(b, "hr")), "---\n\n‸");
});

test("deleting a selection merges the blocks at both ends", async () => {
  const { deleteSelection, typeText, insertBreak, codeEnter, codeIndent, exitBlock } = await import("../../static/editor.js");
  assert.equal(cmd("ab‸cd⁁ef", deleteSelection), "ab‸ef");
  assert.equal(cmd("a**b‸c**\n\nmid\n\nx⁁y", deleteSelection), "a**b‸**y");
  assert.equal(cmd("## Ti‸tle\n\n- one\n- tw⁁o\n- three", deleteSelection), "## Ti‸o\n\n- three");
  assert.equal(cmd("p‸q\n\n| a | b |\n| - | - |\n| c | d |\n\nr⁁s", deleteSelection), "p‸s");
  assert.equal(cmd("| a‸ | b |\n| - | - |\n| c | d⁁ |\n\nz", deleteSelection), "| a‸ |  |\n| --- | --- |\n|  |  |\n\nz");
  assert.equal(cmd("```\nco‸de\n```\n\npa⁁ra", deleteSelection), "```\nco‸\n```\n\nra");
  assert.equal(cmd("x‸y", (b) => typeText(b, "1\n2")), "x1\\\n2‸y");
  assert.equal(cmd("**b‸**", (b) => typeText(b, "!")), "**b!‸**");
  assert.equal(cmd("a‸b", insertBreak), "a\\\n‸b");
  assert.equal(cmd("```\n  if x‸\n```", codeEnter), "```\n  if x\n  ‸\n```");
  assert.equal(cmd("```\nx\n‸\n```", codeEnter), "```\nx\n```\n\n‸");
  assert.equal(cmd("```\n‸a\nb⁁\n```", (b) => codeIndent(b, false)), "```\n  ‸a\n  b⁁\n```");
  assert.equal(cmd("```\n  ‸a\n```", (b) => codeIndent(b, true)), "```\n‸a\n```");
  assert.equal(cmd("| a‸ |\n| - |", exitBlock), "| a |\n| --- |\n\n‸");
});

test("prepareHTML renames style attributes without changing the result", async () => {
  const { prepareHTML } = await import("../../static/editor.js");
  for (const src of [WORD, GDOCS]) {
    const prepared = prepareHTML(src);
    assert.ok(!/\sstyle\s*=/i.test(prepared.replace(/<!--[\s\S]*?-->/g, "")));
    assert.ok(!/<style/i.test(prepared));
    assert.equal(renderMarkdown(htmlToBlocks(parseHTML(prepared)).blocks), renderMarkdown(htmlToBlocks(parseHTML(src)).blocks));
  }
});
