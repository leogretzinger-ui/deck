// Production UI against the actual Go onboarding registry, gateway, plain
// service and Unix proxy. Google, native execution and app data are synthetic.
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdir, mkdtemp, readFile, rm } from "node:fs/promises";
import { resolve } from "node:path";
import { request as httpsRequest } from "node:https";
import { setTimeout as pause } from "node:timers/promises";
import { browser as connectBrowser } from "./browser.mjs";

const root = resolve(import.meta.dirname, "../..");
await readFile(resolve(root, "web/dist/index.html"));
await mkdir(resolve(root, "run"), { recursive: true, mode: 0o700 });
const { browser } = await connectBrowser();
const click = (page, name) => page.getByRole("button", { name, exact: true }).click();
const get = (page, path) => page.evaluate(async path => {
  const response = await fetch(path);
  return { status: response.status, value: response.headers.get("Content-Type")?.includes("application/json") ? await response.json() : undefined };
}, path);
const post = (page, path, value = {}) => page.evaluate(async ({ path, value }) => {
  const csrf = document.cookie.split("; ").find(item => item.startsWith("__Host-noisedeck_csrf="))?.split("=").slice(1).join("=");
  return (await fetch(path, { method: "POST", headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf ?? "" }, body: JSON.stringify(value) })).status;
}, { path, value });
async function stop(origin) {
  if (!origin) return;
  await new Promise((resolveStop, rejectStop) => {
    const request = httpsRequest({ hostname: "127.0.0.1", port: new URL(origin).port, path: "/__fixture/done", method: "POST", rejectUnauthorized: false, timeout: 2000 }, response => {
      response.resume(); response.once("end", () => resolveStop());
    });
    request.once("timeout", () => request.destroy(new Error("fixture shutdown timed out")));
    request.once("error", rejectStop); request.end();
  });
}

