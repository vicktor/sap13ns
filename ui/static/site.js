// sap13ns public site: colour scheme toggle, mobile navigation, language switch,
// comment replies and copy-link. Tiny, dependency-free and CSP-safe (no inline handlers).
(() => {
  "use strict";
  const root = document.documentElement;

  document.addEventListener("click", (e) => {
    // Close an open language switch when clicking elsewhere.
    for (const d of document.querySelectorAll("details.lang-switch[open]")) if (!d.contains(e.target)) d.open = false;
    const el = e.target.closest("[data-theme-toggle],[data-nav-toggle],[data-reply-to],[data-reply-cancel],[data-copy-url]");
    if (!el) return;

    if (el.hasAttribute("data-theme-toggle")) {
      const dark = !root.classList.contains("dark");
      root.classList.toggle("dark", dark);
      try { localStorage.setItem("themeMode", dark ? "dark" : "light"); } catch (_) {}
      return;
    }

    if (el.dataset.navToggle) {
      const nav = document.getElementById(el.dataset.navToggle);
      const open = el.getAttribute("aria-expanded") !== "true";
      el.setAttribute("aria-expanded", String(open));
      nav?.classList.toggle("is-open", open);
      root.classList.toggle("nav-open", open);
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

  // Close the language switch or the mobile menu with Escape.
  document.addEventListener("keydown", (e) => {
    if (e.key !== "Escape") return;
    const lang = document.querySelector("details.lang-switch[open]");
    if (lang) { lang.open = false; lang.querySelector("summary").focus(); return; }
    const btn = document.querySelector('[data-nav-toggle][aria-expanded="true"]');
    if (btn) { btn.click(); btn.focus(); }
  });

  // Follow the OS preference live unless the visitor chose a mode.
  try {
    const mq = matchMedia("(prefers-color-scheme: dark)");
    mq.addEventListener("change", (ev) => { if (!localStorage.getItem("themeMode")) root.classList.toggle("dark", ev.matches); });
  } catch (_) {}
})();
