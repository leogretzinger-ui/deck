import assert from "node:assert/strict";

// Compare against the selected installation's face, preserving UI/mono checks
// without assuming every build carries the licensed family.
export async function assertFontVoice(page, family, voice, message) {
  const declared = await page.evaluate(voice => getComputedStyle(document.body).getPropertyValue(`--font-${voice}`), voice);
  const first = value => value.split(",")[0].trim().replace(/^["']|["']$/g, "");
  assert.ok(first(declared), `--font-${voice} must be declared`);
  assert.equal(first(family), first(declared), message);
}
