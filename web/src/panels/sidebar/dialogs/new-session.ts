import { escapeHtml } from "../sidebar.ts";
import { providerDisplayName, providerTileClass } from "../../../ui/session-identity.ts";
import { DIALOG_BODY, dialogHead, PRIMARY_SM_BUTTON } from "../../../ui/controls.ts";
import { lucideIcon } from "../../../ui/icons.ts";
import * as hints from "../../../lib/hints.ts";

function providerMatchesFilter(provider: string, filter: string): boolean {
  return `${provider} ${providerDisplayName(provider)}`.toLowerCase().includes(filter.trim().toLowerCase());
}

// Keep the server's complete inventory in its stable display order.
export function newSessionVisibleProviders(
  available: readonly string[],
  filter: string,
  launching: string | null,
): readonly string[] {
  return available.filter((provider) => provider === launching || providerMatchesFilter(provider, filter));
}

// The phone sheet's boundary is the one app.css presents the sheet at. The two
// are read in different languages and cannot share a constant, so they are
// named together here: a drift between them renders chips inside a sheet built
// for rows, or rows on a desktop that has room for neither.
function phoneSheet(): boolean {
  if (typeof window === "undefined") return false;
  if (window.matchMedia("(max-width: 600px)").matches) return true;
  // A device frame drawn inside a wide review page is narrow by attribute:
  // the media query reads the review page's viewport, not the frame's box.
  // app.css mirrors every phone rule onto that attribute, so the content
  // decision has to read it too or the framed route renders desktop chips
  // inside phone chrome.
  return document.querySelector('[data-viewport="narrow"]') !== null;
}

export function newSessionBodyHtml(state: {
  available: readonly string[];
  filter: string;
  launching: string | null;
  creating: boolean;
  selection: number | null;
  phone?: boolean;
  // inventoryStatus defaults to served so callers that only pass an
  // available list continue to mean "the server answered with this".
  inventoryStatus?: "served" | "stale" | "unknown";
  inventoryError?: string | null;
  inventoryRefreshing?: boolean;
}): string {
  const visible = newSessionVisibleProviders(state.available, state.filter, state.launching);
  const inventoryStatus = state.inventoryStatus ?? "served";
  const inventoryError = state.inventoryError ?? null;
  const inventoryRefreshing = state.inventoryRefreshing ?? false;
  // The count names what this image offers, not what the bundle can draw.
  // When the server could not be asked, the count is unknown and the field
  // says so rather than inventing a number.
  const placeholder = inventoryStatus === "unknown"
    ? (inventoryError === null ? "Loading harnesses…" : "Harness list unavailable")
    : `Filter ${state.available.length} harnesses`;
  const filter = `
    <!-- The field owns focus the moment the dialog opens and wears the
         focused border: the hairline, quieter than rest,
         with the accent caret carrying the focus. That is the
         .input.caret-accent:focus rule; nothing here overrides it. -->
    <label data-hint="${hints.FILTER_PROVIDER_LIST}" class="inputwrap">
      <span aria-hidden="true" class="inputwrap-lead">${lucideIcon("search", 15)}</span>
      <input type="search" data-new-session-filter aria-label="Filter providers" autocomplete="off" spellcheck="false" placeholder="${placeholder}"
        value="${escapeHtml(state.filter)}"
        class="input input-lead font-input caret-accent max-md:min-h-11">
    </label>`;
  // Three different inventory states. Only a server-stated empty inventory
  // uses the "No harnesses are available" message; a fetch failure keeps the
  // last good list as stale and shows a retry affordance instead.
  const emptyVisible = inventoryStatus === "served" && visible.length === 0;
  const empty = state.available.length === 0
    ? "No harnesses are available"
    : `No provider matches “${escapeHtml(state.filter.trim())}”`;
  const unreadError = inventoryStatus === "stale"
    ? (inventoryError ?? "Could not refresh harness list")
    : inventoryStatus === "unknown"
      ? inventoryError
      : null;
  const unreadBanner = unreadError === null
    ? (inventoryStatus === "unknown" ? `<p role="status" class="text-caption text-text-secondary">Loading harnesses…</p>` : "")
    : `<div data-new-session-inventory-unread class="flex items-center gap-3 text-caption text-text-secondary">
        <span>${escapeHtml(unreadError)}</span>
        <button type="button" data-new-session-inventory-retry ${inventoryRefreshing ? "disabled data-new-session-inventory-retrying" : ""} class="${PRIMARY_SM_BUTTON}">
          ${inventoryRefreshing ? `<span class="mr-1.5 inline-block h-3 w-3 animate-row-spinner rounded-circle border-[1.5px] border-current border-t-transparent" aria-hidden="true"></span>` : ""}Retry
        </button>
      </div>`;
  const chips = visible.map((provider, index) => {
    const name = providerDisplayName(provider);
    const launching = state.launching === provider;
    const armed = state.selection === index;
    return `<button type="button" data-new-session-provider="${provider}" data-selected="${armed ? "true" : "false"}" ${state.creating ? "disabled" : ""}
      data-hint="${hints.newSessionProvider(escapeHtml(name))}"
      class="inline-flex items-center gap-[7px] py-[5px] pr-2.5 pl-2 rounded-pill border border-border bg-surface text-body-sm leading-(--type-body-lh) tracking-(--type-body-ls) text-text-primary cursor-pointer transition-colors hover:border-border-strong hover:bg-wash-hover data-[selected=true]:border-accent-border data-[selected=true]:bg-wash-selected disabled:opacity-45 focus-visible:bg-wash-hover"
      ${armed ? "tabindex=\"0\"" : "tabindex=\"-1\""}>
      <span aria-hidden="true" class="h-[11px] w-[11px] flex-none rounded-mark ${providerTileClass(provider)}"></span>
      ${escapeHtml(name)}
      <span data-new-session-spinner="${provider}" ${launching ? "" : "hidden"} aria-label="Starting ${escapeHtml(name)}"
        class="ml-1 block h-3 w-3 flex-none animate-row-spinner rounded-circle border-[1.5px] border-border-strong border-t-text-secondary"></span>
    </button>`;
  }).join("");
  if (state.phone === true) {
    // The phone uses rows for the same complete list as the desktop.
    const row = (provider: string): string => {
      const name = providerDisplayName(provider);
      const launching = state.launching === provider;
      return `<button type="button" data-new-session-provider="${provider}" ${state.creating ? "disabled" : ""}
        data-hint="${hints.newSessionProvider(escapeHtml(name))}" class="menuitem">
        <span aria-hidden="true" class="h-6 w-6 flex-none rounded-mark ${providerTileClass(provider)}"></span>
        <span class="min-w-0 flex-1 truncate text-text-primary">${escapeHtml(name)}</span>
        <span data-new-session-spinner="${provider}" ${launching ? "" : "hidden"} aria-label="Starting ${escapeHtml(name)}"
          class="block h-3 w-3 flex-none animate-row-spinner rounded-circle border-[1.5px] border-border-strong border-t-text-secondary"></span>
      </button>`;
    };
    const emptyEl = inventoryStatus === "served"
      ? `<p data-new-session-empty ${emptyVisible ? "" : "hidden"} class="text-caption text-text-tertiary">${empty}</p>`
      : "";
    return `${unreadBanner}
      ${filter}
      <div data-new-session-chips role="group" aria-label="Available providers" class="flex flex-col px-2.5 pt-1.5 pb-2">
        ${visible.map(row).join("")}
      </div>
      ${emptyEl}`;
  }
  const emptyEl = inventoryStatus === "served"
    ? `<p data-new-session-empty ${emptyVisible ? "" : "hidden"} class="text-caption text-text-tertiary">${empty}</p>`
    : "";
  return `${unreadBanner}
    ${filter}
    <div data-new-session-chips role="group" aria-label="Available providers" class="flex flex-wrap gap-[7px]">${chips}</div>
    ${emptyEl}`;
}

