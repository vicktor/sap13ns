// Tests for the notice of a site that changed while a page was open
// (ui/static/refresh.js), run against a fake page: it asks only when the
// visitor comes back and five minutes have passed, tells or reloads once
// per version, and does nothing without the version on the script tag.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import vm from "node:vm";

const src = readFileSync(new URL("../../static/refresh.js", import.meta.url), "utf8");

function page({ mode = "ask", version = "aaa", server = "aaa", active = null, framed = false, tag = true } = {}) {
  const listeners = {};
  const body = [];
  const store = {};
  const el = (name) => ({ nodeName: name.toUpperCase(), children: [], attrs: {}, listeners: {},
    setAttribute(k, v) { this.attrs[k] = v; }, addEventListener(k, f) { this.listeners[k] = f; },
    append(...kids) { this.children.push(...kids); }, remove() { body.splice(body.indexOf(this), 1); } });
  const p = { now: 1000, asked: 0, reloads: 0, server, body, store };
  const ctx = {
    document: {
      visibilityState: "visible", activeElement: active, body: { append: (x) => body.push(x) },
      querySelector: () => (tag ? { dataset: { sapVersion: version, sapRefresh: mode, sapRefreshText: "Updated.", sapRefreshUpdate: "Update", sapRefreshClose: "Close" } } : null),
      createElement: el, addEventListener: (k, f) => { listeners[k] = f; },
    },
    Date: { now: () => p.now },
    sessionStorage: { getItem: (k) => store[k] ?? null, setItem: (k, v) => { store[k] = v; } },
    location: { reload: () => { p.reloads++; } },
    fetch: async () => { p.asked++; return { ok: true, json: async () => ({ version: p.server }) }; },
    setInterval: () => 0,
  };
  ctx.window = ctx;
  ctx.top = framed ? {} : ctx;
  vm.runInNewContext(src, ctx);
  p.back = async (after = 6 * 60 * 1000) => { p.now += after; await listeners.visibilitychange?.(); };
  p.listens = () => Boolean(listeners.visibilitychange);
  p.doc = ctx.document;
  return p;
}

test("refresh: asks when the visitor comes back, at most every five minutes", async () => {
  const p = page();
  await p.back(60 * 1000);
  assert.equal(p.asked, 0, "too soon");
  await p.back();
  assert.equal(p.asked, 1);
  assert.equal(p.body.length, 0, "the same version: nothing to say");
  p.doc.visibilityState = "hidden";
  await p.back();
  assert.equal(p.asked, 1, "not while the page is hidden");
});

test("refresh: tells the visitor, once", async () => {
  const p = page();
  p.server = "bbb";
  await p.back();
  assert.equal(p.body.length, 1);
  const [text, update, close] = p.body[0].children;
  assert.equal(text.textContent, "Updated.");
  assert.equal(update.textContent, "Update");
  assert.equal(close.attrs["aria-label"], "Close");
  assert.equal(p.reloads, 0, "never by itself");
  await p.back();
  assert.equal(p.asked, 1, "it stops asking");
  update.listeners.click();
  assert.equal(p.reloads, 1);
  close.listeners.click();
  assert.equal(p.body.length, 0);
});

test("refresh: reloads by itself, unless someone is typing or it did already", async () => {
  const p = page({ mode: "reload" });
  p.server = "bbb";
  await p.back();
  assert.equal(p.reloads, 1);
  assert.equal(p.store["sap13ns-refreshed"], "bbb");
  assert.equal(p.body.length, 0);

  const typing = page({ mode: "reload", server: "bbb", active: { nodeName: "TEXTAREA" } });
  await typing.back();
  assert.equal(typing.reloads, 0);
  assert.equal(typing.body.length, 1, "a notice instead");

  const again = page({ mode: "reload", server: "bbb" });
  again.store["sap13ns-refreshed"] = "bbb";
  await again.back();
  assert.equal(again.reloads, 0, "a page that came back old does not reload for ever");
  assert.equal(again.body.length, 1);
});

test("refresh: nothing without the version, or in a frame", async () => {
  assert.equal(page({ tag: false }).listens(), false);
  assert.equal(page({ framed: true }).listens(), false);
});
