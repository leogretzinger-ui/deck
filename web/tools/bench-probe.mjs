// Probe the font bench with the browser sidecar.
import assert from "node:assert/strict";
import { createServer } from "vite";
import { readFile } from "node:fs/promises";
import { resolve } from "node:path";
import { browser as getBrowser } from "./browser.mjs";

const WEB = resolve(import.meta.dirname, "..");
const server = await createServer({ root: WEB, base: "/", server: { host: "127.0.0.1", port: 0 } });
await server.listen();
try {
const origin = server.resolvedUrls.local[0].replace(/\/$/, "");

const { browser, via } = await getBrowser();
console.log("browser:", via);
const context = await browser.newContext({ viewport: { width: 1480, height: 900 } });
// The sandbox has no route to Google Fonts anyway; abort the host explicitly
// so the refusal legs are deterministic rather than timing-dependent. Every
// attempt is recorded: the families the build bundles (its reference set,
// with the descriptors that make them the app's reference) must never be
// requested remotely, or a competing @font-face without those descriptors
// could win matching and move the reference the bench measures against.
const remoteRequests = [];
await context.route(/fonts\.(googleapis|gstatic)\.com/, (route) => {
  remoteRequests.push(route.request().url());
  route.abort();
});

// Read the font families from the dev server's selected stylesheet.
const bundled = await (async () => {
  const sheet = server.config.resolve.alias.find(({ find }) => find instanceof RegExp && find.test("@fonts"));
  assert.ok(sheet, "the dev server resolves its font stylesheet");
  const css = await readFile(sheet.replacement, "utf8");
  const families = new Set();
  let uiFamily = null;
  let proseFamily = null;
  for (const m of css.matchAll(/@font-face\s*\{[^}]*font-family:\s*"?([^;"\n]+)"?;/g)) families.add(m[1]);
  uiFamily = /:root\s*\{[^}]*--font-ui:\s*"([^"]+)"/.exec(css)?.[1];
  proseFamily = /:root\s*\{[^}]*--font-prose:\s*"([^"]+)"/.exec(css)?.[1];
  assert.ok(families.size > 0, "the build declares no @font-face");
  assert.ok(uiFamily, "the build's interface stack names no leading family");
  assert.ok(proseFamily, "the build's prose stack names no leading family");
  return { families, uiFamily, proseFamily };
})();
console.log("bundled families:", [...bundled.families].join(", "), "· interface:", bundled.uiFamily, "· prose:", bundled.proseFamily);
// A catalogue face no build bundles, so it only exists via the blocked host.
const remoteFace = "Libre Baskerville";
assert.ok(!bundled.families.has(remoteFace), `${remoteFace} is bundled; pick another remote-only face`);

const errors = [];
async function openBench() {
  const page = await context.newPage();
  page.on("pageerror", (e) => errors.push(String(e)));
  await page.goto(origin + "/bench.html");
  // Attached, not visible: a persisted compare mode legitimately hides the
  // showcase on load.
  await page.locator("[data-showcase]").waitFor({ state: "attached" });
  return page;
}

const page = await openBench();

// The tool's chrome is pinned: substitution must never re-set the controls.
{
  const chrome = await page.evaluate(() =>
    document.querySelector("[data-app]").style.getPropertyValue("--tc-chrome"));
  assert.ok(chrome.includes(bundled.uiFamily), `--tc-chrome = ${chrome}, want the build's interface face ${bundled.uiFamily}`);
}

// The substitution mechanism: the specimen container carries the role font
// VARIABLES; no element inside carries a per-element font-family write.
{
  const mech = await page.evaluate(() => {
    const show = document.querySelector("[data-showcase]");
    return {
      vars: ["--font-display", "--font-ui", "--font-prose", "--font-mono"]
        .every((v) => show.style.getPropertyValue(v) !== ""),
      // The markup's own chrome pins are var() references by design; what
      // the rule forbids is the LOGIC writing a concrete family onto an
      // element instead of re-declaring the role variable on the container.
      perElement: [...show.querySelectorAll("*")]
        .filter((el) => el.style.fontFamily !== "" && !el.style.fontFamily.startsWith("var(")).length,
    };
  });
  assert.equal(mech.vars, true, "the specimen container does not re-declare the role font variables");
  assert.equal(mech.perElement, 0, "substitution wrote font-family per element");
}

