import { escapeHtml } from "../sidebar.ts";
import { DIALOG_BODY, DIALOG_FOOT, dialogPromptHead } from "../../../ui/controls.ts";
import { lucideIcon } from "../../../ui/icons.ts";
import * as hints from "../../../lib/hints.ts";

// Reversible removal: the head carries the warning tint.
export function removeProjectConfirmModalHtml(target: { name: string; path: string; live: number }): string {
    const cascade = target.live === 0
      ? ""
      : `Closes ${target.live === 1 ? "its 1 session" : `its ${target.live} sessions`}. `;
    return `
        <div data-remove-project-confirm role="dialog" aria-modal="true" aria-label="Remove ${escapeHtml(target.name)} from the sidebar?" class="dialog">
          ${dialogPromptHead(`Remove ${escapeHtml(target.name)} from the sidebar?`, `${cascade}The directory on disk stays.`, "text-warning-on-tint")}
          ${DIALOG_BODY(`
            <div class="flex items-center gap-2 rounded-control bg-surface-sunken border border-field-border px-3 py-2.5">
              <span aria-hidden="true" class="flex-none text-text-tertiary">${lucideIcon("folder", 14)}</span>
              <span class="min-w-0 truncate font-mono text-text-primary">${escapeHtml(target.path)}</span>
            </div>
          `)}
          ${DIALOG_FOOT(`
            <button type="button" data-remove-project-cancel data-hint="${hints.KEEP_PROJECT_ON_SIDEBAR}"
              class="h-9 rounded-control border border-border bg-surface-card px-3 text-body-sm text-text-primary transition-colors hover:bg-wash-hover pointer-coarse:min-h-11">Cancel</button>
            <button type="button" data-remove-project-confirm-btn data-hint="${hints.REMOVE_PROJECT_FROM_SIDEBAR}"
              class="h-9 rounded-control bg-error px-4 text-body-sm font-medium text-text-on-error transition-colors hover:opacity-90 pointer-coarse:min-h-11">Remove project</button>
          `)}
        </div>`;
}
