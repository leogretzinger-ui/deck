import { applyHarnessLoginFlow, applyHarnessLoginTerminal, harnessLoginHtml, harnessLoginMessages } from "../app/harness-login.ts";
import { openModalScreen } from "../ui/modal-host.ts";

export const HARNESS_LOGIN_PREVIEW_STATES = ["pending", "submitting", "completed", "delivered", "invalid", "expired", "unavailable", "uncertain", "pi"] as const;
type PreviewState = typeof HARNESS_LOGIN_PREVIEW_STATES[number];

export function parseHarnessLoginPreview(params: URLSearchParams): PreviewState | null {
  const value = params.get("harness-login");
  if (value === null) return null;
  if (!(HARNESS_LOGIN_PREVIEW_STATES as readonly string[]).includes(value)) throw new Error("Invalid harness login preview state");
  if ([...params.keys()].some(key => key !== "harness-login" && key !== "theme")) throw new Error("Harness login preview cannot be combined with other fixture axes");
  return value as PreviewState;
}

export function mountHarnessLoginPreview(app: HTMLElement, state: PreviewState): void {
  app.style.cssText = "position:relative;height:100dvh;background:var(--color-surface)";
  app.dataset.harnessLoginPreview = state;
  const root = openModalScreen(app, { dismiss: () => {} }).element;
  root.innerHTML = harnessLoginHtml();
  applyHarnessLoginFlow(root, state === "pi" || state === "delivered" ? "pi-openai" : "codex-browser");
  root.querySelector<HTMLButtonElement>("[data-login-refresh]")!.hidden = true;
  const editing = state === "pending" || state === "invalid" || state === "pi";
  root.querySelector<HTMLFormElement>("form")!.hidden = !editing;
  root.querySelector<HTMLButtonElement>("[data-login-cancel]")!.hidden = state !== "pi";
  root.querySelector<HTMLButtonElement>("[data-login-submit]")!.disabled = true;
  root.querySelector("[data-login-status]")!.textContent = editing && state !== "invalid" ? "" : harnessLoginMessages[state as keyof typeof harnessLoginMessages];
  if (!editing && state !== "submitting") applyHarnessLoginTerminal(root, state === "completed");
  if (state === "invalid") root.querySelector("input")!.setAttribute("aria-invalid", "true");
  // A fixture has no backend and never sends the contents of its input.
  root.querySelector("form")!.addEventListener("submit", event => event.preventDefault());
  document.documentElement.dataset.previewState = `harness-login-${state}`;
}
