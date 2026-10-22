// The preview's agent assets, shaped like the server's scan of the five
// .deck/ locations, and the delete answer the server gives for them. Kept
// free of TypeScript the node test runner cannot strip, like
// fixture-files.ts. Ages are relative to the clock the listing is built
// against, so a capture renders the same ages whenever it runs.
import type { AssetDeleteFile, AssetDeleteResult, AssetEntry, AssetListing, AssetLocation } from "../api/assets.ts";

export type FixtureAssetVariant = "list" | "truncated";

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const FOLDER_LOCK = "Folder — view only";

interface Row {
  name: string;
  dir?: boolean;
  size?: number;
  age: number;
}

const LOCATIONS: Array<{ id: string; name: string; path: string; rows: Row[] }> = [
  { id: "assets", name: "Assets folder", path: ".deck/assets/", rows: [
    { name: "home.png", size: 412_000, age: 2 * MINUTE },
    { name: "home-mobile.png", size: 188_000, age: 2 * MINUTE + 5_000 },
    { name: "chart-latency.png", size: 96_000, age: 6 * MINUTE },
    { name: "icon-512.png", size: 31_000, age: HOUR },
  ] },
  { id: "build", name: "Build scratch", path: ".deck/build/", rows: [
    { name: "worktrees/task-7", dir: true, age: 3 * HOUR + MINUTE },
  ] },
  { id: "visual-diff", name: "Visual-diff captures", path: ".deck/visual-diff/", rows: [
    { name: "actual/home.png", size: 415_000, age: 8 * MINUTE },
  ] },
  { id: "runs", name: "Run receipts", path: ".deck/runs/", rows: [
    { name: "run-14.json", size: 3_000, age: 8 * MINUTE },
  ] },
  { id: "sessions", name: "Session scratch", path: ".deck/sessions/", rows: [
    { name: "claude-7c1e", dir: true, age: 10_000 },
  ] },
];

// fixtureAssetVersion is the deterministic stand-in for the server's
// opaque version, one value per entry, so a delete round-trips inside the
// preview the way it does against the real endpoint.
export function fixtureAssetVersion(path: string, mtime: string): string {
  return `1-fixture-${path}-${mtime}`;
}

// fixtureAssetListing builds the listing as the server would report it at
// `now`. The truncated variant is the "500+ entries" state: Build
// scratch stopped at the scan bound.
export function fixtureAssetListing(now: number, variant: FixtureAssetVariant = "list"): AssetListing {
  const locations: AssetLocation[] = LOCATIONS.map((loc) => ({
    id: loc.id,
    name: loc.name,
    path: loc.path,
    truncated: variant === "truncated" && loc.id === "build",
    entries: loc.rows.map((row): AssetEntry => {
      const path = loc.path + row.name;
      const mtime = new Date(now - row.age).toISOString();
      return {
        path,
        name: row.name,
        kind: row.dir === true ? "dir" : "file",
        size: row.dir === true ? 0 : row.size ?? 0,
        mtime,
        version: fixtureAssetVersion(path, mtime),
        locked: row.dir === true ? FOLDER_LOCK : null,
      };
    }),
  }));
  return { locations, scanned: new Date(now).toISOString(), preview_max_bytes: 16 << 20 };
}

// fixtureAssetDeleteAnswer applies a delete to a fixture listing the way
// the server applies it to disk: each file on its own, removed only when it
// is a listed file whose version still matches.
export function fixtureAssetDeleteAnswer(listing: AssetListing, files: AssetDeleteFile[]): AssetDeleteResult[] {
  return files.map(({ path, version }) => {
    const location = listing.locations.find((loc) => path.startsWith(loc.path) && path.length > loc.path.length);
    if (location === undefined) {
      return { path, outcome: "skipped", reason: "Outside the agent locations" };
    }
    const index = location.entries.findIndex((entry) => entry.path === path);
    const entry = location.entries[index];
    if (entry === undefined) {
      return { path, outcome: "skipped", reason: "Already gone" };
    }
    if (entry.kind !== "file") {
      return { path, outcome: "skipped", reason: "Not a regular file" };
    }
    if (entry.version !== version) {
      const at = new Date(entry.mtime);
      const hhmm = `${String(at.getHours()).padStart(2, "0")}:${String(at.getMinutes()).padStart(2, "0")}`;
      return { path, outcome: "skipped", reason: `Changed on disk after you selected it — modified ${hhmm}` };
    }
    location.entries.splice(index, 1);
    return { path, outcome: "deleted" };
  });
}
