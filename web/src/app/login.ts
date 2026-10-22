// The sign-in screen, shared by the cold start (main.ts) and the
// mid-session expiry (app/live.ts). One module because the two rendered
// the same markup twice and drifted: only the sentence above the button
// differs, and that is a parameter, not a reason to copy a screen.
import { escapeHtml } from "../lib/escape.ts";
import { loginWithPassword, type AuthMode } from "../api/http.ts";
import { PRIMARY_MD_BUTTON } from "../ui/controls.ts";
import { passwordFieldsHtml, bindPasswordForm, setupFailure } from "./password-form.ts";
import * as hints from "../lib/hints.ts";

// LoginState is what the screen is showing.
//
// `denied` is built but NOT reachable from the server today, deliberately:
// internal/auth/oidc.go answers every failed check with a generic
// "login failed" 401 and logs the real reason server-side, so the browser
// cannot know an email was refused for being the wrong one. Rendering it
// would require the server to disclose whether a given address is the
// user's, changing the authentication disclosure contract.
export type LoginState = "signed-out" | "authorizing" | "denied" | "failed";

export interface LoginView {
  mode: AuthMode;
  state: LoginState;
  // reason is the sentence under the lockup. Empty renders nothing.
  reason?: string;
  // deniedEmail is shown only in the denied state.
  deniedEmail?: string;
  setup?: SetupView;
}

// SetupView is what the first-run page shows. "checking" offers no paste
// form while the link's own token is checked; "incomplete" is a pasted link
// that lost its token; a refusal is a check Deck turned away.
export type SetupView = "checking" | "ready" | "missing" | "incomplete" | SetupRefusal;

export interface SetupRefusal {
  status: number | null;
  retryAfter: string | null;
}

// googleMark is Google's own four-colour G. It is their trademark and is
// reproduced exactly; it never picks up a theme colour.
export function googleMark(): string {
  return `<svg width="16" height="16" viewBox="0 0 18 18" aria-hidden="true" class="block flex-none">
    <path fill="#4285F4" d="M17.64 9.2045c0-.6381-.0573-1.2518-.1636-1.8409H9v3.4814h4.8436c-.2086 1.125-.8427 2.0782-1.7959 2.7164v2.2581h2.9087c1.7018-1.5668 2.6836-3.874 2.6836-6.615z"/>
    <path fill="#34A853" d="M9 18c2.43 0 4.4673-.806 5.9564-2.1805l-2.9087-2.2581c-.8059.54-1.8368.859-3.0477.859-2.344 0-4.3282-1.5831-5.036-3.7104H.9574v2.3318C2.4382 15.9832 5.4818 18 9 18z"/>
    <path fill="#FBBC05" d="M3.964 10.71c-.18-.54-.2822-1.1168-.2822-1.71s.1023-1.17.2823-1.71V4.9582H.9573A8.9965 8.9965 0 0 0 0 9c0 1.4523.3477 2.8268.9573 4.0418L3.964 10.71z"/>
    <path fill="#EA4335" d="M9 3.5795c1.3214 0 2.5077.4541 3.4405 1.346l2.5813-2.5814C13.4632.8918 11.4259 0 9 0 5.4818 0 2.4382 2.0168.9573 4.9582L3.964 7.29C4.6718 5.1627 6.6559 3.5795 9 3.5795z"/>
  </svg>`;
}

function spinner(): string {
  return `<span aria-hidden="true"
    class="block h-3 w-3 flex-none animate-row-spinner rounded-circle border-[1.5px] border-text-tertiary border-t-transparent"></span>`;
}

// The lockup is two elements rather than one asset: the mark is inline
// SVG because its rows carry the authorizing sweep, and the wordmark
// stays an image because it never animates. Every row shares the same
// five centres on both axes; the radius grows 7.5 to 22.5 down the rows.
const NF_CENTRES = [22.5, 114.167, 205.833, 297.5, 389.167] as const;

const NF_MARK_ROWS = [7.5, 11.25, 15, 18.75, 22.5]
  .map((radius, row) => NF_CENTRES.map((cx) => `<circle cx="${cx}" cy="${NF_CENTRES[row]}" r="${radius}"/>`).join(""))
  .map((circles) => `<g class="nfrow">${circles}</g>`)

