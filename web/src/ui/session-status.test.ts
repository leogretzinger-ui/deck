import assert from "node:assert/strict";
import test from "node:test";

import type { ApiClient } from "../api/contracts.ts";
import type { SessionDetail } from "../api/sessions.ts";
import {
  isVerifiedRunningSession,
  sessionNeedsAttention,
  sessionStaysListed,
  SessionStatusModel,
} from "./session-status.ts";

function session(partial: Partial<SessionDetail> = {}): SessionDetail {
  return {
    id: "s1",
    project_id: "p1",
    provider: "claude",
    display_name: "",
    status: "running",
    generation: 1,
    created_at: "t",
    updated_at: "t",
    recovery_capability: "none",
    recovery_state: "unavailable",
    presentation_state: "running",
    ...partial,
  };
}

function fakeApi(overrides: Partial<ApiClient> = {}): ApiClient {
  return {
    getSession: () => Promise.resolve(session()),
    ...overrides,
  } as ApiClient;
}

test("refresh loads the session record", async () => {
  const model = new SessionStatusModel(fakeApi(), "s1");
  await model.refresh();
  assert.equal(model.session?.presentation_state, "running");
});

test("only an exact backend running presentation is mountable", () => {
  assert.equal(isVerifiedRunningSession(session()), true);
  for (const presentation_state of [
    "recovering",
    "attachment_unavailable",
    "ended",
    "inactive",
    "resume_ended",
  ] as const) {
    assert.equal(isVerifiedRunningSession(session({ presentation_state })), false);
  }
});

// A refused resume must stay in the list with the attention mark: the row is
// the user's only way to the card that offers Resume and Start fresh
// session, and hiding it as ended is how such a session vanished on them.
test("a session whose resume ended stays listed and marked, ended and inactive do not", () => {
  for (const presentation_state of ["recovering", "attachment_unavailable", "resume_ended"] as const) {
    assert.equal(sessionStaysListed(session({ presentation_state })), true, presentation_state);
    assert.equal(sessionNeedsAttention(session({ presentation_state })), true, presentation_state);
  }
  assert.equal(sessionStaysListed(session()), true);
  assert.equal(sessionNeedsAttention(session()), false);
  for (const presentation_state of ["ended", "inactive"] as const) {
    assert.equal(sessionStaysListed(session({ presentation_state })), false, presentation_state);
  }
});

