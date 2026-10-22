// Strict parser for the tutorial lesson files under lessons/. The
// grammar is specified in that directory's README; anything the README does
// not allow is rejected with the file and line, so a bad lesson fails the
// unit tests instead of reaching a user.

export class LessonParseError extends Error {
  readonly file: string;
  readonly line: number;

  constructor(file: string, line: number, message: string) {
    super(`${file}:${line}: ${message}`);
    this.name = "LessonParseError";
    this.file = file;
    this.line = line;
  }
}

// A named reference with at most one argument, such as
// `session_attached(project=ccparse)` or `agent_cannot_generate_tasks`.
export interface LessonCall {
  name: string;
  arg: string | null;
}

export type LessonGroup = "course" | "explore";
export type LessonPanel = "tasks" | "files" | "chats";

export interface StepSettings {
  // Events that complete the step, in order. `continue` stands alone.
  advanceOn?: readonly LessonCall[];
  when?: LessonCall;
  primary?: string;
  action?: LessonCall;
  thenPrimary?: string;
  thenAction?: LessonCall;
  fallback?: string;
  fallbackAction?: LessonCall;
  fallbackWhen?: LessonCall;
  fallbackPrimaryWhen?: LessonCall;
  prompt?: string;
  // The project whose session receives the prompt or command: a practice
  // project by name, `lane` for the lane of the user's own task, or `source`
  // for the user's own project the lane was made from.
  target?: string;
  command?: string;
  commandNote?: string;
  passText?: string;
  failText?: string;
  generateCount?: number;
  opens?: LessonPanel;
  busyText?: string;
  primaryNote?: string;
  finePrint?: string;
  footer?: string;
  note?: string;
  noteWhen?: LessonCall;
}

export interface LessonStep extends StepSettings {
  title: string;
  body: string;
}

export interface ParsedLesson {
  id: string;
  title: string;
  group: LessonGroup;
  duration: string;
  requires: readonly string[];
  principles: readonly number[];
  steps: readonly LessonStep[];
  // Not every lesson closes with a line and a next lesson.
  done: { next: string; body: string } | null;
}

export const LESSON_REQUIREMENTS: ReadonlySet<string> = new Set([
  "agent-chat", "two-sessions", "github", "claude", "previews", "mempalace", "reachable-install", "review-app",
]);

// Each name maps to the argument forms it accepts: "" for none, `key=*`
// for a keyed value, or a literal flag.
type ArgForms = readonly string[];

const EVENTS: Readonly<Record<string, ArgForms>> = {
  continue: [""],
  agent_picked: [""],
  starter_created: [""],
  session_signed_in: [""],
  session_attached: ["project=*"],
  task_exists: ["", "requirement=*"],
  input_sent: ["", "project=*"],
  session_busy: [""],
  file_opened: ["modified"],
  check_exited: [""],
  task_done: [""],
  project_opened: ["source=github"],
  lane_session_attached: [""],
  review_seen: [""],
  chat_message_seen: [""],
  page_reloaded: ["session_live"],
};

const ACTIONS: Readonly<Record<string, ArgForms>> = {
  "add-prd": [""],
  "generate-tasks": [""],
  "add-task": ["", "requirement=*"],
  "add-project": [""],
  "choose-agent": [""],
  "create-starter": [""],
  "git-identity": [""],
  "insert-prompt": [""],
  "mark-task-done": [""],
  "new-session": [""],
  "open-preview": [""],
  "preview-settings": [""],
  "request-review": [""],
  "run-command": [""],
  "show-address": [""],
  "start-lane": [""],
  "start-session": ["project=*"],
  "upload-files": [""],
};

export const LESSON_ACTIONS: ReadonlySet<string> = new Set(Object.keys(ACTIONS));

const CONDITIONS: Readonly<Record<string, Readonly<Record<string, ArgForms>>>> = {
  when: { agent_needs_login: [""], starter_exists: [""] },
  fallback_when: { no_git_identity: [""], no_task: ["requirement=*"] },
  fallback_primary_when: { agent_cannot_generate_tasks: [""] },
  note_when: { no_review_app: [""] },
};

export const LESSON_CONDITIONS: ReadonlySet<string> = new Set(Object.values(CONDITIONS).flatMap(Object.keys));

