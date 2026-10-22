// Tests for the page builder's pure operations (ui/static/builder.js).
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  getRegion, setRegion, ids, newID, addSection, removeSection, moveSection, duplicateSection, fitColumns, setPreset,
  setStyle, insertBlock, removeBlock, moveBlock, dropIndex, duplicateBlock, setValue, find, step, count, allowed,
  required, starter, summary, parseErrorPath, History, PAGE_REGIONS, validAnchor, anchorsTaken, setAnchor,
  addItem, removeItem, moveItem, setItemValue, newBlock, reid, chooseOption,
  validClasses, setSectionClass, hasBlock, emptyRegion, sectionToHTML, sectionHTML,
} from "../../static/builder.js";

// A seeded random source, so ids are reproducible.
const seeded = (n = 1) => () => { n = (n * 16807) % 2147483647; return n / 2147483647; };

const types = {
  hero: { type: "hero", where: ["*"], fields: [] },
  "post-list": { type: "post-list", where: ["home", "archive", "search"], once: true, fields: [] },
  "footer-meta": { type: "footer-meta", where: ["footer"], once: true, fields: [] },
  menu: { type: "menu", where: ["*"], fields: [{ key: "menu", type: "menu" }] },
};

const base = () => ({
  version: 1,
  pages: {
    home: {
      sections: [
        { id: "s1", layout: "1-1", columns: [[{ id: "a", type: "hero" }, { id: "b", type: "post-list" }], [{ id: "c", type: "menu" }]] },
        { id: "s2", layout: "1", columns: [[{ id: "d", type: "hero" }]] },
      ],
    },
  },
});

test("regions: get, set and remove without touching the input", () => {
  const l = base();
  assert.equal(getRegion(l, "home").sections.length, 2);
  assert.equal(getRegion(l, "header"), null);
  const withHeader = setRegion(l, "header", { sections: [] });
  assert.deepEqual(withHeader.header, { sections: [] });
  assert.equal(l.header, undefined, "input changed");
  const none = setRegion(l, "home", null);
  assert.equal(none.pages, undefined, "an empty pages map is dropped");
  assert.ok(l.pages.home);
});

test("ids are unique across the layouts", () => {
  const l = base();
  assert.deepEqual([...ids(l)].sort(), ["a", "b", "c", "d", "s1", "s2"]);
  const taken = new Set();
  const rand = seeded();
  const made = Array.from({ length: 50 }, () => newID(l, "Rich Text!", taken, rand));
  assert.equal(new Set(made).size, 50);
  for (const id of made) assert.match(id, /^[a-z0-9][a-z0-9-]{0,31}$/);
});

test("sections: add, move, duplicate, remove", () => {
  let l = addSection(base(), "home", 1, "1-1-1", seeded());
  let secs = getRegion(l, "home").sections;
  assert.equal(secs.length, 3);
  assert.equal(secs[1].layout, "1-1-1");
  assert.deepEqual(secs[1].columns, [[], [], []]);
  // A section to a region that does not exist yet creates it.
  const footer = addSection({}, "footer", 0, "2-1", seeded());
  assert.equal(getRegion(footer, "footer").sections[0].columns.length, 2);

  l = moveSection(base(), "home", 0, 1);
  assert.deepEqual(getRegion(l, "home").sections.map((s) => s.id), ["s2", "s1"]);

  l = duplicateSection(base(), "home", 0, types, seeded());
  secs = getRegion(l, "home").sections;
  assert.equal(secs.length, 3);
  assert.notEqual(secs[1].id, "s1");
  assert.deepEqual(secs[1].columns.map((c) => c.map((b) => b.type)), [["hero"], ["menu"]], "once-only blocks are not copied");
  assert.equal(new Set(ids(l)).size, ids(l).size);
  for (const b of secs[1].columns.flat()) assert.ok(!["a", "b", "c"].includes(b.id));

  l = removeSection(base(), "home", 0);
  assert.deepEqual(getRegion(l, "home").sections.map((s) => s.id), ["s2"]);
});

