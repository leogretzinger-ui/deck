// The application-facing API contract. HttpApiClient implements it; tests and
// design previews supply fixture clients. Responses remain server-owned state.
import type { ConsoleSettings, SidebarLayout, TutorialPreference, TutorialRecord } from "./settings.ts";
import type {
  Project,
  GitIdentityProfile,
  GitIdentityCatalog,
  ProjectWriteResult,
  Inspection,
  CloneJob,
  CloneStartRequest,
  CreateProjectOptions,
  ProjectGitIdentitySelection,
  ProjectDirectoryListing,
  StarterCommitResult,
} from "./projects.ts";
import type { SessionSummary, SessionDetail, RecoverResult, HarnessUsageReport } from "./sessions.ts";
import type {
  TaskmasterInitResult,
  TaskmasterInfo,
  TmTask,
  TaskmasterTasks,
  TmRun,
  AddTaskInput,
  AddTaskResult,
} from "./tasks.ts";
import type { ChatLedger, ChatIndex, ChatsStatus } from "./chats.ts";
import type { ProjectPreviewEntry, ProjectPreviewSettings, ProjectPreviewCheck } from "./project-preview.ts";

export interface ApiClient {
  logout(): Promise<void>;
  getSettings(): Promise<ConsoleSettings>;
  saveSettings(next: ConsoleSettings): Promise<ConsoleSettings>;
  getLayout(): Promise<SidebarLayout | null>;
  saveLayout(next: SidebarLayout): Promise<SidebarLayout>;
  // The session the user last opened, remembered on the server so a
  // browser on another device opens the same one. Null when nothing is
  // remembered; saving null forgets it.
  getLastSession(): Promise<string | null>;
  saveLastSession(id: string | null): Promise<void>;
  // The tutorial preference read: null when no record was ever written —
  // the UI's first-use signal. A stored record whose version this build
  // does not speak is served as read, so the caller can preserve it.
  getTutorial(signal?: AbortSignal): Promise<TutorialRecord | null>;
  saveTutorial(next: TutorialPreference, signal?: AbortSignal): Promise<TutorialRecord>;
  listProjects(): Promise<Project[]>;
  listProviders(): Promise<readonly string[]>;
  getHarnessUsage(projectID?: string): Promise<HarnessUsageReport>;
  listGitIdentities(): Promise<GitIdentityCatalog>;
  createGitIdentity(name: string, email: string): Promise<GitIdentityProfile>;
  updateGitIdentity(profileID: string, expectedRevision: number, name: string, email: string): Promise<GitIdentityProfile>;
  setDefaultGitIdentity(profileID: string, expectedCatalogRevision: number, expectedProfileRevision: number): Promise<GitIdentityCatalog>;
  deleteGitIdentity(profileID: string, expectedRevision: number, catalogRevision: number, replacement?: GitIdentityProfile): Promise<void>;
  listProjectDirectories(path?: string): Promise<ProjectDirectoryListing>;
  createProjectDirectory(path: string, name: string): Promise<{ path: string }>;
  createProject(name: string, path: string, profile: GitIdentityProfile | null, options?: CreateProjectOptions): Promise<ProjectWriteResult>;
  inspectProject(input: string, base?: string): Promise<Inspection>;
  startClone(request: CloneStartRequest): Promise<CloneJob>;
  listClones(): Promise<CloneJob[]>;
  getClone(id: string): Promise<CloneJob>;
  cancelClone(id: string): Promise<CloneJob>;
  resumeClone(id: string): Promise<CloneJob>;
  discardClone(id: string): Promise<void>;
  setProjectGitIdentity(projectID: string, selection: ProjectGitIdentitySelection): Promise<ProjectWriteResult>;
  deleteProject(projectID: string): Promise<void>;
  renameProject(projectID: string, name: string): Promise<void>;
  // Makes the first commit of a project the tutorial just seeded: exactly the
  // listed files, a fixed message, the author Deck already wrote. Refused when
  // the repository has any other history.
  commitStarter(projectID: string, paths: readonly string[]): Promise<StarterCommitResult>;
  listSessions(projectID: string): Promise<SessionSummary[]>;
  acknowledgeCompletion?(sessionID: string, generation: number, revision: string): Promise<void>;
  createSession(projectID: string, provider: string): Promise<SessionSummary>;
  // recoverSession is the user's explicit retry (POST recover): the
  // same row revives in place and the result's outcome names what
  // committed — "resumed", or "relaunched" for a row that recorded no
  // conversation to resume. A resume the provider does not continue is a
  // 409 carrying the server's specific refusal sentence; nothing is
  // relaunched in its place.
  recoverSession(sessionID: string): Promise<RecoverResult>;
  // sessionScreen is the last screen of a session's retained dead pane, as
  // bytes to show above the recovery card: the provider's own words about
  // why it stopped. Nothing derives state from it. Resolves null when no
  // screen is retained.
  sessionScreen(sessionID: string): Promise<string | null>;
  getTaskmasterInfo(projectID: string): Promise<TaskmasterInfo>;
  initializeTaskmaster(projectID: string): Promise<TaskmasterInitResult>;
  listTaskmasterTasks(projectID: string): Promise<TaskmasterTasks>;
  getTaskmasterTask(projectID: string, taskID: string): Promise<TmTask>;
  addTaskmasterTask(projectID: string, input: AddTaskInput): Promise<AddTaskResult>;
  listTaskmasterPRDs(projectID: string): Promise<string[]>;
  // numTasks asks the engine for that many tasks (1 to 50); absent keeps its default.
  generateTasks(projectID: string, prd: string, numTasks?: number, harness?: string): Promise<TmRun>;
  uploadPrd(projectID: string, name: string, content: string, replace: boolean): Promise<void>;
  listTaskmasterRuns(projectID: string): Promise<TmRun[]>;
  // The chat ledger read path. A 404 with code no_agent_handle
  // is a STATE: the session's harness has no chat handle.
  getSessionChats(sessionID: string): Promise<ChatLedger>;
  listChats(): Promise<ChatIndex>;
  // Optional so fixture clients that never ask do not grow a method; the
  // caller treats its absence as "unknown".
  getChatsStatus?(signal?: AbortSignal): Promise<ChatsStatus>;
  getTaskmasterRun(projectID: string, runID: string): Promise<TmRun>;
  getSession(sessionID: string): Promise<SessionDetail>;
  deleteSession(sessionID: string): Promise<void>;
  renameSession(sessionID: string, name: string): Promise<void>;
  // The project app preview entry: a one-use ticket and the exact
  // URL to submit it to. Optional so fixture clients that never serve a
  // preview do not grow a method; HttpApiClient always implements it and
  // the caller treats its absence as "not configured".
  projectPreview?(projectID: string): Promise<ProjectPreviewEntry>;
  getProjectPreviewSettings?(projectID: string, signal?: AbortSignal): Promise<ProjectPreviewSettings>;
  enableProjectPreview?(projectID: string): Promise<ProjectPreviewSettings>;
  disableProjectPreview?(projectID: string): Promise<ProjectPreviewSettings>;
  checkProjectPreview?(projectID: string): Promise<ProjectPreviewCheck>;
  setSessionColor(sessionID: string, color: string | null): Promise<void>;
}
