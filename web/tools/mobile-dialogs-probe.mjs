import assert from "node:assert/strict";
import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { existsSync } from "node:fs";
import { extname, join, resolve } from "node:path";
import { browser as getBrowser } from "./browser.mjs";
import { assertMobileSheetMotion } from "./mobile-sheet-motion.mjs";

const WEB = resolve(import.meta.dirname, "..");
const DIST = join(WEB, "dist");
const MIME = {
  ".css": "text/css",
  ".html": "text/html",
  ".js": "text/javascript",
  ".mjs": "text/javascript",
  ".png": "image/png",
  ".svg": "image/svg+xml",
  ".woff2": "font/woff2",
};

if (!existsSync(join(DIST, "preview.html"))) {
  throw new Error("dist/preview.html is missing — run `pnpm build` first");
}

function serve(dir) {
  const server = createServer(async (req, res) => {
    const path = join(dir, decodeURIComponent(new URL(req.url, "http://x").pathname));
    if (!path.startsWith(dir) || !existsSync(path)) {
      res.writeHead(404);
      res.end("not found");
      return;
    }
    res.writeHead(200, { "Content-Type": MIME[extname(path)] ?? "application/octet-stream" });
    res.end(await readFile(path));
  });
  return new Promise((resolveServer) =>
    server.listen(0, "127.0.0.1", () => resolveServer(server)));
}

async function load(page, origin, query = "") {
  await page.goto(`${origin}/preview.html${query}`, { waitUntil: "networkidle" });
  await page.waitForSelector("[data-projects-menu]", { state: "attached" });
  await assertScrollBoundaries(page, query || "terminal");
  await assertBodyScale(page);
}

async function assertBodyScale(page) {
  const failures = await page.evaluate(() => {
    const scope = document.querySelector('[data-viewport="narrow"]') ?? document.body;
    const sample = document.createElement("span");
    sample.className = "text-body-sm";
    sample.textContent = "Type scale probe";
    scope.append(sample);
    const nodes = [...document.querySelectorAll(".text-body-sm")];
    const failures = nodes.filter((node) => {
      const style = getComputedStyle(node);
      const expected = parseFloat(style.getPropertyValue("--type-body-sm-size"))
        * (parseFloat(style.getPropertyValue("--face-ui-size")) || 1);
      // Form controls deliberately retain the 16px iOS zoom floor.
      return !node.matches("input, select, textarea") && node.getClientRects().length > 0
        && Math.abs(parseFloat(style.fontSize) - expected) > 0.1;
    }).map((node) => `${node.tagName}.${node.className}`);
    sample.style.setProperty("--face-ui-size", "1.1");
    sample.style.setProperty("--face-ui-leading", "1.2");
    const scaled = getComputedStyle(sample);
    if (Math.abs(parseFloat(scaled.fontSize)
        - parseFloat(scaled.getPropertyValue("--type-body-sm-size")) * 1.1) > 0.1) {
      failures.push("local typeface trim was ignored");
    }
    const expectedLeading = parseFloat(scaled.fontSize)
      * parseFloat(scaled.getPropertyValue("--type-body-sm-lh")) * 1.2;
    if (Math.abs(parseFloat(scaled.lineHeight) - expectedLeading) > 0.1) {
      failures.push("local leading trim was ignored");
    }
    sample.remove();
    return failures;
  });
  assert.deepEqual(failures, [], "body-small utilities must follow the local type ramp");
}

async function assertScrollBoundaries(page, label) {
  const result = await page.evaluate(() => {
    const touchLayout = matchMedia("(max-width: 600px), (pointer: coarse)").matches;
    const frame = document.querySelector('[data-viewport="narrow"]');
    const scope = touchLayout ? document : frame;
    const scrollers = scope === null ? [] : [...scope.querySelectorAll("*")].filter((node) => {
      const style = getComputedStyle(node);
      return /(auto|scroll)/.test(`${style.overflowX} ${style.overflowY}`);
    });
    if (touchLayout) scrollers.push(document.documentElement, document.body);
    return {
      root: getComputedStyle(document.documentElement).overscrollBehavior,
      touchLayout,
      checked: scrollers.length,
      failures: scrollers.filter((node) => getComputedStyle(node).overscrollBehavior !== "none")
        .map((node) => `${node.tagName}.${node.className}`),
    };
  });
  assert.deepEqual(result.failures, [], `${label}: a phone scroller permits boundary actions`);
  assert.equal(result.root, result.touchLayout ? "none" : "auto",
    `${label}: viewport scroll boundary policy`);
  assert.ok(result.checked > 0 || !result.touchLayout, `${label}: no phone scrollers checked`);
}

// The Open project dialog opens from the PROJECTS header menu on the
// desktop; the phone sheet carries the same action as the folder cell in
// its bottom bar (comp mobile-r8 screen 02), because the eyebrow leaves
// with the desktop drawer.
async function openNewProjectDialog(page) {
  const sheetFolder = page.locator("[data-sheet-open-project]");
  if (await sheetFolder.isVisible()) {
    await sheetFolder.click();
    await page.locator('[data-project-source="local"]').click();
    return;
  }
  await page.locator("[data-projects-menu]").click();
  await page.locator("[data-projects-menu-new-project]").click();
  await page.locator('[data-project-source="local"]').click();
}

async function expandSidebar(page) {
  const workspace = page.locator("[data-workspace]");
  if (await workspace.getAttribute("data-collapsed") === "true") {
    // On a phone the desktop re-expand affordance is hidden; the session list
    // opens from the titlebar's Sessions button instead.
    const expandButton = page.locator('[data-region="sidebar-expand"]');
    if (await expandButton.isVisible().catch(() => false)) {
      await expandButton.click();
    } else {
      await page.getByRole("button", { name: "Sessions", exact: true }).click();
    }
    // Wait for the phone sheet to settle before reaching controls inside it.
    await page.waitForFunction(() => {
      const sidebar = document.querySelector('[data-region="sidebar"]');
      if (!(sidebar instanceof HTMLElement)) return false;
      return sidebar.getBoundingClientRect().left >= -1
        && new DOMMatrixReadOnly(getComputedStyle(sidebar).transform).m42 === 0;
    });
  }
}

// The sheet's close path is the swipe (comp mobile-r8 screen 02): drag a
// row 60px left to park it open, then tap the revealed Close.
async function swipeRowOpen(page, row) {
  const box = await row.boundingBox();
  const y = box.y + box.height / 2;
  const startX = box.x + box.width - 30;
  await row.dispatchEvent("pointerdown", { pointerType: "touch", clientX: startX, clientY: y, isPrimary: true });
  await row.dispatchEvent("pointermove", { pointerType: "touch", clientX: startX - 30, clientY: y, isPrimary: true });
  await row.dispatchEvent("pointermove", { pointerType: "touch", clientX: startX - 80, clientY: y, isPrimary: true });
  await row.dispatchEvent("pointerup", { pointerType: "touch", clientX: startX - 80, clientY: y, isPrimary: true });
}

async function openSessionRemove(page) {
  const row = page.locator("[data-session]:visible").first();
  const sessionID = await row.getAttribute("data-session");
  await swipeRowOpen(page, row);
  const close = row.locator("[data-session-swipe-close]");
  await close.waitFor({ state: "visible" });
  await close.click();
  return { sessionID, trigger: row };
}

async function visibleDialog(page) {
  const dialog = page.locator('[role="dialog"]:visible').last();
  await dialog.waitFor();
  return dialog;
}

// A computed style is empty while the element is still being styled, and
// Number.parseFloat("") is NaN, which fails every numeric comparison — so an
// unmeasurable moment reads exactly like a real violation. fontSizePx waits
// for a value the browser will actually report and names the raw string when
// it never becomes one, so a timing failure cannot be mistaken for a style
// failure again.
async function fontSizePx(locator, what) {
  let raw = "";
  const deadline = Date.now() + 5000;
  while (Date.now() < deadline) {
    raw = await locator.evaluate((node) => getComputedStyle(node).fontSize);
    const px = Number.parseFloat(raw);
    if (Number.isFinite(px)) return px;
    await new Promise((resolve) => setTimeout(resolve, 25));
  }
  throw new Error(`${what} never reported a font size (last value ${JSON.stringify(raw)})`);
}