// data-sweep is absent at rest so the mark sits at full opacity; the
// nfsweep rules in app.css key off its exact value.
function lockup(sweeping: boolean): string {
  return `<div class="flex h-[26px] flex-none items-center gap-[8.6px]">
    <svg width="26" height="26" viewBox="0 0 411.667 411.667" aria-hidden="true" fill="#E8572A" class="nfmark block h-[26px] w-[26px] flex-none"${sweeping ? ' data-sweep="on"' : ""}>${NF_MARK_ROWS}</svg>
    <img src="/brand/noisefloor-wordmark.svg" alt="Noisefloor" width="94.3" height="26" draggable="false" class="block h-[26px] w-[94.3px] flex-none">
  </div>`;
}

// deniedPanel names the address that was refused. It is the one place
// this screen states a fact about an account rather than about the
// browser, so the address is escaped like any other untrusted string.
function deniedPanel(email: string): string {
  return `<div class="flex w-full flex-col gap-[5px] rounded-control border border-error px-3 py-2.5">
    <span class="flex items-center gap-[7px]">
      <span aria-hidden="true" class="h-1.5 w-1.5 flex-none rounded-circle bg-error"></span>
      <span class="text-body-sm font-medium text-error">Access denied</span>
    </span>
    <span class="text-body-sm leading-[1.45] text-text-secondary"><span class="text-text-primary">${escapeHtml(email)}</span> is not a member of this workspace. Ask an owner to add you, then sign in again.</span>
  </div>`;
}

function passwordForm(authorizing: boolean): string {
  return `<form data-login-password class="flex w-full flex-col gap-3">
    <label class="flex flex-col gap-1.5 text-body-sm text-text-secondary">
      Password
      <input data-login-password-input name="password" type="password" autocomplete="current-password"
        required maxlength="72" ${authorizing ? "disabled" : ""} data-hint="${hints.ENTER_PASSWORD}"
        class="h-9 w-full rounded-control border border-field-border bg-field px-3 text-body-sm text-text-primary outline-none focus:border-border-strong">
    </label>
    <button type="submit" ${authorizing ? "disabled" : ""} data-hint="${hints.SIGN_IN_WITH_PASSWORD}"
      class="flex h-9 w-full items-center justify-center gap-2 rounded-control bg-accent px-4 text-body-sm font-medium text-text-on-accent disabled:opacity-60">
      ${authorizing ? `${spinner()}<span>Signing in…</span>` : "Sign in"}
    </button>
  </form>`;
}

// loginHtml renders the whole screen. The caller owns the container.
export function loginHtml(view: LoginView): string {
  const reason = view.reason ?? "";
  const authorizing = view.state === "authorizing";
  // The sweep and the waiting announcement belong to the Google ceremony
  // only: the password form signals progress in its own submit button,
  // and a sweeping lockup above it would say the same thing twice. While
  // the ceremony is away the pending row keeps the button's h-9 geometry:
  // removing the row would shrink the card by the row plus its gap and
  // drag the sweeping mark down with it mid-ceremony.
  const away = authorizing && (view.mode === "oidc" || view.mode === "builtin");
  const body = view.setup !== undefined
    ? setupContent(view.setup)
    : view.mode === "password"
    ? passwordForm(authorizing)
    : view.mode === "oidc" || view.mode === "builtin"
    ? (away
      ? `<div data-login-pending class="flex h-9 w-full items-center justify-center">
          <span role="status" class="sr-only">Waiting for Google</span>
        </div>`
      : `<a data-login-google href="/auth/login" data-hint="${hints.SIGN_IN_WITH_GOOGLE}"
          class="flex h-9 w-full items-center justify-center gap-2.5 rounded-control border border-border bg-surface-card px-4 text-body-sm font-medium text-text-primary transition-colors hover:bg-wash-hover">
          ${googleMark()}<span>Sign in with Google</span>
        </a>`)
    // trusted_proxy has no ceremony to offer: the proxy authenticates and
    // this screen can only say so.
    : `<p class="text-body-sm text-text-secondary">Authenticate through the deployment proxy, then reload this page.</p>`;

  // data-theme rides on <html> (see main.ts), not on this element or the
  // workspace frame it replaces. Carrying it here would re-declare the dark
  // ramp on an inner element and freeze the composite tokens against the
  // light ramp — a white card and an inverse lockup invisible on it.
  return `
    <main class="login-field grid min-h-screen w-screen place-items-center px-6">
      <div aria-hidden="true" class="login-field-grid"></div>
      <div aria-hidden="true" class="login-field-veil"></div>
      <section class="relative flex w-[340px] max-w-full flex-col items-center gap-5 rounded-panel border border-border bg-surface-card px-7 pb-7 pt-8">
        ${lockup(away)}
        ${reason === "" ? "" : `<p class="text-body-sm text-text-tertiary">${escapeHtml(reason)}</p>`}
        ${view.state === "denied" ? deniedPanel(view.deniedEmail ?? "") : ""}
        ${view.state === "failed" ? `<p role="alert" class="text-body-sm text-error">Sign-in failed. Check the password and try again.</p>` : ""}
        ${body}
      </section>
    </main>
  `;
}

