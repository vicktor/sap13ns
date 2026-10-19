// Tests for the cookie consent's choice logic (ui/static/consent.js). The
// file is a classic script: without a document it only exposes its helpers.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import vm from "node:vm";

const src = readFileSync(new URL("../../static/consent.js", import.meta.url), "utf8");
const ctx = vm.createContext({});
vm.runInContext(src, ctx);
const { VERSION, parse, serialize, allowed, decide, matches } = ctx.sap13nsConsent;
const plain = (v) => JSON.parse(JSON.stringify(v));

const optional = ["analytics", "media"];
const services = Object.assign(Object.create(null), { youtube: "media", vimeo: "media", stripe: "necessary" });
const now = Date.parse("2026-10-19T12:00:00Z");
const day = 864e5;
const stored = (o) => JSON.stringify({ v: VERSION, rev: "abc", cats: ["media"], svc: [], at: new Date(now - day).toISOString(), ...o });

test("a valid choice is read back", () => {
  const c = parse(stored({ svc: ["youtube", "nope"] }), "abc", 182, optional, services, now);
  assert.deepEqual(plain(c.cats), ["media"]);
  assert.deepEqual(plain(c.svc), ["youtube"], "unknown services are dropped");
  const round = parse(serialize(c, "abc"), "abc", 182, optional, services, now);
  assert.deepEqual(plain(round), plain(c));
});

test("a choice for another version, too old, from the future or broken asks again", () => {
  assert.equal(parse(stored({ rev: "old" }), "abc", 182, optional, services, now), null, "another revision");
  assert.equal(parse(stored({ v: 2 }), "abc", 182, optional, services, now), null, "another format");
  assert.equal(parse(stored({ at: new Date(now - 183 * day).toISOString() }), "abc", 182, optional, services, now), null, "expired");
  assert.equal(parse(stored({ at: new Date(now + 3 * day).toISOString() }), "abc", 182, optional, services, now), null, "future");
  assert.equal(parse(stored({ at: "yesterday" }), "abc", 182, optional, services, now), null, "bad date");
  assert.equal(parse("{not json", "abc", 182, optional, services, now), null);
  assert.equal(parse(null, "abc", 182, optional, services, now), null);
  assert.equal(parse("[]", "abc", 182, optional, services, now), null);
});

test("categories that are no longer offered are dropped, and cats may be undecided", () => {
  const c = parse(stored({ cats: ["media", "preferences", "media", 3] }), "abc", 182, optional, services, now);
  assert.deepEqual(plain(c.cats), ["media"]);
  const undecided = parse(stored({ cats: null, svc: ["vimeo"] }), "abc", 182, optional, services, now);
  assert.equal(undecided.cats, null);
  assert.deepEqual(plain(undecided.svc), ["vimeo"]);
});

test("what is allowed", () => {
  const none = { cats: null, svc: [] };
  assert.equal(allowed(none, "necessary", optional, services), true);
  assert.equal(allowed(none, "media", optional, services), false, "nothing before a choice");
  assert.equal(allowed(none, "youtube", optional, services), false);
  assert.equal(allowed(none, "stripe", optional, services), true, "a necessary service");
  assert.equal(allowed(none, "unknown", optional, services), false);
  assert.equal(allowed(none, "constructor", optional, services), false, "no prototype keys");
  const once = { cats: null, svc: ["youtube"] };
  assert.equal(allowed(once, "youtube", optional, services), true, "always allowed at a placeholder");
  assert.equal(allowed(once, "vimeo", optional, services), false, "only that service");
  assert.equal(allowed(once, "media", optional, services), false);
  const media = { cats: ["media"], svc: [] };
  assert.equal(allowed(media, "vimeo", optional, services), true, "its category");
  assert.equal(allowed(media, "analytics", optional, services), false);
});

test("a choice of categories keeps the services of accepted ones only", () => {
  const before = { cats: null, svc: ["youtube"], at: null };
  const reject = decide(before, [], services, now);
  assert.deepEqual(plain(reject), { cats: [], svc: [], at: new Date(now).toISOString() });
  const accept = decide(before, ["analytics", "media"], services, now);
  assert.deepEqual(plain(accept.svc), ["youtube"]);
  assert.deepEqual(plain(before.svc), ["youtube"], "input unchanged");
});

test("declared names match exactly or by prefix", () => {
  assert.equal(matches("_pk_id.*", "_pk_id.1.abcd"), true);
  assert.equal(matches("_pk_id.*", "_pk_ses.1"), false);
  assert.equal(matches("theme", "theme"), true);
  assert.equal(matches("theme", "themeMode"), false);
});
