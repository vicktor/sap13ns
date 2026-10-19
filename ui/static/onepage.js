// sap13ns one-page sites: the menu follows the reader. Loaded by shared/head
// on pages with sections to scroll to (PageData.OnePage). A menu link to a
// section of this page (#features) gets aria-current="true" while that
// section is in view, and --sap-scroll-offset keeps the sections clear of a
// sticky header when a link scrolls to them (shared.css: .sap-anchor). The
// scrolling itself is the browser's: smooth only when the reader has not
// asked for reduced motion (CSS). Vanilla, CSP-safe, no inline code.
(() => {
  "use strict";
  const root = document.documentElement;

  // ------------------------------------------------------- sticky header --
  // The first header that stays at the top while scrolling.
  const sticky = () => {
    for (const el of document.querySelectorAll("body header, body [data-sticky]")) {
      const pos = getComputedStyle(el).position;
      if ((pos === "sticky" || pos === "fixed") && el.getBoundingClientRect().top <= 1) return el;
    }
    return null;
  };
  let offset = 0;
  const measure = () => {
    const h = sticky();
    offset = h ? Math.ceil(h.getBoundingClientRect().height) : 0;
    // CSSOM, which the CSP allows (no style attribute in the markup).
    root.style.setProperty("--sap-scroll-offset", `${offset + 16}px`);
  };
  measure();
  addEventListener("resize", measure, { passive: true });
  addEventListener("load", measure, { once: true });

  // -------------------------------------------------------- current link --
  const links = [...document.querySelectorAll('header a[href^="#"], nav a[href^="#"], footer a[href^="#"]')]
    .filter((a) => a.getAttribute("href").length > 1 && !a.classList.contains("skip-link"));
  const byId = new Map();
  for (const a of links) {
    let id;
    try { id = decodeURIComponent(a.getAttribute("href").slice(1)); } catch (_) { continue; }
    const target = document.getElementById(id);
    if (!target || target.id === "main") continue;
    if (!byId.has(id)) byId.set(id, { target, links: [] });
    byId.get(id).links.push(a);
  }
  if (!byId.size || !("IntersectionObserver" in window)) return;

  let current = "";
  const mark = (id) => {
    if (id === current) return;
    current = id;
    for (const [key, { links: ls }] of byId) {
      for (const a of ls) {
        if (key === id) a.setAttribute("aria-current", "true");
        else a.removeAttribute("aria-current");
      }
    }
  };

  // A section is current while it crosses a band below the header, from
  // the header's edge to 40% down the window; with two in the band, the
  // lower one has just arrived and wins. Above the first section (a hero
  // without an anchor), no link is current.
  const inBand = new Set();
  const order = [...byId.keys()].sort((a, b) => {
    const pa = byId.get(a).target, pb = byId.get(b).target;
    return pa.compareDocumentPosition(pb) & Node.DOCUMENT_POSITION_FOLLOWING ? -1 : 1;
  });
  const pick = () => {
    let id = "";
    for (const key of order) if (inBand.has(key)) id = key;
    // At the very bottom the last section may be too short to reach the
    // band: it is current then.
    if (innerHeight + scrollY >= root.scrollHeight - 2) {
      const last = order.at(-1);
      if (byId.get(last).target.getBoundingClientRect().top < innerHeight) id = last;
    }
    mark(id);
  };
  let io;
  const observe = () => {
    io?.disconnect();
    inBand.clear();
    io = new IntersectionObserver((entries) => {
      for (const e of entries) {
        if (e.isIntersecting) inBand.add(e.target.id); else inBand.delete(e.target.id);
      }
      pick();
    }, { rootMargin: `-${offset + 1}px 0px -60% 0px`, threshold: 0 });
    for (const { target } of byId.values()) io.observe(target);
  };
  observe();
  let last = offset;
  addEventListener("resize", () => { if (offset !== last) { last = offset; observe(); } }, { passive: true });
  addEventListener("scroll", () => { if (innerHeight + scrollY >= root.scrollHeight - 2) pick(); }, { passive: true });

  // Following a link marks it at once, before the scroll ends.
  document.addEventListener("click", (e) => {
    const a = e.target.closest?.('a[href^="#"]');
    if (!a) return;
    const id = a.getAttribute("href").slice(1);
    if (byId.has(id)) mark(id);
  });
})();
