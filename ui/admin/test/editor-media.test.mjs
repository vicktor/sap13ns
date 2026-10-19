// Tests for the editor's videos and image layout: video links, the video
// popover's check, pasting a link, the resize maths and the image bar.
// Run with `npm test`.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { parseMarkdown, renderMarkdown, toHTML, readNodes, blockSig, parseVideoURL, parseStart, videoLine, videoPlayerURL, checkVideo,
  videoFacadeHTML, VIDEO_META, VIDEO_TEXT, insertVideo, pasteVideo, parseImageAttrs, imageAttrsMd, imageOp, imgHTML, snapWidth, stepWidth, resizeWidth,
  imageHeight } from "../../static/editor.js";
import { parseHTML } from "./dom.mjs";

const M1 = "";
const viaDOM = (md) => renderMarkdown(readNodes(parseHTML(toHTML(parseMarkdown(md))).childNodes));
// The player script's own check (static/video.js), which every player URL
// must pass.
const PLAYER = new RegExp(/var PLAYER = \/(.+)\/;/.exec(readFileSync(new URL("../../static/video.js", import.meta.url), "utf8"))[1]);

/* ----------------------------------------------------------- video links -- */
test("video URLs: accepted forms and their players", () => {
  const cases = [
    ["https://www.youtube.com/watch?v=dQw4w9WgXcQ", "https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ?autoplay=1"],
    ["https://youtube.com/watch?v=dQw4w9WgXcQ&t=1m30s", "https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ?autoplay=1&start=90"],
    ["https://m.youtube.com/watch?feature=share&v=dQw4w9WgXcQ", "https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ?autoplay=1"],
    ["https://youtu.be/dQw4w9WgXcQ?t=42", "https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ?autoplay=1&start=42"],
    ["https://www.youtube.com/shorts/dQw4w9WgXcQ", "https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ?autoplay=1"],
    ["https://www.youtube.com/embed/dQw4w9WgXcQ?start=10", "https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ?autoplay=1&start=10"],
    ["https://www.youtube.com/live/dQw4w9WgXcQ", "https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ?autoplay=1"],
    ["https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ", "https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ?autoplay=1"],
    ["https://www.youtube.com/playlist?list=PLx0sYbCqOb8TBPRdmBHs5Iftvv9TPboYG", "https://www.youtube-nocookie.com/embed/videoseries?list=PLx0sYbCqOb8TBPRdmBHs5Iftvv9TPboYG&autoplay=1"],
    ["https://vimeo.com/76979871", "https://player.vimeo.com/video/76979871?autoplay=1&dnt=1"],
    ["https://vimeo.com/76979871/9f8e7d6c5b", "https://player.vimeo.com/video/76979871?autoplay=1&dnt=1&h=9f8e7d6c5b"],
    ["https://vimeo.com/76979871#t=1m5s", "https://player.vimeo.com/video/76979871?autoplay=1&dnt=1#t=65s"],
    ["https://player.vimeo.com/video/76979871?h=9f8e7d6c5b", "https://player.vimeo.com/video/76979871?autoplay=1&dnt=1&h=9f8e7d6c5b"],
    ["https://vimeo.com/channels/staffpicks/76979871", "https://player.vimeo.com/video/76979871?autoplay=1&dnt=1"],
    ["https://vimeo.com/groups/motion/videos/76979871", "https://player.vimeo.com/video/76979871?autoplay=1&dnt=1"],
  ];
  for (const [url, player] of cases) {
    const v = parseVideoURL(url);
    assert.ok(v, url);
    assert.equal(videoPlayerURL(v), player, url);
    assert.match(videoPlayerURL(v), PLAYER, url);
  }
  assert.equal(parseVideoURL("https://www.youtube.com/shorts/dQw4w9WgXcQ").portrait, true);
});

