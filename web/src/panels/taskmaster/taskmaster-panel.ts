// The Taskmaster panel's presentation model: task state
// grouped by tag, PRD selection, and the generation lifecycle. DOM-free so
// the logic runs under plain node tests; the DOM shell
// (taskmaster-panel-dom.ts) renders it. The server owns all state — this
// model holds only what the last responses carried.
import { normalizedPriority, taskID } from "../../lib/task-facts.ts";
import type { AddTaskInput, TaskmasterInfo, TmRun, TmTask } from "../../api/tasks.ts";
import { ApiError } from "../../api/http.ts";
import type { ApiClient } from "../../api/contracts.ts";

// PanelState is the panel's visibility lifecycle: loading until the first
// info answer, uninitialized when the server says the project has no task
// state (404 not_taskmaster_project is a state, not an error), ready
// otherwise.
//
// An uninitialized project is offered a board rather than hidden. Task state
// is what a project needs to hold tasks; a PRD is one way to fill a board and
// never a condition of having one, so nothing here asks for a document.
export type PanelState = "loading" | "uninitialized" | "ready";

// PRD_CANONICAL_NAME is the single filename the add-PRD flow stores any
// chosen file under: the user's picked file's own name is
// discarded, so there is one PRD per project and no filename field.
export const PRD_CANONICAL_NAME = "prd.md";

export interface TaskmasterPanelCallbacks {
  onError?: (message: string, cause?: unknown) => void;
}

export class TaskmasterPanelModel {
  state: PanelState = "loading";
  // tags preserves engine task order within each tag; the tag ORDER is
  // master first, then alphabetical — display sorting only.
  tags: [string, TmTask[]][] = [];
  prds: string[] = [];
  selectedPrd: string | null = null;
  activeRun: TmRun | null = null;
  // docsPath is the server-presented documentation directory (absolute)
  // where the add-PRD flow's canonical file lands. Null until detection
  // has answered — the destination note never invents a path.
  docsPath: string | null = null;
  tasksError: string | null = null;
  generateError: string | null = null;
  initializeError: string | null = null;
  // initializing is set SYNCHRONOUSLY so a double click cannot start two
  // initializations against the same project.
  initializing = false;
  addError: string | null = null;
  // generating is set SYNCHRONOUSLY when a generate request starts, so
  // rapid clicks before the first response cannot issue multiple POSTs.
  generating = false;
  adding = false;

  private client: ApiClient;
  private projectID: string;
  private cb: TaskmasterPanelCallbacks;
  private refreshSeq = 0;
  private tasksSeq = 0;
  private prdsRequest: Promise<void> | null = null;

  constructor(client: ApiClient, projectID: string, cb: TaskmasterPanelCallbacks = {}) {
    this.client = client;
    this.projectID = projectID;
    this.cb = cb;
  }

  async refresh(): Promise<void> {
    const seq = ++this.refreshSeq;
    this.docsPath = null;
    let info: TaskmasterInfo;
    try {
      info = await this.client.getTaskmasterInfo(this.projectID);
    } catch (err) {
      if (seq !== this.refreshSeq) return;
      if (err instanceof ApiError && err.code === "not_taskmaster_project") {
        this.state = "uninitialized";
        this.tags = [];
        this.tasksError = null;
        this.prds = [];
        this.selectedPrd = null;
        return;
      }
      // Any other failure leaves the panel ready-but-empty with the
      // error inline: detection flapping must not vanish the task list.
      this.state = "ready";
      this.tasksError = err instanceof Error ? err.message : String(err);
      this.cb.onError?.(this.tasksError, err);
      return;
    }
    if (seq !== this.refreshSeq) return;
    this.state = "ready";
    this.docsPath = info.docs_path;
    await Promise.all([this.refreshTasks(), this.refreshPrds()]);
  }

  async refreshTasks(): Promise<void> {
    const seq = ++this.tasksSeq;
    try {
      const byTag = await this.client.listTaskmasterTasks(this.projectID);
      if (seq !== this.tasksSeq) return;
      this.tags = Object.entries(byTag)
        .map(([tag, file]): [string, TmTask[]] => [tag, qualifySubtaskIDs(file.tasks ?? [])])
        .sort(([a], [b]) => (a === "master" ? -1 : b === "master" ? 1 : a.localeCompare(b)));
      this.tasksError = null;
    } catch (err) {
      if (seq !== this.tasksSeq) return;
      this.tasksError = err instanceof Error ? err.message : String(err);
      this.cb.onError?.(this.tasksError, err);
    }
  }

