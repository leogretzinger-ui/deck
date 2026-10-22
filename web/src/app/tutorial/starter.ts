import { ApiError } from "../../api/http.ts";
import type { ApiClient } from "../../api/contracts.ts";
import type { FileApiClient } from "../../api/files.ts";
import type { LaneApiClient, TmTask } from "../../api/tasks.ts";
import type { Project } from "../../api/projects.ts";
import type { SessionSummary } from "../../api/sessions.ts";
import { taskID } from "../../lib/task-facts.ts";
import type { LessonCall } from "./lessons.ts";

// The practice projects the tutorial creates, as an ordinary state machine
// over the typed API clients. No DOM, no terminal, no command execution, no
// provider output interpretation, no localStorage: every fact comes from a
// real API call and every mutation is one of the client's own write
// methods. The UI (live.ts) observes state through onChange; it never
// drives the sequence itself.

export const starterVersion = 1;
export const starterManifestPath = "tutorial.json";
const starterPrdName = "PRD.md";

export type StarterProjectName = "relnotes" | "ccparse";

export interface StarterFile {
  path: string;
  content: string;
}

export interface StarterBundle {
  relnotes: readonly StarterFile[];
  ccparse: readonly StarterFile[];
}

// Splits the bundled starter files, keyed by import path, into the two
// projects. A file outside both project directories is an error: the bundle
// is exactly what gets uploaded.
export function buildStarterBundle(files: Readonly<Record<string, string>>): StarterBundle {
  const out: Record<StarterProjectName, StarterFile[]> = { relnotes: [], ccparse: [] };
  for (const [key, content] of Object.entries(files)) {
    const match = /\/starters\/(relnotes|ccparse)\/([^/]+)$/.exec(key);
    if (match === null) throw new Error(`starter file ${key} is not directly inside a starter project`);
    out[match[1] as StarterProjectName].push({ path: match[2]!, content });
  }
  for (const name of ["relnotes", "ccparse"] as const) {
    if (out[name].length === 0) throw new Error(`starter project ${name} has no files`);
    out[name].sort((a, b) => (a.path < b.path ? -1 : 1));
  }
  return out;
}

// relnotes reads its sibling by relative path, so a suffixed pair needs the
// reference to name the sibling that was actually created.
export function rewriteSiblingReferences(source: string, siblingName: string): string {
  return source.replace(/\.\.\/ccparse(?![\w-])/g, () => `../${siblingName}`);
}

// The names for a new pair: the plain names when both are free, else the
// same numeric suffix on both, starting at 2.
export function chooseStarterNames(taken: ReadonlySet<string>): { relnotes: string; ccparse: string; suffix: string } | null {
  for (let n = 1; n <= 99; n++) {
    const suffix = n === 1 ? "" : `-${n}`;
    if (!taken.has(`relnotes${suffix}`) && !taken.has(`ccparse${suffix}`)) {
      return { relnotes: `relnotes${suffix}`, ccparse: `ccparse${suffix}`, suffix };
    }
  }
  return null;
}

export interface StarterTaskText {
  title: string;
  description: string;
  details: string;
  priority: "high" | "medium" | "low";
}

// The by-hand task for each PRD requirement. Requirement 2 depends on the
// task for requirement 1; the Tasks add endpoint takes no dependency field,
// so the details say so in words.
export const starterTasks: Readonly<Record<number, StarterTaskText>> = {
  1: {
    title: "Print the Breaking changes section",
    description: "A commit is breaking when its body has a line that starts with BREAKING CHANGE:. Those commits belong under a Breaking changes heading.",
    details: [
      "Today relnotes never prints the Breaking changes section.",
      "Commits whose body has a line starting with \"BREAKING CHANGE:\" should appear under Breaking changes, and nowhere else. The bullet is the text after \"BREAKING CHANGE:\".",
      "relnotes.test.mjs has a failing test for this.",
    ].join("\n"),
    priority: "high",
  },
  2: {
    title: "Treat the ! marker as breaking",
    description: "A subject such as feat(api)!: drop v1 is breaking even when the body says nothing.",
    details: [
      "Depends on the task for requirement 1.",
      "Today these commits are listed as plain features. They should appear under Breaking changes, with the subject's description as the bullet.",
      "Finding the cause is part of this task. It may not be in relnotes. breaking-marker.test.mjs has a failing test for this.",
    ].join("\n"),
    priority: "medium",
  },
};

