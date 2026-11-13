// Page builder (ui/admin/templates/builder.html). The layouts of a theme are
// a header, a footer and one main area per page type, each made of sections
// (with a column preset and appearance options), columns and blocks. Every
// change goes through a pure operation on a copy of the layouts, so undo and
// redo are snapshots; the draft is saved to the server shortly after, and the
// preview reloads. The server validates everything again (internal/builder).
//
// Pure functions are exported for node --test (ui/admin/test/builder.test.mjs).

export const PRESETS = { "1": 1, "1-1": 2, "2-1": 2, "1-2": 2, "1-1-1": 3 };
export const PAGE_REGIONS = ["front", "home", "single", "page", "archive", "search", "404"];
const META = "footer-meta";
const CONTENT = "single-content";
// Ids the core's own markup uses, which a section anchor may not take
// (internal/builder: reservedAnchors).
const RESERVED_ANCHORS = new Set(["main", "menu", "primary-menu", "comments", "respond", "search"]);

const clone = (v) => (v == null ? v : JSON.parse(JSON.stringify(v)));

// ----------------------------------------------------------- regions --

/** getRegion returns a region of the layouts, or null. */
export function getRegion(l, name) {
  if (name === "header" || name === "footer") return l[name] || null;
  return (l.pages && l.pages[name]) || null;
}

/** setRegion returns layouts with region name replaced (null removes it). */
export function setRegion(l, name, region) {
  const out = clone(l) || {};
  if (name === "header" || name === "footer") {
    if (region) out[name] = clone(region); else delete out[name];
    return out;
  }
  out.pages = { ...(out.pages || {}) };
  if (region) out.pages[name] = clone(region); else delete out.pages[name];
  if (!Object.keys(out.pages).length) delete out.pages;
  return out;
}

/** regionPath is the prefix of a region's fields in server errors. */
export const regionPath = (name) => (name === "header" || name === "footer" ? name : `pages.${name}`);

/** ids returns every section and block id in the layouts. */
export function ids(l) {
  const out = new Set();
  const walk = (r) => {
    for (const s of r?.sections || []) {
      out.add(s.id);
      for (const c of s.columns || []) for (const b of c) out.add(b.id);
    }
  };
  walk(l.header); walk(l.footer);
  for (const r of Object.values(l.pages || {})) walk(r);
  return out;
}

/** newID returns an id that is not used in the layouts (or in taken). */
export function newID(l, prefix, taken = new Set(), rand = Math.random) {
  const used = ids(l);
  const p = String(prefix || "b").toLowerCase().replace(/[^a-z0-9-]/g, "").slice(0, 20) || "b";
  for (;;) {
    const id = `${p}-${Math.floor(rand() * 36 ** 5).toString(36).padStart(5, "0")}`;
    if (!used.has(id) && !taken.has(id)) { taken.add(id); return id; }
  }
}

// Apply fn to a copy of region name (created when missing).
function edit(l, name, fn) {
  const out = clone(l) || {};
  let r = getRegion(out, name);
  if (!r) {
    r = { sections: [] };
    if (name === "header" || name === "footer") out[name] = r;
    else (out.pages = out.pages || {})[name] = r;
  }
  r.sections = r.sections || [];
  fn(r, out);
  return out;
}

const col = (r, p) => r.sections[p.s].columns[p.c];

// ---------------------------------------------------------- sections --

/** addSection inserts an empty section with preset at index at. */
export function addSection(l, name, at, preset = "1", rand) {
  const id = newID(l, "s", new Set(), rand);
  return edit(l, name, (r) => {
    const n = PRESETS[preset] || 1;
    r.sections.splice(Math.max(0, Math.min(at, r.sections.length)), 0,
      { id, layout: PRESETS[preset] ? preset : "1", columns: Array.from({ length: n }, () => []) });
  });
}

export function removeSection(l, name, s) {
  return edit(l, name, (r) => { r.sections.splice(s, 1); });
}

/** moveSection moves section from to index to (counted without it). */
export function moveSection(l, name, from, to) {
  return edit(l, name, (r) => {
    const [sec] = r.sections.splice(from, 1);
    r.sections.splice(Math.max(0, Math.min(to, r.sections.length)), 0, sec);
  });
}

/** duplicateSection copies a section after itself with new ids. Blocks
 * allowed once per region are not copied. */
export function duplicateSection(l, name, s, types, rand) {
  const taken = new Set();
  return edit(l, name, (r) => {
    const copy = clone(r.sections[s]);
    copy.id = newID(l, "s", taken, rand);
    copy.columns = copy.columns.map((c) => c.filter((b) => !types[b.type]?.once)
      .map((b) => ({ ...b, id: newID(l, b.type, taken, rand) })));
    r.sections.splice(s + 1, 0, copy);
  });
}

/** sectionToHTML turns section s into one HTML block with markup html,
 * keeping its id, anchor, appearance and class: a section that is one HTML
 * block already keeps that block (and its translations). */
export function sectionToHTML(l, name, s, markup, rand, lang = "") {
  return edit(l, name, (r) => {
    const sec = r.sections[s];
    const sole = sec.columns.flat();
    if (sole.length === 1 && sole[0].type === "html") {
      // In another language the markup is that language's translation;
      // emptied, or the same as the main one, there is none.
      if (lang) {
        const i18n = { ...(sole[0].i18n || {}) };
        const own = { ...(i18n[lang] || {}) };
        if (markup.trim() && markup !== (sole[0].settings?.html || "")) own.html = markup; else delete own.html;
        if (Object.keys(own).length) i18n[lang] = own; else delete i18n[lang];
        if (Object.keys(i18n).length) sole[0].i18n = i18n; else delete sole[0].i18n;
      } else sole[0].settings = { ...(sole[0].settings || {}), html: markup };
      sec.layout = "1";
      sec.columns = [[sole[0]]];
      return;
    }
    sec.layout = "1";
    sec.columns = [[{ id: newID(l, "html", new Set(), rand), type: "html", settings: { html: markup, prose: "false" } }]];
  });
}

/** sectionHTML is the markup a section shows in the preview document doc,
 * as an HTML block can hold it: the columns of a block section in their
 * grid, a theme's section as it rendered, without the preview's text marks
 * (data-sap-text spans). "" when the preview does not show the section.
 * A section that is one HTML block gives its stored markup, in the
 * language lang when it has that translation. */
export function sectionHTML(doc, sec, lang = "") {
  const sole = (sec.columns || []).flat();
  if (sole.length === 1 && sole[0].type === "html") return (lang && sole[0].i18n?.[lang]?.html) || sole[0].settings?.html || "";
  const el = doc?.getElementById(sec.anchor || `sap-${sec.id}`);
  if (!el) return "";
  const inner = el.classList.contains("sap-theme-section") ? el : el.querySelector(":scope > :is(.sap-section-inner, .sap-section-edge)");
  if (!inner) return "";
  const body = cleanMarkup(inner);
  return (PRESETS[sec.layout] || 1) > 1 && inner !== el ? `<div class="sap-cols sap-cols-${sec.layout}">\n${body}\n</div>` : body;
}

/** themeHTML is what the theme's own template shows in the main part of
 * the preview document doc, for a region that is not laid out yet. */
export function themeHTML(doc) {
  const main = doc?.querySelector("main");
  return main ? cleanMarkup(main) : "";
}

// cleanMarkup is el's inner markup without the preview's text marks and
// the visual editor's (its wrappers, an empty image's placeholder).
export function cleanMarkup(el) {
  const copy = el.cloneNode(true);
  for (const m of copy.querySelectorAll("span[data-sap-text], .sap-ed-block, .sap-ed-media")) m.replaceWith(...m.childNodes);
  for (const m of copy.querySelectorAll(".sap-ed-placeholder, .sap-ed-box, .sap-ed-bar, .sap-ed-add, .sap-ed-banner")) m.remove();
  for (const e of [copy, ...copy.querySelectorAll("[contenteditable]")]) e.removeAttribute("contenteditable");
  for (const e of [copy, ...copy.querySelectorAll("*")]) {
    for (const a of [...e.attributes]) if (a.name.startsWith("data-sap-") && !MOTION_ATTRS.has(a.name)) e.removeAttribute(a.name);
    // What the motion player adds as the page is looked at.
    if (e.classList?.contains("sap-in")) { e.classList.remove("sap-in"); if (!e.classList.length) e.removeAttribute("class"); }
    if (e.style?.getPropertyValue("--sap-delay")) { e.style.removeProperty("--sap-delay"); if (!e.getAttribute("style")) e.removeAttribute("style"); }
  }
  return tidyMarkup(copy.innerHTML);
}

// The motion library's attributes (motion.js), which an HTML block keeps.
const MOTION_ATTRS = new Set(["data-sap-animate", "data-sap-stagger", "data-sap-delay"]);

/** partRoots are the parts a theme's page is made of, for turning it into
 * blocks: the element children of main, one section each, without what
 * holds no content (scripts, styles, templates). A single wrapper without
 * a class or an id of its own is looked into. */
export function partRoots(main) {
  const SKIP = new Set(["SCRIPT", "STYLE", "TEMPLATE", "NOSCRIPT", "LINK", "META"]);
  const kids = (n) => [...(n?.childNodes || [])].filter((k) => k.nodeType === 1 && !SKIP.has(k.nodeName.toUpperCase()));
  let parts = kids(main);
  for (let depth = 0; depth < 3 && parts.length === 1; depth++) {
    const box = parts[0];
    if (box.nodeName.toUpperCase() !== "DIV" || box.getAttribute("class") || box.getAttribute("id") || kids(box).length < 2) break;
    parts = kids(box);
  }
  return parts;
}

/** themeParts turns the theme's own page in the document doc into the
 * sections of a layout: [{anchor, html}], one per part of main, at most
 * max (the rest joins the last one). A part's id becomes its section's
 * anchor when it can be one, so that #links still reach it. */
export function themeParts(doc, max = 40) {
  const main = doc?.querySelector("main");
  if (!main) return [];
  const taken = new Set();
  const parts = [];
  for (const root of partRoots(main)) {
    const copy = root.cloneNode(true);
    let anchor = "";
    const id = copy.getAttribute("id") || "";
    if (validAnchor(id) && !taken.has(id)) { anchor = id; taken.add(id); copy.removeAttribute("id"); }
    const box = doc.createElement("div");
    box.append(copy);
    const html = cleanMarkup(box);
    if (!html) continue;
    if (parts.length >= max) parts[parts.length - 1].html += "\n" + html;
    else parts.push({ anchor, html });
  }
  return parts;
}

/** partsRegion is the region made of a theme's page: one full-width
 * section per part, each one HTML block with its translations
 * (translations: {lang: [html, ...]}, in the order of parts). */
export function partsRegion(l, parts, translations = {}, rand) {
  const taken = new Set();
  return {
    sections: parts.map((part, i) => {
      const block = { id: newID(l, "html", taken, rand), type: "html", settings: { html: part.html, prose: "false" } };
      for (const [lang, list] of Object.entries(translations)) {
        if (list[i] && list[i] !== part.html) (block.i18n ||= {})[lang] = { html: list[i] };
      }
      return { id: newID(l, "s", taken, rand), layout: "1", ...(part.anchor ? { anchor: part.anchor } : {}), style: { padding: "none", width: "edge" }, columns: [[block]] };
    }),
  };
}

// Elements that are part of a running text.
const INLINE = new Set(["A", "SPAN", "STRONG", "EM", "B", "I", "U", "S", "SMALL", "MARK", "CODE", "KBD", "SUB", "SUP", "ABBR", "CITE", "Q", "TIME", "BR", "WBR", "IMG", "SVG"]);
const isInline = (n) => INLINE.has(n.nodeName.toUpperCase());
const ownText = (n) => [...n.childNodes].some((k) => k.nodeType === 3 && k.data.trim());

/** textHost is the element whose text a click on t, inside the HTML block
 * root, types over: t itself, or the block of running text it is part of
 * (the heading of a highlighted word). null when there is no text of its
 * own there: an image, a box of other boxes. */
export function textHost(t, root) {
  let el = t;
  while (el && el !== root && (el.nodeType !== 1 || /^(svg|path|g|circle|rect|line|polyline|polygon|ellipse|img|br)$/i.test(el.nodeName))) el = el.parentNode;
  if (!el || el === root) return null;
  while (isInline(el) && el.parentNode && el.parentNode !== root && ownText(el.parentNode)) el = el.parentNode;
  const kids = [...el.childNodes];
  if (kids.some((k) => k.nodeType === 1 && !isInline(k))) return null;
  const text = (n) => (n.nodeType === 3 ? n.data : [...(n.childNodes || [])].map(text).join(""));
  return text(el).trim() ? el : null;
}

/** pathTo is where el is under root: the index of each element among its
 * parent's element children. follow walks it back, in another tree, and
 * gives null when that tree is not the same shape. */
export function pathTo(root, el) {
  const path = [];
  for (let n = el; n !== root; n = n.parentNode) {
    if (!n?.parentNode) return null;
    path.unshift([...n.parentNode.childNodes].filter((k) => k.nodeType === 1).indexOf(n));
  }
  return path;
}
export function follow(root, path, like) {
  let n = root;
  const names = [];
  for (let e = like; e && names.length < path.length; e = e.parentNode) names.unshift(e.nodeName.toUpperCase());
  for (let i = 0; i < path.length; i++) {
    n = [...n.childNodes].filter((k) => k.nodeType === 1)[path[i]];
    if (!n || (like && n.nodeName.toUpperCase() !== names[i])) return null;
  }
  return n;
}

/** syncText gives dst, an element of the stored markup, the text typed
 * over src, the same element in the preview: src's text nodes, and dst's
 * own elements (a highlighted word, a link, an icon) wherever src still
 * has theirs, so that only words change and never the markup. */
export function syncText(src, dst, doc) {
  const own = [...dst.childNodes].filter((n) => n.nodeType === 1);
  const text = (n) => (n.nodeType === 3 ? n.data : [...(n.childNodes || [])].map(text).join(""));
  let k = 0;
  const out = [];
  for (const n of src.childNodes) {
    if (n.nodeType === 3) { if (n.data) out.push(doc.createTextNode(n.data)); continue; }
    if (n.nodeType !== 1) continue;
    let j = k;
    while (j < own.length && own[j].nodeName.toUpperCase() !== n.nodeName.toUpperCase()) j++;
    if (j < own.length) {
      k = j + 1;
      if (!/^svg$/i.test(n.nodeName)) syncText(n, own[j], doc);
      out.push(own[j]);
    } else if (text(n)) out.push(doc.createTextNode(text(n)));
  }
  dst.replaceChildren(...out);
}

// tidyMarkup drops the blank lines and the indentation templates leave.
const tidyMarkup = (markup) => markup.split("\n").map((line) => line.replace(/\s+$/, "")).filter((line) => line.trim()).join("\n").trim();

/** fitColumns gives cols exactly n columns: extra columns are merged into
 * the last one, missing ones are added empty (as the server does). */
export function fitColumns(cols, n) {
  const out = Array.from({ length: n }, () => []);
  (cols || []).forEach((c, i) => out[Math.min(i, n - 1)].push(...c));
  return out;
}

export function setPreset(l, name, s, preset) {
  if (!PRESETS[preset]) return l;
  return edit(l, name, (r) => {
    const sec = r.sections[s];
    sec.layout = preset;
    sec.columns = fitColumns(sec.columns, PRESETS[preset]);
  });
}

/** validAnchor reports whether a can be a section's anchor (#pricing), as
 * the server checks it. */
export function validAnchor(a) {
  return /^[a-z][a-z0-9-]{0,39}$/.test(a) && !a.startsWith("sap-") && !RESERVED_ANCHORS.has(a);
}

/** anchorsTaken lists the anchors on the page of region name other than
 * section s's: the header's and the footer's show on every page. */