test("video URLs: look-alikes and malformed addresses are refused", () => {
  for (const url of [
    "", "dQw4w9WgXcQ", "ftp://youtube.com/watch?v=dQw4w9WgXcQ", "javascript:alert(1)//youtube.com/watch?v=dQw4w9WgXcQ",
    "https://youtube.com.evil.test/watch?v=dQw4w9WgXcQ", "https://evilyoutube.com/watch?v=dQw4w9WgXcQ",
    "https://user:pass@youtube.com/watch?v=dQw4w9WgXcQ", "https://youtube.com:8443/watch?v=dQw4w9WgXcQ",
    "https://www.youtube.com/watch?v=short", "https://www.youtube.com/watch?v=dQw4w9WgXcQx", "https://www.youtube.com/channel/UC123",
    "https://youtu.be/dQw4w9WgXcQ/extra", "https://www.youtube.com/playlist?list=short", "https://vimeo.com/0123", "https://vimeo.com/abc",
    "https://vimeo.com/76979871/NOTHEX", "https://player.vimeo.com/76979871", "https://vimeo.com/channels/a b/76979871",
    "https://www.youtube.com/watch?v=" + "a".repeat(3000),
  ]) assert.equal(parseVideoURL(url), null, url);
});

test("start offsets", () => {
  for (const [s, n] of [["90", 90], ["90s", 90], ["1m30s", 90], ["1h2m3s", 3723], ["1:30", 90], ["1:02:03", 3723], ["", 0], ["1:75", 0], ["x", 0], ["100:00:00", 0], ["0", 0]]) {
    assert.equal(parseStart(s), n, s);
  }
});

test("a video line: bare, in angle brackets or the [video] shortcode", () => {
  assert.equal(videoLine("https://youtu.be/dQw4w9WgXcQ").url, "https://youtu.be/dQw4w9WgXcQ");
  assert.equal(videoLine("<https://youtu.be/dQw4w9WgXcQ>").url, "https://youtu.be/dQw4w9WgXcQ");
  const sc = videoLine('[video url="https://vimeo.com/76979871" start=1:05 title="  The   New Player "]');
  assert.deepEqual([sc.shortcode, sc.start, sc.title, sc.id], [true, 65, "The New Player", "76979871"]);
  assert.equal(videoLine("[video url=https://youtu.be/dQw4w9WgXcQ start=bad]").start, 0);
  for (const line of ["Watch https://youtu.be/dQw4w9WgXcQ", "[video url=https://example.com/a.mp4]", "[videos url=https://youtu.be/dQw4w9WgXcQ]",
    "[video]", "https://youtu.be/dQw4w9WgXcQ'", "[Video](https://youtu.be/dQw4w9WgXcQ)"]) assert.equal(videoLine(line), null, line);
});

test("only a link alone in a top-level paragraph becomes a video block", () => {
  const md = [
    "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
    "Text then https://youtu.be/dQw4w9WgXcQ",
    "https://youtu.be/dQw4w9WgXcQ\nsecond line",
    "- https://youtu.be/dQw4w9WgXcQ",
    "> https://youtu.be/dQw4w9WgXcQ",
    '[video url="https://vimeo.com/76979871" title="Vimeo"]',
    "<https://vimeo.com/76979871>",
  ].join("\n\n");
  const kinds = parseMarkdown(md).map((b) => b.type + (b.kind ? ":" + b.kind : ""));
  assert.deepEqual(kinds, ["raw:video", "p", "p", "list", "quote", "raw:video", "raw:video"]);
  // Kept verbatim, also through the surface.
  assert.equal(renderMarkdown(parseMarkdown(md)), md);
  assert.equal(viaDOM(md), md);
});

