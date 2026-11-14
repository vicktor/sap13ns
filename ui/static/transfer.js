// Tools → Move site: uploads an archive in parts, so that any size goes
// through the server's limit on one request, with a progress bar. A part
// that fails is sent again a few times before the upload stops.
(() => {
  "use strict";
  const form = document.querySelector("[data-transfer-upload]");
  if (!form) return;
  const $ = (sel) => form.querySelector(sel);
  const input = $('input[type="file"]');
  const box = $("[data-transfer-progress]");
  const bar = box?.querySelector(".progress");
  const text = $("[data-transfer-text]");
  const errBox = $("[data-transfer-error]");
  const button = $('button[type="submit"]');
  const csrf = () => $('input[name="csrf"]')?.value || "";
  const chunk = Math.max(1 << 20, Number(form.dataset.chunk) || 8 << 20);
  const msg = (k) => form.dataset["msg" + k] || "";
  const size = (n) => {
    const u = ["B", "KB", "MB", "GB", "TB"];
    let i = 0;
    while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
    return (i ? n.toFixed(1) : n) + " " + u[i];
  };

  function show(done, total) {
    const pct = total ? Math.min(100, Math.floor((done / total) * 100)) : 0;
    box.hidden = false;
    bar.firstElementChild.style.width = pct + "%";
    bar.setAttribute("aria-valuenow", String(pct));
    text.textContent = `${msg("Uploading")} ${pct} % · ${size(done)} / ${size(total)}`;
  }

  function fail(m) {
    box.hidden = true;
    errBox.querySelector("section p").textContent = m || msg("Failed");
    errBox.hidden = false;
    button.disabled = false;
    input.disabled = false;
  }

  async function send(file, id, offset) {
    const q = new URLSearchParams({ offset: String(offset), size: String(file.size) });
    if (id) q.set("id", id);
    else q.set("name", file.name);
    const res = await fetch(form.action + "?" + q, {
      method: "POST",
      credentials: "same-origin",
      headers: { "X-CSRF-Token": csrf(), "Content-Type": "application/octet-stream", Accept: "application/json" },
      body: file.slice(offset, offset + chunk),
    });
    let data = {};
    try { data = await res.json(); } catch { /* not JSON: a proxy's error page */ }
    if (!res.ok) {
      const e = new Error(data.error || msg("Failed"));
      e.final = res.status >= 400 && res.status < 500 && res.status !== 408 && res.status !== 429;
      throw e;
    }
    return data;
  }

  form.addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const file = input.files?.[0];
    if (!file) return fail(msg("Choose"));
    errBox.hidden = true;
    button.disabled = true;
    input.disabled = true;
    let id = "";
    let offset = 0;
    show(0, file.size);
    while (offset < file.size) {
      let data = null;
      for (let attempt = 0; ; attempt++) {
        try {
          data = await send(file, id, offset);
          break;
        } catch (e) {
          if (e.final || attempt >= 3) return fail(e.message);
          await new Promise((r) => setTimeout(r, 1500 * (attempt + 1)));
        }
      }
      id = data.id;
      offset = data.received;
      show(offset, file.size);
    }
    location.reload();
  });
})();
