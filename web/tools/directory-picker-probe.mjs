// Real-browser probe for the directory picker's navigation contract (comp
// desktop-r39): a row cd's into its directory and the path line says so,
// Up climbs to the parent, and at a root's top Up is dead with one allowed
// root and returns to the mount listing when there are several — including
// when the picker was opened straight at a path, which never read the
// mount. The rendered markup cannot prove any of this; only the click
// handlers can, so this drives them against the built preview.
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { existsSync } from "node:fs";
import { extname, join, resolve } from "node:path";
import { browser as getBrowser } from "./browser.mjs";

const WEB = resolve(import.meta.dirname, "..");
const DIST = join(WEB, "dist");
const MIME = {
  ".css": "text/css",
  ".html": "text/html",
  ".js": "text/javascript",
  ".mjs": "text/javascript",
  ".png": "image/png",
  ".svg": "image/svg+xml",
  ".woff2": "font/woff2",
};

if (!existsSync(join(DIST, "preview.html"))) {
  throw new Error("dist/preview.html is missing — run `pnpm build` first");
}

function serve(dir) {
  const server = createServer(async (req, res) => {
    const path = join(dir, decodeURIComponent(new URL(req.url, "http://x").pathname));
    if (!path.startsWith(dir) || !existsSync(path)) {
      res.writeHead(404);
      res.end("not found");
      return;
    }
    res.writeHead(200, { "Content-Type": MIME[extname(path)] ?? "application/octet-stream" });
    res.end(await readFile(path));
  });
  return new Promise((resolveServer) =>
    server.listen(0, "127.0.0.1", () => resolveServer(server)));
}

async function load(page, origin, query) {
  await page.goto(`${origin}/preview.html${query}`, { waitUntil: "networkidle" });
  await page.waitForSelector("[data-directory-dialog]");
  await page.locator("[data-directory-list]").waitFor();
}

const pathLine = (page) => page.locator("[data-directory-path-line]");
const up = (page) => page.locator("[data-directory-parent]");
const rows = (page) => page.locator("[data-directory-path]");

async function expectStanding(page, path, label) {
  await page.locator(`[data-directory-path-line]:text-is("${path}")`).waitFor();
  assert.equal(await pathLine(page).textContent(), path, `${label}: the path line names where you stand`);
  assert.equal(await page.locator("[data-directory-select]").isDisabled(), false, `${label}: the primary is live in a directory`);
}

async function expectMount(page, label) {
  await page.locator('[data-directory-path="/srv/projects"]').waitFor();
  assert.equal(await pathLine(page).count(), 0, `${label}: the mount listing prints no path`);
  assert.equal(await page.locator("[data-directory-select]").isDisabled(), true, `${label}: nothing to choose at the mount`);
  assert.deepEqual(await rows(page).evaluateAll((nodes) => nodes.map((n) => n.dataset.directoryPath)),
    ["/workspace", "/srv/projects"], `${label}: the roots are the rows`);
}

const server = await serve(DIST);
const origin = `http://127.0.0.1:${server.address().port}`;
const { browser, via } = await getBrowser();
console.log(`browser: ${via}`);
try {
  const page = await browser.newPage({ viewport: { width: 1502, height: 818 } });

  // One root: the opener descends into it and there is nowhere above it.
  await load(page, origin, "?dialog=choose-directory");
  await expectStanding(page, "/workspace", "single root");
  assert.equal(await up(page).isDisabled(), true, "single root: Up is dead at the root's top");
  await page.locator('[data-directory-path="/workspace/field-notes"]').click();
  await expectStanding(page, "/workspace/field-notes", "after clicking a row");
  assert.deepEqual(await rows(page).evaluateAll((nodes) => nodes.map((n) => n.dataset.directoryPath)),
    ["/workspace/field-notes/docs", "/workspace/field-notes/src", "/workspace/field-notes/vendor"],
    "a row cd's into its directory: the rows are its children");
  assert.equal(await up(page).isDisabled(), false, "inside a directory Up is live");
  await up(page).click();
  await expectStanding(page, "/workspace", "after Up");
  assert.equal(await up(page).isDisabled(), true, "back at the top, Up is dead again");

  // Several roots: the mount is a listing of roots, and Up from a root's
  // top returns to it.
  await load(page, origin, "?dialog=choose-directory&roots=2");
  await expectMount(page, "two roots on open");
  await page.locator('[data-directory-path="/workspace"]').click();
  await expectStanding(page, "/workspace", "two roots, standing in the first");
  assert.equal(await up(page).isDisabled(), false, "two roots: Up is live at a root's top");
  await up(page).click();
  await expectMount(page, "Up from a root's top");
  await page.locator('[data-directory-path="/srv/projects"]').click();
  await expectStanding(page, "/srv/projects", "the second root");
  await page.locator('[data-directory-path="/srv/projects/staging"]').click();
  await expectStanding(page, "/srv/projects/staging", "inside the second root");
  await up(page).click();
  await expectStanding(page, "/srv/projects", "Up inside the second root climbs one level");

  await page.locator("[data-directory-select]").click();
  await page.locator("[data-directory-dialog]").waitFor({ state: "detached" });
  assert.equal(await page.locator("[data-project-path]").inputValue(), "/srv/projects",
    "Choose this directory took the directory being stood in");

  // The stranding case, on a fresh page: the path is already chosen and
  // Browse opens the picker straight at it, so this sidebar has never read
  // the mount. Up at the root's top has to discover that the mount holds
  // more, or the user is stuck on one root.
  await page.goto(`${origin}/preview.html?dialog=open-project&roots=2&project-path=/srv/projects`, { waitUntil: "networkidle" });
  await page.locator("[data-project-form]").waitFor();
  assert.equal(await page.locator("[data-project-path]").inputValue(), "/srv/projects", "the path is pre-chosen");
  await page.locator("[data-project-browse]").click();
  await page.locator("[data-directory-list]").waitFor();
  await expectStanding(page, "/srv/projects", "opened straight at the chosen path");
  assert.equal(await up(page).isDisabled(), false, "a picker that never read the mount still learns it has more roots");
  await up(page).click();
  await expectMount(page, "Up from a picker opened straight at a path");

  // An empty Folder field: Browse opens in the projects folder, the first
  // root, even when the mount offers more than one.
  await page.goto(`${origin}/preview.html?dialog=open-project&roots=2`, { waitUntil: "networkidle" });
  await page.locator("[data-project-form]").waitFor();
  assert.equal(await page.locator("[data-project-path]").inputValue(), "", "the field is empty");
  await page.locator("[data-project-browse]").click();
  await page.locator("[data-directory-list]").waitFor();
  await expectStanding(page, "/workspace", "Browse on an empty field");
  assert.equal(await up(page).isDisabled(), false, "the other roots stay one step up");

  console.log("directory-picker probe: all assertions passed");
} finally {
  await browser.close();
  server.close();
}
