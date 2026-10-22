// The login screen's browser probe. loginHtml is imported from source —
// it is the exact production renderer — and the probe pages dress its
// output in the built dist stylesheets, so what gets measured is the
// screen that ships, not a fixture that can drift from it.
//
// What only a browser can see, and what this probe therefore owns:
//   - the authorizing sweep runs under no-preference and freezes (not
//     flattens) under reduce;
//   - the card's geometry holds across the outbound ceremony: the
//     pending row reserves the Google action's slot;
//   - the denied panel parses into a header row plus a reason line with
//     the header on its own line. The unit suite asserts on strings; a
//     dropped closing tag passes every string check and only the parsed
//     DOM and the resulting boxes catch it.
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { existsSync } from "node:fs";
import { extname, join, resolve } from "node:path";
import { browser as getBrowser } from "./browser.mjs";
import { createServer as createViteServer } from "vite";
import { loginHtml } from "../src/app/login.ts";

const dist = resolve(import.meta.dirname, "..", "dist");
if (!existsSync(join(dist, "index.html"))) {
  throw new Error("dist/index.html is missing — run pnpm build first");
}

// The stylesheets in the order the real index.html links them, so the
// cascade under measurement is the shipped one.
const stylesheetHrefs = [...(await readFile(join(dist, "index.html"), "utf8"))
  .matchAll(/href="(\/assets\/[^"]+\.css)"/g)].map((m) => m[1]);
if (stylesheetHrefs.length === 0) throw new Error("dist/index.html links no stylesheets");

const views = {
  idle: () => loginHtml({ mode: "oidc", state: "signed-out" }),
  away: () => loginHtml({ mode: "oidc", state: "authorizing" }),
  denied: () => loginHtml({ mode: "builtin", state: "denied", deniedEmail: "refused@evil.test" }),
};

const mime = {
  ".css": "text/css", ".html": "text/html", ".js": "text/javascript",
  ".svg": "image/svg+xml", ".png": "image/png", ".woff2": "font/woff2",
};

const modules = await createViteServer({
  configFile: resolve(import.meta.dirname, "../vite.config.ts"), root: resolve(import.meta.dirname, ".."),
  logLevel: "error", optimizeDeps: { noDiscovery: true, entries: [] },
  server: { middlewareMode: true, hmr: false },
});

const server = createServer(async (req, res) => {
  const url = new URL(req.url, "http://x");
  if (["/src/", "/node_modules/", "/@"].some(prefix => url.pathname.startsWith(prefix))) {
    modules.middlewares(req, res, () => { res.writeHead(404); res.end(); });
    return;
  }
  if (["/setup", "/probe/change", "/probe/password", "/probe/setup-race"].includes(url.pathname)) {
    const head = stylesheetHrefs.map(href => `<link rel="stylesheet" href="${href}">`).join("");
    const script = url.pathname === "/setup"
      ? `import { mountSetup } from "/src/app/login.ts"; await mountSetup(document.querySelector("#host"));`
      : url.pathname === "/probe/setup-race"
      // Two checks in flight on one page: the later, faster one wins and the
      // slower refusal must not replace its screen.
      ? `import { mountSetup } from "/src/app/login.ts"; const host = document.querySelector("#host"); history.replaceState(null, "", "/setup#token=slow-wrong"); window.firstCheck = mountSetup(host); history.replaceState(null, "", "/setup#token=fast"); await mountSetup(host);`
      : url.pathname === "/probe/change"
      ? `import { openPasswordChange } from "/src/app/password-form.ts"; openPasswordChange(document.querySelector("#host"));`
      : `import { loginHtml, bindLoginActions } from "/src/app/login.ts"; const host = document.querySelector("#host"); host.innerHTML = loginHtml({mode:"password",state:"signed-out"}); bindLoginActions(host,"password",() => { window.authenticated = true; });`;
    res.writeHead(200, { "Content-Type": "text/html", "Referrer-Policy": "no-referrer" });
    res.end(`<!doctype html><html lang="en" data-theme="dark"><head>${head}</head><body><div id="host"></div><script type="module">${script}</script></body></html>`);
    return;
  }
  if (url.pathname === "/") { res.writeHead(200, { "Content-Type": "text/html" }); res.end("Setup complete"); return; }
  const view = /^\/probe\/([a-z]+)$/.exec(url.pathname);
  if (view && views[view[1]]) {
    const head = stylesheetHrefs.map((href) => `<link rel="stylesheet" href="${href}">`).join("");
    // data-theme rides on <html> in production (main.ts sets it before
    // anything renders); without it the card's surface colours never
    // resolve against the dark ramp.
    res.writeHead(200, { "Content-Type": "text/html" });
    res.end(`<!doctype html><html lang="en" data-theme="dark"><head>${head}</head><body>${views[view[1]]()}</body></html>`);
    return;
  }
  const path = join(dist, url.pathname);
  if (!path.startsWith(dist) || !existsSync(path)) {
    res.writeHead(404);
    res.end("not found");
    return;
  }
  res.writeHead(200, { "Content-Type": mime[extname(path)] ?? "application/octet-stream" });
  res.end(await readFile(path));
});
await new Promise((resolveServer) => server.listen(0, "127.0.0.1", resolveServer));
const origin = `http://127.0.0.1:${server.address().port}`;

