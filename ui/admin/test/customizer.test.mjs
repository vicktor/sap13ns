// Tests for the theme customiser's pure helpers (ui/static/customizer.js).
import { test } from "node:test";
import assert from "node:assert/strict";
import { normalizeHex, designFrom, needsReload, previewURL, showIf } from "../../static/customizer.js";

test("normalizeHex", () => {
  assert.equal(normalizeHex("#ABC"), "#aabbcc");
  assert.equal(normalizeHex("1d4ed8"), "#1d4ed8");
  assert.equal(normalizeHex(" #1D4ED8 "), "#1d4ed8");
  for (const bad of ["", "#12", "#12345g", "red", "#1234567", null, undefined]) assert.equal(normalizeHex(bad), "");
});

test("designFrom maps form fields to the design JSON", () => {
  const d = designFrom([
    { name: "light.background", value: "#FFF" },
    { name: "light.primary", value: "" },
    { name: "light.border", value: "nope" },
    { name: "dark.background", value: "101014" },
    { name: "fonts.body.stack", value: "serif" },
    { name: "fonts.body.media", value: "7", type: "int" },
    { name: "fonts.heading.stack", value: "media" },
    { name: "fonts.heading.media", value: "7", type: "int" },
    { name: "fonts.mono.stack", value: "" },
    { name: "fonts.mono.media", value: "0", type: "int" },
    { name: "scale.text", value: "110" },
    { name: "header.brand", value: "" },
    { name: "header.logo", value: "3", type: "int", kind: "radio", checked: true },
    { name: "header.hide_search", type: "bool", kind: "checkbox", checked: true },
    { name: "header.hide_lang", type: "bool", kind: "checkbox", checked: false },
    { name: "footer.columns", value: "3", type: "int" },
    { name: "footer.text", value: "  Hello  " },
    { name: "footer.text_i18n.es", value: "" },
    { name: "home.per_page", value: "", type: "int" },
    { name: "css", value: "a{}" },
    { name: "options.accent", value: "blue" },
    { name: "options.hero", value: "true", type: "boolstr", kind: "checkbox", checked: false },
    { name: "options.layout", value: "x" },
  ], { accent: "blue", hero: "true", layout: "y" });
  assert.deepEqual(d.light, { background: "#ffffff" });
  assert.deepEqual(d.dark, { background: "#101014" });
  assert.deepEqual(d.fonts, { body: { stack: "serif" }, heading: { stack: "media", media: 7 }, mono: {} });
  assert.equal(d.scale.text, "110");
  assert.equal(d.header.logo, undefined, "no logo without a logo brand");
  assert.equal(d.header.hide_search, true);
  assert.equal(d.footer.columns, 3);
  assert.equal(d.footer.text, "Hello");
  assert.equal(d.footer.text_i18n, undefined);
  assert.equal(d.home.per_page, 0);
  assert.equal(d.css, "a{}");
  assert.deepEqual(d.options, { hero: "false", layout: "x" }, "only options that differ from the stored ones");
});

test("designFrom skips unchecked radios", () => {
  const d = designFrom([
    { name: "header.brand", value: "logo" },
    { name: "header.logo", value: "3", type: "int", kind: "radio", checked: false },
    { name: "header.logo", value: "4", type: "int", kind: "radio", checked: true },
  ]);
  assert.equal(d.header.logo, 4);
});

test("needsReload only for what CSS cannot show", () => {
  const a = designFrom([{ name: "light.background", value: "#fff" }, { name: "header.sticky", value: "on" }]);
  const b = designFrom([{ name: "light.background", value: "#000" }, { name: "header.sticky", value: "off" }]);
  assert.equal(needsReload(a, b), false);
  const c = designFrom([{ name: "home.layout", value: "grid" }]);
  assert.equal(needsReload(a, c), true);
  const e = designFrom([{ name: "header.hide_search", type: "bool", kind: "checkbox", checked: true }]);
  assert.equal(needsReload(a, e), true);
  const f = designFrom([{ name: "options.accent", value: "red" }], { accent: "blue" });
  assert.equal(needsReload(a, f), true);
});

test("previewURL keeps the draft and refuses other places", () => {
  const o = "http://127.0.0.1:8165";
  assert.equal(previewURL("/blog/hello?page=2#c", o, "apple", "dark"), "/blog/hello?page=2&preview_theme=apple&customize=draft&scheme=dark#c");
  assert.equal(previewURL(o + "/?q=x", o, "bento", ""), "/?q=x&preview_theme=bento&customize=draft");
  assert.equal(previewURL("https://example.com/", o, "apple", "light"), null);
  assert.equal(previewURL("//example.com/x", o, "apple", "light"), null);
  for (const p of ["/admin", "/admin/posts", "/login", "/logout"]) assert.equal(previewURL(p, o, "apple", "light"), null, p);
  assert.notEqual(previewURL("/administrator-notes", o, "apple", "light"), null);
  for (const p of ["/feed.xml", "/sitemap.xml", "/feed.json", "/uploads/2026/10/a.jpg", "/uploads/x"]) assert.equal(previewURL(p, o, "apple", "light"), null, p);
  assert.notEqual(previewURL("/pages/about.html", o, "apple", "light"), null);
});

test("showIf", () => {
  assert.equal(showIf("fonts.body.stack=media", { "fonts.body.stack": "media" }), true);
  assert.equal(showIf("fonts.body.stack=media", { "fonts.body.stack": "serif" }), false);
  assert.equal(showIf("header.brand!=", { "header.brand": "logo" }), true);
  assert.equal(showIf("header.brand!=", {}), false);
  assert.equal(showIf("", {}), true);
});