  async getTask(taskID: string): Promise<TmTask> {
    const task = await this.client.getTaskmasterTask(this.projectID, taskID);
    // A subtask comes back as stored, relative to its parent, so qualify it only
    // when its id is the bare last segment; a stored dotted id is already final.
    const dot = taskID.lastIndexOf(".");
    const relative = dot !== -1 && String(task.id) === taskID.slice(dot + 1);
    return qualifySubtaskIDs([task], relative ? taskID.slice(0, dot) : "")[0]!;
  }

  // addTask delegates identity allocation and mutation to the pinned Tasks
  // engine, then proves success through a fresh canonical read containing
  // the returned identity. A launch/exit/refresh failure remains in-band.
  async addTask(input: AddTaskInput): Promise<boolean> {
    if (this.adding) return false;
    this.adding = true;
    this.addError = null;
    try {
      const created = await this.client.addTaskmasterTask(this.projectID, input);
      await this.refreshTasks();
      if (this.tasksError !== null) {
        this.addError = `task creation was accepted, but refresh failed: ${this.tasksError}`;
        return false;
      }
      const present = this.tags.some(([, tasks]) => hasTaskID(tasks, created.id));
      if (!present) {
        this.addError = `task creation was accepted, but task ${created.id} was not present after refresh`;
        return false;
      }
      return true;
    } catch (err) {
      this.addError = err instanceof Error ? err.message : String(err);
      this.cb.onError?.(this.addError, err);
      return false;
    } finally {
      this.adding = false;
    }
  }

  refreshPrds(fresh = false): Promise<void> {
    if (!fresh && this.prdsRequest !== null) return this.prdsRequest;
    const request = Promise.resolve().then(async () => {
      try {
        const prds = await this.client.listTaskmasterPRDs(this.projectID);
        if (this.prdsRequest !== request) return;
        this.prds = prds;
        if (this.selectedPrd === null || !this.prds.includes(this.selectedPrd)) {
          this.selectedPrd = this.prds[0] ?? null;
        }
      } catch (err) {
        if (this.prdsRequest !== request) return;
        // PRD listing failing must not block the task display; generation
        // simply has nothing to offer.
        this.prds = [];
        this.selectedPrd = null;
        const message = err instanceof Error ? err.message : String(err);
        this.cb.onError?.(message, err);
      }
    }).finally(() => {
      if (this.prdsRequest === request) this.prdsRequest = null;
    });
    this.prdsRequest = request;
    return request;
  }

  selectPrd(name: string): void {
    if (this.prds.includes(name)) {
      this.selectedPrd = name;
    }
  }

  // hasCanonicalPrd is the collision check the add-PRD flow consults before
  // asking the user to confirm a replacement: the canonical
  // prd.md already listed means an upload must be a confirmed replace.
  hasCanonicalPrd(): boolean {
    return this.prds.includes(PRD_CANONICAL_NAME);
  }

  // uploadPrd stores content under the canonical prd.md, then
  // refreshes the list and selects it so Generate is one click away.
  // replace is set only after the user confirmed a collision. Failures
  // surface like generation's: the server's message, in-band.
  async uploadPrd(content: string, replace: boolean): Promise<boolean> {
    try {
      await this.client.uploadPrd(this.projectID, PRD_CANONICAL_NAME, content, replace);
      this.generateError = null;
      // Discard pre-upload listings and await this read even when polling
      // supersedes the general refresh. Polls share the new PRD request.
      const prds = this.refreshPrds(true);
      await Promise.all([this.refresh(), prds]);
      this.selectPrd(PRD_CANONICAL_NAME);
      return true;
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      this.generateError = message;
      this.cb.onError?.(message, err);
      return false;
    }
  }

  // A 409 attaches to the already-running generation rather than treating it
  // as a second failure.
  //
  // initialize creates the task state an unstarted project lacks, then
  // refreshes onto the empty board. The server is idempotent, so a second
  // click settles on the same answer rather than failing.
  async initialize(): Promise<void> {
    if (this.initializing) return;
    this.initializing = true;
    this.initializeError = null;
    try {
      await this.client.initializeTaskmaster(this.projectID);
      await this.refresh();
    } catch (err) {
      this.initializeError = err instanceof Error ? err.message : String(err);
      this.cb.onError?.(this.initializeError, err);
    } finally {
      this.initializing = false;
    }
  }

