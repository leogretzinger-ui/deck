import { lucideIcon } from "../ui/icons.ts";
import * as hints from "../lib/hints.ts";

export type TerminalSoftKey =
  | "escape"
  | "tab"
  | "arrow-up"
  | "arrow-down"
  | "arrow-left"
  | "arrow-right"
  | "backspace"
  | "clear-screen";

type KeyboardInput = Pick<
  KeyboardEvent,
  "type" | "key" | "altKey" | "metaKey" | "shiftKey" | "isComposing"
>;

const arrowFinal: Record<
  Exclude<TerminalSoftKey, "escape" | "tab" | "backspace" | "clear-screen">,
  string
> = {
  "arrow-up": "A",
  "arrow-down": "B",
  "arrow-right": "C",
  "arrow-left": "D",
};

export function terminalSoftKeyBytes(
  key: TerminalSoftKey,
  applicationCursorKeys: boolean,
  ctrl: boolean,
): string {
  if (key === "escape") return "\x1b";
  if (key === "tab") return "\t";
  if (key === "backspace") return "\x7f";
  // Ctrl-L: the shell's clear-screen chord, exactly what a hardware
  // keyboard sends for the pad's broom key.
  if (key === "clear-screen") return "\x0c";
  const final = arrowFinal[key];
  if (ctrl) return `\x1b[1;5${final}`;
  return `${applicationCursorKeys ? "\x1bO" : "\x1b["}${final}`;
}

export function ctrlChordByte(key: string): string | null {
  if (key.length !== 1) return null;
  const code = key.charCodeAt(0);
  if (code >= 65 && code <= 90) return String.fromCharCode(code - 64);
  if (code >= 97 && code <= 122) return String.fromCharCode(code - 96);
  return null;
}

export class TerminalSoftKeyInput {
  private ctrlArmed = false;
  private listeners = new Set<(armed: boolean) => void>();
  private send: (data: string) => void;
  private applicationCursorKeys: () => boolean;

  constructor(
    send: (data: string) => void,
    applicationCursorKeys: () => boolean,
  ) {
    this.send = send;
    this.applicationCursorKeys = applicationCursorKeys;
  }

  activate(key: TerminalSoftKey): void {
    this.send(terminalSoftKeyBytes(key, this.applicationCursorKeys(), this.ctrlArmed));
    this.setCtrlArmed(false);
  }

  toggleCtrl(): boolean {
    this.setCtrlArmed(!this.ctrlArmed);
    return this.ctrlArmed;
  }

  cancelCtrl(): void {
    this.setCtrlArmed(false);
  }

  consumeKeyboardEvent(event: KeyboardInput): boolean {
    if (!this.ctrlArmed || event.type !== "keydown") return false;
    if (event.isComposing) {
      this.setCtrlArmed(false);
      return false;
    }
    if (["Alt", "Control", "Meta", "Shift"].includes(event.key)) return false;
    const byte = ctrlChordByte(event.key);
    if (byte !== null && !event.altKey && !event.metaKey && !event.shiftKey) this.send(byte);
    this.setCtrlArmed(false);
    return true;
  }

  onCtrlChange(listener: (armed: boolean) => void): { dispose(): void } {
    this.listeners.add(listener);
    listener(this.ctrlArmed);
    return { dispose: () => this.listeners.delete(listener) };
  }

  dispose(): void {
    this.setCtrlArmed(false);
    this.listeners.clear();
  }

  private setCtrlArmed(armed: boolean): void {
    if (armed === this.ctrlArmed) return;
    this.ctrlArmed = armed;
    for (const listener of this.listeners) listener(armed);
  }
}

export interface SoftKeyTerminal {
  activateSoftKey(key: TerminalSoftKey): void;
  toggleCtrlChord(): boolean;
  onCtrlChordChange(fn: (armed: boolean) => void): { dispose(): void };
  focus(): void;
  sendInput(data: string): void;
}

type Press = {
  button: HTMLButtonElement;
  startX: number;
  startY: number;
  cancelled: boolean;
  fired: boolean;
  delay: number | null;
  interval: number | null;
};

const repeatable = new Set<TerminalSoftKey>([
  "arrow-up",
  "arrow-down",
  "arrow-left",
  "arrow-right",
]);

// The mobile key strip: one horizontally scrollable 44px row. The four
// arrow keys collapse into a single 3x2 grid key that pops a 4-way pad
// above the strip. Ctrl is a live modifier, not a character key; the
// remaining keys send their literal byte when Ctrl is armed.
// inertSoftKeyTerminal is the strip's receiver when there is no emulator to
// receive: the strip stays drawn while a session is away, and the anatomy
// has to be the same one a live session mounts or a screen that reaches
// recovery in the app and a capture of it depict different chrome. Every key
// is disabled beside it — this stub is what the strip is bound to, not what
// makes it inert.
export function inertSoftKeyTerminal(): SoftKeyTerminal {
  return {
    activateSoftKey: () => {},
    toggleCtrlChord: () => false,
    onCtrlChordChange: () => ({ dispose: () => {} }),
    focus: () => {},
    sendInput: () => {},
  };
}

