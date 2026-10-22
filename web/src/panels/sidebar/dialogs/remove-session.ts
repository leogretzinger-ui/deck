import { SESSION_COLOR_VARS } from "../../../lib/session-colors.ts";
import { escapeHtml } from "../sidebar.ts";
import { DIALOG_BODY, DIALOG_FOOT, dialogPromptHead } from "../../../ui/controls.ts";
import * as hints from "../../../lib/hints.ts";

// Process-ending confirm: the head carries the error tint.
export function removeConfirmModalHtml(target: { label: string; meta: string; color: string | null | undefined }): string {
    return `
        <div data-remove-confirm role="dialog" aria-modal="true" aria-label="Close this session?" class="dialog">
          ${dialogPromptHead("Close this session?", "Ends the session's tmux process when this Deck still owns it; otherwise removes the record only. The recording is kept either way.", "text-error-on-tint")}
          ${DIALOG_BODY(`
            <div class="flex items-center gap-2 rounded-control bg-surface-sunken border border-field-border px-3 py-2.5">
              <span aria-hidden="true" class="h-[6px] w-[6px] flex-none rounded-circle" style="background:${SESSION_COLOR_VARS[target.color ?? ""] ?? "var(--color-text-tertiary)"}"></span>
              <span class="min-w-0 flex flex-col gap-0.5">
                <span class="truncate text-body-sm font-medium text-text-primary">${escapeHtml(target.label)}</span>
                <span class="truncate font-mono text-micro text-text-tertiary">${escapeHtml(target.meta)}</span>
              </span>
            </div>
          `)}
          ${DIALOG_FOOT(`
            <button type="button" data-remove-cancel data-hint="${hints.KEEP_SESSION_OPEN}"
              class="h-9 rounded-control border border-border bg-surface-card px-3 text-body-sm text-text-primary transition-colors hover:bg-wash-hover pointer-coarse:min-h-11">Cancel</button>
            <button type="button" data-remove-confirm-btn data-hint="${hints.CLOSE_THIS_SESSION}"
              class="h-9 rounded-control bg-accent px-4 text-body-sm font-medium text-text-on-accent transition-colors hover:bg-accent-hover pointer-coarse:min-h-11">Close session</button>
          `)}
        </div>`;
}
