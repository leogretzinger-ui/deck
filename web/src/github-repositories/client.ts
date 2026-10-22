import { fetchWithAuthorization } from "../api/http.ts";

// Typed read-only client for the GitHub repository listing the backend
// assembles for one authenticated account. Like the connect client, every
// response is validated field by field before it reaches the model: a
// malformed row must fail the whole read, never render as a partial truth.
// Nothing is cached here — the backend owns the listing cache, and
// refresh=1 asks it to bypass that cache for an explicit user refresh.

export interface GitHubRepository {
  id: number;
  name: string;
  full_name: string;
  owner: string;
  owner_type: string;
  private: boolean;
  description: string;
  clone_url: string;
}

export interface GitHubRepositoryList {
  outcome: string;
  account: string;
  repositories: GitHubRepository[];
  // complete means every page was fetched; SSO may still hide organizations.
  complete: boolean;
  ssoPartial?: boolean;
}

export interface GitHubRepositoryRead {
  outcome: string;
  account: string;
  repository: GitHubRepository | null;
}

export interface GitHubRepositoriesClient {
  list(account: string, refresh: boolean, signal: AbortSignal): Promise<GitHubRepositoryList>;
  repository(account: string, id: number, signal: AbortSignal): Promise<GitHubRepositoryRead>;
}

export interface GitHubRemoteIdentity {
  owner: string;
  name: string;
  full_name: string;
}

const HTTPS_REMOTE = /^https:\/\/github\.com\/([^/]+)\/([^/]+)$/;
const SSH_REMOTE = /^git@github\.com:([^/]+)\/([^/]+)$/;
const OWNER_NAME = /^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$/;
const REPOSITORY_NAME = /^[A-Za-z0-9._-]{1,100}$/;

// githubRemoteIdentity parses the two remote forms a GitHub clone can
// already be registered under, for matching an imported repository against
// the listing. Anything else — another host, credentials in the URL, extra
// path, a port, a scheme-relative string — is not a GitHub remote and gets
// null, never a best-effort guess.
export function githubRemoteIdentity(input: unknown): GitHubRemoteIdentity | null {
  if (typeof input !== "string") return null;
  const match = HTTPS_REMOTE.exec(input) ?? SSH_REMOTE.exec(input);
  if (match === null) return null;
  const owner = match[1]!;
  let name = match[2]!;
  if (name.endsWith(".git")) name = name.slice(0, -".git".length);
  if (!OWNER_NAME.test(owner) || !REPOSITORY_NAME.test(name) || name === "." || name === "..") return null;
  return { owner, name, full_name: `${owner}/${name}` };
}

function record(value: unknown): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) throw new Error("Unreadable GitHub repository response");
  return value as Record<string, unknown>;
}

export function parseGitHubRepository(value: unknown): GitHubRepository {
  const v = record(value);
  if (!Number.isSafeInteger(v.id) || (v.id as number) < 1 ||
      typeof v.name !== "string" || v.name.length === 0 || v.name.length > 100 ||
      typeof v.full_name !== "string" ||
      typeof v.owner !== "string" || v.owner.length === 0 ||
      typeof v.owner_type !== "string" || v.owner_type.length === 0 ||
      typeof v.private !== "boolean" ||
      typeof v.clone_url !== "string") {
    throw new Error("Unreadable GitHub repository");
  }
  // GitHub serves null for an empty description; every other non-string is
  // a shape this client does not speak.
  const description = v.description === null || v.description === undefined ? "" : v.description;
  if (typeof description !== "string") throw new Error("Unreadable GitHub repository");
  // full_name is the canonical owner/name pair; a row whose parts disagree
  // with it cannot be matched against an import safely.
  if (v.full_name !== `${v.owner}/${v.name}`) throw new Error("Unreadable GitHub repository");
  // clone_url is the address a later import would hand to git, so it must
  // be the canonical github.com URL of the very repository the row names —
  // never an arbitrary address a response could point you at.
  const remote = githubRemoteIdentity(v.clone_url);
  if (remote === null || !v.clone_url.startsWith("https://") || remote.full_name !== v.full_name) {
    throw new Error("Unreadable GitHub repository");
  }
  return {
    id: v.id as number,
    name: v.name,
    full_name: v.full_name,
    owner: v.owner,
    owner_type: v.owner_type,
    private: v.private,
    description,
    clone_url: v.clone_url,
  };
}

