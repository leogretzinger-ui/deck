// The info bar: the app's
// ONLY explanation channel — a 26px bottom strip that shows the
// hint (any [data-hint]) of the control under the pointer, or of one
// focused from the keyboard, and stays empty when nothing is hovered, so
// the layout never jumps. Hover text is --text-secondary. Focus and hover are EACH
// ONE delegated listener on the app root, not per-control listeners. The
// "Show info bar" preference persists locally; its switch lands later with
// the ⋯ menu. Storage may be blocked (private mode, a quota, a disabled
// origin), so both the read and the write are guarded: an unreadable
// preference keeps the channel on, and a blocked write applies for this
// page only — the in-memory choice is authoritative once read, so a failed
// write never resurrects the bar.

const STORAGE_KEY = "noisedeck-show-info-bar";

// showInfoBar reads the persisted preference. The info bar is the only
// explanation channel, so anything but an explicit "off" keeps it on;
// a storage read that throws keeps it on as well.
export function showInfoBar(): boolean {
  try {
    return localStorage.getItem(STORAGE_KEY) !== "off";
  } catch {
    return true;
  }
}

// setShowInfoBar persists the choice and returns the applied value, so a
// future toggle can render exactly what took effect. A blocked write
// must not abort the toggle; the preference simply fails to persist.
export function setShowInfoBar(value: boolean): boolean {
  try {
    localStorage.setItem(STORAGE_KEY, value ? "on" : "off");
  } catch {
    // Blocked storage: the choice applies for this page only.
  }
  return value;
}

export type NewsSource = "chats" | "assets" | "notice";

// How long an announced outcome stays on the bar.
const ANNOUNCE_MS = 8000;

// NewsSlot holds the bar's one resting sentence for each source that has
// something to say. The newer item speaks, so an agent's chat starting
// after files landed takes the line and a fresh file takes it back; when
// the speaking source clears, the other's sentence returns. An item's age
// is when it became news: a sentence rewritten without a new time (a chat
// whose wait ticks up, a file count whose age line moves) keeps its place.
export class NewsSlot {
  private items = new Map<NewsSource, { sentence: string; at: number; order: number }>();
  private order = 0;

  set(source: NewsSource, sentence: string | null, at?: number): void {
    if (sentence === null || sentence === "") {
      this.items.delete(source);
      return;
    }
    const held = this.items.get(source);
    if (held !== undefined && at === undefined) {
      held.sentence = sentence;
      return;
    }
    this.items.set(source, { sentence, at: at ?? Date.now(), order: ++this.order });
  }

  get current(): string | null {
    let best: { sentence: string; at: number; order: number } | null = null;
    for (const item of this.items.values()) {
      if (best === null || item.at > best.at || (item.at === best.at && item.order > best.order)) best = item;
    }
    return best?.sentence ?? null;
  }
}

export class InfoBar {
  private host: HTMLElement;
  private status: HTMLElement;
  private root: HTMLElement;
  // The two hint sources keep their own state so focus and a pointer hover
  // cannot fight: a focused control wins while the platform calls its focus
  // visible, and the hovered control's hint speaks for the pointer. A
  // clicked BUTTON is not visible-focused, so it stays quiet — the pointer
  // is already saying where it is, and a clicked row whose hint outlived
  // the click would name it over every control the pointer visits next. A
  // clicked text field is, and speaks: typing is what it is for.
  //
  // Each source holds the ELEMENT, never the text or the modality it had
  // at the time. Both change under a control that neither moves nor
  // refocuses: a row rewrites its own hint when a move lands it on a
  // boundary, and a click-focused control becomes keyboard-focused the
  // moment the user reaches for the keyboard. Reading the DOM every
  // time the bar resolves what to say is what keeps the strip honest.
  private pointerOwner: Element | null = null;
  private focusOwner: Element | null = null;
  // The focused element itself, not the element its hint came from: a hint
  // may sit on a wrapper (a label around a checkbox), and it is the inner
  // control the platform reports as focus-visible.
  private focusedElement: Element | null = null;
  // news is the resting sentence: what the active
  // session's agent is doing in a chat, or how many files agents wrote
  // since the Assets panel was last open. It is the one thing the bar may
  // say unprompted. Hover and focus hints win while they last.
  private news: string | null = null;
  private slot = new NewsSlot();
  private hint: string | null = null;
  private announceTimer: ReturnType<typeof setTimeout> | null = null;
  private disposed = false;
  // The shown preference is read ONCE through the guarded accessor, so a
  // later blocked storage read cannot override a user's choice; only
  // setShown changes it from here on.
  private shown = showInfoBar();

