// Tests for "Update now" on the System page (ui/static/updates.js), run
// against fake elements, a fake fetch and a clock the test moves: the
// dialog follows the update until the new version answers or it fails.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import vm from "node:vm";

const src = readFileSync(new URL("../../static/updates.js", import.meta.url), "utf8");

function element(attrs = {}) {
  const listeners = {};
  return {
    attrs: { ...attrs },
    hidden: false,
    textContent: "",
    hasAttribute(a) { return a in this.attrs; },
    addEventListener: (type, fn) => { (listeners[type] ||= []).push(fn); },
    dispatch: (type, e) => Promise.all((listeners[type] || []).map((fn) => fn(e))),
  };
}

// page builds the System page with the dialog; answers are what fetch
// returns, in order: an object is a JSON answer, null a server that is down.
function page({ waiting = false, since = "2026-10-20T10:00:00Z", answers = [] } = {}) {
  const parts = {};
  for (const k of ["[data-update-text]", "[data-update-spinner]", "[data-update-actions]", "[data-update-reload]", "[data-update-error]", "[data-update-error-title]", "[data-update-error-text]"]) parts[k] = element();
  parts["[data-update-actions]"].hidden = true;
  parts["[data-update-error]"].hidden = true;
  const dlg = element(waiting ? { "data-update-waiting": "" } : {});
  dlg.dataset = {
    from: "0.2.10", since,
    msgWait: "wait", msgInstalling: "installing {v}", msgRestart: "restarting", msgDone: "done {v}",
    msgCurrent: "current", msgFailed: "failed", msgStalled: "stalled", msgSlow: "slow",
  };
  dlg.open = false;
  dlg.showModal = () => { dlg.open = true; };
  dlg.querySelector = (sel) => parts[sel];
  const csrf = { value: "tok" };
  const form = element();
  form.action = "/admin/system/update";
  form.querySelector = () => csrf;
  const doc = { querySelector: (sel) => (sel === "[data-update-dialog]" ? dlg : sel === "[data-update-form]" ? form : null) };
  const clock = { now: 0, timers: [] };
  const calls = [];
  let reloads = 0;
  const fetch = async (url, opts) => {
    calls.push({ url, method: opts?.method || "GET", headers: opts?.headers });
    const a = answers.shift();
    if (a === null || a === undefined) throw new TypeError("down");
    return { ok: (a.status || 200) < 400, status: a.status || 200, headers: { get: () => "application/json" }, json: async () => a.body };
  };
  const ctx = {
    document: doc, fetch,
    location: { reload: () => { reloads++; } },
    setTimeout: (fn, ms) => { clock.timers.push({ at: clock.now + ms, fn }); },
    Date: { now: () => clock.now, parse: (s) => Date.parse(s) },
  };
  vm.runInNewContext(src, ctx);
  // step runs the next timer, moving the clock to it, and waits for it.
  const step = async () => {
    clock.timers.sort((a, b) => a.at - b.at);
    const t = clock.timers.shift();
    if (!t) return false;
    clock.now = t.at;
    await t.fn();
    await new Promise((r) => setImmediate(r));
    return true;
  };
  return { dlg, form, parts, calls, step, clock, reloads: () => reloads, text: () => parts["[data-update-text]"].textContent };
}

test("Update now: the dialog waits through the restart, then reloads on the new version", async () => {
  const p = page({ answers: [
    { status: 202, body: { version: "0.2.10", requested: true, state: "installed", at: "2026-10-20T10:00:00Z" } },
    { body: { version: "0.2.10", requested: false, state: "running", to: "0.2.11", at: "2026-10-20T10:05:00Z" } },
    null, // restarting
    { body: { version: "0.2.11", requested: false, state: "installed", to: "0.2.11", at: "2026-10-20T10:06:00Z" } },
  ] });
  await p.form.dispatch("submit", { preventDefault() {} });
  assert.equal(p.calls[0].method, "POST");
  assert.equal(p.calls[0].headers["X-CSRF-Token"], "tok");
  assert.ok(p.dlg.open, "the dialog opens");
  assert.equal(p.text(), "wait");
  await p.step();
  assert.equal(p.text(), "installing 0.2.11");
  await p.step();
  assert.equal(p.text(), "restarting");
  await p.step();
  assert.equal(p.text(), "done 0.2.11");
  assert.equal(p.parts["[data-update-error]"].hidden, true);
  await p.step(); // the reload after a moment
  assert.equal(p.reloads(), 1);
});

test("Update now: a failure of this update is shown, an older one is not", async () => {
  const p = page({ waiting: true, answers: [
    { body: { version: "0.2.10", requested: true, state: "failed", error: "old failure", at: "2026-10-20T10:00:00Z" } },
    { body: { version: "0.2.10", requested: false, state: "failed", error: "the backup failed", at: "2026-10-20T10:07:00Z" } },
  ] });
  assert.ok(p.dlg.open, "a page loaded during an update opens the dialog");
  await p.step();
  assert.equal(p.parts["[data-update-error]"].hidden, true, "the failure from before is not this update's");
  await p.step();
  assert.equal(p.parts["[data-update-error]"].hidden, false);
  assert.equal(p.parts["[data-update-error-text]"].textContent, "the backup failed");
  assert.equal(p.parts["[data-update-actions]"].hidden, false, "the reload button shows");
  assert.equal(p.reloads(), 0);
  assert.equal(await p.step(), false, "it stops asking");
});

test("Update now: a request the service never takes is reported", async () => {
  const waiting = { body: { version: "0.2.10", requested: true, state: "installed", at: "2026-10-20T10:00:00Z" } };
  const p = page({ waiting: true, answers: Array.from({ length: 30 }, () => waiting) });
  while (p.parts["[data-update-error]"].hidden && (await p.step()));
  assert.equal(p.parts["[data-update-error-text]"].textContent, "stalled");
  assert.ok(p.clock.now > 90e3 && p.clock.now < 120e3, `reported after ${p.clock.now} ms`);
});

test("Update now: a refused request says why", async () => {
  const p = page({ answers: [{ status: 409, body: { error: "not set up" } }] });
  await p.form.dispatch("submit", { preventDefault() {} });
  assert.ok(p.dlg.open);
  assert.equal(p.parts["[data-update-error-text]"].textContent, "not set up");
});
