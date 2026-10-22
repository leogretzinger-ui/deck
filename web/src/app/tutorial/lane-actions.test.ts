import assert from "node:assert/strict";
import test from "node:test";
import { ApiError } from "../../api/http.ts";
import type { Lane, TmTask } from "../../api/tasks.ts";
import type { SessionSummary } from "../../api/sessions.ts";
import { TutorialLanes, type LaneHost } from "./lane-actions.ts";

const lane = (extra: Partial<Lane> = {}): Lane => ({
  tag: "main", task: 4, agent: "claude", project_id: "p-1", lane_project_id: "p-lane",
  branch: "tasks/main/4", phase: "open", review_requested: false, ...extra,
});

const summary = (id: string, project: string, provider: string): SessionSummary => ({ id, project_id: project, provider } as SessionSummary);

interface Harness {
  host: LaneHost;
  lanes: TutorialLanes;
  said: string[];
  calls: string[];
  state: {
    lanes: Lane[];
    tasks: TmTask[];
    providers: string[];
    sessions: SessionSummary[];
    attached: { id: string; project_id: string; provider: string } | null;
    typed: string[];
    agent: string | null;
    confirm: boolean;
    doneError: ApiError | null;
    doneInputs: unknown[];
  };
}

function harness(): Harness {
  const said: string[] = [];
  const calls: string[] = [];
  const state: Harness["state"] = {
    lanes: [], providers: ["claude", "codex", "shell"], sessions: [], attached: null, typed: [], agent: "claude",
    confirm: true, doneError: null, doneInputs: [],
    tasks: [{ id: "4", title: "Print it", description: "Details", status: "pending", revision: 2 }],
  };
  const host: LaneHost = {
    api: {
      listProviders: async () => state.providers,
      createSession: async (project: string, provider: string) => {
        calls.push(`createSession:${project}:${provider}`);
        const created = summary(`s-${state.sessions.length + 1}`, project, provider);
        state.sessions.push(created);
        return created;
      },
      listLanes: async () => state.lanes,
      startLane: async (project: string, input: { tag: string; task: number; agent: string }) => {
        calls.push(`startLane:${project}:${input.tag}:${input.task}:${input.agent}`);
        const started = lane({ project_id: project, task: input.task, agent: input.agent });
        state.lanes = [started];
        return started;
      },
      markTaskDone: async (project: string, task: string, input: unknown) => {
        calls.push(`markTaskDone:${project}:${task}`);
        state.doneInputs.push(input);
        if (state.doneError !== null) throw state.doneError;
        const closed = state.lanes.length > 0;
        state.lanes = [];
        state.tasks[0]!.status = "done";
        return closed ? { status: "done", lane_closed: true, branch: "tasks/main/4" } : { status: "done", lane_closed: false };
      },
      requestLaneReview: async (_project: string, _tag: string, _task: number, agent: string) => {
        calls.push(`requestLaneReview:${agent}`);
        return lane({ review_requested: true, review_agent: agent });
      },
      getLaneReview: async () => ({ requested: true, seen: false }),
      listTaskmasterTasks: async () => ({ main: { tasks: state.tasks } }),
    } as LaneHost["api"],
    say: (message) => said.push(message),
    tasks: () => ({ project: "p-1", tags: [["main", state.tasks]] }),
    agent: () => state.agent,
    sessions: (project) => state.sessions.filter((session) => session.project_id === project),
    attached: () => state.attached,
    refreshSidebar: async () => { calls.push("refreshSidebar"); },
    openSession: async (session) => {
      calls.push(`openSession:${session.id}`);
      state.attached = { id: session.id, project_id: session.project_id, provider: session.provider };
    },
    chooseAgentIn: (project) => { calls.push(`chooseAgentIn:${project}`); },
    type: (text, sent) => { state.typed.push(text); if (state.confirm) sent(); return true; },
    refreshTasks: async () => { calls.push("refreshTasks"); },
    changed: () => {},
  };
  return { host, lanes: new TutorialLanes(host), said, calls, state };
}

