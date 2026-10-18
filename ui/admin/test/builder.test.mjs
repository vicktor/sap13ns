// Tests for the page builder's pure operations (ui/static/builder.js).
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  getRegion, setRegion, ids, newID, addSection, removeSection, moveSection, duplicateSection, fitColumns, setPreset,
  setStyle, insertBlock, removeBlock, moveBlock, dropIndex, duplicateBlock, setValue, find, step, count, allowed,
  required, starter, summary, parseErrorPath, History,
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
  for (const name of ["header", "footer", "home", "single", "page", "archive", "search", "404"]) {
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
  assert.deepEqual(parseErrorPath("pages.home.sections[1].columns[0][2].settings.url"), { region: "home", s: 1, c: 0, b: 2, key: "url", lang: "" });
  assert.deepEqual(parseErrorPath("footer.sections[0].columns[1][0].i18n.en.title"), { region: "footer", s: 0, c: 1, b: 0, key: "title", lang: "en" });
  assert.deepEqual(parseErrorPath("pages.single"), { region: "single", s: null, c: null, b: null, key: "", lang: "" });
  assert.deepEqual(parseErrorPath("header.sections[0].style.background"), { region: "header", s: 0, c: null, b: null, key: "", lang: "" });
  assert.deepEqual(parseErrorPath("pages.404.sections[0].columns[0][0].type"), { region: "404", s: 0, c: 0, b: 0, key: "", lang: "" });
  assert.equal(parseErrorPath("version"), null);
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
