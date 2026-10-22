import assert from "node:assert/strict";
import test from "node:test";
import { readdirSync, readFileSync } from "node:fs";
import type { ApiClient } from "../../api/contracts.ts";
import type { FileApiClient, FileListing } from "../../api/files.ts";
import type { LaneApiClient, TmRun, TmTask } from "../../api/tasks.ts";
import type { Project } from "../../api/projects.ts";
import type { SessionSummary } from "../../api/sessions.ts";
import { ApiError } from "../../api/http.ts";
import {
  TutorialStarterModel,
  buildStarterBundle,
  chooseStarterNames,
  parseStarterManifest,
  requirementsWithTasks,
  rewriteSiblingReferences,
  starterCondition,
  IMPLEMENTED_LESSON_CONDITIONS,
  starterManifestPath,
  starterTasks,
  type StarterBundle,
} from "./starter.ts";

import { LESSON_CONDITIONS } from "./lessons.ts";

const startersDir = new URL("./starters/", import.meta.url);

function bundleFromDisk(): StarterBundle {
  const files: Record<string, string> = {};
  for (const project of ["relnotes", "ccparse"]) {
    for (const name of readdirSync(new URL(`${project}/`, startersDir))) {
      files[`./starters/${project}/${name}`] = readFileSync(new URL(`${project}/${name}`, startersDir), "utf8");
    }
  }
  return buildStarterBundle(files);
}

const bundle = bundleFromDisk();
const paths = (project: "relnotes" | "ccparse") => bundle[project].map((f) => f.path);

// --- Pure helpers ---

test("the bundle holds both projects, flat, with the files the lessons name", () => {
  for (const file of ["PRD.md", "relnotes.mjs", "relnotes.test.mjs", "breaking-marker.test.mjs", "ccparse.pin", "LICENSE"]) {
    assert.ok(paths("relnotes").includes(file), file);
  }
  for (const file of ["index.mjs", "index.test.mjs", "package.json", "LICENSE"]) assert.ok(paths("ccparse").includes(file), file);
  assert.throws(() => buildStarterBundle({ "./starters/other/x.mjs": "" }), /not directly inside/);
  assert.throws(() => buildStarterBundle({ "./starters/ccparse/a": "" }), /relnotes has no files/);
});

test("rewriting names the sibling that exists and nothing else", () => {
  const source = bundle.relnotes.find((f) => f.path === "relnotes.mjs")!.content;
  assert.equal(rewriteSiblingReferences(source, "ccparse"), source);
  const renamed = rewriteSiblingReferences(source, "ccparse-2");
  assert.equal(renamed.includes("../ccparse/"), false);
  assert.equal(renamed.includes("../ccparse "), false);
  assert.ok(renamed.includes('"../ccparse-2/index.mjs"'));
  assert.ok(renamed.includes('"../ccparse-2/package.json"'));
  assert.ok(renamed.includes("ccparse.pin"), "the pin file keeps its name");
  assert.equal(rewriteSiblingReferences('a "../ccparse" b ../ccparse-3 c', "ccparse-2"), 'a "../ccparse-2" b ../ccparse-3 c');
});

test("a pair takes the plain names, else one suffix for both", () => {
  assert.deepEqual(chooseStarterNames(new Set()), { relnotes: "relnotes", ccparse: "ccparse", suffix: "" });
  assert.deepEqual(chooseStarterNames(new Set(["relnotes"])), { relnotes: "relnotes-2", ccparse: "ccparse-2", suffix: "-2" });
  assert.deepEqual(chooseStarterNames(new Set(["ccparse", "relnotes-2"])), { relnotes: "relnotes-3", ccparse: "ccparse-3", suffix: "-3" });
  const all = new Set<string>();
  for (let n = 1; n <= 99; n++) all.add(`relnotes${n === 1 ? "" : `-${n}`}`);
  assert.equal(chooseStarterNames(all), null);
});

test("requirements are covered by their by-hand task or by generated tasks in order", () => {
  const task = (id: string, title: string): TmTask => ({ id, title, status: "pending" });
  assert.deepEqual([...requirementsWithTasks([])], []);
  assert.deepEqual([...requirementsWithTasks([task("1", "Whatever the model called it")])], [1]);
  assert.deepEqual([...requirementsWithTasks([task("1", "A"), task("2", "B"), task("3", "C")])].sort(), [1, 2]);
  assert.deepEqual([...requirementsWithTasks([task("1", starterTasks[2]!.title)])], [2]);
  assert.deepEqual([...requirementsWithTasks([task("1", starterTasks[1]!.title), task("2", starterTasks[2]!.title)])].sort(), [1, 2]);
});