test("Start a lane starts a lane for the next task, then opens the picked agent in it", async () => {
  const h = harness();
  await h.lanes.start();
  assert.deepEqual(h.calls.filter((c) => /^(startLane|createSession|openSession)/.test(c)), [
    "startLane:p-1:main:4:claude", "createSession:p-lane:claude", "openSession:s-1",
  ]);
  assert.ok(h.said.at(-1)!.includes("tasks/main/4"));
  assert.equal(h.lanes.holds({ name: "lane_session_attached", arg: null }), true);
});

test("a lane that failed to start is tried again before another task is picked", async () => {
  const h = harness();
  h.state.tasks = [
    { id: "3", title: "Claimed by the failed lane", status: "pending", claim: { owner: "deck-tutorial", acquiredAt: "", expiresAt: "" } },
    { id: "4", title: "Next", status: "pending" },
  ];
  h.state.lanes = [lane({ task: 3, phase: "failed", failed_step: "register the worktree as a project" })];
  await h.lanes.refresh("p-1");
  await h.lanes.start();
  assert.deepEqual(h.calls.filter((c) => c.startsWith("startLane")), ["startLane:p-1:main:3:claude"]);
});

test("a reload that finds an open lane selects its session instead of starting another", async () => {
  const h = harness();
  h.state.lanes = [lane()];
  h.state.sessions = [summary("s-9", "p-lane", "claude")];
  await h.lanes.refresh("p-1");
  await h.lanes.start();
  assert.deepEqual(h.calls.filter((c) => /^(startLane|createSession)/.test(c)), []);
  assert.ok(h.calls.includes("openSession:s-9"));
});

test("Start a lane with no agent chosen opens the picker and starts once one is", async () => {
  const h = harness();
  h.state.agent = null;
  let choose: (() => void) | null = null;
  h.host.chooseAgent = (then) => { choose = then; };
  await h.lanes.start();
  assert.match(h.said.at(-1)!, /Choose an agent first\. The lane starts as soon as you do/);
  assert.equal(h.calls.some((call) => call.startsWith("startLane")), false);
  h.state.agent = "claude";
  choose!();
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.ok(h.calls.some((call) => call.startsWith("startLane:")));
});

test("Start a lane says what stopped it and what to do first", async () => {
  const h = harness();
  h.state.agent = null;
  await h.lanes.start();
  assert.equal(h.said.at(-1), "Choose an agent first.");
  h.state.agent = "claude";
  h.state.tasks = [{ id: "4", title: "Done already", status: "done" }];
  await h.lanes.start();
  assert.match(h.said.at(-1)!, /No task is ready/);
  h.state.tasks = [{ id: "4", title: "Print it", status: "pending" }];
  h.host.api.startLane = async () => { throw new ApiError(422, "lane_step_failed", "could not claim the task: boom"); };
  await h.lanes.start();
  assert.equal(h.said.at(-1), "The lane did not start. could not claim the task: boom");
});

test("the lane is found from its own project, where the Tasks panel lands once its session is selected", async () => {
  const h = harness();
  h.state.lanes = [lane()];
  h.host.tasks = () => ({ project: "p-lane", tags: [["main", h.state.tasks]] });
  await h.lanes.refresh("p-lane");
  assert.equal(h.lanes.open()?.task, 4);
  assert.equal(h.lanes.taskText()?.title, "Print it");
  await h.lanes.markDone();
  assert.deepEqual(h.calls.filter((c) => c.startsWith("markTaskDone")), ["markTaskDone:p-1:4"], "the close names the project the lane works on");
});

test("lane_session_attached needs the selected session to be an agent in the lane project", async () => {
  const h = harness();
  h.state.lanes = [lane()];
  await h.lanes.refresh("p-1");
  const attached = { name: "lane_session_attached", arg: null };
  assert.equal(h.lanes.holds(attached), false);
  h.state.attached = { id: "s-1", project_id: "p-1", provider: "claude" };
  assert.equal(h.lanes.holds(attached), false, "a session in the source project is not the lane's");
  h.state.attached = { id: "s-2", project_id: "p-lane", provider: "shell" };
  assert.equal(h.lanes.holds(attached), false, "a shell is not the agent");
  h.state.attached = { id: "s-3", project_id: "p-lane", provider: "claude" };
  assert.equal(h.lanes.holds(attached), true);
});

