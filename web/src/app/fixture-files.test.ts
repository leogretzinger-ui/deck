// The fixture's upload answer, kept free of TypeScript the node test
// runner cannot strip (FixtureApiClient uses parameter properties, so it
// loads only in a browser). These tests pin the contract the preview's
// conflict state rides on: a taken name asks with a version, consent is
// that version, and a free name stores.
import assert from "node:assert/strict";
import test from "node:test";

import { ApiError } from "../api/http.ts";
import { fixtureUploadAnswer, fixtureVersionOf } from "./fixture-files.ts";
import type { FileEntry } from "../api/files.ts";

function entry(name: string, size = 100, mtime = "2026-08-02T11:00:00Z"): FileEntry {
  return { name, path: name, dir: false, size, mode: "rw-r--r--", mtime };
}

function refusalOf(entries: FileEntry[], destPath: string, size: number, version?: string): ApiError {
  try {
    fixtureUploadAnswer(entries, destPath, size, version === undefined ? undefined : { version });
  } catch (err) {
    assert.ok(err instanceof ApiError, "the refusal is an ApiError");
    return err;
  }
  throw new Error("upload over a taken fixture name did not refuse");
}

test("a taken name asks with a version and leaves the entry alone", async () => {
  const entries = [entry("go.mod", 412)];
  const err = refusalOf(entries, "go.mod", 17);
  assert.equal(err.status, 409);
  assert.equal(err.conflict?.reason, "exists");
  assert.equal(err.conflict?.existing.name, "go.mod");
  assert.equal(err.conflict?.existing.size, 412);
  assert.deepEqual(entries, [entry("go.mod", 412)], "the refused upload changed nothing");
});

test("consent carrying the asked version replaces; any other version re-asks", async () => {
  const entries = [entry("go.mod", 412)];
  const version = (refusalOf(entries, "go.mod", 17)).conflict?.existing.version;
  assert.ok(version !== undefined);
  assert.equal(fixtureVersionOf(entries[0]!), version, "the version is deterministic for the entry");
  const reask = refusalOf(entries, "go.mod", 17, "1-someone-elses-file");
  assert.equal(reask.conflict?.reason, "changed", "stale consent reports the changed target");
  const stored = fixtureUploadAnswer(entries, "go.mod", 17, { version });
  assert.deepEqual(stored, { path: "go.mod", size: 17 });
  assert.equal(entries.filter((candidate) => candidate.name === "go.mod").length, 1);
  assert.equal(entries[0]?.size, 17);
});

test("a directory holding the name asks as a directory and never consents", async () => {
  const entries: FileEntry[] = [{ name: "docs", path: "docs", dir: true, size: 4096, mode: "rwxr-xr-x", mtime: "2026-08-02T09:00:00Z" }];
  const err = refusalOf(entries, "docs", 10, fixtureVersionOf(entries[0]!));
  assert.equal(err.conflict?.reason, "directory", "even matching consent cannot replace a folder");
});

test("a free name stores into the listing", () => {
  const entries = [entry("go.mod", 412)];
  const stored = fixtureUploadAnswer(entries, "notes.md", 6);
  assert.deepEqual(stored, { path: "notes.md", size: 6 });
  assert.ok(entries.some((candidate) => candidate.name === "notes.md" && candidate.size === 6));
});