test("conditions the model cannot tell are false", () => {
  const pair = { relnotes: {} as Project, ccparse: {} as Project };
  const base = { pair: null, identitiesKnown: false, defaultIdentityId: null, requirements: new Set<number>() };
  const call = (name: string, arg: string | null = null) => ({ name, arg });
  assert.equal(starterCondition(base, call("no_git_identity")), false, "unknown is not missing");
  assert.equal(starterCondition({ ...base, identitiesKnown: true }, call("no_git_identity")), true);
  assert.equal(starterCondition({ ...base, identitiesKnown: true, defaultIdentityId: "x" }, call("no_git_identity")), false);
  assert.equal(starterCondition(base, call("starter_exists")), false);
  assert.equal(starterCondition({ ...base, pair }, call("starter_exists")), true);
  assert.equal(starterCondition({ ...base, pair }, call("no_task", "requirement=2")), true);
  assert.equal(starterCondition({ ...base, pair, requirements: new Set([2]) }, call("no_task", "requirement=2")), false);
  assert.equal(starterCondition(base, call("no_task", "requirement=2")), false, "no starter, no claim about its tasks");
  for (const name of ["unknown_condition", "agent_needs_login", "no_review_app"]) assert.equal(starterCondition({ ...base, pair }, call(name)), false, name);
});

test("the manifest parses only in its exact shape", () => {
  const good = { version: 1, relnotes: { projectId: "a", canonicalPath: "/p/relnotes" }, ccparse: { projectId: "b", canonicalPath: "/p/ccparse" } };
  assert.deepEqual(parseStarterManifest(JSON.stringify(good)), good);
  for (const bad of [
    { ...good, version: 2 },
    { ...good, extra: 1 },
    { ...good, relnotes: { projectId: "a" } },
    { ...good, ccparse: { ...good.ccparse, canonicalPath: "" } },
    { ...good, relnotes: { ...good.relnotes, extra: 1 } },
    [good],
  ]) assert.equal(parseStarterManifest(JSON.stringify(bad)), null, JSON.stringify(bad).slice(0, 40));
  assert.equal(parseStarterManifest("{"), null);
  assert.equal(parseStarterManifest(" ".repeat(9000)), null);
});

// --- The model, against fakes that record every call ---

interface Call {
  method: string;
  args: unknown[];
}

const READ_METHODS = new Set([
  "listProjects", "listProviders", "listGitIdentities", "listProjectDirectories", "listProjectFiles",
  "viewProjectFile", "listTaskmasterTasks", "listTaskmasterPRDs", "getTaskmasterRun",
]);

function project(id: string, path: string): Project {
  return { id, name: path.split("/").pop() ?? id, canonical_path: path, last_used_at: "2026-09-23T00:00:00Z" };
}

function session(projectId: string, provider: string): SessionSummary {
  return {
    id: `s-${projectId}-${provider}`, project_id: projectId, provider, display_name: "", status: "running", generation: 1,
    created_at: "2026-09-23T00:00:00Z", updated_at: "2026-09-23T00:00:00Z", recovery_capability: "none",
    recovery_state: "unavailable", presentation_state: "running",
  } as SessionSummary;
}

interface FakeState {
  calls: Call[];
  providers: string[];
  defaultIdentity: string | null;
  registered: Map<string, Project>;
  directories: Set<string>;
  files: Map<string, Map<string, string>>;
  tasks: TmTask[];
  prds: Set<string>;
  failOn: { method: string; arg?: unknown } | null;
  gates: Map<string, Promise<void>>;
  rootExtra: string | null;
  loseDirectoryResponse: boolean;
  runPolls: number;
  runExit: number | null;
  generated: number;
}

function fakeState(): FakeState {
  return {
    calls: [], providers: ["claude", "opencode"], defaultIdentity: "id-1", registered: new Map(), directories: new Set(),
    files: new Map(), tasks: [], prds: new Set(), failOn: null, gates: new Map(), rootExtra: null,
    loseDirectoryResponse: false, runPolls: 2, runExit: 0, generated: 2,
  };
}

