import type { LessonCall } from "./lessons.ts";

// What a step's `advance_on` can be asked. Every fact comes from state Deck
// already holds: an API answer, a selection, a callback it already fires.
// None of it is read from terminal output or model text.

export interface EventContext {
  lesson: string;
  step: number;
  // The page loaded fresh onto this step from the saved record.
  restored: boolean;
}

// Facts that happen once and are remembered, in order: the sign-in
// finished, a prompt was typed, a file was opened. Every other event
// describes how things stand now.
const LATCHED: ReadonlySet<string> = new Set([
  "session_signed_in", "input_sent", "file_opened", "project_opened",
]);

// A typed prompt belongs to the step that offered it. The same event in
// another lesson, or on another step, is not the same prompt.
const SCOPED: ReadonlySet<string> = new Set(["input_sent"]);

// Deck has no supported way to read the exit status of a command it typed:
// shell sessions run a bare shell with no prompt markers, and tmux reports
// an exit status only for the whole pane process. Whether an agent is working
// is provider state, and Deck takes it from no hook that exists, never from
// terminal output. These steps stay on Continue.
const NOT_OBSERVED: ReadonlySet<string> = new Set(["check_exited", "session_busy"]);

export function eventKey(call: LessonCall): string {
  return call.arg === null ? call.name : `${call.name}(${call.arg})`;
}

export function isLatched(call: LessonCall): boolean {
  return LATCHED.has(call.name);
}

export interface EventFacts {
  // Zero when the event has not happened. A latched event answers the
  // sequence number it was noted at; any other answers 1 while it holds.
  value(call: LessonCall, context: EventContext): number;
}

// `a then b`: each event must hold, and a latched event must have been
// noted after every event before it. A prompt that follows
// `session_attached(project=…)` is the one typed into that project.
export function sequenceDone(events: readonly LessonCall[], facts: EventFacts, context: EventContext): boolean {
  let after = 0;
  let project: string | null = null;
  for (const call of events) {
    if (call.name === "session_attached") project = call.arg;
    const asked = call.name === "input_sent" && call.arg === null && project !== null ? { name: call.name, arg: project } : call;
    const value = facts.value(asked, context);
    if (value === 0) return false;
    if (isLatched(call) && value <= after) return false;
    after = Math.max(after, value);
  }
  return true;
}

// The one control that moves a step on: Continue for explanation and for a
// check Deck cannot observe, Skip step for a step that waits on something
// the user does.
export type StepControl = "continue" | "skip";

export function stepControl(events: readonly LessonCall[] | undefined): StepControl {
  if (events === undefined || events.length === 0) return "continue";
  if (events.some((call) => call.name === "continue" || NOT_OBSERVED.has(call.name))) return "continue";
  return "skip";
}

// The facts that have been noted since the page loaded, with the order they
// arrived in; everything else is asked of `current`. A state that holds is
// given the order it began to hold in, so a fact noted before its
// prerequisite began cannot stand in for what came after.
export class FactBook implements EventFacts {
  private counter = 0;
  private readonly noted = new Map<string, number>();
  private readonly began = new Map<string, number>();
  current: (call: LessonCall, context: EventContext) => boolean = () => false;

  private key(call: LessonCall, context: EventContext): string {
    const key = eventKey(call);
    return SCOPED.has(call.name) ? `${key}@${context.lesson}:${context.step}` : key;
  }

  // scope names the lesson step a scoped fact belongs to.
  note(scope: { lesson: string; step: number } | null, ...calls: LessonCall[]): void {
    const seq = ++this.counter;
    for (const call of calls) {
      this.noted.set(this.key(call, { lesson: scope?.lesson ?? "", step: scope?.step ?? 0, restored: false }), seq);
    }
  }

  // Records which of these states hold now, so that their order is known
  // from when they began and not from when they were first asked about.
  sample(calls: readonly LessonCall[], context: EventContext): void {
    for (const call of calls) this.value(call, context);
  }

  value(call: LessonCall, context: EventContext): number {
    const key = this.key(call, context);
    if (isLatched(call)) return this.noted.get(key) ?? 0;
    if (!this.current(call, context)) {
      this.began.delete(key);
      return 0;
    }
    let seq = this.began.get(key);
    if (seq === undefined) {
      seq = ++this.counter;
      this.began.set(key, seq);
    }
    return seq;
  }
}
