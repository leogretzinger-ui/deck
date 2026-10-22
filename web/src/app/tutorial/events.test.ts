import { test } from "node:test";
import assert from "node:assert/strict";
import { FactBook, eventKey, sequenceDone, stepControl } from "./events.ts";

const call = (name: string, arg: string | null = null) => ({ name, arg });
const context = { lesson: "lesson-1", step: 4, restored: false };

test("event keys carry their argument", () => {
  assert.equal(eventKey(call("input_sent")), "input_sent");
  assert.equal(eventKey(call("input_sent", "project=relnotes")), "input_sent(project=relnotes)");
});

test("a noted fact is answered by its own key only", () => {
  const book = new FactBook();
  book.note({ lesson: "lesson-1", step: 4 }, call("input_sent"), call("input_sent", "project=relnotes"));
  assert.notEqual(book.value(call("input_sent"), context), 0);
  assert.notEqual(book.value(call("input_sent", "project=relnotes"), context), 0);
  assert.equal(book.value(call("input_sent", "project=ccparse"), context), 0);
});

test("a then b needs b noted after a", () => {
  const book = new FactBook();
  const events = [call("input_sent"), call("file_opened", "modified")];
  book.note(null, call("file_opened", "modified"));
  book.note({ lesson: "lesson-1", step: 4 }, call("input_sent"));
  assert.equal(sequenceDone(events, book, context), false);
  book.note(null, call("file_opened", "modified"));
  assert.equal(sequenceDone(events, book, context), true);
});

test("current facts are asked, not remembered", () => {
  const book = new FactBook();
  let holds = true;
  book.current = () => holds;
  assert.equal(sequenceDone([call("agent_picked")], book, context), true);
  holds = false;
  assert.equal(sequenceDone([call("agent_picked")], book, context), false);
});

test("which control moves a step on", () => {
  assert.equal(stepControl(undefined), "continue");
  assert.equal(stepControl([call("continue")]), "continue");
  assert.equal(stepControl([call("check_exited")]), "continue");
  assert.equal(stepControl([call("agent_picked")]), "skip");
  assert.equal(stepControl([call("lane_session_attached")]), "skip", "a lane session attaching is observed");
  assert.equal(stepControl([call("review_seen")]), "skip", "a review arriving is observed");
  assert.equal(stepControl([call("input_sent"), call("session_busy")]), "continue", "an agent working is not observable");
});

test("a prompt noted for one step does not answer the same event on another", () => {
  const book = new FactBook();
  book.note({ lesson: "lesson-1", step: 4 }, call("input_sent"));
  assert.equal(book.value(call("input_sent"), { lesson: "lesson-2", step: 4, restored: false }), 0);
  assert.equal(book.value(call("input_sent"), { lesson: "lesson-1", step: 5, restored: false }), 0);
});

test("a prompt typed before the session attached does not count, and one for another project does not either", () => {
  const book = new FactBook();
  const here = { lesson: "lesson-2", step: 1, restored: false };
  let attached = false;
  book.current = (event) => event.name === "session_attached" && attached;
  const events = [call("session_attached", "project=ccparse"), call("input_sent")];
  book.note({ lesson: "lesson-2", step: 1 }, call("input_sent"), call("input_sent", "project=relnotes"));
  attached = true;
  assert.equal(sequenceDone(events, book, here), false, "the prompt came first, and it went to relnotes");
  book.note({ lesson: "lesson-2", step: 1 }, call("input_sent"), call("input_sent", "project=relnotes"));
  assert.equal(sequenceDone(events, book, here), false, "after attaching, but still into relnotes");
  book.note({ lesson: "lesson-2", step: 1 }, call("input_sent"), call("input_sent", "project=ccparse"));
  assert.equal(sequenceDone(events, book, here), true);
});
