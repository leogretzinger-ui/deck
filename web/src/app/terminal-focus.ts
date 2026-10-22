// Focus routing for the terminal attachment. LiveWorkspace asks for focus
// when a user selects or launches a session; the actual focus move
// waits until the attach handshake succeeds and then checks that it is
// still safe to steal focus. Keeping the guard here makes it testable
// without a browser.

// FocusObstacle names what currently prevents moving focus to the
// terminal. Composing means the user is typing in a form field:
// stealing focus would eat a keystroke and the edit, so the request is
// refused for good. Overlay means focus sits inside an open dialog,
// popover, or menu — which is often the launch picker's own closing
// frames, the residue of the very gesture that requested focus, so the
// request survives and the caller retries on a timer; an overlay the
// user opened afterwards keeps focus simply by outliving the retries.
export type FocusObstacle = "none" | "composing" | "overlay";

export function focusObstacle(
  activeElement: Element | null,
  documentBody: Element | null,
): FocusObstacle {
  if (activeElement === null || activeElement === documentBody) return "none";
  const tag = activeElement.tagName.toLowerCase();
  if (tag === "input" || tag === "textarea" || tag === "select") return "composing";
  const region = activeElement.closest("[data-region]");
  const name = region?.getAttribute("data-region");
  if (name === "modals" || name === "popovers") return "overlay";
  return "none";
}

export function terminalFocusAllowed(
  activeElement: Element | null,
  documentBody: Element | null,
): boolean {
  return focusObstacle(activeElement, documentBody) === "none";
}

// FocusOutcome reports what the caller should do with a focus request:
// granted moves focus now; refused cancels the gesture; retry keeps the
// request because an overlay may still be closing.
export type FocusOutcome = "granted" | "refused" | "retry";

// TerminalFocusRequest carries the one-shot focus request from a user
// session selection or launch to the attach handshake. Reconnects do not
// repeatedly yank focus: a granted or refused outcome clears the request.
export class TerminalFocusRequest {
  private requested = false;

  // cancel retires the request. The retry loop calls this when its
  // attempts run out: an overlay that outlived them is a user
  // gesture of its own, and a request left armed would let some later
  // attach steal focus with no fresh gesture behind it.
  cancel(): void {
    this.requested = false;
  }

  request(): void {
    this.requested = true;
  }

  consume(
    activeElement: Element | null,
    documentBody: Element | null,
    isMobile: boolean,
  ): FocusOutcome {
    if (!this.requested) return "refused";
    // Mobile keeps its soft-key behaviour: the keyboard appears only when
    // the user taps the terminal keys, not on every session launch.
    if (isMobile) {
      this.requested = false;
      return "refused";
    }
    const obstacle = focusObstacle(activeElement, documentBody);
    if (obstacle === "composing") {
      this.requested = false;
      return "refused";
    }
    if (obstacle === "overlay") return "retry";
    this.requested = false;
    return "granted";
  }
}
