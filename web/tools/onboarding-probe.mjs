// Shared production markup and gesture bindings; this fixture performs no setup.
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { readFile, stat } from "node:fs/promises";
import { extname, resolve, sep } from "node:path";
import { browser as connectBrowser } from "./browser.mjs";
import { ONBOARDING_PREVIEW_STATES } from "../src/preview/onboarding-states.ts";
const dist = resolve(import.meta.dirname, "../dist");
const mime = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".woff2": "font/woff2" };
const server = createServer(async (req, res) => {
  try {
    const file = resolve(dist, `.${new URL(req.url, "http://local").pathname}`);
    if (!file.startsWith(`${dist}${sep}`) || !(await stat(file)).isFile()) throw new Error();
    res.writeHead(200, { "Content-Type": mime[extname(file)] ?? "application/octet-stream" }); res.end(await readFile(file));
  } catch { res.writeHead(404); res.end(); }
});
await new Promise(done => server.listen(0, "127.0.0.1", done));
const origin = `http://127.0.0.1:${server.address().port}`;
let browser;
try {
  ({ browser } = await connectBrowser());
  const context = await browser.newContext({ colorScheme: "dark", reducedMotion: "reduce" });
  await context.route("**/*", route => new URL(route.request().url()).origin === origin ? route.continue() : route.abort());
  const page = await context.newPage();
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  const go = async id => { await page.goto(`${origin}/preview.html?onboarding=${id}`, { waitUntil: "networkidle" }); await page.locator(`[data-onboarding-preview="${id}"] h1`).waitFor(); };
  for (const state of ONBOARDING_PREVIEW_STATES) {
    await page.setViewportSize(state.viewport); await go(state.params.onboarding);
    assert.equal(await page.locator("html").getAttribute("data-preview-state"), state.name, "preview state matches its catalog entry");
    assert.equal(await page.locator("h1").count(), 1, state.name);
    assert.equal(await page.locator('x-import, [class*="bp-"]').count(), 0);
    const data = await page.evaluate(() => ({ overflow: document.documentElement.scrollWidth > innerWidth, caps: getComputedStyle(document.querySelector("h1")).fontVariantCaps, buttons: [...document.querySelectorAll("button")].filter(b => b.getBoundingClientRect().width).map(b => ({ label: b.textContent, height: b.getBoundingClientRect().height })) }));
    assert.equal(data.overflow, false, `${state.name}: overflow`); assert.equal(data.caps, "normal", state.name);
    if (state.viewport.width < 600) for (const b of data.buttons) assert.ok(b.height >= 44, `${state.name}: ${b.label}`);
  }
  await go("encryption-required");
  assert.equal(await page.getByRole("button", { name: "Continue without Deck encryption" }).count(), 0);
  await page.getByRole("button", { name: "Set up encryption" }).click();
  await page.getByRole("heading", { name: "Choose how to unlock atlas-7" }).waitFor();
  await go("encryption-unavailable");
  assert.equal(await page.getByRole("button", { name: "Enable workspace encryption" }).count(), 0);
  await page.getByRole("button", { name: "Finish setup" }).click();
  await page.getByRole("heading", { name: "Finishing setup" }).waitFor();
  assert.doesNotMatch(await page.locator("[data-onboarding]").innerText(), /saved unlock method/);
  assert.equal(await page.getByRole("button", { name: "Open atlas-7" }).count(), 0);
  await go("encryption");
  await page.getByRole("button", { name: "What this protects" }).click();
  assert.match(await page.locator("#onboarding-protection").innerText(), /does not encrypt everything in memory/);
  const lastDisclosure = page.locator("#onboarding-protection p").last();
  await lastDisclosure.scrollIntoViewIfNeeded();
  const disclosureBox = await lastDisclosure.boundingBox();
  assert.ok(disclosureBox && disclosureBox.y >= 0 && disclosureBox.y + disclosureBox.height <= page.viewportSize().height, "expanded privacy copy remains reachable within the scrolling setup surface");
  await page.getByRole("button", { name: "Enable workspace encryption" }).click();
  await page.getByRole("button", { name: "Use a password instead" }).click();
  await page.getByLabel("Encryption password", { exact: true }).fill("too short"); await page.getByLabel("Repeat it").fill("too short");
  await page.getByRole("button", { name: "Set password and check it" }).click();
  assert.match(await page.getByRole("alert").innerText(), /15 characters/);
  await go("create");
  assert.equal(await page.locator('[aria-busy="true"]').count(), 1);
  await page.getByRole("button", { name: "Cancel", exact: true }).click();
  await page.getByRole("heading", { name: "Choose how to unlock atlas-7" }).waitFor();
  assert.equal(await page.locator('[data-enrollment="passkey-pending"]').count(), 0);
  await go("resume"); await page.getByRole("button", { name: "Verify your saved unlock method" }).click();
  assert.equal(await page.locator('[data-enrollment="recovery-save"]').count(), 0);
  await go("migration");
  await page.getByRole("heading", { name: "Waiting for your existing data" }).waitFor();
  assert.match(await page.locator("[data-onboarding]").innerText(), /import does not require sharing them/);
  await page.getByRole("button", { name: "Check import status" }).click();
  assert.equal(await page.getByRole("heading", { name: "Workspace ready" }).count(), 0);
  await go("unconfirmed"); await page.getByRole("button", { name: "Reconnect and check" }).click();
  assert.equal(await page.getByRole("heading", { name: "Workspace ready" }).count(), 0);
  await go("unreachable");
  assert.equal(await page.getByRole("button").count(), 1);
  await go("google"); assert.equal(await page.getByRole("button", { name: "Sign in with Google" }).count(), 1);
  assert.equal(await page.getByText("Choose an account").count(), 0);
  assert.deepEqual(errors, []);
  await context.close();
  console.log(`PASS onboarding renderer: ${ONBOARDING_PREVIEW_STATES.length} states, policy branches, saved-method resume, existing password validation, observe-only uncertainty`);
} finally { await browser?.close(); await new Promise(done => server.close(done)); }