test("the editor's player is the site's facade, with no third-party request", () => {
  const html = toHTML(parseMarkdown("https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=90"));
  assert.match(html, /^<div class="md-raw md-video" contenteditable="false" data-raw="https:\/\/www\.youtube\.com\/watch\?v=dQw4w9WgXcQ&amp;t=90" data-kind="video" data-label="Video">/);
  assert.match(html, /<figure class="sap-video sap-video-youtube" data-video-provider="youtube"><button type="button" class="sap-video-link"/);
  assert.match(html, /data-video-src="https:\/\/www\.youtube-nocookie\.com\/embed\/dQw4w9WgXcQ\?autoplay=1&amp;start=90"/);
  assert.match(html, /aria-label="Play YouTube video"/);
  assert.doesNotMatch(html, /<iframe|<img|ytimg/);
  // With what the server knows: title and the poster it caches.
  const v = videoLine("https://vimeo.com/76979871");
  const f = videoFacadeHTML(v, { title: 'A "film" <b>', poster: "/video-thumbs/vm-76979871.jpg", width: 1280, height: 720 });
  assert.match(f, /<img class="sap-video-poster" src="\/video-thumbs\/vm-76979871\.jpg" alt="" width="1280" height="720">/);
  assert.match(f, /aria-label="Play video: A &quot;film&quot; &lt;b&gt; \(Vimeo\)"/);
  assert.match(f, /<span class="sap-video-title">A &quot;film&quot; &lt;b&gt;<\/span>/);
  // Only posters from this site's cache are shown.
  assert.doesNotMatch(videoFacadeHTML(v, { poster: "https://i.vimeocdn.com/x.jpg" }), /<img/);
  assert.doesNotMatch(videoFacadeHTML(v, { poster: '/video-thumbs/x.jpg" onerror="x' }), /<img/);
  // Portrait: Shorts, or what the provider reports.
  assert.match(videoFacadeHTML(videoLine("https://youtube.com/shorts/dQw4w9WgXcQ")), /sap-video-portrait/);
  assert.match(videoFacadeHTML(v, { portrait: true }), /sap-video-portrait/);
  // Embeds turned off in the settings: the block says so.
  VIDEO_META.set("https://vimeo.com/1", { mode: "off" });
  VIDEO_TEXT.off = "Visitors see a link.";
  try {
    assert.match(toHTML(parseMarkdown("https://vimeo.com/1")), /<p class="md-video-note">Visitors see a link\.<\/p><\/div>$/);
    assert.doesNotMatch(toHTML(parseMarkdown("https://vimeo.com/2")), /md-video-note/);
  } finally { VIDEO_META.delete("https://vimeo.com/1"); VIDEO_TEXT.off = ""; }
});

/* ------------------------------------------------------- video popover -- */
test("the video popover accepts YouTube and Vimeo addresses only", () => {
  assert.deepEqual(checkVideo("  https://youtu.be/dQw4w9WgXcQ  "), { ok: true, line: "https://youtu.be/dQw4w9WgXcQ", video: checkVideo("https://youtu.be/dQw4w9WgXcQ").video });
  assert.equal(checkVideo("<https://youtu.be/dQw4w9WgXcQ>").line, "https://youtu.be/dQw4w9WgXcQ");
  assert.equal(checkVideo('[video url="https://vimeo.com/76979871" start=30]').line, '[video url="https://vimeo.com/76979871" start=30]');
  assert.deepEqual(checkVideo(""), { ok: false, empty: true });
  for (const bad of ["https://example.com/video.mp4", "https://www.tiktok.com/@a/video/123", "not a url", "https://youtu.be/x"]) assert.equal(checkVideo(bad).ok, false, bad);
});

