import assert from "node:assert/strict";
import { test } from "node:test";

import { MONO_REF, TRIMS } from "./data.ts";
import {
  catalogueQuery, familyList, googleFontsHref, licOf, monoBase, remoteQuery, setTrim, snapWeight,
  solveMatch, trimOf, trimReadout, weightFor, weightOptionsFor,
  type TrimStore,
} from "./logic.ts";

test("trims belong to the typeface, not the slot", () => {
  let store: TrimStore = {};
  store = setTrim(store, "Victor Mono", "s", "1.06", "compare");
  // The same face read anywhere in the same scope carries the same trim —
  // there is no pane or role in the key.
  assert.equal(trimOf(store, "Victor Mono", "s", "compare"), "1.06");
  // A different face is untouched and reads the default.
  assert.equal(trimOf(store, "JetBrains Mono", "s", "compare"), "1");
});

test("trims are scoped per mode; weight is shared across modes", () => {
  let store: TrimStore = {};
  store = setTrim(store, "Jost", "s", "1.15", "compare");
  // The showcase reads its own baseline, not the compare match: a compare
  // match written against the 13px reference must not silently scale the
  // showcase's own tokens.
  assert.equal(trimOf(store, "Jost", "s", "showcase"), "1");
  assert.equal(trimOf(store, "Jost", "s", "compare"), "1.15");
  store = setTrim(store, "Jost", "w:ui", "500", "compare");
  assert.equal(trimOf(store, "Jost", "w:ui", "showcase"), "500");
});

test("weights are absolute; sub-100 leftovers read as unset", () => {
  let store: TrimStore = {};
  store = setTrim(store, "Jost", "w:ui", "-100", "showcase");
  assert.deepEqual(store, {});
  store = setTrim(store, "Jost", "w:ui", "600", "showcase");
  assert.equal(weightFor(store, "ui", "Jost"), 600);
  assert.equal(weightFor({}, "ui", "Jost"), 400);
  assert.equal(weightFor({}, "display", "Jost"), 300);
});

test("mono candidates render at their own weight, not the token correction", () => {
  // Deck's --type-mono-weight is a correction for the reference cut, not a
  // neutral test setting; a candidate's base is its own declared weight.
  assert.equal(monoBase("Operator Mono SSm"), 400);
  assert.equal(monoBase("Victor Mono"), 400);
  assert.equal(weightFor({}, "mono", "Victor Mono"), 400);
});

test("a shifted ramp snaps to the cuts the face actually has", () => {
  // A tie keeps the earlier cut, matching the prototype's reduce.
  assert.equal(snapWeight("Operator Mono SSm", 600), 500);
  assert.equal(snapWeight("Operator Mono SSm", 650), 700);
  assert.equal(snapWeight("Operator Mono SSm", 450), 400);
  assert.equal(snapWeight("Operator Mono SSm", 100), 300);
  // A face without declared cuts keeps the clamped request.
  assert.equal(snapWeight("Jost", 600), 600);
  assert.equal(snapWeight("Jost", 950), 900);
  assert.deepEqual(weightOptionsFor("Operator Mono SSm"), [300, 400, 500, 700]);
  assert.deepEqual(weightOptionsFor("Anything Else"), [200, 300, 400, 500, 600, 700, 800]);
});

test("candidate queries build the exact Google Fonts URL with a bare-family fallback", () => {
  assert.equal(catalogueQuery("Libre Baskerville"), "ital,wght@0,400;0,700;1,400");
  assert.equal(
    googleFontsHref("Libre Baskerville", catalogueQuery("Libre Baskerville")),
    "https://fonts.googleapis.com/css2?family=Libre+Baskerville:ital,wght@0,400;0,700;1,400&display=swap",
  );
  // The fallback is the bare family — worst case Regular only, never nothing.
  assert.equal(
    googleFontsHref("Libre Baskerville"),
    "https://fonts.googleapis.com/css2?family=Libre+Baskerville&display=swap",
  );
  assert.equal(catalogueQuery("Papyrus"), undefined);
});

test("licence badging: licensed outranks loadability; unknown is unverified", () => {
  const loadsEverything = () => true;
  assert.equal(licOf("Euclid Flex", loadsEverything), "licensed");
  assert.equal(licOf("Operator Mono SSm", loadsEverything), "licensed");
  assert.equal(licOf("Roboto Mono"), "apache");
  assert.equal(licOf("Jost"), "ofl");
  assert.equal(licOf("Helvetica", loadsEverything), "system");
  assert.equal(licOf("Helvetica"), "unverified");
});

