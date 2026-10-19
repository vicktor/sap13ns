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
    if (value === "" || value == null) delete t[key]; else t[key] = String(value);
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

/** summary is a short description of a block for the structure list. */
export function summary(block, t, menus = [], areas = []) {
  const s = block.settings || {};
  const pick = (k) => (s[k] || "").replace(/\[([^\]]*)\]\([^)]*\)/g, "$1").replace(/[#*_`>[\]()]/g, "").replace(/\s+/g, " ").trim();
  let out = "";
  if (t?.fields?.some((f) => f.type === "menu")) out = menus.find((m) => m.id === (s.menu || "primary"))?.label || s.menu || "";
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
  const m = /^(header|footer|pages\.([a-z0-9]+))(?:\.sections\[(\d+)\](?:(\.anchor)|\.columns(?:\[(\d+)\](?:\[(\d+)\](?:(\.items)(?:\[(\d+)\])?)?(?:\.(settings|i18n)\.([a-z0-9_-]+)(?:\.([a-z0-9_]+))?)?)?)?)?)?/i.exec(path || "");
  if (!m) return null;
  const n = (v) => (v == null ? null : Number(v));
  const out = { region: m[2] || m[1], s: n(m[3]), c: n(m[5]), b: n(m[6]), item: m[7] ? (m[8] == null ? -1 : Number(m[8])) : null, key: "", lang: "" };
  if (m[4]) out.anchor = true;
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
  let region = "home";
  try { const r = localStorage.getItem("sap-builder-region"); if (regions.some((x) => x.name === r)) region = r; } catch (_) {}
  const asked = new URLSearchParams(location.search).get("region");
  if (regions.some((x) => x.name === asked)) region = asked;
  let selected = null;            // id of the selected section or block
  let errors = [];                // the last refusal's field errors
  const history = new History(clone(state.layout || {}));
  const layout = () => history.current;

  // ------------------------------------------------------------ status --
  const setStatus = (text, kind = "") => { status.textContent = text; status.dataset.kind = kind; };

  // ------------------------------------------------------------ change --
  // commit records a new state, re-renders and schedules a save.
  function commit(next, key = "", { keepSettings = false } = {}) {
    if (JSON.stringify(next) === JSON.stringify(layout())) return;
    history.push(next, key);
    render({ keepSettings });
    schedule();
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

  function renderIntro() {
    renderRegionSelect();
    const r = getRegion(layout(), region);
    const kids = [];
    const icn = region === "header" ? "panel-top" : region === "footer" ? "panel-bottom" : "layout-template";
    kids.push(h("p", { class: "bl-intro-text" }, icon(icn), h("span", { text: r ? msg.msgBlockRegion : msg.msgThemeRegion })));
    if (!r) {
      kids.push(h("button", { type: "button", class: "btn", "data-size": "sm", onclick: () => {
        const start = starter(region, { title404: msg.msgNfTitle, text404: msg.msgNfText, button404: msg.msgNfButton }, layout());
        selected = start.sections[0]?.id || null;
        commit(setRegion(layout(), region, start));
      } }, icon("plus"), msg.msgBuild));
      // The front page can start from a whole ready-made page.
      const presets = region === "front" ? state.front_presets || [] : [];
      if (presets.length) {
        kids.push(h("p", { class: "bl-intro-or", text: msg.msgPresetsIntro }),
          h("ul", { class: "bl-front-presets" }, presets.map((p) => h("li", {}, h("button", { type: "button", class: "bl-front-preset", onclick: () => {
            const start = reid(p.region, layout());
            selected = null;
            commit(setRegion(layout(), region, start));
            say(fmt(msg.msgPresetApplied, p.label));
          } }, h("strong", { text: p.label }), h("span", { text: p.help }))))));
      }
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
    : f?.type === "url" ? msg.msgBadUrl : f?.type === "email" ? msg.msgBadEmail
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
          h("button", { type: "button", class: "bl-add", onclick: () => openPicker({ s, c, b: blocks.length }) }, icon("plus"), msg.msgAddBlock)))));
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

  function select(id) {
    selected = selected === id ? selected : id;
    renderStructure();
    renderSettings();
    settings.scrollIntoView({ block: "nearest", behavior: matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth" });
    settings.querySelector(FOCUSABLE)?.focus({ preventScroll: true });
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
    const errs = errors.filter((e) => e.region === region && e.s === pos.s && e.c == null && !e.anchor).map((e) => h("p", { class: "cz-field-error", role: "alert", text: e.message }));
    settings.replaceChildren(header("columns-2", `${label} · ${msg.msgSectionSettings}`),
      h("section", { class: "bl-form" }, ...errs, presets, anchorField(sec, pos), ...fields, moveButtons(up, down, pos.s > 0, pos.s < r.sections.length - 1)));
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
    const set = (v) => {
      const p = find(layout(), region, b.id);
      const def = lang ? "" : f.default || "";
      const next = item == null ? setValue(layout(), region, p, f.key, v, lang, def)
        : setItemValue(layout(), region, p, item, f.key, v, lang, def, Boolean(f.one));
      // Turning on a one-only setting turns it off elsewhere: redraw.
      commit(next, `${b.id}.${item ?? ""}.${f.key}.${lang}`, { keepSettings: !(f.one && v === "true") });
    };
    const err = errors.find((e) => sameBlock(e, pos) && (e.item ?? null) === item && e.key === f.key && e.lang === lang);
    const help = [];
    if (f.help && !lang) help.push(f.help);
    if (f.type === "url" && !lang) help.push(msg.msgUrlHint);
    const describedBy = help.length || err ? `${id}-help` : null;
    const common = { id, "aria-describedby": describedBy, "aria-invalid": err ? "true" : null };
    let input;
    const main = lang ? obj.settings?.[f.key] || "" : "";
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
      case "textarea": case "markdown":
        input = h("textarea", { class: "textarea w-full", rows: f.type === "markdown" ? 7 : 3, maxlength: f.max || null, placeholder: main || null, ...common, oninput: (e) => set(e.target.value) });
        input.value = stored;
        break;
      default:
        input = h("input", {
          class: "input w-full", type: f.type === "email" ? "email" : f.type === "int" ? "number" : "text", inputmode: f.type === "url" ? "url" : null,
          maxlength: f.max || (f.type === "url" ? 2000 : null), min: f.type === "int" ? f.min : null, max: f.type === "int" ? f.max : null,
          placeholder: main || f.default || null, autocomplete: "off", spellcheck: f.type === "url" || f.type === "email" ? "false" : null,
          ...common, oninput: (e) => set(e.target.value),
        });
        input.value = stored;
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
  function openPicker(at) {
    pickAt = at;
    const r = getRegion(layout(), region);
    const list = document.getElementById("bl-types");
    const where = regions.find((x) => x.name === region)?.label || region;
    document.getElementById("bl-picker-where").textContent = `${where} · ${fmt(msg.msgSection, at.s + 1)}${(PRESETS[r.sections[at.s].layout] || 1) > 1 ? " · " + fmt(msg.msgColumn, at.c + 1) : ""}`;
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
    const b = newBlock(layout(), t, state.starters || {}, settings0);
    commit(insertBlock(layout(), region, pickAt, b));
    select(b.id);
  }

  // ------------------------------------------------------- drag and drop --
  let drag = null;
  const line = h("div", { class: "bl-drop-line", "aria-hidden": "true" });

  structure.addEventListener("pointerdown", (e) => {
    const grip = e.target.closest(".bl-grip");
    if (!grip || e.button !== 0) return;
    const kind = grip.dataset.grip;
    const from = kind === "section" ? { s: +grip.dataset.s } : { s: +grip.dataset.s, c: +grip.dataset.c, b: +grip.dataset.b };
    drag = { kind, from, grip, x: e.clientX, y: e.clientY, active: false, target: null, item: grip.closest("li") };
    grip.setPointerCapture(e.pointerId);
  });
  structure.addEventListener("pointermove", (e) => {
    if (!drag) return;
    if (!drag.active) {
      if (Math.hypot(e.clientX - drag.x, e.clientY - drag.y) < 5) return;
      drag.active = true;
      drag.item.classList.add("is-dragging");
      root.classList.add("is-dragging");
      structure.append(line);
    }
    e.preventDefault();
    drag.target = dropTarget(e.clientX, e.clientY);
    showLine(drag.target);
    autoscroll(e.clientY);
  });
  const endDrag = (e, cancel) => {
    if (!drag) return;
    const d = drag;
    drag = null;
    try { d.grip.releasePointerCapture(e.pointerId); } catch (_) {}
    line.remove();
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

  function showLine(t) {
    if (!t) { line.hidden = true; return; }
    line.hidden = false;
    const box = structure.getBoundingClientRect();
    let rc, top;
    if (drag.kind === "section") {
      const items = $$(":scope > .bl-sections > .bl-section", structure);
      const list = $(".bl-sections", structure).getBoundingClientRect();
      rc = list;
      top = t.s < items.length ? items[t.s].getBoundingClientRect().top - 4 : items.at(-1).getBoundingClientRect().bottom + 2;
    } else {
      const ul = $(`.bl-blocks[data-s="${t.s}"][data-c="${t.c}"]`, structure);
      const items = [...ul.querySelectorAll(":scope > .bl-block")];
      rc = ul.getBoundingClientRect();
      top = t.b < items.length ? items[t.b].getBoundingClientRect().top - 2 : (items.at(-1)?.getBoundingClientRect().bottom ?? rc.top) + 1;
    }
    // CSSOM (allowed by the CSP): the line follows the pointer.
    line.style.top = `${top - box.top}px`;
    line.style.left = `${rc.left - box.left}px`;
    line.style.width = `${rc.width}px`;
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

  // ------------------------------------------------------------ preview --
  let scheme = document.documentElement.classList.contains("dark") ? "dark" : "light";
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

  function schedule() {
    clearTimeout(timer);
    timer = setTimeout(() => { timer = 0; save(); }, 700);
  }

  async function save() {
    clearTimeout(timer); timer = 0;
    if (inflight) { again = true; return inflight; }
    if (!unsaved()) return;
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
        setStatus(fmt(msg.msgSaved, data.saved_at || ""), "");
        if (publishBtn) publishBtn.disabled = false;
        if (discardBtn) discardBtn.disabled = false;
        if (hadErrors) { renderIntro(); renderStructure(); markFieldErrors(); }
        reloadFrame();
      } catch (_) {
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

  // Publish exactly what the preview shows: finish a pending save first.
  const publishForm = document.querySelector("form[data-bl-publish]");
  publishForm?.addEventListener("submit", async (e) => {
    if (!timer && !inflight && !unsaved()) return;
    e.preventDefault();
    setStatus(msg.msgPending, "saving");
    await save();
    if (inflight) await inflight;
    if (!unsaved() && !errors.length) publishForm.submit();
  });
  window.addEventListener("beforeunload", (e) => {
    if (inflight || unsaved()) { e.preventDefault(); e.returnValue = ""; }
  });

  render();
  frame.src = withScheme(regions.find((x) => x.name === region)?.preview || regions[0]?.preview || "/");
  applyScheme();
}

if (typeof document !== "undefined") {
  const root = document.querySelector("[data-builder]");
  if (root) init(root);
}
