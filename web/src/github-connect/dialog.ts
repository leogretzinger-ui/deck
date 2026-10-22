import { AgentTerminal } from "../terminal/attach.ts";
import { createTerminal, ensureMonoFontLoaded } from "../terminal/term.ts";
import { terminalFontSize } from "../terminal/font-size.ts";
import { resolveTerminalTheme, terminalThemeName } from "../terminal/theme.ts";
import { activateDialog } from "../ui/dialog-focus.ts";
import { dialogHead, SECONDARY_MD_BUTTON } from "../ui/controls.ts";
import { openModalScreen, type ModalScreen } from "../ui/modal-host.ts";
import * as hints from "../lib/hints.ts";
import type { GitHubConnectClient } from "./client.ts";
import { GitHubConnectModel, connectScreen, connectedAccount, signedIn, type GitHubConnectModelOptions } from "./model.ts";
import { GITHUB_DEVICE_URL, githubFootHtml, githubScreenHtml } from "./view.ts";

export interface GitHubConnectDialogOptions extends GitHubConnectModelOptions {
  onClosed?: () => void;
  // Called after the dialog closes on a connected account, including one
  // connected outside Deck before the dialog opened.
  onConnected?: (login: string) => void;
  controllerID?: string;
}

export class GitHubConnectDialog {
  private host: HTMLElement;
  private options: GitHubConnectDialogOptions;
  private model: GitHubConnectModel;
  private root: HTMLElement | null = null;
  private screen: ModalScreen | null = null;
  private rendered = "";
  private terminal: AgentTerminal | null = null;
  private terminalID: string | null = null;
  private terminalMode: "connecting" | "connected" | "reconnect" | "takeover" = "connecting";
  private terminalMessage = "";
  private releaseFocus?: () => void;
  private opener: HTMLElement | null = null;
  private disposed = false;

  constructor(host: HTMLElement, client: GitHubConnectClient, options: GitHubConnectDialogOptions = {}) {
    this.host = host;
    this.options = options;
    this.model = new GitHubConnectModel(client, () => this.render(), options);
  }

  open(): void {
    if (this.disposed || this.root !== null) return;
    this.opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    this.screen = openModalScreen(this.host, { dismiss: () => this.leave() });
    this.root = this.screen.element;
    this.root.innerHTML = `<section data-github-connect-dialog role="dialog" aria-modal="true" aria-label="Connect GitHub" class="dialog" style="width:min(560px,100%);max-height:calc(100dvh - 24px)">
      ${dialogHead("Connect GitHub", { label: "Close", attr: "data-github-close" })}
      <div class="dialog-body overflow-y-auto">
        <div data-github-screen class="flex flex-col gap-3"></div>
        <details data-github-details hidden class="text-body-sm text-text-secondary">
          <summary data-hint="${hints.SHOW_GITHUB_SIGN_IN_DETAILS}" class="cursor-pointer select-none">Show details</summary>
          <div class="mt-3 flex flex-col gap-2">
            <div data-github-terminal style="height:min(36dvh,300px);min-height:180px;overflow:hidden"></div>
            <p data-github-terminal-status hidden role="status"></p>
            <div class="flex flex-wrap gap-2">
              <button type="button" data-github-reattach hidden data-hint="${hints.RECONNECT_GITHUB_SIGN_IN_TERMINAL}" class="${SECONDARY_MD_BUTTON}">Reconnect</button>
              <button type="button" data-github-takeover hidden data-hint="${hints.TAKE_CONTROL_OF_GITHUB_SIGN_IN}" class="${SECONDARY_MD_BUTTON}">Take control</button>
            </div>
          </div>
        </details>
      </div>
      <footer data-github-foot class="dialog-foot flex-wrap"></footer>
    </section>`;
    const dialog = this.root.querySelector<HTMLElement>("[role=dialog]")!;
    this.releaseFocus = activateDialog(dialog, () => this.leave(), ":dialog");
    this.root.querySelector("[data-github-close]")!.addEventListener("click", () => this.leave());
    dialog.addEventListener("click", event => this.act(event));
    this.root.querySelector("[data-github-details]")!.addEventListener("toggle", () => this.render());
    this.root.querySelector("[data-github-reattach]")!.addEventListener("click", () => this.attach(false));
    this.root.querySelector("[data-github-takeover]")!.addEventListener("click", () => this.attach(true));
    this.render();
    void this.model.open();
  }

  private act(event: Event): void {
    const target = event.target instanceof Element ? event.target.closest<HTMLElement>("button") : null;
    if (target === null || this.model.busy) return;
    if (target.matches("[data-github-copy]")) this.copyAndOpen();
    else if (target.matches("[data-github-cancel]")) void this.model.cancel();
    else if (target.matches("[data-github-done]")) this.leave();
    else if (target.matches("[data-github-retry]")) void this.model.retry();
  }

  // The tab opens inside the click so no popup blocker stops it. A refused
  // clipboard leaves the code selected for you to copy.
  private copyAndOpen(): void {
    const code = this.root?.querySelector<HTMLElement>("[data-github-code]");
    if (!code) return;
    const select = () => { const range = document.createRange(); range.selectNodeContents(code); getSelection()?.removeAllRanges(); getSelection()?.addRange(range); };
    try {
      void navigator.clipboard.writeText(code.textContent ?? "").catch(select);
    } catch {
      select();
    }
    window.open(GITHUB_DEVICE_URL, "_blank", "noopener,noreferrer");
  }