function outcome(value: Record<string, unknown>, account: string): string {
  if (typeof value.outcome !== "string" || value.outcome.length === 0) throw new Error("Unreadable GitHub repository response");
  // The account echo binds the answer to the login that was asked about; an
  // answer for any other account must not be applied to this one.
  if (value.account !== account) throw new Error("Unreadable GitHub repository response");
  return value.outcome;
}

export class HttpGitHubRepositoriesClient implements GitHubRepositoriesClient {
  private onAuthorizationLost?: () => void;

  constructor(onAuthorizationLost?: () => void) {
    this.onAuthorizationLost = onAuthorizationLost;
  }

  private async request(path: string, signal: AbortSignal): Promise<unknown> {
    const response = await fetchWithAuthorization(`/api/github/repositories${path}`, {
      method: "GET", credentials: "same-origin", cache: "no-store",
      redirect: "error", referrerPolicy: "no-referrer", signal,
    }, () => { if (!signal.aborted) this.onAuthorizationLost?.(); });
    // The failure body can carry a server diagnostic; it never reaches the
    // caller as-is.
    if (!response.ok) throw new Error("GitHub repository request was not confirmed");
    return response.json();
  }

  async list(account: string, refresh: boolean, signal: AbortSignal): Promise<GitHubRepositoryList> {
    const query = new URLSearchParams({ account });
    if (refresh) query.set("refresh", "1");
    const value = record(await this.request(`?${query.toString()}`, signal));
    const result = outcome(value, account);
    // A non-ok outcome may carry no rows at all; present rows are always an
    // array of valid repositories or the read fails.
    const rows = value.repositories === undefined || value.repositories === null ? [] : value.repositories;
    if (!Array.isArray(rows)) throw new Error("Unreadable GitHub repository list");
    return { outcome: result, account, repositories: rows.map(parseGitHubRepository), complete: value.complete === true, ssoPartial: value.ssoPartial === true };
  }

  async repository(account: string, id: number, signal: AbortSignal): Promise<GitHubRepositoryRead> {
    if (!Number.isSafeInteger(id) || id < 1) throw new Error("Unknown GitHub repository id");
    const query = new URLSearchParams({ account });
    const value = record(await this.request(`/${id}?${query.toString()}`, signal));
    const result = outcome(value, account);
    if (result !== "ok") return { outcome: result, account, repository: null };
    const repository = parseGitHubRepository(value.repository);
    // Selection is by stable id: an answer naming a different id is not the
    // repository you picked, whatever else it says.
    if (repository.id !== id) throw new Error("Unreadable GitHub repository");
    return { outcome: result, account, repository };
  }
}

export interface GitHubAccountIdentity {
  name: string;
  email: string;
}

const NOREPLY_EMAIL = /^[1-9][0-9]*\+[A-Za-z0-9-]{1,39}@users\.noreply\.github\.com$/;

// readGitHubAccountIdentity reads the connected account's profile name and
// its GitHub noreply address, or null when no account is connected.
export async function readGitHubAccountIdentity(signal: AbortSignal, onAuthorizationLost?: () => void): Promise<GitHubAccountIdentity | null> {
  const response = await fetchWithAuthorization("/api/github/account", {
    method: "GET", credentials: "same-origin", cache: "no-store",
    redirect: "error", referrerPolicy: "no-referrer", signal,
  }, () => { if (!signal.aborted) onAuthorizationLost?.(); });
  if (!response.ok) throw new Error("GitHub account request was not confirmed");
  const value = record(await response.json());
  if (value.outcome !== "ok") return null;
  const { name, email } = value;
  if (typeof name !== "string" || name.trim() === "" || /[\u0000-\u001f\u007f]/.test(name) ||
      typeof email !== "string" || !NOREPLY_EMAIL.test(email)) {
    throw new Error("Unreadable GitHub account");
  }
  return { name, email };
}
