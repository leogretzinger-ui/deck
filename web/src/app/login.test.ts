// The sign-in screen. Its inputs include an email the server refused, so
// escaping is the load-bearing test; the rest pins that each state offers
// exactly one action and never two.
import assert from "node:assert/strict";
import test from "node:test";

import { loginHtml } from "./login.ts";

test("the signed-out state offers the Google ceremony", () => {
  const html = loginHtml({ mode: "oidc", state: "signed-out" });
  assert.ok(html.includes('href="/auth/login"'), "the button is the real link");
  assert.ok(html.includes("Sign in with Google"));
  assert.ok(!html.includes("Waiting for Google"), "no waiting status before the click");
});

// While the browser is at Google there is nothing to click: leaving the
// button up invites a second navigation that abandons the first
// transaction, and the server consumes its txn cookie once.
test("the authorizing state replaces the button rather than adding to it", () => {
  const html = loginHtml({ mode: "builtin", state: "authorizing" });
  assert.ok(html.includes("Waiting for Google"));
  assert.ok(!html.includes('href="/auth/login"'), "the link is gone, not merely disabled");
  // The pending row reserves the button's h-9 geometry; login-probe.mjs
  // measures that the card does not reflow across the swap.
  assert.ok(html.includes('data-login-pending class="flex h-9 w-full'));
});

// The lockup is the mark and the wordmark composed in a row, and the
// mark's rows are the sweep's moving parts: their count and radii are
// the geometry the nfsweep delays phase against.
test("the mark is five rows and sweeps only while authorizing", () => {
  const resting = loginHtml({ mode: "oidc", state: "signed-out" });
  const away = loginHtml({ mode: "oidc", state: "authorizing" });
  const rows = away.match(/<g class="nfrow">.*?<\/g>/gs) ?? [];
  assert.equal(rows.length, 5, "one row per radius");
  const radii = ["7.5", "11.25", "15", "18.75", "22.5"];
  rows.forEach((row, i) => {
    assert.equal((row.match(/<circle\b/g) ?? []).length, 5, `row ${i + 1} draws five circles`);
    assert.ok(row.includes(`r="${radii[i]}"`), `row ${i + 1} radius is ${radii[i]}`);
  });
  assert.ok(away.includes('data-sweep="on"'), "the mark sweeps while the ceremony is away");
  assert.ok(!resting.includes("data-sweep"), "the resting mark does not sweep");
  // The password form signals progress in its own submit button; a
  // sweeping lockup above it would announce the same thing twice.
  assert.ok(!loginHtml({ mode: "password", state: "authorizing" }).includes("data-sweep"));
});

// The sweep is the only visual change while authorizing, and it is silent to
// assistive technology; this status element is the authorizing announcement.
test("the authorizing state announces itself to screen readers", () => {
  const html = loginHtml({ mode: "oidc", state: "authorizing" });
  assert.ok(html.includes('<span role="status" class="sr-only">Waiting for Google</span>'));
});

test("trusted_proxy offers no button at all", () => {
  const html = loginHtml({ mode: "trusted_proxy", state: "signed-out" });
  assert.ok(!html.includes('href="/auth/login"'), "there is no ceremony to start");
  assert.ok(html.includes("deployment proxy"));
});

test("password mode renders an accessible bounded credential form", () => {
  const html = loginHtml({ mode: "password", state: "signed-out" });
  assert.ok(html.includes("data-login-password"));
  assert.ok(html.includes('type="password"'));
  assert.ok(html.includes('autocomplete="current-password"'));
  assert.ok(html.includes('maxlength="72"'));
  assert.match(html, />\s*Sign in\s*<\/button>/);
  assert.ok(!html.includes("Google"));
});

test("password failure stays generic and offers a fresh attempt", () => {
  const html = loginHtml({ mode: "password", state: "failed" });
  assert.ok(html.includes('role="alert"'));
  assert.ok(html.includes("Sign-in failed. Check the password and try again."));
  assert.ok(html.includes("data-login-password"));
  assert.ok(!html.includes("locked"));
});

// The refused address is server-supplied and reaches an HTML context.
test("a refused address cannot inject markup", () => {
  const html = loginHtml({
    mode: "builtin",
    state: "denied",
    deniedEmail: `<img src=x onerror="alert(1)">@evil.test`,
  });
  assert.ok(!html.includes("<img src=x"), "no raw tag survives");
  assert.ok(!/<[a-z]+[^>]*\son\w+=/i.test(html), "no element carries an event handler");
  assert.ok(html.includes("&lt;img"), "it renders as visible text instead");
  assert.ok(html.includes("Access denied"));
});