  constructor(root: HTMLElement) {
    this.root = root;
    const host = root.querySelector<HTMLElement>("[data-region=\"info-bar\"]");
    if (host === null) throw new Error("missing info-bar region");
    this.host = host;
    const status = this.host.querySelector<HTMLElement>("[data-info-status]");
    if (status === null) throw new Error("missing info-bar status slot");
    this.status = status;
    this.applyShown();
    // Delegation, not per-control listeners: one pointermove and one
    // focusin/focusout on the app root resolve the nearest [data-hint],
    // so a re-render that rebuilds any single control never drops the
    // wiring. Focus is the keyboard path: the info bar is the only
    // explanation channel, so a control reached by Tab explains itself
    // exactly as one reached by hover does.
    root.addEventListener("pointermove", this.onPointerMove);
    root.addEventListener("pointerleave", this.onPointerLeave);
    root.addEventListener("focusin", this.onFocusIn);
    root.addEventListener("focusout", this.onFocusOut);
    // The platform reports focus visibility but announces no change to it,
    // and a key press is what turns a clicked control into a keyboard-
    // focused one. Without this the strip would carry the pointer's hint
    // until the pointer next moved, which for a user who has just
    // reached for the keyboard may be never.
    root.addEventListener("keydown", this.onKeyDown);
    this.render();
  }

  // setShown is the ⋯-menu switch's hook: it applies the choice to
  // the strip immediately and persists it best-effort. The in-memory state
  // is authoritative, so a blocked write cannot resurrect the bar: the
  // choice holds for this page even when it fails to persist.
  setShown(value: boolean): void {
    this.shown = value;
    setShowInfoBar(value);
    this.applyShown();
  }

  // isShown exposes the in-memory shown state for the ⋯ menu's switch. The
  // field is authoritative once read; localStorage is only a best-effort
  // mirror, so a blocked write must not make the switch disagree with the
  // strip it controls.
  get isShown(): boolean {
    return this.shown;
  }

  // refresh repaints the strip. It is a no-op now that the resting copy is
  // gone, but callers still invoke it on session changes; keeping the hook
  // avoids coupling the removal to every caller. The shown preference was
  // read once at construction and only setShown changes it, so a refresh
  // never re-reads storage and cannot resurrect a hidden bar.
  refresh(): void {
    if (this.disposed) return;
    this.applyShown();
  }

  // setNews replaces one source's resting sentence; null clears it. The
  // slot arbitrates between sources (NewsSlot), and with none speaking the
  // bar renders nothing at rest.
  setNews(source: NewsSource, sentence: string | null, at?: number): void {
    this.slot.set(source, sentence, at);
    const next = this.slot.current;
    if (next === this.news) return;
    this.news = next;
    this.setHint(this.speakingHint());
  }

  // announce speaks one sentence at rest for a few seconds: the outcome of
  // something Deck did for you without asking.
  announce(sentence: string): void {
    if (this.disposed) return;
    if (this.announceTimer !== null) clearTimeout(this.announceTimer);
    this.setNews("notice", sentence, Date.now());
    this.announceTimer = setTimeout(() => {
      this.announceTimer = null;
      this.setNews("notice", null);
    }, ANNOUNCE_MS);
  }