// Which requirements already have a task. A task made by hand carries its
// requirement's fixed title. Generated tasks follow the PRD order, so the
// first n tasks that are not by-hand tasks cover requirements 1 to n.
export function requirementsWithTasks(tasks: readonly TmTask[]): Set<number> {
  return new Set(requirementTasks(tasks).keys());
}

// Which requirements have a task that is done.
export function requirementsDone(tasks: readonly TmTask[]): Set<number> {
  return new Set([...requirementTasks(tasks)].filter(([, task]) => task.status === "done").map(([n]) => n));
}

function requirementTasks(tasks: readonly TmTask[]): Map<number, TmTask> {
  const byHand = new Map(Object.entries(starterTasks).map(([n, task]) => [task.title, Number(n)]));
  const present = new Map<number, TmTask>();
  const generated: TmTask[] = [];
  for (const task of tasks) {
    const requirement = byHand.get(task.title);
    if (requirement === undefined) generated.push(task);
    else present.set(requirement, task);
  }
  for (let n = 1; n <= generated.length && n <= Object.keys(starterTasks).length; n++) present.set(n, generated[n - 1]!);
  return present;
}

export interface StarterPair {
  relnotes: Project;
  ccparse: Project;
}

interface ManifestEntry {
  projectId: string;
  canonicalPath: string;
}

// The completion marker written last into relnotes. It binds the actual
// project ids and canonical paths of both projects, never names, which a
// copy into another directory would satisfy.
export interface StarterManifest {
  version: number;
  relnotes: ManifestEntry;
  ccparse: ManifestEntry;
}

const manifestKeys = ["ccparse", "relnotes", "version"];
const entryKeys = ["canonicalPath", "projectId"];

function parseEntry(value: unknown): ManifestEntry | null {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return null;
  const record = value as Record<string, unknown>;
  const keys = Object.keys(record).sort();
  if (keys.length !== entryKeys.length || entryKeys.some((k, i) => keys[i] !== k)) return null;
  for (const key of entryKeys) {
    if (typeof record[key] !== "string" || record[key] === "" || (record[key] as string).length > 4096) return null;
  }
  return { projectId: record.projectId as string, canonicalPath: record.canonicalPath as string };
}

// Parsing is strict and bounded on purpose: the manifest is a completion
// marker an agent in the project could have touched, so anything outside the
// exact shape is a refusal, not a best-effort read.
export function parseStarterManifest(text: string): StarterManifest | null {
  if (text.length > 8192) return null;
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch {
    return null;
  }
  if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) return null;
  const record = parsed as Record<string, unknown>;
  const keys = Object.keys(record).sort();
  if (keys.length !== manifestKeys.length || manifestKeys.some((k, i) => keys[i] !== k)) return null;
  if (record.version !== starterVersion) return null;
  const relnotes = parseEntry(record.relnotes);
  const ccparse = parseEntry(record.ccparse);
  if (relnotes === null || ccparse === null) return null;
  return { version: starterVersion, relnotes, ccparse };
}

export interface TutorialStarterState {
  busy: boolean;
  error: string | null;
  notice: string | null;
  // Whether the Git identity catalog has been read, so a missing default is
  // a fact and not a loading state.
  identitiesKnown: boolean;
  defaultIdentityId: string | null;
  // The pair this panel can prove it created, or null.
  pair: StarterPair | null;
  requirements: ReadonlySet<number>;
  // Requirements whose task is marked done.
  doneRequirements: ReadonlySet<number>;
}

type ConditionState = Pick<TutorialStarterState, "pair" | "identitiesKnown" | "defaultIdentityId" | "requirements">;

