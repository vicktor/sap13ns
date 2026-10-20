// Tests for the motion library's player (ui/static/motion.js), run against
// fake elements and a fake IntersectionObserver: elements with one of the
// library's animations wait until they come into view, the children of
// data-sap-stagger get growing delays, and nothing runs without the class
// sap-motion on <html>.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import vm from "node:vm";

const src = readFileSync(new URL("../../static/motion.js", import.meta.url), "utf8");

function el(anim, attrs = {}) {
  const classes = new Set();
  const props = {};
  return {
    anim, attrs, children: [], dataset: { ...(attrs.delay ? { sapDelay: attrs.delay } : {}) },
    classList: { add: (c) => classes.add(c), remove: (c) => classes.delete(c), contains: (c) => classes.has(c), toggle: (c, on) => (on ? classes.add(c) : classes.delete(c)) },
    style: { setProperty: (k, v) => { props[k] = v; }, removeProperty: () => {} },
    props, classes, offsetWidth: 1,
  };
}

function page({ motion = true } = {}) {
  const html = el("");
  if (motion) html.classList.add("sap-motion");
  const title = el("sap-fade-up");
  const plain = el("none");
  const list = el("none", { stagger: true });
  list.children = [el("sap-pop"), el("sap-pop"), el("sap-pop")];
  const late = el("sap-fade", { delay: "300" });
  const all = [title, plain, ...list.children, late];
  const observed = [];
  let callback;
  const doc = {
    documentElement: html,
    querySelectorAll: (sel) => {
      if (sel === "[data-sap-stagger]") return [list];
      if (sel === "[data-sap-delay]") return [late];
      if (sel === ".sap-in") return all.filter((e) => e.classes.has("sap-in"));
      return all;
    },
  };
  const ctx = {
    document: doc, window: {},
    getComputedStyle: (e) => ({ animationName: e.anim }),
    matchMedia: () => ({ matches: false }),
    IntersectionObserver: class {
      constructor(cb) { callback = cb; }
      observe(e) { observed.push(e); }
      unobserve(e) { observed.splice(observed.indexOf(e), 1); }
      disconnect() { observed.length = 0; }
    },
  };
  ctx.window = ctx;
  vm.runInNewContext(src, ctx);
  const show = (...els) => callback(els.map((target) => ({ target, isIntersecting: true })));
  return { title, plain, list, late, observed, show, ctx, html };
}

test("motion: animated elements play when they come into view", () => {
  const p = page();
  assert.equal(p.observed.length, 5, "the title, the list's three children and the late one, not the plain element");
  assert.ok(!p.observed.includes(p.plain));
  p.show(p.title);
  assert.ok(p.title.classes.has("sap-in"));
  assert.ok(!p.observed.includes(p.title), "it plays once");
  assert.deepEqual(p.list.children.map((c) => c.props["--sap-delay"]), ["0.00s", "0.09s", "0.18s"]);
  assert.equal(p.late.props["--sap-delay"], "0.3s");
});

test("motion: nothing waits without sap-motion, and replay plays again", () => {
  const p = page({ motion: false });
  assert.equal(p.observed.length, 0);
  p.ctx.window.sapMotion.replay();
  assert.ok(p.html.classes.has("sap-motion"), "replay turns it on again");
  assert.equal(p.observed.length, 5);
  p.show(p.title);
  p.ctx.window.sapMotion.replay();
  assert.ok(!p.title.classes.has("sap-in"), "replay starts from the first frame");
  assert.equal(p.observed.length, 5);
});
