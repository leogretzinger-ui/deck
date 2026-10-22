import { ApiError } from "../../api/http.ts";
import type { ApiClient } from "../../api/contracts.ts";
import type { Lane, LaneApiClient, TmTask } from "../../api/tasks.ts";
import type { SessionSummary } from "../../api/sessions.ts";
import { providerDisplayName } from "../../ui/session-identity.ts";
import type { LessonCall } from "./lessons.ts";
import {
  findLaneTask,
  laneProblem,
  pickLaneTask,
  pickOpenTask,
  reviewCandidates,
  reviewPrompt,
} from "./lane.ts";

// The lane, review and done buttons of the tutorial. Everything the engine
// does happens on the server; this class decides what to ask for and tells the
// user what happened. It never reads a terminal and never presses Return.

export interface LaneHost {
  readonly api: Pick<ApiClient, "listProviders" | "createSession" | "listTaskmasterTasks"> & LaneApiClient;
  say(message: string): void;
  // The project the Tasks panel shows and the tasks it holds, or null while
  // no project's tasks are open.
  tasks(): { project: string; tags: readonly (readonly [string, readonly TmTask[]])[] } | null;
  // The agent the user picked in the tutorial.
  agent(): string | null;
  // The sessions the sidebar lists for a project.
  sessions(projectID: string): readonly SessionSummary[];
  // The selected session when it is attached, else null.
  attached(): { id: string; project_id: string; provider: string } | null;
  refreshSidebar(): Promise<void>;
  // Selects a session the tutorial started.
  openSession(session: SessionSummary): Promise<void>;
  // Opens the new session dialog on a project.
  chooseAgentIn(projectID: string): void;
  // Opens the agent picker with no project and runs `then` with the choice
  // recorded.
  chooseAgent?(then: () => void): void;
  // Types into the attached agent session without pressing Return. `sent`
  // runs only when the server confirms that very write arrived. Returns false
  // when nothing was handed to the terminal.
  type(text: string, sent: () => void): boolean;
  refreshTasks(): Promise<void>;
  // Lane facts changed; the tutorial should look at its step again.
  changed(): void;
}

const REVIEW_POLL_MS = 10_000;

interface ClosedLane {
  project: string;
  tag: string;
  task: number;
  // The task's status read back from the engine after the close: null until a
  // read succeeds. A failed read is not an answer.
  done: boolean | null;
}

export class TutorialLanes {
  private lanes: Lane[] = [];
  private busy = false;
  // The lane whose sessions the user has been told the close will stop.
  private stopConsent: string | null = null;
  private closed: ClosedLane | null = null;
  private observing = false;
  // The review request that was seen on its pull request, by identity.
  private reviewSeenKey: string | null = null;
  private reviewTimer: ReturnType<typeof setInterval> | null = null;
  private reviewPollKey: string | null = null;
  // The lane waiting for the user to pick a reviewer in the new session dialog.
  private armed: Lane | null = null;
  private disposed = false;
  private readonly host: LaneHost;

  constructor(host: LaneHost) {
    this.host = host;
  }

  dispose(): void {
    this.disposed = true;
    this.stopReviewPoll();
  }

  // The open lane of the project whose tasks are showing. The Tasks panel
  // follows the selected session, so once the lane's session is selected it
  // shows the lane's own project, which reads the same task store.
  open(): Lane | null {
    const project = this.host.tasks()?.project;
    return this.lanes.find((lane) => lane.phase === "open" && (lane.project_id === project || lane.lane_project_id === project)) ?? null;
  }

  // A lane whose worktree is already removed and whose run is not yet finished.
  // Closing it again goes to the source project, which replays the recorded
  // finish; the lane's own project has nothing left to close.
  private removed(): Lane | null {
    const project = this.host.tasks()?.project;
    return this.lanes.find((lane) => lane.phase === "removed" && (lane.project_id === project || lane.lane_project_id === project)) ?? null;
  }

  async refresh(projectID: string): Promise<void> {
    try {
      const lanes = await this.host.api.listLanes(projectID);
      if (this.disposed) return;
      this.lanes = [...this.lanes.filter((lane) => lane.project_id !== projectID && lane.lane_project_id !== projectID), ...lanes];
      this.host.changed();
    } catch {
      // A background read: the next one tries again.
    }
  }

