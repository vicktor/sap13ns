// Theme customiser (Appearance → Customise). The form on the left is the
// design; every change is saved as the theme's draft (POST …/draft, JSON)
// and the preview iframe shows it: colour, font and size changes swap the
// draft stylesheet's <link href> in place, structural ones (logo, header
// links, home layout, options, footer text) reload the frame and keep its
// scroll position. Nothing reaches visitors until Publish.
//
// The pure helpers are exported for ui/admin/test/customizer.test.mjs.

const HEX = /^#?([0-9a-f]{3}|[0-9a-f]{6})$/i;

/** normalizeHex("#ABC") → "#aabbcc"; "" when v is not a hex colour. */
export function normalizeHex(v) {
  const m = HEX.exec(String(v ?? "").trim());
  if (!m) return "";
  let h = m[1].toLowerCase();
  if (h.length === 3) h = h.replace(/./g, "$&$&");
  return "#" + h;
}

function setPath(obj, path, value) {
  const keys = path.split(".");
  let o = obj;
  for (const k of keys.slice(0, -1)) o = o[k] ??= {};
  o[keys[keys.length - 1]] = value;
}

/**
 * designFrom builds the design JSON from form fields: [{name, value, type,
 * checked, kind, def}] where type is the data-type ("int", "bool",
 * "boolstr"), kind the input type ("checkbox", "radio", …) and def a
 * theme text's default. Theme options are only kept when they differ from
 * the stored ones, so publishing never undoes an edit made meanwhile on the
 * theme's options page; theme texts only when they differ from their
 * default, so the theme's own translations keep showing until edited.
 */
export function designFrom(fields, storedOptions = {}) {
  const d = { version: 1, light: {}, dark: {}, fonts: { body: {}, heading: {}, mono: {} }, scale: {}, header: {}, footer: {}, home: {}, options: {}, content: {}, content_i18n: {} };
  for (const f of fields) {
    if (!f.name) continue;
    if (f.kind === "radio" && !f.checked) continue;
    let v = f.value ?? "";
    switch (f.type) {
      case "int": v = parseInt(v, 10) || 0; break;
      case "bool": v = !!f.checked; break;
      case "boolstr": v = f.checked ? "true" : "false"; break;
      default: v = String(v);
    }
    const [head] = f.name.split(".");
    if (head === "light" || head === "dark") {
      v = normalizeHex(v);
      if (!v) continue;
    } else if (head === "options") {
      const key = f.name.slice("options.".length);
      if ((storedOptions[key] ?? "") === v) continue;
    } else if (f.name.startsWith("footer.text")) {
      v = v.trim();
      if (!v) continue;
    } else if (head === "content" || head === "content_i18n") {
      v = v.trim();
      if (!v || v === String(f.def ?? "").trim()) continue;
    }
    setPath(d, f.name, v);
  }
  for (const role of ["body", "heading", "mono"]) {
    const font = d.fonts[role];
    if (!font.stack) d.fonts[role] = {};
    else if (font.stack !== "media") delete font.media;
  }
  if (!d.header.brand) delete d.header.logo;
  return d;
}

// Theme texts the preview shows as they are typed (live keys) are left
// out; the other ones (images, lists) need the page again.
function staticContent(m, live) {
  const out = {};
  for (const [k, v] of Object.entries(m || {})) if (!live.has(k)) out[k] = v;
  return out;
}

/** The parts of a design that the stylesheet alone cannot show. */
function structural(d, live) {
  const { brand, logo, hide_search, hide_lang } = d.header ?? {};
  const i18n = {};
  for (const [lang, m] of Object.entries(d.content_i18n || {})) {
    const rest = staticContent(m, live);
    if (Object.keys(rest).length) i18n[lang] = rest;
  }
  return JSON.stringify([brand || "", brand ? logo || 0 : 0, !!hide_search, !!hide_lang, d.footer?.text || "", d.footer?.text_i18n || {}, d.home || {}, d.options || {},
    staticContent(d.content, live), i18n]);
}

/**
 * needsReload reports whether the preview must reload to show next. live
 * holds the keys of the theme texts that the preview already shows as they
 * are typed.
 */
export function needsReload(prev, next, live = new Set()) {
  return structural(prev, live) !== structural(next, live);
}

