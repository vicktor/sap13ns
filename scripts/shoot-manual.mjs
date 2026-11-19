// Takes the screenshots of the user manual (docs/manual/img/*.jpg) from a
// throwaway development instance. It needs Node 22+ and Google Chrome, and no
// npm package: it drives headless Chrome over the DevTools protocol.
//
//	node scripts/shoot-manual.mjs [BASE_URL] [OUT_DIR]
//
// BASE_URL defaults to http://localhost:8163 and OUT_DIR to docs/manual/img.
// Set CHROME to the browser's path when it is not the macOS default.
//
// Never point it at a real site: it signs in as the devseed administrator. Make
// an instance for it, on its own database, port and data directory:
//
//	createdb sap13ns_manual
//	make build                        # the official plugins must be in the binary
//	cat > /tmp/manual.env <<EOF
//	SAP13NS_DATABASE_URL=postgres:///sap13ns_manual?host=/tmp
//	SAP13NS_SECRET_KEY=$(./bin/sap13ns keygen)
//	SAP13NS_BASE_URL=http://localhost:8163
//	SAP13NS_LISTEN=127.0.0.1:8163
//	SAP13NS_DEV=true
//	SAP13NS_DEV_SKIP_2FA=true
//	SAP13NS_DATA_DIR=/tmp/manual-data
//	SAP13NS_UPDATE_CHECK=false
//	EOF
//	./bin/sap13ns migrate -env /tmp/manual.env
//	go run ./scripts/devseed -env /tmp/manual.env
//	./bin/sap13ns serve -env /tmp/manual.env
//
// The analytics screenshot wants a month of visits. Request a few public pages
// with a browser's User-Agent, wait a minute for the collector to write them,
// and copy that day over the previous ones:
//
//	insert into analytics_days(day, views, visitors)
//	  select g::date, 40 + (extract(doy from g)::int * 37 % 160), 20 + (extract(doy from g)::int * 37 % 80)
//	  from generate_series(current_date - 29, current_date - 1, interval '1 day') g on conflict do nothing;
//
// Each screenshot is cropped to a fixed height. After changing one, update the
// width and height of its <img> in docs/manual/sap13ns-manual-es.html, and
// export the PDF again:
//
//	chrome --headless=new --no-pdf-header-footer \
//	  --print-to-pdf=docs/manual/sap13ns-manual-es.pdf docs/manual/sap13ns-manual-es.html
import { spawn } from 'node:child_process';
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const base = (process.argv[2] || 'http://localhost:8163').replace(/\/$/, '');
const outDir = process.argv[3] || 'docs/manual/img';
const chromePath = process.env.CHROME || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
const user = 'admin', password = 'dev-password-123'; // scripts/internal/devdata

const host = new URL(base).host;
if (!/^(localhost|127\.0\.0\.1)(:\d+)?$/.test(host)) {
  console.error(`refusing ${base}: the screenshots come from a local throwaway instance`);
  process.exit(2);
}
mkdirSync(outDir, { recursive: true });
const profile = mkdtempSync(join(tmpdir(), 'sap13ns-manual-'));
const port = 9347;
const chrome = spawn(chromePath, [
  '--headless=new', '--disable-gpu', '--hide-scrollbars', '--no-first-run', '--lang=es',
  `--remote-debugging-port=${port}`, `--user-data-dir=${profile}`, '--window-size=1280,800', 'about:blank',
], { stdio: 'ignore' });

const sleep = ms => new Promise(r => setTimeout(r, ms));
let ws, id = 0;
const pending = new Map();
const waiters = [];

async function findPage() {
  for (let i = 0; i < 50; i++) {
    try {
      const list = await (await fetch(`http://127.0.0.1:${port}/json`)).json();
      const page = list.find(t => t.type === 'page');
      if (page) return page.webSocketDebuggerUrl;
    } catch {}
    await sleep(200);
  }
  throw new Error('chrome did not start');
}

async function connect() {
  ws = new WebSocket(await findPage());
  await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
  ws.onmessage = ev => {
    const m = JSON.parse(ev.data);
    if (m.id && pending.has(m.id)) {
      const { res, rej } = pending.get(m.id);
      pending.delete(m.id);
      m.error ? rej(new Error(m.error.message)) : res(m.result);
    } else if (m.method) {
      for (const w of [...waiters]) {
        if (w.method === m.method) { waiters.splice(waiters.indexOf(w), 1); w.res(m.params); }
      }
    }
  };
}

const send = (method, params = {}) => new Promise((res, rej) => {
  pending.set(++id, { res, rej });
  ws.send(JSON.stringify({ id, method, params }));
});
const once = method => new Promise(res => waiters.push({ method, res }));
const js = async expr => (await send('Runtime.evaluate', { expression: expr, awaitPromise: true, returnByValue: true })).result.value;

