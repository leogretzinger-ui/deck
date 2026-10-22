// A controller id belongs to one live browser document, not one mounted
// terminal. sessionStorage preserves it across same-tab navigation/OIDC, but
// a newly opened tab may inherit a copy. A Web Lock held for the document's
// lifetime makes that copy detection exclusive without timing guesses.
export interface ControllerStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
}

export interface ControllerLock {
  readonly name: string;
}

export interface ControllerLockOptions {
  mode: "exclusive";
  ifAvailable: true;
}

export type RequestControllerLock = (
  name: string,
  options: ControllerLockOptions,
  callback: (lock: ControllerLock | null) => Promise<void>,
) => Promise<void>;

export interface ControllerClaim {
  id: string;
  close(): Promise<void>;
}

const storageKey = "noisedeck.controller-id";
const lockPrefix = "noisedeck-controller:";
const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;
const maxFreshAttempts = 8;

export async function claimTabController(
  storage: ControllerStorage,
  requestLock: RequestControllerLock,
  makeID: () => string,
  signal?: AbortSignal,
): Promise<ControllerClaim> {
  let candidate = readStored(storage) ?? validFresh(makeID);
  for (let attempt = 0; attempt < maxFreshAttempts; attempt++) {
    const claim = await holdController(candidate, requestLock, signal);
    if (claim !== null) {
      writeStored(storage, candidate);
      return claim;
    }
    candidate = validFresh(makeID);
  }
  throw new Error("could not allocate a unique browser controller");
}

async function holdController(
  id: string,
  requestLock: RequestControllerLock,
  signal?: AbortSignal,
): Promise<ControllerClaim | null> {
  if (signal?.aborted) throw signal.reason;
  let release!: () => void;
  const held = new Promise<void>((resolve) => { release = resolve; });
  let resolveAcquired!: (claim: ControllerClaim | null) => void;
  let rejectAcquired!: (cause: unknown) => void;
  const acquired = new Promise<ControllerClaim | null>((resolve, reject) => {
    resolveAcquired = resolve;
    rejectAcquired = reject;
  });
  let released = false;
  let requestDone!: Promise<void>;
  const onAbort = () => rejectAcquired(signal?.reason ?? new Error("controller claim aborted"));
  signal?.addEventListener("abort", onAbort, { once: true });
  const close = (): Promise<void> => {
    if (!released) {
      released = true;
      release();
    }
    return requestDone;
  };

  try {
    requestDone = requestLock(
      `${lockPrefix}${id}`,
      { mode: "exclusive", ifAvailable: true },
      async (lock) => {
        // ifAvailable cannot legally be combined with the Web Locks `signal`
        // option. Cancellation is fenced here instead: a callback that arrives
        // after pagehide returns immediately, releasing any lock it received.
        if (signal?.aborted) return;
        if (lock === null) {
          resolveAcquired(null);
          return;
        }
        resolveAcquired({ id, close });
        await held;
      },
    );
    void requestDone.then(
      () => undefined,
      (cause) => rejectAcquired(cause),
    );
    const claim = await acquired;
    if (claim === null) await requestDone;
    return claim;
  } finally {
    signal?.removeEventListener("abort", onAbort);
  }
}

function validFresh(makeID: () => string): string {
  const id = makeID();
  if (!uuidPattern.test(id)) {
    throw new Error("browser did not produce a valid controller UUID");
  }
  return id;
}

function readStored(storage: ControllerStorage): string | null {
  try {
    const value = storage.getItem(storageKey);
    return value !== null && uuidPattern.test(value) ? value : null;
  } catch {
    return null;
  }
}

function writeStored(storage: ControllerStorage, value: string): void {
  try {
    storage.setItem(storageKey, value);
  } catch {
    // Storage denial loses navigation renewal, not current-page isolation.
  }
}
