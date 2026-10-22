// Capture setup for the Files image viewer states. The reference design
// draws the workspace without the project sidebar, so the sidebar is
// collapsed the way a user collapses it. The docked panel is then 56%
// of the window and starts on a fractional pixel at the window's right
// edge, where a screenshot clip rounded outward runs past the page and is
// clamped short; an invisible integer frame around the panel is what the
// state clips to instead.
export default async function setup(page) {
  await page.waitForFunction(() =>
    document.documentElement.dataset.previewState !== undefined ||
    document.documentElement.dataset.previewError !== undefined);
  const error = await page.evaluate(() => document.documentElement.dataset.previewError);
  if (error !== undefined) throw new Error(`preview did not reach its state: ${error}`);
  if (await page.locator("[data-collapse]").count() > 0) {
    await page.locator("[data-collapse]").click();
  }
  await page.mouse.move(0, 0);
  await page.evaluate(() => document.fonts.ready);
  await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  await page.evaluate(() => {
    const panel = document.querySelector('[data-region="files"]');
    if (panel === null) throw new Error("the files panel is not on the page");
    const box = panel.getBoundingClientRect();
    const frame = document.createElement("div");
    frame.dataset.filesViewerCapture = "";
    frame.inert = true;
    frame.setAttribute("aria-hidden", "true");
    Object.assign(frame.style, {
      position: "absolute", pointerEvents: "none", opacity: "0",
      left: `${Math.floor(box.left)}px`, top: `${Math.floor(box.top)}px`,
      width: `${Math.floor(box.right) - Math.floor(box.left)}px`,
      height: `${Math.floor(box.bottom) - Math.floor(box.top)}px`,
    });
    document.body.append(frame);
    const after = panel.getBoundingClientRect();
    if (["x", "y", "width", "height"].some((key) => after[key] !== box[key])) {
      throw new Error("capture framing changed the panel's layout");
    }
  });
}
