import { bindEnrollmentActions, type EnrollmentAction } from "./enrollment-actions.ts";

export type OnboardingAction = EnrollmentAction
  | { kind: "switch-account" | "resume-setup" | "enable-encryption" | "continue-unencrypted" };
export interface OnboardingActions { run(action: OnboardingAction): Promise<void>; error(message: string): void }
const actions = new Set(["google-sign-in", "switch-account", "check-state", "resume-setup", "enable-encryption", "continue-unencrypted", "toggle-boundary", "open-workspace"]);

/** Dispatches gestures; policy checks and security operations belong to the caller. */
export function bindOnboardingActions(root: HTMLElement, handlers: OnboardingActions): () => void {
  const controller = new AbortController();
  const disposeEnrollment = bindEnrollmentActions(root, {
    run: action => controller.signal.aborted || (root.querySelector('[aria-busy="true"]') && action.kind !== "cancel-passkey") ? Promise.resolve() : handlers.run(action),
    error: message => { if (!controller.signal.aborted) handlers.error(message); },
  });
  root.addEventListener("click", event => {
    const button = event.target instanceof Element ? event.target.closest<HTMLButtonElement>("button[data-onboarding-action]") : null;
    if (!button || !root.contains(button) || button.disabled || root.querySelector('[aria-busy="true"]')) return;
    const kind = button.dataset.onboardingAction;
    if (!kind || !actions.has(kind)) return;
    void handlers.run({ kind } as OnboardingAction).catch(error => {
      if (!controller.signal.aborted) handlers.error(error instanceof Error ? error.message : "This action could not be completed.");
    });
  }, { signal: controller.signal });
  return () => { controller.abort(); disposeEnrollment(); };
}
