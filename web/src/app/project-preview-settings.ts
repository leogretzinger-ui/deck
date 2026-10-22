import { ApiError } from "../api/http.ts";
import type { ApiClient } from "../api/contracts.ts";
import type { ProjectPreviewCheck, ProjectPreviewSettings } from "../api/project-preview.ts";
import { activateDialog } from "../ui/dialog-focus.ts";
import { openModalScreen, type ModalScreen } from "../ui/modal-host.ts";
import { dialogHead, DIALOG_BODY, DIALOG_FOOT, PRIMARY_MD_BUTTON, SECONDARY_MD_BUTTON, GHOST_MD_BUTTON } from "../ui/controls.ts";
import { lucideIcon } from "../ui/icons.ts";
import { escapeHtml } from "../panels/sidebar/sidebar.ts";
import { openProjectPreview } from "./project-preview.ts";

export type PreviewPhase = "loading" | "loadfailed" | "unconfigured" | "static-only" | "enable" | "setup" | "settings" | "disableconfirm" | "disabled";
export interface PreviewIssue { title: string; detail: string; warning?: boolean }

export interface ProjectPreviewObservation {
  check: ProjectPreviewCheck | null;
  checkFailed: boolean;
}

// These are observations made in this browser, never saved project state.
export class ProjectPreviewObservations {
  private readonly values = new Map<string, { settings: ProjectPreviewSettings; observation: ProjectPreviewObservation }>();

  remember(projectID: string, settings: ProjectPreviewSettings | null, observation: ProjectPreviewObservation | null): void {
    if (!settings?.enabled || observation === null) {
      this.values.delete(projectID);
      return;
    }
    this.values.set(projectID, { settings: { ...settings }, observation });
  }

  get(projectID: string, settings: ProjectPreviewSettings): ProjectPreviewObservation | null {
    const previous = this.values.get(projectID);
    if (!settings.enabled || previous === undefined || previous.settings.origin !== settings.origin || previous.settings.port !== settings.port || previous.settings.source !== settings.source) {
      this.values.delete(projectID);
      return null;
    }
    return previous.observation;
  }
}

export class ProjectPreviewSettingsModel {
  settings: ProjectPreviewSettings | null = null;
  phase: PreviewPhase = "loading";
  busy: "enable" | "disable" | "check" | null = null;
  check: ProjectPreviewCheck | null = null;
  checkFailed = false;
  issue: PreviewIssue | null = null;
  detailsOpen = false;
  copied = false;
  private generation = 0;
  private disposed = false;
  onChange: () => void = () => {};
  onObservation: (observation: ProjectPreviewObservation | null) => void = () => {};

  readonly api: ApiClient;
  readonly projectID: string;
  readonly projectName: string;
  constructor(api: ApiClient, projectID: string, projectName: string) {
    this.api = api;
    this.projectID = projectID;
    this.projectName = projectName;
  }

  dispose(): void { this.disposed = true; this.generation++; }
  changed(): void { if (!this.disposed) this.onChange(); }
  private current(generation: number): boolean { return !this.disposed && this.generation === generation; }
  private phaseFor(settings: ProjectPreviewSettings): PreviewPhase {
    if (!settings.configured) return "unconfigured";
    if (settings.enabled) return "settings";
    return settings.self_service && settings.source !== "static" ? "enable" : "static-only";
  }

  async load(): Promise<void> {
    if (this.busy !== null || this.disposed) return;
    const generation = ++this.generation;
    this.phase = "loading";
    this.issue = null;
    this.changed();
    try {
      if (this.api.getProjectPreviewSettings === undefined) throw new Error("Preview settings are unavailable.");
      const settings = await this.api.getProjectPreviewSettings(this.projectID);
      if (!this.current(generation)) return;
      this.settings = settings;
      this.phase = this.phaseFor(settings);
      this.check = null;
      this.checkFailed = false;
    } catch {
      if (!this.current(generation)) return;
      this.phase = "loadfailed";
      this.issue = { title: "Couldn't load preview settings", detail: "Check your connection and try again." };
    }
    this.changed();
  }

