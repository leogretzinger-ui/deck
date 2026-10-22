import { test } from "node:test";
import assert from "node:assert/strict";
import { readdirSync, readFileSync } from "node:fs";

import * as hints from "../../lib/hints.ts";
import { LESSON_ACTIONS, LessonParseError, parseLesson } from "./lessons.ts";
import { COURSE_LESSON_FILES, EXPLORE_LESSON_FILES, buildTutorialCatalog } from "./catalog.ts";
import {
  DEFERRED_ACTIONS,
  WIRED_ACTIONS,
  actionWired,
  runTutorialAction,
  type TutorialActionHost,
} from "./actions.ts";
import {
  emptyStepContext,
  renderDoneAction,
  renderStepContent,
  stepHidden,
  substitute,
  type StepContext,
} from "./step-view.ts";

const lessonsDir = new URL("./lessons/en/", import.meta.url);

function lessonFiles(): Record<string, string> {
  return Object.fromEntries(readdirSync(lessonsDir)
    .filter((name) => name.endsWith(".md"))
    .map((name) => [`./lessons/en/${name}`, readFileSync(new URL(name, lessonsDir), "utf8")]));
}

const FRONT = "---\nid: sample\ntitle: Sample\ngroup: course\nduration: 1 min\nrequires: []\nprinciples: [1]\n---\n\n# Sample\n\n";
const DONE = "\n## done\nnext: all-lessons\n\nAll done.\n";

function lesson(steps: string, front = FRONT, done = DONE) {
  return parseLesson("sample.md", front + steps + done);
}

function rejects(source: string, pattern: RegExp, line?: number): void {
  assert.throws(() => parseLesson("sample.md", source), (error: unknown) => {
    assert.ok(error instanceof LessonParseError, String(error));
    assert.match(error.message, /^sample\.md:\d+: /, "the error names the file and a line");
    assert.match(error.message, pattern);
    if (line !== undefined) assert.equal(error.line, line);
    return true;
  });
}

test("a lesson parses into front-matter, steps with settings, and the done section", () => {
  const parsed = lesson(`## First step
advance_on: input_sent(project=relnotes) then session_busy
primary: Put it in the terminal
action: insert-prompt
fallback_when: no_task(requirement=2)
fallback: Add it by hand
fallback_action: add-task(requirement=2)
opens: tasks
prompt: |
  Do the thing.
  Then stop.

Read **this** first.

## Second step

Only a body.
`);
  assert.equal(parsed.id, "sample");
  assert.equal(parsed.group, "course");
  assert.deepEqual(parsed.principles, [1]);
  assert.equal(parsed.steps.length, 2);
  const [first, second] = parsed.steps;
  assert.equal(first!.title, "First step");
  assert.equal(first!.body, "Read **this** first.");
  assert.deepEqual(first!.advanceOn, [{ name: "input_sent", arg: "project=relnotes" }, { name: "session_busy", arg: null }]);
  assert.deepEqual(first!.action, { name: "insert-prompt", arg: null });
  assert.deepEqual(first!.fallbackWhen, { name: "no_task", arg: "requirement=2" });
  assert.deepEqual(first!.fallbackAction, { name: "add-task", arg: "requirement=2" });
  assert.equal(first!.opens, "tasks");
  assert.equal(first!.prompt, "Do the thing.\nThen stop.");
  assert.equal(second!.body, "Only a body.");
  assert.equal(second!.advanceOn, undefined);
  assert.deepEqual(parsed.done, { next: "all-lessons", body: "All done." });
});

test("a step may have settings and no body, and a lesson may have no done section", () => {
  const parsed = lesson("## Bare\nadvance_on: continue\n", FRONT, "");
  assert.equal(parsed.steps[0]!.body, "");
  assert.equal(parsed.done, null);
});

