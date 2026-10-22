// The task-detail markdown renderer. Its input is untrusted — task text
// comes from a PRD via the task engine, and for generated backlogs it is
// model-written — so the escaping tests below are the load-bearing ones.
import assert from "node:assert/strict";
import test from "node:test";

import { renderMarkdown } from "./markdown.ts";

// --- Safety ---

test("source markup never reaches the output as markup", () => {
  const html = renderMarkdown(`<img src=x onerror="alert(1)"> and <script>alert(2)</script>`);
  assert.ok(!html.includes("<img"), "img must not survive");
  assert.ok(!html.includes("<script"), "script must not survive");
  // "onerror=" DOES appear — as escaped text, which is the point. What
  // must not exist is a real tag carrying an event handler.
  assert.ok(!/<[a-z]+[^>]*\son\w+=/i.test(html), "no element may carry an event handler");
  assert.ok(html.includes("&lt;img"), "it renders as visible text instead");
});

test("markup inside a code fence is text, not markup", () => {
  const html = renderMarkdown("```\n<script>alert(1)</script>\n```");
  assert.ok(html.includes("<pre"), "a fence renders as a code block");
  assert.ok(!html.includes("<script>"), "its contents stay escaped");
  assert.ok(html.includes("&lt;script&gt;"));
});

// A link is the one place a value becomes an attribute, so schemes are
// allowlisted rather than filtered.
test("only http(s) links become anchors", () => {
  const safe = renderMarkdown("[docs](https://example.com/a?b=1)");
  assert.ok(safe.includes('href="https://example.com/a?b=1"'));
  assert.ok(safe.includes('rel="noopener noreferrer"'));
  for (const hostile of [
    "[x](javascript:alert(1))",
    "[x](data:text/html;base64,PHNjcmlwdD4=)",
    "[x](vbscript:msgbox)",
    "[x](/relative/path)",
  ]) {
    const html = renderMarkdown(hostile);
    assert.ok(!html.includes("<a "), `${hostile} must not become a link`);
  }
});

test("a quote in a link target cannot break out of the attribute", () => {
  const html = renderMarkdown('[x](https://example.com/"onmouseover="alert(1))');
  const href = /href="([^"]*)"/.exec(html);
  if (href !== null) {
    assert.ok(!href[1]!.includes('"'), 'the href value contains no raw quote');
    assert.ok(!href[1]!.includes('<'), 'the href value contains no raw angle bracket');
  }
  assert.ok(!/<[a-z]+[^>]*\son\w+=/i.test(html), 'no element may carry an event handler');
});

test("a language hint cannot break out of its attribute", () => {
  const html = renderMarkdown('```js" onload="alert(1)\ncode\n```');
  assert.ok(!html.includes("onload"), "a hostile hint is dropped, not emitted");
});

// --- Rendering ---

test("fenced code becomes a scrollable block that keeps its text", () => {
  const html = renderMarkdown("Run this:\n\n```sh\ngo test ./... -run Foo\n```");
  assert.ok(html.includes("<pre"), "a block element");
  assert.ok(html.includes("overflow-x-auto"), "long lines scroll rather than wrap");
  assert.ok(html.includes('data-lang="sh"'));
  assert.ok(html.includes("go test ./... -run Foo"));
});

test("an unterminated fence still renders as code", () => {
  const html = renderMarkdown("```\nnot closed");
  assert.ok(html.includes("<pre"));
  assert.ok(html.includes("not closed"));
});

test("inline code, bold and italic render", () => {
  const html = renderMarkdown("set `PS1` to **bold** and *thin*");
  assert.ok(html.includes("<code"));
  assert.ok(html.includes("PS1"));
  assert.ok(html.includes("<strong"));
  assert.ok(html.includes("<em>thin</em>"));
});

// The inline-code placeholder is NUL-delimited, not a bare index in
// spaces: a bare index would eat ordinary prose containing a number.
test("prose containing bare numbers survives inline-code extraction", () => {
  const html = renderMarkdown("run `make` at step 3 of 5 then 7 more");
  assert.ok(html.includes("step 3 of 5"), "digits in prose are untouched");
  assert.ok(html.includes("7 more"));
  assert.ok(html.includes("<code"));
});

