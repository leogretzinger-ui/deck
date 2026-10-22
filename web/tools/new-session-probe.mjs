// Exercise the complete picker, keyboard selection, and focus across sidebar polls.
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { existsSync } from "node:fs";
import { extname, join, resolve } from "node:path";
import { browser as getBrowser } from "./browser.mjs";

const dist = resolve(import.meta.dirname, "..", "dist");
if (!existsSync(join(dist, "preview.html"))) {
  throw new Error("dist/preview.html is missing — run pnpm build first (embed builds drop the preview)");
}

const mime = { ".css": "text/css", ".html": "text/html", ".js": "text/javascript", ".svg": "image/svg+xml", ".png": "image/png", ".woff2": "font/woff2" };
const server = createServer(async (req, res) => {
  const name = new URL(req.url, "http://x").pathname.slice(1);
  const path = join(dist, name);
  if (!path.startsWith(dist) || !existsSync(path)) {
    res.writeHead(404);
    res.end("not found");
    return;
  }
  res.writeHead(200, { "Content-Type": mime[extname(path)] ?? "application/octet-stream" });
  res.end(await readFile(path));
});

await new Promise((listening) => server.listen(0, "127.0.0.1", listening));
const origin = `http://127.0.0.1:${server.address().port}`;
let browser;
try {
  ({ browser } = await getBrowser());
  const context = await browser.newContext({ viewport: { width: 1400, height: 900 } });

  const openPicker = async (query = "") => {
    const page = await context.newPage();
    const errors = [];
    page.on("pageerror", (error) => errors.push(error.message));
    await page.goto(`${origin}/preview.html?dialog=new-session${query}`, { waitUntil: "networkidle" });
    await page.waitForSelector("[data-new-session-chips] [data-new-session-provider]");
    return { page, errors };
  };

  // The armed chip is the one the picker will start, and it is also the only
  // chip in the tab order: a row that arms one chip visually but leaves the
  // tab stop on another sends the keyboard user somewhere they cannot see.
  const armed = (page) => page.evaluate(() => {
    const chips = [...document.querySelectorAll("[data-new-session-provider]")];
    const selected = chips.filter((c) => c.dataset.selected === "true").map((c) => c.dataset.newSessionProvider);
    const tabbable = chips.filter((c) => c.getAttribute("tabindex") === "0").map((c) => c.dataset.newSessionProvider);
    return { selected, tabbable, order: chips.map((c) => c.dataset.newSessionProvider) };
  });

  {
    const { page, errors } = await openPicker();
    const first = await armed(page);
    assert.ok(first.order.length >= 3, `the picker needs several chips to walk: ${JSON.stringify(first.order)}`);

    // Arming starts from the filter, which is where focus lands when the
    // dialog opens: the arrows have to reach the row without a tab first.
    await page.focus("[data-new-session-filter]");
    await page.keyboard.press("ArrowRight");
    let state = await armed(page);
    assert.deepEqual(state.selected, [first.order[0]], "ArrowRight from the filter arms the first chip");
    assert.deepEqual(state.tabbable, [first.order[0]], "the armed chip owns the tab stop");

    await page.keyboard.press("ArrowRight");
    state = await armed(page);
    assert.deepEqual(state.selected, [first.order[1]], "ArrowRight steps forward one");

    await page.keyboard.press("ArrowLeft");
    await page.keyboard.press("ArrowLeft");
    state = await armed(page);
    assert.deepEqual(state.selected, [first.order.at(-1)], "ArrowLeft off the front wraps to the last chip");

    await page.keyboard.press("ArrowRight");
    state = await armed(page);
    assert.deepEqual(state.selected, [first.order[0]], "ArrowRight off the end wraps to the first chip");

    // Down and Up are the same walk: the chips wrap, so a grid the eye reads
    // in rows still moves one step per press.
    await page.keyboard.press("ArrowDown");
    state = await armed(page);
    assert.deepEqual(state.selected, [first.order[1]], "ArrowDown steps like ArrowRight");
    await page.keyboard.press("ArrowUp");
    state = await armed(page);
    assert.deepEqual(state.selected, [first.order[0]], "ArrowUp steps like ArrowLeft");

    assert.deepEqual(errors, [], "the picker threw while walking the chips");
    await page.close();
  }

  // A launch reveals the chosen provider's spinner and disables every other
  // choice, and then the dialog closes — so the evidence is gone by the time a
  // poll could read it. Record the reveal as it happens instead.
  const watchLaunch = (page) => page.evaluate(() => {
    const seen = { chip: null, rec: false, disabledAtReveal: null, recDisabledAtReveal: null };
    window.__launchWatch = seen;
    const observer = new MutationObserver((records) => {
      for (const record of records) {
        const el = record.target;
        if (!(el instanceof HTMLElement) || el.hidden) continue;
        const chip = el.getAttribute("data-new-session-spinner");
        const isRec = el.hasAttribute("data-new-session-rec-spinner");
        if (chip === null && !isRec) continue;
        if (chip !== null) seen.chip = chip;
        if (isRec) seen.rec = true;
        seen.disabledAtReveal ??= [...document.querySelectorAll("[data-new-session-provider]")]
          .every((b) => b.disabled);
        // Read here rather than after: the dialog closes on launch, so by the
        // time a poll could look, the control is gone and any claim about its
        // disabled state is unfalsifiable.
        const card = document.querySelector("[data-new-session-recommendation]");
        seen.recDisabledAtReveal ??= card instanceof HTMLButtonElement ? card.disabled : null;
      }
    });
    observer.observe(document.body, { subtree: true, attributes: true, attributeFilter: ["hidden"] });
  });
  const launched = (page) => page.evaluate(() => window.__launchWatch);

  {
    const { page, errors } = await openPicker();
    assert.equal(await page.locator("[data-new-session-recommendation]").count(), 0);
    await watchLaunch(page);
    await page.focus("[data-new-session-filter]");
    await page.keyboard.press("Enter");
    assert.equal((await launched(page)).chip, null, "Enter without a selection launches nothing");
    await page.keyboard.press("Tab");
    assert.equal(await page.evaluate(() => document.activeElement?.hasAttribute("data-new-session-provider")), true,
      "Tab from search reaches the first harness");
    await page.keyboard.press("Space");
    const after = await launched(page);
    assert.ok(after.chip !== null, "Space activates the focused harness");
    assert.equal(after.disabledAtReveal, true);
    assert.deepEqual(errors, []);
    await page.close();
  }

  {
    const { page, errors } = await openPicker();
    await watchLaunch(page);
    await page.focus("[data-new-session-filter]");
    await page.keyboard.press("ArrowRight");
    await page.keyboard.press("ArrowRight");
    const target = (await armed(page)).selected[0];
    await page.keyboard.press("Enter");
    const after = await launched(page);
    assert.equal(after.chip, target, `Enter did not start the armed chip: ${JSON.stringify(after)}`);
    assert.equal(after.rec, false, "Enter on an armed chip started the recommendation instead");
    assert.equal(after.disabledAtReveal, true, "a launch left other providers startable");
    assert.deepEqual(errors, [], "the picker threw on the chip launch");
    await page.close();
  }

  {
    const { page, errors } = await openPicker("&sidebar-poll=100");
    await page.locator("[data-new-session-filter]").fill("co");
    await page.keyboard.press("ArrowRight");
    await page.evaluate(() => {
      window.__pickerNode = document.querySelector("[data-new-session-modal]");
      window.__pickerFocus = document.activeElement;
    });
    const before = await armed(page);
    await page.waitForTimeout(650);
    assert.deepEqual(await armed(page), before, "polling preserves list order and keyboard selection");
    assert.equal(await page.evaluate(() => window.__pickerNode === document.querySelector("[data-new-session-modal]")), true,
      "polling preserves the picker DOM");
    assert.equal(await page.evaluate(() => window.__pickerFocus === document.activeElement), true,
      "polling preserves focus on the selected harness");
    assert.equal(await page.locator("[data-new-session-filter]").inputValue(), "co");
    assert.deepEqual(errors, []);
    await page.close();
  }

  // The phone presents the same complete inventory as touch-sized rows.
  {
    const phone = await browser.newContext({ viewport: { width: 393, height: 864 } });
    const page = await phone.newPage();
    const errors = [];
    page.on("pageerror", (error) => errors.push(error.message));
    await page.goto(`${origin}/preview.html?frame=phone&dialog=new-session`, { waitUntil: "networkidle" });
    await page.waitForSelector("[data-new-session-modal]");
    const seen = await page.evaluate(() => ({
      sections: [...document.querySelectorAll(".eyebrow")].map((el) => el.textContent.trim()),
      rows: [...document.querySelectorAll("[data-new-session-provider]")].map((el) => el.className),
      chips: document.querySelector("[data-new-session-provider].inline-flex") !== null,
      headShown: (() => {
        const head = document.querySelector("[data-new-session-modal] .dialog-head");
        return head !== null && getComputedStyle(head).display !== "none";
      })(),
      recommendation: document.querySelector("[data-new-session-recommendation]") !== null,
      firstRowLabel: document.querySelector("[data-new-session-provider]")?.textContent.trim() ?? null,
      providers: [...document.querySelectorAll("[data-new-session-provider]")]
        .map((el) => el.dataset.newSessionProvider),
      inventory: Number(document.querySelector("[data-new-session-filter]")
        ?.getAttribute("placeholder")?.match(/(\d+)/)?.[1] ?? 0),
    }));
    assert.deepEqual(seen.sections, [],
      `the phone sheet's sections are ${JSON.stringify(seen.sections)}`);
    assert.ok(seen.rows.length > 0 && seen.rows.every((c) => c.includes("menuitem")),
      "the phone sheet draws chips where the comp draws rows");
    assert.equal(seen.headShown, false, "the phone sheet draws a title row the comp does not");
    assert.equal(seen.recommendation, false,
      "the phone sheet draws the desktop recommendation card");
    assert.equal(seen.rows.length, seen.inventory,
      `the phone sheet lists ${seen.rows.length} of ${seen.inventory} harnesses`);
    assert.equal(new Set(seen.providers).size, seen.providers.length, "every harness appears once");
    assert.deepEqual(seen.providers, [...seen.providers].sort(), "phone keeps the served order");
    assert.deepEqual(errors, [], "the phone sheet threw");
    await phone.close();
  }

  console.log("new-session probe: all assertions passed");
} finally {
  await browser?.close();
  server.close();
}