interface ConditionContext {
  agent?: string | null;
  agentNeedsLogin?: boolean;
  reviewAppAvailable?: boolean;
}

const conditionHandlers: Readonly<Record<string, (state: ConditionState, call: LessonCall, context: ConditionContext) => boolean>> = {
  starter_exists: (state) => state.pair !== null,
  no_git_identity: (state) => state.identitiesKnown && state.defaultIdentityId === null,
  no_task: (state, call) => {
    const n = /^requirement=(\d+)$/.exec(call.arg ?? "");
    return n !== null && state.pair !== null && !state.requirements.has(Number(n[1]));
  },
  agent_cannot_generate_tasks: (_state, _call, context) =>
    context.agent != null && !["claude", "codex", "kimi", "omp"].includes(context.agent),
  agent_needs_login: (_state, _call, context) => context.agentNeedsLogin === true,
  no_review_app: (_state, _call, context) => context.reviewAppAvailable === false,
};

export const IMPLEMENTED_LESSON_CONDITIONS: ReadonlySet<string> = new Set(Object.keys(conditionHandlers));

export function starterCondition(state: ConditionState, call: LessonCall, context: ConditionContext = {}): boolean {
  return Object.hasOwn(conditionHandlers, call.name) ? conditionHandlers[call.name]!(state, call, context) : false;
}

function baseName(path: string): string {
  return path.slice(path.lastIndexOf("/") + 1);
}

class Halted extends Error {}

export interface StarterCreated {
  pair: StarterPair;
  session: SessionSummary | null;
}

export interface TutorialStarterOptions {
  onChange?: () => void;
  wait?: (ms: number) => Promise<void>;
  pollMs?: number;
  maxPolls?: number;
}

export class TutorialStarterModel {
  readonly state: TutorialStarterState = {
    busy: false,
    error: null,
    notice: null,
    identitiesKnown: false,
    defaultIdentityId: null,
    pair: null,
    requirements: new Set(),
    doneRequirements: new Set(),
  };

  private generation = 0;
  private tasksSeq = 0;
  private tasksReading = false;
  private suspended = false;
  private disposed = false;

  private readonly api: ApiClient & LaneApiClient;
  private readonly files: FileApiClient;
  private readonly bundle: StarterBundle;
  private readonly onChange: () => void;
  private readonly wait: (ms: number) => Promise<void>;
  private readonly pollMs: number;
  private readonly maxPolls: number;

  constructor(api: ApiClient & LaneApiClient, files: FileApiClient, bundle: StarterBundle, options: TutorialStarterOptions = {}) {
    this.api = api;
    this.files = files;
    this.bundle = bundle;
    this.onChange = options.onChange ?? (() => {});
    this.wait = options.wait ?? ((ms) => new Promise((resolve) => setTimeout(resolve, ms)));
    this.pollMs = options.pollMs ?? 1500;
    this.maxPolls = options.maxPolls ?? 400;
  }

  // Every in-flight chain fences against this: an await that resolves
  // after a suspend, dispose, or newer operation must not issue the next
  // request or publish state. Requests the server already accepted may
  // have committed; nothing here retries or undoes them silently.
  private live(gen: number): void {
    if (this.disposed || this.suspended || gen !== this.generation) throw new Halted();
  }

  private publish(gen: number, patch: Partial<TutorialStarterState>): void {
    this.live(gen);
    Object.assign(this.state, patch);
    this.onChange();
  }

  private fail(gen: number, message: string): void {
    if (this.disposed || this.suspended || gen !== this.generation) return;
    this.publish(gen, { busy: false, error: message, notice: null });
  }

  private begin(gen: number, notice: string | null = null): void {
    this.publish(gen, { busy: true, error: null, notice });
  }

  private errorMessage(err: unknown): string {
    if (err instanceof ApiError) return err.message;
    if (err instanceof Error) return err.message;
    return String(err);
  }

  private idle(): boolean {
    return !this.disposed && !this.suspended && !this.state.busy;
  }

