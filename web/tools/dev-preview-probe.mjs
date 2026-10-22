// Real-browser proof that the gated /__dev/ preview works end to end.
//
// The prefix is the whole point and the whole risk: Vite must own the subtree
// (base), the Go backend must forward it without stripping it, and the HMR
// WebSocket must survive the same hop. Nothing about that is provable from a
// unit test — ReverseProxy carrying a 101 is, but a browser actually applying
// a hot update through a based dev server is not.
//
// It needs a backend binary, since the route lives in the Go server:
//   go build -o /tmp/noise-deck ./cmd/noisedeck
//   NOISEDECK_BINARY=/tmp/noise-deck pnpm test:dev-preview
//
// That is also why it is NOT in tools/run-probes.mjs: the probes CI job sets
// up pnpm and Node only, and every other probe serves its own fixture with no
// backend at all. Adding a Go toolchain and a binary build to that job for one
// probe is a change to make deliberately, not a side effect of this one.
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import net from "node:net";

if (process.env.PLAYWRIGHT_BROWSERS_PATH === undefined &&
    existsSync("/workspace/tools/ms-playwright")) {
  process.env.PLAYWRIGHT_BROWSERS_PATH = "/workspace/tools/ms-playwright";
}
const { browser: getBrowser } = await import("./browser.mjs");

const BASE = "/__dev/";
const webRoot = new URL("..", import.meta.url);
const repoRoot = new URL("../..", import.meta.url);
const binary = process.env.NOISEDECK_BINARY ?? "";
if (binary === "" || !existsSync(binary)) {
  console.error("dev-preview probe needs a backend binary: build one and set NOISEDECK_BINARY.");
  console.error("  go build -o /tmp/noise-deck ./cmd/noisedeck");
  process.exit(2);
}

const cssPath = new URL("src/styles/app.css", webRoot).pathname;
const cssOriginal = readFileSync(cssPath, "utf8");
// A property no rule in the app sets, on a selector that always exists, so a
// changed computed value can only have come from this edit.
const MARKER = "#__dev_probe_marker__";
const before = "rgb(1, 2, 3)";
const after = "rgb(4, 5, 6)";

const vitePort = await freePort();
const backendPort = await freePort();
const runDir = mkdtempSync(join(tmpdir(), "devpreview-"));
const children = [];
let failure = null;