test("Request review starts a different agent, and types the prompt only on the next press", async () => {
  const h = harness();
  h.state.lanes = [lane()];
  await h.lanes.refresh("p-1");
  h.state.attached = { id: "s-1", project_id: "p-lane", provider: "claude" };

  await h.lanes.requestReview();
  assert.deepEqual(h.calls.filter((c) => c.startsWith("createSession")), ["createSession:p-lane:codex"]);
  assert.equal(h.state.typed.length, 0, "a fresh agent may be asking a question: nothing is typed yet");
  assert.match(h.said.at(-1)!, /not a formal approval/);

  h.state.confirm = false;
  await h.lanes.requestReview();
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(h.state.typed.length, 1);
  assert.ok(!h.calls.includes("requestLaneReview:codex"), "nothing is recorded before the write is confirmed");
  h.state.confirm = true;
  await h.lanes.requestReview();
  await new Promise((resolve) => setTimeout(resolve, 0));
  h.state.typed.shift();
  assert.ok(h.state.typed[0]!.includes("gh pr comment"));
  assert.ok(h.calls.includes("requestLaneReview:codex"));
  assert.match(h.said.at(-1)!, /press Return to send it/);
  assert.match(h.said.at(-1)!, /not a formal approval/);
});

test("Request review never types into the agent that wrote the change", async () => {
  const h = harness();
  h.state.lanes = [lane()];
  await h.lanes.refresh("p-1");
  h.state.sessions = [summary("s-1", "p-lane", "claude")];
  h.state.attached = { id: "s-1", project_id: "p-lane", provider: "claude" };
  await h.lanes.requestReview();
  assert.equal(h.state.typed.length, 0);
  assert.ok(h.calls.some((c) => c === "createSession:p-lane:codex"));
});

test("with only one agent available the review step says so and points at Skip step", async () => {
  const h = harness();
  h.state.providers = ["claude", "shell"];
  h.state.lanes = [lane()];
  await h.lanes.refresh("p-1");
  await h.lanes.requestReview();
  assert.match(h.said.at(-1)!, /only agent available/);
  assert.match(h.said.at(-1)!, /Skip step/);
  assert.deepEqual(h.calls.filter((c) => c.startsWith("createSession")), []);
});

test("with several other agents the new session dialog opens on the lane, and the author is refused", async () => {
  const h = harness();
  h.state.providers = ["claude", "codex", "kimi", "shell"];
  h.state.lanes = [lane()];
  await h.lanes.refresh("p-1");
  await h.lanes.requestReview();
  assert.ok(h.calls.includes("chooseAgentIn:p-lane"));
  h.lanes.noteCreated({ provider: "claude" });
  assert.match(h.said.at(-1)!, /wrote the change/);
  await h.lanes.requestReview();
  h.lanes.noteCreated({ provider: "kimi" });
  assert.match(h.said.at(-1)!, /Started kimi/);
  h.lanes.noteCreated({ provider: "claude" });
  assert.match(h.said.at(-1)!, /Started kimi/, "a session made outside the review prompt is ignored");
});

test("review_seen holds once the poll sees a comment, and stops polling", async () => {
  const h = harness();
  h.state.lanes = [lane({ review_requested: true })];
  await h.lanes.refresh("p-1");
  let seen = false;
  let reads = 0;
  h.host.api.getLaneReview = async () => { reads++; return { requested: true, seen }; };
  h.lanes.syncReviewPoll(true);
  await new Promise((resolve) => setTimeout(resolve, 5));
  assert.equal(h.lanes.holds({ name: "review_seen", arg: null }), false);
  seen = true;
  h.lanes.syncReviewPoll(false);
  h.lanes.syncReviewPoll(true);
  await new Promise((resolve) => setTimeout(resolve, 5));
  assert.equal(h.lanes.holds({ name: "review_seen", arg: null }), true);
  const before = reads;
  h.lanes.syncReviewPoll(true);
  await new Promise((resolve) => setTimeout(resolve, 5));
  assert.equal(reads, before);
  h.lanes.dispose();
});

