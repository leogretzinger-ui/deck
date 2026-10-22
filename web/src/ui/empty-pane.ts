// The session pane's empty states and its loading
// states. `no-session` is a project in
// context with nothing open; `no-project` is a console whose registry read
// succeeded and found nothing. Both render only after the server has
// answered: until then the pane is `loading` — a spinner and a line saying
// what is happening, never an empty state — and after a failed read it is
// `error`, which says the sessions are still on the server and keeps
// retrying. A failed read is not an empty database, so it never borrows
// the no-project copy or its actions.
// The settled states share one anatomy — icon, small-caps title, one
// short body line, one primary action — so they read as one family, and
// all sit on the page surface rather than the terminal ground: this is
// the app speaking, not output from a session that is not running, so
// nothing here is mono.
import { escapeHtml } from "../lib/escape.ts";
import { lucideIcon, type IconName } from "./icons.ts";
import { panelTriggerHtml, panelsBadgeHtml, panelsLabel, type PanelTriggerSurface } from "./panel-trigger.ts";
import { projectIdentityHtml } from "./terminal-identity.ts";
import * as hints from "../lib/hints.ts";

export type EmptyPane = "off" | "no-session" | "no-project" | "loading" | "error";

// The loading pane's two phases: the first
// two seconds say only that the page is reconnecting; past that it says the
// wait is the runtime's, and that nothing has been closed.
export type LoadingPhase = "reconnect" | "slow";

// The empty titlebar's right side: the two docked-panel triggers and the
// account menu, in the exact classes the session chrome uses, so an empty
// pane never dresses itself in controls a session would not carry.
export function titlebarRightHtml(
  surface: PanelTriggerSurface,
  options: {
    panels: boolean;
    filesPressed?: boolean;
    tasksPressed?: boolean;
    sessions?: boolean;
    // chats renders the Chats trigger: only a session has an agent to
    // watch, so the empty pane never carries it.
    chats?: { pressed?: boolean; unseen?: boolean; hint?: string };
    // assets renders the Assets trigger after Chats; count is how many
    // files are new since the panel was last open.
    assets?: { pressed?: boolean; count?: number };
  },
): string {
  const more = surface === "live" ? "data-live-more" : "data-titlebar-more";
  const anchor = surface === "live" ? "data-live-more-anchor" : "data-titlebar-more-anchor";
  const sessions = options.panels && options.sessions
    ? `<button data-${surface}-sessions aria-label="Sessions" data-hint="${hints.OPEN_SESSION_LIST}"
        class="grid h-7 w-7 flex-none place-items-center rounded-control-sm text-text-secondary transition-colors hover:bg-field hover:text-text-primary md:hidden max-md:h-11 max-md:w-11">
        ${lucideIcon("layers", 18)}
      </button>`
    : "";
  const panels = options.panels
    ? `<span data-desktop-panels class="inline-flex items-center gap-(--space-4)">${panelTriggerHtml(surface, "files", { pressed: options.filesPressed })}
      ${panelTriggerHtml(surface, "tasks", { pressed: options.tasksPressed })}
      ${options.chats ? panelTriggerHtml(surface, "chats", options.chats) : ""}
      ${options.assets ? panelTriggerHtml(surface, "assets", { ...options.assets, count: options.assets.count ?? 0 }) : ""}</span>
      <button data-${surface}-panels data-mobile-panels data-hint="${options.chats ? hints.OPEN_PANELS : hints.OPEN_PROJECT_PANELS}" aria-label="${panelsLabel(options.assets?.count ?? 0)}" aria-haspopup="menu" aria-expanded="false" data-unseen="${options.chats?.unseen ?? false}"${options.assets ? ` data-assets-new="${options.assets.count ?? 0}"` : ""} class="relative grid h-11 w-11 flex-none place-items-center rounded-control-sm text-text-secondary hover:bg-field">${lucideIcon("layout-panel-left", 18)}${options.assets ? panelsBadgeHtml(options.assets.count ?? 0) : ""}</button>`
    : "";
  // The shared cluster owns spacing and the titlebar's right edge.
  return `<span data-titlebar-actions class="ml-auto inline-flex flex-none items-center gap-(--space-4)">${sessions}${panels}
    <span class="relative inline-flex" ${anchor}>
      <button ${more} aria-label="Settings" data-hint="${hints.OPEN_TERMINAL_IDENTITY_ACCOUNT_SETTINGS}"
        class="grid h-7 w-7 flex-none place-items-center rounded-control-sm text-text-secondary transition-colors hover:bg-field hover:text-text-primary max-md:h-11 max-md:w-11">${settingsGlyph()}</button>
    </span></span>`;
}

// The titlebar an empty pane carries. `no-session` keeps the session
// chrome's whole shape with the project in the identity slot; every
// other state sheds the identity AND the panel triggers — Files and
// Tasks have no readable project to draw from, and a populated chrome
// above the pane would promise panels that cannot read anything. Only
// the account menu stays.
export function emptyTitlebarHtml(
  surface: PanelTriggerSurface,
  state: {
    kind: Exclude<EmptyPane, "off">;
    project: { name: string; tint: string | null } | null;
    filesPressed?: boolean;
    tasksPressed?: boolean;
    sessions?: boolean;
  },
): string {
  const identity = state.kind === "no-session" && state.project !== null
    ? projectIdentityHtml(state.project, state.project.tint)
    : "";
  return `${identity}
    ${titlebarRightHtml(surface, {
      panels: state.kind === "no-session",
      filesPressed: state.filesPressed,
      tasksPressed: state.tasksPressed,
      sessions: state.sessions ?? (state.kind === "no-session"),
    })}`;
}