  dispose(): void {
    if (this.disposed) return;
    this.disposed = true;
    if (this.announceTimer !== null) clearTimeout(this.announceTimer);
    this.root.removeEventListener("pointermove", this.onPointerMove);
    this.root.removeEventListener("pointerleave", this.onPointerLeave);
    this.root.removeEventListener("focusin", this.onFocusIn);
    this.root.removeEventListener("focusout", this.onFocusOut);
    this.root.removeEventListener("keydown", this.onKeyDown);
  }

  private applyShown(): void {
    this.host.dataset.shown = this.shown ? "true" : "false";
  }

  private onPointerMove = (event: PointerEvent): void => {
    this.pointerOwner = hintElement(event.target);
    this.setHint(this.speakingHint());
  };

  private onPointerLeave = (): void => {
    this.pointerOwner = null;
    this.setHint(this.speakingHint());
  };

  private onFocusIn = (event: FocusEvent): void => {
    this.focusOwner = hintElement(event.target);
    this.focusedElement = event.target instanceof Element ? event.target : null;
    this.setHint(this.speakingHint());
  };

  private onFocusOut = (): void => {
    this.focusOwner = null;
    this.focusedElement = null;
    this.setHint(this.speakingHint());
  };

  private onKeyDown = (): void => {
    this.setHint(this.speakingHint());
  };

  // speakingHint is the one precedence, in one place: keyboard focus, then
  // the pointer, then the resting news sentence.
  private speakingHint(): string | null {
    return this.keyboardHint() ?? this.pointerHint() ?? this.news;
  }

  private pointerHint(): string | null {
    return hintOf(liveElement(this.pointerOwner));
  }

  // keyboardHint is the focused control's hint while the platform calls
  // that focus visible — which :focus-visible answers for the FOCUSED
  // element, so a hint inherited from a wrapper still speaks.
  private keyboardHint(): string | null {
    if (this.focusedElement === null) return null;
    return isFocusVisible(this.focusedElement) ? hintOf(this.focusOwner) : null;
  }

  private setHint(hint: string | null): void {
    if (hint === this.hint) return;
    this.hint = hint;
    this.render();
  }

  private render(): void {
    if (this.disposed) return;
    const hint = this.hint;
    this.status.textContent = hint ?? "";
    this.status.dataset.mode = hint !== null && hint === this.news && this.keyboardHint() === null && this.pointerHint() === null ? "news" : "hint";
  }
}

// hintElement resolves a pointer or focus event target to the nearest
// element carrying a hint, or null when nothing on the way up has one.
function hintElement(target: EventTarget | null): Element | null {
  return target instanceof Element ? target.closest("[data-hint]") : null;
}

// liveElement drops an element the document no longer holds. A re-render
// replaces the row under the pointer without any pointer event, and a
// detached node still answers getAttribute, so the strip would go on
// explaining a control that is no longer on the page — and would hold the
// strip against the news sentence underneath it.
//
// A detached owner is ignored at the next RECOMPUTATION, and removing a node
// schedules none: an unchanged news sentence returns before resolving, so the
// old text can outlast the row under a still pointer.
//
// The focus source needs no such check: a detached element cannot match
// :focus-visible.
function liveElement(element: Element | null): Element | null {
  return element !== null && element.isConnected ? element : null;
}

// hintOf reads an element's hint; an empty hint counts as none.
function hintOf(element: Element | null): string | null {
  if (element === null) return null;
  const value = element.getAttribute("data-hint");
  return value !== null && value !== "" ? value : null;
}

// isFocusVisible asks the platform whether the focus ring belongs on this
// control: :focus-visible matches a button reached by Tab and not one
// reached by a click, and matches a text field either way, since typing is
// what it is for. A browser that cannot evaluate the selector throws, and
// the fallback is to speak: the hint then holds the strip until blur, and
// a bar that over-explains beats a Tab that explains nothing.
function isFocusVisible(element: Element): boolean {
  try {
    return element.matches(":focus-visible");
  } catch {
    return true;
  }
}
