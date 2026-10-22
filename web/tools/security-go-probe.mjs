// Production UI/WebCrypto against the actual Go gateway, registry and grants.
// Only Google sign-in, native lifecycle and private application data are
// synthetic. No security endpoint is intercepted or replaced by this probe.
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { mkdirSync } from "node:fs";
import { resolve } from "node:path";
import { request as httpsRequest } from "node:https";
import { setTimeout as pause } from "node:timers/promises";
import { browser as connectBrowser } from "./browser.mjs";

const root = resolve(import.meta.dirname, "../..");
const migrating = process.env.DECK_WORKSPACE_BROWSER_MIGRATION === "1";
await readFile(resolve(root, "web/dist/index.html"));
mkdirSync(resolve(root, "run"), { recursive: true, mode: 0o700 });
const artifacts = await mkdtemp(resolve(root, "run/security-go-"));
const ready = resolve(artifacts, "ready.json");
const fixture = spawn("go", ["test", "-race", "./internal/workspacehost", "-run", "^TestBrowserGatewayFixture$", "-count=1", "-timeout=10m", "-v"], {
  cwd: root, env: { ...process.env, DECK_WORKSPACE_BROWSER_FIXTURE: ready }, stdio: ["ignore", "pipe", "pipe"], detached: true,
});
let fixtureOutput = "", exit;
for (const stream of [fixture.stdout, fixture.stderr]) stream.on("data", data => { fixtureOutput = (fixtureOutput + data.toString()).slice(-16_384); });
const exited = new Promise(resolveExit => fixture.once("close", (code, signal) => { exit = { code, signal }; resolveExit(exit); }));
fixture.once("error", error => { fixtureOutput = error.message; });
let browser, origin, probeFailure;
const contexts = [], pageErrors = [], requestFailures = [], mutations = [], challengeReads = [], challenges = new Map();
const passwords = ["fixture encryption password", "fixture replacement encryption password", "fixture spare encryption password"];
const recoveryKeys = [];
const apiFailures = [];
const previewSettingsPath = "/api/projects/fixture-project/preview/settings";

