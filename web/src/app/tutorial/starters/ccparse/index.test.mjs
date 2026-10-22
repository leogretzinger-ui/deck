import test from "node:test";
import assert from "node:assert/strict";
import { parse } from "./index.mjs";

test("type, scope and description", () => {
  assert.deepEqual(parse("feat(cli): accept a range argument"), { type: "feat", scope: "cli", breaking: false, description: "accept a range argument" });
});

test("no scope", () => {
  assert.deepEqual(parse("fix: handle an empty log"), { type: "fix", scope: null, breaking: false, description: "handle an empty log" });
});

test("a subject that does not follow the form", () => {
  assert.deepEqual(parse("Merge branch 'main'"), { type: null, scope: null, breaking: false, description: "Merge branch 'main'" });
});

test("surrounding whitespace is ignored", () => {
  assert.equal(parse("  docs: trim  ").description, "trim");
});