function makeClients(state: FakeState): { api: ApiClient & LaneApiClient; files: FileApiClient } {
  const record = (method: string, args: unknown[]): void => {
    state.calls.push({ method, args });
    const fail = state.failOn;
    if (fail && fail.method === method && (fail.arg === undefined || fail.arg === args[1] || fail.arg === args[0])) {
      state.failOn = null;
      throw new ApiError(500, "boom", `${method} exploded`);
    }
  };
  const gate = (method: string): Promise<void> | null => state.gates.get(method) ?? null;
  const dirFor = (projectID: string): Map<string, string> => {
    const p = state.registered.get(projectID);
    if (!p) throw new ApiError(404, "not_found", "no such project");
    return state.files.get(p.canonical_path)!;
  };
  let polls = 0;
  const run = (status: string): TmRun => ({
    id: "run-1", project_id: "p-2", argv: [], prd: "PRD.md", status, exit_code: status === "running" ? null : state.runExit, started_at: "t",
  });

  const api = {
    listProjects() { record("listProjects", []); return [...state.registered.values()]; },
    listProviders() { record("listProviders", []); return state.providers; },
    listGitIdentities() {
      record("listGitIdentities", []);
      return {
        profiles: [{ id: "id-1", name: "Op", email: "op@example.com", revision: 1, created_at: "", updated_at: "" }],
        default_profile_id: state.defaultIdentity, revision: 1,
      };
    },
    listProjectDirectories(path?: string) {
      record("listProjectDirectories", path === undefined ? [] : [path]);
      const names = [...state.directories].map((full) => full.slice(full.lastIndexOf("/") + 1));
      return { current_path: "/srv/projects", parent_path: null, directories: names.map((name) => ({ name, path: `/srv/projects/${name}` })) };
    },
    async createProjectDirectory(path: string, name: string) {
      record("createProjectDirectory", [path, name]);
      await gate("createProjectDirectory");
      const full = `${path}/${name}`;
      if (state.directories.has(full)) throw new ApiError(409, "exists", "directory already exists");
      state.directories.add(full);
      state.files.set(full, new Map());
      if (state.loseDirectoryResponse) throw new Error("directory response lost");
      return { path: full };
    },
    async createProject(name: string, path: string) {
      record("createProject", [name, path]);
      await gate("createProject");
      const p = project(`p-${state.registered.size + 1}`, path);
      state.registered.set(p.id, p);
      dirFor(p.id).set(".git", "");
      if (state.rootExtra) dirFor(p.id).set(state.rootExtra, "stray");
      return p;
    },
    async deleteProject(projectID: string) { record("deleteProject", [projectID]); },
    async createSession(projectID: string, provider: string) {
      record("createSession", [projectID, provider]);
      return session(projectID, provider);
    },
    async commitStarter(projectID: string, filePaths: string[]) {
      record("commitStarter", [projectID, filePaths]);
      return { commit: "abc", created: true };
    },
    async initializeTaskmaster(projectID: string) {
      record("initializeTaskmaster", [projectID]);
      return { path: "/store", tag: "master", already_initialized: false };
    },
    listTaskmasterTasks(projectID: string) { record("listTaskmasterTasks", [projectID]); return { master: { tasks: [...state.tasks] } }; },
    listTaskmasterPRDs(projectID: string) { record("listTaskmasterPRDs", [projectID]); return [...state.prds]; },
    addTaskmasterTask(projectID: string, input: { title: string; description: string; details: string; priority: string }) {
      record("addTaskmasterTask", [projectID, input]);
      const id = String(state.tasks.length + 1);
      state.tasks.push({ id, title: input.title, description: input.description, status: "pending" });
      return { id };
    },
    markTaskDone(projectID: string, taskID: string, input: { tag: string; revision?: number }) {
      record("markTaskDone", [projectID, taskID, input]);
      const task = state.tasks.find((candidate) => String(candidate.id) === taskID);
      if (!task) throw new ApiError(500, "boom", "no such task");
      task.status = "done";
      return { status: "done", lane_closed: false };
    },
    async uploadPrd(projectID: string, name: string, content: string, replace: boolean) {
      record("uploadPrd", [projectID, name, content, replace]);
      state.prds.add(name);
    },
    async generateTasks(projectID: string, prd: string, numTasks?: number, harness?: string) {
      record("generateTasks", [projectID, prd, numTasks, harness]);
      polls = 0;
      return run("running");
    },
    getTaskmasterRun(projectID: string, runID: string) {
      record("getTaskmasterRun", [projectID, runID]);
      polls++;
      if (polls < state.runPolls) return run("running");
      if (state.runExit === 0) {
        for (let i = 0; i < state.generated; i++) state.tasks.push({ id: String(state.tasks.length + 1), title: `Generated ${i + 1}`, status: "pending" });
      }
      return run("exited");
    },
  } as unknown as ApiClient & LaneApiClient;

  const files = {
    listProjectFiles(projectID: string, path: string): FileListing {
      state.calls.push({ method: "listProjectFiles", args: [projectID, path] });
      const names = [...dirFor(projectID).keys()];
      return { path, at_root: path === "", entries: names.map((name) => ({ name, path: name, dir: false, mode: "-rw-r--r--", mtime: "t" })) };
    },
    viewProjectFile(projectID: string, path: string) {
      state.calls.push({ method: "viewProjectFile", args: [projectID, path] });
      const content = dirFor(projectID).get(path);
      if (content === undefined) throw new ApiError(404, "not_found", "no such file");
      return { content };
    },
    async uploadProjectFile(projectID: string, destPath: string, body: Blob) {
      state.calls.push({ method: "uploadProjectFile", args: [projectID, destPath] });
      const fail = state.failOn;
      if (fail && fail.method === "uploadProjectFile" && fail.arg === destPath) {
        state.failOn = null;
        throw new ApiError(500, "boom", "upload exploded");
      }
      dirFor(projectID).set(destPath, await body.text());
      return { path: destPath, size: 0 };
    },
  } as unknown as FileApiClient;
  return { api, files };
}

