import assert from "node:assert/strict";
import test from "node:test";

import {
  clearLastSession,
  pickInitialSession,
  readLastSession,
  writeLastSession,
  type LastSessionStorage,
} from "./last-session.ts";

function store(initial: Record<string, string> = {}): LastSessionStorage & {
  raw: Map<string, string>;
} {
  const raw = new Map<string, string>(Object.entries(initial));
  return {
    raw,
    getItem: (k) => raw.get(k) ?? null,
    setItem: (k, v) => void raw.set(k, v),
    removeItem: (k) => void raw.delete(k),
  };
}

function refusing(
  base: ReturnType<typeof store>,
  operations: Partial<Record<"get" | "set" | "remove", boolean>>,
): LastSessionStorage {
  return {
    getItem: (k) => {
      if (operations.get) throw new Error("getItem denied");
      return base.raw.get(k) ?? null;
    },
    setItem: (k, v) => {
      if (operations.set) throw new Error("setItem denied");
      base.raw.set(k, v);
    },
    removeItem: (k) => {
      if (operations.remove) throw new Error("removeItem denied");
      base.raw.delete(k);
    },
  };
}

const idA = "11111111-1111-4111-8111-111111111111";
const idB = "22222222-2222-4222-8222-222222222222";
const idC = "33333333-3333-4333-8333-333333333333";

function sessions(...rows: { id: string; presentation_state: string }[]) {
  return rows;
}

test("writeLastSession persists and readLastSession returns it", () => {
  const s = store();
  assert.equal(readLastSession(s), null);
  writeLastSession(s, idA);
  assert.equal(readLastSession(s), idA);
  assert.equal(s.raw.get("noisedeck.last-session-id"), idA);
});

test("readLastSession returns null when no preference is stored", () => {
  assert.equal(readLastSession(store()), null);
});

test("readLastSession swallows a read refusal and falls back to null", () => {
  const s = refusing(store({ "noisedeck.last-session-id": idA }), { get: true });
  assert.equal(readLastSession(s), null);
});

test("writeLastSession swallows a write refusal without throwing", () => {
  const s = refusing(store(), { set: true });
  assert.doesNotThrow(() => writeLastSession(s, idA));
  assert.equal(readLastSession(s), null);
});

test("clearLastSession removes a stored id and is a no-op when none exists", () => {
  const s = store({ "noisedeck.last-session-id": idA });
  clearLastSession(s);
  assert.equal(readLastSession(s), null);
  assert.doesNotThrow(() => clearLastSession(s));
});

test("clearLastSession swallows a removal refusal without throwing", () => {
  const s = refusing(store({ "noisedeck.last-session-id": idA }), { remove: true });
  assert.doesNotThrow(() => clearLastSession(s));
});

test("writeLastSession updates the stored id on a later explicit selection", () => {
  const s = store();
  writeLastSession(s, idA);
  writeLastSession(s, idB);
  assert.equal(readLastSession(s), idB);
});

test("pickInitialSession restores the stored id when it is still present", () => {
  const list = sessions({ id: idA, presentation_state: "running" }, { id: idB, presentation_state: "running" });
  assert.equal(pickInitialSession(idA, list), idA);
});

test("pickInitialSession never restores a non-running stored session", () => {
  const list = sessions({ id: idA, presentation_state: "ended" }, { id: idB, presentation_state: "running" });
  assert.equal(pickInitialSession(idA, list), idB);
});

test("pickInitialSession falls back to running-first when the stored id is absent", () => {
  const list = sessions({ id: idA, presentation_state: "ended" }, { id: idB, presentation_state: "running" });
  assert.equal(pickInitialSession(null, list), idB);
});

test("pickInitialSession falls back to running-first when the stored id was deleted", () => {
  const list = sessions({ id: idA, presentation_state: "ended" }, { id: idB, presentation_state: "running" });
  assert.equal(pickInitialSession(idC, list), idB);
});

test("pickInitialSession falls back when the stored id is arbitrary or corrupt", () => {
  const list = sessions({ id: idA, presentation_state: "ended" }, { id: idB, presentation_state: "running" });
  assert.equal(pickInitialSession("not-a-real-id", list), idB);
  assert.equal(pickInitialSession("", list), idB);
});

test("pickInitialSession returns null when none are verified running", () => {
  const list = sessions({ id: idA, presentation_state: "ended" }, { id: idB, presentation_state: "inactive" });
  assert.equal(pickInitialSession(null, list), null);
});

test("pickInitialSession returns null when there are no sessions", () => {
  assert.equal(pickInitialSession(idA, []), null);
  assert.equal(pickInitialSession(null, []), null);
});

// The server's memory follows the user across devices. These pin the
// contract the live shell relies on: writes reach the server in gesture
// order, one adoption barrier is shared until it settles, a late answer
// never overrides a choice made meanwhile, and a server that does not
// answer leaves the browser's memory in charge within the bound.
import { LastSessionMemory, type LastSessionRemote } from "./last-session.ts";