const FRONT_MATTER_KEYS = new Set(["id", "title", "group", "duration", "requires", "principles"]);

type SettingKind = "text" | "block" | "events" | "action" | "condition" | "count" | "panel" | "target";

const SETTINGS: Readonly<Record<string, { field: keyof StepSettings; kind: SettingKind }>> = {
  advance_on: { field: "advanceOn", kind: "events" },
  when: { field: "when", kind: "condition" },
  primary: { field: "primary", kind: "text" },
  action: { field: "action", kind: "action" },
  then_primary: { field: "thenPrimary", kind: "text" },
  then_action: { field: "thenAction", kind: "action" },
  fallback: { field: "fallback", kind: "text" },
  fallback_action: { field: "fallbackAction", kind: "action" },
  fallback_when: { field: "fallbackWhen", kind: "condition" },
  fallback_primary_when: { field: "fallbackPrimaryWhen", kind: "condition" },
  prompt: { field: "prompt", kind: "block" },
  target: { field: "target", kind: "target" },
  command: { field: "command", kind: "text" },
  command_note: { field: "commandNote", kind: "text" },
  pass_text: { field: "passText", kind: "text" },
  fail_text: { field: "failText", kind: "text" },
  generate_count: { field: "generateCount", kind: "count" },
  opens: { field: "opens", kind: "panel" },
  busy_text: { field: "busyText", kind: "text" },
  primary_note: { field: "primaryNote", kind: "text" },
  fine_print: { field: "finePrint", kind: "text" },
  footer: { field: "footer", kind: "text" },
  note: { field: "note", kind: "text" },
  note_when: { field: "noteWhen", kind: "condition" },
};

export const PROMPT_TARGETS: ReadonlySet<string> = new Set(["relnotes", "ccparse", "lane", "source"]);

const PLACEHOLDERS = new Set(["agent", "task.title", "task.description", "starter.relnotes"]);
const LINK_TARGET = /^(?:lesson:([a-z0-9-]+)|action:([a-z][a-z-]*))$/;

interface Line {
  text: string;
  number: number;
}

export function parseLesson(file: string, source: string): ParsedLesson {
  const fail = (line: number, message: string): never => {
    throw new LessonParseError(file, line, message);
  };
  const lines: Line[] = source.replace(/\r\n?/g, "\n").split("\n").map((text, index) => ({ text, number: index + 1 }));
  // A trailing newline leaves one empty element that is not a line of the file.
  if (lines.length > 0 && lines[lines.length - 1]!.text === "") lines.pop();

  const front = parseFrontMatter(lines, fail);
  let cursor = front.next;
  while (cursor < lines.length && lines[cursor]!.text.trim() === "") cursor++;
  const heading = lines[cursor];
  if (heading === undefined || !heading.text.startsWith("# ")) fail(heading?.number ?? lines.length, "expected the `# title` line after the front-matter");
  if (heading!.text.slice(2).trim() !== front.title) {
    fail(heading!.number, `the # title must match the front-matter title "${front.title}"`);
  }
  cursor++;

  const sections: { title: string; line: number; lines: Line[] }[] = [];
  for (; cursor < lines.length; cursor++) {
    const line = lines[cursor]!;
    if (line.text.startsWith("## ")) {
      const title = line.text.slice(3).trim();
      if (title === "") fail(line.number, "a step needs a title");
      sections.push({ title, line: line.number, lines: [] });
    } else if (line.text.startsWith("# ")) {
      fail(line.number, "only one `#` title is allowed");
    } else if (sections.length === 0) {
      if (line.text.trim() !== "") fail(line.number, "text before the first step");
    } else {
      sections[sections.length - 1]!.lines.push(line);
    }
  }
  if (sections.length === 0) fail(heading!.number, "the lesson has no steps");

  const steps: LessonStep[] = [];
  let done: { next: string; body: string } | null = null;
  for (const [index, section] of sections.entries()) {
    const { settings, body, bodyLine } = parseSection(section.lines, fail);
    if (section.title === "done") {
      if (index !== sections.length - 1) fail(section.line, "`## done` must be the last section");
      done = parseDone(settings, body, section.line, bodyLine, fail);
      continue;
    }
    steps.push(buildStep(section.title, section.line, settings, body, bodyLine, fail));
  }
  if (steps.length === 0) fail(sections[0]!.line, "the lesson has no steps before `## done`");

  return { ...front.meta, steps, done };
}

