// Synthetic workspace checks; run after pnpm build using the pinned browser service.
import assert from "node:assert/strict";
import { createServer as createViteServer } from "vite";
import { createServer } from "node:http";
import { readFile, mkdir } from "node:fs/promises";
import { existsSync } from "node:fs";
import { extname, join, resolve } from "node:path";
import { browser as getBrowser } from "./browser.mjs";
import { eventually } from "./eventually.mjs";

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

await new Promise((resolveServer) => server.listen(0, "127.0.0.1", resolveServer));
const origin = `http://127.0.0.1:${server.address().port}`;
let browser;
let vite;
try {
  ({ browser } = await getBrowser());
  const context = await browser.newContext({ viewport: { width: 1400, height: 900 } });
  const page = await context.newPage();
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  await page.goto(`${origin}/preview.html?sidebar-poll=250`);
  await page.waitForSelector("html[data-preview-state]");
  const sidebar = page.locator('[data-region="sidebar"]');
  const handle = page.getByRole("separator", { name: "Sidebar width" });
  const width = async () => Math.round((await sidebar.boundingBox()).width);
  const expectWidth = async expected => eventually(width, value => value === expected, `sidebar width ${expected}`);
  const drag = async delta => {
    const box = await sidebar.boundingBox();
    await page.mouse.move(box.x + box.width, box.y + 180);
    await page.mouse.down();
    await page.mouse.move(box.x + box.width + delta, box.y + 180, { steps: 8 });
    await page.mouse.up();
  };
  await expectWidth(272);
  assert.equal(await handle.getAttribute("aria-orientation"), "vertical");
  await page.waitForTimeout(650); // settle the streaming fixture before retaining nodes
  await page.evaluate(() => {
    window.__widthRow = document.querySelector('[data-session="sess-2"]');
    const input = document.createElement("input");
    input.id = "probe-focus";
    document.querySelector('[data-region="terminal"]').append(input);
    input.focus();
  });
  await drag(80);
  await expectWidth(352);
  assert.equal(await page.locator("#probe-focus").evaluate(el => el === document.activeElement), true);
  await page.waitForTimeout(600);
  assert.equal(await page.evaluate(() => window.__widthRow === document.querySelector('[data-session="sess-2"]')), true);
  await handle.focus();
  await page.keyboard.press("ArrowRight");
  await expectWidth(360);
  await page.keyboard.press("Shift+ArrowLeft");
  await expectWidth(328);
  await page.reload();
  await page.waitForSelector("html[data-preview-state]");
  await expectWidth(328);
  await handle.focus();
  await page.keyboard.press("End");
  await expectWidth(480);
  await page.setViewportSize({ width: 800, height: 900 });
  await expectWidth(320);
  assert.equal(await handle.getAttribute("aria-valuemax"), "320");
  await page.setViewportSize({ width: 1400, height: 900 });
  await expectWidth(480); // viewport clamps never overwrite the chosen width
  await handle.focus();
  await page.keyboard.press("Home");
  await expectWidth(220);
  await page.keyboard.press("Enter");
  await expectWidth(272);
  await drag(64);
  let box = await sidebar.boundingBox();
  await page.mouse.dblclick(box.x + box.width, box.y + 180);
  await expectWidth(272);
  await drag(96);
  await handle.focus();
  // Programmatic activation keeps separator focus until the real collapse handler runs.
  await page.locator("[data-collapse]").evaluate(el => el.click());
  assert.equal(await page.getByRole("button", { name: "Show sidebar" }).evaluate(el => el === document.activeElement), true);
  assert.equal(await handle.count(), 0);
  assert.equal(await page.locator('[data-sidebar-resize]').getAttribute("tabindex"), "-1");
  await page.getByRole("button", { name: "Show sidebar" }).click();
  await expectWidth(368);
  // Cancel capture on blur: later movement must not create a phantom resize.
  box = await sidebar.boundingBox();
  await page.mouse.move(box.x + box.width, box.y + 180);
  await page.mouse.down();
  await page.evaluate(() => window.dispatchEvent(new Event("blur")));
  await page.mouse.move(box.x + box.width + 50, box.y + 180);
  await page.mouse.up();
  await expectWidth(368);
  assert.equal(await page.locator('[data-sidebar-resizing]').count(), 0);
  const assets = resolve(import.meta.dirname, "../../.deck/assets");
  await mkdir(assets, { recursive: true });
  await page.screenshot({ path: join(assets, "sidebar-width-desktop.png") });
  await handle.focus();
  await page.setViewportSize({ width: 390, height: 844 });
  await eventually(() => page.locator('[data-sidebar-resize]').isVisible(), v => !v, "mobile hides separator");
  assert.equal(await page.locator('[data-sidebar-resize]').getAttribute("tabindex"), "-1");
  assert.equal(await page.locator('[data-workspace]').evaluate(el => el.style.getPropertyValue("--sidebar-w")), "272px");
  assert.equal(await page.evaluate(() => {
    const el = document.activeElement;
    return el instanceof HTMLElement && el !== document.body && el.getClientRects().length > 0
      && getComputedStyle(el).visibility !== "hidden" && !el.closest("[inert]");
  }), true, "mobile transition restores focus to a visible control");
  await page.screenshot({ path: join(assets, "sidebar-width-mobile.png") });
  await page.locator("[data-sidebar-sheet-close]").click();
  await eventually(() => sidebar.getAttribute("aria-hidden"), value => value === "true", "mobile drawer closes");
  await page.locator("[data-titlebar-sessions]").click();
  await eventually(() => sidebar.getAttribute("aria-hidden"), value => value === "false", "mobile drawer reopens");
  assert.equal(await page.locator('[data-sidebar-resize]').isVisible(), false);
  await page.setViewportSize({ width: 1400, height: 900 });
  await expectWidth(368);
  // Corrupt and inaccessible storage both leave a functional default control.
  await page.evaluate(() => localStorage.setItem("deck.sidebar-width", "invalid"));
  await page.reload();
  await page.waitForSelector("html[data-preview-state]");
  await expectWidth(272);
  const blocked = await browser.newContext({ viewport: { width: 1400, height: 900 } });
  await blocked.addInitScript(() => {
    for (const method of ["getItem", "setItem"]) {
      const original = Storage.prototype[method];
      Storage.prototype[method] = function (key, ...args) {
        if (key === "deck.sidebar-width") throw new Error("blocked");
        return original.call(this, key, ...args);
      };
    }
  });
  const blockedPage = await blocked.newPage();
  await blockedPage.goto(`${origin}/preview.html`);
  await blockedPage.waitForSelector("html[data-preview-state]");
  const blockedHandle = blockedPage.getByRole("separator", { name: "Sidebar width" });
  await blockedHandle.focus();
  await blockedPage.keyboard.press("ArrowRight");
  assert.equal(await blockedHandle.getAttribute("aria-valuenow"), "280");
  await blocked.close();
  // Mount the real terminal adapter in a synthetic preview. Source modules are
  // served by a private ephemeral Vite server; no backend or terminal session.
  vite = await createViteServer({
    root: resolve(import.meta.dirname, ".."),
    configFile: resolve(import.meta.dirname, "../vite.config.ts"),
    server: { host: "127.0.0.1", port: 0, strictPort: false },
  });
  await vite.listen();
  const terminalPage = await context.newPage();
  terminalPage.on("pageerror", error => errors.push(error.message));
  await terminalPage.goto(`http://127.0.0.1:${vite.httpServer.address().port}/preview.html`);
  await terminalPage.waitForSelector("html[data-preview-state]");
  await terminalPage.addStyleTag({ url: "/node_modules/@xterm/xterm/css/xterm.css" });
  await terminalPage.evaluate(async () => {
    const { AgentTerminal } = await import("/src/terminal/attach.ts");
    const { createTerminal } = await import("/src/terminal/term.ts");
    const host = document.querySelector('[data-region="terminal"]');
    host.replaceChildren();
    window.__widthTerminal = new AgentTerminal(host, {}, {
      mountTerminal: container => createTerminal(container, {}, 14),
    });
    window.__widthTerminal.mounted.fit();
    window.__widthTerminal.mounted.focus();
    window.__widthTerminal.mounted.terminal.write("Synthetic terminal resize check\r\n");
  });
  const cols = () => terminalPage.evaluate(() => window.__widthTerminal.mounted.terminal.cols);
  const initialCols = await cols();
  const terminalSidebar = terminalPage.locator('[data-region="sidebar"]');
  const terminalBox = await terminalSidebar.boundingBox();
  await terminalPage.mouse.move(terminalBox.x + terminalBox.width, terminalBox.y + 180);
  await terminalPage.mouse.down();
  await terminalPage.mouse.move(terminalBox.x + terminalBox.width + 160, terminalBox.y + 180, { steps: 8 });
  await terminalPage.mouse.up();
  await eventually(cols, value => value < initialCols, "real xterm refits after pointer resize");
  assert.equal(await terminalPage.locator(".xterm-helper-textarea").evaluate(el => el === document.activeElement), true);
  await terminalPage.locator("[data-collapse]").click();
  await eventually(cols, value => value > initialCols, "real xterm refits after collapse");
  // A standalone controller makes disposal observable without reaching into Sidebar.
  const cleanup = await terminalPage.evaluate(async () => {
    const { SidebarWidth } = await import("/src/panels/sidebar/width.ts");
    const workspace = document.createElement("div");
    workspace.style.width = "1200px";
    workspace.innerHTML = '<div data-sidebar-resize></div>';
    document.body.append(workspace);
    const handle = workspace.firstElementChild;
    const controller = new SidebarWidth(workspace, handle);
    handle.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight" }));
    const before = handle.getAttribute("aria-valuenow");
    controller.dispose();
    handle.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight" }));
    handle.dispatchEvent(new MouseEvent("dblclick"));
    window.dispatchEvent(new Event("resize"));
    const after = handle.getAttribute("aria-valuenow");
    workspace.remove();
    window.__widthTerminal.dispose();
    return { before, after };
  });
  assert.equal(cleanup.after, cleanup.before, "disposed controller leaves no active input/resize listeners");
  assert.deepEqual(errors, []);
  console.log("PASS: pointer/keyboard resize, focus, poll identity, persistence, bounds, reset, collapse, blur cleanup, mobile, invalid/blocked storage, real xterm refit, disposal; synthetic screenshots saved");
  await context.close();
} finally {
  await vite?.close();
  await browser?.close();
  server.close();
}
