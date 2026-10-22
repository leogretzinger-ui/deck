// The importer rounds crop geometry to CSS pixels. Enclose the entire live
// surface in an integer frame so a fractional bottom edge cannot be clamped.
export default async function setup(page) {
  await page.locator("[data-harness-login-dialog]").waitFor();
  await page.evaluate(() => document.fonts.ready);
  const family = await page.locator("[data-harness-login-dialog]").evaluate(el => getComputedStyle(el).fontFamily);
  const expected = process.env.NOISEDECK_CAPTURE_FONT;
  if (expected && !family.includes(expected)) throw new Error(`Expected ${expected}, got ${family}`);
  console.log(`Harness login capture font: ${family}`);
  await page.evaluate(() => {
    const dialog = document.querySelector("[data-harness-login-dialog]");
    const box = dialog.getBoundingClientRect();
    const frame = document.createElement("div");
    frame.dataset.harnessLoginCapture = "";
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
