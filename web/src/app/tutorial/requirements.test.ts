import { test } from "node:test";
import assert from "node:assert/strict";
import { LESSON_REQUIREMENTS } from "./lessons.ts";
import {
  chatsFact,
  githubFact,
  isLoopbackHostname,
  requirementNotes,
  unknownRequirementFacts,
  type RequirementFacts,
} from "./requirements.ts";

const facts = (over: Partial<RequirementFacts> = {}): RequirementFacts => ({ ...unknownRequirementFacts, hostname: "deck.example.com", ...over });

test("agent-chat and mempalace need the Chats data to be available", () => {
  for (const requirement of ["agent-chat", "mempalace"]) {
    const [note, ...rest] = requirementNotes([requirement], facts({ chats: false }));
    assert.equal(rest.length, 0);
    assert.deepEqual({ text: note?.text, unmet: note?.unmet }, { text: "Needs agent chat (MemPalace)", unmet: true });
    assert.deepEqual(requirementNotes([requirement], facts({ chats: true })), []);
    assert.deepEqual(requirementNotes([requirement], facts({ chats: null })), []);
  }
});

test("github, previews and a reachable install are unmet only when known to be missing", () => {
  assert.deepEqual(requirementNotes(["github"], facts({ github: false })), [{ text: "Needs a GitHub connection", unmet: true }]);
  assert.deepEqual(requirementNotes(["github"], facts({ github: true })), []);
  assert.deepEqual(requirementNotes(["github"], facts({ github: null })), []);
  assert.deepEqual(requirementNotes(["previews"], facts({ previews: false })), [{ text: "Needs project previews", unmet: true }]);
  assert.deepEqual(requirementNotes(["previews"], facts({ previews: true })), []);
  assert.deepEqual(requirementNotes(["previews"], facts({ previews: null })), []);
  assert.deepEqual(requirementNotes(["reachable-install"], facts({ hostname: "localhost" })), [{ text: "Needs Deck reachable from your phone", unmet: true }]);
  assert.deepEqual(requirementNotes(["reachable-install"], facts({ hostname: "deck.example.com" })), []);
});

test("two-sessions, claude and review-app are information and never dim a lesson", () => {
  const all = facts({ chats: false, github: false, previews: false });
  assert.deepEqual(requirementNotes(["two-sessions"], all), [{ text: "Uses two agent sessions", unmet: false }]);
  assert.deepEqual(requirementNotes(["claude"], all), [{ text: "Uses your Claude plan or API key", unmet: false }]);
  assert.deepEqual(requirementNotes(["review-app"], all), [{ text: "Needs the review app", unmet: false }]);
});

test("notes follow the lesson's order and agent-chat with mempalace says it once", () => {
  assert.deepEqual(
    requirementNotes(["agent-chat", "two-sessions", "mempalace"], facts({ chats: false })).map((note) => note.text),
    ["Needs agent chat (MemPalace)", "Uses two agent sessions"],
  );
});

test("every requirement the parser accepts has a defined note", () => {
  const unmet = facts({ chats: false, github: false, previews: false, hostname: "127.0.0.1" });
  for (const requirement of LESSON_REQUIREMENTS) {
    assert.equal(requirementNotes([requirement], unmet).length, 1, requirement);
  }
});

test("loopback hostnames", () => {
  for (const name of ["localhost", "LOCALHOST", "127.0.0.1", "127.1.2.3", "::1", "[::1]", "deck.localhost"]) assert.equal(isLoopbackHostname(name), true, name);
  for (const name of ["", "deck.example.com", "192.168.1.20", "127.0.0.1.example.com", "localhost.example.com", "10.0.0.5"]) assert.equal(isLoopbackHostname(name), false, name);
});

test("only an outcome that says no connection becomes false for GitHub", () => {
  assert.equal(githubFact("authenticated"), true);
  for (const outcome of ["no-account", "missing-cli", "auth-issue", "authentication-required"]) assert.equal(githubFact(outcome), false, outcome);
  for (const outcome of ["timeout", "malformed", "oversized", "run-error", "unknown", ""]) assert.equal(githubFact(outcome), null, outcome);
  assert.deepEqual(requirementNotes(["github"], facts({ github: githubFact("timeout") })), []);
});

test("agent chat is false only when the status says it is off, and a working read overrides it", () => {
  assert.equal(chatsFact("ready", false), true);
  for (const state of ["unavailable", "loading", "off", "no-agent", undefined]) {
    assert.equal(chatsFact(state, null), null, `${state}: no answer yet is unknown`);
    assert.equal(chatsFact(state, true), true, `${state}: available`);
    assert.equal(chatsFact(state, false), false, `${state}: off`);
  }
});

test("the agent chat note says what is missing and how to turn it on only when it is off", () => {
  assert.deepEqual(requirementNotes(["agent-chat"], facts({ chats: null })), []);
  assert.deepEqual(requirementNotes(["agent-chat"], facts({ chats: true })), []);
  const [note, ...rest] = requirementNotes(["agent-chat", "mempalace"], facts({ chats: false }));
  assert.equal(rest.length, 0, "one note per distinct text");
  assert.equal(note?.text, "Needs agent chat (MemPalace)");
  assert.equal(note?.unmet, true);
  assert.equal(note?.enable?.sentence, "Docker installs add the agent chat overlay to turn it on.");
  assert.match(note?.enable?.url ?? "", /#agent-chat-and-shared-memory$/);
});