async function go(path, wait = 700) {
  const loaded = once('Page.loadEventFired');
  await send('Page.navigate', { url: base + path });
  await loaded;
  await sleep(wait);
}

// The instance runs on localhost in dev mode: show a neutral address instead,
// and drop a notice that only dev mode prints for the active theme (it reloads
// the themes on every request, so the design screen takes the active one for
// another).
const tidy = `(() => {
  const host = ${JSON.stringify(host)};
  const fix = t => t.split('http://' + host).join('https://ejemplo.org').split(host).join('ejemplo.org')
    .replace(' Este no es el tema activo: lo que publiques se aplicará cuando se active.', '');
  const w = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT); let n;
  while ((n = w.nextNode())) { const v = fix(n.nodeValue); if (v !== n.nodeValue) n.nodeValue = v; }
  for (const i of document.querySelectorAll('input')) {
    if (i.type !== 'text' && i.type !== 'url') continue;
    const v = fix(i.value); if (v !== i.value) i.value = v;
  }
})()`;

// shot saves one screenshot, height CSS pixels tall: the whole window when main
// is false, else only the content to the right of the sidebar.
async function shot(name, height, { main = true, width = 1280 } = {}) {
  await send('Emulation.setDeviceMetricsOverride', { width, height, deviceScaleFactor: 1.75, mobile: false });
  await sleep(300);
  await js(tidy);
  // JPEG, because Chrome puts a JPEG in the PDF as it is and re-encodes any other format without loss, which is much larger.
  const params = { format: 'jpeg', quality: 90 };
  if (main) {
    const x = await js(`Math.round(document.getElementById('main')?.getBoundingClientRect().left || 0)`);
    params.clip = { x, y: 0, width: width - x, height, scale: 1 };
  }
  const { data } = await send('Page.captureScreenshot', params);
  writeFileSync(join(outDir, name + '.jpg'), Buffer.from(data, 'base64'));
  console.log(name.padEnd(12), String(Math.round(data.length * 3 / 4 / 1024)).padStart(4) + ' KB ', await js('location.pathname'));
}

try {
  await connect();
  await send('Page.enable');
  await send('Runtime.enable');
  await send('Network.enable');
  await send('Network.setExtraHTTPHeaders', { headers: { 'Accept-Language': 'es-ES,es;q=0.9' } });
  await send('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-color-scheme', value: 'light' }] });

  await go('/'); await shot('sitio', 760, { main: false });
  await go('/login'); await shot('login', 620, { main: false, width: 900 });
  const signedIn = once('Page.loadEventFired');
  await js(`(() => { document.getElementById('login').value=${JSON.stringify(user)}; document.getElementById('password').value=${JSON.stringify(password)}; document.querySelector('form').submit(); })()`);
  await signedIn; await sleep(900);

  await go('/admin/'); await shot('escritorio', 800, { main: false });
  if (await js('location.pathname') !== '/admin/') throw new Error('could not sign in: is this a devseed instance?');
  await go('/admin/posts'); await shot('entradas', 500);
  // A published post with some text, for the editor.
  const post = await js(`(() => { const a=[...document.querySelectorAll('a[href^="/admin/posts/"]')].find(a=>/\\/admin\\/posts\\/\\d+$/.test(a.getAttribute('href')) && a.textContent.includes('Un CMS')); return a ? a.getAttribute('href') : ''; })()`);
  if (!post) throw new Error('the sample post was not found');
  await go(post, 1500); await shot('editor', 650);
  await go('/admin/media'); await shot('medios', 575);
  await go('/admin/comments'); await shot('comentarios', 530);
  await go('/admin/appearance/themes'); await shot('temas', 600);
  await go('/admin/appearance/builder', 3000); await shot('diseno', 640);
  await go('/admin/appearance/menus'); await shot('menus', 585);
  await go('/admin/plugins'); await shot('plugins', 600);
  await go('/admin/users'); await shot('usuarios', 520);
  await go('/admin/settings'); await shot('ajustes', 600);
  await go('/admin/tools/backups'); await shot('copias', 540);
  await go('/admin/analytics', 1500); await shot('analitica', 790);
  await go('/admin/settings/ai'); await shot('ia', 620);
  await go('/admin/profile/2fa'); await shot('2fa', 530);
} catch (e) {
  console.error('ERROR', e.message);
  process.exitCode = 1;
} finally {
  try { ws.close(); } catch {}
  chrome.kill();
  await sleep(300);
  rmSync(profile, { recursive: true, force: true });
}