test("generate_count must belong to generate-tasks", () => {
  const parsed = lesson("## Generate\nprimary: Generate tasks\naction: generate-tasks\ngenerate_count: 2\n");
  assert.equal(parsed.steps[0]!.generateCount, 2);
  rejects(FRONT + "## Count\ngenerate_count: 2\n" + DONE, /generate_count belongs to a generate-tasks action/);
});

test("target names the project that receives an insert-prompt", () => {
  const parsed = lesson("## Ask\nprimary: Put it in the terminal\naction: insert-prompt\ntarget: ccparse\nprompt: |\n  Go.\n");
  assert.equal(parsed.steps[0]!.target, "ccparse");
  const staged = lesson("## Ask\nprimary: Start it\naction: start-session(project=ccparse)\nthen_primary: Put it in the terminal\nthen_action: insert-prompt\ntarget: lane\nprompt: |\n  Go.\n");
  assert.equal(staged.steps[0]!.target, "lane");
  rejects(FRONT + "## Ask\nprimary: Put it in the terminal\naction: insert-prompt\ntarget: elsewhere\nprompt: |\n  Go.\n" + DONE, /target is relnotes, ccparse, lane, source, not "elsewhere"/);
  rejects(FRONT + "## Run\nprimary: Mark it\naction: mark-task-done\ntarget: relnotes\n" + DONE, /"target" needs an insert-prompt or run-command action/);
});

test("every shipped prompt step, and every command step of the course, names the project it is about", () => {
  for (const entry of buildTutorialCatalog(lessonFiles())) {
    for (const item of entry.steps) {
      if (item.prompt !== undefined) assert.ok(item.target !== undefined, `${entry.id}: "${item.title}" has a prompt and no target`);
      if (item.command !== undefined && entry.optional !== true) assert.ok(item.target !== undefined, `${entry.id}: "${item.title}" has a command and no target`);
    }
  }
});

test("front-matter rejections", () => {
  rejects("# No front-matter\n", /must start with a `---`/, 1);
  rejects(FRONT.replace("id: sample\n", "id: sample\ncolour: red\n") + "## A\n" + DONE, /unknown front-matter key "colour"/, 3);
  rejects(FRONT.replace("title: Sample\n", "") + "## A\n" + DONE, /missing "title"/);
  rejects(FRONT.replace("id: sample\n", "") + "## A\n" + DONE, /missing "id"/);
  rejects(FRONT.replace("group: course", "group: bonus") + "## A\n" + DONE, /group is "course" or "explore"/);
  rejects(FRONT.replace("id: sample\n", "id: sample\nid: again\n") + "## A\n" + DONE, /duplicate front-matter key "id"/);
  rejects(FRONT.replace("requires: []", "requires: [telepathy]") + "## A\n" + DONE, /unknown requirement "telepathy"/);
  rejects(FRONT.replace("group: course", "group: explore") + "## A\n" + DONE, /principles belong to course lessons only/);
  rejects(FRONT.replace("# Sample", "# Other") + "## A\n" + DONE, /must match the front-matter title/);
  rejects("---\nid: sample\ntitle: Sample\n", /not closed/);
});

test("a setting after the body is rejected, not read as text", () => {
  const source = lessonSource("## A\nadvance_on: continue\n\nSome body.\nprimary: Go\n");
  rejects(source, /setting "primary" appears after the body/, source.split("\n").indexOf("primary: Go") + 1);
  rejects(lessonSource("## A\n\nSome body.\n\naction: insert-prompt\n"), /setting "action" appears after the body/);
});

function lessonSource(steps: string): string {
  return FRONT + steps + DONE;
}

