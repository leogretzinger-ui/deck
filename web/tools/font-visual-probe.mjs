// Compare unchanged font baselines: baseline dist, candidate dist, output dir.
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { readFile, mkdir, writeFile } from "node:fs/promises";
import { resolve, join, extname, sep } from "node:path";
import { PNG } from "pngjs";
import { browser as connectBrowser } from "./browser.mjs";
import { PREVIEW_STATES } from "../src/preview/preview-states.ts";

assert.ok(process.argv.length === 5 || (process.argv.length === 6 && process.argv[5] === "--workspace"), "expected baseline-dist candidate-dist output-dir [--workspace]");
const [baseline, candidate, output] = process.argv.slice(2, 5).map(p => resolve(p));
const config = JSON.parse(await readFile(new URL("../../.visual-diff/visual-diff.json", import.meta.url)));
const workspaceStates = {};
const desktop = ["baseline", "menu-settings", "menu-theme", "menu-project", "menu-session", "menu-projects", "dialog-new-session", "dialog-open-project", "dialog-choose-directory", "dialog-git-identity"];
const mobile = ["baseline", "sidebar-open", "dialog-new-session", "panel-files", "viewer-code", "panel-tasks", "menu-settings", "dialog-open-project", "menu-theme", "dialog-custom-theme", "task-detail", "recovery-restarting"];
for (const [device, names] of [["desktop", desktop], ["mobile", mobile]]) {
  for (const name of names) {
    const state = PREVIEW_STATES.find(s => s.name === name);
    assert.ok(state, name);
    const params = new URLSearchParams({ ...state.params, ...(device === "mobile" ? { frame: "phone" } : {}) });
    workspaceStates[`${device}-${name}`] = {
      route: { url: `http://localhost/preview.html?${params}` },
      viewport: device === "mobile" ? { width: 393, height: 852 } : { width: 1502, height: 818 },
      readiness: { selector: name === "baseline" ? "[data-workspace]" : state.ready, settle: 250 },
    };
  }
}
const states = process.argv[5] === "--workspace" ? workspaceStates : { ...config.states, ...workspaceStates };
const types = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".woff2": "font/woff2", ".svg": "image/svg+xml", ".png": "image/png" };
const servers = [];
async function serve(root) {
  const server = createServer(async (req, res) => {
    const pathname = new URL(req.url, "http://localhost").pathname;
    const file = resolve(root, `.${pathname === "/" ? "/index.html" : pathname}`);
    if (!file.startsWith(root + sep)) { res.writeHead(403); res.end(); return; }
    try { const body = await readFile(file); res.setHeader("Content-Type", types[extname(file)] ?? "application/octet-stream"); res.end(body); }
    catch { res.writeHead(404); res.end(); }
  });
  await new Promise(r => server.listen(0, "127.0.0.1", r));
  servers.push(server);
  return `http://127.0.0.1:${server.address().port}`;
}
await mkdir(output, { recursive: true });
const origins = [await serve(baseline), await serve(candidate)];
let browser;
const results = [];
try {
  ({ browser } = await connectBrowser());
  for (const [name, state] of Object.entries(states)) {
    const shots = [];
    for (const origin of origins) {
      const context = await browser.newContext({ viewport: { width: state.viewport.width, height: state.viewport.height }, deviceScaleFactor: 2 });
      try {
        const page = await context.newPage();
        await page.clock.setFixedTime(new Date("2026-09-22T12:00:00Z"));
        const fontErrors = [];
        page.on("response", r => { if (/\.woff2(?:\?|$)/.test(r.url()) && !r.ok()) fontErrors.push(r.url()); });
        const route = new URL(state.route.url);
        await page.goto(origin + route.pathname + route.search, { waitUntil: "networkidle" });
        if (state.readiness?.selector) await page.waitForSelector(state.readiness.selector, { timeout: state.readiness.timeout ?? 10000 });
        await page.evaluate(() => document.fonts.ready);
        await page.waitForTimeout(state.readiness?.settle ?? 250);
        assert.deepEqual(fontErrors, [], `${name}: failed font requests`);
        const failed = await page.evaluate(() => [...document.fonts].filter(f => f.status === "error").map(f => f.family));
        assert.deepEqual(failed, [], `${name}: fonts failed to decode`);
        // Compare complete viewports, with no font or terminal masks.
        shots.push(await page.screenshot({ animations: "disabled", caret: "hide" }));
      } finally { await context.close(); }
    }
    const a = PNG.sync.read(shots[0]); const b = PNG.sync.read(shots[1]);
    assert.equal(a.width, b.width); assert.equal(a.height, b.height);
    let pixels = 0;
    for (let i = 0; i < a.data.length; i += 4) if (!a.data.subarray(i, i + 4).equals(b.data.subarray(i, i + 4))) pixels++;
    results.push({ state: name, changedPixels: pixels, totalPixels: a.width * a.height });
    if (pixels) {
      await writeFile(join(output, `${name}-baseline.png`), shots[0]);
      await writeFile(join(output, `${name}-candidate.png`), shots[1]);
    }
    console.log(`${name}: ${pixels} changed pixels`);
  }
  assert.equal(results.filter(r => r.changedPixels).length, 0, "font selection changed visual baselines");
} finally {
  await writeFile(join(output, "results.json"), JSON.stringify(results, null, 2) + "\n");
  await browser?.close();
  await Promise.all(servers.map(server => new Promise(r => server.close(r))));
}
