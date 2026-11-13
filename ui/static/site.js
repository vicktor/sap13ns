// sap13ns public site: colour scheme toggle, mobile navigation, language switch,
// comment replies and copy-link. Tiny, dependency-free and CSP-safe (no inline handlers).
(() => {
  "use strict";
  const root = document.documentElement;

  // ------------------------------------------------------ mobile menu --
  // A [data-nav-toggle] button opens the element it names (aria-controls)
  // by giving it .is-open, and <html> .nav-open while any menu is open.
  const openToggles = () => document.querySelectorAll('[data-nav-toggle][aria-expanded="true"]');
  const navOf = (btn) => document.getElementById(btn.dataset.navToggle);
  const setNav = (btn, open) => {
    btn.setAttribute("aria-expanded", String(open));
    navOf(btn)?.classList.toggle("is-open", open);
    root.classList.toggle("nav-open", openToggles().length > 0);
  };
  // closeNavs closes every open menu; with refocus, focus returns to its toggle.
  const closeNavs = (refocus) => {
    for (const btn of openToggles()) {
      setNav(btn, false);
      if (refocus) btn.focus();
    }
  };

  document.addEventListener("click", (e) => {
    // Close an open language switch when clicking elsewhere.
    for (const d of document.querySelectorAll("details.lang-switch[open]")) if (!d.contains(e.target)) d.open = false;
    // A language chosen in the switch is remembered, so that the home page
    // opens in it next time instead of the browser's language (lang.go).
    const pick = e.target.closest(".lang-switch a[hreflang]");
    if (pick && /^[a-z]{2,3}(-[a-z0-9]{2,8})?$/.test(pick.hreflang)) {
      const secure = location.protocol === "https:" ? "; Secure" : "";
      document.cookie = "sap13ns-lang=" + pick.hreflang + "; Path=/; Max-Age=31536000; SameSite=Lax" + secure;
    }
    // A click outside an open menu (the dark scrim, the page) closes it, and
    // so does following one of its links (in-page anchors included).
    for (const btn of openToggles()) {
      const nav = navOf(btn);
      if (btn.contains(e.target)) continue;
      if (!nav?.contains(e.target) || e.target.closest("a[href]")) setNav(btn, false);
    }
    // A link with data-popup opens in a small window of its own (a call, a
    // booking page), centred on the screen: "460x740" by default, or the size
    // it gives. The link
    // stays a plain link when the window cannot open, with a modifier key
    // or in the design preview.
    const pop = e.target.closest("a[data-popup][href]");
    if (pop && !e.defaultPrevented && e.button === 0 && !(e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) && /^https?:$/.test(pop.protocol)) {
      const m = /^(\d{3,4})x(\d{3,4})$/.exec(pop.dataset.popup || "");
      const w = Math.min(m ? +m[1] : 460, screen.availWidth || 460), h = Math.min(m ? +m[2] : 740, screen.availHeight || 740);
      // In the middle of the screen the page is on.
      const left = Math.round((screen.availLeft || 0) + Math.max(0, ((screen.availWidth || w) - w) / 2));
      const top = Math.round((screen.availTop || 0) + Math.max(0, ((screen.availHeight || h) - h) / 2));
      const win = window.open(pop.href, "_blank", `popup=yes,width=${w},height=${h},left=${left},top=${top},resizable=yes,scrollbars=yes`);
      if (win) {
        e.preventDefault();
        try { win.opener = null; } catch (_) { /* another origin already */ }
      }
      return;
    }
    const el = e.target.closest("[data-theme-toggle],[data-nav-toggle],[data-reply-to],[data-reply-cancel],[data-copy-url]");
    if (!el) return;

    if (el.hasAttribute("data-theme-toggle")) {
      const dark = !root.classList.contains("dark");
      root.classList.toggle("dark", dark);
      window.sap13nsTheme?.save(dark ? "dark" : "light");
      return;
    }

    if (el.dataset.navToggle) {
      setNav(el, el.getAttribute("aria-expanded") !== "true");
      return;
    }

    if (el.dataset.replyTo) {
      const form = document.querySelector(".comment-form");
      if (!form) return;
      form.querySelector("[data-reply-parent]").value = el.dataset.replyTo;
      const banner = form.querySelector("[data-reply-banner]");
      banner.querySelector("[data-reply-label]").textContent = el.dataset.replyName || "";
      banner.hidden = false;
      el.closest("li").after(document.getElementById("respond"));
      form.querySelector("textarea")?.focus();
      return;
    }

    if (el.hasAttribute("data-reply-cancel")) {
      const respond = document.getElementById("respond");
      const form = respond.querySelector(".comment-form");
      form.querySelector("[data-reply-parent]").value = "";
      form.querySelector("[data-reply-banner]").hidden = true;
      document.getElementById("comments")?.append(respond);
      return;
    }

    if (el.dataset.copyUrl) {
      navigator.clipboard?.writeText(el.dataset.copyUrl).then(() => {
        el.classList.add("is-copied");
        setTimeout(() => el.classList.remove("is-copied"), 1600);
      });
    }
  });

  // Close the language switch or the mobile menu with Escape; focus returns
  // to the control that opened it.
  document.addEventListener("keydown", (e) => {
    if (e.key !== "Escape") return;
    const lang = document.querySelector("details.lang-switch[open]");
    if (lang) { lang.open = false; lang.querySelector("summary").focus(); return; }
    closeNavs(true);
  });

  // Growing to the desktop layout hides the toggle (each theme picks its own
  // breakpoint): close the menu, so that it is not open, or the page scroll
  // locked, when the window narrows again.
  let resizing = 0;
  addEventListener("resize", () => {
    cancelAnimationFrame(resizing);
    resizing = requestAnimationFrame(() => {
      for (const btn of openToggles()) if (!btn.getClientRects().length) setNav(btn, false);
    });
  });
  // Back/forward cache: a page left with its menu open comes back closed.
  addEventListener("pageshow", (e) => { if (e.persisted) closeNavs(false); });
  addEventListener("hashchange", () => closeNavs(false));

  // ----------------------------------------------------- theme preview --
  // An administrator's ?preview_theme= preview (the server adds the meta tag
  // only then) keeps the theme on the site's own pages and searches.
  const preview = document.querySelector('meta[name="sap-preview-theme"]')?.content;
  if (preview) {
    const PRIVATE = /^\/(admin|login|logout|register|setup|uploads|static|themes|plugin-assets|api)(\/|$)/;
    const keep = (u) => u.origin === location.origin && !PRIVATE.test(u.pathname) &&
      !(/\.[a-z0-9]{1,8}$/i.test(u.pathname) && !/\.html?$/i.test(u.pathname));
    for (const a of document.querySelectorAll("a[href]")) {
      let u;
      try { u = new URL(a.getAttribute("href"), location.href); } catch (_) { continue; }
      if (!keep(u) || u.searchParams.has("preview_theme")) continue;
      u.searchParams.set("preview_theme", preview);
      a.setAttribute("href", u.pathname + u.search + u.hash);
    }
    document.addEventListener("submit", (e) => {
      const f = e.target;
      if (!(f instanceof HTMLFormElement) || (f.method || "get").toLowerCase() !== "get") return;
      if (!keep(new URL(f.action || location.href, location.href)) || f.querySelector('input[name="preview_theme"]')) return;
      const input = document.createElement("input");
      input.type = "hidden"; input.name = "preview_theme"; input.value = preview;
      f.append(input);
    });
  }

  // Follow the OS preference live unless the visitor chose a mode.
  try {
    const mq = matchMedia("(prefers-color-scheme: dark)");
    mq.addEventListener("change", (ev) => { if (!localStorage.getItem("themeMode")) root.classList.toggle("dark", ev.matches); });
  } catch (_) {}
})();
