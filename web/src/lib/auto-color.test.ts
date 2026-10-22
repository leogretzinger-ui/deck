import type { ProjectWithSessions } from "../panels/sidebar/sidebar.ts";
import assert from "node:assert/strict";
import test from "node:test";

import {
  SESSION_COLOR_VARS,
} from "./session-colors.ts";
import { projectChevronHtml, sessionRailStyle } from "../panels/sidebar/markup.ts";
import { applyAutoColors, autoColorHues, autoColorWheel } from "./auto-color.ts";

function projectGroup(
  id: string,
  name: string,
  color?: string | null,
): ProjectWithSessions {
  return {
    project: { id, name, canonical_path: `/srv/${name}`, last_used_at: "t" },
    sessions: [],
    color,
  };
}

// The wheel is the tag palette's marks in the strip's circle order,
// neutrals filtered out — never "the palette's first eleven
// entries", because the strip opens with "no color" and carries neutral
// identities of its own.
test("the wheel is the tag palette's marks, in circle order, neutrals excluded", () => {
  assert.deepEqual(autoColorWheel(), [
    "crimson", "vermilion", "gold", "lime", "emerald",
    "teal", "cerulean", "sapphire", "indigo", "violet", "orchid",
  ]);
});

// Even intervals over the eleven-hue wheel,
// round(i × 11 / n) % 11 — full rainbow at any count, near-duplicates
// skipped at low counts. The pins name actual hues, so a change to the
// arithmetic is a visible diff against real names, not a silent reshuffle.
test("auto-color samples the eleven-hue wheel at even intervals", () => {
  assert.deepEqual(autoColorHues(1), ["crimson"]);
  // Two projects sit half a wheel apart, not crimson and vermilion.
  assert.deepEqual(autoColorHues(2), ["crimson", "cerulean"]);
  assert.deepEqual(autoColorHues(3), ["crimson", "emerald", "sapphire"]);
  assert.deepEqual(autoColorHues(4), ["crimson", "lime", "cerulean", "indigo"]);
  assert.deepEqual(autoColorHues(5), [
    "crimson", "gold", "emerald", "sapphire", "violet",
  ]);
  assert.deepEqual(autoColorHues(6), [
    "crimson", "gold", "emerald", "cerulean", "sapphire", "violet",
  ]);
  const seven = autoColorHues(7);
  assert.deepEqual(seven, [
    "crimson", "gold", "lime", "teal", "cerulean", "indigo", "violet",
  ]);
  assert.equal(new Set(seven).size, 7, "seven projects get seven distinct hues");
});

// The round step closes the circle: ten of eleven take every hue but teal
// (round(5.5) rounds up to keep the intervals even), and wheel neighbours
// such as crimson and orchid can land on the ends of the run, because the
// sample wraps the way the wheel it samples does. Even spacing is the
// rule; the ends of a run get no special treatment.
test("just under the wheel size the even intervals skip a hue and close the circle", () => {
  assert.deepEqual(autoColorHues(10), [
    "crimson", "vermilion", "gold", "lime", "emerald",
    "cerulean", "sapphire", "indigo", "violet", "orchid",
  ]);
  assert.deepEqual(autoColorHues(11), [...autoColorWheel()]);
});

test("auto-color degenerates sanely", () => {
  assert.deepEqual(autoColorHues(0), []);
  assert.deepEqual(autoColorHues(-3), []);
  assert.deepEqual(autoColorHues(1), ["crimson"]);
});

// Twelve rows cannot have eleven unique hues, so past the wheel size the
// ramp walks the wheel two slots per row. The pin names actual hues, so a
// change to the policy is a visible diff against real names, not a silent
// reshuffle.
test("past the wheel size the ramp strides two slots per row", () => {
  assert.deepEqual(autoColorHues(12), [
    "crimson", "gold", "emerald", "cerulean", "indigo", "orchid",
    "vermilion", "lime", "teal", "sapphire", "violet", "crimson",
  ]);
});

