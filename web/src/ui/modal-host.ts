// One modal per modals region. The host owns a single scrim and a stack of
// screens inside it, and shows only the top screen: opening a dialog while
// another is open pushes a screen with a Back control instead of raising a
// second overlay. Owners render into their screen's element and decide what
// dismissal means; the host only asks.
import { DIALOG_ICON_BUTTON } from "./controls.ts";
import { lucideIcon } from "./icons.ts";
import * as hints from "../lib/hints.ts";

export interface ModalScreen {
  readonly element: HTMLElement;
  readonly open: boolean;
  close(): void;
}

export interface ModalScreenOptions {
  // Back, Escape with focus outside the dialog, and (unless disabled) a
  // click on the scrim ask the owner to leave. The owner closes the screen
  // when it agrees.
  dismiss: () => void;
  dismissOnScrim?: boolean;
}

interface Entry {
  element: HTMLElement;
  options: ModalScreenOptions;
  returnFocus: HTMLElement | null;
}

// Hosts with at least one screen open, across every modals region.
const openHosts = new Set<ModalHost>();

// Bounds a whole-modal close against an owner that never settles.
const MAX_DISMISS_ASKS = 64;

class ModalHost {
  private readonly region: HTMLElement;
  private scrim: HTMLElement | null = null;
  private observer: MutationObserver | null = null;
  private readonly stack: Entry[] = [];
  // Set while a click on a pushed screen's head close control is being
  // handled: the screens beneath close too, whether the owner closed its
  // screen or only stepped back inside it.
  private closingAll: Entry | null = null;

  constructor(region: HTMLElement) {
    this.region = region;
  }

  get depth(): number {
    return this.stack.length;
  }

  open(options: ModalScreenOptions): ModalScreen {
    const scrim = this.ensureScrim();
    const active = document.activeElement as HTMLElement | null;
    const element = document.createElement("div");
    element.dataset.modalScreen = "";
    const below = this.stack.at(-1);
    if (below !== undefined) below.element.hidden = true;
    const entry: Entry = { element, options, returnFocus: active ?? null };
    this.stack.push(entry);
    openHosts.add(this);
    scrim.append(element);
    this.decorate();
    const stack = this.stack;
    return {
      element,
      get open() { return stack.includes(entry); },
      close: () => this.close(entry),
    };
  }

  // reattach puts the scrim back in its region after a fallback region
  // re-rendered its own markup over it.
  reattach(): void {
    if (this.scrim !== null && this.scrim.parentNode !== this.region) this.region.append(this.scrim);
  }

  private ensureScrim(): HTMLElement {
    if (this.scrim !== null) {
      this.reattach();
      return this.scrim;
    }
    const scrim = document.createElement("div");
    scrim.className = "scrim";
    scrim.dataset.modalScrim = "";
    scrim.addEventListener("click", (event) => {
      if (event.target !== scrim) return;
      const top = this.stack.at(-1);
      if (top !== undefined && top.options.dismissOnScrim !== false) top.options.dismiss();
    });
    scrim.addEventListener("click", (event) => {
      const top = this.stack.at(-1);
      const target = event.target as Element | null;
      if (top === undefined || this.stack.length < 2 || typeof target?.closest !== "function") return;
      const close = target.closest("[data-dialog-close]");
      if (close === null || !top.element.contains(close)) return;
      this.closingAll = top;
      setTimeout(() => {
        if (this.closingAll !== top) return;
        this.closingAll = null;
        this.dismissAll();
      });
    }, true);
    document.addEventListener("keydown", this.onKeyDown);
    if (typeof MutationObserver !== "undefined") {
      this.observer = new MutationObserver(() => this.decorate());
      this.observer.observe(scrim, { childList: true, subtree: true });
    }
    this.region.append(scrim);
    this.scrim = scrim;
    return scrim;
  }

  // Escape inside a dialog is the dialog's own; this catches the press
  // that lands while focus sits outside every screen.
  private readonly onKeyDown = (event: KeyboardEvent): void => {
    if (event.key !== "Escape" || event.defaultPrevented) return;
    const top = this.stack.at(-1);
    if (top === undefined) return;
    const active = document.activeElement;
    if (active !== null && this.scrim?.contains(active) === true) return;
    event.preventDefault();
    top.options.dismiss();
  };