  // The user's own project the lane was made from: the open lane's, else the
  // one that was just closed.
  sourceProject(): string | null {
    return this.open()?.project_id ?? this.closed?.project ?? null;
  }

  taskText(): { title: string; description: string } | null {
    const lane = this.open();
    const task = lane === null ? null : findLaneTask(this.host.tasks()?.tags ?? [], lane.tag, lane.task);
    return task === null ? null : { title: task.title, description: task.description ?? "" };
  }

  // Whether a lane event holds right now.
  holds(call: LessonCall): boolean {
    switch (call.name) {
      case "lane_session_attached": {
        const lane = this.open();
        const session = this.host.attached();
        return lane !== null && session !== null && session.project_id === lane.lane_project_id && session.provider !== "shell";
      }
      case "review_seen": {
        const key = reviewKey(this.open());
        return key !== null && key === this.reviewSeenKey;
      }
      default: return false;
    }
  }

  // Whether the lane's task is done. An open lane's task is not done yet; after
  // the lane closes the answer is the task's own status.
  taskDone(): boolean | null {
    if (this.open() !== null) return false;
    const closed = this.closed;
    if (closed === null) return null;
    if (closed.done !== null) return closed.done;
    // Not read back yet: the Tasks data the step follows may already show it.
    const seen = findLaneTask(this.host.tasks()?.tags ?? [], closed.tag, closed.task);
    return seen?.status === "done";
  }

  // Reads the closed lane's task again after a Tasks refresh, until one read
  // succeeds.
  async observeClosed(): Promise<void> {
    const closed = this.closed;
    if (closed === null || closed.done !== null || this.observing || this.disposed) return;
    this.observing = true;
    try {
      const done = await this.statusIsDone(closed);
      if (done !== null && this.closed === closed) {
        closed.done = done;
        this.host.changed();
      }
    } finally {
      this.observing = false;
    }
  }

  async start(): Promise<void> {
    if (this.busy) return;
    const say = (message: string): void => this.host.say(message);
    const tasks = this.host.tasks();
    if (tasks === null) {
      say("Select a session in your project first, so Deck knows whose tasks to use.");
      return;
    }
    const agent = this.host.agent();
    if (agent === null) {
      if (this.host.chooseAgent === undefined) {
        say("Choose an agent first.");
        return;
      }
      say("Choose an agent first. The lane starts as soon as you do.");
      this.host.chooseAgent(() => { void this.start(); });
      return;
    }
    this.busy = true;
    try {
      let lane = this.open();
      if (lane === null) {
        // A lane that failed to start still holds its claim, so it is tried
        // again before any other task is picked.
        const failed = this.lanes.find((candidate) => candidate.phase === "failed" && candidate.project_id === tasks.project);
        const task = failed === undefined ? pickLaneTask(tasks.tags) : { tag: failed.tag, id: failed.task, title: `task ${failed.task}` };
        if (task === null) {
          say("No task is ready for a lane. Add a task first, or finish the tasks it waits on.");
          return;
        }
        say(`Starting a lane for task ${task.id}, ${task.title}.`);
        lane = await this.host.api.startLane(tasks.project, { tag: task.tag, task: task.id, agent });
      }
      await this.refresh(tasks.project);
      await this.host.refreshSidebar();
      const opened = lane;
      const existing = this.host.sessions(opened.lane_project_id).find((session) => session.provider === opened.agent);
      if (existing !== undefined) {
        await this.host.openSession(existing);
        say(`The lane for task ${lane.task} is open on ${lane.branch}. Selected the ${providerDisplayName(lane.agent)} session in it.`);
        return;
      }
      const session = await this.host.api.createSession(lane.lane_project_id, lane.agent);
      await this.host.openSession(session);
      say(`The lane for task ${lane.task} is open on ${lane.branch}. Started ${providerDisplayName(lane.agent)} in it.`);
    } catch (err) {
      say(`The lane did not start. ${laneProblem(err)}`);
      await this.refresh(tasks.project);
    } finally {
      this.busy = false;
    }
  }