test("unknown settings, actions, events and conditions are rejected", () => {
  rejects(lessonSource("## A\ncolour: red\n"), /unknown setting "colour"/);
  rejects(lessonSource("## A\nconstructor: ignored\n"), /unknown setting "constructor"/);
  rejects(lessonSource("## A\nprimary: Go\naction: explode\n"), /unknown action "explode"/);
  rejects(lessonSource("## A\nadvance_on: user_is_happy\n"), /unknown event "user_is_happy"/);
  rejects(lessonSource("## A\nadvance_on: session_attached\n"), /needs an argument/);
  rejects(lessonSource("## A\nadvance_on: task_done(requirement=1)\n"), /does not accept "requirement=1"/);
  rejects(lessonSource("## A\nadvance_on: continue then task_done\n"), /`continue` cannot be combined/);
  rejects(lessonSource("## A\nwhen: weekend\n"), /unknown condition "weekend"/);
  rejects(lessonSource("## A\nwhen: agent_cannot_generate_tasks\n"), /unknown condition "agent_cannot_generate_tasks"/);
  rejects(lessonSource("## A\nfallback: Hand\nfallback_action: add-task\nfallback_when: starter_exists\n"), /unknown condition "starter_exists"/);
  rejects(lessonSource("## A\nopens: kitchen\n"), /opens is tasks, files or chats/);
  rejects(lessonSource("## A\nprimary: Go\naction: start-session\n"), /needs an argument/);
});

test("structural rejections", () => {
  rejects(lessonSource("## A\nprimary: Go\nprimary: Again\naction: add-project\n"), /duplicate setting "primary"/);
  rejects(lessonSource("## A\nSome body straight under the heading.\n"), /expected a `key: value` setting/);
  rejects(lessonSource("## A\nprompt: |\n\nbody\n"), /needs lines indented by two spaces/);
  rejects(lessonSource("## A\nprimary:\naction: add-project\n"), /has no value/);
  rejects(lessonSource("## A\nprimary: Go\n"), /"primary" needs "action"/);
  rejects(lessonSource("## A\naction: add-project\n"), /"action" needs "primary"/);
  rejects(lessonSource("## A\nprompt: |\n  Text\n"), /"prompt" needs an insert-prompt action/);
  rejects(lessonSource("## A\nfallback_when: no_git_identity\n"), /"fallback_when" needs "fallback"/);
  rejects(lessonSource("## \n"), /a step needs a title/);
  rejects(FRONT + "Stray text.\n\n## A\n" + DONE, /text before the first step/);
  rejects(FRONT + DONE, /no steps/);
  rejects(FRONT + "## done\nnext: all-lessons\n\nBye.\n\n## After\n", /`## done` must be the last section/);
  rejects(lessonSource("## A\n").replace("next: all-lessons", "next: all-lessons\nprimary: Go"), /not allowed in the done section/);
  rejects(lessonSource("## A\n").replace("next: all-lessons\n", ""), /needs a "next" setting/);
  rejects(lessonSource("## A\n").replace("All done.", ""), /needs a closing line/);
});

test("placeholders and links in lesson text are rejected at their own line", () => {
  const at = (source: string, needle: string): number => source.split("\n").findIndex((line) => line.includes(needle)) + 1;
  const cases: [string, RegExp, string][] = [
    ["## A\n\nFirst line.\nHello {user}.\n", /unknown placeholder \{user\}/, "Hello {user}"],
    ["## A\n\nHello {user_name}.\n", /unknown placeholder \{user_name\}/, "Hello {user_name}"],
    ["## A\n\nHello {task.title2}.\n", /unknown placeholder \{task\.title2\}/, "Hello {task.title2}"],
    ["## A\n\nHello {Agent}.\n", /unknown placeholder \{Agent\}/, "Hello {Agent}"],
    ["## A\n\nSee [the docs](docs/readme.md).\n", /must use the lesson: or action: scheme/, "the docs"],
    ["## A\n\nOne.\n\nTwo.\nSee [x](action:explode).\n", /unknown action "explode"/, "action:explode"],
    ["## A\n\nSee [x](javascript:alert(1)).\n", /must use the lesson: or action: scheme/, "javascript"],
    ["## A\nprimary: Go {user}\naction: add-project\n", /unknown placeholder/, "primary:"],
    ["## A\nprimary: Go\naction: insert-prompt\nprompt: |\n  one\n  two [x](docs/a.md)\n", /must use the lesson: or action: scheme/, "two [x]"],
    ["## A\nfooter: See [x](action:explode)\n", /unknown action/, "footer:"],
  ];
  for (const [steps, pattern, needle] of cases) {
    const source = lessonSource(steps);
    rejects(source, pattern, at(source, needle));
  }
  const closing = lessonSource("## A\n").replace("All done.", "Done.\nSee [x](docs/a.md).");
  rejects(closing, /must use the lesson: or action: scheme/, at(closing, "See [x]"));
  const ok = lesson("## A\n\n{agent} says [go](lesson:lesson-2), [identity](action:git-identity), [site](https://example.com).\n");
  assert.equal(ok.steps.length, 1);
});