  async change(enabled: boolean): Promise<void> {
    if (this.disposed || this.busy !== null || this.settings?.self_service !== true || this.settings.source === "static") return;
    const generation = ++this.generation;
    this.busy = enabled ? "enable" : "disable";
    this.check = null;
    this.checkFailed = false;
    this.onObservation(null);
    this.issue = null;
    this.changed();
    try {
      const method = enabled ? this.api.enableProjectPreview : this.api.disableProjectPreview;
      if (method === undefined) throw new Error("Preview settings are unavailable.");
      const settings = await method.call(this.api, this.projectID);
      if (!this.current(generation)) return;
      this.settings = settings;
      this.phase = settings.enabled ? (enabled ? "setup" : "settings") : (enabled ? this.phaseFor(settings) : "disabled");
      this.check = null;
      this.checkFailed = false;
    } catch (err) {
      if (!this.current(generation)) return;
      if (err instanceof ApiError && err.code === "preview_ports_exhausted") {
        this.issue = { title: "No dev-server ports are free", detail: "Ask the installation owner to expand the preview port range. Disabling a preview keeps its reserved port.", warning: true };
      } else {
        this.issue = {
          title: enabled ? "Enable outcome unconfirmed" : "Disable outcome unconfirmed",
          detail: enabled
            ? "The request did not complete successfully. Deck is checking the saved settings; a URL and port may already be assigned."
            : "The request did not complete successfully. Browser access may already be revoked, even if the saved settings still say enabled. Retry disabling to confirm it is saved.",
        };
        // A failed write can revoke the gateway before persistence fails. A
        // settings read also observes the gateway, so disabled cannot prove
        // that the database write succeeded. Keep the retry action available.
        try {
          const settings = await this.api.getProjectPreviewSettings?.(this.projectID);
          if (!this.current(generation)) return;
          if (settings !== undefined) {
            this.settings = settings;
            if (enabled) this.phase = this.phaseFor(settings);

          }
        } catch {
          if (!this.current(generation)) return;
        }
        if (enabled) this.issue.detail = "The request did not complete successfully. A URL and port may already be assigned. Refresh settings to confirm the saved choice.";
      }
    }
    if (!this.current(generation)) return;
    this.busy = null;
    this.changed();
  }

  async checkConnectivity(): Promise<void> {
    if (this.disposed || this.busy !== null || !this.settings?.enabled) return;
    const generation = ++this.generation;
    this.busy = "check";
    this.checkFailed = false;
    this.changed();
    try {
      if (this.api.checkProjectPreview === undefined) throw new Error("Connectivity checks are unavailable.");
      const result = await this.api.checkProjectPreview(this.projectID);
      if (!this.current(generation)) return;
      this.check = result;
    } catch {
      if (!this.current(generation)) return;
      this.checkFailed = true;
      this.check = null;
    }
    this.busy = null;
    this.onObservation({ check: this.check, checkFailed: this.checkFailed });
    this.changed();
  }
}

export function previewCheckedWhen(checkedAt: string, now = Date.now()): string {
  const time = Date.parse(checkedAt);
  if (!Number.isFinite(time)) return checkedAt;
  const seconds = Math.max(0, Math.floor((now - time) / 1000));
  if (seconds < 5) return "just now";
  if (seconds < 60) return `${seconds} s ago`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)} min ago`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)} h ago`;
  return `${new Date(time).toISOString().slice(0, 16).replace("T", " ")} UTC`;
}

export function previewConnectionLabel(model: Pick<ProjectPreviewSettingsModel, "busy" | "checkFailed" | "check">): string {
  if (model.busy === "check") return "Checking…";
  if (model.checkFailed) return "Check failed";
  if (model.check === null) return "Not checked yet";
  return model.check.reachable ? "Accepting connections" : "Not responding";
}