async function assertDialog(page, label) {
  const dialog = await visibleDialog(page);
  const result = await dialog.evaluate((node) => {
    const frame = node.closest(".scrim");
    if (!(frame instanceof HTMLElement)) throw new Error("dialog has no scrim frame");
    const frameRect = frame.getBoundingClientRect();
    const frameStyle = getComputedStyle(frame);
    const left = frameRect.left + Number.parseFloat(frameStyle.paddingLeft);
    const top = frameRect.top + Number.parseFloat(frameStyle.paddingTop);
    const right = frameRect.right - Number.parseFloat(frameStyle.paddingRight);
    const bottom = frameRect.bottom - Number.parseFloat(frameStyle.paddingBottom);
    const rect = node.getBoundingClientRect();
    const activeInside = document.activeElement === node || node.contains(document.activeElement);
    return {
      activeInside,
      bottom: rect.bottom,
      clientHeight: node.clientHeight,
      clientWidth: node.clientWidth,
      left: rect.left,
      overflowY: getComputedStyle(node).overflowY,
      right: rect.right,
      scrollHeight: node.scrollHeight,
      scrollWidth: node.scrollWidth,
      top: rect.top,
      viewportBottom: bottom,
      viewportLeft: left,
      viewportRight: right,
      viewportTop: top,
    };
  });
  assert.equal(result.activeInside, true, `${label}: initial focus is outside the dialog`);
  const geometry = JSON.stringify(result);
  assert.ok(result.left >= result.viewportLeft - 1, `${label}: dialog overflows left ${geometry}`);
  assert.ok(result.right <= result.viewportRight + 1, `${label}: dialog overflows right ${geometry}`);
  assert.ok(result.top >= result.viewportTop - 1, `${label}: dialog overflows top ${geometry}`);
  assert.ok(result.bottom <= result.viewportBottom + 1, `${label}: dialog overflows bottom ${geometry}`);
  assert.ok(result.scrollWidth <= result.clientWidth + 1, `${label}: horizontal content overflow`);
  if (result.scrollHeight > result.clientHeight + 1) {
    assert.match(result.overflowY, /auto|scroll/, `${label}: tall content is not scrollable`);
  }

  const focusable = dialog.locator(
    'button:not([disabled]):visible,input:not([disabled]):visible,select:not([disabled]):visible,textarea:not([disabled]):visible,a[href]:visible,[tabindex]:not([tabindex="-1"]):visible',
  );
  const count = await focusable.count();
  assert.ok(count > 0, `${label}: dialog has no focusable control`);
  for (let i = 0; i < count; i += 1) {
    const control = focusable.nth(i);
    await control.scrollIntoViewIfNeeded();
    const box = await control.boundingBox();
    assert.ok(box !== null, `${label}: control ${i} is unreachable`);
    assert.ok(box.x >= -1 && box.x + box.width <= page.viewportSize().width + 1,
      `${label}: control ${i} overflows horizontally`);
  }

  const touchTargets = dialog.locator(
    'button:not([disabled]):visible,input:not([disabled]):not([type="hidden"]):visible,select:not([disabled]):visible,textarea:not([disabled]):visible,[role="button"]:visible',
  );
  const touchTargetCount = await touchTargets.count();
  for (let i = 0; i < touchTargetCount; i += 1) {
    const box = await touchTargets.nth(i).boundingBox();
    assert.ok(box !== null, `${label}: touch target ${i} is unreachable`);
    assert.ok(box.width >= 43.5 && box.height >= 43.5,
      `${label}: touch target ${i} is ${box.width}×${box.height}, below 44×44`);
  }
  // From the INITIAL focus state — before the probe touches anything —
  // reverse Tab must stay inside the trap. A container-focused dialog
  // (the :dialog initial state) once let the first Shift+Tab fall through
  // to the page behind the scrim.
  await page.keyboard.press("Shift+Tab");
  assert.equal(await dialog.evaluate((node) => node.contains(document.activeElement)), true,
    `${label}: the first Shift+Tab left the dialog`);
  await focusable.last().focus();
  await page.keyboard.press("Tab");
  assert.equal(await focusable.first().evaluate((node) => document.activeElement === node), true,
    `${label}: Tab escapes the dialog`);
  await page.keyboard.press("Shift+Tab");
  assert.equal(await focusable.last().evaluate((node) => document.activeElement === node), true,
    `${label}: Shift+Tab escapes the dialog`);
}

async function setLiveFrameHeight(page, height) {
  await page.locator("[data-workspace]").evaluate((node, value) => {
    node.style.height = `${value}px`;
    node.style.transform = "translateY(0px)";
  }, height);
  await page.evaluate(() => new Promise((resolveFrame) =>
    requestAnimationFrame(() => requestAnimationFrame(resolveFrame))));
}

async function restoreLiveFrame(page) {
  await page.locator("[data-workspace]").evaluate((node) => {
    node.style.removeProperty("height");
    node.style.removeProperty("transform");
  });
  await page.evaluate(() => new Promise((resolveFrame) =>
    requestAnimationFrame(() => requestAnimationFrame(resolveFrame))));
}

async function viewportMetrics(page) {
  return page.evaluate(() => ({
    innerWidth: window.innerWidth,
    scale: window.visualViewport?.scale ?? 1,
    width: window.visualViewport?.width ?? window.innerWidth,
  }));
}

async function expectEscapeRestore(page, dialogLabel, returnSelector) {
  await page.keyboard.press("Escape");
  await page.waitForFunction((name) =>
    ![...document.querySelectorAll('[role="dialog"]')]
      .some((node) => node.getAttribute("aria-label") === name), dialogLabel);
  if (returnSelector !== null) {
    const restored = await page.locator(returnSelector).first().evaluate((node) => ({
      active: document.activeElement?.outerHTML ?? "",
      matches: document.activeElement === node,
      target: node.outerHTML,
    }));
    assert.equal(restored.matches, true,
      `${dialogLabel}: focus was not restored ${JSON.stringify(restored)}`);
  }
}

async function expectBackdropDismiss(page, scrimSelector, dialogLabel) {
  await page.locator(scrimSelector).evaluate((node) =>
    node.dispatchEvent(new MouseEvent("click", { bubbles: true })));
  await page.waitForFunction((name) =>
    ![...document.querySelectorAll('[role="dialog"]')]
      .some((node) => node.getAttribute("aria-label") === name), dialogLabel);
}

