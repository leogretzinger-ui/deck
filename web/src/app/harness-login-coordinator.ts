import { isSupportedHarnessLoginFlow, type HarnessLoginAttempt, type HarnessLoginListing } from "./harness-login.ts";
import type { LastSessionStorage } from "./last-session.ts";

export type HarnessLoginCloseReason = "dismissed" | "terminal" | "context-lost";

// Only opaque facts: which attempt, which session, how long the no-nag
// suppression lasts. A CSRF token or a callback URL never lands here.
export interface SuppressedLoginAttempt { id: string; session: string; expires: number }

export interface HarnessLoginCoordinatorPorts {
  listing(session: string, signal: AbortSignal): Promise<HarnessLoginListing>;
  raise(session: string, listing: HarnessLoginListing): boolean;
  occupied(): boolean;
  session(): string | null;
  attached(): boolean;
  visible(): boolean;
  now(): number;
  changed?(): void;
}

const pollIntervalMS = 15000;
const listingTimeoutMS = 8000;
// Match the backend registration bound without evicting live dismissals.
const suppressionLimit = 1024;
const storeKey = "noisedeck.harness-login";

export class HarnessLoginCoordinator {
  private ports: HarnessLoginCoordinatorPorts;
  private store: LastSessionStorage;
  private timer: ReturnType<typeof setInterval> | null = null;
  private generation = 0;
  private polling = false;
  private pollAgain = false;
  private request: AbortController | null = null;
  private raisedFor: string | null = null;
  private last: { session: string; attempts: HarnessLoginAttempt[] } | null = null;
  private suppressed: SuppressedLoginAttempt[];
  private disposed = false;
  private readonly onVisible = (): void => {
    if (!this.disposed && document.visibilityState === "visible") void this.poll();
  };

  constructor(ports: HarnessLoginCoordinatorPorts, store: LastSessionStorage) {
    this.ports = ports;
    this.store = store;
    this.suppressed = this.readStore().filter((entry) => entry.expires > ports.now());
  }

  start(): void {
    if (this.disposed || this.timer !== null) return;
    this.timer = setInterval(() => void this.poll(), pollIntervalMS);
    if (typeof document !== "undefined") document.addEventListener("visibilitychange", this.onVisible);
    void this.poll();
  }

  dispose(): void {
    this.disposed = true;
    this.invalidate();
    if (this.timer !== null) {
      clearInterval(this.timer);
      this.timer = null;
    }
    if (typeof document !== "undefined") document.removeEventListener("visibilitychange", this.onVisible);
  }

  // Fence responses even if a different controller returns to the same session.
  invalidate(): void {
    ++this.generation;
    this.request?.abort();
    this.request = null;
    this.polling = false;
    this.pollAgain = false;
    this.last = null;
    this.raisedFor = null;
    this.ports.changed?.();
  }

  pollNow(): Promise<void> {
    if (this.polling) this.pollAgain = true;
    return this.poll();
  }

  pendingFor(session: string): HarnessLoginAttempt[] {
    return this.last !== null && this.last.session === session
      ? this.last.attempts.filter(attempt => Date.parse(attempt.expires) > this.ports.now()) : [];
  }

  closed(session: string, attempts: string[], reason: HarnessLoginCloseReason): void {
    if (this.raisedFor === session) this.raisedFor = null;
    if (reason === "dismissed") this.suppress(session, attempts);
    if (reason === "terminal" && this.last?.session === session) {
      this.last.attempts = this.last.attempts.filter(attempt => !attempts.includes(attempt.id));
      this.ports.changed?.();
    }
  }

  private async poll(): Promise<void> {
    if (this.disposed || this.polling) return;
    const session = this.ports.session();
    if (session === null || !this.ports.attached() || !this.ports.visible()) return;
    this.polling = true;
    const generation = ++this.generation;
    const request = new AbortController();
    this.request = request;
    const timeout = setTimeout(() => request.abort(), listingTimeoutMS);
    try {
      const listing = await this.ports.listing(session, request.signal);
      if (this.disposed || generation !== this.generation || this.ports.session() !== session || !this.ports.attached() || !this.ports.visible()) return;
      this.applyListing(session, listing);
    } catch {
      // A failed poll carries no verdict — the next tick asks again.
    } finally {
      clearTimeout(timeout);
      if (generation === this.generation) {
        this.polling = false;
        this.request = null;
        if (this.pollAgain) {
          this.pollAgain = false;
          void this.poll();
        }
      }
    }
  }

  private applyListing(session: string, listing: HarnessLoginListing): void {
    const attempts = listing.unavailable === true ? [] : listing.pending.filter((attempt) => isSupportedHarnessLoginFlow(attempt.flow) && Date.parse(attempt.expires) > this.ports.now());
    const changed = this.last?.session !== session || this.last.attempts.map(a => a.id).join("\0") !== attempts.map(a => a.id).join("\0");
    this.last = { session, attempts };
    if (changed) this.ports.changed?.();
    if (attempts.length === 0) return;
    if (this.raisedFor !== null || this.ports.occupied()) return;
    const fresh = attempts.filter((attempt) => !this.suppressed.some((entry) => entry.id === attempt.id));
    if (fresh.length === 0) return;
    this.raisedFor = session;
    if (!this.ports.raise(session, listing)) {
      this.raisedFor = null;
      return;
    }
    // Suppress only after the dialog actually mounted.
    this.suppress(session, fresh.map((attempt) => attempt.id));
  }

  private suppress(session: string, ids: string[]): void {
    const known = new Map(this.suppressed.filter(entry => entry.expires > this.ports.now()).map((entry) => [entry.id, entry]));
    for (const id of ids) {
      if (known.has(id)) continue;
      const fromListing = this.last?.attempts.find((attempt) => attempt.id === id);
      const parsed = fromListing === undefined ? NaN : Date.parse(fromListing.expires);
      known.set(id, { id, session, expires: Number.isNaN(parsed) ? this.ports.now() + 300000 : parsed });
    }
    this.suppressed = [...known.values()].slice(-suppressionLimit);
    try {
      this.store.setItem(storeKey, JSON.stringify(this.suppressed));
    } catch {
      // A denied store loses persistence across reload, never behavior.
    }
  }

  private readStore(): SuppressedLoginAttempt[] {
    try {
      const raw = this.store.getItem(storeKey);
      if (raw === null) return [];
      const parsed: unknown = JSON.parse(raw);
      if (!Array.isArray(parsed)) return [];
      return parsed.filter((entry): entry is SuppressedLoginAttempt => {
        if (typeof entry !== "object" || entry === null) return false;
        const { id, session, expires } = entry as Record<string, unknown>;
        return typeof id === "string" && id.length <= 256 && typeof session === "string" && session.length <= 256 && typeof expires === "number" && Number.isFinite(expires);
      }).slice(-suppressionLimit).map(({ id, session, expires }) => ({ id, session, expires }));
    } catch {
      return [];
    }
  }
}