test("errors carry the line of the offending text", () => {
  const source = lessonSource("## A\nadvance_on: continue\n\nBody.\n\n## B\nprimary: Go\nwhat: no\n");
  assert.throws(() => parseLesson("x.md", source), (error: unknown) => {
    assert.ok(error instanceof LessonParseError);
    assert.equal(error.file, "x.md");
    assert.equal(error.line, source.split("\n").findIndex((line) => line === "what: no") + 1);
    return true;
  });
});

// --- The shipped files ---

test("every lesson file on disk parses", () => {
  const files = lessonFiles();
  assert.ok(Object.keys(files).length > 0, "the lessons directory is empty");
  for (const [path, text] of Object.entries(files)) {
    assert.doesNotThrow(() => parseLesson(path, text), path);
  }
});

test("the catalog is built from the files in the documented order", () => {
  const catalog = buildTutorialCatalog(lessonFiles());
  assert.deepEqual(catalog.slice(0, 3).map((entry) => entry.id), ["lesson-1", "lesson-2", "lesson-3"]);
  assert.equal(catalog.length, COURSE_LESSON_FILES.length + EXPLORE_LESSON_FILES.length);
  assert.deepEqual(catalog.slice(3).map((entry) => entry.id), [
    "away", "reviewer", "sessions", "files", "tasks", "previews", "memory", "reconnect", "logins", "appearance", "what-an-agent-can-reach", "core-principles",
  ]);
  assert.deepEqual(catalog.map((entry) => entry.optional), catalog.map((_, index) => index >= 3));
  assert.equal(new Set(catalog.map((entry) => entry.id)).size, catalog.length);
});

test("a lesson with a done section ends on a closing page that names the next lesson", () => {
  const catalog = buildTutorialCatalog(lessonFiles());
  const first = catalog[0]!;
  const last = first.steps.at(-1)!;
  assert.equal(last.title, "Done");
  assert.deepEqual(last.done, { next: "lesson-2" });
  assert.match(last.body, /^Lesson 1 is done\./);
  const noDone = catalog.find((entry) => entry.id === "core-principles")!;
  assert.equal(noDone.steps.at(-1)!.done, undefined);
});

test("the catalog refuses unlisted, missing, misgrouped and dangling lessons", () => {
  const files = lessonFiles();
  assert.throws(() => buildTutorialCatalog({ ...files, "./extra.md": FRONT + "## A\n" + DONE }), /extra\.md is not in the catalog order/);
  const { "./lessons/en/core-principles.md": _dropped, ...missing } = files;
  assert.throws(() => buildTutorialCatalog(missing), /core-principles\.md is missing/);
  const regrouped = { ...files, "./lessons/en/core-principles.md": files["./lessons/en/core-principles.md"]!.replace("group: explore", "group: course") };
  assert.throws(() => buildTutorialCatalog(regrouped), /catalog lists it under "explore"/);
  const dangling = { ...files, "./lessons/en/lesson-1-your-first-task.md": files["./lessons/en/lesson-1-your-first-task.md"]!.replace("next: lesson-2", "next: lesson-9") };
  assert.throws(() => buildTutorialCatalog(dangling), /unknown lesson "lesson-9"/);
  const dupe = { ...files, "./lessons/en/core-principles.md": files["./lessons/en/core-principles.md"]!.replace("id: core-principles", "id: lesson-1") };
  assert.throws(() => buildTutorialCatalog(dupe), /duplicate lesson id "lesson-1"/);
  const reserved = { ...files, "./lessons/en/core-principles.md": files["./lessons/en/core-principles.md"]!.replace("id: core-principles", "id: welcomed") };
  assert.throws(() => buildTutorialCatalog(reserved), /reserved for the welcome page/);
});