function makeModel(state = fakeState()) {
  const { api, files } = makeClients(state);
  return { state, model: new TutorialStarterModel(api, files, bundle, { wait: async () => {}, maxPolls: 10 }) };
}

const writes = (state: FakeState): string[] => state.calls.filter((c) => !READ_METHODS.has(c.method)).map((c) => c.method);
const named = (state: FakeState, method: string): Call[] => state.calls.filter((c) => c.method === method);

// A pair as a finished create leaves it, without going through create.
function seeded(state: FakeState, suffix = "") {
  const relnotes = project("p-r", `/srv/projects/relnotes${suffix}`);
  const ccparse = project("p-c", `/srv/projects/ccparse${suffix}`);
  state.registered.set(relnotes.id, relnotes);
  state.registered.set(ccparse.id, ccparse);
  state.files.set(relnotes.canonical_path, new Map([[starterManifestPath, JSON.stringify({
    version: 1,
    relnotes: { projectId: relnotes.id, canonicalPath: relnotes.canonical_path },
    ccparse: { projectId: ccparse.id, canonicalPath: ccparse.canonical_path },
  })]]));
  state.files.set(ccparse.canonical_path, new Map());
  return { relnotes, ccparse };
}

test("create makes both projects, seeds them, commits, writes the manifest last, then starts the agent in relnotes", async () => {
  const { state, model } = makeModel();
  const created = await model.create("claude");
  assert.equal(model.state.error, null);
  assert.equal(model.state.busy, false);
  assert.ok(created);
  assert.equal(created.session?.provider, "claude");
  assert.equal(created.session?.project_id, created.pair.relnotes.id);
  assert.deepEqual(named(state, "createProjectDirectory").map((c) => c.args), [["/srv/projects", "ccparse"], ["/srv/projects", "relnotes"]]);
  assert.equal(created.pair.relnotes.canonical_path, "/srv/projects/relnotes");
  assert.equal(created.pair.ccparse.canonical_path, "/srv/projects/ccparse");
  const uploads = (id: string) => named(state, "uploadProjectFile").filter((c) => c.args[0] === id).map((c) => c.args[1]);
  assert.deepEqual(uploads(created.pair.ccparse.id), paths("ccparse"));
  assert.deepEqual(uploads(created.pair.relnotes.id), [...paths("relnotes"), starterManifestPath]);
  assert.deepEqual(named(state, "commitStarter").map((c) => c.args), [
    [created.pair.ccparse.id, paths("ccparse")],
    [created.pair.relnotes.id, paths("relnotes")],
  ]);
  assert.deepEqual(named(state, "initializeTaskmaster").map((c) => c.args), [[created.pair.relnotes.id]]);
  const prd = named(state, "uploadPrd");
  assert.equal(prd.length, 1);
  assert.deepEqual(prd[0]!.args.slice(0, 2), [created.pair.relnotes.id, "PRD.md"]);
  assert.equal(prd[0]!.args[3], false, "the PRD is never replaced");
  const order = writes(state);
  assert.equal(order.at(-1), "createSession");
  assert.equal(order.at(-2), "uploadProjectFile", "the manifest is the last file written");
  assert.ok(order.lastIndexOf("commitStarter") < order.indexOf("initializeTaskmaster"));
  const manifest = parseStarterManifest(state.files.get("/srv/projects/relnotes")!.get(starterManifestPath)!);
  assert.deepEqual(manifest, {
    version: 1,
    relnotes: { projectId: created.pair.relnotes.id, canonicalPath: "/srv/projects/relnotes" },
    ccparse: { projectId: created.pair.ccparse.id, canonicalPath: "/srv/projects/ccparse" },
  });
  assert.equal(writes(state).includes("deleteProject"), false);
  assert.equal(model.state.pair?.relnotes.id, created.pair.relnotes.id);
});

test("a taken name moves both projects to the same suffix and points relnotes at its sibling", async () => {
  const state = fakeState();
  state.directories.add("/srv/projects/relnotes");
  const { model } = makeModel(state);
  const created = await model.create("claude");
  assert.ok(created);
  assert.deepEqual(named(state, "createProjectDirectory").map((c) => c.args[1]), ["ccparse-2", "relnotes-2"]);
  const source = state.files.get("/srv/projects/relnotes-2")!.get("relnotes.mjs")!;
  assert.ok(source.includes('"../ccparse-2/index.mjs"'));
  assert.equal(source.includes("../ccparse/"), false);
  const plain = bundle.relnotes.find((f) => f.path === "relnotes.mjs")!.content;
  assert.equal(state.files.get("/srv/projects/relnotes-2")!.get("PRD.md"), bundle.relnotes.find((f) => f.path === "PRD.md")!.content);
  assert.notEqual(source, plain);

  const other = fakeState();
  other.directories.add("/srv/projects/ccparse");
  const second = makeModel(other);
  await second.model.create("claude");
  assert.deepEqual(named(other, "createProjectDirectory").map((c) => c.args[1]), ["ccparse-2", "relnotes-2"], "either name taken moves both");
});

