// Tests for the state logic of the collapsible sidebar groups
// (ui/static/nav-groups.js). The file is a classic script, so it runs in a
// context without a document, where it only exposes its helpers.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import vm from "node:vm";

const src = readFileSync(new URL("../../static/nav-groups.js", import.meta.url), "utf8");
const ctx = vm.createContext({});
vm.runInContext(src, ctx);
const { KEY, readCollapsed, writeCollapsed, initialCollapsed, toggled } = ctx.sap13nsNavGroups;
// Arrays made in the script's context have another Array.prototype.
const plain = (v) => JSON.parse(JSON.stringify(v));

function memoryStorage(init = {}) {
  const data = { ...init };
  return {
    data,
    getItem: (k) => (k in data ? data[k] : null),
    setItem: (k, v) => { data[k] = String(v); },
  };
}

const broken = {
  getItem() { throw new Error("SecurityError"); },
  setItem() { throw new Error("QuotaExceededError"); },
};

test("the group of the current page is always open on load", () => {
  const groups = ["network", "content", "appearance", "admin"];
  assert.deepEqual(plain(initialCollapsed(groups, ["content", "appearance"], "content")), ["appearance"]);
  assert.deepEqual(plain(initialCollapsed(groups, ["content", "appearance", "admin"], "admin")), ["content", "appearance"]);
  assert.deepEqual(plain(initialCollapsed(groups, ["admin"], "")), ["admin"], "no current group: the stored state");
  assert.deepEqual(plain(initialCollapsed(groups, [], "content")), []);
});

test("stored groups that are not on the page are ignored", () => {
  assert.deepEqual(plain(initialCollapsed(["content"], ["gone", "content"], "")), ["content"]);
});

test("folding and opening a group updates the stored list", () => {
  assert.deepEqual(plain(toggled([], "content", true)), ["content"]);
  assert.deepEqual(plain(toggled(["content"], "content", true)), ["content"], "no duplicate");
  assert.deepEqual(plain(toggled(["admin", "content"], "content", false)), ["admin"]);
  assert.deepEqual(plain(toggled(["admin"], "content", false)), ["admin"]);
});

test("the state round-trips through the storage", () => {
  const s = memoryStorage();
  assert.equal(writeCollapsed(s, ["content", "admin", "content"]), true);
  assert.equal(s.data[KEY], '["admin","content"]');
  assert.deepEqual(plain(readCollapsed(s)), ["admin", "content"]);
});

test("storage failures are tolerated", () => {
  assert.deepEqual(plain(readCollapsed(broken)), [], "getItem throws");
  assert.equal(writeCollapsed(broken, ["content"]), false, "setItem throws");
  assert.deepEqual(plain(readCollapsed(null)), [], "no storage at all");
  assert.equal(writeCollapsed(null, ["content"]), false);
  assert.deepEqual(plain(readCollapsed(memoryStorage())), [], "nothing stored yet");
});

test("unexpected stored values are ignored", () => {
  for (const v of ["{not json", '{"content":true}', '"content"', "42", "null"]) {
    assert.deepEqual(plain(readCollapsed(memoryStorage({ [KEY]: v }))), [], v);
  }
  assert.deepEqual(plain(readCollapsed(memoryStorage({ [KEY]: '["content",3,null,"admin"]' }))), ["content", "admin"]);
});
