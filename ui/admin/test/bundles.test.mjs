// The committed JavaScript bundles (ui/static/*.min.js) match their sources:
// a source edited without npm run build fails here, as production serves the
// bundle, never the source. See scripts/js-bundles.mjs.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import * as esbuild from "esbuild";
import { bundles, options, committed } from "../../../scripts/js-bundles.mjs";

const root = new URL("../../../", import.meta.url).pathname;

for (const b of bundles) {
  test(`${b.out} is up to date (npm run build)`, async () => {
    const r = await esbuild.build({ ...options(root, b), write: false });
    assert.equal(r.outputFiles.length, 1);
    assert.ok(committed(root, b) === r.outputFiles[0].text, `${b.out} is stale: run npm run build and commit it`);
  });

  test(`${b.out} is CSP-safe and has no source map`, () => {
    const js = committed(root, b);
    assert.doesNotMatch(js, /sourceMappingURL/);
    assert.doesNotMatch(js, /\beval\s*\(|new Function\s*\(/);
    // Each source is in it, in order.
    assert.ok(js.startsWith(`/* sap13ns ${b.name} bundle: ${b.sources.join(", ")}`));
  });
}

test("the templates load the bundles, not their sources", () => {
  const head = readFileSync(new URL("../../shared/templates/head.html", import.meta.url), "utf8");
  const assistant = readFileSync(new URL("../../shared/templates/assistant.html", import.meta.url), "utf8");
  const layout = readFileSync(new URL("../templates/layout.html", import.meta.url), "utf8");
  for (const [name, html] of [["shared/head", head + assistant], ["admin layout", layout]]) {
    for (const b of bundles) {
      for (const s of b.sources) {
        assert.ok(!html.includes(`"${s}"`), `${name} loads ${s}, which ${b.out} holds`);
      }
    }
  }
  assert.match(head, /"site\.min\.js"/);
  assert.match(layout, /"admin\.min\.js"/);
});
