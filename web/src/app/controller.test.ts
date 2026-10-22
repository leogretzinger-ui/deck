import assert from "node:assert/strict";
import test from "node:test";

import {
  claimTabController,
  type ControllerLock,
  type ControllerLockOptions,
  type ControllerStorage,
  type RequestControllerLock,
} from "./controller.ts";

const first = "11111111-1111-4111-8111-111111111111";
const second = "22222222-2222-4222-8222-222222222222";
const third = "33333333-3333-4333-8333-333333333333";

function memory(initial: string | null = null): ControllerStorage {
  let value = initial;
  return {
    getItem: () => value,
    setItem: (_key, next) => { value = next; },
  };
}

class LockPool {
  private held = new Set<string>();

  request: RequestControllerLock = async (
    name: string,
    options: ControllerLockOptions,
    callback: (lock: ControllerLock | null) => Promise<void>,
  ): Promise<void> => {
    if (this.held.has(name)) {
      await callback(null);
      return;
    }
    this.held.add(name);
    try {
      await callback({ name });
    } finally {
      this.held.delete(name);
    }
  };
}

test("same-tab navigation reuses the stored controller after release", async () => {
  const locks = new LockPool();
  const store = memory(first);
  const claim = await claimTabController(store, locks.request, () => second);
  assert.equal(claim.id, first);
  await claim.close();
  const next = await claimTabController(store, locks.request, () => second);
  assert.equal(next.id, first);
  await next.close();
});

test("a concurrently live copied tab rotates while the owner remains stable", async () => {
  const locks = new LockPool();
  const owner = await claimTabController(memory(first), locks.request, () => second);
  const copy = await claimTabController(memory(first), locks.request, () => third);
  assert.equal(owner.id, first);
  assert.equal(copy.id, third);
  assert.notEqual(copy.id, owner.id);
  await owner.close();
  await copy.close();
});

test("simultaneous copied tabs cannot both hold one controller", async () => {
  const locks = new LockPool();
  const a = claimTabController(memory(first), locks.request, () => second);
  const b = claimTabController(memory(first), locks.request, () => third);
  const [firstClaim, secondClaim] = await Promise.all([a, b]);
  assert.deepEqual(new Set([firstClaim.id, secondClaim.id]), new Set([first, third]));
  await firstClaim.close();
  await secondClaim.close();
});

test("invalid storage starts with a fresh valid controller", async () => {
  const locks = new LockPool();
  const claim = await claimTabController(memory("not-a-uuid"), locks.request, () => first);
  assert.equal(claim.id, first);
  await claim.close();
});

test("storage refusal still yields an isolated controller", async () => {
  const refused: ControllerStorage = {
    getItem: () => { throw new Error("denied"); },
    setItem: () => { throw new Error("denied"); },
  };
  const locks = new LockPool();
  const claim = await claimTabController(refused, locks.request, () => first);
  assert.equal(claim.id, first);
  await claim.close();
});

test("an aborted page refuses to allocate a controller", async () => {
  const abort = new AbortController();
  abort.abort(new Error("page hidden"));
  const locks = new LockPool();
  await assert.rejects(
    claimTabController(memory(), locks.request, () => first, abort.signal),
    /page hidden/,
  );
});

test("a lock callback arriving after pagehide releases without yielding a claim", async () => {
  let invoke!: () => Promise<void>;
  const delayed: RequestControllerLock = (_name, _options, callback) =>
    new Promise<void>((resolve, reject) => {
      invoke = async () => {
        try {
          await callback({ name: "delayed" });
          resolve();
        } catch (cause) {
          reject(cause);
        }
      };
    });
  const abort = new AbortController();
  const pending = claimTabController(memory(), delayed, () => first, abort.signal);
  abort.abort(new Error("page hidden"));
  await assert.rejects(pending, /page hidden/);
  await invoke();
});