test("a registered project counts as taken even when the folder list does not show it", async () => {
  const state = fakeState();
  const old = project("p-old", "/srv/projects/ccparse");
  state.registered.set(old.id, old);
  state.files.set(old.canonical_path, new Map([["mine.txt", "keep"]]));
  const { model } = makeModel(state);
  const created = await model.create("claude");
  assert.ok(created);
  assert.deepEqual(named(state, "createProjectDirectory").map((c) => c.args[1]), ["ccparse-2", "relnotes-2"]);
  assert.equal(named(state, "uploadProjectFile").some((c) => c.args[0] === "p-old"), false, "the existing project is untouched");
  assert.deepEqual([...state.files.get(old.canonical_path)!.keys()], ["mine.txt"]);
});

test("the AGENTS.md registration adds is committed with the starter files", async () => {
  const state = fakeState();
  state.rootExtra = "AGENTS.md";
  const { model } = makeModel(state);
  const created = await model.create("claude");
  assert.equal(model.state.error, null);
  assert.ok(created);
  assert.deepEqual(named(state, "commitStarter").map((c) => c.args), [
    [created.pair.ccparse.id, [...paths("ccparse"), "AGENTS.md"]],
    [created.pair.relnotes.id, [...paths("relnotes"), "AGENTS.md"]],
  ]);
});

test("a new directory that is not empty is never written to", async () => {
  const state = fakeState();
  state.rootExtra = "stray.txt";
  const { model } = makeModel(state);
  assert.equal(await model.create("claude"), null);
  assert.ok(model.state.error?.includes("not empty"));
  assert.ok(model.state.error?.includes("Left in place"));
  assert.deepEqual(writes(state), ["createProjectDirectory", "createProject"]);
  assert.equal(model.state.pair, null);
});

test("without a default Git identity nothing is created", async () => {
  const state = fakeState();
  state.defaultIdentity = null;
  const { model } = makeModel(state);
  assert.equal(await model.create("claude"), null);
  assert.ok(model.state.error?.includes("Git identity"));
  assert.equal(model.state.identitiesKnown, true);
  assert.equal(model.state.defaultIdentityId, null);
  assert.deepEqual(writes(state), []);
});

test("an agent this deployment lacks stops the create before any write", async () => {
  const { state, model } = makeModel();
  assert.equal(await model.create("nonesuch"), null);
  assert.ok(model.state.error?.includes("not available"));
  assert.deepEqual(writes(state), []);
});

test("with no agent picked the projects are made and no session starts", async () => {
  const { state, model } = makeModel();
  const created = await model.create(null);
  assert.ok(created);
  assert.equal(created.session, null);
  assert.equal(writes(state).includes("createSession"), false);
  assert.ok(model.state.notice?.includes("No agent was chosen"));
  assert.equal(model.state.error, null);
});

test("a failure part way says what is in place, deletes nothing, and the next try takes new names", async () => {
  const state = fakeState();
  state.failOn = { method: "uploadProjectFile", arg: "relnotes.mjs" };
  const { model } = makeModel(state);
  assert.equal(await model.create("claude"), null);
  const message = model.state.error ?? "";
  assert.ok(message.includes("uploading the starter files (relnotes)"), message);
  assert.ok(message.includes("nothing was deleted"), message);
  assert.ok(message.includes("/srv/projects/ccparse") && message.includes("/srv/projects/relnotes"), message);
  assert.equal(writes(state).includes("deleteProject"), false);
  assert.equal(writes(state).includes("commitStarter") && named(state, "commitStarter").length, 1, "relnotes was never committed");
  assert.equal(state.files.get("/srv/projects/relnotes")!.has(starterManifestPath), false, "no manifest for an incomplete project");
  assert.equal(model.state.pair, null);
  assert.equal(model.state.busy, false);

  state.calls.length = 0;
  const retry = await model.create("claude");
  assert.ok(retry);
  assert.deepEqual(named(state, "createProjectDirectory").map((c) => c.args[1]), ["ccparse-2", "relnotes-2"]);
  assert.equal(named(state, "uploadProjectFile").some((c) => c.args[0] === "p-1" || c.args[0] === "p-2"), false, "the first attempt's folders are not touched again");
});

test("a commit that fails stops before the tasks are set up and before the manifest", async () => {
  const state = fakeState();
  state.failOn = { method: "commitStarter" };
  const { model } = makeModel(state);
  assert.equal(await model.create("claude"), null);
  assert.ok(model.state.error?.includes("making the starter commit"));
  assert.equal(named(state, "initializeTaskmaster").length, 0);
  assert.equal(named(state, "uploadProjectFile").some((c) => c.args[1] === starterManifestPath), false);
});