try {
  writeFileSync(cssPath, `${cssOriginal}\nbody { border-color: ${before}; }\n`);

  children.push(spawn(process.execPath,
    ["node_modules/vite/bin/vite.js", "--host", "127.0.0.1", "--port", String(vitePort), "--strictPort"],
    { cwd: webRoot, stdio: ["ignore", "pipe", "pipe"],
      env: { ...process.env, NOISEDECK_DEV_PREVIEW_BASE: BASE } }));

  const configPath = join(runDir, "config.yaml");
  writeFileSync(configPath, [
    `server:`,
    `  bind: "127.0.0.1:${backendPort}"`,
    `  allowed_origins: ["http://127.0.0.1:${backendPort}"]`,
    `auth: { mode: "none" }`,
    `database: { path: "${join(runDir, "deck.db")}" }`,
    `tmux: { socket_path: "${join(runDir, "tmux.sock")}", session_prefix: "devprev-" }`,
    `projects: { allowed_roots: ["${runDir}"] }`,
    `experiments:`,
    `  dev_preview: true`,
    `  dev_preview_upstream: "127.0.0.1:${vitePort}"`,
    ``,
  ].join("\n"));
  children.push(spawn(binary, ["serve", "-config", configPath],
    { cwd: repoRoot, stdio: ["ignore", "pipe", "pipe"] }));

  const origin = `http://127.0.0.1:${backendPort}`;
  await waitForHTTP(`${origin}/livez`);

  const { browser, via } = await getBrowser();
  const page = await browser.newPage();
  const consoleLines = [];
  page.on("console", (m) => consoleLines.push(m.text()));
  // "[vite] connected" alone proves a socket, not its ROUTE: Vite's client
  // falls back to dialing the dev server directly, and this browser can reach
  // loopback, so a broken proxy could still log it. Record every socket URL
  // and require the one that carries HMR to be the backend's.
  const socketURLs = [];
  page.on("websocket", (ws) => socketURLs.push(ws.url()));
  // A hot update must not reload the page. Counting real navigations is what
  // separates HMR from "the browser gave up and fetched everything again".
  let navigations = 0;
  page.on("framenavigated", (f) => { if (f === page.mainFrame()) navigations += 1; });

  await page.goto(`${origin}${BASE}`, { waitUntil: "domcontentloaded" });

  // 1. The prefix survives the hop: Vite's own client is requested under it.
  const clientSrc = await page.getAttribute('script[src*="@vite/client"]', "src");
  assert.equal(clientSrc, `${BASE}@vite/client`,
    "Vite's client is not under the dev-preview base — the backend stripped the prefix or base is unset");

  // 2. The HMR socket connected THROUGH the backend. This is the one thing
  //    the Go unit tests cannot show.
  await page.waitForFunction(() => document.body !== null);
  await waitFor(() => consoleLines.some((l) => l.includes("[vite] connected")),
    () => `HMR never connected through ${BASE}. Console:\n  ${consoleLines.join("\n  ") || "(silent)"}`);

  // Checked here AND again after the hot update: a fallback socket opened
  // later would otherwise be recorded and never looked at, and the update
  // that arrives over it would count as proof of a route it did not use.
  const assertRouted = (when) => {
    const proxied = socketURLs.filter((u) => u.startsWith(`ws://127.0.0.1:${backendPort}${BASE}`));
    const direct = socketURLs.filter((u) => u.includes(`:${vitePort}`));
    assert.notEqual(proxied.length, 0,
      `no HMR socket went through the backend ${when}. Sockets opened: ${socketURLs.join(", ") || "(none)"}`);
    assert.deepEqual(direct, [],
      `the HMR client dialed Vite directly ${when}, so the proxied route is NOT what was proven: ${direct.join(", ")}`);
    return proxied[0];
  };
  const hmrSocket = assertRouted("on load");

  assert.equal(await borderColor(page), before, "the probe's own stylesheet did not load");
  const navigationsBeforeEdit = navigations;

  // 3. An edit reaches the browser, hot.
  writeFileSync(cssPath, `${cssOriginal}\nbody { border-color: ${after}; }\n`);
  await waitFor(async () => (await borderColor(page)) === after,
    async () => `the edit never arrived: border-color is ${await borderColor(page)}, want ${after}`);

  assert.equal(navigations, navigationsBeforeEdit,
    "the page reloaded instead of hot-updating — HMR is degraded to a full refresh");
  assertRouted("by the time the update arrived");

  console.log(`dev-preview probe PASSED (browser: ${via})`);
  console.log(`  base ${BASE} preserved through the backend proxy`);
  console.log(`  HMR socket ${hmrSocket} — through the backend, no direct fallback`);
  console.log(`  applied a CSS edit with no page reload`);
  await browser.close();
} catch (err) {
  failure = err;
} finally {
  writeFileSync(cssPath, cssOriginal);
  for (const child of children) child.kill("SIGKILL");
}
if (failure) { console.error(failure.message ?? failure); process.exit(1); }

function borderColor(page) {
  return page.evaluate(() => getComputedStyle(document.body).borderColor);
}

async function waitFor(condition, describe, timeoutMs = 20000) {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    if (await condition()) return;
    if (Date.now() > deadline) throw new Error(await describe());
    await new Promise((r) => setTimeout(r, 200));
  }
}

async function waitForHTTP(url, timeoutMs = 30000) {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    try {
      const res = await fetch(url);
      if (res.ok) return;
    } catch { /* not listening yet */ }
    if (Date.now() > deadline) throw new Error(`${url} never became ready`);
    await new Promise((r) => setTimeout(r, 250));
  }
}

function freePort() {
  return new Promise((resolve, reject) => {
    const srv = net.createServer();
    srv.on("error", reject);
    srv.listen(0, "127.0.0.1", () => {
      const { port } = srv.address();
      srv.close(() => resolve(port));
    });
  });
}