type Fail = (line: number, message: string) => never;

interface FrontMatter {
  meta: Omit<ParsedLesson, "steps" | "done">;
  title: string;
  next: number;
}

function parseFrontMatter(lines: Line[], fail: Fail): FrontMatter {
  if (lines[0]?.text !== "---") fail(1, "the file must start with a `---` front-matter block");
  const values = new Map<string, { value: string; line: number }>();
  let cursor = 1;
  for (; cursor < lines.length && lines[cursor]!.text !== "---"; cursor++) {
    const line = lines[cursor]!;
    const match = /^([a-z]+): (.*)$/.exec(line.text);
    if (match === null) fail(line.number, "front-matter lines are `key: value`");
    const [, key, value] = match!;
    if (!FRONT_MATTER_KEYS.has(key!)) fail(line.number, `unknown front-matter key "${key}"`);
    if (values.has(key!)) fail(line.number, `duplicate front-matter key "${key}"`);
    if (value!.trim() === "") fail(line.number, `front-matter key "${key}" is empty`);
    values.set(key!, { value: value!.trim(), line: line.number });
  }
  if (cursor >= lines.length) fail(lines.length, "the front-matter block is not closed with `---`");
  const need = (key: string): { value: string; line: number } => {
    const entry = values.get(key);
    if (entry === undefined) fail(cursor + 1, `front-matter is missing "${key}"`);
    return entry!;
  };
  const id = need("id");
  if (!/^[a-z0-9-]{1,40}$/.test(id.value)) fail(id.line, "id is a short lowercase key");
  const title = need("title");
  const group = need("group");
  if (group.value !== "course" && group.value !== "explore") fail(group.line, 'group is "course" or "explore"');
  const duration = need("duration");
  const requires = values.get("requires");
  const requirements = requires === undefined ? [] : parseList(requires, fail);
  for (const requirement of requirements) {
    if (!LESSON_REQUIREMENTS.has(requirement)) fail(requires!.line, `unknown requirement "${requirement}"`);
  }
  const principlesEntry = values.get("principles");
  if (principlesEntry !== undefined && group.value !== "course") fail(principlesEntry.line, "principles belong to course lessons only");
  const principles = principlesEntry === undefined ? [] : parseList(principlesEntry, fail).map((item) => {
    if (!/^[1-9]\d?$/.test(item)) fail(principlesEntry.line, `principle "${item}" is not a number`);
    return Number(item);
  });
  return {
    meta: {
      id: id.value,
      title: title.value,
      group: group.value as LessonGroup,
      duration: duration.value,
      requires: requirements,
      principles,
    },
    title: title.value,
    next: cursor + 1,
  };
}

function parseList(entry: { value: string; line: number }, fail: Fail): string[] {
  const match = /^\[(.*)\]$/.exec(entry.value);
  if (match === null) fail(entry.line, "expected a list such as `[a, b]`");
  const inner = match![1]!.trim();
  if (inner === "") return [];
  return inner.split(",").map((item) => {
    const trimmed = item.trim();
    if (trimmed === "") fail(entry.line, "empty list item");
    return trimmed;
  });
}

interface RawSetting {
  key: string;
  value: string;
  line: number;
}

// The settings are the lines directly under the heading, up to the first
// blank line; the body is everything after it.
function parseSection(lines: Line[], fail: Fail): { settings: RawSetting[]; body: string; bodyLine: number } {
  const settings: RawSetting[] = [];
  let cursor = 0;
  while (cursor < lines.length && lines[cursor]!.text.trim() !== "") {
    const line = lines[cursor]!;
    const match = /^([a-z][a-z_]*):(?: (.*))?$/.exec(line.text);
    if (match === null) fail(line.number, "expected a `key: value` setting; the body starts after a blank line");
    const key = match![1]!;
    let value = (match![2] ?? "").trim();
    if (value === "|") {
      const block: string[] = [];
      while (cursor + 1 < lines.length && lines[cursor + 1]!.text.startsWith("  ")) {
        cursor++;
        block.push(lines[cursor]!.text.slice(2));
      }
      if (block.length === 0) fail(line.number, `"${key}: |" needs lines indented by two spaces`);
      value = block.join("\n");
    } else if (value === "") {
      fail(line.number, `setting "${key}" has no value`);
    }
    settings.push({ key, value, line: line.number });
    cursor++;
  }
  const body = lines.slice(cursor);
  for (const line of body) {
    const key = /^([a-z][a-z_]*):(?:\s|$)/.exec(line.text)?.[1];
    if (key !== undefined && (Object.hasOwn(SETTINGS, key) || key === "next")) {
      fail(line.number, `setting "${key}" appears after the body; settings go directly under the heading`);
    }
  }
  const text = body.map((line) => line.text).join("\n").trim();
  const bodyLine = body.find((line) => line.text.trim() !== "")?.number ?? 0;
  return { settings, body: text, bodyLine };
}