async function runWidth(browser, origin, width) {
  const context = await browser.newContext({
    viewport: { width, height: 640 },
    hasTouch: true,
    isMobile: true,
  });
  const page = await context.newPage();
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  try {
    await load(page, origin);
    await assertMobileSheetMotion(page, `${width}px preview`);
    await assertMobileSheetMotion(page, `${width}px preview`, true);
    await page.emulateMedia({ reducedMotion: "no-preference" });
    const panelsButton = page.getByRole("button", { name: "Panels", exact: true });
    const targetBox = await panelsButton.boundingBox();
    assert.ok(targetBox && targetBox.width >= 44 && targetBox.height >= 44,
      `${width}px Panels keeps a 44px touch target`);
    await panelsButton.click();
    for (const label of ["Files", "Tasks", "Chats", "Assets"]) {
      const item = page.getByRole("menuitem", { name: label, exact: true });
      assert.equal(await item.isVisible(), true, `${width}px Panels exposes ${label}`);
    }
    await page.keyboard.press("Escape");
    assert.equal(await panelsButton.evaluate((el) => el === document.activeElement), true,
      `${width}px Escape returns focus to Panels`);
    await expandSidebar(page);
    await openNewProjectDialog(page);
    await assertDialog(page, `${width}px open project`);
    await page.locator("[data-project-path]").focus();
    await setLiveFrameHeight(page, 360);
    await assertDialog(page, `${width}px open project in a 360px live frame`);
    await restoreLiveFrame(page);
    // Escape returns focus to the control that opened the dialog: the
    // sheet's folder cell on a phone, the eyebrow on the desktop.
    await expectEscapeRestore(page, "Open project",
      (await page.locator("[data-sheet-open-project]").isVisible()) ? "[data-sheet-open-project]" : "[data-projects-menu]");

    await openNewProjectDialog(page);
    await page.locator("[data-project-browse]").click();
    await page.locator("[data-directory-dialog]").waitFor();
    await assertDialog(page, `${width}px directory picker`);
    // The listing owns a fixed 210px viewport, so the dialog holds one
    // size whether the open directory is full or empty.
    const listingBox = () => page.locator("[data-directory-list]").boundingBox();
    const dialogBox = () => page.locator("[data-directory-dialog]").boundingBox();
    const populated = { list: await listingBox(), dialog: await dialogBox() };
    assert.equal(Math.round(populated.list.height), 210,
      `${width}px directory listing is a fixed 210px viewport`);
    await setLiveFrameHeight(page, 360);
    await assertDialog(page, `${width}px directory picker in a 360px live frame`);
    await page.locator('[data-directory-path="/workspace/field-notes"]').click();
    await page.locator("[data-directory-new-folder]:not([disabled])").waitFor();
    await page.locator("[data-directory-new-folder]").click();
    await page.locator("[data-directory-new-name]").waitFor();
    assert.ok(
      await fontSizePx(page.locator("[data-directory-new-name]"), `${width}px new-folder input`) >= 16,
      `${width}px new-folder input can trigger iOS focus zoom`,
    );
    await assertDialog(page, `${width}px directory input and actions in a 360px live frame`);
    await restoreLiveFrame(page);
    await page.keyboard.press("Escape");
    await page.locator("[data-directory-dialog]").waitFor({ state: "detached" });
    assert.equal(await page.locator("[data-project-browse]").evaluate((node) => document.activeElement === node), true,
      `${width}px directory picker: focus was not restored to Browse`);

    await page.locator("[data-project-browse]").click();
    await page.locator('[data-directory-path="/workspace/field-notes"]').click();
    await page.locator("[data-directory-select]:not([disabled])").waitFor();
    const empty = { list: await listingBox(), dialog: await dialogBox() };
    assert.equal(Math.round(empty.list.height), 210,
      `${width}px empty directory keeps the 210px listing viewport`);
    assert.equal(Math.round(empty.dialog.height), Math.round(populated.dialog.height),
      `${width}px directory dialog changed height between full and empty listings`);
    const viewportBeforeSelection = await viewportMetrics(page);
    assert.equal(viewportBeforeSelection.innerWidth, width, `${width}px layout viewport is not actual width`);
    await page.locator("[data-directory-select]").click();
    await page.locator("[data-directory-dialog]").waitFor({ state: "detached" });
    const viewportAfterSelection = await viewportMetrics(page);
    assert.deepEqual(
      viewportAfterSelection,
      viewportBeforeSelection,
      `${width}px Choose directory changed visual viewport scale or width`,
    );
    assert.equal(await page.locator("[data-project-path]").inputValue(), "/workspace/field-notes",
      `${width}px Choose directory lost the canonical path`);
    assert.ok(
      await fontSizePx(page.locator("[data-project-path]"), `${width}px project-path input`) >= 16,
      `${width}px project-path input can trigger iOS focus zoom`,
    );
    assert.equal(await page.locator("[data-project-browse]").evaluate((node) => document.activeElement === node), true,
      `${width}px Choose directory did not focus the keyboard-usable Browse control`);
    // Every editable control in the route sits on the 16px iOS floor:
    // a smaller focused select or field zooms the whole page.
    {
      const select = page.locator("[data-project-git-profile]");
      if (await select.count() > 0) {
        const size = Number.parseFloat(await select.evaluate(
          (node) => getComputedStyle(node).fontSize,
        ));
        assert.ok(size >= 16,
          `${width}px identity select is ${size}px and can trigger iOS focus zoom`);
      }
    }
    // The field asks the server what the chosen path is; the readout below
    // it settles before the dialog's geometry is judged.
    await page.locator("[data-project-form]:not([data-inspecting])").waitFor();
    await assertDialog(page, `${width}px Open project after Choose directory`);
    await page.locator("[data-project-browse]").focus();
    await page.keyboard.press("Enter");
    await page.locator("[data-directory-dialog]").waitFor();
    await page.keyboard.press("Escape");
    await page.locator("[data-directory-dialog]").waitFor({ state: "detached" });
    await expectBackdropDismiss(page, "[data-modal-scrim]", "Open project");

    await page.locator("[data-new-session]").first()
      .locator("xpath=ancestor::li[1]").locator("[data-project-toggle]").hover();
    await page.locator("[data-new-session]").first().click();
    await assertDialog(page, `${width}px new session`);
    const providerFilter = page.locator("[data-new-session-filter]");
    assert.equal(await providerFilter.evaluate((node) => getComputedStyle(node).fontSize), "16px",
      `${width}px auto-focused picker filter prevents mobile focus zoom`);
    const providerButtons = page.locator("[data-new-session-provider]:visible");
    // Counted against the inventory the field itself names, not against a
    // number typed here: a hard-coded count agrees with whatever the picker
    // happens to render, so it called fifteen "the full inventory" while a
    // harness was being dropped.
    const inventory = Number((await providerFilter.getAttribute("placeholder"))?.match(/(\d+)/)?.[1] ?? 0);
    assert.ok(inventory > 0, `${width}px picker filter names no inventory`);
    assert.equal(await providerButtons.count(), inventory,
      `${width}px provider picker exposes the full inventory`);
    await providerFilter.press("ArrowDown");
    // ArrowDown selects the first row of the complete inventory.
    const firstListed = await providerButtons.first().getAttribute("data-new-session-provider");
    assert.equal(await page.evaluate(() => document.activeElement?.getAttribute("data-new-session-provider")),
      firstListed, `${width}px ArrowDown pins the first provider`);
    await page.keyboard.press("Enter");
    await page.locator("[data-new-session-modal]").waitFor({ state: "detached" });
    await page.locator("[data-new-session]").first()
      .locator("xpath=ancestor::li[1]").locator("[data-project-toggle]").hover();
    await page.locator("[data-new-session]").first().click();
    await assertDialog(page, `${width}px new session after commit`);
    const piButton = page.locator('[data-new-session-provider="pi"]');
    assert.equal(await piButton.getByText("pi", { exact: true }).count(), 1,
      `${width}px provider picker exposes pi`);
    const piBox = await piButton.boundingBox();
    assert.ok(piBox && piBox.x >= 0 && piBox.x + piBox.width <= width,
      `${width}px pi provider button escaped the viewport`);
    await expectEscapeRestore(page, "New session", "[data-new-session]");

    // The sheet has no per-row menus (comp mobile-r8 screen 02): swipe is
    // the row's one action surface. The row swipes open to reveal the
    // 110px Close action, a tap elsewhere closes it, and the evidence
    // tick stays on the resting row.
    const firstRow = page.locator("[data-session]:visible").first();
    const evidence = firstRow.locator("[data-session-meta]");
    assert.equal(await firstRow.locator("[data-session-menu]").isVisible(), false,
      `${width}px still exposes a per-row menu on the sheet`);
    assert.equal(await evidence.isVisible(), true, `${width}px hides session evidence`);
    // At rest the surface is layout-transparent (display:contents) and
    // the action is hidden; the gesture promotes the surface to the
    // sliding layer.
    assert.equal(await firstRow
      .locator("[data-session-swipe-surface]")
      .evaluate((node) => getComputedStyle(node).display), "contents",
      `${width}px the resting surface is not layout-transparent`);
    assert.equal(await firstRow
      .locator("[data-session-swipe-close]")
      .evaluate((node) => getComputedStyle(node).visibility), "hidden",
      `${width}px the Close action is painted at rest`);
    await swipeRowOpen(page, firstRow);
    assert.equal(await firstRow.evaluate((node) => node.classList.contains("swipe-open")), true,
      `${width}px the swiped row did not park open`);
    await page.waitForFunction(() => {
      const surface = document.querySelector('[data-session].swipe-open [data-session-swipe-surface]');
      return surface !== null && getComputedStyle(surface).display === "flex"
        && getComputedStyle(surface).right === "100px";
    });
    const closeAction = firstRow.locator("[data-session-swipe-close]");
    const closeBox = await closeAction.boundingBox();
    assert.ok(closeBox && closeBox.width >= 100 && closeBox.height >= 43.5,
      `${width}px Close action is ${closeBox?.width}x${closeBox?.height}, not a usable target`);
    // Tap-away on another row closes without selecting it.
    const secondRow = page.locator("[data-session]:visible").nth(1);
    const selectedBefore = await page.locator('[data-session][aria-selected="true"]').getAttribute("data-session");
    await secondRow.click();
    assert.equal(await firstRow.evaluate((node) => node.classList.contains("swipe-open")), false,
      `${width}px tap-away left the row parked open`);
    assert.equal(
      await page.locator('[data-session][aria-selected="true"]').getAttribute("data-session"),
      selectedBefore,
      `${width}px the tap-away selected a session`,
    );

    await page.emulateMedia({ reducedMotion: "reduce" });
    await swipeRowOpen(page, firstRow);
    assert.equal(await firstRow.locator("[data-session-swipe-surface]").evaluate((node) =>
      getComputedStyle(node).transitionDuration), "0s",
    `${width}px reduced motion still animates swipe settling`);
    await secondRow.click();
    assert.equal(await firstRow.locator("[data-session-swipe-surface]").evaluate((node) =>
      getComputedStyle(node).display), "contents",
    `${width}px reduced-motion swipe close waits for an absent transition`);
    await page.emulateMedia({ reducedMotion: "no-preference" });

    let { sessionID: removeSessionID } = await openSessionRemove(page);
    await assertDialog(page, `${width}px close session`);
    assert.equal(
      await page.getByRole("dialog", { name: "Close this session?" }).getByText(
        "Ends the session's tmux process when this Deck still owns it; otherwise removes the record only. The recording is kept either way.",
        { exact: true },
      ).count(),
      1,
      `${width}px close session copy does not state the recording is kept`,
    );
    await expectEscapeRestore(
      page,
      "Close this session?",
      `[data-session="${removeSessionID}"]`,
    );

    ({ sessionID: removeSessionID } = await openSessionRemove(page));
    await page.locator("[data-remove-confirm-btn]").click();
    await page.locator("[data-remove-confirm]").waitFor({ state: "detached" });
    assert.equal(await page.locator("[data-modal-scrim]").count(), 0,
      `${width}px successful removal left modal chrome visible`);
    assert.equal(
      await page.locator('[data-project-toggle="proj-1"]:focus').count(),
      1,
      `${width}px successful removal did not restore focus to its project`,
    );

    await load(page, origin, "?delayed-removal=1");
    await expandSidebar(page);
    const delayed = await openSessionRemove(page);
    const delayedSessionID = delayed.sessionID;
    const terminalBeforeRemoval = await page.locator('[data-region="terminal"]').innerText();
    await page.locator("[data-remove-confirm-btn]").click();
    await page.locator("[data-remove-confirm-btn][disabled]").waitFor();
    assert.equal(
      await page.locator(`[data-session="${delayedSessionID}"]`).count(),
      1,
      `${width}px delayed removal dropped the session row before confirmation`,
    );
    assert.equal(
      await page.locator('[data-region="terminal"]').innerText(),
      terminalBeforeRemoval,
      `${width}px delayed removal replaced the terminal presentation`,
    );
    await page.locator("[data-remove-confirm]").waitFor({ state: "detached" });
    assert.equal(
      await page.locator(`[data-session="${delayedSessionID}"]`).count(),
      0,
      `${width}px confirmed removal left the session row mounted`,
    );
    assert.equal(
      await page.locator('[data-project-toggle="proj-1"]:focus').count(),
      1,
      `${width}px delayed removal did not restore focus to its project`,
    );

    await load(page, origin);
    await expandSidebar(page);

    const projectMenu = page.locator("[data-project-menu]").first();
    await projectMenu.locator("xpath=ancestor::li[1]").locator("[data-project-toggle]").hover();
    await projectMenu.click({ force: true });
    await page.locator("[data-project-menu-remove]").click();
    await assertDialog(page, `${width}px remove project`);
    await expectEscapeRestore(page, "Remove field-notes from the sidebar?", "[data-project-menu]");

    // The sheet keeps every Projects-menu command reachable: its ellipsis
    // opens the same menu the desktop eyebrow anchors, and the bottom bar's
    // New session is a real opener with its own focus return.
    // An empty expanded group draws exactly one New session affordance:
    // the empty rail's own row. The phone's in-group chrome row must stay
    // suppressed there, or the group shows two adjacent creation controls.
    {
      const emptyToggle = page.locator('[data-project-toggle="proj-2"]');
      if (await emptyToggle.isVisible().catch(() => false)) {
        if ((await emptyToggle.getAttribute("aria-expanded")) !== "true") {
          await emptyToggle.click();
        }
        const rows = page.locator("#sessions-proj-2 > li:visible", { hasText: "New session" });
        assert.equal(await rows.count(), 1,
          `${width}px empty group draws ${await rows.count()} New session rows`);
        // On a phone the row is a 44px touch target.
        if (width <= 600) {
          const box = await rows.first().locator("button").boundingBox();
          assert.ok(box !== null && box.height >= 43.5,
            `${width}px empty-group New session row is ${box?.height}px, below 44px`);
        }
        await emptyToggle.click();
      }
    }
    // A populated group offers no in-group New session row at all: the
    // project row's + and the sheet's bottom bar are the ways in.
    {
      const populatedToggle = page.locator('[data-project-toggle="proj-1"]');
      if (await populatedToggle.isVisible().catch(() => false)) {
        if ((await populatedToggle.getAttribute("aria-expanded")) !== "true") {
          await populatedToggle.click();
        }
        const rows = page.locator("#sessions-proj-1 > li:visible", { hasText: "New session" });
        assert.equal(await rows.count(), 0,
          `${width}px populated group draws ${await rows.count()} New session rows`);
      }
    }
    if (await page.locator("[data-sidebar-sheet-menu]").isVisible()) {
      await page.locator("[data-sidebar-sheet-menu]").click();
      // The popover clamps to the viewport: its trigger sits near the right
      // edge, and an unclamped left-anchored menu ran 92px off-screen
      // (design review M2). Placement lands on a later tick, so the
      // assertion waits for the inline position — a just-mounted menu sits
      // at its host's origin and would pass vacuously.
      {
        await page.waitForFunction(() =>
          (document.querySelector("[data-projects-menu-popover]")?.style.left ?? "") !== "");
        const box = await page.locator("[data-projects-menu-popover]").boundingBox();
        const vw = page.viewportSize().width;
        assert.ok(box !== null && box.x >= 0 && box.x + box.width <= vw + 0.5,
          `${width}px sheet menu leaves the viewport: ${JSON.stringify(box)} in ${vw}`);
      }
      for (const cmd of ["projects-menu-sort", "projects-menu-expand-all", "projects-menu-collapse-all"]) {
        assert.equal(await page.locator(`[data-${cmd}]`).isVisible(), true,
          `${width}px sheet menu lost ${cmd}`);
      }
      await page.keyboard.press("Escape");
      await page.locator("[data-projects-menu-popover]").waitFor({ state: "detached" }).catch(() => {});

      // The menu's dialog rows hand focus back to the sheet's ellipsis —
      // the trigger that anchored them — not the hidden desktop eyebrow.
      for (const [row, dialogSel] of [
        ["projects-menu-new-session", "[data-new-session-modal]"],
        ["projects-menu-new-project", "[data-project-source-dialog]"],
      ]) {
        await page.locator("[data-sidebar-sheet-menu]").click();
        await page.locator(`[data-${row}]`).click();
        await page.locator(dialogSel).waitFor();
        await page.keyboard.press("Escape");
        await page.locator(dialogSel).waitFor({ state: "detached" });
        const back = await page.evaluate(() =>
          document.activeElement?.hasAttribute("data-sidebar-sheet-menu") ?? false);
        assert.equal(back, true, `${width}px Escape from ${row} did not return focus to the sheet menu`);
      }

      await page.locator("[data-sheet-new-session]").click();
      await page.locator("[data-new-session-modal]").waitFor();
      await page.keyboard.press("Escape");
      await page.locator("[data-new-session-modal]").waitFor({ state: "detached" });
      const active = await page.evaluate(() =>
        document.activeElement?.hasAttribute("data-sheet-new-session") ?? false);
      assert.equal(active, true, `${width}px Escape did not return focus to the sheet's New session`);
    }

    // The sheet closes by its own X; the desktop drawer by the collapse cell.
    if (await page.locator("[data-sidebar-sheet-close]").isVisible()) {
      await page.locator("[data-sidebar-sheet-close]").click();
    } else {
      await page.locator("[data-collapse]").click();
    }
    await page.getByRole("button", { name: "Panels", exact: true }).click();
    await page.getByRole("menuitem", { name: "Tasks", exact: true }).click();
    await page.locator('[data-region="tasks"][data-open="true"]').waitFor();
    const taskCard = page.locator("[data-tm-task]").first();
    await taskCard.click();
    await assertDialog(page, `${width}px task detail`);
    await setLiveFrameHeight(page, 360);
    await assertDialog(page, `${width}px task detail in a 360px live frame`);
    await restoreLiveFrame(page);
    await expectEscapeRestore(page, await visibleDialog(page).then((d) => d.getAttribute("aria-label")), "[data-tm-task]");

    const addPrd = page.locator('[data-tm-add-prd="header"]');
    await addPrd.click();
    await assertDialog(page, `${width}px add PRD`);
    await page.locator('[role="dialog"][aria-label="Add PRD"] button').first().focus();
    await setLiveFrameHeight(page, 360);
    await assertDialog(page, `${width}px add PRD in a 360px live frame`);
    await restoreLiveFrame(page);
    await expectEscapeRestore(page, "Add PRD", '[data-tm-add-prd="header"]');

    assert.deepEqual(errors, [], `${width}px page errors`);
    console.log(`mobile dialogs: ${width}px PASS`);
  } finally {
    await context.close();
  }
}

