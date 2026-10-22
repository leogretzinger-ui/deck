// Only the opaque application session id is persisted. The authoritative
// session collection validates it without assuming an id format.
export interface LastSessionStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
  removeItem(key: string): void;
}

const storageKey = "noisedeck.last-session-id";

export function readLastSession(storage: LastSessionStorage): string | null {
  try {
    return storage.getItem(storageKey) ?? null;
  } catch {
    return null;
  }
}

export function writeLastSession(storage: LastSessionStorage, id: string): void {
  try {
    storage.setItem(storageKey, id);
  } catch {
    // Storage denial loses last-session reopen, not current selection.
  }
}

export function clearLastSession(storage: LastSessionStorage): void {
  try {
    storage.removeItem(storageKey);
  } catch {
    // Storage denial leaves a stale id that membership checks already guard.
  }
}

export function pickInitialSession(
  storedID: string | null,
  sessions: readonly { id: string; presentation_state: string }[],
): string | null {
  if (storedID !== null && sessions.some((s) =>
    s.id === storedID && s.presentation_state === "running")) {
    return storedID;
  }
  return sessions.find((s) => s.presentation_state === "running")?.id ?? null;
}

// LastSessionRemote is the server's memory of the last opened session, so
// a browser on another device opens the same one.
export interface LastSessionRemote {
  read(): Promise<string | null>;
  write(id: string | null): Promise<void>;
}

// LastSessionMemory keeps the browser's memory and the server's in step.
//
// Writes go to the server one at a time, in the order the user made
// them: two in flight at once could land reversed and leave the server
// holding an older choice than the browser. The server's answer is adopted
// into the browser once, at start, behind one shared barrier every refresh
// waits on until it settles, and only if the user has not chosen a
// session in the meantime — a late answer is older than that choice. The
// read is bounded so a server that never answers cannot hold the first
// selection; the browser's own memory is the fallback either way.
export class LastSessionMemory {
  private queue: Promise<void> = Promise.resolve();
  private adoption: Promise<void> | null = null;
  private chosen = false;
  private readonly storage: LastSessionStorage;
  private readonly remote: LastSessionRemote;
  private readonly readTimeoutMS: number;
  private readonly wait: (ms: number) => { done: Promise<void>; cancel(): void };

  constructor(
    storage: LastSessionStorage,
    remote: LastSessionRemote,
    readTimeoutMS = 3000,
    wait: (ms: number) => { done: Promise<void>; cancel(): void } = waitFor,
  ) {
    this.storage = storage;
    this.remote = remote;
    this.readTimeoutMS = readTimeoutMS;
    this.wait = wait;
  }

  // remember records a user's choice in both memories. A clear with
  // nothing remembered writes nothing: a deselect of a session this
  // browser never chose is not a change of the user's place.
  remember(id: string | null): void {
    if (id === null) {
      if (readLastSession(this.storage) === null) return;
      clearLastSession(this.storage);
    } else {
      writeLastSession(this.storage, id);
    }
    this.chosen = true;
    this.queue = this.queue.then(() => this.remote.write(id)).catch(() => {});
  }

  // adopt asks the server once and mirrors its answer into the browser's
  // memory. Every caller shares the one promise until it settles; it never
  // rejects, and it never outlives the read bound.
  adopt(): Promise<void> {
    if (this.adoption === null) {
      this.adoption = this.readRemote().then((remote) => {
        if (remote !== null && !this.chosen) writeLastSession(this.storage, remote);
      });
    }
    return this.adoption;
  }

  private async readRemote(): Promise<string | null> {
    const timer = this.wait(this.readTimeoutMS);
    try {
      return await Promise.race([
        this.remote.read().catch(() => null),
        timer.done.then(() => null),
      ]);
    } finally {
      timer.cancel();
    }
  }
}

function waitFor(ms: number): { done: Promise<void>; cancel(): void } {
  let handle: ReturnType<typeof setTimeout> | null = null;
  const done = new Promise<void>((resolve) => {
    handle = setTimeout(resolve, ms);
  });
  return {
    done,
    cancel: () => {
      if (handle !== null) clearTimeout(handle);
    },
  };
}
