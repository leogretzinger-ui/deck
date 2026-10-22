// TaskmasterInitResult reports the store the project bound to. The path is
// echoed because a worktree binds to its repository family's store, which
// does not sit under the project boundary.
export interface TaskmasterInitResult {
  path: string;
  tag: string;
  already_initialized: boolean;
}

// TaskmasterInfo is the detected project structure. A 404 with code
// not_taskmaster_project is a STATE (the project is not a Taskmaster
// project), surfaced through ApiError for the caller to classify.
export interface TaskmasterInfo {
  path: string;
  config_path: string;
  tasks_path: string;
  docs_path: string;
}

// TmTask mirrors the Tasks JSON response. id/parentId/dependencies stay
// unknown-typed so a field shape this product does not read cannot break the
// parse.
export interface TmTask {
  id: unknown;
  parentId?: unknown;
  title: string;
  description?: string;
  status: string;
  priority?: string;
  dependencies?: unknown[];
  details?: string;
  subtasks?: TmTask[];
  // Optional precomputed subtask progress. Real taskmaster data carries
  // subtasks (flattened into the columns); the fixture supplies counts so
  // the progress bar renders without phantom cards.
  subtasks_done?: number;
  subtasks_total?: number;
  updatedAt?: string;
  revision?: number;
  claim?: {
    owner: string;
    acquiredAt: string;
    expiresAt: string;
  };
}

// TaskmasterTasks is the engine response keyed by tag, exactly as served.
export type TaskmasterTasks = Record<string, { tasks: TmTask[] }>;

// TmRun is one generation job record.
export interface TmRun {
  id: string;
  project_id: string;
  argv: string[];
  prd: string | null;
  status: string;
  exit_code: number | null;
  stdout?: string;
  stderr?: string;
  started_at: string;
  finished_at?: string;
  duration_ms?: number;
}

export interface AddTaskInput {
  title: string;
  description: string;
  details: string;
  priority: "high" | "medium" | "low";
}

export interface AddTaskResult {
  id: string;
}

// A task lane Deck opened through the Tasks engine: the task's own branch and
// worktree, registered as the project `lane_project_id`. It carries no
// capability; the engine's fencing tokens never leave the server.
export interface Lane {
  tag: string;
  task: number;
  // The agent that writes in the lane.
  agent: string;
  project_id: string;
  lane_project_id: string;
  branch: string;
  phase: "claimed" | "running" | "bound" | "open" | "removed" | "failed";
  failed_step?: string;
  review_requested: boolean;
  review_agent?: string;
  // Identifies one review request; a later request is a different review.
  review_requested_at?: string;
}

export interface StartLaneInput {
  tag: string;
  task: number;
  agent: string;
}

export interface TaskDoneInput {
  tag: string;
  // The task revision the caller saw; a task without a lane is refused when
  // it has moved on.
  revision?: number;
  // Consent to stop the sessions still running in the lane.
  stopSessions?: boolean;
}

export interface TaskDoneResult {
  status: string;
  lane_closed: boolean;
  branch?: string;
  // The local branch stayed in the repository because Git could not show it
  // was safe to delete.
  branch_kept?: boolean;
  warning?: string;
}

export interface LaneReview {
  requested: boolean;
  // A comment or review arrived on the lane's pull request after the review
  // was requested.
  seen: boolean;
  pull_request?: number;
}

export interface LaneApiClient {
  listLanes(projectID: string): Promise<Lane[]>;
  startLane(projectID: string, input: StartLaneInput): Promise<Lane>;
  // Marks a task done: closes its lane when it has one, else writes the status.
  markTaskDone(projectID: string, taskID: string, input: TaskDoneInput): Promise<TaskDoneResult>;
  requestLaneReview(projectID: string, tag: string, task: number, agent: string): Promise<Lane>;
  getLaneReview(projectID: string, tag: string, task: number): Promise<LaneReview>;
}