  private close(entry: Entry): void {
    const index = this.stack.indexOf(entry);
    if (index === -1) return;
    const wasTop = index === this.stack.length - 1;
    const active = document.activeElement;
    const focusWasInside = active !== null && entry.element.contains(active);
    this.stack.splice(index, 1);
    entry.element.remove();
    const top = this.stack.at(-1);
    if (top === undefined) {
      this.removeScrim();
    } else {
      top.element.hidden = false;
      this.decorate();
    }
    if (wasTop && (focusWasInside || document.activeElement === null || document.activeElement === document.body)) {
      const back = entry.returnFocus;
      if (back?.isConnected === true && (top === undefined || top.element.contains(back))) {
        back.focus({ preventScroll: true });
      } else if (top !== undefined) {
        top.element.querySelector<HTMLElement>('[role="dialog"], [role="alertdialog"]')?.focus({ preventScroll: true });
      }
    }
    if (this.closingAll === entry) {
      this.closingAll = null;
      this.dismissAll();
    }
  }

  // dismissAll asks the top screen to leave until none is left. An owner
  // may answer by stepping back inside its own screen, so it is asked
  // again; an answer that changes nothing is a refusal and ends the close.
  private dismissAll(): void {
    for (let asks = 0; asks < MAX_DISMISS_ASKS; asks++) {
      const top = this.stack.at(-1);
      if (top === undefined) return;
      this.decorate();
      const before = top.element.innerHTML;
      top.options.dismiss();
      this.decorate();
      if (this.stack.at(-1) === top && top.element.innerHTML === before) return;
    }
  }

  private removeScrim(): void {
    openHosts.delete(this);
    this.observer?.disconnect();
    this.observer = null;
    document.removeEventListener("keydown", this.onKeyDown);
    this.scrim?.remove();
    this.scrim = null;
  }

  // decorate gives every pushed screen a Back control at the start of its
  // head, unless the screen draws its own, and takes it off a screen that
  // is no longer pushed.
  private decorate(): void {
    this.stack.forEach((entry, index) => {
      const head = entry.element.querySelector<HTMLElement>(".dialog-head");
      if (head === null) return;
      const injected = head.querySelector<HTMLElement>(":scope > [data-modal-back]");
      if (index === 0) {
        injected?.remove();
        return;
      }
      if (injected !== null || head.querySelector('[aria-label="Back"]') !== null) return;
      const back = document.createElement("button");
      back.type = "button";
      back.dataset.modalBack = "";
      back.dataset.hint = hints.GO_BACK;
      back.setAttribute("aria-label", "Back");
      back.className = DIALOG_ICON_BUTTON;
      back.innerHTML = lucideIcon("chevron-left", 16);
      back.addEventListener("click", (event) => {
        event.stopPropagation();
        if (this.stack.at(-1) === entry) entry.options.dismiss();
      });
      head.prepend(back);
    });
  }
}

const hosts = new WeakMap<HTMLElement, ModalHost>();

function hostFor(region: HTMLElement): ModalHost {
  let host = hosts.get(region);
  if (host === undefined) {
    host = new ModalHost(region);
    hosts.set(region, host);
  }
  return host;
}

// modalOpen reports whether any modal screen is open.
export function modalOpen(): boolean {
  return openHosts.size > 0;
}

// returnFocus hands focus back to a control behind the modal once the
// modal has closed. While any screen is still open the keyboard stays
// with that screen, so the call does nothing.
export function returnFocus(target: HTMLElement | null | undefined): void {
  if (modalOpen()) return;
  target?.focus({ preventScroll: true });
}

// openModalScreen shows a screen in the region's one modal, pushing it
// onto whatever screen is already open there.
export function openModalScreen(region: HTMLElement, options: ModalScreenOptions): ModalScreen {
  return hostFor(region).open(options);
}

// ModalSlot keeps one owner's screen in step with markup the owner
// re-renders: markup opens the screen if needed, empty markup closes it.
export class ModalSlot {
  private readonly region: () => HTMLElement;
  private readonly options: ModalScreenOptions;
  private host: ModalHost | null = null;
  private screen: ModalScreen | null = null;

  constructor(region: () => HTMLElement, options: ModalScreenOptions) {
    this.region = region;
    this.options = options;
  }

  get element(): HTMLElement | null {
    return this.screen?.open === true ? this.screen.element : null;
  }

  render(markup: string): HTMLElement | null {
    if (markup === "") {
      this.close();
      return null;
    }
    if (this.screen === null || !this.screen.open || this.host === null) {
      this.host = hostFor(this.region());
      this.screen = this.host.open(this.options);
    } else {
      this.host.reattach();
    }
    this.screen.element.innerHTML = markup;
    return this.screen.element;
  }

  close(): void {
    const screen = this.screen;
    this.screen = null;
    screen?.close();
  }
}
