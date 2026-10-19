// Tests for the select glue's pure helpers (ui/static/selects.js).
import { test } from "node:test";
import assert from "node:assert/strict";
import { fold, typeahead } from "../../static/selects.js";

test("fold lowers the case and drops accents", () => {
  assert.equal(fold("Árbol Ñandú"), "arbol nandu");
  assert.equal(fold(""), "");
  assert.equal(fold(null), "");
});

test("typeahead picks the first label that starts with the buffer", () => {
  const labels = ["Administrator", "Author", "Editor", "Éditeur", "Subscriber"];
  assert.equal(typeahead(labels, "au"), 1);
  assert.equal(typeahead(labels, "ed"), 2);
  assert.equal(typeahead(labels, "édi"), 2, "accents fold");
  assert.equal(typeahead(labels, "SUB"), 4, "case folds");
  assert.equal(typeahead(labels, "zz"), -1);
  assert.equal(typeahead(labels, ""), -1);
});

test("typeahead cycles through the labels with one repeated letter", () => {
  const labels = ["Administrator", "Author", "Editor", "Éditeur", "Subscriber"];
  assert.equal(typeahead(labels, "a", -1), 0);
  assert.equal(typeahead(labels, "a", 0), 1);
  assert.equal(typeahead(labels, "a", 1), 0, "wraps around");
  assert.equal(typeahead(labels, "ee", 2), 3, "the same letter twice moves on");
  assert.equal(typeahead(labels, "s", 4), 4, "the only match stays");
  assert.equal(typeahead(labels, "x", 0), -1);
});
