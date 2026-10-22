// MobileTerminalCompose buffers phone text, pastes it, then presses Enter.
//
// The row is a real text field — the one exception to terminal-only
// input: text accumulates in the field — so dictation and voice-narration apps, which
// edit a field's value rather than emitting keystrokes, work — and nothing
// reaches the session until Send or Return flushes the buffer. Control
// keys stay with the key strip; the terminal itself remains the only
// surface where a keystroke leaves as it is typed.
interface ComposeTerminal {
  paste(text: string): void;
  sendInput(data: string): void;
}

export class MobileTerminalCompose {
  private readonly terminal: ComposeTerminal;
  private disposed = false;

  constructor(terminal: ComposeTerminal) {
    this.terminal = terminal;
  }

  // Use xterm's native paste handling so applications that enable bracketed
  // paste can distinguish draft line breaks from the subsequent submit key.
  // Both calls are synchronous; no timer or provider-specific key is needed.
  // An empty buffer still answers a TUI prompt that only wants Enter.
  flush(value: string): void {
    if (this.disposed) return;
    if (value !== "") this.terminal.paste(value);
    this.terminal.sendInput("\r");
  }

  dispose(): void {
    this.disposed = true;
  }
}

// isComposeSubmitIntent names the input intents that flush the buffer
// instead of editing it: the soft keyboard's Return. Every other intent —
// typing, dictation's composition and text insertions, deletions inside
// the draft — belongs to the field and is left alone.
export function isComposeSubmitIntent(inputType: string): boolean {
  return inputType === "insertLineBreak" || inputType === "insertParagraph";
}
