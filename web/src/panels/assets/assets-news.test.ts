import assert from "node:assert/strict";
import test, { mock } from "node:test";

import type { AssetEntry, AssetListing } from "../../api/assets.ts";
import { AssetsFeed, AssetsSeen, AssetsWatcher, WATCH_INTERVAL_MS, assetsNews, assetsSeenKey, newestMtime, unseenAssets, type AssetsUnseen } from "./assets-news.ts";

function file(path: string, mtime: string): AssetEntry {
  return { path, name: path.split("/").at(-1)!, kind: "file", size: 1, mtime, version: `v-${mtime}`, locked: null };
}

function listing(...entries: AssetEntry[]): AssetListing {
  return {
    locations: [{ id: "assets", name: "Assets folder", path: ".deck/assets/", truncated: false, entries }],
    scanned: "2026-10-18T12:00:00Z",
    preview_max_bytes: 1 << 20,
  };
}

function memoryStorage(): Map<string, string> & Pick<Storage, "getItem" | "setItem"> {
  const store = new Map<string, string>() as Map<string, string> & Pick<Storage, "getItem" | "setItem">;
  store.getItem = (key) => store.get(key) ?? null;
  store.setItem = (key, value) => void store.set(key, value);
  return store;
}

const folder: AssetEntry = { path: ".deck/build/w", name: "w", kind: "dir", size: 0, mtime: "2026-10-18T13:00:00Z", version: "d", locked: "Folder — view only" };

test("new is a file later than the last-seen instant; folders never count", () => {
  const scan = listing(
    file(".deck/assets/a.png", "2026-10-18T11:00:00Z"),
    file(".deck/assets/b.png", "2026-10-18T11:05:00Z"),
    file(".deck/assets/c.png", "2026-10-18T11:03:00Z"),
  );
  scan.locations[0]!.entries.push(folder);
  assert.deepEqual(unseenAssets(scan, "2026-10-18T11:00:00Z"), { count: 2, newest: "2026-10-18T11:05:00Z" });
  assert.deepEqual(unseenAssets(scan, "2026-10-18T11:05:00Z"), { count: 0, newest: null });
  assert.equal(newestMtime(scan), "2026-10-18T11:05:00Z");
});

test("the news line names the count and the newest file's age", () => {
  const now = Date.parse("2026-10-18T12:00:00Z");
  assert.equal(assetsNews({ count: 3, newest: "2026-10-18T11:59:00Z" }, now), "3 new assets · newest 1m ago");
  assert.equal(assetsNews({ count: 1, newest: "2026-10-18T11:59:40Z" }, now), "1 new asset · just now");
  assert.equal(assetsNews({ count: 2, newest: "2026-10-18T11:59:59Z" }, now), "2 new assets · newest just now");
  assert.equal(assetsNews({ count: 0, newest: null }, now), null);
});

test("a first visit counts nothing new and remembers the newest file", () => {
  const storage = memoryStorage();
  const seen = new AssetsSeen("p1", storage);
  const scan = listing(file(".deck/assets/a.png", "2026-10-18T11:00:00Z"));
  const start = seen.baseline(scan);
  assert.equal(start, "2026-10-18T11:00:00Z");
  assert.equal(storage.get(assetsSeenKey("p1")), "2026-10-18T11:00:00Z");
  assert.equal(assetsSeenKey("p1"), "noisedeck.assets.seen.p1");
  assert.deepEqual(unseenAssets(scan, start), { count: 0, newest: null });
});

test("a first visit to empty folders makes the first file new", () => {
  const seen = new AssetsSeen("p1", memoryStorage());
  const start = seen.baseline(listing());
  const later = listing(file(".deck/assets/a.png", "2026-10-18T11:00:00Z"));
  assert.equal(unseenAssets(later, start).count, 1);
});

test("marking seen moves to the newest listed mtime and never back", () => {
  const storage = memoryStorage();
  const seen = new AssetsSeen("p1", storage);
  seen.markSeen(listing(file(".deck/assets/a.png", "2026-10-18T11:05:00Z")));
  assert.equal(seen.value, "2026-10-18T11:05:00Z");
  seen.markSeen(listing(file(".deck/assets/old.png", "2026-10-18T10:00:00Z")));
  assert.equal(seen.value, "2026-10-18T11:05:00Z", "a listing after a delete does not rewind the instant");
  const scan = listing(file(".deck/assets/old.png", "2026-10-18T10:00:00Z"), file(".deck/assets/b.png", "2026-10-18T11:06:00Z"));
  assert.deepEqual(unseenAssets(scan, seen.value!), { count: 1, newest: "2026-10-18T11:06:00Z" });
});

test("blocked storage holds the instant for this page", () => {
  const blocked = {
    getItem: () => { throw new Error("denied"); },
    setItem: () => { throw new Error("denied"); },
  };
  const seen = new AssetsSeen("p1", blocked);
  assert.equal(seen.value, null);
  seen.markSeen(listing(file(".deck/assets/a.png", "2026-10-18T11:05:00Z")));
  assert.equal(seen.value, "2026-10-18T11:05:00Z");
  assert.equal(new AssetsSeen("p1", null).baseline(listing()), "1970-01-01T00:00:00Z");
});

