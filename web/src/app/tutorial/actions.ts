import { LESSON_ACTIONS, type LessonCall } from "./lessons.ts";

// One table from a lesson action name to what Deck does for it. Text-bearing
// actions receive the step's already-substituted prompt or command.
export interface TutorialActionHost {
  // Reports the outcome of an action in the panel.
  say(message: string): void;
  chooseAgent(): void;
  newSession(): void;
  addProject(): void;
  gitIdentity(): void;
  // Types the prompt into an attached agent console. `target` names the
  // project whose agent session receives it (relnotes, ccparse or lane);
  // null means the selected session. Never presses Enter.
  insertPrompt(text: string, target: string | null): void;
  // Types the command into a plain shell session of the current project,
  // creating that session if none is selected. `target` names the project
  // whose shell is used, as for prompts; null means the selected project.
  // Never presses Enter.
  runCommand(text: string, target: string | null): void;
  uploadFiles(): void;
  previewSettings(): void;
  openPreview(): void;
  showAddress(): void;
  // Creates the two practice projects and starts the picked agent in relnotes.
  createStarter(): void;
  // Adds the starter PRD to relnotes when it is missing.
  addPrd(): void;
  // Adds the PRD if missing, then generates exactly this many tasks. A null
  // count means the user's own project: generate with the default count from
  // the Tasks panel's PRD, or open its Add PRD flow when it has none.
  generateTasks(count: number | null): void;
  // Adds the task for one requirement of the relnotes PRD by hand. A null
  // requirement opens the Tasks panel's Add task form on the user's project.
  addTask(requirement: number | null): void;
  // Starts the picked agent in one of the practice projects.
  startSession(project: string): void;
  // Opens a lane for the next open task of the user's project and starts the
  // picked agent in it.
  startLane(): void;
  // Starts a second agent in the lane and types a review prompt for it.
  requestReview(): void;
  // Marks the lesson's task done, closing its lane when it has one.
  markTaskDone(): void;
}

// The one numeric or name argument a lesson call or button carries, as
// `key=value`.
function argValue(arg: string | null, key: string): string | null {
  const prefix = `${key}=`;
  return arg !== null && arg.startsWith(prefix) ? arg.slice(prefix.length) : null;
}

type Handler = (host: TutorialActionHost, text: string, arg: string | null) => void;

const ACTION_TABLE: Readonly<Record<string, Handler>> = {
  "choose-agent": (host) => host.chooseAgent(),
  "new-session": (host) => host.newSession(),
  "add-project": (host) => host.addProject(),
  "git-identity": (host) => host.gitIdentity(),
  "insert-prompt": (host, text, arg) => host.insertPrompt(text, argValue(arg, "target")),
  "run-command": (host, text, arg) => host.runCommand(text, argValue(arg, "target")),
  "upload-files": (host) => host.uploadFiles(),
  "preview-settings": (host) => host.previewSettings(),
  "open-preview": (host) => host.openPreview(),
  "show-address": (host) => host.showAddress(),
  "create-starter": (host) => host.createStarter(),
  "add-prd": (host) => host.addPrd(),
  "generate-tasks": (host, _text, arg) => {
    const given = argValue(arg, "count");
    if (given === null) {
      host.generateTasks(null);
      return;
    }
    const count = Number(given);
    if (Number.isInteger(count) && count >= 1 && count <= 50) host.generateTasks(count);
  },
  "add-task": (host, _text, arg) => {
    const given = argValue(arg, "requirement");
    if (given === null) {
      host.addTask(null);
      return;
    }
    const requirement = Number(given);
    if (Number.isInteger(requirement) && requirement >= 1) host.addTask(requirement);
  },
  "start-session": (host, _text, arg) => {
    const project = argValue(arg, "project");
    if (project !== null) host.startSession(project);
  },
  "start-lane": (host) => host.startLane(),
  "request-review": (host) => host.requestReview(),
  "mark-task-done": (host) => host.markTaskDone(),
};

// Actions the table runs today. The rest render as disabled buttons.
export const WIRED_ACTIONS: ReadonlySet<string> = new Set(Object.keys(ACTION_TABLE));

export const DEFERRED_ACTIONS: ReadonlySet<string> = new Set([...LESSON_ACTIONS].filter((name) => !WIRED_ACTIONS.has(name)));

// Whether a button for this call does anything. Starting a session needs the
// project it names.
export function actionWired(call: LessonCall): boolean {
  if (!WIRED_ACTIONS.has(call.name)) return false;
  return call.name !== "start-session" || argValue(call.arg, "project") !== null;
}

export const DEFERRED_REASON = "Available in a later update";

// Runs the named action; false when the table has no entry for it.
export function runTutorialAction(host: TutorialActionHost, name: string, text: string, arg: string | null = null): boolean {
  const handler = Object.hasOwn(ACTION_TABLE, name) ? ACTION_TABLE[name] : undefined;
  if (handler === undefined) return false;
  handler(host, text, arg);
  return true;
}
