// Keyboard-aware sizing for the workspace frame.
//
// `dvh` accounts for browser chrome but NOT the software keyboard: on
// iOS the layout viewport keeps its full height when the keyboard opens,
// so the bottom of the app — the terminal's prompt, a focused field —
// ends up underneath it with no way to scroll it into view. The visual
// viewport is the only thing that reports the actually-visible region,
// so the frame follows it.
//
// Sizing the frame is preferable to scrolling the focused element into
// view: the terminal is the whole surface, so scrolling would push its
// top off-screen to reveal its bottom. Shrinking instead keeps every row
// visible and lets the terminal reflow — its ResizeObserver refits xterm
// when the host changes size, so the row count follows the keyboard.

// ViewportMetrics is the subset of VisualViewport this reads. Declared
// structurally so the geometry can be exercised without a browser.
export interface ViewportMetrics {
  height: number;
  offsetTop: number;
}

// frameStyle computes the frame's height and vertical offset from the
// visual viewport.
//
// offsetTop is non-zero when the page itself has been scrolled under the
// keyboard, which iOS does when a field near the bottom is focused. The
// frame is translated back by that amount so it stays pinned to the
// visible region instead of drifting upward out of it.
//
// A zero or absent height means the browser reported nothing usable; the
// caller is told to drop back to the stylesheet rather than apply a
// collapsed frame.
export function frameStyle(vv: ViewportMetrics | null): { height: string; transform: string } | null {
  if (vv === null || !Number.isFinite(vv.height) || vv.height <= 0) return null;
  const offset = Number.isFinite(vv.offsetTop) ? Math.max(0, vv.offsetTop) : 0;
  return {
    // floor, never round: rounding up can exceed the visible region by
    // half a pixel, and nothing sets overflow:hidden on html/body, so
    // that raises a scrollbar — which w-screen then turns into
    // horizontal overflow too. Undershooting by a fraction is invisible.
    height: `${Math.floor(vv.height)}px`,
    // Always emitted, even at zero: a non-none transform makes the frame
    // the containing block for its position:fixed descendants (the
    // mobile sidebar overlay, modals, popovers). Emitting "" at zero
    // would make that containment appear and disappear with the
    // keyboard, so those layers behave one way scrolled and another way
    // not. Constant is predictable.
    transform: `translateY(${Math.floor(offset)}px)`,
  };
}

// A page scrolled under the keyboard must scroll home once the keyboard
// is gone. iOS can leave the page displaced after a touch drag on the
// bottom chrome — the visual viewport reports full height (no keyboard)
// with a stuck positive offsetTop, and the translated frame then shows a
// permanent band of dead space at the bottom until something rehomes the
// scroll. Full height plus offset is that stuck state; with the keyboard
// up, height is short of the layout viewport and the offset is load-
// bearing, so this must stay false.
export function viewportNeedsRehome(
  vv: ViewportMetrics,
  layoutHeight: number,
): boolean {
  if (!Number.isFinite(vv.height) || !Number.isFinite(vv.offsetTop)) return false;
  return vv.height >= layoutHeight - 1 && vv.offsetTop > 0;
}

// The keyboard's exit outlives its events: iOS animates the dismissal
// and can fire its last visualViewport resize mid-animation — or, when
// the focused field blurs, none at all — leaving the frame pinned at
// keyboard height with a dead band where the keyboard was. Geometry is
// therefore re-read on this cascade after every signal, and focusout
// joins the signals: a field losing focus is the dismissal the
// viewport events miss.
export const SETTLE_DELAYS_MS = [100, 300, 700];

// trackVisualViewport binds `el` to the visual viewport and returns a
// disposer. Where the API is absent (older browsers, tests) it does
// nothing at all, leaving the stylesheet's dvh height in charge.
export function trackVisualViewport(el: HTMLElement): () => void {
  const vv = typeof window === "undefined" ? undefined : window.visualViewport;
  if (!vv) return () => {};

  // The rehome waits out the gesture: scrolling during an active drag
  // would fight the rubber-band. A quiet stretch after the last viewport
  // event with the stuck geometry still present is the settled state.
  let rehomeTimer: number | null = null;

  const apply = (): void => {
    const style = frameStyle(vv);
    if (style === null) {
      el.style.removeProperty("height");
      el.style.removeProperty("transform");
      return;
    }
    el.style.height = style.height;
    el.style.transform = style.transform;
    if (rehomeTimer !== null) window.clearTimeout(rehomeTimer);
    rehomeTimer = null;
    if (viewportNeedsRehome(vv, window.innerHeight)) {
      rehomeTimer = window.setTimeout(() => {
        rehomeTimer = null;
        if (viewportNeedsRehome(vv, window.innerHeight)) window.scrollTo(0, 0);
      }, 250);
    }
  };

  let settleTimers: number[] = [];
  const settle = (): void => {
    apply();
    for (const timer of settleTimers) window.clearTimeout(timer);
    settleTimers = SETTLE_DELAYS_MS.map((ms) => window.setTimeout(apply, ms));
  };

  settle();
  vv.addEventListener("resize", settle);
  vv.addEventListener("scroll", settle);
  window.addEventListener("focusout", settle);
  return () => {
    vv.removeEventListener("resize", settle);
    vv.removeEventListener("scroll", settle);
    window.removeEventListener("focusout", settle);
    for (const timer of settleTimers) window.clearTimeout(timer);
    if (rehomeTimer !== null) window.clearTimeout(rehomeTimer);
    el.style.removeProperty("height");
    el.style.removeProperty("transform");
  };
}