function memoryStorage(): LastSessionStorage & { map: Map<string, string> } {
  const map = new Map<string, string>();
  return {
    map,
    getItem: (key) => map.get(key) ?? null,
    setItem: (key, value) => { map.set(key, value); },
    removeItem: (key) => { map.delete(key); },
  };
}

function deferred<T>(): { promise: Promise<T>; resolve: (value: T) => void; reject: (err: unknown) => void } {
  let resolve!: (value: T) => void;
  let reject!: (err: unknown) => void;
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

const flush = () => new Promise<void>((resolve) => setImmediate(resolve));

test("server writes run one at a time in the order the user chose", async () => {
  const storage = memoryStorage();
  const started: Array<string | null> = [];
  const pending: Array<{ resolve: (value: void) => void }> = [];
  const remote: LastSessionRemote = {
    read: () => Promise.resolve(null),
    write: (id) => {
      started.push(id);
      const d = deferred<void>();
      pending.push(d);
      return d.promise;
    },
  };
  const memory = new LastSessionMemory(storage, remote);
  memory.remember("old");
  memory.remember(null);
  memory.remember("new");
  await flush();
  assert.deepEqual(started, ["old"], "the second write waits for the first to land");
  assert.equal(readLastSession(storage), "new", "the browser already holds the latest choice");
  pending[0]!.resolve();
  await flush();
  assert.deepEqual(started, ["old", null]);
  pending[1]!.resolve();
  await flush();
  assert.deepEqual(started, ["old", null, "new"], "the server ends on the user's latest choice");
});

test("a failed server write does not stall the writes after it", async () => {
  const storage = memoryStorage();
  const started: Array<string | null> = [];
  const remote: LastSessionRemote = {
    read: () => Promise.resolve(null),
    write: (id) => {
      started.push(id);
      return id === "bad" ? Promise.reject(new Error("offline")) : Promise.resolve();
    },
  };
  const memory = new LastSessionMemory(storage, remote);
  memory.remember("bad");
  memory.remember("good");
  await flush();
  await flush();
  assert.deepEqual(started, ["bad", "good"]);
});

test("a clear with nothing remembered writes nothing", async () => {
  const storage = memoryStorage();
  const started: Array<string | null> = [];
  const memory = new LastSessionMemory(storage, {
    read: () => Promise.resolve(null),
    write: (id) => { started.push(id); return Promise.resolve(); },
  });
  memory.remember(null);
  await flush();
  assert.deepEqual(started, []);
  memory.remember("s1");
  memory.remember(null);
  await flush();
  await flush();
  assert.deepEqual(started, ["s1", null], "a clear of a remembered choice does reach the server");
  assert.equal(readLastSession(storage), null);
});

test("adoption is one shared barrier that mirrors the server into the browser", async () => {
  const storage = memoryStorage();
  writeLastSession(storage, "local");
  const read = deferred<string | null>();
  const memory = new LastSessionMemory(storage, { read: () => read.promise, write: () => Promise.resolve() });
  const first = memory.adopt();
  const second = memory.adopt();
  assert.equal(first, second, "an overlapping refresh waits on the same adoption, not a null");
  let settled = false;
  void first.then(() => { settled = true; });
  await flush();
  assert.equal(settled, false, "the barrier holds until the server answers");
  assert.equal(readLastSession(storage), "local");
  read.resolve("remote");
  await first;
  assert.equal(readLastSession(storage), "remote", "the server's choice outranks the browser's old one");
  assert.equal(memory.adopt(), first, "a later call shares the settled barrier");
});

test("a late server answer never overrides a choice the user made meanwhile", async () => {
  const storage = memoryStorage();
  const read = deferred<string | null>();
  const memory = new LastSessionMemory(storage, { read: () => read.promise, write: () => Promise.resolve() });
  const adoption = memory.adopt();
  memory.remember("chosen");
  read.resolve("remote");
  await adoption;
  assert.equal(readLastSession(storage), "chosen");
});

test("a server that does not answer within the bound leaves the browser in charge", async () => {
  const storage = memoryStorage();
  writeLastSession(storage, "local");
  const timer = deferred<void>();
  let cancelled = false;
  const memory = new LastSessionMemory(
    storage,
    { read: () => new Promise(() => {}), write: () => Promise.resolve() },
    3000,
    () => ({ done: timer.promise, cancel: () => { cancelled = true; } }),
  );
  const adoption = memory.adopt();
  let settled = false;
  void adoption.then(() => { settled = true; });
  await flush();
  assert.equal(settled, false);
  timer.resolve();
  await adoption;
  assert.equal(readLastSession(storage), "local");
  assert.equal(cancelled, true, "the bound's timer is released either way");
});

test("a server read that fails leaves the browser in charge", async () => {
  const storage = memoryStorage();
  writeLastSession(storage, "local");
  const memory = new LastSessionMemory(storage, {
    read: () => Promise.reject(new Error("offline")),
    write: () => Promise.resolve(),
  });
  await memory.adopt();
  assert.equal(readLastSession(storage), "local");
});
