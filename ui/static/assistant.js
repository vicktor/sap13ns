// Site assistant widget. The floating button opens a modal chat (native
// <dialog>: focus stays inside, Escape closes, focus returns to the button).
// Questions go to POST /api/assistant with the last turns of the
// conversation, which lives only in this tab (sessionStorage); the answer
// streams in as server-sent events and is replaced by the server-rendered,
// sanitized HTML with numbered links to its sources. Without JavaScript
// the button is a plain link to the /ask page.
(() => {
  "use strict";
  const root = document.querySelector("[data-assistant]");
  if (!root || !window.fetch || typeof HTMLDialogElement !== "function") return;

  const $ = (sel) => root.querySelector(sel);
  const fab = $("[data-assistant-open]");
  const dialog = $("[data-assistant-dialog]");
  const log = $("[data-assistant-log]");
  const form = $("[data-assistant-form]");
  const input = $("[data-assistant-input]");
  const send = $("[data-assistant-send]");
  const suggest = $("[data-assistant-suggest]");
  const t = (key) => root.dataset["t" + key] || "";
  const lang = root.dataset.lang || document.documentElement.lang || "";
  const storeKey = "sap-assistant:" + lang;
  const maxTurns = 6; // sent with each question, as the server keeps
  const greeting = log.firstElementChild;

  let turns = load(); // [{role: "user"|"assistant", text, html?, sources?, error?}]
  let busy = null; // AbortController of the question in flight

  function load() {
    try {
      const v = JSON.parse(sessionStorage.getItem(storeKey) || "[]");
      return Array.isArray(v) ? v.slice(-30) : [];
    } catch {
      return [];
    }
  }
  function save() {
    try {
      sessionStorage.setItem(storeKey, JSON.stringify(turns.slice(-30)));
    } catch {
      // Private mode or full storage: the conversation just is not kept.
    }
  }

  function el(tag, cls, text) {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text != null) e.textContent = text;
    return e;
  }

  function bubble(role) {
    const msg = el("div", "sap-assistant-msg " + (role === "user" ? "is-user" : "is-bot"));
    const b = el("div", "sap-assistant-bubble");
    msg.append(b);
    log.append(msg);
    return b;
  }

  // Links only to pages of this site (the server sends paths).
  const safePath = (u) => typeof u === "string" && /^\/(?![/\\])/.test(u);

  function renderSources(b, sources) {
    if (!Array.isArray(sources) || !sources.length) return;
    const nav = el("nav", "sap-assistant-sources");
    nav.setAttribute("aria-label", t("Sources"));
    const ol = el("ol");
    for (const s of sources) {
      if (!safePath(s.url)) continue;
      const a = el("a");
      a.href = s.url;
      a.append(el("span", "sap-assistant-n", String(s.n)), el("span", "", s.heading ? s.title + " · " + s.heading : s.title));
      const li = el("li");
      li.append(a);
      ol.append(li);
    }
    nav.append(el("p", "sap-assistant-sources-title", t("Sources")), ol);
    b.append(nav);
  }

  function renderTurn(turn) {
    const b = bubble(turn.role);
    if (turn.role === "assistant" && turn.html) {
      b.innerHTML = turn.html; // sanitized by the server
      renderSources(b, turn.sources);
    } else {
      b.textContent = turn.text;
    }
    if (turn.error) b.parentElement.classList.add("is-error");
    return b;
  }

  function scrollDown() {
    log.scrollTop = log.scrollHeight;
  }

  function typing(b, label) {
    const p = el("p", "sap-assistant-typing");
    p.append(el("i"), el("i"), el("i"), el("span", "", label));
    b.replaceChildren(p);
  }

  function history() {
    return turns.filter((x) => !x.error).slice(-maxTurns).map((x) => ({ role: x.role, text: x.text }));
  }

  // readEvents parses a text/event-stream body; data lines hold JSON.
  async function readEvents(body, fn) {
    const reader = body.getReader();
    const dec = new TextDecoder();
    let buf = "";
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      buf += dec.decode(value, { stream: true }).replace(/\r\n?/g, "\n");
      let i;
      while ((i = buf.indexOf("\n\n")) >= 0) {
        const block = buf.slice(0, i);
        buf = buf.slice(i + 2);
        let event = "message";
        let data = "";
        for (const line of block.split("\n")) {
          if (line.startsWith("event:")) event = line.slice(6).trim();
          else if (line.startsWith("data:")) data += line.slice(5).trim();
        }
        if (!data) continue;
        let v;
        try {
          v = JSON.parse(data);
        } catch {
          continue;
        }
        fn(event, v);
      }
    }
  }

  async function ask(question) {
    question = question.trim();
    if (!question || busy) return;
    const hist = history();
    const userTurn = { role: "user", text: question };
    turns.push(userTurn);
    save();
    renderTurn(userTurn);
    if (suggest) suggest.hidden = true;
    input.value = "";

    const b = bubble("assistant");
    typing(b, "");
    scrollDown();
    busy = new AbortController();
    log.setAttribute("aria-busy", "true");
    send.disabled = true;

    let text = "";
    let done = null;
    let error = "";
    try {
      const res = await fetch(root.dataset.endpoint, {
        method: "POST",
        headers: { "Content-Type": "application/json", Accept: "text/event-stream, application/json" },
        body: JSON.stringify({ question, history: hist, lang }),
        signal: busy.signal,
      });
      const type = res.headers.get("Content-Type") || "";
      if (!res.ok) {
        let msg = "";
        try {
          msg = (await res.json()).error || "";
        } catch {
          // not JSON
        }
        throw new Error(msg || t("Error"));
      }
      if (type.startsWith("text/event-stream") && res.body) {
        await readEvents(res.body, (event, data) => {
          if (event === "thinking" && !text) {
            typing(b, t("Thinking"));
          } else if (event === "delta" && typeof data.text === "string") {
            text += data.text;
            const shown = text.trimStart(); // models often open with blank lines
            if (!shown) return;
            b.classList.add("is-streaming");
            b.textContent = shown;
            scrollDown();
          } else if (event === "done") {
            done = data;
          } else if (event === "error") {
            error = data.error || t("Error");
          }
        });
      } else {
        done = await res.json();
      }
      if (!done && !error) error = t("Error");
    } catch (e) {
      if (e && e.name === "AbortError") {
        b.parentElement.remove();
        return;
      }
      error = navigator.onLine === false ? t("Offline") : (e && e.message) || t("Error");
    } finally {
      busy = null;
      log.removeAttribute("aria-busy");
      send.disabled = false;
    }

    b.classList.remove("is-streaming");
    let turn;
    if (done) {
      turn = { role: "assistant", text: String(done.text || ""), html: String(done.html || ""), sources: done.sources || [] };
      b.innerHTML = turn.html;
      renderSources(b, turn.sources);
    } else {
      turn = { role: "assistant", text: error, error: true };
      b.textContent = error;
      b.parentElement.classList.add("is-error");
    }
    turns.push(turn);
    save();
    scrollDown();
    if (dialog.open) input.focus();
  }

  function reset() {
    if (busy) busy.abort();
    turns = [];
    save();
    log.replaceChildren(greeting);
    if (suggest) suggest.hidden = false;
    input.value = "";
    input.focus();
  }

  function open() {
    if (!dialog.open) dialog.showModal();
    scrollDown();
    input.focus();
  }

  for (const turn of turns) renderTurn(turn);
  if (turns.length && suggest) suggest.hidden = true;

  fab.addEventListener("click", (e) => {
    e.preventDefault();
    open();
  });
  $("[data-assistant-close]").addEventListener("click", () => dialog.close());
  $("[data-assistant-reset]").addEventListener("click", reset);
  // A click on the backdrop (outside the panel's box) closes it.
  dialog.addEventListener("click", (e) => {
    if (e.target !== dialog) return;
    const r = dialog.getBoundingClientRect();
    if (e.clientX < r.left || e.clientX > r.right || e.clientY < r.top || e.clientY > r.bottom) dialog.close();
  });
  dialog.addEventListener("close", () => fab.focus());
  form.addEventListener("submit", (e) => {
    e.preventDefault();
    ask(input.value);
  });
  input.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && !e.shiftKey && !e.isComposing) {
      e.preventDefault();
      form.requestSubmit();
    }
  });
  if (suggest) {
    suggest.addEventListener("click", (e) => {
      const btn = e.target.closest("button");
      if (btn) ask(btn.textContent);
    });
  }
})();
