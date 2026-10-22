// The app-level keyboard chords: the four
// bindings the menus print, and nothing else. A printed shortcut is a
// promise, so the glyph a menu shows and the chord this module claims
// come from the same SHORTCUTS table — one cannot outlive the other.
// ⌘N, ⌘T and ⌘W are absent on purpose: the browser takes them before
// the page sees the event, so the glyph would advertise a key that does
// nothing. If a desktop build ever owns the browser's reserved chords,
// the table grows a target axis here — the one place that would need it.
export type ShortcutCommand = "toggleRail" | "toggleInfoBar" | "toggleYolo" | "renameSelection";

// The glyph column a menu row prints, as modifier glyphs plus the key:
// ⌥⌘Y is two modifiers and one key, so the cluster rules in
// menuShortcutHtml can apply per glyph.
export const SHORTCUTS: Record<ShortcutCommand, { modifiers: readonly string[]; key: string }> = {
  toggleRail: { modifiers: ["⌘"], key: "B" },
  toggleInfoBar: { modifiers: ["⌘"], key: "/" },
  toggleYolo: { modifiers: ["⌥", "⌘"], key: "Y" },
  renameSelection: { modifiers: ["⌘"], key: "R" },
};

// Any object carrying these fields matches — the DOM layer hands the
// KeyboardEvent itself; tests hand a plain object.
export interface ShortcutKeyEvent {
  key: string;
  code: string;
  altKey: boolean;
  ctrlKey: boolean;
  metaKey: boolean;
  shiftKey: boolean;
  repeat: boolean;
  defaultPrevented: boolean;
}

// Where the keystroke landed. The terminal's hidden textarea is not a
// text entry: it is the CLI's own input surface, so its chords are
// classified as the terminal's, not a field's.
export type ShortcutTarget = "terminal" | "text-entry" | "chrome";

// The element shape shortcutTarget reads. Structural on purpose: the DOM
// hands an Element, the tests hand a plain object, and neither needs the
// other's baggage.
export interface ShortcutTargetElement {
  tagName?: unknown;
  classList?: { contains(name: string): boolean };
  isContentEditable?: unknown;
}

export function shortcutTarget(target: ShortcutTargetElement | EventTarget | null): ShortcutTarget {
  const el = target as ShortcutTargetElement | null;
  if (el === null || typeof el.tagName !== "string") return "chrome";
  if (el.tagName === "TEXTAREA" && el.classList?.contains("xterm-helper-textarea") === true) {
    return "terminal";
  }
  if (el.tagName === "INPUT" || el.tagName === "TEXTAREA" || el.tagName === "SELECT" || el.isContentEditable === true) {
    return "text-entry";
  }
  return "chrome";
}

// shortcutCommand maps one keystroke to a command, or null when the page
// should leave it alone. The mod key is ⌘ or Ctrl (the same honest
// binding the search chip's label already makes), EXCEPT into the
// terminal: a Ctrl chord there is a CLI byte — Ctrl+R is the shell's
// history search, Ctrl+B a tmux prefix — while a ⌘-held key never
// becomes one, so meta chords are safe to claim everywhere.
export function shortcutCommand(event: ShortcutKeyEvent, target: ShortcutTarget): ShortcutCommand | null {
  if (event.defaultPrevented || event.repeat) return null;
  if (target === "text-entry") return null;
  const mod = event.metaKey || (event.ctrlKey && target !== "terminal");
  if (!mod || event.shiftKey) return null;
  if (event.altKey) {
    // ⌥⌘Y: Option+Y emits "¥" on a US layout, so the physical key code
    // decides alongside the label. ⇧⌘Y is rejected above with every
    // other shifted chord — it is Bitwarden's, and an extension hotkey
    // never reaches the page, so the collision cannot be detected here.
    return event.code === "KeyY" || event.key.toLowerCase() === "y" ? "toggleYolo" : null;
  }
  if (event.key === "/" || event.code === "Slash") return "toggleInfoBar";
  switch (event.key.toLowerCase()) {
    case "b":
      return "toggleRail";
    case "r":
      return "renameSelection";
    default:
      return null;
  }
}