async function run(policy) {
  const artifacts = await mkdtemp(resolve(root, `run/onboarding-go-${policy}-`));
  const ready = resolve(artifacts, "ready.json");
  const child = spawn("go", ["test", "-race", "./internal/workspacehost", "-run", "^TestBrowserGatewayFixture$", "-count=1", "-timeout=10m", "-v"], {
    cwd: root, env: { ...process.env, DECK_WORKSPACE_BROWSER_FIXTURE: ready, DECK_WORKSPACE_BROWSER_POLICY: policy }, stdio: ["ignore", "pipe", "pipe"], detached: true,
  });
  let output = "", exit, origin, failure;
  for (const stream of [child.stdout, child.stderr]) stream.on("data", data => { output = (output + data.toString()).slice(-16_384); });
  const exited = new Promise(resolveExit => child.once("close", (code, signal) => { exit = { code, signal }; resolveExit(exit); }));
  child.once("error", error => { output = error.message; });
  const contexts = [], errors = [], mutations = [];
  let logouts = 0;
  async function page() {
    const context = await browser.newContext({ ignoreHTTPSErrors: true, reducedMotion: "reduce", colorScheme: "dark", viewport: { width: 1120, height: 720 } });
    contexts.push(context);
    const page = await context.newPage(); page.setDefaultTimeout(15_000);
    page.on("pageerror", error => errors.push(error.message));
    page.on("request", request => {
      const url = new URL(request.url());
      if (url.origin !== origin) { errors.push("request outside local HTTPS fixture"); return; }
      if (request.method() === "POST" && url.pathname === "/auth/logout") logouts++;
      if (request.method() === "POST" && url.pathname.startsWith("/api/security/")) mutations.push(url.pathname);
    });
    return page;
  }
  async function admitted(page) {
    await page.locator("[data-live-more]").waitFor();
    assert.equal((await get(page, "/api/projects")).status, 200);
    const state = (await get(page, "/api/security/v1/state")).value;
    assert.equal(state.workspaceAuthorized, true); assert.equal(state.browserGranted, false);
    assert.equal(state.onboarding.protection, "none"); assert.equal(state.onboarding.setupComplete, true);
    assert.equal(state.storage, "unencrypted"); assert.equal(state.runtime, "running");
    assert.equal((await page.context().cookies()).some(cookie => cookie.name === "__Host-deck_workspace"), false);
  }
  try {
    const deadline = Date.now() + 240_000;
    while (!origin && Date.now() < deadline) {
      if (exit) throw new Error(`Go fixture exited before readiness: ${output}`);
      try { const value = JSON.parse(await readFile(ready, "utf8")); if (value.ready) origin = value.origin; } catch { /* Readiness is published after mounting the gateway. */ }
      if (!origin) await pause(100);
    }
    assert.ok(origin && /^https:\/\/localhost:\d+$/.test(origin));
    const a = await page();
    await a.goto(origin, { waitUntil: "domcontentloaded" });
    await a.locator('[data-onboarding="needlink"]').waitFor();
    const fresh = (await get(a, "/api/security/v1/onboarding")).value;
    assert.deepEqual(fresh, { policy, protection: "undecided", started: false, setupComplete: false });
    await a.goto(`${origin}/auth/login`, { waitUntil: "domcontentloaded" });
    await a.locator('[data-onboarding="needlink"]').waitFor();
    assert.equal(await post(a, "/api/security/v1/onboarding/protection", { protection: "none" }), 403, "Google alone cannot claim or select the fresh workspace");
    assert.equal((await get(a, "/api/projects")).status, 403);
    if (policy === "optional") {
      await a.context().clearCookies();
      await a.goto(`${origin}/__fixture/invitation`, { waitUntil: "domcontentloaded" });
      await a.locator('[data-onboarding="welcome"]').waitFor();
      await a.route(`${origin}/auth/login`, route => route.continue({ url: `${origin}/auth/login?owner=ineligible` }));
      await click(a, "Sign in with Google");
      await a.locator('[data-onboarding="needlink"]').waitFor();
      assert.equal((await get(a, "/api/projects")).status, 403);
      await a.unroute(`${origin}/auth/login`);
    } else {
      // Preserve the real successful session response, then expire its cookie
      // before the production coordinator reads private setup state.
      await a.route(`${origin}/auth/session`, async route => {
        try {
          const response = await route.fetch({ url: `${origin.replace("localhost", "127.0.0.1")}/auth/session`,
            headers: { ...route.request().headers(), host: new URL(origin).host } });
          assert.equal(response.status(), 204);
          await a.context().clearCookies({ name: "__Host-noisedeck_session" });
          await route.fulfill({ response });
        } catch {
          errors.push("fixture could not expire the verified Google session");
          await route.abort();
        }
      });
      await a.goto(`${origin}/__fixture/invitation`, { waitUntil: "domcontentloaded" });
      await a.locator('[data-onboarding="needlink"]').waitFor();
      await a.unroute(`${origin}/auth/session`);
      assert.equal((await get(a, "/api/projects")).status, 401);
      assert.equal((await a.context().cookies()).some(cookie => cookie.name === "__Host-deck_invitation" && cookie.httpOnly), true,
        "expiry after a successful session probe must preserve the original private setup handoff");
    }
    assert.equal(new URL(a.url()).hash, "");
    assert.equal((await get(a, "/api/security/v1/onboarding")).value.started, false, "rejected authorization cannot bind the owner or consume setup");
    assert.equal(await a.getByText("Setup was started and has not finished.", { exact: false }).count(), 0);
    await click(a, "Use a different account");
    await a.locator(`[data-onboarding="encryption"][data-policy="${policy}"]`).waitFor();
    assert.equal(logouts, 0, "switching before ownership must preserve the private setup handoff");
    assert.equal(mutations.filter(path => path.endsWith("/invitation/handoff")).length, 1, "the original handoff redeems after an eligible sign-in");
    if (policy === "optional") assert.equal(await post(a, "/__fixture/uncertain-start"), 204);
    await click(a, policy === "optional" ? "Continue without Deck encryption" : "Finish setup");
    if (policy === "optional") {
      await a.locator('[data-onboarding="unconfirmed"]').waitFor();
      const before = (await get(a, "/__fixture/observation")).value;
      assert.equal(before.plainCalls, 1); assert.equal(before.privateRequests, 0);
      assert.deepEqual(before.calls, [], "unencrypted setup never invokes the encrypted controller");
      assert.equal((await get(a, "/api/projects")).status, 403);
      await a.reload({ waitUntil: "domcontentloaded" });
      await a.locator('[data-onboarding="unconfirmed"]').waitFor();
      assert.equal((await get(a, "/__fixture/observation")).value.plainCalls, 1, "reload observes the saved intent without replaying start");
      assert.equal(await post(a, "/__fixture/fail-start"), 204);
      await click(a, "Reconnect and check");
      await a.locator('[data-onboarding="resume"]').waitFor();
      assert.equal((await get(a, "/__fixture/observation")).value.plainCalls, 1, "a failed start is observed, not replayed");
      assert.equal(await post(a, "/__fixture/uncertain-start"), 204);
      await click(a, "Continue setup");
      await a.locator('[data-onboarding="unconfirmed"]').waitFor();
      assert.equal((await get(a, "/__fixture/observation")).value.plainCalls, 2, "continuing after a failed start issues one fresh start");
      assert.equal(await post(a, "/__fixture/settle-start"), 204);
      await click(a, "Reconnect and check");
    }
    await a.getByRole("heading", { name: "Workspace ready", exact: true }).waitFor();
    assert.equal((await get(a, "/__fixture/observation")).value.privateRequests, 0, "confirmed summary still precedes private app construction");
    await click(a, "Open browser-go-fixture"); await admitted(a);
    assert.equal(await post(a, "/api/security/v1/methods/prepare", { kind: "password", label: "Forbidden method" }), 403);
    assert.equal(await post(a, "/api/security/v1/onboarding/protection", { protection: "encrypted" }), 403);
    await a.locator("[data-live-more]").click();
    assert.equal(await a.locator("[data-workspace-lock], [data-workspace-methods]").count(), 0);
    const b = await page();
    await b.goto(origin, { waitUntil: "domcontentloaded" });
    await b.locator('[data-onboarding="signin"]').waitFor();
    await click(b, "Sign in with Google"); await admitted(b);
    await click(a, "Sign out");
    await a.locator('[data-onboarding="signin"]').waitFor();
    assert.equal((await get(a, "/api/projects")).status, 401);
    assert.equal((await get(b, "/api/projects")).status, 200, "sign-out does not revoke another browser");
    assert.equal((await get(b, "/__fixture/observation")).value.plainState.runtime, "running");
    await click(a, "Sign in with Google"); await admitted(a);
    const setupCalls = policy === "optional" ? 2 : 1;
    assert.equal((await get(a, "/__fixture/observation")).value.plainCalls, setupCalls, "returning owner does not repeat setup");
    assert.equal(await post(a, "/__fixture/stop-plain"), 204);
    await a.reload({ waitUntil: "domcontentloaded" });
    await a.locator('[data-security-runtime="plain-stopped"]').waitFor();
    const beforeStart = (await get(a, "/api/security/v1/state")).value.plainOperationId;
    await click(a, "Start workspace"); await admitted(a);
    assert.notEqual((await get(a, "/api/security/v1/state")).value.plainOperationId, beforeStart);
    assert.equal((await get(a, "/__fixture/observation")).value.plainCalls, setupCalls + 1);
    const c = await page();
    await c.goto(`${origin}/auth/login?owner=other`, { waitUntil: "domcontentloaded" });
    await c.locator('[data-onboarding="notowner"]').waitFor();
    assert.equal((await get(c, "/api/projects")).status, 403);
    assert.equal(mutations.filter(path => path.endsWith("/onboarding/start")).length, setupCalls + 1);
    assert.deepEqual(errors, []);
    console.log(`PASS: real Go ${policy} onboarding, receipt authority, observed plain completion, separate browser sign-out, returning owner, reboot start and no encrypted grant/controller.`);
  } catch (error) {
    failure = error;
    for (const context of contexts) for (const page of context.pages()) if (!page.isClosed()) console.error("Onboarding probe stopped at", { policy, headings: await page.locator("h1").allTextContents() });
    throw error;
  } finally {
    for (const context of contexts) await context.close().catch(() => {});
    await stop(origin).catch(() => {});
    if (!exit) await Promise.race([exited, pause(10_000)]);
    if (!exit) process.kill(-child.pid, "SIGTERM");
    await exited;
    await rm(artifacts, { recursive: true, force: true });
    if (exit.code !== 0 && !failure) throw new Error(`Go fixture failed: ${output}`);
  }
}

try {
  for (const policy of ["optional", "unavailable"]) await run(policy);
} finally { await browser.close(); }
console.log("Google, FUSE/Podman, provider CLIs, deployment hosts and password managers remain unqualified by this fixture.");