test("prompts and commands are single lines Deck can type without pressing Enter", () => {
  for (const entry of buildTutorialCatalog(lessonFiles())) {
    for (const step of entry.steps) {
      for (const text of [step.prompt, step.command]) {
        if (text === undefined) continue;
        assert.ok(!/[\u0000-\u001f\u007f-\u009f]/u.test(text), `${entry.id}: "${step.title}" has a control character`);
      }
    }
  }
});

// --- Hints ---

test("every primary, fallback and then_primary label has a matching hint string", () => {
  const known = new Set<string>((Object.values(hints) as unknown[]).filter((value): value is string => typeof value === "string"));
  const labels = new Set<string>();
  for (const entry of buildTutorialCatalog(lessonFiles())) {
    for (const step of entry.steps) {
      for (const label of [step.primary, step.fallback, step.thenPrimary]) {
        if (label === undefined) continue;
        // `A → B` names two controls; a label with its own link names the link text.
        for (const part of label.split(" → ")) {
          const link = /\[([^\]]+)\]\([^)]+\)/.exec(part);
          labels.add(link === null ? part : link[1]!);
        }
      }
    }
  }
  assert.ok(labels.size > 10);
  // {agent} stays literal in both the label and the hint template; the
  // panel substitutes it when it draws the button.
  const missing = [...labels].filter((label) => !known.has(label));
  assert.deepEqual(missing, [], "labels with no matching hint string");
});

// --- Actions ---

test("every lesson action is either wired or deferred, and none collides with the panel's own", () => {
  assert.deepEqual([...DEFERRED_ACTIONS].sort(), []);
  assert.equal(WIRED_ACTIONS.size + DEFERRED_ACTIONS.size, LESSON_ACTIONS.size);
  for (const name of LESSON_ACTIONS) assert.ok(WIRED_ACTIONS.has(name) !== DEFERRED_ACTIONS.has(name), name);
  for (const internal of ["close", "collapse", "show", "next", "back", "lessons", "lesson", "copy"]) {
    assert.ok(!LESSON_ACTIONS.has(internal), internal);
  }
});

test("the action table sends each wired action to its host call and nothing else", () => {
  const calls: string[] = [];
  const host = new Proxy({} as TutorialActionHost, {
    get: (_target, name: string) => (...args: unknown[]) => { calls.push([name, ...args].join(":")); },
  });
  const args: Record<string, string> = {
    "generate-tasks": "count=2", "add-task": "requirement=2", "start-session": "project=ccparse",
  };
  for (const name of ["start-lane", "request-review", "mark-task-done"]) assert.ok(WIRED_ACTIONS.has(name), name);
  for (const name of WIRED_ACTIONS) assert.equal(runTutorialAction(host, name, "typed", args[name] ?? null), true, name);
  assert.equal(calls.length, WIRED_ACTIONS.size);
  assert.ok(calls.includes("insertPrompt:typed:"));
  assert.ok(calls.includes("runCommand:typed:"));
  assert.ok(calls.includes("generateTasks:2"));
  assert.ok(calls.includes("addTask:2"));
  assert.ok(calls.includes("startSession:ccparse"));
  assert.ok(calls.includes("startLane"));
  assert.ok(calls.includes("requestReview"));
  assert.ok(calls.includes("markTaskDone"));
  const before = calls.length;
  for (const name of [...DEFERRED_ACTIONS, "toString", "constructor", "nope"]) assert.equal(runTutorialAction(host, name, ""), false, name);
  // A count, requirement or project that is missing or out of range reaches no host call.
  for (const [name, arg] of [["generate-tasks", "count=0"], ["generate-tasks", "count=51"], ["generate-tasks", "count=x"],
    ["add-task", "requirement=0"], ["add-task", "requirement=x"], ["start-session", null], ["start-session", "requirement=1"]] as const) {
    runTutorialAction(host, name, "", arg);
  }
  assert.equal(calls.length, before);
  // Without a count or requirement the same actions work on the user's own project.
  runTutorialAction(host, "generate-tasks", "", null);
  runTutorialAction(host, "add-task", "", null);
  assert.deepEqual(calls.slice(before), ["generateTasks:", "addTask:"]);
  calls.length = 0;
  runTutorialAction(host, "insert-prompt", "typed", "target=ccparse");
  assert.deepEqual(calls, ["insertPrompt:typed:ccparse"]);
});

