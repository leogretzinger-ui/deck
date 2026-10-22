// The info bar's DOM-free logic: the persisted "Show info bar" preference
// and the strip's empty resting state. Hover delegation and the color mode
// live in the browser, where the live smoke exercises them.
import assert from "node:assert/strict";
import test from "node:test";

import { InfoBar, NewsSlot, setShowInfoBar, showInfoBar } from "./info-bar.ts";

// Minimal localStorage stub, as the terminal preferences' tests use.
function withStorage(run: (store: Map<string, string>) => void): void {
  const store = new Map<string, string>();
  const original = Object.getOwnPropertyDescriptor(globalThis, "localStorage");
  Object.defineProperty(globalThis, "localStorage", {
    value: {
      getItem: (k: string) => store.get(k) ?? null,
      setItem: (k: string, v: string) => void store.set(k, v),
      removeItem: (k: string) => void store.delete(k),
    },
    configurable: true,
  });
  try {
    run(store);
  } finally {
    if (original === undefined) {
      delete (globalThis as { localStorage?: unknown }).localStorage;
    } else {
      Object.defineProperty(globalThis, "localStorage", original);
    }
  }
}

// There is no jsdom here, so the InfoBar's root/host/status are stubs that
// record the one attribute the CSS reads to decide whether the strip is
// laid out (data-shown) — mirroring how the docked-panels tests stub the
// data-open attribute.
function infoBarHarness(): {
  bar: InfoBar;
  status: { textContent: string; dataset: Record<string, string> };
  host: { dataset: Record<string, string> };
  listening: string[];
} {
  const status = {
    dataset: {} as Record<string, string>,
    textContent: "",
  };
  const host = {
    dataset: {} as Record<string, string>,
    querySelector: (selector: string) =>
      selector === "[data-info-status]" ? status : null,
  };
  // The delegated listeners are the wiring the tests below drive by hand,
  // so the stub records which ones were actually registered.
  const listening: string[] = [];
  const root = {
    querySelector: (selector: string) =>
      selector === '[data-region="info-bar"]' ? host : null,
    addEventListener: (type: string) => void listening.push(type),
    removeEventListener: (type: string) => void listening.splice(listening.indexOf(type), 1),
  };
  const bar = new InfoBar(root as unknown as HTMLElement);
  return { bar, status, host, listening };
}

test("the info bar defaults on and only an explicit off hides it", () => {
  withStorage((store) => {
    assert.equal(showInfoBar(), true);
    store.set("noisedeck-show-info-bar", "banana");
    assert.equal(showInfoBar(), true, "garbage must not hide the only explanation channel");
    setShowInfoBar(false);
    assert.equal(showInfoBar(), false);
    assert.equal(store.get("noisedeck-show-info-bar"), "off");
    setShowInfoBar(true);
    assert.equal(showInfoBar(), true);
    assert.equal(store.get("noisedeck-show-info-bar"), "on");
  });
});

test("blocked storage keeps the preference path from aborting", () => {
  const original = Object.getOwnPropertyDescriptor(globalThis, "localStorage");
  Object.defineProperty(globalThis, "localStorage", {
    value: {
      getItem: () => { throw new Error("storage unavailable"); },
      setItem: () => { throw new Error("storage unavailable"); },
      removeItem: () => { throw new Error("storage unavailable"); },
    },
    configurable: true,
  });
  try {
    assert.equal(showInfoBar(), true, "a blocked read keeps the only explanation channel on");
    assert.equal(setShowInfoBar(false), false, "a blocked write still applies for this page");
    assert.equal(showInfoBar(), true, "a blocked write does not fabricate persistence");
  } finally {
    if (original === undefined) {
      delete (globalThis as { localStorage?: unknown }).localStorage;
    } else {
      Object.defineProperty(globalThis, "localStorage", original);
    }
  }
});

test("setShown(false) hides the strip even when storage is blocked", () => {
  const original = Object.getOwnPropertyDescriptor(globalThis, "localStorage");
  Object.defineProperty(globalThis, "localStorage", {
    value: {
      getItem: () => { throw new Error("storage unavailable"); },
      setItem: () => { throw new Error("storage unavailable"); },
      removeItem: () => { throw new Error("storage unavailable"); },
    },
    configurable: true,
  });
  try {
    const { bar, host } = infoBarHarness();
    assert.equal(host.dataset.shown, "true", "a blocked read keeps the channel on");
    bar.setShown(false);
    assert.equal(host.dataset.shown, "false",
      "a blocked write must not resurrect the bar");
    bar.dispose();
  } finally {
    if (original === undefined) {
      delete (globalThis as { localStorage?: unknown }).localStorage;
    } else {
      Object.defineProperty(globalThis, "localStorage", original);
    }
  }
});

