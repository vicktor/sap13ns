// sap13ns: notifications of new posts (internal/web/push.go). On only while
// the site offers them: shared/head then puts the site's public key on the
// bundle's script tag (data-sap-push), and shared/footer-meta the link
// "Notify me of new posts". The browser's permission is asked only after a
// click on a toggle: the footer's link, or any element with
// data-sap-push-toggle or the class sap-push-toggle (an HTML block's
// button). The worker (/sw.js) shows what the site sends.
(() => {
  "use strict";
  const TOGGLES = "[data-sap-push-toggle], .sap-push-toggle";
  const tag = document.querySelector("script[data-sap-push]");
  const can = "serviceWorker" in navigator && "PushManager" in window && "Notification" in window && window.top === window;
  if (!tag || !can) {
    // A button an author wrote does nothing here: it is not shown.
    for (const el of document.querySelectorAll(".sap-push-toggle")) el.hidden = true;
    return;
  }
  const text = { on: tag.dataset.sapPushOn || "", off: tag.dataset.sapPushOff || "", blocked: tag.dataset.sapPushBlocked || "" };
  const key = (() => {
    const b = atob(tag.dataset.sapPush.replace(/-/g, "+").replace(/_/g, "/"));
    return Uint8Array.from(b, (c) => c.charCodeAt(0));
  })();
  const send = (path, body) => fetch(path, { method: "POST", credentials: "omit", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
  const label = (t) => { for (const l of document.querySelectorAll("[data-sap-push-label]")) l.textContent = t; };
  const show = (subscribed) => {
    for (const w of document.querySelectorAll("[data-sap-push-wrap]")) w.hidden = false;
    for (const el of document.querySelectorAll(TOGGLES)) el.setAttribute("aria-pressed", String(subscribed));
    label(subscribed ? text.off : text.on);
  };
  const registration = () => navigator.serviceWorker.ready;

  registration().then((reg) => reg.pushManager.getSubscription()).then((sub) => show(Boolean(sub))).catch(() => {});

  let busy = false;
  document.addEventListener("click", async (e) => {
    const el = e.target.closest && e.target.closest(TOGGLES);
    if (!el) return;
    e.preventDefault();
    if (busy) return;
    busy = true;
    try {
      const reg = await registration();
      let sub = await reg.pushManager.getSubscription();
      if (sub) {
        await send("/api/push/unsubscribe", { endpoint: sub.endpoint }).catch(() => {});
        await sub.unsubscribe();
        show(false);
      } else if ((await Notification.requestPermission()) !== "granted") {
        label(text.blocked);
      } else {
        sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: key });
        const j = sub.toJSON();
        const r = await send("/api/push/subscribe", { endpoint: j.endpoint, keys: j.keys, lang: document.documentElement.lang || "" });
        if (!r.ok) {
          await sub.unsubscribe();
          throw new Error("not kept");
        }
        show(true);
      }
    } catch (_) {
      label(text.blocked);
    }
    busy = false;
  });
})();
