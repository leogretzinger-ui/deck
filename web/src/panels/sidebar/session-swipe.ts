// The phone session row's swipe gesture ("swipe
// a row to close"). The row's surface slides left to reveal one Close
// action behind it; there are no per-row menus on the sheet. This module
// is the DOM-free core: the shell feeds it pointer deltas and applies the
// state it returns.

// A drag must be decisively horizontal before the row claims it — the
// sheet scrolls vertically, and a diagonal ambiguity belongs to the
// scroller. Once claimed, the gesture ends in open (revealed) or closed.
const CLAIM_RATIO = 1.5;
const CLAIM_DISTANCE = 12;
// The surface parks at 100px; a release past half that commits.
export const SWIPE_REVEAL_PX = 100;
const COMMIT_DISTANCE = SWIPE_REVEAL_PX / 2;

export type SwipePhase = "idle" | "pending" | "dragging";

export interface SwipeState {
  phase: SwipePhase;
  // The leftward travel to paint mid-drag, clamped to [0, SWIPE_REVEAL_PX].
  travel: number;
}

export class SessionSwipeGesture {
  private phase: SwipePhase = "idle";
  private startX = 0;
  private startY = 0;
  private fromOpen = false;

  begin(x: number, y: number, fromOpen: boolean): void {
    this.phase = "pending";
    this.startX = x;
    this.startY = y;
    this.fromOpen = fromOpen;
  }

  // move returns the paint state. A pending gesture claims the drag only
  // when the travel is decisively horizontal; a vertical drift cancels so
  // the sheet's own scroll keeps the gesture.
  move(x: number, y: number): SwipeState {
    if (this.phase === "idle") return { phase: "idle", travel: 0 };
    const dx = x - this.startX;
    const dy = y - this.startY;
    if (this.phase === "pending") {
      if (Math.abs(dx) < CLAIM_DISTANCE && Math.abs(dy) < CLAIM_DISTANCE) {
        return { phase: "pending", travel: 0 };
      }
      if (Math.abs(dx) < Math.abs(dy) * CLAIM_RATIO) {
        this.phase = "idle";
        return { phase: "idle", travel: 0 };
      }
      this.phase = "dragging";
    }
    return { phase: "dragging", travel: this.travelFor(dx) };
  }

  // end commits the gesture: true means the row rests open. An unclaimed
  // tap answers with where it began, so a plain tap never toggles here.
  end(x: number): boolean {
    const dx = x - this.startX;
    const wasDragging = this.phase === "dragging";
    this.phase = "idle";
    if (!wasDragging) return this.fromOpen;
    return this.travelFor(dx) >= COMMIT_DISTANCE;
  }

  cancel(): void {
    this.phase = "idle";
  }

  // Leftward pointer travel (negative dx) increases the reveal; a gesture
  // that began on an open row starts from the parked position, so a right
  // swipe walks it home.
  private travelFor(dx: number): number {
    const base = this.fromOpen ? SWIPE_REVEAL_PX : 0;
    return Math.min(SWIPE_REVEAL_PX, Math.max(0, base - dx));
  }
}
