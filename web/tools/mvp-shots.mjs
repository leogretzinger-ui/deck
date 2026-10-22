// Multi-state capture harness for the MVP workspace preview. Serves
// web/dist and iterates the URL-drivable state catalog
// (src/preview/preview-states.ts): each state is reached from a bare
// preview.html?<params> URL — no scripted interaction — and screenshotted as
// its own PNG under /workspace/shots/mvp-visual/<state>.png by default
// (durable; /tmp is wiped); override the directory with MVP_VISUAL_OUT for
// parallel lanes.
//
// A state that fails to reach itself fails loudly (non-zero exit): the page
// marks the reached state (data-preview-state) or the failure
// (data-preview-error), and a capture never falls back to the default view.
// Captures that come out byte-identical are reported as NOTEs: two
// "distinct" states with the same pixels means a driver did not change the
// UI. Writes manifest.json listing the states captured this run and removes
// stale <state>.png files so a state from an earlier run is never mistaken
// for a current capture. Non-zero exit: every reached state is re-captured
// through the same URL and must be pixel-identical to its first capture, or
// the harness itself is not trustworthy.
//
// Usage: node tools/mvp-shots.mjs
// Requires: dist/ built (pnpm build) and a browser — the agent-browser
// sidecar via NOISE_BROWSER_WS, or a local one (see tools/browser.mjs).
import { browser as getBrowser } from "./browser.mjs";
import { PREVIEW_STATES } from "../src/preview/preview-states.ts";
import { createServer } from "node:http";
import { readFile, writeFile, mkdir, readdir, unlink } from "node:fs/promises";
import { existsSync } from "node:fs";
import { extname, join, resolve } from "node:path";
import { createHash } from "node:crypto";
import { PNG } from "pngjs";

const WEB = resolve(import.meta.dirname, "..");
const DIST = join(WEB, "dist");
const OUT = process.env.MVP_VISUAL_OUT ?? "/workspace/shots/mvp-visual";
const VIEWPORT = { width: 1502, height: 818 };
const SCALE = 2; // deviceScaleFactor: captures are 3004x1636 px
const SETTLE_MS = 250;

// The capture loop's frozen clock. Stubbing Date.now on every page load
// keeps every wall-clock-dependent label identical across a run — an
// unwalled clock makes re-captures drift at minute boundaries (flaky
// determinism).
const CLOCK_MS = Date.parse("2026-08-02T12:00:00Z");

const MIME = {
  ".html": "text/html",
  ".js": "text/javascript",
  ".mjs": "text/javascript",
  ".css": "text/css",
  ".map": "application/json",
  ".svg": "image/svg+xml",
  ".png": "image/png",
  ".woff2": "font/woff2",
};

// Injected after a state is reached so no in-flight transition or animation
// lands mid-capture; the fixture content itself is static (no wall-clock
// dependence).
const ANTI_ANIMATION = [
  "*,*::before,*::after{",
  "animation:none!important;",
  "transition:none!important;",
  "}",
].join("");

class StateUnreachable extends Error {}

function serve(dir) {
  const server = createServer(async (req, res) => {
    const path = join(dir, decodeURIComponent(new URL(req.url, "http://x").pathname));
    if (!path.startsWith(dir) || !existsSync(path)) {
      res.writeHead(404);
      res.end("nf");
      return;
    }
    res.writeHead(200, { "Content-Type": MIME[extname(path)] ?? "application/octet-stream" });
    res.end(await readFile(path));
  });
  return new Promise((r) => server.listen(0, "127.0.0.1", () => r(server)));
}

async function settle(page) {
  await page.evaluate(() => document.fonts.ready);
  await page.waitForTimeout(SETTLE_MS);
}

async function loadPreview(page, port, state) {
  await page.setViewportSize(state.viewport ?? VIEWPORT);
  const query = new URLSearchParams(state.params).toString();
  await page.goto(
    `http://127.0.0.1:${port}/preview.html${query ? `?${query}` : ""}`,
    { waitUntil: "networkidle" },
  );
  // The preview marks the page once the requested state has rendered
  // (data-preview-state) or failed to (data-preview-error). Wait for the
  // marker rather than assuming the default view is the requested state.
  await page.waitForFunction(() => {
    const root = document.documentElement;
    return root.dataset.previewState !== undefined || root.dataset.previewError !== undefined;
  }, { timeout: 10_000 });
  const result = await page.evaluate(() => ({
    state: document.documentElement.dataset.previewState ?? null,
    error: document.documentElement.dataset.previewError ?? null,
  }));
  if (result.error !== null) {
    throw new StateUnreachable(`${state.name}: ${result.error}`);
  }
  if (result.state !== state.name) {
    throw new StateUnreachable(`${state.name}: reached ${result.state ?? "nothing"}`);
  }
  // Freeze transitions BEFORE capture: a capture that lands mid-transition
  // is a flake, not a state (panel transitions are verified by interaction
  // probes, not pixel captures).
  await page.addStyleTag({ content: ANTI_ANIMATION });
  await settle(page);
}