async function runAddPrdReturnTargets(browser, origin) {
  const context = await browser.newContext({
    viewport: { width: 390, height: 640 },
    hasTouch: true,
    isMobile: true,
  });
  const page = await context.newPage();
  try {
    await load(page, origin, "?tasks=onboarding");
    await page.getByRole("button", { name: "Panels", exact: true }).click();
    await page.getByRole("menuitem", { name: "Tasks", exact: true }).click();
    await page.locator('[data-region="tasks"][data-open="true"]').waitFor();
    for (const source of ["onboarding", "header"]) {
      const selector = `[data-tm-add-prd="${source}"]`;
      await page.locator(selector).click();
      await assertDialog(page, `Add PRD from ${source}`);
      await expectEscapeRestore(page, "Add PRD", selector);
    }
    console.log("mobile dialogs: exact Add PRD return targets PASS");
    await page.setViewportSize({ width: 1024, height: 768 });
    await assertDesktopOpenProjectTransition(page, origin);
  } finally {
    await context.close();
  }
}

async function assertDesktopOpenProjectTransition(page, origin) {
  await load(page, origin);
  await expandSidebar(page);
  await openNewProjectDialog(page);
  const before = await page.locator("[data-project-form]").boundingBox();
  assert.ok(before !== null, "desktop Open project dialog is unreachable");
  assert.equal(
    await page.locator("[data-project-path]").evaluate((node) => getComputedStyle(node).fontSize),
    "13px",
    "desktop project-path typography changed",
  );
  await page.locator("[data-project-browse]").click();
  await page.locator('[data-directory-path="/workspace/field-notes"]').click();
  await page.locator("[data-directory-select]:not([disabled])").click();
  const after = await page.locator("[data-project-form]").boundingBox();
  assert.deepEqual(after, before, "Choose directory changed desktop dialog geometry");
  assert.equal(await page.locator("[data-project-browse]").evaluate((node) => document.activeElement === node), true,
    "desktop Choose directory did not return focus to Browse");
  console.log("mobile dialogs: desktop Open project geometry PASS");
}

