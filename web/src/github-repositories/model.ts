import type { GitHubStatus } from "../github-connect/client.ts";
import type { GitHubRepositoriesClient, GitHubRepository } from "./client.ts";
export { githubRemoteIdentity } from "./client.ts";
export type { GitHubRemoteIdentity, GitHubRepositoriesClient, GitHubRepository } from "./client.ts";

export type GitHubRepositoriesPhase = "idle" | "loading" | "ready" | "error" | "checking" | "disposed";

export interface GitHubRepositoriesState {
  phase: GitHubRepositoriesPhase;
  // outcome is the last server outcome verbatim: the connect status outcome
  // when no listing was read, the listing's own outcome otherwise.
  outcome: string;
  account: string | null;
  repositories: GitHubRepository[];
  query: string;
  owner: string | null;
  selected: GitHubRepository | null;
  // pendingId is the repository id a selection is revalidating, while it is
  // revalidating. A row the server has not confirmed is never "selected".
  pendingId: number | null;
  // Filtering covers the fetched pages; SSO may still hide organizations.
  complete: boolean;
  ssoPartial: boolean;
  message: string;
}

const INITIAL: GitHubRepositoriesState = {
  phase: "idle", outcome: "", account: null, repositories: [], query: "",
  owner: null, selected: null, pendingId: null, complete: false, ssoPartial: false, message: "",
};

// The narrow slice of the connect client this model reads: the native
// status is the authority on whether an account exists at all. Only an
// authenticated outcome with a login justifies a listing request.
export interface GitHubAccountStatus {
  status(signal: AbortSignal): Promise<GitHubStatus>;
}

export function filterRepositories(repositories: readonly GitHubRepository[], query: string, owner: string | null): GitHubRepository[] {
  const needle = query.trim().toLowerCase();
  return repositories.filter(repository => {
    if (owner !== null && repository.owner !== owner) return false;
    if (needle === "") return true;
    return repository.full_name.toLowerCase().includes(needle) ||
      repository.name.toLowerCase().includes(needle) ||
      repository.description.toLowerCase().includes(needle);
  });
}

export class GitHubRepositoriesModel {
  state: GitHubRepositoriesState = { ...INITIAL };
  private accountStatus: GitHubAccountStatus;
  private client: GitHubRepositoriesClient;
  private changed: () => void;
  private generation = 0;
  private request: AbortController | null = null;

  constructor(accountStatus: GitHubAccountStatus, client: GitHubRepositoriesClient, changed: () => void = () => {}) {
    this.accountStatus = accountStatus;
    this.client = client;
    this.changed = changed;
  }

  get busy(): boolean { return this.state.phase === "loading" || this.state.phase === "checking"; }

  // Owner options derive from the returned rows, never from a remembered
  // list: an owner that no row names cannot be filtered by.
  get owners(): string[] {
    return [...new Set(this.state.repositories.map(repository => repository.owner))].sort();
  }

  get visible(): GitHubRepository[] {
    return filterRepositories(this.state.repositories, this.state.query, this.state.owner);
  }

  refresh(): Promise<void> { return this.load(true); }

  load(refresh = false): Promise<void> { return this.read(refresh); }

  // Sign-in already checked this account. The repository endpoint checks it
  // again before returning any rows, so no second status read is needed here.
  connected(login: string): Promise<void> { return this.read(true, login); }

