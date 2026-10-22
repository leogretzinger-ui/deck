import assert from "node:assert/strict";
import { test } from "node:test";
import { onboardingHtml, type OnboardingScreen } from "./onboarding.ts";
import { parseOnboardingPreview } from "../../preview/onboarding-states.ts";
const workspace = { name: '<img src=x onerror="bad">', address: "safe.example" };
const render = (screen: OnboardingScreen) => onboardingHtml({ workspace, screen });

test("required and unavailable policies expose only their authorized branch", () => {
  assert.doesNotMatch(render({ kind: "encryption", policy: "required" }), /continue-unencrypted/);
  const unavailable = render({ kind: "encryption", policy: "unavailable" });
  assert.doesNotMatch(unavailable, /enable-encryption|at the moment/);
  assert.match(unavailable, /does not support Deck encryption/);
  const unknown = render({ kind: "unreachable" });
  assert.doesNotMatch(unknown, /continue-unencrypted|enable-encryption|open-workspace/);
  assert.match(unknown, /data-onboarding-action="check-state"/);
});
test("unknown setup exposes observation without pretending that it completed", () => {
  const html = render({ kind: "unconfirmed" });
  assert.match(html, /data-onboarding-action="check-state"/);
  assert.doesNotMatch(html, /data-onboarding-action="(?:resume-setup|open-workspace)"|Setup confirmed/);
});
test("resume requires an explicit saved-method proof without exposing a recovery key", () => {
  const html = render({ kind: "resume", requiresMethod: true, rows: [{ label: "Saved method", state: "done" }] });
  assert.match(html, /Verify your saved unlock method/);
  assert.doesNotMatch(html, /recovery-save|textarea|DK1-|Continue to your recovery key/);
});
test("only supplied facts render and all metadata is escaped", () => {
  const html = render({ kind: "done", encryption: { kind: "unencrypted", policy: "optional" } });
  assert.doesNotMatch(html, /<img src=x|Unlock method|Recovery key|<dt>Owner<\/dt>/);
  assert.match(html, /&lt;img/);
  assert.doesNotMatch(render({ kind: "resume", requiresMethod: false, rows: [] }), /September|Signed in as/);
});
test("encrypted forms reuse the 15-character and repeat password contract", () => {
  const html = render({ kind: "enrollment", enrollment: { kind: "password" } });
  assert.match(html, /At least 15 characters/);
  assert.match(html, /name="repeat"/);
  assert.match(html, /Step 3 of 4/);
  assert.doesNotMatch(html, /correct horse|value=".*password/);
});
test("onboarding routes reject mixed or unknown axes", () => {
  assert.equal(parseOnboardingPreview(new URLSearchParams("onboarding=encryption-required")), "encryption-required");
  assert.throws(() => parseOnboardingPreview(new URLSearchParams("onboarding=bogus")));
  assert.throws(() => parseOnboardingPreview(new URLSearchParams("onboarding=welcome&security=1a")));
});

test("plain setup progress does not suggest an unlock method exists", () => {
  assert.doesNotMatch(render({ kind: "finishing", encrypted: false, rows: [] }), /saved unlock method/);
  assert.match(render({ kind: "finishing", rows: [] }), /saved unlock method/);
});

test("rejected setup can switch accounts without claiming a used link or an owner", () => {
  assert.doesNotMatch(render({ kind: "needlink" }), /switch-account/);
  const html = render({ kind: "needlink", canSwitchAccount: true });
  assert.match(html, /data-onboarding-action="switch-account"/);
  assert.doesNotMatch(html, /Link expired|already been used|belongs to another/);
});