async function runDesktopRestingState(browser, origin) {
  const context = await browser.newContext({ viewport: { width: 1024, height: 768 } });
  const page = await context.newPage();
  try {
    await load(page, origin);
    // The preview's resting state is both docked panels closed: the
    // terminal is the app, and a panel is something the user asks
    // for through a ?panel= param or a trigger. A bare desktop load that
    // opened one would render the Tasks panel across half the window
    // while the baseline readiness wait ([data-projects-menu]) still
    // passed, so the rendered panels carry the rule.
    for (const name of ["files", "tasks"]) {
      assert.equal(
        await page.locator(`[data-region="${name}"]`).getAttribute("data-open"),
        "false",
        `the ${name} panel starts closed on a bare desktop preview load`,
      );
    }
    console.log("mobile dialogs: desktop resting state PASS");
  } finally {
    await context.close();
  }
}

async function runDesktopSessionActions(browser, origin) {
  const context = await browser.newContext({ viewport: { width: 1024, height: 768 } });
  const page = await context.newPage();
  try {
    await load(page, origin);
    await expandSidebar(page);
    const session = page.locator("[data-session]:visible").first();
    // The trigger hides by opacity on its wrapper span, not display (comp:
    // the reveal is an opacity transition), so visibility alone cannot see
    // the resting state - and the opacity lives on the wrapper, not the
    // button, which always computes 1 on its own.
    assert.equal(await session.locator("[data-session-menu]").evaluate((node) => {
      const style = getComputedStyle(node.closest(".session-menu-trigger"));
      return `${style.opacity}/${style.pointerEvents}`;
    }), "0/none", "desktop session actions menu is visible before hover");
    assert.equal(await session.locator("[data-session-meta]").isVisible(), true,
      "desktop session evidence is not visible before hover");
    await session.hover();
    await page.waitForFunction(() => {
      const trigger = document.querySelector("[data-session]:hover .session-menu-trigger");
      return trigger !== null && getComputedStyle(trigger).opacity === "1";
    });
    assert.equal(await session.locator("[data-rename-session]:visible, [data-remove-session]:visible").count(), 0,
      "desktop exposes inline rename/remove controls on hover");
    await page.locator("[data-new-session]:visible").first()
      .locator("xpath=ancestor::li[1]").locator("[data-project-toggle]").hover();
    await page.locator("[data-new-session]:visible").first().click();
    const providerFilter = page.locator("[data-new-session-filter]");
    // The comp's filter is the md bp-input: 36px control, 15px control
    // type, 15px lead icon.
    assert.equal(await providerFilter.evaluate((node) => getComputedStyle(node).fontSize), "15px",
      "desktop picker filter keeps the comp's md control type");
    assert.equal(Math.round((await providerFilter.boundingBox()).height), 36,
      "desktop picker filter keeps the comp's 36px md control height");
    const filterIcon = providerFilter
      .locator("xpath=ancestor::label[1]").locator(".inputwrap-lead svg");
    assert.equal(await filterIcon.evaluate((node) => node.getAttribute("width")), "15",
      "desktop picker filter keeps the comp's 15px lead icon");
    await providerFilter.press("ArrowDown");
    assert.equal(await page.evaluate(() => document.activeElement?.getAttribute("data-new-session-provider")),
      "claude", "desktop ArrowDown selects the first harness in served order");
    await expectEscapeRestore(page, "New session", "[data-new-session]");
    console.log("mobile dialogs: desktop session actions PASS");
  } finally {
    await context.close();
  }
}

async function runActiveSessionSurface(browser, origin, width) {
  const context = await browser.newContext({
    viewport: { width, height: 640 },
    hasTouch: true,
    isMobile: true,
  });
  const page = await context.newPage();
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  try {
    await load(page, origin);
    await expandSidebar(page);
    // Five verified rows plus the limbo row: a session the server cannot
    // yet verify stays REACHABLE with the content-free warning dot — a
    // redeploy once made a live session vanish from the list entirely.
    // Ended and inactive rows are the hidden ones.
    assert.equal(await page.locator("[data-session]").count(), 6,
      `${width}px active sidebar row count`);
    for (const session of ["sess-1", "sess-2", "sess-3", "sess-9", "sess-10", "sess-6"]) {
      assert.equal(await page.locator(`[data-session="${session}"]`).count(), 1,
        `${width}px listed row ${session} is missing`);
    }
    assert.equal(await page.locator('[data-session="sess-6"] [data-meta-attention], [data-session="sess-6"] [aria-label="needs attention"]').count() > 0, true,
      `${width}px the limbo row carries no attention dot`);
    for (const session of ["sess-4", "sess-5", "sess-7", "sess-8"]) {
      assert.equal(await page.locator(`[data-session="${session}"]`).count(), 0,
        `${width}px ended/inactive row ${session} leaked into the active sidebar`);
    }
    assert.equal(await page.locator(
      "[data-session-presentation-state], [data-session-recover], [data-live-session-metadata]",
    ).count(), 0, `${width}px reconciliation chrome is still mounted`);
    for (const copy of ["Running", "Recovering", "Deck attachment unavailable", "Ended", "Inactive"]) {
      assert.equal(await page.getByText(copy, { exact: true }).count(), 0,
        `${width}px reconciliation copy ${copy} is visible`);
    }

    assert.deepEqual(errors, [], `${width}px active-session surface page errors`);
    console.log(`mobile dialogs: ${width}px active-session surface PASS`);
  } finally {
    await context.close();
  }
}

