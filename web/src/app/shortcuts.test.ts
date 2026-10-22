// Tests for the app-level chords. The matcher is pure — the DOM
// wiring only classifies the target and hands the event through — so
// every acceptance rule about which keystroke claims which command is
// decided here.
import assert from "node:assert/strict";
import test from "node:test";

import { SHORTCUTS, shortcutCommand, shortcutTarget, type ShortcutKeyEvent } from "./shortcuts.ts";

const key = (over: Partial<ShortcutKeyEvent>): ShortcutKeyEvent => ({
  key: "",
  code: "",
  altKey: false,
  ctrlKey: false,
  metaKey: false,
  shiftKey: false,
  repeat: false,
  defaultPrevented: false,
  ...over,
});

test("every printed glyph resolves to a live command", () => {
  // A printed shortcut is a promise: each table entry must have a
  // keystroke this matcher claims, or the glyph would be dead type.
  const chords: Record<keyof typeof SHORTCUTS, ShortcutKeyEvent> = {
    toggleRail: key({ key: "b", code: "KeyB", metaKey: true }),
    toggleInfoBar: key({ key: "/", code: "Slash", metaKey: true }),
    toggleYolo: key({ key: "¥", code: "KeyY", altKey: true, metaKey: true }),
    renameSelection: key({ key: "r", code: "KeyR", metaKey: true }),
  };
  for (const command of Object.keys(SHORTCUTS) as (keyof typeof SHORTCUTS)[]) {
    assert.equal(shortcutCommand(chords[command], "chrome"), command);
  }
});

test("the four chords fire with Cmd held on any surface", () => {
  for (const target of ["chrome", "terminal"] as const) {
    assert.equal(shortcutCommand(key({ key: "b", metaKey: true }), target), "toggleRail");
    assert.equal(shortcutCommand(key({ key: "/", metaKey: true }), target), "toggleInfoBar");
    assert.equal(shortcutCommand(key({ key: "r", metaKey: true }), target), "renameSelection");
    assert.equal(
      shortcutCommand(key({ key: "y", altKey: true, metaKey: true }), target),
      "toggleYolo",
    );
  }
});

test("Option+Y fires on a US layout, where the label is ¥", () => {
  assert.equal(
    shortcutCommand(key({ key: "¥", code: "KeyY", altKey: true, metaKey: true }), "chrome"),
    "toggleYolo",
    "the physical code decides alongside the label",
  );
  assert.equal(
    shortcutCommand(key({ key: "¥", code: "IntlYen", altKey: true, metaKey: true }), "chrome"),
    null,
    "a ¥ from a different physical key is not the binding",
  );
});

test("Ctrl chords are claimed by the chrome, not by the terminal", () => {
  // Into the terminal a Ctrl chord is a CLI byte — Ctrl+R is the
  // shell's history search, Ctrl+B a tmux prefix — so only the
  // ⌘-flavoured chord is claimed there.
  const ctrlR = key({ key: "r", ctrlKey: true });
  assert.equal(shortcutCommand(ctrlR, "chrome"), "renameSelection");
  assert.equal(shortcutCommand(ctrlR, "terminal"), null);
  const metaR = key({ key: "r", metaKey: true });
  assert.equal(shortcutCommand(metaR, "terminal"), "renameSelection");
});

test("shifted chords stay unclaimed", () => {
  // ⇧⌘Y is Bitwarden's, and an extension hotkey never reaches the page,
  // so the collision is undetectable: the binding must be ⌥⌘Y only.
  assert.equal(
    shortcutCommand(key({ key: "Y", metaKey: true, shiftKey: true }), "chrome"),
    null,
  );
  for (const k of ["b", "r", "/"]) {
    assert.equal(
      shortcutCommand(key({ key: k, metaKey: true, shiftKey: true }), "chrome"),
      null,
      `⇧⌘${k} is not ours`,
    );
  }
});

test("modifierless and half-modified chords are not ours", () => {
  for (const k of ["b", "r", "/", "y"]) {
    assert.equal(shortcutCommand(key({ key: k }), "chrome"), null, `plain ${k}`);
  }
  assert.equal(shortcutCommand(key({ key: "y", altKey: true }), "chrome"), null, "⌥Y alone");
  assert.equal(shortcutCommand(key({ key: "y", metaKey: true }), "chrome"), null, "⌘Y is browser History");
  assert.equal(shortcutCommand(key({ key: "b", ctrlKey: true, altKey: true }), "chrome"), null, "⌃⌥B");
});

test("Slash resolves by label and by physical code", () => {
  assert.equal(shortcutCommand(key({ key: "/", code: "Slash", metaKey: true }), "chrome"), "toggleInfoBar");
  assert.equal(shortcutCommand(key({ key: "7", code: "Slash", metaKey: true }), "chrome"), "toggleInfoBar",
    "a layout where the slash chord reports a label the US layout puts elsewhere");
});

test("text entry keeps its keys, and claimed events do not re-fire", () => {
  const renameField = key({ key: "r", metaKey: true });
  assert.equal(shortcutCommand(renameField, "text-entry"), null,
    "the inline rename field owns its keystrokes");
  assert.equal(shortcutCommand(key({ ...renameField, repeat: true }), "chrome"), null,
    "a held key must not strobe the toggles");
  assert.equal(shortcutCommand(key({ ...renameField, defaultPrevented: true }), "chrome"), null,
    "an already-claimed event is left alone");
});

test("targets classify: xterm's textarea is the terminal, fields are text entry", () => {
  const el = (tagName: string, opts: { classes?: string[]; contentEditable?: boolean } = {}) => ({
    tagName,
    classList: { contains: (c: string): boolean => (opts.classes ?? []).includes(c) },
    isContentEditable: opts.contentEditable ?? false,
  });
  assert.equal(shortcutTarget(el("TEXTAREA", { classes: ["xterm-helper-textarea"] })), "terminal");
  assert.equal(shortcutTarget(el("INPUT")), "text-entry");
  assert.equal(shortcutTarget(el("TEXTAREA")), "text-entry");
  assert.equal(shortcutTarget(el("SELECT")), "text-entry");
  assert.equal(shortcutTarget(el("DIV", { contentEditable: true })), "text-entry");
  assert.equal(shortcutTarget(el("BUTTON")), "chrome");
  assert.equal(shortcutTarget(null), "chrome");
});
