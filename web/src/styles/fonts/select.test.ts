import assert from "node:assert/strict";
import { test } from "node:test";
import { licensedFiles, selectFontSet } from "./select.ts";

const licensedSheet = Array.from({ length: 42 }, (_, i) => `@font-face{src:url("/fonts/Test${i}.woff2")}`).join("\n");
const files = licensedFiles(licensedSheet);

test("the manifest lists each required font once", () => {
  assert.equal(files.length, 42);
  assert.equal(new Set(files).size, files.length);
  for (const file of files) assert.doesNotMatch(file, /\/free\//);
});

for (const override of [undefined, "", "auto", "licensed", "free"]) {
  test(`${JSON.stringify(override)} with complete supplied assets`, () => {
    assert.equal(selectFontSet({ override, licensedSheet, supplied: true, present: () => true }), override === "free" ? "free" : "licensed");
  });
  test(`${JSON.stringify(override)} without supplied assets`, () => {
    const select = () => selectFontSet({ override, licensedSheet, supplied: false, present: () => { throw new Error("must not inspect assets"); } });
    if (override === "licensed") assert.throws(select, /requires assets.*NOISEDECK_LICENSED_FONTS_DIR/);
    else assert.equal(select(), "free");
  });
  test(`${JSON.stringify(override)} with incomplete supplied assets`, () => {
    for (const missing of files) {
      const select = () => selectFontSet({ override, licensedSheet, supplied: true, present: (file) => file !== missing });
      if (override === "free") assert.equal(select(), "free");
      else assert.throws(select, (err: Error) => err.message.includes("Incomplete licensed font set") && err.message.includes(missing));
    }
  });
}

test("empty supplied sets fail and invalid modes fail even without assets", () => {
  assert.throws(() => selectFontSet({ override: "auto", licensedSheet, supplied: true, present: () => false }), /42 missing/);
  assert.throws(() => selectFontSet({ override: "bogus", licensedSheet, supplied: false, present: () => false }), /must be "auto", "free" or "licensed"/);
});

for (const override of [undefined, "", "auto", "free", "licensed"]) {
  for (const supplied of [false, true]) {
    test(`${JSON.stringify(override)} without a licensed stylesheet, supplied=${supplied}`, () => {
      const select = () => selectFontSet({ override, licensedSheet: undefined, supplied, present: () => { throw new Error("must not inspect assets"); } });
      if (override === "licensed") assert.throws(select, /NOISEDECK_FONTS=licensed requires the licensed stylesheet/);
      else assert.equal(select(), "free");
    });
  }
}