/** liveChanges lists the live texts that differ between two designs, as [lang, key]. */
export function liveChanges(prev, next, live, main) {
  const out = [];
  const langs = new Set([...Object.keys(prev.content_i18n || {}), ...Object.keys(next.content_i18n || {})]);
  for (const k of live) {
    if ((prev.content?.[k] ?? "") !== (next.content?.[k] ?? "")) out.push([main, k]);
    for (const l of langs) if ((prev.content_i18n?.[l]?.[k] ?? "") !== (next.content_i18n?.[l]?.[k] ?? "")) out.push([l, k]);
  }
  return out;
}

const PRIVATE = /^\/(admin|login|logout|register|setup)(\/|$)/;

/**
 * previewURL turns a link clicked inside the preview into the URL to load
 * in the frame (keeping the theme, the draft and the colour scheme), or
 * null when it must not load there: other sites, the admin, sign-in, and
 * files such as feeds and uploads, which cannot be framed.
 */
export function previewURL(href, origin, slug, scheme) {
  let u;
  try { u = new URL(href, origin); } catch (_) { return null; }
  if (u.origin !== origin || PRIVATE.test(u.pathname)) return null;
  if (/\.[a-z0-9]{1,8}$/i.test(u.pathname) && !/\.html?$/i.test(u.pathname)) return null;
  if (u.pathname.startsWith("/uploads/")) return null;
  u.searchParams.set("preview_theme", slug);
  u.searchParams.set("customize", "draft");
  if (scheme) u.searchParams.set("scheme", scheme);
  return u.pathname + u.search + u.hash;
}

/** showIf evaluates a data-show-if rule ("name=value", "name!=value"). */
export function showIf(rule, values) {
  const m = /^([^!=]+)(!?=)(.*)$/.exec(rule || "");
  if (!m) return true;
  const v = values[m[1]] ?? "";
  return m[2] === "=" ? v === m[3] : v !== m[3];
}

const fmt = (s, ...args) => { let i = 0; return String(s ?? "").replace(/%[sd]/g, () => String(args[i++] ?? "")); };

// --------------------------------------------------------------- browser --