export function emptyPaneHtml(state: {
  kind: Exclude<EmptyPane, "off">;
  projectName: string | null;
  // error is the server's own message: the `error` state shows it under
  // its reassurance, so the failure is on the page without becoming the
  // page's explanation.
  error?: string;
  phase?: LoadingPhase;
  // session names the row a cold load is reattaching to, from the
  // remembered selection and the cached list; null reconnects the list.
  session?: string | null;
  // cached says the rail beside the pane is the previous visit's list, so
  // the slow phase can say whose sessions those are.
  cached?: boolean;
}): string {
  if (state.kind === "loading") return loadingPaneHtml(state.phase ?? "reconnect", state.session ?? null, state.cached === true);
  const title = state.kind === "no-session"
    ? "No session open"
    : state.kind === "error" ? "Can't reach the runtime" : "No projects yet";
  const icon: IconName = state.kind === "no-session"
    ? "layers"
    : state.kind === "error" ? "triangle-alert" : "folder";
  const iconTone = state.kind === "error" ? "text-warning" : "text-text-tertiary";
  const action = state.kind === "no-session" ? "New session" : state.kind === "error" ? "Retry now" : "Add project";
  const actionHint = state.kind === "no-session" && state.projectName !== null
    ? hints.projectNewSession(state.projectName)
    : state.kind === "error" ? hints.RETRY_REACHING_THE_RUNTIME : hints.ADD_WORKSPACE_DIRECTORY_AS_PROJECT;
  const error = state.error ?? "";
  // The body names the project in context so the sentence and the button
  // always agree about what gets created; a failed action that left a
  // message on a settled state replaces the copy with it rather than
  // stacking a second explanation under it. The error state's body is the
  // reassurance, and the server's message trails it as a caption, so the
  // user reads "still there" before "what went wrong".
  const failed = state.kind !== "error" && error !== "";
  const body = failed
    ? escapeHtml(error)
    : state.kind === "no-session"
      ? `Pick a session from the side panel, or start a new one in <em>${escapeHtml(state.projectName ?? "")}</em>.`
      : state.kind === "error"
        ? (state.cached === true
          ? "The list on the left is from your last visit. Your sessions are still on the server. Retrying every 10 seconds."
          : "Your sessions are still on the server. This page just can't see them yet. Retrying every 10 seconds.")
        : "Point Deck at a directory, an existing repository, or a repository to clone.";
  const bodyTone = failed ? "text-error" : "text-text-tertiary";
  const detail = state.kind === "error" && error !== ""
    ? `<p data-empty-detail class="max-w-[44ch] text-caption text-error">${escapeHtml(error)}</p>`
    : "";
  const actionHtml = `<button type="button" data-empty-action data-hint="${escapeHtml(actionHint)}"
          class="mt-(--space-2) h-9 rounded-control bg-accent px-3 text-body-sm font-medium text-text-on-accent transition-colors hover:bg-accent-hover pointer-coarse:min-h-11 focus-visible:bg-wash-hover">${action}</button>`;
  return `
    <div data-empty-pane="${state.kind}" ${state.kind === "error" ? 'role="alert"' : ""} class="grid h-full place-items-center px-6 pb-[7%] text-center font-ui font-normal tracking-normal">
      <!-- Optical, not geometric, centring: a block held at the pane's
           exact middle sits mid-void in a tall window and reads as still
           loading, so the bottom padding lifts the composition's centre
           above the pane's. -->
      <div class="flex flex-col items-center gap-(--space-3)">
        <span aria-hidden="true" class="${iconTone}">${lucideIcon(icon, 20)}</span>
        <h2 data-empty-title class="text-title-3 font-title-caps text-text-primary">${title}</h2>
        <p data-empty-body class="max-w-[44ch] text-body-sm ${bodyTone}">${body}</p>
        ${detail}
        ${actionHtml}
      </div>
    </div>`;
}

// The loading pane is not a member of the empty family: no icon, no
// title, no action — a spinner and one line, because the page has nothing
// to report yet and an empty-state shape here is what reads as "lost". It
// keeps the family's surface and lift so the settled state lands in place.
function loadingPaneHtml(phase: LoadingPhase, session: string | null, cached: boolean): string {
  const spinner = `<span aria-hidden="true" class="block h-[13px] w-[13px] flex-none animate-spinner rounded-circle border-[1.5px] border-border-strong border-t-text-secondary"></span>`;
  const line = session !== null
    ? `Reattaching to <em>${escapeHtml(session)}</em>…`
    : "Reconnecting to your sessions…";
  const content = phase === "reconnect"
    ? `<div class="flex items-center gap-(--space-3) text-body-sm text-text-secondary">${spinner}<span data-empty-line>${line}</span></div>`
    : `<div class="flex flex-col items-center gap-(--space-2)">
        <div class="flex items-center gap-(--space-3) text-body-sm text-text-primary">${spinner}<span data-empty-line>Still reaching the runtime</span></div>
        <p data-empty-body class="max-w-[44ch] text-caption text-text-secondary">${
          cached
            ? "These are your sessions as of your last visit. They keep running on the server while this page reconnects."
            : "Sessions keep running on the server while this page reconnects. Nothing has been closed."
        }</p>
      </div>`;
  return `
    <div data-empty-pane="loading" data-empty-phase="${phase}" role="status" aria-busy="true" class="grid h-full place-items-center px-6 pb-[7%] text-center font-ui font-normal tracking-normal">
      ${content}
    </div>`;
}

// The global settings trigger uses the shared cogwheel.
// Exported because both the live titlebar and the preview render it
// through titlebarRightHtml, and a local copy in each is how the two
// drift.
export function settingsGlyph(): string {
  return lucideIcon("settings", 18);
}
