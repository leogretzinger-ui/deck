import { validateNewEncryptionPassword } from "../../security/envelope.ts";

export type EnrollmentAction =
  | { kind: "toggle-boundary" | "google-sign-in" | "create-passkey" | "cancel-passkey" | "choose-password" | "copy-key" | "download-kit" | "print-kit" | "verify-recovery" | "show-recovery" | "resume-recovery" | "copy-address" | "sign-out" | "open-workspace" | "add-method" | "check-state" }
  | { kind: "set-password"; password: string }
  | { kind: "name-method"; name: string }
  | { kind: "check-recovery"; key: string }
  | { kind: "import-kit"; file: File };

export interface EnrollmentActions {
  run(action: EnrollmentAction): Promise<void>;
  error(message: string): void;
}

const commands = new Set(["toggle-boundary", "google-sign-in", "create-passkey", "cancel-passkey", "choose-password", "copy-key", "download-kit", "print-kit", "verify-recovery", "show-recovery", "resume-recovery", "copy-address", "sign-out", "open-workspace", "add-method", "check-state"]);

// This binder reads user gestures only. The controller decides which result,
// if any, advances enrollment; a click never enables a credential by itself.
export function bindEnrollmentActions(root: HTMLElement, actions: EnrollmentActions): () => void {
  const lifetime = new AbortController();
  const options = { signal: lifetime.signal };
  const run = (action: EnrollmentAction): void => {
    void actions.run(action).catch(error => actions.error(error instanceof Error ? error.message : "The request did not finish. Try again."));
  };
  root.addEventListener("click", event => {
    const target = event.target instanceof Element ? event.target.closest<HTMLButtonElement>("[data-enroll-action]") : null;
    if (!target || target.disabled) return;
    const command = target.dataset.enrollAction;
    if (command === "import-kit") { root.querySelector<HTMLInputElement>("[data-enroll-kit-file]")?.click(); return; }
    if (command && commands.has(command)) run({ kind: command } as EnrollmentAction);
  }, options);
  root.addEventListener("input", event => {
    const target = event.target;
    if (!(target instanceof HTMLInputElement || target instanceof HTMLTextAreaElement)) return;
    if (target.name === "recovery") {
      const submit = target.form?.querySelector<HTMLButtonElement>('button[type="submit"]');
      if (submit) submit.disabled = target.value.trim().length === 0;
      const groups = target.closest(".enroll-group-editor")?.querySelector(".enroll-group-preview");
      if (groups) groups.replaceChildren(...target.value.trim().split(/[\s-]+/).map(value => {
        const group = document.createElement("span");
        group.textContent = value;
        if (value.length !== 4 && value.toUpperCase() !== "DK1") group.className = "enroll-malformed-group";
        return group;
      }));
    }
    if (target.name === "password") {
      let valid = true;
      try { validateNewEncryptionPassword(target.value); } catch { valid = false; }
      const meter = root.querySelector<HTMLElement>(".enroll-length-meter");
      if (meter) meter.dataset.longEnough = String(valid);
      const hint = root.querySelector("#enroll-password-hint");
      if (hint) hint.textContent = `${valid ? "Long enough. " : ""}At least 15 characters; a few unrelated words beat one clever one.`;
    }
  }, options);
  root.addEventListener("submit", event => {
    if (!(event.target instanceof HTMLFormElement)) return;
    event.preventDefault();
    const form = event.target;
    const values = new FormData(form);
    switch (form.dataset.enrollForm) {
      case "password": {
        const password = String(values.get("password") ?? "");
        const repeated = String(values.get("repeat") ?? "");
        try {
          validateNewEncryptionPassword(password);
          if (password.normalize("NFC") !== repeated.normalize("NFC")) throw new Error("The passwords do not match. Type the same password twice.");
          form.reset();
          run({ kind: "set-password", password });
        } catch (error) { actions.error(error instanceof Error ? error.message : "Check the password."); }
        break;
      }
      case "method-name": run({ kind: "name-method", name: String(values.get("methodName") ?? "").trim() }); break;
      case "recovery": run({ kind: "check-recovery", key: String(values.get("recovery") ?? "") }); break;
    }
  }, options);
  root.addEventListener("change", event => {
    if (!(event.target instanceof HTMLInputElement) || !event.target.matches("[data-enroll-kit-file]")) return;
    const file = event.target.files?.[0];
    event.target.value = "";
    if (file) run({ kind: "import-kit", file });
  }, options);
  root.addEventListener("dragover", event => {
    if (root.querySelector('[data-enroll-form="recovery"]')) event.preventDefault();
  }, options);
  root.addEventListener("drop", event => {
    if (!root.querySelector('[data-enroll-form="recovery"]')) return;
    event.preventDefault();
    const file = event.dataTransfer?.files[0];
    if (file) run({ kind: "import-kit", file });
  }, options);
  return () => lifetime.abort();
}
