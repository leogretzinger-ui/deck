import type { SessionSummary } from "../api/sessions.ts";

export type ActivityEdge = { session_id: string; generation: number };

type Clock = {
  setTimeout(callback: () => void, delay: number): number;
  clearTimeout(timer: number): void;
};

// Content-free output edges temporarily replace a row's metadata with a
// spinner. Session generation fences stale events, and each session owns an
// independent silence window so concurrent output cannot interfere.
export class OutputActivityWindows {
  private timers = new Map<string, number>();
  private readonly sessions: () => SessionSummary[];
  private readonly change: (sessionID: string, active: boolean) => void;
  private readonly clock: Clock;
  private readonly silenceMS: number;

  constructor(
    sessions: () => SessionSummary[],
    change: (sessionID: string, active: boolean) => void,
    clock: Clock = window,
    silenceMS = 1500,
  ) {
    this.sessions = sessions;
    this.change = change;
    this.clock = clock;
    this.silenceMS = silenceMS;
  }

  ingest(edge: ActivityEdge): void {
    const session = this.sessions().find((candidate) => candidate.id === edge.session_id);
    if (session === undefined || session.status !== "running" || session.generation !== edge.generation) return;
    this.mark(edge.session_id);
  }

  mark(sessionID: string): void {
    this.change(sessionID, true);
    const previous = this.timers.get(sessionID);
    if (previous !== undefined) this.clock.clearTimeout(previous);
    const timer = this.clock.setTimeout(() => {
      this.timers.delete(sessionID);
      this.change(sessionID, false);
    }, this.silenceMS);
    this.timers.set(sessionID, timer);
  }

  clear(sessionID: string): void {
    const timer = this.timers.get(sessionID);
    if (timer === undefined) return;
    this.clock.clearTimeout(timer);
    this.timers.delete(sessionID);
    this.change(sessionID, false);
  }

  reconcile(): void {
    const current = new Map(this.sessions().map((session) => [session.id, session]));
    for (const id of [...this.timers.keys()]) {
      const session = current.get(id);
      if (session === undefined || session.status !== "running") this.clear(id);
    }
  }

  dispose(): void {
    for (const id of [...this.timers.keys()]) this.clear(id);
  }
}