function button(action: string, label: string, kind: "primary" | "secondary" | "ghost" = "secondary", disabled = false): string {
  const classes = kind === "primary" ? PRIMARY_MD_BUTTON : kind === "ghost" ? GHOST_MD_BUTTON : SECONDARY_MD_BUTTON;
  return `<button type="button" data-preview-${action} ${disabled ? "disabled" : ""} class="${classes}">${label}</button>`;
}
function issueHtml(issue: PreviewIssue | null): string {
  if (issue === null) return "";
  return `<div role="alert" class="project-preview-issue ${issue.warning ? "project-preview-warning" : ""}"><span aria-hidden="true">${lucideIcon("triangle-alert", 15)}</span><span><strong>${escapeHtml(issue.title)}</strong><span>${escapeHtml(issue.detail)}</span></span></div>`;
}

export function projectPreviewSettingsHtml(model: ProjectPreviewSettingsModel): string {
  const { phase, settings, busy, issue } = model;
  const name = escapeHtml(model.projectName);
  const origin = escapeHtml(settings?.origin ?? "");
  const close = button("close", "Done", "primary");
  let title = "Preview settings", description = "", body = "", foot = close;
  if (phase === "loading" || phase === "loadfailed") {
    description = phase === "loading" ? "Loading the project's preview settings…" : "The current settings couldn't be confirmed.";
    body = phase === "loading" ? '<span class="project-preview-pending" role="status"><span class="project-preview-spinner" aria-hidden="true"></span>Loading…</span>' : issueHtml(issue);
    foot = button("close", "Cancel") + (phase === "loadfailed" ? button("refresh", "Try again", "primary") : "");
  } else if (phase === "unconfigured" || phase === "static-only") {
    title = phase === "unconfigured" ? "Previews aren't set up" : "Preview setup is managed by the installation owner";
    description = phase === "unconfigured"
      ? "The installation owner enables previews once. After that, any project can enable a preview from this menu without a restart."
      : "This installation uses manually registered previews. This project doesn't have an enabled preview.";
    body = '<p>The installation owner can enable self-service previews using the project preview operations guide.</p>';
  } else if (phase === "enable") {
    title = `Preview ${name}`;
    description = settings?.origin ? "Deck keeps this project's private URL and reserved dev-server port." : "Deck assigns a private URL and reserves a dev-server port for this project.";
    body = `<div class="project-preview-explainer"><span><strong>Only people signed in to this Deck installation can open it.</strong> Sharing the URL doesn't grant access.</span><span><strong>Nothing starts.</strong> The project declares its dev command once; when the agent launches it through Deck, the launcher supplies this assignment. Deck doesn't pick a framework.</span><span><strong>The server runs like any other process here.</strong> Enabling a preview doesn't confine it.</span></div>${busy === "enable" ? '<span class="project-preview-pending" role="status"><span class="project-preview-spinner" aria-hidden="true"></span>Assigning a URL and port…</span>' : ""}${issueHtml(issue)}`;
    foot = button("close", "Cancel", "secondary", busy !== null) + button("enable", busy === "enable" ? "Enabling…" : issue ? "Try again" : "Enable preview", "primary", busy !== null);
    if (issue && !issue.warning) foot = button("refresh", "Refresh settings") + foot;
  } else if (phase === "disableconfirm") {
    title = "Disable preview access?";
    description = "Browser access is revoked for everyone signed in, including you. The URL and port stay reserved.";
    body = `<span class="project-preview-address">${origin}</span><p>The dev server isn't stopped. If one is running, stop it from the project's session. Enabling this preview again uses the same URL and port.</p>${issueHtml(issue)}`;
    foot = button("back", "Cancel", "secondary", busy !== null) + button("disable", busy === "disable" ? "Disabling…" : issue ? "Try again" : "Disable access", "primary", busy !== null);
  } else if (phase === "disabled") {
    title = "Preview access disabled";
    description = `${name} keeps its URL and reserved port. Its menu shows Preview… again.`;
    body = `<p>If a dev server was running, it still is. Stop it from the project's session if you no longer need it.</p>${issueHtml(issue)}`;
    foot = button("reenable", "Enable preview…") + close;
  } else {
    title = phase === "setup" ? "Preview enabled" : "Preview settings";
    description = phase === "setup"
      ? `${name} has a stable private URL. The dev server starts from the project's session${model.check !== null || model.checkFailed ? "." : "; connectivity hasn't been checked."}`
      : `${name} · only people signed in to this Deck installation can open the URL.`;
    const checked = model.check?.checked_at;
    const lastCheck = checked === undefined ? "Not checked yet" : `${model.check?.reachable ? "Connection accepted" : "Connection unsuccessful"} · ${escapeHtml(previewCheckedWhen(checked))}`;
    body = `<div class="project-preview-card"><div class="project-preview-row"><span class="project-preview-label">URL</span><span class="project-preview-url">${origin}</span><button type="button" data-preview-copy class="project-preview-small-button">${model.copied ? "Copied" : "Copy URL"}</button></div><div class="project-preview-row project-preview-connection"><span class="project-preview-label">Dev server</span><span data-preview-connectivity role="status" class="project-preview-status"><span aria-hidden="true" class="${busy === "check" ? "project-preview-spinner" : `project-preview-dot ${model.check?.reachable && !model.checkFailed ? "project-preview-up" : ""}`}"></span><span>${previewConnectionLabel(model)}${checked && busy !== "check" && !model.checkFailed ? ` <span data-preview-age class="project-preview-age">· checked ${escapeHtml(previewCheckedWhen(checked))}</span>` : ""}</span></span><button type="button" data-preview-check class="project-preview-small-button" ${busy !== null ? "disabled" : ""}>Check connectivity</button></div><button type="button" data-preview-details aria-expanded="${model.detailsOpen}" class="project-preview-details-toggle"><span aria-hidden="true">${model.detailsOpen ? "▾" : "▸"}</span>Details</button>${model.detailsOpen ? `<div class="project-preview-details"><div><span class="project-preview-label">Port</span><span class="project-preview-port">${settings?.port ?? ""}</span></div><div><span class="project-preview-label">Last check</span><span data-preview-last-check>${model.checkFailed ? "Check failed" : lastCheck}</span></div><p>The check is a TCP connection to the reserved port. It shows something is listening, not that the app renders.</p></div>` : ""}</div>${model.checkFailed ? '<p class="project-preview-note" role="alert">Deck couldn’t complete the connectivity check. Try again.</p>' : ""}${settings?.source === "static" ? '<p class="project-preview-note">This preview is managed by the installation’s configuration. Contact the installation owner to change or disable it.</p>' : model.check?.reachable ? "" : '<p class="project-preview-note">Start the dev server from the project’s session. The agent launches the declared command through Deck and the launcher supplies this assignment.</p>'}${issueHtml(issue)}`;
    foot = (issue?.title === "Enable outcome unconfirmed" ? button("refresh", "Refresh settings") : "") + (phase === "settings" && settings?.source !== "static" && settings?.self_service ? button("confirm-disable", "Disable access…", "ghost", busy !== null) : "") + button("close", "Done") + button("open", 'Open preview <span aria-hidden="true">↗</span>', "primary");
  }
  let head = dialogHead(title, null, description).replace("<h2 ", '<h2 id="project-preview-title" ');
  if (phase === "disableconfirm") head = head.replace("</header>", `<span aria-hidden="true" class="project-preview-head-warning">${lucideIcon("triangle-alert", 18)}</span></header>`);
  return `<div data-project-preview-dialog data-preview-phase="${phase}" role="dialog" aria-modal="true" aria-labelledby="project-preview-title" class="dialog project-preview-dialog" tabindex="-1"><span class="project-preview-grabber" aria-hidden="true"></span>${head}${DIALOG_BODY(body)}${DIALOG_FOOT(foot)}</div>`;
}