function fakeClient(scans: AssetListing[]) {
  const state = {
    reads: 0,
    subscribed: 0,
    unsubscribed: 0,
    fire: () => {},
    close: () => {},
    pending: [] as Array<() => void>,
    hold: false,
  };
  const client = {
    async listProjectAssets(): Promise<AssetListing> {
      state.reads++;
      const scan = scans[Math.min(state.reads - 1, scans.length - 1)]!;
      if (state.hold) await new Promise<void>((resolve) => state.pending.push(resolve));
      return structuredClone(scan);
    },
    subscribeProjectAssetEvents(_project: string, onChange: () => void, onClosed?: () => void): () => void {
      state.subscribed++;
      state.fire = onChange;
      state.close = () => onClosed?.();
      return () => void state.unsubscribed++;
    },
  };
  return { client, state };
}

const settle = () => new Promise<void>((resolve) => setImmediate(resolve));

test("the watcher counts files that land after the baseline and forgets on stop", async () => {
  const a = file(".deck/assets/a.png", "2026-10-18T11:00:00Z");
  const b = file(".deck/assets/b.png", "2026-10-18T11:01:00Z");
  const c = file(".deck/assets/c.png", "2026-10-18T11:02:00Z");
  const { client, state } = fakeClient([listing(a), listing(a, b, c)]);
  const seen: AssetsUnseen[] = [];
  const watcher = new AssetsWatcher(client, "p1", new AssetsSeen("p1", memoryStorage()), (unseen) => seen.push(unseen));
  watcher.start();
  await settle();
  assert.deepEqual(seen.at(-1), { count: 0, newest: null }, "the first read is the baseline");
  state.fire();
  await settle();
  assert.deepEqual(seen.at(-1), { count: 2, newest: "2026-10-18T11:02:00Z" });
  watcher.stop();
  assert.equal(state.unsubscribed, 1);
  assert.deepEqual(watcher.unseen, { count: 0, newest: null });
  state.fire();
  await settle();
  assert.equal(state.reads, 2, "a stopped watcher reads nothing");
});

test("a burst of events while a read is in flight costs one more read", async () => {
  const a = file(".deck/assets/a.png", "2026-10-18T11:00:00Z");
  const { client, state } = fakeClient([listing(a)]);
  const watcher = new AssetsWatcher(client, "p1", new AssetsSeen("p1", memoryStorage()), () => {});
  watcher.start();
  await settle();
  state.hold = true;
  state.fire();
  state.fire();
  state.fire();
  state.fire();
  await settle();
  assert.equal(state.reads, 2);
  state.hold = false;
  state.pending.shift()!();
  await settle();
  await settle();
  assert.equal(state.reads, 3, "the three events behind the first coalesce into one read");
  watcher.stop();
});

test("a closed feed falls back to polling the listing", async () => {
  mock.timers.enable({ apis: ["setInterval"] });
  try {
    const a = file(".deck/assets/a.png", "2026-10-18T11:00:00Z");
    const b = file(".deck/assets/b.png", "2026-10-18T11:01:00Z");
    const { client, state } = fakeClient([listing(a), listing(a, b)]);
    const seen: AssetsUnseen[] = [];
    const watcher = new AssetsWatcher(client, "p1", new AssetsSeen("p1", memoryStorage()), (unseen) => seen.push(unseen));
    watcher.start();
    await settle();
    mock.timers.tick(WATCH_INTERVAL_MS);
    await settle();
    assert.equal(state.reads, 1, "an open feed is not polled");
    state.close();
    mock.timers.tick(WATCH_INTERVAL_MS);
    await settle();
    assert.equal(state.reads, 2);
    assert.deepEqual(seen.at(-1), { count: 1, newest: "2026-10-18T11:01:00Z" });
    watcher.stop();
  } finally {
    mock.timers.reset();
  }
});

test("the feed lives per project: a session switch inside it keeps the feed, the count and the news time", async () => {
  const a = file(".deck/assets/a.png", "2026-10-18T11:00:00Z");
  const b = file(".deck/assets/b.png", "2026-10-18T11:01:00Z");
  const { client, state } = fakeClient([listing(a), listing(a, b)]);
  let clock = 1_000;
  let changes = 0;
  const feed = new AssetsFeed(client, () => memoryStorage(), () => void changes++, () => clock);
  feed.follow("p1", false);
  await settle();
  state.fire();
  await settle();
  assert.equal(feed.count, 1);
  assert.equal(feed.at, 1_000);
  const subscribed = state.subscribed;
  const reads = state.reads;
  clock = 5_000;
  changes = 0;
  feed.follow("p1", false);
  await settle();
  assert.equal(state.subscribed, subscribed, "the same project does not resubscribe");
  assert.equal(state.reads, reads, "and does not rescan");
  assert.equal(feed.count, 1, "the count survives the switch");
  assert.equal(feed.at, 1_000, "the news keeps the time it became news");
  assert.equal(changes, 0, "nothing repaints");
  feed.follow("p2", false);
  await settle();
  assert.equal(state.unsubscribed, 1, "a different project drops the old feed");
  assert.equal(state.subscribed, subscribed + 1, "and follows its own");
  assert.equal(feed.projectID, "p2");
  feed.stop();
});