function parseCall(raw: string, table: Readonly<Record<string, ArgForms>>, what: string, line: number, fail: Fail): LessonCall {
  const match = /^([a-z][a-z0-9_-]*)(?:\((.+)\))?$/.exec(raw);
  if (match === null) fail(line, `"${raw}" is not a valid ${what}`);
  const name = match![1]!;
  const arg = match![2] ?? null;
  const forms = Object.hasOwn(table, name) ? table[name] : undefined;
  if (forms === undefined) fail(line, `unknown ${what} "${name}"`);
  const ok = forms!.some((form) => {
    if (form === "") return arg === null;
    if (arg === null) return false;
    if (form.endsWith("=*")) return new RegExp(`^${form.slice(0, -1)}[A-Za-z0-9._-]+$`).test(arg);
    return arg === form;
  });
  if (!ok) fail(line, arg === null ? `${what} "${name}" needs an argument` : `${what} "${name}" does not accept "${arg}"`);
  return { name, arg };
}

function buildStep(title: string, headingLine: number, raw: RawSetting[], body: string, bodyLine: number, fail: Fail): LessonStep {
  const fields: Record<string, unknown> = {};
  for (const { key, value, line } of raw) {
    const spec = Object.hasOwn(SETTINGS, key) ? SETTINGS[key] : undefined;
    if (spec === undefined) fail(line, `unknown setting "${key}"`);
    if (spec!.field in fields) fail(line, `duplicate setting "${key}"`);
    switch (spec!.kind) {
      case "text":
      case "block":
        fields[spec!.field] = value;
        break;
      case "events": {
        const events = value.split(/\s+then\s+/).map((part) => parseCall(part.trim(), EVENTS, "event", line, fail));
        if (events.some((event) => event.name === "continue") && events.length > 1) fail(line, "`continue` cannot be combined with other events");
        fields[spec!.field] = events;
        break;
      }
      case "action":
        fields[spec!.field] = parseCall(value, ACTIONS, "action", line, fail);
        break;
      case "condition":
        fields[spec!.field] = parseCall(value, CONDITIONS[key]!, "condition", line, fail);
        break;
      case "count":
        if (!/^[1-9]\d?$/.test(value)) fail(line, "generate_count is a whole number from 1 to 99");
        fields[spec!.field] = Number(value);
        break;
      case "target":
        if (!PROMPT_TARGETS.has(value)) fail(line, `target is ${[...PROMPT_TARGETS].join(", ")}, not "${value}"`);
        fields[spec!.field] = value;
        break;
      case "panel":
        if (value !== "tasks" && value !== "files" && value !== "chats") fail(line, `opens is tasks, files or chats, not "${value}"`);
        fields[spec!.field] = value;
        break;
    }
  }
  const step = { title, body, ...fields } as LessonStep;
  validateStep(step, headingLine, bodyLine, raw, fail);
  return step;
}