test("lost responses name the step and what to check", async () => {
  const dir = fakeState();
  dir.loseDirectoryResponse = true;
  const first = makeModel(dir);
  assert.equal(await first.model.create("claude"), null);
  assert.ok(first.model.state.error?.includes("creating the folder (ccparse)"));
  assert.ok(first.model.state.error?.includes("No registration request was sent"));
  assert.equal(named(dir, "createProject").length, 0);

  const reg = fakeState();
  reg.failOn = { method: "createProject" };
  const second = makeModel(reg);
  assert.equal(await second.model.create("claude"), null);
  assert.ok(second.model.state.error?.includes("registering the project (ccparse)"));
  assert.ok(second.model.state.error?.includes("may already be registered"));
  assert.equal(named(reg, "uploadProjectFile").length, 0);

  const session = fakeState();
  session.failOn = { method: "createSession" };
  const third = makeModel(session);
  const result = await third.model.create("claude");
  assert.equal(result?.session, null, "the projects exist even though the agent did not start");
  assert.ok(third.model.state.error?.includes("starting the agent"));
  assert.ok(third.model.state.pair, "the finished pair is still known");
});

test("suspending fences an in-flight create: no further request is sent", async () => {
  const state = fakeState();
  let release!: () => void;
  state.gates.set("createProject", new Promise<void>((resolve) => { release = resolve; }));
  const { model } = makeModel(state);
  const pending = model.create("claude");
  while (!state.calls.some((c) => c.method === "createProject")) await new Promise((resolve) => setTimeout(resolve, 0));
  model.suspend();
  const before = writes(state).length;
  release();
  assert.equal(await pending, null);
  assert.equal(writes(state).length, before, "nothing after the fence");
  assert.equal(model.state.busy, false);
  assert.ok(model.state.notice?.includes("interrupted"));
  assert.equal(model.state.pair, null);
  // A new gesture works after resume.
  model.resume();
  assert.ok(await model.create("claude"));

  const disposed = makeModel();
  const gate = new Promise<void>((resolve) => { release = resolve; });
  disposed.state.gates.set("createProjectDirectory", gate);
  const run = disposed.model.create("claude");
  while (!disposed.state.calls.some((c) => c.method === "createProjectDirectory")) await new Promise((resolve) => setTimeout(resolve, 0));
  disposed.model.dispose();
  release();
  assert.equal(await run, null);
  assert.deepEqual(writes(disposed.state), ["createProjectDirectory"]);
});

test("only one action runs at a time", async () => {
  const state = fakeState();
  let release!: () => void;
  state.gates.set("createProjectDirectory", new Promise<void>((resolve) => { release = resolve; }));
  const { model } = makeModel(state);
  const first = model.create("claude");
  while (!state.calls.some((c) => c.method === "createProjectDirectory")) await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(await model.create("claude"), null);
  await model.generateTasks(2);
  await model.addTask(1);
  assert.equal(named(state, "createProjectDirectory").length, 1);
  release();
  assert.ok(await first);
});

test("observe recognises the pair by its manifest and rejects a copied one", async () => {
  const state = fakeState();
  const { relnotes } = seeded(state);
  const { model } = makeModel(state);
  await model.observe();
  assert.equal(model.state.pair?.relnotes.id, relnotes.id);
  assert.equal(model.state.pair?.ccparse.id, "p-c");
  assert.equal(model.state.defaultIdentityId, "id-1");
  assert.deepEqual(writes(state), [], "observing writes nothing");

  // The same manifest copied into a different directory binds nothing.
  const copy = fakeState();
  const original = seeded(copy);
  const clone = project("p-x", "/srv/projects/relnotes-2");
  copy.registered.set(clone.id, clone);
  copy.registered.delete(original.relnotes.id);
  copy.files.set(clone.canonical_path, new Map([[starterManifestPath, state.files.get(original.relnotes.canonical_path)!.get(starterManifestPath)!]]));
  const second = makeModel(copy);
  await second.model.observe();
  assert.equal(second.model.state.pair, null);

  // A removed ccparse breaks the pair.
  state.registered.delete("p-c");
  await model.observe();
  assert.equal(model.state.pair, null);
});

test("generateTasks adds the PRD only if missing, asks for the count, and reports the run's exit", async () => {
  const state = fakeState();
  seeded(state);
  const { model } = makeModel(state);
  await model.observe();
  await model.generateTasks(2);
  assert.equal(model.state.error, null);
  assert.deepEqual(named(state, "generateTasks").map((c) => c.args), [["p-r", "PRD.md", 2, undefined]]);
  assert.equal(named(state, "uploadPrd").length, 1, "the PRD was missing and is added");
  assert.ok(named(state, "getTaskmasterRun").length >= 1, "progress comes from the run record");
  assert.equal(model.state.notice, "Generated 2 tasks.");
  assert.deepEqual([...model.state.requirements].sort(), [1, 2]);
  assert.equal(model.state.busy, false);

  state.calls.length = 0;
  state.tasks.length = 0;
  await model.generateTasks(2);
  assert.equal(named(state, "uploadPrd").length, 0, "an existing PRD is left alone");

  const failing = fakeState();
  seeded(failing);
  failing.runExit = 3;
  const bad = makeModel(failing);
  await bad.model.observe();
  await bad.model.generateTasks(2);
  assert.ok(bad.model.state.error?.includes("exit code 3"));
  assert.equal(bad.model.state.notice, null);

  const running = fakeState();
  seeded(running);
  running.runPolls = 1000;
  const slow = makeModel(running);
  await slow.model.observe();
  await slow.model.generateTasks(2);
  assert.ok(slow.model.state.notice?.includes("still running"));
  assert.equal(slow.model.state.busy, false);
});