async function fixtureState(page) {
  return page.evaluate(async () => { const response = await fetch("/__fixture/observation"); if (!response.ok) throw new Error("fixture observation unavailable"); return response.json(); });
}
async function securityState(page) {
  return page.evaluate(async () => { const response = await fetch("/api/security/v1/state"); if (!response.ok) throw new Error("security observation unavailable"); return response.json(); });
}
async function methods(page) {
  return page.evaluate(async () => { const response = await fetch("/api/security/v1/unlock-options"); if (!response.ok) throw new Error("method observation unavailable"); return response.json(); });
}
async function backendStatus(page, path = "/api/projects") {
  return page.evaluate(async path => (await fetch(path)).status, path);
}
async function fixtureAction(page, action) {
  assert.equal(await page.evaluate(async path => (await fetch(path, { method: "POST" })).status, `/__fixture/${action}`), 204);
}
async function stopFixture() {
  if (!origin) return;
  // Cleanup is independent of browser connectivity. This fixed loopback
  // request accepts only the disposable fixture's self-signed certificate.
  await new Promise((resolveStop, rejectStop) => {
    const request = httpsRequest({ hostname: "127.0.0.1", port: new URL(origin).port, path: "/__fixture/done",
      method: "POST", rejectUnauthorized: false, timeout: 2000 }, response => {
      response.resume();
      response.once("end", () => response.statusCode === 204 ? resolveStop() : rejectStop(new Error("fixture refused shutdown")));
    });
    request.once("timeout", () => request.destroy(new Error("fixture shutdown timed out")));
    request.once("error", rejectStop);
    request.end();
  });
}
async function newPage() {
  const context = await browser.newContext({ ignoreHTTPSErrors: true, reducedMotion: "reduce", colorScheme: "dark", viewport: { width: 1120, height: 720 }, acceptDownloads: true });
  contexts.push(context);
  const page = await context.newPage();
  page.setDefaultTimeout(15_000);
  page.on("pageerror", error => pageErrors.push(error.message));
  page.on("request", request => {
    const url = new URL(request.url());
    if (url.origin !== origin && url.protocol !== "blob:") { requestFailures.push("request outside the local fixture"); return; }
    if (!url.pathname.startsWith("/api/security/v1/") || request.method() === "GET") return;
    const text = request.postData() ?? "";
    if (passwords.some(password => text.includes(password)) || recoveryKeys.some(key => text.includes(key)) || /"(?:password|recoverySecret|prfResult|proofPrivateKey)"\s*:/.test(text)) {
      requestFailures.push("a wrapping secret appeared in a security request");
    }
    const body = text ? JSON.parse(text) : {};
    if (url.pathname.endsWith("/operations")) {
      mutations.push({ challengeId: body.challengeId, hasVolumeSecret: Boolean(body.volumeSecret) });
    }
  });
  page.on("response", response => {
    if (new URL(response.url()).pathname.startsWith("/api/security/v1/") && response.status() >= 400) {
      apiFailures.push({ status: response.status(), resource: new URL(response.url()).pathname.replace(/[A-Za-z0-9_-]{30,}/g, "id") });
    }
    if (!response.url().endsWith("/api/security/v1/challenges") || response.status() !== 201) return;
    challengeReads.push((async () => {
      try { const challenge = await response.json(); challenges.set(challenge.id, response.request().postDataJSON()); }
      catch { requestFailures.push("challenge response could not be observed"); }
    })());
  });
  return page;
}
async function click(page, name) { await page.getByRole("button", { name, exact: true }).click(); }
async function fillPassword(page, password, button) {
  await page.getByLabel("Encryption password", { exact: true }).fill(password);
  await click(page, button);
}
async function admitted(page) {
  await page.locator("[data-live-more]").waitFor();
  assert.equal(await backendStatus(page), 200, "admission reaches the real private Unix proxy");
}
async function authorize(page, password, button = "Verify this browser") {
  await click(page, "Use encryption password");
  await fillPassword(page, password, button);
  await admitted(page);
}
async function manage(page) {
  await page.locator("[data-live-more]").click();
  await page.getByRole("button", { name: /Unlock methods/ }).click();
  await page.getByRole("heading", { name: "How browser-go-fixture is unlocked", exact: true }).waitFor();
}
async function authorizeChange(page, label, password) {
  await click(page, label);
  await fillPassword(page, password, "Verify and continue");
}
async function downloadKit(page, button) {
  const [download] = await Promise.all([page.waitForEvent("download"), click(page, button)]);
  const stream = await download.createReadStream();
  const chunks = [];
  for await (const chunk of stream) chunks.push(chunk);
  const bytes = Buffer.concat(chunks);
  const kit = JSON.parse(bytes.toString("utf8"));
  assert.ok(kit.format === "deck-recovery-kit" && kit.version === 1 && kit.instanceId === "browser-go-fixture", "download is a versioned kit for this installation");
  assert.ok(/^DK1-(?:[0-9A-HJKMNP-TV-Z]{4}-){13}[0-9A-HJKMNP-TV-Z]{4}$/.test(kit.recoveryKey), "download includes the canonical recovery key");
  recoveryKeys.push(kit.recoveryKey);
  await download.delete();
  return { bytes, kit };
}