test("setShown persists, and a later refresh cannot resurrect the strip", () => {
  withStorage((store) => {
    const { bar, host } = infoBarHarness();
    assert.equal(host.dataset.shown, "true");
    bar.setShown(false);
    assert.equal(host.dataset.shown, "false");
    assert.equal(store.get("noisedeck-show-info-bar"), "off");
    bar.refresh();
    assert.equal(host.dataset.shown, "false",
      "a refresh must not re-read storage and override the in-memory choice");
    bar.dispose();
  });
});

test("the strip is empty at rest and stays in hint mode", () => {
  const { status } = infoBarHarness();
  assert.equal(status.textContent, "", "nothing hovered means no text");
  assert.equal(status.dataset.mode, "hint", "the only mode is hint");
});

test("the news tier speaks at rest and yields to hover and focus", () => {
  withStorage(() => {
    const { bar, status } = infoBarHarness();
    assert.equal(status.textContent, "", "nothing at rest before any news");
    bar.setNews("chats", "claude-61c3 is in a chat with codex-9f2a");
    assert.equal(status.textContent, "claude-61c3 is in a chat with codex-9f2a");
    assert.equal(status.dataset.mode, "news");
    bar.setNews("chats", "");
    assert.equal(status.textContent, "", "an empty sentence clears the tier");
    bar.setNews("chats", "codex-9f2a is in a chat with kimi-2b77 · a2a#3ff6c0 written, no echo seen");
    assert.equal(status.dataset.mode, "news");
    bar.setNews("chats", null);
    assert.equal(status.textContent, "");
    assert.equal(status.dataset.mode, "hint");
  });
});

test("an announced outcome speaks at rest, then clears itself", (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  withStorage(() => {
    const { bar, status } = infoBarHarness();
    bar.announce("Commits as Ada Lovelace <ada@example.invalid>");
    assert.equal(status.textContent, "Commits as Ada Lovelace <ada@example.invalid>");
    assert.equal(status.dataset.mode, "news");
    t.mock.timers.tick(7_999);
    assert.equal(status.textContent, "Commits as Ada Lovelace <ada@example.invalid>");
    t.mock.timers.tick(1);
    assert.equal(status.textContent, "", "the announcement leaves on its own");
    bar.dispose();
  });
});

// The bar resolves a hint through `instanceof Element`, closest() and
// matches(":focus-visible"), so a hinted control here is an Element for the
// duration of a test: `hint` is what it says, `focusVisible` is whether the
// platform would call its focus keyboard-driven.
function withControls(run: (
  control: (hint: string, focusVisible?: boolean) => unknown,
  controlInsideHintedWrapper: (hint: string) => unknown,
) => void): void {
  class FakeElement {
    hint: string;
    focusVisible: boolean;
    owner: FakeElement | null;
    // The bar drops a hint whose element the document no longer holds, so a
    // control here is on the page until a test detaches it.
    isConnected = true;
    constructor(hint: string, focusVisible: boolean, owner: FakeElement | null = null) {
      this.hint = hint;
      this.focusVisible = focusVisible;
      this.owner = owner;
    }
    // closest("[data-hint]") from a control inside a hinted wrapper
    // resolves to the wrapper, which is where the hint lives.
    closest(): FakeElement {
      return this.owner ?? this;
    }
    getAttribute(): string {
      return this.hint;
    }
    matches(selector: string): boolean {
      return selector === ":focus-visible" && this.focusVisible;
    }
  }
  const previous = (globalThis as { Element?: unknown }).Element;
  (globalThis as { Element?: unknown }).Element = FakeElement;
  try {
    run((hint, focusVisible = false) => new FakeElement(hint, focusVisible),
        (hint) => new FakeElement("", true, new FakeElement(hint, false)));
  } finally {
    (globalThis as { Element?: unknown }).Element = previous;
  }
}

function events(bar: InfoBar): {
  focusIn: (target: unknown) => void;
  focusOut: () => void;
  pointerMove: (target: unknown) => void;
  pointerLeave: () => void;
  keyDown: () => void;
} {
  const bound = bar as unknown as {
    onFocusIn: (event: unknown) => void;
    onFocusOut: () => void;
    onPointerMove: (event: unknown) => void;
    onPointerLeave: () => void;
    onKeyDown: () => void;
  };
  return {
    focusIn: (target) => bound.onFocusIn({ target }),
    focusOut: () => bound.onFocusOut(),
    pointerMove: (target) => bound.onPointerMove({ target }),
    pointerLeave: () => bound.onPointerLeave(),
    keyDown: () => bound.onKeyDown(),
  };
}