test("generateTasks says when a generation is already running, and refuses without a verified pair", async () => {
  const state = fakeState();
  seeded(state);
  const { model } = makeModel(state);
  await model.observe();
  state.failOn = { method: "generateTasks" };
  await model.generateTasks(2);
  assert.ok(model.state.error?.includes("Generating tasks stopped"));
  assert.equal(model.state.busy, false);

  const bare = makeModel();
  await bare.model.generateTasks(2);
  assert.ok(bare.model.state.error?.includes("not set up"));
  assert.deepEqual(writes(bare.state), []);
});

test("a pair whose project was removed is refused and forgotten", async () => {
  const state = fakeState();
  seeded(state);
  const { model } = makeModel(state);
  await model.observe();
  state.registered.delete("p-c");
  await model.generateTasks(2);
  assert.ok(model.state.error?.includes("no longer registered"));
  assert.equal(model.state.pair, null);
  assert.deepEqual(writes(state), []);
});

test("addTask sends the fixed text for its requirement once, and the second needs the first", async () => {
  const state = fakeState();
  seeded(state);
  const { model } = makeModel(state);
  await model.observe();

  await model.addTask(2);
  assert.ok(model.state.error?.includes("requirement 1 first"));
  assert.equal(named(state, "addTaskmasterTask").length, 0);

  await model.addTask(1);
  assert.deepEqual(named(state, "addTaskmasterTask").map((c) => c.args), [["p-r", starterTasks[1]]]);
  assert.deepEqual([...model.state.requirements], [1]);

  await model.addTask(1);
  assert.equal(named(state, "addTaskmasterTask").length, 1, "an existing requirement is not added twice");
  assert.ok(model.state.notice?.includes("already has a task"));

  await model.addTask(2);
  assert.deepEqual(named(state, "addTaskmasterTask").map((c) => (c.args[1] as { title: string }).title), [starterTasks[1]!.title, starterTasks[2]!.title]);
  assert.ok(starterTasks[2]!.details.includes("Depends on the task for requirement 1"));
  assert.deepEqual([...model.state.requirements].sort(), [1, 2]);

  await model.addTask(3);
  assert.ok(model.state.error?.includes("no requirement 3"));
});

test("markRequirementDone writes the task's status once, at the revision it read, with no lane", async () => {
  const state = fakeState();
  seeded(state);
  const { model } = makeModel(state);
  await model.observe();
  await model.markRequirementDone(1);
  assert.ok(model.state.error?.includes("no task for requirement 1"), model.state.error ?? "");
  assert.equal(named(state, "markTaskDone").length, 0);

  await model.addTask(1);
  state.tasks[0]!.revision = 4;
  assert.equal(await model.markRequirementDone(1), true);
  assert.deepEqual(named(state, "markTaskDone").map((c) => c.args), [["p-r", "1", { tag: "master", revision: 4 }]]);
  assert.deepEqual([...model.state.doneRequirements], [1]);
  assert.ok(model.state.notice?.includes("Marked the task for requirement 1 done"));

  assert.equal(await model.markRequirementDone(1), true);
  assert.equal(named(state, "markTaskDone").length, 1, "a done task is not written again");
});

test("markRequirementDone reports the refusal and changes no state it did not read", async () => {
  const state = fakeState();
  seeded(state);
  const { model } = makeModel(state);
  await model.observe();
  await model.addTask(1);
  state.failOn = { method: "markTaskDone" };
  assert.equal(await model.markRequirementDone(1), false);
  assert.ok(model.state.error?.includes("markTaskDone exploded"));
  assert.equal(model.state.doneRequirements.size, 0);
});

test("startAgent opens the picked agent in the chosen practice project", async () => {
  const state = fakeState();
  seeded(state);
  const { model } = makeModel(state);
  await model.observe();
  const opened = await model.startAgent("ccparse", "claude");
  assert.equal(opened?.project_id, "p-c");
  assert.deepEqual(named(state, "createSession").map((c) => c.args), [["p-c", "claude"]]);
  assert.equal(await model.startAgent("ccparse", "nonesuch"), null);
  assert.ok(model.state.error?.includes("not available"));
  assert.equal(named(state, "createSession").length, 1);

  const bare = makeModel();
  assert.equal(await bare.model.startAgent("ccparse", "claude"), null);
  assert.ok(bare.model.state.error?.includes("not set up"));
  assert.deepEqual(writes(bare.state), []);
});

