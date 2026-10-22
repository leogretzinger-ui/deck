import assert from "node:assert/strict";
import test from "node:test";

import { harnessLoginLabel } from "./harness-login.ts";

const attempt = (flow: string) => ({ id: flow, flow, expires: "2030-01-01T00:00:00Z" });

test("the login label names the agent the waiting attempts belong to", () => {
  assert.equal(harnessLoginLabel([attempt("pi-openai")]), "Finish Pi login");
  assert.equal(harnessLoginLabel([attempt("pi-openai"), attempt("pi-anthropic")]), "Finish Pi login");
  assert.equal(harnessLoginLabel([attempt("codex-browser"), attempt("pi-openai")]), "Finish agent login");
  assert.equal(harnessLoginLabel([attempt("unknown-flow")]), "Finish agent login");
  assert.equal(harnessLoginLabel([]), "Finish agent login");
});
