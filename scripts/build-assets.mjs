// Build-time asset pipeline. Outputs are committed and embedded in the Go
// binary, so production servers never need Node.js.
//
//   npm run build   -> icons, Basecoat JS, minified CSS for admin and every theme
//   npm run watch   -> rebuild CSS on template changes
import { spawn, spawnSync } from "node:child_process";
import { copyFileSync, existsSync, mkdirSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";

const root = new URL("..", import.meta.url).pathname;
const watch = process.argv.includes("--watch");

// 1. Lucide icons: keep only the inner markup; the Go side renders the <svg>.
function buildIcons() {
  const names = readFileSync(join(root, "scripts/icons.txt"), "utf8")
    .split("\n").map((l) => l.trim()).filter((l) => l && !l.startsWith("#"));
  const out = join(root, "internal/icons/lucide");
  rmSync(out, { recursive: true, force: true });
  mkdirSync(out, { recursive: true });
  for (const name of names) {
    const src = readFileSync(join(root, "node_modules/lucide-static/icons", `${name}.svg`), "utf8");
    const inner = src.replace(/<!--[\s\S]*?-->/g, "").replace(/^[\s\S]*?<svg[^>]*>/, "").replace(/<\/svg>[\s\S]*$/, "");
    writeFileSync(join(out, `${name}.svg`), inner.replace(/\s*\n\s*/g, "").trim() + "\n");
  }
  copyFileSync(join(root, "node_modules/lucide-static/LICENSE"), join(out, "LICENSE"));
  console.log(`icons: ${names.length}`);
}

// 2. Basecoat runtime + the interactive components used by the admin panel.
function copyBasecoatJS() {
  const out = join(root, "ui/static/vendor/basecoat");
  mkdirSync(out, { recursive: true });
  for (const f of ["basecoat.min.js", "dropdown-menu.min.js", "tabs.min.js", "toast.min.js", "sidebar.min.js", "popover.min.js", "select.min.js", "combobox.min.js"]) {
    copyFileSync(join(root, "node_modules/basecoat-css/dist/js", f), join(out, f));
  }
  copyFileSync(join(root, "node_modules/basecoat-css/LICENSE.md"), join(out, "LICENSE.md"));
  console.log("basecoat js: copied");
}

// 3. Tailwind CSS: admin panel and one stylesheet per theme.
function cssTargets() {
  const targets = [
    { name: "admin", input: "ui/admin/src/admin.css", output: "ui/static/admin.css" },
    // The base of the themes converted from WordPress (internal/themeimport).
    { name: "themeimport", input: "internal/themeimport/src/base.css", output: "internal/themeimport/base.css" },
  ];
  const themesDir = join(root, "ui/themes");
  for (const t of readdirSync(themesDir, { withFileTypes: true })) {
    const input = `ui/themes/${t.name}/src/theme.css`;
    if (t.isDirectory() && existsSync(join(root, input))) {
      targets.push({ name: `theme:${t.name}`, input, output: `ui/themes/${t.name}/static/theme.css` });
    }
  }
  return targets;
}

const tailwind = join(root, "node_modules/.bin/tailwindcss");

if (!process.argv.includes("--css-only")) {
  buildIcons();
  copyBasecoatJS();
}

if (process.argv.includes("--no-css")) process.exit(0);

const only = process.argv.find((a) => a.startsWith("--only="))?.slice(7);
const targets = cssTargets().filter((t) => !only || t.name === only || t.name === `theme:${only}`);

if (watch) {
  for (const t of targets) {
    spawn(tailwind, ["-i", t.input, "-o", t.output, "--watch"], { cwd: root, stdio: "inherit" });
  }
} else {
  let failed = false;
  for (const t of targets) {
    const r = spawnSync(tailwind, ["-i", t.input, "-o", t.output, "--minify"], { cwd: root, encoding: "utf8" });
    if (r.status !== 0) {
      failed = true;
      console.error(`css ${t.name}: FAILED\n${r.stderr}`);
    } else {
      const size = readFileSync(join(root, t.output)).length;
      console.log(`css ${t.name}: ${(size / 1024).toFixed(1)} KiB`);
    }
  }
  if (failed) process.exit(1);
}
