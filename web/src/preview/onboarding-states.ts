// Deliberate branch coverage, not a product of every policy, device and form.
export const ONBOARDING_PREVIEW_IDS = [
  "welcome", "welcome-required", "welcome-unavailable", "google", "needlink", "expired", "used", "notowner", "unreachable", "interrupted", "resume",
  "encryption", "encryption-required", "encryption-unavailable", "encryption-boundary",
  "choose", "create", "check", "incompatible", "setpassword", "ready", "key", "verify",
  "finishing", "finishing-unencrypted", "migration", "migration-mobile", "unconfirmed", "failed", "done", "done-unencrypted", "signin", "signin-unencrypted", "newbrowser",
  "welcome-mobile", "needlink-mobile", "encryption-mobile", "encryption-required-mobile", "encryption-unavailable-mobile", "resume-mobile", "done-mobile",
] as const;
export type OnboardingPreviewId = typeof ONBOARDING_PREVIEW_IDS[number];
export function parseOnboardingPreview(params: URLSearchParams): OnboardingPreviewId | null {
  const value = params.get("onboarding");
  if (value === null) return null;
  if (!(ONBOARDING_PREVIEW_IDS as readonly string[]).includes(value)) throw new Error(`invalid onboarding: ${JSON.stringify(value)}`);
  if ([...params.keys()].some(key => key !== "onboarding" && key !== "theme")) throw new Error("Onboarding preview cannot be combined with other fixture axes.");
  return value as OnboardingPreviewId;
}
export const ONBOARDING_PREVIEW_STATES = ONBOARDING_PREVIEW_IDS.map(id => ({
  name: `onboarding-${id}`, params: { onboarding: id }, ready: `[data-onboarding-preview="${id}"] h1`,
  viewport: id.endsWith("-mobile") ? { width: 393, height: 852 } : { width: 1180, height: 760 },
}));