export function anchorsTaken(l, name, s) {
  const out = new Set();
  const add = (r, skip) => (r?.sections || []).forEach((sec, i) => { if (sec.anchor && i !== skip) out.add(sec.anchor); });
  const global = name === "header" || name === "footer";
  add(l.header, name === "header" ? s : -1);
  add(l.footer, name === "footer" ? s : -1);
  if (global) for (const r of Object.values(l.pages || {})) add(r, -1);
  else add(getRegion(l, name), s);
  return out;
}

/** setAnchor sets section s's anchor; an empty one is removed. */
export function setAnchor(l, name, s, anchor) {
  return edit(l, name, (r) => {
    const sec = r.sections[s];
    if (anchor) sec.anchor = anchor; else delete sec.anchor;
  });
}

/** validClasses normalises a CSS class field as the server does
 * (internal/builder ValidClasses): up to 5 names of lower-case letters,
 * digits and hyphens starting with a letter, without repeats. It returns
 * null when v cannot be used. */
export function validClasses(v) {
  const out = [];
  for (const c of String(v ?? "").split(/\s+/).filter(Boolean)) {
    if (!/^[a-z][a-z0-9-]{0,39}$/.test(c)) return null;
    if (!out.includes(c)) out.push(c);
  }
  return out.length > 5 ? null : out.join(" ");
}

/** setSectionClass sets section s's CSS classes; empty removes them. */
export function setSectionClass(l, name, s, cls) {
  return edit(l, name, (r) => {
    const sec = r.sections[s];
    if (cls) sec.class = cls; else delete sec.class;
  });
}

/** hasBlock reports whether a section has a block of type. */
export const hasBlock = (sec, type) => (sec?.columns || []).some((c) => c.some((b) => b.type === type));

/** setStyle sets a section appearance option; the default is not stored. */
export function setStyle(l, name, s, key, value, def) {
  return edit(l, name, (r) => {
    const sec = r.sections[s];
    const style = { ...(sec.style || {}) };
    if (!value || value === def) delete style[key]; else style[key] = value;
    if (Object.keys(style).length) sec.style = style; else delete sec.style;
  });
}

// ------------------------------------------------------------ blocks --

export function insertBlock(l, name, p, block) {
  return edit(l, name, (r) => {
    const c = col(r, p);
    c.splice(Math.max(0, Math.min(p.b, c.length)), 0, clone(block));
  });
}

export function removeBlock(l, name, p) {
  return edit(l, name, (r) => { col(r, p).splice(p.b, 1); });
}

/** moveBlock moves the block at from to position to, whose index is
 * counted without the moved block. */
export function moveBlock(l, name, from, to) {
  return edit(l, name, (r) => {
    const [b] = col(r, from).splice(from.b, 1);
    const c = col(r, to);
    c.splice(Math.max(0, Math.min(to.b, c.length)), 0, b);
  });
}

/** dropIndex converts a drop position counted with the dragged block in
 * place (what the pointer sees) into moveBlock's. */
export function dropIndex(from, to) {
  if (from.s === to.s && from.c === to.c && to.b > from.b) return { ...to, b: to.b - 1 };
  return to;
}

export function duplicateBlock(l, name, p, rand) {
  return edit(l, name, (r) => {
    const c = col(r, p);
    const copy = clone(c[p.b]);
    copy.id = newID(l, copy.type, new Set(), rand);
    c.splice(p.b + 1, 0, copy);
  });
}

/** setValue sets a block setting (lang "" ) or its translation into lang.
 * Empty values and the field's default are removed. */
export function setValue(l, name, p, key, value, lang = "", def = "") {
  return edit(l, name, (r) => {
    const b = col(r, p)[p.b];
    if (!lang) {
      const s = { ...(b.settings || {}) };
      if (value === "" || value == null || value === def) delete s[key]; else s[key] = String(value);
      if (Object.keys(s).length) b.settings = s; else delete b.settings;
      return;
    }
    const all = { ...(b.i18n || {}) };
    const t = { ...(all[lang] || {}) };
    if (value === "" || value == null || (def && value === def)) delete t[key]; else t[key] = String(value);
    if (Object.keys(t).length) all[lang] = t; else delete all[lang];
    if (Object.keys(all).length) b.i18n = all; else delete b.i18n;
  });
}

// ------------------------------------------------------------- items --
// A landing block (features, plans, questions…) repeats items, each with
// its settings and translations as a block has them.

const itemsOf = (r, p) => {
  const b = col(r, p)[p.b];
  b.items = b.items || [];
  return b;
};
const tidy = (b) => { if (!b.items.length) delete b.items; };

/** addItem inserts item into the block at p (at the end by default). */
export function addItem(l, name, p, item = {}, at = Infinity) {
  return edit(l, name, (r) => {
    const b = itemsOf(r, p);
    b.items.splice(Math.max(0, Math.min(at, b.items.length)), 0, clone(item));
  });
}

export function removeItem(l, name, p, i) {
  return edit(l, name, (r) => { const b = itemsOf(r, p); b.items.splice(i, 1); tidy(b); });
}

/** moveItem moves item from to index to. */
export function moveItem(l, name, p, from, to) {
  return edit(l, name, (r) => {
    const b = itemsOf(r, p);
    if (to < 0 || to >= b.items.length) return;
    const [it] = b.items.splice(from, 1);
    b.items.splice(to, 0, it);
  });
}

/** setItemValue sets a setting of item i (lang "") or its translation, as
 * setValue does a block's. With one, turning the setting on turns it off
 * in the other items (one highlighted plan). */
export function setItemValue(l, name, p, i, key, value, lang = "", def = "", one = false) {
  return edit(l, name, (r) => {
    const b = itemsOf(r, p);
    const it = b.items[i];
    if (!it) return;
    if (!lang) {
      const s = { ...(it.settings || {}) };
      if (value === "" || value == null || value === def) delete s[key]; else s[key] = String(value);
      if (Object.keys(s).length) it.settings = s; else delete it.settings;
      if (one && String(value) === "true") {
        b.items.forEach((x, j) => {
          if (j === i || !x.settings?.[key]) return;
          delete x.settings[key];
          if (!Object.keys(x.settings).length) delete x.settings;
        });
      }
      return;
    }
    const all = { ...(it.i18n || {}) };
    const t = { ...(all[lang] || {}) };
    if (value === "" || value == null) delete t[key]; else t[key] = String(value);
    if (Object.keys(t).length) all[lang] = t; else delete all[lang];
    if (Object.keys(all).length) it.i18n = all; else delete it.i18n;
  });
}

/** newBlock is a block of type t as the editor adds it: the type's starter
 * (sample items) when it has one. */
export function newBlock(l, t, starters = {}, settings = {}, rand) {
  const s = starters[t.type] || {};
  const b = { id: newID(l, t.type, new Set(), rand), type: t.type };
  const set = { ...(s.settings || {}), ...settings };
  if (Object.keys(set).length) b.settings = set;
  if (s.items?.length) b.items = clone(s.items);
  if (s.i18n) b.i18n = clone(s.i18n);
  return b;
}

/** reid returns a copy of region with new ids for its sections and blocks,
 * none of them used in the layouts l (a preset's ids are placeholders). */
export function reid(region, l = {}, rand) {
  const taken = new Set();
  const out = clone(region) || { sections: [] };
  for (const s of out.sections || []) {
    s.id = newID(l, "s", taken, rand);
    for (const c of s.columns || []) for (const b of c) b.id = newID(l, b.type, taken, rand);
  }
  return out;
}

/** find locates a section or block by id in a region. */
export function find(l, name, id) {
  const r = getRegion(l, name);
  if (!r || !id) return null;
  for (let s = 0; s < (r.sections || []).length; s++) {
    const sec = r.sections[s];
    if (sec.id === id) return { kind: "section", s };
    for (let c = 0; c < sec.columns.length; c++) {
      const b = sec.columns[c].findIndex((x) => x.id === id);
      if (b >= 0) return { kind: "block", s, c, b };
    }
  }
  return null;
}

/** step is where a keyboard move takes a block (dir up, down, left or
 * right), in moveBlock's terms, or null at an edge. Up and down cross into
 * the neighbouring column or section. */
export function step(l, name, p, dir) {
  const r = getRegion(l, name);
  const secs = r.sections;
  const len = (s, c) => secs[s].columns[c].length;
  if (dir === "up") {
    if (p.b > 0) return { s: p.s, c: p.c, b: p.b - 1 };
    if (p.c > 0) return { s: p.s, c: p.c - 1, b: len(p.s, p.c - 1) };
    if (p.s > 0) { const c = secs[p.s - 1].columns.length - 1; return { s: p.s - 1, c, b: len(p.s - 1, c) }; }
    return null;
  }
  if (dir === "down") {
    if (p.b < len(p.s, p.c) - 1) return { s: p.s, c: p.c, b: p.b + 1 };
    if (p.c < secs[p.s].columns.length - 1) return { s: p.s, c: p.c + 1, b: 0 };
    if (p.s < secs.length - 1) return { s: p.s + 1, c: 0, b: 0 };
    return null;
  }
  const c = p.c + (dir === "left" ? -1 : 1);
  if (c < 0 || c >= secs[p.s].columns.length) return null;
  return { s: p.s, c, b: Math.min(p.b, len(p.s, c)) };
}

/** count returns how many blocks of type a region has. */
export function count(region, type) {
  let n = 0;
  for (const s of region?.sections || []) for (const c of s.columns) for (const b of c) if (b.type === type) n++;
  return n;
}

/** allowed reports whether a block type may be added to a region. */
export function allowed(t, name, region) {
  if (!t || !(t.where || []).some((w) => w === "*" || w === name)) return false;
  return !(t.once && count(region, t.type) > 0);
}

/** required reports whether a block may not be removed from its region:
 * the footer's credits (the licence requires them) and the content of
 * posts and pages. */
export function required(type, name) {
  return (type === META && name === "footer") || (type === CONTENT && (name === "single" || name === "page"));
}

/** starter is the first layout of a region the user starts building. */
export function starter(name, msg = {}, l = {}, rand) {
  const taken = new Set();
  const id = (p) => newID(l, p, taken, rand);
  const b = (type, settings) => ({ id: id(type), type, ...(settings ? { settings } : {}) });
  const sec = (layout, columns, style) => ({ id: id("s"), layout, ...(style ? { style } : {}), columns });
  switch (name) {
    case "header": return { sections: [sec("1", [[b("site-header")]])] };
    case "footer": return {
      sections: [
        sec("1-1", [[b("menu", { menu: "footer" })], [b("social-links", { align: "right" })]]),
        sec("1", [[b(META)]], { padding: "small" }),
      ],
    };
    case "front": return { sections: [sec("1", [[b("hero", { size: "large" })]], { padding: "large" })] };
    case "home": return { sections: [sec("1", [[b("hero", { size: "large" })]]), sec("1", [[b("post-list")]])] };
    case "single": return {
      sections: [sec("1", [[b("breadcrumbs"), b("featured-image"), b(CONTENT), b("post-meta"), b("pagination"), b("comments")]], { width: "narrow" })],
    };
    case "page": return { sections: [sec("1", [[b(CONTENT)]], { width: "narrow" })] };
    case "archive": return { sections: [sec("1", [[b("breadcrumbs"), b("post-list", { layout: "list" })]])] };
    case "search": return { sections: [sec("1", [[b("search"), b("post-list", { layout: "list" })]], { width: "narrow" })] };
    case "404": return {
      sections: [sec("1", [[
        b("hero", { title: msg.title404 || "Page not found", text: msg.text404 || "", button_label: msg.button404 || "", button_url: "/" }),
        b("search"),
      ]], { width: "narrow", align: "center" })],
    };
  }
  return { sections: [] };
}

/** emptyRegion is a region with nothing in it yet but what it requires:
 * one empty section to add blocks to, the post content on posts and pages,
 * the credits line in the footer (the licence requires it). */
export function emptyRegion(name, l = {}, rand) {
  const taken = new Set();
  const id = (p) => newID(l, p, taken, rand);
  const sec = (columns, style) => ({ id: id("s"), layout: "1", ...(style ? { style } : {}), columns });
  if (name === "single" || name === "page") return { sections: [sec([[{ id: id(CONTENT), type: CONTENT }]], { width: "narrow" })] };
  if (name === "footer") return { sections: [sec([[]]), sec([[{ id: id(META), type: META }]], { padding: "small" })] };
  return { sections: [sec([[]])] };
}

