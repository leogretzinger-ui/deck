// The fixture's upload answer, kept free of TypeScript the node test
// runner cannot strip (FixtureApiClient uses parameter properties, so it
// loads only in a browser). One function applies an upload to a fixture
// listing the way the server applies it to a directory: a taken name
// refuses with the ask, consent is the version that ask reported, and a
// free name stores.
import { ApiError } from "../api/http.ts";
import type { FileEntry, UploadConsent } from "../api/files.ts";

// fixtureVersionOf is the deterministic stand-in for the server's opaque
// version: one value per fixture entry, so consent round-trips inside a
// preview the way it does against the real endpoint.
export function fixtureVersionOf(entry: FileEntry): string {
  return `1-fixture-${entry.name}-${entry.mtime}`;
}

export function fixtureUploadAnswer(
  entries: FileEntry[],
  destPath: string,
  size: number,
  consent?: UploadConsent,
  // mtime is when the store happened; a replaced file is a new generation
  // with its own modification time, as on disk.
  mtime = "2026-08-02T11:22:00Z",
): { path: string; size: number } {
  const cut = destPath.lastIndexOf("/");
  const name = cut < 0 ? destPath : destPath.slice(cut + 1);
  const index = entries.findIndex((candidate) => candidate.name === name);
  if (index < 0) {
    entries.push({ name, path: destPath, dir: false, size, mode: "rw-r--r--", mtime });
    return { path: destPath, size };
  }
  const existing = entries[index]!;
  const version = fixtureVersionOf(existing);
  // A folder holding the name is never replaceable, consent or not — the
  // same rule the server enforces at commit.
  if (!existing.dir && consent !== undefined && consent.version === version) {
    entries.splice(index, 1, { ...existing, size, mtime });
    return { path: destPath, size };
  }
  throw new ApiError(409, "conflict", `${name}: destination already exists`, {
    reason: existing.dir ? "directory" : consent === undefined ? "exists" : "changed",
    message: `${name}: destination already exists`,
    existing: {
      name,
      dir: existing.dir,
      size: existing.size ?? 0,
      mtime: existing.mtime,
      version,
    },
  });
}
