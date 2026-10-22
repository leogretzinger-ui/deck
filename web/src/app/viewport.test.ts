// The keyboard-aware frame geometry. Pure, so the arithmetic is checked
// without a browser: `dvh` does not shrink for a software keyboard, so
// this is what keeps a focused field visible on iOS.
import assert from "node:assert/strict";
import test from "node:test";

import { frameStyle, viewportNeedsRehome } from "./viewport.ts";

test("with no keyboard the frame is the full visual viewport", () => {
  assert.deepEqual(frameStyle({ height: 844, offsetTop: 0 }), {
    height: "844px",
    transform: "translateY(0px)",
  });
});

// The case this exists for: the keyboard takes roughly 300px, and the
// frame must shrink to what remains rather than keep its full height
// with the bottom hidden underneath.
test("an open keyboard shrinks the frame to the visible region", () => {
  assert.equal(frameStyle({ height: 508, offsetTop: 0 })?.height, "508px");
});

// iOS scrolls the page under the keyboard when a field near the bottom
// is focused; without compensating, the frame drifts up out of view.
test("a page scrolled under the keyboard is translated back", () => {
  assert.deepEqual(frameStyle({ height: 508, offsetTop: 120 }), {
    height: "508px",
    transform: "translateY(120px)",
  });
});

// Floor, not round: a frame half a pixel taller than the visible region
// raises a scrollbar, and w-screen turns that into horizontal overflow.
test("fractional metrics floor, so the frame never exceeds the region", () => {
  const style = frameStyle({ height: 507.6, offsetTop: 119.4 });
  assert.equal(style?.height, "507px");
  assert.equal(style?.transform, "translateY(119px)");
});

// Unusable readings must fall back to the stylesheet rather than
// collapse the frame to nothing.
test("a missing or nonsensical viewport yields no override", () => {
  assert.equal(frameStyle(null), null);
  assert.equal(frameStyle({ height: 0, offsetTop: 0 }), null);
  assert.equal(frameStyle({ height: Number.NaN, offsetTop: 0 }), null);
});

test("a negative offset never pushes the frame off-screen", () => {
  assert.equal(frameStyle({ height: 800, offsetTop: -40 })?.transform, "translateY(0px)");
});

// The stuck state a touch drag can leave behind: full-height visual
// viewport (no keyboard) with a positive offset. The keyboard-open
// geometry — short height plus a load-bearing offset — must never
// qualify, or the rehome would scroll the page out from under a focused
// field.
test("a full-height viewport with a stuck offset asks to rehome", () => {
  assert.equal(viewportNeedsRehome({ height: 800, offsetTop: 24 }, 800), true);
  assert.equal(viewportNeedsRehome({ height: 799.5, offsetTop: 24 }, 800), true);
});

test("the keyboard-open offset is load-bearing, never rehomed", () => {
  assert.equal(viewportNeedsRehome({ height: 508, offsetTop: 120 }, 800), false);
});

test("a settled viewport asks for nothing", () => {
  assert.equal(viewportNeedsRehome({ height: 800, offsetTop: 0 }, 800), false);
  assert.equal(viewportNeedsRehome({ height: Number.NaN, offsetTop: 10 }, 800), false);
  assert.equal(viewportNeedsRehome({ height: 800, offsetTop: Number.NaN }, 800), false);
});