test("a review seen in one lane does not satisfy another lane's review step", async () => {
  const h = harness();
  let current = "p-1";
  h.host.tasks = () => ({ project: current, tags: [["main", h.state.tasks]] });
  h.state.lanes = [
    lane({ project_id: "p-1", lane_project_id: "l-1", review_requested: true, review_requested_at: "2026-10-15T08:00:00Z" }),
    lane({ project_id: "p-2", lane_project_id: "l-2", review_requested: true, review_requested_at: "2026-10-15T08:30:00Z" }),
  ];
  await h.lanes.refresh("p-1");
  await h.lanes.refresh("p-2");
  const asked: string[] = [];
  h.host.api.getLaneReview = async (project: string) => { asked.push(project); return { requested: true, seen: project === "p-1" }; };
  const seen = { name: "review_seen", arg: null };

  h.lanes.syncReviewPoll(true);
  await new Promise((resolve) => setTimeout(resolve, 5));
  assert.equal(h.lanes.holds(seen), true, "lane A's review is seen");

  current = "p-2";
  assert.equal(h.lanes.holds(seen), false, "lane B has not been seen");
  h.lanes.syncReviewPoll(true);
  await new Promise((resolve) => setTimeout(resolve, 5));
  assert.deepEqual(asked, ["p-1", "p-2"], "lane B is asked about on its own");
  assert.equal(h.lanes.holds(seen), false);
  h.lanes.dispose();
});

test("a newer review request on the same lane starts the observation again", async () => {
  const h = harness();
  h.state.lanes = [lane({ review_requested: true, review_requested_at: "2026-10-15T08:00:00Z" })];
  await h.lanes.refresh("p-1");
  h.host.api.getLaneReview = async () => ({ requested: true, seen: true });
  const seen = { name: "review_seen", arg: null };
  h.lanes.syncReviewPoll(true);
  await new Promise((resolve) => setTimeout(resolve, 5));
  assert.equal(h.lanes.holds(seen), true);
  h.state.lanes = [lane({ review_requested: true, review_requested_at: "2026-10-15T09:00:00Z" })];
  await h.lanes.refresh("p-1");
  assert.equal(h.lanes.holds(seen), false, "the first request's comment is not the second request's");
  h.lanes.dispose();
});

test("an answer that arrives after the lane changed is discarded", async () => {
  const h = harness();
  let current = "p-1";
  h.host.tasks = () => ({ project: current, tags: [["main", h.state.tasks]] });
  h.state.lanes = [
    lane({ project_id: "p-1", lane_project_id: "l-1", review_requested: true, review_requested_at: "2026-10-15T08:00:00Z" }),
    lane({ project_id: "p-2", lane_project_id: "l-2", review_requested: true, review_requested_at: "2026-10-15T08:30:00Z" }),
  ];
  await h.lanes.refresh("p-1");
  await h.lanes.refresh("p-2");
  let release: (value: { requested: boolean; seen: boolean }) => void = () => {};
  h.host.api.getLaneReview = () => new Promise((resolve) => { release = resolve; });
  h.lanes.syncReviewPoll(true);
  current = "p-2";
  release({ requested: true, seen: true });
  await new Promise((resolve) => setTimeout(resolve, 5));
  assert.equal(h.lanes.holds({ name: "review_seen", arg: null }), false, "lane A's late answer must not mark lane B");
  current = "p-1";
  assert.equal(h.lanes.holds({ name: "review_seen", arg: null }), false, "and it is dropped, not kept for later");
  h.lanes.dispose();
});

test("Mark task done closes the lane, and task_done follows the task's real status", async () => {
  const h = harness();
  h.state.lanes = [lane()];
  await h.lanes.refresh("p-1");
  assert.equal(h.lanes.taskDone(), false);
  await h.lanes.markDone();
  assert.deepEqual(h.calls.filter((c) => c.startsWith("markTaskDone")), ["markTaskDone:p-1:4"]);
  assert.deepEqual(h.state.doneInputs, [{ tag: "main", revision: undefined, stopSessions: false }]);
  assert.match(h.said.at(-1)!, /Marked task 4 done and removed its worktree/);
  assert.equal(h.lanes.taskDone(), true, "the status was read back from the engine");

  const refused = harness();
  refused.state.lanes = [lane()];
  await refused.lanes.refresh("p-1");
  refused.host.api.markTaskDone = async () => ({ status: "done", lane_closed: true, branch: "tasks/main/4" });
  await refused.lanes.markDone();
  assert.equal(refused.lanes.taskDone(), false, "a click is not proof: the engine still shows the task open");
});

