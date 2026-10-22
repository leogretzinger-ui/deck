import assert from "node:assert/strict";
import { resolve } from "node:path";
import { createServer } from "vite";
import { browser as connectBrowser } from "./browser.mjs";

const server = await createServer({ configFile: resolve(import.meta.dirname, "../vite.config.ts"), root: resolve(import.meta.dirname, ".."), logLevel: "error", optimizeDeps: { noDiscovery: true, entries: [] }, server: { host: "127.0.0.1", port: 0 } });
await server.listen();
const origin = `http://127.0.0.1:${server.httpServer.address().port}`;
let browser;
try {
  ({ browser } = await connectBrowser());
  const context = await browser.newContext({ viewport: { width: 1280, height: 800 } });
  await context.addCookies([{ name: "noisedeck_csrf", value: "fixture-cookie", url: origin }]);
  const requests = [];
  const errors = [];
  let beginReply;
  let authorizationReply;
  let waitingListing = { pending: [], csrf: "fixture-login" };
  let submitReply;
  let cancelStatus = 200;
  let counter = 0;
  let listingGets = 0;
  const pending = (id = "attempt-a", flow = "codex-browser", ttl = 300000) => ({ id, flow, expires: new Date(Date.now() + ttl).toISOString() });
  const listing = (attempts = [pending()]) => ({ pending: attempts, csrf: "fixture-login" });
  await context.route(`${origin}/fixture`, route => route.fulfill({ contentType: "text/html", body: `<!doctype html><style>#host{position:fixed;inset:0;pointer-events:none}#host>.scrim{pointer-events:auto}</style><button id="opener">Open login</button><div id="terminal-home"><div id="native-terminal" tabindex="0">Native terminal fixture</div></div><div id="host"></div><script type="module">
    import "/src/styles/app.css";
    import "/src/styles/fonts/free.css";
    import { HarnessLoginDialog } from '/src/app/harness-login.ts';
    window.closedEvents=[];
    window.openLogin=(listing,options={})=>{ document.querySelector('#opener').focus(); window.login=new HarnessLoginDialog(document.querySelector('#host'),'session-a',{...options,listing,onClosed:(attempts,reason)=>closedEvents.push({attempts,reason})}); };
    window.ready=true;
  </script>` }));
  await context.route(`${origin}/api/harness-login`, async route => {
    const request = route.request();
    const body = request.postDataJSON();
    assert.equal(request.method(), "POST");
    assert.equal(request.headers()["x-harness-login-csrf"], "fixture-login");
    assert.equal(request.headers()["x-csrf-token"], "fixture-cookie");
    assert.equal(request.headers().referer, undefined);
    requests.push(body);
    const json = (status, body) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
    if (body.action === "authorization") return authorizationReply ? authorizationReply(route, body) : json(200, { url: "https://auth.openai.com/oauth/authorize?state=synthetic-opener-state" });
    if (body.action === "begin") {
      if (beginReply) return beginReply(route, body);
      return json(200, { id: `handle-${++counter}` });
    }
    if (body.action === "submit") return submitReply ? submitReply(route, body) : json(200, { status: "completed" });
    if (body.action === "cancel") return json(cancelStatus, { status: "cancelled" });
    assert.equal(body.action, "dismiss");
    return json(200, { status: "dismissed" });
  });
  await context.route(`${origin}/api/harness-login?**`, route => {
    listingGets++;
    return route.fulfill({ contentType: "application/json", body: JSON.stringify(waitingListing) });
  });
  await context.route("https://auth.openai.com/**", route => {
    assert.equal(route.request().headers().referer, undefined, "authorization navigation sends no Deck referrer");
    return route.fulfill({ contentType: "text/html", body: "Synthetic OpenAI page" });
  });
  const page = await context.newPage();
  page.on("pageerror", error => errors.push(error.message));
  await page.goto(`${origin}/fixture`);
  await page.waitForFunction(() => window.ready);
  const dialog = page.locator("[data-harness-login-dialog]");
  const input = page.locator("[data-login-callback]");
  const open = value => page.evaluate(value => window.openLogin(value), value);
  const close = () => page.evaluate(() => window.login.close());
  const count = action => requests.filter(request => request.action === action).length;
  const until = async (check, description) => {
    const end = Date.now() + 5000;
    while (!check()) {
      if (Date.now() >= end) throw new Error(`Timed out: ${description}`);
      await new Promise(resolve => setTimeout(resolve, 20));
    }
  };
  const settle = () => new Promise(resolve => setTimeout(resolve, 100));
  const canary = "http://localhost:1457/auth/callback?code=synthetic-private-canary&state=fixture";

  await open(listing());
  await input.waitFor({ state: "visible" });
  assert.equal(await dialog.getAttribute("aria-label"), "Codex login helper");
  assert.equal(await dialog.locator(".dialog-desc").isHidden(), true);
  assert.equal(await dialog.innerText().then(text => text.includes("For the selected session")), false);
  assert.equal(await page.locator("[data-login-cancel]").isVisible(), false, "Codex cancellation stays in the native terminal");
  assert.equal(await input.getAttribute("inputmode"), "url");
  assert.equal(await input.evaluate(el => el === document.activeElement), true);
  await input.fill(canary);
  await page.evaluate(() => window.oldInput = document.querySelector('[data-login-callback]'));
  await input.press("Escape");
  assert.equal(count("begin"), 0, "showing and focusing the form does not claim the attempt");
  assert.equal(count("dismiss"), 0, "an unclaimed attempt needs no dismissal request");
  assert.equal(await page.evaluate(() => oldInput.value), "");
  assert.equal(await page.locator("#opener").evaluate(el => el === document.activeElement), true);
  assert.deepEqual(await page.evaluate(() => closedEvents.at(-1)), { attempts: ["attempt-a"], reason: "dismissed" });
  await open(listing([pending("attempt-a", "pi-openai")]));
  await input.waitFor({ state: "visible" });
  assert.equal(await dialog.locator(".dialog-desc").innerText(), "For the selected session", "Pi keeps its existing context subtitle");
  assert.equal(await dialog.locator(".dialog-body [data-login-refresh]").innerText(), "Check for a waiting login", "Pi retains its existing refresh control");
  await page.getByRole("button", { name: "Cancel login", exact: true }).click();
  await dialog.waitFor({ state: "detached" });
  assert.equal(count("cancel"), 1);
  assert.equal(count("dismiss"), 0);

  let releaseBegin;
  beginReply = route => new Promise(resolve => { releaseBegin = async () => { await route.fulfill({ contentType: "application/json", body: JSON.stringify({ id: "late-handle" }) }); resolve(); }; });
  await open(listing());
  await input.fill(canary);
  await input.press("Enter");
  await until(() => releaseBegin, "delayed Begin");
  const submitsBeforeClose = count("submit");
  await close();
  await releaseBegin();
  await until(() => requests.some(r => r.action === "dismiss" && r.handle === "late-handle"), "late Begin cleanup");
  assert.equal(count("submit"), submitsBeforeClose, "a late claim never delivers a closed form");

  releaseBegin = undefined;
  beginReply = route => new Promise(resolve => { releaseBegin = async () => { await route.fulfill({ contentType: "application/json", body: JSON.stringify({ id: "late-cancel-handle" }) }); resolve(); }; });
  await open(listing([pending("attempt-a", "pi-openai")]));
  await page.getByRole("button", { name: "Cancel login", exact: true }).click();
  await until(() => releaseBegin, "delayed cancellation claim");
  const cancelsBeforeClose = count("cancel");
  await close();
  await releaseBegin();
  await until(() => requests.some(r => r.action === "dismiss" && r.handle === "late-cancel-handle"), "late cancellation claim cleanup");
  assert.equal(count("cancel"), cancelsBeforeClose);
  beginReply = undefined;

  let releaseSubmit;
  submitReply = route => new Promise(resolve => { releaseSubmit = async () => { await route.fulfill({ status: 400, contentType: "application/json", body: "{}" }); resolve(); }; });
  await open(listing());
  await input.waitFor({ state: "visible" });
  await input.fill(canary);
  await input.press("Enter");
  await until(() => releaseSubmit, "delayed invalid callback");
  const submittedHandle = requests.at(-1).handle;
  const dismissBefore = count("dismiss");
  assert.equal(await input.inputValue(), "");
  await close();
  assert.equal(count("dismiss"), dismissBefore);
  await releaseSubmit();
  await until(() => requests.some(r => r.action === "dismiss" && r.handle === submittedHandle), "late invalid callback cleanup");

  submitReply = route => new Promise(resolve => { releaseSubmit = async () => { await route.abort("failed"); resolve(); }; });
  releaseSubmit = undefined;
  await open(listing());
  await input.waitFor({ state: "visible" });
  await input.fill(canary);
  await input.press("Enter");
  await until(() => releaseSubmit, "uncertain submission");
  const beforeUncertain = { dismiss: count("dismiss"), submit: count("submit") };
  await close();
  await releaseSubmit();
  await settle();
  assert.equal(count("dismiss"), beforeUncertain.dismiss);
  assert.equal(count("submit"), beforeUncertain.submit);

  submitReply = route => route.fulfill({ status: 400, contentType: "application/json", body: "{}" });
  await open(listing());
  await input.waitFor({ state: "visible" });
  await input.fill(canary);
  await input.press("Enter");
  await page.getByText("That URL doesn’t match this sign-in.", { exact: false }).waitFor();
  assert.equal(await input.inputValue(), "");
  assert.equal(await input.getAttribute("aria-invalid"), "true");
  submitReply = undefined;
  await input.fill(canary);
  await input.press("Enter");
  await page.getByText("Native login completed. Return to the terminal.", { exact: true }).waitFor();
  assert.equal(await page.locator("[data-login-later]").innerText(), "Continue to Codex");
  assert.equal(await page.locator("[data-login-intro]").isHidden(), true);
  await close();

  cancelStatus = 409;
  await open(listing([pending("attempt-a", "pi-openai")]));
  await input.waitFor({ state: "visible" });
  await page.getByRole("button", { name: "Cancel login", exact: true }).click();
  await page.getByText("Cancellation could not be confirmed.", { exact: false }).waitFor();
  assert.equal(await page.locator("[data-login-later]").innerText(), "Close");
  await close();
  cancelStatus = 200;

  await open(listing([pending("expiring", "codex-browser", 500)]));
  await input.waitFor({ state: "visible" });
  await input.fill(canary);
  await page.getByText("This login attempt expired.", { exact: false }).waitFor();
  assert.equal(await input.inputValue(), "");
  assert.equal(await input.isHidden(), true);
  assert.equal(await page.locator("[data-login-intro]").isHidden(), true);
  await close();

  await open(listing([pending("pi-a", "pi-openai"), pending("pi-b", "pi-anthropic")]));
  await page.getByRole("button", { name: "Finish Pi · Anthropic login", exact: true }).waitFor();
  assert.equal(await dialog.getAttribute("aria-label"), "Finish Pi login");
  assert.equal(await dialog.locator(".dialog-title").innerText(), "Finish Pi login");
  await close();

  const beforeChoice = count("begin");
  await open(listing([pending("choice-a"), pending("choice-b", "pi-openai")]));
  await page.getByRole("button", { name: "Finish Pi · ChatGPT login", exact: true }).waitFor();
  assert.equal(count("begin"), beforeChoice);
  assert.equal(await dialog.getAttribute("aria-label"), "Finish agent login", "mixed agents keep the generic heading");
  await page.getByRole("button", { name: "Finish Pi · ChatGPT login", exact: true }).click();
  await page.getByLabel("Localhost callback URL", { exact: true }).waitFor();
  submitReply = route => route.fulfill({ contentType: "application/json", body: JSON.stringify({ status: "delivered" }) });
  await input.fill(canary);
  await input.press("Enter");
  await page.getByText("Callback delivered. Check the native CLI for the authentication result.", { exact: true }).waitFor();
  await close();
  await open({ ...listing([]), unavailable: true });
  await page.getByText("Callback handoff is unavailable in this Deck instance.", { exact: false }).waitFor();
  assert.equal(await page.locator("[data-login-later]").innerText(), "Close");
  assert.equal(await page.locator("[data-login-intro]").isHidden(), true);
  await close();

  for (const viewport of [{ width: 1280, height: 800 }, { width: 393, height: 852 }]) {
    await page.setViewportSize(viewport);
    await open(listing());
    await input.waitFor({ state: "visible" });
    await page.evaluate(() => document.fonts.ready);
    for (let tab = 0; tab < 10; tab++) {
      await page.keyboard.press("Tab");
      assert.equal(await dialog.evaluate(el => el.contains(document.activeElement)), true, "keyboard focus stays in the dialog");
    }
    if (viewport.width < 640) {
      const box = await dialog.boundingBox();
      assert.ok(Math.abs(box.y + box.height - viewport.height) < 1, "phone dialog is a bottom sheet");
      assert.ok(box.y > 0, "phone dialog leaves session context visible");
      assert.ok(Number.parseFloat(await input.evaluate(el => getComputedStyle(el).fontSize)) >= 16, "phone field avoids focus zoom");
      const submit = await page.locator("[data-login-submit]").boundingBox();
      assert.ok(submit.height >= 44, "phone primary action has a touch target");
    }
    await input.fill(canary);
    await page.keyboard.press("Escape");
    await dialog.waitFor({ state: "detached" });
    assert.equal(await page.locator("#opener").evaluate(el => el === document.activeElement), true);
  }

  await page.setViewportSize({ width: 1280, height: 900 });
  await page.evaluate(() => {
    window.originalTerminal = document.querySelector('#native-terminal');
    window.openLogin(undefined, { initialCodex: true, terminalHost: window.originalTerminal });
  });
  await dialog.waitFor();
  assert.equal(await dialog.locator('[data-login-status]').innerText(), '');
  assert.equal(await dialog.locator('[data-login-status]').isHidden(), true);
  assert.equal(await dialog.locator('.dialog-desc').isHidden(), true);
  assert.equal(await page.locator('[data-login-intro]').innerText(), 'Choose a sign-in option in Codex below.');
  const refresh = dialog.locator('.dialog-foot [data-login-refresh]');
  assert.equal(await refresh.innerText(), 'Check for login');
  const refreshStyle = await refresh.evaluate(button => ({ border: Number.parseFloat(getComputedStyle(button).borderTopWidth), width: button.getBoundingClientRect().width, dialogWidth: button.closest('[role=dialog]').getBoundingClientRect().width }));
  assert.ok(refreshStyle.border > 0 && refreshStyle.width < refreshStyle.dialogWidth - 40, 'manual login check is an outlined footer button, not a full-width menu row');
  const beforeManualCheck = listingGets;
  await refresh.click();
  await until(() => listingGets === beforeManualCheck + 1, 'explicit footer login check');
  assert.equal(await dialog.locator('[data-login-status]').innerText(), '');
  assert.equal(await refresh.evaluate(button => button === document.activeElement), true, 'an empty refresh does not steal control focus');
  assert.equal(await page.locator('[data-login-submit]').isVisible(), false, 'no callback action before native registration');
  assert.equal(await page.evaluate(() => document.querySelector('[data-login-terminal]').contains(window.originalTerminal)), true);
  await page.locator('#native-terminal').focus();
  await page.keyboard.press('Escape');
  assert.equal(await dialog.isVisible(), true, 'native terminal Escape does not dismiss guidance');
  const dialogIdentity = await dialog.evaluate(el => { window.originalDialog = el; return true; });
  assert.equal(dialogIdentity, true);
  waitingListing = listing();
  await page.evaluate(() => window.login.notifyPending());
  await page.locator('[data-login-open]').waitFor({ state: 'visible' });
  assert.equal(await dialog.evaluate(el => el === window.originalDialog), true, 'registration updates the same dialog');
  assert.equal(await page.locator('[data-login-warning]').innerText(), 'A localhost error after sign-in is expected: Codex runs on Deck. Copy that tab’s full address below.');
  assert.equal(await page.locator('[data-login-privacy]').innerText(), 'Paste here, not in the terminal.');
  const beginsBeforeOpen = count('begin');
  const popupPromise = context.waitForEvent('page');
  await page.locator('[data-login-open]').focus();
  await page.keyboard.press('Enter');
  const popup = await popupPromise;
  await popup.waitForURL('https://auth.openai.com/oauth/authorize?state=synthetic-opener-state');
  assert.equal(await popup.evaluate(() => window.opener), null, 'provider tab cannot control Deck');
  assert.equal(count('begin'), beginsBeforeOpen, 'opening native URL does not claim the callback');
  await popup.close();
  authorizationReply = route => route.fulfill({ contentType: 'application/json', body: JSON.stringify({ url: 'https://unexpected.invalid/authorize?private=synthetic' }) });
  const refusedPopupPromise = context.waitForEvent('page');
  await page.locator('[data-login-open]').click();
  const refusedPopup = await refusedPopupPromise;
  await until(() => refusedPopup.isClosed(), 'untrusted authorization destination closes popup');
  await page.getByText('Could not open this sign-in.', { exact: false }).waitFor();
  assert.equal(await page.locator('[data-login-status]').innerText().then(text => text.includes('unexpected.invalid')), false, 'unsafe address is never echoed');
  authorizationReply = undefined;
  await page.evaluate(() => { window.realOpen = window.open; window.open = () => null; });
  const authorizationsBeforeBlock = count('authorization');
  await page.locator('[data-login-open]').click();
  await page.getByText('Allow pop-ups for Deck', { exact: false }).waitFor();
  assert.equal(count('authorization'), authorizationsBeforeBlock, 'blocked popup never requests a private URL');
  await page.evaluate(() => { window.open = window.realOpen; });
  await page.getByRole('button', { name: 'Use terminal instead', exact: true }).click();
  assert.equal(await page.evaluate(() => document.querySelector('#terminal-home').firstElementChild === window.originalTerminal), true, 'dismiss restores exact terminal node');
  await page.evaluate(() => window.openLogin(undefined, { initialCodex: true, terminalHost: window.originalTerminal }));
  await page.locator('[data-login-open]').waitFor({ state: 'visible' });
  await page.evaluate(() => window.login.close('context-lost'));
  assert.equal(await page.evaluate(() => document.querySelector('#terminal-home').firstElementChild === window.originalTerminal), true, 'context loss restores exact terminal node');

  assert.equal(await page.evaluate(() => JSON.stringify({ ...localStorage, ...sessionStorage }).includes("synthetic-private-canary")), false);
  assert.equal(page.url(), `${origin}/fixture`);
  assert.deepEqual(errors, []);

  // The standalone preview draws the dialog through the same modal host as
  // the live app: one scrim, the dialog centered on desktop and a bottom
  // sheet on the phone.
  const preview = await context.newPage();
  preview.on("pageerror", error => errors.push(error.message));
  for (const viewport of [{ width: 1280, height: 800 }, { width: 393, height: 852 }]) {
    await preview.setViewportSize(viewport);
    await preview.goto(`${origin}/preview.html?harness-login=pending`);
    const previewDialog = preview.locator("[data-harness-login-dialog]");
    await previewDialog.waitFor();
    assert.equal(await preview.locator(".scrim").count(), 1, "the preview draws one scrim");
    assert.equal(await previewDialog.evaluate(el => el.closest(".scrim > [data-modal-screen]") !== null), true,
      "the preview dialog is a screen of the modal host");
    const box = await previewDialog.boundingBox();
    if (viewport.width < 640) {
      assert.ok(Math.abs(box.y + box.height - viewport.height) < 1, "the phone preview is a bottom sheet");
    } else {
      assert.ok(Math.abs(box.x + box.width / 2 - viewport.width / 2) < 1.5, "the desktop preview centers the dialog");
      assert.ok(box.y > 0, "the desktop preview does not pin the dialog to the top edge");
    }
  }
  assert.deepEqual(errors, []);
  await context.close();
  console.log("harness login probe: lifecycle, late responses, expiry, cancellation and privacy passed");
} finally {
  await browser?.close();
  await server.close();
}