// The policy both regimes serve: two rows side by side must be
// distinguishable. No count may hand neighbouring rows the same hue — two
// side-by-side rows in one hue read as one project — and where the
// sampling leaves room, neighbouring rows must also steer clear of
// sapphire next to indigo, the palette's closest pair (dEok 0.055). Within
// one wheel the even intervals decide, and only at eight, ten, and eleven
// rows does the nearest-slot sample necessarily contain both hues of that
// pair; those three counts are the whole exception list, and the
// stride-two regime owns the guarantee outright from twelve up.
test("neighbouring rows never share a hue, and past the wheel never the closest pair", () => {
  const forcedBySampling = [8, 10, 11];
  for (let n = 1; n <= 30; n++) {
    const hues = autoColorHues(n);
    assert.equal(hues.length, n);
    for (let i = 1; i < hues.length; i++) {
      assert.notEqual(
        hues[i], hues[i - 1],
        `n=${n}: rows ${i - 1} and ${i} both take ${hues[i]}`,
      );
      const neighbours = [hues[i - 1]!, hues[i]!].sort().join("+");
      if (neighbours === "indigo+sapphire") {
        assert.ok(
          forcedBySampling.includes(n),
          `n=${n}: sapphire and indigo sit side by side only where the sampling forces them`,
        );
      }
    }
    if (n >= 11) {
      assert.equal(new Set(hues).size, 11, `n=${n}: every hue still appears`);
    }
  }
});

// A neutral leaking into an assignment would render no swatch at all
// ("no color" has no var) or a neutral that is not a hue (the neutral
// identities are pinned raw hex). Resolve every assigned hue through the
// shipped palette map and demand a mark var.
test("neutrals never appear in an auto-color assignment", () => {
  for (let n = 1; n <= 30; n++) {
    for (const hue of autoColorHues(n)) {
      const value = SESSION_COLOR_VARS[hue];
      assert.ok(
        typeof value === "string" && value.startsWith("var(--color-mark-"),
        `n=${n}: "${hue}" is not a mark hue`,
      );
    }
  }
});

// The ramp reads down the rail, so hue i goes to the row at displayed
// position i — not to the i-th name alphabetically. Here the displayed
// order is zulu, alpha, mike; alphabetical order would be alpha, mike,
// zulu and would hand crimson to alpha instead of zulu.
test("auto-color assigns hues by displayed position, not by name", () => {
  const groups = [
    projectGroup("p1", "zulu"),
    projectGroup("p2", "alpha"),
    projectGroup("p3", "mike"),
  ];
  applyAutoColors(groups, true, ["p1", "p2", "p3"]);
  assert.deepEqual(groups.map((g) => g.autoColor), [
    "var(--color-mark-crimson)",
    "var(--color-mark-emerald)",
    "var(--color-mark-sapphire)",
  ]);

  const chevron = projectChevronHtml(false, groups[0]!.autoColor);
  const rail = sessionRailStyle(groups[0]!.autoColor);
  assert.ok(chevron.includes("var(--color-mark-crimson)"), "the top row's token tints the chevron");
  assert.ok(rail.includes("var(--color-mark-crimson)"), "the top row's token tints the rail");
});

// The groups array itself can arrive in any order (the server's); the
// presented order is what pairs ids with positions, so the same displayed
// sequence yields the same per-project hues whatever the input order.
test("displayed-order mapping is keyed by id, not by array position", () => {
  const groups = [
    projectGroup("p1", "alpha"),
    projectGroup("p2", "zulu"),
    projectGroup("p3", "mike"),
  ];
  applyAutoColors(groups, true, ["p2", "p3", "p1"]);
  const tintOf = (id: string): string | null | undefined =>
    groups.find((g) => g.project.id === id)?.autoColor;
  assert.equal(tintOf("p2"), "var(--color-mark-crimson)", "zulu renders first, so zulu is crimson");
  assert.equal(tintOf("p3"), "var(--color-mark-emerald)", "mike renders second");
  assert.equal(tintOf("p1"), "var(--color-mark-sapphire)", "alpha renders last");
});