export function bindLoginActions(
  container: ParentNode,
  mode: AuthMode,
  onAuthenticated: () => void | Promise<void>,
): void {
  container.querySelector("[data-login-google]")?.addEventListener("click", () => {
    if (container instanceof HTMLElement) container.innerHTML = loginHtml({ mode, state: "authorizing" });
  });
  container.querySelector<HTMLFormElement>("[data-login-password]")?.addEventListener("submit", (event) => {
    event.preventDefault();
    const form = event.currentTarget as HTMLFormElement;
    const input = form.querySelector<HTMLInputElement>("[data-login-password-input]");
    if (input === null || input.value === "") return;
    const password = input.value;
    input.value = "";
    if (container instanceof HTMLElement) container.innerHTML = loginHtml({ mode, state: "authorizing" });
    void loginWithPassword(password).then(onAuthenticated).catch(() => {
      if (!(container instanceof HTMLElement)) return;
      container.innerHTML = loginHtml({ mode, state: "failed" });
      bindLoginActions(container, mode, onAuthenticated);
      container.querySelector<HTMLInputElement>("[data-login-password-input]")?.focus();
    });
  });
}

function setupContent(view: SetupView): string {
  const title = '<h1 class="text-title-3 font-medium text-text-primary">Create your password</h1>';
  if (view === "ready") {
    return `${title}<form data-password-editor class="flex w-full flex-col gap-3">
    ${passwordFieldsHtml(false)}
    <button type="submit" data-hint="${hints.CREATE_PASSWORD}" class="${PRIMARY_MD_BUTTON}">Create password</button>
    <p data-password-message role="status" class="text-body-sm text-error"></p>
  </form>`;
  }
  if (view === "checking") {
    return `${title}<p role="status" class="flex items-center justify-center gap-2 text-body-sm text-text-secondary">${spinner()}<span>Checking your setup link…</span></p>`;
  }
  const message = view === "missing"
    ? `<p class="text-center text-body-sm text-text-secondary">Paste your setup link or token to continue.</p>`
    : `<p role="alert" class="text-center text-body-sm text-error">${view === "incomplete"
      ? "That link is cut short. Copy the whole link, including everything after the # sign."
      : setupFailure(view.status, view.retryAfter)}</p>`;
  const renew = typeof view === "string" || view.status === 401
    ? `<div class="flex w-full flex-col gap-2">
    <p class="text-center text-body-sm text-text-tertiary">Deck prints a fresh link where the first one appeared: the startup output and the <code class="font-mono text-micro">setup-url</code> file. You can also ask the agent that installed Deck to show you the link again.</p>
    <p class="text-center text-body-sm text-text-tertiary">If Deck is stopped, run this on the host as the service user:</p>
    <pre class="w-full select-all overflow-x-auto rounded-control border border-border bg-surface-sunken p-2.5 text-center"><code class="font-mono text-micro text-text-primary">noise-deck password reset</code></pre>
  </div>`
    : "";
  return `${title}${message}
  <form data-setup-token class="flex w-full flex-col gap-3">
    <label class="flex flex-col gap-1.5 text-body-sm text-text-secondary">
      Setup link or token
      <input data-setup-token-input name="setup_token" type="password" autocomplete="off" spellcheck="false"
        autocapitalize="off" required data-hint="${hints.ENTER_SETUP_LINK}"
        class="h-9 w-full rounded-control border border-field-border bg-field px-3 text-body-sm text-text-primary outline-none focus:border-border-strong">
    </label>
    <button type="submit" data-hint="${hints.CHECK_SETUP_LINK}" class="${PRIMARY_MD_BUTTON}">Continue</button>
  </form>${renew}`;
}