  // A missing PRD remains an explicit error because generateError controls
  // whether the add-PRD modal closes; a silent return would report success
  // without starting a job.
  async generate(harness?: string): Promise<void> {
    if (this.generating || this.activeRun?.status === "running") {
      return;
    }
    if (this.selectedPrd === null) {
      this.generateError = "no PRD is available to parse — the upload did not register";
      return;
    }
    this.generating = true;
    try {
      this.generateError = null;
      if (harness === undefined) {
        try {
          const sessions = await this.client.listSessions(this.projectID);
          const activity = (session: typeof sessions[number]): number => Math.max(
            Date.parse(session.updated_at) || 0, Date.parse(session.last_output_at ?? "") || 0,
          );
          harness = sessions.sort((a, b) => activity(b) - activity(a))[0]?.provider;
        } catch {
          // The harness is optional; generation proceeds without one.
        }
      }
      try {
        this.activeRun = await this.client.generateTasks(this.projectID, this.selectedPrd, undefined, harness);
      } catch (err) {
        if (err instanceof ApiError && err.code === "generation_running") {
          await this.attachRunning();
          return;
        }
        this.generateError = err instanceof Error ? err.message : String(err);
        this.cb.onError?.(this.generateError, err);
        return;
      }
      if (this.activeRun.status !== "running") {
        // An instantly-finished job: the exit is already the answer, and
        // the task response may have changed, so reload it.
        await this.refreshTasks();
      }
    } finally {
      this.generating = false;
    }
  }

  pollActive(): boolean {
    return this.state === "ready" &&
      this.activeRun !== null && this.activeRun.status === "running";
  }

  // totalTasks counts every task across every tag, including subtasks, so
  // the titlebar badge reflects the full project backlog.
  totalTasks(): number {
    let n = 0;
    const count = (tasks: TmTask[]): void => {
      for (const t of tasks) {
        n += 1;
        if (t.subtasks) count(t.subtasks);
      }
    };
    for (const [, tasks] of this.tags) count(tasks);
    return n;
  }

  private async attachRunning(): Promise<void> {
    try {
      const runs = await this.client.listTaskmasterRuns(this.projectID);
      this.activeRun = runs.find((r) => r.status === "running") ?? null;
      if (this.activeRun === null) {
        // The 409's run finished between the two answers: nothing to
        // attach to, and the tasks are fresh already.
        await this.refreshTasks();
      }
    } catch (err) {
      this.generateError = err instanceof Error ? err.message : String(err);
      this.cb.onError?.(this.generateError, err);
    }
  }

  // pollRun performs ONE poll of the active run; the shell owns the
  // interval. A finished run reloads the authoritative tasks.
  // Overlapping polls are skipped, and a stale response (an older request
  // resolving after a newer one) is discarded: observed state must never
  // regress.
  private polling = false;
  private pollSeq = 0;

  async pollRun(): Promise<void> {
    if (this.activeRun === null || this.activeRun.status !== "running" || this.polling) {
      return;
    }
    this.polling = true;
    const seq = ++this.pollSeq;
    const runID = this.activeRun.id;
    let run: TmRun;
    try {
      run = await this.client.getTaskmasterRun(this.projectID, runID);
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      this.cb.onError?.(message, err);
      this.polling = false;
      return;
    }
    this.polling = false;
    if (seq !== this.pollSeq || this.activeRun === null || this.activeRun.id !== runID) {
      return; // a newer poll or a newer run superseded this response
    }
    this.activeRun = run;
    if (run.status !== "running") {
      await this.refreshTasks();
    }
  }

  // runSummary is the one-line honest rendering of a run: exit
  // status when observed, "unobserved" when not, duration when finished.
  runSummary(run: TmRun): string {
    if (run.status === "running") {
      return "running";
    }
    const parts: string[] = [];
    if (run.exit_code !== null) {
      parts.push(`exited ${run.exit_code}`);
    } else {
      parts.push("exit unobserved");
    }
    if (run.duration_ms !== undefined && run.duration_ms !== null) {
      parts.push(`${(run.duration_ms / 1000).toFixed(1)}s`);
    }
    return parts.join(" · ");
  }
}

export type BoardSortKey = "id" | "status" | "priority";

export function priorityRank(p: string | undefined): number {
  switch (normalizedPriority(p)) {
    case "high":
      return 0;
    case "medium":
      return 1;
    case "low":
      return 2;
    default:
      return 3;
  }
}

// compareTaskIDs is a numeric-aware id compare ("10" after "9", "1.10"
// after "1.9"). Only PLAIN integers compare as numbers: a dotted
// subtask id like "1.10" is segment notation, not a decimal — as a
// Number it would sort before "1.9". Everything else uses a
// numeric-aware string compare (which handles the segments) so the
// order is total and stable.
export function compareTaskIDs(a: unknown, b: unknown): number {
  const as = taskID(a);
  const bs = taskID(b);
  if (/^\d+$/.test(as) && /^\d+$/.test(bs)) return Number(as) - Number(bs);
  return as.localeCompare(bs, undefined, { numeric: true });
}