// Without a presented order — the model's refresh paths, ahead of the
// render that supplies one — the alphabetical default orders the ramp:
// that is the display order until you drag.
test("without a presented order the alphabetical default orders the ramp", () => {
  const groups = [projectGroup("p1", "zulu"), projectGroup("p2", "alpha")];
  applyAutoColors(groups, true);
  assert.equal(groups[0]!.autoColor, "var(--color-mark-cerulean)", "zulu sorts second alphabetically");
  assert.equal(groups[1]!.autoColor, "var(--color-mark-crimson)", "alpha sorts first");
});

// Auto-color projects: the preference must reach the sidebar.
// The mark tokens flow straight through the existing chevron and rail helpers,
// so the test asserts at the level that broke - the rendered row - not just
// the pure hue list.
test("auto-color applies distinct mark tokens to uncoloured projects", () => {
  const groups = [
    projectGroup("p1", "alpha"),
    projectGroup("p2", "beta"),
    projectGroup("p3", "gamma"),
    projectGroup("p4", "delta"),
  ];
  applyAutoColors(groups, true);
  const colors = groups.map((g) => g.autoColor);
  assert.ok(colors.every((c) => c?.startsWith("var(--color-mark-")), "every project gets a mark token");
  assert.equal(new Set(colors).size, 4, "four projects get four distinct hues");
  // The alphabetical default orders the ramp: alpha, beta, delta, gamma.
  // Four spread over the wheel as crimson, lime, cerulean, indigo.
  assert.deepEqual(colors, [
    "var(--color-mark-crimson)",
    "var(--color-mark-lime)",
    "var(--color-mark-indigo)",
    "var(--color-mark-cerulean)",
  ]);

  const chevron = projectChevronHtml(false, groups[0]!.autoColor);
  const rail = sessionRailStyle(groups[0]!.autoColor);
  assert.ok(chevron.includes("var(--color-mark-crimson)"), "the token tints the chevron");
  assert.ok(rail.includes("var(--color-mark-crimson)"), "the token tints the rail");
});

test("auto-color leaves an assigned project colour alone", () => {
  const groups = [
    projectGroup("p1", "alpha", "red"),
    projectGroup("p2", "beta"),
  ];
  applyAutoColors(groups, true);
  assert.equal(groups[0]!.color, "red", "an assigned colour wins");
  // beta is the second of two alphabetically ordered projects, so it gets
  // the spread's second hue (cerulean), not the wheel's second entry
  // (vermilion).
  assert.equal(groups[1]!.autoColor, "var(--color-mark-cerulean)", "an uncoloured project gets a mark token");
});

// The chevron and rail resolve a user-assigned colour ahead of the tint, but the
// session tiles read the tint directly. A tint left standing under a colour the
// you picked later would paint the tiles one hue and the row another.
test("a user-assigned colour drops the derived tint under it", () => {
  const groups = [projectGroup("p1", "alpha"), projectGroup("p2", "beta")];
  const tintOf = (i: number): string | null | undefined => groups[i]!.autoColor;

  applyAutoColors(groups, true);
  assert.ok(tintOf(0)?.startsWith("var(--color-mark-"));

  groups[0]!.color = "blue";
  applyAutoColors(groups, true);
  assert.equal(tintOf(0), undefined, "the tint gives way to the tag");
  assert.ok(tintOf(1)?.startsWith("var(--color-mark-"), "the others keep theirs");

  groups[0]!.color = null;
  applyAutoColors(groups, true);
  assert.ok(tintOf(0)?.startsWith("var(--color-mark-"), "clearing the tag lets the tint return");
});

test("auto-color clears only its own tints when turned off", () => {
  const groups = [projectGroup("p1", "alpha"), projectGroup("p2", "beta")];
  applyAutoColors(groups, true);
  assert.ok(groups[0]!.autoColor?.startsWith("var(--color-mark-"));
  applyAutoColors(groups, false);
  assert.equal(groups[0]!.autoColor, undefined, "auto tints are removed");
  assert.equal(groups[1]!.autoColor, undefined);
});

test("turning auto-color off keeps a user-assigned colour", () => {
  const groups = [projectGroup("p1", "alpha", "red")];
  applyAutoColors(groups, true);
  applyAutoColors(groups, false);
  assert.equal(groups[0]!.color, "red", "a deliberate choice survives the toggle");
});