async function runSettingsMenu(browser, origin, width) {
  const context = await browser.newContext({ viewport: { width, height: 640 }, hasTouch: true, isMobile: true });
  const page = await context.newPage();
  try {
    await load(page, origin);
    await page.getByRole("button", { name: "Settings" }).click();
    // The rows the comp keeps stay keyboard-operable on the sheet; the
    // info-bar and side-panel rows leave — the phone renders neither
    // surface, so the toggles would control nothing it shows.
    for (const control of [
      page.locator("[data-confirm-before-closing]"),
    ]) {
      const box = await control.boundingBox();
      assert.ok(box !== null && box.x >= 0 && box.x + box.width <= width,
        `${width}px settings switch escaped the viewport`);
      await control.focus();
      await page.keyboard.press("Enter");
      assert.ok((await toggleChecked(control)) === "false",
        `${width}px settings toggle row was not keyboard-operable`);
    }
    await assertSettingsSheetContract(page, `${width}px live settings sheet`);
    // The header's X closes the sheet and hands focus back to the trigger.
    await page.locator("[data-settings-sheet-close]").click();
    await page.locator("[data-more-popover]").waitFor({ state: "detached" });
    assert.equal(
      await page.locator("[data-titlebar-more]").evaluate((el) => document.activeElement === el),
      true, `${width}px the settings X did not return focus to More`);
  } finally {
    await context.close();
  }
}

// The phone settings sheet contract (comp mobile-r8 screen 07): a titled
// header in the title-3 voice with a close control, the info-bar and
// side-panel rows gone, and each trimmed label showing exactly one of its
// wide/phone pair.
async function assertSettingsSheetContract(page, label) {
  const title = page.locator("[data-settings-sheet-chrome] > span");
  assert.equal(await title.isVisible(), true, `${label}: the sheet header is missing`);
  assert.equal((await title.innerText()).trim(), "Session settings", `${label}: header copy`);
  const titleType = await title.evaluate((el) => {
    const s = getComputedStyle(el);
    return { size: s.fontSize, weight: s.fontWeight };
  });
  // title-3 is the FIXED 17px token: the scope-following var reads the
  // laptop scope's 16px when the phone frame sits inside a wide page.
  assert.equal(titleType.size, "17px", `${label}: header is title-3`);
  assert.equal(titleType.weight, "500", `${label}: header is medium`);
  assert.equal(await page.locator("[data-settings-sheet-close]").isVisible(), true,
    `${label}: the header close control is missing`);
  for (const row of [
    "[data-show-info-bar]", "[data-show-side-panel]",
    "[data-auto-color]", "[data-confirm-before-removing]", "[data-git-identities]",
  ]) {
    assert.equal(await page.locator(row).isVisible(), false,
      `${label}: ${row} still renders on the sheet`);
  }
  for (const row of ["[data-show-tutorial]", "[data-github-connect]"]) {
    assert.equal(await page.locator(row).isVisible(), true,
      `${label}: the sheet lost the accepted ${row} action`);
  }
  assert.equal(await page.locator("[data-harness-login]").count(), 0,
    `${label}: the sheet offers a login nobody started`);
  const readoutType = await page.locator("[data-font-size]").evaluate(el => ({
    actual: getComputedStyle(el).fontFamily,
    label: getComputedStyle(document.querySelector("[data-font-label]")).fontFamily,
  }));
  assert.equal(readoutType.actual, readoutType.label,
    `${label}: the numeric setting must use the interface face`);
  // The visual stepper is 40px; its clickable box keeps the 44px touch floor.
  for (const stepper of ["[data-font-dec]", "[data-font-inc]"]) {
    const box = await page.locator(stepper).boundingBox();
    // The comp hints the stepper at 40px; the sheet's 44px touch floor
    // wins on a phone, as it does for every sheet control.
    assert.ok(box !== null && Math.round(box.width) === 44 && Math.round(box.height) === 44,
      `${label}: ${stepper} is ${box?.width}x${box?.height}, not the 44px touch stepper`);
    assert.equal(await page.locator(`${stepper} [data-copy-phone] svg`).isVisible(), true,
      `${label}: ${stepper} lost its chevron glyph`);
  }
  assert.equal(await page.locator("[data-theme-swatches]").isVisible(), true,
    `${label}: the theme row lost its swatch preview`);
  assert.equal(await page.locator("[data-theme-disclosure]").isVisible(), true,
    `${label}: the theme row lost its disclosure chevron`);
  assert.equal(await page.locator("[data-menu-shortcut]:visible").count(), 0,
    `${label}: a shortcut chip renders on the sheet`);
  for (const row of ["[data-skip-permissions]", "[data-confirm-before-closing]"]) {
    assert.equal(await page.locator(`${row} [data-row-switch]`).isVisible(), true,
      `${label}: ${row} lost its switch`);
  }
  assert.equal(await settingsLabelColors(page), 1, `${label}: the sheet rows use more than one label color`);
  for (const [row, phone] of [
    ["[data-skip-permissions]", "Yolo mode"],
    ["[data-confirm-before-closing]", "Confirm before closing"],
  ]) {
    assert.equal(await page.locator(`${row} [data-copy-phone]`).isVisible(), true,
      `${label}: ${row} phone copy is hidden`);
    assert.equal((await page.locator(`${row} [data-copy-phone]`).innerText()).trim(), phone,
      `${label}: ${row} phone copy`);
    assert.equal(await page.locator(`${row} [data-copy-wide]`).isVisible(), false,
      `${label}: ${row} leaks its wide copy beside the phone copy`);
  }
}

// The desktop popover holds its ground: no sheet header, every row, the
// wide copy — and the toggles still operate.
async function assertDesktopSettingsMenu(page) {
  assert.equal(await page.locator("[data-settings-sheet-chrome]").isVisible(), false,
    "desktop settings grew the sheet header");
  for (const row of ["[data-show-info-bar]", "[data-show-side-panel]"]) {
    assert.equal(await page.locator(row).isVisible(), true,
      `desktop settings lost ${row}`);
  }
  for (const [row, wide] of [
    ["[data-skip-permissions]", "Enable yolo mode"],
    ["[data-confirm-before-closing]", "Confirm before closing a session"],
  ]) {
    assert.equal((await page.locator(`${row} [data-copy-wide]`).innerText()).trim(), wide,
      `desktop ${row} copy`);
    assert.equal(await page.locator(`${row} [data-copy-phone]`).isVisible(), false,
      `desktop ${row} leaks the phone copy`);
  }
  for (const row of ["[data-show-info-bar]", "[data-skip-permissions]", "[data-confirm-before-removing]"]) {
    assert.equal(await page.locator(`${row} [data-row-switch]`).isVisible(), true,
      `desktop ${row} lost its switch`);
  }
  assert.equal(await settingsLabelColors(page), 1, "desktop settings rows use more than one label color");
  const control = page.locator("[data-show-info-bar]");
  await control.focus();
  await page.keyboard.press("Enter");
  assert.ok((await toggleChecked(control)) === "false",
    "desktop info-bar toggle was not keyboard-operable");
}

// The number of distinct label colors across the visible settings rows.
async function settingsLabelColors(page) {
  await page.mouse.move(0, 0);
  return await page.locator("[data-more-popover]").evaluate((menu) => new Set(
    [...menu.querySelectorAll(":scope > .menuitem, :scope > .relative > .menuitem, [data-font-label]")]
      .filter((row) => row.offsetParent !== null)
      .map((row) => getComputedStyle(row.querySelector(".flex-1") ?? row).color)).size);
}

