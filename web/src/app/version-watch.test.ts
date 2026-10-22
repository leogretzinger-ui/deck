import assert from "node:assert/strict";
import test from "node:test";

import { documentBuildID, fetchBuildID, VersionWatcher, type VersionTimer } from "./version-watch.ts";

class FakeTimer implements VersionTimer {
  callback: (() => void) | null = null;
  clears = 0;
  set(callback: () => void): number {
    this.callback = callback;
    return 7;
  }
  clear(id: number): void {
    assert.equal(id, 7);
    this.clears += 1;
    this.callback = null;
  }
}

test("the document build identity is the bundle baseline", () => {
  const doc = (content: string | undefined) => ({
    querySelector: () => content === undefined ? null : { content },
  }) as unknown as Document;
  assert.equal(documentBuildID(doc("build-a")), "build-a");
  assert.equal(documentBuildID(doc("__NOISEDECK_BUILD_ID__")), null);
  assert.equal(documentBuildID(doc(undefined)), null);
});

test("a current tab stays quiet across repeated checks", async () => {
  const timer = new FakeTimer();
  let notices = 0;
  const watcher = new VersionWatcher("build-a", async () => "build-a", () => { notices += 1; }, timer);
  watcher.start();
  await Promise.resolve();
  await watcher.poll();
  assert.equal(notices, 0);
  assert.equal(timer.clears, 0);
  watcher.dispose();
});

test("an old tab announces once and stops polling without reloading", async () => {
  const timer = new FakeTimer();
  let notices = 0;
  const watcher = new VersionWatcher("build-a", async () => "build-b", () => { notices += 1; }, timer);
  watcher.start();
  await Promise.resolve();
  await watcher.poll();
  assert.equal(notices, 1);
  assert.equal(timer.clears, 1);
  await watcher.poll();
  assert.equal(notices, 1);
});

test("offline and authentication failures remain quiet and retry later", async () => {
  const timer = new FakeTimer();
  let attempt = 0;
  let notices = 0;
  const watcher = new VersionWatcher("build-a", async () => {
    attempt += 1;
    if (attempt < 3) throw new Error(attempt === 1 ? "offline" : "unauthorized");
    return "build-b";
  }, () => { notices += 1; }, timer);
  await watcher.poll();
  await watcher.poll();
  await watcher.poll();
  assert.equal(attempt, 3);
  assert.equal(notices, 1);
});

test("dispose fences a late answer", async () => {
  let release!: (id: string) => void;
  const answer = new Promise<string>((resolve) => { release = resolve; });
  let notices = 0;
  const watcher = new VersionWatcher("build-a", () => answer, () => { notices += 1; }, new FakeTimer());
  watcher.start();
  watcher.dispose();
  release("build-b");
  await answer;
  await Promise.resolve();
  assert.equal(notices, 0);
});

test("a build check reports authorization loss before the watcher can retry", async () => {
  const original = globalThis.fetch;
  try {
    for (const [status, code] of [[401, "unauthorized"], [403, "unlock-required"]] as const) {
      let requests = 0;
      globalThis.fetch = async (input, init) => {
        requests += 1;
        assert.equal(input, "/api/build");
        assert.equal(init?.credentials, "same-origin");
        assert.equal(init?.cache, "no-store");
        return new Response(JSON.stringify({ code }), { status, headers: { "Content-Type": "application/json" } });
      };
      let losses = 0;
      const watcher = new VersionWatcher("build-a", () => fetchBuildID(() => {
        losses += 1;
        watcher.dispose();
      }), () => assert.fail("a refusal cannot announce a new build"), new FakeTimer());
      await watcher.poll();
      assert.equal(losses, 1);
      await watcher.poll();
      assert.equal(requests, 1, "the disposed workspace does not keep polling");
    }
  } finally { globalThis.fetch = original; }
});
