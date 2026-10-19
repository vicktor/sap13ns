// sap13ns post editor: a Word-like visual mode over the Markdown textarea,
// Markdown <-> DOM conversion for a constrained subset, and smart paste
// (Word, Google Docs, LibreOffice, web pages).
//
// The textarea[name=content] stays the source of truth: every visual change is
// serialized back into it and an `input` event is dispatched, so autosave, the
// word count and the unsaved-changes guard keep working unchanged.
//
// Loaded as an ES module on the post editor only. Everything above the
// "browser" section is pure (no globals) and exported for the Node tests in
// ui/static/editor.test.mjs. Strict CSP: no inline styles or handlers.

/* ================================================================== model ==
Block  {type:"p", runs} | {type:"h", level, runs} | {type:"code", lang, text}
       {type:"quote", blocks} | {type:"hr"} | {type:"raw", kind, text}
       {type:"list", ordered, start, loose, items:[{checked, blocks}]}
       {type:"table", align:[null|"left"|"center"|"right"], rows:[[runs]]}
Run    {t:"text", m} | {a:"br", m} | {a:"img", src, alt, title, m} | {a:"raw", v, m}
Marks  {s, e, d, c, l:{href, title, auto}}  (strong, em, del, code, link)
"raw" blocks/runs are anything outside the subset (HTML, shortcodes, footnotes,
reference links, unknown entities): they are kept verbatim and shown as
non-editable chips, so switching modes never loses content. */

