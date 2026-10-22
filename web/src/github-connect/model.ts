import type { GitHubAttempt, GitHubConnectClient, GitHubCurrent, GitHubStatus } from "./client.ts";

export type GitHubConnectPhase = "idle" | "checking" | "starting" | "ready" | "cancelling" | "closed";
export interface GitHubConnectState {
  phase: GitHubConnectPhase;
  status: GitHubStatus | null;
  attempt: GitHubAttempt | null;
  failure: "read" | "start" | null;
  followed: boolean;
}

export type GitHubConnectScreen =
  | { kind: "checking" }
  | { kind: "starting"; running: boolean }
  | { kind: "code"; code: string }
  | { kind: "connected"; login: string }
  | { kind: "missing-cli"; platform: string }
  | { kind: "ended" }
  | { kind: "notice"; message: string }
  | { kind: "failed"; message: string };

// definite reports whether a status read answers anything about the
// connection. A timeout, a failed or unreadable gh run, or an account gh
// could not verify in time does not; it counts as a failed read.
function definite(status: GitHubStatus): boolean {
  if (status.outcome === "auth-issue") return status.state !== "timeout";
  return ["authenticated", "no-account", "missing-cli", "authentication-required"].includes(status.outcome);
}

// connectedLogin names the account a status read reports connected, null
// when it reports none, and undefined when the read answers nothing.
export function connectedLogin(status: GitHubStatus): string | null | undefined {
  if (!definite(status)) return undefined;
  return status.outcome === "authenticated" ? status.login ?? "" : null;
}

export function connectAction(status: GitHubStatus | null): "connect" | "reconnect" | null {
  if (status === null || (status.source !== undefined && status.source !== "stored")) return null;
  if (status.outcome === "no-account") return "connect";
  if (status.outcome === "auth-issue" && status.source === "stored" && status.source_writeable === true &&
      status.active === true && status.state === "error" && Boolean(status.login)) return "reconnect";
  return null;
}

const FAILURES = {
  read: "Deck couldn't read your GitHub connection.",
  start: "Deck couldn't start GitHub sign-in.",
  lost: "Deck lost track of this GitHub sign-in.",
} as const;

// pending reports an attempt that may still be running: one running, or one
// tmux could not be read for just now.
function pending(attempt: GitHubAttempt | null): boolean {
  return attempt?.status === "running" || (attempt?.status === "unavailable" && attempt.final !== true);
}

// connectScreen derives the one thing the screen shows from the latest state.
// Anything short of a settled read shows Checking, never an older answer.
export function connectScreen(state: GitHubConnectState): GitHubConnectScreen {
  const { status, attempt } = state;
  if (state.failure !== null) return { kind: "failed", message: FAILURES[state.failure] };
  if (state.phase === "starting") return { kind: "starting", running: false };
  if (state.phase !== "ready") return { kind: "checking" };
  if (status === null || !definite(status)) return { kind: "failed", message: FAILURES.read };
  if (status.outcome === "authenticated") return { kind: "connected", login: status.login ?? "" };
  if (attempt?.status === "running") return attempt.code ? { kind: "code", code: attempt.code } : { kind: "starting", running: true };
  if (pending(attempt)) return { kind: "checking" };
  if (status.outcome === "missing-cli") return { kind: "missing-cli", platform: status.platform ?? "" };
  if (status.outcome === "authentication-required") {
    return { kind: "notice", message: "Turn on password, OIDC, or trusted-proxy sign-in for Deck before you connect GitHub." };
  }
  if (status.source === "environment") {
    return { kind: "notice", message: "Your GitHub access comes from a token in Deck's environment. Change it there; Deck won't replace it." };
  }
  if (status.source !== undefined && status.source !== "stored") {
    return { kind: "notice", message: "Deck can't tell where your GitHub sign-in is stored. Fix it with the GitHub CLI first." };
  }
  if (connectAction(status) !== null) return attempt?.status === "unavailable" ? { kind: "failed", message: FAILURES.lost } : { kind: "ended" };
  if (status.outcome === "auth-issue") return { kind: "notice", message: "Your GitHub connection needs attention. Check it with the GitHub CLI." };
  return { kind: "failed", message: FAILURES.read };
}

