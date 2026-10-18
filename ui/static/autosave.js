// Editor autosave and edit lock. Existing posts send a heartbeat to the
// server (renewing the lock and storing unsaved changes); new posts keep a
// draft copy in this browser until they are saved for the first time.
(() => {
  "use strict";
  const form = document.getElementById("post-form");
  if (!form) return;
  const $ = (sel, root = document) => root.querySelector(sel);
  const field = (name) => form.elements.namedItem(name);
  const csrf = () => field("csrf")?.value || "";
  const snapshot = () => ({
    title: field("title")?.value ?? "",
    content: field("content")?.value ?? "",
    excerpt: field("excerpt")?.value ?? "",
  });
  const same = (a, b) => a.title === b.title && a.content === b.content && a.excerpt === b.excerpt;
  const fmt = (tpl, v) => (tpl || "%s").replace("%s", v);
  const status = $("[data-autosave-status]");
  const say = (msg) => { if (status) status.textContent = msg; };
  const clock = (d) => d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });

  function fill(data) {
    for (const k of ["title", "content", "excerpt"]) {
      const el = field(k);
      if (el && typeof data[k] === "string") {
        el.value = data[k];
        el.dispatchEvent(new Event("input", { bubbles: true }));
      }
    }
  }

  let submitting = false;
  form.addEventListener("submit", () => { submitting = true; });

  // ------------------------------------------------------------ server --
  const base = form.dataset.heartbeat;
  if (base) {
    let last = snapshot();
    let busy = false;
    const lockNotice = $("[data-lock-notice]");
    const lockText = $("[data-lock-text]");

    async function beat(force) {
      if (busy || submitting) return;
      busy = true;
      const now = snapshot();
      const body = same(now, last) && !force ? {} : now;
      try {
        const res = await fetch(base + "/heartbeat", {
          method: "POST", credentials: "same-origin",
          headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf() },
          body: JSON.stringify(body),
        });
        if (!res.ok) throw new Error(String(res.status));
        const data = await res.json();
        if (data.saved_at) { last = now; say(fmt(form.dataset.msgSaved, clock(new Date(data.saved_at)))); }
        if (lockNotice) {
          lockNotice.hidden = !data.locked_by;
          if (data.locked_by && lockText) lockText.textContent = fmt(lockText.dataset.template, data.locked_by);
        }
      } catch {
        say(form.dataset.msgOffline || "");
      } finally {
        busy = false;
      }
    }

    setInterval(() => beat(false), 15000);
    document.addEventListener("visibilitychange", () => { if (document.visibilityState === "hidden") beat(false); });
    window.addEventListener("pagehide", () => {
      if (submitting) return;
      const fd = new FormData();
      fd.append("csrf", csrf());
      navigator.sendBeacon?.(base + "/unlock", fd);
    });

    const notice = $("[data-autosave-notice]");
    $("[data-autosave-restore]")?.addEventListener("click", async () => {
      const res = await fetch(base + "/autosave", { credentials: "same-origin" });
      if (res.ok) { fill(await res.json()); last = snapshot(); }
      if (notice) notice.hidden = true;
    });
    $("[data-autosave-discard]")?.addEventListener("click", async () => {
      await fetch(base + "/autosave", { method: "DELETE", credentials: "same-origin", headers: { "X-CSRF-Token": csrf() } });
      if (notice) notice.hidden = true;
    });
    return;
  }

  // ----------------------------------------------------------- browser --
  const kind = form.dataset.localDraft;
  if (!kind) return;
  const key = "sap13ns:draft:new:" + kind;
  const store = {
    get() { try { return JSON.parse(localStorage.getItem(key) || "null"); } catch { return null; } },
    set(v) { try { localStorage.setItem(key, JSON.stringify(v)); } catch { /* storage unavailable */ } },
    clear() { try { localStorage.removeItem(key); } catch { /* storage unavailable */ } },
  };

  const saved = store.get();
  const empty = (s) => !s.title.trim() && !s.content.trim();
  if (saved && saved.t && !empty(saved) && empty(snapshot())) {
    const box = document.createElement("div");
    box.className = "alert edit-notice";
    box.setAttribute("role", "status");
    const h = document.createElement("h2");
    h.textContent = fmt(form.dataset.msgLocal, new Date(saved.t).toLocaleString());
    const sec = document.createElement("section");
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = "btn";
    btn.dataset.size = "sm";
    btn.textContent = form.dataset.msgRestore || "Restore";
    btn.addEventListener("click", () => { fill(saved); box.remove(); });
    sec.append(btn);
    box.append(h, sec);
    form.before(box);
  }

  let last = snapshot();
  setInterval(() => {
    if (submitting) return;
    const now = snapshot();
    if (same(now, last)) return;
    last = now;
    if (empty(now)) store.clear();
    else { store.set({ ...now, t: Date.now() }); say(fmt(form.dataset.msgSaved, clock(new Date()))); }
  }, 5000);
  form.addEventListener("submit", () => store.clear());
})();
