import { enrollmentHtml, type EnrollmentScreen, type EnrollmentView } from "../app/security/enrollment.ts";
import { bindEnrollmentActions, type EnrollmentAction } from "../app/security/enrollment-actions.ts";
import { mountManagementPreview, type ManagementFigure } from "./management-preview.ts";
import type { SecurityPreviewFigure, EnrollmentPreviewFigure } from "./security-states.ts";
import { RUNTIME_PREVIEW_FIGURES, mountRuntimePreview, type RuntimePreviewFigure } from "./security-runtime-preview.ts";

// These values are design fixtures. This module is reachable only from preview.html.
const workspace = { name: "atlas-7", address: "atlas-7.deck.example", operator: "op-4412" };
const exampleKey = "DK1-7QH2-KMD4-9XTF-B3PV-R6NC-J5LW-2SYE-8AGK-D4MU-X9TB-Q7HZ-3FRN-W5JC-5K2A";
const screens: Record<EnrollmentPreviewFigure, EnrollmentScreen> = {
  "1a": { kind: "invitation", expires: "22 Sep, 18:00" },
  "1b": { kind: "invitation", expires: "22 Sep, 18:00", boundaryOpen: true },
  "1c": { kind: "invitation-expired" },
  "1d": { kind: "interrupted", email: "owner@fablework.example", interruptedAt: "18 Sep", methodName: "Laptop passkey", methodKind: "passkey" },
  "1m": { kind: "invitation", expires: "22 Sep, 18:00" },
  "2a": { kind: "choose-method" },
  "2b": { kind: "passkey-pending" },
  "2c": { kind: "checking-method", methodKind: "passkey" },
  "2d": { kind: "passkey-incompatible" },
  "2e": { kind: "password" },
  "2f": { kind: "method-ready", methodKind: "passkey", methodName: "Laptop passkey" },
  "2m": { kind: "choose-method" },
  "2n": { kind: "passkey-incompatible" },
  "3a": { kind: "recovery-save", key: exampleKey, example: true },
  "3b": { kind: "recovery-verify" },
  "3c": { kind: "recovery-verify", input: "4RTP 8KWN 2HDL QX93 7MFB J6VS 5CZY 9TAE RN2K 3GUP W8XM 6QHD H4TN", failure: { kind: "wrong-key" } },
  "3d": { kind: "recovery-verify", input: "7QH2 KMD4 9XTF B3PV R6NC J5LVV 2SYE 8AGK D4MU X9TB Q7HZ 3FRN W5JC", failure: { kind: "format", message: "Group 6 has five characters. Every group has four.", hint: "Two letters are easy to read as one. Compare group 6 with your saved copy — the rest of the key has not been checked yet." } },
  "3e": { kind: "complete", methodName: "Laptop passkey", methodCheckedAt: "a minute ago", recoveryCheckedAt: "just now", kitDownloadRequested: true },
  "3m": { kind: "recovery-save", key: exampleKey, example: true },
  "3n": { kind: "recovery-verify" },
};

export function mountSecurityPreview(root: HTMLElement, figure: SecurityPreviewFigure): void {
  if (figure.startsWith("6") || figure === "7b") { mountManagementPreview(root, figure as ManagementFigure); return; }
  if ((RUNTIME_PREVIEW_FIGURES as readonly string[]).includes(figure)) { mountRuntimePreview(root, figure as RuntimePreviewFigure); return; }
  document.documentElement.dataset.securityPreview = "true";
  root.dataset.securityFigure = figure;
  let view: EnrollmentView = { workspace, screen: screens[figure as EnrollmentPreviewFigure] };
  let unbind: (() => void) | undefined;
  function render(): void {
    unbind?.();
    root.innerHTML = enrollmentHtml(view);
    unbind = bindEnrollmentActions(root, { run: act, error(message) { view = { ...view, error: message }; render(); } });
    if (figure === "2e" && view.screen.kind === "password") {
      root.querySelectorAll<HTMLInputElement>('input[type="password"]').forEach(input => { input.value = "preview password"; });
      root.querySelector<HTMLInputElement>('input[name="password"]')?.dispatchEvent(new Event("input", { bubbles: true }));
    }
  }
  async function act(action: EnrollmentAction): Promise<void> {
    // Navigation between fixture states is a design preview, never enrollment.
    switch (action.kind) {
      case "toggle-boundary":
        if (view.screen.kind === "invitation") view = { workspace, screen: { ...view.screen, boundaryOpen: !view.screen.boundaryOpen } };
        break;
      case "google-sign-in": case "cancel-passkey": view = { workspace, screen: screens["2a"] }; break;
      case "choose-password": view = { workspace, screen: screens["2e"] }; break;
      case "create-passkey": view = { workspace, screen: screens["2b"] }; break;
      default: view = { ...view, notice: "Design preview. This action requires a connected workspace." };
    }
    render();
  }
  render();
  document.documentElement.dataset.previewState = `security-${figure}`;
}
