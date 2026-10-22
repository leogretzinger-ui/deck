// Exercise the shipped bundle: Connect GitHub from first-use setup through a sign-in that finishes by itself.
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { readFile, mkdir } from "node:fs/promises";
import { extname, resolve, sep } from "node:path";
import { browser as connectBrowser } from "./browser.mjs";
const dist = resolve(import.meta.dirname, "../dist");
const mime = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".woff2": "font/woff2", ".svg": "image/svg+xml" };
const server = createServer(async (req, res) => {
  const name = new URL(req.url, "http://fixture").pathname;
  const path = resolve(dist, "." + (name === "/" ? "/index.html" : name));
  if (!path.startsWith(dist + sep)) { res.writeHead(404).end(); return; }
  try { const bytes = await readFile(path); res.writeHead(200, { "Content-Type": mime[extname(path)] ?? "application/octet-stream" }).end(bytes); }
  catch { res.writeHead(404).end(); }
});
await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
const origin = `http://127.0.0.1:${server.address().port}`;
let browser;
try {
  ({ browser } = await connectBrowser());
  for (const viewport of [{ width: 1280, height: 800 }, { width: 390, height: 844 }]) {
    const context = await browser.newContext({ viewport });
    await context.addCookies([{ name: "noisedeck_csrf", value: "cookie-fixture", url: origin }]);
    const page = await context.newPage(), errors = [], frames = [];
    page.on("pageerror", error => errors.push(error.message));
    let starts = 0, cancels = 0, attempt = null, connections = 0, attemptReads = 0;
    let status = { outcome: "no-account", active: false }, loseStart = false, identitySaved = false, savedIdentity = null;
    let authMode = "trusted_proxy", statusReads = 0, staleStatusAfterConnect = false;
    const code = "2DFA-5170";
    const profile = { id: "identity", name: "Fixture", email: "fixture@example.invalid", revision: 1, created_at: "2026-09-23T00:00:00Z", updated_at: "2026-09-23T00:00:00Z" };
    await context.grantPermissions(["clipboard-read", "clipboard-write"], { origin });
    await context.route("https://github.com/**", route => route.fulfill({ status: 200, contentType: "text/html", body: "<title>device</title>" }));
    await page.route(`${origin}/auth/session`, route => route.fulfill({ status: 204, headers: { "X-Noisedeck-Auth-Mode": authMode } }));
    await page.route(`${origin}/api/**`, async route => {
      const request = route.request(), path = new URL(request.url()).pathname;
      const json = (body, code = 200) => route.fulfill({ status: code, contentType: "application/json", body: JSON.stringify(body) });
      if (path === "/api/github/connect/status") {
        statusReads++;
        if (staleStatusAfterConnect && status.outcome === "authenticated" &&
            await page.locator("[data-github-connect-dialog]").count() === 0) {
          staleStatusAfterConnect = false;
          return json({ outcome: "no-account", active: false });
        }
        return json(status);
      }
      if (path === "/api/github/repositories") return json({ outcome: "ok", account: "fixture-user", complete: true, repositories: [
        { id: 1, name: "alpha", full_name: "fixture-user/alpha", owner: "fixture-user", owner_type: "User", private: false, description: "", clone_url: "https://github.com/fixture-user/alpha.git" }] });
      if (path === "/api/github/connect/attempt") {
        // gh prints the code a moment after launch: the first read after a start has none.
        attemptReads++;
        if (attempt?.status === "running" && attemptReads > 1) attempt = { ...attempt, code };
        return json({ csrf: "connect-fixture", attempt });
      }
      if (["/api/github/connect/start", "/api/github/connect/cancel"].includes(path)) {
        assert.equal(request.method(), "POST");
        assert.equal(request.headers()["x-csrf-token"], "cookie-fixture");
        assert.equal(request.headers()["x-github-connect-csrf"], "connect-fixture");
        if (path.endsWith("/start")) {
          starts++;
          attemptReads = 0;
          attempt = { id: `attempt-${starts}`, status: "running", generation: 1, started_at: "2026-09-23T00:00:00Z" };
          return loseStart ? route.abort("failed") : json(attempt);
        }
        assert.equal(request.postDataJSON().id, attempt.id);
        cancels++; attempt = { ...attempt, status: "cancelled" };
        return route.fulfill({ status: 204 });
      }
      if (path === "/api/settings") return json({ skip_permissions: false, skip_close_confirm: false, skip_remove_project_confirm: false });
      if (path === "/api/settings/tutorial") return json({}, 404);
      if (path === "/api/harness-login") return json({ pending: [], csrf: "fixture" });
      if (path === "/api/projects") return json([]);
      if (path === "/api/git-identities") {
        if (request.method() === "POST") { identitySaved = true; savedIdentity = request.postDataJSON(); return json(profile, 201); }
        return json({ profiles: identitySaved ? [profile] : [], default_profile_id: identitySaved ? profile.id : null, revision: identitySaved ? 1 : 0 });
      }
      if (path === "/api/providers") return json({ providers: [] });
      if (path === "/api/session-activity/events") return route.fulfill({ contentType: "text/event-stream", body: ": fixture\n\n" });
      if (path === "/api/settings/sidebar-layout") return json({ expanded: [], projects: [], sessions: {} });
      return json({ error: "not_found", message: path }, 404);
    });
    await page.routeWebSocket(`${origin.replace("http", "ws")}/api/github/connect/attempt/*/terminal?*`, socket => {
      connections++;
      socket.send(JSON.stringify({ type: "attached", token: 17, generation: 1 }));
      socket.send(JSON.stringify({ type: "output", data: Buffer.from("Synthetic native GitHub login\r\n").toString("base64") }));
      socket.onMessage(raw => frames.push(JSON.parse(raw)));
    });
    await page.goto(origin, { waitUntil: "networkidle" });
    // The desktop settings menu opens the Git identity form, which offers
    // Connect GitHub until it reads a connected account; the phone sheet has
    // no identity row. Otherwise the menu's own GitHub connection row opens it.
    const desktop = viewport.width > 600;
    const identity = page.locator("[data-git-identity-onboarding]");
    const connect = identity.getByRole("button", { name: "Connect GitHub", exact: true });
    const openIdentity = async () => {
      await page.locator("[data-live-more]").click();
      await page.getByRole("button", { name: "Git identity…" }).click();
      await identity.waitFor();
      await identity.locator("[data-github-connect], [data-github-connected]").first().waitFor();
    };
    const closeIdentity = async () => {
      await identity.locator("[data-git-identity-onboarding-close]").click();
      await identity.waitFor({ state: "detached" });
    };
    const openConnect = async () => {
      if (desktop && status.outcome !== "authenticated") {
        if (await identity.isVisible() && !await connect.isVisible()) await closeIdentity();
        if (!await identity.isVisible()) await openIdentity();
        await connect.click();
        return;
      }
      if (await identity.isVisible()) await closeIdentity();
      await page.locator("[data-live-more]").click();
      await page.getByRole("button", { name: "GitHub connection…", exact: true }).click();
    };
    if (desktop) {
      await openIdentity();
      assert.equal(await connect.locator("svg").count(), 1, "Connect GitHub carries the GitHub mark");
      assert.ok(await connect.getAttribute("data-hint"));
    }
    assert.equal(starts, 0, "nothing starts before the click");
    await openConnect();
    const dialog = page.locator("[data-github-connect-dialog]");
    // Back leaves the pushed screen for the identity dialog; the head's X would close the whole modal.
    const close = async () => {
      const back = dialog.locator("[data-modal-back]");
      await (await back.count() ? back : dialog.locator("[data-github-close]")).click();
      await dialog.waitFor({ state: "detached" });
      // The screen beneath re-renders as it returns; let that land before typing into it.
      await page.waitForTimeout(100);
    };
    const shows = text => dialog.locator("[data-github-screen]").getByText(text, { exact: true }).waitFor();
    const codeView = dialog.locator("[data-github-code]");
    await codeView.waitFor();
    assert.equal(starts, 1, "one click starts sign-in");
    assert.equal(await codeView.textContent(), code);
    assert.equal(await dialog.locator("[data-github-screen]").innerText().then(t => t.replace(/\s+/g, " ").trim()),
      `${code} Copy code and open GitHub Deck connects as soon as you approve it on GitHub.`);
    assert.deepEqual(await dialog.locator("[data-github-foot] button").allInnerTexts(), ["Cancel"]);
    assert.equal(await dialog.getByText("Read status").count(), 0);
    assert.equal(await dialog.locator(".xterm").count(), 0, "the raw pane stays behind Show details");
    assert.equal(identitySaved, false);
    for (const button of await dialog.locator("button").all()) assert.ok(await button.getAttribute("data-hint"), await button.innerText());
    if (process.env.NOISE_GITHUB_CAPTURE_DIR) {
      await mkdir(process.env.NOISE_GITHUB_CAPTURE_DIR, { recursive: true });
      await page.evaluate(() => document.fonts.ready);
      await page.screenshot({ path: resolve(process.env.NOISE_GITHUB_CAPTURE_DIR, `${process.env.NOISE_GITHUB_CAPTURE_LABEL ?? "build"}-${viewport.width > 600 ? "desktop" : "mobile"}-code.png`) });
    }
    const box = await dialog.boundingBox();
    assert.ok(box.width <= viewport.width && box.x >= 0, "dialog fits viewport");

    const [tab] = await Promise.all([context.waitForEvent("page"), dialog.getByRole("button", { name: "Copy code and open GitHub" }).click()]);
    assert.equal(tab.url(), "https://github.com/login/device");
    await tab.close();
    assert.equal(await page.evaluate(() => navigator.clipboard.readText()), code);

    await dialog.getByText("Show details", { exact: true }).click();
    await dialog.locator(".xterm").waitFor();
    await page.waitForFunction(() => document.querySelector("[data-github-terminal-status]").textContent === "");
    await dialog.locator(".xterm-helper-textarea").press("a");
    await page.waitForTimeout(100);
    assert.ok(frames.some(frame => frame.type === "input" && frame.token === 17 && frame.generation === 1), "the details pane keeps input fencing");

    await close();
    assert.equal(cancels, 0, "close leaves sign-in running");
    if (desktop) assert.equal(await identity.isVisible(), true, "closing Connect GitHub returns to the identity screen");
    await page.reload({ waitUntil: "networkidle" });
    const draft = { name: "Draft Author", email: "draft@example.invalid" };
    if (desktop) {
      await openIdentity();
      await identity.getByRole("textbox", { name: "Git author name" }).fill(draft.name);
      await identity.getByRole("textbox", { name: "Git author email" }).fill(draft.email);
    }
    await openConnect();
    await codeView.waitFor();
    assert.equal(starts, 1, "reopening finds the same sign-in");
    assert.ok(connections >= 1);

    status = { outcome: "authenticated", active: true, login: "fixture-user", state: "success", source: "stored", source_writeable: true };
    attempt = { ...attempt, status: "exited" };
    // The finished sign-in closes the dialog by itself, and the identity screen beneath already names the account.
    await page.waitForFunction(() => document.querySelector("[data-github-connect-dialog]") === null);
    assert.equal(starts, 1);
    if (desktop) {
      assert.equal(await page.evaluate(() => document.querySelector("[data-git-identity-onboarding] [data-github-connected]")?.textContent.trim()),
        "Connected to GitHub as fixture-user", "the opener shows the account as the dialog closes");
      assert.equal(await identity.isVisible(), true, "the identity step still follows the connection");
      assert.equal(await identity.locator("[data-github-connect]").count(), 0, "nothing is left to connect");
      assert.equal(await page.evaluate(() => document.querySelector("[data-git-identity-onboarding]").contains(document.activeElement)), true,
        "focus stays with the identity screen");
    }
    await page.waitForTimeout(100);
    assert.equal(await page.locator("[data-github-connect-dialog]").count(), 0, "nothing reopens the dialog");
    if (desktop) {
      assert.equal(await identity.getByRole("textbox", { name: "Git author name" }).inputValue(), draft.name, "the connection keeps the typed name");
      assert.equal(await identity.getByRole("textbox", { name: "Git author email" }).inputValue(), draft.email, "the connection keeps the typed email");
      await identity.getByRole("button", { name: "Save identity" }).click();
      await identity.waitFor({ state: "detached" });
      assert.deepEqual([savedIdentity?.name, savedIdentity?.email], [draft.name, draft.email], "the draft saves as typed");
      identitySaved = false;
    }

    // Opening on an account that is already connected shows which one it is.
    await page.reload({ waitUntil: "networkidle" });
    await openConnect();
    await shows("Connected as fixture-user");
    assert.deepEqual(await dialog.locator("[data-github-foot] button").allInnerTexts(), ["Done"]);
    assert.equal(await dialog.getByText("ended", { exact: false }).count(), 0, "a known status replaces the process line");
    if (process.env.NOISE_GITHUB_CAPTURE_DIR) {
      await page.screenshot({ path: resolve(process.env.NOISE_GITHUB_CAPTURE_DIR, `${process.env.NOISE_GITHUB_CAPTURE_LABEL ?? "build"}-${viewport.width > 600 ? "desktop" : "mobile"}-connected.png`) });
    }
    await dialog.getByRole("button", { name: "Done" }).click();
    await dialog.waitFor({ state: "detached" });

    await openConnect();
    await shows("Connected as fixture-user");
    await shows("To use a different account, sign out with the GitHub CLI and connect again.");
    assert.deepEqual(await dialog.locator("[data-github-foot] button").allInnerTexts(), ["Done"]);
    await close();

    status = { outcome: "auth-issue", active: true, state: "error", source: "environment", login: "fixture-user" };
    await openConnect();
    await shows("Your GitHub access comes from a token in Deck's environment. Change it there; Deck won't replace it.");
    await close();

    status = { outcome: "missing-cli", active: false, platform: "darwin" };
    await openConnect();
    await shows("Deck can't find the GitHub CLI (gh). Install it, then try again.");
    assert.equal(await dialog.locator("[data-github-install] code").textContent(), "brew install gh");
    assert.deepEqual(await dialog.locator("[data-github-foot] button").allInnerTexts(), ["Try again"]);
    await close();
    assert.equal(starts, 1, "a connected account and notices start nothing");

    status = { outcome: "no-account", active: false }; attempt = null; loseStart = true;
    await openConnect();
    await codeView.waitFor();
    assert.equal(starts, 2, "a lost start response is reconciled by reading, never replayed");
    loseStart = false;
    await dialog.getByRole("button", { name: "Cancel" }).click();
    await shows("Sign-in ended before GitHub approved it.");
    assert.equal(cancels, 1);
    assert.deepEqual(await dialog.locator("[data-github-foot] button").allInnerTexts(), ["Try again"]);
    assert.equal(starts, 2, "cancel starts nothing");

    // A sign-in Deck lost track of fails with Try again, which starts a new one.
    await dialog.getByRole("button", { name: "Try again" }).click();
    await codeView.waitFor();
    attempt = { ...attempt, status: "unavailable", final: true };
    await shows("Deck lost track of this GitHub sign-in.");
    assert.deepEqual(await dialog.locator("[data-github-foot] button").allInnerTexts(), ["Try again"]);
    assert.equal(starts, 3);
    await dialog.getByRole("button", { name: "Try again" }).click();
    await codeView.waitFor();
    assert.equal(starts, 4);
    await dialog.getByRole("button", { name: "Cancel" }).click();
    await shows("Sign-in ended before GitHub approved it.");
    await close();

    status = { outcome: "authenticated", active: true, login: "fixture-user", state: "success", source: "stored", source_writeable: true };
    attempt = { ...attempt, status: "exited" };
    if (desktop) {
      await identity.getByRole("textbox", { name: "Git author name" }).fill(profile.name);
      await identity.getByRole("textbox", { name: "Git author email" }).fill(profile.email);
      await identity.getByRole("button", { name: "Save identity" }).click();
      await identity.waitFor({ state: "detached" });
    }
    await page.locator("[data-live-more]").click();
    await page.getByRole("button", { name: "GitHub connection…", exact: true }).click();
    await shows("Connected as fixture-user");
    await close();

    // Import from GitHub offers only Connect GitHub, and a finished sign-in lists repositories without a refresh.
    status = { outcome: "no-account", active: false }; attempt = null;
    const tutorial = page.locator('[data-region="tutorial"][data-open="true"]');
    if (await tutorial.count()) await tutorial.getByRole("button", { name: "Close tutorial", exact: true }).click();
    await page.locator("[data-empty-action]").click();
    await page.locator('[data-project-source-dialog] [data-project-source="github"]').click();
    const picker = page.locator("[data-github-picker]");
    const pickerConnect = picker.getByRole("button", { name: "Connect GitHub", exact: true });
    await pickerConnect.waitFor();
    assert.equal(await picker.locator("[data-github-repo-refresh]").count(), 0, "Connect GitHub is the only action");
    await pickerConnect.click();
    await codeView.waitFor();
    assert.equal(starts, 5);
    // The connection has confirmed the account. The picker must use that
    // handoff to request a server-validated list, not replace it with another
    // status snapshot before it has tried listing repositories.
    staleStatusAfterConnect = true;
    status = { outcome: "authenticated", active: true, login: "fixture-user", state: "success", source: "stored", source_writeable: true };
    attempt = { ...attempt, status: "exited" };
    await dialog.waitFor({ state: "detached" });
    await picker.getByText("Connected as", { exact: false }).waitFor({ timeout: 5000 });
    assert.equal(await pickerConnect.count(), 0, "the finished sign-in replaces Connect GitHub");
    await picker.getByRole("button", { name: "Import fixture-user/alpha", exact: true }).waitFor();
    staleStatusAfterConnect = false;

    // An account connected outside Deck reaches the picker once you close the dialog on it.
    status = { outcome: "no-account", active: false }; attempt = null;
    await picker.locator('[data-project-source="choose"]').click();
    await page.locator('[data-project-source-dialog] [data-project-source="github"]').click();
    await pickerConnect.waitFor();
    status = { outcome: "authenticated", active: true, login: "fixture-user", state: "success", source: "stored", source_writeable: true };
    await pickerConnect.click();
    await shows("Connected as fixture-user");
    await dialog.getByRole("button", { name: "Done" }).click();
    await dialog.waitFor({ state: "detached" });
    await picker.getByRole("button", { name: "Import fixture-user/alpha", exact: true }).waitFor({ timeout: 5000 });
    assert.equal(starts, 5, "an account already connected starts nothing");
    authMode = "none";
    await page.reload({ waitUntil: "networkidle" });
    const readsBefore = statusReads;
    await page.locator("[data-live-more]").click();
    await page.getByRole("button", { name: "GitHub connection…", exact: true }).click();
    await shows("Turn on password, OIDC, or trusted-proxy sign-in for Deck before you connect GitHub.");
    assert.equal(statusReads, readsBefore, "unauthenticated mode explains the prerequisite without reading or starting");
    assert.equal(starts, 5);
    assert.deepEqual(errors, []);
    await context.close();
  }
  console.log("GitHub built desktop/mobile: one-click code, copy and open, details pane, reopen, self-closing connect, connected reopen, cancel, notices, lost start, lost attempt, global reopen and a picker that lists repositories once sign-in finishes or you close on a connected account passed");
} finally { await browser?.close(); await new Promise(resolve => server.close(resolve)); }
