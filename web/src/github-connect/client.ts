import { csrfToken, fetchWithAuthorization } from "../api/http.ts";

export interface GitHubStatus {
  outcome: string;
  login?: string;
  state?: string;
  source?: string;
  source_writeable?: boolean;
  active?: boolean;
  git_protocol?: string;
  platform?: string;
}

export interface GitHubAttempt {
  id: string;
  status: "running" | "cancelled" | "exited" | "unavailable";
  generation: number;
  started_at: string;
  code?: string;
  final?: true;
}

export interface GitHubCurrent { csrf: string; attempt: GitHubAttempt | null }

export interface GitHubConnectClient {
  status(signal: AbortSignal): Promise<GitHubStatus>;
  current(signal: AbortSignal): Promise<GitHubCurrent>;
  start(csrf: string, signal: AbortSignal): Promise<GitHubAttempt>;
  cancel(id: string, csrf: string, signal: AbortSignal): Promise<void>;
}

function record(value: unknown): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) throw new Error("Unreadable GitHub response");
  return value as Record<string, unknown>;
}

function attempt(value: unknown): GitHubAttempt {
  const v = record(value);
  if (typeof v.id !== "string" || v.id.length === 0 || v.id.length > 256 ||
      !["running", "cancelled", "exited", "unavailable"].includes(String(v.status)) ||
      !Number.isInteger(v.generation) || Number(v.generation) < 1 || typeof v.started_at !== "string") {
    throw new Error("Unreadable GitHub attempt");
  }
  const result: GitHubAttempt = { id: v.id, status: v.status as GitHubAttempt["status"], generation: v.generation as number, started_at: v.started_at };
  if (typeof v.code === "string" && /^[0-9A-Z]{4}-[0-9A-Z]{4}$/.test(v.code)) result.code = v.code;
  if (v.final === true) result.final = true;
  return result;
}

export class HttpGitHubConnectClient implements GitHubConnectClient {
  private onAuthorizationLost?: () => void;
  private cookie: () => string;

  constructor(onAuthorizationLost?: () => void, cookie: () => string = () => document.cookie) {
    this.onAuthorizationLost = onAuthorizationLost;
    this.cookie = cookie;
  }

  private async request(path: string, signal: AbortSignal, body?: object, csrf?: string): Promise<Response> {
    const headers = new Headers();
    if (body !== undefined) {
      if (!csrf) throw new Error("Read GitHub status before changing the connection");
      headers.set("Content-Type", "application/json");
      headers.set("X-GitHub-Connect-CSRF", csrf);
      const token = csrfToken(this.cookie());
      if (token !== null) headers.set("X-CSRF-Token", token);
    }
    const response = await fetchWithAuthorization(`/api/github/connect/${path}`, {
      method: body === undefined ? "GET" : "POST", credentials: "same-origin", cache: "no-store",
      redirect: "error", referrerPolicy: "no-referrer", headers, signal,
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    }, () => { if (!signal.aborted) this.onAuthorizationLost?.(); });
    if (!response.ok) throw new Error("GitHub connection request was not confirmed");
    return response;
  }

  async status(signal: AbortSignal): Promise<GitHubStatus> {
    const value = record(await (await this.request("status", signal)).json());
    if (typeof value.outcome !== "string") throw new Error("Unreadable GitHub status");
    const result: GitHubStatus = { outcome: value.outcome };
    for (const key of ["login", "state", "source", "git_protocol", "platform"] as const) {
      if (typeof value[key] === "string") result[key] = value[key];
    }
    result.active = value.active === true;
    result.source_writeable = value.source_writeable === true;
    return result;
  }

  async current(signal: AbortSignal): Promise<GitHubCurrent> {
    const value = record(await (await this.request("attempt", signal)).json());
    if (typeof value.csrf !== "string" || value.csrf.length === 0) throw new Error("Unreadable GitHub attempt");
    return { csrf: value.csrf, attempt: value.attempt === null ? null : attempt(value.attempt) };
  }

  async start(csrf: string, signal: AbortSignal): Promise<GitHubAttempt> {
    return attempt(await (await this.request("start", signal, {}, csrf)).json());
  }

  async cancel(id: string, csrf: string, signal: AbortSignal): Promise<void> {
    await this.request("cancel", signal, { id }, csrf);
  }
}