// signedIn names the account a sign-in this screen started or followed has
// connected, or is null. Opening on an account already connected is not one.
export function signedIn(state: GitHubConnectState): string | null {
  if (state.phase !== "ready" || !state.followed || state.status?.outcome !== "authenticated") return null;
  return state.status.login ?? "";
}

// connectedAccount names the account the screen shows connected, or is null.
// Closing on it tells the rest of Deck, whoever made the sign-in.
export function connectedAccount(state: GitHubConnectState): string | null {
  const screen = connectScreen(state);
  return screen.kind === "connected" ? screen.login : null;
}

// A connected account ends the sign-in. Otherwise an attempt that may still
// be running is read again until it settles.
function settled(state: GitHubConnectState): boolean {
  return state.status?.outcome === "authenticated" || !pending(state.attempt);
}

export interface GitHubConnectModelOptions { pollMs?: number; retryMs?: number }

const RETRY_CAP_MS = 10000;
const FAIL_AFTER_MS = 30000;

export class GitHubConnectModel {
  state: GitHubConnectState = { phase: "idle", status: null, attempt: null, failure: null, followed: false };
  private client: GitHubConnectClient;
  private changed: () => void;
  private pollMs: number;
  private retryMs: number;
  private csrf = "";
  private generation = 0;
  private request: AbortController | null = null;
  private sleeping: ((awake: boolean) => void) | null = null;

  constructor(client: GitHubConnectClient, changed: () => void = () => {}, options: GitHubConnectModelOptions = {}) {
    this.client = client;
    this.changed = changed;
    this.pollMs = options.pollMs ?? 1500;
    this.retryMs = options.retryMs ?? 1000;
  }

  get busy(): boolean { return ["checking", "starting", "cancelling"].includes(this.state.phase); }
  get running(): boolean { return this.state.attempt?.status === "running"; }

  // open reads first, then starts sign-in when nothing is connected:
  // the click that opened the screen asked for it.
  open(): Promise<void> { return this.read(true); }
  retry(): Promise<void> { return this.read(true); }

  // cancel is never resent: whatever its outcome, a fresh read decides
  // what the screen shows, and a sign-in still running is followed again.
  async cancel(): Promise<void> {
    if (this.busy || !this.running) return;
    const attempt = this.state.attempt!;
    const generation = this.begin("cancelling");
    const request = this.request!;
    const timeout = setTimeout(() => request.abort(), 20000);
    try {
      await this.client.cancel(attempt.id, this.csrf, request.signal);
    } catch {
      // Reconciled below.
    } finally {
      this.end(generation, timeout);
    }
    if (!this.current(generation)) return;
    if (await this.reconcile(this.begin("checking")) && !settled(this.state)) this.poll();
  }

  private async read(autoStart: boolean): Promise<void> {
    if (this.busy || this.state.phase === "closed") return;
    await this.check(autoStart);
  }

  private async check(autoStart: boolean): Promise<void> {
    if (!await this.reconcile(this.begin("checking"))) return;
    if (!settled(this.state)) this.poll();
    else if (autoStart && connectAction(this.state.status) !== null) await this.launch();
  }

  // launch never replays a start whose outcome is unknown: it reads
  // instead, and a running attempt found that way is the same sign-in.
  // A start that returns an attempt already over is read the same way.
  private async launch(): Promise<void> {
    const generation = this.begin("starting");
    this.set({ followed: true });
    const request = this.request!;
    const timeout = setTimeout(() => request.abort(), 20000);
    let attempt: GitHubAttempt | null = null;
    try {
      attempt = await this.client.start(this.csrf, request.signal);
      if (request.signal.aborted) attempt = null;
    } catch {
      // Reconciled below.
    } finally {
      this.end(generation, timeout);
    }
    if (!this.current(generation)) return;
    if (attempt?.status === "running") {
      this.set({ phase: "ready", attempt });
      this.poll();
      return;
    }
    if (!await this.reconcile(this.begin("checking"))) return;
    if (!settled(this.state)) this.poll();
    else if (attempt === null && connectAction(this.state.status) !== null) this.set({ failure: "start" });
  }