  // Reads what exists: the Git identity default, the practice projects this
  // tutorial can prove it made, and which requirements have tasks. A failed
  // read keeps what was known and says nothing; it is background work.
  async observe(): Promise<void> {
    if (!this.idle()) return;
    const gen = this.generation;
    try {
      const identities = await this.api.listGitIdentities();
      const projects = await this.api.listProjects();
      const pair = await this.findPair(projects);
      const seq = ++this.tasksSeq;
      const tasks = pair === null ? [] : await this.readRequirementTasks(pair.relnotes.id).catch(() => []);
      if (gen !== this.generation || !this.idle()) return;
      // A newer task answer, from the Tasks panel or a later read, outranks this one.
      const current = seq === this.tasksSeq;
      Object.assign(this.state, {
        identitiesKnown: true,
        defaultIdentityId: identities.default_profile_id,
        pair,
        ...(current ? { requirements: requirementsWithTasks(tasks), doneRequirements: requirementsDone(tasks) } : {}),
      });
      this.onChange();
    } catch {
      // Nothing to report: the next observation tries again.
    }
  }

  private async findPair(projects: readonly Project[]): Promise<StarterPair | null> {
    const candidates = projects
      .filter((p) => /^relnotes(?:-\d+)?$/.test(p.name))
      .sort((a, b) => Number(b.name.slice(9) || 1) - Number(a.name.slice(9) || 1))
      .slice(0, 5);
    for (const relnotes of candidates) {
      try {
        const pair = await this.verifiedPair(projects, relnotes);
        if (pair !== null) return pair;
      } catch {
        // A project whose manifest cannot be read is not ours.
      }
    }
    return null;
  }

  private async verifiedPair(projects: readonly Project[], relnotes: Project): Promise<StarterPair | null> {
    const view = await this.files.viewProjectFile(relnotes.id, starterManifestPath);
    const manifest = parseStarterManifest(view.content);
    if (manifest === null || manifest.relnotes.projectId !== relnotes.id || manifest.relnotes.canonicalPath !== relnotes.canonical_path) return null;
    const ccparse = projects.find((p) => p.id === manifest.ccparse.projectId);
    if (ccparse === undefined || ccparse.canonical_path !== manifest.ccparse.canonicalPath) return null;
    return { relnotes, ccparse };
  }

  // Confirms the pair is still registered where this panel recorded it and
  // still carries its manifest, before any action touches it.
  private async reverify(gen: number): Promise<StarterPair | null> {
    const known = this.state.pair;
    if (known === null) {
      this.fail(gen, "The practice projects are not set up. Create them from lesson 1 first.");
      return null;
    }
    const projects = await this.api.listProjects();
    this.live(gen);
    const relnotes = projects.find((p) => p.id === known.relnotes.id);
    const ccparse = projects.find((p) => p.id === known.ccparse.id);
    if (!relnotes || !ccparse || relnotes.canonical_path !== known.relnotes.canonical_path || ccparse.canonical_path !== known.ccparse.canonical_path) {
      this.publish(gen, { pair: null, requirements: new Set(), doneRequirements: new Set() });
      this.fail(gen, "The practice projects are no longer registered where this panel created them. Nothing was changed. Create them again from lesson 1.");
      return null;
    }
    const pair = await this.verifiedPair(projects, relnotes);
    this.live(gen);
    if (pair === null || pair.ccparse.id !== ccparse.id) {
      this.publish(gen, { pair: null, requirements: new Set(), doneRequirements: new Set() });
      this.fail(gen, "The tutorial manifest in relnotes no longer matches the registered projects. Nothing was changed.");
      return null;
    }
    return pair;
  }

