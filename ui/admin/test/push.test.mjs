// Tests for the notifications of new posts in the page (ui/static/push.js)
// and in the worker (ui/static/sw.js): the permission is asked only after
// a click, the subscription goes to the site, and a notification opens
// only an address of the site.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import vm from "node:vm";

const read = (f) => readFileSync(new URL("../../static/" + f, import.meta.url), "utf8");

function page({ tag = true, permission = "granted", subscribed = false, saved = true } = {}) {
  const p = { asked: 0, sent: [], listeners: {}, sub: null };
  const el = (attrs = {}) => ({ hidden: attrs.hidden || false, textContent: "", attrs: {}, setAttribute(k, v) { this.attrs[k] = v; } });
  p.wrap = el({ hidden: true }); p.toggle = el(); p.label = el(); p.own = el();
  const subscription = () => ({ endpoint: "https://fcm.googleapis.com/x", toJSON: () => ({ endpoint: "https://fcm.googleapis.com/x", keys: { p256dh: "k", auth: "a" } }), unsubscribe: async () => { p.sub = null; } });
  if (subscribed) p.sub = subscription();
  const reg = { pushManager: { getSubscription: async () => p.sub, subscribe: async (o) => { p.options = o; p.sub = subscription(); return p.sub; } } };
  const ctx = {
    document: {
      documentElement: { lang: "es" },
      querySelector: () => (tag ? { dataset: { sapPush: "AQID", sapPushOn: "Notify me", sapPushOff: "Stop", sapPushBlocked: "Blocked" } } : null),
      querySelectorAll: (sel) => (sel === "[data-sap-push-wrap]" ? [p.wrap] : sel === "[data-sap-push-label]" ? [p.label] : sel === ".sap-push-toggle" ? [p.own] : [p.toggle, p.own]),
      addEventListener: (k, f) => { p.listeners[k] = f; },
    },
    navigator: { serviceWorker: { ready: Promise.resolve(reg) } },
    PushManager: {}, Notification: { requestPermission: async () => { p.asked++; return permission; } },
    fetch: async (path, o) => { p.sent.push([path, JSON.parse(o.body), o.headers["Content-Type"]]); return { ok: saved }; },
    atob: (s) => Buffer.from(s, "base64").toString("binary"), Uint8Array, JSON, String, Boolean, Error, Promise,
  };
  ctx.window = ctx; ctx.top = ctx;
  vm.runInNewContext(read("push.js"), ctx);
  p.click = async () => { await p.listeners.click?.({ target: { closest: () => p.toggle }, preventDefault() {} }); };
  p.settle = () => new Promise((r) => setTimeout(r, 0));
  return p;
}

test("push: the link shows what a click does, and nothing is asked before it", async () => {
  const p = page();
  await p.settle();
  assert.equal(p.wrap.hidden, false);
  assert.equal(p.label.textContent, "Notify me");
  assert.equal(p.asked, 0);
  await p.click();
  assert.equal(p.asked, 1);
  assert.deepEqual([...p.options.applicationServerKey], [1, 2, 3]);
  assert.deepEqual(p.sent, [["/api/push/subscribe", { endpoint: "https://fcm.googleapis.com/x", keys: { p256dh: "k", auth: "a" }, lang: "es" }, "application/json"]]);
  assert.equal(p.label.textContent, "Stop");
  assert.equal(p.toggle.attrs["aria-pressed"], "true");
  await p.click();
  assert.equal(p.sent[1][0], "/api/push/unsubscribe");
  assert.equal(p.sub, null);
  assert.equal(p.label.textContent, "Notify me");
});

test("push: a refusal, or a site that did not keep it, leaves nothing behind", async () => {
  const no = page({ permission: "denied" });
  await no.settle(); await no.click();
  assert.equal(no.sub, null);
  assert.equal(no.sent.length, 0);
  assert.equal(no.label.textContent, "Blocked");
  const full = page({ saved: false });
  await full.settle(); await full.click();
  assert.equal(full.sub, null, "the browser's subscription is undone");
});

test("push: without the site's key, an author's button is not shown", async () => {
  const p = page({ tag: false });
  assert.equal(p.own.hidden, true);
  assert.equal(p.listeners.click, undefined);
  assert.equal(p.wrap.hidden, true);
});

test("worker: a notification shows what the site sent and opens only its own addresses", async () => {
  const listeners = {}; const shown = []; const opened = [];
  const self = { SAP: { push: true }, location: { origin: "https://site.test", hostname: "site.test" }, addEventListener: (k, f) => { listeners[k] = f; },
    registration: { showNotification: async (title, o) => { shown.push([title, o]); } },
    clients: { matchAll: async () => [], openWindow: async (u) => { opened.push(u); } } };
  vm.runInNewContext(read("sw.js"), { self, URL, caches: {}, Promise, String, Math, Set });
  const wait = async (fn) => { let done; fn((p) => { done = p; }); await done; };
  await wait((waitUntil) => listeners.push({ data: { json: () => ({ title: "Hola", body: "Texto", url: "/blog/hola", icon: "/i.png", tag: "post-hola" }) }, waitUntil }));
  assert.equal(shown[0][0], "Hola");
  assert.equal(shown[0][1].body, "Texto");
  assert.equal(shown[0][1].data.url, "/blog/hola");
  await wait((waitUntil) => listeners.push({ data: { json: () => { throw new Error("not json"); } }, waitUntil }));
  assert.equal(shown[1][0], "site.test", "a message that is not the site's still shows something");
  const click = async (url) => { let done = Promise.resolve(); listeners.notificationclick({ notification: { close() {}, data: { url } }, waitUntil: (p) => { done = p; } }); await done; };
  await click("/blog/hola");
  await click("https://evil.test/x");
  await click("//evil.test/x");
  assert.deepEqual(opened, ["https://site.test/blog/hola"]);
});
