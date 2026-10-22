import assert from "node:assert/strict";
import test from "node:test";
import type { GitIdentityProfile, Project } from "../../../api/projects.ts";
import { agentStartsNote, projectGitIdentitySetupHtml } from "./project-git-identity.ts";

const profile = (id: string): GitIdentityProfile => ({
  id, name: `Author ${id}`, email: `${id}@example.invalid`, revision: 1, created_at: "t", updated_at: "t",
});
const project: Project = { id: "p1", name: "legacy", canonical_path: "/workspace/legacy", last_used_at: "t", git_identity_setup_revision: 0 };
const screen = (retryProvider: string | null) => projectGitIdentitySetupHtml({
  project, retryProvider, profiles: [profile("a"), profile("b")], selected: profile("a"), busy: false, error: null,
});

test("the project screen is titled Git identity with a sentence-case label and no notice box", () => {
  const html = screen(null);
  assert.ok(html.includes('aria-label="Git identity"'));
  assert.ok(html.includes(">Git identity</h2>"));
  assert.ok(!/choose git identity/i.test(html));
  assert.ok(!html.includes("bg-wash-selected"), "no selection wash used as a notice");
  assert.match(html, /<span class="text-body-sm font-medium text-text-secondary">Git identity<\/span>/);
  assert.ok(!html.includes("font-label"), "the label is not set in small caps");
  assert.ok(!html.includes("data-project-identity-waiting"), "nothing waits when you opened it yourself");
  for (const button of html.match(/<button[^>]*>/g) ?? []) assert.match(button, /data-hint="/, button);
});

test("a waiting session start is one plain sentence", () => {
  const html = screen("codex");
  assert.ok(html.includes(">Codex starts as soon as you apply this.</p>"));
  assert.ok(!/retry/i.test(html));
  assert.equal(agentStartsNote("shell", "save"), "Terminal starts as soon as you save this.");
});
