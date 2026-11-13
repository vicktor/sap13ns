// sap13ns service worker, served at /sw.js after one line that sets self.SAP
// (internal/web/worker.go). Not part of a bundle: a worker is its own file.
//
// It does two things, each only when the site turned it on (self.SAP):
// reading without a connection, below, and showing the notifications of
// new posts, at the end.
//
// Without a connection, a visitor still reads the public pages this
// browser opened. The rules that keep it safe:
//   - pages come from the network first; a copy answers only when the
//     network fails, so nobody reads an old page while online;
//   - only public responses are kept (Cache-Control: public), pages and
//     files alike: a page rendered for someone signed in never is one,
//     and neither is the stylesheet of a design draft, which is not
//     handled at all;
//   - the admin, the API, sign-in and previews are never touched;
//   - only this site's own addresses: anything else goes to the network
//     as if there were no worker.
"use strict";

const SAP = self.SAP || {};
const VERSION = "1";
const PAGES = "sap-pages-" + VERSION;
const ASSETS = "sap-assets-" + VERSION;
const OFFLINE = "/api/offline";
const MAX = { [PAGES]: 40, [ASSETS]: 150 };
const HANDS_OFF = /^\/(admin|api|mcp|sso|billing|login|logout|register|setup|feed|sw\.js)(\/|$)/;
const FILES = /^\/(static|themes|uploads)\//;

const isPublic = (res) => res.ok && res.type === "basic" && /(^|[\s,])public([\s,]|$)/.test(res.headers.get("Cache-Control") || "");
const isPage = (res) => /^text\/html/.test(res.headers.get("Content-Type") || "");

async function keep(name, req, res) {
  const cache = await caches.open(name);
  await cache.put(req, res);
  const keys = await cache.keys();
  for (const old of keys.slice(0, Math.max(0, keys.length - MAX[name]))) await cache.delete(old);
}

// The offline page and what it needs to look right.
async function install() {
  if (!SAP.offline) return;
  try {
    const res = await fetch(new Request(OFFLINE, { cache: "reload", credentials: "omit" }));
    if (!res.ok) return;
    const html = await res.clone().text();
    await (await caches.open(PAGES)).put(OFFLINE, res);
    const files = [...html.matchAll(/(?:href|src)="(\/(?:static|themes)\/[^"]+)"/g)].map((m) => m[1].replace(/&amp;/g, "&"));
    const assets = await caches.open(ASSETS);
    await Promise.all([...new Set(files)].map((f) => assets.add(f).catch(() => {})));
  } catch (_) { /* the next install tries again */ }
}

self.addEventListener("install", (e) => e.waitUntil(install().then(() => self.skipWaiting())));
self.addEventListener("activate", (e) => e.waitUntil(
  caches.keys()
    .then((keys) => Promise.all(keys.filter((k) => k.startsWith("sap-") && k !== PAGES && k !== ASSETS).map((k) => caches.delete(k))))
    .then(() => self.clients.claim())));

async function page(req) {
  try {
    const res = await fetch(req);
    if (isPublic(res) && isPage(res)) await keep(PAGES, req.url, res.clone()).catch(() => {});
    return res;
  } catch (err) {
    const copy = (await caches.match(req.url, { ignoreVary: true })) || (await caches.match(OFFLINE, { ignoreVary: true }));
    if (copy) return copy;
    throw err;
  }
}

// A file whose address says its version never changes: the copy first.
async function file(req, url) {
  const fixed = url.searchParams.has("v") || /^\/themes\/[^/]+\/custom\//.test(url.pathname);
  if (fixed) {
    const copy = await caches.match(req.url, { ignoreVary: true });
    if (copy) return copy;
  }
  try {
    const res = await fetch(req);
    if (isPublic(res)) await keep(ASSETS, req.url, res.clone()).catch(() => {});
    return res;
  } catch (err) {
    const copy = await caches.match(req.url, { ignoreVary: true });
    if (copy) return copy;
    throw err;
  }
}

self.addEventListener("fetch", (e) => {
  if (!SAP.offline) return;
  const req = e.request;
  if (req.method !== "GET") return;
  const url = new URL(req.url);
  if (url.origin !== self.location.origin) return;
  if (url.searchParams.has("preview_theme") || url.searchParams.has("customize")) return;
  if (req.mode === "navigate") {
    if (!HANDS_OFF.test(url.pathname)) e.respondWith(page(req));
    return;
  }
  // A design draft's stylesheet is its author's alone.
  if (/^\/themes\/[^/]+\/custom\/draft-/.test(url.pathname)) return;
  if (FILES.test(url.pathname) && !req.headers.has("Range")) e.respondWith(file(req, url));
});

// Notifications of new posts (internal/web/push.go): what the site sent,
// shown; a click opens the post, in a window of the site that is open
// already when there is one. Only addresses of this site are opened.
self.addEventListener("push", (e) => {
  let d = {};
  try { d = e.data.json(); } catch (_) { /* not ours: a plain notice */ }
  e.waitUntil(self.registration.showNotification(String(d.title || self.location.hostname), {
    body: String(d.body || ""), icon: d.icon || undefined, badge: d.icon || undefined, tag: d.tag || undefined, lang: d.lang || undefined,
    data: { url: String(d.url || "/") },
  }));
});

self.addEventListener("notificationclick", (e) => {
  e.notification.close();
  let url;
  try { url = new URL((e.notification.data && e.notification.data.url) || "/", self.location.origin); } catch (_) { return; }
  if (url.origin !== self.location.origin) return;
  e.waitUntil(self.clients.matchAll({ type: "window", includeUncontrolled: true }).then((all) => {
    for (const c of all) if (c.url === url.href && "focus" in c) return c.focus();
    return self.clients.openWindow(url.href);
  }));
});
