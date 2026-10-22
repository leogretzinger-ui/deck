import { LessonParseError, parseLesson, type ParsedLesson } from "./lessons.ts";
import { WELCOMED, type TutorialLesson } from "./model.ts";

// Lesson files by name, in the order the panel lists them. A file that is
// not named here, or a name with no file, is an error: ordering is a
// decision, not a directory listing.
export const COURSE_LESSON_FILES: readonly string[] = [
  "lesson-1-your-first-task",
  "lesson-2-two-agents-one-bug",
  "lesson-3-your-own-code",
];

export const EXPLORE_LESSON_FILES: readonly string[] = [
  "a-long-job-while-im-away",
  "a-pull-request-that-needs-a-reviewer",
  "several-sessions-at-once",
  "files-and-uploads",
  "tasks-from-a-brief",
  "seeing-the-app-im-building",
  "what-my-agents-remember",
  "when-the-connection-drops",
  "two-logins-two-identities",
  "making-it-mine",
  "what-an-agent-can-reach",
  "core-principles",
];

export const ALL_LESSONS = "all-lessons";
export const DONE_STEP_TITLE = "Done";

function basename(path: string): string {
  return path.slice(path.lastIndexOf("/") + 1).replace(/\.md$/, "");
}

// Builds the catalog from lesson file contents keyed by path. It is the
// whole of the loading logic, so node tests can run it on files read from
// disk while the app feeds it the bundled copies.
export function buildTutorialCatalog(files: Readonly<Record<string, string>>): TutorialLesson[] {
  const byName = new Map<string, { path: string; text: string }>();
  for (const [path, text] of Object.entries(files)) byName.set(basename(path), { path, text });

  const ordered = [
    ...COURSE_LESSON_FILES.map((name) => ({ name, group: "course" as const })),
    ...EXPLORE_LESSON_FILES.map((name) => ({ name, group: "explore" as const })),
  ];
  const listed = new Set(ordered.map((entry) => entry.name));
  for (const name of byName.keys()) {
    if (!listed.has(name)) throw new Error(`lesson file ${name}.md is not in the catalog order`);
  }

  const parsed: { lesson: ParsedLesson; file: string }[] = [];
  for (const { name, group } of ordered) {
    const entry = byName.get(name);
    if (entry === undefined) throw new Error(`lesson file ${name}.md is missing`);
    const lesson = parseLesson(entry.path, entry.text);
    if (lesson.group !== group) throw new LessonParseError(entry.path, 1, `group is "${lesson.group}" but the catalog lists it under "${group}"`);
    parsed.push({ lesson, file: entry.path });
  }

  const ids = new Set<string>();
  for (const { lesson, file } of parsed) {
    if (ids.has(lesson.id)) throw new LessonParseError(file, 1, `duplicate lesson id "${lesson.id}"`);
    if (lesson.id === WELCOMED) throw new LessonParseError(file, 1, `lesson id "${WELCOMED}" is reserved for the welcome page`);
    ids.add(lesson.id);
  }
  const known = (id: string): boolean => id === ALL_LESSONS || ids.has(id);
  for (const { lesson, file } of parsed) {
    if (lesson.done !== null && !known(lesson.done.next)) {
      throw new LessonParseError(file, 1, `done.next names unknown lesson "${lesson.done.next}"`);
    }
    const texts = [...lesson.steps.flatMap((step) => [step.body, step.fallback, step.footer, step.note, step.finePrint, step.primaryNote]), lesson.done?.body];
    for (const text of texts) {
      for (const match of (text ?? "").matchAll(/\]\(lesson:([a-z0-9-]+)\)/g)) {
        if (!known(match[1]!)) throw new LessonParseError(file, 1, `link names unknown lesson "${match[1]}"`);
      }
    }
  }

  return parsed.map(({ lesson }) => ({
    id: lesson.id,
    title: lesson.title,
    optional: lesson.group === "explore",
    duration: lesson.duration,
    requires: lesson.requires,
    principles: lesson.principles,
    steps: lesson.done === null
      ? lesson.steps
      : [...lesson.steps, { title: DONE_STEP_TITLE, body: lesson.done.body, done: { next: lesson.done.next } }],
  }));
}
