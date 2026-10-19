// Tests for the pricing table's monthly/yearly switch (ui/static/plans.js),
// run against a few fake elements: the script only reads attributes,
// classes and events.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import vm from "node:vm";

const src = readFileSync(new URL("../../static/plans.js", import.meta.url), "utf8");

function element(doc, attrs = {}, dataset = {}) {
  const classes = new Set();
  const listeners = {};
  const el = {
    attrs: { ...attrs },
    dataset: { ...dataset },
    hidden: false,
    tabIndex: 0,
    getAttribute: (a) => (a in el.attrs ? el.attrs[a] : null),
    setAttribute: (a, v) => { el.attrs[a] = String(v); },
    classList: {
      add: (c) => classes.add(c),
      toggle: (c, on) => (on ? classes.add(c) : classes.delete(c)),
      contains: (c) => classes.has(c),
    },
    addEventListener: (type, fn) => { (listeners[type] ||= []).push(fn); },
    dispatch: (type, e) => { for (const fn of listeners[type] || []) fn(e); },
    focus: () => { doc.activeElement = el; },
    closest: (sel) => (sel === '[role="radio"]' && el.attrs.role === "radio" ? el : null),
  };
  return el;
}

// A table like the sap13ns theme's: two radios, two plan buttons.
function page() {
  const doc = { activeElement: null };
  const month = element(doc, { role: "radio", "aria-checked": "true" }, { interval: "month" });
  const year = element(doc, { role: "radio", "aria-checked": "false" }, { interval: "year" });
  const group = element(doc);
  group.hidden = true;
  group.querySelectorAll = () => [month, year];
  const pro = element(doc, { href: "/register" }, { hrefYear: "/register?plan=pro&interval=year" });
  const box = element(doc);
  box.querySelector = (sel) => (sel === "[data-plan-switch]" ? group : null);
  box.querySelectorAll = () => [pro];
  doc.querySelectorAll = (sel) => (sel === "[data-plans]" ? [box] : []);
  vm.runInNewContext(src, { document: doc });
  return { doc, box, group, month, year, pro };
}

test("the switch shows itself, monthly by default", () => {
  const { box, group, month, year, pro } = page();
  assert.equal(group.hidden, false);
  assert.ok(box.classList.contains("has-switch"));
  assert.ok(!box.classList.contains("is-yearly"));
  assert.equal(month.getAttribute("aria-checked"), "true");
  assert.equal(year.getAttribute("aria-checked"), "false");
  assert.equal(month.tabIndex, 0);
  assert.equal(year.tabIndex, -1);
  assert.equal(pro.getAttribute("href"), "/register");
});

test("choosing yearly shows the yearly prices and sends the buttons there", () => {
  const { box, group, month, year, pro } = page();
  group.dispatch("click", { target: year });
  assert.ok(box.classList.contains("is-yearly"));
  assert.equal(year.getAttribute("aria-checked"), "true");
  assert.equal(month.getAttribute("aria-checked"), "false");
  assert.equal(pro.getAttribute("href"), "/register?plan=pro&interval=year");
  group.dispatch("click", { target: month });
  assert.ok(!box.classList.contains("is-yearly"));
  assert.equal(pro.getAttribute("href"), "/register");
});

test("arrow keys, Home and End move the choice and the focus", () => {
  const { doc, box, group, month, year } = page();
  let prevented = 0;
  const key = (k) => group.dispatch("keydown", { key: k, preventDefault: () => prevented++ });
  month.focus();
  key("ArrowRight");
  assert.equal(doc.activeElement, year);
  assert.ok(box.classList.contains("is-yearly"));
  key("ArrowRight"); // wraps
  assert.equal(doc.activeElement, month);
  key("End");
  assert.equal(doc.activeElement, year);
  key("Home");
  assert.equal(doc.activeElement, month);
  assert.ok(!box.classList.contains("is-yearly"));
  key("Tab"); // not the group's
  assert.equal(prevented, 4);
});