test("inserting a video from the toolbar", () => {
  const md = (blocks) => renderMarkdown(blocks);
  // In an empty paragraph it takes its place.
  assert.equal(md(insertVideo(parseMarkdown("Intro\n\n" + M1), "https://youtu.be/dQw4w9WgXcQ")), "Intro\n\nhttps://youtu.be/dQw4w9WgXcQ");
  // In the middle of a paragraph it splits it.
  assert.equal(md(insertVideo(parseMarkdown("Before" + M1 + "after"), "https://vimeo.com/76979871")), "Before\n\nhttps://vimeo.com/76979871\n\nafter");
  // In a list (or a quote) it goes after it: only top-level links are players.
  const out = insertVideo(parseMarkdown("- one" + M1 + "\n- two\n\nNext"), "https://vimeo.com/76979871");
  assert.equal(md(out), "- one\n- two\n\nhttps://vimeo.com/76979871\n\nNext");
  const carets = JSON.stringify(out, (key, v) => (key === "src" ? undefined : v)).split(M1).length - 1;
  assert.equal(carets, 1, "one caret, after the video");
  assert.ok(out[2].runs[0].t.includes(M1));
});

/* ---------------------------------------------------------------- paste -- */
test("a video link pasted on an empty top-level line becomes a video", () => {
  const blocks = pasteVideo(parseMarkdown("Intro\n\n" + M1), " https://www.youtube.com/watch?v=dQw4w9WgXcQ \n");
  assert.deepEqual(blocks.map((b) => b.type + (b.kind ? ":" + b.kind : "")), ["p", "raw:video", "p"]);
  assert.equal(renderMarkdown(blocks), "Intro\n\nhttps://www.youtube.com/watch?v=dQw4w9WgXcQ");
  // Anywhere else it is pasted as text.
  assert.equal(pasteVideo(parseMarkdown("Some text " + M1), "https://youtu.be/dQw4w9WgXcQ"), null);
  assert.equal(pasteVideo(parseMarkdown("- " + M1), "https://youtu.be/dQw4w9WgXcQ"), null);
  assert.equal(pasteVideo(parseMarkdown("> " + M1), "https://youtu.be/dQw4w9WgXcQ"), null);
  assert.equal(pasteVideo(parseMarkdown(M1), "https://example.com/page"), null);
  assert.equal(pasteVideo(parseMarkdown(M1), "https://youtu.be/dQw4w9WgXcQ\nhttps://vimeo.com/76979871"), null);
});

/* --------------------------------------------------------- image layout -- */
test("image layout attributes: strict, canonical and kept through the surface", () => {
  assert.deepEqual(parseImageAttrs("width=480 align=left"), { width: 480, align: "left" });
  assert.deepEqual(parseImageAttrs("align=full"), { align: "full" });
  for (const bad of ["width=0", "width=0480", "width=4097", "width=12px", "align=wide", "width=1 width=2", "height=10", "align=left foo=1"]) {
    assert.equal(parseImageAttrs(bad), null, bad);
  }
  const md = "Text ![Cat](/uploads/cat.jpg){width=480 align=left} wraps.\n\n![Dog](/uploads/dog.jpg \"Title\"){align=center}";
  assert.equal(renderMarkdown(parseMarkdown(md)), md);
  assert.equal(viaDOM(md), md);
  // Other orders are read and written canonically.
  assert.equal(renderMarkdown(parseMarkdown("![a](/a.png){align=right width=300}")), "![a](/a.png){width=300 align=right}");
  // Anything else after an image stays text, and text that looks like
  // attributes is escaped.
  assert.equal(renderMarkdown(parseMarkdown("![a](/a.png){width=big}")), "![a](/a.png){width=big}");
  assert.equal(renderMarkdown(parseMarkdown("![a](/a.png)\\{width=3}")), "![a](/a.png)\\{width=3}");
  assert.equal(renderMarkdown(parseMarkdown("![a](/a.png) {width=3}")), "![a](/a.png) {width=3}");
  // On the surface: width and alignment attributes plus the site's class.
  assert.equal(imgHTML({ src: "/c.jpg", alt: "C", width: 480, align: "left" }), '<img src="/c.jpg" alt="C" width="480" data-align="left" class="align-left">');
  assert.equal(imgHTML({ src: "/c.jpg", alt: "" }), '<img src="/c.jpg" alt="">');
});

