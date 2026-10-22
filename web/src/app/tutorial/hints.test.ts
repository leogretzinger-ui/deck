import { test } from "node:test";
import assert from "node:assert/strict";
import { readdirSync, readFileSync } from "node:fs";
import { buildTutorialCatalog } from "./catalog.ts";
import { renderLessonList } from "./lesson-list.ts";
import { unknownRequirementFacts } from "./requirements.ts";
import { emptyStepContext, renderDoneAction, renderStepContent, type StepContext } from "./step-view.ts";

const lessonsDir = new URL("./lessons/en/", import.meta.url);
const catalog = buildTutorialCatalog(Object.fromEntries(readdirSync(lessonsDir)
  .filter((name) => name.endsWith(".md"))
  .map((name) => [`./lessons/en/${name}`, readFileSync(new URL(name, lessonsDir), "utf8")])));

function buttonsWithoutHint(html: string): string[] {
  return (html.match(/<button\b[^>]*>/g) ?? []).filter((tag) => !/\sdata-hint="[^"]+"/.test(tag));
}

test("every button a step renders carries a hint", () => {
  const contexts: StepContext[] = [
    emptyStepContext,
    { ...emptyStepContext, agent: "Codex", relnotes: "/work/relnotes", task: { title: "Fix it", description: "Do it." }, holds: () => true },
    { ...emptyStepContext, primaryDone: true, promptExpanded: true, holds: () => true },
  ];
  let rendered = 0;
  for (const lesson of catalog) {
    lesson.steps.forEach((step, index) => {
      for (const context of contexts) {
        const html = renderStepContent(step, context, `${lesson.id}-${index}`);
        rendered += (html.match(/<button\b/g) ?? []).length;
        assert.deepEqual(buttonsWithoutHint(html), [], `${lesson.id} step ${index}`);
      }
    });
  }
  assert.ok(rendered > 50);
});

test("the lesson list and the closing action carry hints", () => {
  const list = renderLessonList(catalog, catalog[0]!.id, [], unknownRequirementFacts);
  assert.equal((list.match(/<button\b/g) ?? []).length, catalog.length);
  assert.deepEqual(buttonsWithoutHint(list), []);
  assert.match(list, /data-hint="Open the lesson &quot;Your first task&quot;\. It takes about 8 min"/);
  assert.deepEqual(buttonsWithoutHint(renderDoneAction("lesson-2", "Two agents, one bug")), []);
  assert.deepEqual(buttonsWithoutHint(renderDoneAction("all-lessons", null)), []);
});

test("the picked agent is named in the hint of the button that starts it", () => {
  const step = catalog[0]!.steps.find((entry) => entry.action?.name === "create-starter")!;
  const html = renderStepContent(step, { ...emptyStepContext, agent: "Codex" }, "k");
  assert.match(html, /data-hint="Creates the practice projects relnotes and ccparse, and starts Codex in relnotes"/);
});

test("no button template in the tutorial sources is written without a hint", () => {
  const sources = readdirSync(new URL("./", import.meta.url))
    .filter((name) => name.endsWith(".ts") && !name.endsWith(".test.ts"))
    .map((name) => new URL(name, import.meta.url));
  sources.push(new URL("../../ui/markdown.ts", import.meta.url));
  for (const file of sources) {
    const text = readFileSync(file, "utf8");
    for (const match of text.matchAll(/<button\b[^`]*?>/g)) {
      assert.match(match[0], /data-hint=/, `${file.pathname}: ${match[0].slice(0, 80)}`);
    }
  }
});
