import { validateNewEncryptionPassword } from "../../security/envelope.ts";

export type ManagementAction =
  | { kind: "close" | "open-workspace" | "review-methods" | "cancel" | "add" | "add-passkey" | "add-password" | "add-recovery" | "check-passkey" | "confirm-remove" | "activate" | "copy-key" | "download-kit" | "print-kit" | "saved-key" | "observe" }
  | { kind: "detail" | "test" | "replace" | "remove" | "use-method"; id: string }
  | { kind: "password"; password: string; current?: string }
  | { kind: "credential" | "candidate"; value: string }
  | { kind: "name"; value: string }
  | { kind: "import-kit"; file: File };

const plain = new Set(["close", "open-workspace", "review-methods", "cancel", "add", "add-passkey", "add-password", "add-recovery", "check-passkey", "confirm-remove", "activate", "copy-key", "download-kit", "print-kit", "saved-key", "observe"]);
const identified = new Set(["detail", "test", "replace", "remove", "use-method"]);

export function bindManagementActions(root: HTMLElement, run: (action: ManagementAction) => Promise<void>, error: (message: string) => void): () => void {
  const lifetime = new AbortController();
  const options = { signal: lifetime.signal };
  const dispatch = (action: ManagementAction): void => { void run(action).catch(() => error("The request did not finish. Check the workspace state before trying again.")); };
  root.addEventListener("click", event => {
    const target = event.target instanceof Element ? event.target.closest<HTMLButtonElement>("[data-method-action]") : null;
    if (!target || target.disabled || target.closest("[inert]")) return;
    const kind = target.dataset.methodAction ?? "";
    if (kind === "import-kit") { root.querySelector<HTMLInputElement>("[data-method-kit-file]")?.click(); return; }
    if (plain.has(kind)) dispatch({ kind } as ManagementAction);
    else if (identified.has(kind) && target.dataset.methodId) dispatch({ kind, id: target.dataset.methodId } as ManagementAction);
  }, options);
  root.addEventListener("submit", event => {
    if (!(event.target instanceof HTMLFormElement)) return;
    event.preventDefault();
    if (event.target.closest("[inert]")) return;
    const form = event.target;
    const data = new FormData(form);
    if (form.dataset.methodForm === "password") {
      const password = String(data.get("password") ?? "");
      try { validateNewEncryptionPassword(password); }
      catch { error("Use an encryption password of at least 15 characters, without invalid Unicode."); return; }
      const current = data.has("current") ? String(data.get("current")) : undefined;
      form.reset();
      dispatch({ kind: "password", password, current });
    } else if (form.dataset.methodForm === "name") {
      const value = String(data.get("name") ?? "").trim();
      if (!value || new TextEncoder().encode(value).length > 128 || /[\x00\r\n]/.test(value)) { error("Use a short name for this method."); return; }
      dispatch({ kind: "name", value });
    } else if (form.dataset.methodForm === "authorize" || form.dataset.methodForm === "candidate") {
      const value = String(data.get("current") ?? data.get("password") ?? data.get("recovery") ?? "");
      form.reset();
      dispatch({ kind: form.dataset.methodForm === "authorize" ? "credential" : "candidate", value });
    }
  }, options);
  root.addEventListener("input", event => {
    if (!(event.target instanceof HTMLInputElement) || event.target.name !== "password") return;
    let valid = true;
    try { validateNewEncryptionPassword(event.target.value); } catch { valid = false; }
    const meter = root.querySelector<HTMLElement>(".enroll-length-meter");
    if (meter) meter.dataset.longEnough = String(valid);
  }, options);
  root.addEventListener("keydown", event => {
    const dialog = root.querySelector<HTMLElement>('[role="dialog"]');
    if (!dialog || event.defaultPrevented) return;
    if (event.key === "Escape") { event.preventDefault(); dispatch({ kind: "cancel" }); return; }
    if (event.key !== "Tab") return;
    const controls = [...dialog.querySelectorAll<HTMLElement>('button:not(:disabled), input:not(:disabled), textarea:not(:disabled), a[href], [tabindex="0"]')].filter(element => !element.closest("[inert]") && element.getClientRects().length > 0);
    const first = controls[0], last = controls.at(-1);
    if (!first || !last) { event.preventDefault(); dialog.querySelector<HTMLElement>("h1")?.focus(); return; }
    if (event.shiftKey && (document.activeElement === first || !controls.includes(document.activeElement as HTMLElement))) { event.preventDefault(); last.focus(); }
    else if (!event.shiftKey && (document.activeElement === last || !controls.includes(document.activeElement as HTMLElement))) { event.preventDefault(); first.focus(); }
  }, options);
  root.addEventListener("change", event => {
    if (!(event.target instanceof HTMLInputElement) || !event.target.matches("[data-method-kit-file]")) return;
    const file = event.target.files?.[0];
    event.target.value = "";
    if (file) dispatch({ kind: "import-kit", file });
  }, options);
  return () => lifetime.abort();
}