async function toggleChecked(row) {
  assert.equal(await row.getAttribute("role"), "switch");
  return await row.getAttribute("aria-checked");
}

async function grabberMetrics(node) {
  return await node.evaluate((el) => {
    const s = window.getComputedStyle(el, "::before");
    return { width: s.width, height: s.height };
  });
}

async function appBox(page) {
  const box = await page.locator("#app").boundingBox();
  const vp = page.viewportSize();
  return box ?? { x: 0, y: 0, width: vp.width, height: vp.height };
}

async function assertNarrowThemeList(page, label) {
  const themeList = page.locator("[data-theme-list]");
  await themeList.waitFor();
  const frame = await appBox(page);
  const touchMin = await page.evaluate(() =>
    Number.parseFloat(getComputedStyle(document.documentElement).getPropertyValue("--touch-min") || "44"));
  // The pushed screen owns the whole app area: an offset here clips its own
  // chrome — an inline top on the desktop flyout once beat the phone's
  // inset and started the screen 6px above the viewport.
  const listBox = await themeList.boundingBox();
  assert.ok(listBox !== null, `${label}: theme list is unreachable`);
  assert.ok(Math.abs(listBox.y - frame.y) <= 0.5 && Math.abs(listBox.height - frame.height) <= 1,
    `${label}: the pushed screen does not own the app area: list ${JSON.stringify(listBox)} vs app ${JSON.stringify(frame)}`);
  // The chrome's back and close cells are touch targets and keep the 44px
  // floor; the rows keep it too — the comp's measured pitch is 45px (44px
  // row plus the 1px gap). One loop over every button conflated the two
  // kinds, so either rule asserted on the wrong element.
  for (const cell of await themeList.locator(".theme-back, .theme-close").all()) {
    const box = await cell.boundingBox();
    assert.ok(box !== null && box.height >= touchMin - 0.5,
      `${label}: a chrome cell is ${box?.height}px tall, below --touch-min (${touchMin}px)`);
  }
  const choices = await themeList.locator(".theme-rows button").all();
  assert.ok(choices.length > 0, `${label}: theme list has no choices`);
  for (let i = 0; i < choices.length; i += 1) {
    const box = await choices[i].boundingBox();
    assert.ok(box !== null, `${label}: theme choice ${i} is unreachable`);
    assert.ok(
      box.x >= frame.x - 1 && box.x + box.width <= frame.x + frame.width + 1,
      `${label}: theme choice ${i} overflows app frame ${JSON.stringify({ box, frame })}`,
    );
    assert.ok(
      box.height >= touchMin - 0.5,
      `${label}: theme choice ${i} is ${box.height}px tall, below --touch-min (${touchMin}px)`,
    );
  }
  // The five swatches are what a theme is chosen by (handoff 15.10) and the
  // reason this surface is a pushed screen rather than a Select. Hiding them
  // scores WELL on a pixel gate — fewer pixels drawn is fewer pixels
  // differing — so their presence has to be asserted, not inferred.
  const strips = await themeList.locator(".theme-swatch-strip").all();
  assert.ok(strips.length > 0, `${label}: the theme rows carry no swatch strips`);
  for (let i = 0; i < strips.length; i += 1) {
    const chip = strips[i].locator("span > span").first();
    const box = await chip.boundingBox();
    assert.ok(box !== null, `${label}: swatch strip ${i} is not rendered`);
    assert.ok(
      box.height >= 12 && box.height <= 14,
      `${label}: swatch chip ${i} is ${box.height}px, want the comp's 13px`,
    );
  }
}

// The pushed screen replaces the control that opened it, so focus has to be
// placed by hand; without it the keyboard is left on <body> with no way back.
async function assertPushedThemeFocus(page, label) {
  await page.locator("[data-theme-flyout]").click();
  await page.locator("[data-theme-list]").waitFor();
  const onEntry = await page.evaluate(() => document.activeElement?.getAttribute("data-theme-back") !== null);
  assert.ok(onEntry, `${label}: entering the pushed theme screen left focus off its back control`);

  await page.locator("[data-theme-back]").click();
  await page.locator("[data-theme-flyout]").waitFor();
  const onBack = await page.evaluate(() => document.activeElement?.getAttribute("data-theme-flyout") !== null);
  assert.ok(onBack, `${label}: leaving the pushed theme screen did not restore focus to the row that pushed it`);

  await page.locator("[data-theme-flyout]").click();
  await page.locator("[data-theme-list]").waitFor();
  await page.keyboard.press("Escape");
  await page.locator("[data-theme-flyout]").waitFor();
  const afterEscape = await page.locator("[data-theme-list]").count();
  assert.equal(afterEscape, 0, `${label}: Escape left the pushed theme screen open`);
  const escapeFocus = await page.evaluate(() => document.activeElement?.getAttribute("data-theme-flyout") !== null);
  assert.ok(escapeFocus, `${label}: Escape did not return focus to the Terminal theme row`);

  // The second Escape is the menu's own level, and it must hand focus back
  // to the trigger — the contract every menu here follows. Asserting only
  // that the pushed screen closed would leave a keyboard stranded on <body>
  // with nothing to return to.
  await page.keyboard.press("Escape");
  await page.locator("[data-more-popover]").waitFor({ state: "detached" });
  const onTrigger = await page.evaluate(() => {
    const a = document.activeElement;
    return a !== null && (a.hasAttribute("data-titlebar-more") || a.hasAttribute("data-live-more"));
  });
  assert.ok(onTrigger, `${label}: closing the menu with Escape did not restore focus to its trigger`);
}

async function assertSheetGeometry(page, selector, label) {
  const node = page.locator(selector).first();
  await node.waitFor();
  const rect = await node.boundingBox();
  const frame = await appBox(page);
  const expectedBottom = frame.y + frame.height;
  assert.ok(rect !== null, `${label}: sheet is unreachable`);
  assert.ok(Math.abs(rect.width - frame.width) <= 1,
    `${label}: sheet width ${rect.width} != app ${frame.width}`);
  assert.ok(Math.abs(rect.x - frame.x) <= 1,
    `${label}: sheet left ${rect.x} != app ${frame.x}`);
  assert.ok(Math.abs((rect.y + rect.height) - expectedBottom) <= 1,
    `${label}: sheet bottom ${rect.y + rect.height} != app bottom ${expectedBottom}`);
  const grabber = await grabberMetrics(node);
  assert.ok(parseFloat(grabber.width) >= 30 && parseFloat(grabber.height) >= 3,
    `${label}: grabber missing (${grabber.width} × ${grabber.height})`);
}

async function assertSheetScrim(page, selector, label) {
  const node = page.locator(selector).first();
  await node.waitFor();
  const color = await node.evaluate((el) => {
    const backdrop = el.closest(".scrim") ?? el.closest("[data-popover-backdrop]");
    return backdrop ? getComputedStyle(backdrop).backgroundColor : null;
  });
  assert.ok(color !== null, `${label}: sheet has no scrim or popover backdrop`);
  assert.equal(color, "rgba(5, 10, 18, 0.6)",
    `${label}: sheet scrim background is ${color}`);
}

async function assertPrimaryAction(page, selector, label) {
  const btn = page.locator(selector).first();
  await btn.waitFor();
  const box = await btn.boundingBox();
  const footWidth = await btn.locator('xpath=ancestor::*[contains(@class, "dialog-foot")][1]').evaluate(
    (el) => {
      const style = window.getComputedStyle(el);
      return el.clientWidth - Number.parseFloat(style.paddingLeft) - Number.parseFloat(style.paddingRight);
    },
  );
  assert.ok(box !== null, `${label}: primary action is unreachable`);
  assert.ok(Math.abs(box.width - footWidth) <= 1,
    `${label}: primary action width ${box.width} != foot content ${footWidth}`);
  assert.ok(box.height >= 43.5,
    `${label}: primary action height ${box.height} < 44px`);
}

async function assertDesktopPresentation(page, selector, label) {
  const node = page.locator(selector).first();
  await node.waitFor();
  const rect = await node.boundingBox();
  const vp = page.viewportSize();
  assert.ok(rect !== null, `${label}: desktop surface is unreachable`);
  assert.ok(rect.width <= vp.width - 10,
    `${label}: desktop surface is full width (${rect.width})`);
  assert.ok((rect.y + rect.height) <= vp.height - 10,
    `${label}: desktop surface is bottom-anchored (${rect.y + rect.height})`);
  const grabber = await grabberMetrics(node);
  const grabberWidth = Number.parseFloat(grabber.width);
  assert.ok(Number.isNaN(grabberWidth) || grabberWidth < 10,
    `${label}: desktop surface has a grabber (${grabber.width})`);
}