  // Creates both projects side by side, registers them with the default Git
  // identity, uploads the starter files, makes the starter commit, seeds the
  // Tasks engine in relnotes, and writes the manifest last. A directory that
  // already exists is never seeded: the pair takes the next free suffix.
  async create(agent: string | null): Promise<StarterCreated | null> {
    if (!this.idle()) return null;
    const gen = ++this.generation;
    const stageWords = {
      check: "checking your setup",
      directory: "creating the folder",
      registration: "registering the project",
      seed: "uploading the starter files",
      commit: "making the starter commit",
      tasks: "setting up tasks",
      manifest: "recording the result",
      session: "starting the agent",
    } as const;
    let stage: keyof typeof stageWords = "check";
    let working = "";
    const made: string[] = [];
    try {
      this.begin(gen, "Checking your setup…");
      const identities = await this.api.listGitIdentities();
      this.live(gen);
      this.publish(gen, { identitiesKnown: true, defaultIdentityId: identities.default_profile_id });
      const profile = identities.profiles.find((p) => p.id === identities.default_profile_id) ?? null;
      if (profile === null) {
        this.fail(gen, "No default Git identity is set, so nothing was created. Set a Git identity, then create the practice projects.");
        return null;
      }
      if (agent !== null) {
        const providers = await this.api.listProviders();
        this.live(gen);
        if (!providers.includes(agent)) {
          this.fail(gen, `"${agent}" is not available in this deployment, so nothing was created. Choose another agent first.`);
          return null;
        }
      }
      const root = await this.api.listProjectDirectories();
      this.live(gen);
      const parent = root.current_path || root.directories[0]?.path || "";
      if (!parent.startsWith("/")) {
        this.fail(gen, "Deck has no projects folder to create them in, so nothing was created.");
        return null;
      }
      const listing = await this.api.listProjectDirectories(parent);
      this.live(gen);
      const registered = await this.api.listProjects();
      this.live(gen);
      const taken = new Set([
        ...listing.directories.map((d) => d.name),
        ...registered.map((p) => baseName(p.canonical_path)),
      ]);
      // A registered name counts as taken even when the folder listing does
      // not show it, so an existing project is never seeded.
      const names = chooseStarterNames(taken);
      if (names === null) {
        this.fail(gen, "Every practice project name up to 99 is taken, so nothing was created.");
        return null;
      }

      const projects: Partial<Record<StarterProjectName, Project>> = {};
      for (const which of ["ccparse", "relnotes"] as const) {
        const name = names[which];
        working = name;
        // A fresh empty directory is a precondition, not a cleanup step: a
        // directory that already exists was not created for this tutorial
        // and is never registered, seeded, or retried.
        stage = "directory";
        this.begin(gen, `Creating ${name}…`);
        const created = await this.api.createProjectDirectory(parent, name);
        this.live(gen);
        made.push(created.path);
        stage = "registration";
        const project = await this.api.createProject(name, created.path, profile);
        this.live(gen);
        projects[which] = project;
        stage = "seed";
        const rootListing = await this.files.listProjectFiles(project.id, "");
        this.live(gen);
        const rootNames = rootListing.entries.map((e) => e.name);
        // Registration adds AGENTS.md, which the starter commit then carries.
        const instructions = rootListing.entries.some((e) => e.name === "AGENTS.md" && !e.dir);
        const expected = instructions ? 2 : 1;
        if (rootListing.truncated || rootNames.length !== expected || !rootNames.includes(".git")) {
          this.fail(
            gen,
            `The new project directory ${created.path} is not empty (found ${rootNames.join(", ") || "nothing where .git was expected"}); nothing was written to it. ${this.leftover(made)}`,
          );
          return null;
        }
        const starter = this.bundle[which];
        for (const file of starter) {
          const content = which === "relnotes" && file.path === "relnotes.mjs"
            ? rewriteSiblingReferences(file.content, names.ccparse)
            : file.content;
          await this.files.uploadProjectFile(project.id, file.path, new Blob([content]));
          this.live(gen);
        }
        stage = "commit";
        await this.api.commitStarter(project.id, [...starter.map((f) => f.path), ...(instructions ? ["AGENTS.md"] : [])]);
        this.live(gen);
      }
      const relnotes = projects.relnotes!;
      const ccparse = projects.ccparse!;

      stage = "tasks";
      working = names.relnotes;
      this.begin(gen, "Setting up tasks in relnotes…");
      await this.api.initializeTaskmaster(relnotes.id);
      this.live(gen);
      const prd = this.bundle.relnotes.find((f) => f.path === starterPrdName);
      if (prd === undefined) throw new Error("The starter has no PRD.");
      await this.api.uploadPrd(relnotes.id, starterPrdName, prd.content, false);
      this.live(gen);

      // The manifest lands last and binds what actually exists. Until it is
      // written, the directories are honestly incomplete.
      stage = "manifest";
      const manifest: StarterManifest = {
        version: starterVersion,
        relnotes: { projectId: relnotes.id, canonicalPath: relnotes.canonical_path },
        ccparse: { projectId: ccparse.id, canonicalPath: ccparse.canonical_path },
      };
      await this.files.uploadProjectFile(relnotes.id, starterManifestPath, new Blob([JSON.stringify(manifest)]));
      this.live(gen);
      const pair: StarterPair = { relnotes, ccparse };
      const created = `Created ${relnotes.canonical_path} and ${ccparse.canonical_path}`;
      if (agent === null) {
        this.publish(gen, { pair, requirements: new Set(), doneRequirements: new Set(), busy: false, notice: `${created}. No agent was chosen, so no session was started. Choose an agent, then start one in relnotes.` });
        return { pair, session: null };
      }
      this.publish(gen, { pair, requirements: new Set(), doneRequirements: new Set(), notice: `${created}.` });
      stage = "session";
      const session = await this.api.createSession(relnotes.id, agent);
      this.live(gen);
      this.publish(gen, { busy: false, notice: `${created}, and started ${agent} in relnotes.` });
      return { pair, session };
    } catch (err) {
      if (err instanceof Halted) return null;
      const recovery = stage === "check"
        ? "No directory or registration request was sent."
        : stage === "directory"
          ? `Directory creation was not confirmed for ${working}. Check the projects folder before trying again; the next try uses a new name. No registration request was sent for it.`
          : stage === "registration"
            ? `Registration was not confirmed for ${working}. It may already be registered; check the project list. No starter files were sent to it.`
            : stage === "session"
              ? `Both projects are ready. Start ${agent ?? "an agent"} in relnotes from the project's New session button.`
              : `The directories and any files already written stay in place; nothing was deleted. The next try uses a new name. ${this.leftover(made)}`;
      this.fail(gen, `Creating the practice projects stopped while ${stageWords[stage]}${working === "" ? "" : ` (${working})`}: ${this.errorMessage(err)} ${recovery}`);
      return stage === "session" && this.state.pair !== null ? { pair: this.state.pair, session: null } : null;
    }
  }