/** summary is a short description of a block for the structure list. */
export function summary(block, t, menus = [], areas = []) {
  const s = block.settings || {};
  const pick = (k) => (s[k] || "").replace(/\[([^\]]*)\]\([^)]*\)/g, "$1").replace(/[#*_`>[\]()]/g, "").replace(/\s+/g, " ").trim();
  let out = "";
  if (block.type === "html") {
    out = (s.html || "").replace(/<[^>]*>?/g, " ").replace(/&[a-z#0-9]+;/gi, " ").replace(/\s+/g, " ").trim() || (s.class ? `.${s.class.split(" ").join(" .")}` : "");
  } else if (t?.fields?.some((f) => f.type === "menu")) out = menus.find((m) => m.id === (s.menu || "primary"))?.label || s.menu || "";
  else if (t?.fields?.some((f) => f.type === "area")) out = areas.find((a) => a.id === (s.area || "sidebar"))?.label || s.area || "";
  else out = pick("title") || pick("label") || pick("text") || pick("caption");
  return out.length > 48 ? out.slice(0, 47) + "…" : out;
}

/** parseErrorPath maps a server error field to its region and position:
 * "pages.home.sections[1].columns[0][2].settings.url" →
 * {region: "home", s: 1, c: 0, b: 2, item: null, key: "url", lang: ""}.
 * item is the index of a block's item (….items[3].settings.title), or -1
 * for the list itself (….items); anchor marks a section's anchor. */
export function parseErrorPath(path) {
  const m = /^(header|footer|pages\.([a-z0-9]+))(?:\.sections\[(\d+)\](?:(\.anchor|\.class)|\.columns(?:\[(\d+)\](?:\[(\d+)\](?:(\.items)(?:\[(\d+)\])?)?(?:\.(settings|i18n)\.([a-z0-9_-]+)(?:\.([a-z0-9_]+))?)?)?)?)?)?/i.exec(path || "");
  if (!m) return null;
  const n = (v) => (v == null ? null : Number(v));
  const out = { region: m[2] || m[1], s: n(m[3]), c: n(m[5]), b: n(m[6]), item: m[7] ? (m[8] == null ? -1 : Number(m[8])) : null, key: "", lang: "" };
  if (m[4] === ".anchor") out.anchor = true;
  if (m[4] === ".class") out.sectionClass = true;
  if (m[9] === "settings") out.key = m[10];
  if (m[9] === "i18n") { out.lang = m[10]; out.key = m[11] || ""; }
  return out;
}

/** chooseOption picks the option a select shows, as a native select does:
 * the one with the value, else the first. Returns its index (-1 when there
 * are no options), value and label. */
export function chooseOption(options, value) {
  let i = options.findIndex((o) => String(o.value) === String(value ?? ""));
  if (i < 0 && options.length) i = 0;
  const o = options[i];
  return { index: i, value: o ? String(o.value) : "", label: o ? String(o.label) : "" };
}

/** History keeps snapshots for undo and redo. Pushes with the same key
 * within a short time (typing in one field) are merged into one step. */
export class History {
  constructor(initial, limit = 100) { this.stack = [initial]; this.at = 0; this.limit = limit; this.key = ""; this.time = 0; }
  get current() { return this.stack[this.at]; }
  get canUndo() { return this.at > 0; }
  get canRedo() { return this.at < this.stack.length - 1; }
  push(state, key = "", now = Date.now()) {
    if (key && key === this.key && now - this.time < 1500 && this.at > 0 && !this.canRedo) {
      this.stack[this.at] = state;
    } else {
      this.stack.splice(this.at + 1);
      this.stack.push(state);
      if (this.stack.length > this.limit) this.stack.shift();
      this.at = this.stack.length - 1;
    }
    this.key = key; this.time = now;
    return state;
  }
  undo() { if (this.canUndo) this.at--; this.key = ""; return this.current; }
  redo() { if (this.canRedo) this.at++; this.key = ""; return this.current; }
}

const fmt = (s, ...args) => { let i = 0; return String(s ?? "").replace(/%[sd]/g, () => String(args[i++] ?? "")); };

// ------------------------------------------------------ visual editing --

/** markdownOf turns the HTML of a Markdown field, as typed over in the
 * preview (root's children), back into Markdown: paragraphs, headings,
 * lists, quotes, code, bold, italic and links. Other elements keep their
 * text. */
export function markdownOf(root) {
  const TEXT = 3, ELEM = 1;
  // Only childNodes, nodeName, data and getAttribute: the tests' small DOM.
  const kidsOf = (n) => [...(n.childNodes || [])];
  const elems = (n) => kidsOf(n).filter((c) => c.nodeType === ELEM);
  const textOf = (n) => (n.nodeType === TEXT ? n.data ?? n.nodeValue ?? "" : kidsOf(n).map(textOf).join(""));
  const wrap = (t, mark) => {
    const m = /^(\s*)([\s\S]*?)(\s*)$/.exec(t);
    return m[2] ? `${m[1]}${mark}${m[2]}${mark}${m[3]}` : t;
  };
  const inline = (n) => {
    if (n.nodeType === TEXT) return textOf(n).replace(/\s+/g, " ").replace(/([\\*_`[\]])/g, "\\$1");
    if (n.nodeType !== ELEM) return "";
    const kids = () => kidsOf(n).map(inline).join("");
    switch (n.nodeName) {
      case "BR": return "\n";
      case "STRONG": case "B": return wrap(kids(), "**");
      case "EM": case "I": return wrap(kids(), "_");
      case "CODE": { const t = textOf(n).replace(/`/g, ""); return t ? "`" + t + "`" : ""; }
      case "A": {
        const t = kids();
        const href = (n.getAttribute("href") || "").replace(/[()\s]/g, (c) => ({ "(": "%28", ")": "%29" })[c] || "%20");
        return href && t.trim() ? `[${t}](${href})` : t;
      }
      default: return kids();
    }
  };
  // A paragraph's lines, without what would make them a heading or a list.
  const para = (t) => t.split("\n").map((l) => l.replace(/^ +| +$/g, "")).filter(Boolean)
    .map((l) => l.replace(/^(#{1,6}\s|[-+>]\s)/, "\\$1").replace(/^(\d+)([.)]\s)/, "$1\\$2")).join("  \n");
  const out = [];
  let run = [];
  const flush = () => { const t = para(run.join("")); if (t) out.push(t); run = []; };
  for (const n of kidsOf(root)) {
    const tag = n.nodeType === ELEM ? n.nodeName : "";
    if (/^H[1-6]$/.test(tag)) {
      flush();
      const t = inline(n).replace(/\s+/g, " ").trim();
      if (t) out.push("#".repeat(Number(tag[1])) + " " + t);
    } else if (tag === "UL" || tag === "OL") {
      flush();
      const items = elems(n).filter((li) => li.nodeName === "LI")
        .map((li, i) => `${tag === "OL" ? `${i + 1}.` : "-"} ${inline(li).replace(/\s+/g, " ").trim()}`)
        .filter((l) => !/^(-|\d+\.) $/.test(l));
      if (items.length) out.push(items.join("\n"));
    } else if (tag === "BLOCKQUOTE") {
      flush();
      const t = markdownOf(n);
      if (t) out.push(t.split("\n").map((l) => (l ? "> " + l : ">")).join("\n"));
    } else if (tag === "PRE") {
      flush();
      const code = textOf(n).replace(/```/g, "").replace(/^\n+|\n+$/g, "");
      if (code.trim()) out.push("```\n" + code + "\n```");
    } else if (tag === "HR") {
      flush();
      out.push("---");
    } else if (tag === "P" || tag === "DIV" || tag === "SECTION") {
      flush();
      const inner = elems(n).some((c) => /^(P|DIV|UL|OL|H[1-6]|BLOCKQUOTE|PRE)$/.test(c.nodeName));
      if (inner) { const t = markdownOf(n); if (t) out.push(t); } else { run.push(inline(n)); flush(); }
    } else {
      run.push(inline(n));
    }
  }
  flush();
  return out.join("\n\n");
}

/** regionOfClasses is the region a preview element of class list cls
 * belongs to (shared/blocks-*: sap-region-header, sap-region-footer,
 * sap-region-<page>), or "". */
export function regionOfClasses(cls) {
  const names = [...cls].filter((c) => c.startsWith("sap-region-") && c !== "sap-region-main").map((c) => c.slice(11));
  return names[0] || "";
}

// --------------------------------------------------------------- browser --

function init(root) {
  const $ = (sel, el = root) => el.querySelector(sel);
  const $$ = (sel, el = root) => [...el.querySelectorAll(sel)];
  const msg = root.dataset;
  const state = JSON.parse(msg.state || "{}");
  const base = msg.base;
  const slug = state.theme;
  const types = Object.fromEntries((state.types || []).map((t) => [t.type, t]));
  const styles = state.styles || [];
  const langs = (state.langs || []).filter((x) => x.code !== state.main_lang);
  const regions = state.regions || [];
  const status = document.getElementById("bl-status");
  const publishBtn = document.getElementById("bl-publish");
  const discardBtn = document.getElementById("bl-discard");
  const frame = document.getElementById("bl-frame");
  const live = document.getElementById("bl-live");
  const structure = $("#bl-structure");
  const settings = $("#bl-settings");
  const intro = $("#bl-intro");
  const picker = document.getElementById("bl-picker");
  const csrf = () => document.querySelector("input[name=csrf]")?.value || "";
  const iconTpl = document.getElementById("bl-icons");

  const icon = (name) => {
    const src = iconTpl?.content.querySelector(`[data-icon="${CSS.escape(name)}"] svg`);
    return src ? document.importNode(src, true) : document.createElement("span");
  };
  const h = (tag, attrs = {}, ...kids) => {
    const el = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs)) {
      if (v == null || v === false) continue;
      if (k === "class") el.className = v;
      else if (k === "text") el.textContent = v;
      else if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
      else el.setAttribute(k, v === true ? "" : v);
    }
    for (const k of kids.flat()) if (k != null && k !== false) el.append(k);
    return el;
  };
  // A Basecoat select (the admin's "select" partial, built here): the
  // trigger has the id for <label for>, onchange gets the chosen value from
  // the hidden input's change event (ui/static/selects.js).
  const optionEls = (id, options, value) => {
    const c = chooseOption(options, value);
    return options.map((o, i) => h("div", { role: "option", id: `${id}-opt-${i}`, "data-value": String(o.value), "aria-selected": i === c.index ? "true" : null, text: o.label }));
  };
  const selectBox = (id, options, value, onchange, attrs = {}) => {
    const c = chooseOption(options, value);
    const hidden = h("input", { type: "hidden", value: c.value });
    hidden.addEventListener("change", () => onchange(hidden.value));
    return h("div", { class: "select w-full", id: `${id}-select` },
      h("button", { type: "button", id, role: "combobox", "aria-haspopup": "listbox", "aria-expanded": "false", "aria-controls": `${id}-listbox`, ...attrs },
        h("span", { class: "truncate", text: c.label }), icon("chevron-down")),
      h("div", { id: `${id}-popover`, "data-popover": true, "aria-hidden": "true" },
        h("div", { role: "listbox", id: `${id}-listbox`, "aria-orientation": "vertical", "aria-labelledby": id }, optionEls(id, options, value))),
      hidden);
  };
  // The first control of a settings panel, to focus it.
  const FOCUSABLE = "input:not([type=hidden]), textarea, .select > button";
  const say = (text) => { live.textContent = ""; setTimeout(() => { live.textContent = text; }, 30); };
  const esc = (t) => String(t).replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);
  const toast = (category, title) => document.dispatchEvent(new CustomEvent("basecoat:toast", { detail: { config: { category, title: esc(title) } } }));

  // The region to open: ?region=front (a link from a theme's sample front
  // page), else the last one edited.
  // A theme with its own front page (onepage's sample sections) opens on it.
  let region = state.theme_front ? "front" : "home";
  try { const r = localStorage.getItem("sap-builder-region"); if (regions.some((x) => x.name === r)) region = r; } catch (_) {}
  const asked = new URLSearchParams(location.search).get("region");
  // ?preset=one-page (the theme's own note) puts that ready-made page first.
  const recommended = new URLSearchParams(location.search).get("preset") || state.recommended || "";
  if (regions.some((x) => x.name === asked)) region = asked;
  let selected = null;            // id of the selected section or block
  let errors = [];                // the last refusal's field errors
  const history = new History(clone(state.layout || {}));
  const layout = () => history.current;

  // ------------------------------------------------------------ status --
  const setStatus = (text, kind = "") => { status.textContent = text; status.dataset.kind = kind; };

  // ------------------------------------------------------------ change --
  // commit records a new state, re-renders and schedules a save.
  // quietly: the change is already on the preview (typed over it), so the
  // save does not reload it.
  function commit(next, key = "", { keepSettings = false, quietly = false } = {}) {
    if (JSON.stringify(next) === JSON.stringify(layout())) return;
    history.push(next, key);
    render({ keepSettings });
    schedule(700, quietly);
  }

  // ------------------------------------------------------------- render --
  function render({ keepSettings = false } = {}) {
    renderIntro();
    renderStructure();
    if (!keepSettings) renderSettings();
    $("[data-undo]").disabled = !history.canUndo;
    $("[data-redo]").disabled = !history.canRedo;
  }

  function renderRegionSelect() {
    const root = $("#bl-region-select");
    // ● built with blocks, ⚠ has refused values.
    const opts = regions.map((r) => ({ value: r.name, label: `${r.label}${getRegion(layout(), r.name) ? " ●" : ""}${errors.some((e) => e.region === r.name) ? " ⚠" : ""}` }));
    root.querySelector('[role="listbox"]').replaceChildren(...optionEls("bl-region", opts, region));
    root.querySelector(":scope > input[type=hidden]").value = chooseOption(opts, region).value;
    root.querySelector(":scope > button > span").textContent = chooseOption(opts, region).label;
    root.refresh?.();
  }

  // primaryStart is how the region's theme page becomes editable, the
  // first thing offered: the theme's own sections where it declares them
  // (nothing is lost), else the page turned into blocks where the theme
  // has a page of its own to turn (the front page, not found), else the
  // region's starting layout, whose blocks draw what the site works out
  // (the posts, a post's content).
  function primaryStart() {
    const own = region === "front" ? (state.front_presets || []).find((p) => p.name === "theme") : null;
    if (own) {
      return { kind: "theme", label: own.label, help: own.help, run: () => {
        selected = null;
        commit(setRegion(layout(), region, reid(own.region, layout())));
        say(fmt(msg.msgPresetApplied, own.label));
      } };
    }
    if ((region === "front" && state.theme_front) || region === "404") {
      return { kind: "convert", label: msg.msgConvert, help: msg.msgConvertHelp, run: () => confirmThen(msg.msgConvertConfirm, convertTheme) };
    }
    return { kind: "build", label: msg.msgBuild, help: "", run: () => {
      const start = starter(region, { title404: msg.msgNfTitle, text404: msg.msgNfText, button404: msg.msgNfButton }, layout());
      selected = null;
      commit(setRegion(layout(), region, start));
    } };
  }

  // convertTheme turns the theme's page of the region into blocks: one
  // section per part of the page, in every language of the site. The page
  // is read again for each language (the preview may show another one, or
  // what was typed over it).
  let converting = false;
  async function convertTheme() {
    const name = region;
    const info = regions.find((x) => x.name === name);
    if (converting || !info?.preview || getRegion(layout(), name)) return;
    converting = true;
    setStatus(msg.msgConverting, "saving");
    const read = async (url) => {
      const res = await fetch(url, { credentials: "same-origin", headers: { Accept: "text/html" } });
      // The not-found page answers 404, and is what is asked for.
      if (!res.ok && !(name === "404" && res.status === 404)) throw new Error(String(res.status));
      return new DOMParser().parseFromString(await res.text(), "text/html");
    };
    try {
      const first = await read(info.preview);
      const docs = { [first.documentElement.lang || state.main_lang]: first };
      const missing = [];
      for (const code of [state.main_lang, ...langs.map((x) => x.code)]) {
        if (docs[code]) continue;
        // The same page in that language, with what the preview asks for.
        const href = first.querySelector(`link[rel="alternate"][hreflang="${CSS.escape(code)}"]`)?.getAttribute("href");
        let u = null;
        try { u = href ? new URL(href, location.origin) : null; } catch (_) {}
        if (u && u.origin === location.origin) {
          for (const [k, v] of new URL(info.preview, location.origin).searchParams) u.searchParams.set(k, v);
          try { docs[code] = await read(u.pathname + u.search); } catch (_) {}
        }
        if (!docs[code]) missing.push(code);
      }
      const parts = themeParts(docs[state.main_lang]);
      if (!parts.length) throw new Error("empty");
      const max = types.html?.fields?.find((f) => f.key === "html")?.max || 50000;
      const translations = {};
      for (const l of langs) {
        const other = docs[l.code] ? themeParts(docs[l.code]) : [];
        if (other.length === parts.length) translations[l.code] = other.map((p) => p.html);
        else if (!missing.includes(l.code)) missing.push(l.code);
      }
      if ([parts.map((p) => p.html), ...Object.values(translations)].flat().some((html) => html.length > max)) {
        setStatus("", "");
        toast("error", fmt(msg.msgConvertTooBig, max));
        return;
      }
      if (name !== region || getRegion(layout(), name)) return;
      selected = null;
      commit(setRegion(layout(), name, partsRegion(layout(), parts, translations)));
      const lost = missing.filter((c) => c !== state.main_lang).map((c) => langs.find((x) => x.code === c)?.name || c);
      const done = fmt(msg.msgConverted, parts.length) + (lost.length ? " " + fmt(msg.msgConvertedNoLang, lost.join(", ")) : "");
      toast(lost.length ? "warning" : "success", done);
      say(done);
    } catch (_) {
      setStatus("", "");
      toast("error", msg.msgConvertFailed);
    } finally {
      converting = false;
    }
  }

  // confirmThen asks message in the admin's dialog, then runs yes.
  function confirmThen(message, yes) {
    const dlg = document.getElementById("confirm-dialog");
    if (!dlg) { if (window.confirm(message)) yes(); return; }
    dlg.querySelector("#confirm-dialog-description").textContent = message;
    const accept = dlg.querySelector("[data-confirm-accept]");
    const handler = () => { dlg.close(); yes(); };
    accept.addEventListener("click", handler, { once: true });
    dlg.addEventListener("close", () => accept.removeEventListener("click", handler), { once: true });
    dlg.showModal();
    accept.focus();
  }

  // replacing runs start, which builds the region out of blocks. On the
  // front page of a theme that has its own, it asks first: what it starts
  // takes that page's place once published.
  function replacing(start) {
    if (region !== "front" || !state.theme_front) return start();
    confirmThen(msg.msgReplaceFront, start);
  }

  function renderIntro() {
    renderRegionSelect();
    const r = getRegion(layout(), region);
    const kids = [];
    const icn = region === "header" ? "panel-top" : region === "footer" ? "panel-bottom" : "layout-template";
    if (r) kids.push(h("p", { class: "bl-intro-text" }, icon(icn), h("span", { text: msg.msgBlockRegion })),
      h("p", { class: "bl-intro-text bl-intro-edit" }, icon("mouse-pointer-click"), h("span", { text: msg.msgEditHint })));
    if (!r) {
      // The theme's template is fixed: say so, and offer to edit its file
      // or to turn the page into blocks (primaryStart). Starting a new
      // page instead comes after, out of the way where there is a page of
      // the theme's to lose.
      const info = regions.find((x) => x.name === region);
      const presets = (region === "front" ? state.front_presets || [] : []).filter((p) => p.name !== "theme");
      const first = primaryStart();
      const card = (tag, attrs, iconName, title, help, badge) => h(tag, { class: "bl-front-preset bl-start", ...attrs },
        h("strong", {}, icon(iconName), h("span", { text: title }), badge ? h("span", { class: "badge", "data-variant": "secondary", text: badge }) : null),
        h("span", { text: help }));
      kids.push(h("h2", { class: "bl-empty-title" }, icon(icn), h("span", { text: msg.msgThemeRegion })),
        h("p", { class: "bl-empty-text", text: first.kind === "build" ? msg.msgThemeRegionHelp : msg.msgThemeChoice }));
      const options = [];
      if (first.kind !== "build") options.push(h("li", {}, card("button", { type: "button", "data-start": first.kind, onclick: first.run }, "layout-template", first.label, first.help, msg.msgRecommended)));
      if (info?.code) options.push(h("li", {}, card("a", { href: info.code, "data-start": "code" }, "file-code", msg.msgEditThemeFile, msg.msgEditThemeFileHelp)));
      if (options.length) kids.push(h("ul", { class: "bl-front-presets" }, options));
      const others = [];
      if (presets.length) {
        const order = [...presets].sort((a, b) => (b.name === recommended) - (a.name === recommended));
        others.push(h("p", { class: "bl-intro-or", text: msg.msgPresetsIntro }),
          h("ul", { class: "bl-front-presets" }, order.map((p) => h("li", {}, h("button", {
            type: "button", class: "bl-front-preset", "data-preset": p.name, "data-recommended": first.kind === "build" && p.name === recommended ? "" : null, onclick: () => replacing(() => {
              const start = reid(p.region, layout());
              selected = null;
              commit(setRegion(layout(), region, start));
              say(fmt(msg.msgPresetApplied, p.label));
            }),
          }, h("strong", {}, p.label, first.kind === "build" && p.name === recommended ? h("span", { class: "badge", "data-variant": "secondary", text: msg.msgRecommended }) : null),
          h("span", { text: p.help }))))));
      }
      const build = () => {
        const start = starter(region, { title404: msg.msgNfTitle, text404: msg.msgNfText, button404: msg.msgNfButton }, layout());
        selected = start.sections[0]?.id || null;
        commit(setRegion(layout(), region, start));
      };
      const empty = () => {
        const start = emptyRegion(region, layout());
        selected = start.sections[0]?.id || null;
        commit(setRegion(layout(), region, start));
        say(msg.msgStartedEmpty);
      };
      others.push(h("div", { class: "bl-empty-actions" },
        h("button", { type: "button", class: "btn", "data-size": "sm", "data-variant": presets.length || first.kind !== "build" ? "outline" : null, onclick: () => replacing(build) },
          icon("plus"), presets.length ? msg.msgBuildBasic : msg.msgBuild),
        h("button", { type: "button", class: "btn", "data-variant": presets.length ? "ghost" : "outline", "data-size": "sm", onclick: () => replacing(empty) },
          icon("columns-2"), msg.msgStartEmpty),
        htmlDialog ? h("button", { type: "button", class: "btn", "data-variant": "outline", "data-size": "sm", onclick: () => openThemeHTML(true) }, icon("file-code"), msg.msgStartBlankHtml) : null,
        // Regions with the credits or the content stay blocks; a page that
        // converts into sections needs no single block of its markup.
        htmlDialog && first.kind !== "convert" && !emptyRegion(region).sections.some((sec) => htmlLocked(sec))
          ? h("button", { type: "button", class: "btn", "data-variant": "outline", "data-size": "sm", onclick: openThemeHTML }, icon("code"), msg.msgStartHtml) : null),
        h("p", { class: "hint", text: msg.msgEmptyNext }));
      if (first.kind === "build") kids.push(...others);
      else kids.push(h("details", { class: "bl-other-starts" }, h("summary", { text: msg.msgOtherStarts }), h("div", { class: "bl-other-starts-body" }, others)));
    } else {
      if (r.sections.some((s) => s.anchor)) {
        kids.push(h("p", { class: "hint bl-intro-hint" }, msg.msgAnchorsHint, " ",
          state.menus_url ? h("a", { href: state.menus_url, target: "_blank", rel: "noopener", text: msg.msgEditMenus }) : null));
      }
      if (region === "header") {
        const id = "bl-top-bar";
        kids.push(h("label", { class: "check", for: id },
          h("input", { type: "checkbox", class: "input", role: "switch", id, checked: r.top_bar !== "off", onchange: (e) => {
            const next = clone(r);
            if (e.target.checked) delete next.top_bar; else next.top_bar = "off";
            commit(setRegion(layout(), region, next));
          } }), msg.msgTopBar));
      }
      kids.push(h("button", { type: "button", class: "btn", "data-variant": "ghost", "data-size": "sm", onclick: () => {
        selected = null;
        commit(setRegion(layout(), region, null));
        toast("info", msg.msgUnbuilt);
      } }, msg.msgUnbuild));
    }
    for (const e of errors.filter((x) => x.region === region && x.s == null)) kids.push(h("p", { class: "cz-field-error", role: "alert", text: e.message }));
    intro.replaceChildren(...kids);
    intro.dataset.built = r ? "true" : "false";
  }

  // The server's messages are in English; the ones typing can cause are
  // shown in the admin's language.
  const errText = (f, e) => (e.anchor ? (/already the anchor/.test(e.message) ? fmt(msg.msgAnchorTaken, /^"([^"]*)"/.exec(e.message)?.[1] || "") : msg.msgBadAnchor)
    : f?.type === "url" ? msg.msgBadLink : f?.type === "email" ? msg.msgBadEmail : f?.type === "class" ? msg.msgBadClass
    : /^at most \d+ characters$/.test(e.message) ? fmt(msg.msgTooLong, f?.max || "") : e.message);
  // fid is the id of a field's input: a block's, or an item's (item i).
  const fid = (key, lang, item) => `bl-f-${item != null && item >= 0 ? `i${item}-` : ""}${key}${lang ? "-" + lang : ""}`;
  const sameBlock = (e, pos) => e.region === region && e.s === pos.s && e.c === pos.c && e.b === pos.b;
  const errorsAt = (p) => errors.filter((e) => e.region === region && e.s === p.s && (p.c == null || (e.c === p.c && e.b === p.b)));

  function iconButton(name, label, onclick, extra = {}) {
    return h("button", { type: "button", class: "btn", "data-variant": "ghost", "data-size": "icon", "aria-label": label, title: label, onclick, ...extra }, icon(name));
  }

  function renderStructure() {
    const l = layout();
    const r = getRegion(l, region);
    if (!r) { structure.replaceChildren(); structure.hidden = true; return; }
    structure.hidden = false;
    const list = h("ol", { class: "bl-sections", "data-drop-sections": "" });
    r.sections.forEach((sec, s) => {
      const n = PRESETS[sec.layout] || 1;
      const meta = [sec.anchor ? `#${sec.anchor}` : "", n > 1 ? fmt(msg.msgColumns, n) : "", ...styles.map((st) => sec.style?.[st.key] && st.choices.find((c) => c.value === sec.style[st.key])?.label).filter(Boolean)].filter(Boolean).join(" · ");
      const label = fmt(msg.msgSection, s + 1);
      const secErr = errors.some((e) => e.region === region && e.s === s);
      const li = h("li", { class: "bl-section", "data-id": sec.id, "aria-current": selected === sec.id ? "true" : null, "data-invalid": secErr ? "" : null },
        h("div", { class: "bl-row bl-row-section" },
          h("button", { type: "button", class: "bl-grip", "data-grip": "section", "data-s": s, "aria-label": `${label}: ${msg.msgMove}`, title: msg.msgMove }, icon("grip-vertical")),
          h("button", { type: "button", class: "bl-select", "data-select": sec.id, "aria-pressed": selected === sec.id ? "true" : "false", onclick: () => select(sec.id) },
            icon("columns-2"), h("span", { class: "bl-name", text: label }), meta ? h("span", { class: "bl-meta", text: meta }) : null),
          htmlDialog ? iconButton("code", `${label}: ${msg.msgEditHtml}`, () => openHTML(sec, { s }), { disabled: htmlLocked(sec) || null }) : null,
          iconButton("copy", `${label}: ${msg.msgDuplicate}`, () => commit(duplicateSection(layout(), region, s, types))),
          iconButton("trash-2", `${label}: ${msg.msgDelete}`, () => {
            if (sec.columns.flat().some((b) => required(b.type, region))) { toast("info", msg.msgRequired); return; }
            if (selected === sec.id) selected = null;
            commit(removeSection(layout(), region, s)); say(msg.msgDeleted);
          }, { disabled: sec.columns.flat().some((b) => required(b.type, region)) || null })),
        h("div", { class: "bl-columns" }, sec.columns.map((blocks, c) => h("div", { class: "bl-column" },
          n > 1 ? h("p", { class: "bl-col-label", text: fmt(msg.msgColumn, c + 1) }) : null,
          h("ul", { class: "bl-blocks", "data-drop-blocks": "", "data-s": s, "data-c": c },
            blocks.length ? blocks.map((b, i) => blockRow(b, { s, c, b: i })) : h("li", { class: "bl-empty", text: msg.msgEmpty })),
          h("button", { type: "button", class: "bl-add", onclick: () => openPicker({ s, c, b: blocks.length }) }, icon("plus"), msg.msgAddBlock)))),
        // An empty section says how to fill it: blocks, or its own HTML.
        htmlDialog && !sec.columns.flat().length ? h("div", { class: "bl-empty-section" },
          h("p", { text: msg.msgEmptySection }),
          h("button", { type: "button", class: "btn", "data-variant": "secondary", "data-size": "sm", onclick: () => openHTML(sec, { s }) }, icon("code"), msg.msgWriteHtml)) : null);
      list.append(li);
    });
    const adder = h("div", { class: "bl-add-section" },
      h("span", { class: "bl-add-label", text: msg.msgAddSection }),
      h("div", { class: "bl-presets", role: "group", "aria-label": msg.msgAddSection },
        (state.presets || Object.keys(PRESETS)).map((p) => h("button", {
          type: "button", class: "bl-preset", "aria-label": `${msg.msgAddSection}: ${presetLabel(p)}`, title: presetLabel(p),
          onclick: () => {
            const next = addSection(layout(), region, r.sections.length, p);
            const sec = getRegion(next, region).sections.at(-1);
            selected = sec.id;
            commit(next);
          },
        }, presetShape(p)))));
    structure.replaceChildren(list, adder);
  }

  // data-msg-preset-2-1 is dataset["msgPreset-2-1"]: digits keep their dash.
  const presetLabel = (p) => msg["msgPreset-" + p] || p;
  function presetShape(p) {
    return h("span", { class: `bl-shape bl-shape-${p}`, "aria-hidden": "true" }, p.split("-").map(() => h("span")));
  }

  function blockRow(b, p) {
    const t = types[b.type];
    const label = t?.label || b.type;
    const sum = summary(b, t, state.menus, state.areas) || (b.items?.length ? fmt(msg.msgItemCount, b.items.length) : "");
    const bad = errorsAt(p).length > 0;
    const req = required(b.type, region);
    return h("li", { class: "bl-block", "data-id": b.id, "aria-current": selected === b.id ? "true" : null, "data-invalid": bad ? "" : null },
      h("div", { class: "bl-row" },
        h("button", { type: "button", class: "bl-grip", "data-grip": "block", "data-s": p.s, "data-c": p.c, "data-b": p.b, "aria-label": `${label}: ${msg.msgMove}`, title: msg.msgMove }, icon("grip-vertical")),
        h("button", { type: "button", class: "bl-select", "data-select": b.id, "aria-pressed": selected === b.id ? "true" : "false", onclick: () => select(b.id) },
          icon(t?.icon || "box"), h("span", { class: "bl-name", text: label }), sum ? h("span", { class: "bl-meta", text: sum }) : null,
          bad ? h("span", { class: "bl-flag", "aria-hidden": "true" }, icon("circle-alert")) : null),
        t?.once ? null : iconButton("copy", `${label}: ${msg.msgDuplicate}`, () => commit(duplicateBlock(layout(), region, p))),
        req ? h("span", { class: "bl-lock", title: msg.msgRequired, "aria-label": msg.msgRequired, role: "img" }, icon("lock"))
          : iconButton("trash-2", `${label}: ${msg.msgDelete}`, () => {
            if (selected === b.id) selected = null;
            commit(removeBlock(layout(), region, p)); say(msg.msgDeleted);
          })));
  }

  // select shows the settings of a section or block, and marks it on the
  // preview. fromPreview: it was clicked there, which keeps the focus and
  // the preview where they are.
  function select(id, { fromPreview = false } = {}) {
    selected = selected === id ? selected : id;
    renderStructure();
    renderSettings();
    const smooth = matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth";
    if (fromPreview) {
      // Only the panel scrolls (it has its own scroll on wide screens).
      const panel = settings.closest(".bl-panel");
      if (panel && panel.scrollHeight > panel.clientHeight) {
        const top = settings.getBoundingClientRect().top - panel.getBoundingClientRect().top + panel.scrollTop - 8;
        panel.scrollTo({ top, behavior: smooth });
      }
    } else {
      settings.scrollIntoView({ block: "nearest", behavior: smooth });
      settings.querySelector(FOCUSABLE)?.focus({ preventScroll: true });
    }
    visual.mark(!fromPreview);
  }

  // ----------------------------------------------------------- settings --
  function renderSettings() {
    const l = layout();
    const pos = find(l, region, selected);
    if (!pos) {
      settings.replaceChildren(h("p", { class: "bl-hint", text: getRegion(l, region) ? msg.msgSelectHint : "" }));
      settings.hidden = !getRegion(l, region);
      return;
    }
    settings.hidden = false;
    if (pos.kind === "section") return sectionSettings(pos);
    return blockSettings(pos);
  }

  function header(iconName, title, help) {
    return h("header", {}, h("h2", { id: "bl-settings-title" }, icon(iconName), h("span", { text: title })), help ? h("p", { text: help }) : null);
  }

  function moveButtons(onUp, onDown, upOK, downOK) {
    return h("div", { class: "bl-move" },
      h("button", { type: "button", class: "btn", "data-variant": "outline", "data-size": "sm", disabled: !upOK || null, onclick: onUp }, icon("chevron-up"), msg.msgMoveUp),
      h("button", { type: "button", class: "btn", "data-variant": "outline", "data-size": "sm", disabled: !downOK || null, onclick: onDown }, icon("chevron-down"), msg.msgMoveDown));
  }

  function sectionSettings(pos) {
    const r = getRegion(layout(), region);
    const sec = r.sections[pos.s];
    const label = fmt(msg.msgSection, pos.s + 1);
    const presets = h("fieldset", { class: "bl-preset-field" }, h("legend", { text: msg.msgLayout }),
      h("div", { class: "bl-presets" }, (state.presets || Object.keys(PRESETS)).map((p) => {
        const id = `bl-preset-${p}`;
        return h("label", { class: "bl-preset", for: id, title: presetLabel(p) },
          h("input", { type: "radio", class: "sr-only", name: "bl-preset", id, value: p, checked: sec.layout === p, onchange: () => commit(setPreset(layout(), region, pos.s, p), "", { keepSettings: true }) }),
          presetShape(p), h("span", { class: "sr-only", text: presetLabel(p) }));
      })));
    const fields = styles.map((st) => {
      const id = `bl-style-${st.key}`;
      return h("div", { class: "field" }, h("label", { for: id, text: st.label }),
        selectBox(id, st.choices, sec.style?.[st.key] || st.choices[0]?.value,
          (v) => commit(setStyle(layout(), region, pos.s, st.key, v, st.choices[0]?.value), "", { keepSettings: true })));
    });
    const up = () => { commit(moveSection(layout(), region, pos.s, pos.s - 1)); say(fmt(msg.msgMoved, label)); };
    const down = () => { commit(moveSection(layout(), region, pos.s, pos.s + 1)); say(fmt(msg.msgMoved, label)); };
    const errs = errors.filter((e) => e.region === region && e.s === pos.s && e.c == null && !e.anchor && !e.sectionClass).map((e) => h("p", { class: "cz-field-error", role: "alert", text: e.message }));
    settings.replaceChildren(header("columns-2", `${label} · ${msg.msgSectionSettings}`),
      h("section", { class: "bl-form" }, ...errs, htmlButton(sec, pos, msg.msgEditHtml), presets, anchorField(sec, pos), ...fields,
        // The credits' section takes no class (the server refuses one).
        hasBlock(sec, META) ? null : sectionClassField(sec, pos),
        moveButtons(up, down, pos.s > 0, pos.s < r.sections.length - 1)));
  }

  // ---------------------------------------------------- edit as HTML --
  // A section's markup, as the preview shows it, in the code editor;
  // applying it makes the section one HTML block (sanitised on the server).
  // The credits and the content of posts cannot go: they stay blocks.
  const htmlDialog = document.getElementById("bl-html-dialog");
  const htmlBox = document.getElementById("bl-html-input");
  let htmlAt = null;
  const htmlLocked = (sec) => sec.columns.flat().some((b) => b.type === META || b.type === CONTENT);
  function htmlButton(sec, pos, text) {
    if (!htmlDialog) return null;
    const locked = htmlLocked(sec);
    return h("div", { class: "field bl-html-field" },
      h("button", { type: "button", class: "btn w-full", "data-variant": "secondary", disabled: locked || null, onclick: () => openHTML(sec, { s: pos.s }) }, icon("code"), text),
      h("p", { class: "hint", text: locked ? msg.msgEditHtmlLocked : msg.msgEditHtmlHint }));
  }
  function openHTML(sec, pos) {
    const sole = sec.columns.flat();
    // An HTML block is edited in the language the preview shows.
    const shown = frameDoc()?.documentElement.lang || "";
    const lang = sole.length === 1 && sole[0].type === "html" ? langs.find((x) => x.code === shown) : null;
    const markup = sole.length ? sectionHTML(frameDoc(), sec, lang?.code) : "";
    // An empty section starts an empty editor.
    if (!markup && sole.length && !(sole.length === 1 && sole[0].type === "html")) { toast("info", msg.msgEditHtmlNoPreview); return; }
    htmlAt = { id: sec.id, s: pos.s, lang: lang?.code || "" };
    document.getElementById("bl-html-where").textContent = fmt(msg.msgSection, pos.s + 1) + (lang ? ` · ${lang.name}` : "");
    const ed = codeEditor(htmlBox, "html");
    if (ed) ed.value = markup; else htmlBox.value = markup;
    htmlDialog.showModal();
    (ed || htmlBox).focus();
  }
  // openThemeHTML starts a region from its theme template's markup: one
  // section holding it as an HTML block.
  // With blank, the region starts with an empty HTML section instead (before
  // the blocks a page cannot do without).
  function openThemeHTML(blank) {
    const markup = blank === true ? "" : themeHTML(frameDoc());
    if (blank !== true && !markup) { toast("info", msg.msgEditHtmlNoPreview); return; }
    htmlAt = { fresh: region, blank: blank === true };
    document.getElementById("bl-html-where").textContent = regions.find((r) => r.name === region)?.label || region;
    const ed = codeEditor(htmlBox, "html");
    if (ed) ed.value = markup; else htmlBox.value = markup;
    htmlDialog.showModal();
    (ed || htmlBox).focus();
  }
  if (htmlDialog) {
    htmlDialog.querySelector("[data-html-apply]").addEventListener("click", () => {
      if (htmlAt?.fresh) {
        htmlDialog.close();
        if (htmlAt.fresh !== region || getRegion(layout(), region)) return;
        // The theme's markup brings its own widths and spacing.
        const start = emptyRegion(region, layout());
        let next = setRegion(layout(), region, start);
        if (start.sections[0].columns.flat().some((b) => b.type === META || b.type === CONTENT)) next = addSection(next, region, 0);
        getRegion(next, region).sections[0].style = { padding: "none", width: "edge" };
        selected = getRegion(next, region).sections[0].id;
        commit(sectionToHTML(next, region, 0, htmlBox.value));
        say(msg.msgEditHtmlDone);
        return;
      }
      const at = htmlAt && find(layout(), region, htmlAt.id);
      htmlDialog.close();
      if (!at) return;
      selected = htmlAt.id;
      commit(sectionToHTML(layout(), region, at.s, htmlBox.value, undefined, htmlAt.lang));
      say(msg.msgEditHtmlDone);
    });
  }

  // codeEditor puts the code editor (ui/admin/src/code-editor.js) over a
  // textarea once it is in the page; without it the textarea stays.
  function codeEditor(textarea, lang) {
    const api = window.sapCodeEditor;
    if (!api) return null;
    if (textarea.isConnected) return api.attach(textarea, { lang });
    queueMicrotask(() => { if (textarea.isConnected) api.attach(textarea, { lang }); });
    return null;
  }

  // classInput is a CSS class field: checked as it is typed, and only
  // saved once it is valid (validClasses).
  function classInput(id, value, err, hint, onValid) {
    const note = h("p", { class: err ? "cz-field-error" : "hint", id: `${id}-help`, text: err ? msg.msgBadClass : hint });
    const input = h("input", {
      class: "input w-full bl-mono", type: "text", id, maxlength: 210, autocomplete: "off", spellcheck: "false", autocapitalize: "off",
      "aria-describedby": `${id}-help`, "aria-invalid": err ? "true" : null,
      oninput: (e) => {
        const v = validClasses(e.target.value);
        note.textContent = v == null ? msg.msgBadClass : hint;
        note.className = v == null ? "cz-field-error" : "hint";
        if (v == null) { e.target.setAttribute("aria-invalid", "true"); return; }
        e.target.removeAttribute("aria-invalid");
        onValid(v);
      },
    });
    input.value = value || "";
    return [input, note];
  }

  function sectionClassField(sec, pos) {
    const id = "bl-section-class";
    const err = errors.find((e) => e.region === region && e.s === pos.s && e.sectionClass);
    const [input, note] = classInput(id, sec.class, err, msg.msgClassHint,
      (v) => commit(setSectionClass(layout(), region, pos.s, v), `${sec.id}.class`, { keepSettings: true }));
    return h("div", { class: "field" }, h("label", { for: id, text: msg.msgClass }), input, note);
  }

  // anchorField edits a section's anchor (#pricing): checked as it is
  // typed, and only saved once it is valid and free on this page.
  function anchorField(sec, pos) {
    const id = "bl-anchor";
    const err = errors.find((e) => e.region === region && e.s === pos.s && e.anchor);
    const note = h("p", { class: err ? "cz-field-error" : "hint", id: `${id}-help`, text: err ? errText(null, err) : msg.msgAnchorHint });
    const input = h("input", {
      class: "input w-full", type: "text", id, maxlength: 40, autocomplete: "off", spellcheck: "false", autocapitalize: "off",
      "aria-describedby": `${id}-help`, "aria-invalid": err ? "true" : null,
      oninput: (e) => {
        const v = e.target.value.trim();
        const problem = !v ? "" : !validAnchor(v) ? msg.msgBadAnchor
          : anchorsTaken(layout(), region, pos.s).has(v) ? fmt(msg.msgAnchorTaken, v) : "";
        note.textContent = problem || msg.msgAnchorHint;
        note.className = problem ? "cz-field-error" : "hint";
        if (problem) { e.target.setAttribute("aria-invalid", "true"); return; }
        e.target.removeAttribute("aria-invalid");
        commit(setAnchor(layout(), region, pos.s, v), `${sec.id}.anchor`, { keepSettings: true });
      },
    });
    input.value = sec.anchor || "";
    return h("div", { class: "field" }, h("label", { for: id, text: msg.msgAnchor }),
      h("div", { class: "bl-anchor" }, h("span", { class: "bl-anchor-hash", "aria-hidden": "true", text: "#" }), input), note);
  }

  // fieldInput is the input of field f of block b, or of its item number
  // item, in the main language (lang "") or a translation.
  function fieldInput(f, b, pos, lang, item = null) {
    const id = fid(f.key, lang, item);
    const obj = item == null ? b : b.items?.[item] || {};
    const stored = lang ? obj.i18n?.[lang]?.[f.key] || "" : obj.settings?.[f.key] || "";
    // A theme's section shows its texts until they are edited: the inputs
    // start with them, and a text typed back to one is not stored.
    const themed = item == null && String(b.type).startsWith("theme-") && f.type !== "image";
    const theirs = themed ? (lang ? f.hints?.[lang] : f.default) || "" : "";
    const shown = stored || theirs;
    const set = (v) => {
      const p = find(layout(), region, b.id);
      const def = lang ? theirs : f.default || "";
      const next = item == null ? setValue(layout(), region, p, f.key, v, lang, def)
        : setItemValue(layout(), region, p, item, f.key, v, lang, def, Boolean(f.one));
      // Turning on a one-only setting turns it off elsewhere: redraw.
      commit(next, `${b.id}.${item ?? ""}.${f.key}.${lang}`, { keepSettings: !(f.one && v === "true") });
    };
    const err = errors.find((e) => sameBlock(e, pos) && (e.item ?? null) === item && e.key === f.key && e.lang === lang);
    const help = [];
    if (f.help && !lang) help.push(f.help);
    if (f.type === "url" && !lang) help.push(msg.msgLinkHint);
    const describedBy = help.length || err ? `${id}-help` : null;
    const common = { id, "aria-describedby": describedBy, "aria-invalid": err ? "true" : null };
    let input;
    // A translation shows the main text, or what a theme's section shows
    // in that language until edited (f.hints).
    const main = lang ? obj.settings?.[f.key] || f.hints?.[lang] || "" : "";
    switch (f.type) {
      case "bool":
        return h("div", { class: "field" }, h("label", { class: "check", for: id },
          h("input", { type: "checkbox", class: "input", role: "switch", ...common, checked: (stored || f.default || "false") === "true", onchange: (e) => set(e.target.checked ? "true" : "false") }), f.label),
        describedBy ? h("p", { class: err ? "cz-field-error" : "hint", id: `${id}-help`, text: err ? errText(f, err) : help.join(" ") }) : null);
      case "icon": {
        // A grid of the icons on offer, as radio buttons.
        const cur = stored || f.default || "";
        return h("fieldset", { class: "field bl-icon-field", id, "aria-describedby": describedBy, "aria-invalid": err ? "true" : null },
          h("legend", { text: f.label }),
          h("div", { class: "bl-icon-grid" }, (f.choices || []).map((c) => {
            const cid = `${id}-${c.value}`;
            return h("label", { class: "bl-icon-choice", for: cid, title: c.label },
              h("input", { type: "radio", class: "sr-only", name: `${id}-icon`, id: cid, value: c.value, checked: c.value === cur, onchange: () => set(c.value) }),
              icon(c.value), h("span", { class: "sr-only", text: c.label }));
          })),
          describedBy ? h("p", { class: err ? "cz-field-error" : "hint", id: `${id}-help`, text: err ? errText(f, err) : help.join(" ") }) : null);
      }
      case "select": case "menu": case "area": {
        const opts = f.type === "menu" ? state.menus.map((m) => ({ value: m.id, label: m.label }))
          : f.type === "area" ? state.areas.map((a) => ({ value: a.id, label: a.label })) : f.choices;
        const cur = stored || f.default || opts[0]?.value || "";
        input = selectBox(id, opts, cur, set, common);
        break;
      }
      case "image": {
        const cur = stored;
        const img = state.images.find((m) => String(m.id) === cur);
        input = h("div", { class: "bl-image" },
          img ? h("img", { src: img.thumb, alt: "", class: "bl-thumb" }) : h("span", { class: "bl-thumb bl-thumb-empty" }, icon("image")),
          selectBox(id, [{ value: "", label: msg.msgNoImage }, ...state.images.map((m) => ({ value: String(m.id), label: m.name || `#${m.id}` }))], cur, set, common));
        if (!state.images.length) help.push(msg.msgNoImages);
        break;
      }
      case "class": {
        const [el, note] = classInput(id, stored, err, help.join(" "), set);
        return h("div", { class: "field" }, h("label", { for: id, text: f.label }), el, note);
      }
      case "html":
        // Code: monospace, no spell checking; sanitised on the server.
        input = h("textarea", { class: "textarea w-full bl-code", rows: 14, maxlength: f.max || null, spellcheck: "false", autocapitalize: "off",
          autocomplete: "off", ...common, oninput: (e) => set(e.target.value) });
        input.value = stored;
        codeEditor(input, "html");
        break;
      case "textarea": case "markdown":
        input = h("textarea", { class: "textarea w-full", rows: f.type === "markdown" ? 7 : 3, maxlength: f.max || null, placeholder: main || (lang ? null : f.default) || null, ...common, oninput: (e) => set(e.target.value) });
        input.value = shown;
        break;
      default:
        input = h("input", {
          class: "input w-full", type: f.type === "email" ? "email" : f.type === "int" ? "number" : "text", inputmode: f.type === "url" ? "url" : null,
          maxlength: f.max || (f.type === "url" ? 2000 : null), min: f.type === "int" ? f.min : null, max: f.type === "int" ? f.max : null,
          placeholder: main || f.default || null, autocomplete: "off", spellcheck: f.type === "url" || f.type === "email" ? "false" : null,
          ...common, oninput: (e) => set(e.target.value),
        });
        input.value = shown;
    }
    return h("div", { class: "field" }, h("label", { for: id, text: f.label }), input,
      describedBy || help.length ? h("p", { class: err ? "cz-field-error" : "hint", id: `${id}-help`, text: err ? errText(f, err) : help.join(" ") }) : null);
  }

  function blockSettings(pos) {
    const r = getRegion(layout(), region);
    const b = r.sections[pos.s].columns[pos.c][pos.b];
    const t = types[b.type] || { label: b.type, fields: [] };
    const parts = [header(t.icon || "box", t.label, t.help)];
    const form = h("section", { class: "bl-form" });
    for (const f of t.fields) form.append(fieldInput(f, b, pos, ""));
    form.append(...translations(t.fields, b, pos, null));
    if (t.items) form.append(itemsEditor(t, b, pos));
    const blockErr = errors.find((e) => sameBlock(e, pos) && !e.key && e.item == null);
    if (blockErr) form.prepend(h("p", { class: "cz-field-error", role: "alert", text: blockErr.message }));
    const up = step(layout(), region, pos, "up");
    const down = step(layout(), region, pos, "down");
    form.append(moveButtons(() => moveTo(pos, up, t.label), () => moveTo(pos, down, t.label), Boolean(up), Boolean(down)));
    const html = htmlButton(r.sections[pos.s], pos, msg.msgEditSectionHtml);
    if (html) form.append(html);
    parts.push(form);
    settings.replaceChildren(...parts);
  }

  // translations are the fields of a block (item null) or of one of its
  // items in each of the site's other languages, folded away until used.
  function translations(fields, b, pos, item) {
    const tr = fields.filter((f) => f.i18n);
    if (!tr.length || !langs.length) return [];
    const obj = item == null ? b : b.items?.[item] || {};
    return langs.map((lg) => {
      const open = errors.some((e) => e.lang === lg.code && sameBlock(e, pos) && (e.item ?? null) === item) || Boolean(obj.i18n?.[lg.code]);
      return h("details", { class: "bl-i18n", open: open || null },
        h("summary", {}, icon("languages"), h("span", { text: `${msg.msgTranslations}: ${lg.name}` })),
        h("p", { class: "hint", text: msg.msgTranslationHint }),
        tr.map((f) => fieldInput(f, b, pos, lg.code, item)));
    });
  }

  // The items editor of a landing block: one folding row per item, with
  // its fields and translations, buttons to reorder and remove it, and one
  // to add another up to the type's limit. Open rows stay open across
  // redraws (keyed by block id and position).
  const openItems = new Set();
  const remapOpen = (bid, map) => {
    const keep = [...openItems].filter((k) => k.startsWith(bid + ":"));
    for (const k of keep) openItems.delete(k);
    for (const k of keep) { const j = map(Number(k.slice(bid.length + 1))); if (j >= 0) openItems.add(`${bid}:${j}`); }
  };
  function itemsEditor(t, b, pos) {
    const sch = t.items;
    const its = b.items || [];
    const at = () => find(layout(), region, b.id);
    const preview = (it) => {
      const s = it.settings || {};
      const f = sch.fields.find((x) => (x.type === "text" || x.type === "textarea") && s[x.key]);
      const text = f ? String(s[f.key]).replace(/\s+/g, " ").trim() : "";
      return text.length > 42 ? text.slice(0, 41) + "…" : text;
    };
    const list = h("ol", { class: "bl-items" }, its.map((it, i) => {
      const key = `${b.id}:${i}`;
      const bad = errors.some((e) => sameBlock(e, pos) && e.item === i);
      const open = openItems.has(key) || bad;
      const title = `${sch.label} ${i + 1}`;
      const bodyId = `bl-item-${i}`;
      const body = h("div", { class: "bl-item-body", id: bodyId, hidden: !open || null },
        sch.fields.map((f) => fieldInput(f, b, pos, "", i)), ...translations(sch.fields, b, pos, i));
      const toggle = h("button", { type: "button", class: "bl-select bl-item-toggle", "aria-expanded": String(open), "aria-controls": bodyId, onclick: (e) => {
        const show = body.hidden;
        body.hidden = !show;
        e.currentTarget.setAttribute("aria-expanded", String(show));
        if (show) openItems.add(key); else openItems.delete(key);
      } }, icon("chevron-down"), h("span", { class: "bl-name", text: title }), h("span", { class: "bl-meta", text: preview(it) }),
      bad ? h("span", { class: "bl-flag", "aria-hidden": "true" }, icon("circle-alert")) : null);
      const move = (to) => {
        remapOpen(b.id, (j) => (j === i ? to : j === to ? i : j));
        commit(moveItem(layout(), region, at(), i, to));
        say(fmt(msg.msgMoved, title));
      };
      return h("li", { class: "bl-item", "data-invalid": bad ? "" : null },
        h("div", { class: "bl-row" }, toggle,
          iconButton("chevron-up", `${title}: ${msg.msgMoveUp}`, () => move(i - 1), { disabled: i === 0 || null }),
          iconButton("chevron-down", `${title}: ${msg.msgMoveDown}`, () => move(i + 1), { disabled: i === its.length - 1 || null }),
          iconButton("trash-2", `${title}: ${msg.msgDelete}`, () => {
            remapOpen(b.id, (j) => (j === i ? -1 : j > i ? j - 1 : j));
            commit(removeItem(layout(), region, at(), i));
            say(msg.msgDeleted);
          })),
        body);
    }));
    const full = its.length >= sch.max;
    const add = h("button", { type: "button", class: "btn", "data-variant": "outline", "data-size": "sm", disabled: full || null, onclick: () => {
      openItems.add(`${b.id}:${its.length}`);
      commit(addItem(layout(), region, at(), {}));
      settings.querySelector(`#bl-item-${its.length} :is(${FOCUSABLE})`)?.focus();
    } }, icon("plus"), sch.add);
    const listErr = errors.filter((e) => sameBlock(e, pos) && e.item === -1).map((e) => h("p", { class: "cz-field-error", role: "alert", text: e.message }));
    return h("div", { class: "bl-items-field", role: "group", "aria-labelledby": "bl-items-title" },
      h("p", { class: "bl-items-title", id: "bl-items-title", text: fmt(msg.msgItems, its.length, sch.max) }),
      ...listErr, its.length ? list : h("p", { class: "hint", text: msg.msgNoItems }),
      h("div", { class: "bl-items-add" }, add, full ? h("span", { class: "hint", text: fmt(msg.msgMaxItems, sch.max) }) : null));
  }

  function moveTo(from, to, label) {
    if (!to) { say(msg.msgCannotMove); return; }
    commit(moveBlock(layout(), region, from, to));
    say(fmt(msg.msgMoved, label));
  }

  // ------------------------------------------------------------- picker --
  let pickAt = null;
  // at is where the block goes ({s, c, b}); with fresh, it goes into a new
  // section inserted at index s.
  function openPicker(at) {
    pickAt = at;
    const r = getRegion(layout(), region);
    const list = document.getElementById("bl-types");
    const where = regions.find((x) => x.name === region)?.label || region;
    document.getElementById("bl-picker-where").textContent = at.fresh ? `${where} · ${msg.msgNewSection}`
      : `${where} · ${fmt(msg.msgSection, at.s + 1)}${(PRESETS[r.sections[at.s].layout] || 1) > 1 ? " · " + fmt(msg.msgColumn, at.c + 1) : ""}`;
    list.replaceChildren(...(state.types || []).filter((t) => t.where.some((w) => w === "*" || w === region)).map((t) => {
      const ok = allowed(t, region, r);
      return h("li", {}, h("button", { type: "button", class: "bl-type", disabled: !ok || null, onclick: () => addBlock(t) },
        icon(t.icon), h("span", { class: "bl-type-text" }, h("strong", { text: t.label }), h("span", { text: ok ? t.help || "" : msg.msgOnce }))));
    }));
    picker.showModal();
    list.querySelector("button:not([disabled])")?.focus();
  }

  function addBlock(t) {
    picker.close();
    const settings0 = {};
    if (t.fields.some((f) => f.key === "area" && f.type === "area")) {
      const areas = state.areas.map((a) => a.id);
      const area = areas.includes("sidebar") ? "sidebar" : areas.find((a) => a !== "top") || areas[0];
      if (area && area !== "sidebar") settings0.area = area;
    }
    let next = layout();
    let at = pickAt;
    if (at.fresh) {
      next = addSection(next, region, at.s, "1");
      at = { s: at.s, c: 0, b: 0 };
    }
    const b = newBlock(next, t, state.starters || {}, settings0);
    commit(insertBlock(next, region, at, b));
    select(b.id);
  }

  // ------------------------------------------------------- drag and drop --
  // The dragged section or block is lifted: a copy follows the pointer,
  // the item leaves the list, and a gap of its size opens where it would
  // land, the other items sliding aside (flip).
  let drag = null;
  const gap = h("li", { class: "bl-gap", "aria-hidden": "true" });
  const reduced = () => matchMedia("(prefers-reduced-motion: reduce)").matches;
  function flip(change) {
    const items = $$(".bl-section, .bl-block, .bl-add-section", structure);
    const before = new Map(items.map((el) => [el, el.getBoundingClientRect().top]));
    change();
    if (reduced()) return;
    for (const el of items) {
      const d = before.get(el) - el.getBoundingClientRect().top;
      if (d) el.animate([{ transform: `translateY(${d}px)` }, { transform: "none" }], { duration: 180, easing: "cubic-bezier(.2,.8,.2,1)" });
    }
  }

  structure.addEventListener("pointerdown", (e) => {
    const grip = e.target.closest(".bl-grip");
    if (!grip || e.button !== 0) return;
    const kind = grip.dataset.grip;
    const from = kind === "section" ? { s: +grip.dataset.s } : { s: +grip.dataset.s, c: +grip.dataset.c, b: +grip.dataset.b };
    drag = { kind, from, grip, x: e.clientX, y: e.clientY, active: false, target: null, item: grip.closest("li"), ghost: null };
    grip.setPointerCapture(e.pointerId);
  });
  structure.addEventListener("pointermove", (e) => {
    if (!drag) return;
    if (!drag.active) {
      if (Math.hypot(e.clientX - drag.x, e.clientY - drag.y) < 5) return;
      drag.active = true;
      const rc = drag.item.getBoundingClientRect();
      drag.dx = drag.x - rc.left;
      drag.dy = drag.y - rc.top;
      // CSSOM (allowed by the CSP): the copy and the gap are sized by script.
      drag.ghost = drag.item.cloneNode(true);
      drag.ghost.classList.add("bl-ghost");
      drag.ghost.removeAttribute("data-id");
      drag.ghost.style.width = `${rc.width}px`;
      document.body.append(drag.ghost);
      gap.style.height = `${Math.min(rc.height, drag.kind === "section" ? 120 : rc.height)}px`;
      gap.dataset.kind = drag.kind;
      flip(() => { drag.item.before(gap); drag.item.classList.add("is-dragging"); });
      root.classList.add("is-dragging");
    }
    e.preventDefault();
    drag.ghost.style.left = `${e.clientX - drag.dx}px`;
    drag.ghost.style.top = `${e.clientY - drag.dy}px`;
    const t = dropTarget(e.clientX, e.clientY);
    if (t && (!drag.target || t.s !== drag.target.s || t.c !== drag.target.c || t.b !== drag.target.b)) flip(() => placeGap(t));
    drag.target = t;
    autoscroll(e.clientY);
  });
  const endDrag = (e, cancel) => {
    if (!drag) return;
    const d = drag;
    drag = null;
    try { d.grip.releasePointerCapture(e.pointerId); } catch (_) {}
    d.ghost?.remove();
    gap.remove();
    root.classList.remove("is-dragging");
    d.item.classList.remove("is-dragging");
    if (!d.active || cancel || !d.target) return;
    const label = d.kind === "section" ? fmt(msg.msgSection, d.from.s + 1) : types[getRegion(layout(), region).sections[d.from.s].columns[d.from.c][d.from.b]?.type]?.label || "";
    if (d.kind === "section") {
      const to = d.target.s > d.from.s ? d.target.s - 1 : d.target.s;
      if (to !== d.from.s) { commit(moveSection(layout(), region, d.from.s, to)); say(fmt(msg.msgMoved, label)); }
    } else {
      const to = dropIndex(d.from, d.target);
      if (to.s !== d.from.s || to.c !== d.from.c || to.b !== d.from.b) { commit(moveBlock(layout(), region, d.from, to)); say(fmt(msg.msgMoved, label)); }
    }
    focusGrip(d.kind === "section" ? getRegion(layout(), region).sections[d.target.s > d.from.s ? d.target.s - 1 : d.target.s]?.id : find(layout(), region, d.item.dataset.id) && d.item.dataset.id);
  };
  structure.addEventListener("pointerup", (e) => endDrag(e, false));
  structure.addEventListener("pointercancel", (e) => endDrag(e, true));

  // dropTarget is the position under the pointer: {s} for a section, or
  // {s, c, b} for a block, counted with the dragged item in place.
  function dropTarget(x, y) {
    if (drag.kind === "section") {
      const items = $$(":scope > .bl-sections > .bl-section", structure);
      let s = items.length;
      for (let i = 0; i < items.length; i++) {
        const rc = items[i].getBoundingClientRect();
        if (y < rc.top + rc.height / 2) { s = i; break; }
      }
      return { s };
    }
    const el = document.elementFromPoint(x, y);
    let ul = el?.closest(".bl-column")?.querySelector(".bl-blocks");
    if (!ul) return drag.target;
    const items = [...ul.querySelectorAll(":scope > .bl-block")];
    let b = items.length;
    for (let i = 0; i < items.length; i++) {
      const rc = items[i].getBoundingClientRect();
      if (y < rc.top + rc.height / 2) { b = i; break; }
    }
    return { s: +ul.dataset.s, c: +ul.dataset.c, b };
  }

  // placeGap puts the gap where target t would drop the item.
  function placeGap(t) {
    if (drag.kind === "section") {
      const items = $$(":scope > .bl-sections > .bl-section", structure);
      if (t.s < items.length) items[t.s].before(gap); else $(".bl-sections", structure).append(gap);
      return;
    }
    const ul = $(`.bl-blocks[data-s="${t.s}"][data-c="${t.c}"]`, structure);
    if (!ul) return;
    const items = [...ul.querySelectorAll(":scope > .bl-block")];
    if (t.b < items.length) items[t.b].before(gap); else ul.append(gap);
  }

  function autoscroll(y) {
    const panel = $(".bl-panel");
    const rc = panel.getBoundingClientRect();
    if (panel.scrollHeight <= panel.clientHeight) {
      if (y < 60) window.scrollBy(0, -12); else if (y > innerHeight - 60) window.scrollBy(0, 12);
      return;
    }
    if (y < rc.top + 40) panel.scrollTop -= 12; else if (y > rc.bottom - 40) panel.scrollTop += 12;
  }

  function focusGrip(id) {
    if (!id) return;
    structure.querySelector(`li[data-id="${CSS.escape(id)}"] > .bl-row > .bl-grip`)?.focus();
  }

  // Keyboard moves on a handle.
  structure.addEventListener("keydown", (e) => {
    const grip = e.target.closest(".bl-grip");
    if (!grip || e.ctrlKey || e.metaKey) return;
    const dir = { ArrowUp: "up", ArrowDown: "down", ArrowLeft: "left", ArrowRight: "right" }[e.key];
    if (!dir) return;
    e.preventDefault();
    const id = grip.closest("li").dataset.id;
    if (grip.dataset.grip === "section") {
      const s = +grip.dataset.s;
      const n = getRegion(layout(), region).sections.length;
      const to = dir === "up" ? s - 1 : dir === "down" ? s + 1 : -1;
      if (to < 0 || to >= n) { say(msg.msgCannotMove); return; }
      commit(moveSection(layout(), region, s, to));
      say(fmt(msg.msgMoved, fmt(msg.msgSection, to + 1)));
    } else {
      const p = { s: +grip.dataset.s, c: +grip.dataset.c, b: +grip.dataset.b };
      const to = step(layout(), region, p, dir);
      const label = types[getRegion(layout(), region).sections[p.s].columns[p.c][p.b].type]?.label || "";
      if (!to) { say(msg.msgCannotMove); return; }
      commit(moveBlock(layout(), region, p, to));
      const sec = getRegion(layout(), region).sections[to.s];
      say(fmt(msg.msgMoved, `${label} · ${fmt(msg.msgSection, to.s + 1)}${sec.columns.length > 1 ? " · " + fmt(msg.msgColumn, to.c + 1) : ""}`));
    }
    focusGrip(id);
  });

  // ------------------------------------------------------- undo / redo --
  function undoRedo(redo) {
    const before = layout();
    const next = redo ? history.redo() : history.undo();
    if (next === before) return;
    if (!find(next, region, selected)) selected = null;
    render();
    schedule();
  }
  $("[data-undo]").addEventListener("click", () => undoRedo(false));
  $("[data-redo]").addEventListener("click", () => undoRedo(true));
  document.addEventListener("keydown", (e) => {
    if (!(e.ctrlKey || e.metaKey) || e.altKey) return;
    const k = e.key.toLowerCase();
    if (k !== "z" && k !== "y") return;
    // Text fields keep their own undo.
    if (e.target.closest?.("input[type=text], input:not([type]), input[type=email], textarea")) return;
    if (picker.open) return;
    e.preventDefault();
    undoRedo(k === "y" || e.shiftKey);
  });

  // ------------------------------------------------------------- regions --
  $("#bl-region-select").addEventListener("change", (e) => {
    region = e.target.value;
    try { localStorage.setItem("sap-builder-region", region); } catch (_) {}
    selected = null;
    render();
    loadPreview();
  });

  // ----------------------------------------------------- visual editing --
  // The preview is where the page is edited. The server marks a draft
  // preview's sections (data-sap-sec), blocks (data-sap-blk) and the texts
  // and images they show (data-sap-f, data-sap-i for an item): a click on a
  // text types over it, on an image opens the media library, anywhere else
  // in a block shows its settings. The section under the pointer gets a
  // toolbar (move, duplicate, delete, add a block) and a button to add a
  // section after it. A part still drawn by the theme's fixed template
  // offers to become editable.
  const visual = (() => {
    let doc = null;
    let typing = null;   // the element being typed over
    let hoverSec = null; // the section under the pointer
    let ui = null;       // the overlay's elements in doc
    const mainLang = state.main_lang || "";
    const mediaOpener = document.getElementById("bl-media-opener");
    let pickImage = null; // what a chosen image goes to
    let reveal = false;   // scroll to the selection once the preview reloads

    const regionLabel = (name) => regions.find((x) => x.name === name)?.label || name;
    const regionOf = (el) => { const r = el.closest(".sap-region"); return r ? regionOfClasses(r.classList) : ""; };
    // The preview's language, as setValue takes it ("" for the main one).
    const langOf = () => { const l = doc?.documentElement.lang || ""; return l && l !== mainLang && langs.some((x) => x.code === l) ? l : ""; };
    const blockOf = (id) => { const p = find(layout(), region, id); return p?.kind === "block" ? { p, b: col(getRegion(layout(), region), p)[p.b] } : null; };
    const fieldOf = (type, key, item) => (item != null ? types[type]?.items?.fields : types[type]?.fields)?.find((f) => f.key === key) || null;
    const valueOf = (b, item, key, lang) => {
      const obj = item == null ? b : b.items?.[item] || {};
      return (lang ? obj.i18n?.[lang]?.[key] : obj.settings?.[key]) || "";
    };
    const iconIn = (name) => doc.importNode(icon(name), true);
    const reduced = () => matchMedia("(prefers-reduced-motion: reduce)").matches;

    // The overlay: boxes over the hovered and the selected block, the
    // hovered section's toolbar and "add a section" button.
    function build() {
      const mk = (tag, cls, attrs = {}) => { const el = doc.createElement(tag); el.className = cls; for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, v); return el; };
      const hover = mk("div", "sap-ed-box sap-ed-hover", { "aria-hidden": "true" });
      const hoverTag = mk("span", "sap-ed-tag");
      hover.append(hoverTag);
      const sel = mk("div", "sap-ed-box sap-ed-selected", { "aria-hidden": "true" });
      const selTag = mk("span", "sap-ed-tag");
      sel.append(selTag);
      const bar = mk("div", "sap-ed-bar", { role: "toolbar" });
      const label = mk("span", "sap-ed-label");
      const btn = (act, iconName, title) => { const b = mk("button", "", { type: "button", "data-act": act, title, "aria-label": title }); b.append(iconIn(iconName)); return b; };
      bar.append(label, btn("up", "chevron-up", msg.msgMoveUp), btn("down", "chevron-down", msg.msgMoveDown),
        btn("block", "plus", msg.msgAddBlock), btn("settings", "sliders-horizontal", msg.msgEdit),
        btn("duplicate", "copy", msg.msgDuplicate), btn("delete", "trash-2", msg.msgDelete));
      const add = mk("button", "sap-ed-add", { type: "button" });
      add.append(iconIn("plus"), doc.createTextNode(msg.msgAddSectionHere));
      bar.addEventListener("click", (e) => {
        const b = e.target.closest("button[data-act]");
        if (!b || b.disabled || !hoverSec) return;
        e.preventDefault();
        const reg = regionOf(hoverSec);
        if (reg && reg !== region) switchRegion(reg);
        sectionAction(b.dataset.act, hoverSec.dataset.sapSec);
      });
      add.addEventListener("click", (e) => {
        e.preventDefault();
        const reg = hoverSec ? regionOf(hoverSec) : "";
        if (reg && reg !== region) switchRegion(reg);
        const p = hoverSec && find(layout(), region, hoverSec.dataset.sapSec);
        if (p) openPicker({ s: p.s + 1, c: 0, b: 0, fresh: true });
      });
      doc.body.append(hover, sel, bar, add);
      return { hover, hoverTag, sel, selTag, bar, label, add };
    }

    const place = (el, r) => {
      el.style.top = `${r.top}px`; el.style.left = `${r.left}px`;
      el.style.width = `${r.width}px`; el.style.height = `${r.height}px`;
    };
    const show = (el, on) => { if (on) el.dataset.show = ""; else delete el.dataset.show; };
    const typeLabel = (el) => types[el.dataset.sapType]?.label || el.dataset.sapType || "";

    // The marked element of the selected section or block.
    const selectedEl = () => (selected && doc ? doc.querySelector(`[data-sap-blk="${CSS.escape(selected)}"], [data-sap-sec="${CSS.escape(selected)}"]`) : null);

    function redraw() {
      if (!ui || !doc?.body.contains(ui.bar)) return;
      const s = selectedEl();
      if (s && regionOf(s) === region) {
        const r = s.getBoundingClientRect();
        place(ui.sel, { top: r.top - 4, left: r.left - 4, width: r.width + 8, height: r.height + 8 });
        ui.selTag.textContent = s.dataset.sapBlk ? typeLabel(s) : fmt(msg.msgSection, (find(layout(), region, selected)?.s ?? 0) + 1);
        show(ui.sel, true);
      } else show(ui.sel, false);
      const reg = hoverSec ? regionOf(hoverSec) : "";
      const p = reg && find(layout(), reg, hoverSec.dataset.sapSec);
      if (p && !typing) {
        const r = hoverSec.getBoundingClientRect();
        const vw = doc.documentElement.clientWidth;
        const secs = getRegion(layout(), reg).sections;
        const n = secs.length;
        ui.label.textContent = fmt(msg.msgSection, p.s + 1);
        ui.bar.querySelector('[data-act="up"]').disabled = p.s === 0;
        ui.bar.querySelector('[data-act="down"]').disabled = p.s === n - 1;
        ui.bar.querySelector('[data-act="duplicate"]').disabled = false;
        ui.bar.querySelector('[data-act="delete"]').disabled = secs[p.s].columns.flat().some((b) => required(b.type, reg));
        show(ui.bar, true);
        const bw = ui.bar.offsetWidth;
        ui.bar.style.top = `${Math.max(4, r.top + 6)}px`;
        ui.bar.style.left = `${Math.min(vw - bw - 6, Math.max(6, r.right - bw - 6))}px`;
        ui.add.style.top = `${r.bottom}px`;
        ui.add.style.left = `${Math.min(vw - 90, Math.max(90, r.left + r.width / 2))}px`;
        show(ui.add, r.bottom > 0 && r.bottom < doc.documentElement.clientHeight + 14);
      } else { show(ui.bar, false); show(ui.add, false); }
    }

    function sectionAction(act, id) {
      const p = find(layout(), region, id);
      if (!p) return;
      const r = getRegion(layout(), region);
      const sec = r.sections[p.s];
      switch (act) {
        // The moved or copied section stays selected, in view.
        case "up": case "down": {
          const to = act === "up" ? p.s - 1 : p.s + 1;
          if (to < 0 || to >= r.sections.length) return;
          selected = id;
          reveal = true;
          commit(moveSection(layout(), region, p.s, to));
          say(fmt(msg.msgMoved, fmt(msg.msgSection, p.s + 1)));
          break;
        }
        case "duplicate": {
          const next = duplicateSection(layout(), region, p.s, types);
          selected = getRegion(next, region).sections[p.s + 1].id;
          reveal = true;
          commit(next);
          break;
        }
        case "delete":
          if (sec.columns.flat().some((b) => required(b.type, region))) { toast("info", msg.msgRequired); return; }
          if (selected === id || sec.columns.flat().some((b) => b.id === selected)) selected = null;
          commit(removeSection(layout(), region, p.s));
          toast("info", msg.msgDeleted);
          break;
        case "block": openPicker({ s: p.s, c: 0, b: sec.columns[0].length }); break;
        case "settings": select(id, { fromPreview: true }); break;
      }
    }

    // Typing over a text: plain for text fields, with its paragraphs,
    // lists and links for Markdown. Every keystroke goes to the draft;
    // Enter (in a one-line text) or a click elsewhere ends it, Escape
    // puts back what it said.
    // markup: for a text inside an HTML block, {dst, doc}: the same
    // element in the block's stored markup, which takes what is typed.
    function typeOver(el, f, id, item, lang, at, markup = null) {
      const rich = f.type === "markdown";
      const before = el.innerHTML;
      const start = layout();
      typing = el;
      redraw();
      el.contentEditable = rich ? "true" : "plaintext-only";
      if (!rich && el.contentEditable !== "plaintext-only") el.contentEditable = "true";
      el.spellcheck = true;
      el.focus({ preventScroll: true });
      // The caret where the click was.
      const sel = doc.getSelection();
      const range = at && doc.caretRangeFromPoint?.(at.x, at.y);
      if (sel && range && el.contains(range.startContainer)) { sel.removeAllRanges(); sel.addRange(range); }
      const read = () => (markup ? (syncText(el, markup.dst, markup.doc), markup.doc.body.innerHTML) : rich ? markdownOf(el) : el.innerText.replace(/ /g, " ").replace(/\s+/g, " ").trim());
      const set = (v, quietly = true) => {
        const at = blockOf(id);
        if (!at) return;
        const def = lang ? "" : f.default || "";
        const next = item == null ? setValue(layout(), region, at.p, f.key, v, lang, def)
          : setItemValue(layout(), region, at.p, item, f.key, v, lang, def, Boolean(f.one));
        commit(next, `${id}.${item ?? ""}.${f.key}.${lang}`, { keepSettings: true, quietly });
        // The panel's field follows.
        const input = settings.querySelector(`#${CSS.escape(fid(f.key, lang, item))}`);
        if (input && "value" in input && input.value !== v) input.value = v;
      };
      const onInput = () => set(read());
      const onKey = (e) => {
        if (e.key === "Escape") {
          e.preventDefault();
          el.innerHTML = before;
          if (JSON.stringify(start) !== JSON.stringify(layout())) commit(start, "", { keepSettings: true, quietly: true });
          el.blur();
        } else if (e.key === "Enter" && !rich) {
          e.preventDefault();
          el.blur();
        } else if (e.key === " " && el.closest("summary, button")) {
          // A space in a <summary> would open or close the question.
          e.preventDefault();
          doc.execCommand("insertText", false, " ");
        }
      };
      const onPaste = (e) => {
        e.preventDefault();
        const t = e.clipboardData?.getData("text/plain") || "";
        doc.execCommand("insertText", false, rich ? t : t.replace(/\s+/g, " "));
      };
      el.addEventListener("input", onInput);
      el.addEventListener("keydown", onKey);
      el.addEventListener("paste", onPaste);
      el.addEventListener("blur", () => {
        el.removeEventListener("input", onInput);
        el.removeEventListener("keydown", onKey);
        el.removeEventListener("paste", onPaste);
        el.removeAttribute("contenteditable");
        typing = null;
        renderSettings();
        redraw();
        // Markdown is shown as the site renders it, and an emptied text as
        // what replaces it (the site's title, the theme's text…).
        if (rich || !el.textContent.trim()) {
          if (unsaved() || timer || inflight) schedule(150); else reloadFrame();
        }
      }, { once: true });
    }

    function chooseImage(id, item, f) {
      if (!mediaOpener) return;
      pickImage = { id, item, f };
      mediaOpener.click();
    }
    mediaOpener?.addEventListener("sap13ns:media-pick", (e) => {
      const it = e.detail;
      const to = pickImage;
      pickImage = null;
      if (!to || !it?.id) return;
      if (!state.images.some((m) => String(m.id) === String(it.id))) state.images.push({ id: it.id, thumb: it.thumb || it.url, name: it.name || "" });
      const at = blockOf(to.id);
      if (!at) return;
      const next = to.item == null ? setValue(layout(), region, at.p, to.f.key, String(it.id), "", "")
        : setItemValue(layout(), region, at.p, to.item, to.f.key, String(it.id), "", "");
      commit(next);
      select(to.id, { fromPreview: true });
    });

    // The banner of a part that is still the theme's fixed template.
    function banner() {
      doc.querySelector(".sap-ed-banner")?.remove();
      if (getRegion(layout(), region)) return;
      const box = doc.createElement("div");
      box.className = "sap-ed-banner";
      box.setAttribute("role", "note");
      const p = doc.createElement("p");
      const strong = doc.createElement("strong");
      strong.textContent = fmt(msg.msgFixedTitle, regionLabel(region));
      p.append(strong, doc.createTextNode(msg.msgFixedHelp));
      const go = doc.createElement("button");
      go.type = "button";
      go.append(iconIn("pencil"), doc.createTextNode(msg.msgMakeEditable));
      go.addEventListener("click", makeEditable);
      box.append(p, go);
      doc.body.append(box);
    }
    // makeEditable turns the region into blocks, the way the panel offers
    // first (primaryStart): never a ready-made page in place of the
    // theme's own.
    function makeEditable() {
      if (getRegion(layout(), region)) return;
      const first = primaryStart();
      first.run();
      if (first.kind !== "convert") toast("success", msg.msgNowEditable);
    }

    function onClick(e) {
      if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
      const t = e.target;
      if (!t.closest || t.closest(".sap-ed-bar, .sap-ed-add, .sap-ed-banner")) return;
      // Inside the text being typed over: place the caret, never follow it.
      if (typing && typing.contains(t)) { e.preventDefault(); return; }
      const blk = t.closest("[data-sap-blk]");
      const sec = t.closest("[data-sap-sec]");
      const where = (blk || sec) && regionOf(blk || sec);
      if (!where) {
        if (!getRegion(layout(), region) && !t.closest("a[href], button, summary, input, select, textarea, label")) {
          const b = doc.querySelector(".sap-ed-banner");
          if (b) { delete b.dataset.nudge; void b.offsetWidth; b.dataset.nudge = ""; }
        }
        return;
      }
      e.preventDefault();
      e.stopImmediatePropagation();
      if (where !== region) switchRegion(where);
      const fEl = t.closest("[data-sap-f]");
      if (blk && fEl && blk.contains(fEl)) {
        const id = blk.dataset.sapBlk;
        const at = blockOf(id);
        const item = fEl.dataset.sapI != null ? Number(fEl.dataset.sapI) : null;
        const f = at && fieldOf(at.b.type, fEl.dataset.sapF, item);
        if (f) {
          select(id, { fromPreview: true });
          if (f.type === "image") { chooseImage(id, item, f); return; }
          const lang = f.i18n ? langOf() : "";
          // A text that shows a fallback (the site's title) starts from it.
          typeOver(fEl, f, id, item, lang, { x: e.clientX, y: e.clientY });
          return;
        }
      }
      // A text inside an HTML block: typed over where it is, and written
      // into the block's markup at the same place.
      const at = blk && blockOf(blk.dataset.sapBlk);
      const root = at?.b.type === "html" ? blk.querySelector(".sap-b-html") : null;
      const host = root && root.contains(t) ? textHost(t, root) : null;
      if (host) {
        const id = blk.dataset.sapBlk;
        const f = fieldOf("html", "html", null);
        const lang = langOf();
        const path = pathTo(root, host);
        const stored = new DOMParser().parseFromString(`<body>${valueOf(at.b, null, "html", lang) || valueOf(at.b, null, "html", "")}`, "text/html");
        const dst = path && follow(stored.body, path, host);
        const squash = (v) => v.replace(/\s+/g, " ").trim();
        select(id, { fromPreview: true });
        if (f && dst && squash(dst.textContent) === squash(host.textContent)) {
          typeOver(host, f, id, null, lang, { x: e.clientX, y: e.clientY }, { dst, doc: stored });
          return;
        }
        toast("info", msg.msgHtmlTextElsewhere);
        return;
      }
      select(blk ? blk.dataset.sapBlk : sec.dataset.sapSec, { fromPreview: true });
    }

    function onOver(e) {
      if (!ui) return;
      const t = e.target;
      if (!t.closest || t.closest(".sap-ed-bar, .sap-ed-add")) return;
      const sec = t.closest("[data-sap-sec]");
      if (sec !== hoverSec) { hoverSec = sec; redraw(); }
      const blk = t.closest("[data-sap-blk]");
      if (blk && !typing) {
        const r = blk.getBoundingClientRect();
        place(ui.hover, { top: r.top - 4, left: r.left - 4, width: r.width + 8, height: r.height + 8 });
        ui.hoverTag.textContent = typeLabel(blk);
        show(ui.hover, blk.dataset.sapBlk !== selected);
      } else show(ui.hover, false);
    }

    // switchRegion edits another part of the page, clicked in the preview
    // (the header while editing the front page): the preview stays.
    function switchRegion(name) {
      region = name;
      try { localStorage.setItem("sap-builder-region", region); } catch (_) {}
      selected = null;
      render();
      banner();
    }

    function attach(d) {
      doc = d;
      typing = null;
      hoverSec = null;
      ui = null;
      if (!doc.body || doc.documentElement.dataset.sapEd) return;
      doc.documentElement.dataset.sapEd = "";
      const css = doc.createElement("link");
      css.rel = "stylesheet";
      css.href = state.edit_css || "/static/page-edit.css";
      doc.head.append(css);
      doc.documentElement.classList.add("sap-ed-on");
      // What each marked element is, for the stylesheet: images take a
      // pointer, texts a caret.
      for (const el of doc.querySelectorAll("[data-sap-f]")) {
        const blk = el.closest("[data-sap-blk]");
        const t = blk && types[blk.dataset.sapType];
        const fs = el.dataset.sapI != null ? t?.items?.fields : t?.fields;
        const f = fs?.find((x) => x.key === el.dataset.sapF);
        if (f) el.dataset.sapKind = f.type === "image" ? "image" : "text";
        else el.removeAttribute("data-sap-f");
      }
      ui = build();
      doc.addEventListener("click", onClick, true);
      doc.addEventListener("mouseover", onOver);
      doc.documentElement.addEventListener("mouseleave", () => { hoverSec = null; show(ui.hover, false); redraw(); });
      const win = doc.defaultView;
      let raf = 0;
      const later = () => { if (!raf) raf = win.requestAnimationFrame(() => { raf = 0; redraw(); }); };
      win.addEventListener("scroll", later, { passive: true });
      win.addEventListener("resize", later);
      // Images and fonts still loading move things.
      win.addEventListener("load", later);
      banner();
      redraw();
      if (reveal) {
        reveal = false;
        // After the preview has restored its scroll position.
        win.setTimeout(() => mark(true), 60);
      }
    }

    // mark draws the selection after a change in the panel; scroll brings
    // it into view.
    function mark(scroll) {
      if (!doc || !ui) return;
      // Not on the page yet (just added): once the preview reloads.
      if (scroll && !selectedEl()) reveal = true;
      if (scroll) {
        const el = selectedEl();
        const r = el?.getBoundingClientRect();
        if (r && (r.top < 0 || r.bottom > doc.documentElement.clientHeight)) el.scrollIntoView({ block: "center", behavior: reduced() ? "auto" : "smooth" });
      }
      redraw();
    }

    return { attach, mark, banner: () => doc && banner() };
  })();

  // ------------------------------------------------------------ preview --
  let scheme =document.documentElement.classList.contains("dark") ? "dark" : "light";
  let reloadScroll = null;
  const frameDoc = () => { try { return frame.contentDocument; } catch (_) { return null; } };
  const withScheme = (u) => { const x = new URL(u, location.origin); x.searchParams.set("scheme", scheme); return x.pathname + x.search; };
  const PRIVATE = /^\/(admin|login|logout|register|setup)(\/|$)/;
  function previewURL(href) {
    let u;
    try { u = new URL(href, location.origin); } catch (_) { return null; }
    if (u.origin !== location.origin || PRIVATE.test(u.pathname) || u.pathname.startsWith("/uploads/")) return null;
    if (/\.[a-z0-9]{1,8}$/i.test(u.pathname) && !/\.html?$/i.test(u.pathname)) return null;
    u.searchParams.set("preview_theme", slug);
    u.searchParams.set("customize", "draft");
    u.searchParams.set("scheme", scheme);
    return u.pathname + u.search + u.hash;
  }
  function loadPreview() {
    const info = regions.find((x) => x.name === region);
    const url = info?.preview;
    if (!url) { toast("info", msg.msgNoPreview); return; }
    const want = withScheme(url);
    let cur = "";
    try { cur = frame.contentWindow.location.pathname + frame.contentWindow.location.search; } catch (_) {}
    // Header and footer show on every page: stay where the preview is.
    if ((region === "header" || region === "footer") && cur && cur !== "blank") return;
    if (cur !== want) frame.src = want;
  }
  function reloadFrame() {
    const win = frame.contentWindow;
    try { reloadScroll = win.scrollY; } catch (_) { reloadScroll = 0; }
    $(".cz-frame").dataset.loading = "";
    try { win.location.reload(); } catch (_) { frame.src = frame.src; }
  }
  function applyScheme() {
    frameDoc()?.documentElement.classList.toggle("dark", scheme === "dark");
    for (const b of $$("[data-scheme]")) b.setAttribute("aria-pressed", String(b.dataset.scheme === scheme));
    const open = $("[data-open-preview]");
    let href = null;
    try { href = previewURL(frame.contentWindow.location.href); } catch (_) {}
    if (open && href) open.href = href;
  }
  frame.addEventListener("load", () => {
    delete $(".cz-frame").dataset.loading;
    const doc = frameDoc();
    if (!doc) return;
    applyScheme();
    visual.attach(doc);
    if (reloadScroll != null) { frame.contentWindow.scrollTo({ top: reloadScroll, behavior: "instant" }); reloadScroll = null; }
    doc.addEventListener("click", (e) => {
      if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
      const a = e.target.closest?.("a[href]");
      if (!a || a.getAttribute("href").startsWith("#")) return;
      e.preventDefault();
      const url = previewURL(a.href);
      if (url) { frame.contentWindow.location.assign(url); return; }
      const target = new URL(a.href, location.href);
      if (target.origin === location.origin && PRIVATE.test(target.pathname)) return;
      window.open(a.href, "_blank", "noopener");
      if (target.origin !== location.origin) toast("info", msg.msgExternal);
    });
    doc.addEventListener("submit", (e) => {
      const f = e.target;
      if ((f.method || "get").toLowerCase() !== "get") { e.preventDefault(); toast("info", msg.msgBlocked); return; }
      for (const [k, v] of [["preview_theme", slug], ["customize", "draft"], ["scheme", scheme]]) {
        let input = f.querySelector(`input[type=hidden][name="${k}"]`);
        if (!input) { input = doc.createElement("input"); input.type = "hidden"; input.name = k; f.append(input); }
        input.value = v;
      }
    });
  });
  for (const b of $$("[data-device]")) b.addEventListener("click", () => {
    $(".cz-frame").dataset.device = b.dataset.device;
    for (const x of $$("[data-device]")) x.setAttribute("aria-pressed", String(x === b));
  });
  for (const b of $$("[data-scheme]")) b.addEventListener("click", () => { scheme = b.dataset.scheme; applyScheme(); });
  $("[data-reload]").addEventListener("click", reloadFrame);

  // ------------------------------------------------------------- saving --
  let saved = JSON.stringify(layout());
  let timer = 0;
  let inflight = null;
  let again = false;
  const unsaved = () => JSON.stringify(layout()) !== saved;

  // needReload: a change the preview does not show yet is waiting to be
  // saved (not one typed over the preview itself).
  let needReload = false;
  function schedule(delay = 700, quietly = false) {
    if (!quietly) needReload = true;
    clearTimeout(timer);
    timer = setTimeout(() => { timer = 0; save(); }, delay);
  }

  async function save() {
    clearTimeout(timer); timer = 0;
    if (inflight) { again = true; return inflight; }
    if (!unsaved()) {
      // Undone back to what is saved: the preview still shows the change.
      if (needReload) { needReload = false; reloadFrame(); }
      return;
    }
    const reload = needReload;
    needReload = false;
    const sent = JSON.stringify(layout());
    const body = `{"layout":${sent}}`;
    setStatus(msg.msgSaving, "saving");
    inflight = (async () => {
      try {
        const res = await fetch(base + "/draft", {
          method: "POST", credentials: "same-origin",
          headers: { "Content-Type": "application/json", Accept: "application/json", "X-CSRF-Token": csrf() }, body,
        });
        const data = await res.json().catch(() => ({}));
        if (!res.ok || !data.ok) {
          needReload = needReload || reload;
          errors = (data.errors || []).map((e) => ({ ...parseErrorPath(e.field), message: e.message, field: e.field })).filter((e) => e.region);
          setStatus(fmt(msg.msgFailed, data.errors?.length ? msg.msgInvalid : data.message || res.status), "error");
          renderIntro();
          renderStructure();
          if (!document.activeElement?.closest?.("#bl-settings")) renderSettings();
          else markFieldErrors();
          return;
        }
        const hadErrors = errors.length > 0;
        errors = [];
        saved = sent;
        // What the server keeps (an HTML block's markup, sanitised) is
        // what the preview shows: the editor goes on from it, unless it
        // changed meanwhile.
        if (data.layout && JSON.stringify(layout()) === sent) {
          const kept = JSON.stringify(data.layout);
          if (kept !== sent) { history.stack[history.at] = data.layout; saved = kept; }
        }
        setStatus(fmt(msg.msgSaved, data.saved_at || ""), "");
        if (publishBtn) publishBtn.disabled = false;
        if (discardBtn) discardBtn.disabled = false;
        if (hadErrors) { renderIntro(); renderStructure(); markFieldErrors(); }
        if (reload) reloadFrame();
      } catch (_) {
        needReload = needReload || reload;
        setStatus(msg.msgOffline, "error");
      } finally {
        inflight = null;
        if (again) { again = false; save(); }
      }
    })();
    return inflight;
  }

  // markFieldErrors updates the error notes of the open settings without
  // rebuilding them (the user may be typing there).
  function markFieldErrors() {
    const pos = find(layout(), region, selected);
    for (const el of $$("[aria-invalid]", settings)) el.removeAttribute("aria-invalid");
    for (const p of $$(".cz-field-error", settings)) p.remove();
    if (!pos || pos.kind !== "block") return;
    for (const e of errors.filter((x) => sameBlock(x, pos) && x.key)) {
      const el = settings.querySelector(`#${CSS.escape(fid(e.key, e.lang, e.item))}`);
      if (!el) continue;
      el.setAttribute("aria-invalid", "true");
      const t = types[getRegion(layout(), region).sections[pos.s].columns[pos.c][pos.b].type];
      const fields = e.item != null && e.item >= 0 ? t?.items?.fields : t?.fields;
      el.closest(".field")?.append(h("p", { class: "cz-field-error", text: errText(fields?.find((f) => f.key === e.key), e) }));
    }
  }

  // ---------------------------------------------------------- custom CSS --
  // The theme's custom CSS, saved into the design's draft (POST …/css):
  // the preview links the draft stylesheet, Publish puts it live.
  const cssBox = document.getElementById("bl-css-input");
  const cssStatus = document.getElementById("bl-css-status");
  const cssBadge = document.getElementById("bl-css-badge");
  let cssSaved = state.css || "";
  let cssTimer = 0;
  let cssInflight = null;
  let cssAgain = false;
  let cssError = false;
  const cssUnsaved = () => Boolean(cssBox) && cssBox.value !== cssSaved;
  const setCSSStatus = (text, kind = "") => { cssStatus.textContent = text; cssStatus.className = kind === "error" ? "cz-field-error" : "cz-status"; };
  if (cssBox) {
    cssBox.value = cssSaved;
    if (cssSaved) cssBox.closest("details").open = true;
    const cssEditor = codeEditor(cssBox, "css");
    // An editor in a closed <details> measures itself once it shows.
    cssBox.closest("details")?.addEventListener("toggle", () => cssEditor?.view.requestMeasure());
    if (cssBadge) cssBadge.hidden = !state.css_pending;
    cssBox.addEventListener("input", () => { clearTimeout(cssTimer); cssTimer = setTimeout(() => { cssTimer = 0; saveCSS(); }, 900); });
  }
  async function saveCSS() {
    clearTimeout(cssTimer); cssTimer = 0;
    if (cssInflight) { cssAgain = true; return cssInflight; }
    if (!cssUnsaved()) return;
    const sent = cssBox.value;
    setCSSStatus(msg.msgSaving);
    cssInflight = (async () => {
      try {
        const res = await fetch(base + "/css", {
          method: "POST", credentials: "same-origin",
          headers: { "Content-Type": "application/json", Accept: "application/json", "X-CSRF-Token": csrf() }, body: JSON.stringify({ css: sent }),
        });
        const data = await res.json().catch(() => ({}));
        if (!res.ok || !data.ok) {
          cssError = true;
          cssBox.setAttribute("aria-invalid", "true");
          setCSSStatus(fmt(msg.msgCssRefused, data.errors?.[0]?.message || data.message || res.status), "error");
          return;
        }
        cssError = false;
        cssBox.removeAttribute("aria-invalid");
        cssSaved = sent;
        setCSSStatus(fmt(msg.msgSaved, data.saved_at || ""));
        if (cssBadge) cssBadge.hidden = !data.pending;
        if (data.pending) {
          if (publishBtn) publishBtn.disabled = false;
          if (discardBtn) discardBtn.disabled = false;
          setStatus(msg.msgUnpublished, "");
        }
        reloadFrame();
      } catch (_) {
        setCSSStatus(msg.msgOffline, "error");
      } finally {
        cssInflight = null;
        if (cssAgain) { cssAgain = false; saveCSS(); }
      }
    })();
    return cssInflight;
  }

  // Publish exactly what the preview shows: finish pending saves first.
  const publishForm = document.querySelector("form[data-bl-publish]");
  publishForm?.addEventListener("submit", async (e) => {
    if (!timer && !inflight && !unsaved() && !cssTimer && !cssInflight && !cssUnsaved()) return;
    e.preventDefault();
    setStatus(msg.msgPending, "saving");
    await save();
    if (inflight) await inflight;
    await saveCSS();
    if (cssInflight) await cssInflight;
    if (cssError) { cssBox.focus(); setStatus(msg.msgCssInvalid, "error"); return; }
    if (!unsaved() && !errors.length && !cssUnsaved()) publishForm.submit();
  });
  window.addEventListener("beforeunload", (e) => {
    if (inflight || unsaved() || cssInflight || cssUnsaved()) { e.preventDefault(); e.returnValue = ""; }
  });

  render();
  frame.src = withScheme(regions.find((x) => x.name === region)?.preview || regions[0]?.preview || "/");
  applyScheme();
  if (recommended) intro.querySelector(`[data-preset="${CSS.escape(recommended)}"]`)?.focus();
}

if (typeof document !== "undefined") {
  const root = document.querySelector("[data-builder]");
  if (root) init(root);
}
