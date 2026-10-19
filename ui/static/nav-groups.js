// Collapsible groups of the admin sidebar (Content, Appearance,
// Administration, Network…). The heading of each group is a button with
// aria-expanded; the groups a viewer folded are remembered in this browser's
// localStorage, a convenience that may fail (private windows, blocked
// storage) without breaking anything. The group of the current page always
// opens on load. Without JavaScript, or storage, every group is open.
//
// A classic script loaded right after the sidebar, not deferred, so that the
// folded groups are closed before the first paint.
(() => {
  "use strict";

  const KEY = "adminNavCollapsed";

  // readCollapsed returns the ids of the groups stored as folded: [] when
  // the storage is missing, throws or holds anything else.
  function readCollapsed(storage) {
    try {
      const v = JSON.parse(storage.getItem(KEY) || "[]");
      return Array.isArray(v) ? v.filter((id) => typeof id === "string") : [];
    } catch (_) {
      return [];
    }
  }

  // writeCollapsed stores the folded groups, and reports whether it could.
  function writeCollapsed(storage, ids) {
    try {
      storage.setItem(KEY, JSON.stringify([...new Set(ids)].sort()));
      return true;
    } catch (_) {
      return false;
    }
  }

  // initialCollapsed is the set of groups to fold on load: the stored ones
  // that are on the page, except the group of the current page.
  function initialCollapsed(groups, stored, current) {
    return groups.filter((id) => id !== current && stored.includes(id));
  }

  // toggled is the stored list after the viewer folds (collapsed) or opens
  // the group id.
  function toggled(stored, id, collapsed) {
    const rest = stored.filter((x) => x !== id);
    return collapsed ? [...rest, id] : rest;
  }

  if (typeof document === "undefined") {
    // Under test (ui/admin/test/nav-groups.test.mjs).
    globalThis.sap13nsNavGroups = { KEY, readCollapsed, writeCollapsed, initialCollapsed, toggled };
    return;
  }

  let storage = null;
  try { storage = window.localStorage; } catch (_) {}

  const groups = Array.from(document.querySelectorAll(".sidebar [data-nav-group]"));
  if (!groups.length) return;
  const nav = groups[0].closest("nav");
  const button = (g) => g.querySelector(":scope > h3 > .nav-group-toggle");
  const fold = (g, collapsed) => {
    g.toggleAttribute("data-collapsed", collapsed);
    button(g)?.setAttribute("aria-expanded", String(!collapsed));
  };

  const current = groups.find((g) => g.querySelector('[aria-current="page"]'))?.dataset.navGroup || "";
  const folded = initialCollapsed(groups.map((g) => g.dataset.navGroup), readCollapsed(storage), current);
  if (folded.length) {
    nav?.setAttribute("data-nav-instant", "");
    for (const g of groups) fold(g, folded.includes(g.dataset.navGroup));
    requestAnimationFrame(() => requestAnimationFrame(() => nav?.removeAttribute("data-nav-instant")));
  }

  for (const g of groups) {
    button(g)?.addEventListener("click", () => {
      const collapsed = !g.hasAttribute("data-collapsed");
      fold(g, collapsed);
      if (storage) writeCollapsed(storage, toggled(readCollapsed(storage), g.dataset.navGroup, collapsed));
    });
  }
})();