test("the denied label reads Access denied at body-sm", () => {
  const html = loginHtml({ mode: "builtin", state: "denied", deniedEmail: "nobody@evil.test" });
  assert.ok(html.includes('<span class="text-body-sm font-medium text-error">Access denied</span>'));
  assert.ok(!html.includes("access denied"), "the lowercase micro label is gone");
});

// The reason line is caller-supplied and differs between a cold start and
// an expiry; it is the only copy that varies between the two callers.
test("the reason line renders when given and vanishes when not", () => {
  const withReason = loginHtml({
    mode: "builtin", state: "signed-out", reason: "Your browser session has expired.",
  });
  assert.ok(withReason.includes("Your browser session has expired."));
  const without = loginHtml({ mode: "builtin", state: "signed-out" });
  assert.ok(!without.includes("<p class=\"text-body-sm text-text-tertiary\""), without);
});

test("a reason cannot inject markup either", () => {
  const html = loginHtml({ mode: "builtin", state: "signed-out", reason: "<script>alert(1)</script>" });
  assert.ok(!html.includes("<script>alert"), "no raw script survives");
});

// The field is three layers: the two decorative ones must stay out of the
// accessibility tree and out of the way of the card's controls.
test("the field decoration is inert", () => {
  const html = loginHtml({ mode: "builtin", state: "signed-out" });
  for (const cls of ["login-field-grid", "login-field-veil"]) {
    const el = new RegExp(`<div([^>]*)class="${cls}"`).exec(html);
    assert.notEqual(el, null, `${cls} is rendered`);
    assert.ok(el![1]!.includes('aria-hidden="true"'), `${cls} is hidden from assistive tech`);
  }
});

// The screen replaces the workspace frame rather than sitting inside it,
// and data-theme now rides on <html> (see main.ts), so the screen must NOT
// carry its own: re-declaring the ramp on an inner element freezes the
// composite tokens against the light palette.
test("the screen carries no theme of its own", () => {
  const html = loginHtml({ mode: "builtin", state: "signed-out" });
  const main = /<main([^>]*)>/.exec(html);
  assert.notEqual(main, null);
  assert.ok(!/data-theme=/.test(main![1]!), `theme must not ride on the screen: ${main![1]}`);
});

test("setup needs a valid link before offering a password form", async () => {
  const { setupHtml, takeSetupToken } = await import("./login.ts");
  const missing = setupHtml("missing");
  assert.match(missing, /Create your password/);
  assert.match(missing, /Paste your setup link or token/);
  assert.match(missing, /<pre[^>]*><code[^>]*>noise-deck password reset<\/code><\/pre>/);
  assert.ok(!/Restart Deck/.test(missing));
  assert.ok(!missing.includes("data-password-editor"));
  assert.match(missing, /data-setup-token-input[^>]*autocomplete="off" spellcheck="false"/);
  const ready = setupHtml("ready");
  assert.match(ready, /data-password-editor/);
  assert.match(ready, /autocomplete="new-password"/);
  assert.ok(!ready.includes('autocomplete="current-password"'));
  assert.ok(!ready.includes("Sign in"));
  assert.ok(!ready.includes("data-setup-token"));
  const checking = setupHtml("checking");
  assert.match(checking, /role="status"[^>]*>.*Checking your setup link/s);
  assert.ok(!checking.includes("data-setup-token"), "no paste form races the link's own check");
  assert.ok(!checking.includes("data-password-editor"));
  assert.equal(takeSetupToken({ hash: "#token=fragment", search: "?token=query" }), "fragment");
  assert.equal(takeSetupToken({ hash: "", search: "?token=query" }), "");
  assert.equal(takeSetupToken({ hash: "#token=", search: "?token=query" }), "");
  assert.equal(takeSetupToken({ hash: "", search: "" }), "");
});

