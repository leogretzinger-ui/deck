import assert from "node:assert/strict";
import test from "node:test";

import { assetsLabel, flashAssetsArrived, panelTriggerHtml, panelsBadgeHtml, panelsLabel } from "./panel-trigger.ts";

test("Files and Project tasks share one desktop trigger primitive", () => {
  for (const trigger of ["files", "tasks"] as const) {
    const html = panelTriggerHtml("live", trigger, { pressed: true });
    assert.match(html, new RegExp(`data-live-${trigger}`));
    assert.match(html, /class="group\/panel-trigger flex h-7/);
    assert.match(html, /data-panel-trigger-visual[^>]*h-7/);
    assert.match(html, /border-border-strong/);
    assert.match(html, /gap-1\b/);
    assert.match(html, /aria-pressed="true"/);
  }
});

test("the triggers are secondary sm buttons carrying a data-hint", () => {
  const files = panelTriggerHtml("live", "files");
  const tasks = panelTriggerHtml("live", "tasks");
  assert.match(files, /data-hint="Browse the files in this project's working tree"/);
  assert.match(tasks, /data-hint="Open the task board parsed from the project's PRD"/);
  assert.match(files, /bg-surface-card/);
  assert.match(files, /rounded-control-sm/);
  assert.match(files, /font-medium/);
  // The label type belongs to the button: body-sm's 1.5 line ratio would
  // break the 13px line box the secondary sm control uses.
  assert.equal(files.includes("leading-[13px]"), true);
  assert.equal(files.includes("tracking-[0.004em]"), true);
  assert.equal(files.includes("text-body-sm"), false);
  assert.match(files, /width="14" height="14"/);
  assert.match(tasks, /width="14" height="14"/);
  assert.match(tasks, /M12 3v18/);
  assert.match(files, /M20 20a2/);
});

test("desktop panel triggers keep visible labels and accessible names", () => {
  const files = panelTriggerHtml("titlebar", "files");
  const tasks = panelTriggerHtml("titlebar", "tasks");
  assert.match(files, /aria-label="Files"/);
  assert.match(tasks, /aria-label="Project tasks"/);
  assert.match(files, /<span>Files<\/span>/);
  assert.match(tasks, /<span>Tasks<\/span>/);
});

test("the Tasks trigger never renders a count badge", () => {
  const tasks = panelTriggerHtml("live", "tasks");
  assert.match(tasks, /<span>Tasks<\/span>/);
  assert.equal(tasks.includes("data-live-tasks-count"), false);
  assert.equal(tasks.includes("bg-surface-muted"), false);
});

test("a chat-derived hint is escaped at the renderer boundary", () => {
  const html = panelTriggerHtml("live", "chats", { hint: 'Watch the chats x" onmouseover="alert(1)' , unseen: true });
  assert.ok(!html.includes('" onmouseover="'), "a quote in agent data cannot close the attribute");
  assert.match(html, /data-hint="Watch the chats x&quot; onmouseover=&quot;alert\(1\)"/);
  assert.match(html, /data-unseen="true"/);
});

test("the Assets trigger carries its count in a hidden badge and its name", () => {
  const unseen = panelTriggerHtml("live", "assets", { count: 3 });
  assert.match(unseen, /aria-label="Assets, 3 new"/);
  assert.match(unseen, /data-assets-new="3"/);
  assert.match(unseen, /<span data-assets-badge aria-hidden="true"\s+class="[^"]*bg-accent[^"]*text-text-on-accent[^"]*">3<\/span>/);
  assert.match(unseen, /-top-\[3px\] -right-\[5px\]/);
  assert.equal(unseen.includes("font-mono"), false, "digits are interface voice");
  const seen = panelTriggerHtml("live", "assets", { count: 0 });
  assert.match(seen, /aria-label="Assets"/);
  assert.match(seen, /data-assets-badge aria-hidden="true" hidden/);
  assert.equal(assetsLabel(1), "Assets, 1 new");
  assert.equal(panelTriggerHtml("live", "assets").includes("data-assets-badge"), false, "no count, no badge");
  assert.equal(panelTriggerHtml("live", "chats", { unseen: true }).includes("data-assets-badge"), false);
});

test("the Panels button badge is the small solid badge, hidden at zero", () => {
  assert.equal(panelsBadgeHtml(3), '<span data-assets-badge aria-hidden="true" class="assets-panels-badge">3</span>');
  assert.match(panelsBadgeHtml(0), /aria-hidden="true" hidden/);
});

test("only the Assets trigger takes a count", () => {
  const chats = panelTriggerHtml("live", "chats", { count: 3 });
  assert.match(chats, /aria-label="Chats"/);
  assert.equal(chats.includes("data-assets-badge"), false);
  assert.equal(chats.includes("data-assets-new"), false);
});

test("the Panels button's name carries the count its badge hides", () => {
  assert.equal(panelsLabel(0), "Panels");
  assert.equal(panelsLabel(1), "Panels, 1 new asset");
  assert.equal(panelsLabel(3), "Panels, 3 new assets");
});

// A root holding one Assets trigger whose visual records every wash started
// on it, styled by the given motion tokens.
function washRoot(tokens: Record<string, string>) {
  const washes: { keyframes: unknown; options: unknown; cancelled: boolean }[] = [];
  const visual = {
    animate(keyframes: unknown, options: unknown) {
      const wash = { keyframes, options, cancelled: false, cancel() { wash.cancelled = true; } };
      washes.push(wash);
      return wash;
    },
  };
  const trigger = { querySelector: () => visual };
  const root = {
    ownerDocument: { defaultView: { getComputedStyle: () => ({ getPropertyValue: (name: string) => tokens[name] ?? "" }) } },
    querySelector: (selector: string) => (selector === "[data-live-assets]" ? trigger : null),
  };
  return { root: root as unknown as HTMLElement, washes };
}

const MOTION = { "--dur-notice": "600ms", "--ease-out": "cubic-bezier(0.2, 0, 0, 1)", "--color-accent-tint": "rgb(232, 239, 255)" };

test("an arrival washes the trigger from the accent tint over the notice duration", () => {
  const { root, washes } = washRoot(MOTION);
  flashAssetsArrived(root, "live");
  assert.equal(washes.length, 1);
  assert.deepEqual(washes[0]!.keyframes, [{ backgroundColor: "rgb(232, 239, 255)" }, {}]);
  assert.deepEqual(washes[0]!.options, { duration: 600, easing: "cubic-bezier(0.2, 0, 0, 1)" });
});

test("the notice duration reads in either unit the build writes it in", () => {
  for (const [token, ms] of [["600ms", 600], [".6s", 600], ["0s", 0], ["0ms", 0]] as const) {
    const { root, washes } = washRoot({ ...MOTION, "--dur-notice": token });
    flashAssetsArrived(root, "live");
    assert.equal((washes[0]!.options as { duration: number }).duration, ms, token);
  }
});

test("arrivals in a burst restart the wash: only the latest one plays", () => {
  const { root, washes } = washRoot(MOTION);
  flashAssetsArrived(root, "live");
  flashAssetsArrived(root, "live");
  flashAssetsArrived(root, "live");
  assert.deepEqual(washes.map((wash) => wash.cancelled), [true, true, false]);
});

test("reduced motion's zero duration leaves only the badge", () => {
  const { root, washes } = washRoot({ ...MOTION, "--dur-notice": "0s" });
  flashAssetsArrived(root, "live");
  assert.deepEqual(washes[0]!.options, { duration: 0, easing: "cubic-bezier(0.2, 0, 0, 1)" });
});
