// sap13ns admin panel behaviour. Vanilla JS, no inline handlers (strict CSP):
// everything is wired through data-* attributes and event delegation.
(() => {
  "use strict";

  const $ = (sel, root = document) => root.querySelector(sel);
  const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));
  const csrf = () => $('input[name="csrf"]')?.value || "";
  const esc = (s) => String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
  const t = (key) => (document.documentElement.lang === "es" ? (ES[key] || key) : key);
  const ES = {
    "Uploading…": "Subiendo…", "Upload failed": "Error al subir", "Processing…": "Procesando…", "The file is too large": "El archivo es demasiado grande", "Copied": "Copiado", "Nothing selected": "No hay nada seleccionado",
    "Choose an action": "Elige una acción", "Loading…": "Cargando…", "No files": "No hay archivos", "Preview failed": "No se pudo generar la vista previa",
    "words": "palabras", "min read": "min de lectura", "Link text": "texto", "Image description": "descripción",
  };

  // ------------------------------------------------------------ toasts --
  function toast(category, title) {
    document.dispatchEvent(new CustomEvent("basecoat:toast", { detail: { config: { category, title: esc(title) } } }));
  }

  // ------------------------------------------------------------- theme --
  function toggleTheme() {
    const dark = !document.documentElement.classList.contains("dark");
    document.documentElement.classList.toggle("dark", dark);
    window.sap13nsTheme?.save(dark ? "dark" : "light");
  }

  // ------------------------------------------------------------ dialogs --
  function askConfirm(message, onAccept) {
    const dlg = $("#confirm-dialog");
    if (!dlg) { if (window.confirm(message)) onAccept(); return; }
    $("#confirm-dialog-description", dlg).textContent = message;
    const accept = $("[data-confirm-accept]", dlg);
    const handler = () => { dlg.close(); onAccept(); };
    accept.addEventListener("click", handler, { once: true });
    dlg.addEventListener("close", () => accept.removeEventListener("click", handler), { once: true });
    dlg.showModal();
    accept.focus();
  }

  // ----------------------------------------------------------- clicks --
  document.addEventListener("click", (e) => {
    const el = e.target.closest("[data-theme-toggle],[data-sidebar-toggle],[data-dialog-open],[data-dialog-close],[data-password-toggle],[data-copy],[data-copy-text],[data-download-text],[data-back],[data-confirm],[data-md],[data-media-picker],[data-featured-clear],[data-media-more]");
    // Clicking the backdrop of a dialog closes it.
    if (e.target instanceof HTMLDialogElement && e.target.hasAttribute("data-overlay-close")) { e.target.close(); return; }
    if (!el) return;

    if (el.hasAttribute("data-theme-toggle")) return toggleTheme();
    if (el.dataset.sidebarToggle) { const sb = document.getElementById(el.dataset.sidebarToggle); sb?.toggle?.(); return; }
    if (el.dataset.dialogOpen) { document.getElementById(el.dataset.dialogOpen)?.showModal(); return; }
    if (el.hasAttribute("data-dialog-close")) { el.closest("dialog")?.close(); return; }
    if (el.dataset.passwordToggle) {
      const input = document.getElementById(el.dataset.passwordToggle);
      if (input) { input.type = input.type === "password" ? "text" : "password"; el.setAttribute("aria-pressed", String(input.type === "text")); }
      return;
    }
    if (el.dataset.copy || el.dataset.copyText) {
      const src = document.getElementById(el.dataset.copy || el.dataset.copyText);
      const text = el.dataset.copy ? src?.value : $$("li", src).map((li) => li.textContent.trim()).join("\n");
      navigator.clipboard?.writeText(text || "").then(() => toast("success", t("Copied")));
      return;
    }
    if (el.dataset.downloadText) {
      const src = document.getElementById(el.dataset.downloadText);
      const text = $$("li", src).map((li) => li.textContent.trim()).join("\n") + "\n";
      const a = document.createElement("a");
      a.href = URL.createObjectURL(new Blob([text], { type: "text/plain" }));
      a.download = el.dataset.filename || "codes.txt";
      a.click();
      setTimeout(() => URL.revokeObjectURL(a.href), 1000);
      return;
    }
    if (el.hasAttribute("data-back") && history.length > 1) { e.preventDefault(); history.back(); return; }
    if (el.dataset.confirm && el.matches("button, input[type=submit]")) {
      e.preventDefault();
      // requestSubmit() does not fire click events, so this does not loop.
      askConfirm(el.dataset.confirm, () => el.form?.requestSubmit(el));
      return;
    }
    if (el.dataset.md) return editorFormat(el.closest("[data-editor]"), el.dataset.md);
    if (el.dataset.mediaPicker) return openPicker(el.dataset.mediaPicker, el);
    if (el.hasAttribute("data-featured-clear")) return setFeatured(null);
    if (el.hasAttribute("data-media-more")) return loadMedia(false);
  });

  // ------------------------------------------------------ form helpers --
  document.addEventListener("change", (e) => {
    const el = e.target;
    if (el.dataset?.selectAll) {
      const form = el.closest("form");
      $$(`input[type=checkbox][name="${el.dataset.selectAll}"]`, form).forEach((cb) => { cb.checked = el.checked; });
      updateSelected(form);
    } else if (el.matches?.('input[type=checkbox][name="ids"]')) {
      updateSelected(el.closest("form"));
    }
    if (el.hasAttribute?.("data-autosubmit")) {
      if (el.files?.length) uploadForm(el.form, el.files);
      // A native select, or the hidden input of a Basecoat select (selects.js).
      else if (el.tagName === "SELECT" || el.type === "hidden") el.form?.requestSubmit();
    }
  });

  function updateSelected(form) {
    if (!form) return;
    const n = $$('input[name="ids"]:checked', form).length;
    const out = $("[data-selected-count]", form);
    if (out) out.textContent = n ? `${n} ✓` : "";
  }

  document.addEventListener("submit", (e) => {
    const form = e.target;
    if (form.hasAttribute("data-bulk") && !e.submitter?.hasAttribute("formaction")) {
      const action = $('select[name="action"], input[name="action"]', form);
      if (action && !action.value) { e.preventDefault(); toast("warning", t("Choose an action")); return; }
      if (!$$('input[name="ids"]:checked', form).length) { e.preventDefault(); toast("warning", t("Nothing selected")); return; }
    }
    if (form.hasAttribute("data-unsaved")) form.dataset.dirty = "";
    // Prevent double submits.
    if (e.submitter && !e.defaultPrevented) setTimeout(() => { e.submitter.disabled = true; setTimeout(() => { e.submitter.disabled = false; }, 4000); }, 0);
  });

  // Unsaved-changes warning.
  document.addEventListener("input", (e) => {
    const form = e.target.closest?.("form[data-unsaved]");
    if (form) form.dataset.dirty = "1";
    if (e.target.dataset?.strength) strength(e.target);
    if (e.target.hasAttribute?.("data-counter")) counter(e.target);
    if (e.target.hasAttribute?.("data-slug-source")) slugFromTitle(e.target);
    if (e.target.hasAttribute?.("data-slug-target")) e.target.dataset.slugLocked = "1";
    if (e.target.dataset?.revealOnChange) {
      const box = document.getElementById(e.target.dataset.revealOnChange);
      if (box) box.hidden = e.target.value === e.target.defaultValue;
    }
    if (e.target.hasAttribute?.("data-editor-text")) wordCount(e.target);
    if (e.target.dataset?.bubble) bubblePreview(e.target);
  });

  // Settings → Site assistant: the preview follows the chat button's corner,
  // height and label as they are chosen.
  function bubblePreview(el) {
    const box = $("[data-bubble-preview]", el.form || document);
    if (!box) return;
    const kind = el.dataset.bubble;
    if (kind === "label") {
      const label = $("[data-bubble-preview-label]", box);
      if (label) label.textContent = el.value.trim();
    } else if (el.checked) {
      box.dataset[kind] = el.value;
    }
  }
  window.addEventListener("beforeunload", (e) => {
    if ($$("form[data-unsaved]").some((f) => f.dataset.dirty === "1")) { e.preventDefault(); e.returnValue = ""; }
  });

  function counter(el) {
    const out = $(`[data-counter-for="${el.id}"]`);
    if (out) { const n = el.value.length; out.textContent = `${n}/${el.dataset.counter}`; out.classList.toggle("text-warning", n > +el.dataset.counter); }
  }

  // Password strength: length and variety; the server enforces the real policy.
  function strength(input) {
    const box = document.getElementById(input.dataset.strength);
    if (!box) return;
    const v = input.value;
    let score = 0;
    if (v.length >= 10) score++;
    if (v.length >= 14) score++;
    if (/[a-z]/.test(v) && /[A-Z0-9]/.test(v)) score++;
    if (/[^A-Za-z0-9]/.test(v) || v.length >= 20) score++;
    if (/^(.)\1+$/.test(v) || v.length < 8) score = v ? 1 : 0;
    box.dataset.score = String(score);
  }

  // Slugs mirror the Go Slugify: transliterate, lowercase, dashes.
  function slugify(s) {
    return s.normalize("NFD").replace(/[̀-ͯ]/g, "").replace(/ß/g, "ss").replace(/æ/g, "ae").replace(/ø/g, "o").replace(/œ/g, "oe")
      .toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 120).replace(/-+$/, "");
  }
  function slugFromTitle(src) {
    const target = $("[data-slug-target]", src.form || document);
    if (!target || target.dataset.slugLocked) return;
    target.value = slugify(src.value);
  }

  // ------------------------------------------------------------ editor --
  const wrap = (ta, before, after, placeholder) => {
    const { selectionStart: s, selectionEnd: en, value } = ta;
    const sel = value.slice(s, en) || placeholder;
    ta.setRangeText(before + sel + after, s, en, "end");
    if (!value.slice(s, en)) { ta.selectionStart = s + before.length; ta.selectionEnd = s + before.length + sel.length; }
  };
  const prefixLines = (ta, prefix) => {
    const { selectionStart: s, selectionEnd: en, value } = ta;
    const start = value.lastIndexOf("\n", s - 1) + 1;
    const block = value.slice(start, en);
    let i = 0;
    const out = block.split("\n").map((l) => (typeof prefix === "function" ? prefix(++i) : prefix) + l).join("\n");
    ta.setRangeText(out, start, en, "end");
  };
  function editorFormat(root, kind) {
    const ta = $("[data-editor-text]", root);
    if (!ta || ta.hidden) return;
    ta.focus();
    switch (kind) {
      case "bold": wrap(ta, "**", "**", "texto"); break;
      case "italic": wrap(ta, "_", "_", "texto"); break;
      case "code": ta.value.slice(ta.selectionStart, ta.selectionEnd).includes("\n") ? wrap(ta, "\n```\n", "\n```\n", "") : wrap(ta, "`", "`", "code"); break;
      case "link": wrap(ta, "[", "](https://)", t("Link text")); break;
      case "heading": prefixLines(ta, "## "); break;
      case "quote": prefixLines(ta, "> "); break;
      case "ul": prefixLines(ta, "- "); break;
      case "ol": prefixLines(ta, (n) => `${n}. `); break;
    }
    ta.dispatchEvent(new Event("input", { bubbles: true }));
  }
  document.addEventListener("keydown", (e) => {
    const ta = e.target.closest?.("[data-editor-text]");
    if (!ta || !(e.ctrlKey || e.metaKey)) return;
    const map = { b: "bold", i: "italic", k: "link" };
    const kind = map[e.key.toLowerCase()];
    if (kind) { e.preventDefault(); editorFormat(ta.closest("[data-editor]"), kind); }
  });
  // Tab inserts two spaces instead of leaving the editor (Esc then Tab leaves).
  let escPressed = false;
  document.addEventListener("keydown", (e) => {
    const ta = e.target.closest?.("[data-editor-text]");
    if (!ta) return;
    if (e.key === "Escape") { escPressed = true; return; }
    if (e.key === "Tab" && !e.shiftKey && !escPressed) { e.preventDefault(); ta.setRangeText("  ", ta.selectionStart, ta.selectionEnd, "end"); }
    escPressed = false;
  });
  function wordCount(ta) {
    const out = $("[data-word-count]", ta.closest("[data-editor]"));
    if (!out) return;
    const words = (ta.value.replace(/```[\s\S]*?```/g, " ").match(/[\p{L}\p{N}]+/gu) || []).length;
    out.textContent = `${words} ${t("words")} · ${Math.max(1, Math.round(words / 220))} ${t("min read")}`;
  }
  $$("[data-editor-text]").forEach(wordCount);
  $$("[data-counter]").forEach(counter);

  // ------------------------------------------------------------- media --
  const picker = { mode: "insert", page: 1, q: "", busy: false };

  // openPicker opens the media dialog: "insert" puts the file in the
  // editor, "featured" sets the featured image, and "image" hands the image
  // chosen to whoever listens for sap13ns:media-pick on the button that
  // opened it (the customiser's images).
  function openPicker(mode, opener) {
    const dlg = $("#media-dialog");
    if (!dlg) return;
    picker.mode = mode;
    picker.opener = opener;
    dlg.showModal();
    loadMedia(true);
  }

  async function loadMedia(reset) {
    const grid = $("[data-media-grid]");
    const more = $("[data-media-more]");
    if (!grid || picker.busy) return;
    if (reset) { picker.page = 1; grid.innerHTML = ""; }
    picker.busy = true;
    try {
      const params = new URLSearchParams({ page: String(picker.page), q: picker.q });
      if (picker.mode !== "insert") params.set("type", "image");
      const res = await fetch("/admin/api/media?" + params, { credentials: "same-origin" });
      const data = await res.json();
      for (const item of data.items) grid.append(mediaButton(item));
      if (!grid.children.length) grid.innerHTML = `<p class="muted col-span-full py-8 text-center">${esc(t("No files"))}</p>`;
      more.hidden = !data.more;
      picker.page++;
    } finally { picker.busy = false; }
  }

  function mediaButton(item) {
    const b = document.createElement("button");
    b.type = "button";
    b.title = item.name;
    if (item.image) {
      const img = document.createElement("img");
      img.src = item.thumb; img.alt = item.alt || ""; img.loading = "lazy";
      b.append(img);
    } else {
      b.innerHTML = `<span class="media-file"><small>${esc(item.mime)}</small><small>${esc(item.name)}</small></span>`;
    }
    b.addEventListener("click", () => pick(item));
    return b;
  }

  function pick(item) {
    $("#media-dialog")?.close();
    if (picker.mode === "featured") return setFeatured(item);
    if (picker.mode === "image") {
      picker.opener?.dispatchEvent(new CustomEvent("sap13ns:media-pick", { bubbles: true, detail: item }));
      return;
    }
    const ta = $("[data-editor-text]");
    if (!ta) return;
    insertMedia(ta, item);
  }

  function insertMedia(ta, item) {
    const md = item.image ? `![${(item.alt || "").replace(/[\[\]]/g, "") || t("Image description")}](${item.medium || item.url})` : `[${item.name.replace(/[\[\]]/g, "")}](${item.url})`;
    const before = ta.value.slice(0, ta.selectionStart);
    const sep = before && !before.endsWith("\n\n") ? (before.endsWith("\n") ? "\n" : "\n\n") : "";
    ta.setRangeText(sep + md + "\n", ta.selectionStart, ta.selectionEnd, "end");
    ta.dispatchEvent(new Event("input", { bubbles: true }));
    ta.focus();
  }

  function setFeatured(item) {
    const input = $("#featured_media_id");
    const img = $("[data-featured-img]");
    const clear = $("[data-featured-clear]");
    if (!input || !img) return;
    input.value = item ? item.id : "";
    img.hidden = !item;
    if (item) { img.src = item.medium || item.url; img.alt = item.alt || ""; }
    if (clear) clear.hidden = !item;
    input.dispatchEvent(new Event("input", { bubbles: true }));
  }

  let searchTimer;
  document.addEventListener("input", (e) => {
    if (!e.target.hasAttribute?.("data-media-search")) return;
    clearTimeout(searchTimer);
    searchTimer = setTimeout(() => { picker.q = e.target.value.trim(); loadMedia(true); }, 250);
  });
  document.addEventListener("change", async (e) => {
    if (!e.target.hasAttribute?.("data-media-upload") || !e.target.files.length) return;
    const items = await upload(e.target.files, e.target.closest("dialog"));
    e.target.value = "";
    if (items.length) loadMedia(true);
  });

  // sendFiles posts files to the library with XMLHttpRequest, whose upload
  // events give the progress (fetch has none). It resolves with the status
  // and the JSON answer: data is null when the answer is not sap13ns's (a
  // proxy's 413 page, say) and status 0 when the request did not get through.
  function sendFiles(files, onProgress, flash) {
    return new Promise((resolve) => {
      const fd = new FormData();
      fd.append("csrf", csrf());
      for (const f of files) fd.append("file", f);
      const xhr = new XMLHttpRequest();
      xhr.open("POST", "/admin/media");
      xhr.setRequestHeader("Accept", "application/json");
      xhr.setRequestHeader("X-CSRF-Token", csrf());
      if (flash) xhr.setRequestHeader("X-Sap13ns-Flash", "1");
      xhr.upload.addEventListener("progress", (e) => {
        if (e.lengthComputable && onProgress) onProgress(Math.min(100, Math.round((e.loaded / e.total) * 100)));
      });
      xhr.addEventListener("load", () => {
        let data = null;
        if ((xhr.getResponseHeader("Content-Type") || "").includes("application/json")) {
          try { data = JSON.parse(xhr.responseText); } catch (_) { /* not JSON after all */ }
        }
        resolve({ status: xhr.status, data });
      });
      xhr.addEventListener("error", () => resolve({ status: 0, data: null }));
      xhr.send(fd);
    });
  }

  // uploadErrors are the messages of an upload that failed, whole or in part.
  function uploadErrors({ status, data }) {
    if (!data) return [status === 413 ? t("The file is too large") : t("Upload failed")];
    if (data.errors?.length) return data.errors;
    if (status >= 400) return [data.error || t("Upload failed")];
    return [];
  }

  // progressBox drives the upload status inside root: a bar with a line of
  // text while sending, then the errors, if any. Toasts would not do in the
  // media dialog, which the top layer puts above them.
  function progressBox(root) {
    const box = $("[data-upload-progress]", root);
    const errs = $("[data-upload-errors]", root);
    if (!box) return null;
    const bar = $(".progress", box);
    const text = $("[data-upload-text]", box);
    const show = (pct, msg) => {
      if (bar) { bar.firstElementChild.style.width = pct + "%"; bar.setAttribute("aria-valuenow", String(pct)); }
      if (text) text.textContent = msg;
    };
    return {
      start() {
        if (errs) errs.hidden = true;
        box.hidden = false;
        show(0, t("Uploading…"));
      },
      progress(pct) { show(pct, pct < 100 ? `${t("Uploading…")} ${pct} %` : t("Processing…")); },
      end(messages) {
        box.hidden = true;
        show(0, "");
        if (!errs || !messages.length) return;
        $("section", errs).replaceChildren(...messages.map((m) => Object.assign(document.createElement("p"), { textContent: m })));
        errs.hidden = false;
      },
    };
  }

  // upload sends files and returns the items stored. With a root that has a
  // status box (the media dialog), the progress and the errors show there;
  // otherwise in toasts.
  const uploading = new WeakSet();
  async function upload(files, root) {
    const ui = root && progressBox(root);
    if (ui && uploading.has(root)) return [];
    if (ui) { uploading.add(root); ui.start(); } else toast("info", t("Uploading…"));
    const res = await sendFiles(files, ui?.progress);
    const errs = uploadErrors(res);
    if (ui) { uploading.delete(root); ui.end(errs); } else errs.forEach((m) => toast("error", m));
    return res.data?.items || [];
  }

  // Media library page: the same upload, then a reload, since the server
  // leaves the result as a flash, as it does for the plain form post that
  // works without JavaScript.
  async function uploadForm(form, files) {
    const ui = progressBox(form);
    if (!ui || form.classList.contains("uploading")) return;
    form.classList.add("uploading");
    ui.start();
    const res = await sendFiles(files, ui.progress, true);
    if (res.data) { location.reload(); return; }
    form.classList.remove("uploading");
    ui.end(uploadErrors(res));
    const input = $('input[type="file"]', form);
    if (input) input.value = ""; // so the same file can be chosen again
  }

  // Drag and drop: on the editor it uploads and inserts; on the library it submits.
  document.addEventListener("dragover", (e) => {
    const zone = e.target.closest?.("[data-dropzone]");
    if (!zone || !e.dataTransfer?.types.includes("Files")) return;
    e.preventDefault();
    zone.classList.add("dragging");
  });
  document.addEventListener("dragleave", (e) => { e.target.closest?.("[data-dropzone]")?.classList.remove("dragging"); });
  document.addEventListener("drop", async (e) => {
    const zone = e.target.closest?.("[data-dropzone]");
    if (!zone || !e.dataTransfer?.files.length) return;
    e.preventDefault();
    zone.classList.remove("dragging");
    if (zone.matches("[data-upload-form]")) return uploadForm(zone, e.dataTransfer.files);
    const items = await upload(e.dataTransfer.files, zone.closest("dialog"));
    if (zone.hasAttribute("data-editor-text")) items.forEach((it) => insertMedia(zone, it));
    else loadMedia(true);
  });

  // Paste images straight into the editor.
  document.addEventListener("paste", async (e) => {
    const ta = e.target.closest?.("[data-editor-text]");
    const files = Array.from(e.clipboardData?.files || []).filter((f) => f.type.startsWith("image/"));
    if (!ta || !files.length) return;
    e.preventDefault();
    (await upload(files)).forEach((it) => insertMedia(ta, it));
  });

  // Basecoat tabs need their panels' hidden attribute kept in sync on some browsers.
  document.addEventListener("click", (e) => {
    const tab = e.target.closest?.('.tabs [role="tab"]');
    if (!tab) return;
    const tabs = tab.closest(".tabs");
    $$('[role="tabpanel"]', tabs).forEach((p) => { p.hidden = p.id !== tab.getAttribute("aria-controls"); });
  });
})();