test("presets keep every block", () => {
  assert.deepEqual(fitColumns([[1], [2], [3]], 2), [[1], [2, 3]]);
  assert.deepEqual(fitColumns([[1]], 3), [[1], [], []]);
  assert.deepEqual(fitColumns(undefined, 1), [[]]);
  let l = setPreset(base(), "home", 0, "1");
  assert.deepEqual(getRegion(l, "home").sections[0].columns.map((c) => c.map((b) => b.id)), [["a", "b", "c"]]);
  l = setPreset(l, "home", 0, "1-1-1");
  assert.equal(getRegion(l, "home").sections[0].columns.length, 3);
  const b0 = base();
  assert.equal(setPreset(b0, "home", 0, "4-1"), b0, "unknown presets are ignored");
});

test("styles drop their defaults", () => {
  let l = setStyle(base(), "home", 0, "background", "muted", "none");
  assert.deepEqual(getRegion(l, "home").sections[0].style, { background: "muted" });
  l = setStyle(l, "home", 0, "background", "none", "none");
  assert.equal(getRegion(l, "home").sections[0].style, undefined);
});

test("blocks: insert, move, duplicate, remove", () => {
  let l = insertBlock(base(), "home", { s: 1, c: 0, b: 0 }, { id: "n", type: "menu" });
  assert.deepEqual(getRegion(l, "home").sections[1].columns[0].map((b) => b.id), ["n", "d"]);

  // Down within a column: the index counts without the moved block.
  l = moveBlock(base(), "home", { s: 0, c: 0, b: 0 }, { s: 0, c: 0, b: 1 });
  assert.deepEqual(getRegion(l, "home").sections[0].columns[0].map((b) => b.id), ["b", "a"]);
  // Across sections.
  l = moveBlock(base(), "home", { s: 0, c: 1, b: 0 }, { s: 1, c: 0, b: 1 });
  assert.deepEqual(getRegion(l, "home").sections[0].columns[1], []);
  assert.deepEqual(getRegion(l, "home").sections[1].columns[0].map((b) => b.id), ["d", "c"]);

  // A drop below the next block, as the pointer sees it.
  assert.deepEqual(dropIndex({ s: 0, c: 0, b: 0 }, { s: 0, c: 0, b: 2 }), { s: 0, c: 0, b: 1 });
  assert.deepEqual(dropIndex({ s: 0, c: 0, b: 1 }, { s: 0, c: 0, b: 0 }), { s: 0, c: 0, b: 0 });
  assert.deepEqual(dropIndex({ s: 0, c: 0, b: 0 }, { s: 0, c: 1, b: 1 }), { s: 0, c: 1, b: 1 });

  l = duplicateBlock(base(), "home", { s: 0, c: 1, b: 0 }, seeded());
  const col = getRegion(l, "home").sections[0].columns[1];
  assert.equal(col.length, 2);
  assert.equal(col[1].type, "menu");
  assert.notEqual(col[1].id, "c");

  l = removeBlock(base(), "home", { s: 0, c: 0, b: 1 });
  assert.deepEqual(getRegion(l, "home").sections[0].columns[0].map((b) => b.id), ["a"]);
});

test("values: settings, translations, defaults and empties", () => {
  const p = { s: 0, c: 0, b: 0 };
  let l = setValue(base(), "home", p, "title", "Hola");
  assert.deepEqual(getRegion(l, "home").sections[0].columns[0][0].settings, { title: "Hola" });
  l = setValue(l, "home", p, "title", "Hello", "en");
  assert.deepEqual(getRegion(l, "home").sections[0].columns[0][0].i18n, { en: { title: "Hello" } });
  l = setValue(l, "home", p, "title", "", "en");
  assert.equal(getRegion(l, "home").sections[0].columns[0][0].i18n, undefined);
  l = setValue(l, "home", p, "align", "center", "", "center");
  l = setValue(l, "home", p, "title", "");
  assert.equal(getRegion(l, "home").sections[0].columns[0][0].settings, undefined);
});