// Compare mode: three panes, reference in pane 1.
await page.locator('[data-mode="compare"]').click();
await page.locator("[data-pane]").first().waitFor();
assert.equal(await page.locator("[data-pane]").count(), 3, "compare must show three panes");

const setPane = async (i, face) => {
  await page.locator("[data-pane] [data-pane-font]").nth(i).selectOption(face);
};

// Leg 1 — the serif-fallback assert refuses to report numbers for a face
// that cannot load: the remote-only face exists only via the blocked host.
{
  await setPane(0, remoteFace);
  await page.locator('[data-match="x"]').click();
  await page.locator("[data-note]").filter({ hasText: "did not load" }).waitFor({ timeout: 10_000 });
  const note = await page.locator("[data-note]").innerText();
  assert.match(note, new RegExp(`${remoteFace}.*did not load — no numbers reported`),
    `the refusal must name the face: ${note}`);
  const trim = await page.locator('[data-pane] [data-trim="s"]').first().inputValue();
  assert.equal(trim, "1", `an unloaded face wrote a size trim (${trim}) off the serif fallback`);
}

// Leg 2 — an always-present face produces stable, non-default numbers. The
// shipped prose face measures against the shipped mono reference.
{
  await setPane(0, bundled.proseFamily);
  await page.locator('[data-match="x"]').click();
  const read = async () => ({
    s: await page.locator('[data-pane] [data-trim="s"]').first().inputValue(),
    l: await page.locator('[data-pane] [data-trim="l"]').first().inputValue(),
    t: await page.locator('[data-pane] [data-trim="t"]').first().inputValue(),
  });
  let first;
  // document.fonts resolves asynchronously; the match settles when the trims
  // leave their defaults.
  for (let i = 0; i < 40; i++) {
    first = await read();
    if (first.s !== "1") break;
    await page.waitForTimeout(250);
    if (i % 8 === 7) await page.locator('[data-match="x"]').click();
  }
  assert.notEqual(first.s, "1", "a loadable face never produced a match number");
  await page.locator('[data-match="x"]').click();
  await page.waitForTimeout(400);
  const second = await read();
  assert.deepEqual(second, first,
    `the same face measured twice gave ${JSON.stringify(second)} then ${JSON.stringify(first)}`);
}