  private leftover(made: readonly string[]): string {
    return made.length === 0 ? "" : `Left in place: ${made.join(", ")}. Remove them from their project row menu if you don't want them.`;
  }

  // Starts the picked agent in one of the practice projects.
  async startAgent(which: StarterProjectName, provider: string): Promise<SessionSummary | null> {
    if (!this.idle()) return null;
    const gen = ++this.generation;
    try {
      this.begin(gen);
      const providers = await this.api.listProviders();
      this.live(gen);
      if (!providers.includes(provider)) {
        this.fail(gen, `"${provider}" is not available in this deployment. Choose another agent first.`);
        return null;
      }
      const pair = await this.reverify(gen);
      if (pair === null) return null;
      const session = await this.api.createSession(pair[which].id, provider);
      this.live(gen);
      this.publish(gen, { busy: false, notice: `Started ${provider} in ${which}.` });
      return session;
    } catch (err) {
      if (err instanceof Halted) return null;
      this.fail(gen, `Starting the session stopped: ${this.errorMessage(err)}`);
      return null;
    }
  }

  // Puts the starter PRD into relnotes when it is not there. Never replaces.
  private async ensurePrd(gen: number, pair: StarterPair): Promise<boolean> {
    const existing = await this.api.listTaskmasterPRDs(pair.relnotes.id);
    this.live(gen);
    if (existing.includes(starterPrdName)) return false;
    const prd = this.bundle.relnotes.find((f) => f.path === starterPrdName);
    if (prd === undefined) throw new Error("The starter has no PRD.");
    await this.api.uploadPrd(pair.relnotes.id, starterPrdName, prd.content, false);
    this.live(gen);
    return true;
  }

