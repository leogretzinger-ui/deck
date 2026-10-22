import { runtimeHtml, bindRuntimeActions, type RuntimeView } from "../app/security/runtime.ts";

export const RUNTIME_PREVIEW_FIGURES = ["4a", "4b", "4c", "4e", "4f", "4g", "4h", "4i", "4j", "4k", "4l", "4m", "4n", "5b", "5c", "5d", "5e", "5f", "5m", "7a", "7c", "7d", "7m"] as const;
export type RuntimePreviewFigure = typeof RUNTIME_PREVIEW_FIGURES[number];
// Synthetic observations exercise optional layouts, never the live authority boundary.
const sessions = [
  { name: "rewrite-scheduler", command: "pnpm test", age: "40s", state: "running" as const },
  { name: "flaky-test-hunt", command: "claude", age: "11m", state: "running" as const },
];
const fixtures: Record<RuntimePreviewFigure, Partial<RuntimeView> & Pick<RuntimeView, "screen">> = {
  "4a": { screen: { kind: "signed-out" } },
  "4b": { screen: { kind: "locked" }, email: "owner@fablework.example" },
  "4c": { screen: { kind: "progress", operation: "unlock", storage: "open", runtime: "starting" } },
  "4e": { screen: { kind: "locked", cancelled: true }, methods: ["passkey", "recovery"] },
  "4f": { screen: { kind: "incompatible" } },
  "4g": { screen: { kind: "unavailable" }, lastReached: "yesterday, 19:41" },
  "4h": { screen: { kind: "unlock-failed" } },
  "4i": { screen: { kind: "locked", restarted: { at: "09:14", stopped: 2 } }, methods: ["passkey"] },
  "4j": { screen: { kind: "authorize-browser" } },
  "4k": { screen: { kind: "password", context: "unlock" } },
  "4l": { screen: { kind: "password", context: "unlock", wrong: true } },
  "4m": { screen: { kind: "locked" }, email: "owner@fablework.example" },
  "4n": { screen: { kind: "unlock-failed" } },
  "5b": { screen: { kind: "lock-confirm" }, sessions },
  "5c": { screen: { kind: "progress", operation: "lock", storage: "open", runtime: "stopping" }, sessions: sessions.map((s, i) => ({ ...s, state: i ? "stopping" : "stopped" })) },
  "5d": { screen: { kind: "locked", confirmed: true }, email: "owner@fablework.example", lockReceipt: { at: "16:41", stopped: 2 } },
  "5e": { screen: { kind: "lock-failed" }, sessions: sessions.map((s, i) => ({ ...s, state: i ? "unconfirmed" : "stopped" })) },
  "5f": { screen: { kind: "unknown", operation: "lock" } },
  "5m": { screen: { kind: "lock-confirm" }, sessions },
  "7a": { screen: { kind: "recovery", context: "unlock" } },
  "7c": { screen: { kind: "help" } },
  "7d": { screen: { kind: "lost-methods" } },
  "7m": { screen: { kind: "recovery", context: "unlock" } },
};
export function mountRuntimePreview(root: HTMLElement, figure: RuntimePreviewFigure): void {
  document.documentElement.dataset.securityPreview = "true";
  root.dataset.securityFigure = figure;
  let view: RuntimeView = { name: "atlas-7", address: "atlas-7.deck.example", methods: ["passkey", "password", "recovery"], ...fixtures[figure] };
  let unbind: (() => void) | undefined;
  function render(): void {
    unbind?.(); root.innerHTML = runtimeHtml(view);
    unbind = bindRuntimeActions(root, async action => {
      if (action.kind === "help" || action.kind === "lost-methods") view = { ...view, screen: { kind: action.kind } };
      else view = { ...view, notice: "Design preview. This action requires a connected workspace." };
      render();
    }, error => { view = { ...view, error: error instanceof Error ? error.message : String(error) }; render(); });
    const password = root.querySelector<HTMLInputElement>('input[name="password"]');
    if (password) password.value = "preview password";
    if (figure === "7a" && view.screen.kind === "recovery") root.querySelector<HTMLTextAreaElement>('textarea')!.value = "DK1-7QH2-KMD4-9XTF-B3PV-R6NC-J5LW-2SYE-8AGK-D4MU-X9TB-Q7HZ-3FRN-W5JC-5K2A";
  }
  render(); document.documentElement.dataset.previewState = `security-${figure}`;
}
