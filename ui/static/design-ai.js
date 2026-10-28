// Design with AI (ui/admin/templates/partials.html, design-header): a chat
// on the design screen. Each message goes with the conversation so far to
// the theme's design/ai endpoint (internal/web/design_ai.go), which starts
// a job; the job's result says what changed in the draft, and the screen
// reloads to show it. The conversation lives in sessionStorage, per theme,
// so it survives that reload and the switch between the two tabs.

const MAX_TURNS = 12;

// history keeps the last turns of a conversation.
export function trimHistory(msgs, max = MAX_TURNS) {
  const out = msgs.filter((m) => (m.role === "user" || m.role === "assistant") && typeof m.content === "string" && m.content.trim());
  while (out.length > max) out.shift();
  while (out.length && out[0].role !== "user") out.shift();
  return out;
}

const box = typeof document !== "undefined" && document.querySelector("[data-design-ai]");
if (box) {
  const msg = box.dataset;
  const key = `sap13ns:design-ai:${msg.slug}`;
  const openKey = `${key}:open`;
  const toggle = document.querySelector("[data-design-ai-toggle]");
  const log = box.querySelector("[data-design-ai-log]");
  const form = box.querySelector("[data-design-ai-form]");
  const input = box.querySelector("#design-ai-prompt");
  const send = box.querySelector("[data-design-ai-send]");
  const status = box.querySelector("[data-design-ai-status]");
  const csrf = () => document.querySelector("input[name=csrf]")?.value || "";
  const fmt = (s, ...a) => { let i = 0; return s.replace(/%s/g, () => a[i++] ?? ""); };

  const load = () => { try { return JSON.parse(sessionStorage.getItem(key) || "[]"); } catch { return []; } };
  const save = (h) => { try { sessionStorage.setItem(key, JSON.stringify(trimHistory(h, 40))); } catch { /* private mode */ } };
  let history = load();

  function bubble(role, text, pending) {
    const li = document.createElement("li");
    li.className = "design-ai-msg";
    li.dataset.role = role;
    if (pending) li.dataset.pending = "";
    const who = document.createElement("span");
    who.className = "sr-only";
    who.textContent = `${role === "user" ? msg.msgYou : msg.msgAi}: `;
    li.append(who, text);
    return li;
  }

  function render(pending = false) {
    log.replaceChildren(...history.map((m) => bubble(m.role, m.content)));
    if (pending) log.append(bubble("assistant", msg.msgWorking, true));
    log.scrollTop = log.scrollHeight;
  }

  function show(open) {
    box.hidden = !open;
    toggle?.setAttribute("aria-expanded", String(open));
    try { sessionStorage.setItem(openKey, open ? "1" : ""); } catch { /* private mode */ }
    if (open) { log.scrollTop = log.scrollHeight; input.focus(); }
  }
  toggle?.addEventListener("click", () => show(box.hidden));
  let wasOpen = "";
  try { wasOpen = sessionStorage.getItem(openKey) || ""; } catch { /* private mode */ }
  if (wasOpen) show(true);
  render();

  box.querySelector("[data-design-ai-clear]")?.addEventListener("click", () => {
    history = [];
    save(history);
    render();
    status.textContent = "";
    input.focus();
  });

  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

  async function poll(job) {
    for (;;) {
      await sleep(1500);
      const res = await fetch(`/admin/api/ai/jobs/${encodeURIComponent(job)}`, { headers: { Accept: "application/json" } });
      const j = await res.json().catch(() => ({}));
      if (!res.ok) throw new Error(j.error || res.statusText);
      if (j.done) {
        if (j.error) throw new Error(j.error);
        return j.result || {};
      }
    }
  }

  // Enter sends; Shift+Enter starts a new line.
  input.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && !e.shiftKey && !e.isComposing) {
      e.preventDefault();
      form.requestSubmit();
    }
  });

  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    const text = input.value.trim();
    if (!text || send.disabled) return;
    history.push({ role: "user", content: text });
    save(history);
    render(true);
    input.value = "";
    send.disabled = true;
    status.textContent = "";
    const lang = document.querySelector("iframe")?.contentDocument?.documentElement.lang || "";
    try {
      const res = await fetch(msg.url, {
        method: "POST",
        headers: { "Content-Type": "application/json", Accept: "application/json", "X-CSRF-Token": csrf() },
        body: JSON.stringify({ messages: trimHistory(history), lang }),
      });
      const j = await res.json().catch(() => ({}));
      if (!res.ok || !j.job) throw new Error(j.error || res.statusText);
      const out = await poll(j.job);
      history.push({ role: "assistant", content: out.reply || "" });
      save(history);
      render();
      // New pages and a new front page change the Publish form too.
      if (out.design || out.layout || out.front || out.pages?.length) {
        status.textContent = msg.msgReloading;
        location.reload();
        return;
      }
      status.textContent = "";
    } catch (err) {
      status.textContent = fmt(msg.msgFailed, err.message);
      // The question stays, so it can be sent again.
      history.pop();
      save(history);
      render();
      input.value = text;
    } finally {
      send.disabled = false;
    }
  });
}