  private poll(): void {
    const generation = this.generation;
    void this.sleep(this.pollMs).then(async awake => {
      if (awake && !settled(this.state) && await this.reconcile(generation) && !settled(this.state)) this.poll();
    });
  }

  // reconcile reads until one read lands, showing Checking while reads fail.
  // It backs off between failures and never replays a start or cancel; after
  // FAIL_AFTER_MS it stops and offers Try again.
  private async reconcile(generation: number): Promise<boolean> {
    const since = Date.now();
    for (let delay = this.retryMs; ; delay = Math.min(delay * 2, RETRY_CAP_MS)) {
      if (await this.load(generation)) return true;
      if (!this.current(generation)) return false;
      if (Date.now() - since >= FAIL_AFTER_MS) {
        this.set({ phase: "idle", failure: "read" });
        return false;
      }
      this.set({ phase: "checking" });
      if (!await this.sleep(delay)) return false;
    }
  }

  private async load(generation: number): Promise<boolean> {
    this.request?.abort();
    const request = new AbortController();
    this.request = request;
    const timeout = setTimeout(() => request.abort(), 15000);
    try {
      const read = await this.fetch(request.signal, generation);
      if (read === null || !this.current(generation) || request.signal.aborted) return false;
      const [status, current] = read;
      this.csrf = current.csrf;
      const followed = this.state.followed || (current.attempt?.status === "running" && status.outcome !== "authenticated");
      this.set({ phase: "ready", status, attempt: current.attempt, followed });
      return true;
    } catch {
      return false;
    } finally {
      clearTimeout(timeout);
      request.abort();
      if (this.request === request) this.request = null;
    }
  }

  // Each response is acted on as it arrives: a failed or indefinite one ends
  // the read at once. A status read paired with an attempt that is over can
  // predate its end, so a fresh one replaces it.
  private fetch(signal: AbortSignal, generation: number): Promise<[GitHubStatus, GitHubCurrent] | null> {
    return new Promise(resolve => {
      let done = false;
      let reads = 0;
      let status: GitHubStatus | null = null;
      let current: GitHubCurrent | null = null;
      const finish = (read: [GitHubStatus, GitHubCurrent] | null) => { if (!done) { done = true; resolve(read); } };
      const readStatus = () => {
        const read = ++reads;
        this.client.status(signal).then(value => {
          if (done || read !== reads) return;
          if (!definite(value)) return finish(null);
          status = value;
          if (current !== null) finish([status, current]);
        }, () => { if (read === reads) finish(null); });
      };
      readStatus();
      this.client.current(signal).then(value => {
        if (done) return;
        current = value;
        if (value.attempt === null || value.attempt.status === "running") {
          if (status !== null) finish([status, current]);
          return;
        }
        status = null;
        if (this.current(generation) && this.state.phase !== "checking") this.set({ phase: "checking" });
        readStatus();
      }, () => finish(null));
    });
  }

  private sleep(ms: number): Promise<boolean> {
    this.stopPolling();
    const generation = this.generation;
    return new Promise(resolve => {
      const timer = setTimeout(() => wake(true), ms);
      const wake = (awake: boolean) => {
        clearTimeout(timer);
        if (this.sleeping === wake) this.sleeping = null;
        resolve(awake && this.current(generation));
      };
      this.sleeping = wake;
    });
  }

  private begin(phase: "checking" | "starting" | "cancelling"): number {
    this.stopPolling();
    const generation = ++this.generation;
    this.request?.abort();
    this.request = new AbortController();
    this.set({ phase, failure: null });
    return generation;
  }

  private end(generation: number, timeout: ReturnType<typeof setTimeout>): void {
    clearTimeout(timeout);
    if (this.current(generation)) { this.request?.abort(); this.request = null; }
  }

  private stopPolling(): void {
    this.sleeping?.(false);
  }

  private current(generation: number): boolean { return this.state.phase !== "closed" && generation === this.generation; }
  private set(update: Partial<GitHubConnectState>): void { this.state = { ...this.state, ...update }; this.changed(); }

  dispose(): void {
    ++this.generation;
    this.stopPolling();
    this.request?.abort();
    this.request = null;
    this.csrf = "";
    this.state = { phase: "closed", status: null, attempt: null, failure: null, followed: false };
  }
}
