// sap13ns passkeys (WebAuthn). Vanilla JS, no inline handlers (strict CSP).
//
//   form[data-passkey-register]  add a passkey (profile and 2FA pages)
//   [data-passkey-login]         sign in on the sign-in page (with autofill, a.k.a.
//                                conditional UI) or confirm a password login
//                                on its second step (data-mode="2fa"), at
//                                the addresses of data-begin and data-finish
//
// Every control is rendered hidden and only shown when the browser supports
// WebAuthn. Translated messages come from data-msg-* attributes.
(() => {
  "use strict";
  if (window.sapPasskeysLoaded) return;
  window.sapPasskeysLoaded = true;

  const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));
  const esc = (s) => String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
  const supported = typeof window.PublicKeyCredential === "function" && !!navigator.credentials && window.isSecureContext;

  function toast(category, title) {
    document.dispatchEvent(new CustomEvent("basecoat:toast", { detail: { config: { category, title: esc(title) } } }));
  }

  // ------------------------------------------------------------ base64url --
  function toBuf(s) {
    const b64 = s.replace(/-/g, "+").replace(/_/g, "/");
    const bin = atob(b64 + "===".slice((b64.length + 3) % 4));
    const out = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
    return out.buffer;
  }
  function toB64(buf) {
    const bytes = new Uint8Array(buf);
    let bin = "";
    for (let i = 0; i < bytes.length; i++) bin += String.fromCharCode(bytes[i]);
    return btoa(bin).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  }

  // The server sends the options as JSON (binary fields base64url-encoded);
  // the browser API wants ArrayBuffers.
  function creationOptions(o) {
    return Object.assign({}, o, {
      challenge: toBuf(o.challenge),
      user: Object.assign({}, o.user, { id: toBuf(o.user.id) }),
      excludeCredentials: (o.excludeCredentials || []).map((c) => Object.assign({}, c, { id: toBuf(c.id) })),
    });
  }
  function requestOptions(o) {
    const out = Object.assign({}, o, { challenge: toBuf(o.challenge) });
    if (o.allowCredentials) out.allowCredentials = o.allowCredentials.map((c) => Object.assign({}, c, { id: toBuf(c.id) }));
    return out;
  }
  // PublicKeyCredential -> the JSON shape go-webauthn parses. Written by hand
  // rather than with toJSON(), which some password-manager shims lack.
  function credentialJSON(cred) {
    const r = cred.response;
    const response = { clientDataJSON: toB64(r.clientDataJSON) };
    if (r.attestationObject) {
      response.attestationObject = toB64(r.attestationObject);
      if (typeof r.getTransports === "function") response.transports = r.getTransports();
    } else {
      response.authenticatorData = toB64(r.authenticatorData);
      response.signature = toB64(r.signature);
      if (r.userHandle) response.userHandle = toB64(r.userHandle);
    }
    let ext = {};
    try { ext = cred.getClientExtensionResults() || {}; } catch (_) {}
    return {
      id: cred.id, rawId: toB64(cred.rawId), type: cred.type,
      authenticatorAttachment: cred.authenticatorAttachment || undefined,
      response, clientExtensionResults: ext.credProps ? { credProps: ext.credProps } : {},
    };
  }

  // ---------------------------------------------------------------- fetch --
  async function post(url, body) {
    const headers = { "Content-Type": "application/json", Accept: "application/json" };
    const token = document.querySelector('input[name="csrf"]')?.value;
    if (token) headers["X-CSRF-Token"] = token;
    const res = await fetch(url, { method: "POST", credentials: "same-origin", headers, body: JSON.stringify(body) });
    let data = {};
    try { data = await res.json(); } catch (_) {}
    if (!res.ok) throw Object.assign(new Error(data.error || ""), { server: true, status: res.status, data });
    return data;
  }

  // Go to url; a link that only changes the #fragment would not reload the
  // page (and its list of passkeys), so reload explicitly.
  function go(url) {
    const target = new URL(url || location.href, location.href);
    if (target.origin !== location.origin) return;
    if (target.pathname === location.pathname && target.search === location.search) {
      location.hash = target.hash;
      location.reload();
    } else {
      location.assign(target.href);
    }
  }

  function describe(err, el) {
    if (err && err.server) return ["error", err.message || el.dataset.msgFailed];
    switch (err && err.name) {
      case "NotAllowedError": case "AbortError": return ["warning", el.dataset.msgCancelled];
      case "InvalidStateError": return ["warning", el.dataset.msgExists || el.dataset.msgFailed];
      case "SecurityError": return ["error", el.dataset.msgInsecure];
      default: return ["error", el.dataset.msgFailed];
    }
  }

  // --------------------------------------------------------- registration --
  function initRegister(form) {
    form.hidden = false;
    const button = form.querySelector('button[type="submit"]');
    const reauth = form.querySelector("[data-passkey-reauth]");
    const password = form.querySelector('input[name="current_password"]');
    const name = form.querySelector('input[name="name"]');
    // Listening on the form runs before admin.js's document-level submit
    // handler, so preventDefault() keeps it from disabling the button.
    form.addEventListener("submit", async (e) => {
      e.preventDefault();
      if (button.disabled) return;
      button.disabled = true;
      try {
        const opts = await post("/admin/profile/passkeys/begin", { password: reauth && !reauth.hidden ? password.value : "" });
        const cred = await navigator.credentials.create({ publicKey: creationOptions(opts.publicKey) });
        if (!cred) throw new DOMException("", "NotAllowedError");
        const res = await post("/admin/profile/passkeys/finish", { name: name ? name.value : "", next: form.dataset.next || "", credential: credentialJSON(cred) });
        go(res.redirect);
        return; // stay disabled while the page changes
      } catch (err) {
        if (err.server && err.data && err.data.reauth && reauth) {
          reauth.hidden = false;
          password.value = "";
          password.focus();
        }
        const [kind, text] = describe(err, form);
        toast(kind, text);
      }
      button.disabled = false;
    });
  }

  // -------------------------------------------------------------- sign-in --
  let autofill = null; // AbortController of the pending conditional request
  let autofillBegin = null; // its begin request, so the button never races it

  function initLogin(box) {
    box.hidden = false;
    const mode = box.dataset.mode || "";
    box.querySelector("[data-passkey-signin]").addEventListener("click", () => signIn(box, mode, false));
    if (!mode) startAutofill(box);
  }

  async function startAutofill(box) {
    if (typeof PublicKeyCredential.isConditionalMediationAvailable !== "function") return;
    let ok = false;
    try { ok = await PublicKeyCredential.isConditionalMediationAvailable(); } catch (_) {}
    if (ok) signIn(box, "", true);
  }

  async function signIn(box, mode, conditional) {
    const button = box.querySelector("[data-passkey-signin]");
    if (!conditional) {
      if (button.disabled) return;
      button.disabled = true;
      if (autofill) { autofill.abort(); autofill = null; }
      if (autofillBegin) await autofillBegin.catch(() => {});
    }
    const ctrl = new AbortController();
    let opts = null, cred = null, leaving = false;
    try {
      const begin = post(box.dataset.begin, { mode });
      if (conditional) { autofill = ctrl; autofillBegin = begin; }
      opts = await begin;
      if (conditional) autofillBegin = null;
      if (ctrl.signal.aborted) return;
      const req = { publicKey: requestOptions(opts.publicKey), signal: ctrl.signal };
      if (conditional) req.mediation = "conditional";
      cred = await navigator.credentials.get(req);
      if (!cred) throw new DOMException("", "NotAllowedError");
      if (autofill === ctrl) autofill = null;
      const res = await post(box.dataset.finish, { next: box.dataset.next || "", credential: credentialJSON(cred) });
      leaving = true;
      go(res.redirect || "/admin/");
      return;
    } catch (err) {
      if (conditional) autofillBegin = null;
      // Superseded by the button, or a browser-side failure of the silent
      // autofill request: nothing to tell the user.
      if (!err.server && (ctrl.signal.aborted || conditional)) return;
      if (err.server && err.data && err.data.unknown && cred && opts && typeof PublicKeyCredential.signalUnknownCredential === "function") {
        // Ask the passkey provider to forget a credential this site deleted.
        PublicKeyCredential.signalUnknownCredential({ rpId: opts.publicKey.rpId, credentialId: cred.id }).catch(() => {});
      }
      if (err.server && err.data && err.data.redirect && err.status === 401) { leaving = true; go(err.data.redirect); return; }
      const [kind, text] = describe(err, box);
      toast(kind, text);
    } finally {
      if (!conditional && !leaving) button.disabled = false;
    }
    if (!mode) startAutofill(box); // offer the autofill again after a failure
  }

  function init() {
    if (!supported) {
      $$("[data-passkey-unsupported]").forEach((p) => { if (p.textContent.trim()) p.hidden = false; });
      return;
    }
    $$("form[data-passkey-register]").forEach(initRegister);
    $$("[data-passkey-login]").forEach(initLogin);
  }
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", init);
  else init();
})();