export class ProjectPreviewSettingsDialog {
  readonly model: ProjectPreviewSettingsModel;
  private readonly host: HTMLElement;
  private readonly screen: ModalScreen;
  private teardown: (() => void) | null = null;
  private disposed = false;
  private readonly ageTimer: ReturnType<typeof setInterval>;
  private copyTimer: ReturnType<typeof setTimeout> | null = null;

  private readonly returnFocus: () => void;
  constructor(region: HTMLElement, api: ApiClient, projectID: string, projectName: string, returnFocus: () => void) {
    this.returnFocus = returnFocus;
    this.screen = openModalScreen(region, { dismiss: () => this.dismiss() });
    this.host = this.screen.element;
    this.model = new ProjectPreviewSettingsModel(api, projectID, projectName);
    this.model.onChange = () => this.render();
    this.ageTimer = setInterval(() => this.updateAge(), 1000);
    this.render();
  }

  close(restoreFocus = true): void {
    if (this.disposed) return;
    this.disposed = true;
    this.model.dispose();
    this.teardown?.();
    clearInterval(this.ageTimer);
    if (this.copyTimer !== null) clearTimeout(this.copyTimer);
    this.screen.close();
    if (restoreFocus) this.returnFocus();
  }

  private dismiss(): void {
    if (this.model.busy === "enable" || this.model.busy === "disable") return;
    if (this.model.phase === "disableconfirm") {
      this.model.phase = "settings";
      this.model.changed();
    } else this.close();
  }