test("starting a session is wired only where the step names the project", () => {
  const call = (name: string, arg: string | null = null) => ({ name, arg });
  assert.equal(actionWired(call("generate-tasks")), true);
  assert.equal(actionWired(call("add-task")), true);
  assert.equal(actionWired(call("add-task", "requirement=1")), true);
  assert.equal(actionWired(call("start-session", "project=ccparse")), true);
  assert.equal(actionWired(call("start-session")), false);
  assert.equal(actionWired(call("create-starter")), true);
  assert.equal(actionWired(call("mark-task-done")), true);
  assert.equal(actionWired(call("start-lane")), true);
  assert.equal(actionWired(call("request-review")), true);
});

test("buttons carry the arguments their action needs, and unwired ones are disabled", () => {
  const generate = renderStepContent(step("## Tasks\nprimary: Generate tasks\naction: generate-tasks\ngenerate_count: 2\n\nBody.\n"), emptyStepContext, "k");
  assert.match(generate, /data-tutorial-action="generate-tasks" data-tutorial-arg="count=2"/);
  const own = renderStepContent(step("## Tasks\nprimary: Generate tasks\naction: generate-tasks\n\nBody.\n"), emptyStepContext, "k");
  assert.match(own, /data-tutorial-action="generate-tasks" data-hint="[^"]+">Generate tasks<\/button>/);
  assert.doesNotMatch(own, /data-tutorial-arg/);
  const prompt = renderStepContent(step("## Ask\nprimary: Put it in the terminal\naction: insert-prompt\ntarget: ccparse\nprompt: |\n  Go.\n"), emptyStepContext, "k");
  assert.match(prompt, /data-tutorial-action="insert-prompt" data-tutorial-text="Go\." data-tutorial-arg="target=ccparse"/);
  const manual = renderStepContent(step("## Second\nfallback: Add it by hand\nfallback_action: add-task(requirement=2)\n\nBody.\n"), emptyStepContext, "k");
  assert.match(manual, /data-tutorial-action="add-task" data-tutorial-arg="requirement=2"/);
  const session = renderStepContent(step("## Other\nprimary: Start it\naction: start-session(project=ccparse)\n\nBody.\n"), emptyStepContext, "k");
  assert.match(session, /data-tutorial-action="start-session" data-tutorial-arg="project=ccparse"/);
});

// --- Rendering ---

function step(source: string) {
  return lesson(source).steps[0]!;
}

test("placeholders take the chosen agent, else words, and never stay raw", () => {
  assert.equal(substitute("Ask {agent} to build it", emptyStepContext).text, "Ask your agent to build it");
  assert.equal(substitute("{agent} is working", emptyStepContext).text, "Your agent is working");
  assert.equal(substitute("Ask {agent}", { ...emptyStepContext, agent: "codex" }).text, "Ask codex");
  const unknown = substitute('Task "{task.title}". {task.description} Go.', emptyStepContext);
  assert.equal(unknown.text, 'Task "your task". Go.');
  assert.equal(unknown.complete, false);
  const known = substitute('Task "{task.title}". {task.description} Go.', { ...emptyStepContext, task: { title: "Fix it", description: "It is broken." } });
  assert.equal(known.text, 'Task "Fix it". It is broken. Go.');
  assert.equal(known.complete, true);
  assert.equal(substitute("node {starter.relnotes}/relnotes.mjs", { ...emptyStepContext, relnotes: "/w/relnotes-2" }).text, "node /w/relnotes-2/relnotes.mjs");
});