test("find and keyboard steps", () => {
  const l = base();
  assert.deepEqual(find(l, "home", "s2"), { kind: "section", s: 1 });
  assert.deepEqual(find(l, "home", "c"), { kind: "block", s: 0, c: 1, b: 0 });
  assert.equal(find(l, "home", "zz"), null);
  assert.equal(find(l, "footer", "a"), null);

  assert.deepEqual(step(l, "home", { s: 0, c: 0, b: 1 }, "up"), { s: 0, c: 0, b: 0 });
  assert.equal(step(l, "home", { s: 0, c: 0, b: 0 }, "up"), null);
  assert.deepEqual(step(l, "home", { s: 0, c: 0, b: 0 }, "down"), { s: 0, c: 0, b: 1 });
  assert.deepEqual(step(l, "home", { s: 0, c: 0, b: 1 }, "down"), { s: 0, c: 1, b: 0 }, "into the next column");
  assert.deepEqual(step(l, "home", { s: 0, c: 1, b: 0 }, "down"), { s: 1, c: 0, b: 0 }, "into the next section");
  assert.deepEqual(step(l, "home", { s: 1, c: 0, b: 0 }, "up"), { s: 0, c: 1, b: 1 }, "to the end of the previous section");
  assert.equal(step(l, "home", { s: 1, c: 0, b: 0 }, "down"), null);
  assert.deepEqual(step(l, "home", { s: 0, c: 0, b: 1 }, "right"), { s: 0, c: 1, b: 1 });
  assert.equal(step(l, "home", { s: 0, c: 0, b: 0 }, "left"), null);
});

test("what may be added and removed", () => {
  const home = getRegion(base(), "home");
  assert.equal(count(home, "hero"), 2);
  assert.ok(allowed(types.hero, "home", home));
  assert.ok(!allowed(types["post-list"], "home", home), "once per region");
  assert.ok(allowed(types["post-list"], "archive", null));
  assert.ok(!allowed(types["footer-meta"], "home", home));
  assert.ok(required("footer-meta", "footer"));
  assert.ok(required("single-content", "single") && required("single-content", "page"));
  assert.ok(!required("footer-meta", "home") && !required("hero", "footer"));
});

test("starters are valid for their regions", () => {
  const all = { ...types,
    "site-header": { type: "site-header", where: ["header"], once: true }, "social-links": { type: "social-links", where: ["*"] },
    breadcrumbs: { type: "breadcrumbs", where: ["single", "page", "archive", "search", "404"], once: true },
    "featured-image": { type: "featured-image", where: ["single", "page"], once: true }, "single-content": { type: "single-content", where: ["single", "page"], once: true },
    "post-meta": { type: "post-meta", where: ["single", "page"] }, pagination: { type: "pagination", where: ["home", "archive", "search", "single"], once: true },
    comments: { type: "comments", where: ["single", "page"], once: true }, search: { type: "search", where: ["*"] },
  };
  for (const name of ["header", "footer", "front", "home", "single", "page", "archive", "search", "404"]) {
    const r = starter(name, { title404: "No está" }, {}, seeded());
    assert.ok(r.sections.length > 0, name);
    const seen = {};
    for (const s of r.sections) {
      assert.equal(s.columns.length, { 1: 1, "1-1": 2, "2-1": 2, "1-2": 2, "1-1-1": 3 }[s.layout], `${name}: columns`);
      for (const b of s.columns.flat()) {
        const t = all[b.type];
        assert.ok(t, `${name}: ${b.type}`);
        assert.ok(t.where.includes("*") || t.where.includes(name), `${name}: ${b.type} not allowed`);
        seen[b.type] = (seen[b.type] || 0) + 1;
        if (t.once) assert.equal(seen[b.type], 1, `${name}: ${b.type} twice`);
      }
    }
    if (name === "footer") assert.equal(seen["footer-meta"], 1);
    if (name === "single" || name === "page") assert.equal(seen["single-content"], 1);
    const l = setRegion({}, name, r);
    assert.equal(ids(l).size, r.sections.length + r.sections.flatMap((s) => s.columns.flat()).length, `${name}: unique ids`);
  }
  assert.equal(starter("404", { title404: "No está" }).sections[0].columns[0][0].settings.title, "No está");
});

test("summary", () => {
  const menus = [{ id: "primary", label: "Primary menu" }, { id: "footer", label: "Footer menu" }];
  assert.equal(summary({ type: "menu", settings: { menu: "footer" } }, types.menu, menus), "Footer menu");
  assert.equal(summary({ type: "menu" }, types.menu, menus), "Primary menu");
  assert.equal(summary({ type: "rich-text", settings: { text: "## **Hello** [world](https://x)" } }, { fields: [] }), "Hello world");
  assert.ok(summary({ type: "hero", settings: { title: "x".repeat(80) } }, types.hero).length <= 48);
});

