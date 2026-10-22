import assert from "node:assert/strict";
import test from "node:test";
import type { GitIdentityProfile } from "../../api/projects.ts";
import { identityNeed, prefillIdentity, silentGitIdentity } from "./identity-need.ts";

const profile = (id: string): GitIdentityProfile => ({
  id, name: `Author ${id}`, email: `${id}@example.invalid`, revision: 1, created_at: "t", updated_at: "t",
});

test("no identity on file asks you to create one", () => {
  assert.deepEqual(identityNeed({ profiles: [], default_profile_id: null, revision: 0 }), { kind: "create" });
  assert.deepEqual(identityNeed(null), { kind: "create" });
});

test("one identity applies without asking, default or not", () => {
  const only = profile("a");
  assert.deepEqual(identityNeed({ profiles: [only], default_profile_id: "a", revision: 1 }), { kind: "apply", profile: only });
  assert.deepEqual(identityNeed({ profiles: [only], default_profile_id: null, revision: 1 }), { kind: "apply", profile: only });
});

test("several identities apply the default, and ask only without one", () => {
  const a = profile("a"), b = profile("b");
  assert.deepEqual(identityNeed({ profiles: [a, b], default_profile_id: "b", revision: 2 }), { kind: "apply", profile: b });
  assert.deepEqual(identityNeed({ profiles: [a, b], default_profile_id: null, revision: 2 }), { kind: "choose" });
  assert.equal(silentGitIdentity({ profiles: [a, b], default_profile_id: "gone", revision: 2 }), null);
});

test("GitHub fills only the empty fields", () => {
  const github = { name: "Octo Cat", email: "583231+octocat@users.noreply.github.com" };
  assert.deepEqual(prefillIdentity({ name: "", email: "" }, github), github);
  assert.deepEqual(prefillIdentity({ name: "Typed", email: " " }, github), { name: "Typed", email: github.email });
  assert.deepEqual(prefillIdentity({ name: "", email: "" }, null), { name: "", email: "" }, "nothing to fill without GitHub");
  assert.deepEqual(prefillIdentity({ name: "", email: "" }, github, { name: true, email: false }), { name: "", email: github.email },
    "a field you have focused stays empty");
});
