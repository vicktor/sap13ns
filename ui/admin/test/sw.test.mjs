// Tests for the site's service worker (ui/static/sw.js), run with fake
// caches and a fake network: pages come from the network first, only
// public responses are kept, and what is not the site's public side is
// left alone.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import vm from "node:vm";

const src = readFileSync(new URL("../../static/sw.js", import.meta.url), "utf8");
const ORIGIN = "https://site.test";

function worker({ offline = true } = {}) {
  const listeners = {};
  const stores = {};
  const net = { online: true, pages: {}, asked: [] };
  const res = (body, { cc = "public, no-cache", type = "text/html; charset=utf-8", ok = true } = {}) => ({
    body, ok, type: "basic", headers: { get: (k) => ({ "cache-control": cc, "content-type": type })[k.toLowerCase()] ?? null },
    clone() { return this; }, text: async () => body,
  });
  const open = async (name) => {
    const m = (stores[name] ||= new Map());
    return {
      put: async (req, r) => { m.delete(String(req.url || req)); m.set(String(req.url || req).replace(/^\//, ORIGIN + "/"), r); },
      add: async (u) => { m.set(ORIGIN + u, res("file", { type: "text/css" })); },
      keys: async () => [...m.keys()].map((url) => ({ url })),
      delete: async (k) => m.delete(k.url || k),
    };
  };
  const ctx = {
    self: null, URL, Request: class { constructor(url) { this.url = url; } }, Promise, Set, Math,
    caches: {
      open, keys: async () => Object.keys(stores), delete: async (k) => delete stores[k],
      match: async (u) => { const key = String(u).replace(/^\//, ORIGIN + "/"); for (const m of Object.values(stores)) if (m.has(key)) return m.get(key); },
    },
    fetch: async (req) => {
      const url = String(req.url || req).replace(/^\//, ORIGIN + "/");
      net.asked.push(url);
      if (!net.online) throw new TypeError("offline");
      return net.pages[url] || res("page " + url);
    },
  };
  ctx.self = { SAP: { offline }, location: { origin: ORIGIN }, addEventListener: (k, f) => { listeners[k] = f; }, skipWaiting: async () => {}, clients: { claim: async () => {} } };
  ctx.self.caches = ctx.caches;
  vm.runInNewContext(src, ctx);
  const request = (path, { mode = "navigate", method = "GET", origin = ORIGIN } = {}) => ({ url: origin + path, mode, method, headers: { has: () => false } });
  const get = async (path, opts) => {
    let answer;
    listeners.fetch({ request: request(path, opts), respondWith: (p) => { answer = p; } });
    return answer === undefined ? undefined : answer.then((r) => r.body, (e) => "ERROR " + e.message);
  };
  const install = async () => { let done; listeners.install({ waitUntil: (p) => { done = p; } }); await done; };
  return { net, stores, get, install, res };
}

test("worker: pages come from the network, and a copy answers only without it", async () => {
  const w = worker();
  await w.install();
  assert.ok(w.stores["sap-pages-1"].has(ORIGIN + "/api/offline"), "the offline page is kept at install");
  assert.equal(await w.get("/blog/one"), "page " + ORIGIN + "/blog/one");
  w.net.pages[ORIGIN + "/blog/one"] = w.res("newer");
  assert.equal(await w.get("/blog/one"), "newer", "online, never the copy");
  w.net.online = false;
  assert.equal(await w.get("/blog/one"), "newer", "offline, the last copy");
  assert.equal(await w.get("/blog/two"), "page " + ORIGIN + "/api/offline", "offline, a page never opened shows the offline page");
});

test("worker: only public pages are kept", async () => {
  const w = worker();
  w.net.pages[ORIGIN + "/account"] = w.res("mine", { cc: "" });
  w.net.pages[ORIGIN + "/secret"] = w.res("secret", { cc: "private, no-store" });
  w.net.pages[ORIGIN + "/data"] = w.res("{}", { type: "application/json" });
  for (const p of ["/account", "/secret", "/data"]) await w.get(p);
  assert.equal(w.stores["sap-pages-1"]?.size || 0, 0);
});

test("worker: the admin, the API, other sites and writes are left alone", async () => {
  const w = worker();
  for (const p of ["/admin/", "/admin/posts", "/api/assistant", "/login", "/mcp", "/sso/start", "/feed", "/?preview_theme=x", "/?customize=draft"]) {
    assert.equal(await w.get(p), undefined, p);
  }
  assert.equal(await w.get("/blog/one", { method: "POST" }), undefined);
  assert.equal(await w.get("/x.png", { mode: "no-cors", origin: "https://other.test" }), undefined);
  assert.equal(await w.get("/blog/data.json", { mode: "cors" }), undefined, "a request that is no page and no file of the site");
  assert.equal(w.net.asked.length, 0);
});

test("worker: files with their version in the address come from the copy", async () => {
  const w = worker();
  const css = "/themes/x/static/theme.css?v=abc";
  await w.get(css, { mode: "no-cors" });
  await w.get(css, { mode: "no-cors" });
  assert.equal(w.net.asked.filter((u) => u.endsWith(css)).length, 1, "asked once");
  await w.get("/static/plain.js", { mode: "no-cors" });
  await w.get("/static/plain.js", { mode: "no-cors" });
  assert.equal(w.net.asked.filter((u) => u.endsWith("/static/plain.js")).length, 2, "without a version, the network first");
  w.net.online = false;
  assert.equal(await w.get("/static/plain.js", { mode: "no-cors" }), "page " + ORIGIN + "/static/plain.js", "and its copy without one");
});

test("worker: off, it handles nothing", async () => {
  const w = worker({ offline: false });
  await w.install();
  assert.equal(Object.keys(w.stores).length, 0);
  assert.equal(await w.get("/blog/one"), undefined);
});
