// "Update now" on the System page. The form is posted in JSON, then a
// blocking dialog asks /admin/system/update-status every few seconds until
// the server answers with a new version, or the update service reports that
// it failed or had nothing to install. While the site restarts nothing
// answers, or the proxy answers with an error page: both mean "wait".
(() => {
  const dlg = document.querySelector("[data-update-dialog]");
  if (!dlg) return;
  const form = document.querySelector("[data-update-form]");
  const $ = (sel) => dlg.querySelector(sel);
  const msg = (key, v) => (dlg.dataset["msg" + key[0].toUpperCase() + key.slice(1)] || "").replace("{v}", v || "");
  const every = 5000; // ms between checks
  const stallAfter = 90e3; // the request is still there: the service never started
  const slowAfter = 20 * 60e3;

  const text = (s) => { $("[data-update-text]").textContent = s; };
  const finish = (ok, title, detail) => {
    $("[data-update-spinner]").hidden = true;
    $("[data-update-actions]").hidden = false;
    if (ok) { text(title); setTimeout(() => location.reload(), 3000); return; }
    text("");
    $("[data-update-error-title]").textContent = title;
    $("[data-update-error-text]").textContent = detail || "";
    $("[data-update-error]").hidden = false;
  };

  // The dialog cannot be closed while the update runs: only the reload
  // button at the end leaves it.
  dlg.addEventListener("cancel", (e) => e.preventDefault());
  $("[data-update-reload]").addEventListener("click", () => location.reload());

  async function state() {
    try {
      const res = await fetch("/admin/system/update-status", { credentials: "same-origin", cache: "no-store", headers: { Accept: "application/json" } });
      if (!res.ok || !(res.headers.get("Content-Type") || "").includes("application/json")) return null;
      return await res.json();
    } catch (_) {
      return null; // the server is down for the restart
    }
  }

  // follow polls until the update ends. from is the version running when
  // it started; since, the time of the service's status then: only a newer
  // status is about this update.
  function follow(from, since, to) {
    dlg.showModal();
    text(msg("wait"));
    const started = Date.now();
    let slow = false;
    const tick = async () => {
      const st = await state();
      const newer = st && st.at && (!since || Date.parse(st.at) > Date.parse(since));
      if (st && st.version && st.version !== from) return finish(true, msg("done", st.version));
      if (newer && st.state === "failed") return finish(false, msg("failed"), st.error);
      if (newer && st.state === "current") return finish(true, msg("current"));
      if (st && st.requested && !newer && Date.now() - started > stallAfter) return finish(false, msg("failed"), msg("stalled"));
      if (!st) text(msg("restart"));
      else if (newer && st.state === "running") text(msg("installing", st.to || to));
      if (!slow && Date.now() - started > slowAfter) {
        slow = true;
        text(msg("slow"));
        $("[data-update-actions]").hidden = false;
      }
      setTimeout(tick, every);
    };
    setTimeout(tick, every);
  }

  form?.addEventListener("submit", async (e) => {
    e.preventDefault();
    const csrf = form.querySelector('input[name="csrf"]')?.value || "";
    let res, data = null;
    try {
      res = await fetch(form.action, { method: "POST", credentials: "same-origin", headers: { Accept: "application/json", "X-CSRF-Token": csrf } });
      data = await res.json();
    } catch (_) { /* answered below */ }
    if (!res || !res.ok || !data) {
      dlg.showModal();
      return finish(false, msg("failed"), data?.error || "");
    }
    follow(data.version, data.at || dlg.dataset.since, "");
  });

  // An update already under way when the page loads (it was reloaded).
  if (dlg.hasAttribute("data-update-waiting")) follow(dlg.dataset.from, dlg.dataset.since, "");
})();
