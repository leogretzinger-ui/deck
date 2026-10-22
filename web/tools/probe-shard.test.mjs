import { test } from "node:test";
import assert from "node:assert/strict";
import { selectShard } from "./probe-shard.mjs";

test("shards cover every probe exactly once, including separate modes", () => {
  const entries = Array.from({ length: 26 }, (_, i) => ({ probe: i }));
  entries[2] = ["activity", "tasks-panel", { mode: "activity" }];
  const shards = [1, 2, 3].map((index) => selectShard(entries, `${index}/3`));
  assert.deepEqual(shards.map((shard) => shard.length), [9, 9, 8]);
  const flattened = shards.flat();
  assert.equal(new Set(flattened).size, entries.length);
  for (const entry of entries) assert.ok(flattened.includes(entry));
  assert.deepEqual(shards[2][0], entries[2]);
});

test("local runs retain the full inventory", () => {
  const entries = ["one", "two"];
  assert.equal(selectShard(entries), entries);
  assert.deepEqual(selectShard(entries, "1/1"), entries);
});

test("invalid or empty shards fail instead of passing with no tests", () => {
  for (const shard of ["", "0/3", "4/3", "1/0", "1/4", "1.5/3", "1/3junk", "9007199254740992/9007199254740992"]) {
    assert.throws(() => selectShard(["a", "b", "c"], shard));
  }
});