  // Starts a different agent in the lane, or, when one is already selected,
  // types the review prompt into it. Typing waits for a second press because
  // a new agent may open with a sign-in or trust question that would take the
  // text; Deck does not read the terminal to find out.
  async requestReview(): Promise<void> {
    if (this.busy) return;
    const say = (message: string): void => this.host.say(message);
    const lane = this.open();
    if (lane === null) {
      say("Start a lane first. The review is for the pull request on its branch.");
      return;
    }
    this.busy = true;
    try {
      const selected = this.host.attached();
      if (selected !== null && selected.project_id === lane.lane_project_id
        && selected.provider !== "shell" && selected.provider !== lane.agent) {
        await this.typeReview(lane, selected.provider);
        return;
      }
      const candidates = reviewCandidates(await this.host.api.listProviders(), lane.agent);
      if (candidates.length === 0) {
        say(`${providerDisplayName(lane.agent)} is the only agent available here, and a review needs a different one. Use Skip step to move on.`);
        return;
      }
      await this.host.refreshSidebar();
      const reviewer = this.host.sessions(lane.lane_project_id).find((session) => candidates.includes(session.provider));
      if (reviewer !== undefined) {
        await this.host.openSession(reviewer);
        say(`Selected ${providerDisplayName(reviewer.provider)} in the lane. ${REVIEW_NEXT}`);
        return;
      }
      if (candidates.length > 1) {
        this.armed = lane;
        this.host.chooseAgentIn(lane.lane_project_id);
        say(`Choose an agent other than ${providerDisplayName(lane.agent)} for the review.`);
        return;
      }
      const session = await this.host.api.createSession(lane.lane_project_id, candidates[0]!);
      await this.host.openSession(session);
      say(`Started ${providerDisplayName(session.provider)} in the lane. ${REVIEW_NEXT}`);
    } catch (err) {
      say(`The review did not start. ${laneProblem(err)}`);
    } finally {
      this.busy = false;
    }
  }

  // A session was created through the new session dialog.
  noteCreated(session: { provider: string }): void {
    const lane = this.armed;
    this.armed = null;
    if (lane === null) return;
    if (session.provider === lane.agent) {
      this.host.say(`${providerDisplayName(lane.agent)} wrote the change. Choose Request review again and pick a different agent.`);
      return;
    }
    this.host.say(`Started ${providerDisplayName(session.provider)} in the lane. ${REVIEW_NEXT}`);
  }

  private async typeReview(lane: Lane, reviewer: string): Promise<void> {
    const handed = this.host.type(reviewPrompt(this.taskText()?.title ?? null), () => {
      void this.recordReview(lane, reviewer);
    });
    if (!handed) this.host.say("Wait until the reviewer is attached, then choose Request review again.");
  }

  // Runs once the review prompt is confirmed delivered, so the request is only
  // recorded for a prompt that arrived.
  private async recordReview(lane: Lane, reviewer: string): Promise<void> {
    try {
      await this.host.api.requestLaneReview(lane.project_id, lane.tag, lane.task, reviewer);
    } catch (err) {
      this.host.say(`Deck could not record the review request. ${laneProblem(err)}`);
      return;
    }
    this.host.say(
      `Read the review prompt for ${providerDisplayName(reviewer)}, then press Return to send it. `
      + "It posts its findings as a comment on the pull request, not a formal approval.",
    );
    await this.refresh(lane.project_id);
  }