// newSessionDialogTarget resolves the project the New session dialog names.
// The stored id is the one the launch creates against, so it is the only
// source: every path that opens the dialog sets it, and an id that matches no
// project names nothing. Falling back to another resolution here would name a
// project the session will not land in, which is worse than naming none.
export function newSessionDialogTarget(
  groups: readonly { project: { id: string; name: string } }[],
  storedID: string | null,
): { id: string; name: string } | null {
  if (storedID === null) return null;
  return groups.find((group) => group.project.id === storedID)?.project ?? null;
}

export interface NewSessionModalState {
  // The harnesses this image can launch, in the order they are drawn. The
  // server is the only thing that knows either.
  available: readonly string[];
  // inventoryStatus carries whether the available list is fresh, stale, or
  // unknown, so the dialog can render a truthful empty/unread state.
  inventoryStatus?: "served" | "stale" | "unknown";
  inventoryError?: string | null;
  // inventoryRefreshing is true while the retry button has triggered a refresh
  // and the fresh answer has not returned yet.
  inventoryRefreshing?: boolean;
  creating: boolean;
  target: { id: string; name: string } | null;
  // Replaces the project line under the title when no project is named.
  subtitle?: string;
  filter: string;
  launching: string | null;
  selection: number | null;
}

// The provider picker is deliberately a finite inventory rather than a
// free-form command: the server still decides whether the selected CLI can
// launch. Filtering only changes presentation; the body's markup is
// newSessionBodyHtml above.
export function newSessionModalHtml(state: NewSessionModalState): string {
    return `
        <div data-new-session-modal role="dialog" aria-modal="true" aria-label="New session" class="dialog" data-new-session-dialog>
          ${dialogHead(
            "New session",
            { label: "Close", attr: "data-new-session-cancel", disabled: state.creating },
            state.target === null ? state.subtitle : `in ${escapeHtml(state.target.name)}`,
          )}
          ${DIALOG_BODY(newSessionBodyHtml({
            available: state.available,
            inventoryStatus: state.inventoryStatus,
            inventoryError: state.inventoryError,
            inventoryRefreshing: state.inventoryRefreshing,
            filter: state.filter,
            launching: state.launching,
            creating: state.creating,
            selection: state.selection,
            phone: phoneSheet(),
          }))}
        </div>`;
}