test("each setup refusal says why, and only an expired link offers the reset command", async () => {
  const { setupHtml } = await import("./login.ts");
  const refused = (status: number | null, retryAfter: string | null = null) => setupHtml({ status, retryAfter });
  assert.match(refused(401), /invalid or expired/);
  assert.match(refused(401), /startup output and the .*setup-url.* file/);
  assert.match(refused(401), /ask the agent that installed Deck/);
  assert.match(refused(401), /If Deck is stopped/);
  assert.match(refused(401), /noise-deck password reset/);
  assert.match(refused(403), /does not accept this address/);
  assert.ok(!refused(403).includes("noise-deck password reset"));
  assert.match(refused(429, "17"), /Try again in 17 seconds\./);
  assert.match(refused(429, "1"), /Try again in 1 second\./);
  assert.match(refused(429), /Try again in a minute\./);
  assert.match(refused(null), /Deck did not answer/);
  assert.match(setupHtml("incomplete"), /cut short/);
  for (const html of [refused(401), refused(403), refused(429, "5"), refused(null), setupHtml("incomplete")]) {
    assert.match(html, /role="alert" class="text-center/);
    assert.match(html, /data-setup-token-input/);
    assert.ok(!html.includes("Restart Deck"));
  }
});

test("a pasted setup link or bare token yields the token", async () => {
  const { setupTokenFrom } = await import("./login.ts");
  assert.equal(setupTokenFrom("https://deck.example:8443/setup#token=abc-DEF_123"), "abc-DEF_123");
  assert.equal(setupTokenFrom("  https://deck.example/setup#token=abc\n"), "abc");
  assert.equal(setupTokenFrom("https://deck.example/se\ntup#tok\nen=ab\nc"), "abc");
  assert.equal(setupTokenFrom(" abc-DEF_123 "), "abc-DEF_123");
  assert.equal(setupTokenFrom("#token=abc"), "abc");
  assert.equal(setupTokenFrom("https://deck.example/setup"), "");
  assert.equal(setupTokenFrom("https://deck.example/setup#token="), "");
  assert.equal(setupTokenFrom("   "), "");
});

// Drives /setup with a scripted check endpoint: each mount reads `hash`, and
// `paste` submits the paste form the current screen offers.
async function setupPage(valid: (token: string) => boolean) {
  const { mountSetup } = await import("./login.ts");
  const location = { hash: "", search: "", replace: () => {} };
  const urls: unknown[] = [];
  const checked: string[] = [];
  const screens: string[] = [];
  let submit: (() => void) | null = null;
  const input = { value: "", focus: () => {} };
  const form = {
    querySelector: (selector: string) => selector === "[data-setup-token-input]" ? input : { disabled: false },
    addEventListener: (_type: string, handler: (event: { preventDefault(): void }) => void) => {
      submit = () => handler({ preventDefault: () => {} });
    },
  };
  const container = {
    set innerHTML(html: string) { screens.push(html); submit = null; },
    querySelector: (selector: string) => selector === "[data-setup-token]" && screens.at(-1)!.includes("data-setup-token") ? form : null,
  } as unknown as HTMLElement;
  const window = {
    location,
    history: { replaceState: (_state: unknown, _title: string, url: unknown) => { urls.push(url); location.hash = ""; } },
  };
  const fetch = async (_url: string, init: { body: string }) => {
    const { token } = JSON.parse(init.body) as { token: string };
    checked.push(token);
    return new Response(null, { status: valid(token) ? 204 : 401 });
  };
  const run = async (body: () => Promise<void>) => {
    const g = globalThis as Record<string, unknown>;
    const saved = { window: g.window, fetch: g.fetch };
    g.window = window;
    g.fetch = fetch;
    try { await body(); } finally { g.window = saved.window; g.fetch = saved.fetch; }
  };
  const mount = (hash: string) => run(async () => { location.hash = hash; await mountSetup(container); });
  const paste = (text: string) => run(async () => {
    assert.ok(submit, "the screen offers the paste form");
    input.value = text;
    submit!();
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
  return { mount, paste, urls, checked, screens };
}

test("a restore that finds no fragment keeps the validated setup form", async () => {
  const page = await setupPage(() => true);
  await page.mount("#token=abc");
  await page.mount("");
  assert.deepEqual(page.urls, ["/setup", "/setup"]);
  assert.deepEqual(page.checked, ["abc", "abc"], "the restore checks the token it already held");
  assert.ok(page.screens.every((html) => !html.includes("data-setup-token")), "no paste form replaces the password form");
  assert.match(page.screens.at(-1)!, /data-password-editor/);
});

for (const [name, pasted] of [["link", "https://deck.example/setup#token=fresh"], ["bare token", "fresh"]] as const) {
  test(`a restore after a pasted ${name} keeps the pasted token's setup form`, async () => {
    const page = await setupPage((token) => token === "fresh");
    await page.mount("#token=expired");
    assert.match(page.screens.at(-1)!, /invalid or expired/);
    await page.paste(pasted);
    assert.match(page.screens.at(-1)!, /data-password-editor/);
    await page.mount("");
    assert.deepEqual(page.checked, ["expired", "fresh", "fresh"], "the restore checks the pasted token");
    assert.match(page.screens.at(-1)!, /data-password-editor/);
  });
}
