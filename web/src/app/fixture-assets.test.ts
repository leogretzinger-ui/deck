import { test } from "node:test";
import assert from "node:assert/strict";
import { fixtureAssetDeleteAnswer, fixtureAssetListing } from "./fixture-assets.ts";

const NOW = Date.parse("2026-10-18T12:00:00Z");

test("the fixture lists the five locations in the server's order and shape", () => {
  const listing = fixtureAssetListing(NOW);
  assert.deepEqual(listing.locations.map((loc) => loc.path), [
    ".deck/assets/", ".deck/build/", ".deck/visual-diff/", ".deck/runs/", ".deck/sessions/",
  ]);
  assert.equal(listing.scanned, "2026-10-18T12:00:00.000Z");
  assert.ok(listing.locations.every((loc) => !loc.truncated));
  for (const loc of listing.locations) {
    const times = loc.entries.map((entry) => Date.parse(entry.mtime));
    assert.deepEqual(times, [...times].sort((a, b) => b - a), `${loc.id} lists newest first`);
    for (const entry of loc.entries) {
      assert.ok(entry.path.startsWith(loc.path) && entry.path.slice(loc.path.length) === entry.name);
      assert.equal(entry.locked, entry.kind === "dir" ? "Folder — view only" : null);
    }
  }
  const home = listing.locations[0]!.entries[0]!;
  assert.equal(home.name, "home.png");
  assert.equal(home.size, 412_000);
  assert.equal(NOW - Date.parse(home.mtime), 2 * 60_000);
});

test("the truncated variant marks Build scratch only", () => {
  const listing = fixtureAssetListing(NOW, "truncated");
  assert.deepEqual(listing.locations.filter((loc) => loc.truncated).map((loc) => loc.id), ["build"]);
});

test("the fixture delete answers each file the way the server does", () => {
  const listing = fixtureAssetListing(NOW);
  const home = listing.locations[0]!.entries.find((entry) => entry.name === "home.png")!;
  const icon = listing.locations[0]!.entries.find((entry) => entry.name === "icon-512.png")!;
  const folder = listing.locations[1]!.entries[0]!;
  const results = fixtureAssetDeleteAnswer(listing, [
    { path: home.path, version: home.version },
    { path: home.path, version: home.version },
    { path: icon.path, version: "1-stale" },
    { path: folder.path, version: folder.version },
    { path: "README.md", version: "v" },
    { path: ".deck/assets/", version: "v" },
  ]);
  assert.deepEqual(results.map((r) => [r.outcome, r.reason]), [
    ["deleted", undefined],
    ["skipped", "Already gone"],
    ["skipped", `Changed on disk after you selected it — modified ${hhmm(icon.mtime)}`],
    ["skipped", "Not a regular file"],
    ["skipped", "Outside the agent locations"],
    ["skipped", "Outside the agent locations"],
  ]);
  assert.ok(!listing.locations[0]!.entries.some((entry) => entry.path === home.path), "a deleted file leaves the listing");
  assert.ok(listing.locations[0]!.entries.some((entry) => entry.path === icon.path), "a skipped file stays");
});

function hhmm(iso: string): string {
  const at = new Date(iso);
  return `${String(at.getHours()).padStart(2, "0")}:${String(at.getMinutes()).padStart(2, "0")}`;
}