// setSoftKeysInert is the whole "present, not accepting input" treatment:
// the attribute the CSS reads AND the disabled state on every button. Setting
// only the attribute leaves keys that look inert and still fire, which is the
// one thing a strip drawn beside a session that cannot receive a keystroke
// must not do.
export function setSoftKeysInert(root: HTMLElement, inert: boolean): void {
  root.toggleAttribute("data-soft-keys-inert", inert);
  root.querySelectorAll<HTMLButtonElement>("button").forEach((button) => {
    button.disabled = inert;
  });
}

export class MobileTerminalSoftKeys {
  private presses = new Map<number, Press>();
  private ctrlSubscription: { dispose(): void };
  private root: HTMLElement;
  private terminal: SoftKeyTerminal;
  private ctrlArmed = false;
  private arrowPadOpen = false;
  private arrowPadContainer: HTMLElement | null = null;
  private onKeyDown: (event: KeyboardEvent) => void;

  constructor(root: HTMLElement, terminal: SoftKeyTerminal) {
    this.root = root;
    this.terminal = terminal;
    this.onKeyDown = (event) => {
      if (event.key === "Escape" && this.arrowPadOpen) {
        event.stopPropagation();
        this.closeArrowPad();
      }
    };
    document.addEventListener("keydown", this.onKeyDown, { capture: true });
    root.innerHTML = `
      <button type="button" data-terminal-soft-key="escape" aria-label="Escape" data-hint="${hints.SEND_ESCAPE_KEY}" class="h-11 min-w-11 flex-none"><span data-soft-key-visual>esc</span></button>
      <button type="button" data-terminal-soft-key="tab" aria-label="Tab" data-hint="${hints.SEND_TAB_KEY}" class="h-11 min-w-11 flex-none"><span data-soft-key-visual>tab</span></button>
      <button type="button" data-terminal-ctrl aria-label="Ctrl modifier" data-hint="${hints.TOGGLE_CTRL_MODIFIER}" aria-pressed="false" class="h-11 min-w-11 flex-none"><span data-soft-key-visual>ctrl</span></button>
      <button type="button" data-terminal-arrow-pad aria-label="Arrow pad" data-hint="${hints.OPEN_ARROW_PAD}" aria-expanded="false" class="h-11 min-w-11 flex-none">
        <span data-soft-key-visual><span data-terminal-arrow-grid aria-hidden="true" class="pointer-events-none grid grid-cols-3 gap-px">
          <span class="col-start-2">↑</span>
          <span class="col-start-1">←</span>
          <span class="col-start-2">↓</span>
          <span class="col-start-3">→</span>
        </span></span>
      </button>
      <button type="button" data-terminal-byte="/" aria-label="Forward slash" data-hint="${hints.SEND_FORWARD_SLASH}" class="h-11 min-w-11 flex-none"><span data-soft-key-visual>/</span></button>
      <button type="button" data-terminal-byte="|" aria-label="Pipe" data-hint="${hints.SEND_PIPE}" class="h-11 min-w-11 flex-none"><span data-soft-key-visual>|</span></button>
      <button type="button" data-terminal-byte="~" aria-label="Tilde" data-hint="${hints.SEND_TILDE}" class="h-11 min-w-11 flex-none"><span data-soft-key-visual>~</span></button>
      <span aria-hidden="true"></span>
      <span class="sr-only" data-terminal-ctrl-status aria-live="polite"></span>
    `;
    root.querySelectorAll<HTMLButtonElement>("[data-terminal-soft-key]").forEach((button) => {
      this.bindButton(button, button.dataset.terminalSoftKey as TerminalSoftKey);
    });
    const ctrl = root.querySelector<HTMLButtonElement>("[data-terminal-ctrl]")!;
    this.bindButton(ctrl, null);
    root.querySelectorAll<HTMLButtonElement>("[data-terminal-byte]").forEach((button) => {
      this.bindLiteralButton(button, button.dataset.terminalByte!);
    });
    const arrowPad = root.querySelector<HTMLButtonElement>("[data-terminal-arrow-pad]")!;
    this.bindArrowPadButton(arrowPad);
    const status = root.querySelector<HTMLElement>("[data-terminal-ctrl-status]")!;
    this.ctrlSubscription = terminal.onCtrlChordChange((armed) => {
      this.ctrlArmed = armed;
      ctrl.setAttribute("aria-pressed", String(armed));
      status.textContent = armed ? "Control modifier armed" : "";
    });
  }