test("a keyboard-focused control's hint wins over the news sentence, and the news returns after blur", () => {
  withStorage(() => {
    const { bar, status } = infoBarHarness();
    bar.setNews("chats", "claude-61c3 is in a chat with codex-9f2a");
    const { focusIn, focusOut } = events(bar);
    withControls((control) => {
      focusIn(control("Close the chats", true));
      assert.equal(status.textContent, "Close the chats");
      assert.equal(status.dataset.mode, "hint");
      focusOut();
    });
    assert.equal(status.textContent, "claude-61c3 is in a chat with codex-9f2a");
    assert.equal(status.dataset.mode, "news");
  });
});

// Focus outlives a click, and a project row IS its own toggle button, so
// clicking one must not take the strip from the controls the pointer
// visits next.
test("a clicked control's hint does not follow the pointer onto other controls", () => {
  withStorage(() => {
    const { bar, status } = infoBarHarness();
    const { focusIn, pointerMove, pointerLeave } = events(bar);
    withControls((control) => {
      focusIn(control("Hide the sessions in deck"));
      pointerMove(control("Switch to claude in deck"));
      assert.equal(status.textContent, "Switch to claude in deck",
        "the pointer names the control it is over, not the one that was clicked");
      pointerLeave();
      assert.equal(status.textContent, "",
        "leaving every hinted control clears the strip rather than reverting to the clicked row");
    });
  });
});

test("a Tab-focused control keeps explaining itself while the pointer sits over nothing", () => {
  withStorage(() => {
    const { bar, status } = infoBarHarness();
    const { focusIn, pointerMove } = events(bar);
    withControls((control) => {
      focusIn(control("Hide the sessions in deck", true));
      pointerMove(null);
      assert.equal(status.textContent, "Hide the sessions in deck",
        "the keyboard path explains itself with the pointer elsewhere");
      pointerMove(control("Switch to claude in deck"));
      assert.equal(status.textContent, "Hide the sessions in deck",
        "and it still outranks a hover, so Tab is never overwritten by a stray pointermove");
    });
  });
});

// A browser that cannot evaluate :focus-visible must not silence the
// keyboard path, which is the only way a Tab user learns a control. The
// hint then holds the strip until blur.
test("a control that cannot answer :focus-visible still speaks when focused", () => {
  withStorage(() => {
    const { bar, status } = infoBarHarness();
    const { focusIn } = events(bar);
    class OldElement {
      isConnected = true;
      closest(): OldElement {
        return this;
      }
      getAttribute(): string {
        return "Hide the sessions in deck";
      }
      matches(): boolean {
        throw new Error("unknown pseudo-class");
      }
    }
    const previous = (globalThis as { Element?: unknown }).Element;
    (globalThis as { Element?: unknown }).Element = OldElement;
    try {
      focusIn(new OldElement());
      assert.equal(status.textContent, "Hide the sessions in deck");
    } finally {
      (globalThis as { Element?: unknown }).Element = previous;
    }
  });
});

// A hint may sit on a wrapper — a label around its checkbox — and it is the
// inner control the platform reports as focus-visible. Reading visibility
// off the hint's owner would silence every inherited hint on the keyboard
// path, which is the only way a Tab user learns those rows.
test("a control inside a hinted wrapper speaks its inherited hint when Tab reaches it", () => {
  withStorage(() => {
    const { bar, status } = infoBarHarness();
    const { focusIn } = events(bar);
    withControls((_control, inWrapper) => {
      focusIn(inWrapper("Show only tasks that are ready"));
      assert.equal(status.textContent, "Show only tasks that are ready");
    });
  });
});

// Visibility is the platform's live answer, not a fact captured at focus
// time: a control focused by click becomes focus-visible the moment the
// user reaches for the keyboard, and must speak from then on.
test("a clicked control starts speaking once the platform calls its focus visible", () => {
  withStorage(() => {
    const { bar, status } = infoBarHarness();
    const { focusIn, pointerMove, keyDown } = events(bar);
    withControls((control) => {
      const clicked = control("Hide the sessions in deck") as { focusVisible: boolean };
      focusIn(clicked);
      pointerMove(control("Switch to claude in deck"));
      assert.equal(status.textContent, "Switch to claude in deck");
      clicked.focusVisible = true;
      keyDown();
      assert.equal(status.textContent, "Hide the sessions in deck",
        "the key press that changed the modality is what re-resolves the strip");
    });
  });
});