test("emphasis inside a code span is not applied", () => {
  const html = renderMarkdown("use `snake_case_name` here");
  assert.ok(html.includes("snake_case_name"), "underscores survive verbatim");
  assert.ok(!html.includes("<em>"));
});

test("lists render as lists", () => {
  const bullets = renderMarkdown("- one\n- two");
  assert.ok(bullets.includes("<ul"));
  assert.equal((bullets.match(/<li/g) ?? []).length, 2);
  const numbered = renderMarkdown("1. first\n2. second");
  assert.ok(numbered.includes("<ol"));
});

// A fence between two steps ends the block, so what follows is a second
// <ol> — which restarts at 1 unless it carries the author's own numbering.
test("a numbered list resumes its count after an embedded code block", () => {
  const html = renderMarkdown("1. first\n2. second\n\n```sh\nmake\n```\n\n3. third\n4. fourth");
  const starts = [...html.matchAll(/<ol([^>]*)>/g)].map((m) => m[1]!);
  assert.equal(starts.length, 2, "the fence splits the list in two");
  assert.ok(!starts[0]!.includes("start="), "the first list starts at 1 implicitly");
  assert.ok(starts[1]!.includes('start="3"'), `the second resumes at 3: ${html}`);
});

test("a list that really does start at 1 carries no start attribute", () => {
  const html = renderMarkdown("1. first\n2. second");
  assert.ok(!html.includes("start="), html);
});

// `start` is an attribute built from source text. Injection into it is
// structurally impossible — a block is only treated as an ordered list
// when EVERY line matches `\d+[.)]`, so the value is digits by the time it
// is read — but the value is still bounded, because an ordinal that is not
// a step number is a typo and rendering it as one hides the typo.
test("only a plausible ordinal reaches the start attribute", () => {
  for (const odd of ["99999", "0"]) {
    const html = renderMarkdown(`${odd}. item\n2. next`);
    assert.ok(!html.includes("start="), `${odd} produced ${html}`);
  }
  assert.ok(renderMarkdown("9999. item").includes('start="9999"'), "a four-digit step is still a step");
});

test("no ordered list can emit a non-numeric start", () => {
  for (const hostile of ['1" onload="alert(1)', "1.5", "-1"]) {
    const html = renderMarkdown(`${hostile}. item\n2. next`);
    assert.ok(!/start="[^"]*\D/.test(html), `${hostile} produced ${html}`);
    assert.ok(!/<[a-z]+[^>]*\son\w+=/i.test(html), "no element carries an event handler");
  }
});

test("headings render as emphasised lines, not document headings", () => {
  const html = renderMarkdown("## Section\n\nbody");
  assert.ok(html.includes("Section"));
  assert.ok(!html.includes("<h2"), "task text must not inject document structure");
});

test("a single newline is a wrap; a blank line starts a paragraph", () => {
  const html = renderMarkdown("one\ntwo\n\nthree");
  assert.equal((html.match(/<p/g) ?? []).length, 2);
  assert.ok(html.includes("one two"));
});

test("empty input renders nothing", () => {
  assert.equal(renderMarkdown(""), "");
  assert.equal(renderMarkdown("   \n  "), "");
});

// The placeholder is a SECOND channel that escaping-first does not
// cover: code spans are extracted before the link rule writes an href
// and expanded back afterwards, so a target that swallows a placeholder
// would have raw markup substituted into an attribute value. A backtick
// pair straddling the link target is enough; no NUL in the source is
// required.
test("a code span straddling a link target cannot inject into the href", () => {
  const html = renderMarkdown("[x](https://e.com/`a`)");
  const href = /href="([^"]*)"/.exec(html);
  if (href !== null) {
    assert.ok(!href[1]!.includes("<"), "no markup in the href value");
    assert.ok(!href[1]!.includes("class="), "no attribute smuggled into the href");
  }
  assert.ok(!/<a[^>]*<code/.test(html), "no element opened inside the anchor tag");
  assert.ok(!/<[a-z]+[^>]*\son\w+=/i.test(html), "no element carries an event handler");
});

