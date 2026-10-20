import { test } from "node:test";
import assert from "node:assert/strict";
import { trimHistory } from "../../static/design-ai.js";

test("trimHistory keeps the last turns, starting with the user", () => {
  const h = [];
  for (let i = 0; i < 15; i++) h.push({ role: i % 2 ? "assistant" : "user", content: `m${i}` });
  const out = trimHistory(h);
  assert.equal(out.length, 11);
  assert.equal(out[0].role, "user");
  assert.equal(out.at(-1).content, "m14");
});

test("trimHistory drops empty and unknown turns", () => {
  const out = trimHistory([{ role: "system", content: "x" }, { role: "user", content: " " }, { role: "assistant", content: "a" }, { role: "user", content: "q" }]);
  assert.deepEqual(out, [{ role: "user", content: "q" }]);
});