async function grab(page) {
  await page.addStyleTag({ content: ANTI_ANIMATION });
  await settle(page);
  return Buffer.from(await page.screenshot());
}

async function capture(page, name) {
  const path = join(OUT, `${name}.png`);
  await writeFile(path, await grab(page));
  console.log(`capture ${name} -> ${path}`);
  return path;
}

async function shot(page, port, state) {
  await loadPreview(page, port, state);
  await capture(page, state.name);
}

// The states to capture come from the URL-drivable catalog (PREVIEW_STATES),
// imported above — a state added there is captured here automatically.

// Pixels that differ between two PNGs (counted for the determinism proof).
function diffCount(a, b) {
  if (a.width !== b.width || a.height !== b.height) {
    throw new Error(`size mismatch in determinism check: ${a.width}x${a.height} vs ${b.width}x${b.height}`);
  }
  let n = 0;
  for (let i = 0; i < a.data.length; i += 4) {
    if (a.data[i] !== b.data[i] || a.data[i + 1] !== b.data[i + 1] ||
        a.data[i + 2] !== b.data[i + 2] || a.data[i + 3] !== b.data[i + 3]) {
      n += 1;
    }
  }
  return n;
}

async function main() {
  await mkdir(OUT, { recursive: true });
  const server = await serve(DIST);
  const port = server.address().port;
  const { browser, via } = await getBrowser();
  console.log(`browser: ${via}`);
  let deterministic = true;
  try {
    const page = await browser.newPage({ viewport: VIEWPORT, deviceScaleFactor: SCALE });
    // Runs before every page script on every navigation (including the
    // determinism re-loads), pinning wall time for the age labels.
    await page.addInitScript((ms) => { Date.now = () => ms; }, CLOCK_MS);
    const captured = [];
    for (const state of PREVIEW_STATES) {
      await shot(page, port, state);
      captured.push(state.name);

      // Determinism: re-load the same state from a fresh page load and
      // require pixel identity with the first capture.
      await loadPreview(page, port, state);
      const second = PNG.sync.read(await grab(page));
      const first = PNG.sync.read(await readFile(join(OUT, `${state.name}.png`)));
      const mismatched = diffCount(first, second);
      if (mismatched === 0) {
        console.log(`determinism: ${state.name} re-capture is pixel-identical (0 mismatched px)`);
      } else {
        deterministic = false;
        console.log(`determinism FAIL: ${state.name} re-capture differs in ${mismatched} px`);
      }
    }

    // Duplicate detection: two supposedly distinct states producing the same
    // pixels means a driver did not actually change the UI. Report it so the
    // loop never silently certifies a state that was not reached.
    const byHash = new Map();
    for (const name of captured) {
      const hash = createHash("md5").update(await readFile(join(OUT, `${name}.png`))).digest("hex");
      const group = byHash.get(hash) ?? [];
      group.push(name);
      byHash.set(hash, group);
    }
    for (const group of byHash.values()) {
      if (group.length > 1) {
        console.log(`NOTE identical captures: ${group.join(", ")} (state not distinct in current UI)`);
      }
    }

    // Isolate this run from earlier ones: drop <state>.png files not captured
    // now and record the current capture set for downstream tooling.
    for (const file of await readdir(OUT)) {
      if (!file.endsWith(".png")) continue;
      if (!captured.includes(file.slice(0, -4))) {
        await unlink(join(OUT, file));
        console.log(`remove stale capture ${file}`);
      }
    }
    await writeFile(
      join(OUT, "manifest.json"),
      JSON.stringify({ states: captured, capturedAt: new Date().toISOString() }, null, 2),
    );

    console.log(`captured ${captured.length}/${PREVIEW_STATES.length} states`);
  } finally {
    await browser.close();
    server.close();
  }
  if (!deterministic) process.exit(1);
}

await main();