// Leg 3 — a trim belongs to the typeface, not the pane: the same family in
// another pane carries the trim that was set once.
{
  await setPane(1, bundled.proseFamily);
  const slider = page.locator("[data-pane]").first().locator('[data-trim="s"]');
  await slider.evaluate((el) => {
    el.value = "1.12";
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await page.waitForTimeout(300);
  const other = await page.locator("[data-pane]").nth(1).locator('[data-trim="s"]').inputValue();
  assert.equal(other, "1.12", `the trim did not carry with the typeface (pane 2 reads ${other})`);
  const third = await page.locator("[data-pane]").nth(2).locator('[data-trim="s"]').inputValue();
  assert.notEqual(third, "1.12", "a different family inherited another face's trim");
}

// Leg 4 — readouts track their sliders live (rail sliders carry data-out).
await page.locator('[data-mode="showcase"]').click();
{
  const slider = page.locator('[data-trim="s"][data-role="prose"]');
  await slider.evaluate((el) => {
    el.value = "1.06";
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
  const out = await page.locator('[data-out="s"][data-role="prose"]').innerText();
  assert.equal(out, "106%", `size readout = ${out}, want 106%`);
  const track = page.locator('[data-trim="t"][data-role="prose"]');
  await track.evaluate((el) => {
    el.value = "0.012";
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
  const tOut = await page.locator('[data-out="t"][data-role="prose"]').innerText();
  assert.equal(tOut, "+12", `track readout = ${tOut}, want +12`);
}

// Leg 5 — the persistence split: picks, mode and palette persist under
// bp.fontsub.*; trims are session-scoped and do not follow into a new tab.
{
  await page.locator('[data-mode="compare"]').click();
  await page.locator("[data-palette]").selectOption("violet");
  const keys = await page.evaluate(() => Object.keys(localStorage).filter((k) => !k.startsWith("bp.fontsub.")));
  assert.deepEqual(keys, [], `persistence escaped the bp.fontsub.* namespace: ${keys}`);
  const trimStore = await page.evaluate(() => sessionStorage.getItem("bp.fontsub.trims"));
  assert.ok(trimStore && trimStore.includes(bundled.proseFamily),
    "trims did not land in the session-scoped store");

  const fresh = await openBench();
  const restored = await fresh.evaluate(() => ({
    mode: localStorage.getItem("bp.fontsub.mode"),
    palette: document.documentElement.getAttribute("data-bench-palette"),
    compareShown: document.querySelector("[data-compare]").style.display !== "none",
    trims: sessionStorage.getItem("bp.fontsub.trims"),
  }));
  assert.equal(restored.mode, "compare", "mode did not persist");
  assert.equal(restored.palette, "violet", "palette did not persist");
  assert.equal(restored.compareShown, true, "the persisted mode was not restored");
  assert.equal(restored.trims, null, "trims leaked across the session boundary");
  const freshTrim = await fresh.locator("[data-pane]").first().locator('[data-trim="s"]').inputValue();
  assert.equal(freshTrim, "1", `a session-scoped trim survived into a new tab (${freshTrim})`);
  await fresh.close();
}

assert.deepEqual(errors, [], `page errors: ${errors.join("; ")}`);

// Every role specimen keeps its own spacing: the interface face's body-level
// tracking and word correction (nonzero under the free set) never reach a
// mono or prose specimen or a display sample, and a mono specimen carries
// the mono tracking at its own size.
{
  const spacing = await page.evaluate(() => {
    // What the specimen's own voice says its tracking is, resolved at the
    // specimen: the mono tracking, or the bench's prose trim (which the
    // earlier legs may have moved off zero — a trim belongs to the face).
    const resolvedAt = (el, declaration) => {
      const probe = document.createElement("span");
      probe.style.cssText = `letter-spacing:${declaration}`;
      el.appendChild(probe);
      const v = getComputedStyle(probe).letterSpacing;
      probe.remove();
      return v;
    };
    const rows = [];
    for (const el of document.querySelectorAll("[data-wrole=mono], #bench code, .bp-code")) rows.push({ role: "mono", ws: getComputedStyle(el).wordSpacing, ls: getComputedStyle(el).letterSpacing, want: resolvedAt(el, "var(--type-mono-tracking)") });
    for (const el of document.querySelectorAll("[data-wrole=prose], .bp-prose, .bp-source__quote")) rows.push({ role: "prose", ws: getComputedStyle(el).wordSpacing, ls: getComputedStyle(el).letterSpacing, want: resolvedAt(el, "var(--tc-prose-ls, normal)") });
    for (const el of document.querySelectorAll(".bp-display-1, .bp-display-2, .bp-display-3")) rows.push({ role: "display", ws: getComputedStyle(el).wordSpacing });
    return { rows, body: getComputedStyle(document.body).letterSpacing };
  });
  assert.ok(spacing.rows.filter((r) => r.role === "mono").length >= 3, "mono specimens are tagged");
  assert.ok(spacing.rows.filter((r) => r.role === "prose").length >= 3, "prose specimens are tagged");
  for (const r of spacing.rows) {
    assert.equal(r.ws, "0px", `${r.role} specimen inherited word-spacing ${r.ws}`);
    if (r.role === "mono") assert.equal(r.ls, r.want, `mono specimen tracks ${r.ls}, the mono tracking here is ${r.want}`);
    if (r.role === "prose") assert.ok(r.ls === r.want || (r.want === "normal" && r.ls === "0px"), `prose specimen tracks ${r.ls}, its trim says ${r.want}`);
  }
  console.log(`role spacing: ${spacing.rows.length} specimens keep their own (body tracks ${spacing.body})`);
}

// The reference faces are never fetched: after the page mounted its roles and
// panes (which load candidates), no remote request names a bundled family.
{
  // Reset re-sets every role to the reference, the loading path a fresh
  // mount also takes; the rail may be collapsed here, so click through the
  // DOM rather than the pointer.
  await page.evaluate(() => document.querySelector("[data-reset]").click());
  await page.waitForTimeout(200);
  const named = remoteRequests.filter((u) => {
    const fam = decodeURIComponent(new URL(u).searchParams.get("family") ?? "").split(":")[0].replace(/\+/g, " ");
    return bundled.families.has(fam);
  });
  assert.deepEqual(named, [], `remote requests for bundled families: ${named.join(", ")}`);
  console.log(`remote font requests: ${remoteRequests.length}, none for a bundled family`);
}

await context.close();
await browser.close();
} finally {
  await server.close();
}
console.log("bench-probe: all assertions passed");
