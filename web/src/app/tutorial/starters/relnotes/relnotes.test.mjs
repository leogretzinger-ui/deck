import test from "node:test";
import assert from "node:assert/strict";
import { render } from "./relnotes.mjs";

test("groups features and fixes, leaves out empty sections", () => {
  const notes = render([
    { subject: "feat(cli): accept a range argument", body: "" },
    { subject: "fix: handle an empty log", body: "" },
    { subject: "Merge branch 'main'", body: "" },
  ]);
  assert.equal(notes, [
    "## Features",
    "- (cli) accept a range argument",
    "",
    "## Fixes",
    "- handle an empty log",
    "",
    "## Other",
    "- Merge branch 'main'",
    "",
  ].join("\n"));
});

test("a BREAKING CHANGE line in the body puts the commit under Breaking changes", () => {
  const notes = render([
    { subject: "feat(api): new auth flow", body: "Rewrites login.\n\nBREAKING CHANGE: the v1 token endpoint is gone" },
    { subject: "fix: typo", body: "" },
  ]);
  assert.equal(notes, [
    "## Breaking changes",
    "- (api) the v1 token endpoint is gone",
    "",
    "## Fixes",
    "- typo",
    "",
  ].join("\n"));
});
