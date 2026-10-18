// Reloads pages that show a running background job (data-poll="ms").
(() => {
  const el = document.querySelector("[data-poll]");
  if (!el) return;
  const ms = Math.max(1000, Number(el.dataset.poll) || 3000);
  setTimeout(() => { if (document.visibilityState === "visible") location.reload(); else document.addEventListener("visibilitychange", () => location.reload(), { once: true }); }, ms);
})();
