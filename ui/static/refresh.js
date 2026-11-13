// sap13ns: a page that is open learns that the site changed under it (a new
// theme, a published design, new scripts or styles) and tells the visitor,
// or reloads. On only with Settings → Site → "When the site changes while a
// page is open": shared/head then puts the site's version on the bundle's
// script tag, and GET /api/site-version answers the current one
// (internal/web/refresh.go). Asked when the visitor comes back to the page,
// at most every five minutes; never in a frame (the design preview).
(() => {
  "use strict";
  const tag = document.querySelector("script[data-sap-version]");
  if (!tag || window.top !== window) return;
  const have = tag.dataset.sapVersion;
  const mode = tag.dataset.sapRefresh;
  const EVERY = 5 * 60 * 1000;
  const KEY = "sap13ns-refreshed";
  let last = Date.now();
  let done = false;
  let asking = false;

  const typing = () => {
    const a = document.activeElement;
    return Boolean(a) && (a.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(a.nodeName));
  };
  const remembered = () => { try { return sessionStorage.getItem(KEY); } catch (_) { return null; } };
  const remember = (v) => { try { sessionStorage.setItem(KEY, v); } catch (_) { /* private mode */ } };

  const notice = () => {
    const box = document.createElement("div");
    box.className = "sap-refresh";
    box.setAttribute("role", "status");
    const text = document.createElement("p");
    text.textContent = tag.dataset.sapRefreshText || "";
    const update = document.createElement("button");
    update.type = "button";
    update.className = "sap-refresh-update";
    update.textContent = tag.dataset.sapRefreshUpdate || "";
    update.addEventListener("click", () => location.reload());
    const close = document.createElement("button");
    close.type = "button";
    close.className = "sap-refresh-close";
    close.setAttribute("aria-label", tag.dataset.sapRefreshClose || "");
    close.textContent = "\u00d7";
    close.addEventListener("click", () => box.remove());
    box.append(text, update, close);
    document.body.append(box);
  };

  const check = async () => {
    if (done || asking || document.visibilityState !== "visible" || Date.now() - last < EVERY) return;
    asking = true;
    last = Date.now();
    try {
      const r = await fetch("/api/site-version", { cache: "no-store", credentials: "omit", headers: { Accept: "application/json" } });
      const now = r.ok ? (await r.json()).version : "";
      if (now && now !== have) {
        done = true;
        // Reloading once per version: a page that still shows the old one
        // after it (a cache in between) must not reload for ever.
        if (mode === "reload" && !typing() && remembered() !== now) {
          remember(now);
          location.reload();
        } else notice();
      }
    } catch (_) { /* offline: the next time */ }
    asking = false;
  };
  document.addEventListener("visibilitychange", check);
  setInterval(check, EVERY);
})();
