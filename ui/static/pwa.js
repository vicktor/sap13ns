// sap13ns: registers the site's service worker (/sw.js, internal/web/worker.go)
// on public pages while the site uses it: shared/head then puts data-sap-sw
// on the bundle's script tag. Without it, a worker this browser still has
// from before is removed.
(() => {
  "use strict";
  if (!("serviceWorker" in navigator)) return;
  const sw = navigator.serviceWorker;
  if (!document.querySelector("script[data-sap-sw]")) {
    if (sw.controller) {
      sw.getRegistrations().then((all) => { for (const r of all) r.unregister(); }).catch(() => {});
      // Its copies go with it (the worker that removes itself may never run).
      if ("caches" in window) caches.keys().then((keys) => { for (const k of keys) if (k.startsWith("sap-")) caches.delete(k); }).catch(() => {});
    }
    return;
  }
  if (window.top !== window) return;
  const register = () => sw.register("/sw.js", { scope: "/", updateViaCache: "none" }).catch(() => { /* blocked: the site works without it */ });
  if (document.readyState === "complete") register(); else addEventListener("load", register, { once: true });
})();
