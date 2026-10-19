/* Cookie consent: the banner, the cookie settings dialog and the notes of
 * click-to-load content (internal/web/consent.go, shared/consent.html).
 *
 * The server renders the same page for everyone; this script applies the
 * visitor's choice, which it keeps in localStorage (never in a cookie, so
 * the server never sees it) as {v, rev, cats, svc, at}:
 *   - cats: the optional categories accepted, or null before any choice;
 *   - svc: the services allowed with "Always allow …" at a placeholder;
 *   - rev: the version of the choice (the categories offered and the
 *     administrator's revision); another version asks again;
 *   - at: when it was made; after data-days days the banner asks again.
 * Plugin scripts ask window.sapConsent.allowed("youtube" | "analytics" …)
 * before loading anything that needs consent, and sapConsent.on(fn) or the
 * "sap:consent" event tells them when the choice changes.
 *
 * Without a document (the tests, ui/admin/test/consent.test.mjs) the file
 * only exposes its pure helpers as globalThis.sap13nsConsent.
 */
(() => {
  "use strict";

  const VERSION = 1;
  const DAY = 864e5;
  const has = (o, k) => Object.prototype.hasOwnProperty.call(o, k);

  // parse returns the stored choice if it is valid for this version of
  // the site's list (rev) and not older than days, or null. Unknown
  // categories and services are dropped.
  function parse(raw, rev, days, optional, services, now) {
    let c;
    try { c = JSON.parse(raw || "null"); } catch (_) { return null; }
    if (!c || typeof c !== "object" || c.v !== VERSION || c.rev !== rev || typeof c.at !== "string") return null;
    const at = Date.parse(c.at);
    if (!(at > 0) || now - at > days * DAY || at - now > DAY) return null;
    const list = (v, ok) => (Array.isArray(v) ? v.filter((x, i) => typeof x === "string" && ok(x) && v.indexOf(x) === i) : null);
    return {
      cats: list(c.cats, (x) => optional.includes(x)),
      svc: list(c.svc, (x) => has(services, x)) || [],
      at: c.at,
    };
  }

  // serialize is what parse reads back.
  function serialize(choice, rev) {
    return JSON.stringify({ v: VERSION, rev, cats: choice.cats, svc: choice.svc, at: choice.at });
  }

  // allowed reports whether id, a category or a service, may load:
  // necessary always, an optional category once accepted, a service when
  // its category is or the visitor allowed it at a placeholder.
  function allowed(choice, id, optional, services) {
    if (id === "necessary") return true;
    if (optional.includes(id)) return !!(choice.cats && choice.cats.includes(id));
    if (has(services, id)) {
      const cat = services[id];
      return cat === "necessary" || choice.svc.includes(id) || !!(choice.cats && choice.cats.includes(cat));
    }
    return false;
  }

  // decide records a choice of categories: the services of refused
  // categories lose their "Always allow".
  function decide(choice, cats, services, now) {
    return {
      cats: cats.slice(),
      svc: choice.svc.filter((id) => cats.includes(services[id])),
      at: new Date(now).toISOString(),
    };
  }

  // matches reports whether a stored name is the declared one ("name" or
  // "prefix*").
  function matches(declared, name) {
    return declared.endsWith("*") ? name.startsWith(declared.slice(0, -1)) : name === declared;
  }

  const helpers = { VERSION, parse, serialize, allowed, decide, matches };
  if (typeof document === "undefined") {
    globalThis.sap13nsConsent = helpers;
    return;
  }

  const dialog = document.querySelector("dialog[data-consent-dialog]");
  if (!dialog) return;
  const root = document.documentElement;
  const banner = document.querySelector("[data-consent-banner]");
  const key = dialog.dataset.key || "sap13ns-consent";
  const rev = dialog.dataset.rev || "";
  const days = Number(dialog.dataset.days) || 182;
  const fill = (s, vars) => s.replace(/\{(\w+)\}/g, (m, k) => (k in vars ? vars[k] : m));
  const text = (name, vars) => fill(dialog.dataset["t" + name] || "", vars || {});

  // The categories the visitor can switch, and the services, from the list.
  const toggles = [...dialog.querySelectorAll("[data-consent-toggle]")];
  const optional = toggles.map((b) => b.dataset.consentToggle);
  const services = Object.create(null); // id -> category
  const serviceInfo = Object.create(null); // id -> {name, categoryName}
  for (const row of dialog.querySelectorAll("tr[data-service]")) {
    const id = row.dataset.service;
    if (has(services, id)) continue;
    services[id] = row.dataset.category;
    const cat = row.closest("[data-consent-category]");
    serviceInfo[id] = { name: row.dataset.name || id, categoryName: (cat && cat.querySelector("h3") || {}).textContent || "" };
  }

  const storage = () => { try { return window.localStorage; } catch (_) { return null; } };
  let choice = parse((storage() || { getItem: () => null }).getItem(key), rev, days, optional, services, Date.now()) ||
    { cats: null, svc: [], at: null };
  const save = () => {
    if (!choice.at) choice.at = new Date().toISOString();
    try { storage().setItem(key, serialize(choice, rev)); } catch (_) { /* private mode: asked again next time */ }
  };

  // ------------------------------------------------------------ clearing --
  // The site's own optional cookies and storage of a refused category go.
  function clear() {
    for (const row of dialog.querySelectorAll("tr[data-clear]")) {
      if (allowed(choice, row.dataset.category, optional, services)) continue;
      const declared = row.dataset.name || "";
      if (row.dataset.clear === "cookie") {
        for (const part of document.cookie.split(";")) {
          const name = part.split("=")[0].trim();
          if (name && matches(declared, name)) {
            document.cookie = name + "=; Max-Age=0; path=/; SameSite=Lax";
          }
        }
        continue;
      }
      for (const area of ["localStorage", "sessionStorage"]) {
        try {
          const st = window[area];
          for (let i = st.length - 1; i >= 0; i--) {
            const k = st.key(i);
            if (k !== key && matches(declared, k)) st.removeItem(k);
          }
        } catch (_) { /* storage blocked */ }
      }
    }
  }

  // ---------------------------------------------------------------- notes --
  // A placeholder of click-to-load content (data-consent="<service>", or a
  // core video player) whose service is not allowed gets a note after it:
  // who and what it is, "Always allow …" and a link to the settings. A
  // placeholder with its own privacy notice ([data-consent-note] inside)
  // gets only the two controls, at the end of that notice.
  const notes = new Map(); // placeholder -> what was added
  const placeholders = () => document.querySelectorAll("[data-consent], figure.sap-video[data-video-provider]");
  const serviceOf = (el) => el.dataset.consent || el.dataset.videoProvider || "";

  function note(el, id) {
    const info = serviceInfo[id];
    const controls = document.createElement("span");
    controls.className = "sap-consent-controls";
    const label = document.createElement("label");
    const box = document.createElement("input");
    box.type = "checkbox";
    label.append(box, " ", text("Always", { provider: info.name }));
    const link = document.createElement("a");
    link.href = "#sap-consent";
    link.setAttribute("data-consent-open", "");
    link.textContent = text("Settings");
    controls.append(label, " ", link);

    const own = el.querySelector("[data-consent-note]");
    let added = controls;
    if (own) {
      own.append(" ", controls);
    } else {
      added = document.createElement("p");
      added.className = "sap-consent-note";
      const span = document.createElement("span");
      span.textContent = text("Note", { provider: info.name, category: info.categoryName.trim() });
      added.append(span, " ", controls);
      el.after(added);
    }
    box.addEventListener("change", () => {
      choice.svc = choice.svc.filter((x) => x !== id);
      if (box.checked) choice.svc.push(id);
      save();
      apply(added);
    });
    return added;
  }

  // ---------------------------------------------------------------- state --
  const listeners = [];
  function apply(keep) {
    for (const b of toggles) b.setAttribute("aria-checked", String(allowed(choice, b.dataset.consentToggle, optional, services)));
    if (banner) banner.hidden = choice.cats !== null;
    for (const el of placeholders()) {
      const id = serviceOf(el);
      if (!has(services, id)) continue;
      let n = notes.get(el);
      if (allowed(choice, id, optional, services)) {
        if (n && n !== keep) { n.remove(); notes.delete(el); }
      } else if (!n) {
        notes.set(el, note(el, id));
      } else {
        n.querySelector("input").checked = false;
      }
    }
    const state = { categories: (choice.cats || []).slice(), services: choice.svc.slice(), decided: choice.cats !== null };
    for (const fn of listeners) { try { fn(state); } catch (_) { /* a plugin's bug */ } }
    document.dispatchEvent(new CustomEvent("sap:consent", { detail: state }));
  }

  function choose(cats) {
    const fromBanner = banner && banner.contains(document.activeElement);
    choice = decide(choice, cats, services, Date.now());
    save();
    clear();
    apply();
    if (dialog.open) dialog.close();
    if (fromBanner) (document.getElementById("main") || document.body).focus({ preventScroll: true });
  }

  // --------------------------------------------------------------- dialog --
  let opener = null;
  function open(from) {
    if (dialog.open) return;
    opener = from || document.activeElement;
    apply();
    dialog.showModal();
    dialog.querySelector(".sap-consent-close").focus();
  }
  dialog.addEventListener("close", () => {
    if (location.hash === "#sap-consent") history.replaceState(null, "", location.pathname + location.search);
    // Back to what opened it, unless that went with the banner.
    const back = opener && opener.isConnected && opener.getClientRects().length ? opener : document.getElementById("main");
    opener = null;
    if (back) back.focus({ preventScroll: true });
  });
  // Tab stays in the dialog (the page behind is inert, but the browser's
  // own controls are not).
  dialog.addEventListener("keydown", (e) => {
    if (e.key !== "Tab") return;
    const all = [...dialog.querySelectorAll("button:not([disabled]), a[href], summary, input:not([disabled])")].filter((x) => x.getClientRects().length);
    if (!all.length) return;
    const first = all[0], last = all[all.length - 1];
    if (e.shiftKey && document.activeElement === first) { last.focus(); e.preventDefault(); }
    else if (!e.shiftKey && document.activeElement === last) { first.focus(); e.preventDefault(); }
  });
  // A click on the backdrop closes it, as Escape does.
  dialog.addEventListener("click", (e) => { if (e.target === dialog) dialog.close(); });

  document.addEventListener("click", (e) => {
    const el = e.target.closest && e.target.closest("[data-consent-open], a[href='#sap-consent'], [data-consent-accept], [data-consent-reject], [data-consent-save], [data-consent-close], [data-consent-toggle]");
    if (!el) return;
    if (el.matches("[data-consent-toggle]")) {
      el.setAttribute("aria-checked", String(el.getAttribute("aria-checked") !== "true"));
    } else if (el.matches("[data-consent-accept]")) {
      choose(optional);
    } else if (el.matches("[data-consent-reject]")) {
      choose([]);
    } else if (el.matches("[data-consent-save]")) {
      choose(toggles.filter((b) => b.getAttribute("aria-checked") === "true").map((b) => b.dataset.consentToggle));
    } else if (el.matches("[data-consent-close]")) {
      dialog.close();
    } else {
      e.preventDefault();
      open(el);
    }
  });
  addEventListener("hashchange", () => { if (location.hash === "#sap-consent") open(); });

  // Out of the footer: no theme style or stacking context reaches them, and
  // the banner comes first in the reading order.
  if (banner) document.body.prepend(banner);
  document.body.append(dialog);
  root.classList.add("sap-consent-ready");

  window.sapConsent = {
    allowed: (id) => allowed(choice, id, optional, services),
    get decided() { return choice.cats !== null; },
    open: () => open(),
    on: (fn) => { if (typeof fn === "function") listeners.push(fn); },
  };
  clear();
  apply();
  if (location.hash === "#sap-consent") open();
})();
