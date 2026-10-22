import type { GitIdentityProfile, Project } from "../../../api/projects.ts";
import { escapeHtml } from "../sidebar.ts";
import { providerDisplayName } from "../../../ui/session-identity.ts";
import { DIALOG_BODY, DIALOG_FOOT, dialogHead, PRIMARY_MD_BUTTON, SECONDARY_MD_BUTTON, SELECT_FIELD, selectShellHtml } from "../../../ui/controls.ts";
import * as hints from "../../../lib/hints.ts";

export interface ProjectGitIdentitySetupState {
  project: Project | undefined;
  retryProvider: string | null;
  profiles: readonly GitIdentityProfile[];
  selected: GitIdentityProfile | null;
  busy: boolean;
  error: string | null;
}

// agentStartsNote names the session waiting on an identity. The provider
// label is lowercase, so the sentence capitalises its first letter.
export function agentStartsNote(provider: string, verb: "apply" | "save"): string {
  const agent = providerDisplayName(provider);
  return `${agent.charAt(0).toUpperCase()}${agent.slice(1)} starts as soon as you ${verb} this.`;
}

export function projectGitIdentitySetupHtml(state: ProjectGitIdentitySetupState): string {
    const { project, profiles, selected } = state;
    return `
        <div data-project-git-identity role="dialog" aria-modal="true" aria-label="Git identity" class="dialog">
          ${dialogHead("Git identity", { label: "Close", attr: "data-project-identity-cancel", disabled: state.busy },
            `Choose who commits in ${escapeHtml(project?.name ?? "this project")}.`)}
          ${DIALOG_BODY(`
            ${state.retryProvider === null ? "" : `<p data-project-identity-waiting class="text-body-sm text-text-primary">${escapeHtml(agentStartsNote(state.retryProvider, "apply"))}</p>`}
            <label class="block">
              <span class="text-body-sm font-medium text-text-secondary">Git identity</span>
              ${selectShellHtml(`<select data-project-identity-profile data-hint="${hints.CHOOSE_GIT_IDENTITY_FOR_PROJECT}" class="${SELECT_FIELD} font-input h-10 w-full rounded-control border border-border-strong bg-surface-card pl-2 text-body-sm text-text-primary">
                ${profiles.map((profile) => `<option value="${escapeHtml(profile.id)}" ${profile.id === selected?.id ? "selected" : ""}>${escapeHtml(profile.name)} — ${escapeHtml(profile.email)}</option>`).join("")}
              </select>`, "mt-1")}
              <p class="mt-1.5 text-caption text-text-tertiary">Writes to this repo's local git config, replacing any values already there.</p>
            </label>
            ${state.error ? `<div class="rounded-control bg-error-tint px-3 py-2 text-caption text-error-on-tint">${escapeHtml(state.error)}</div>` : ""}
          `)}
          ${DIALOG_FOOT(`
            <button type="button" data-project-identity-cancel ${state.busy ? "disabled" : ""} data-hint="${hints.CANCEL_CHOOSING_IDENTITY}" class="${SECONDARY_MD_BUTTON}">Cancel</button>
            <button type="button" data-project-identity-save ${selected === null || state.busy ? "disabled" : ""} data-hint="${hints.APPLY_IDENTITY_TO_PROJECT}" class="${PRIMARY_MD_BUTTON}">Apply identity</button>
          `)}
        </div>`;
}