test("server error paths", () => {
  assert.deepEqual(parseErrorPath("pages.home.sections[1].columns[0][2].settings.url"), { region: "home", s: 1, c: 0, b: 2, item: null, key: "url", lang: "" });
  assert.deepEqual(parseErrorPath("footer.sections[0].columns[1][0].i18n.en.title"), { region: "footer", s: 0, c: 1, b: 0, item: null, key: "title", lang: "en" });
  assert.deepEqual(parseErrorPath("pages.single"), { region: "single", s: null, c: null, b: null, item: null, key: "", lang: "" });
  assert.deepEqual(parseErrorPath("header.sections[0].style.background"), { region: "header", s: 0, c: null, b: null, item: null, key: "", lang: "" });
  assert.deepEqual(parseErrorPath("pages.404.sections[0].columns[0][0].type"), { region: "404", s: 0, c: 0, b: 0, item: null, key: "", lang: "" });
  assert.equal(parseErrorPath("version"), null);
  // Landing blocks: an item's setting or translation, the item list, and
  // a section's anchor.
  assert.deepEqual(parseErrorPath("pages.front.sections[2].columns[0][1].items[3].settings.title"), { region: "front", s: 2, c: 0, b: 1, item: 3, key: "title", lang: "" });
  assert.deepEqual(parseErrorPath("pages.front.sections[0].columns[1][0].items[0].i18n.en.answer"), { region: "front", s: 0, c: 1, b: 0, item: 0, key: "answer", lang: "en" });
  assert.deepEqual(parseErrorPath("pages.front.sections[0].columns[0][0].items"), { region: "front", s: 0, c: 0, b: 0, item: -1, key: "", lang: "" });
  assert.deepEqual(parseErrorPath("pages.front.sections[4].anchor"), { region: "front", s: 4, c: null, b: null, item: null, key: "", lang: "", anchor: true });
});

test("anchors: validity, uniqueness on a page, set and remove", () => {
  for (const ok of ["features", "a", "pricing-2", "x".repeat(40)]) assert.ok(validAnchor(ok), ok);
  for (const bad of ["", "Features", "1x", "-x", "main", "comments", "search", "sap-x", "a b", "x".repeat(41), "é"]) assert.ok(!validAnchor(bad), bad);
  const l = {
    header: { sections: [{ id: "h", anchor: "top", layout: "1", columns: [[]] }] },
    pages: {
      front: { sections: [{ id: "a", anchor: "features", layout: "1", columns: [[]] }, { id: "b", layout: "1", columns: [[]] }] },
      home: { sections: [{ id: "c", anchor: "pricing", layout: "1", columns: [[]] }] },
    },
  };
  assert.deepEqual([...anchorsTaken(l, "front", 1)].sort(), ["features", "top"]);
  assert.deepEqual([...anchorsTaken(l, "front", 0)].sort(), ["top"], "its own anchor is not taken");
  assert.deepEqual([...anchorsTaken(l, "header", 0)].sort(), ["features", "pricing"], "the header shares every page");
  const set = setAnchor(l, "front", 1, "faq");
  assert.equal(set.pages.front.sections[1].anchor, "faq");
  assert.equal(l.pages.front.sections[1].anchor, undefined, "input changed");
  assert.ok(!("anchor" in setAnchor(set, "front", 1, "").pages.front.sections[1]));
});