// A row may rewrite its own hint while the pointer rests on it — the
// project move rows do, when a move lands one on a boundary and the verb
// it offered is gone. The strip reads the hint when it resolves one, so
// the re-announce that follows the rewrite says the new sentence.
test("a hint rewritten under a resting pointer is re-read, not remembered", () => {
  withStorage(() => {
    const { bar, status } = infoBarHarness();
    const { focusIn, pointerMove } = events(bar);
    withControls((control) => {
      const row = control("Swap places with the project below") as { hint: string };
      pointerMove(row);
      assert.equal(status.textContent, "Swap places with the project below");
      row.hint = "Already last in the sidebar";
      focusIn(row);
      assert.equal(status.textContent, "Already last in the sidebar",
        "the row re-announces its new hint without the pointer moving");
    });
  });
});

// Every source the bar resolves from needs an event that re-resolves it,
// and the tests above drive those handlers directly — so the delegation
// itself is asserted here, or removing a listener would leave them green.
test("the bar delegates every source it resolves from, and lets go on dispose", () => {
  withStorage(() => {
    const { bar, listening } = infoBarHarness();
    assert.deepEqual([...listening].sort(),
      ["focusin", "focusout", "keydown", "pointerleave", "pointermove"]);
    bar.dispose();
    assert.deepEqual(listening, [], "a disposed bar leaves no listener on the root");
  });
});

// A re-render replaces the row under the pointer without any pointer event.
// The bar holds the element, not its text, so a detached row would keep
// answering — and would hold the strip against the news sentence beneath it.
test("a hint whose control has left the page stops speaking", () => {
  withStorage(() => {
    const { bar, status } = infoBarHarness();
    const { pointerMove } = events(bar);
    withControls((control) => {
      const row = control("Switch to claude in deck") as { isConnected: boolean };
      pointerMove(row);
      assert.equal(status.textContent, "Switch to claude in deck");

      bar.setNews("chats", "claude-61c3 is in a chat with codex-9f2a");
      assert.equal(status.textContent, "Switch to claude in deck",
        "a control still on the page outranks the news");

      row.isConnected = false;
      bar.setNews("chats", "codex-9f2a is in a chat with kimi-2b77");
      assert.equal(status.textContent, "codex-9f2a is in a chat with kimi-2b77",
        "the replaced row no longer holds the strip");
      assert.equal(status.dataset.mode, "news");
    });
  });
});

test("the newer news item takes the slot, and clearing it hands the slot back", () => {
  const slot = new NewsSlot();
  assert.equal(slot.current, null, "an empty slot says nothing");
  slot.set("chats", "claude-61c3 is in a chat with codex-9f2a", 1_000);
  slot.set("assets", "3 new assets · newest 1m ago", 2_000);
  assert.equal(slot.current, "3 new assets · newest 1m ago", "files that landed after the chat began speak");
  slot.set("chats", "claude-61c3 is in a chat with kimi-2b77", 3_000);
  assert.equal(slot.current, "claude-61c3 is in a chat with kimi-2b77", "a later chat takes the line back");
  slot.set("chats", null);
  assert.equal(slot.current, "3 new assets · newest 1m ago", "clearing the chat falls back to the files");
  slot.set("assets", "");
  assert.equal(slot.current, null, "an empty sentence clears its source");
});

test("a sentence rewritten without a new time keeps its place", () => {
  const slot = new NewsSlot();
  slot.set("chats", "claude-61c3 is in a chat with codex-9f2a · waiting 1m", 1_000);
  slot.set("assets", "1 new asset · just now", 2_000);
  slot.set("chats", "claude-61c3 is in a chat with codex-9f2a · waiting 2m");
  assert.equal(slot.current, "1 new asset · just now", "a ticking wait is not news");
  slot.set("assets", "1 new asset · 1m ago");
  assert.equal(slot.current, "1 new asset · 1m ago", "the age moves in place");
  slot.set("assets", null);
  assert.equal(slot.current, "claude-61c3 is in a chat with codex-9f2a · waiting 2m",
    "the chat's rewritten sentence is what returns");
});

test("two items of the same age go to the one set last", () => {
  const slot = new NewsSlot();
  slot.set("assets", "2 new assets · newest just now", 5_000);
  slot.set("chats", "claude-61c3 is in a chat with codex-9f2a", 5_000);
  assert.equal(slot.current, "claude-61c3 is in a chat with codex-9f2a");
});

test("the bar speaks the arbitrated sentence in the news tier", () => {
  withStorage(() => {
    const { bar, status } = infoBarHarness();
    bar.setNews("chats", "claude-61c3 is in a chat with codex-9f2a", 1_000);
    bar.setNews("assets", "3 new assets · newest 1m ago", 2_000);
    assert.equal(status.textContent, "3 new assets · newest 1m ago");
    assert.equal(status.dataset.mode, "news");
    bar.setNews("assets", null);
    assert.equal(status.textContent, "claude-61c3 is in a chat with codex-9f2a");
    bar.setNews("chats", null);
    assert.equal(status.textContent, "");
    assert.equal(status.dataset.mode, "hint");
  });
});
