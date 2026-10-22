import { ApiError, csrfToken, readJSON } from "../api/http.ts";
import { activateDialog } from "../ui/dialog-focus.ts";
import { dialogHead, PRIMARY_MD_BUTTON } from "../ui/controls.ts";
import { openModalScreen } from "../ui/modal-host.ts";
import * as hints from "../lib/hints.ts";

const MIN_PASSWORD_LENGTH = 8;
const PASSWORD_TOO_SHORT = `Use at least ${MIN_PASSWORD_LENGTH} characters for your password.`;

function passwordInput(name: string, label: string, autocomplete: string): string {
  const minlength = name === "current_password" ? "" : ` minlength="${MIN_PASSWORD_LENGTH}"`;
  const rule = name === "password";
  const field = `<label class="flex flex-col gap-1.5 text-body-sm text-text-secondary">${label}
    <input name="${name}" type="password" autocomplete="${autocomplete}" required${minlength} maxlength="72"${rule ? ' aria-describedby="password-rule"' : ""}
      data-hint="${name === "confirm_password" ? hints.CONFIRM_NEW_PASSWORD : name === "current_password" ? hints.ENTER_CURRENT_PASSWORD : hints.ENTER_NEW_PASSWORD}"
      class="h-9 w-full rounded-control border border-field-border bg-field px-3 text-body-sm text-text-primary outline-none focus:border-border-strong">
  </label>`;
  return rule
    ? `<div class="flex flex-col gap-1.5">${field}
    <p id="password-rule" class="text-body-sm text-text-tertiary">Use at least ${MIN_PASSWORD_LENGTH} characters.</p>
  </div>`
    : field;
}

export function passwordFieldsHtml(change: boolean): string {
  return `${change ? passwordInput("current_password", "Current password", "current-password") : ""}
    ${passwordInput("password", "New password", "new-password")}
    ${passwordInput("confirm_password", "Confirm new password", "new-password")}`;
}

// setupFailure says why Deck refused a setup request. A null status means no
// answer arrived; the request may still have been applied, so reloading shows
// where things stand.
export function setupFailure(status: number | null, retryAfter: string | null): string {
  if (status === null) return "Deck did not answer. Check your connection, then reload this page.";
  if (status === 401) return "This link is invalid or expired. Ask whoever installed Deck for a new one.";
  if (status === 403) return "Deck does not accept this address. Open Deck at the address the installer gave you.";
  if (status === 400) return PASSWORD_TOO_SHORT;
  if (status === 429) {
    const seconds = Number.parseInt(retryAfter ?? "", 10);
    return seconds > 0
      ? `Too many attempts. Try again in ${seconds} ${seconds === 1 ? "second" : "seconds"}.`
      : "Too many attempts. Try again in a minute.";
  }
  return "Deck could not finish this step. Try again.";
}

export function bindPasswordForm(container: HTMLElement, token: string | null, onSaved: () => void): void {
  const form = container.querySelector<HTMLFormElement>("[data-password-editor]");
  form?.addEventListener("submit", (event) => {
    event.preventDefault();
    const input = (name: string) => form.elements.namedItem(name) as HTMLInputElement;
    const password = input("password").value;
    const message = form.querySelector<HTMLElement>("[data-password-message]")!;
    if ([...password].length < MIN_PASSWORD_LENGTH) { message.textContent = PASSWORD_TOO_SHORT; return; }
    if (password !== input("confirm_password").value) { message.textContent = "Your new passwords do not match."; return; }
    if (new TextEncoder().encode(password).length > 72) { message.textContent = "Use a shorter password."; return; }
    const body = token === null ? { current_password: input("current_password").value, password } : { token, password };
    form.querySelectorAll<HTMLInputElement>("input").forEach((field) => { field.value = ""; });
    const submit = form.querySelector<HTMLButtonElement>('[type="submit"]')!;
    submit.disabled = true;
    message.textContent = "Saving your password…";
    const headers: Record<string, string> = { "Content-Type": "application/json" };
    const csrf = csrfToken(document.cookie);
    if (csrf !== null) headers["X-CSRF-Token"] = csrf;
    let retryAfter: string | null = null;
    void fetch(token === null ? "/auth/password/change" : "/auth/setup", {
      method: "POST", credentials: "same-origin", cache: "no-store", headers, body: JSON.stringify(body),
    }).then((response) => {
      retryAfter = response.headers.get("Retry-After");
      return readJSON<void>(response);
    }).then(() => { if (form.isConnected) onSaved(); }).catch((cause: unknown) => {
      if (!form.isConnected) return;
      const status = cause instanceof ApiError ? cause.status : null;
      message.textContent = token !== null
        ? setupFailure(status, retryAfter)
        : status === 400
        ? PASSWORD_TOO_SHORT
        : status === 409
        ? "Remove the password hash from your service configuration. Restart Deck before changing your password."
        : "Password change failed. Check your current password and try again.";
      submit.disabled = false;
    });
  });
}

export function passwordChangeHtml(): string {
  return `<section role="dialog" aria-modal="true" aria-label="Change password" class="dialog m-auto w-[340px] max-w-full">
    ${dialogHead("Change password", { label: "Close", attr: "data-password-close" })}
    <form data-password-editor class="dialog-body flex flex-col gap-3">
      ${passwordFieldsHtml(true)}
      <p data-password-message role="status" class="text-body-sm text-error"></p>
      <button type="submit" data-hint="${hints.SAVE_PASSWORD}" class="${PRIMARY_MD_BUTTON}">Change password</button>
    </form>
  </section>`;
}

export function openPasswordChange(region: HTMLElement, preview = false): void {
  let deactivate = () => {};
  const close = () => { deactivate(); screen.close(); };
  const screen = openModalScreen(region, { dismiss: close });
  const root = screen.element;
  root.innerHTML = passwordChangeHtml();
  root.querySelector("[data-password-close]")?.addEventListener("click", close);
  deactivate = activateDialog(root.querySelector<HTMLElement>('[role="dialog"]')!, close);
  if (preview) {
    root.querySelector("form")?.addEventListener("submit", event => {
      event.preventDefault();
      root.querySelector<HTMLElement>("[data-password-message]")!.textContent = "Preview only. Your password has not changed.";
    });
    return;
  }
  bindPasswordForm(root, null, () => {
    const form = root.querySelector<HTMLFormElement>("form");
    if (form !== null) form.innerHTML = '<p role="status" class="text-body-sm text-text-secondary">Your password has been changed.</p>';
  });
}