  render(): void {
    if (this.disposed) return;
    const focused = document.activeElement instanceof HTMLButtonElement && this.host.contains(document.activeElement) ? document.activeElement.getAttributeNames().find((name) => name.startsWith("data-preview-")) : undefined;
    this.teardown?.();
    this.host.innerHTML = projectPreviewSettingsHtml(this.model);
    const dialog = this.host.querySelector<HTMLElement>("[data-project-preview-dialog]")!;
    this.teardown = activateDialog(dialog, () => this.dismiss(), focused ? `[${focused}]` : ":dialog");
    const bind = (action: string, fn: () => void): void => { this.host.querySelector(`[data-preview-${action}]`)?.addEventListener("click", fn); };
    bind("close", () => this.dismiss());
    bind("refresh", () => { void this.model.load(); });
    bind("enable", () => { void this.model.change(true); });
    bind("disable", () => { void this.model.change(false); });
    bind("check", () => { void this.model.checkConnectivity(); });
    bind("back", () => this.dismiss());
    bind("reenable", () => { this.model.phase = "enable"; this.model.issue = null; this.render(); });
    bind("confirm-disable", () => { this.model.phase = "disableconfirm"; this.model.issue = null; this.render(); });
    bind("details", () => { this.model.detailsOpen = !this.model.detailsOpen; this.render(); });
    bind("copy", () => { void this.copyURL(); });
    bind("open", () => openProjectPreview(this.model.projectID, this.model.api, (message) => {
      if (this.disposed) return;
      this.model.issue = { title: "Couldn't open preview", detail: message };
      this.render();
    }));
  }

  private updateAge(): void {
    const check = this.model.check;
    if (this.disposed || check === null || this.model.busy === "check") return;
    const age = previewCheckedWhen(check.checked_at);
    const status = this.host.querySelector("[data-preview-age]");
    if (status !== null) status.textContent = `· checked ${age}`;
    const details = this.host.querySelector("[data-preview-last-check]");
    if (details !== null) details.textContent = `${check.reachable ? "Connection accepted" : "Connection unsuccessful"} · ${age}`;
  }

  private async copyURL(): Promise<void> {
    const origin = this.model.settings?.origin;
    if (!origin) return;
    try {
      await navigator.clipboard.writeText(origin);
      if (this.disposed) return;
      this.model.copied = true;
      this.render();
      if (this.copyTimer !== null) clearTimeout(this.copyTimer);
      this.copyTimer = setTimeout(() => { this.model.copied = false; this.render(); }, 1800);
    } catch {
      if (this.disposed) return;
      this.model.issue = { title: "Couldn't copy the URL", detail: "Select and copy the URL above, or try again." };
      this.render();
    }
  }
}