test("items: add, move, remove, values and one-only settings", () => {
  const p = { s: 0, c: 0, b: 0 };
  let l = { pages: { front: { sections: [{ id: "s", layout: "1", columns: [[{ id: "pr", type: "pricing" }]] }] } } };
  const items = (x) => x.pages.front.sections[0].columns[0][0].items;
  l = addItem(l, "front", p, { settings: { name: "A" } });
  l = addItem(l, "front", p, { settings: { name: "B" } });
  l = addItem(l, "front", p, { settings: { name: "Z" } }, 0);
  assert.deepEqual(items(l).map((i) => i.settings.name), ["Z", "A", "B"]);
  l = moveItem(l, "front", p, 0, 2);
  assert.deepEqual(items(l).map((i) => i.settings.name), ["A", "B", "Z"]);
  assert.deepEqual(moveItem(l, "front", p, 0, -1), l, "no move past the edge");
  l = setItemValue(l, "front", p, 0, "highlight", "true", "", "false", true);
  l = setItemValue(l, "front", p, 1, "highlight", "true", "", "false", true);
  assert.deepEqual(items(l).map((i) => i.settings.highlight), [undefined, "true", undefined], "one highlighted plan");
  l = setItemValue(l, "front", p, 1, "highlight", "false", "", "false", true);
  assert.equal(items(l)[1].settings.highlight, undefined, "the default is not stored");
  l = setItemValue(l, "front", p, 2, "name", "Zeta", "en");
  assert.deepEqual(items(l)[2].i18n, { en: { name: "Zeta" } });
  l = setItemValue(l, "front", p, 2, "name", "", "en");
  assert.equal(items(l)[2].i18n, undefined);
  l = removeItem(l, "front", p, 0);
  l = removeItem(l, "front", p, 0);
  l = removeItem(l, "front", p, 0);
  assert.equal(items(l), undefined, "an empty list is dropped");
});

test("new blocks start from their starter; presets get fresh ids", () => {
  const starters = { features: { type: "features", items: [{ settings: { title: "Rápido" } }], i18n: { en: { title: "Why" } } } };
  const l = base();
  const b = newBlock(l, { type: "features" }, starters, { columns: "4" }, seeded());
  assert.equal(b.type, "features");
  assert.ok(!ids(l).has(b.id));
  assert.deepEqual(b.settings, { columns: "4" });
  assert.deepEqual(b.items, [{ settings: { title: "Rápido" } }]);
  b.items[0].settings.title = "x";
  assert.equal(starters.features.items[0].settings.title, "Rápido", "the starter is copied");
  assert.deepEqual(newBlock(l, { type: "hero" }, starters, {}, seeded()).items, undefined);

  const preset = { sections: [{ id: "s-1", anchor: "intro", layout: "1", columns: [[{ id: "b-2", type: "hero" }]] }, { id: "s-3", layout: "1", columns: [[{ id: "b-4", type: "cta" }]] }] };
  const r = reid(preset, l, seeded());
  const all = new Set([...ids(l)]);
  for (const s of r.sections) {
    assert.ok(!all.has(s.id)); all.add(s.id);
    for (const x of s.columns.flat()) { assert.ok(!all.has(x.id)); all.add(x.id); }
  }
  assert.equal(r.sections[0].anchor, "intro");
  assert.equal(preset.sections[0].id, "s-1", "input changed");
  assert.ok(PAGE_REGIONS.includes("front"));
  assert.equal(starter("front", {}, {}, seeded()).sections[0].columns[0][0].type, "hero");
});

test("history: undo, redo and merged typing", () => {
  const h = new History({ v: 0 });
  assert.ok(!h.canUndo && !h.canRedo);
  h.push({ v: 1 }, "", 0);
  h.push({ v: 2 }, "title", 1000);
  h.push({ v: 3 }, "title", 1500); // same field, soon after: one step
  assert.deepEqual(h.undo(), { v: 1 });
  assert.deepEqual(h.redo(), { v: 3 });
  h.push({ v: 4 }, "title", 9000); // too late to merge
  assert.deepEqual(h.undo(), { v: 3 });
  h.push({ v: 5 }, "", 9500); // a new change drops the redo branch
  assert.ok(!h.canRedo);
  assert.deepEqual(h.undo(), { v: 3 });
  assert.deepEqual(h.undo(), { v: 1 });
  assert.deepEqual(h.undo(), { v: 0 });
  assert.deepEqual(h.undo(), { v: 0 });
  const small = new History(0, 3);
  for (let i = 1; i <= 5; i++) small.push(i);
  assert.deepEqual(small.stack, [3, 4, 5]);
});

test("chooseOption picks what a native select would show", () => {
  const opts = [{ value: "", label: "No image" }, { value: "3", label: "Logo" }, { value: 7, label: "Hero" }];
  assert.deepEqual(chooseOption(opts, "3"), { index: 1, value: "3", label: "Logo" });
  assert.deepEqual(chooseOption(opts, "7"), { index: 2, value: "7", label: "Hero" }, "numbers compare as strings");
  assert.deepEqual(chooseOption(opts, ""), { index: 0, value: "", label: "No image" });
  assert.deepEqual(chooseOption(opts, undefined), { index: 0, value: "", label: "No image" });
  assert.deepEqual(chooseOption(opts.slice(1), "gone"), { index: 0, value: "3", label: "Logo" }, "unknown: the first");
  assert.deepEqual(chooseOption([], "x"), { index: -1, value: "", label: "" });
});

