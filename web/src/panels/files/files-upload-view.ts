// Presentation of browser-observed uploads. A completed transfer still waits
// for the server; cancellation never promises that the destination is unchanged.
import { escapeHtml } from "../../lib/escape.ts";
import { formatFileSize } from "../../lib/format.ts";
import { lucideIcon } from "../../ui/icons.ts";
import type { UploadRecord } from "./files-panel.ts";

// Shared row and detail facts, derived from the same observed upload record.
export interface UploadInfo {
  percent: number;
  cancel: boolean;
  dismiss: boolean;
  amount: string;
  state: string;
  note: string;
  meta: string;
  error: boolean;
  cancelLabel: string;
}

// Keep state labels shared between rows and details, including the distinction
// between transferring bytes and awaiting confirmation from the workspace.
export function uploadInfo(record: UploadRecord): UploadInfo {
  const percent = record.total > 0 ? Math.floor(Math.min(1, record.sent / record.total) * 100) : 0;
  const cancel = ["queued", "preparing", "uploading", "confirming"].includes(record.status);
  const dismiss = ["complete", "failed", "unconfirmed", "stopped", "too-large"].includes(record.status);
  const amount = `${formatFileSize(record.sent)} of ${formatFileSize(record.total)}`;
  const states = {
    queued: ["Queued", "Waiting for the preceding file. Cancelling skips only this file."],
    preparing: ["Preparing", "Preparing this file for transfer. Keep this tab open."],
    uploading: [`${record.replaces ? "Replacing" : "Uploading"} · ${percent}%`, "Keep this tab open. Closing the Files panel stops queued work; an active upload may already have reached the workspace."],
    confirming: ["Confirming", "All bytes were sent. Waiting for the workspace to confirm that it stored the file. You can stop waiting, but the file may already have been written."],
    conflict: ["Waiting for your choice", "Choose Replace, Upload as, or Cancel in the filename dialog."],
    complete: ["Checking folder", "The workspace confirmed the upload. Waiting for a folder listing to show the stored file."],
    failed: ["Upload failed", record.error ?? "The workspace reported an error. Check the folder before sending the file again."],
    unconfirmed: ["Outcome unknown", "No server confirmation was received. The file may have been written. Check the folder before sending it again; Deck will not retry it automatically."],
    stopped: ["Stopped", "This file has been removed from the upload queue. No further transfer will be sent."],
    "too-large": ["Over 32 MiB", "This file exceeds the 32 MiB limit and was not sent."],
  } as const;
  const [state, note] = states[record.status];
  const meta = record.status === "uploading" ? `${record.replaces ? "Replacing · " : ""}${percent}%`
    : record.status === "unconfirmed" ? "Unconfirmed"
    : record.status === "conflict" ? "Needs a choice" : state;
  const error = ["failed", "unconfirmed", "too-large"].includes(record.status);
  return {
    percent, cancel, dismiss, amount, state, note, meta, error,
    cancelLabel: record.status === "confirming" ? "Stop waiting" : "Cancel upload",
  };
}

// A full ring means all bytes were sent; the adjacent state still names
// confirmation or folder reconciliation rather than claiming success.
export function uploadRing(record: UploadRecord): string {
  const info = uploadInfo(record);
  if (info.error) return lucideIcon("triangle-alert", 14);
  if (record.status === "conflict") return lucideIcon("circle-help", 14);
  if (record.status === "stopped") return lucideIcon("file-text", 14);
  const fraction = record.status === "confirming" || record.status === "complete" ? 1 : info.percent / 100;
  return `<svg aria-hidden="true" class="files-upload-ring${record.status === "preparing" ? " is-preparing" : ""}" width="14" height="14" viewBox="0 0 14 14">
    <circle class="files-upload-ring-track" cx="7" cy="7" r="5.5"/>
    <circle data-files-upload-ring-value cx="7" cy="7" r="5.5" pathLength="100" stroke-dasharray="${record.status === "preparing" ? 20 : fraction * 100} 100"/>
  </svg>`;
}

// Dismiss lives in the header, away from Cancel at the bottom. Repeated input
// on Cancel must not erase the uncertain outcome that replaces the transfer.
export function uploadDetailsHtml(record: UploadRecord, rootLabel: string, mobile: boolean): string {
  const info = uploadInfo(record);
  const destination = `${rootLabel}/${record.destDir === "" ? "" : `${record.destDir}/`}`;
  const title = mobile ? "files-upload-sheet-title" : "files-upload-viewer-title";
  // There is no byte range for an empty file; its state text carries progress.
  const progress = record.total > 0 && (record.status === "uploading" || record.status === "confirming" || record.status === "complete");
  return `<section data-files-upload-details="${record.id}" data-status="${record.status}" class="files-upload-details${mobile ? " files-upload-sheet" : ""}" ${mobile ? `role="dialog" aria-modal="true" aria-labelledby="${title}"` : `aria-labelledby="${title}"`}>
    ${mobile ? '<div class="files-upload-grabber" aria-hidden="true"></div>' : ""}
    <div class="files-upload-details-head${mobile ? " dialog-head" : ""}">
      <span id="${title}" class="min-w-0 flex-1 truncate text-body-sm font-medium text-text-primary">${escapeHtml(record.name)}</span>
      ${info.dismiss ? `<button type="button" data-files-upload-action="${record.id}" data-action="dismiss" data-focus-key="upload-dismiss:${record.id}" class="files-upload-dismiss">Dismiss</button>` : ""}
      <button type="button" data-files-upload-close ${mobile ? "data-dialog-close " : ""}data-focus-key="upload-close" aria-label="Close upload details" class="files-upload-close">${lucideIcon("x", 14)}</button>
    </div>
    <div class="files-upload-details-body">
      <span data-files-upload-announcement role="status" class="sr-only">${escapeHtml(info.state.split(" · ")[0]!)}</span>
      <div data-files-upload-state tabindex="-1" data-focus-key="upload-state:${record.id}" class="text-body-sm font-medium ${info.error ? "text-error" : "text-text-primary"}">${escapeHtml(info.state)}</div>
      <div class="files-upload-location text-caption text-text-tertiary">${escapeHtml(destination)} · ${record.position} of ${record.batchSize}</div>
      <div data-files-upload-progress ${progress ? "" : "hidden"} role="progressbar" aria-label="Bytes transferred" aria-valuemin="0" aria-valuemax="${Math.max(1, record.total)}" aria-valuenow="${record.sent}" class="files-upload-progress"><span style="width:${record.status === "uploading" ? info.percent : 100}%"></span></div>
      <div data-files-upload-amount class="text-body-sm text-text-secondary">${escapeHtml(info.amount)}</div>
      <p data-files-upload-note class="text-caption leading-relaxed text-text-tertiary">${escapeHtml(info.note)}</p>
    </div>
    ${info.cancel ? `<div class="files-upload-details-actions"><button type="button" data-files-upload-action="${record.id}" data-action="cancel" data-focus-key="upload-cancel:${record.id}">${info.cancelLabel}</button></div>` : ""}
  </section>`;
}
