import assert from "node:assert/strict";
import test from "node:test";

import { MobileTerminalCompose, isComposeSubmitIntent } from "./mobile-compose.ts";

function composeRecorder() {
  const sent: Array<["paste" | "input", string]> = [];
  const compose = new MobileTerminalCompose({
    paste: (text) => sent.push(["paste", text]),
    sendInput: (data) => sent.push(["input", data]),
  });
  return { compose, sent };
}

test("a flush pastes the complete draft before a separate submit key", () => {
  const { compose, sent } = composeRecorder();
  const draft = "go test ./...\n第二行 café\n";
  compose.flush(draft);
  assert.deepEqual(sent, [["paste", draft], ["input", "\r"]]);
});

test("an empty flush still answers a bare-Enter prompt", () => {
  const { compose, sent } = composeRecorder();
  compose.flush("");
  assert.deepEqual(sent, [["input", "\r"]]);
});

test("a disposed row sends nothing", () => {
  const { compose, sent } = composeRecorder();
  compose.dispose();
  compose.flush("late");
  assert.deepEqual(sent, []);
});

// Only the soft keyboard's Return flushes; every editing intent — typing,
// dictation's compositions and insertions, in-draft deletions, paste —
// belongs to the field and must be left alone, or voice apps break.
test("only the Return intents flush the buffer", () => {
  assert.equal(isComposeSubmitIntent("insertLineBreak"), true);
  assert.equal(isComposeSubmitIntent("insertParagraph"), true);
  for (const editing of [
    "insertText",
    "insertCompositionText",
    "insertFromPaste",
    "insertReplacementText",
    "deleteContentBackward",
    "deleteContentForward",
    "deleteWordBackward",
  ]) {
    assert.equal(isComposeSubmitIntent(editing), false, `${editing} must edit the field`);
  }
});