test("validClasses checks CSS class fields as the server does", () => {
  assert.equal(validClasses(""), "");
  assert.equal(validClasses("  promo   wide promo "), "promo wide");
  assert.equal(validClasses("a b c d e"), "a b c d e");
  for (const bad of ["A", "1a", "a_b", "a.b", "-a", "a b c d e f", 'a"b', "x".repeat(41)]) assert.equal(validClasses(bad), null, bad);
});

test("setSectionClass sets and removes a section's classes", () => {
  let l = setSectionClass(base(), "home", 1, "promo");
  assert.equal(getRegion(l, "home").sections[1].class, "promo");
  l = setSectionClass(l, "home", 1, "");
  assert.ok(!("class" in getRegion(l, "home").sections[1]));
});

test("emptyRegion keeps what a region requires", () => {
  const front = emptyRegion("front", {}, seeded());
  assert.equal(front.sections.length, 1);
  assert.deepEqual(front.sections[0].columns, [[]]);
  const page = emptyRegion("page", {}, seeded());
  assert.ok(hasBlock(page.sections[0], "single-content"));
  const footer = emptyRegion("footer", {}, seeded());
  assert.ok(footer.sections.some((s) => hasBlock(s, "footer-meta")));
  assert.ok(!hasBlock(footer.sections[0], "footer-meta"));
});

test("summary of an HTML block is its text", () => {
  assert.equal(summary({ type: "html", settings: { html: "<section class=\"x\"><h2>Our&nbsp;offer</h2><p>Now</p></section>" } }, { fields: [] }), "Our offer Now");
  assert.equal(summary({ type: "html", settings: { class: "promo wide" } }, { fields: [] }), ".promo .wide");
});

test("parseErrorPath marks a section's class", () => {
  assert.deepEqual(parseErrorPath("pages.front.sections[2].class"), { region: "front", s: 2, c: null, b: null, item: null, key: "", lang: "", sectionClass: true });
});

test("sectionToHTML: a section becomes one HTML block and keeps its settings", () => {
  const l = { pages: { front: { sections: [
    { id: "s-1", layout: "1-1", anchor: "offer", class: "promo", style: { padding: "large" },
      columns: [[{ id: "hero-1", type: "hero", settings: { title: "Hi" } }], [{ id: "image-1", type: "image", settings: {} }]] },
  ] } } };
  const out = sectionToHTML(l, "front", 0, "<p>Hi</p>", seeded());
  const sec = getRegion(out, "front").sections[0];
  assert.equal(sec.id, "s-1");
  assert.equal(sec.anchor, "offer");
  assert.equal(sec.class, "promo");
  assert.deepEqual(sec.style, { padding: "large" });
  assert.equal(sec.layout, "1");
  assert.equal(sec.columns.length, 1);
  assert.equal(sec.columns[0].length, 1);
  assert.equal(sec.columns[0][0].type, "html");
  assert.equal(sec.columns[0][0].settings.html, "<p>Hi</p>");
  assert.ok(!ids(l).has(sec.columns[0][0].id), "a new id");
  // The original is untouched (undo keeps it).
  assert.equal(getRegion(l, "front").sections[0].columns.length, 2);

  // A section that is one HTML block keeps the block and its translations.
  const again = sectionToHTML(out, "front", 0, "<p>Hola</p>");
  const b = getRegion(again, "front").sections[0].columns[0][0];
  assert.equal(b.id, sec.columns[0][0].id);
  assert.equal(b.settings.html, "<p>Hola</p>");
});

test("sectionHTML: an HTML block section gives its stored markup", () => {
  const sec = { id: "s-1", layout: "1", columns: [[{ id: "h", type: "html", settings: { html: "<p>x</p>" } }]] };
  assert.equal(sectionHTML(null, sec), "<p>x</p>");
  assert.equal(sectionHTML(null, { id: "s-2", layout: "1", columns: [[{ id: "a", type: "hero" }]] }), "");
});