  private async read(refresh: boolean, connectedLogin?: string): Promise<void> {
    // A new load supersedes any pass in flight, so the newest request's answer
    // wins; the fresh listing decides what exists and drops a pending selection.
    if (this.state.phase === "disposed") return;
    const generation = ++this.generation;
    const request = new AbortController();
    this.request?.abort();
    this.request = request;
    this.set({ phase: "loading", repositories: [], selected: null, complete: false, ssoPartial: false, pendingId: null, message: refresh ? "Refreshing GitHub repositories…" : "Reading GitHub repositories…" });
    const timeout = setTimeout(() => request.abort(), 75000);
    try {
      let account = connectedLogin;
      if (!account) {
        const status = await this.accountStatus.status(request.signal);
        if (!this.current(generation)) return;
        if (request.signal.aborted) throw new Error("Repository read timed out");
        if (status.outcome !== "authenticated" || typeof status.login !== "string" || status.login.length === 0) {
          // No authenticated account is a state, not an error: the connect
          // outcome stands and there is nothing to list.
          this.set({ phase: "ready", outcome: status.outcome, account: null, repositories: [], selected: null, owner: null, complete: false, message: "" });
          return;
        }
        account = status.login;
      }
      const list = await this.client.list(account, refresh, request.signal);
      if (!this.current(generation)) return;
      if (request.signal.aborted) throw new Error("Repository read timed out");
      if (list.account !== account || list.outcome !== "ok" || !list.complete) {
        this.set({ phase: "ready", outcome: list.account !== account ? "account-changed" : list.outcome === "ok" ? "limit" : list.outcome,
          account, repositories: [], selected: null, owner: null, complete: false, message: "" });
        return;
      }
      const owners = new Set(list.repositories.map(repository => repository.owner));
      const owner = this.state.owner !== null && owners.has(this.state.owner) ? this.state.owner : null;
      const previous = this.state.selected;
      const selected = previous === null ? null : list.repositories.find(repository => repository.id === previous.id) ?? null;
      this.set({ phase: "ready", outcome: list.outcome, account, repositories: list.repositories, complete: list.complete, ssoPartial: list.ssoPartial === true, owner, selected, message: "" });
    } catch {
      if (this.current(generation)) this.set({ phase: "error", outcome: "unavailable", account: null, repositories: [], selected: null, complete: false, message: "GitHub repositories could not be read. Nothing was retried." });
    } finally {
      clearTimeout(timeout);
      request.abort();
      if (this.current(generation)) this.request = null;
    }
  }

  // select confirms the picked stable id against the backend before naming
  // a selection, so a renamed repository comes back under its current name
  // and a moved or vanished one cannot be selected. It never starts a
  // clone; importing is a separate explicit step.
  async select(id: number): Promise<GitHubRepository | null> {
    if (this.state.phase !== "ready" || this.state.account === null || !this.state.complete || this.state.outcome !== "ok" || !this.state.repositories.some(repository => repository.id === id)) return null;
    if (!Number.isSafeInteger(id) || id < 1) return null;
    const account = this.state.account;
    const generation = ++this.generation;
    const request = new AbortController();
    this.request?.abort();
    this.request = request;
    this.set({ phase: "checking", pendingId: id, message: "" });
    const timeout = setTimeout(() => request.abort(), 15000);
    try {
      const result = await this.client.repository(account, id, request.signal);
      if (!this.current(generation) || this.state.account !== account) return null;
      if (request.signal.aborted) throw new Error("Repository read timed out");
      if (result.account !== account || result.outcome !== "ok" || result.repository === null) {
        this.set({ phase: "ready", outcome: result.account !== account ? "account-changed" : result.outcome,
          repositories: [], complete: false, ssoPartial: false, selected: null, pendingId: null, message: "" });
        return null;
      }
      this.set({ phase: "ready", selected: result.repository, pendingId: null, message: "" });
      return result.repository;
    } catch {
      // An unconfirmed pick selects nothing; the row the listing showed is
      // not evidence the repository still exists.
      if (this.current(generation)) this.set({ phase: "ready", outcome: "unavailable", repositories: [], complete: false, ssoPartial: false, selected: null, pendingId: null, message: "The repository could not be confirmed. It was not selected." });
      return null;
    } finally {
      clearTimeout(timeout);
      request.abort();
      if (this.current(generation)) this.request = null;
    }
  }

  clearSelection(): void {
    if (this.state.phase !== "ready") return;
    this.set({ selected: null });
  }

  setQuery(query: string): void {
    if (this.state.phase === "disposed") return;
    this.set({ query });
  }

  setOwner(owner: string | null): void {
    if (this.state.phase === "disposed") return;
    if (owner !== null && !this.owners.includes(owner)) return;
    this.set({ owner });
  }

  // The server reported the grant gone. Every row and the selection were
  // read under that grant, so they are cleared, and nothing retries on its
  // own — an explicit load is the only way back.
  authorizationLost(): void {
    if (this.state.phase === "disposed") return;
    ++this.generation;
    this.request?.abort();
    this.request = null;
    this.state = { ...INITIAL, phase: "error", message: "GitHub access was lost. The repository list was cleared; read it again after reconnecting." };
    this.changed();
  }

  reset(): void {
    if (this.state.phase === "disposed") return;
    ++this.generation;
    this.request?.abort();
    this.request = null;
    this.state = { ...INITIAL };
    this.changed();
  }

  close(): void {
    ++this.generation;
    this.request?.abort();
    this.request = null;
    this.state = { ...INITIAL, phase: "disposed" };
  }

  private current(generation: number): boolean { return this.state.phase !== "disposed" && generation === this.generation; }
  private set(update: Partial<GitHubRepositoriesState>): void { this.state = { ...this.state, ...update }; this.changed(); }
}