function init(root) {
  const $ = (sel, el = root) => el.querySelector(sel);
  const $$ = (sel, el = root) => [...el.querySelectorAll(sel)];
  const msg = root.dataset;
  const state = JSON.parse(msg.state || "{}");
  const base = msg.base;
  const slug = msg.slug;
  const form = $("#cz-form");
  const frame = $("#cz-frame");
  const status = document.getElementById("cz-status");
  const publishBtn = document.getElementById("cz-publish");
  const discardBtn = document.getElementById("cz-discard");
  const csrf = () => document.querySelector("input[name=csrf]")?.value || "";

  let defaults = { light: state.light || {}, dark: state.dark || {} };
  let scheme = document.documentElement.classList.contains("dark") ? "dark" : "light";
  let device = "desktop";

  // ------------------------------------------------------------ form --
  const fields = () => [...form.elements].filter((el) => el.name && !el.disabled).map((el) => ({
    name: el.name, value: el.value, type: el.dataset.type, checked: el.checked, kind: el.type, def: el.dataset.default,
  }));
  // The theme texts the preview shows as they are typed.
  const live = new Set($$("[data-live]", form).map((el) => el.dataset.live));
  const mainLang = $("[data-content-lang]")?.dataset.contentLang || document.documentElement.lang;
  const current = () => designFrom(fields(), state.options || {});
  const values = () => {
    const out = {};
    for (const el of form.elements) {
      if (!el.name || ((el.type === "radio" || el.type === "checkbox") && !el.checked)) continue;
      out[el.name] = el.value;
    }
    return out;
  };
  const field = (name) => form.elements.namedItem(name);

  function updateVisibility() {
    const v = values();
    for (const el of $$("[data-show-if]", form)) el.hidden = !showIf(el.dataset.showIf, v);
  }

  function updateSamples() {
    for (const sel of $$("[data-font-sample]", form)) {
      const sample = document.getElementById(sel.dataset.fontSample);
      if (!sample) continue;
      const stack = sel.value === "body" ? state.fonts?.[field("fonts.body.stack")?.value] : state.fonts?.[sel.value];
      // CSSOM, not a style attribute: allowed by the admin's CSP.
      sample.style.fontFamily = stack || "";
    }
  }

  // Colour rows: the picker and the hex field edit the same value; an
  // empty hex field means the theme's colour, which the picker then shows.
  function syncColor(name, from) {
    const text = field(name);
    const pick = $(`[data-pick="${name}"]`, form);
    const clear = $(`[data-clear="${name}"]`, form);
    if (!text || !pick) return;
    const [sch, tok] = name.split(".");
    if (from === "pick") text.value = pick.value;
    const hex = normalizeHex(text.value);
    pick.value = hex || normalizeHex(defaults[sch]?.[tok]) || "#000000";
    text.placeholder = defaults[sch]?.[tok] || "";
    clear.hidden = !text.value;
    const bad = text.value !== "" && !hex;
    text.setAttribute("aria-invalid", String(bad));
  }
  const syncAllColors = () => { for (const el of $$(".cz-hex", form)) syncColor(el.name); };

  // ------------------------------------------------------ messages --
  function setStatus(text, kind = "") {
    status.textContent = text;
    if (kind) status.dataset.kind = kind; else delete status.dataset.kind;
  }

  function showErrors(errors = []) {
    for (const el of $$(".cz-field-error", form)) el.remove();
    for (const el of $$("[aria-invalid=true]", form)) if (!el.classList.contains("cz-hex")) el.removeAttribute("aria-invalid");
    let first = null;
    for (const e of errors) {
      let el = field(e.field);
      if (el instanceof RadioNodeList) el = el[0];
      // fonts.body → its select; header.logo → the logo grid.
      el ??= field(e.field + ".stack") || $(`[name^="${e.field}."]`, form);
      // A select's hidden input: mark and focus its trigger instead.
      if (el?.type === "hidden") el = el.parentElement?.querySelector(":scope > button") || null;
      if (!el) continue;
      el.setAttribute("aria-invalid", "true");
      const p = document.createElement("p");
      p.className = "cz-field-error";
      p.id = "cz-err-" + e.field.replace(/[^\w-]/g, "-");
      p.textContent = e.message;
      const host = el.closest(".field, .cz-color, fieldset") || el.parentElement;
      host.after(p);
      el.setAttribute("aria-describedby", p.id);
      const details = el.closest("details");
      if (details) details.open = true;
      first ??= el;
    }
    return first;
  }

  function showWarnings(warnings = []) {
    const box = $("#cz-warnings");
    for (const row of $$(".cz-color.is-warned", form)) row.classList.remove("is-warned");
    box.replaceChildren();
    if (!warnings.length) {
      box.dataset.ok = "";
      box.textContent = msg.msgNoWarnings;
      return;
    }
    delete box.dataset.ok;
    const ul = document.createElement("ul");
    const label = (s, tok) => $(`[data-color="${s}.${tok}"] > label`, form)?.textContent || tok;
    for (const w of warnings) {
      const [fg, bg] = w.pair.split("/");
      const li = document.createElement("li");
      li.textContent = `${w.scheme === "dark" ? "☾" : "☀"} ` + fmt(msg.msgContrast, label(w.scheme, fg), label(w.scheme, bg), w.ratio.toFixed(2), w.minimum);
      ul.append(li);
      for (const tok of [fg, bg]) $(`[data-color="${w.scheme}.${tok}"]`, form)?.classList.add("is-warned");
    }
    box.append(ul);
  }

  // --------------------------------------------------------- preview --
  const frameDoc = () => { try { return frame.contentDocument; } catch (_) { return null; } };
  let reloadScroll = null;

  function applyScheme() {
    const doc = frameDoc();
    doc?.documentElement.classList.toggle("dark", scheme === "dark");
    for (const b of $$("[data-scheme]")) b.setAttribute("aria-pressed", String(b.dataset.scheme === scheme));
    const open = $("[data-open-preview]");
    const href = previewURL(frame.contentWindow?.location.href || frame.src, location.origin, slug, scheme);
    if (open && href) open.href = href;
  }

  function reloadFrame() {
    const win = frame.contentWindow;
    reloadScroll = win ? win.scrollY : 0;
    $(".cz-frame").dataset.loading = "";
    try { win.location.reload(); } catch (_) { frame.src = frame.src; }
  }

  // Swap the draft stylesheet without reloading: load the new one first,
  // then drop the old, so the page never shows unstyled.
  function swapCSS(url, classes) {
    const doc = frameDoc();
    const old = doc?.querySelector("link[data-sap-custom]");
    if (!old) return reloadFrame();
    const html = doc.documentElement;
    for (const c of [...html.classList]) if (/^sap-(sticky-|brand-center$)/.test(c)) html.classList.remove(c);
    for (const c of (classes || "").split(/\s+/)) if (c && c !== "sap-has-logo") html.classList.add(c);
    // The URL is a hash of the stylesheet: the same URL, the same rules.
    if (old.getAttribute("href") === url) return;
    const link = old.cloneNode();
    link.href = url;
    link.addEventListener("load", () => { for (const l of doc.querySelectorAll("link[data-sap-custom]")) if (l !== link) l.remove(); }, { once: true });
    link.addEventListener("error", () => link.remove(), { once: true });
    old.after(link);
  }

  // ------------------------------------------- texts and images --
  let editMode = true;
  let textLang = mainLang;
  const content = $("[data-cz-content]", form);

  // showLang shows the fields of one language; with go, the preview moves
  // to the same page in that language (or its home page).
  function showLang(lang, go) {
    if (!content) return;
    textLang = lang;
    for (const el of $$("[data-content-field][data-lang]", content)) el.hidden = el.dataset.lang !== lang;
    for (const b of $$("[data-content-lang]", content)) b.setAttribute("aria-pressed", String(b.dataset.contentLang === lang));
    const doc = frameDoc();
    if (!go || !doc || doc.documentElement.lang === lang) return;
    const alt = doc.querySelector(`link[rel="alternate"][hreflang="${lang}"]`)?.href || (lang === mainLang ? "/" : `/${lang}/`);
    const url = previewURL(alt, location.origin, slug, scheme);
    if (url) frame.contentWindow.location.assign(url);
  }

  // openField opens the field of key in lang and takes the focus there.
  function openField(key, lang) {
    if (!content) return;
    let el = $(`[data-content-field="${key}"][data-lang="${lang}"]`, content);
    if (el && lang !== textLang) showLang(lang, false);
    el ??= $(`[data-content-field="${key}"]:not([data-lang])`, content) || $(`[data-content-field="${key}"]`, content);
    if (!el) return;
    content.open = true;
    const group = el.closest("details.cz-group");
    if (group) group.open = true;
    el.scrollIntoView({ block: "center", behavior: "smooth" });
    (el.querySelector("input:not([type=hidden]), textarea, button"))?.focus({ preventScroll: true });
    el.classList.add("is-target");
    setTimeout(() => el.classList.remove("is-target"), 1600);
  }

  // showText writes a text in the preview as it is typed.
  function showText(input) {
    const doc = frameDoc();
    if (!doc || doc.documentElement.lang !== input.lang) return;
    for (const el of doc.querySelectorAll(`[data-sap-edit="${input.dataset.live}"]`)) el.textContent = input.value.trim() || input.dataset.default || "";
  }

  // In the preview, the marked texts and images open their field when
  // clicked (and show it on hover), unless the edit mode is off.
  function markEditable(doc) {
    const label = (key) => $(`[data-content-field="${key}"] label, [data-content-field="${key}"] .label`, content)?.textContent.trim() || key;
    doc.addEventListener("click", (e) => {
      const el = editMode && e.target.closest?.("[data-sap-edit]");
      if (!el) return;
      e.preventDefault();
      e.stopImmediatePropagation();
      openField(el.dataset.sapEdit, doc.documentElement.lang);
    }, true);
    doc.addEventListener("mouseover", (e) => {
      const el = editMode && e.target.closest?.("[data-sap-edit]");
      if (!el || el.dataset.sapHover) return;
      el.dataset.sapHover = "";
      // CSSOM, not a style attribute: the site's CSP allows it.
      el.style.outline = "2px dashed #3b82f6";
      el.style.outlineOffset = "4px";
      el.style.cursor = "pointer";
      el.title = fmt(msg.msgEdit, label(el.dataset.sapEdit));
      el.addEventListener("mouseleave", () => {
        delete el.dataset.sapHover;
        el.style.outline = el.style.outlineOffset = el.style.cursor = "";
        el.removeAttribute("title");
      }, { once: true });
    });
  }

  if (content) {
    for (const b of $$("[data-content-lang]", content)) b.addEventListener("click", () => showLang(b.dataset.contentLang, true));
    // An image from the media library (admin.js's picker), or the theme's.
    content.addEventListener("sap13ns:media-pick", (e) => {
      const box = e.target.closest("[data-content-field]");
      if (!box || !e.detail) return;
      $("[data-content-image]", box).value = String(e.detail.id);
      $(".cz-image-thumb", box).src = e.detail.thumb || e.detail.url;
      $("[data-theme-image]", box).hidden = false;
      schedule(100);
    });
    content.addEventListener("click", (e) => {
      const b = e.target.closest("[data-theme-image]");
      if (!b) return;
      const box = b.closest("[data-content-field]");
      $("[data-content-image]", box).value = "";
      $(".cz-image-thumb", box).src = $(".cz-image-thumb", box).dataset.themeSrc;
      b.hidden = true;
      $("[data-media-picker]", box)?.focus();
      schedule(100);
    });
  }
  const editBtn = $("[data-edit-mode]");
  editBtn?.addEventListener("click", () => {
    editMode = !editMode;
    editBtn.setAttribute("aria-pressed", String(editMode));
  });

  frame.addEventListener("load", () => {
    delete $(".cz-frame").dataset.loading;
    const doc = frameDoc();
    if (!doc) return;
    applyScheme();
    if (content) {
      markEditable(doc);
      // The preview went to another language: edit its texts.
      const lang = doc.documentElement.lang;
      if (lang && lang !== textLang && $(`[data-content-lang="${lang}"]`, content)) showLang(lang, false);
    }
    if (reloadScroll != null) {
      frame.contentWindow.scrollTo({ top: reloadScroll, behavior: "instant" });
      reloadScroll = null;
    }
    // Keep navigation inside the preview on the draft.
    doc.addEventListener("click", (e) => {
      if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
      const a = e.target.closest?.("a[href]");
      if (!a || a.getAttribute("href").startsWith("#")) return;
      e.preventDefault();
      const url = previewURL(a.href, location.origin, slug, scheme);
      if (url) { frame.contentWindow.location.assign(url); return; }
      const target = new URL(a.href, location.href);
      if (target.origin === location.origin && PRIVATE.test(target.pathname)) return;
      // Other sites and files (feeds, uploads) open in a new tab.
      window.open(a.href, "_blank", "noopener");
      if (target.origin !== location.origin) toast("info", msg.msgExternal);
    });
    doc.addEventListener("submit", (e) => {
      const f = e.target;
      if ((f.method || "get").toLowerCase() !== "get") {
        e.preventDefault();
        toast("info", msg.msgBlocked);
        return;
      }
      for (const [k, v] of [["preview_theme", slug], ["customize", "draft"], ["scheme", scheme]]) {
        let input = f.querySelector(`input[type=hidden][name="${k}"]`);
        if (!input) { input = doc.createElement("input"); input.type = "hidden"; input.name = k; f.append(input); }
        input.value = v;
      }
    });
  });

  for (const b of $$("[data-device]")) b.addEventListener("click", () => {
    device = b.dataset.device;
    $(".cz-frame").dataset.device = device;
    for (const x of $$("[data-device]")) x.setAttribute("aria-pressed", String(x === b));
  });
  for (const b of $$("[data-scheme]")) b.addEventListener("click", () => { scheme = b.dataset.scheme; applyScheme(); });
  $("[data-reload]")?.addEventListener("click", reloadFrame);
  // Editing the dark colours? Show them.
  for (const tab of $$("[data-scheme-tab]")) tab.addEventListener("click", () => {
    if (scheme !== tab.dataset.schemeTab) { scheme = tab.dataset.schemeTab; applyScheme(); }
  });

  // ------------------------------------------------------------ saving --
  let saved = current();       // what the server has
  let shown = saved;           // what the preview shows
  let timer = 0;               // a scheduled save
  let inflight = null;         // the running save, a promise
  let again = false;           // changes arrived during it
  let failed = false;          // the last save was refused
  let okStatus = status.textContent;

  const esc = (t) => String(t).replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);
  function toast(category, title) {
    document.dispatchEvent(new CustomEvent("basecoat:toast", { detail: { config: { category, title: esc(title) } } }));
  }

  async function post(path, body) {
    const res = await fetch(base + path, {
      method: "POST", credentials: "same-origin",
      headers: { "Content-Type": "application/json", Accept: "application/json", "X-CSRF-Token": csrf() },
      body: JSON.stringify(body),
    });
    let data = {};
    try { data = await res.json(); } catch (_) {}
    return { res, data };
  }

  const unsaved = () => JSON.stringify(current()) !== JSON.stringify(saved);

  async function save() {
    clearTimeout(timer);
    timer = 0;
    if (inflight) { again = true; return inflight; }
    const design = current();
    if (!unsaved()) {
      // Back to what the server has (say, an invalid edit was undone).
      if (failed) { failed = false; showErrors([]); setStatus(okStatus); }
      return;
    }
    setStatus(msg.msgSaving, "saving");
    inflight = (async () => {
      try {
        const { res, data } = await post("/draft", design);
        if (!res.ok || !data.ok) {
          failed = true;
          const first = showErrors(data.errors);
          const general = data.errors?.find((e) => !e.field);
          setStatus(fmt(msg.msgFailed, general?.message || (data.errors?.length ? msg.msgInvalid : data.message || res.status)), "error");
          if (first && !form.contains(document.activeElement)) first.focus();
          return;
        }
        failed = false;
        showErrors([]);
        showWarnings(data.warnings);
        if (data.light) defaults = { light: data.light, dark: data.dark };
        syncAllColors();
        saved = design;
        okStatus = fmt(msg.msgSaved, data.saved_at);
        setStatus(okStatus);
        publishBtn.disabled = false;
        discardBtn.disabled = false;
        // A live text the page shows nowhere marked (an alt text, say)
        // needs the page again too.
        const doc = frameDoc();
        const unseen = liveChanges(shown, design, live, mainLang).some(([l, k]) => doc?.documentElement.lang === l && !doc.querySelector(`[data-sap-edit="${k}"]`));
        if (unseen || needsReload(shown, design, live)) reloadFrame();
        else swapCSS(data.css_url, data.classes);
        shown = design;
      } catch (_) {
        failed = true;
        setStatus(fmt(msg.msgFailed, msg.msgOffline), "error");
      }
    })();
    try { await inflight; } finally { inflight = null; }
    if (again) { again = false; return save(); }
  }

  function schedule(delay) {
    clearTimeout(timer);
    timer = setTimeout(save, delay);
  }

  form.addEventListener("input", (e) => {
    const el = e.target;
    if (el.dataset.pick) syncColor(el.dataset.pick, "pick");
    else if (el.classList.contains("cz-hex")) syncColor(el.name);
    if (el.dataset.fontSample || el.name === "fonts.body.stack") updateSamples();
    if (el.dataset.live) showText(el);
    updateVisibility();
    // Typing waits for a pause; pickers and selects save quickly.
    schedule(el.tagName === "TEXTAREA" || el.type === "text" || el.type === "number" ? 700 : 250);
  });
  form.addEventListener("change", () => schedule(150));
  form.addEventListener("submit", (e) => { e.preventDefault(); save(); });

  form.addEventListener("click", async (e) => {
    const clear = e.target.closest("[data-clear]");
    if (clear) {
      const name = clear.dataset.clear;
      field(name).value = "";
      syncColor(name);
      field(name).focus();
      schedule(100);
      return;
    }
    if (e.target.closest("[data-derive]")) {
      const btn = e.target.closest("[data-derive]");
      btn.disabled = true;
      try {
        const { res, data } = await post("/derive", current());
        if (!res.ok || !data.ok) { showErrors(data.errors); toast("error", data.message || String(res.status)); return; }
        for (const [tok, hex] of Object.entries(data.dark || {})) {
          const el = field("dark." + tok);
          if (el) { el.value = hex; syncColor("dark." + tok); }
        }
        if (scheme !== "dark") { scheme = "dark"; applyScheme(); }
        toast("success", msg.msgDerived);
        save();
      } catch (_) {
        toast("error", msg.msgOffline);
      } finally {
        btn.disabled = false;
      }
    }
  });

  // Publish only what the preview shows: finish a pending save first.
  const publishForm = document.querySelector("form[data-cz-publish]");
  publishForm?.addEventListener("submit", async (e) => {
    if (!timer && !inflight && !unsaved()) return;
    e.preventDefault();
    setStatus(msg.msgPending, "saving");
    await save();
    if (!unsaved()) publishForm.submit();
  });

  window.addEventListener("beforeunload", (e) => {
    if (inflight || unsaved()) { e.preventDefault(); e.returnValue = ""; }
  });

  updateVisibility();
  updateSamples();
  syncAllColors();
  showWarnings(state.warnings);
  // Load the preview in the admin's colour scheme.
  frame.src = previewURL(frame.dataset.src, location.origin, slug, scheme);
  applyScheme();
}

if (typeof document !== "undefined") {
  const root = document.querySelector("[data-customizer]");
  if (root) init(root);
}