  setInert(inert: boolean): void {
    if (inert) {
      for (const pointerID of [...this.presses.keys()]) this.cancelPress(pointerID);
      this.closeArrowPad();
    }
    setSoftKeysInert(this.root, inert);
  }

  dispose(): void {
    for (const pointerID of [...this.presses.keys()]) this.cancelPress(pointerID);
    this.ctrlSubscription.dispose();
    document.removeEventListener("keydown", this.onKeyDown, { capture: true });
    this.closeArrowPad();
    this.root.replaceChildren();
  }

  private bindButton(button: HTMLButtonElement, key: TerminalSoftKey | null): void {
    const activate = () => {
      if (key === null) this.terminal.toggleCtrlChord();
      else this.terminal.activateSoftKey(key);
      this.terminal.focus();
    };
    button.addEventListener("pointerdown", (event) => {
      if (event.button !== 0 || this.presses.has(event.pointerId)) return;
      event.preventDefault();
      button.setPointerCapture(event.pointerId);
      const press: Press = {
        button,
        startX: event.clientX,
        startY: event.clientY,
        cancelled: false,
        fired: false,
        delay: null,
        interval: null,
      };
      this.presses.set(event.pointerId, press);
      if (key !== null && repeatable.has(key) && !this.ctrlArmed) {
        press.delay = window.setTimeout(() => {
          if (press.cancelled) return;
          activate();
          press.fired = true;
          press.interval = window.setInterval(activate, 80);
        }, 400);
      }
    });
    button.addEventListener("pointermove", (event) => {
      const press = this.presses.get(event.pointerId);
      if (press === undefined) return;
      if (Math.hypot(event.clientX - press.startX, event.clientY - press.startY) > 10) {
        press.cancelled = true;
        this.clearTimers(press);
      }
    });
    button.addEventListener("pointerup", (event) => {
      const press = this.presses.get(event.pointerId);
      if (press === undefined) return;
      event.preventDefault();
      if (!press.cancelled && !press.fired) activate();
      this.cancelPress(event.pointerId);
    });
    button.addEventListener("pointercancel", (event) => this.cancelPress(event.pointerId));
    button.addEventListener("lostpointercapture", (event) => this.cancelPress(event.pointerId));
    button.addEventListener("click", (event) => {
      event.preventDefault();
      if (event.detail === 0) activate();
    });
  }

  private bindLiteralButton(button: HTMLButtonElement, byte: string): void {
    this.bindButton(button, null);
    // Re-purpose the null-key activate path: remove the Ctrl-only listener
    // added by bindButton and install one that sends the literal byte.
    const clone = button.cloneNode(true) as HTMLButtonElement;
    button.replaceWith(clone);
    this.bindLiteralDirect(clone, byte);
  }

  private bindLiteralDirect(button: HTMLButtonElement, byte: string): void {
    const send = () => {
      const chord = this.ctrlArmed ? ctrlChordByte(byte) : null;
      this.terminal.sendInput(chord ?? byte);
      if (this.ctrlArmed) this.terminal.toggleCtrlChord();
      this.terminal.focus();
    };
    const startRepeat = (press: Press) => {
      press.delay = window.setTimeout(() => {
        if (press.cancelled) return;
        send();
        press.fired = true;
        press.interval = window.setInterval(send, 80);
      }, 400);
    };
    button.addEventListener("pointerdown", (event) => {
      if (event.button !== 0 || this.presses.has(event.pointerId)) return;
      event.preventDefault();
      button.setPointerCapture(event.pointerId);
      const press: Press = {
        button,
        startX: event.clientX,
        startY: event.clientY,
        cancelled: false,
        fired: false,
        delay: null,
        interval: null,
      };
      this.presses.set(event.pointerId, press);
      startRepeat(press);
    });
    button.addEventListener("pointermove", (event) => {
      const press = this.presses.get(event.pointerId);
      if (press === undefined) return;
      if (Math.hypot(event.clientX - press.startX, event.clientY - press.startY) > 10) {
        press.cancelled = true;
        this.clearTimers(press);
      }
    });
    button.addEventListener("pointerup", (event) => {
      const press = this.presses.get(event.pointerId);
      if (press === undefined) return;
      event.preventDefault();
      if (!press.cancelled && !press.fired) send();
      this.cancelPress(event.pointerId);
    });
    button.addEventListener("pointercancel", (event) => this.cancelPress(event.pointerId));
    button.addEventListener("lostpointercapture", (event) => this.cancelPress(event.pointerId));
    button.addEventListener("click", (event) => {
      event.preventDefault();
      if (event.detail === 0) send();
    });
  }