test("addPrd leaves an existing PRD as it is", async () => {
  const state = fakeState();
  seeded(state);
  const { model } = makeModel(state);
  await model.observe();
  await model.addPrd();
  assert.equal(named(state, "uploadPrd").length, 1);
  await model.addPrd();
  assert.equal(named(state, "uploadPrd").length, 1);
  assert.ok(model.state.notice?.includes("left it as it is") || model.state.notice?.includes("left as it is"));
});

test("resume without a suspension does not cancel work in flight", async () => {
  const state = fakeState();
  let release!: () => void;
  state.gates.set("createProject", new Promise<void>((resolve) => { release = resolve; }));
  const { model } = makeModel(state);
  const pending = model.create("claude");
  while (!state.calls.some((c) => c.method === "createProject")) await new Promise((resolve) => setTimeout(resolve, 0));
  model.resume();
  release();
  assert.ok(await pending);
});

test("background task reads run one at a time and a stale answer never overwrites a newer one", async () => {
  const state = fakeState();
  const { api, files } = makeClients(state);
  const model = new TutorialStarterModel(api, files, bundle, { wait: async () => {}, maxPolls: 10 });
  const { relnotes, ccparse } = seeded(state);
  await model.observe();
  assert.ok(model.state.pair);
  const pending: Array<(tasks: TmTask[]) => void> = [];
  let reads = 0;
  (api as unknown as { listTaskmasterTasks: (id: string) => Promise<unknown> }).listTaskmasterTasks = () => {
    reads += 1;
    return new Promise((resolve) => pending.push((tasks) => resolve({ master: { tasks } })));
  };
  const task = (status: string): TmTask => ({ id: "1", title: "Generated 1", status });
  const first = model.refreshTasks();
  const second = model.refreshTasks();
  const third = model.refreshTasks();
  await Promise.resolve();
  assert.equal(reads, 1, "a read still pending blocks the next");
  // The Tasks panel reports a newer answer while the background read is out.
  model.noteTasks([task("done")]);
  assert.equal(model.state.doneRequirements.has(1), true);
  pending[0]!([task("pending")]);
  await Promise.all([first, second, third]);
  assert.equal(model.state.doneRequirements.has(1), true, "the older pending answer was discarded");
  const next = model.refreshTasks();
  await Promise.resolve();
  assert.equal(reads, 2, "the next read starts once the earlier one has settled");
  pending[1]!([task("done")]);
  await next;
  assert.ok(relnotes && ccparse);
});

test("an observation whose task answer is still out never overwrites a newer one", async () => {
  const state = fakeState();
  const { api, files } = makeClients(state);
  const model = new TutorialStarterModel(api, files, bundle, { wait: async () => {}, maxPolls: 10 });
  seeded(state);
  await model.observe();
  const task = (status: string): TmTask => ({ id: "1", title: "Generated 1", status });
  let release!: (tasks: TmTask[]) => void;
  (api as unknown as { listTaskmasterTasks: (id: string) => Promise<unknown> }).listTaskmasterTasks = () =>
    new Promise((resolve) => { release = (tasks) => resolve({ master: { tasks } }); });
  const observing = model.observe();
  while (release === undefined) await new Promise((resolve) => setTimeout(resolve, 0));
  model.noteTasks([task("done")]);
  assert.equal(model.state.doneRequirements.has(1), true);
  release([task("pending")]);
  await observing;
  assert.equal(model.state.doneRequirements.has(1), true, "the older answer left the done task alone");
  assert.ok(model.state.pair, "the rest of the observation still landed");
});

test("every condition accepted by the lesson parser has an implementation", () => {
  assert.deepEqual([...LESSON_CONDITIONS].sort(), [...IMPLEMENTED_LESSON_CONDITIONS].sort());
});

test("generation fallback follows the picked agent's adapter", () => {
  const state = { pair: null, identitiesKnown: false, defaultIdentityId: null, requirements: new Set<number>() };
  const call = { name: "agent_cannot_generate_tasks", arg: null };
  for (const agent of ["claude", "codex", "kimi", "omp", null]) {
    assert.equal(starterCondition(state, call, { agent }), false, String(agent));
  }
  for (const agent of ["shell", "terminal", "opencode", "pi", "future-agent"]) {
    assert.equal(starterCondition(state, call, { agent }), true, agent);
  }
  assert.equal(starterCondition(state, { name: "agent_needs_login", arg: null }, { agentNeedsLogin: true }), true);
  assert.equal(starterCondition(state, { name: "no_review_app", arg: null }, { reviewAppAvailable: false }), true);
});

test("tutorial generation passes the picked agent to the API", async () => {
  for (const agent of ["claude", "codex", "kimi", "omp"]) {
    const state = fakeState();
    seeded(state);
    const { model } = makeModel(state);
    await model.observe();
    await model.generateTasks(2, agent);
    assert.deepEqual(named(state, "generateTasks").map((c) => c.args), [["p-r", "PRD.md", 2, agent]]);
  }
});