test("the feed pauses while the panel is open and starts over when no session is open", async () => {
  const a = file(".deck/assets/a.png", "2026-10-18T11:00:00Z");
  const b = file(".deck/assets/b.png", "2026-10-18T11:01:00Z");
  const { client, state } = fakeClient([listing(a), listing(a, b)]);
  const feed = new AssetsFeed(client, () => memoryStorage(), () => {}, () => 7);
  feed.follow("p1", false);
  await settle();
  state.fire();
  await settle();
  assert.equal(feed.count, 1);
  feed.follow("p1", true);
  assert.equal(feed.count, 0, "an open panel has nothing unseen");
  assert.equal(feed.at, null);
  assert.equal(state.unsubscribed, 1);
  feed.stop();
  assert.equal(feed.projectID, null);
});

test("only the reading a start makes is marked first", async () => {
  const a = file(".deck/assets/a.png", "2026-10-18T11:00:00Z");
  const b = file(".deck/assets/b.png", "2026-10-18T11:01:00Z");
  const { client, state } = fakeClient([listing(a, b), listing(a, b)]);
  const storage = memoryStorage();
  storage.setItem(assetsSeenKey("p1"), "2026-10-18T10:00:00Z");
  const firsts: boolean[] = [];
  const watcher = new AssetsWatcher(client, "p1", new AssetsSeen("p1", storage), (_unseen, first) => firsts.push(first));
  watcher.start();
  await settle();
  state.fire();
  await settle();
  watcher.stop();
  watcher.start();
  await settle();
  assert.deepEqual(firsts, [true, false, true], "files waiting at a start are not arrivals");
  watcher.stop();
});

test("the feed reports an arrival only for an increase the running watcher sees", async (t) => {
  const a = file(".deck/assets/a.png", "2026-10-18T11:00:00Z");
  const b = file(".deck/assets/b.png", "2026-10-18T11:01:00Z");
  const c = file(".deck/assets/c.png", "2026-10-18T11:02:00Z");
  const { client, state } = fakeClient([listing(a, b), listing(a, b, c), listing(a, b, c)]);
  const storage = memoryStorage();
  storage.setItem(assetsSeenKey("p1"), "2026-10-18T10:00:00Z");
  const arrivals: boolean[] = [];
  const feed = new AssetsFeed(client, () => storage, (arrived) => arrivals.push(arrived));
  t.after(() => feed.stop());
  feed.follow("p1", false);
  await settle();
  assert.equal(feed.count, 2);
  assert.deepEqual(arrivals, [false], "files waiting when the feed starts are not arrivals");
  state.fire();
  await settle();
  assert.deepEqual(arrivals, [false, true], "a file that lands is");
  feed.follow("p1", false);
  await settle();
  assert.deepEqual(arrivals, [false, true], "a session switch inside the project reports nothing");
  feed.stop();
});

test("a project switch and back, and a resume after the panel closes, report no arrival until a file lands", async (t) => {
  const a = file(".deck/assets/a.png", "2026-10-18T11:00:00Z");
  const b = file(".deck/assets/b.png", "2026-10-18T11:01:00Z");
  const c = file(".deck/assets/c.png", "2026-10-18T11:02:00Z");
  const { client, state } = fakeClient([listing(a, b)]);
  const storage = memoryStorage();
  storage.setItem(assetsSeenKey("p1"), "2026-10-18T10:00:00Z");
  storage.setItem(assetsSeenKey("p2"), "2026-10-18T10:00:00Z");
  const arrivals: boolean[] = [];
  const feed = new AssetsFeed(client, () => storage, (arrived) => arrivals.push(arrived));
  t.after(() => feed.stop());
  feed.follow("p1", false);
  await settle();
  feed.follow("p2", false);
  await settle();
  feed.follow("p1", false);
  await settle();
  assert.equal(feed.count, 2, "each first read counts the waiting files");
  assert.ok(!arrivals.includes(true), "a project switch, there and back, is never an arrival");
  feed.pause();
  storage.setItem(assetsSeenKey("p1"), "2026-10-18T10:30:00Z");
  feed.resume();
  await settle();
  assert.ok(!arrivals.includes(true), "the read a resume makes is never an arrival");
  const scans = [listing(a, b, c)];
  client.listProjectAssets = async () => structuredClone(scans[0]!);
  state.fire();
  await settle();
  assert.equal(arrivals.at(-1), true, "a file landing after that first read is");
  feed.stop();
});