test("the match solver holds anchor, line box, and advance", () => {
  const refSize = parseFloat(MONO_REF["--type-mono-size"] ?? "13");
  const ref = { inkPerEm: 0.548, advancePerEm: 0.6 };
  const face = { inkPerEm: 0.619, advancePerEm: 0.55 };
  const got = solveMatch(ref, face);
  assert.ok(got !== null);
  // Size: the anchor measures the same, so a taller-per-em face shrinks.
  const mul = parseFloat(got.s);
  assert.ok(mul < 1, `taller face got mul=${mul}, want < 1`);
  assert.ok(Math.abs(mul - (0.548 / 0.619)) < 0.011);
  // Lead holds the reference line box: lead is the inverse of the size trim.
  assert.ok(Math.abs(parseFloat(got.l) * mul - 1) < 0.02);
  // Tracking holds the advance: size*(advance+track) equals the reference's.
  const size = refSize * mul;
  const adv = size * (face.advancePerEm + parseFloat(got.t));
  assert.ok(Math.abs(adv - refSize * ref.advancePerEm) < refSize * 0.005);
});

test("the solver clamps to the slider ranges", () => {
  const got = solveMatch({ inkPerEm: 0.9, advancePerEm: 0.6 }, { inkPerEm: 0.3, advancePerEm: 0.9 });
  assert.ok(got !== null);
  assert.equal(got.s, TRIMS[0]!.max);
  assert.equal(got.t, TRIMS[2]!.min);
});

test("an unmeasurable face refuses to report numbers", () => {
  // Zero ink means nothing rendered — the serif-fallback assert upstream
  // reports it as unloaded, and the solver must refuse rather than emit
  // trims measured off a fallback face.
  assert.equal(solveMatch({ inkPerEm: 0, advancePerEm: 0.6 }, { inkPerEm: 0.5, advancePerEm: 0.6 }), null);
  assert.equal(solveMatch({ inkPerEm: 0.548, advancePerEm: 0.6 }, { inkPerEm: 0, advancePerEm: 0.6 }), null);
});

test("trim readouts: percentages for size and lead, signed mills for track", () => {
  assert.equal(trimReadout("s", 1), "100%");
  assert.equal(trimReadout("s", 1.06), "106%");
  assert.equal(trimReadout("l", 0.8), "80%");
  assert.equal(trimReadout("t", 0), "0");
  assert.equal(trimReadout("t", 0.012), "+12");
  assert.equal(trimReadout("t", -0.004), "−4");
});

// A family the build ships is never fetched remotely, whatever the case of
// the name; every other catalogue family still gets its query.
test("remoteQuery skips the families the build supplies", () => {
  const supplied = ["Jost", "EB Garamond", "Victor Mono"];
  assert.equal(remoteQuery("Jost", supplied), undefined);
  assert.equal(remoteQuery(" eb garamond ", supplied), undefined);
  assert.equal(remoteQuery("Victor Mono", supplied), undefined);
  assert.equal(remoteQuery("Libre Baskerville", supplied), catalogueQuery("Libre Baskerville"));
  assert.equal(remoteQuery("Jost", ["Euclid Flex"]), catalogueQuery("Jost"));
  assert.equal(remoteQuery("Euclid Flex", ["Euclid Flex"]), undefined);
});

// One entry per family, the reference group first: a free build's reference
// faces are catalogue faces too and two roles share Jost, so the picker
// showed Jost three times and EB Garamond twice.
test("familyList names each family once, reference group first", () => {
  const list = familyList(
    ["Jost", "Jost", "EB Garamond", "IBM Plex Mono"], "Deck · free set",
    [{ name: "EB Garamond", lic: "ofl" }, { name: "jost", lic: "ofl" }, { name: "Inter", lic: "ofl" }],
    ["inter", "Menlo"], ["  Menlo ", "Berkeley Mono"],
    () => "ofl",
  );
  assert.deepEqual(list.map((f) => [f.name, f.group]), [
    ["Jost", "Deck · free set"], ["EB Garamond", "Deck · free set"], ["IBM Plex Mono", "Deck · free set"],
    ["Inter", "Free · redistributable"], ["Menlo", "On this machine"], ["Berkeley Mono", "On this machine"],
  ]);
});