// Pairs of settings that only make sense together, checked after parsing so
// the error can name the step.
function validateStep(step: LessonStep, headingLine: number, bodyLine: number, raw: RawSetting[], fail: Fail): void {
  const lineOf = (key: string): number => raw.find((setting) => setting.key === key)?.line ?? headingLine;
  const requires: [boolean, string, string][] = [
    [step.primary !== undefined, "primary", "action"],
    [step.thenPrimary !== undefined, "then_primary", "then_action"],
    [step.thenAction !== undefined, "then_action", "then_primary"],
    [step.fallbackWhen !== undefined, "fallback_when", "fallback"],
    [step.fallbackPrimaryWhen !== undefined, "fallback_primary_when", "fallback"],
    [step.passText !== undefined, "pass_text", "command"],
    [step.failText !== undefined, "fail_text", "command"],
    [step.commandNote !== undefined, "command_note", "command"],
    [step.noteWhen !== undefined, "note_when", "note"],
    [step.primaryNote !== undefined, "primary_note", "primary"],
  ];
  const present = (key: string): boolean => raw.some((setting) => setting.key === key);
  for (const [applies, key, partner] of requires) {
    if (applies && !present(partner)) fail(lineOf(key), `"${key}" needs "${partner}" in the same step`);
  }
  if (step.action !== undefined && step.primary === undefined) fail(lineOf("action"), '"action" needs "primary" in the same step');
  if (step.fallbackAction !== undefined && step.fallback === undefined) fail(lineOf("fallback_action"), '"fallback_action" needs "fallback"');
  if (step.generateCount !== undefined && step.action?.name !== "generate-tasks") fail(lineOf("generate_count"), "generate_count belongs to a generate-tasks action");
  if (step.prompt !== undefined && ![step.action, step.thenAction].some((action) => action?.name === "insert-prompt")) {
    fail(lineOf("prompt"), '"prompt" needs an insert-prompt action in the same step');
  }
  if (step.target !== undefined && ![step.action, step.thenAction].some((action) => action?.name === "insert-prompt" || action?.name === "run-command")) {
    fail(lineOf("target"), '"target" needs an insert-prompt or run-command action in the same step');
  }
  const texts: [string | undefined, number][] = [
    [step.title, headingLine],
    [step.body, bodyLine],
    [step.prompt, lineOf("prompt") + 1],
    [step.command, lineOf("command")],
    [step.primary, lineOf("primary")],
    [step.fallback, lineOf("fallback")],
    [step.thenPrimary, lineOf("then_primary")],
    [step.primaryNote, lineOf("primary_note")],
    [step.finePrint, lineOf("fine_print")],
    [step.footer, lineOf("footer")],
    [step.note, lineOf("note")],
    [step.commandNote, lineOf("command_note")],
    [step.passText, lineOf("pass_text")],
    [step.failText, lineOf("fail_text")],
    [step.busyText, lineOf("busy_text")],
  ];
  for (const [value, line] of texts) if (value !== undefined) checkText(value, line, fail);
}

// Every `{...}` must be a known placeholder and every link must use the
// lesson: or action: scheme (or an absolute https URL).
// `line` is where the text starts; a match reports the line it sits on.
function checkText(text: string, line: number, fail: Fail): void {
  const at = (index: number): number => line + (text.slice(0, index).match(/\n/g)?.length ?? 0);
  for (const match of text.matchAll(/\{([^{}\s]+)\}/g)) {
    if (!PLACEHOLDERS.has(match[1]!)) fail(at(match.index), `unknown placeholder {${match[1]}}`);
  }
  for (const match of text.matchAll(/\[([^\]]*)\]\(([^)]*)\)/g)) {
    const target = match[2]!;
    if (/^https:\/\//.test(target)) continue;
    const parsed = LINK_TARGET.exec(target);
    if (parsed === null) fail(at(match.index), `link target "${target}" must use the lesson: or action: scheme`);
    if (parsed![2] !== undefined && !LESSON_ACTIONS.has(parsed![2])) fail(at(match.index), `link names unknown action "${parsed![2]}"`);
  }
}

function parseDone(settings: RawSetting[], body: string, line: number, bodyLine: number, fail: Fail): { next: string; body: string } {
  let next: string | null = null;
  for (const setting of settings) {
    if (setting.key !== "next") fail(setting.line, `"${setting.key}" is not allowed in the done section; it has only "next"`);
    if (next !== null) fail(setting.line, 'duplicate setting "next"');
    if (!/^[a-z0-9-]{1,40}$/.test(setting.value)) fail(setting.line, '"next" names a lesson id or all-lessons');
    next = setting.value;
  }
  if (next === null) fail(line, 'the done section needs a "next" setting');
  if (body === "") fail(line, "the done section needs a closing line");
  checkText(body, bodyLine, fail);
  return { next: next!, body };
}