const ZW = "\u200B";
const M1 = "\uE000"; // selection start marker (commands only, never serialized)
const M2 = "\uE001"; // selection end marker
const ASCII_PUNCT = /[!-/:-@[-`{-~]/;
const isWS = (c) => c === undefined || /\s/u.test(c);
const isPunct = (c) => c !== undefined && /[\p{P}\p{S}]/u.test(c);
const isAlnum = (c) => c !== undefined && /[\p{L}\p{N}]/u.test(c);
const escHTML = (s) => String(s).replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" })[c]);
const isBlank = (l) => /^[ \t]*$/.test(l);
const normLabel = (s) => s.trim().replace(/\s+/g, " ").toLowerCase();
const stripInvisible = (s) => s.replace(/[\u200B\uE000\uE001]/g, "");

export const LABELS = { html: "HTML", footnote: "Footnote", ref: "Link references", shortcode: "Shortcode", video: "Video" };

/* ---------------------------------------------------------------- videos --
A YouTube or Vimeo link alone on a top-level line (bare, as <URL> or as
[video url=… start=… title=…]) is a video (internal/content/video.go, which
these functions mirror). The editor keeps the line verbatim as a "video" raw
block and shows the same click-to-load player as the site. */
const YT_ID = /^[A-Za-z0-9_-]{11}$/, YT_LIST = /^[A-Za-z0-9_-]{10,64}$/;
const VM_ID = /^[1-9][0-9]{0,11}$/, VM_HASH = /^[0-9a-f]{6,20}$/, VM_NAME = /^[A-Za-z0-9_-]{1,64}$/;
const START_DURATION = /^(?:(\d{1,2})h)?(?:(\d{1,4})m)?(?:(\d{1,6})s?)?$/, START_CLOCK = /^(?:(\d{1,2}):)?(\d{1,3}):(\d{2})$/;
const MAX_START = 99 * 3600 + 59 * 60 + 59;
const reVideoTag = /^\[([a-z][a-z0-9_-]{0,39})((?:\s+[a-z][a-z0-9_-]*(?:=(?:"[^"\]\n]*"|'[^'\]\n]*'|[^\s\]"']+))?)*)\s*(\/)?\]$/;
const reVideoAttr = /([a-z][a-z0-9_-]{0,39})(?:=(?:"([^"]*)"|'([^']*)'|([^\s"']+)))?/g;

// parseStart reads a start offset: 90, 90s, 1m30s, 1h2m3s, 1:30 or 1:02:03.
export function parseStart(s) {
  s = String(s ?? "").trim().toLowerCase();
  if (!s) return 0;
  let n = 0, m;
  if ((m = START_CLOCK.exec(s))) {
    if (+m[3] > 59) return 0;
    n = (+m[1] || 0) * 3600 + +m[2] * 60 + +m[3];
  } else if ((m = START_DURATION.exec(s))) n = (+m[1] || 0) * 3600 + (+m[2] || 0) * 60 + (+m[3] || 0);
  return n > 0 && n <= MAX_START ? n : 0;
}
const startOf = (q, frag, keys) => {
  for (const k of keys) { const n = parseStart(q.get(k)); if (n) return n; }
  for (const part of frag.split("&")) if (part.startsWith("t=")) return parseStart(part.slice(2));
  return 0;
};
// parseVideoURL recognises a YouTube video, Short, live stream or playlist,
// or a Vimeo video, on the providers' own hosts only. It returns
// {provider, id, list, hash, start, portrait} or null.
export function parseVideoURL(raw) {
  raw = String(raw ?? "").trim();
  if (!raw || raw.length > 2048) return null;
  const m = /^(https?):\/\/([^/?#]*)([^?#]*)(?:\?([^#]*))?(?:#(.*))?$/i.exec(raw);
  if (!m || /[@:\s]/.test(m[2])) return null; // credentials, a port
  let segs, frag;
  try {
    segs = decodeURIComponent(m[3]).split("/").filter(Boolean);
    frag = decodeURIComponent(m[5] || "");
  } catch (_) { return null; }
  const q = new URLSearchParams(m[4] || "");
  const host = m[2].toLowerCase();
  const v = { provider: "youtube", id: "", list: "", hash: "", start: 0, portrait: false };
  if (["youtube.com", "www.youtube.com", "m.youtube.com", "music.youtube.com", "youtube-nocookie.com", "www.youtube-nocookie.com"].includes(host)) {
    const list = q.get("list") || "";
    if ((segs.length === 1 && segs[0] === "playlist") || (segs.length === 2 && segs[0] === "embed" && segs[1] === "videoseries")) {
      return YT_LIST.test(list) ? { ...v, list } : null;
    }
    if (segs.length === 1 && segs[0] === "watch") v.id = q.get("v") || "";
    else if (segs.length === 2 && ["embed", "live", "v"].includes(segs[0])) v.id = segs[1];
    else if (segs.length === 2 && segs[0] === "shorts") { v.id = segs[1]; v.portrait = true; }
    else return null;
    v.start = startOf(q, frag, ["t", "start"]);
    return YT_ID.test(v.id) ? v : null;
  }
  if (host === "youtu.be") {
    if (segs.length !== 1 || !YT_ID.test(segs[0])) return null;
    return { ...v, id: segs[0], start: startOf(q, frag, ["t", "start"]) };
  }
  if (host === "vimeo.com" || host === "www.vimeo.com" || host === "player.vimeo.com") {
    v.provider = "vimeo";
    v.hash = q.get("h") || "";
    if (host === "player.vimeo.com") {
      if (segs.length !== 2 || segs[0] !== "video") return null;
      v.id = segs[1];
    } else if (segs.length === 1) v.id = segs[0];
    else if (segs.length === 2 && VM_ID.test(segs[0])) { v.id = segs[0]; v.hash = segs[1]; }
    else if (segs.length === 3 && segs[0] === "channels" && VM_NAME.test(segs[1])) v.id = segs[2];
    else if (segs.length === 4 && segs[0] === "groups" && VM_NAME.test(segs[1]) && segs[2] === "videos") v.id = segs[3];
    else return null;
    if (!VM_ID.test(v.id) || (v.hash && !VM_HASH.test(v.hash))) return null;
    const fs = startOf(new URLSearchParams(), frag, []);
    v.start = fs || parseStart(q.get("t"));
    return v;
  }
  return null;
}
// videoLine reads a line that names a video: a bare URL, <URL> or the
// [video] shortcode. It returns the video plus {url, title, shortcode}, or null.
export function videoLine(line) {
  line = String(line ?? "").trim();
  if (line.startsWith("[video") && line.endsWith("]")) {
    const m = reVideoTag.exec(line);
    if (!m || m[1] !== "video") return null;
    const attrs = {};
    for (const a of m[2].matchAll(reVideoAttr)) attrs[a[1]] = a[2] || a[3] || a[4] || "true";
    const v = parseVideoURL(attrs.url);
    if (!v) return null;
    if ("start" in attrs) v.start = parseStart(attrs.start);
    return { ...v, url: attrs.url.trim(), title: String(attrs.title || "").trim().replace(/\s+/g, " ").slice(0, 200), shortcode: true };
  }
  const url = line.startsWith("<") && line.endsWith(">") ? line.slice(1, -1) : line;
  if (/[ \t<>"']/.test(url)) return null;
  const v = parseVideoURL(url);
  return v ? { ...v, url, title: "", shortcode: false } : null;
}
// videoPlayerURL is the player loaded on click (content.Video.PlayerURL).
export function videoPlayerURL(v) {
  if (v.provider === "vimeo") return "https://player.vimeo.com/video/" + v.id + "?autoplay=1&dnt=1" + (v.hash ? "&h=" + v.hash : "") + (v.start ? "#t=" + v.start + "s" : "");
  if (v.list) return "https://www.youtube-nocookie.com/embed/videoseries?list=" + v.list + "&autoplay=1";
  return "https://www.youtube-nocookie.com/embed/" + v.id + "?autoplay=1" + (v.start ? "&start=" + v.start : "");
}
// checkVideo validates what was typed in the video popover.
export function checkVideo(value) {
  const line = String(value ?? "").trim();
  if (!line) return { ok: false, empty: true };
  const v = videoLine(line);
  return v ? { ok: true, line: v.shortcode ? line : v.url, video: v } : { ok: false };
}
// The player's texts, in the page language (setupEditor fills them in), and
// what /admin/api/video said about each URL: {title, poster, width, height,
// portrait, mode}.
export const VIDEO_TEXT = { watch: "Watch on %s", play: "Play video: %s", untitled: "%s video", playUntitled: "Play %s", off: "" };
export const VIDEO_META = new Map();
const fmt = (s, ...args) => { let k = 0; return String(s).replace(/%[sd]/g, () => String(args[k++] ?? "")); };
// videoFacadeHTML is the player as content.videoHTML renders it, with a
// button (the editor plays it itself).
export function videoFacadeHTML(v, meta = {}) {
  const provider = v.provider === "vimeo" ? "Vimeo" : "YouTube";
  const title = v.title || meta.title || "";
  const shown = title || fmt(VIDEO_TEXT.untitled, provider);
  const frameTitle = title ? title + " (" + provider + ")" : shown;
  const label = title ? fmt(VIDEO_TEXT.play, frameTitle) : fmt(VIDEO_TEXT.playUntitled, shown);
  const size = meta.width > 0 && meta.height > 0 && meta.width <= 4096 && meta.height <= 4096 ? ` width="${meta.width}" height="${meta.height}"` : "";
  const poster = /^\/video-thumbs\/[a-z]{2,4}-[A-Za-z0-9_-]{1,64}\.jpg$/.test(meta.poster || "") ? `<img class="sap-video-poster" src="${meta.poster}" alt=""${size}>` : "";
  return `<figure class="sap-video sap-video-${v.provider}${v.portrait || meta.portrait ? " sap-video-portrait" : ""}" data-video-provider="${v.provider}">` +
    `<button type="button" class="sap-video-link" tabindex="-1" data-video-src="${escHTML(videoPlayerURL(v))}" data-video-title="${escHTML(frameTitle)}" aria-label="${escHTML(label)}">` +
    poster + `<span class="sap-video-play" aria-hidden="true"></span><span class="sap-video-caption"><span class="sap-video-title">${escHTML(shown)}</span>` +
    `<span class="sap-video-source">${escHTML(fmt(VIDEO_TEXT.watch, provider))}</span></span></button></figure>`;
}
function videoBlockHTML(text) {
  const v = videoLine(text);
  if (!v) return null;
  const meta = VIDEO_META.get(v.url);
  const note = meta && meta.mode === "off" && VIDEO_TEXT.off ? `<p class="md-video-note">${escHTML(VIDEO_TEXT.off)}</p>` : "";
  return `<div class="md-raw md-video" contenteditable="false" data-raw="${escHTML(text)}" data-kind="video" data-label="${escHTML(LABELS.video)}"${meta ? " data-video-ready" : ""}>` +
    videoFacadeHTML(v, meta || {}) + note + "</div>";
}

/* ----------------------------------------------------------- image layout --
![alt](src){width=480 align=left}: the width in pixels and how the image
sits in the text (content.ImageAttrs). */
export const IMAGE_ALIGNS = ["left", "right", "center", "full"];
export const MAX_IMAGE_WIDTH = 4096;
const reImageAttrs = /^\{([a-z]{1,10}=[a-z0-9]{1,10}(?: [a-z]{1,10}=[a-z0-9]{1,10}){0,3})\}/;
// parseImageAttrs reads the inside of {width=480 align=left}: null when
// anything in it is unknown, out of range or repeated.
export function parseImageAttrs(s) {
  const out = {}, seen = new Set();
  for (const part of String(s).split(" ")) {
    const [k, v] = part.split("=");
    if (seen.has(k)) return null;
    seen.add(k);
    if (k === "width") {
      const n = Number(v);
      if (!Number.isInteger(n) || n < 1 || n > MAX_IMAGE_WIDTH || String(n) !== v) return null;
      out.width = n;
    } else if (k === "align" && IMAGE_ALIGNS.includes(v)) out.align = v;
    else return null;
  }
  return seen.size ? out : null;
}
export function imageAttrsMd(run) {
  const p = [];
  if (run.width) p.push("width=" + run.width);
  if (run.align) p.push("align=" + run.align);
  return p.length ? "{" + p.join(" ") + "}" : "";
}
// imageOp applies an image bar action to an image run: an alignment (the
// active one again removes it), "original" (no width) or "width".
export function imageOp(run, op, value) {
  const r = { ...run };
  switch (op) {
    case "left": case "right": case "center":
      if (r.align === op) delete r.align; else r.align = op;
      break;
    case "full":
      if (r.align === "full") delete r.align; else { r.align = "full"; delete r.width; }
      break;
    case "original":
      delete r.width;
      if (r.align === "full") delete r.align;
      break;
    case "width": {
      const n = Math.round(Number(value));
      if (!(n >= 1 && n <= MAX_IMAGE_WIDTH)) return r;
      r.width = n;
      if (r.align === "full") delete r.align;
      break;
    }
  }
  return r;
}
// snapWidth rounds a width to tenths of the column, from 10% to 100%.
export function snapWidth(px, column) {
  const step = column / 10;
  if (!(step > 0)) return Math.max(1, Math.min(MAX_IMAGE_WIDTH, Math.round(px)));
  const n = Math.max(1, Math.min(10, Math.round(px / step)));
  return Math.max(1, Math.min(MAX_IMAGE_WIDTH, Math.round(n * step)));
}
// resizeWidth is the width a corner drag asks for, keeping the image's
// ratio (height / width): the larger of the horizontal and the vertical
// movement wins.
export function resizeWidth(startW, dx, dy, corner, ratio) {
  const byX = startW + (/e$/.test(corner) ? dx : -dx);
  const byY = ratio > 0 ? startW + (/^s/.test(corner) ? dy : -dy) / ratio : byX;
  return Math.abs(byX - startW) >= Math.abs(byY - startW) ? byX : byY;
}
// stepWidth is the width delta tenths of the column away (arrow keys).
export function stepWidth(cur, column, delta) {
  const step = column / 10;
  if (!(step > 0)) return snapWidth(cur, column);
  return snapWidth(Math.max(1, Math.min(10, Math.round(cur / step) + delta)) * step, column);
}
// imageHeight is the height of an image shown w wide (as the server rounds).
export const imageHeight = (w, natW, natH) => (natW > 0 && natH > 0 ? Math.floor((w * natH + Math.floor(natW / 2)) / natW) : 0);

/* ------------------------------------------------------------------ runs -- */
const sameLink = (a, b) => (!a && !b) || (!!a && !!b && a.href === b.href && (a.title || "") === (b.title || ""));
const sameMarks = (a = {}, b = {}) => !!a.s === !!b.s && !!a.e === !!b.e && !!a.d === !!b.d && !!a.c === !!b.c && sameLink(a.l, b.l);
const sameX = (a = {}, b = {}) => sameMarks(a, b) && (a.sz || 0) === (b.sz || 0) && !!a.mono === !!b.mono && !!a.pre === !!b.pre;
function cleanMarks(m = {}) {
  const o = {};
  if (m.s) o.s = true;
  if (m.e) o.e = true;
  if (m.d) o.d = true;
  if (m.c) o.c = true;
  if (m.l) o.l = m.l.auto ? { href: m.l.href, title: m.l.title || "", auto: true } : { href: m.l.href, title: m.l.title || "" };
  return o;
}
function pushRun(runs, r, eq = sameMarks) {
  if (r.t !== undefined) {
    if (r.t === "") return;
    const last = runs[runs.length - 1];
    if (last && last.t !== undefined && eq(last.m, r.m)) { last.t += r.t; return; }
    runs.push({ t: r.t, m: r.m || {} });
    return;
  }
  runs.push(r);
}
const runsText = (runs) => runs.map((r) => (r.t !== undefined ? r.t : r.a === "br" ? "\n" : r.a === "raw" ? r.v : "")).join("");
const hasContent = (runs) => runs.some((r) => r.a || (r.t !== undefined && stripInvisible(r.t).trim() !== ""));

/* ========================================================= Markdown parse == */
const reATX = /^ {0,3}(#{1,6})(?=[ \t]|$)(.*)$/;
const reHR = /^ {0,3}(?:(?:\*[ \t]*){3,}|(?:-[ \t]*){3,}|(?:_[ \t]*){3,})$/;
const reFence = /^( {0,3})(`{3,}|~{3,})(.*)$/;
const reQuote = /^ {0,3}>/;
const reSetext = /^ {0,3}(=+|-+)[ \t]*$/;
const reHTML1 = /^ {0,3}<(?:script|pre|style|textarea)(?=[\s>]|$)/i;
const reHTML6 = /^ {0,3}<\/?(?:address|article|aside|base|basefont|blockquote|body|caption|center|col|colgroup|dd|details|dialog|dir|div|dl|dt|fieldset|figcaption|figure|footer|form|frame|frameset|h[1-6]|head|header|hr|html|iframe|legend|li|link|main|menu|menuitem|nav|noframes|ol|optgroup|option|p|param|search|section|summary|table|tbody|td|tfoot|th|thead|title|tr|track|ul)(?=[\s/>]|$)/i;
const reHTML7 = /^ {0,3}(?:<[A-Za-z][A-Za-z0-9-]*(?:\s+[A-Za-z_:][\w.:-]*(?:\s*=\s*(?:[^\s"'=<>`]+|'[^']*'|"[^"]*"))?)*\s*\/?>|<\/[A-Za-z][A-Za-z0-9-]*\s*>)[ \t]*$/;
const reFootDef = /^ {0,3}\[\^[^\]\s]+\]:/;
const reLinkDef = /^ {0,3}\[((?:[^\]\\]|\\.)+)\]:[ \t]*(?:<[^>\n]*>|\S+)(?:[ \t]+(?:"[^"]*"|'[^']*'|\([^)]*\)))?[ \t]*$/;
const reTableDelim = /^ {0,3}\|?[ \t]*:?-+:?[ \t]*(?:\|[ \t]*:?-+:?[ \t]*)*\|?[ \t]*$/;
const reShortcode = /^\[\/?[a-z][a-z0-9_-]{0,39}(?:\s+[a-z][a-z0-9_-]*(?:=(?:"[^"\]\n]*"|'[^'\]\n]*'|[^\s\]"']+))?)*\s*\/?\]$/;
const reInlineHTML = /^(?:<[A-Za-z][A-Za-z0-9-]*(?:\s+[A-Za-z_:][A-Za-z0-9_.:-]*(?:\s*=\s*(?:[^\s"'=<>`]+|'[^']*'|"[^"]*"))?)*\s*\/?>|<\/[A-Za-z][A-Za-z0-9-]*\s*>|<!--(?!>|->)[\s\S]*?-->|<\?[\s\S]*?\?>|<![A-Za-z][^>]*>|<!\[CDATA\[[\s\S]*?\]\]>)/;

function indentOf(line) {
  let col = 0;
  for (const ch of line) {
    if (ch === " ") col++;
    else if (ch === "\t") col += 4 - (col % 4);
    else break;
  }
  return col;
}
// Removes n columns of leading whitespace (tabs count to the next stop of 4).
function stripIndent(line, n) {
  let col = 0, i = 0;
  while (i < line.length && col < n) {
    const ch = line[i];
    if (ch === " ") { col++; i++; }
    else if (ch === "\t") {
      const w = 4 - (col % 4);
      if (col + w > n) return " ".repeat(col + w - n) + line.slice(i + 1);
      col += w; i++;
    } else break;
  }
  return line.slice(i);
}
function htmlStart(line, inPara) {
  if (!/^ {0,3}</.test(line)) return 0;
  const t = line.replace(/^ {0,3}/, "");
  if (reHTML1.test(line)) return 1;
  if (t.startsWith("<!--")) return 2;
  if (t.startsWith("<?")) return 3;
  if (t.startsWith("<![CDATA[")) return 5;
  if (/^<![A-Za-z]/.test(t)) return 4;
  if (reHTML6.test(line)) return 6;
  if (!inPara && reHTML7.test(line)) return 7;
  return 0;
}
function htmlEnds(type, line) {
  switch (type) {
    case 1: return /<\/(?:script|pre|style|textarea)>/i.test(line);
    case 2: return line.includes("-->");
    case 3: return line.includes("?>");
    case 4: return line.includes(">");
    case 5: return line.includes("]]>");
  }
  return false;
}
function fenceOpen(line) {
  const m = reFence.exec(line);
  if (!m || (m[2][0] === "`" && m[3].includes("`"))) return null;
  return m;
}
function listMarker(line) {
  let m = /^( {0,3})([-+*])(?=[ \t]|$)/.exec(line), ordered = false, num = 0, ch, mlen;
  if (m) { ch = m[2]; mlen = 1; }
  else {
    m = /^( {0,3})(\d{1,9})([.)])(?=[ \t]|$)/.exec(line);
    if (!m) return null;
    ordered = true; num = parseInt(m[2], 10); ch = m[3]; mlen = m[2].length + 1;
  }
  const ind = m[1].length, after = line.slice(ind + mlen);
  if (isBlank(after)) return { ordered, num, ch, width: ind + mlen + 1, first: "", empty: true };
  const sp = indentOf(after);
  if (sp >= 5) return { ordered, num, ch, width: ind + mlen + 1, first: stripIndent(after, 1), empty: false };
  return { ordered, num, ch, width: ind + mlen + sp, first: stripIndent(after, sp), empty: false };
}
// Can this line interrupt a paragraph (or end a lazy continuation)?
function interrupts(line) {
  if (isBlank(line)) return true;
  if (indentOf(line) >= 4) return false;
  if (reATX.test(line) || reHR.test(line) || reQuote.test(line) || fenceOpen(line)) return true;
  const h = htmlStart(line, true);
  if (h && h !== 7) return true;
  const lm = listMarker(line);
  return !!(lm && !lm.empty && (!lm.ordered || lm.num === 1));
}
function lazyOK(buf) {
  let fence = false;
  for (const l of buf) if (fenceOpen(l) || (fence && /^ {0,3}(`{3,}|~{3,})[ \t]*$/.test(l))) fence = !fence;
  const last = buf[buf.length - 1];
  return !fence && last !== undefined && !isBlank(last) && !reATX.test(last) && !reHR.test(last) && indentOf(last) < 4;
}
function splitRow(line) {
  let s = line.trim();
  if (s.startsWith("|")) s = s.slice(1);
  if (s.endsWith("|") && !s.endsWith("\\|")) s = s.slice(0, -1);
  const cells = [];
  let cur = "";
  for (let k = 0; k < s.length; k++) {
    const c = s[k];
    if (c === "\\" && s[k + 1] === "|") { cur += "|"; k++; continue; }
    if (c === "|") { cells.push(cur.trim()); cur = ""; continue; }
    cur += c;
  }
  cells.push(cur.trim());
  return cells;
}

function parseBlocks(lines, ctx, top) {
  const out = [];
  const n = lines.length;
  let i = 0;
  while (i < n) {
    const line = lines[i];
    if (isBlank(line)) { i++; continue; }
    const start = i;
    let b = null, m;
    if (indentOf(line) >= 4) {
      const buf = [];
      let j = i;
      while (j < n && (isBlank(lines[j]) || indentOf(lines[j]) >= 4)) { buf.push(stripIndent(lines[j], 4)); j++; }
      while (buf.length && isBlank(buf[buf.length - 1])) { buf.pop(); j--; }
      b = { type: "code", lang: "", text: buf.join("\n") };
      i = j;
    } else if ((m = fenceOpen(line))) {
      const fi = m[1].length, fch = m[2][0], flen = m[2].length;
      const info = m[3].trim().replace(/\\([!-/:-@[-`{-~])/g, "$1");
      const buf = [];
      let j = i + 1;
      while (j < n) {
        const cm = /^ {0,3}(`{3,}|~{3,})[ \t]*$/.exec(lines[j]);
        if (cm && cm[1][0] === fch && cm[1].length >= flen) { j++; break; }
        buf.push(stripIndent(lines[j], fi));
        j++;
      }
      b = { type: "code", lang: info.split(/\s+/)[0] || "", text: buf.join("\n") };
      i = j;
    } else if ((m = reATX.exec(line))) {
      let text = m[2].replace(/^[ \t]+/, "").replace(/[ \t]+$/, "");
      text = text.replace(/(^|[ \t]+)#+$/, "").trim();
      b = { type: "h", level: m[1].length, text };
      i++;
    } else if (reHR.test(line)) {
      b = { type: "hr" };
      i++;
    } else if (reQuote.test(line)) {
      const buf = [];
      let j = i;
      while (j < n) {
        const l = lines[j];
        if (reQuote.test(l)) {
          let c = l.replace(/^ {0,3}>/, "");
          if (c.startsWith(" ")) c = c.slice(1);
          else if (c.startsWith("\t")) c = "  " + c.slice(1);
          buf.push(c);
          j++;
        } else if (!isBlank(l) && lazyOK(buf) && !interrupts(l)) { buf.push(l); j++; }
        else break;
      }
      b = { type: "quote", blocks: parseBlocks(buf, ctx) };
      i = j;
    } else if ((m = htmlStart(line, false))) {
      let j = i;
      if (m <= 5) {
        while (j < n) { const end = htmlEnds(m, lines[j]); j++; if (end) break; }
      } else {
        while (j < n && !isBlank(lines[j])) j++;
      }
      b = { type: "raw", kind: "html", text: lines.slice(i, j).join("\n") };
      i = j;
    } else if (reFootDef.test(line)) {
      let j = i + 1;
      while (j < n) {
        const l = lines[j];
        if (!isBlank(l) && indentOf(l) >= 4) { j++; continue; }
        if (isBlank(l) && j + 1 < n && indentOf(lines[j + 1]) >= 4 && !isBlank(lines[j + 1])) { j++; continue; }
        if (!isBlank(l) && !interrupts(l) && !reFootDef.test(l) && !reLinkDef.test(l) && !isBlank(lines[j - 1])) { j++; continue; }
        break;
      }
      b = { type: "raw", kind: "footnote", text: lines.slice(i, j).join("\n") };
      i = j;
    } else if (reLinkDef.test(line)) {
      let j = i;
      while (j < n && reLinkDef.test(lines[j])) { ctx.defs.add(normLabel(reLinkDef.exec(lines[j])[1])); j++; }
      b = { type: "raw", kind: "ref", text: lines.slice(i, j).join("\n") };
      i = j;
    } else if (listMarker(line)) {
      const first = listMarker(line);
      const items = [];
      let j = i, loose = false, prevBlank = false, end = i;
      while (j < n) {
        const mk = listMarker(lines[j]);
        if (!mk || mk.ordered !== first.ordered || mk.ch !== first.ch || reHR.test(lines[j])) break;
        if (items.length && prevBlank) loose = true;
        const W = mk.width;
        const buf = [mk.first];
        let k = j + 1;
        while (k < n) {
          const l = lines[k];
          if (isBlank(l)) {
            if (mk.empty && buf.length === 1) break;
            buf.push("");
            k++;
            continue;
          }
          if (indentOf(l) >= W) { buf.push(stripIndent(l, W)); k++; continue; }
          if (!isBlank(buf[buf.length - 1]) && lazyOK(buf) && !interrupts(l) && !listMarker(l)) { buf.push(l.replace(/^[ \t]+/, "")); k++; continue; }
          break;
        }
        let tb = 0;
        while (buf.length > 1 && isBlank(buf[buf.length - 1])) { buf.pop(); tb++; }
        prevBlank = tb > 0;
        const blocks = parseBlocks(buf, ctx);
        for (let q = 1; q < blocks.length; q++) {
          for (let x = blocks[q - 1].l1; x < blocks[q].l0; x++) if (isBlank(buf[x])) loose = true;
        }
        let checked = null;
        if (blocks[0] && blocks[0].type === "p") {
          const tm = /^\[([ xX])\][ \t]+/.exec(blocks[0].text);
          if (tm) { checked = tm[1] !== " "; blocks[0].text = blocks[0].text.slice(tm[0].length); }
        }
        items.push({ checked, blocks });
        end = k - tb;
        j = k;
      }
      b = { type: "list", ordered: first.ordered, start: first.ordered ? first.num : 1, loose, items };
      i = end;
    } else if (line.includes("|") && i + 1 < n && reTableDelim.test(lines[i + 1]) && lines[i + 1].includes("-") &&
      splitRow(line).length === splitRow(lines[i + 1]).length) {
      const head = splitRow(line);
      const align = splitRow(lines[i + 1]).map((c) => (/^:-+:$/.test(c) ? "center" : /^-+:$/.test(c) ? "right" : /^:-+$/.test(c) ? "left" : null));
      const rows = [head];
      let j = i + 2;
      while (j < n && !isBlank(lines[j]) && !interrupts(lines[j])) {
        const cells = splitRow(lines[j]).slice(0, head.length);
        while (cells.length < head.length) cells.push("");
        rows.push(cells);
        j++;
      }
      b = { type: "table", align, rows };
      i = j;
    } else {
      const buf = [line];
      let j = i + 1, level = 0;
      while (j < n) {
        const l = lines[j];
        if (isBlank(l)) break;
        const st = reSetext.exec(l);
        if (st && indentOf(l) < 4) { level = st[1][0] === "=" ? 1 : 2; j++; break; }
        if (interrupts(l)) break;
        buf.push(l);
        j++;
      }
      const text = buf.map((l) => l.replace(/^[ \t]+/, "")).join("\n").replace(/[ \t]+$/, "");
      // A video link alone in a top-level paragraph is a player.
      if (top && !level && buf.length === 1 && videoLine(text)) b = { type: "raw", kind: "video", text };
      else b = level ? { type: "h", level, text } : { type: "p", text };
      i = j;
    }
    b.l0 = start;
    b.l1 = Math.max(i, start + 1);
    out.push(b);
  }
  return out;
}

function resolve(blocks, ctx) {
  for (const b of blocks) {
    if (b.type === "p" || b.type === "h") { b.runs = parseInline(b.text, ctx); delete b.text; }
    else if (b.type === "quote") resolve(b.blocks, ctx);
    else if (b.type === "list") for (const it of b.items) resolve(it.blocks, ctx);
    else if (b.type === "table") b.rows = b.rows.map((r) => r.map((c) => parseInline(c, ctx)));
    delete b.l0; delete b.l1;
  }
}

// parseMarkdown returns the block tree. Top-level blocks also carry `src`
// (their exact source) and `o0`/`o1` (UTF-16 offsets into the input).
export function parseMarkdown(src) {
  src = String(src ?? "").replace(/\r\n?/g, "\n").replace(/\u0000/g, "\uFFFD");
  const lines = src.split("\n");
  const ctx = { defs: new Set() };
  const blocks = parseBlocks(lines, ctx, true);
  const starts = [];
  let o = 0;
  for (const l of lines) { starts.push(o); o += l.length + 1; }
  const meta = blocks.map((b) => ({ src: lines.slice(b.l0, b.l1).join("\n"), o0: starts[b.l0], o1: starts[b.l1 - 1] + lines[b.l1 - 1].length }));
  resolve(blocks, ctx);
  blocks.forEach((b, k) => Object.assign(b, meta[k]));
  return blocks;
}

/* --------------------------------------------------------- inline parse -- */
const ENT = {
  amp: "&", lt: "<", gt: ">", quot: '"', apos: "'", copy: "©", reg: "®", trade: "™", hellip: "…", mdash: "—", ndash: "–",
  lsquo: "‘", rsquo: "’", ldquo: "“", rdquo: "”", laquo: "«", raquo: "»", bull: "•", middot: "·", deg: "°", plusmn: "±",
  times: "×", divide: "÷", euro: "€", pound: "£", yen: "¥", cent: "¢", sect: "§", para: "¶", iexcl: "¡", iquest: "¿",
  aacute: "á", eacute: "é", iacute: "í", oacute: "ó", uacute: "ú", ntilde: "ñ", Aacute: "Á", Eacute: "É", Iacute: "Í",
  Oacute: "Ó", Uacute: "Ú", Ntilde: "Ñ", uuml: "ü", Uuml: "Ü", auml: "ä", ouml: "ö", Auml: "Ä", Ouml: "Ö", szlig: "ß",
  ccedil: "ç", Ccedil: "Ç", agrave: "à", egrave: "è", ograve: "ò", rarr: "→", larr: "←", hearts: "♥", check: "✓",
};
const decodeEntities = (s) => s.replace(/&(?:#[xX]([0-9a-fA-F]{1,6})|#([0-9]{1,7})|([A-Za-z][A-Za-z0-9]{1,31}));/g, (all, h, d, name) => {
  if (h || d) { const cp = parseInt(h || d, h ? 16 : 10); return cp && cp <= 0x10ffff ? String.fromCodePoint(cp) : "\uFFFD"; }
  return ENT[name] ?? all;
});
const unescapeMd = (s) => s.replace(/\\([!-/:-@[-`{-~])/g, "$1");

function parseLinkTail(s, p) {
  if (s[p] !== "(") return null;
  let i = p + 1;
  const skipWS = () => { while (i < s.length && /[ \t\n]/.test(s[i])) i++; };
  skipWS();
  let href = "";
  if (s[i] === "<") {
    let j = i + 1;
    while (j < s.length && s[j] !== ">" && s[j] !== "\n" && s[j] !== "<") { if (s[j] === "\\") j++; j++; }
    if (s[j] !== ">") return null;
    href = s.slice(i + 1, j);
    i = j + 1;
  } else {
    let depth = 0;
    const st = i;
    while (i < s.length) {
      const c = s[i];
      if (c === "\\" && ASCII_PUNCT.test(s[i + 1] || "")) { i += 2; continue; }
      if (c === "(") depth++;
      else if (c === ")") { if (depth === 0) break; depth--; }
      else if (/\s/.test(c) || c < " ") break;
      i++;
    }
    if (depth) return null;
    href = s.slice(st, i);
  }
  const beforeTitle = i;
  skipWS();
  let title = "";
  if (i > beforeTitle && (s[i] === '"' || s[i] === "'" || s[i] === "(")) {
    const close = s[i] === "(" ? ")" : s[i];
    let j = i + 1;
    while (j < s.length && s[j] !== close) { if (s[j] === "\\") j++; j++; }
    if (j >= s.length) return null;
    title = s.slice(i + 1, j);
    i = j + 1;
    skipWS();
  }
  if (s[i] !== ")") return null;
  return { href: unescapeMd(decodeEntities(href)), title: unescapeMd(decodeEntities(title)), end: i + 1 };
}

function processEmphasis(delims, bottom) {
  const list = delims.slice(bottom);
  let ci = 0;
  while (ci < list.length) {
    const closer = list[ci];
    if (!closer.close || closer.n === 0) { ci++; continue; }
    let found = -1;
    for (let oi = ci - 1; oi >= 0; oi--) {
      const op = list[oi];
      if (op.ch !== closer.ch || !op.open || op.n === 0) continue;
      if (closer.ch === "~") { if (op.n !== closer.n) continue; }
      else if ((op.close || closer.open) && (op.orig + closer.orig) % 3 === 0 && !(op.orig % 3 === 0 && closer.orig % 3 === 0)) continue;
      found = oi;
      break;
    }
    if (found < 0) { ci++; continue; }
    const op = list[found];
    const use = closer.ch === "~" ? closer.n : op.n >= 2 && closer.n >= 2 ? 2 : 1;
    const mk = { k: closer.ch === "~" ? "d" : use === 2 ? "s" : "e" };
    op.after.unshift(mk);
    closer.before.push(mk);
    op.n -= use;
    closer.n -= use;
    list.splice(found + 1, ci - found - 1);
    ci = found + 1;
    if (op.n === 0) { list.splice(found, 1); ci--; }
    if (closer.n === 0) list.splice(ci, 1);
  }
  delims.length = bottom;
}

function parseInline(src, ctx) {
  const nodes = [], delims = [], brackets = [];
  let text = "", i = 0;
  const flushText = () => { if (text) { nodes.push({ k: "text", v: text }); text = ""; } };
  const n = src.length;
  while (i < n) {
    const c = src[i];
    if (c === "\\") {
      const nx = src[i + 1];
      if (nx === "\n") { flushText(); nodes.push({ k: "br" }); i += 2; while (src[i] === " " || src[i] === "\t") i++; continue; }
      if (nx !== undefined && ASCII_PUNCT.test(nx)) { text += nx; i += 2; continue; }
      text += c; i++;
      continue;
    }
    if (c === "`") {
      let run = 1;
      while (src[i + run] === "`") run++;
      let j = i + run, close = -1;
      while (j < n) {
        const k = src.indexOf("`", j);
        if (k < 0) break;
        let r = 1;
        while (src[k + r] === "`") r++;
        if (r === run) { close = k; break; }
        j = k + r;
      }
      if (close >= 0) {
        let v = src.slice(i + run, close).replace(/\n/g, " ");
        if (v.length >= 2 && v[0] === " " && v[v.length - 1] === " " && /[^ ]/.test(v)) v = v.slice(1, -1);
        flushText();
        nodes.push({ k: "code", v });
        i = close + run;
        continue;
      }
      text += "`".repeat(run); i += run;
      continue;
    }
    if (c === "<") {
      const rest = src.slice(i, i + 4000);
      let m = /^<([A-Za-z][A-Za-z0-9+.-]{1,31}:[^\s<>]*)>/.exec(rest);
      if (m) { flushText(); nodes.push({ k: "auto", href: m[1], v: m[1] }); i += m[0].length; continue; }
      m = /^<([A-Za-z0-9.!#$%&'*+/=?^_`{|}~-]+@[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*)>/.exec(rest);
      if (m) { flushText(); nodes.push({ k: "auto", href: "mailto:" + m[1], v: m[1] }); i += m[0].length; continue; }
      m = reInlineHTML.exec(rest);
      if (m) { flushText(); nodes.push({ k: "raw", v: m[0] }); i += m[0].length; continue; }
      text += c; i++;
      continue;
    }
    if (c === "&") {
      const m = /^&(?:#[xX]([0-9a-fA-F]{1,6})|#([0-9]{1,7})|([A-Za-z][A-Za-z0-9]{1,31}));/.exec(src.slice(i, i + 40));
      if (m) {
        let ch = null;
        if (m[1] || m[2]) { const cp = parseInt(m[1] || m[2], m[1] ? 16 : 10); ch = cp && cp <= 0x10ffff ? String.fromCodePoint(cp) : "\uFFFD"; }
        else if (ENT[m[3]] !== undefined) ch = ENT[m[3]];
        if (ch !== null && !/[\u00A0\u200B\uE000\uE001\s]/.test(ch)) { text += ch; i += m[0].length; continue; }
        flushText();
        nodes.push({ k: "raw", v: m[0] });
        i += m[0].length;
        continue;
      }
      text += c; i++;
      continue;
    }
    if (c === "*" || c === "_" || c === "~") {
      let run = 1;
      while (src[i + run] === c) run++;
      if (c === "~" && run > 2) { text += c.repeat(run); i += run; continue; }
      const before = src[i - 1], after = src[i + run];
      const lf = !isWS(after) && (!isPunct(after) || isWS(before) || isPunct(before));
      const rf = !isWS(before) && (!isPunct(before) || isWS(after) || isPunct(after));
      let open = lf, close = rf;
      if (c === "_") { open = lf && (!rf || isPunct(before)); close = rf && (!lf || isPunct(after)); }
      flushText();
      const d = { k: "delim", ch: c, n: run, orig: run, open, close, before: [], after: [] };
      nodes.push(d);
      if (open || close) delims.push(d);
      i += run;
      continue;
    }
    if (c === "[" || (c === "!" && src[i + 1] === "[")) {
      flushText();
      const b = { k: "brk", img: c === "!", pos: i, active: true, dl: delims.length };
      nodes.push(b);
      brackets.push(b);
      i += c === "!" ? 2 : 1;
      continue;
    }
    if (c === "]") {
      flushText();
      const op = brackets.pop();
      if (!op) { text += "]"; i++; continue; }
      if (!op.active) { op.k = "text"; op.v = op.img ? "![" : "["; text += "]"; i++; continue; }
      const lk = parseLinkTail(src, i + 1);
      if (lk) {
        processEmphasis(delims, op.dl);
        const o = op.img ? { img: true, src: lk.href, title: lk.title } : { l: { href: lk.href, title: lk.title } };
        let end = lk.end;
        if (op.img) {
          // {width=480 align=left} right after the image.
          const am = reImageAttrs.exec(src.slice(end, end + 100));
          const at = am && parseImageAttrs(am[1]);
          if (at) { Object.assign(o, at); end += am[0].length; }
        }
        op.k = "open";
        op.o = o;
        nodes.push({ k: "close", o });
        if (!op.img) for (const b of brackets) if (!b.img) b.active = false;
        i = end;
        continue;
      }
      // Reference links, footnote references and shortcodes stay verbatim.
      const label = src.slice(op.pos + (op.img ? 2 : 1), i);
      let end = i + 1, raw = false;
      const refm = /^\[((?:[^\]\\]|\\.)*)\]/.exec(src.slice(i + 1, i + 1000));
      if (refm && ctx.defs.has(normLabel(refm[1] || label))) { end = i + 1 + refm[0].length; raw = true; }
      else if (ctx.defs.has(normLabel(label))) raw = true;
      else if (!op.img && /^\^[^\]\s]+$/.test(label)) raw = true;
      else if (!op.img && reShortcode.test("[" + label + "]")) raw = true;
      if (raw) {
        nodes.splice(nodes.indexOf(op));
        if (delims.length > op.dl) delims.length = op.dl;
        nodes.push({ k: "raw", v: src.slice(op.pos, end) });
        i = end;
        continue;
      }
      op.k = "text";
      op.v = op.img ? "![" : "[";
      text += "]";
      i++;
      continue;
    }
    if (c === "\n") {
      if (/ {2,}$/.test(text)) { text = text.replace(/ +$/, ""); flushText(); nodes.push({ k: "br" }); }
      else text = text.replace(/ +$/, "") + "\n";
      i++;
      while (src[i] === " " || src[i] === "\t") i++;
      continue;
    }
    text += c;
    i++;
  }
  flushText();
  processEmphasis(delims, 0);
  return flatten(nodes);
}

function flatten(nodes) {
  const runs = [], open = [];
  let img = null;
  const marks = () => {
    const m = {};
    for (const o of open) { if (o.k === "s") m.s = true; else if (o.k === "e") m.e = true; else if (o.k === "d") m.d = true; else if (o.l) m.l = o.l; }
    return m;
  };
  const textOut = (t) => { if (!t) return; if (img) img.alt += t; else pushRun(runs, { t, m: marks() }); };
  const openM = (o) => {
    if (o.img) { if (!img) img = { src: o.src, title: o.title, alt: "", o, m: marks() }; return; }
    open.push(o);
  };
  const closeM = (o) => {
    if (o.img) {
      if (img && img.o === o) {
        const run = { a: "img", src: img.src, alt: img.alt.replace(/\s+/g, " ").trim(), title: img.title, m: img.m };
        if (o.width) run.width = o.width;
        if (o.align) run.align = o.align;
        runs.push(run);
        img = null;
      }
      return;
    }
    const k = open.lastIndexOf(o);
    if (k >= 0) open.splice(k, 1);
  };
  for (const nd of nodes) {
    switch (nd.k) {
      case "text": textOut(nd.v); break;
      case "brk": textOut(nd.img ? "![" : "["); break;
      case "delim":
        for (const o of nd.before) closeM(o);
        if (nd.n) textOut(nd.ch.repeat(nd.n));
        for (const o of nd.after) openM(o);
        break;
      case "code": if (img) img.alt += nd.v; else pushRun(runs, { t: nd.v, m: { ...marks(), c: true } }); break;
      case "br": if (img) img.alt += " "; else runs.push({ a: "br", m: marks() }); break;
      case "raw": if (img) img.alt += nd.v; else runs.push({ a: "raw", v: nd.v, m: marks() }); break;
      case "auto": if (img) img.alt += nd.v; else pushRun(runs, { t: nd.v, m: { ...marks(), l: { href: nd.href, title: "", auto: true } } }); break;
      case "open": openM(nd.o); break;
      case "close": closeM(nd.o); break;
    }
  }
  return runs;
}

/* ================================================== Markdown serializer == */
// Runs -> properly nested inline tree: at each run pick the not-yet-open mark
// with the longest span, so `**a *b*** *c*` style overlaps nest minimally.
const KEYS = ["l", "s", "e", "d"];
const hasMark = (r, k) => (k === "l" ? !!(r.m && r.m.l) : !!(r.m && r.m[k]));
const sameMark = (a, b, k) => (k === "l" ? hasMark(a, k) && hasMark(b, k) && sameLink(a.m.l, b.m.l) : hasMark(a, k) && hasMark(b, k));
function nest(runs, from = 0, to = runs.length, open = {}) {
  const out = [];
  let i = from;
  while (i < to) {
    const r = runs[i];
    let best = null, bestLen = 0;
    for (const k of KEYS) {
      if (open[k] || !hasMark(r, k)) continue;
      let j = i + 1;
      while (j < to && sameMark(runs[j], r, k)) j++;
      if (j - i > bestLen) { best = k; bestLen = j - i; }
    }
    if (best) {
      const node = { k: best, kids: nest(runs, i, i + bestLen, { ...open, [best]: true }) };
      if (best === "l") node.l = r.m.l;
      out.push(node);
      i += bestLen;
      continue;
    }
    if (r.t !== undefined && r.m && r.m.c) {
      const last = out[out.length - 1];
      if (last && last.k === "c") last.v += r.t; else out.push({ k: "c", v: r.t });
    } else out.push(r);
    i++;
  }
  return out;
}

function escapeInline(s, o, tildes) {
  let r = "";
  for (let i = 0; i < s.length; i++) {
    const c = s[i], p = s[i - 1], nx = s[i + 1];
    switch (c) {
      case "\\": r += nx === undefined || ASCII_PUNCT.test(nx) ? "\\\\" : "\\"; break;
      case "*": case "`": r += "\\" + c; break;
      case "_": r += isAlnum(p) && isAlnum(nx) ? "_" : "\\_"; break;
      case "[": r += o.link ? "\\[" : "["; break;
      case "]": r += o.link || nx === "(" || nx === "[" || nx === ":" ? "\\]" : "]"; break;
      case "<": r += nx !== undefined && /[A-Za-z/!?]/.test(nx) ? "\\<" : "<"; break;
      case "&": r += /^&(?:#[xX]?[0-9a-fA-F]+|[A-Za-z][A-Za-z0-9]*);/.test(s.slice(i, i + 40)) ? "\\&" : "&"; break;
      case "~": r += tildes ? "\\~" : "~"; break;
      case "|": r += o.cell ? "\\|" : "|"; break;
      default: r += c;
    }
  }
  return r;
}
function escapeLineStart(s, cont) {
  if (/^#{1,6}(?:[ \t]|$)/.test(s) || /^>/.test(s) || /^[-+*](?:[ \t]|$)/.test(s)) return "\\" + s;
  const m = /^(\d{1,9})([.)])(?=[ \t]|$)/.exec(s);
  if (m) return m[1] + "\\" + s.slice(m[1].length);
  if (/^(?:-[ \t]*){3,}$/.test(s) || (cont && /^(?:=+|-+)[ \t]*$/.test(s))) return "\\" + s;
  if (/^\|?[ \t]*:?-+:?[ \t]*\|/.test(s) || (cont && /^:?-+:?[ \t]*$/.test(s))) return "\\" + s;
  return s;
}
function escapeText(t, o, before) {
  const lines = t.split("\n");
  const tildes = (t.match(/~/g) || []).length >= 2;
  let res = "";
  for (let k = 0; k < lines.length; k++) {
    let line = lines[k];
    if (k > 0) {
      line = line.replace(/^[ \t]+/, "");
      if (o.heading || o.cell) res += " ";
      else res = res.replace(/[ \t]+$/, "") + "\n";
    }
    let s = escapeInline(line, o, tildes);
    const atStart = k > 0 ? !(o.heading || o.cell) : !!o.block && (before === "" || before.endsWith("\n"));
    if (atStart) s = escapeLineStart(s, k > 0 || before !== "");
    res += s;
  }
  return res;
}
function codeSpan(v, o) {
  v = stripInvisible(v).replace(/\n/g, " ");
  if (!v) return "";
  if (o.cell) v = v.replace(/\|/g, "\\|");
  const ticks = v.match(/`+/g) || [];
  let n = 1;
  while (ticks.some((x) => x.length === n)) n++;
  const f = "`".repeat(n);
  const pad = v.startsWith("`") || v.endsWith("`") || (v.startsWith(" ") && v.endsWith(" ") && v.trim()) ? " " : "";
  return f + pad + v + pad + f;
}
function balanced(u) {
  let d = 0;
  for (let i = 0; i < u.length; i++) {
    if (u[i] === "\\") { i++; continue; }
    if (u[i] === "(") d++;
    else if (u[i] === ")" && --d < 0) return false;
  }
  return d === 0;
}
function mdDest(u) {
  u = String(u || "");
  if (!u) return "<>";
  if (/[\s<>]/.test(u) || !balanced(u)) return "<" + u.replace(/[<>\\]/g, "\\$&") + ">";
  return u.replace(/\\(?=[!-/:-@[-`{-~])/g, "\\\\");
}
const mdTitle = (t) => (t ? ' "' + t.replace(/["\\]/g, "\\$&") + '"' : "");
const mdAlt = (a) => stripInvisible(a || "").replace(/\s+/g, " ").replace(/[[\]\\]/g, "\\$&");

function emitNodes(nodes, o, out) {
  for (const nd of nodes) {
    if (nd.k === "s" || nd.k === "e" || nd.k === "d") {
      const d = nd.k === "s" ? "**" : nd.k === "e" ? "*" : "~~";
      const start = out.s.length;
      emitNodes(nd.kids, o, out);
      const inner = out.s.slice(start);
      out.s = out.s.slice(0, start);
      const m = /^(\s*)([\s\S]*?)(\s*)$/.exec(inner);
      out.s += m[2] ? m[1] + d + m[2] + d + m[3] : inner;
    } else if (nd.k === "l") {
      const l = nd.l;
      const k0 = nd.kids[0];
      const plain = nd.kids.length === 1 && k0.t !== undefined ? stripInvisible(k0.t) : null;
      if (l.auto && plain !== null && (plain === l.href || "mailto:" + plain === l.href) && !/[\s<>]/.test(l.href)) {
        out.s += "<" + plain + ">";
        continue;
      }
      out.s += "[";
      emitNodes(nd.kids, { ...o, link: true }, out);
      out.s += "](" + mdDest(l.href) + mdTitle(l.title) + ")";
    } else if (nd.k === "c") out.s += codeSpan(nd.v, o);
    else if (nd.a === "br") out.s += o.heading || o.cell ? " " : "\\\n";
    else if (nd.a === "img") { out.s += "![" + mdAlt(nd.alt) + "](" + mdDest(nd.src) + mdTitle(nd.title) + ")" + imageAttrsMd(nd); out.img = out.s.length; }
    else if (nd.a === "raw") out.s += o.cell ? nd.v.replace(/\n/g, " ").replace(/\|/g, "\\|") : o.heading ? nd.v.replace(/\n/g, " ") : nd.v;
    else if (nd.t !== undefined) {
      const s = escapeText(nd.t, o, out.s);
      // Text such as "{width=2}" right after an image is not its layout.
      const am = out.img === out.s.length && reImageAttrs.exec(s);
      out.s += am && parseImageAttrs(am[1]) ? "\\" + s : s;
    }
  }
}

function inlineMd(runs, o) {
  const rs = [];
  for (const r of runs) {
    if (r.t !== undefined) { const t = stripInvisible(r.t).replace(/\u00A0/g, " "); if (t) pushRun(rs, { t, m: r.m }); }
    else rs.push(r);
  }
  while (rs.length && rs[0].a === "br") rs.shift();
  while (rs.length && rs[rs.length - 1].a === "br") rs.pop();
  if (rs.length && rs[0].t !== undefined) { rs[0] = { t: rs[0].t.replace(/^\s+/, ""), m: rs[0].m }; if (!rs[0].t) rs.shift(); }
  const li = rs.length - 1;
  if (li >= 0 && rs[li].t !== undefined) { rs[li] = { t: rs[li].t.replace(/\s+$/, ""), m: rs[li].m }; if (!rs[li].t) rs.pop(); }
  const out = { s: "" };
  emitNodes(nest(rs), o, out);
  return out.s.replace(/[ \t]+\n/g, (m, off, s) => (s[off - 1] === "\\" ? m : "\n")).replace(/^\s+|\s+$/g, "");
}

function codeMd(b) {
  const text = stripInvisible(b.text);
  let n = 3;
  for (const r of text.match(/^ {0,3}`{3,}/gm) || []) n = Math.max(n, r.trim().length + 1);
  const f = "`".repeat(n);
  return f + (b.lang || "").replace(/[`\s]/g, "") + "\n" + (text ? text + "\n" : "") + f;
}
function tableMd(b) {
  const ncol = Math.max(1, ...b.rows.map((r) => r.length));
  const row = (cells) => "| " + Array.from({ length: ncol }, (_, k) => inlineMd(cells[k] || [], { cell: true })).join(" | ") + " |";
  const al = Array.from({ length: ncol }, (_, k) => {
    const a = b.align && b.align[k];
    return a === "center" ? ":---:" : a === "right" ? "---:" : a === "left" ? ":---" : "---";
  });
  return [row(b.rows[0] || []), "| " + al.join(" | ") + " |", ...b.rows.slice(1).map(row)].join("\n");
}
function listNeedsLoose(b) {
  if (b.loose) return true;
  for (const it of b.items) {
    for (let q = 1; q < it.blocks.length; q++) {
      const a = it.blocks[q - 1], c = it.blocks[q];
      if (a.type === "p" && (c.type === "p" || c.type === "table" || c.type === "raw")) return true;
    }
  }
  return false;
}
function listMd(b, ch) {
  const loose = listNeedsLoose(b);
  const start = Number.isFinite(b.start) ? b.start : 1;
  return b.items.map((it, idx) => {
    const marker = b.ordered ? start + idx + ch : ch;
    const pad = " ".repeat(marker.length + 1);
    let body = renderBlocks(it.blocks, { inItem: true }, loose ? "\n\n" : "\n");
    if (it.checked != null && (!it.blocks.length || it.blocks[0].type === "p")) body = (it.checked ? "[x] " : "[ ] ") + body;
    return body.split("\n").map((l, k) => (k === 0 ? (l ? marker + " " + l : marker) : l ? pad + l : "")).join("\n");
  }).join(loose ? "\n\n" : "\n");
}
function blockMd(b, o = {}) {
  switch (b.type) {
    case "p": return inlineMd(b.runs, { block: true });
    case "h": { const t = inlineMd(b.runs, { heading: true }); return t ? "#".repeat(Math.min(6, Math.max(1, b.level))) + " " + t : ""; }
    case "hr": return o.inItem ? "***" : "---";
    case "code": return codeMd(b);
    case "raw": return b.text;
    case "quote": {
      const inner = renderBlocks(b.blocks, {});
      return inner ? inner.split("\n").map((l) => (l ? "> " + l : ">")).join("\n") : "";
    }
    case "list": return listMd(b, o.ch || (b.ordered ? "." : "-"));
    case "table": return tableMd(b);
  }
  return "";
}
// Adjacent lists of the same kind must use different markers, or Markdown
// would merge them into one list.
function renderBlocks(blocks, o = {}, sep = "\n\n") {
  const parts = [];
  let prev = null;
  for (const b of blocks) {
    let ch = null;
    if (b.type === "list") {
      const choices = b.ordered ? [".", ")"] : ["-", "*", "+"];
      const prevCh = prev && prev.type === "list" && prev.ordered === b.ordered ? prev.ch : null;
      ch = choices.find((x) => x !== prevCh);
    }
    const s = blockMd(b, { ...o, ch });
    if (!s) continue;
    parts.push(s);
    prev = { type: b.type, ordered: b.ordered, ch };
  }
  return parts.join(sep);
}
export function renderMarkdown(blocks) { return renderBlocks(blocks); }
export const blockSig = (b) => blockMd(b);
const listCh = (src) => { const m = /^ {0,3}(?:([-+*])|\d{1,9}([.)]))/.exec(src); return m ? m[1] || m[2] : null; };

// joinTop serializes top-level entries [{block, src}] where src is the original
// Markdown of an untouched block. Returns the Markdown and per-entry offsets.
export function joinTop(entries) {
  let md = "";
  const segs = [];
  let prev = null;
  const outs = entries.map((e) => (e.src != null ? e.src : e.md != null ? e.md : blockMd(e.block)));
  for (let k = 0; k < entries.length; k++) {
    const e = entries[k], b = e.block;
    let s = outs[k];
    if (!s) continue;
    let ch = null;
    if (b.type === "list") {
      ch = e.src != null ? listCh(e.src) : b.ordered ? "." : "-";
      if (e.src == null) {
        const nx = entries.slice(k + 1).find((x, q) => outs[k + 1 + q]);
        const nextCh = nx && nx.block.type === "list" && nx.block.ordered === b.ordered && nx.src != null ? listCh(nx.src) : null;
        const prevCh = prev && prev.type === "list" && prev.ordered === b.ordered ? prev.ch : null;
        const pick = (b.ordered ? [".", ")"] : ["-", "*", "+"]).find((x) => x !== prevCh && x !== nextCh) || ch;
        if (pick !== ch) { ch = pick; s = blockMd(b, { ch }); }
      }
    }
    if (md) md += "\n\n";
    segs.push({ k, start: md.length, end: md.length + s.length });
    md += s;
    prev = { type: b.type, ordered: b.ordered, ch };
  }
  return { md, segs };
}

/* ======================================================= surface HTML == */
const safeHref = (h) => !/^\s*(?:javascript|vbscript|data|file):/i.test(h || "");
const safeSrc = (s) => /^(?:https?:|blob:|data:image\/(?:png|jpe?g|gif|webp|avif);)/i.test(s || "") || !/^\s*[a-z][a-z0-9+.-]*:/i.test(s || "");
// imgHTML renders an image run; its width and alignment are attributes and
// the class the site uses (img.align-left floats with the text around it).
export function imgHTML(nd) {
  return `<img ${safeSrc(nd.src) ? "src" : "data-src"}="${escHTML(nd.src || "")}" alt="${escHTML(nd.alt || "")}"${nd.title ? ` title="${escHTML(nd.title)}"` : ""}` +
    `${nd.width ? ` width="${nd.width}"` : ""}${nd.align ? ` data-align="${nd.align}" class="align-${nd.align}"` : ""}>`;
}
function nodesHTML(nodes) {
  let s = "";
  for (const nd of nodes) {
    if (nd.k === "s") s += "<strong>" + nodesHTML(nd.kids) + "</strong>";
    else if (nd.k === "e") s += "<em>" + nodesHTML(nd.kids) + "</em>";
    else if (nd.k === "d") s += "<del>" + nodesHTML(nd.kids) + "</del>";
    else if (nd.k === "l") {
      const h = nd.l.href || "";
      s += `<a ${safeHref(h) ? "href" : "data-href"}="${escHTML(h)}"${nd.l.title ? ` title="${escHTML(nd.l.title)}"` : ""}${nd.l.auto ? ' data-auto=""' : ""}>${nodesHTML(nd.kids)}</a>`;
    } else if (nd.k === "c") s += "<code>" + escHTML(nd.v) + "</code>";
    else if (nd.a === "br") s += "<br>";
    else if (nd.a === "img") s += imgHTML(nd);
    else if (nd.a === "raw") s += `<span class="md-raw" contenteditable="false" data-raw="${escHTML(nd.v)}">${escHTML(nd.v)}</span>`;
    else if (nd.t !== undefined) s += escHTML(nd.t);
  }
  return s;
}
const inlineHTML = (runs) => nodesHTML(nest(runs));
function liHTML(it, loose) {
  const ck = it.checked == null ? "" : ` data-checked="${it.checked ? "true" : "false"}"`;
  const bl = it.blocks;
  let inner = !loose && bl[0] && bl[0].type === "p" ? inlineHTML(bl[0].runs) + toHTML(bl.slice(1)) : toHTML(bl);
  if (!inner || (!loose && bl.length === 1 && bl[0].type === "p" && !bl[0].runs.length)) inner = "<br>";
  return `<li${ck}>${inner}</li>`;
}
export function blockHTML(b) {
  switch (b.type) {
    case "p": return "<p>" + (inlineHTML(b.runs) || "<br>") + "</p>";
    case "h": { const lv = Math.min(6, Math.max(1, b.level)); return `<h${lv}>${inlineHTML(b.runs) || "<br>"}</h${lv}>`; }
    case "hr": return "<hr>";
    case "code": {
      let t = b.text;
      if (t.endsWith("\n")) t += "\n";
      if (t.startsWith("\n")) t = "\n" + t;
      return `<pre data-lang="${escHTML(b.lang || "")}" spellcheck="false">${t ? escHTML(t) : "<br>"}</pre>`;
    }
    case "quote": return "<blockquote>" + (toHTML(b.blocks) || "<p><br></p>") + "</blockquote>";
    case "list": {
      const tag = b.ordered ? "ol" : "ul";
      const st = b.ordered && b.start !== 1 && Number.isFinite(b.start) ? ` start="${b.start}"` : "";
      return `<${tag}${st}>` + (b.items.length ? b.items : [{ checked: null, blocks: [] }]).map((it) => liHTML(it, listNeedsLoose(b))).join("") + `</${tag}>`;
    }
    case "table": {
      const ncol = Math.max(1, ...b.rows.map((r) => r.length));
      const al = (k) => (b.align && b.align[k] ? ` data-align="${b.align[k]}"` : "");
      const cells = (r, tag) => Array.from({ length: ncol }, (_, k) => `<${tag}${al(k)}>${inlineHTML(r[k] || []) || "<br>"}</${tag}>`).join("");
      return "<table><thead><tr>" + cells(b.rows[0] || [], "th") + "</tr></thead><tbody>" +
        b.rows.slice(1).map((r) => "<tr>" + cells(r, "td") + "</tr>").join("") + "</tbody></table>";
    }
    case "raw": {
      const kind = b.kind || "html";
      const video = kind === "video" && videoBlockHTML(b.text);
      if (video) return video;
      return `<div class="md-raw" contenteditable="false" data-raw="${escHTML(b.text)}" data-kind="${kind}" data-label="${escHTML(LABELS[kind] || kind)}">${escHTML(b.text)}</div>`;
    }
  }
  return "";
}
export function toHTML(blocks) { return blocks.map(blockHTML).join(""); }

/* ===================================================== DOM -> blocks == */
// Works on real DOM nodes and on any minimal tree exposing nodeType, nodeName,
// childNodes, data and getAttribute (see the Node tests).
const DROP = new Set(["script", "style", "noscript", "template", "iframe", "object", "embed", "svg", "canvas", "video", "audio",
  "head", "meta", "link", "title", "button", "input", "select", "textarea", "option", "map", "area", "math", "xml", "o:p", "colgroup", "col"]);
const CONTAINERS = new Set(["div", "section", "article", "main", "header", "footer", "aside", "nav", "figure", "figcaption", "address",
  "center", "details", "summary", "fieldset", "form", "body", "html", "hgroup", "dir", "menu", "dd", "dl", "search", "legend", "caption"]);
const tagOf = (n) => String(n.nodeName || "").toLowerCase();
const attr = (n, a) => (n.getAttribute ? n.getAttribute(a) : null);
// Pasted markup has its style attributes renamed before parsing (see
// prepareHTML) so the strict CSP does not log a violation for each of them.
const styleOf = (n) => attr(n, "style") ?? attr(n, "data-paste-style") ?? "";
const kids = (n) => Array.from(n.childNodes || []);

function newB(ctx) { return { ctx, blocks: [], runs: [] }; }
function lastChar(runs) {
  const r = runs[runs.length - 1];
  if (!r) return "";
  if (r.t !== undefined) return r.t[r.t.length - 1] || "";
  return r.a === "br" ? "\n" : "x";
}
function bText(B, t, m) {
  if (!t) return;
  if (B.ctx.paste && !m.pre) {
    t = t.replace(/[\t\n\r\f \u00A0]+/g, " ");
    if (t[0] === " ") { const p = lastChar(B.runs); if (p === "" || p === " " || p === "\n") t = t.slice(1); }
    if (!t) return;
  } else t = t.replace(/\u00A0/g, " ");
  pushRun(B.runs, { t, m }, B.ctx.paste ? sameX : sameMarks);
}
function bFlush(B, force, type) {
  let runs = B.runs;
  B.runs = [];
  const paste = B.ctx.paste;
  while (runs.length && runs[runs.length - 1].a === "br") runs.pop();
  if (paste) {
    while (runs.length && runs[0].a === "br") runs.shift();
    if (runs[0] && runs[0].t !== undefined && !runs[0].m.pre) runs[0].t = runs[0].t.replace(/^\s+/, "");
    const l = runs[runs.length - 1];
    if (l && l.t !== undefined && !l.m.pre) l.t = l.t.replace(/\s+$/, "");
    runs = runs.filter((r) => r.a || r.t);
  }
  const has = hasContent(runs) || (!paste && runs.some((r) => r.t));
  if (!has && !(force && !paste)) return;
  let block = null;
  if (paste && (!type || type.type === "p")) {
    const textRuns = runs.filter((r) => r.t !== undefined && r.t.trim());
    if (textRuns.length && textRuns.every((r) => r.m.mono)) block = { type: "code", lang: "", text: runsText(runs), _line: true };
    else if (textRuns.length && runsText(runs).trim().length <= 120) {
      const min = Math.min(...textRuns.map((r) => r.m.sz || 0));
      const bold = textRuns.every((r) => r.m.s);
      const level = min >= 24 ? 1 : min >= 18 ? 2 : min >= 15 ? 3 : min >= 13 && bold ? 4 : 0;
      if (level) block = { type: "h", level, runs };
    }
    if (!block) runs = runs.map((r) => (r.t !== undefined && r.m.mono ? { t: r.t, m: { ...r.m, c: true } } : r));
  }
  if (!block) block = { ...(type || { type: "p" }), runs };
  if (block.runs) block.runs = finalRuns(block.runs, block.type === "h");
  B.blocks.push(block);
}
function finalRuns(runs, heading) {
  const out = [];
  for (const r of runs) {
    const m = cleanMarks(r.m);
    if (heading) delete m.s;
    if (r.t !== undefined) pushRun(out, { t: r.t, m });
    else out.push({ ...r, m });
  }
  return out;
}
function bFinish(B) {
  bFlush(B);
  let blocks = groupWordLists(B.blocks);
  const out = [];
  for (const b of blocks) {
    const prev = out[out.length - 1];
    if (b._line && prev && prev._line) { prev.text += "\n" + b.text; continue; }
    if (b._wq && prev && prev._wq) { prev.blocks.push(...b.blocks); continue; }
    out.push(b);
  }
  for (const b of out) { delete b._line; delete b._wq; }
  return out;
}
function groupWordLists(blocks) {
  const out = [];
  for (let k = 0; k < blocks.length;) {
    if (blocks[k].type !== "_wli") { out.push(blocks[k++]); continue; }
    const items = [];
    while (k < blocks.length && blocks[k].type === "_wli") items.push(blocks[k++]);
    out.push(...nestItems(items));
  }
  return out;
}
// Builds nested lists from flat [{level, ordered, start, blocks|runs}] items.
function nestItems(items) {
  const roots = [], stack = [];
  for (const it of items) {
    const li = { checked: it.checked ?? null, blocks: it.blocks || [{ type: "p", runs: it.runs }] };
    while (stack.length && stack[stack.length - 1].level > it.level) stack.pop();
    let top = stack[stack.length - 1];
    if (top && top.level === it.level) {
      if (top.list.ordered === it.ordered) { top.list.items.push(li); top.last = li; continue; }
      stack.pop();
      top = stack[stack.length - 1];
    }
    const list = { type: "list", ordered: it.ordered, start: it.start || 1, loose: false, items: [li] };
    if (top) top.last.blocks.push(list); else roots.push(list);
    stack.push({ level: it.level, list, last: li });
  }
  return roots;
}

function cleanHref(h) {
  if (!h) return null;
  h = h.trim();
  const g = /^https?:\/\/(?:www\.)?google\.[a-z.]+\/url\?(?:[^#]*&)?q=([^&#]+)/i.exec(h);
  if (g) { try { h = decodeURIComponent(g[1]); } catch (_) { /* keep */ } }
  return /^(?:https?:|mailto:)/i.test(h) ? h : null;
}
const MONO = /courier|consolas|monaco|menlo|monospace|source code|roboto mono|fira (?:code|mono)|jetbrains|lucida console|sf mono|ubuntu mono|inconsolata|cascadia/i;
function marksFor(n, tag, m, style, ctx) {
  let r = m;
  const set = (k, v) => { if (!!r[k] !== v) r = { ...r, [k]: v }; };
  switch (tag) {
    case "strong": case "b": set("s", true); break;
    case "em": case "i": case "cite": case "dfn": case "var": set("e", true); break;
    case "del": case "s": case "strike": set("d", true); break;
    case "code": case "kbd": case "samp": case "tt": set("c", true); break;
    case "a": {
      const href = ctx.paste ? cleanHref(attr(n, "href")) : attr(n, "data-href") ?? attr(n, "href");
      if (href) r = { ...r, l: { href, title: attr(n, "title") || "", auto: !ctx.paste && attr(n, "data-auto") != null } };
      break;
    }
  }
  if (ctx.paste && style) {
    const fw = /(?:^|;)\s*font-weight\s*:\s*([^;!]+)/i.exec(style);
    if (fw) { const v = fw[1].trim().toLowerCase(); set("s", v === "bold" || v === "bolder" || +v >= 600); }
    const fs = /(?:^|;)\s*font-style\s*:\s*([^;!]+)/i.exec(style);
    if (fs) set("e", /italic|oblique/i.test(fs[1]));
    const td = /(?:^|;)\s*text-decoration(?:-line)?\s*:\s*([^;!]+)/i.exec(style);
    if (td && /line-through/i.test(td[1])) set("d", true);
    const sz = /(?:^|;)\s*font-size\s*:\s*([\d.]+)\s*(pt|px)/i.exec(style);
    if (sz) r = { ...r, sz: sz[2].toLowerCase() === "px" ? +sz[1] * 0.75 : +sz[1] };
    const ff = /(?:^|;)\s*font-family\s*:\s*([^;]+)/i.exec(style);
    if (ff) r = { ...r, mono: MONO.test(ff[1]) };
    const ws = /(?:^|;)\s*white-space\s*:\s*([a-z-]+)/i.exec(style);
    if (ws) r = { ...r, pre: /^pre(?:-wrap)?$/i.test(ws[1]) };
  }
  return r;
}
function walkKids(n, m, B) {
  let skip = false;
  for (const c of kids(n)) {
    if (c.nodeType === 8) {
      const d = String(c.data || "").trim();
      if (/^\[if !support(?:Lists|LineBreakNewLine|Footnotes|Annotations)\]/i.test(d)) skip = true;
      else if (/^\[endif\]/i.test(d)) skip = false;
      continue;
    }
    if (!skip) walk(c, m, B);
  }
}
function textWithBreaks(n) {
  let s = "";
  for (const c of kids(n)) {
    if (c.nodeType === 3) s += c.data;
    else if (c.nodeType === 1) {
      const t = tagOf(c);
      if (t === "br") s += "\n";
      else if (DROP.has(t)) continue;
      else if (t === "div" || t === "p" || t === "li" || t === "tr") { if (s && !s.endsWith("\n")) s += "\n"; s += textWithBreaks(c); }
      else s += textWithBreaks(c);
    }
  }
  return s;
}
function readPre(n, ctx) {
  let text = textWithBreaks(n).replace(/\u00A0/g, " ");
  if (text.endsWith("\n")) text = text.slice(0, -1);
  let lang = "";
  if (ctx.paste) {
    for (const el of [n, ...kids(n).filter((c) => c.nodeType === 1)]) {
      const lm = /(?:^|\s)(?:language|lang)-([\w+#.-]+)/.exec(attr(el, "class") || "");
      if (lm) { lang = lm[1]; break; }
    }
  } else lang = (attr(n, "data-lang") || "").trim();
  return { type: "code", lang, text };
}
function inlineOf(el, m, ctx) {
  const sub = newB(ctx);
  walkKids(el, m, sub);
  return flattenBlocks(bFinish(sub));
}
function flattenBlocks(blocks) {
  const out = [];
  const add = (runs) => {
    if (!runs.length) return;
    if (out.length) pushRun(out, { t: " ", m: {} });
    for (const r of runs) pushRun(out, r.t !== undefined ? { t: r.t, m: r.m } : { ...r });
  };
  for (const b of blocks) {
    if (b.type === "p" || b.type === "h") add(b.runs);
    else if (b.type === "code") add([{ t: b.text.replace(/\n/g, " "), m: { c: true } }]);
    else if (b.type === "list") for (const it of b.items) add(flattenBlocks(it.blocks));
    else if (b.type === "quote") add(flattenBlocks(b.blocks));
    else if (b.type === "table") for (const r of b.rows) for (const c of r) add(c);
    else if (b.type === "raw") add([{ a: "raw", v: b.text, m: {} }]);
  }
  return out;
}
function readTable(n, m, ctx) {
  const trs = [];
  const visit = (el) => { for (const c of kids(el)) { const t = tagOf(c); if (t === "tr") trs.push(c); else if (t === "thead" || t === "tbody" || t === "tfoot") visit(c); } };
  visit(n);
  const rows = trs.map((tr) => kids(tr).filter((c) => /^t[dh]$/.test(tagOf(c)))).filter((r) => r.length);
  if (!rows.length) return null;
  if (ctx.paste && rows.reduce((a, r) => a + r.length, 0) === 1) {
    const sub = newB(ctx);
    walkKids(rows[0][0], m, sub);
    return bFinish(sub);
  }
  const out = rows.map((r) => r.map((c) => inlineOf(c, m, ctx)));
  const ncol = Math.max(...out.map((r) => r.length));
  for (const r of out) while (r.length < ncol) r.push([]);
  const align = Array.from({ length: ncol }, (_, k) => {
    const c = rows[0][k];
    if (!c) return null;
    let a = ctx.paste ? attr(c, "align") || (/text-align\s*:\s*(left|center|right)/i.exec(styleOf(c)) || [])[1] : attr(c, "data-align");
    a = (a || "").toLowerCase();
    if (ctx.paste && a === "left") a = "";
    return /^(?:left|center|right)$/.test(a) ? a : null;
  });
  return { type: "table", align, rows: out };
}
function readList(n, m, ctx) {
  const ordered = tagOf(n) === "ol";
  const st = parseInt(attr(n, "start"), 10);
  const entries = [];
  let looseDom = false;
  const appendToLast = (blocks) => { if (entries.length) entries[entries.length - 1].blocks.push(...blocks); else entries.push({ blocks, checked: null, level: 0 }); };
  for (const c of kids(n)) {
    if (c.nodeType === 3) {
      if (!c.data.trim() || ctx.paste) { if (c.data.trim()) appendToLast([{ type: "p", runs: [{ t: c.data.trim(), m: {} }] }]); continue; }
      appendToLast([{ type: "p", runs: [{ t: c.data, m: {} }] }]);
      continue;
    }
    if (c.nodeType !== 1) continue;
    const tag = tagOf(c);
    if (tag === "li") {
      const sub = newB(ctx);
      walkKids(c, m, sub);
      const blocks = bFinish(sub);
      if (!ctx.paste && kids(c).some((x) => tagOf(x) === "p")) looseDom = true;
      let checked = null;
      if (!ctx.paste) { const dc = attr(c, "data-checked"); if (dc === "true" || dc === "false") checked = dc === "true"; }
      else if (attr(c, "role") === "checkbox") checked = attr(c, "aria-checked") === "true";
      else { const cb = findEl(c, (x) => tagOf(x) === "input" && (attr(x, "type") || "").toLowerCase() === "checkbox"); if (cb) checked = attr(cb, "checked") != null; }
      if (ctx.paste && !blocks.length) continue;
      entries.push({ blocks, checked, level: parseInt(attr(c, "aria-level"), 10) || 0 });
    } else if (tag === "ul" || tag === "ol") {
      const sub = readList(c, m, ctx);
      if (sub) appendToLast([sub]);
    } else {
      const sub = newB(ctx);
      walk(c, m, sub);
      const blocks = bFinish(sub);
      if (blocks.length) appendToLast(blocks);
    }
  }
  if (!entries.length) return ctx.paste ? null : { type: "list", ordered, start: 1, loose: false, items: [{ checked: null, blocks: [] }] };
  const levels = new Set(entries.map((e) => e.level));
  if (ctx.paste && levels.size > 1) {
    const nested = nestItems(entries.map((e) => ({ level: e.level || 1, ordered, checked: e.checked, blocks: e.blocks })));
    if (nested.length === 1) { nested[0].start = st > 0 ? st : 1; return nested[0]; }
  }
  const items = entries.map((e) => ({ checked: e.checked, blocks: e.blocks }));
  const loose = ctx.paste ? items.some((it) => it.blocks.filter((b) => b.type === "p").length > 1) : looseDom;
  return { type: "list", ordered, start: st >= 0 ? st : 1, loose, items };
}
function findEl(n, pred) {
  for (const c of kids(n)) {
    if (c.nodeType !== 1) continue;
    if (pred(c)) return c;
    const f = findEl(c, pred);
    if (f) return f;
  }
  return null;
}
function textContent(n) {
  if (n.nodeType === 3) return n.data;
  return kids(n).map(textContent).join("");
}
function wordMarker(n) {
  const sp = findEl(n, (x) => /mso-list\s*:\s*ignore/i.test(styleOf(x)));
  if (sp) return textContent(sp).replace(/[\s\u00A0]+/g, " ").trim();
  let on = false, s = "";
  for (const c of kids(n)) {
    if (c.nodeType === 8) { const d = String(c.data || ""); if (/^\[if !supportLists\]/i.test(d.trim())) on = true; else if (/^\[endif\]/i.test(d.trim())) on = false; continue; }
    if (on) s += textContent(c);
  }
  return s.replace(/[\s\u00A0]+/g, " ").trim();
}
const isOrderedMarker = (mk) => /^\(?(?:[0-9]{1,4}|[a-zA-Z]|[ivxlcdmIVXLCDM]{1,6})[.)]$/.test(mk);

function bImage(B, n, m) {
  const ctx = B.ctx;
  const src = ((ctx.paste ? attr(n, "src") : attr(n, "data-src") ?? attr(n, "src")) || "").trim();
  const alt = (attr(n, "alt") || "").replace(/\s+/g, " ").trim();
  if (ctx.paste) {
    if (attr(n, "width") === "1" && attr(n, "height") === "1") return;
    if (/^data:image\/(?:png|jpe?g|gif|webp|avif);base64,/i.test(src)) { if (!ctx.images.data.includes(src)) ctx.images.data.push(src); }
    else if (!/^https?:\/\//i.test(src)) { ctx.images.dropped++; return; }
  }
  if (!src) return;
  B.runs.push({ a: "img", src, alt, title: ctx.paste ? "" : attr(n, "title") || "", m, ...(ctx.paste ? {} : imgLayoutOf(n)) });
}
// imgLayoutOf reads the width and alignment that imgHTML rendered.
function imgLayoutOf(n) {
  const out = {};
  const w = attr(n, "width") || "", a = attr(n, "data-align");
  const at = /^[0-9]{1,4}$/.test(w) && parseImageAttrs("width=" + w);
  if (at) out.width = at.width;
  if (IMAGE_ALIGNS.includes(a)) out.align = a;
  return out;
}
function walk(n, m, B) {
  const ctx = B.ctx;
  const nt = n.nodeType;
  if (nt === 3) return bText(B, n.data != null ? n.data : n.nodeValue || "", m);
  if (nt === 9 || nt === 11) return walkKids(n, m, B);
  if (nt !== 1) return;
  const tag = tagOf(n);
  if (DROP.has(tag) || tag.startsWith("v:") || tag.startsWith("w:wrap")) return;
  const style = ctx.paste ? styleOf(n) : "";
  if (ctx.paste) {
    if (/mso-list\s*:\s*ignore|display\s*:\s*none|mso-hide\s*:\s*all/i.test(style) || attr(n, "hidden") != null) return;
  } else if (attr(n, "data-raw") != null) {
    const v = attr(n, "data-raw");
    if (tag === "div") { bFlush(B); B.blocks.push({ type: "raw", kind: attr(n, "data-kind") || "html", text: v }); return; }
    B.runs.push({ a: "raw", v, m });
    return;
  }
  const m2 = marksFor(n, tag, m, style, ctx);
  // Code copied from editors (VS Code, IDEs): a monospace white-space:pre box.
  if (ctx.paste && m2.pre && m2.mono && (CONTAINERS.has(tag) || tag === "p")) {
    bFlush(B);
    B.blocks.push(readPre(n, ctx));
    return;
  }
  switch (tag) {
    case "br":
      if (ctx.paste) {
        if (!B.runs.length) return;
        if (B.runs[B.runs.length - 1].a === "br") { B.runs.pop(); bFlush(B); return; }
      }
      B.runs.push({ a: "br", m: m2 });
      return;
    case "img": return bImage(B, n, m2);
    case "hr": bFlush(B); B.blocks.push({ type: "hr" }); return;
    case "h1": case "h2": case "h3": case "h4": case "h5": case "h6": {
      bFlush(B);
      const runs = finalRuns(inlineOf(n, ctx.paste ? { ...m2, s: false, sz: 0 } : m2, ctx), ctx.paste);
      if (!ctx.paste || hasContent(runs)) B.blocks.push({ type: "h", level: +tag[1], runs });
      return;
    }
    case "ul": case "ol": { bFlush(B); const l = readList(n, m2, ctx); if (l) B.blocks.push(l); return; }
    case "blockquote": {
      bFlush(B);
      const sub = newB(ctx);
      walkKids(n, m2, sub);
      const bl = bFinish(sub);
      if (bl.length || !ctx.paste) B.blocks.push({ type: "quote", blocks: bl });
      return;
    }
    case "pre": bFlush(B); B.blocks.push(readPre(n, ctx)); return;
    case "table": {
      bFlush(B);
      const t = readTable(n, m2, ctx);
      if (Array.isArray(t)) B.blocks.push(...t); else if (t) B.blocks.push(t);
      return;
    }
    case "p": case "li": case "dt": {
      if (ctx.paste) {
        const wl = /mso-list\s*:\s*(l\d+)\s+level(\d+)\s+(lfo\d+)/i.exec(style);
        if (wl) {
          bFlush(B);
          const mk = wordMarker(n);
          const runs = finalRuns(inlineOf(n, m2, ctx));
          if (hasContent(runs)) B.blocks.push({ type: "_wli", level: +wl[2], ordered: isOrderedMarker(mk), start: parseInt(mk, 10) || 1, runs });
          return;
        }
        const cls = attr(n, "class") || "";
        if (/\bMsoTitle\b/i.test(cls)) {
          bFlush(B);
          const runs = finalRuns(inlineOf(n, { ...m2, sz: 0 }, ctx), true);
          if (hasContent(runs)) B.blocks.push({ type: "h", level: 1, runs });
          return;
        }
        if (/\bMso(?:Intense)?Quote\b/i.test(cls)) {
          bFlush(B);
          const runs = finalRuns(inlineOf(n, m2, ctx));
          if (hasContent(runs)) B.blocks.push({ type: "quote", blocks: [{ type: "p", runs }], _wq: true });
          return;
        }
      }
      bFlush(B);
      walkKids(n, tag === "dt" ? { ...m2, s: true } : m2, B);
      bFlush(B, tag === "p" && !ctx.paste);
      return;
    }
    default:
      if (CONTAINERS.has(tag)) { bFlush(B); walkKids(n, m2, B); bFlush(B); return; }
      walkKids(n, m2, B);
  }
}

// readNodes converts editor-surface DOM nodes back into blocks.
export function readNodes(nodes, opts = {}) {
  const ctx = { paste: !!opts.paste, images: { data: [], dropped: 0 } };
  const B = newB(ctx);
  for (const n of nodes) walk(n, {}, B);
  return bFinish(B);
}
function eachBlock(blocks, fn) {
  for (const b of blocks) {
    fn(b);
    if (b.type === "quote") eachBlock(b.blocks, fn);
    else if (b.type === "list") for (const it of b.items) eachBlock(it.blocks, fn);
  }
}
// prepareHTML readies clipboard HTML for DOMParser: <style> blocks are removed
// and style attributes renamed, so parsing triggers no CSP inline-style checks.
export function prepareHTML(html) {
  return String(html)
    .replace(/<style\b[\s\S]*?<\/style\s*>/gi, "")
    .replace(/<[a-zA-Z][^>]*>/g, (tag) => tag.replace(/(\s)style(\s*=)/gi, "$1data-paste-style$2"));
}
// htmlToBlocks sanitizes pasted HTML (a parsed document/body or any node) to the
// structural subset. images.data lists data: images to upload; images.dropped
// counts images that could not be kept (file:, cid:, relative...).
export function htmlToBlocks(root) {
  const ctx = { paste: true, images: { data: [], dropped: 0 } };
  const B = newB(ctx);
  walk(root, {}, B);
  return { blocks: normalizeHeadings(bFinish(B)), images: ctx.images };
}
// normalizeHeadings shifts pasted headings so the top level becomes h2 (h1 is
// the post title) and clamps the rest to h4.
export function normalizeHeadings(blocks) {
  let min = 7;
  eachBlock(blocks, (b) => { if (b.type === "h") min = Math.min(min, b.level); });
  const shift = min < 2 ? 2 - min : 0;
  eachBlock(blocks, (b) => { if (b.type === "h") b.level = Math.min(4, b.level + shift); });
  return blocks;
}
// Plain text that looks like Markdown is parsed as such when pasted visually.
export function looksLikeMarkdown(t) {
  return /^(?:#{1,6} |[-*+] |\d{1,9}[.)] |> |```|~~~|\|.*\|$)/m.test(t) || /\*\*[^*\n]+\*\*|\[[^\]\n]+\]\([^)\s]+\)|`[^`\n]+`/.test(t);
}
// textToBlocks: one line is inserted as is; several become one paragraph each.
export function textToBlocks(t) {
  const lines = String(t).replace(/\r\n?/g, "\n").replace(/\t/g, "    ").split("\n");
  if (lines.length === 1) return lines[0] ? [{ type: "p", runs: [{ t: lines[0], m: {} }] }] : [];
  return lines.filter((p) => p.trim()).map((p) => ({ type: "p", runs: [{ t: p.trim(), m: {} }] }));
}
export const _internal = { parseInline, nest, inlineMd, ZW, M1, M2 };


/* ========================================================= edit commands == */
// Commands work on blocks in which the selection is marked by M1 (start/caret)
// and M2 (end) characters inside text. The browser layer inserts the markers
// into the DOM, reads the affected top-level blocks, applies a command,
// renders them back and turns the markers into the new selection. Keeping the
// commands pure makes them testable and keeps the DOM canonical.
const caretRun = (m = {}) => ({ t: M1, m });
const leafText = (b) => (b.type === "p" || b.type === "h" ? runsText(b.runs) : b.type === "code" ? b.text : b.type === "table" ? b.rows.flat().map(runsText).join("") : "");
function splitAt(runs, ch = M1) {
  const before = [], after = [];
  let found = false, marks = {};
  for (const r of runs) {
    if (!found && r.t !== undefined && r.t.includes(ch)) {
      const i = r.t.indexOf(ch);
      marks = r.m || {};
      if (i > 0) before.push({ t: r.t.slice(0, i), m: r.m });
      if (i + 1 < r.t.length) after.push({ t: r.t.slice(i + 1), m: r.m });
      found = true;
      continue;
    }
    (found ? after : before).push(r);
  }
  return [before, after, marks];
}
// locate returns the path to the leaf holding ch: steps {arr, i} (block arr[i]),
// {arr, i, j} (item j of list arr[i]) and {arr, i, row, col} (table cell).
function locate(blocks, ch, chain = []) {
  for (let i = 0; i < blocks.length; i++) {
    const b = blocks[i];
    if ((b.type === "p" || b.type === "h") && runsText(b.runs).includes(ch)) return [...chain, { arr: blocks, i }];
    if (b.type === "code" && b.text.includes(ch)) return [...chain, { arr: blocks, i }];
    if (b.type === "quote") { const r = locate(b.blocks, ch, [...chain, { arr: blocks, i }]); if (r) return r; }
    if (b.type === "list") {
      for (let j = 0; j < b.items.length; j++) { const r = locate(b.items[j].blocks, ch, [...chain, { arr: blocks, i, j }]); if (r) return r; }
    }
    if (b.type === "table") {
      for (let row = 0; row < b.rows.length; row++) for (let col = 0; col < b.rows[row].length; col++) {
        if (runsText(b.rows[row][col]).includes(ch)) return [...chain, { arr: blocks, i, row, col }];
      }
    }
  }
  return null;
}
const leafRuns = (st) => (st.row !== undefined ? st.arr[st.i].rows[st.row][st.col] : st.arr[st.i].runs);
const setLeafRuns = (st, r) => { if (st.row !== undefined) st.arr[st.i].rows[st.row][st.col] = r; else st.arr[st.i].runs = r; };
function eachRuns(blocks, fn, onCode) {
  for (const b of blocks) {
    if (b.type === "p" || b.type === "h") b.runs = fn(b.runs);
    else if (b.type === "code") { if (onCode) onCode(b); }
    else if (b.type === "quote") eachRuns(b.blocks, fn, onCode);
    else if (b.type === "list") for (const it of b.items) eachRuns(it.blocks, fn, onCode);
    else if (b.type === "table") for (const row of b.rows) for (let k = 0; k < row.length; k++) row[k] = fn(row[k]);
  }
}
const scanCode = (st) => (b) => { for (const ch of b.text) { if (ch === M1) st.on = true; else if (ch === M2) st.on = false; } };
function allMarked(blocks, test) {
  const st = { on: false };
  let any = false, all = true;
  eachRuns(blocks, (runs) => {
    for (const r of runs) {
      if (r.t === undefined) continue;
      for (const p of r.t.split(/([])/)) {
        if (p === M1) st.on = true;
        else if (p === M2) st.on = false;
        else if (st.on && stripInvisible(p).trim()) { any = true; if (!test(r.m || {})) all = false; }
      }
    }
    return runs;
  }, scanCode(st));
  return any && all;
}
function applyMarks(blocks, fn) {
  const st = { on: false };
  eachRuns(blocks, (runs) => {
    const out = [];
    for (const r of runs) {
      if (r.t === undefined) { out.push(st.on ? { ...r, m: fn(r.m || {}, r) } : r); continue; }
      for (const p of r.t.split(/([])/)) {
        if (!p) continue;
        if (p === M1 || p === M2) { st.on = p === M1; out.push({ t: p, m: null }); continue; }
        out.push({ t: p, m: st.on ? fn(r.m || {}, r) : r.m });
      }
    }
    for (let k = 0; k < out.length; k++) {
      if (out[k].m !== null) continue;
      const nb = out[k].t === M1 ? out[k + 1] || out[k - 1] : out[k - 1] || out[k + 1];
      out[k].m = nb && nb.m ? nb.m : {};
    }
    const merged = [];
    for (const x of out) pushRun(merged, x.t !== undefined ? { t: x.t, m: x.m } : x);
    return merged;
  }, scanCode(st));
}
const hasSelection = (blocks) => !!locate(blocks, M2);

export function toggleMark(blocks, key) {
  if (!hasSelection(blocks)) {
    // Collapsed caret: a zero-width run carries the pending formatting.
    const path = locate(blocks, M1);
    const leaf = path && path[path.length - 1];
    if (!leaf || leaf.arr[leaf.i].type === "code") return blocks;
    const [bf, af, m] = splitAt(leafRuns(leaf));
    const nm = { ...m };
    if (nm[key]) delete nm[key]; else nm[key] = true;
    setLeafRuns(leaf, [...bf, { t: ZW + M1, m: nm }, ...af]);
    return blocks;
  }
  const on = !allMarked(blocks, (m) => !!m[key]);
  applyMarks(blocks, (m, r) => {
    if (key === "c" && r.a) return m;
    const n = { ...m };
    if (on) n[key] = true; else delete n[key];
    return n;
  });
  return blocks;
}
export function isMarked(blocks, key) { return allMarked(blocks, (m) => !!m[key]); }

export function setLink(blocks, link) {
  if (!hasSelection(blocks)) {
    const path = locate(blocks, M1);
    const leaf = path && path[path.length - 1];
    if (!leaf || leaf.arr[leaf.i].type === "code") return blocks;
    const runs = leafRuns(leaf);
    const idx = runs.findIndex((r) => r.t !== undefined && r.t.includes(M1));
    const cur = runs[idx] && runs[idx].m && runs[idx].m.l;
    if (!cur) {
      if (link) {
        const [bf, af, m] = splitAt(runs);
        setLeafRuns(leaf, [...bf, { t: link.text || link.href, m: { ...m, l: { href: link.href, title: "" } } }, caretRun({ ...m, l: undefined }), ...af].map((r) => (r.m && r.m.l === undefined ? { ...r, m: cleanMarks(r.m) } : r)));
      }
      return blocks;
    }
    let a = idx, z = idx;
    while (a > 0 && runs[a - 1].m && sameLink(runs[a - 1].m.l, cur)) a--;
    while (z < runs.length - 1 && runs[z + 1].m && sameLink(runs[z + 1].m.l, cur)) z++;
    const out = runs.map((r, k) => {
      if (k < a || k > z) return r;
      const n = { ...r.m };
      if (link) n.l = { href: link.href, title: cur.title || "" }; else delete n.l;
      return { ...r, m: n };
    });
    const merged = [];
    for (const r of out) pushRun(merged, r.t !== undefined ? { t: r.t, m: r.m } : r);
    setLeafRuns(leaf, merged);
    return blocks;
  }
  applyMarks(blocks, (m) => {
    const n = { ...m };
    if (link) n.l = { href: link.href, title: "" }; else delete n.l;
    return n;
  });
  return blocks;
}

// selectedLeaves lists the leaf blocks touched by the selection, in order.
function selectedLeaves(blocks) {
  const out = [];
  const ranged = hasSelection(blocks);
  let on = false;
  const visit = (arr, inList) => {
    for (let i = 0; i < arr.length; i++) {
      const b = arr[i];
      if (b.type === "quote") { visit(b.blocks, inList); continue; }
      if (b.type === "list") { for (const it of b.items) visit(it.blocks, true); continue; }
      const t = leafText(b);
      const s = t.includes(M1), e = t.includes(M2);
      const sel = on || s;
      if (s) on = ranged && !e;
      else if (e) on = false;
      if (sel) out.push({ arr, i, b, inList });
    }
  };
  visit(blocks, false);
  return out;
}
export function setBlockType(blocks, level) {
  const leaves = selectedLeaves(blocks).filter((x) => (x.b.type === "p" || x.b.type === "h") && !x.inList);
  const same = level && leaves.length && leaves.every((x) => x.b.type === "h" && x.b.level === level);
  for (const x of leaves) x.arr[x.i] = level && !same ? { type: "h", level, runs: x.b.runs } : { type: "p", runs: x.b.runs };
  return blocks;
}
function codeToParas(b) {
  const paras = b.text.split("\n").filter((l) => stripInvisible(l).trim() || /[]/.test(l)).map((l) => ({ type: "p", runs: [{ t: l, m: {} }] }));
  return paras.length ? paras : [{ type: "p", runs: [caretRun()] }];
}
export function toggleCodeBlock(blocks) {
  const leaves = selectedLeaves(blocks);
  if (leaves.length && leaves.every((x) => x.b.type === "code")) {
    for (const x of [...leaves].reverse()) x.arr.splice(x.i, 1, ...codeToParas(x.b));
    return blocks;
  }
  const groups = [];
  for (const x of leaves) {
    if (!["p", "h", "code"].includes(x.b.type)) continue;
    const g = groups[groups.length - 1];
    if (g && g.arr === x.arr && g.end === x.i - 1) { g.items.push(x); g.end = x.i; }
    else groups.push({ arr: x.arr, start: x.i, end: x.i, items: [x] });
  }
  for (const g of groups.reverse()) {
    const text = g.items.map((x) => (x.b.type === "code" ? x.b.text : runsText(x.b.runs).replace(/​/g, ""))).join("\n");
    g.arr.splice(g.start, g.end - g.start + 1, { type: "code", lang: "", text });
  }
  return blocks;
}
// commonScope finds the container array holding both ends of the selection.
function commonScope(p1, p2) {
  let q = 0;
  while (q < p1.length - 1 && q < p2.length - 1 && p1[q].arr === p2[q].arr && p1[q].i === p2[q].i && p1[q].j === p2[q].j) q++;
  if (p1[q].arr !== p2[q].arr) return null;
  return { arr: p1[q].arr, i1: Math.min(p1[q].i, p2[q].i), i2: Math.max(p1[q].i, p2[q].i), depth: q };
}
function mergeLists(arr, i) {
  const b = arr[i];
  const nx = arr[i + 1], pv = arr[i - 1];
  if (nx && nx.type === "list" && nx.ordered === b.ordered) { b.items.push(...nx.items); arr.splice(i + 1, 1); }
  if (pv && pv.type === "list" && pv.ordered === b.ordered) { pv.items.push(...b.items); arr.splice(i, 1); }
}
export function toggleList(blocks, ordered) {
  const p1 = locate(blocks, M1);
  if (!p1) return blocks;
  const p2 = locate(blocks, M2) || p1;
  let k = -1;
  for (let q = 0; q < Math.min(p1.length, p2.length); q++) {
    if (p1[q].arr !== p2[q].arr || p1[q].i !== p2[q].i) break;
    if (p1[q].j !== undefined) k = q;
    if (p1[q].j !== p2[q].j) break;
  }
  if (k >= 0) {
    const S = p1[k], list = S.arr[S.i];
    if (list.ordered !== ordered) { list.ordered = ordered; list.start = 1; return blocks; }
    const j1 = Math.min(S.j, p2[k].j), j2 = Math.max(S.j, p2[k].j);
    const rep = [];
    if (j1 > 0) rep.push({ ...list, items: list.items.slice(0, j1) });
    for (const it of list.items.slice(j1, j2 + 1)) rep.push(...(it.blocks.length ? it.blocks : [{ type: "p", runs: [] }]));
    if (j2 + 1 < list.items.length) rep.push({ ...list, items: list.items.slice(j2 + 1), start: (list.start || 1) + j2 + 1 });
    S.arr.splice(S.i, 1, ...rep);
    return blocks;
  }
  const sc = commonScope(p1, p2);
  if (!sc) return blocks;
  const items = [];
  for (const b of sc.arr.slice(sc.i1, sc.i2 + 1)) {
    if (b.type === "list") items.push(...b.items);
    else items.push({ checked: null, blocks: [b.type === "h" ? { type: "p", runs: b.runs } : b] });
  }
  sc.arr.splice(sc.i1, sc.i2 - sc.i1 + 1, { type: "list", ordered, start: 1, loose: false, items });
  mergeLists(sc.arr, sc.i1);
  return blocks;
}
export function toggleQuote(blocks) {
  const p1 = locate(blocks, M1);
  if (!p1) return blocks;
  for (let q = p1.length - 2; q >= 0; q--) {
    const st = p1[q];
    if (st.j === undefined && st.arr[st.i].type === "quote") { st.arr.splice(st.i, 1, ...st.arr[st.i].blocks); return blocks; }
  }
  const sc = commonScope(p1, locate(blocks, M2) || p1);
  if (!sc) return blocks;
  sc.arr.splice(sc.i1, sc.i2 - sc.i1 + 1, { type: "quote", blocks: sc.arr.slice(sc.i1, sc.i2 + 1) });
  return blocks;
}
function indentAt(path, k, j2) {
  const S = path[k], list = S.arr[S.i], j1 = S.j;
  if (j1 === 0) return false;
  const prev = list.items[j1 - 1];
  const moved = list.items.splice(j1, j2 - j1 + 1);
  const last = prev.blocks[prev.blocks.length - 1];
  if (last && last.type === "list") last.items.push(...moved);
  else prev.blocks.push({ type: "list", ordered: list.ordered, start: 1, loose: false, items: moved });
  return true;
}
function outdentAt(path, k, j2 = path[k].j) {
  const S = path[k], list = S.arr[S.i], j1 = S.j;
  const moved = list.items.slice(j1, j2 + 1), tail = list.items.slice(j2 + 1);
  const P = k > 0 && path[k - 1].j !== undefined ? path[k - 1] : null;
  if (P) {
    const outer = P.arr[P.i];
    list.items.splice(j1);
    if (tail.length) moved[moved.length - 1].blocks.push({ type: "list", ordered: list.ordered, start: 1, loose: list.loose, items: tail });
    if (!list.items.length) S.arr.splice(S.i, 1);
    outer.items.splice(P.j + 1, 0, ...moved);
    return;
  }
  const rep = [];
  if (j1 > 0) rep.push({ ...list, items: list.items.slice(0, j1) });
  for (const it of moved) rep.push(...(it.blocks.length ? it.blocks : [{ type: "p", runs: [] }]));
  if (tail.length) rep.push({ ...list, items: tail, start: (list.start || 1) + j2 + 1 });
  S.arr.splice(S.i, 1, ...rep);
}
const listStep = (path) => { for (let q = path.length - 1; q >= 0; q--) if (path[q].j !== undefined) return q; return -1; };
export function indentList(blocks, outdent) {
  const p1 = locate(blocks, M1);
  if (!p1) return blocks;
  const k = listStep(p1);
  if (k < 0) return blocks;
  const p2 = locate(blocks, M2);
  let j2 = p1[k].j;
  if (p2 && p2.length > k && p2[k].arr === p1[k].arr && p2[k].i === p1[k].i && p2[k].j !== undefined) j2 = Math.max(j2, p2[k].j);
  if (outdent) outdentAt(p1, k, j2); else indentAt(p1, k, j2);
  return blocks;
}
const noLink = (m) => { const n = cleanMarks(m); delete n.l; return n; };
// Enter: split the block at the caret (list items, quotes and headings follow
// word-processor rules: an empty item outdents, an empty quote line exits).
export function splitBlock(blocks) {
  const path = locate(blocks, M1);
  if (!path) return blocks;
  const leaf = path[path.length - 1], b = leaf.arr[leaf.i];
  if (leaf.row !== undefined || b.type === "code") return blocks;
  const parent = path[path.length - 2];
  const [before, after, marks] = splitAt(b.runs);
  const caret = caretRun(noLink(marks));
  if (parent && parent.j !== undefined && leaf.i === 0) {
    const list = parent.arr[parent.i], item = list.items[parent.j];
    if (!hasContent(b.runs) && item.blocks.length === 1) { outdentAt(path, path.length - 2); return blocks; }
    const fresh = { checked: item.checked != null ? false : null, blocks: [{ type: "p", runs: [caret, ...after] }, ...item.blocks.slice(1)] };
    item.blocks = [{ ...b, runs: before }];
    list.items.splice(parent.j + 1, 0, fresh);
    return blocks;
  }
  if (parent && parent.j === undefined && b.type === "p" && !hasContent(b.runs) && parent.arr[parent.i].type === "quote") {
    const q = parent.arr[parent.i];
    const rep = [];
    if (leaf.i > 0) rep.push({ type: "quote", blocks: q.blocks.slice(0, leaf.i) });
    rep.push({ type: "p", runs: [caretRun()] });
    if (leaf.i + 1 < q.blocks.length) rep.push({ type: "quote", blocks: q.blocks.slice(leaf.i + 1) });
    parent.arr.splice(parent.i, 1, ...rep);
    return blocks;
  }
  let first = { ...b, runs: before }, second;
  if (b.type === "h") {
    if (!hasContent(before) && hasContent(after)) { first = { type: "p", runs: [] }; second = { ...b, runs: [caretRun(), ...after] }; }
    else second = hasContent(after) ? { ...b, runs: [caretRun(), ...after] } : { type: "p", runs: [caretRun()] };
  } else second = { type: "p", runs: [caret, ...after] };
  leaf.arr.splice(leaf.i, 1, first, second);
  return blocks;
}
function lastText(b) {
  if (!b) return null;
  if (b.type === "p" || b.type === "h") return b;
  if (b.type === "quote") return lastText(b.blocks[b.blocks.length - 1]);
  if (b.type === "list") { const it = b.items[b.items.length - 1]; return it ? lastText(it.blocks[it.blocks.length - 1]) : null; }
  return null;
}
// Backspace at the start of a block.
export function joinBackward(blocks) {
  const path = locate(blocks, M1);
  if (!path) return blocks;
  const leaf = path[path.length - 1];
  if (leaf.row !== undefined) return blocks;
  const b = leaf.arr[leaf.i], parent = path[path.length - 2];
  if (b.type === "h") { leaf.arr[leaf.i] = { type: "p", runs: b.runs }; return blocks; }
  if (b.type === "code") { leaf.arr.splice(leaf.i, 1, ...codeToParas(b)); return blocks; }
  if (parent && parent.j !== undefined && leaf.i === 0) { outdentAt(path, path.length - 2); return blocks; }
  if (parent && parent.j === undefined && leaf.i === 0 && parent.arr[parent.i].type === "quote") {
    const q = parent.arr[parent.i];
    q.blocks.shift();
    if (q.blocks.length) parent.arr.splice(parent.i, 0, b); else parent.arr.splice(parent.i, 1, b);
    return blocks;
  }
  const arr = leaf.arr, i = leaf.i, prev = arr[i - 1];
  if (!prev) return blocks;
  if (prev.type === "p" || prev.type === "h") { prev.runs = [...prev.runs, ...b.runs]; arr.splice(i, 1); }
  else if (prev.type === "hr" || prev.type === "raw") arr.splice(i - 1, 1);
  else if (prev.type === "code") { prev.text += (prev.text ? "\n" : "") + runsText(b.runs); arr.splice(i, 1); }
  else if (prev.type === "list" || prev.type === "quote") { const t = lastText(prev); if (t) { t.runs = [...t.runs, ...b.runs]; arr.splice(i, 1); } }
  else if (prev.type === "table") {
    const row = prev.rows[prev.rows.length - 1];
    const [bf, af] = splitAt(b.runs);
    row[row.length - 1] = [...row[row.length - 1], caretRun()];
    b.runs = [...bf, ...af];
    if (!hasContent(b.runs)) arr.splice(i, 1);
  }
  return blocks;
}
// Delete at the end of a block.
export function joinForward(blocks) {
  const path = locate(blocks, M1);
  if (!path) return blocks;
  const leaf = path[path.length - 1];
  if (leaf.row !== undefined) return blocks;
  const b = leaf.arr[leaf.i], parent = path[path.length - 2];
  if (b.type !== "p" && b.type !== "h") return blocks;
  const arr = leaf.arr, i = leaf.i, next = arr[i + 1];
  if (!next && parent && parent.j !== undefined) {
    const list = parent.arr[parent.i], ni = list.items[parent.j + 1];
    if (!ni) return blocks;
    const f = ni.blocks[0];
    if (f && (f.type === "p" || f.type === "h")) { b.runs = [...b.runs, ...f.runs]; ni.blocks.shift(); }
    list.items[parent.j].blocks.push(...ni.blocks);
    list.items.splice(parent.j + 1, 1);
    return blocks;
  }
  if (!next) return blocks;
  if (next.type === "p" || next.type === "h") { b.runs = [...b.runs, ...next.runs]; arr.splice(i + 1, 1); }
  else if (next.type === "hr" || next.type === "raw") arr.splice(i + 1, 1);
  else if (next.type === "list" || next.type === "quote") {
    const holder = next.type === "list" ? next.items[0] && next.items[0].blocks : next.blocks;
    const f = holder && holder[0];
    if (f && (f.type === "p" || f.type === "h")) {
      b.runs = [...b.runs, ...f.runs];
      holder.shift();
      if (next.type === "list" && !holder.length) next.items.shift();
      if ((next.type === "list" && !next.items.length) || (next.type === "quote" && !next.blocks.length)) arr.splice(i + 1, 1);
    }
  }
  return blocks;
}
function appendCaret(blocks) {
  const last = blocks[blocks.length - 1];
  if (!last) return false;
  if (last.type === "code") { last.text += M1; return true; }
  const t = lastText(last);
  if (t) { t.runs.push(caretRun()); return true; }
  return false;
}
const containsCaret = (blocks) => !!locate(blocks, M1);
// insertBlocks puts blocks at the caret: one paragraph is inserted inline,
// several split the current block; inside a list they become list items.
export function insertBlocks(blocks, ins, opts = {}) {
  const path = locate(blocks, M1);
  ins = ins.map((b) => b);
  const preset = containsCaret(ins);
  if (!path) { blocks.push(...ins); return blocks; }
  const leaf = path[path.length - 1];
  const b = leaf.arr[leaf.i];
  const inline = !opts.blocks && ins.length === 1 && ins[0].type === "p";
  if (leaf.row !== undefined || (b.type === "h" && !opts.blocks) || inline) {
    // Inline insertions take on the formatting around the caret.
    const runs = inline ? ins[0].runs : flattenBlocks(ins);
    const [bf, af, cm] = splitAt(leafRuns(leaf));
    const ctxm = cleanMarks(cm);
    const out = [];
    for (const r of [...bf, ...runs.map((r) => (r.t !== undefined ? { t: r.t, m: { ...ctxm, ...cleanMarks(r.m) } } : r)), ...(preset ? [] : [caretRun(ctxm)]), ...af]) {
      pushRun(out, r.t !== undefined ? { t: r.t, m: r.m } : r);
    }
    setLeafRuns(leaf, out);
    return blocks;
  }
  if (b.type === "code") {
    b.text = b.text.replace(M1, flattenBlocks(ins).map((r) => (r.t !== undefined ? r.t : "")).join("") + M1);
    return blocks;
  }
  const parent = path[path.length - 2];
  const [bf, af, marks] = splitAt(b.runs);
  if (parent && parent.j !== undefined && leaf.i === 0) {
    const list = parent.arr[parent.i], item = list.items[parent.j];
    const fresh = [];
    for (const x of ins) {
      if (x.type === "list") fresh.push(...x.items);
      else fresh.push({ checked: null, blocks: [x.type === "h" ? { type: "p", runs: x.runs } : x] });
    }
    const placed = preset || appendCaret(fresh[fresh.length - 1].blocks);
    const rest = item.blocks.slice(1);
    const tail = hasContent(af) || rest.length || !placed ? { checked: item.checked != null ? false : null, blocks: [{ type: "p", runs: [...(placed ? [] : [caretRun()]), ...af] }, ...rest] } : null;
    const emptyHead = !hasContent(bf);
    item.blocks = [{ ...b, runs: bf }];
    list.items.splice(parent.j + (emptyHead ? 0 : 1), emptyHead ? 1 : 0, ...fresh, ...(tail ? [tail] : []));
    return blocks;
  }
  const out = [];
  if (hasContent(bf)) out.push({ ...b, runs: bf });
  out.push(...ins);
  const placed = preset || appendCaret(ins);
  if (hasContent(af) || !placed) out.push({ type: b.type === "h" ? "h" : "p", level: b.level, runs: [...(placed ? [] : [caretRun(noLink(marks))]), ...af] });
  leaf.arr.splice(leaf.i, 1, ...out);
  return blocks;
}
export function newTable(cols = 3, rows = 3) {
  return { type: "table", align: Array(cols).fill(null), rows: Array.from({ length: rows }, (_, r) => Array.from({ length: cols }, (_, c) => (r === 0 && c === 0 ? [caretRun()] : []))) };
}
export function tableOp(blocks, op) {
  const path = locate(blocks, M1);
  const leaf = path && path[path.length - 1];
  if (!leaf || leaf.row === undefined) return blocks;
  const t = leaf.arr[leaf.i];
  const ncol = Math.max(...t.rows.map((r) => r.length));
  t.align = Array.from({ length: ncol }, (_, k) => (t.align && t.align[k]) || null);
  for (const r of t.rows) while (r.length < ncol) r.push([]);
  const { row, col } = leaf;
  const [bf, af] = splitAt(t.rows[row][col]);
  t.rows[row][col] = [...bf, ...af];
  const empty = () => Array.from({ length: ncol }, () => []);
  const put = (r, c) => { t.rows[r][c] = [...t.rows[r][c], caretRun()]; };
  const drop = () => leaf.arr.splice(leaf.i, 1, { type: "p", runs: [caretRun()] });
  switch (op) {
    case "row-add": t.rows.splice(row + 1, 0, empty()); put(row + 1, col); break;
    case "col-add": for (const r of t.rows) r.splice(col + 1, 0, []); t.align.splice(col + 1, 0, null); put(row, col + 1); break;
    case "row-del": if (t.rows.length <= 1) drop(); else { t.rows.splice(row, 1); put(Math.min(row, t.rows.length - 1), col); } break;
    case "col-del": if (ncol <= 1) drop(); else { for (const r of t.rows) r.splice(col, 1); t.align.splice(col, 1); put(row, Math.min(col, ncol - 2)); } break;
    case "table-del": drop(); break;
    case "next": { let r = row, c = col + 1; if (c >= ncol) { r++; c = 0; } if (r >= t.rows.length) t.rows.push(empty()); put(r, c); break; }
    case "prev": { let r = row, c = col - 1; if (c < 0) { r--; c = ncol - 1; } if (r < 0) { r = 0; c = 0; } put(r, c); break; }
    case "down": { if (row + 1 >= t.rows.length) t.rows.push(empty()); put(row + 1, col); break; }
    default: put(row, col);
  }
  return blocks;
}
// replacePrefix turns "## |" style typed prefixes into blocks (autoformat).
export function autoformat(blocks, kind, arg) {
  const path = locate(blocks, M1);
  const leaf = path && path[path.length - 1];
  if (!leaf || leaf.row !== undefined) return blocks;
  const b = leaf.arr[leaf.i];
  if (b.type !== "p") return blocks;
  const [, after] = splitAt(b.runs);
  const p = { type: "p", runs: [caretRun(), ...after] };
  let nb;
  switch (kind) {
    case "h": nb = { type: "h", level: arg, runs: p.runs }; break;
    case "ul": nb = { type: "list", ordered: false, start: 1, loose: false, items: [{ checked: null, blocks: [p] }] }; break;
    case "ol": nb = { type: "list", ordered: true, start: arg || 1, loose: false, items: [{ checked: null, blocks: [p] }] }; break;
    case "quote": nb = { type: "quote", blocks: [p] }; break;
    case "code": nb = { type: "code", lang: arg || "", text: runsText(p.runs) }; break;
    case "hr": leaf.arr.splice(leaf.i, 1, { type: "hr" }, { type: "p", runs: [caretRun()] }); return blocks;
    default: return blocks;
  }
  leaf.arr[leaf.i] = nb;
  return blocks;
}
// deleteSelection removes everything between M1 and M2. Text blocks at both
// ends are merged (word-processor style); tables and code blocks are only
// trimmed so their structure survives.
export function deleteSelection(blocks) {
  const p1 = locate(blocks, M1), p2 = locate(blocks, M2);
  if (!p1 || !p2) return blocks;
  const l1 = p1[p1.length - 1], l2 = p2[p2.length - 1];
  if (l1.arr === l2.arr && l1.i === l2.i && l1.row === l2.row && l1.col === l2.col) {
    const b = l1.arr[l1.i];
    if (b.type === "code") { b.text = b.text.replace(/[\s\S]*/, M1); return blocks; }
    const [bf, rest, m] = splitAt(leafRuns(l1), M1);
    const [, af] = splitAt(rest, M2);
    setLeafRuns(l1, [...bf, caretRun(m), ...af]);
    return blocks;
  }
  const st = { phase: 0, keep: null };
  const isText = (b) => b.type === "p" || b.type === "h";
  const visit = (arr) => {
    for (let i = 0; i < arr.length && st.phase < 2; i++) {
      const b = arr[i];
      if (b.type === "quote") { visit(b.blocks); if (st.phase > 0 && !b.blocks.length) arr.splice(i--, 1); continue; }
      if (b.type === "list") {
        for (let j = 0; j < b.items.length && st.phase < 2; j++) {
          visit(b.items[j].blocks);
          if (st.phase > 0 && !b.items[j].blocks.length) b.items.splice(j--, 1);
        }
        if (st.phase > 0 && !b.items.length) arr.splice(i--, 1);
        continue;
      }
      if (b.type === "table") {
        const t = leafText(b);
        if (st.phase === 1 && !t.includes(M2)) { arr.splice(i--, 1); continue; }
        if (st.phase === 0 && !t.includes(M1)) continue;
        cells: for (const row of b.rows) for (let c = 0; c < row.length; c++) {
          const ct = runsText(row[c]);
          if (st.phase === 0) {
            if (!ct.includes(M1)) continue;
            const [bf, , m] = splitAt(row[c], M1);
            row[c] = [...bf, caretRun(m)];
            st.keep = { cell: true };
            st.phase = 1;
          } else if (ct.includes(M2)) { row[c] = splitAt(row[c], M2)[1]; st.phase = 2; break cells; }
          else row[c] = [];
        }
        continue;
      }
      if (isText(b) || b.type === "code") {
        const t = leafText(b);
        if (st.phase === 0) {
          if (!t.includes(M1)) continue;
          if (b.type === "code") b.text = b.text.slice(0, b.text.indexOf(M1) + 1);
          else { const [bf, , m] = splitAt(b.runs, M1); b.runs = [...bf, caretRun(m)]; }
          st.keep = b;
          st.phase = 1;
          continue;
        }
        if (!t.includes(M2)) { arr.splice(i--, 1); continue; }
        st.phase = 2;
        const k = st.keep;
        if (b.type === "code") {
          const tail = b.text.slice(b.text.indexOf(M2) + 1);
          if (k.type === "code") { k.text += tail; arr.splice(i--, 1); } else b.text = tail;
        } else {
          const af = splitAt(b.runs, M2)[1];
          if (k.type === "p" || k.type === "h") { k.runs = [...k.runs, ...af]; arr.splice(i--, 1); } else b.runs = af;
        }
        return;
      }
      if (st.phase === 1) arr.splice(i--, 1);
    }
  };
  visit(blocks);
  return blocks;
}
function insertRunsAtCaret(blocks, make) {
  const path = locate(blocks, M1);
  if (!path) return blocks;
  const leaf = path[path.length - 1], b = leaf.arr[leaf.i];
  const [bf, af, m] = splitAt(leafRuns(leaf));
  const out = [];
  for (const r of [...bf, ...make(m, leaf.row !== undefined), caretRun(m), ...af]) pushRun(out, r.t !== undefined ? { t: r.t, m: r.m } : r);
  if (b.type !== "code") setLeafRuns(leaf, out);
  return blocks;
}
// typeText inserts plain text at the caret, inheriting the caret's marks.
export function typeText(blocks, text) {
  const path = locate(blocks, M1);
  if (!path) return blocks;
  const leaf = path[path.length - 1], b = leaf.arr[leaf.i];
  if (b.type === "code" && leaf.row === undefined) { b.text = b.text.replace(M1, () => text + M1); return blocks; }
  return insertRunsAtCaret(blocks, (m, cell) => {
    const out = [];
    (cell ? [text.replace(/\s*\n\s*/g, " ")] : text.split("\n")).forEach((l, k) => { if (k) out.push({ a: "br", m: {} }); if (l) out.push({ t: l, m }); });
    return out;
  });
}
export function insertBreak(blocks) {
  const path = locate(blocks, M1);
  const leaf = path && path[path.length - 1];
  if (!leaf) return blocks;
  if (leaf.arr[leaf.i].type === "code") return typeText(blocks, "\n");
  return insertRunsAtCaret(blocks, () => [{ a: "br", m: {} }]);
}
// codeEnter: Enter inside a code block keeps the line's indentation; Enter on
// an empty last line leaves the block.
export function codeEnter(blocks) {
  const path = locate(blocks, M1);
  const leaf = path && path[path.length - 1];
  const b = leaf && leaf.arr[leaf.i];
  if (!b || b.type !== "code") return blocks;
  if (b.text.endsWith("\n" + M1)) {
    b.text = b.text.slice(0, -2);
    leaf.arr.splice(leaf.i + 1, 0, { type: "p", runs: [caretRun()] });
    return blocks;
  }
  const before = b.text.slice(0, b.text.indexOf(M1));
  const ind = /^[ \t]*/.exec(before.slice(before.lastIndexOf("\n") + 1))[0];
  b.text = b.text.replace(M1, "\n" + ind + M1);
  return blocks;
}
export function codeIndent(blocks, outdent) {
  const path = locate(blocks, M1);
  const leaf = path && path[path.length - 1];
  const b = leaf && leaf.arr[leaf.i];
  if (!b || b.type !== "code") return blocks;
  const t = b.text, s = t.indexOf(M1), e = t.includes(M2) ? t.indexOf(M2) : s;
  if (!outdent && s === e) { b.text = t.replace(M1, "  " + M1); return blocks; }
  const ls = t.lastIndexOf("\n", s - 1) + 1;
  let le = t.indexOf("\n", e);
  if (le < 0) le = t.length;
  const seg = t.slice(ls, le).split("\n").map((l) => (outdent ? l.replace(/^([]*)( {1,2}|\t)/, "$1") : "  " + l)).join("\n");
  b.text = t.slice(0, ls) + seg + t.slice(le);
  return blocks;
}
// exitBlock adds an empty paragraph after the given top-level blocks and puts
// the caret there (Ctrl+Enter out of a table or code block).
export function exitBlock(blocks) {
  const strip = (s) => s.replace(/[]/g, "");
  eachRuns(blocks, (runs) => runs.map((r) => (r.t !== undefined ? { t: strip(r.t), m: r.m } : r)).filter((r) => r.t !== ""), (b) => { b.text = strip(b.text); });
  blocks.push({ type: "p", runs: [caretRun()] });
  return blocks;
}

function stripMarkers(blocks) {
  const strip = (s) => s.replace(/[]/g, "");
  eachRuns(blocks, (runs) => runs.map((r) => (r.t !== undefined ? { t: strip(r.t), m: r.m } : r)).filter((r) => r.t !== ""), (b) => { b.text = strip(b.text); });
}
// insertVideo puts a video block at the caret. Videos are players only at
// the top level, so inside a list, a quote or a table it goes after them.
export function insertVideo(blocks, line) {
  const v = { type: "raw", kind: "video", text: line };
  const path = locate(blocks, M1);
  if (!path) { blocks.push(v, { type: "p", runs: [caretRun()] }); return blocks; }
  const b = path[0].arr[path[0].i];
  if (path.length === 1 && b.type === "p" && !hasContent(b.runs)) {
    blocks.splice(path[0].i, 1, v, { type: "p", runs: [caretRun()] });
    return blocks;
  }
  if (path.length === 1 && path[0].row === undefined && (b.type === "p" || b.type === "h")) return insertBlocks(blocks, [v], { blocks: true });
  stripMarkers(blocks);
  blocks.splice(path[0].i + 1, 0, v, { type: "p", runs: [caretRun()] });
  return blocks;
}
// pasteVideo turns a video link pasted on an empty top-level line into a
// video block; null when it does not apply (the text is pasted as usual).
export function pasteVideo(blocks, text) {
  const line = String(text ?? "").trim();
  if (line.includes("\n") || !videoLine(line)) return null;
  const path = locate(blocks, M1);
  if (!path || path.length !== 1 || hasSelection(blocks)) return null;
  const b = path[0].arr[path[0].i];
  if (b.type !== "p" || hasContent(b.runs)) return null;
  blocks.splice(path[0].i, 1, { type: "raw", kind: "video", text: line }, { type: "p", runs: [caretRun()] });
  return blocks;
}

/* ============================================================== browser == */
// Everything below runs only on the post editor page. The textarea stays the
// source of truth: the visual surface is rendered from it and serialized back
// into it (debounced, and synchronously before submit, mode switches and API
// calls), firing `input` so word count, autosave and the unsaved-changes
// guard keep working unchanged.
const STRUCT_RE = /^(?:P|H[1-6]|UL|OL|LI|BLOCKQUOTE|PRE|TABLE|THEAD|TBODY|TFOOT|TR|TD|TH|HR|DIV)$/;
const HOLDER_RE = /^(?:P|H[1-6]|PRE|TD|TH|LI)$/;
const TOP_RE = /^(?:P|H[1-6]|UL|OL|BLOCKQUOTE|PRE|TABLE|HR)$/;
const HOLDERS = "p,h1,h2,h3,h4,h5,h6,pre,td,th,li";
const MIME = "application/x-sap13ns-md";

function setupEditor() {
  const root = document.querySelector("[data-editor]");
  const ta = root && root.querySelector("[data-editor-text]");
  const surface = root && root.querySelector("[data-editor-visual]");
  if (!ta || !surface || root.hasAttribute("data-editor-ready")) return;
  root.setAttribute("data-editor-ready", "");
  const $ = (s, r = root) => r.querySelector(s);
  const $$ = (s, r = root) => Array.from(r.querySelectorAll(s));
  const form = ta.form;
  const preview = $("[data-editor-preview]");
  const tools = $("[data-editor-tools]");
  const toolbar = $(".editor-toolbar");
  const say = (k, fb) => root.dataset["msg" + k] || fb;
  // The labels of raw Markdown boxes, in the page language.
  Object.assign(LABELS, { footnote: say("Footnote", LABELS.footnote), ref: say("LinkRefs", LABELS.ref), shortcode: say("Shortcode", LABELS.shortcode), video: say("Video", LABELS.video) });
  Object.assign(VIDEO_TEXT, {
    watch: say("VideoWatch", VIDEO_TEXT.watch), play: say("VideoPlay", VIDEO_TEXT.play), untitled: say("VideoUntitled", VIDEO_TEXT.untitled),
    playUntitled: say("VideoPlayUntitled", VIDEO_TEXT.playUntitled), off: say("VideoOff", ""),
  });
  const csrf = () => (document.querySelector('input[name="csrf"]') || {}).value || "";
  const toast = (category, title) => document.dispatchEvent(new CustomEvent("basecoat:toast", { detail: { config: { category, title: escHTML(title) } } }));
  const storeKey = "sap13ns:editor-mode:" + (root.dataset.editorUser || "0");
  const isEl = (n) => !!n && n.nodeType === 1;
  const inSurface = (n) => !!n && (n === surface || surface.contains(n));
  const elOf = (n) => (isEl(n) ? n : n && n.parentNode);
  const closest = (n, sel) => { const e = elOf(n); const f = e && e.closest(sel); return f && f !== surface && surface.contains(f) ? f : null; };
  const tops = () => Array.from(surface.childNodes);
  const topOf = (n) => { while (n && n.parentNode !== surface) n = n.parentNode; return n || null; };
  const indexIn = (n) => Array.prototype.indexOf.call(n.parentNode.childNodes, n);
  const blockEl = (n) => isEl(n) && STRUCT_RE.test(n.tagName);

  let mode = null, editMode = "visual";
  let lastMd = null;       // Markdown the surface was rendered from / last synced
  let trail = "";          // trailing newline of the original source, kept on sync
  let preserve = new Map(); // canonical block Markdown -> original source text
  let segMap = new Map();  // top-level node -> {start, end} in ta.value
  let dirty = false, stale = false, syncing = false, syncTimer = 0;
  let lastRange = null, mediaPending = false;

  /* ---------------------------------------------------------- render/sync */
  function render(md) {
    const blocks = parseMarkdown(md);
    preserve = new Map();
    for (const b of blocks) preserve.set(blockSig(b), b.src);
    surface.innerHTML = toHTML(blocks);
    segMap = new Map();
    tops().forEach((n, k) => { if (blocks[k]) segMap.set(n, { start: blocks[k].o0, end: blocks[k].o1 }); });
    trail = /\n$/.test(md) ? "\n" : "";
    lastMd = md;
    dirty = false;
    stale = false;
    lastRange = null;
    fixStructure();
  }
  function serialize() {
    const units = tops().filter((n) => isEl(n) || (n.nodeType === 3 && n.data.trim()));
    const entries = [], owner = [];
    units.forEach((n, u) => {
      for (const b of readNodes([n])) {
        const md = blockSig(b);
        entries.push({ block: b, md, src: preserve.has(md) ? preserve.get(md) : null });
        owner.push(u);
      }
    });
    const r = joinTop(entries);
    const map = new Map();
    for (const s of r.segs) {
      const n = units[owner[s.k]], cur = map.get(n);
      map.set(n, cur ? { start: cur.start, end: s.end } : { start: s.start, end: s.end });
    }
    return { md: r.md ? r.md + trail : "", map };
  }
  function emitInput() {
    syncing = true;
    try { ta.dispatchEvent(new Event("input", { bubbles: true })); } finally { syncing = false; }
  }
  function flushSync() {
    clearTimeout(syncTimer);
    syncTimer = 0;
    if (!dirty) return;
    dirty = false;
    const r = serialize();
    segMap = r.map;
    lastMd = r.md;
    if (r.md !== ta.value) { ta.value = r.md; emitInput(); }
  }
  function touch() {
    dirty = true;
    clearTimeout(syncTimer);
    syncTimer = setTimeout(flushSync, 150);
    updatePlaceholder();
  }
  // fixStructure keeps the surface editable: never empty, and ending in a
  // paragraph when the last block cannot take a caret after it (table, code).
  function fixStructure() {
    const last = surface.lastElementChild;
    if (!last || /^(?:TABLE|PRE|HR|DIV)$/.test(last.tagName)) surface.appendChild(Object.assign(document.createElement("p"), { innerHTML: "<br>" }));
    updatePlaceholder();
  }
  function updatePlaceholder() {
    const f = surface.firstChild;
    const empty = surface.childNodes.length === 1 && f.nodeName === "P" && !surface.textContent && !surface.querySelector("img,[data-raw]");
    surface.toggleAttribute("data-empty", empty);
  }
  // External changes: autosave restore, AI tools or the media picker set the
  // textarea and fire `input` (or `sap:content-set`); re-render from it.
  function external() {
    if (syncing) return;
    if (ta.value === lastMd) return;
    if (mode !== "visual") { stale = true; return; }
    const hadFocus = document.activeElement === surface || mediaPending;
    const pos = Math.max(0, Math.min(ta.selectionEnd, ta.value.length) - 1);
    render(ta.value);
    if (hadFocus) { surface.focus({ preventScroll: true }); caretAtSource(pos); }
    mediaPending = false;
    pushState();
  }
  ta.addEventListener("input", external);
  ta.addEventListener("sap:content-set", external);
  if (form) {
    form.addEventListener("submit", () => { if (mode === "visual") flushSync(); }, true);
    form.addEventListener("formdata", (e) => { if (mode === "visual") { flushSync(); e.formData.set(ta.name, ta.value); } });
  }
  window.addEventListener("beforeunload", () => flushSync());
  document.addEventListener("visibilitychange", () => { if (document.hidden) flushSync(); });
  surface.addEventListener("focusout", () => flushSync());
  // Context bars (code language, table) go away when focus leaves the editor.
  root.addEventListener("focusout", () => setTimeout(() => {
    if ((popOpen === "code" || popOpen === "table" || popOpen === "raw" || popOpen === "imagebar") && !root.contains(document.activeElement)) closePop(false);
  }, 0));

  /* --------------------------------------------------------------- videos */
  // Video blocks are rendered at once and filled in with the title and the
  // poster the server knows (/admin/api/video), once per address.
  const videoReqs = new Map();
  function videoInfo(url) {
    let p = videoReqs.get(url);
    if (!p) {
      p = fetch("/admin/api/video?url=" + encodeURIComponent(url), { credentials: "same-origin", headers: { Accept: "application/json" } })
        .then((res) => (res.ok ? res.json() : null))
        .catch(() => null)
        .then((d) => {
          if (!d) { videoReqs.delete(url); return null; }
          const meta = { title: String(d.title || ""), poster: String(d.poster || ""), width: +d.width || 0, height: +d.height || 0, portrait: !!d.portrait, mode: String(d.mode || "") };
          VIDEO_META.set(url, meta);
          return meta;
        });
      videoReqs.set(url, p);
    }
    return p;
  }
  function hydrateVideos() {
    for (const el of surface.querySelectorAll("div.md-video:not([data-video-ready])")) {
      el.setAttribute("data-video-ready", "");
      const line = el.getAttribute("data-raw") || "";
      const v = videoLine(line);
      if (!v) continue;
      videoInfo(v.url).then((meta) => {
        if (!meta || !el.isConnected || el.getAttribute("data-raw") !== line || el.querySelector("iframe")) return;
        const tpl = document.createElement("template");
        tpl.innerHTML = blockHTML({ type: "raw", kind: "video", text: line });
        if (tpl.content.firstChild) el.replaceChildren(...tpl.content.firstChild.childNodes);
      });
    }
  }
  new MutationObserver(hydrateVideos).observe(surface, { childList: true });

  /* ------------------------------------------------------------ selection */
  function curRange() {
    const sel = window.getSelection();
    if (!sel || !sel.rangeCount) return null;
    const r = sel.getRangeAt(0);
    return inSurface(r.startContainer) && inSurface(r.endContainer) && r.startContainer !== surface.parentNode ? r : null;
  }
  function useRange() {
    const r = curRange();
    if (r) return r;
    if (lastRange && inSurface(lastRange.startContainer) && inSurface(lastRange.endContainer)) {
      const sel = window.getSelection();
      sel.removeAllRanges();
      sel.addRange(lastRange);
      return sel.getRangeAt(0);
    }
    const p = surface.lastElementChild;
    if (!p) return null;
    const r2 = document.createRange();
    r2.selectNodeContents(p);
    r2.collapse(false);
    const sel = window.getSelection();
    sel.removeAllRanges();
    sel.addRange(r2);
    return sel.getRangeAt(0);
  }
  function setSel(a, b) {
    const sel = window.getSelection();
    const z = b || a;
    try { sel.setBaseAndExtent(a.node, a.off, z.node, z.off); } catch (_) { /* detached */ }
    lastRange = sel.rangeCount ? sel.getRangeAt(0).cloneRange() : null;
  }
  function holderOf(n) {
    for (let e = elOf(n); e && e !== surface; e = e.parentNode) {
      if (HOLDER_RE.test(e.tagName)) return e;
      if (/^(?:UL|OL|BLOCKQUOTE|TABLE|THEAD|TBODY|TFOOT|TR)$/.test(e.tagName)) return null;
    }
    return null;
  }
  // A list item holds text only when its content starts inline (tight lists).
  const leafHolder = (h) => h.tagName !== "LI" || !blockEl(Array.from(h.childNodes).find((c) => c.nodeType !== 3 || c.data !== ""));
  const holderList = () => Array.from(surface.querySelectorAll(HOLDERS)).filter((h) => leafHolder(h) && !h.closest("[data-raw]"));
  function holderEnd(h) {
    let k = h.childNodes.length;
    if (h.tagName === "LI") { const i = Array.from(h.childNodes).findIndex(blockEl); if (i >= 0) k = i; }
    if (k > 0 && h.childNodes[k - 1].nodeName === "BR") k--;
    return { node: h, off: k };
  }
  function cmpPoint(a, b) {
    const r = document.createRange();
    r.setStart(a.node, a.off);
    return -r.comparePoint(b.node, b.off); // >0: a after b
  }
  // normPoint moves a selection point to a text position inside a holder.
  function normPoint(node, off, isEnd) {
    for (let e = elOf(node); e && e !== surface; e = e.parentNode) {
      if (e.hasAttribute("data-raw")) {
        const inline = e.tagName !== "DIV";
        node = e.parentNode;
        off = indexIn(e) + (inline || isEnd ? 1 : 0);
        if (inline) return { node, off };
        break;
      }
    }
    if (node.nodeType === 3) return { node, off };
    const h = holderOf(node);
    const before = node.childNodes[off - 1], after = node.childNodes[off];
    let structural = !h || (node === h && h.tagName === "LI" && (blockEl(before) || (!before && blockEl(after))));
    if (!structural) {
      // After a holder's trailing <br> placeholder means "end of its text".
      if (isEl(node) && before && before.nodeName === "BR" && !Array.from(node.childNodes).slice(off).some((c) => c.nodeType !== 3 || c.data)) return { node, off: off - 1 };
      return { node, off };
    }
    const pt = { node, off }, list = holderList();
    if (!isEnd) {
      for (const x of list) if (cmpPoint(holderEnd(x), pt) >= 0) return cmpPoint({ node: x, off: 0 }, pt) > 0 ? { node: x, off: 0 } : pt;
      const l = list[list.length - 1];
      return l ? holderEnd(l) : pt;
    }
    for (const x of list.slice().reverse()) if (cmpPoint({ node: x, off: 0 }, pt) <= 0) return cmpPoint(holderEnd(x), pt) < 0 ? holderEnd(x) : pt;
    return list[0] ? { node: list[0], off: 0 } : pt;
  }
  function textBefore(h, pt) {
    const r = document.createRange();
    r.setStart(h, 0);
    r.setEnd(pt.node, pt.off);
    const f = r.cloneContents();
    return { text: r.toString().replace(/​/g, ""), atoms: !!f.querySelector("img,[data-raw],br") };
  }
  function textAfter(h, pt) {
    const r = document.createRange();
    r.setStart(pt.node, pt.off);
    const e = holderEnd(h);
    r.setEnd(e.node, e.off);
    const f = r.cloneContents();
    return { text: r.toString().replace(/​/g, ""), atoms: !!f.querySelector("img,[data-raw]") };
  }
  const atHolderStart = (h, pt) => { const t = textBefore(h, pt); return !t.text && !t.atoms; };
  const atHolderEnd = (h, pt) => { const t = textAfter(h, pt); return !t.text && !t.atoms; };
  function insertMarker(pt, ch) {
    if (pt.node.nodeType === 3) { pt.node.insertData(pt.off, ch); return; }
    pt.node.insertBefore(document.createTextNode(ch), pt.node.childNodes[pt.off] || null);
  }
  // takeMarkers removes the M1/M2 markers rendered into fresh nodes and turns
  // them into the selection.
  function takeMarkers(nodes) {
    const texts = [];
    for (const n of nodes) {
      if (n.nodeType === 3) { texts.push(n); continue; }
      if (!isEl(n)) continue;
      const tw = document.createTreeWalker(n, NodeFilter.SHOW_TEXT);
      for (let t = tw.nextNode(); t; t = tw.nextNode()) texts.push(t);
    }
    let a = null, b = null;
    for (const t of texts) {
      let k;
      while ((k = t.data.search(/[]/)) >= 0) {
        const ch = t.data[k];
        t.deleteData(k, 1);
        if (ch === M1) a = { node: t, off: k }; else b = { node: t, off: k };
      }
    }
    for (const n of nodes) {
      if (!isEl(n)) continue;
      for (const h of [n, ...n.querySelectorAll(HOLDERS)]) {
        if (!HOLDER_RE.test(h.tagName) || !leafHolder(h) || h.closest("[data-raw]")) continue;
        if (!h.textContent && !h.querySelector("img,br,[data-raw]")) { h.appendChild(document.createElement("br")); continue; }
        if (h.tagName === "PRE" || Array.from(h.childNodes).some(blockEl) || (!h.textContent && !h.querySelector("img,[data-raw]"))) continue;
        let last = h.lastChild;
        while (last && last.nodeType === 3 && !last.data) last = last.previousSibling;
        if (last && last.nodeName === "BR") h.appendChild(document.createElement("br"));
      }
    }
    if (a) setSel(a, b);
    return !!a;
  }
  function caretAtEnd(n) {
    const h = isEl(n) ? (holderList().filter((x) => n.contains(x)).pop() || n) : n;
    const e = isEl(h) && HOLDER_RE.test(h.tagName) ? holderEnd(h) : { node: h, off: isEl(h) ? h.childNodes.length : h.data.length };
    setSel(e);
  }
  function caretAtSource(pos) {
    let best = null;
    for (const n of tops()) { const s = segMap.get(n); if (s && s.start <= pos) best = n; }
    caretAtEnd(best || surface.lastChild);
    const el = elOf(window.getSelection().focusNode);
    if (el && el.scrollIntoView) el.scrollIntoView({ block: "nearest" });
  }

  /* ------------------------------------------------------------- commands */
  // command inserts the selection markers, reads the affected top-level
  // blocks, applies fn and renders the result back in place.
  function command(fn, opts = {}) {
    const r = useRange();
    if (!r) return false;
    endTyping();
    const s = normPoint(r.startContainer, r.startOffset, false);
    let e = r.collapsed ? null : normPoint(r.endContainer, r.endOffset, true);
    if (e) {
      const hs = holderOf(s.node), he = holderOf(e.node);
      // A triple-click selection ends at the start of the next block.
      if (he && he !== hs && atHolderStart(he, e)) {
        const list = holderList(), k = list.indexOf(he);
        if (k > 0 && cmpPoint(holderEnd(list[k - 1]), s) >= 0) e = holderEnd(list[k - 1]);
      }
      if (cmpPoint(e, s) <= 0) e = null;
    }
    const all = tops();
    let i1 = all.indexOf(topOf(s.node)), i2 = e ? all.indexOf(topOf(e.node)) : i1;
    if (i1 < 0 || i2 < 0) return false;
    if (opts.wide !== false) { i1 = Math.max(0, i1 - 1); i2 = Math.min(all.length - 1, i2 + 1); }
    if (e) insertMarker(e, M2);
    insertMarker(s, M1);
    const nodes = all.slice(i1, i2 + 1);
    let blocks = readNodes(nodes);
    try { blocks = fn(blocks) || blocks; } catch (err) { console.error("editor command failed", err); }
    if (!blocks.length) blocks = [{ type: "p", runs: [caretRun()] }];
    const fresh = place(nodes, toHTML(blocks));
    if (!takeMarkers(fresh) && fresh.length) caretAtEnd(fresh[fresh.length - 1]);
    fixStructure();
    touch();
    pushState();
    updateState();
    return true;
  }
  // rerender replaces top-level nodes by their canonical rendering, keeping
  // the selection (used after native edits produced markup outside the model).
  function rerender(nodes) {
    const all = tops();
    const idx = nodes.map((n) => all.indexOf(n)).filter((k) => k >= 0).sort((x, y) => x - y);
    if (!idx.length) return;
    const slice = all.slice(idx[0], idx[idx.length - 1] + 1);
    const r = curRange();
    const inside = (pt) => slice.some((n) => n === pt.node || n.contains(pt.node));
    if (r) {
      const s = { node: r.startContainer, off: r.startOffset }, e = { node: r.endContainer, off: r.endOffset };
      if (!r.collapsed && inside(e)) insertMarker(normPoint(e.node, e.off, true), M2);
      if (inside(s)) insertMarker(normPoint(s.node, s.off, false), M1);
    }
    const fresh = place(slice, toHTML(readNodes(slice)));
    takeMarkers(fresh);
    fixStructure();
  }
  function place(nodes, html) {
    const tpl = document.createElement("template");
    tpl.innerHTML = html;
    const fresh = Array.from(tpl.content.childNodes);
    const ref = nodes[nodes.length - 1].nextSibling;
    for (const n of nodes) n.remove();
    surface.insertBefore(tpl.content, ref);
    return fresh;
  }
  const mark = (k) => command((b) => toggleMark(b, k), { wide: false });

  /* -------------------------------------------------------------- history */
  const hist = { stack: [], i: -1, timer: 0 };
  function pointPath(node, off) {
    const el = elOf(node);
    if (!inSurface(el)) return null;
    const r = document.createRange();
    r.setStart(el, 0);
    r.setEnd(node, off);
    const path = [];
    for (let e = el; e && e !== surface; e = e.parentElement) path.unshift(Array.prototype.indexOf.call(e.parentElement.children, e));
    return { path, chars: r.toString().length };
  }
  function pathPoint(p) {
    if (!p) return null;
    let el = surface;
    for (const k of p.path) { el = el.children[k]; if (!el) return null; }
    let left = p.chars;
    const tw = document.createTreeWalker(el, NodeFilter.SHOW_TEXT);
    for (let t = tw.nextNode(); t; t = tw.nextNode()) { if (left <= t.data.length) return { node: t, off: left }; left -= t.data.length; }
    return isEl(el) && HOLDER_RE.test(el.tagName) ? holderEnd(el) : { node: el, off: 0 };
  }
  function snapshot() {
    const r = curRange() || lastRange;
    const ok = r && inSurface(r.startContainer);
    return { html: surface.innerHTML, a: ok ? pointPath(r.startContainer, r.startOffset) : null, b: ok && !r.collapsed ? pointPath(r.endContainer, r.endOffset) : null };
  }
  function pushState() {
    clearTimeout(hist.timer);
    hist.timer = 0;
    const s = snapshot();
    if (hist.stack[hist.i] && hist.stack[hist.i].html === s.html) { hist.stack[hist.i] = s; return; }
    hist.stack.length = hist.i + 1;
    hist.stack.push(s);
    if (hist.stack.length > 100) hist.stack.shift();
    hist.i = hist.stack.length - 1;
  }
  function resetHistory() { hist.stack = []; hist.i = -1; pushState(); }
  function beginTyping() { if (!hist.timer && hist.stack[hist.i]) hist.stack[hist.i] = { ...hist.stack[hist.i], ...selOnly() }; }
  function selOnly() { const s = snapshot(); return { a: s.a, b: s.b }; }
  function typed() { clearTimeout(hist.timer); hist.timer = setTimeout(pushState, 700); }
  function endTyping() { if (hist.timer) pushState(); }
  function restore(s) {
    if (popOpen === "imagebar") closePop(false);
    surface.innerHTML = s.html;
    const a = pathPoint(s.a);
    surface.focus({ preventScroll: true });
    if (a) setSel(a, pathPoint(s.b));
    fixStructure();
    touch();
    updateState();
  }
  function undo() { endTyping(); if (hist.i > 0) restore(hist.stack[--hist.i]); }
  function redo() { endTyping(); if (hist.i < hist.stack.length - 1) restore(hist.stack[++hist.i]); }

  /* -------------------------------------------------------- native typing */
  // holderOK: does a holder still contain only markup the model produces?
  function holderOK(h) {
    if (h.tagName === "PRE") return Array.from(h.childNodes).every((c) => c.nodeType === 3 || (c.nodeName === "BR" && c === h.lastChild));
    let nodes = Array.from(h.childNodes);
    if (h.tagName === "LI") { const k = nodes.findIndex(blockEl); if (k >= 0) nodes = nodes.slice(0, k); }
    if (nodes.some(blockEl)) return false;
    const bl = readNodes(nodes);
    const want = !bl.length ? "" : bl.length === 1 && bl[0].type === "p" ? inlineHTML(bl[0].runs) : null;
    if (want == null) return false;
    const a = document.createElement("template"), b = document.createElement("template");
    a.innerHTML = want;
    for (const c of nodes) b.content.appendChild(c.cloneNode(true));
    const norm = (s) => s.replace(/&nbsp;/g, " ").replace(/(?:<br>)+$/, "");
    return norm(a.innerHTML) === norm(b.innerHTML);
  }
  function afterNative() {
    const bad = tops().filter((n) => (n.nodeType === 3 ? n.data !== "" : !(isEl(n) && (TOP_RE.test(n.tagName) || (n.tagName === "DIV" && n.hasAttribute("data-raw"))))));
    const r = curRange();
    const h = r && holderOf(r.startContainer);
    if (h && !holderOK(h)) { const t = topOf(h); if (t && !bad.includes(t)) bad.push(t); }
    if (!surface.firstChild) { surface.innerHTML = "<p><br></p>"; setSel({ node: surface.firstChild, off: 0 }); }
    else if (bad.length) rerender(bad);
    fixStructure();
    touch();
    typed();
    updateState();
  }
  surface.addEventListener("input", (e) => { if (!e.isComposing) afterNative(); });
  surface.addEventListener("compositionend", () => afterNative());

  function onBeforeInput(e) {
    const t = e.inputType || "";
    const r = curRange();
    if (!r) return;
    const hs = holderOf(r.startContainer), he = holderOf(r.endContainer);
    const cross = !r.collapsed && (hs !== he || !hs);
    const pt = { node: r.startContainer, off: r.startOffset };
    switch (t) {
      case "historyUndo": e.preventDefault(); undo(); return;
      case "historyRedo": e.preventDefault(); redo(); return;
      case "formatBold": e.preventDefault(); mark("s"); return;
      case "formatItalic": e.preventDefault(); mark("e"); return;
      case "formatStrikeThrough": e.preventDefault(); mark("d"); return;
      case "insertParagraph": e.preventDefault(); enter(false); return;
      case "insertLineBreak": e.preventDefault(); enter(true); return;
      case "insertFromPaste": case "insertFromPasteAsQuotation": case "insertFromDrop": case "deleteByDrag": case "deleteByCut":
        e.preventDefault(); return; // handled by the paste, drop and cut listeners
      case "insertText": case "insertReplacementText": {
        const text = e.data != null ? e.data : e.dataTransfer ? e.dataTransfer.getData("text/plain") : "";
        if (cross) { e.preventDefault(); command((b) => typeText(deleteSelection(b), text)); return; }
        if (t === "insertText" && text === " " && r.collapsed && hs && autoformatSpace(hs, pt)) { e.preventDefault(); return; }
        beginTyping();
        return;
      }
    }
    if (/Composition/.test(t)) return; // IME: let the browser compose, fix up on compositionend
    if (t.startsWith("delete")) {
      if (cross) { e.preventDefault(); command(deleteSelection); return; }
      if (r.collapsed && hs) {
        const back = t.includes("Backward");
        if (back && atHolderStart(hs, pt)) { e.preventDefault(); command(joinBackward); return; }
        if (!back && atHolderEnd(hs, pt)) { e.preventDefault(); command(joinForward); return; }
      }
      beginTyping();
      return;
    }
    // Native rich-text commands would inject markup outside the model.
    if (t.startsWith("format") || /^insert(?:OrderedList|UnorderedList|HorizontalRule|Link|FromYank)$/.test(t)) e.preventDefault();
  }
  surface.addEventListener("beforeinput", onBeforeInput);

  function autoformatSpace(h, pt) {
    if (h.tagName !== "P" || closest(h, "li,td,th")) return false;
    const before = textBefore(h, pt);
    if (before.atoms) return false;
    const s = before.text;
    let m;
    if ((m = /^(#{1,4})$/.exec(s))) return command((b) => autoformat(b, "h", Math.max(2, m[1].length)));
    if (/^[-*+]$/.test(s)) return command((b) => autoformat(b, "ul"));
    if ((m = /^(\d{1,9})[.)]$/.exec(s))) return command((b) => autoformat(b, "ol", +m[1]));
    if (s === ">") return command((b) => autoformat(b, "quote"));
    if ((m = /^(?:```|~~~)([\w+#.-]*)$/.exec(s))) return command((b) => autoformat(b, "code", m[1]));
    return false;
  }
  function enter(soft) {
    const r = curRange();
    if (!r) return;
    if (closest(r.startContainer, "pre")) { command((b) => (soft ? typeText(deleteSelection(b), "\n") : codeEnter(deleteSelection(b))), { wide: false }); return; }
    if (closest(r.startContainer, "td,th")) { command((b) => tableOp(deleteSelection(b), "down"), { wide: false }); return; }
    if (soft) { command((b) => insertBreak(deleteSelection(b)), { wide: false }); return; }
    const h = holderOf(r.startContainer);
    const pt = { node: r.startContainer, off: r.startOffset };
    if (r.collapsed && h && h.tagName === "P" && !closest(h, "li") && atHolderEnd(h, pt)) {
      const s = textBefore(h, pt).text.trim();
      let m;
      if ((m = /^(?:```|~~~)([\w+#.-]*)$/.exec(s))) { command((b) => autoformat(b, "code", m[1])); return; }
      if (/^(?:-{3,}|\*{3,}|_{3,})$/.test(s)) { command((b) => autoformat(b, "hr")); return; }
    }
    command((b) => splitBlock(deleteSelection(b)));
  }

  /* -------------------------------------------------------------- keyboard */
  let escPressed = false;
  surface.addEventListener("keydown", (e) => {
    const mod = e.ctrlKey || e.metaKey;
    if (e.key === "Escape") { escPressed = true; closePop(); return; }
    if (e.key === "Tab" && !mod && !e.altKey) {
      if (escPressed) { escPressed = false; return; }
      if (tabKey(e.shiftKey)) e.preventDefault();
      return;
    }
    escPressed = false;
    if (!mod) return;
    const k = (e.key || "").toLowerCase();
    let done = true;
    if (e.altKey && /^Digit[0-4]$/.test(e.code)) { const n = +e.code.slice(5); command((b) => setBlockType(b, n === 0 ? 0 : Math.max(2, n)), { wide: false }); }
    else if (e.shiftKey && (e.code === "Digit7" || e.code === "Digit8")) command((b) => toggleList(b, e.code === "Digit7"));
    else if (e.shiftKey && k === "x") mark("d");
    else if (e.shiftKey && k === "z") redo();
    else if (k === "enter") exitOrToggle();
    else if (e.shiftKey || e.altKey) done = false;
    else if (k === "b") mark("s");
    else if (k === "i") mark("e");
    else if (k === "k") openLink();
    else if (k === "z") undo();
    else if (k === "y") redo();
    else done = false;
    if (done) e.preventDefault();
  });
  function tabKey(shift) {
    const r = curRange();
    if (!r) return false;
    if (closest(r.startContainer, "pre")) return command((b) => codeIndent(b, shift), { wide: false });
    if (closest(r.startContainer, "td,th")) return command((b) => tableOp(b, shift ? "prev" : "next"), { wide: false });
    if (closest(r.startContainer, "li")) return command((b) => indentList(b, shift));
    return false;
  }
  function exitOrToggle() {
    const r = curRange();
    if (!r) return;
    const li = closest(r.startContainer, "li[data-checked]");
    if (li) { toggleTask(li); return; }
    if (closest(r.startContainer, "pre,td,th,blockquote")) command(exitBlock, { wide: false });
  }
  function toggleTask(li) {
    li.setAttribute("data-checked", li.getAttribute("data-checked") === "true" ? "false" : "true");
    touch();
    pushState();
  }

  /* ---------------------------------------------------- clipboard and drop */
  function fragmentNodes(r) {
    let inner = r.cloneContents();
    let anc = elOf(r.commonAncestorContainer);
    const chain = [];
    while (anc && anc !== surface && /^(?:UL|OL|TABLE|THEAD|TBODY|TFOOT|TR)$/.test(anc.tagName)) { chain.push(anc); anc = anc.parentNode; }
    for (const a of chain) { const c = a.cloneNode(false); c.appendChild(inner); inner = c; }
    return isEl(inner) ? [inner] : Array.from(inner.childNodes);
  }
  function rangeMarkdown(r) { return renderMarkdown(readNodes(fragmentNodes(r))); }
  function putClipboard(dt, r) {
    const inPre = closest(r.startContainer, "pre") && closest(r.startContainer, "pre") === closest(r.endContainer, "pre");
    const blocks = readNodes(fragmentNodes(r));
    const md = inPre ? r.toString() : renderMarkdown(blocks);
    dt.setData("text/plain", md);
    dt.setData("text/html", toHTML(blocks).replace(/ (?:contenteditable|spellcheck|data-raw|data-kind|data-label|data-lang)="[^"]*"/g, ""));
    dt.setData(MIME, md);
  }
  surface.addEventListener("copy", (e) => {
    const r = curRange();
    if (!r || r.collapsed || !e.clipboardData) return;
    e.preventDefault();
    putClipboard(e.clipboardData, r);
  });
  surface.addEventListener("cut", (e) => {
    const r = curRange();
    if (!r || r.collapsed || !e.clipboardData) return;
    e.preventDefault();
    putClipboard(e.clipboardData, r);
    command(deleteSelection);
  });
  surface.addEventListener("paste", (e) => {
    if (!e.clipboardData) return;
    e.preventDefault();
    e.stopPropagation();
    pasteData(e.clipboardData);
  });
  const onlyImage = (blocks) => blocks.length === 1 && blocks[0].type === "p" && blocks[0].runs.length === 1 && blocks[0].runs[0].a === "img";
  // pasteFrom turns clipboard/drag data into blocks (or files to upload).
  function pasteFrom(dt) {
    const md = dt.getData(MIME), html = dt.getData("text/html"), text = dt.getData("text/plain");
    const files = Array.from(dt.files || []).filter((f) => /^image\//.test(f.type));
    if (md) return { blocks: parseMarkdown(md), text };
    if (html && html.trim()) {
      const doc = new DOMParser().parseFromString(prepareHTML(html), "text/html");
      const res = htmlToBlocks(doc.body);
      if (!(files.length && (onlyImage(res.blocks) || !res.blocks.length))) return { blocks: res.blocks, images: res.images, text };
    }
    if (files.length) return { files, text };
    if (text) return { blocks: looksLikeMarkdown(text) ? normalizeHeadings(parseMarkdown(text)) : textToBlocks(text), text, plain: true };
    return { text };
  }
  // A video link pasted on an empty top-level line becomes a video.
  function emptyTopLine(r) {
    const h = r && r.collapsed ? holderOf(r.startContainer) : null;
    return !!h && h.tagName === "P" && h.parentNode === surface && !stripInvisible(h.textContent).trim() && !h.querySelector("img,[data-raw]");
  }
  function pasteData(dt) {
    const r = curRange() || useRange();
    const vt = (dt.getData("text/plain") || "").trim();
    if (!dt.getData(MIME) && !(dt.files && dt.files.length) && emptyTopLine(r) && videoLine(vt) && !vt.includes("\n")) {
      command((b) => pasteVideo(b, vt) || b);
      return;
    }
    const p = pasteFrom(dt);
    if (r && closest(r.startContainer, "pre")) {
      if (p.text) command((b) => typeText(deleteSelection(b), p.text.replace(/\r\n?/g, "\n")), { wide: false });
      return;
    }
    if (p.files) { uploadInsert(p.files); return; }
    if (!p.blocks || !p.blocks.length) return;
    command((b) => insertBlocks(deleteSelection(b), p.blocks));
    if (p.images) handleImages(p.images);
  }
  function handleImages(images) {
    if (images.dropped) toast("warning", say("ImagesDropped", "Some pasted images could not be kept. Upload them from the media library."));
    if (images.data.length) uploadData(images.data);
  }
  function dataFile(u, i) {
    const m = /^data:(image\/[\w.+-]+);base64,([\s\S]*)$/.exec(u);
    if (!m) return null;
    const bin = atob(m[2].replace(/\s+/g, ""));
    const bytes = new Uint8Array(bin.length);
    for (let k = 0; k < bin.length; k++) bytes[k] = bin.charCodeAt(k);
    const ext = { "image/jpeg": "jpg", "image/png": "png", "image/gif": "gif", "image/webp": "webp", "image/avif": "avif" }[m[1]] || "png";
    return new File([bytes], `pasted-${Date.now()}-${i + 1}.${ext}`, { type: m[1] });
  }
  async function upload(files) {
    const fd = new FormData();
    for (const f of files) fd.append("file", f);
    toast("info", say("Uploading", "Uploading…"));
    try {
      const res = await fetch("/admin/media", { method: "POST", body: fd, credentials: "same-origin", headers: { Accept: "application/json", "X-CSRF-Token": csrf() } });
      const data = await res.json().catch(() => ({}));
      for (const err of data.errors || []) toast("error", err);
      if (!res.ok && !data.errors) toast("error", data.error || say("UploadFailed", "Upload failed"));
      return data.items || [];
    } catch (_) {
      toast("error", say("UploadFailed", "Upload failed"));
      return [];
    }
  }
  // Pasted data: images are shown at once and swapped for uploaded copies.
  async function uploadData(list) {
    const files = list.map(dataFile);
    const ok = files.filter(Boolean);
    const items = ok.length ? await upload(ok.slice(0, 20)) : [];
    let k = 0;
    list.forEach((u, i) => {
      const item = files[i] ? items[k++] : null;
      const sel = `img[src="${CSS.escape(u)}"]`;
      for (const img of surface.querySelectorAll(sel)) {
        if (item && item.url) { img.setAttribute("src", item.medium || item.url); if (!img.getAttribute("alt") && item.alt) img.setAttribute("alt", item.alt); }
        else img.remove();
      }
      if (mode !== "visual" || !surface.querySelector(sel)) {
        const re = new RegExp("!\\[[^\\]\\n]*\\]\\(<?" + u.replace(/[.*+?^${}()|[\]\\]/g, "\\$&") + ">?\\)", "g");
        if (ta.value.includes(u)) { ta.value = ta.value.replace(re, item && item.url ? (s) => s.replace(u, item.medium || item.url) : ""); lastMd = mode === "visual" ? lastMd : ta.value; emitInput(); }
      }
    });
    if (mode === "visual") { touch(); flushSync(); }
  }
  const mediaBlock = (it) => (it.image
    ? { type: "p", runs: [{ a: "img", src: it.medium || it.url, alt: it.alt || "", title: "", m: {} }] }
    : { type: "p", runs: [{ t: it.name || it.url, m: { l: { href: it.url, title: "" } } }] });
  async function uploadInsert(files) {
    const items = await upload(files);
    if (items.length) command((b) => insertBlocks(deleteSelection(b), items.map(mediaBlock), { blocks: true }));
  }
  surface.addEventListener("dragstart", (e) => e.preventDefault());
  surface.addEventListener("dragover", (e) => { if (!e.dataTransfer) return; e.preventDefault(); e.dataTransfer.dropEffect = "copy"; surface.classList.add("dragging"); });
  surface.addEventListener("dragleave", () => surface.classList.remove("dragging"));
  surface.addEventListener("drop", (e) => {
    surface.classList.remove("dragging");
    if (!e.dataTransfer) return;
    e.preventDefault();
    e.stopPropagation();
    let pos = null;
    if (document.caretPositionFromPoint) { const p = document.caretPositionFromPoint(e.clientX, e.clientY); if (p) pos = { node: p.offsetNode, off: p.offset }; }
    else if (document.caretRangeFromPoint) { const p = document.caretRangeFromPoint(e.clientX, e.clientY); if (p) pos = { node: p.startContainer, off: p.startOffset }; }
    surface.focus({ preventScroll: true });
    if (pos && inSurface(pos.node)) setSel(pos);
    pasteData(e.dataTransfer);
  });
  // The media picker inserts through the textarea (admin.js insertMedia):
  // point the textarea caret after the current block; external() re-renders.
  document.addEventListener("click", (e) => {
    const b = e.target.closest && e.target.closest('[data-media-picker="insert"]');
    if (!b || mode !== "visual" || !root.contains(b)) return;
    flushSync();
    const r = lastRange && inSurface(lastRange.endContainer) ? lastRange : null;
    const top = r ? topOf(r.endContainer) : null;
    const seg = top && segMap.get(top);
    const pos = seg ? seg.end : ta.value.length;
    ta.setSelectionRange(pos, pos);
    mediaPending = true;
  }, true);
  const dlg = document.getElementById("media-dialog");
  if (dlg) dlg.addEventListener("close", () => setTimeout(() => { mediaPending = false; }, 0));

  /* ------------------------------------------------------- markdown mode */
  function mdChanged() { ta.dispatchEvent(new Event("input", { bubbles: true })); }
  function mdWrap(before, after, ph) {
    const { selectionStart: s, selectionEnd: en, value } = ta;
    const sel = value.slice(s, en) || ph;
    ta.setRangeText(before + sel + after, s, en, "end");
    if (!value.slice(s, en)) ta.setSelectionRange(s + before.length, s + before.length + sel.length);
    mdChanged();
  }
  function mdInsertBlock(md, selectInner) {
    const s = ta.selectionStart, en = ta.selectionEnd, v = ta.value;
    const before = v.slice(0, s), after = v.slice(en);
    const pre = before && !before.endsWith("\n\n") ? (before.endsWith("\n") ? "\n" : "\n\n") : "";
    const post = after.startsWith("\n\n") ? "" : after.startsWith("\n") ? "\n" : "\n\n";
    ta.setRangeText(pre + md + post, s, en, "end");
    if (selectInner) { const a = s + pre + selectInner[0], z = a + selectInner[1]; ta.setSelectionRange(a, z); }
    mdChanged();
  }
  const MD_EXTRA = {
    strike: () => mdWrap("~~", "~~", say("Text", "text")),
    codeblock: () => { const sel = ta.value.slice(ta.selectionStart, ta.selectionEnd); mdInsertBlock("```\n" + sel + "\n```", [4, sel.length]); },
    hr: () => mdInsertBlock("---"),
    table: () => mdInsertBlock("|  |  |  |\n| --- | --- | --- |\n|  |  |  |", [2, 0]),
    video: () => openVideo(),
  };
  // Paste in Markdown mode: rich HTML becomes Markdown, plain text stays as is.
  ta.addEventListener("paste", (e) => {
    const dt = e.clipboardData;
    if (!dt || mode !== "markdown") return;
    const md = dt.getData(MIME);
    let out = md || null, images = null;
    if (!out) {
      const html = dt.getData("text/html");
      if (!html || !html.trim()) return;
      const files = Array.from(dt.files || []).filter((f) => /^image\//.test(f.type));
      const res = htmlToBlocks(new DOMParser().parseFromString(prepareHTML(html), "text/html").body);
      if (!res.blocks.length || (files.length && onlyImage(res.blocks))) return; // let admin.js upload the file
      const rendered = renderMarkdown(res.blocks);
      const plain = (dt.getData("text/plain") || "").replace(/\r\n?/g, "\n").trim();
      if (!res.images.data.length && !res.images.dropped && rendered.replace(/\\(.)/g, "$1").replace(/\s+/g, " ") === plain.replace(/\s+/g, " ")) return;
      out = rendered;
      images = res.images;
    }
    e.preventDefault();
    e.stopPropagation();
    ta.setRangeText(out, ta.selectionStart, ta.selectionEnd, "end");
    mdChanged();
    if (images) handleImages(images);
  });

  /* ------------------------------------------------------------- popovers */
  const pops = {};
  $$("[data-editor-pop]").forEach((p) => { pops[p.dataset.editorPop] = p; });
  let popOpen = null, popRange = null, popTarget = null, popReturn = null;
  let frame = null, frameImg = null, drag = null, videoTarget = null; // image resize frame, video being changed
  function rectOf(r) {
    let rect = r.getBoundingClientRect();
    if (!rect.width && !rect.height) { const h = holderOf(r.startContainer) || elOf(r.startContainer); if (h) rect = h.getBoundingClientRect(); }
    return rect;
  }
  function position(pop, rect, where) {
    pop.hidden = false;
    const rr = root.getBoundingClientRect();
    const pw = pop.offsetWidth, ph = pop.offsetHeight;
    const tb = toolbar ? toolbar.getBoundingClientRect().bottom - rr.top : 0;
    let left = where === "right" ? rect.right - rr.left - pw : rect.left - rr.left;
    left = Math.max(8, Math.min(left, rr.width - pw - 8));
    let top = rect.bottom - rr.top + 6;
    if (where === "right" || where === "above") { top = rect.top - rr.top - ph - 6; if (top < tb + 4) top = rect.bottom - rr.top + 6; }
    pop.style.left = Math.round(left) + "px";
    pop.style.top = Math.round(top) + "px";
  }
  function openPop(name, rect, where) {
    closePop();
    const pop = pops[name];
    if (!pop) return null;
    popOpen = name;
    position(pop, rect, where);
    return pop;
  }
  function closePop(refocus) {
    if (!popOpen) return;
    const pop = pops[popOpen];
    pop.hidden = true;
    const name = popOpen;
    popOpen = null;
    if (popReturn) popReturn.setAttribute("aria-expanded", "false");
    if (name === "imagebar") hideFrame();
    if (name === "video") videoTarget = null;
    if (refocus && mode === "markdown") ta.focus({ preventScroll: true });
    else if (refocus) {
      surface.focus({ preventScroll: true });
      if (popRange) { const sel = window.getSelection(); sel.removeAllRanges(); sel.addRange(popRange); }
    }
    if (name !== "code" && name !== "table") popTarget = null;
  }
  function restorePopRange() {
    surface.focus({ preventScroll: true });
    if (popRange && inSurface(popRange.startContainer)) { const sel = window.getSelection(); sel.removeAllRanges(); sel.addRange(popRange); }
  }
  document.addEventListener("mousedown", (e) => {
    if (!popOpen || popOpen === "code" || popOpen === "table") return;
    const pop = pops[popOpen];
    if (pop.contains(e.target) || (popReturn && popReturn.contains(e.target)) || (frame && frame.contains(e.target))) return;
    closePop(false);
  });
  for (const pop of Object.values(pops)) {
    pop.addEventListener("keydown", (e) => {
      if (e.key === "Escape") { e.preventDefault(); closePop(true); }
      if (pop.getAttribute("role") === "menu" && (e.key === "ArrowDown" || e.key === "ArrowUp")) {
        e.preventDefault();
        const items = $$("[role=menuitem]", pop), k = items.indexOf(document.activeElement);
        items[(k + (e.key === "ArrowDown" ? 1 : items.length - 1)) % items.length].focus();
      }
    });
    // Context bars keep the editor selection when clicked.
    if (pop.classList.contains("is-bar")) pop.addEventListener("mousedown", (e) => { if (!e.target.closest("input")) e.preventDefault(); });
  }
  const field = (pop, sel) => pop.querySelector(sel);
  function safeURL(v) {
    v = (v || "").trim();
    if (!v) return "";
    if (/^(?:https?:|mailto:|tel:)/i.test(v) || /^[/#?.]/.test(v)) return v;
    if (/^[^\s@/]+@[^\s@/]+\.[a-z]{2,}$/i.test(v)) return "mailto:" + v;
    if (/^[a-z][a-z0-9+.-]*:/i.test(v)) return null;
    return "https://" + v;
  }
  function openLink() {
    if (mode !== "visual") return;
    const r = useRange();
    if (!r) return;
    popRange = r.cloneRange();
    const a = closest(r.startContainer, "a");
    const pop = pops.link;
    if (!pop) return;
    const url = field(pop, "[data-pop-url]"), text = field(pop, "[data-pop-text]"), err = field(pop, "[data-pop-error]");
    url.value = a ? a.getAttribute("href") || a.getAttribute("data-href") || "" : "";
    text.value = "";
    field(pop, "[data-pop-textrow]").hidden = !(r.collapsed && !a);
    field(pop, "[data-pop-unlink]").hidden = !a;
    const open = field(pop, "[data-pop-open]");
    open.hidden = !(a && a.getAttribute("href"));
    if (a && a.getAttribute("href")) open.setAttribute("href", a.getAttribute("href"));
    err.hidden = true;
    popReturn = $('[data-md="link"]');
    openPop("link", rectOf(r));
    url.focus();
    url.select();
  }
  function applyLink() {
    const pop = pops.link;
    const href = safeURL(field(pop, "[data-pop-url]").value);
    if (href === null) { const err = field(pop, "[data-pop-error]"); err.textContent = say("BadUrl", "Enter a valid web address."); err.hidden = false; return; }
    const text = field(pop, "[data-pop-text]").value.trim();
    closePop(false);
    restorePopRange();
    command((b) => setLink(b, href ? { href, text } : null), { wide: false });
  }
  if (pops.link) {
    pops.link.addEventListener("click", (e) => {
      if (e.target.closest("[data-pop-apply]")) applyLink();
      else if (e.target.closest("[data-pop-cancel]")) closePop(true);
      else if (e.target.closest("[data-pop-unlink]")) { closePop(false); restorePopRange(); command((b) => setLink(b, null), { wide: false }); }
    });
    pops.link.addEventListener("keydown", (e) => { if (e.key === "Enter" && e.target.matches("input")) { e.preventDefault(); applyLink(); } });
  }
  function openImage(img) {
    const pop = pops.image;
    if (!pop) return;
    const r = document.createRange();
    r.selectNode(img);
    popRange = r;
    popTarget = img;
    const sel = window.getSelection();
    sel.removeAllRanges();
    sel.addRange(r);
    field(pop, "[data-pop-alt]").value = img.getAttribute("alt") || "";
    popReturn = null;
    openPop("image", img.getBoundingClientRect());
    field(pop, "[data-pop-alt]").focus();
  }
  if (pops.image) {
    const applyAlt = () => {
      const img = popTarget;
      if (img && img.isConnected) { img.setAttribute("alt", field(pops.image, "[data-pop-alt]").value.replace(/\s+/g, " ").trim()); touch(); pushState(); }
      closePop(true);
    };
    pops.image.addEventListener("click", (e) => {
      if (e.target.closest("[data-pop-apply]")) applyAlt();
      else if (e.target.closest("[data-pop-cancel]")) closePop(true);
      else if (e.target.closest("[data-pop-remove]")) { const img = popTarget; closePop(false); restorePopRange(); if (img && img.isConnected) command(deleteSelection, { wide: false }); }
    });
    pops.image.addEventListener("keydown", (e) => { if (e.key === "Enter" && e.target.matches("input")) { e.preventDefault(); applyAlt(); } });
  }
  function openHeading(btn) {
    const r = useRange();
    if (!r) return;
    if (popOpen === "heading") { closePop(true); return; }
    popRange = r.cloneRange();
    popReturn = btn;
    btn.setAttribute("aria-expanded", "true");
    const pop = openPop("heading", btn.getBoundingClientRect());
    const h = closest(r.startContainer, "h1,h2,h3,h4,h5,h6");
    const cur = h ? +h.tagName[1] : 0;
    $$("[data-block]", pop).forEach((b) => b.setAttribute("aria-checked", String(+b.dataset.block === cur)));
    (pop.querySelector(`[data-block="${cur}"]`) || pop.querySelector("[data-block]")).focus();
  }
  if (pops.heading) {
    pops.heading.addEventListener("click", (e) => {
      const b = e.target.closest("[data-block]");
      if (!b) return;
      const lv = +b.dataset.block;
      closePop(false);
      restorePopRange();
      command((bl) => setBlockType(bl, lv), { wide: false });
    });
  }
  // Context bars for code blocks, tables and opaque blocks follow the caret.
  function updateContext() {
    // The image bar stays while its image is selected (or the focus is in
    // the bar or on a handle, outside the surface).
    if (popOpen === "imagebar") { const r = curRange(); if (r && !imageSelected(r)) closePop(false); return; }
    if (mode !== "visual" || (popOpen && popOpen !== "code" && popOpen !== "table" && popOpen !== "raw")) return;
    const r = curRange();
    const pre = r && closest(r.startContainer, "pre");
    const cell = r && closest(r.startContainer, "table");
    if (pre && pops.code) {
      if (popOpen !== "code" || popTarget !== pre) {
        openPop("code", pre.getBoundingClientRect(), "right");
        popTarget = pre;
        field(pops.code, "[data-pop-lang]").value = pre.getAttribute("data-lang") || "";
      } else position(pops.code, pre.getBoundingClientRect(), "right");
    } else if (cell && pops.table) {
      if (popOpen !== "table" || popTarget !== cell) { openPop("table", cell.getBoundingClientRect(), "right"); popTarget = cell; }
      else position(pops.table, cell.getBoundingClientRect(), "right");
    } else if (popOpen === "code" || popOpen === "table") {
      if (!(popOpen === "code" && pops.code.contains(document.activeElement))) closePop(false);
    }
  }
  if (pops.code) {
    const lang = field(pops.code, "[data-pop-lang]");
    lang.addEventListener("input", () => {
      const pre = popTarget;
      if (!pre || !pre.isConnected || pre.tagName !== "PRE") return;
      pre.setAttribute("data-lang", lang.value.replace(/[^\w+#.-]/g, "").slice(0, 30));
      touch();
    });
    lang.addEventListener("change", () => pushState());
    lang.addEventListener("keydown", (e) => {
      if (e.key !== "Enter" && e.key !== "Escape") return;
      e.preventDefault();
      e.stopPropagation();
      pushState();
      const pre = popTarget;
      surface.focus({ preventScroll: true });
      if (pre && pre.isConnected) caretAtEnd(pre);
    });
    pops.code.addEventListener("click", (e) => {
      if (!e.target.closest("[data-pop-uncode]")) return;
      const pre = popTarget;
      if (pre && pre.isConnected && !curRange()) { surface.focus({ preventScroll: true }); caretAtEnd(pre); }
      closePop(false);
      command(toggleCodeBlock, { wide: false });
    });
  }
  if (pops.table) {
    pops.table.addEventListener("click", (e) => {
      const b = e.target.closest("[data-table]");
      if (!b) return;
      if (!curRange() && lastRange) useRange();
      const op = b.dataset.table;
      if (op === "table-del") closePop(false);
      command((bl) => tableOp(bl, op), { wide: false });
      updateContext();
    });
  }
  function openRaw(el) {
    const pop = pops.raw;
    if (!pop) return;
    popTarget = el;
    popReturn = null;
    field(pop, "[data-pop-label]").textContent = el.getAttribute("data-label") || LABELS[el.getAttribute("data-kind")] || say("Markup", "Markup");
    const isVideo = el.getAttribute("data-kind") === "video";
    $$("[data-pop-video-only]", pop).forEach((b) => { b.hidden = !isVideo; });
    openPop("raw", el.getBoundingClientRect(), isVideo ? "above" : undefined);
    popTarget = el;
  }
  if (pops.raw) {
    pops.raw.addEventListener("click", (e) => {
      const el = popTarget;
      if (!el || !el.isConnected) { closePop(false); return; }
      if (e.target.closest("[data-pop-change]")) { openVideo(el); return; }
      if (e.target.closest("[data-pop-source]")) {
        flushSync();
        const seg = segMap.get(topOf(el));
        closePop(false);
        setMode("markdown", true);
        if (seg) {
          const raw = el.getAttribute("data-raw") || "";
          const k = ta.value.indexOf(raw, seg.start);
          const [a, z] = k >= 0 && k < seg.end ? [k, k + raw.length] : [seg.start, seg.end];
          ta.setSelectionRange(a, z);
          scrollTextarea(a);
        }
      } else if (e.target.closest("[data-pop-delete]")) {
        closePop(false);
        const top = topOf(el);
        el.remove();
        if (top && top !== el && isEl(top) && !top.textContent.trim() && !top.querySelector("img,[data-raw]")) top.remove();
        fixStructure();
        touch();
        pushState();
      }
    });
  }

  // The video popover inserts a video (toolbar, both modes) or changes the
  // address of one (target: its block).
  function openVideo(target) {
    const pop = pops.video;
    if (!pop) return;
    const btn = $('[data-md="video"]');
    if (popOpen === "video" && !target) { closePop(true); return; }
    closePop(false);
    if (mode === "visual" && !target) { const r = useRange(); popRange = r ? r.cloneRange() : null; }
    const input = field(pop, "[data-pop-video]");
    const cur = target ? videoLine(target.getAttribute("data-raw")) : null;
    input.value = cur ? cur.url : "";
    field(pop, "[data-pop-error]").hidden = true;
    popReturn = target ? null : btn;
    if (popReturn) popReturn.setAttribute("aria-expanded", "true");
    openPop("video", (target || btn || toolbar).getBoundingClientRect(), target ? "above" : undefined);
    videoTarget = target || null;
    input.focus();
    input.select();
  }
  function applyVideo() {
    const pop = pops.video;
    const input = field(pop, "[data-pop-video]");
    const res = checkVideo(input.value);
    if (!res.ok) {
      const err = field(pop, "[data-pop-error]");
      err.textContent = say("BadVideo", "Enter a YouTube or Vimeo address.");
      err.hidden = false;
      input.focus();
      return;
    }
    const target = videoTarget;
    closePop(false);
    if (target && target.isConnected) { changeVideo(target, res); return; }
    if (mode === "markdown") { ta.focus({ preventScroll: true }); mdInsertBlock(res.line); return; }
    restorePopRange();
    command((b) => insertVideo(deleteSelection(b), res.line));
  }
  // changeVideo gives a video block a new address. A [video] shortcode keeps
  // its start and title.
  function changeVideo(el, res) {
    const old = el.getAttribute("data-raw") || "";
    let line = res.line;
    const was = videoLine(old);
    if (was && was.shortcode && !res.video.shortcode) line = old.replace(/(\surl=)(?:"[^"]*"|'[^']*'|[^\s\]"']+)/, (_, p) => p + '"' + res.video.url + '"');
    if (!videoLine(line)) line = res.line;
    place([el], blockHTML({ type: "raw", kind: "video", text: line }));
    fixStructure();
    touch();
    pushState();
  }
  if (pops.video) {
    pops.video.addEventListener("click", (e) => {
      if (e.target.closest("[data-pop-apply]")) applyVideo();
      else if (e.target.closest("[data-pop-cancel]")) closePop(true);
    });
    pops.video.addEventListener("keydown", (e) => { if (e.key === "Enter" && e.target.matches("input")) { e.preventDefault(); applyVideo(); } });
    field(pops.video, "[data-pop-video]").addEventListener("input", () => { field(pops.video, "[data-pop-error]").hidden = true; });
  }

  /* --------------------------------------------------------- image layout */
  // Clicking an image selects it: a frame with four resize handles goes over
  // it and the image bar above it sets the alignment and the width. Widths
  // snap to tenths of the column; the image keeps its ratio.
  function columnOf(img) {
    const box = holderOf(img) || surface;
    const cs = getComputedStyle(box);
    return Math.max(1, box.clientWidth - (parseFloat(cs.paddingLeft) || 0) - (parseFloat(cs.paddingRight) || 0));
  }
  function ensureFrame() {
    if (frame) return frame;
    frame = document.createElement("div");
    frame.className = "editor-img-frame";
    frame.hidden = true;
    for (const c of ["nw", "ne", "sw", "se"]) {
      const h = document.createElement("span");
      h.className = "editor-img-handle";
      h.dataset.corner = c;
      h.tabIndex = 0;
      h.setAttribute("role", "slider");
      h.setAttribute("aria-label", say("ResizeImage", "Resize image"));
      h.setAttribute("aria-valuemin", "10");
      h.setAttribute("aria-valuemax", "100");
      frame.appendChild(h);
    }
    const size = document.createElement("span");
    size.className = "editor-img-size";
    size.setAttribute("aria-hidden", "true");
    frame.appendChild(size);
    root.appendChild(frame);
    frame.addEventListener("pointerdown", (e) => {
      const h = e.target.closest("[role=slider]");
      if (!h || !frameImg || e.button !== 0) return;
      e.preventDefault();
      h.setPointerCapture(e.pointerId);
      const r = frameImg.getBoundingClientRect();
      drag = { h, id: e.pointerId, x: e.clientX, y: e.clientY, w: r.width, ratio: r.width ? r.height / r.width : 0, col: columnOf(frameImg), corner: h.dataset.corner, last: 0 };
      frame.classList.add("is-resizing");
    });
    frame.addEventListener("pointermove", (e) => {
      if (!drag || e.pointerId !== drag.id || !frameImg) return;
      const w = snapWidth(resizeWidth(drag.w, e.clientX - drag.x, e.clientY - drag.y, drag.corner, drag.ratio), drag.col);
      if (w === drag.last) return;
      drag.last = w;
      frameImg.setAttribute("width", String(w));
      frameImg.classList.remove("align-full");
      placeFrame();
    });
    const endDrag = (e) => {
      if (!drag || e.pointerId !== drag.id) return;
      const { h, last } = drag;
      drag = null;
      frame.classList.remove("is-resizing");
      if (last) applyImageOp("width", last);
      h.focus({ preventScroll: true });
    };
    frame.addEventListener("pointerup", endDrag);
    frame.addEventListener("pointercancel", endDrag);
    frame.addEventListener("keydown", (e) => {
      if (!e.target.closest("[role=slider]") || !frameImg) return;
      if (e.key === "Escape") { e.preventDefault(); closePop(true); return; }
      const delta = { ArrowRight: 1, ArrowUp: 1, PageUp: 1, ArrowLeft: -1, ArrowDown: -1, PageDown: -1, Home: -10, End: 10 }[e.key];
      if (!delta) return;
      e.preventDefault();
      const cur = +frameImg.getAttribute("width") || frameImg.getBoundingClientRect().width;
      applyImageOp("width", stepWidth(cur, columnOf(frameImg), delta));
    });
    window.addEventListener("resize", () => { if (frameImg) requestAnimationFrame(placeFrame); });
    return frame;
  }
  function hideFrame() {
    frameImg = null;
    drag = null;
    if (frame) { frame.hidden = true; frame.classList.remove("is-resizing"); }
  }
  function placeFrame() {
    if (!frame || !frameImg) return;
    if (!frameImg.isConnected) { closePop(false); return; }
    const rr = root.getBoundingClientRect(), ir = frameImg.getBoundingClientRect();
    frame.hidden = false;
    frame.style.left = Math.round(ir.left - rr.left) + "px";
    frame.style.top = Math.round(ir.top - rr.top) + "px";
    frame.style.width = Math.round(ir.width) + "px";
    frame.style.height = Math.round(ir.height) + "px";
    const w = +frameImg.getAttribute("width") || Math.round(ir.width);
    const text = w + " px";
    frame.querySelector(".editor-img-size").textContent = text;
    const pct = Math.max(10, Math.min(100, Math.round(w / columnOf(frameImg) * 10) * 10));
    for (const h of frame.querySelectorAll("[role=slider]")) { h.setAttribute("aria-valuenow", String(pct)); h.setAttribute("aria-valuetext", text); }
    if (popOpen === "imagebar") position(pops.imagebar, ir, "above");
  }
  function selectImage(img) {
    frameImg = img;
    const r = document.createRange();
    r.selectNode(img);
    popRange = r;
    const sel = window.getSelection();
    sel.removeAllRanges();
    sel.addRange(r);
    lastRange = r.cloneRange();
    if (!img.complete) img.addEventListener("load", () => { if (frameImg === img) placeFrame(); }, { once: true });
  }
  // imageSelected: does the range select the framed image (and no text)?
  const imageSelected = (r) => !!frameImg && !r.collapsed && r.intersectsNode(frameImg) && !stripInvisible(r.toString()).trim();
  function openImageBar(img) {
    const pop = pops.imagebar;
    if (!pop) { openImage(img); return; }
    ensureFrame();
    closePop(false);
    popReturn = null;
    selectImage(img);
    openPop("imagebar", img.getBoundingClientRect(), "above");
    frameImg = img;
    syncImageBar();
  }
  function syncImageBar() {
    const pop = pops.imagebar, img = frameImg;
    if (!pop || !img) return;
    const a = img.getAttribute("data-align") || "";
    $$("[data-img]", pop).forEach((b) => { if (IMAGE_ALIGNS.includes(b.dataset.img)) b.setAttribute("aria-pressed", String(b.dataset.img === a)); });
    const wi = field(pop, "[data-img-width]");
    if (document.activeElement !== wi) wi.value = img.getAttribute("width") || "";
    placeFrame();
  }
  const imageRunOf = (img) => { const b = readNodes([img])[0]; const r = b && b.runs && b.runs[0]; return r && r.a === "img" ? r : null; };
  // applyImageOp changes the framed image (see imageOp) and renders it anew.
  function applyImageOp(op, value) {
    const img = frameImg;
    if (!img || !img.isConnected) { closePop(false); return; }
    const run = imageRunOf(img);
    if (!run) return;
    const tpl = document.createElement("template");
    tpl.innerHTML = imgHTML(imageOp(run, op, value));
    const fresh = tpl.content.firstChild;
    img.replaceWith(fresh);
    selectImage(fresh);
    touch();
    pushState();
    syncImageBar();
  }
  if (pops.imagebar) {
    pops.imagebar.addEventListener("click", (e) => {
      const b = e.target.closest("[data-img]");
      if (!b) return;
      const op = b.dataset.img, img = frameImg;
      if (op === "alt") { if (img && img.isConnected) openImage(img); return; }
      if (op === "remove") { closePop(false); restorePopRange(); if (img && img.isConnected) command(deleteSelection, { wide: false }); return; }
      applyImageOp(op);
    });
    const wi = field(pops.imagebar, "[data-img-width]");
    const setWidth = () => {
      const v = wi.value.trim();
      if (!v) { applyImageOp("original"); return; }
      const n = Math.round(Number(v));
      if (n >= 1 && n <= MAX_IMAGE_WIDTH) applyImageOp("width", n);
      else wi.value = frameImg ? frameImg.getAttribute("width") || "" : "";
    };
    wi.addEventListener("change", setWidth);
    wi.addEventListener("keydown", (e) => { if (e.key === "Enter") { e.preventDefault(); setWidth(); } });
  }

  /* ---------------------------------------------------------- surface UI */
  surface.addEventListener("mousedown", (e) => {
    const li = e.target.closest && e.target.closest("li[data-checked]");
    if (li && e.target === li && e.clientX < li.getBoundingClientRect().left) { e.preventDefault(); toggleTask(li); }
  });
  surface.addEventListener("click", (e) => {
    // A video plays on click (static/video.js leaves the editor's to it).
    const vid = e.target.closest && e.target.closest("div.md-video");
    if (vid && inSurface(vid)) {
      const btn = e.target.closest(".sap-video-link");
      if (btn && window.sapVideo) window.sapVideo.play(btn);
      openRaw(vid);
      return;
    }
    const img = e.target.closest && e.target.closest("img");
    if (img && inSurface(img) && !img.closest("[data-raw]")) { openImageBar(img); return; }
    const raw = e.target.closest && e.target.closest("[data-raw]");
    if (raw && inSurface(raw)) { openRaw(raw); return; }
    const a = e.target.closest && e.target.closest("a[href]");
    if (a && (e.ctrlKey || e.metaKey)) { e.preventDefault(); window.open(a.href, "_blank", "noopener"); }
  });
  document.addEventListener("selectionchange", () => {
    const r = curRange();
    if (!r) return;
    lastRange = r.cloneRange();
    updateState();
    updateContext();
  });
  const STATE = {
    bold: (n) => !!closest(n, "strong,b"),
    italic: (n) => !!closest(n, "em,i"),
    strike: (n) => !!closest(n, "del,s"),
    code: (n) => !!closest(n, "code"),
    link: (n) => !!closest(n, "a"),
    heading: (n) => !!closest(n, "h1,h2,h3,h4,h5,h6"),
    quote: (n) => !!closest(n, "blockquote"),
    ul: (n) => { const li = closest(n, "li"); return !!li && li.parentNode.tagName === "UL"; },
    ol: (n) => { const li = closest(n, "li"); return !!li && li.parentNode.tagName === "OL"; },
    codeblock: (n) => !!closest(n, "pre"),
  };
  function updateState() {
    const btns = tools ? $$("[data-md]", tools) : [];
    if (mode !== "visual") { btns.forEach((b) => b.removeAttribute("aria-pressed")); return; }
    const r = curRange() || lastRange;
    const n = r && inSurface(r.startContainer) ? r.startContainer : null;
    for (const b of btns) {
      const f = STATE[b.dataset.md];
      if (f) b.setAttribute("aria-pressed", String(!!(n && f(n))));
    }
    const u = $('[data-md="undo"]'), d = $('[data-md="redo"]');
    if (u) u.disabled = hist.i <= 0 && !hist.timer;
    if (d) d.disabled = hist.i >= hist.stack.length - 1;
  }

  /* --------------------------------------------------------------- toolbar */
  function visualTool(kind, btn) {
    switch (kind) {
      case "bold": return mark("s");
      case "italic": return mark("e");
      case "strike": return mark("d");
      case "code": {
        const r = useRange();
        if (r && !r.collapsed && holderOf(r.startContainer) !== holderOf(r.endContainer)) return command(toggleCodeBlock);
        return mark("c");
      }
      case "link": return openLink();
      case "heading": return openHeading(btn);
      case "quote": return command(toggleQuote);
      case "ul": return command((b) => toggleList(b, false));
      case "ol": return command((b) => toggleList(b, true));
      case "codeblock": return command(toggleCodeBlock);
      case "hr": return command((b) => insertBlocks(deleteSelection(b), [{ type: "hr" }], { blocks: true }));
      case "table": return command((b) => insertBlocks(deleteSelection(b), [newTable(3, 3)], { blocks: true }));
      case "video": return openVideo();
      case "undo": return undo();
      case "redo": return redo();
    }
  }
  if (tools) tools.addEventListener("mousedown", (e) => { if (mode === "visual" && e.target.closest("button")) e.preventDefault(); });
  root.addEventListener("click", (e) => {
    const mb = e.target.closest("[data-editor-mode]");
    if (mb && root.contains(mb)) { setMode(mb.dataset.editorMode, true); return; }
    const b = e.target.closest("[data-md]");
    if (!b || !tools || !tools.contains(b)) return;
    if (mode === "visual") { e.stopPropagation(); if (b.dataset.md !== "heading") surface.focus({ preventScroll: true }); visualTool(b.dataset.md, b); }
    else if (mode === "markdown" && MD_EXTRA[b.dataset.md]) { e.stopPropagation(); ta.focus(); MD_EXTRA[b.dataset.md](); }
  });

  /* ----------------------------------------------------------------- modes */
  function scrollTextarea(pos) {
    const lines = ta.value.slice(0, pos).split("\n").length;
    const lh = parseFloat(getComputedStyle(ta).lineHeight) || 24;
    ta.scrollTop = Math.max(0, (lines - 3) * lh);
    const rect = ta.getBoundingClientRect();
    const y = rect.top + (lines - 1) * lh - ta.scrollTop;
    if (y < 80 || y > window.innerHeight - 40) window.scrollBy({ top: y - window.innerHeight / 3 });
  }
  function sourceRangeOfSelection() {
    flushSync();
    const r = curRange() || lastRange;
    if (!r || !inSurface(r.startContainer)) return null;
    const a = pointSource(r.startContainer, r.startOffset), b = r.collapsed ? a : pointSource(r.endContainer, r.endOffset);
    if (a >= 0 && b >= a) return [a, b];
    const t1 = topOf(r.startContainer), t2 = topOf(r.endContainer);
    const s1 = t1 && segMap.get(t1), s2 = t2 && segMap.get(t2);
    return s1 && s2 ? [s1.start, r.collapsed ? s1.start : s2.end] : null;
  }
  // pointSource maps a surface point to an offset in the Markdown source by
  // rendering everything before it in its top-level block and matching that
  // prefix against the block's source text.
  function pointSource(node, off) {
    const top = topOf(node);
    const seg = top && segMap.get(top);
    if (!seg) return -1;
    const r = document.createRange();
    r.setStartBefore(top);
    try { r.setEnd(node, off); } catch (_) { return -1; }
    if (!r.toString() && !r.cloneContents().querySelector("img,[data-raw],hr")) return seg.start;
    const pre = renderMarkdown(readNodes(Array.from(r.cloneContents().childNodes)));
    const src = ta.value.slice(seg.start, seg.end);
    for (let cut = pre; cut; cut = cut.replace(/(?:\*+|_+|~+|`+|\]\([^)]*\)|\])$/, "")) {
      if (src.startsWith(cut)) return seg.start + cut.length;
      if (cut === cut.replace(/(?:\*+|_+|~+|`+|\]\([^)]*\)|\])$/, "")) break;
    }
    const plain = r.toString();
    if (plain.length >= seg.end - seg.start) return seg.end;
    return -1;
  }
  function setMode(m, focus) {
    if (!["visual", "markdown", "preview"].includes(m)) m = "visual";
    if (m === mode) return;
    const prev = mode;
    let carry = null;
    if (prev === "visual") { carry = sourceRangeOfSelection(); flushSync(); }
    else if (prev === "markdown") carry = [ta.selectionStart, ta.selectionEnd];
    const h = (prev === "visual" ? surface : prev === "markdown" ? ta : preview).offsetHeight;
    mode = m;
    if (m !== "preview") editMode = m;
    closePop(false);
    if (m === "visual" && (stale || ta.value !== lastMd)) { render(ta.value); resetHistory(); }
    ta.hidden = m !== "markdown";
    surface.hidden = m !== "visual";
    if (preview) preview.hidden = m !== "preview";
    root.dataset.mode = m;
    $$("[data-editor-mode]").forEach((b) => { const on = b.dataset.editorMode === m; b.classList.toggle("active", on); b.setAttribute("aria-pressed", String(on)); });
    if (tools) tools.classList.toggle("invisible", m === "preview");
    $$("[data-visual-only]").forEach((b) => { b.hidden = m !== "visual"; });
    const hb = $('[data-md="heading"]');
    if (hb) { if (m === "visual") hb.setAttribute("aria-haspopup", "menu"); else { hb.removeAttribute("aria-haspopup"); hb.removeAttribute("aria-expanded"); } }
    if (m !== "preview") { try { localStorage.setItem(storeKey, m); } catch (_) { /* private mode */ } }
    if (m === "preview") loadPreview(h);
    if (focus && m === "visual") {
      surface.focus({ preventScroll: true });
      if (carry && prev === "markdown") reveal(carry[0], carry[1], true);
    } else if (focus && m === "markdown") {
      ta.focus({ preventScroll: true });
      if (carry) { ta.setSelectionRange(carry[0], carry[1]); scrollTextarea(carry[0]); }
    }
    updateState();
  }
  async function loadPreview(h) {
    if (!preview) return;
    preview.style.minHeight = Math.max(h || 0, 200) + "px";
    preview.innerHTML = `<p class="muted">${escHTML(say("Loading", "Loading…"))}</p>`;
    try {
      const res = await fetch("/admin/api/preview", {
        method: "POST", credentials: "same-origin",
        headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf() },
        body: JSON.stringify({ content: ta.value }),
      });
      if (!res.ok) throw new Error(String(res.status));
      const data = await res.json();
      if (mode === "preview") preview.innerHTML = data.html; // sanitized server-side
    } catch (_) {
      preview.innerHTML = `<p class="text-destructive">${escHTML(say("PreviewFailed", "Preview failed"))}</p>`;
    }
  }

  /* ------------------------------------------------------------ public API */
  function plainOf(md) {
    const parts = [];
    const visit = (bs) => { for (const b of bs) {
      if (b.type === "p" || b.type === "h") parts.push(runsText(b.runs));
      else if (b.type === "code") parts.push(b.text);
      else if (b.type === "quote") visit(b.blocks);
      else if (b.type === "list") for (const it of b.items) visit(it.blocks);
      else if (b.type === "table") for (const row of b.rows) parts.push(row.map(runsText).join(" "));
    } };
    visit(parseMarkdown(md));
    return parts.join(" ").replace(/\s+/g, " ").trim();
  }
  function textPoint(el, k) {
    const tw = document.createTreeWalker(el, NodeFilter.SHOW_TEXT);
    let left = k;
    for (let t = tw.nextNode(); t; t = tw.nextNode()) { if (left <= t.data.length) return { node: t, off: left }; left -= t.data.length; }
    return null;
  }
  function reveal(start, end, quiet) {
    start = Math.max(0, Math.min(+start || 0, ta.value.length));
    end = Math.max(start, Math.min(end == null ? start : +end, ta.value.length));
    if (mode === "preview") setMode(editMode, false);
    if (mode === "markdown") { ta.focus({ preventScroll: true }); ta.setSelectionRange(start, end); scrollTextarea(start); return; }
    flushSync();
    const all = tops();
    const hit = all.filter((n) => { const s = segMap.get(n); return s && s.end >= start && s.start <= end; });
    const first = hit[0] || all.filter((n) => { const s = segMap.get(n); return s && s.start <= start; }).pop() || all[0];
    if (!first) return;
    const last = hit[hit.length - 1] || first;
    surface.focus({ preventScroll: true });
    const seg = segMap.get(first);
    let done = false;
    if (seg && hit.length <= 1) {
      const phrase = plainOf(ta.value.slice(start, end));
      const text = first.textContent.replace(/\s+/g, " ");
      const approx = plainOf(ta.value.slice(seg.start, start)).length;
      if (!phrase && start === end) { const p = textPoint(first, Math.min(approx, first.textContent.length)); if (p) { setSel(p); done = true; } }
      else if (phrase) {
        let best = -1;
        for (let k = text.indexOf(phrase); k >= 0; k = text.indexOf(phrase, k + 1)) if (best < 0 || Math.abs(k - approx) < Math.abs(best - approx)) best = k;
        if (best >= 0 && first.textContent.length === text.length) {
          const a = textPoint(first, best), b = textPoint(first, best + phrase.length);
          if (a && b) { setSel(a, b); done = true; }
        }
      }
    }
    if (!done) {
      const r = document.createRange();
      r.setStart(first, 0);
      r.setEnd(last, last.childNodes.length);
      const a = normPoint(r.startContainer, r.startOffset, false), b = normPoint(r.endContainer, r.endOffset, true);
      setSel(a, start === end ? null : b);
    }
    const el = elOf(window.getSelection().anchorNode);
    if (el && !quiet) el.scrollIntoView({ block: "center", behavior: "smooth" });
    else if (el) el.scrollIntoView({ block: "nearest" });
    updateState();
  }
  function getSel() {
    if (mode !== "visual") {
      const s = ta.selectionStart, e = ta.selectionEnd;
      return { markdown: ta.value.slice(s, e), start: s, end: e };
    }
    flushSync();
    const r = curRange() || (lastRange && inSurface(lastRange.startContainer) ? lastRange : null);
    if (!r) return { markdown: "", start: -1, end: -1 };
    const span = sourceRangeOfSelection();
    if (r.collapsed) return { markdown: "", start: span ? span[0] : -1, end: span ? span[0] : -1 };
    const md = rangeMarkdown(r);
    if (span && ta.value.slice(span[0], span[1]) === md) return { markdown: md, start: span[0], end: span[1] };
    if (span) {
      const k = ta.value.indexOf(md, Math.max(0, span[0] - md.length));
      if (k >= 0 && k <= span[1]) return { markdown: md, start: k, end: k + md.length };
    }
    return { markdown: md, start: -1, end: -1 };
  }
  function mdInsertAt(md) {
    const e = ta.selectionEnd;
    ta.setSelectionRange(e, e);
    mdInsertBlock(md);
  }
  window.sapEditor = {
    mode: () => mode,
    getSelection: getSel,
    replaceSelection(md) {
      md = String(md == null ? "" : md);
      if (mode !== "visual") {
        ta.setRangeText(md, ta.selectionStart, ta.selectionEnd, "end");
        stale = true;
        emitInput();
        return;
      }
      useRange();
      const blocks = parseMarkdown(md);
      for (const b of blocks) preserve.set(blockSig(b), b.src);
      command((b) => insertBlocks(deleteSelection(b), blocks));
      flushSync();
    },
    insertAtCursor(md) {
      md = String(md == null ? "" : md).replace(/^\n+|\n+$/g, "");
      if (!md) return;
      if (mode !== "visual") { mdInsertAt(md); stale = true; return; }
      flushSync();
      const r = curRange() || (lastRange && inSurface(lastRange.endContainer) ? lastRange : null);
      let top = r ? topOf(r.endContainer) : null;
      if (!top) top = surface.lastChild;
      const blocks = parseMarkdown(md);
      for (const b of blocks) preserve.set(blockSig(b), b.src);
      const tpl = document.createElement("template");
      tpl.innerHTML = toHTML(blocks);
      const fresh = Array.from(tpl.content.childNodes);
      const emptyTop = top && isEl(top) && top.tagName === "P" && !top.textContent && !top.querySelector("img,[data-raw]");
      surface.insertBefore(tpl.content, top ? top.nextSibling : null);
      if (emptyTop) top.remove();
      surface.focus({ preventScroll: true });
      if (fresh.length) { caretAtEnd(fresh[fresh.length - 1]); elOf(fresh[0]).scrollIntoView && elOf(fresh[0]).scrollIntoView({ block: "nearest" }); }
      fixStructure();
      touch();
      pushState();
      flushSync();
    },
    setContent(md) {
      md = String(md == null ? "" : md);
      ta.value = md;
      if (mode === "visual") { render(md); pushState(); } else stale = true;
      emitInput();
    },
    reveal: (start, end) => reveal(start, end, false),
    flush: () => flushSync(),
  };

  /* ------------------------------------------------------------------ init */
  const syncRO = () => { const ro = ta.readOnly || ta.disabled; surface.setAttribute("contenteditable", ro ? "false" : "true"); surface.setAttribute("aria-readonly", String(ro)); };
  syncRO();
  new MutationObserver(syncRO).observe(ta, { attributes: true, attributeFilter: ["readonly", "disabled"] });
  let saved = null;
  try { saved = localStorage.getItem(storeKey); } catch (_) { /* storage blocked */ }
  render(ta.value);
  resetHistory();
  setMode(saved === "markdown" ? "markdown" : "visual", false);
}

if (typeof document !== "undefined" && typeof window !== "undefined") {
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", setupEditor);
  else setupEditor();
}