// A NUL is legal in JSON, so task text really can carry one: the
// placeholder's delimiter must be enforced rather than assumed absent.
test("a forged placeholder in the source cannot reach the output", () => {
  const NUL = String.fromCharCode(0);
  const html = renderMarkdown(`[x](https://e.com/${NUL}0${NUL}) and ${NUL}0${NUL} alone`);
  assert.ok(!html.includes(NUL), "no NUL survives into the output");
  assert.ok(!/<a[^>]*<code/.test(html), "no element opened inside the anchor tag");
  const href = /href="([^"]*)"/.exec(html);
  if (href !== null) assert.ok(!href[1]!.includes("<"), "no markup in the href value");
});

// The guard must not cost ordinary links their query strings.
test("an ampersand-bearing link still round-trips", () => {
  const html = renderMarkdown("[q](https://example.com/s?a=1&b=2)");
  assert.ok(html.includes('href="https://example.com/s?a=1&amp;b=2"'), html);
});

// Stripping NUL at the door is what stops a FORGED placeholder from
// expanding: without it, a source-supplied delimiter pair matching a
// real code span's index re-emits that span somewhere the author never
// wrote it. Not markup injection — the expansion is this module's own
// fixed template — but task text that rewrites itself is its own
// defect, and the delimiter must be enforced rather than assumed.
test("a forged placeholder cannot re-emit a real code span", () => {
  const NUL = String.fromCharCode(0);
  const html = renderMarkdown(`\`real\` then ${NUL}0${NUL}`);
  assert.equal((html.match(/<code/g) ?? []).length, 1, "the code span appears exactly once");
  assert.ok(!html.includes(NUL));
});

test("without a known set, task references render as plain text", () => {
  const html = renderMarkdown("See Task 126 for context.");
  assert.ok(!html.includes("data-task-ref"), html);
  assert.ok(html.includes("Task 126"));
});

test("a resolvable reference becomes a focusable span carrying its id", () => {
  const html = renderMarkdown("See Task 126 for context.", new Set(["126"]));
  assert.ok(html.includes('data-task-ref="126"'), html);
  assert.ok(html.includes("Task 126"), "the visible text survives");
  assert.ok(html.includes('tabindex="0"'), "it is keyboard reachable");
  assert.ok(html.includes('aria-label="Task 126"'));
  assert.ok(!html.includes('role="button"'), "an informational tooltip must not imply activation");
});

test("the task-dash form also linkifies", () => {
  const html = renderMarkdown("blocked by task-9", new Set(["9"]));
  assert.ok(html.includes('data-task-ref="9"'), html);
});

test("an unknown id fails closed as plain text", () => {
  const html = renderMarkdown("See Task 999 for context.", new Set(["126"]));
  assert.ok(!html.includes("data-task-ref"), html);
  assert.ok(html.includes("Task 999"));
});

test("prose like 'task 3 of 7' linkifies only when 3 is a real id", () => {
  const known = new Set(["3"]);
  const resolved = renderMarkdown("task 3 of 7 remaining", known);
  assert.ok(resolved.includes('data-task-ref="3"'));
  const unknown = renderMarkdown("task 3 of 7 remaining", new Set(["7"]));
  assert.ok(!unknown.includes("data-task-ref"), "3 is not in the cache, so nothing linkifies");
});

test("a reference cannot smuggle markup through the id attribute", () => {
  const hostile = `Task 126" onload="alert(1)<img src=x onerror=alert(2)>`;
  const html = renderMarkdown(hostile, new Set(["126"]));
  assert.ok(!/<[a-z]+[^>]*\son\w+=/i.test(html), "no element may carry an event handler");
  assert.ok(!html.includes("<img"), "no img tag survives");
  const idAttr = /data-task-ref="([^"]*)"/.exec(html);
  assert.ok(idAttr !== null);
  assert.equal(idAttr![1], "126");
});