async function runSheetPresentation(browser, origin) {
  // The two sheet surfaces under handoff §15.1 rule 3: the new-session picker
  // and the settings sheet. Terminal theme and Custom theme are pushed screens,
  // and Open project is a full-screen form; they stay off this list.
  const sheetCases = [
    { query: "?dialog=new-session", selector: "[data-new-session-modal]", label: "new session" },
    { query: "?menu=settings", selector: "[data-more-popover]", label: "settings" },
  ];

  const narrow = await browser.newContext({
    viewport: { width: 390, height: 640 },
    hasTouch: true,
    isMobile: true,
  });
  const page = await narrow.newPage();
  try {
    for (const c of sheetCases) {
      await load(page, origin, c.query);
      await assertSheetGeometry(page, c.selector, `real narrow sheet ${c.label}`);
      await assertSheetScrim(page, c.selector, `real narrow sheet ${c.label}`);
    }
    await load(page, origin, "?menu=settings");
    await assertSettingsSheetContract(page, "real narrow settings sheet");
    await page.locator("[data-settings-sheet-close]").click();
    await page.locator("[data-more-popover]").waitFor({ state: "detached" });
    assert.equal(
      await page.locator("[data-titlebar-more]").evaluate((el) => document.activeElement === el),
      true, "real narrow settings X did not return focus to More");
    await load(page, origin, "?menu=theme");
    await assertNarrowThemeList(page, "real narrow terminal theme");
    await load(page, origin, "?menu=settings");
    await assertPushedThemeFocus(page, "real narrow terminal theme");
    console.log("mobile dialogs: real narrow sheet presentation PASS");
  } finally {
    await narrow.close();
  }

  const framed = await browser.newContext({ viewport: { width: 1024, height: 768 } });
  const fpage = await framed.newPage();
  try {
    await load(fpage, origin, "?frame=phone");
    await assertMobileSheetMotion(fpage, "framed phone");
    await assertMobileSheetMotion(fpage, "framed phone", true);
    await fpage.emulateMedia({ reducedMotion: "no-preference" });
    for (const c of sheetCases) {
      const query = `?frame=phone${c.query.replace("?", "&")}`;
      await load(fpage, origin, query);
      await assertSheetGeometry(fpage, c.selector, `framed sheet ${c.label}`);
      await assertSheetScrim(fpage, c.selector, `framed sheet ${c.label}`);
    }
    await load(fpage, origin, "?frame=phone&menu=settings");
    await assertSettingsSheetContract(fpage, "framed settings sheet");
    await load(fpage, origin, "?frame=phone&menu=theme");
    await assertNarrowThemeList(fpage, "framed terminal theme");
    await load(fpage, origin, "?frame=phone&menu=settings");
    await assertPushedThemeFocus(fpage, "framed terminal theme");
    console.log("mobile dialogs: framed sheet presentation PASS");
  } finally {
    await framed.close();
  }

  const desktop = await browser.newContext({ viewport: { width: 1024, height: 768 } });
  const dpage = await desktop.newPage();
  try {
    const cases = [
      { query: "?dialog=new-session", selector: "[data-new-session-modal]", label: "new session" },
      { query: "?menu=settings", selector: "[data-more-popover]", label: "settings" },
      { query: "?dialog=open-project", selector: "[data-project-form]", label: "open project" },
      { query: "?menu=theme", selector: "[data-theme-list]", label: "terminal theme" },
      { query: "?dialog=custom-theme", selector: "[data-theme-dialog]", label: "custom theme" },
    ];
    for (const c of cases) {
      await load(dpage, origin, c.query);
      await assertDesktopPresentation(dpage, c.selector, `desktop ${c.label}`);
    }
    await load(dpage, origin, "?menu=settings");
    await assertDesktopSettingsMenu(dpage);
    // The colour picker must be reachable inside the dialog. It used to be a
    // popover, and .dialog carries max-height with overflow:auto, so an
    // ancestor that scrolls clipped it whatever its z-index: the disc lost its
    // lower half and both knobs with it, which is exactly the part a user
    // drags. Assert the knob lands inside the dialog's box.
    await load(dpage, origin, "?dialog=custom-theme");
    await dpage.locator("[data-theme-hex]").focus();
    await dpage.locator("[data-wheel-knob]").waitFor();
    const knobInside = await dpage.evaluate(() => {
      const knob = document.querySelector("[data-wheel-knob]").getBoundingClientRect();
      const dialog = document.querySelector(".dialog").getBoundingClientRect();
      return { knobBottom: knob.bottom, dialogBottom: dialog.bottom, inside: knob.bottom <= dialog.bottom && knob.top >= dialog.top };
    });
    assert.ok(knobInside.inside,
      `the colour picker's knob is cut off: knob bottom ${knobInside.knobBottom}, dialog bottom ${knobInside.dialogBottom}`);
    console.log("mobile dialogs: the colour picker opens fully inside its dialog PASS");

    const footActions = await dpage.locator("[data-theme-dialog] .dialog-foot button").evaluateAll((buttons) =>
      buttons.map((b) => ({ label: b.textContent.trim(), height: b.getBoundingClientRect().height, size: getComputedStyle(b).fontSize })));
    assert.deepEqual(footActions.map((a) => a.label), ["Reset", "Cancel", "Apply theme"],
      "custom theme footer lost an action");
    for (const a of footActions) {
      assert.equal(a.height, 36, `custom theme ${a.label} is ${a.height}px, not the 36px md button`);
      assert.equal(a.size, footActions[0].size, `custom theme ${a.label} type is ${a.size}, not ${footActions[0].size}`);
    }
    console.log("mobile dialogs: custom theme actions share one size PASS");

    console.log("mobile dialogs: desktop presentation unchanged PASS");
  } finally {
    await desktop.close();
  }
}

const server = await serve(DIST);
const origin = `http://127.0.0.1:${server.address().port}`;
let browser;
try {
  const got = await getBrowser();
  browser = got.browser;
  console.log(`browser: ${got.via}`);
  for (const width of [320, 390, 430]) {
    if (process.env.NOISEDECK_SETTINGS_ONLY === "1") {
      await runSettingsMenu(browser, origin, width);
    } else {
      await runWidth(browser, origin, width);
      await runActiveSessionSurface(browser, origin, width);
    }
  }
  const landscape = await browser.newContext({
    viewport: { width: 844, height: 390 }, hasTouch: true, isMobile: true,
  });
  try {
    await load(await landscape.newPage(), origin, "?sidebar=open");
    console.log("mobile dialogs: landscape touch scroll boundaries PASS");
  } finally {
    await landscape.close();
  }
  if (process.env.NOISEDECK_SETTINGS_ONLY !== "1") {
    // A fullscreen route's header clears the device status bar via
    // env(safe-area-inset-top). Headless resolves env() to 0, so the
    // stylesheet is the observable: both scopes must carry the inset on
    // the fullscreen head, or a notched phone puts the close X under the
    // clock row.
    {
      const { readdir } = await import("node:fs/promises");
      const assets = join(DIST, "assets");
      const cssFiles = (await readdir(assets)).filter((name) => name.endsWith(".css"));
      const css = (await Promise.all(
        cssFiles.map((name) => readFile(join(assets, name), "utf8")),
      )).join("\n");
      const heads = css.match(/\[data-phone-fullscreen\][^{}]*\.dialog-head[^{}]*\{[^}]*\}/g) ?? [];
      const insets = heads.filter((rule) => rule.includes("safe-area-inset-top"));
      assert.ok(insets.length >= 2,
        `fullscreen dialog-head safe-area rules missing: found ${insets.length} of 2 scopes`);
    }
    await runAddPrdReturnTargets(browser, origin);
    await runDesktopRestingState(browser, origin);
    await runDesktopSessionActions(browser, origin);
    await runSheetPresentation(browser, origin);
  }
} finally {
  await browser?.close();
  server.close();
}