test("no shipped step renders a raw placeholder, with or without context", () => {
  const known: StepContext = { ...emptyStepContext, agent: "claude", task: { title: "T", description: "D" }, relnotes: "/p" };
  for (const entry of buildTutorialCatalog(lessonFiles())) {
    for (const [index, item] of entry.steps.entries()) {
      for (const context of [emptyStepContext, known]) {
        const html = renderStepContent(item, context, `${entry.id}-${index}`) + substitute(item.title, context).text;
        assert.doesNotMatch(html, /\{(agent|task\.|starter\.)/, `${entry.id}: ${item.title}`);
      }
    }
  }
});

test("a prompt shows in full with Copy and an insert button carrying the text", () => {
  const item = step("## Ask\nprimary: Put it in the terminal\naction: insert-prompt\nprompt: |\n  Fix <b>it</b> & stop.\n\nHere is a prompt.\n");
  const html = renderStepContent(item, emptyStepContext, "k");
  assert.ok(html.includes('<pre class="tutorial-prompt"><code>Fix &lt;b&gt;it&lt;/b&gt; &amp; stop.</code></pre>'), html);
  assert.ok(html.includes('data-tutorial-action="insert-prompt" data-tutorial-text="Fix &lt;b&gt;it&lt;/b&gt; &amp; stop."'), html);
  assert.ok(html.includes('data-tutorial-action="copy"'), html);
  assert.ok(html.includes("Here is a prompt."));
});

test("a prompt that needs a task is shown but cannot be inserted until one is known", () => {
  const item = step('## Ask\nprimary: Put it in the terminal\naction: insert-prompt\nprompt: |\n  Task "{task.title}". {task.description} Go.\n');
  const without = renderStepContent(item, emptyStepContext, "k");
  assert.match(without, /<button[^>]*disabled[^>]*>Put it in the terminal<\/button>/);
  assert.ok(without.includes("Needs a task first"));
  assert.ok(!without.includes('data-tutorial-action="insert-prompt"'));
  const withTask = renderStepContent(item, { ...emptyStepContext, task: { title: "Fix", description: "Now." } }, "k");
  assert.ok(withTask.includes('data-tutorial-action="insert-prompt" data-tutorial-text="Task &quot;Fix&quot;. Now. Go."'), withTask);
});

test("task text is escaped and never becomes markup or a tutorial link", () => {
  const item = step("## Body\n\nWorking on {task.title}.\n");
  const html = renderStepContent(item, { ...emptyStepContext, task: { title: "<img src=x onerror=1> [go](action:insert-prompt)", description: "" } }, "k");
  assert.ok(!html.includes("<img"), html);
  assert.ok(!html.includes("<button"), html);
  assert.ok(html.includes("&lt;img"), html);
});

test("an action that needs a detail the step lacks renders disabled with a visible reason and no action attribute", () => {
  const item = { ...step("## Go\nprimary: Start it\naction: start-session(project=ccparse)\n"), action: { name: "start-session", arg: null } };
  const html = renderStepContent(item, emptyStepContext, "k");
  assert.match(html, /<button[^>]*disabled[^>]*aria-disabled="true"[^>]*aria-describedby="k-primary"[^>]*>Start it<\/button>/);
  assert.ok(html.includes('<span id="k-primary" class="tutorial-reason">Available in a later update</span>'), html);
  assert.ok(!html.includes("sr-only"), html);
  assert.ok(!html.includes("data-tutorial-action"), html);
});

test("the lane, review and done buttons render enabled", () => {
  for (const [label, action] of [["Start a lane", "start-lane"], ["Request review", "request-review"], ["Mark task done", "mark-task-done"]]) {
    const html = renderStepContent(step(`## Go\nprimary: ${label}\naction: ${action}\n`), emptyStepContext, "k");
    assert.ok(html.includes(`data-tutorial-action="${action}" data-hint=`) && html.includes(`>${label}</button>`), html);
    assert.doesNotMatch(html, / disabled /);
  }
});

test("a wired action renders an enabled button", () => {
  const html = renderStepContent(step("## Go\nprimary: Choose an agent…\naction: choose-agent\nprimary_note: Takes a moment.\n"), emptyStepContext, "k");
  assert.ok(/data-tutorial-action="choose-agent" data-hint="[^"]+">Choose an agent…<\/button>/.test(html), html);
  assert.ok(html.includes("Takes a moment."));
});

test("a command shows with its button and a note, and with Copy when the action is another", () => {
  const run = renderStepContent(step("## Run\nprimary: Put in a terminal\naction: run-command\ncommand: node --test\ncommand_note: Runs the tests.\n"), emptyStepContext, "k");
  assert.ok(run.includes('<pre class="tutorial-command"><code>node --test</code></pre>'), run);
  assert.ok(run.includes('data-tutorial-action="run-command" data-tutorial-text="node --test"'), run);
  assert.ok(run.includes("Runs the tests."));
  const show = renderStepContent(step("## Look\nprimary: Mark task done\naction: mark-task-done\ncommand: git diff\n"), emptyStepContext, "k");
  assert.ok(show.includes('data-tutorial-action="copy" data-tutorial-text="git diff"'), show);
  assert.ok(!show.includes('data-tutorial-action="run-command"'), show);
});

test("fallbacks honour their conditions and links", () => {
  const source = "## Go\nprimary: Add PRD…\naction: add-prd\nfallback_when: no_git_identity\nfallback: Add task…\nfallback_action: add-task\n";
  assert.ok(!renderStepContent(step(source), emptyStepContext, "k").includes("Add task…"));
  const holds: StepContext = { ...emptyStepContext, holds: (call) => call.name === "no_git_identity" };
  assert.ok(renderStepContent(step(source), holds, "k").includes("Add task…"));
  const linked = renderStepContent(step("## Go\nfallback: Commits as [Set a Git identity…](action:git-identity)\nfallback_action: git-identity\n"), emptyStepContext, "k");
  assert.match(linked, /<button type="button" data-tutorial-action="git-identity" class="[^"]*" data-hint="[^"]+">Set a Git identity…<\/button>/);
});

test("note_when, fine print and footer render in their place", () => {
  const source = "## Go\nnote_when: no_review_app\nnote: Without the app.\nfine_print: Always new folders.\nfooter: Read [more](lesson:lesson-2)\n";
  const html = renderStepContent(step(source), emptyStepContext, "k");
  assert.ok(!html.includes("Without the app."));
  assert.ok(html.includes("Always new folders."));
  assert.ok(html.includes('data-lesson="lesson-2"'), html);
  assert.ok(renderStepContent(step(source), { ...emptyStepContext, holds: () => true }, "k").includes("Without the app."));
});

test("a step with a when condition is hidden unless the condition holds", () => {
  const item = step("## Sign in\nwhen: agent_needs_login\n\nFinish there.\n");
  assert.equal(stepHidden(item, emptyStepContext), true);
  assert.equal(stepHidden(item, { ...emptyStepContext, holds: () => true }), false);
  assert.equal(stepHidden(step("## Plain\n\nText.\n"), emptyStepContext), false);
});

test("the closing page offers the next lesson, or All lessons", () => {
  assert.match(renderDoneAction("lesson-2", "Two agents, one bug"), /data-tutorial-action="lesson" data-lesson="lesson-2" data-hint="[^"]+">Two agents, one bug</);
  assert.match(renderDoneAction("all-lessons", null), /data-tutorial-action="lessons" data-hint="[^"]+">All lessons</);
  assert.equal(renderDoneAction("gone", null), "");
});