const settle = (page) => page.evaluate(() => document.fonts.ready);

const box = (page, selector) => page.evaluate((sel) => {
  const el = document.querySelector(sel);
  if (!el) return null;
  const r = el.getBoundingClientRect();
  return { x: r.x, y: r.y, width: r.width, height: r.height };
}, selector);

const rowStyle = (page, prop) => page.evaluate((p) => {
  const rows = document.querySelectorAll("svg.nfmark .nfrow");
  return [...rows].map((row) => getComputedStyle(row)[p]);
}, prop);

// Samples one row's computed opacity over time; an infinite wave is at
// a different phase on every sample, so a static value can only mean the
// animation is not running.
async function sampleRow(page, index, times, gapMs) {
  const samples = [];
  for (let i = 0; i < times; i++) {
    samples.push(parseFloat(await page.evaluate((idx) => {
      const rows = document.querySelectorAll("svg.nfmark .nfrow");
      return getComputedStyle(rows[idx]).opacity;
    }, index)));
    if (i < times - 1) await page.waitForTimeout(gapMs);
  }
  return samples;
}

let browser;
try {
  ({ browser } = await getBrowser());

  // Geometry first, at the desktop size where the card is widest and a
  // collapse is most visible: the card must not shrink and the sweeping mark
  // must not move when the Google action gives way to the pending row.
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 }, reducedMotion: "no-preference" });
  const page = await ctx.newPage();

  await page.goto(`${origin}/probe/idle`, { waitUntil: "networkidle" });
  await settle(page);
  const idleCard = await box(page, "section");
  const idleMark = await box(page, "svg.nfmark");
  const idleButton = await box(page, "[data-login-google]");
  assert.ok(idleCard && idleMark && idleButton, "the idle screen renders card, mark, and button");
  assert.deepEqual(await rowStyle(page, "animationName"), Array(5).fill("none"),
    "the resting mark does not animate");

  await page.goto(`${origin}/probe/away`, { waitUntil: "networkidle" });
  await settle(page);
  const awayCard = await box(page, "section");
  const awayMark = await box(page, "svg.nfmark");
  const pending = await box(page, "[data-login-pending]");
  assert.ok(pending, "the authorizing state reserves the action row");
  assert.ok(Math.abs(awayCard.height - idleCard.height) < 0.5,
    `the card must not change height across the ceremony: idle ${idleCard.height}px, authorizing ${awayCard.height}px`);
  assert.equal(awayCard.width, idleCard.width, "the card must not change width across the ceremony");
  assert.ok(Math.abs(awayMark.y - idleMark.y) < 0.5,
    `the sweeping mark must not move: idle y ${idleMark.y}px, authorizing y ${awayMark.y}px`);
  assert.ok(Math.abs(pending.height - idleButton.height) < 0.5 && Math.abs(pending.width - idleButton.width) < 0.5,
    `the pending row reserves the button's slot: button ${idleButton.width}x${idleButton.height}, reserved ${pending.width}x${pending.height}`);

  // The sweep itself: all five rows carry it and at least one row's
  // opacity visibly travels.
  assert.deepEqual(await rowStyle(page, "animationName"), Array(5).fill("nfsweep"),
    "every row carries the sweep while the ceremony is away");
  const swing = await sampleRow(page, 4, 4, 400);
  assert.ok(Math.max(...swing) - Math.min(...swing) > 0.05,
    `the mark must visibly sweep under no-preference; opacities sampled ${swing.join(", ")}`);

  // The denied panel: parse the real DOM, then read the boxes it lays
  // out. A missing closing tag nests the reason inside the header's flex
  // row, which squeezes the label onto two lines.
  await page.goto(`${origin}/probe/denied`, { waitUntil: "networkidle" });
  await settle(page);
  const denied = await page.evaluate(() => {
    const label = [...document.querySelectorAll("span")]
      .find((s) => s.textContent === "Access denied");
    const reason = [...document.querySelectorAll("span")]
      .find((s) => s.textContent.includes("is not a member of this workspace"));
    if (!label || !reason) return null;
    const header = label.parentElement;
    const headerBox = header.getBoundingClientRect();
    const labelBox = label.getBoundingClientRect();
    const reasonBox = reason.getBoundingClientRect();
    return {
      siblings: header.parentElement === reason.parentElement,
      headerOwnLine: headerBox.bottom <= reasonBox.top + 0.5,
      labelOneLine: labelBox.height <= 23,
    };
  });
  assert.ok(denied, "the denied panel renders its header label and its reason");
  assert.equal(denied.siblings, true,
    "the reason must be a sibling of the header row, not nested inside it");
  assert.equal(denied.headerOwnLine, true, "the header sits on its own line above the reason");
  assert.equal(denied.labelOneLine, true,
    "'Access denied' renders as one line, not squeezed inside the header's flex row");

  const setupToken = "synthetic-setup-token";
  const setupRequests = [];
  const refusals = {
    wrong: { status: 401 }, "slow-wrong": { status: 401 }, foreign: { status: 403 },
    busy: { status: 429, headers: { "Retry-After": "17" } },
  };
  const held = new Map();
  const hold = (token) => {
    let release;
    held.set(token, new Promise(resolveHold => { release = resolveHold; }));
    return () => release();
  };
  await ctx.route(`${origin}/auth/**`, async route => {
    const request = route.request();
    assert.equal(request.method(), "POST");
    assert.ok(!request.url().includes(setupToken), "setup token stays out of URLs");
    assert.equal(request.headers().referer, undefined, "no setup token enters a Referer");
    const body = request.postDataJSON();
    setupRequests.push({ path: new URL(request.url()).pathname, body, headers: request.headers() });
    const gate = held.get(body.token);
    held.delete(body.token);
    await gate;
    if (body.token === "offline" || body.password === "offline password") { await route.abort("internetdisconnected"); return; }
    await route.fulfill((body.password === "expired password" ? refusals.wrong : refusals[body.token]) ?? { status: 204 });
  });
  const tokenInput = page.getByLabel("Setup link or token", { exact: true });
  const pasteToken = async (value) => {
    await tokenInput.fill(value);
    await page.getByRole("button", { name: "Continue", exact: true }).click();
  };
  const tokenInputFocused = () => page.evaluate(() => {
    const input = document.activeElement;
    return input?.matches("[data-setup-token-input]") === true && input.value === "";
  });

  await page.goto(`${origin}/setup`);
  await page.getByRole("heading", { name: "Create your password" }).waitFor();
  await tokenInput.waitFor();
  assert.equal(await page.locator("[data-password-editor]").count(), 0, "missing links offer no password form");
  const missingLayout = await page.evaluate(() => {
    const message = [...document.querySelectorAll("p")].find(p => p.textContent.includes("Paste your setup link"));
    const command = document.querySelector("pre code");
    return {
      centered: message !== undefined && getComputedStyle(message).textAlign === "center",
      command: command?.textContent,
      commandLines: command?.getClientRects().length,
      commandOwnLine: command?.closest("pre")?.parentElement?.tagName === "DIV",
    };
  });
  assert.equal(missingLayout.centered, true, "the setup message is centered under the heading");
  assert.equal(missingLayout.command, "noise-deck password reset");
  assert.equal(missingLayout.commandLines, 1, "the reset command never breaks across lines");
  assert.equal(missingLayout.commandOwnLine, true, "the reset command sits in its own block");

  await pasteToken(`  ${origin}/setup#token=${setupToken}\n`);
  await page.getByRole("button", { name: "Create password", exact: true }).waitFor();
  assert.deepEqual(setupRequests.at(-1), {
    path: "/auth/setup/check", body: { token: setupToken }, headers: setupRequests.at(-1).headers,
  }, "a pasted link submits only its token");
  assert.ok(!(await page.content()).includes(setupToken), "a pasted token never enters the DOM");
  assert.equal(await page.getByLabel("New password", { exact: true }).evaluate(input => input === document.activeElement), true,
    "a successful paste puts focus in New password");

  await page.goto(`${origin}/setup?token=ignored`);
  await page.getByText("Paste your setup link or token to continue.").waitFor();
  assert.equal(await page.locator("[data-password-editor]").count(), 0, "query tokens do not authorize setup");
  assert.equal(setupRequests.length, 1, "query tokens are never submitted for setup");
  assert.equal(new URL(page.url()).search, "", "setup removes rejected query tokens from history");
  await pasteToken(`${origin}/setup`);
  await page.getByRole("alert").filter({ hasText: "cut short" }).waitFor();
  assert.equal(setupRequests.length, 1, "a link without its token is never submitted");

  // A fragment-only goto stays in the document; the reload runs the page.
  await page.goto(`${origin}/setup#token=wrong`);
  await page.reload();
  await page.getByRole("alert").filter({ hasText: "invalid or expired" }).waitFor();
  assert.ok((await page.locator("main").innerText()).includes("noise-deck password reset"));
  assert.ok(!(await page.locator("main").innerText()).includes("Restart Deck"));
  await page.reload();
  await page.getByText("Paste your setup link or token to continue.").waitFor();
  await tokenInput.waitFor();
  await pasteToken("wrong");
  await page.getByRole("alert").filter({ hasText: "invalid or expired" }).waitFor();
  await pasteToken("foreign");
  await page.getByRole("alert").filter({ hasText: "does not accept this address" }).waitFor();
  assert.equal(await tokenInputFocused(), true, "a refused paste keeps focus in an emptied input");
  assert.ok(!(await page.locator("main").innerText()).includes("noise-deck password reset"),
    "a refused address is not fixed by a new link");
  await pasteToken(`${origin}/setup#token=busy`);
  await page.getByRole("alert").filter({ hasText: "Try again in 17 seconds." }).waitFor();
  assert.equal(await tokenInputFocused(), true);
  await pasteToken("wrong");
  await page.getByRole("alert").filter({ hasText: "invalid or expired" }).waitFor();
  assert.equal(await tokenInputFocused(), true);
  await pasteToken("offline");
  await page.getByRole("alert").filter({ hasText: "Deck did not answer" }).waitFor();
  assert.equal(await tokenInputFocused(), true, "a check that never reached Deck keeps the input ready for a retry");
  assert.equal(await page.getByRole("button", { name: "Continue", exact: true }).isEnabled(), true);

  const releaseSlow = hold("slow");
  await page.goto(`${origin}/setup?probe=slow#token=slow`);
  await page.getByRole("status").filter({ hasText: "Checking your setup link" }).waitFor();
  assert.equal(await page.locator("[data-setup-token], [data-password-editor]").count(), 0,
    "while the link's own check runs there is no form to type into");
  releaseSlow();
  await page.getByRole("button", { name: "Create password", exact: true }).waitFor();

  const releaseRefusal = hold("slow-wrong");
  await page.goto(`${origin}/probe/setup-race`);
  await page.getByRole("button", { name: "Create password", exact: true }).waitFor();
  await page.getByLabel("New password", { exact: true }).fill("typed before the late answer");
  releaseRefusal();
  await page.evaluate(() => window.firstCheck);
  assert.equal(await page.getByRole("alert").count(), 0, "a late refusal does not replace a newer screen");
  assert.equal(await page.getByLabel("New password", { exact: true }).inputValue(), "typed before the late answer");

  await page.goto(`${origin}/setup?token=ignored#token=${setupToken}`);
  await page.getByRole("button", { name: "Create password", exact: true }).waitFor();
  assert.equal(new URL(page.url()).hash, "", "setup removes the token from browser history");
  assert.equal(new URL(page.url()).search, "", "setup also removes query tokens");
  assert.ok(!(await page.content()).includes(setupToken), "setup token never enters the DOM");
  assert.equal(await page.getByText("Use at least 8 characters.", { exact: true }).isVisible(), true,
    "the setup screen shows the length rule before you submit");
  await page.getByLabel("New password", { exact: true }).fill("expired password");
  await page.getByLabel("Confirm new password", { exact: true }).fill("expired password");
  await page.getByRole("button", { name: "Create password", exact: true }).click();
  await page.getByRole("status").filter({ hasText: "invalid or expired" }).waitFor();
  await page.getByLabel("New password", { exact: true }).fill("offline password");
  await page.getByLabel("Confirm new password", { exact: true }).fill("offline password");
  await page.getByRole("button", { name: "Create password", exact: true }).click();
  await page.getByRole("status").filter({ hasText: "Deck did not answer" }).waitFor();
  assert.equal(await page.getByRole("button", { name: "Create password", exact: true }).isEnabled(), true,
    "a save that never reached Deck can be retried");
  assert.equal(await page.getByLabel("New password", { exact: true }).isEditable(), true);
  await page.getByLabel("New password", { exact: true }).fill("new password");
  await page.getByLabel("Confirm new password", { exact: true }).fill("different");
  await page.getByRole("button", { name: "Create password", exact: true }).click();
  await page.getByRole("status").filter({ hasText: "do not match" }).waitFor();
  assert.equal(setupRequests.filter(request => request.path === "/auth/setup").length, 2);
  await page.getByLabel("Confirm new password", { exact: true }).fill("new password");
  await page.getByRole("button", { name: "Create password", exact: true }).click();
  await page.waitForURL(`${origin}/`);
  const submitted = setupRequests.findLast(request => request.path === "/auth/setup");
  assert.deepEqual(submitted.body, { token: setupToken, password: "new password" });

  await ctx.addCookies([{ name: "noisedeck_csrf", value: "fixture-csrf", url: origin }]);
  await page.goto(`${origin}/probe/change`);
  const change = page.getByRole("dialog", { name: "Change password" });
  await change.waitFor();
  assert.equal(await page.getByRole("dialog").count(), 1);
  await change.getByLabel("Current password", { exact: true }).fill("current password");
  await change.getByLabel("New password", { exact: true }).fill("changed password");
  await change.getByLabel("Confirm new password", { exact: true }).fill("changed password");
  await change.getByRole("button", { name: "Change password", exact: true }).click();
  await change.getByRole("status").filter({ hasText: "has been changed" }).waitFor();
  const changed = setupRequests.find(request => request.path === "/auth/password/change");
  assert.deepEqual(changed.body, { current_password: "current password", password: "changed password" });
  assert.equal(changed.headers["x-csrf-token"], "fixture-csrf");
  await change.getByRole("button", { name: "Close", exact: true }).click();
  assert.equal(await page.getByRole("dialog").count(), 0);

  await page.goto(`${origin}/probe/password`);
  await page.getByLabel("Password", { exact: true }).fill("legacy bootstrap password");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await page.waitForFunction(() => window.authenticated === true);
  assert.deepEqual(setupRequests.find(request => request.path === "/auth/password").body, { password: "legacy bootstrap password" });

  await ctx.close();

  // Reduced motion: the wave must stop without flattening — each row
  // frozen at its own phase offset, bottom row brightest.
  const reduced = await browser.newContext({ viewport: { width: 1280, height: 800 }, reducedMotion: "reduce" });
  const reducePage = await reduced.newPage();
  await reducePage.goto(`${origin}/probe/away`, { waitUntil: "networkidle" });
  await settle(reducePage);
  assert.deepEqual(await rowStyle(reducePage, "animationName"), Array(5).fill("none"),
    "no row animates under prefers-reduced-motion");
  const frozen = await sampleRow(reducePage, 0, 3, 400);
  assert.ok(frozen.every((o) => o === frozen[0]),
    `under reduce the mark is frozen, but its opacity moved: ${frozen.join(", ")}`);
  const still = (await rowStyle(reducePage, "opacity")).map(Number);
  assert.ok(still[0] < still[1] && still[1] < still[2] && still[2] < still[3] && still[3] < still[4] && still[4] === 1,
    `the freeze holds each row at its phase offset, bottom row brightest: ${still.join(", ")}`);
  await reduced.close();

  console.log("login: existing sign-in, setup, password change, sweep, reduced motion, and geometry PASS");
} finally {
  await browser?.close();
  server.close();
  await modules.close();
}