  async addPrd(): Promise<void> {
    if (!this.idle()) return;
    const gen = ++this.generation;
    try {
      this.begin(gen);
      const pair = await this.reverify(gen);
      if (pair === null) return;
      const added = await this.ensurePrd(gen, pair);
      this.publish(gen, { busy: false, notice: added ? "Added PRD.md to relnotes." : "relnotes already has PRD.md; it was left as it is." });
    } catch (err) {
      if (err instanceof Halted) return;
      this.fail(gen, `Adding the PRD stopped: ${this.errorMessage(err)}`);
    }
  }

  // Adds the PRD if missing and starts one generation run asking for count
  // tasks, then follows the run record until it exits. Only the run's status
  // and exit code are read.
  async generateTasks(count: number, harness?: string): Promise<void> {
    if (!this.idle()) return;
    const gen = ++this.generation;
    try {
      this.begin(gen);
      const pair = await this.reverify(gen);
      if (pair === null) return;
      await this.ensurePrd(gen, pair);
      const before = await this.readRequirementTasks(pair.relnotes.id);
      this.live(gen);
      this.begin(gen, "Generating tasks. This is a model request and can take a minute.");
      let run = await this.api.generateTasks(pair.relnotes.id, starterPrdName, count, harness);
      this.live(gen);
      for (let polls = 0; run.status === "running"; polls++) {
        if (polls >= this.maxPolls) {
          this.publish(gen, { busy: false, notice: "Generation is still running. Its result will show in the Tasks panel when it finishes." });
          return;
        }
        await this.wait(this.pollMs);
        this.live(gen);
        run = await this.api.getTaskmasterRun(pair.relnotes.id, run.id);
        this.live(gen);
      }
      const after = await this.readRequirementTasks(pair.relnotes.id);
      this.live(gen);
      const requirements = requirementsWithTasks(after);
      if (run.exit_code !== 0) {
        this.publish(gen, {
          busy: false,
          requirements,
          error: run.exit_code === null
            ? "Task generation ended without an exit status. Check the run in the Tasks panel."
            : `Task generation failed with exit code ${run.exit_code}. The run is listed in the Tasks panel.`,
          notice: null,
        });
        return;
      }
      const added = after.length - before.length;
      this.publish(gen, {
        busy: false,
        requirements,
        notice: added > 0 ? `Generated ${added} ${added === 1 ? "task" : "tasks"}.` : "Generation finished but added no tasks.",
      });
    } catch (err) {
      if (err instanceof Halted) return;
      this.fail(gen, `Generating tasks stopped: ${this.errorMessage(err)}`);
    }
  }

  private async readRequirementTasks(relnotesID: string): Promise<TmTask[]> {
    const byTag = await this.api.listTaskmasterTasks(relnotesID);
    return Object.values(byTag).flatMap((entry) => entry.tasks);
  }

  // Takes the relnotes tasks from a read someone else made, such as the
  // Tasks panel's own refresh. It also retires any background read still on
  // its way, which can only be older.
  noteTasks(tasks: readonly TmTask[]): void {
    this.tasksSeq += 1;
    this.applyTasks(tasks);
  }

  private applyTasks(tasks: readonly TmTask[]): void {
    if (this.disposed || this.suspended || this.state.pair === null) return;
    const requirements = requirementsWithTasks(tasks);
    const done = requirementsDone(tasks);
    const same = (a: ReadonlySet<number>, b: ReadonlySet<number>): boolean => a.size === b.size && [...a].every((n) => b.has(n));
    if (same(requirements, this.state.requirements) && same(done, this.state.doneRequirements)) return;
    this.state.requirements = requirements;
    this.state.doneRequirements = done;
    this.onChange();
  }