  // Marks the lesson's task done. Lesson 3 closes the lane; without one it
  // writes the status of the only open task.
  async markDone(): Promise<void> {
    if (this.busy) return;
    const say = (message: string): void => this.host.say(message);
    const tasks = this.host.tasks();
    this.busy = true;
    try {
      const lane = this.open() ?? this.removed();
      let target: { project: string; tag: string; task: number; revision?: number };
      if (lane !== null) {
        target = { project: lane.project_id, tag: lane.tag, task: lane.task };
      } else if (tasks !== null) {
        const picked = pickOpenTask(tasks.tags);
        if (picked === null) {
          say("Deck cannot tell which task this lesson is about, because more than one is open. Mark the task done from the Tasks panel or with noise tasks.");
          return;
        }
        target = { project: tasks.project, tag: picked.tag, task: picked.id, revision: picked.revision };
      } else {
        say("Select a session in your project first, so Deck knows whose tasks to use.");
        return;
      }
      const key = `${target.project}/${target.tag}/${target.task}`;
      try {
        const result = await this.host.api.markTaskDone(target.project, String(target.task), {
          tag: target.tag, revision: target.revision, stopSessions: this.stopConsent === key,
        });
        this.stopConsent = null;
        say(doneMessage(target.task, result));
        const closed: ClosedLane = { project: target.project, tag: target.tag, task: target.task, done: await this.statusIsDone(target) };
        this.closed = closed;
        if (closed.done === null) {
          say(`${doneMessage(target.task, result)} Deck could not read the task status to confirm it, so it will keep trying.`);
        }
      } catch (err) {
        if (err instanceof ApiError && err.code === "lane_sessions_live") {
          this.stopConsent = key;
          say(`${err.message}. Choose Mark task done again to stop them and close the lane.`);
          return;
        }
        say(`The task was not marked done. ${laneProblem(err)}`);
        return;
      }
      await Promise.all([this.refresh(target.project), this.host.refreshSidebar(), this.host.refreshTasks()]);
    } finally {
      this.busy = false;
    }
  }

  private async statusIsDone(target: { project: string; tag: string; task: number }): Promise<boolean | null> {
    try {
      const byTag = await this.host.api.listTaskmasterTasks(target.project);
      const tags = Object.entries(byTag).map(([tag, file]) => [tag, file.tasks] as const);
      return findLaneTask(tags, target.tag, target.task)?.status === "done";
    } catch {
      return null;
    }
  }

  // Watches the lane's pull request for the review while a step waits on it.
  // What is seen belongs to one lane and one review request: another lane, or
  // a newer request on the same lane, starts again, and an answer that arrives
  // after the lane or request changed is dropped.
  syncReviewPoll(waiting: boolean): void {
    const key = waiting && !this.disposed ? reviewKey(this.open()) : null;
    if (key === null || key === this.reviewSeenKey) {
      this.stopReviewPoll();
      return;
    }
    if (this.reviewTimer !== null) {
      if (this.reviewPollKey === key) return;
      this.stopReviewPoll();
    }
    this.reviewPollKey = key;
    const check = async (): Promise<void> => {
      const asked = this.open();
      if (reviewKey(asked) !== key || asked === null) return;
      if (typeof document !== "undefined" && document.visibilityState !== "visible") return;
      try {
        const review = await this.host.api.getLaneReview(asked.project_id, asked.tag, asked.task);
        if (this.disposed || !review.seen || reviewKey(this.open()) !== key) return;
        this.reviewSeenKey = key;
        this.stopReviewPoll();
        this.host.changed();
      } catch {
        // GitHub may be unreachable for a while; the next poll tries again.
      }
    };
    void check();
    this.reviewTimer = setInterval(() => { void check(); }, REVIEW_POLL_MS);
  }

  private stopReviewPoll(): void {
    if (this.reviewTimer !== null) clearInterval(this.reviewTimer);
    this.reviewTimer = null;
    this.reviewPollKey = null;
  }
}

// One review request on one lane, or null while none was asked for.
function reviewKey(lane: Lane | null): string | null {
  if (lane === null || !lane.review_requested) return null;
  return [lane.project_id, lane.tag, lane.task, lane.review_requested_at ?? ""].join("/");
}

const REVIEW_NEXT = "When it is ready, choose Request review again and Deck types the review prompt. This review is a comment from another model, not a formal approval.";

function doneMessage(task: number, result: { lane_closed: boolean; branch?: string; branch_kept?: boolean; warning?: string }): string {
  if (!result.lane_closed) return `Marked task ${task} done.`;
  const branch = result.branch_kept === true
    ? `The branch ${result.branch} is still in your repository because Git cannot tell it is merged. Delete it with git branch -D when you no longer need it.`
    : `The branch ${result.branch} is gone too.`;
  return `Marked task ${task} done and removed its worktree. ${branch}${result.warning === undefined ? "" : ` ${result.warning}`}`;
}
