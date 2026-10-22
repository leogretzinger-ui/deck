import { test } from "node:test";
import assert from "node:assert/strict";
import { ApiError } from "../../api/http.ts";
import type { AssetDeleteFile, AssetDeleteResult, AssetListing } from "../../api/assets.ts";
import { fixtureAssetListing } from "../../app/fixture-assets.ts";
import {
  AssetsPanelModel,
  DELETE_BATCH,
  UNCONFIRMED_REASON,
  countWord,
  deleteTitle,
  isImageAsset,
  outcomeDescription,
  outcomeFact,
  outcomeTitle,
  truncatedNote,
  type AssetOutcome,
} from "./assets-panel.ts";

const NOW = Date.parse("2026-10-18T12:00:00Z");

class FakeClient {
  listing: AssetListing = fixtureAssetListing(NOW);
  lists = 0;
  sent: AssetDeleteFile[][] = [];
  subscribed = 0;
  unsubscribed = 0;
  onChange: (() => void) | null = null;
  answer: (files: AssetDeleteFile[]) => Promise<AssetDeleteResult[]> = async (files) =>
    files.map((file) => ({ path: file.path, outcome: "deleted" }));
  async listProjectAssets(): Promise<AssetListing> {
    this.lists++;
    return structuredClone(this.listing);
  }
  async deleteProjectAssets(_project: string, files: AssetDeleteFile[]): Promise<AssetDeleteResult[]> {
    this.sent.push(files);
    return this.answer(files);
  }
  subscribeProjectAssetEvents(_project: string, onChange: () => void): () => void {
    this.subscribed++;
    this.onChange = onChange;
    return () => {
      this.unsubscribed++;
      this.onChange = null;
    };
  }
}

async function opened(client = new FakeClient()): Promise<{ client: FakeClient; model: AssetsPanelModel }> {
  const model = new AssetsPanelModel(client, "proj-1");
  await model.open();
  return { client, model };
}

test("open scans and follows the change feed; close stops following it", async () => {
  const { client, model } = await opened();
  assert.equal(model.state, "ready");
  assert.equal(client.lists, 1);
  assert.equal(client.subscribed, 1);
  client.onChange?.();
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(client.lists, 2, "an assets-changed event rescans");
  model.close();
  assert.equal(client.unsubscribed, 1);
  assert.equal(client.onChange, null);
});

test("the resting summary counts listed files and their bytes", async () => {
  const { model } = await opened();
  assert.equal(model.summary, "6 files · 1.1 MB");
});

test("a truncated scan says its totals are a listing and names the location", async () => {
  const client = new FakeClient();
  client.listing = fixtureAssetListing(NOW, "truncated");
  const { model } = await opened(client);
  assert.equal(model.summary, "6 files listed · 1.1 MB");
  assert.equal(truncatedNote(model.truncated), "The scan stopped at 500 entries in Build scratch, so some files there aren't listed. Totals count listed files only.");
  assert.equal(truncatedNote([...model.truncated, model.locations[0]!, model.locations[3]!]),
    "The scan stopped at 500 entries in Build scratch, Assets folder and Run receipts, so some files there aren't listed. Totals count listed files only.");
});

test("only regular unlocked files join the selection", async () => {
  const { model } = await opened();
  model.toggle(".deck/assets/home.png");
  model.toggle(".deck/assets/home-mobile.png");
  model.toggle(".deck/build/worktrees/task-7");
  model.toggle(".deck/sessions/claude-7c1e");
  assert.equal(model.selectedCount, 2);
  assert.equal(model.summary, "2 selected · 600.0 KB");
  model.toggle(".deck/assets/home.png");
  assert.equal(model.selectedCount, 1);
  model.clearSelection();
  assert.equal(model.selectedCount, 0);
});

test("delete sends exactly the selected paths with the versions they were picked at", async () => {
  const { client, model } = await opened();
  const home = model.entries().find((entry) => entry.name === "home.png")!;
  const run = model.entries().find((entry) => entry.name === "run-14.json")!;
  model.toggle(home.path);
  model.toggle(".deck/runs/run-14.json");
  // A rescan that sees a rewritten file must not move the picked version.
  client.listing.locations[0]!.entries[0]!.version = "2-rewritten";
  await model.refresh();
  const outcomes = await model.delete();
  assert.deepEqual(client.sent, [[
    { path: home.path, version: home.version },
    { path: run.path, version: run.version },
  ]]);
  assert.deepEqual(outcomes.map((o) => o.outcome), ["deleted", "deleted"]);
  assert.equal(model.selectedCount, 0);
  assert.ok(!model.entries().some((entry) => entry.path === home.path), "a deleted file leaves the list");
});

