// The last served project/session list, kept in the browser so a cold load
// draws what you saw last instead of an empty rail. It is a
// memory of the previous answer, never an answer of its own: a cached list
// renders dimmed and inert until the server confirms it, and the next
// successful read replaces it wholesale. Only the listing fields the rail
// draws are stored — never a token, a credential, or a path the server did
// not already present to this browser.
import type { Project } from "../../api/projects.ts";
import type { SessionSummary } from "../../api/sessions.ts";
import type { ProjectWithSessions } from "./sidebar.ts";

export interface ListCacheStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
  removeItem(key: string): void;
}

const storageKey = "noisedeck.session-list";

interface CachedList {
  groups: Array<{ project: Project; sessions: SessionSummary[] }>;
}

// readListCache returns the remembered list, or null when there is none or
// it does not hold the shape the rail renders from. A cache that parses
// but is structurally wrong is dropped on the spot: the model must never
// construct over an entry that would throw at first render, and a stale
// bad entry would otherwise outlive every server answer that could not
// replace it.
export function readListCache(storage: ListCacheStorage): ProjectWithSessions[] | null {
  let raw: string | null;
  try {
    raw = storage.getItem(storageKey);
  } catch {
    return null;
  }
  if (raw === null) return null;
  const groups = parseCachedList(raw);
  if (groups === null) clearListCache(storage);
  return groups;
}

function parseCachedList(raw: string): ProjectWithSessions[] | null {
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return null;
  }
  if (!isRecord(parsed) || !Array.isArray(parsed.groups)) return null;
  const groups: ProjectWithSessions[] = [];
  for (const entry of parsed.groups) {
    if (!isRecord(entry) || !Array.isArray(entry.sessions)) return null;
    const project = cachedProject(entry.project);
    if (project === null) return null;
    const sessions: SessionSummary[] = [];
    for (const session of entry.sessions) {
      const summary = cachedSession(session);
      if (summary === null) return null;
      sessions.push(summary);
    }
    groups.push({ project, sessions });
  }
  return groups;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

// The records are rebuilt field by field rather than passed through: only
// the fields the rail renders from come back, each at the type its
// renderer expects, so a stray value under a known or unknown key cannot
// reach a render that would throw on it.
function cachedProject(value: unknown): Project | null {
  if (!isRecord(value)) return null;
  const { id, name, canonical_path, last_used_at } = value;
  if (!isSafeID(id) || typeof name !== "string" || typeof canonical_path !== "string"
    || typeof last_used_at !== "string") return null;
  return { id, name, canonical_path, last_used_at };
}

// The rail keys plain objects by project and session id (saved row
// orders, colours), so an id that names an Object.prototype property —
// "__proto__", "constructor", "toString" — would read an inherited
// function where a saved order was expected and throw in the first
// render. The server never serves such an id; a cache that holds one is
// not the server's answer.
function isSafeID(value: unknown): value is string {
  return typeof value === "string" && value !== "" && !(value in Object.prototype);
}

function cachedSession(value: unknown): SessionSummary | null {
  if (!isRecord(value)) return null;
  const {
    id, project_id, provider, display_name, status, generation, created_at, updated_at,
    recovery_capability, recovery_state, presentation_state, last_output_at, color,
  } = value;
  if (!isSafeID(id) || !isSafeID(project_id) || typeof provider !== "string"
    || typeof display_name !== "string" || typeof status !== "string" || typeof generation !== "number"
    || typeof created_at !== "string" || typeof updated_at !== "string") return null;
  if (!isOneOf(recovery_capability, RECOVERY_CAPABILITIES) || !isOneOf(recovery_state, RECOVERY_STATES)
    || !isOneOf(presentation_state, PRESENTATION_STATES)) return null;
  if (last_output_at !== undefined && typeof last_output_at !== "string") return null;
  if (color !== undefined && color !== null && typeof color !== "string") return null;
  const summary: SessionSummary = {
    id, project_id, provider, display_name, status, generation, created_at, updated_at,
    recovery_capability, recovery_state, presentation_state,
  };
  if (last_output_at !== undefined) summary.last_output_at = last_output_at;
  if (color !== undefined) summary.color = color;
  return summary;
}

const RECOVERY_CAPABILITIES = ["resume", "relaunch", "none"] as const;
const RECOVERY_STATES = ["unavailable", "automatic_pending", "recovering"] as const;
const PRESENTATION_STATES = ["running", "recovering", "attachment_unavailable", "ended", "inactive", "resume_ended"] as const;

function isOneOf<T extends string>(value: unknown, options: readonly T[]): value is T {
  return typeof value === "string" && (options as readonly string[]).includes(value);
}

export function writeListCache(storage: ListCacheStorage, groups: readonly ProjectWithSessions[]): void {
  const value: CachedList = {
    groups: groups.map(({ project, sessions }) => ({ project, sessions })),
  };
  try {
    storage.setItem(storageKey, JSON.stringify(value));
  } catch {
    // Storage denial costs the next cold load its last-known rail, nothing
    // you can see now.
  }
}

function clearListCache(storage: ListCacheStorage): void {
  try {
    storage.removeItem(storageKey);
  } catch {
    // A bad cache the browser will not clear is still rejected on every
    // read, so it never reaches the model.
  }
}
