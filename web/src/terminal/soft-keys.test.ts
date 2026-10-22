import assert from "node:assert/strict";
import test from "node:test";

import {
  ctrlChordByte,
  TerminalSoftKeyInput,
  terminalSoftKeyBytes,
} from "./soft-keys.ts";

test("soft keys match normal and application cursor key bytes", () => {
  assert.equal(terminalSoftKeyBytes("escape", false, false), "\x1b");
  assert.equal(terminalSoftKeyBytes("tab", false, false), "\t");
  assert.equal(terminalSoftKeyBytes("arrow-up", false, false), "\x1b[A");
  assert.equal(terminalSoftKeyBytes("arrow-down", true, false), "\x1bOB");
  assert.equal(terminalSoftKeyBytes("arrow-left", false, true), "\x1b[1;5D");
  assert.equal(terminalSoftKeyBytes("arrow-right", true, true), "\x1b[1;5C");
});

test("Ctrl chord translation is limited to ASCII letters", () => {
  for (let offset = 0; offset < 26; offset += 1) {
    assert.equal(ctrlChordByte(String.fromCharCode(97 + offset)), String.fromCharCode(1 + offset));
    assert.equal(ctrlChordByte(String.fromCharCode(65 + offset)), String.fromCharCode(1 + offset));
  }
  assert.equal(ctrlChordByte("2"), null);
  assert.equal(ctrlChordByte("/"), null);
  assert.equal(ctrlChordByte("ß"), null);
  assert.equal(ctrlChordByte("Enter"), null);
});

test("Ctrl is a deliberate one-shot chord and sends nothing when armed", () => {
  const sent: string[] = [];
  const states: boolean[] = [];
  const input = new TerminalSoftKeyInput((data) => sent.push(data), () => false);
  input.onCtrlChange((armed) => states.push(armed));

  assert.equal(input.toggleCtrl(), true);
  assert.deepEqual(sent, []);
  assert.equal(input.consumeKeyboardEvent({
    type: "keydown",
    key: "c",
    altKey: false,
    metaKey: false,
    shiftKey: false,
    isComposing: false,
  }), true);
  assert.deepEqual(sent, ["\x03"]);
  assert.deepEqual(states, [false, true, false]);

  assert.equal(input.consumeKeyboardEvent({
    type: "keydown",
    key: "d",
    altKey: false,
    metaKey: false,
    shiftKey: false,
    isComposing: false,
  }), false);
  assert.deepEqual(sent, ["\x03"]);
});

test("composing input cancels an armed Ctrl chord without consuming input", () => {
  const sent: string[] = [];
  const input = new TerminalSoftKeyInput((data) => sent.push(data), () => true);
  input.toggleCtrl();
  assert.equal(input.consumeKeyboardEvent({
    type: "keydown",
    key: "Process",
    altKey: false,
    metaKey: false,
    shiftKey: false,
    isComposing: true,
  }), false);
  input.activate("arrow-up");
  assert.deepEqual(sent, ["\x1bOA"]);
});

test("unsupported and shifted Ctrl follow-ups are consumed without bytes", () => {
  const sent: string[] = [];
  const states: boolean[] = [];
  const input = new TerminalSoftKeyInput((data) => sent.push(data), () => false);
  input.onCtrlChange((armed) => states.push(armed));

  for (const event of [
    { key: "2", altKey: false, metaKey: false, shiftKey: false },
    { key: "C", altKey: false, metaKey: false, shiftKey: true },
    { key: "c", altKey: true, metaKey: false, shiftKey: false },
  ]) {
    input.toggleCtrl();
    assert.equal(input.consumeKeyboardEvent({
      type: "keydown",
      isComposing: false,
      ...event,
    }), true);
  }

  assert.deepEqual(sent, []);
  assert.deepEqual(states, [false, true, false, true, false, true, false]);
});

test("Ctrl disarms on cancellation and disposal", () => {
  const states: boolean[] = [];
  const input = new TerminalSoftKeyInput(() => {}, () => false);
  input.onCtrlChange((armed) => states.push(armed));
  input.toggleCtrl();
  input.cancelCtrl();
  input.toggleCtrl();
  input.dispose();
  assert.deepEqual(states, [false, true, false, true, false]);
});