  private bindArrowPadButton(button: HTMLButtonElement): void {
    button.addEventListener("click", (event) => {
      event.preventDefault();
      this.toggleArrowPad();
    });
    button.addEventListener("pointerdown", (event) => {
      if (event.button !== 0) return;
      event.preventDefault();
    });
  }

  private toggleArrowPad(): void {
    this.arrowPadOpen ? this.closeArrowPad() : this.openPopover();
  }

  // Public so the preview fixture can render the popped state for a
  // deterministic screenshot; the live app leaves it closed until tapped.
  openArrowPad(): void {
    this.openPopover();
  }

  private openPopover(): void {
    if (this.arrowPadOpen) return;
    this.arrowPadOpen = true;
    const button = this.root.querySelector<HTMLButtonElement>("[data-terminal-arrow-pad]")!;
    button.setAttribute("aria-expanded", "true");
    const pad = document.createElement("div");
    pad.dataset.terminalArrowPadPopover = "";
    // Every pad glyph renders through the generated lucide module: no
    // path data lives here.
    const chevronUp = lucideIcon("chevron-up", 22);
    const chevronDown = lucideIcon("chevron-down", 22);
    const chevronLeft = lucideIcon("chevron-left", 22);
    const chevronRight = lucideIcon("chevron-right", 22);
    const returnIcon = lucideIcon("corner-down-left", 20);
    const backspaceIcon = lucideIcon("delete", 20);
    const broomIcon = lucideIcon("broom", 20);
    pad.innerHTML = `
      <button type="button" data-terminal-soft-key="backspace" aria-label="Backspace" data-hint="${hints.SEND_BACKSPACE_KEY}">${backspaceIcon}</button>
      <button type="button" data-terminal-soft-key="arrow-up" aria-label="Arrow up" data-hint="${hints.SEND_ARROW_UP}">${chevronUp}</button>
      <button type="button" data-terminal-soft-key="clear-screen" aria-label="Clear screen" data-hint="${hints.SEND_CLEAR_SCREEN}">${broomIcon}</button>
      <button type="button" data-terminal-soft-key="arrow-left" aria-label="Arrow left" data-hint="${hints.SEND_ARROW_LEFT}">${chevronLeft}</button>
      <button type="button" data-terminal-byte="\r" aria-label="Return" data-hint="${hints.SEND_RETURN_KEY}">${returnIcon}</button>
      <button type="button" data-terminal-soft-key="arrow-right" aria-label="Arrow right" data-hint="${hints.SEND_ARROW_RIGHT}">${chevronRight}</button>
      <span aria-hidden="true"></span>
      <button type="button" data-terminal-soft-key="arrow-down" aria-label="Arrow down" data-hint="${hints.SEND_ARROW_DOWN}">${chevronDown}</button>
      <span aria-hidden="true"></span>
    `;
    pad.querySelectorAll<HTMLButtonElement>("[data-terminal-soft-key]").forEach((b) => {
      this.bindButton(b, b.dataset.terminalSoftKey as TerminalSoftKey);
    });
    pad.querySelectorAll<HTMLButtonElement>("[data-terminal-byte]").forEach((b) => {
      this.bindLiteralButton(b, b.dataset.terminalByte!);
    });
    this.arrowPadContainer = document.createElement("div");
    this.arrowPadContainer.style.position = "absolute";
    this.arrowPadContainer.style.right = "0";
    this.arrowPadContainer.style.left = "0";
    // The popped pad rises from the top of the dock so it never floats
    // in the middle of the terminal content; the fixture adds bottom
    // padding to the scrollback when the pad is open.
    this.arrowPadContainer.style.bottom = "100%";
    this.arrowPadContainer.appendChild(pad);
    this.root.parentElement?.appendChild(this.arrowPadContainer);
  }

  private closeArrowPad(): void {
    if (!this.arrowPadOpen) return;
    this.arrowPadOpen = false;
    const button = this.root.querySelector<HTMLButtonElement>("[data-terminal-arrow-pad]");
    button?.setAttribute("aria-expanded", "false");
    this.arrowPadContainer?.remove();
    this.arrowPadContainer = null;
  }

  private cancelPress(pointerID: number): void {
    const press = this.presses.get(pointerID);
    if (press === undefined) return;
    this.clearTimers(press);
    this.presses.delete(pointerID);
    if (press.button.hasPointerCapture(pointerID)) press.button.releasePointerCapture(pointerID);
  }

  private clearTimers(press: Press): void {
    if (press.delay !== null) window.clearTimeout(press.delay);
    if (press.interval !== null) window.clearInterval(press.interval);
    press.delay = null;
    press.interval = null;
  }
}
