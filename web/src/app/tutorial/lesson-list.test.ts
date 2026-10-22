import { test } from "node:test";
import assert from "node:assert/strict";
import { readdirSync, readFileSync } from "node:fs";
import { buildTutorialCatalog } from "./catalog.ts";
import { lessonRow, renderLessonList } from "./lesson-list.ts";
import { unknownRequirementFacts, type RequirementFacts } from "./requirements.ts";

const lessonsDir = new URL("./lessons/en/", import.meta.url);
const catalog = buildTutorialCatalog(Object.fromEntries(readdirSync(lessonsDir)
  .filter((name) => name.endsWith(".md"))
  .map((name) => [`./lessons/en/${name}`, readFileSync(new URL(name, lessonsDir), "utf8")])));
const byId = (id: string) => catalog.find((lesson) => lesson.id === id)!;
const facts = (over: Partial<RequirementFacts> = {}): RequirementFacts => ({ ...unknownRequirementFacts, hostname: "deck.example.com", ...over });

test("a row shows the duration, and an unmet need dims it with the reason", () => {
  const met = lessonRow(byId("lesson-3"), "lesson-1", [], facts({ github: true }));
  assert.equal(met.caption, `${byId("lesson-3").duration}`);
  assert.equal(met.dimmed, false);
  const unmet = lessonRow(byId("lesson-3"), "lesson-1", [], facts({ github: false }));
  assert.equal(unmet.caption, `${byId("lesson-3").duration} · Needs a GitHub connection`);
  assert.equal(unmet.dimmed, true);
});

test("lesson 2 dims with the reason and the way to turn agent chat on only when it is off", () => {
  const off = lessonRow(byId("lesson-2"), "lesson-1", [], facts({ chats: false }));
  assert.match(off.caption, /Needs agent chat \(MemPalace\)/);
  assert.equal(off.dimmed, true);
  const html = renderLessonList(catalog, "lesson-1", [], facts({ chats: false }));
  assert.match(html, /Docker installs add the agent chat overlay to turn it on\. <a href="[^"]+#agent-chat-and-shared-memory"[^>]*data-hint="[^"]+">Agent chat setup<\/a>/);
  for (const chats of [true, null]) {
    const row = lessonRow(byId("lesson-2"), "lesson-1", [], facts({ chats }));
    assert.equal(row.dimmed, false, String(chats));
    assert.equal(row.enable, null, String(chats));
    assert.doesNotMatch(renderLessonList(catalog, "lesson-1", [], facts({ chats })), /agent chat overlay/);
  }
});

test("information is shown without dimming", () => {
  const row = lessonRow(byId("lesson-2"), "lesson-1", [], facts({ chats: true }));
  assert.match(row.caption, / · Uses two agent sessions$/);
  assert.equal(row.dimmed, false);
});

test("done and current come from the model's position", () => {
  const row = lessonRow(byId("lesson-1"), "lesson-1", ["lesson-1"], facts());
  assert.equal(row.done, true);
  assert.equal(row.current, true);
  assert.equal(lessonRow(byId("lesson-2"), "lesson-1", ["lesson-1"], facts()).done, false);
});

test("the list keeps every lesson, in both groups, whatever the installation can do", () => {
  const html = renderLessonList(catalog, "lesson-1", ["lesson-1"], facts({ chats: false, github: false, previews: false, hostname: "localhost" }));
  assert.equal((html.match(/data-tutorial-action="lesson"/g) ?? []).length, catalog.length);
  assert.ok(html.indexOf("Get started") < html.indexOf("Explore Deck"));
  assert.equal((html.match(/aria-label="Done"/g) ?? []).length, 1);
  assert.equal((html.match(/aria-current="step"/g) ?? []).length, 1);
  assert.match(html, /data-lesson="lesson-1" data-dimmed="false" aria-current="step"/);
  assert.match(html, /data-lesson="lesson-3" data-dimmed="true"/);
  assert.match(html, /Needs project previews/);
  assert.match(html, /Needs Deck reachable from your phone/);
});

test("every shipped lesson has a duration on its row", () => {
  for (const lesson of catalog) assert.match(lessonRow(lesson, "", [], facts()).caption, /^\d+ min/, lesson.id);
});