export function setupHtml(view: SetupView): string {
  return loginHtml({ mode: "password", state: "signed-out", setup: view });
}

export function takeSetupToken(location: Pick<Location, "hash" | "search">): string {
  return new URLSearchParams(location.hash.slice(1)).get("token") ?? "";
}

// setupTokenFrom accepts a whole setup link or the bare token. Tokens are
// base64url, so a link that lost its fragment yields "" rather than a token.
export function setupTokenFrom(pasted: string): string {
  const value = pasted.replace(/\s+/g, "");
  const fragment = value.indexOf("#");
  if (fragment !== -1) return takeSetupToken({ hash: value.slice(fragment), search: "" });
  return value.includes("/") ? "" : value;
}

async function checkSetupToken(token: string): Promise<"valid" | "set" | SetupRefusal> {
  try {
    const response = await fetch("/auth/setup/check", {
      method: "POST", headers: { "Content-Type": "application/json" },
      credentials: "same-origin", cache: "no-store", body: JSON.stringify({ token }),
    });
    if (response.status === 404) return "set";
    if (response.ok) return "valid";
    return { status: response.status, retryAfter: response.headers.get("Retry-After") };
  } catch {
    return { status: null, retryAfter: null };
  }
}

// Every screen change bumps the generation, so a check that answers after
// the screen moved on is dropped.
let setupGeneration = 0;

// The fragment is stripped on mount, so a back-forward cache restore that
// remounts the page finds none; the link's token, or a pasted one that passed
// its check, is reused.
let heldSetupToken = "";

function showSetup(container: HTMLElement, view: SetupView, focus: boolean): void {
  setupGeneration++;
  container.innerHTML = setupHtml(view);
  const form = container.querySelector<HTMLFormElement>("[data-setup-token]");
  if (form === null) return;
  const input = form.querySelector<HTMLInputElement>("[data-setup-token-input]")!;
  form.addEventListener("submit", (event) => {
    event.preventDefault();
    const pasted = input.value;
    input.value = "";
    const token = setupTokenFrom(pasted);
    if (token === "") { showSetup(container, pasted.trim() === "" ? "missing" : "incomplete", true); return; }
    form.querySelector<HTMLButtonElement>('[type="submit"]')!.disabled = true;
    void openSetup(container, token, true);
  });
  if (focus) input.focus();
}

async function openSetup(container: HTMLElement, token: string, pasted: boolean): Promise<void> {
  const generation = ++setupGeneration;
  const result = await checkSetupToken(token);
  if (generation !== setupGeneration) return;
  if (result === "set") { window.location.replace("/"); return; }
  if (result !== "valid") { showSetup(container, result, pasted); return; }
  heldSetupToken = token;
  showSetup(container, "ready", false);
  bindPasswordForm(container, token, () => window.location.replace("/"));
  if (pasted) container.querySelector<HTMLInputElement>('input[name="password"]')?.focus();
}

export async function mountSetup(container: HTMLElement): Promise<void> {
  const token = takeSetupToken(window.location) || heldSetupToken;
  heldSetupToken = token;
  window.history.replaceState(null, "", "/setup");
  showSetup(container, token === "" ? "missing" : "checking", false);
  if (token !== "") await openSetup(container, token, false);
}
