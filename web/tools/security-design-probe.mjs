// Verifies the exported security prototype, not Deck's encryption or runtime.
// Usage: node web/tools/security-design-probe.mjs <corrected-export-directory>
// Cached dependencies in <export>/deps must match support.js's SRI declarations.
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { createServer } from "node:http";
import { mkdir, readFile, realpath, stat, writeFile } from "node:fs/promises";
import { extname, isAbsolute, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";

assert.ok(process.env.NOISE_BROWSER_WS, "NOISE_BROWSER_WS is required; this probe never launches a local browser");
assert.equal(process.argv.length, 3, "usage: node web/tools/security-design-probe.mjs <corrected-export-directory>");

const exportDir = await realpath(resolve(process.argv[2]));
const projectDir = resolve(fileURLToPath(new URL("../../", import.meta.url)));
const artifacts = join(projectDir, ".visual-diff", "security-design-probe");
await mkdir(artifacts, { recursive: true });

const { browser: connectBrowser } = await import("./browser.mjs");
const support = await readFile(join(exportDir, "support.js"), "utf8");
const dependencies = new Map();
for (const [name, filename, expectedURL] of [
  ["REACT", "react.js", "https://unpkg.com/react@18.3.1/umd/react.production.min.js"],
  ["REACT_DOM", "react_dom.js", "https://unpkg.com/react-dom@18.3.1/umd/react-dom.production.min.js"],
  ["BABEL", "babel.js", "https://unpkg.com/@babel/standalone@7.29.0/babel.min.js"],
]) {
  const declaredURL = new RegExp(`\\bvar ${name}_URL = "([^"]+)"`).exec(support)?.[1];
  const integrity = new RegExp(`\\bvar ${name}_SRI = "(sha384-[^"]+)"`).exec(support)?.[1];
  assert.equal(declaredURL, expectedURL, `${name}: unexpected CDN dependency`);
  const body = await readFile(join(exportDir, "deps", filename));
  assert.equal(`sha384-${createHash("sha384").update(body).digest("base64")}`, integrity,
    `${filename}: cached bytes must match the export's integrity declaration`);
  dependencies.set(expectedURL, body);
}

const mime = {
  ".html": "text/html", ".js": "text/javascript", ".css": "text/css",
  ".svg": "image/svg+xml", ".png": "image/png", ".woff2": "font/woff2", ".txt": "text/plain",
};
const serverErrors = [];
const server = createServer(async (req, res) => {
  try {
    const pathname = decodeURIComponent(new URL(req.url, "http://localhost").pathname);
    const file = await realpath(resolve(exportDir, `.${pathname}`));
    const fromRoot = relative(exportDir, file);
    if (fromRoot.startsWith("..") || isAbsolute(fromRoot) || !(await stat(file)).isFile()) {
      res.writeHead(404).end();
      return;
    }
    res.writeHead(200, { "Content-Type": mime[extname(file)] ?? "application/octet-stream" });
    res.end(await readFile(file));
  } catch (error) {
    if (error.code !== "ENOENT") serverErrors.push(error.message);
    res.writeHead(404).end();
  }
});
await new Promise((done) => server.listen(0, "127.0.0.1", done));
const origin = `http://127.0.0.1:${server.address().port}`;
const forbiddenRequests = [];
const pageErrors = [];
const results = [];
let browser;

async function contextFor(viewport) {
  const context = await browser.newContext({ viewport, acceptDownloads: true, serviceWorkers: "block" });
  await context.grantPermissions(["clipboard-read", "clipboard-write"], { origin });
  await context.route("**/*", async (route) => {
    const url = route.request().url();
    const dependency = dependencies.get(url);
    if (dependency !== undefined) {
      await route.fulfill({ status: 200, contentType: "text/javascript", body: dependency,
        headers: { "Access-Control-Allow-Origin": "*" } });
    } else if (new URL(url).origin === origin) {
      await route.continue();
    } else {
      forbiddenRequests.push(url);
      await route.abort("blockedbyclient");
    }
  });
  context.on("page", (page) => {
    page.setDefaultTimeout(10_000);
    page.on("pageerror", (error) => pageErrors.push(error.message));
  });
  return context;
}

async function heading(frame, name) {
  await frame.getByRole("heading", { name, exact: true }).waitFor();
}

async function screenText(frame, expected) {
  await frame.getByText(expected, { exact: true }).waitFor();
}

async function importFile(page, frame, payload) {
  const chooser = page.waitForEvent("filechooser");
  await frame.getByRole("button", { name: "Import kit file", exact: true }).click();
  await (await chooser).setFiles(payload);
}

async function screenshot(frame, name) {
  await frame.screenshot({ path: join(artifacts, `${name}.png`), animations: "disabled" });
}

async function exerciseFlow(device) {
  const context = await contextFor(device === "desktop"
    ? { width: 1280, height: 1100 } : { width: 441, height: 1150 });
  try {
    const page = await context.newPage();
    await page.goto(`${origin}/Workspace%20Security%20Flow.dc.html`, { waitUntil: "networkidle" });
    const frame = page.locator(".bp-ink-grid");
    await heading(frame, "Set up atlas-7");
    await page.evaluate(() => document.fonts.ready);
    await page.getByRole("button", { name: device === "desktop" ? "Desktop" : "Mobile", exact: true }).click();
    const size = await frame.boundingBox();
    assert.equal(size.width, device === "desktop" ? 1180 : 393, "prototype frame width");
    assert.equal(size.height, device === "desktop" ? 760 : 852, "prototype frame height");

    const password = `prototype ${device} password`;
    await frame.getByRole("button", { name: "Sign in with Google to continue", exact: true }).click();
    await frame.getByRole("button", { name: "Use a password instead", exact: true }).click();
    await frame.locator('input[type="password"]').fill(password);
    await frame.getByRole("button", { name: "Set password and check it", exact: true }).click();
    await heading(frame, "Checking this password");
    await frame.getByRole("button", { name: "Continue — the check succeeds", exact: true }).click();
    await heading(frame, "This password unlocked atlas-7");
    await frame.getByRole("button", { name: "Continue to your recovery key", exact: true }).click();
    await heading(frame, "Save your recovery key");

    // A rejected clipboard write must not claim success. The next write is
    // held until released, then uses Chromium's real clipboard operation.
    await page.evaluate(() => {
      window.__probeWriteClipboard = navigator.clipboard.writeText.bind(navigator.clipboard);
      Object.defineProperty(navigator.clipboard, "writeText", { configurable: true,
        value: async () => { throw new Error("simulated clipboard refusal"); } });
    });
    await frame.getByRole("button", { name: "Copy key", exact: true }).click();
    await frame.getByRole("button", { name: "Copy failed — select the key", exact: true }).waitFor();
    assert.equal(await frame.getByText("Key copied", { exact: true }).count(), 0);
    await page.evaluate(() => {
      Object.defineProperty(navigator.clipboard, "writeText", { configurable: true,
        value: (text) => new Promise((resolveWrite, rejectWrite) => {
          window.__probeReleaseClipboard = async () => {
            try { await window.__probeWriteClipboard(text); resolveWrite(); }
            catch (error) { rejectWrite(error); }
          };
        }) });
    });
    await frame.getByRole("button", { name: "Copy failed — select the key", exact: true }).click();
    await page.waitForFunction(() => typeof window.__probeReleaseClipboard === "function");
    assert.equal(await frame.getByText("Key copied", { exact: true }).count(), 0,
      "pending clipboard writes must not be reported as copied");
    await page.evaluate(() => window.__probeReleaseClipboard());
    await frame.getByRole("button", { name: "Key copied", exact: true }).waitFor();
    const copiedKey = await page.evaluate(() => navigator.clipboard.readText());
    assert.match(copiedKey, /^(?:[A-Z0-9]{4} ){13}[A-Z0-9]{4}$/);
    await page.evaluate(() => {
      Object.defineProperty(navigator.clipboard, "writeText", { configurable: true, value: window.__probeWriteClipboard });
    });

    const downloading = page.waitForEvent("download");
    await frame.getByRole("button", { name: "Download recovery kit", exact: true }).click();
    const download = await downloading;
    assert.equal(download.suggestedFilename(), "atlas-7-recovery-kit-EXAMPLE.txt");
    const kitPath = join(artifacts, `${device}-recovery-kit-EXAMPLE.txt`);
    await download.saveAs(kitPath);
    assert.equal(await download.failure(), null);
    const kit = await readFile(kitPath, "utf8");
    assert.ok(kit.startsWith("Deck recovery kit — EXAMPLE, not a real key\n"));
    assert.ok(kit.includes(`\nRecovery key\n${copiedKey}\n`), "download must contain the displayed/copied example key");
    assert.ok(kit.includes("atlas-7.deck.example"));
    await frame.getByRole("button", { name: "Download requested", exact: true }).waitFor();
    await frame.getByRole("button", { name: "I have saved it — check it works", exact: true }).click();
    await heading(frame, "Check your recovery key");

    // Import both an invalid file and a valid kit whose key differs. Seeing
    // that other key in the field proves Import reads the supplied file.
    await importFile(page, frame, { name: "invalid.txt", mimeType: "text/plain", buffer: Buffer.from("not a recovery kit") });
    await screenText(frame, "Choose the example recovery kit downloaded from this prototype.");
    const changedKey = `${copiedKey[0] === "A" ? "B" : "A"}${copiedKey.slice(1)}`;
    await importFile(page, frame, { name: "changed-example.txt", mimeType: "text/plain",
      buffer: Buffer.from(kit.replace(copiedKey, changedKey)) });
    await page.waitForFunction((expected) => document.querySelector(".bp-ink-grid textarea")?.value === expected, changedKey);
    await frame.getByRole("button", { name: "Check key", exact: true }).click();
    await screenText(frame, "This key did not unlock atlas-7.");
    await importFile(page, frame, { name: download.suggestedFilename(), mimeType: "text/plain", buffer: Buffer.from(kit) });
    await page.waitForFunction((expected) => document.querySelector(".bp-ink-grid textarea")?.value === expected, copiedKey);
    await frame.getByRole("button", { name: "Check key", exact: true }).click();
    await heading(frame, "atlas-7 is ready");
    await screenText(frame, "Encryption password");
    await screenshot(frame, `${device}-setup-complete`);
    await frame.getByRole("button", { name: "Open atlas-7", exact: true }).click();
    await frame.getByRole("button", { name: "Lock", exact: true }).waitFor();

    // Signing out does not restart enrollment or stop the running workspace.
    await frame.getByRole("button", { name: "Lock", exact: true }).click();
    await frame.getByRole("button", { name: /^Sign out / }).click();
    await frame.getByRole("button", { name: "Sign in with Google", exact: true }).click();
    await heading(frame, "Your workspace is running");
    assert.doesNotMatch(await frame.innerText(), /rewrite-scheduler|flaky-test-hunt|pnpm test|claude ·/,
      "new browser authorization must not expose session details");
    await frame.getByRole("button", { name: "Use encryption password", exact: true }).click();
    await heading(frame, "Authorize this browser");
    await frame.getByRole("button", { name: "Verify with passkey instead", exact: true }).click();
    await heading(frame, "Your workspace is running");
    await frame.getByRole("button", { name: "Use encryption password", exact: true }).click();
    await frame.locator('input[type="password"]').fill("wrong example password");
    await frame.getByRole("button", { name: "Authorize browser", exact: true }).click();
    await screenText(frame, "That password did not unlock atlas-7.");
    await frame.locator('input[type="password"]').fill(password);
    await frame.getByRole("button", { name: "Authorize browser", exact: true }).click();
    await frame.getByRole("button", { name: "Lock", exact: true }).waitFor();
    assert.doesNotMatch(await frame.innerText(), /Unlocking atlas-7|Starting the workspace/,
      "authorizing a browser must not enter workspace startup");
    await screenshot(frame, `${device}-authorized-running`);

    await frame.getByRole("button", { name: "Lock", exact: true }).click();
    await frame.getByRole("button", { name: /^Lock workspace / }).click();
    await frame.getByRole("button", { name: "Stop sessions and lock", exact: true }).click();
    await screenText(frame, "Locking atlas-7");
    assert.equal(await frame.getByRole("heading", { name: "atlas-7 is locked", exact: true }).count(), 0);
    await frame.getByRole("button", { name: "Continue — the server confirms it locked", exact: true }).click();
    await heading(frame, "atlas-7 is locked");
    await screenText(frame, "Storage closed at 16:41. Two sessions stopped.");
    await screenshot(frame, `${device}-lock-confirmed`);

    await frame.getByRole("button", { name: "Use encryption password", exact: true }).click();
    await frame.locator('input[type="password"]').fill("wrong example password");
    await frame.getByRole("button", { name: "Unlock", exact: true }).click();
    await screenText(frame, "That password did not unlock atlas-7.");
    await frame.locator('input[type="password"]').fill(password);
    await frame.getByRole("button", { name: "Unlock", exact: true }).click();
    await heading(frame, "Unlocking atlas-7");
    await frame.getByRole("button", { name: "Continue — the workspace starts", exact: true }).click();
    await frame.getByRole("button", { name: "Lock", exact: true }).waitFor();

    await page.getByRole("button", { name: "Lock unconfirmed", exact: true }).click();
    await heading(frame, "Could not confirm whether your workspace locked");
    assert.equal(await frame.getByRole("heading", { name: "atlas-7 is locked", exact: true }).count(), 0);
    await frame.getByRole("button", { name: "Reconnect — it had locked", exact: true }).click();
    await heading(frame, "atlas-7 is locked");
    await screenText(frame, "It had finished locking. Storage closed at 16:41.");
    await page.getByRole("button", { name: "Lock unconfirmed", exact: true }).click();
    await frame.getByRole("button", { name: "Reconnect — it is still running", exact: true }).click();
    await frame.getByRole("button", { name: "Lock", exact: true }).waitFor();

    await page.getByRole("button", { name: "Recover access", exact: true }).click();
    await importFile(page, frame, { name: download.suggestedFilename(), mimeType: "text/plain", buffer: Buffer.from(kit) });
    await page.waitForFunction((expected) => document.querySelector(".bp-ink-grid textarea")?.value === expected, copiedKey);
    await frame.getByRole("button", { name: "Unlock with this key", exact: true }).click();
    await frame.getByRole("button", { name: "Add an encryption password", exact: true }).click();
    await frame.locator('input[type="password"]').fill(`${password} replacement`);
    await frame.getByRole("button", { name: "Set password and check it", exact: true }).click();
    await heading(frame, "Checking this password");
    await frame.getByRole("button", { name: "Continue — the check succeeds", exact: true }).click();
    await frame.getByRole("button", { name: "Finish", exact: true }).click();
    await screenText(frame, "Replacement method checked");
    assert.match(await frame.innerText(), /existing recovery key keeps working until you replace it and check the new recovery key/);
    assert.equal(await frame.getByRole("heading", { name: "Save your recovery key", exact: true }).count(), 0);
    await page.getByRole("button", { name: "Recover access", exact: true }).click();
    await frame.locator("textarea").fill(copiedKey.replaceAll(" ", "").toLowerCase());
    await frame.getByRole("button", { name: "Unlock with this key", exact: true }).click();
    await frame.getByRole("button", { name: "Add a passkey", exact: true }).waitFor();

    assert.equal(await page.locator(".sc-logic-error,.sc-placeholder-error").count(), 0);
    results.push({ device, status: "passed" });
    console.log(`${device}: prototype flow passed`);
  } finally {
    await context.close();
  }
}

try {
  ({ browser } = await connectBrowser());
  for (const device of ["desktop", "mobile"]) await exerciseFlow(device);
  const context = await contextFor({ width: 1400, height: 1000 });
  try {
    const page = await context.newPage();
    await page.goto(`${origin}/Workspace%20Security.dc.html`, { waitUntil: "networkidle" });
    await page.locator('figure[data-screen-label="7m Recover on mobile"]').waitFor();
    await page.evaluate(() => document.fonts.ready);
    assert.equal(await page.locator("figure[data-screen-label]").count(), 52, "the complete security canvas renders");
    assert.equal(await page.locator(".sc-logic-error,.sc-placeholder-error").count(), 0);
    results.push({ canvasFigures: 52, status: "passed" });
  } finally {
    await context.close();
  }
  assert.deepEqual(pageErrors, [], "prototype pages must not raise uncaught exceptions");
  assert.deepEqual(forbiddenRequests, [], "all requests must remain local or use verified cached dependencies");
  assert.deepEqual(serverErrors, [], "fixture server must not encounter unexpected errors");
  await writeFile(join(artifacts, "report.json"), JSON.stringify({ scope: "design prototype only", results }, null, 2));
  console.log(`Security design prototype verified; artifacts: ${artifacts}`);
  console.log("This does not verify production encryption, WebAuthn, storage isolation, or runtime locking.");
} finally {
  await browser?.close();
  await new Promise((done) => server.close(done));
}