  // Reads the relnotes tasks again, one read at a time. A failed or
  // superseded read changes nothing.
  async refreshTasks(): Promise<void> {
    const pair = this.state.pair;
    if (pair === null || !this.idle() || this.tasksReading) return;
    const gen = this.generation;
    const seq = ++this.tasksSeq;
    this.tasksReading = true;
    try {
      const tasks = await this.readRequirementTasks(pair.relnotes.id);
      if (gen !== this.generation || !this.idle() || seq !== this.tasksSeq || this.state.pair?.relnotes.id !== pair.relnotes.id) return;
      this.applyTasks(tasks);
    } catch {
      // Background read: the next one tries again.
    } finally {
      this.tasksReading = false;
    }
  }

  // The by-hand path: the task for one PRD requirement, with fixed text.
  async addTask(requirement: number): Promise<void> {
    if (!this.idle()) return;
    const gen = ++this.generation;
    const text = starterTasks[requirement];
    try {
      this.begin(gen);
      if (text === undefined) {
        this.fail(gen, `The PRD has no requirement ${requirement}.`);
        return;
      }
      const pair = await this.reverify(gen);
      if (pair === null) return;
      const present = requirementsWithTasks(await this.readRequirementTasks(pair.relnotes.id));
      this.live(gen);
      if (present.has(requirement)) {
        this.publish(gen, { busy: false, requirements: present, notice: `Requirement ${requirement} already has a task.` });
        return;
      }
      if (requirement > 1 && !present.has(requirement - 1)) {
        this.fail(gen, `Add the task for requirement ${requirement - 1} first; this one depends on it.`);
        return;
      }
      await this.api.addTaskmasterTask(pair.relnotes.id, text);
      this.live(gen);
      const next = requirementsWithTasks(await this.readRequirementTasks(pair.relnotes.id));
      this.live(gen);
      this.publish(gen, { busy: false, requirements: next, notice: `Added the task for requirement ${requirement}: ${text.title}.` });
    } catch (err) {
      if (err instanceof Halted) return;
      this.fail(gen, `Adding the task stopped: ${this.errorMessage(err)}`);
    }
  }

  // Marks the task for one PRD requirement done. The practice project has no
  // lane, so this is a status write fenced by the revision the task was read at.
  async markRequirementDone(requirement: number): Promise<boolean> {
    if (!this.idle()) return false;
    const gen = ++this.generation;
    try {
      this.begin(gen);
      const pair = await this.reverify(gen);
      if (pair === null) return false;
      const byTag = await this.api.listTaskmasterTasks(pair.relnotes.id);
      this.live(gen);
      let found: { tag: string; task: TmTask } | null = null;
      for (const [tag, entry] of Object.entries(byTag)) {
        const task = requirementTasks(entry.tasks).get(requirement);
        if (task !== undefined) found = { tag, task };
      }
      if (found === null) {
        this.fail(gen, `There is no task for requirement ${requirement} yet.`);
        return false;
      }
      if (found.task.status !== "done") {
        await this.api.markTaskDone(pair.relnotes.id, taskID(found.task.id), { tag: found.tag, revision: found.task.revision });
        this.live(gen);
      }
      const after = await this.readRequirementTasks(pair.relnotes.id);
      this.live(gen);
      this.publish(gen, {
        busy: false,
        requirements: requirementsWithTasks(after),
        doneRequirements: requirementsDone(after),
        notice: `Marked the task for requirement ${requirement} done.`,
      });
      return true;
    } catch (err) {
      if (err instanceof Halted) return false;
      this.fail(gen, `Marking the task done stopped: ${this.errorMessage(err)}`);
      return false;
    }
  }

  // Suspending kills every in-flight chain at its next fence; requests the
  // server already accepted may have committed, and nothing resumes them
  // automatically. resume lets a new user gesture start.
  suspend(): void {
    this.generation += 1;
    this.suspended = true;
    if (this.state.busy) {
      this.state.busy = false;
      this.state.notice = "The action was interrupted. A request may have completed; inspect the projects before trying another action.";
    }
    this.onChange();
  }

  resume(): void {
    if (this.disposed || !this.suspended) return;
    this.generation += 1;
    this.suspended = false;
  }

  dispose(): void {
    this.generation += 1;
    this.disposed = true;
    this.state.busy = false;
  }
}
