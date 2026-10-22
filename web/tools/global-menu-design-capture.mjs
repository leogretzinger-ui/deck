import assert from "node:assert/strict";
import { assertFontVoice } from "./font-assertions.mjs";

// The importer rounds crop geometry to CSS pixels. Enclose the entire live
// surface in an integer frame so a fractional bottom edge cannot be clamped.
export default async function setup(page) {
  await page.locator("[data-more-popover]").waitFor();
  await page.evaluate(() => document.fonts.ready);
  const family = await page.locator("[data-more-popover]").evaluate(el => getComputedStyle(el).fontFamily);
  const expected = process.env.NOISEDECK_CAPTURE_FONT;
  assert.ok(expected === "Euclid Flex" || expected === "Jost", "Set NOISEDECK_CAPTURE_FONT to the intended licensed or OFL UI face");
  await assertFontVoice(page, family, "ui", "The menu uses the selected UI font");
  assert.equal(family.split(",")[0].trim().replace(/^['"]|['"]$/g, ""), expected);
  const loaded = await page.evaluate(expected => [...document.fonts].some(face =>
    face.family.replace(/^['"]|['"]$/g, "") === expected && face.status === "loaded"), expected);
  assert.ok(loaded, `The menu's ${expected} face must actually load`);
  console.log(`Global menu capture font: ${family}`);
  await page.evaluate(() => {
    const dialog = document.querySelector("[data-more-popover]");
    const box = dialog.getBoundingClientRect();
    const frame = document.createElement("div");
    frame.dataset.globalMenuCapture = "";
    frame.inert = true;
    frame.setAttribute("aria-hidden", "true");
    Object.assign(frame.style, { position: "absolute", pointerEvents: "none", opacity: "0",
      left: `${Math.floor(box.left)}px`, top: `${Math.floor(box.top)}px`,
      width: `${Math.ceil(box.right) - Math.floor(box.left)}px`, height: `${Math.ceil(box.bottom) - Math.floor(box.top)}px` });
    document.body.append(frame);
    const after = dialog.getBoundingClientRect();
    if (["x", "y", "width", "height"].some(key => after[key] !== box[key])) throw new Error("Capture framing changed dialog layout");
  });
}