// boardTasks applies the toolbar's state to one column's tasks —
// display-only, the engine response order is never mutated. Search
// matches title or id, case-insensitive. priorities=null means no
// filter. "status" groups a column's mixed statuses together (To do
// holds pending+blocked, Done holds done/cancelled/deferred), ordered
// by status label then id. sortDesc flips the primary direction; the
// id tiebreak always ascends so the order stays total and stable.
export function boardTasks(
  tasks: TmTask[],
  query: string,
  priorities: ReadonlySet<string> | null,
  sort: BoardSortKey,
  sortDesc = false,
): TmTask[] {
  const q = query.trim().toLowerCase();
  const out = tasks.filter((t) =>
    (q === "" ||
      t.title.toLowerCase().includes(q) ||
      taskID(t.id).toLowerCase().includes(q)) &&
    (priorities === null || priorities.has(normalizedPriority(t.priority)))
  );
  const flip = (n: number): number => (sortDesc ? -n : n);
  if (sort === "id") {
    out.sort((a, b) => flip(compareTaskIDs(a.id, b.id)));
  } else if (sort === "priority") {
    out.sort((a, b) =>
      flip(priorityRank(a.priority) - priorityRank(b.priority)) || compareTaskIDs(a.id, b.id)
    );
  } else {
    out.sort((a, b) =>
      flip(a.status.localeCompare(b.status)) || compareTaskIDs(a.id, b.id)
    );
  }
  return out;
}

// qualifySubtaskIDs rewrites subtask ids to the dotted, parent-qualified
// form the rest of the panel already assumes.
//
// The engine response carries a subtask id RELATIVE to its parent: every
// parent's first subtask is literally `1`. The board flattens subtasks in
// beside top-level tasks, and findTask resolves a click by matching the
// rendered id, so a relative id is both ambiguous on screen and wrong as a
// key — nine parents with subtasks means nine cards reading "#1", all
// resolving to task 1.
//
// Qualifying once on read settles the display, the lookup and the sort
// together: compareTaskIDs already orders "1.10" after "1.9".
//
// This is a display projection. The served response is untouched and task ids
// stay exactly as the engine returned them.
export function qualifySubtaskIDs(tasks: TmTask[], prefix = ""): TmTask[] {
  const qualify = (t: TmTask, prefix: string): TmTask => {
    const own = taskID(t.id);
    // A file that already stores the dotted form must not become "1.1.1".
    const id = prefix === "" || own.startsWith(`${prefix}.`) ? own : `${prefix}.${own}`;
    const dependencies = prefix !== "" && t.dependencies ? t.dependencies.map((d) => { const dep = taskID(d); return dep.includes(".") ? dep : `${prefix}.${dep}`; }) : t.dependencies;
    const subtasks = t.subtasks?.map((s) => qualify(s, id));
    return subtasks === undefined ? { ...t, id, dependencies } : { ...t, id, dependencies, subtasks };
  };
  return tasks.map((t) => qualify(t, prefix));
}

function hasTaskID(tasks: TmTask[], id: string): boolean {
  for (const task of tasks) {
    if (taskID(task.id) === id) return true;
    if (task.subtasks && hasTaskID(task.subtasks, id)) return true;
  }
  return false;
}

export function depsDisplay(deps: unknown[] | undefined): string {
  if (!deps || deps.length === 0) {
    return "";
  }
  return deps.map(taskID).join(", ");
}

// kanbanColumns groups all tasks across tags into the board's three
// status columns: To do (pending and blocked), In progress (including
// review), Done (including cancelled/deferred). Canonical order is
// preserved within a column.
export function kanbanColumns(tags: [string, TmTask[]][]): [string, TmTask[]][] {
  const columns: [string, TmTask[]][] = [
    ["To do", []],
    ["In progress", []],
    ["Done", []],
  ];
  const [todoCol, doingCol, doneCol] = columns as [
    [string, TmTask[]],
    [string, TmTask[]],
    [string, TmTask[]],
  ];
  const flatten = (tasks: TmTask[], out: TmTask[]): void => {
    for (const t of tasks) {
      out.push(t);
      if (t.subtasks) flatten(t.subtasks, out);
    }
  };
  const all: TmTask[] = [];
  for (const [, tasks] of tags) {
    flatten(tasks, all);
  }
  for (const t of all) {
    switch (t.status) {
      case "in-progress":
      case "review":
        doingCol[1].push(t);
        break;
      case "done":
      case "cancelled":
      case "deferred":
        doneCol[1].push(t);
        break;
      default:
        todoCol[1].push(t);
    }
  }
  return columns;
}
