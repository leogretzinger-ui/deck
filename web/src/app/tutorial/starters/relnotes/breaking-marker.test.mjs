import test from "node:test";
import assert from "node:assert/strict";
import { render } from "./relnotes.mjs";

test("a ! after the type puts the commit under Breaking changes", () => {
  const notes = render([{ subject: "feat(api)!: drop v1", body: "" }]);
  assert.equal(notes, "## Breaking changes\n- (api) drop v1\n");
});