test("pasted images do not bring their sizes along", () => {
  const blocks = readNodes(parseHTML('<p><img src="https://x.test/a.png" alt="a" width="300" data-align="left"></p>').childNodes, { paste: true });
  assert.equal(renderMarkdown(blocks), "![a](https://x.test/a.png)");
});

test("resize maths: tenths of the column, the image's ratio", () => {
  const col = 680;
  assert.equal(snapWidth(347, col), 340);
  assert.equal(snapWidth(380, col), 408);
  assert.equal(snapWidth(5, col), 68); // at least a tenth
  assert.equal(snapWidth(2000, col), 680); // at most the column
  assert.equal(snapWidth(500, 0), 500);
  // Corners: the right ones grow to the right, the left ones to the left.
  assert.equal(resizeWidth(400, 100, 0, "se", 0.5), 500);
  assert.equal(resizeWidth(400, 100, 0, "ne", 0.5), 500);
  assert.equal(resizeWidth(400, 100, 0, "nw", 0.5), 300);
  assert.equal(resizeWidth(400, -100, 0, "sw", 0.5), 500);
  // A vertical drag keeps the ratio: 50px taller is 100px wider at 2:1.
  assert.equal(resizeWidth(400, 10, 50, "se", 0.5), 500);
  assert.equal(resizeWidth(400, 0, 50, "nw", 0.5), 300);
  // Arrow keys: one tenth more or less, within 10%..100%.
  assert.equal(stepWidth(340, col, 1), 408);
  assert.equal(stepWidth(340, col, -1), 272);
  assert.equal(stepWidth(68, col, -1), 68);
  assert.equal(stepWidth(680, col, 1), 680);
  assert.equal(stepWidth(300, col, 10), 680);
  // The height follows the original's ratio, rounded as the server does.
  assert.equal(imageHeight(480, 1200, 800), 320);
  assert.equal(imageHeight(333, 1000, 667), 222);
  assert.equal(imageHeight(100, 0, 0), 0);
});

test("the image bar updates the Markdown", () => {
  const block = parseMarkdown("Text ![Cat](/uploads/cat.jpg) more")[0];
  const k = block.runs.findIndex((r) => r.a === "img");
  const apply = (op, v) => { block.runs[k] = imageOp(block.runs[k], op, v); return blockSig(block); };
  assert.equal(apply("left"), "Text ![Cat](/uploads/cat.jpg){align=left} more");
  assert.equal(apply("width", 480), "Text ![Cat](/uploads/cat.jpg){width=480 align=left} more");
  assert.equal(apply("right"), "Text ![Cat](/uploads/cat.jpg){width=480 align=right} more");
  assert.equal(apply("right"), "Text ![Cat](/uploads/cat.jpg){width=480} more"); // the active one again: inline
  assert.equal(apply("center"), "Text ![Cat](/uploads/cat.jpg){width=480 align=center} more");
  assert.equal(apply("full"), "Text ![Cat](/uploads/cat.jpg){align=full} more"); // the column's width
  assert.equal(apply("width", 300), "Text ![Cat](/uploads/cat.jpg){width=300} more");
  assert.equal(apply("width", 0), "Text ![Cat](/uploads/cat.jpg){width=300} more"); // out of range: unchanged
  assert.equal(apply("width", 5000), "Text ![Cat](/uploads/cat.jpg){width=300} more");
  assert.equal(apply("center"), "Text ![Cat](/uploads/cat.jpg){width=300 align=center} more");
  assert.equal(apply("original"), "Text ![Cat](/uploads/cat.jpg){align=center} more");
  assert.equal(imageAttrsMd(imageOp(block.runs[k], "center")), "");
  // The surface renders the result and reads it back unchanged.
  const html = toHTML([block]);
  assert.match(html, /<img src="\/uploads\/cat\.jpg" alt="Cat" data-align="center" class="align-center">/);
  assert.equal(renderMarkdown(readNodes(parseHTML(html).childNodes)), "Text ![Cat](/uploads/cat.jpg){align=center} more");
});
