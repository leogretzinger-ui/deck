import { managementHtml, type ManagedMethod, type ManagementScreen, type ManagementView } from "../app/security/management.ts";
import { bindManagementActions, type ManagementAction } from "../app/security/management-actions.ts";

export const MANAGEMENT_FIGURES = ["6a", "6b", "6c", "6d", "6e", "6m", "7b"] as const;
export type ManagementFigure = typeof MANAGEMENT_FIGURES[number];
const methods: ManagedMethod[] = [
  { id: "laptop", revision: 1, kind: "passkey", label: "Laptop passkey", status: "active", checked: "3 hours ago" },
  { id: "phone", revision: 1, kind: "passkey", label: "Phone passkey", status: "active", checked: "6 days ago" },
  { id: "password", revision: 1, kind: "password", label: "Encryption password", status: "active" },
  { id: "recovery", revision: 1, kind: "recovery", label: "Recovery key", status: "active", checked: "2 Aug" },
];
const candidate: ManagedMethod = { id: "new", revision: 1, kind: "passkey", label: "Security key in the drawer", status: "pending", checked: "just now" };
const screens: Record<ManagementFigure, ManagementScreen> = {
  "6a": { kind: "list" },
  "7b": { kind: "recovery-checklist", usedRecovery: { id: "recovery", revision: 1 } },
  "6b": { kind: "ready", candidate },
  "6c": { kind: "ready", candidate: { ...candidate, label: "New phone passkey" }, replacement: methods[1]! },
  "6d": { kind: "last-method", method: methods[0]! },
  "6e": { kind: "password", replacement: methods[2]! },
  "6m": { kind: "list" },
};

// Synthetic design data is reachable only through preview.html.
export function mountManagementPreview(root: HTMLElement, figure: ManagementFigure): void {
  document.documentElement.dataset.securityPreview = "true";
  root.dataset.securityFigure = figure;
  let view: ManagementView = { name: "atlas-7", methods: figure === "6m" ? [{ ...methods[1]!, checked: "3 hours ago" }, { ...methods[0]!, checked: "6 days ago" }, ...methods.slice(2)] : figure === "7b" ? [{ ...methods[0]!, label: "New laptop passkey", checked: "just now" }, ...methods.slice(1)] : methods, screen: screens[figure] };
  let unbind: (() => void) | undefined;
  const render = (): void => {
    unbind?.(); root.innerHTML = managementHtml(view);
    unbind = bindManagementActions(root, act, message => { view = { ...view, error: message }; render(); });
    if (figure === "6e" && view.screen.kind === "password") {
      root.querySelectorAll<HTMLInputElement>('input[type="password"]').forEach(input => { input.value = "x".repeat(input.name === "current" ? 16 : 17); });
      root.querySelector<HTMLInputElement>('input[name="password"]')?.dispatchEvent(new Event("input", { bubbles: true }));
    }
  };
  async function act(action: ManagementAction): Promise<void> {
    const method = "id" in action ? methods.find(item => item.id === action.id) : undefined;
    if (action.kind === "cancel" || action.kind === "close") view = { ...view, screen: { kind: "list" }, error: undefined, notice: undefined };
    else if (action.kind === "review-methods") view = { ...view, screen: { kind: "list" } };
    else if (action.kind === "detail" && method) view = { ...view, screen: { kind: "detail", method } };
    else if (action.kind === "test" && method) view = { ...view, screen: { kind: "authorize", purpose: "test", method } };
    else if (action.kind === "add" || action.kind === "add-passkey") view = { ...view, screen: { kind: "choose" } };
    else if (action.kind === "add-password") view = { ...view, screen: { kind: "password" } };
    else if (action.kind === "replace" && method) view = { ...view, screen: method.kind === "password" ? { kind: "password", replacement: method } : { kind: "choose", replacement: method } };
    else if (action.kind === "remove" && method) view = { ...view, screen: { kind: "remove", method } };
    else view = { ...view, notice: "Design preview. This action requires a connected workspace." };
    render();
  }
  render();
  document.documentElement.dataset.previewState = `security-${figure}`;
}