test("markup shaped like a reference stays escaped", () => {
  const html = renderMarkdown('<task-1 x=">"> wild', new Set(["1"]));
  assert.ok(!/<task/i.test(html), "no custom element reaches the DOM");
  assert.ok(!/<[a-z]+[^>]*\son\w+=/i.test(html), "no element carries an event handler");
});

test("a reference inside inline code is not linkified", () => {
  const html = renderMarkdown("run `Task 126` now", new Set(["126"]));
  assert.ok(!html.includes("data-task-ref"), "code spans are protected");
  assert.ok(html.includes("<code"), "the code span still renders");
});

test("a reference survives surrounding emphasis", () => {
  const html = renderMarkdown("**see Task 126 now**", new Set(["126"]));
  assert.ok(html.includes("<strong"), "the bold still applies");
  assert.ok(html.includes('data-task-ref="126"'), "the reference inside bold linkifies");
  assert.ok(/<strong[^>]*>[^<]*<span data-task-ref/.test(html), html);
});

test("task-shaped text inside a markdown link is left to the link renderer", () => {
  const label = renderMarkdown("[Task 126](https://example.com)", new Set(["126"]));
  assert.ok(label.includes('<a href="https://example.com"'));
  assert.ok(!label.includes("data-task-ref"));

  const target = renderMarkdown("[docs](https://example.com/task-126)", new Set(["126"]));
  assert.ok(target.includes('<a href="https://example.com/task-126"'));
  assert.ok(!target.includes("data-task-ref"));
});

// --- Tutorial link schemes ---

const TUTORIAL_ACTIONS: ReadonlySet<string> = new Set(["git-identity", "add-project"]);

test("lesson: and action: links are plain text unless the caller opts in", () => {
  for (const source of ["[next](lesson:lesson-2)", "[sign in](action:git-identity)"]) {
    const html = renderMarkdown(source);
    assert.ok(!html.includes("<button"), html);
    assert.ok(!html.includes("data-tutorial-action"), html);
    assert.ok(!html.includes("<a "), html);
    assert.ok(html.includes(source), "the link survives as visible text");
  }
});

test("opted-in tutorial links become buttons and never carry an href", () => {
  const lesson = renderMarkdown("[Next lesson](lesson:lesson-2)", undefined, TUTORIAL_ACTIONS);
  assert.ok(lesson.includes('<button type="button" data-tutorial-action="lesson" data-lesson="lesson-2"'), lesson);
  const action = renderMarkdown("Commits as [Set a Git identity…](action:git-identity)", undefined, TUTORIAL_ACTIONS);
  assert.ok(action.includes('<button type="button" data-tutorial-action="git-identity"'), action);
  assert.ok(action.includes(">Set a Git identity…</button>"), action);
  for (const html of [lesson, action]) {
    assert.ok(!html.includes("href"), html);
    assert.ok(!html.includes("<a "), html);
  }
});

test("opted-in tutorial links reject names outside the allowlist and malformed ids", () => {
  for (const source of [
    "[x](action:close)",
    "[x](action:next)",
    "[x](action:Git-identity)",
    "[x](lesson:Lesson_2)",
    '[x](lesson:a"onclick="alert(1))',
    "[x](lesson:)",
  ]) {
    const html = renderMarkdown(source, undefined, TUTORIAL_ACTIONS);
    assert.ok(!html.includes("<button"), `${source} -> ${html}`);
    assert.ok(!/<[a-z]+[^>]*\son\w+=/i.test(html), html);
  }
});

test("opted-in rendering still escapes labels and still refuses javascript: targets", () => {
  const html = renderMarkdown("[<img src=x onerror=alert(1)>](lesson:lesson-2) [y](javascript:alert(1))", undefined, TUTORIAL_ACTIONS);
  assert.ok(!html.includes("<img"), html);
  assert.ok(!html.includes("<a "), html);
  assert.ok(html.includes("&lt;img"), html);
});