  private render(): void {
    if (this.disposed || this.root === null) return;
    const state = this.model.state;
    const login = signedIn(state);
    if (login !== null) {
      this.close(login);
      return;
    }
    const screen = connectScreen(state);
    const key = JSON.stringify(screen);
    if (key !== this.rendered) {
      this.rendered = key;
      const focused = this.root.contains(document.activeElement) && document.activeElement?.closest("[data-github-screen],[data-github-foot]") !== null;
      this.root.querySelector("[data-github-screen]")!.innerHTML = githubScreenHtml(screen);
      const foot = this.root.querySelector<HTMLElement>("[data-github-foot]")!;
      foot.innerHTML = githubFootHtml(screen);
      foot.hidden = foot.children.length === 0;
      if (focused) this.root.querySelector<HTMLElement>("[role=dialog]")!.focus({ preventScroll: true });
    }
    this.root.querySelectorAll<HTMLButtonElement>("[data-github-screen] button, [data-github-foot] button").forEach(button => { button.disabled = this.model.busy; });
    const running = screen.kind === "code" || (screen.kind === "starting" && screen.running);
    const details = this.root.querySelector<HTMLDetailsElement>("[data-github-details]")!;
    details.hidden = !running;
    if (!running || !details.open) this.disposeTerminal();
    else if (state.phase === "ready" && this.terminalID !== state.attempt!.id) this.mountTerminal(state.attempt!.id);
    this.renderTerminal();
  }

  private mountTerminal(id: string): void {
    this.disposeTerminal();
    if (this.root === null) return;
    this.terminalID = id;
    const container = this.root.querySelector<HTMLElement>("[data-github-terminal]")!;
    container.hidden = false;
    const current = () => !this.disposed && this.terminalID === id;
    const ended = (takeover: boolean, message: string) => {
      if (!current()) return;
      this.terminalMode = takeover ? "takeover" : "reconnect";
      this.terminalMessage = message;
      this.renderTerminal();
    };
    const fontSize = terminalFontSize();
    this.terminal = new AgentTerminal(container, {
      onAttached: () => {
        if (!current()) return;
        this.terminalMode = "connected";
        this.terminalMessage = "";
        this.renderTerminal();
      },
      onTakeoverRequired: () => ended(true, "Another browser controls this sign-in. Take control to continue here."),
      onFenced: () => ended(true, "Another browser took control of this sign-in."),
      onLeaseExpired: () => ended(false, "Terminal control expired. Reconnect to continue the same sign-in."),
      onDropped: () => ended(false, "The terminal disconnected. Reconnect to the same sign-in."),
      onError: () => ended(false, "The terminal is unavailable."),
      onClose: () => {
        if (current() && this.terminalMode !== "takeover") ended(false, "The terminal disconnected. Nothing was restarted.");
      },
      onWriteStatus: code => {
        if (code !== "sent" && code !== "dropped") ended(false, "Terminal input could not be confirmed. Check the native CLI before sending it again.");
      },
    }, {
      controllerID: this.options.controllerID,
      terminalPath: attempt => `/api/github/connect/attempt/${encodeURIComponent(attempt)}/terminal`,
      mountTerminal: host => createTerminal(host, resolveTerminalTheme(terminalThemeName()), fontSize),
    });
    void ensureMonoFontLoaded(container, fontSize).then(() => {
      if (current()) this.terminal?.setFontSize(fontSize);
    }).catch(() => {});
    this.attach(false);
  }

  private attach(takeover: boolean): void {
    if (this.disposed || this.model.state.phase !== "ready" || this.model.state.attempt?.status !== "running" || this.terminalID === null) return;
    this.terminalMode = "connecting";
    this.terminalMessage = "Connecting to the sign-in terminal…";
    this.renderTerminal();
    this.terminal?.attach(this.terminalID, { takeover });
  }

  private renderTerminal(): void {
    if (this.disposed || this.root === null) return;
    const allowed = this.model.state.phase === "ready" && this.model.state.attempt?.status === "running";
    const message = this.root.querySelector<HTMLElement>("[data-github-terminal-status]")!;
    message.textContent = this.terminalMessage;
    message.hidden = this.terminalMessage === "";
    this.root.querySelector<HTMLButtonElement>("[data-github-reattach]")!.hidden = !allowed || this.terminalMode !== "reconnect";
    this.root.querySelector<HTMLButtonElement>("[data-github-takeover]")!.hidden = !allowed || this.terminalMode !== "takeover";
  }

  private disposeTerminal(): void {
    this.terminalID = null;
    this.terminal?.dispose();
    this.terminal = null;
    this.terminalMessage = "";
  }

  dispose(): void {
    this.close(null);
  }

  private leave(): void {
    this.close(connectedAccount(this.model.state));
  }

  private close(login: string | null): void {
    if (this.disposed) return;
    this.disposed = true;
    this.model.dispose();
    this.disposeTerminal();
    this.releaseFocus?.();
    this.screen?.close();
    this.screen = null;
    this.root = null;
    if (this.opener?.isConnected) this.opener.focus({ preventScroll: true });
    this.options.onClosed?.();
    if (login !== null) this.options.onConnected?.(login);
  }
}