test("skipped and unanswered files stay with their outcome until the next scan", async () => {
  const client = new FakeClient();
  client.answer = async (files) => [
    { path: files[0]!.path, outcome: "deleted" },
    { path: files[1]!.path, outcome: "skipped", reason: "The workspace refused: permission denied" },
  ];
  const { model } = await opened(client);
  model.toggle(".deck/assets/home.png");
  model.toggle(".deck/assets/icon-512.png");
  model.toggle(".deck/visual-diff/actual/home.png");
  const outcomes = await model.delete();
  assert.deepEqual(outcomes.map((o) => [o.outcome, o.reason]), [
    ["deleted", ""],
    ["skipped", "The workspace refused: permission denied"],
    ["unconfirmed", UNCONFIRMED_REASON],
  ]);
  assert.equal(outcomeFact(model.outcomeFor(".deck/assets/icon-512.png")!), "Skipped — the workspace refused: permission denied");
  assert.equal(outcomeFact(model.outcomeFor(".deck/visual-diff/actual/home.png")!), "Unconfirmed — delete sent, no answer");
  await model.refresh();
  assert.equal(model.outcomeFor(".deck/assets/icon-512.png"), undefined, "a rescan clears the outcomes");
});

test("a request that fails after it was sent leaves every file in it unconfirmed", async () => {
  const client = new FakeClient();
  client.answer = async () => { throw new ApiError(502, "bad_gateway", "upstream closed"); };
  const { model } = await opened(client);
  model.toggle(".deck/assets/home.png");
  model.toggle(".deck/assets/icon-512.png");
  const outcomes = await model.delete();
  assert.deepEqual(outcomes.map((o) => o.outcome), ["unconfirmed", "unconfirmed"]);
  assert.equal(model.entries().filter((entry) => entry.name.endsWith(".png")).length, 5, "nothing leaves the list");

  client.answer = async () => { throw new TypeError("Failed to fetch"); };
  model.toggle(".deck/assets/home.png");
  assert.deepEqual((await model.delete()).map((o) => o.outcome), ["unconfirmed"]);
});

test("a refusal of the whole request is an answer, not an unknown", async () => {
  const client = new FakeClient();
  client.answer = async () => { throw new ApiError(400, "bad_request", "too many files"); };
  const { model } = await opened(client);
  model.toggle(".deck/assets/home.png");
  const [outcome] = await model.delete();
  assert.equal(outcome!.outcome, "skipped");
  assert.equal(outcome!.reason, "The workspace refused the request: too many files");
});

test("a selection larger than the server's bound goes out in batches", async () => {
  const client = new FakeClient();
  const location = client.listing.locations[0]!;
  const template = location.entries[0]!;
  location.entries = Array.from({ length: DELETE_BATCH + 3 }, (_, i) => ({ ...template, path: `.deck/assets/f${i}.png`, name: `f${i}.png`, version: `v${i}` }));
  const { model } = await opened(client);
  for (const entry of location.entries) model.toggle(entry.path);
  await model.delete();
  assert.deepEqual(client.sent.map((batch) => batch.length), [DELETE_BATCH, 3]);
});

test("the outcome sentences count in words up to nine", () => {
  const make = (kinds: AssetOutcome["outcome"][]): AssetOutcome[] => kinds.map((outcome, i) => ({ path: `p${i}`, size: 1, outcome, reason: "" }));
  assert.equal(outcomeTitle(make(["deleted", "deleted", "skipped", "skipped", "unconfirmed"])), "2 of 5 files deleted");
  assert.equal(outcomeTitle(make(["deleted", "deleted"])), "2 files deleted");
  assert.equal(outcomeTitle(make(["deleted"])), "1 file deleted");
  assert.equal(outcomeDescription(make(["deleted", "deleted", "skipped", "skipped", "unconfirmed"])),
    "Two were skipped and nothing was done to them. One delete is unconfirmed — rescan to see whether it's still there.");
  assert.equal(outcomeDescription(make(["deleted"])), "");
  assert.equal(outcomeDescription(make(["skipped"])), "One was skipped and nothing was done to it.");
  assert.equal(outcomeDescription(make(["unconfirmed", "unconfirmed", "unconfirmed"])),
    "Three deletes are unconfirmed — rescan to see whether they're still there.");
  assert.equal(outcomeDescription(make(Array.from({ length: 12 }, () => "skipped"))), "12 were skipped and nothing was done to them.");
  assert.equal(countWord(9, true), "Nine");
  assert.equal(countWord(10), "10");
  assert.equal(deleteTitle(1), "Delete 1 file?");
  assert.equal(deleteTitle(5), "Delete 5 files?");
});

test("only image files under the preview bound get a thumbnail", () => {
  const listing = fixtureAssetListing(NOW);
  const [home, , , icon] = listing.locations[0]!.entries;
  assert.ok(isImageAsset(home!, listing.preview_max_bytes));
  assert.ok(!isImageAsset({ ...icon!, size: listing.preview_max_bytes + 1 }, listing.preview_max_bytes));
  assert.ok(!isImageAsset(listing.locations[3]!.entries[0]!, listing.preview_max_bytes));
  assert.ok(!isImageAsset(listing.locations[1]!.entries[0]!, listing.preview_max_bytes));
});
