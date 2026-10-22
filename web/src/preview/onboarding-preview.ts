import { onboardingHtml, type OnboardingView, type OnboardingScreen } from "../app/security/onboarding.ts";
import { bindOnboardingActions, type OnboardingAction } from "../app/security/onboarding-actions.ts";
import { mountRuntimePreview } from "./security-runtime-preview.ts";
import type { OnboardingPreviewId } from "./onboarding-states.ts";

// Synthetic examples belong exclusively to preview.html, never the coordinator.
const workspace = { name: "atlas-7", address: "atlas-7.deck.example" };
const email = "owner@fablework.example";
const methodName = "Laptop passkey";
const exampleKey = "DK1-7QH2-KMD4-9XTF-B3PV-R6NC-J5LW-2SYE-8AGK-D4MU-X9TB-Q7HZ-3FRN-W5JC-5K2A";
const screens: Record<string, OnboardingScreen> = {
  welcome: { kind: "welcome", policy: "optional" },
  "welcome-required": { kind: "welcome", policy: "required" },
  "welcome-unavailable": { kind: "welcome", policy: "unavailable" },
  google: { kind: "google" }, needlink: { kind: "needlink" }, expired: { kind: "expired" }, used: { kind: "used" },
  notowner: { kind: "notowner", email: "sam@elsewhere.example" }, unreachable: { kind: "unreachable" }, interrupted: { kind: "interrupted" },
  resume: { kind: "resume", email, startedAt: "21 September", requiresMethod: true, rows: [
    { label: "Google account", state: "done", detail: "Done" }, { label: "Workspace encryption", state: "done", detail: "Selected" },
    { label: methodName, state: "done", detail: "Checked" }, { label: "Recovery key", state: "pending", detail: "Not checked" },
  ] },
  encryption: { kind: "encryption", policy: "optional", email },
  "encryption-required": { kind: "encryption", policy: "required" },
  "encryption-unavailable": { kind: "encryption", policy: "unavailable" },
  "encryption-boundary": { kind: "encryption", policy: "optional", email, boundaryOpen: true },
  choose: { kind: "enrollment", enrollment: { kind: "choose-method" } },
  create: { kind: "enrollment", enrollment: { kind: "passkey-pending" } },
  check: { kind: "enrollment", enrollment: { kind: "checking-method", methodKind: "passkey" } },
  incompatible: { kind: "enrollment", enrollment: { kind: "passkey-incompatible" } },
  setpassword: { kind: "enrollment", enrollment: { kind: "password" } },
  ready: { kind: "enrollment", enrollment: { kind: "method-ready", methodKind: "passkey", methodName } },
  key: { kind: "enrollment", enrollment: { kind: "recovery-save", key: exampleKey, example: true } },
  verify: { kind: "enrollment", enrollment: { kind: "recovery-verify" } },
  finishing: { kind: "finishing", rows: [{ label: `${methodName} checked`, state: "done" }, { label: "Recovery key checked", state: "done" }, { label: "Setting up encrypted storage", state: "active" }, { label: "Confirming atlas-7 is ready", state: "pending" }] },
  "finishing-unencrypted": { kind: "finishing", encrypted: false, rows: [{ label: `Signed in as ${email}`, state: "done" }, { label: "Preparing atlas-7", state: "active" }, { label: "Confirming atlas-7 is ready", state: "pending" }] },
  migration: { kind: "migration" },
  unconfirmed: { kind: "unconfirmed" }, failed: { kind: "failed", methodKind: "passkey" },
  done: { kind: "done", email, encryption: { kind: "encrypted", methodName } },
  "done-unencrypted": { kind: "done", email, encryption: { kind: "unencrypted", policy: "optional" } },
  signin: { kind: "signin", encrypted: true }, "signin-unencrypted": { kind: "signin", encrypted: false },
};
export function mountOnboardingPreview(root: HTMLElement, id: OnboardingPreviewId): void {
  document.documentElement.dataset.onboardingPreview = "true";
  root.dataset.onboardingPreview = id;
  if (id === "newbrowser") {
    mountRuntimePreview(root, "4j");
    document.documentElement.dataset.previewState = `onboarding-${id}`;
    return;
  }
  let view: OnboardingView = { workspace, screen: screens[id.replace(/-mobile$/, "")]! };
  let dispose: (() => void) | undefined;
  function render(): void {
    dispose?.();
    root.innerHTML = onboardingHtml({ ...view, busy: view.screen.kind === "enrollment" && view.screen.enrollment.kind === "passkey-pending" });
    dispose = bindOnboardingActions(root, { run: act, error(message) { view = { ...view, error: message }; render(); } });
  }
  async function act(action: OnboardingAction): Promise<void> {
    // Previews navigate between examples; no credential or backend operation succeeds here.
    switch (action.kind) {
      case "toggle-boundary": if (view.screen.kind === "encryption") view = { ...view, screen: { ...view.screen, boundaryOpen: !view.screen.boundaryOpen } }; break;
      case "enable-encryption": view = { workspace, screen: screens.choose! }; break;
      case "continue-unencrypted": view = { workspace, screen: screens["finishing-unencrypted"]! }; break;
      case "choose-password": view = { workspace, screen: screens.setpassword! }; break;
      case "create-passkey": view = { workspace, screen: screens.create! }; break;
      case "cancel-passkey": view = { workspace, screen: screens.choose! }; break;
      default: view = { ...view, notice: "Design preview. This action requires a connected workspace." };
    }
    render();
  }
  render();
  document.documentElement.dataset.previewState = `onboarding-${id}`;
}
