import assert from "node:assert/strict";

// Sample paused browser transitions so a slow runner cannot skip the frame
// between the sheet's open and closed positions.
export async function assertMobileSheetMotion(page, label, reduced = false) {
  await page.emulateMedia({ reducedMotion: reduced ? "reduce" : "no-preference" });
  const result = await page.evaluate(() => {
    const sheet = document.querySelector('[data-region="sidebar"]');
    const scrim = document.querySelector('[data-region="sidebar-scrim"]');
    const workspace = document.querySelector("[data-workspace]");
    const terminal = document.querySelector('[data-region="terminal"]');
    const sessions = document.querySelector("[data-live-sessions], [data-titlebar-sessions]");
    const open = sessions?.getClientRects().length ? sessions
      : document.querySelector('[data-region="sidebar-expand"]');
    const close = () => sheet.querySelector("[data-sidebar-sheet-close]");
    const animations = () => [...sheet.getAnimations(), ...scrim.getAnimations()];
    const finish = () => animations().forEach((animation) => animation.finish());
    const sample = () => ({
      y: new DOMMatrixReadOnly(getComputedStyle(sheet).transform).m42,
      height: sheet.getBoundingClientRect().height,
      opacity: Number(getComputedStyle(scrim).opacity),
      hidden: getComputedStyle(sheet).visibility === "hidden",
      inert: sheet.inert,
      ariaHidden: sheet.getAttribute("aria-hidden"),
      terminal: terminal.getBoundingClientRect().toJSON(),
    });
    const halfway = () => {
      const moving = animations();
      const midpoint = Math.max(0, ...moving.map((animation) =>
        Number(animation.effect.getTiming().duration))) / 2;
      for (const animation of moving) {
        animation.pause();
        animation.currentTime = midpoint;
      }
      return moving.filter((animation) =>
        animation.transitionProperty === "transform" || animation.transitionProperty === "opacity").length;
    };

    if (workspace.dataset.collapsed !== "true") close().click();
    finish();
    const before = sample();
    open.click();
    const openingAnimations = halfway();
    const opening = sample();
    finish();
    const opened = sample();

    const scroll = sheet.querySelector(".overflow-y-auto");
    scroll.scrollTop = 80;
    const scrollTop = scroll.scrollTop;
    close().focus();
    close().click();
    const closingAnimations = halfway();
    const closing = sample();
    // Even while visible during dismissal, no sheet control may take focus.
    sheet.querySelector("input").focus();
    const closingFocusBlocked = !sheet.contains(document.activeElement);
    const focusReturned = document.activeElement === open;

    // Reopening partway through dismissal must reverse at the current frame.
    open.click();
    const reversed = sample();
    finish();
    const reopened = sample();
    const scrollPreserved = scroll.scrollTop === scrollTop;
    scrim.click();
    finish();
    const closed = sample();
    const scrimHidden = getComputedStyle(scrim).visibility === "hidden";
    const scrimInactive = getComputedStyle(scrim).pointerEvents === "none";
    return {
      before, opening, opened, closing, reversed, reopened, closed,
      openingAnimations, closingAnimations, closingFocusBlocked,
      focusReturned, scrollPreserved, scrimHidden, scrimInactive,
    };
  });

  assert.equal(result.before.hidden, true, `${label}: initial sheet is not hidden`);
  assert.equal(result.opening.inert, false, `${label}: opening sheet remains inert`);
  assert.equal(result.opened.y, 0, `${label}: sheet does not settle at its resting position`);
  assert.equal(result.opened.opacity, 1, `${label}: scrim does not become opaque`);
  assert.equal(result.opened.ariaHidden, "false", `${label}: open sheet is hidden from accessibility`);
  assert.equal(result.closing.inert, true, `${label}: closing sheet still accepts input`);
  assert.equal(result.closing.ariaHidden, "true", `${label}: closing sheet remains in accessibility`);
  assert.equal(result.closingFocusBlocked, true, `${label}: a closing control accepted focus`);
  assert.equal(result.focusReturned, true, `${label}: close did not return focus to Sessions`);
  assert.equal(result.scrollPreserved, true, `${label}: toggling reset the list scroll position`);
  assert.equal(result.reopened.y, 0, `${label}: reversed opening did not settle`);
  assert.equal(result.closed.hidden, true, `${label}: backdrop dismissal did not hide the sheet`);
  assert.equal(result.scrimHidden && result.scrimInactive, true, `${label}: hidden scrim intercepts input`);
  for (const state of [result.opening, result.opened, result.closing, result.reopened, result.closed]) {
    assert.deepEqual(state.terminal, result.before.terminal, `${label}: sheet resized or moved the terminal`);
  }
  if (reduced) {
    assert.equal(result.openingAnimations, 0, `${label}: reduced motion animates opening`);
    assert.equal(result.closingAnimations, 0, `${label}: reduced motion animates closing`);
    assert.equal(result.opening.y, 0, `${label}: reduced-motion opening is not immediate`);
    assert.equal(result.closing.hidden, true, `${label}: reduced-motion dismissal is not immediate`);
  } else {
    assert.equal(result.openingAnimations, 2, `${label}: sheet and scrim must both animate`);
    assert.equal(result.closingAnimations, 2, `${label}: dismissal skips its transition`);
    assert.equal(result.closing.hidden, false, `${label}: sheet disappears before dismissal finishes`);
    for (const state of [result.opening, result.closing]) {
      assert.ok(state.y > 0 && state.y < state.height, `${label}: no intermediate sheet position`);
      assert.ok(state.opacity > 0 && state.opacity < 1, `${label}: no intermediate backdrop opacity`);
    }
    assert.ok(Math.abs(result.reversed.y - result.closing.y) < 1,
      `${label}: reversing dismissal jumps to an endpoint`);
  }
  console.log(`${label}: Sessions sheet ${reduced ? "reduced motion" : "motion"} PASS`);
}

export async function assertSheetResize(page, label) {
  const viewport = page.viewportSize();
  await page.emulateMedia({ reducedMotion: "no-preference" });
  try {
    for (const closing of [false, true]) {
      await page.setViewportSize({ width: 390, height: viewport.height });
      await page.evaluate((closing) => {
        const sheet = document.querySelector('[data-region="sidebar"]');
        const workspace = document.querySelector("[data-workspace]");
        if (workspace.dataset.collapsed === "true") {
          document.querySelector("[data-live-sessions], [data-titlebar-sessions]").click();
        }
        if (closing) {
          sheet.getAnimations().forEach((animation) => animation.finish());
          sheet.querySelector("[data-sidebar-sheet-close]").click();
        }
        for (const animation of sheet.getAnimations()) {
          animation.pause();
          animation.currentTime = 90;
        }
      }, closing);
      await page.setViewportSize({ width: 1280, height: viewport.height });
      const state = await page.locator('[data-region="sidebar"]').evaluate((node) => ({
        duration: getComputedStyle(node).transitionDuration,
        transform: getComputedStyle(node).transform,
        visibility: getComputedStyle(node).visibility,
        animations: node.getAnimations().length,
        inert: node.inert,
      }));
      assert.deepEqual(state, {
        duration: "0s", transform: "none", visibility: closing ? "hidden" : "visible",
        animations: 0, inert: closing,
      }, `${label}: ${closing ? "dismissal" : "opening"} motion survived a desktop resize`);
    }
  } finally {
    await page.setViewportSize(viewport);
  }
  console.log(`${label}: resize cancels mobile sheet motion PASS`);
}