test("a failed status read after the close is not cached as not done", async () => {
  const h = harness();
  h.state.lanes = [lane()];
  await h.lanes.refresh("p-1");
  let failing = true;
  h.host.api.listTaskmasterTasks = async () => {
    if (failing) throw new Error("engine unreachable");
    return { main: { tasks: h.state.tasks } };
  };
  await h.lanes.markDone();
  assert.match(h.said.at(-1)!, /could not read the task status to confirm it/);
  assert.equal(h.state.tasks[0]!.status, "done", "the close itself succeeded");
  assert.equal(h.lanes.taskDone(), true, "the Tasks data the step follows shows the task done");

  h.state.tasks[0]!.status = "pending";
  assert.equal(h.lanes.taskDone(), false, "unread and not shown done is not done yet, and not stuck there");
  h.state.tasks[0]!.status = "done";
  assert.equal(h.lanes.taskDone(), true);

  // The next Tasks refresh reads it again; a failure keeps asking, a success answers.
  h.state.tasks = [];
  await h.lanes.observeClosed();
  assert.equal(h.lanes.taskDone(), false);
  failing = false;
  h.state.tasks = [{ id: "4", title: "Print it", status: "done" }];
  await h.lanes.observeClosed();
  h.state.tasks = [];
  assert.equal(h.lanes.taskDone(), true, "a successful read is the answer");
});

test("retrying a partly closed lane goes to the source project even while Tasks shows the lane project", async () => {
  const h = harness();
  h.state.lanes = [lane()];
  h.host.tasks = () => ({ project: "p-lane", tags: [["main", h.state.tasks]] });
  await h.lanes.refresh("p-lane");
  h.state.doneError = new ApiError(422, "lane_step_failed", "could not finish the run: engine unreachable");
  await h.lanes.markDone();
  assert.deepEqual(h.calls.filter((c) => c.startsWith("markTaskDone")), ["markTaskDone:p-1:4"]);

  // The server removed the worktree and kept the lane for the retry.
  h.state.lanes = [lane({ phase: "removed" })];
  await h.lanes.refresh("p-lane");
  assert.equal(h.lanes.open(), null, "the lane is no longer open");
  h.state.doneError = null;
  await h.lanes.markDone();
  assert.deepEqual(h.calls.filter((c) => c.startsWith("markTaskDone")), ["markTaskDone:p-1:4", "markTaskDone:p-1:4"],
    "the retry names the source project, never the lane project whose checkout is gone");
});

test("Mark task done asks before it stops the sessions in the lane, and refuses work at risk", async () => {
  const h = harness();
  h.state.lanes = [lane()];
  await h.lanes.refresh("p-1");
  h.state.doneError = new ApiError(409, "lane_sessions_live", "these sessions are still running in the lane: claude");
  await h.lanes.markDone();
  assert.match(h.said.at(-1)!, /Choose Mark task done again to stop them/);
  h.state.doneError = null;
  await h.lanes.markDone();
  assert.deepEqual(h.state.doneInputs.map((input) => (input as { stopSessions: boolean }).stopSessions), [false, true]);

  const risky = harness();
  risky.state.lanes = [lane()];
  await risky.lanes.refresh("p-1");
  risky.state.doneError = new ApiError(409, "lane_dirty", "Nothing was changed. The worktree has 2 uncommitted files; commit and push them first.");
  await risky.lanes.markDone();
  assert.match(risky.said.at(-1)!, /^The task was not marked done\. Nothing was changed\./);
  assert.equal(risky.lanes.taskDone(), false);
});

test("without a lane Mark task done fences on the revision it read, and refuses to guess between tasks", async () => {
  const h = harness();
  await h.lanes.markDone();
  assert.deepEqual(h.state.doneInputs, [{ tag: "main", revision: 2, stopSessions: false }]);
  assert.match(h.said.at(-1)!, /^Marked task 4 done\./);

  const two = harness();
  two.state.tasks = [{ id: "1", title: "A", status: "pending" }, { id: "2", title: "B", status: "pending" }];
  await two.lanes.markDone();
  assert.deepEqual(two.state.doneInputs, []);
  assert.match(two.said.at(-1)!, /cannot tell which task/);
});
