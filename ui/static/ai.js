// AI assistant panel of the post editor. Talks to /admin/api/ai/*; uses the
// visual editor's API (window.sapEditor) when present, else the textarea.
(() => {
  "use strict";
  const panel = document.querySelector("[data-ai]");
  const form = document.getElementById("post-form");
  if (!panel || !form) return;
  const msg = panel.dataset;
  const $ = (sel, root = document) => root.querySelector(sel);
  const field = (name) => form.elements.namedItem(name);
  const out = $("[data-ai-result]", panel);
  const csrf = () => field("csrf")?.value || "";
  const textarea = () => field("content");
  const fmt = (tpl, v) => (tpl || "%s").replace("%s", v).replace("%d", v);
  const el = (tag, props = {}, ...kids) => {
    const n = document.createElement(tag);
    for (const [k, v] of Object.entries(props)) {
      if (k === "class") n.className = v;
      else if (k === "dataset") Object.assign(n.dataset, v);
      else if (k.startsWith("on")) n.addEventListener(k.slice(2), v);
      else n.setAttribute(k, v);
    }
    n.append(...kids.filter((k) => k != null));
    return n;
  };
  const btn = (label, onclick, variant = "outline") =>
    el("button", { type: "button", class: "btn", dataset: { size: "sm", variant }, onclick }, label);

  function show(...nodes) { out.replaceChildren(...nodes); out.hidden = nodes.length === 0; }
  function info(text, ...extra) { show(el("p", { class: "text-sm" }, text), ...extra); }
  function fail(text) { show(el("p", { class: "text-sm text-destructive", role: "alert" }, text || msg.msgError)); }

  // ------------------------------------------------------------ editor glue --
  const ed = () => window.sapEditor;
  function selection() {
    if (ed()?.getSelection) return ed().getSelection();
    const ta = textarea();
    return { markdown: ta.value.slice(ta.selectionStart, ta.selectionEnd), start: ta.selectionStart, end: ta.selectionEnd };
  }
  function changed(ta) {
    ta.dispatchEvent(new Event("input", { bubbles: true }));
    ta.dispatchEvent(new CustomEvent("sap:content-set", { bubbles: true }));
  }
  function replaceSelection(md) {
    if (ed()?.replaceSelection) return ed().replaceSelection(md);
    const ta = textarea();
    ta.setRangeText(md, ta.selectionStart, ta.selectionEnd, "end");
    changed(ta);
  }
  function insertBlock(md) {
    if (ed()?.insertAtCursor) return ed().insertAtCursor(md);
    const ta = textarea();
    const before = ta.value.slice(0, ta.selectionEnd);
    const sep = before && !before.endsWith("\n\n") ? (before.endsWith("\n") ? "\n" : "\n\n") : "";
    ta.setRangeText(sep + md + "\n\n", ta.selectionEnd, ta.selectionEnd, "end");
    changed(ta);
  }
  function reveal(start, end) {
    if (ed()?.reveal) return ed().reveal(start, end);
    const ta = textarea();
    ta.focus();
    ta.setSelectionRange(start, end);
  }
  function setField(name, value) {
    const f = field(name);
    if (!f) return null;
    const prev = f.value;
    f.value = value;
    f.dispatchEvent(new Event("input", { bubbles: true }));
    return () => { f.value = prev; f.dispatchEvent(new Event("input", { bubbles: true })); show(); };
  }
  function setFeatured(item) {
    const input = $("#featured_media_id"), img = $("[data-featured-img]"), clear = $("[data-featured-clear]");
    if (!input || !img) return;
    input.value = item.media_id;
    img.hidden = false;
    img.src = item.thumb || item.url;
    img.alt = item.alt || "";
    if (clear) clear.hidden = false;
    input.dispatchEvent(new Event("input", { bubbles: true }));
  }

  function article() {
    return {
      post_id: Number(panel.dataset.postId) || 0,
      title: field("title")?.value || "",
      excerpt: field("excerpt")?.value || "",
      content: textarea()?.value || "",
      lang: field("lang")?.value || panel.dataset.lang,
    };
  }

  async function call(path, body) {
    const res = await fetch("/admin/api/ai/" + path, {
      method: "POST", credentials: "same-origin",
      headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf() },
      body: JSON.stringify(body),
    });
    let data = {};
    try { data = await res.json(); } catch { /* not JSON */ }
    if (!res.ok) throw new Error(data.error || msg.msgError);
    return data;
  }

  async function job(path, body) {
    const { job: id } = await call(path, body);
    for (let i = 0; i < 400; i++) {
      await new Promise((r) => setTimeout(r, 1500));
      const res = await fetch("/admin/api/ai/jobs/" + id, { credentials: "same-origin" });
      const j = await res.json();
      if (!res.ok) throw new Error(j.error || msg.msgError);
      if (j.done) { if (j.error) throw new Error(j.error); return j.result; }
    }
    throw new Error(msg.msgError);
  }

  // --------------------------------------------------------------- tasks --
  function lintList(findings, offset = 0) {
    if (!findings?.length) return el("p", { class: "text-sm muted" }, msg.msgClean);
    const list = el("ul", { class: "ai-findings" });
    for (const f of findings.slice(0, 50)) {
      list.append(el("li", {},
        el("button", { type: "button", class: "link", onclick: () => reveal(offset + f.start, offset + f.end) }, "“" + f.phrase + "”"),
        f.suggestion ? el("span", { class: "muted" }, " — " + f.suggestion) : null));
    }
    return el("div", {}, el("p", { class: "text-sm font-medium" }, fmt(msg.msgFindings, findings.length)), list);
  }

  const tasks = {
    async summary() {
      const { text } = await call("summary", article());
      const undo = setField("excerpt", text);
      info(msg.msgExcerpt, undo ? btn(msg.msgUndo, undo, "ghost") : null);
    },
    async seo() {
      const r = await call("seo", article());
      const undo = setField("meta_description", r.description);
      const row = (label, value, apply) => el("div", { class: "ai-kv" }, el("span", { class: "muted" }, label),
        el("span", {}, value), apply ? btn(msg.msgUse, apply, "ghost") : null);
      show(el("p", { class: "text-sm" }, msg.msgMeta, " ", undo ? btn(msg.msgUndo, undo, "ghost") : ""),
        row(msg.msgSeoTitle, r.title), row(msg.msgOgTitle, r.og_title),
        row(msg.msgSlug, r.slug, () => setField("slug", r.slug)),
        row(msg.msgKeywords, (r.keywords || []).join(", ")));
    },
    async header() {
      const r = await job("image", { ...article(), kind: "header" });
      setFeatured(r);
      show(el("img", { src: r.thumb || r.url, alt: r.alt || "", class: "ai-thumb" }), el("p", { class: "text-sm" }, msg.msgFeatured));
    },
    async inline() {
      const sel = selection();
      const r = await job("image", { ...article(), kind: "inline", text: sel.markdown || "" });
      const alt = (sel.markdown || r.alt || "").replace(/[\[\]\n]/g, " ").trim().slice(0, 120);
      const md = `![${alt}](${r.url})`;
      show(el("img", { src: r.thumb || r.url, alt: "", class: "ai-thumb" }), el("p", { class: "text-sm" }, msg.msgImage),
        btn(msg.msgInsert, () => { insertBlock(md); show(); }));
    },
    async write() {
      const mode = $("[data-ai-mode]", panel).value;
      const brief = $("[data-ai-brief]", panel).value.trim();
      const sel = selection();
      let text = sel.markdown;
      if (mode === "continue") text = textarea().value;
      if ((mode === "improve" || mode === "shorten") && !text.trim()) return fail(msg.msgSelect);
      if ((mode === "draft" || mode === "outline") && !brief) return fail(msg.msgBrief);
      const r = await call("write", { ...article(), mode, text, brief, tone: $("[data-ai-tone]", panel).value.trim() });
      const pre = el("div", { class: "ai-text" }, r.text);
      const actions = el("div", { class: "flex flex-wrap gap-2" },
        (mode === "improve" || mode === "shorten") ? btn(msg.msgReplace, () => { replaceSelection(r.text); show(); }, "default") : null,
        btn(msg.msgInsert, () => { insertBlock(r.text); show(); }),
        btn(msg.msgCopy, async (e) => { try { await navigator.clipboard.writeText(r.text); e.target.textContent = msg.msgCopied; } catch { /* denied */ } }, "ghost"));
      show(pre, actions, r.findings?.length ? lintList(r.findings) : null);
    },
    async translate() {
      const to = $("[data-ai-to]", panel).value;
      const r = await job("translate", { ...article(), to });
      info(msg.msgTranslated, el("a", { href: r.edit_url, class: "btn", dataset: { size: "sm" } }, msg.msgOpen));
    },
    async lint() {
      const sel = selection();
      const text = sel.markdown && sel.start >= 0 ? sel.markdown : textarea().value;
      const offset = sel.markdown && sel.start >= 0 ? sel.start : 0;
      const r = await call("lint", { text, lang: article().lang });
      show(lintList(r.findings, offset));
    },
  };

  let busy = false;
  panel.addEventListener("click", async (e) => {
    const b = e.target.closest("[data-ai-task]");
    if (!b || busy) return;
    const task = tasks[b.dataset.aiTask];
    if (!task) return;
    busy = true;
    panel.querySelectorAll("[data-ai-task]").forEach((x) => { x.disabled = true; });
    b.setAttribute("aria-busy", "true");
    info(msg.msgWorking);
    try { await task(); } catch (err) { fail(err.message); } finally {
      busy = false;
      b.removeAttribute("aria-busy");
      panel.querySelectorAll("[data-ai-task]").forEach((x) => { x.disabled = false; });
    }
  });
})();