try {
  const deadline = Date.now() + 240_000;
  while (!origin && Date.now() < deadline) {
    if (exit) throw new Error(`Go fixture exited before readiness: ${fixtureOutput}`);
    try { const metadata = JSON.parse(await readFile(ready, "utf8")); if (metadata.ready) origin = metadata.origin; } catch { /* The Go fixture has not published readiness yet. */ }
    if (!origin) await pause(100);
  }
  assert.ok(origin && /^https:\/\/localhost:\d+$/.test(origin), "Go fixture publishes a loopback HTTPS origin");
  ({ browser } = await connectBrowser());
  console.log(`Browser: Chromium ${browser.version()}`);
  const a = await newPage();
  await a.clock.install();
  await a.goto(`${origin}/__fixture/invitation`, { waitUntil: "domcontentloaded" });
  await a.locator('[data-onboarding="welcome"]').waitFor();
  assert.equal(new URL(a.url()).hash, "", "invitation is removed from navigation history before sign-in");
  await click(a, "Sign in with Google");
  await click(a, "Set up encryption");
  await click(a, "Use a password instead");
  await a.getByLabel("Encryption password", { exact: true }).fill(passwords[0]);
  await a.getByLabel("Repeat it", { exact: true }).fill(passwords[0]);
  await click(a, "Set password and check it");
  await a.getByRole("heading", { name: "Password checked", exact: true }).waitFor();
  assert.equal((await fixtureState(a)).privateRequests, 0, "enrollment does not mount private application data");
  await a.reload({ waitUntil: "domcontentloaded" });
  await click(a, "Verify your saved unlock method");
  await fillPassword(a, passwords[0], "Continue setup");
  await a.getByRole("heading", { name: "Password checked", exact: true }).waitFor();
  await a.getByLabel("Name this method", { exact: true }).fill("Browser fixture password");
  await click(a, "Continue to your recovery key");
  let originalKit = await downloadKit(a, "Download recovery kit");
  assert.equal(originalKit.kit.address, origin);
  await click(a, "I have saved it — check it works");
  await a.locator("[data-enroll-kit-file]").setInputFiles({ name: "wrong.json", mimeType: "application/json", buffer: Buffer.from("{}") });
  await a.getByRole("alert").filter({ hasText: "does not belong" }).waitFor();
  await a.context().clearCookies({ name: "__Host-noisedeck_session" });
  const expiredProof = a.waitForResponse(response => new URL(response.url()).pathname.startsWith("/api/security/v1/") && response.status() === 401);
  await a.locator("[data-enroll-kit-file]").setInputFiles({ name: "kit.json", mimeType: "application/json", buffer: originalKit.bytes });
  await expiredProof;
  await a.locator('[data-onboarding="interrupted"]').waitFor();
  assert.equal(await a.locator("textarea, input, [data-enrollment], [data-live-more]").count(), 0,
    "a real setup 401 removes the recovery form and private workspace");
  assert.ok(!(await a.locator("body").textContent()).includes(originalKit.kit.recoveryKey), "expiry does not render the submitted recovery key");
  assert.equal((await fixtureState(a)).privateRequests, 0);
  await click(a, "Sign in with Google");
  await click(a, "Verify your saved unlock method");
  await fillPassword(a, passwords[0], "Continue setup");
  await a.getByRole("heading", { name: "Password checked", exact: true }).waitFor();
  await a.getByLabel("Name this method", { exact: true }).fill("Browser fixture password");
  await click(a, "Continue to your recovery key");
  originalKit = await downloadKit(a, "Download recovery kit");
  await click(a, "I have saved it — check it works");
  await a.locator("[data-enroll-kit-file]").setInputFiles({ name: "kit.json", mimeType: "application/json", buffer: originalKit.bytes });
  await fillPassword(a, passwords[0], "Finish setup");
  if (migrating) {
    await a.getByRole("heading", { name: "Waiting for your existing data", exact: true }).waitFor();
    const pinned = await securityState(a);
    assert.equal(pinned.migration, "pending"); assert.equal(pinned.onboarding.setupComplete, false);
    assert.equal(pinned.browserGranted, false); assert.equal(pinned.workspaceAuthorized, false);
    assert.deepEqual((await fixtureState(a)).calls, ["initialize"]);
    assert.notEqual(await backendStatus(a), 200, "pending import cannot admit private app");
    await a.reload({ waitUntil: "domcontentloaded" });
    await a.getByRole("heading", { name: "Waiting for your existing data", exact: true }).waitFor();
    await click(a, "Check import status");
    await a.waitForLoadState("networkidle");
    await a.getByRole("heading", { name: "Waiting for your existing data", exact: true }).waitFor();
    assert.deepEqual((await fixtureState(a)).calls, ["initialize"], "checking import cannot launch or initialize");
    await fixtureAction(a, "import-complete");
    await click(a, "Check import status");
    await a.getByLabel("Encryption password", { exact: true }).waitFor();
    assert.equal((await securityState(a)).workspaceAuthorized, false, "import receipt alone grants no browser authority");
    await fillPassword(a, passwords[0], "Finish setup");
    await a.getByRole("heading", { name: "Workspace ready", exact: true }).waitFor();
    assert.equal((await securityState(a)).initializingOperation, undefined);
    assert.deepEqual((await fixtureState(a)).calls, ["initialize", "start"]);
    console.log("PASS: migration pauses before app launch; reload/check stays read-only; completed import requires fresh saved-method proof.");
  }
  await a.getByRole("heading", { name: "Workspace ready", exact: true }).waitFor();
  assert.equal((await fixtureState(a)).privateRequests, 0);
  await click(a, "Open browser-go-fixture");
  await admitted(a);
  await a.locator('[data-project-toggle="fixture-project"]').hover();
  await a.locator('[data-project-menu="fixture-project"]').click();
  const previewRead = a.waitForResponse(response => new URL(response.url()).pathname === previewSettingsPath && response.request().method() === "GET");
  await a.locator("[data-project-menu-popover]").getByRole("menuitem", { name: "Preview…", exact: true }).click();
  assert.equal((await previewRead).status(), 200, "preview settings use the admitted private Unix proxy");
  const previewDialog = a.getByRole("dialog", { name: "Preview Fixture project", exact: true });
  await previewDialog.waitFor();
  await a.clock.pauseAt(new Date());
  await a.context().clearCookies({ name: "__Host-deck_workspace" });
  const previewRefusal = a.waitForResponse(response => new URL(response.url()).pathname === previewSettingsPath && response.request().method() === "PUT");
  await previewDialog.getByRole("button", { name: "Enable preview", exact: true }).click();
  const refused = await previewRefusal;
  assert.equal(refused.status(), 403, "the real private proxy refuses a missing workspace bearer");
  // Paused timers keep periodic admission checks from hiding a broken refusal
  // callback. The mutation response itself must detach all private UI.
  await a.locator('[data-security-runtime="authorize-browser"]').waitFor();
  assert.equal(await a.locator("[data-live-more], [data-project-preview-dialog]").count(), 0);
  assert.equal((await refused.json()).code, "unlock-required");
  assert.equal((await securityState(a)).browserGranted, false, "Google remains valid after only the workspace cookie is removed");
  await a.clock.resume();
  await authorize(a, passwords[0]);
  const initialNative = (await fixtureState(a)).calls;
  assert.deepEqual(initialNative, ["initialize", "start"]);
  assert.ok((await securityState(a)).browserGranted);
  console.log("PASS: HTTPS enrollment, setup 401 cleanup and resume, real Go proofs, and immediate private preview teardown on proxy 403 while browser timers are paused.");

  const b = await newPage();
  await b.goto(`${origin}/auth/login`, { waitUntil: "domcontentloaded" });
  await b.getByRole("heading", { name: "Your workspace is running", exact: true }).waitFor();
  assert.equal(await backendStatus(b), 403, "Google alone cannot use private APIs");
  assert.equal(await backendStatus(b, previewSettingsPath), 403, "Google alone cannot read project preview settings");
  await click(b, "Use encryption password");
  await fillPassword(b, "incorrect fixture password", "Verify this browser");
  await b.getByRole("alert").filter({ hasText: "That password did not unlock" }).waitFor();
  assert.equal(await backendStatus(b), 403);
  await fillPassword(b, passwords[0], "Try again");
  await admitted(b);
  assert.equal(await backendStatus(b, previewSettingsPath), 200, "method proof admits the new project preview API too");
  assert.deepEqual((await fixtureState(b)).calls, initialNative, "second browser authorization never remounts or restarts");
  console.log("PASS: separate browser needs method proof for private app and preview settings; wrong password denied; authorization does not remount.");

  await manage(a);
  await click(a, "Manage Browser fixture password");
  await click(a, "Remove this method");
  await a.getByRole("heading", { name: "This is the only everyday way to unlock browser-go-fixture", exact: true }).waitFor();
  assert.equal(await a.locator('[data-method-action="confirm-remove"]').count(), 0);
  await click(a, "Close");
  await click(a, "Manage Browser fixture password");
  await click(a, "Change encryption password");
  await a.getByLabel("Current password", { exact: true }).fill(passwords[0]);
  await a.getByLabel("New password", { exact: true }).fill(passwords[1]);
  const oldPassword = (await methods(a)).find(method => method.kind === "password" && method.status === "active");
  await click(a, "Change and check it");
  await a.getByRole("heading", { name: "Check the new password", exact: true }).waitFor();
  await a.getByLabel("New password", { exact: true }).fill("incorrect saved candidate");
  await click(a, "Check saved method");
  await a.getByRole("alert").waitFor();
  assert.ok((await methods(a)).some(method => method.id === oldPassword.id && method.status === "active"), "wrong replacement leaves original active");
  await a.getByLabel("New password", { exact: true }).fill(passwords[1]);
  await click(a, "Check saved method");
  await click(a, "Replace “Browser fixture password”");
  await authorizeChange(a, "Browser fixture password", passwords[0]);
  await a.getByRole("heading", { name: "Your workspace is running", exact: true }).waitFor();
  await b.getByRole("heading", { name: "Your workspace is running", exact: true }).waitFor();
  assert.ok(!(await methods(a)).some(method => method.id === oldPassword.id && method.status === "active"));
  assert.equal(await backendStatus(a), 403);
  assert.equal(await backendStatus(b), 403, "replacement revokes other browsers granted by the old method");
  await click(b, "Use encryption password");
  await fillPassword(b, passwords[0], "Verify this browser");
  await b.getByRole("alert").filter({ hasText: "That password did not unlock" }).waitFor();
  assert.equal(await backendStatus(b), 403, "old password no longer authorizes this browser");
  await fillPassword(b, passwords[1], "Try again");
  await admitted(b);

  await click(a, "Use recovery key");
  await a.getByRole("textbox", { name: /^Recovery key\b/ }).fill(originalKit.kit.recoveryKey);
  await click(a, "Verify this browser");
  await a.getByRole("heading", { name: "Set up a way to unlock from now on", exact: true }).waitFor();
  const oldRecovery = (await methods(a)).find(method => method.kind === "recovery" && method.status === "active");
  await click(a, "Get a new one");
  await authorizeChange(a, "Encryption password", passwords[1]);
  const replacementKit = await downloadKit(a, "Download kit");
  assert.ok(replacementKit.kit.recoveryKey !== originalKit.kit.recoveryKey, "replacement generates a different recovery key");
  assert.ok((await methods(a)).some(method => method.id === oldRecovery.id && method.status === "active"));
  await click(a, "I have saved it — check it works");
  await a.locator("[data-method-kit-file]").setInputFiles({ name: "replacement-kit.json", mimeType: "application/json", buffer: replacementKit.bytes });
  await click(a, "Replace “Recovery key”");
  await authorizeChange(a, "Encryption password", passwords[1]);
  await a.getByRole("heading", { name: "Your workspace is running", exact: true }).waitFor();
  assert.equal(await backendStatus(b), 200, "recovery replacement preserves password-authorized browsers");
  assert.ok(!(await methods(a)).some(method => method.id === oldRecovery.id && method.status === "active"));
  await authorize(a, passwords[1]);
  console.log("PASS: password and recovery replacement preserve old methods until tested and revoke only affected browser grants.");

  // The last-method guard offers the explicit route to add another password.
  await manage(a);
  await click(a, "Manage Encryption password");
  await click(a, "Remove this method");
  await click(a, "Add another method");
  await click(a, "Use an encryption password");
  await a.getByLabel("New password", { exact: true }).fill(passwords[2]);
  await click(a, "Save and check it");
  await authorizeChange(a, "Encryption password", passwords[1]);
  await a.getByLabel("New password", { exact: true }).fill(passwords[2]);
  await click(a, "Check saved method");
  await a.getByLabel("Name this method").fill("Spare password");
  await click(a, "Save method");
  await authorizeChange(a, "Encryption password", passwords[1]);
  await click(a, "Manage Spare password");
  await click(a, "Remove this method");
  await click(a, "Remove method");
  await authorizeChange(a, "Encryption password", passwords[1]);
  await a.getByRole("heading", { name: "How browser-go-fixture is unlocked", exact: true }).waitFor();
  assert.equal((await methods(a)).filter(method => method.status === "active" && method.kind === "password").length, 1);
  await click(a, "Close");
  await admitted(a);
  assert.deepEqual((await fixtureState(a)).calls, initialNative, "all method changes are envelope operations without native effects");

  await a.locator("[data-live-more]").click();
  await click(a, "Sign out Leaves your sessions running");
  await a.getByRole("button", { name: "Sign in with Google", exact: true }).waitFor();
  assert.equal(await backendStatus(a), 401);
  assert.equal(await backendStatus(b), 200, "logout preserves the other browser");
  assert.deepEqual((await fixtureState(b)).calls, initialNative);
  console.log("PASS: add/remove, last-method refusal, logout isolation, and continued native runtime.");
  await click(a, "Sign in with Google");
  await a.getByRole("heading", { name: "Your workspace is running", exact: true }).waitFor();
  await click(a, "Use recovery key");
  await a.getByRole("textbox", { name: /^Recovery key\b/ }).fill(originalKit.kit.recoveryKey);
  await click(a, "Verify this browser");
  await a.getByRole("alert").waitFor();
  assert.equal(await backendStatus(a), 403, "old recovery key cannot authorize after replacement");
  await a.locator("[data-security-kit-file]").setInputFiles({ name: "replacement-kit.json", mimeType: "application/json", buffer: replacementKit.bytes });
  await a.getByRole("heading", { name: "Set up a way to unlock from now on", exact: true }).waitFor();
  await click(a, "Open browser-go-fixture");
  await admitted(a);

  await fixtureAction(a, "uncertain-lock");
  await a.locator("[data-live-more]").click();
  await click(a, "Lock workspace Stops your sessions and locks storage");
  await click(a, "Stop sessions and lock");
  await fillPassword(a, passwords[1], "Confirm lock");
  await a.getByRole("heading", { name: "Could not confirm whether your workspace locked", exact: true }).waitFor();
  const duringLock = (await fixtureState(a)).calls;
  assert.deepEqual(duringLock, [...initialNative, "lock"]);
  assert.equal(await backendStatus(b), 403, "lock proof revokes all browser grants before stop preparation");
  await a.reload({ waitUntil: "domcontentloaded" });
  await a.getByRole("heading", { name: "Could not confirm whether your workspace locked", exact: true }).waitFor();
  assert.deepEqual((await fixtureState(a)).calls, duringLock, "reload observes without retrying native lock");
  const pendingLock = await a.evaluate(() => sessionStorage.getItem("deck.security.pending-operation.v1"));
  assert.ok(pendingLock && JSON.parse(pendingLock).operation === "lock");
  await fixtureAction(a, "closed-observation");
  const unconfirmed = await a.evaluate(async id => (await fetch(`/api/security/v1/operations/${id}`)).json(), JSON.parse(pendingLock).id);
  assert.ok(unconfirmed.status === "unconfirmed" && unconfirmed.locked && unconfirmed.storage === "closed" && unconfirmed.runtime === "stopped",
    "Go keeps an unconfirmed lock operation distinct from positively observed storage closure");
  await a.reload({ waitUntil: "domcontentloaded" });
  await a.getByRole("heading", { name: "Could not confirm whether your workspace locked", exact: true }).waitFor();
  assert.equal(await a.evaluate(() => sessionStorage.getItem("deck.security.pending-operation.v1")), pendingLock,
    "closed storage alone cannot clear the pending lock operation");
  assert.equal(await a.getByText("Storage is closed and the workspace has stopped.", { exact: true }).count(), 0);
  assert.equal((await securityState(a)).browserGranted, false);
  assert.equal(await backendStatus(a), 403);
  assert.deepEqual((await fixtureState(a)).calls, duringLock, "closed observation does not replay lock or authorize another native effect");
  await fixtureAction(a, "settle-lock");
  await click(a, "Reconnect and check");
  await a.getByText("Storage is closed and the workspace has stopped.", { exact: true }).waitFor();
  assert.deepEqual((await fixtureState(a)).calls, duringLock);
  await click(a, "Unlock again");
  await authorize(a, passwords[1], "Unlock");
  assert.deepEqual((await fixtureState(a)).calls, [...duringLock, "unlock", "start"]);
  await a.locator("[data-live-more]").click();
  await click(a, "Lock workspace Stops your sessions and locks storage");
  await click(a, "Stop sessions and lock");
  await fillPassword(a, passwords[1], "Confirm lock");
  await a.getByText("Storage is closed and the workspace has stopped.", { exact: true }).waitFor();
  assert.deepEqual((await fixtureState(a)).calls, [...duringLock, "unlock", "start", "lock"]);

  await Promise.all(challengeReads);
  for (const mutation of mutations) mutation.operation = challenges.get(mutation.challengeId)?.operation;
  assert.ok(mutations.some(action => action.operation === "resume-enrollment"));
  for (const operation of ["verify-method", "initialize", "authorize-browser", "replace-method", "add-method", "remove-method", "lock", "unlock"]) {
    assert.ok(mutations.some(action => action.operation === operation), `real Go accepted ${operation}`);
  }
  assert.ok(mutations.every(action => action.hasVolumeSecret === ["initialize", "unlock", "resume-initialization"].includes(action.operation)), "only explicit native init/unlock transfers a volume secret");
  assert.deepEqual(requestFailures, []);
  assert.deepEqual(pageErrors, []);
} catch (error) {
  probeFailure = error;
  for (const context of contexts) for (const page of context.pages()) {
    if (!page.isClosed()) console.error("Probe stopped at", { headings: await page.locator("h1").allTextContents(), alerts: await page.getByRole("alert").allTextContents() });
  }
  console.error("Security API refusals", apiFailures);
  throw error;
} finally {
  for (const context of contexts) await context.close().catch(() => {});
  try { await stopFixture(); } catch { /* A failed fixture may already have stopped. */ }
  await browser?.close().catch(() => {});
  if (!exit) await Promise.race([exited, pause(10_000)]);
  if (!exit) process.kill(-fixture.pid, "SIGTERM");
  await exited;
  await rm(artifacts, { recursive: true, force: true });
  if (exit.code !== 0 && !probeFailure) throw new Error(`Go fixture did not finish cleanly: ${fixtureOutput}`);
}
console.log("PASS: production browser WebCrypto and UI interoperated with Go enrollment, saved-envelope proofs, downloaded-kit import, browser grants, password/recovery replacement, add/remove, logout, and observe-only uncertain lock; fixture shut down cleanly. Google, native FUSE/Podman, OS passkeys, and password managers remain unqualified.");
