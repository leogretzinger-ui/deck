// Submit a one-use preview ticket from a tab opened by the user’s click.
import { ApiError } from "../api/http.ts";
import type { ApiClient } from "../api/contracts.ts";
import type { ProjectPreviewEntry } from "../api/project-preview.ts";

export const PREVIEW_NOT_CONFIGURED = "Preview is not configured for this project.";

const POPUP_BLOCKED = "Deck could not open a preview tab. Allow pop-ups for this page and try again.";
const ENTRY_REFUSED = "The preview address could not be opened.";
const REQUEST_FAILED = "The preview could not be opened.";

// Reject malformed entry addresses before constructing the form.
export function previewEntryProblem(raw: string, deckOrigin: string): string | null {
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    return ENTRY_REFUSED;
  }
  if ((url.protocol !== "https:" && !isLoopbackEntry(url, deckOrigin)) ||
      url.hostname === "" ||
      url.username !== "" ||
      url.password !== "" ||
      url.search !== "" ||
      url.hash !== "" ||
      url.origin === deckOrigin) {
    return ENTRY_REFUSED;
  }
  return null;
}

// A loopback install serves previews over plain HTTP from 127.0.0.x literals,
// but only when Deck itself is reached over loopback HTTP: an HTTPS page must
// never POST the ticket to a plain-HTTP address.
function isLoopbackEntry(url: URL, deckOrigin: string): boolean {
  if (url.protocol !== "http:" || !isLoopbackLiteral(url.hostname)) return false;
  let deck: URL;
  try {
    deck = new URL(deckOrigin);
  } catch {
    return false;
  }
  return deck.protocol === "http:" && (deck.hostname === "127.0.0.1" || deck.hostname === "localhost");
}

// `new URL` has already parsed any all-numeric host as IPv4: shorthand and
// integer forms arrive as dotted decimal and out-of-range forms never arrive
// at all, so no range check can be reached here. What remains is a name the
// parser kept verbatim, and the anchors are the whole check: a DNS name that
// merely begins with 127, or ends in a numeric-looking label such as
// 127.0.0.1e1, is not a loopback literal. 127.0.0.1 is refused: cookies
// ignore the port, so a preview on Deck's own address is the same site as
// Deck and could read and send its cookies.
const LOOPBACK_LITERAL = /^127(\.(0|[1-9][0-9]{0,2})){3}$/;

function isLoopbackLiteral(hostname: string): boolean {
  return hostname !== "127.0.0.1" && LOOPBACK_LITERAL.test(hostname);
}

// Open synchronously during the click gesture to avoid popup blocking.
export function openProjectPreview(
  projectID: string,
  api: ApiClient,
  onFailure: (message: string, cause?: unknown) => void,
): void {
  const popup = window.open("", "_blank");
  if (popup === null) {
    onFailure(POPUP_BLOCKED);
    return;
  }
  // The app must not retain a handle to Deck’s window.
  popup.opener = null;
  void (async () => {
    try {
      if (api.projectPreview === undefined) {
        throw new ApiError(404, "preview_unavailable", PREVIEW_NOT_CONFIGURED);
      }
      const entry = await api.projectPreview(projectID);
      const problem = previewEntryProblem(entry.url, window.location.origin);
      if (problem !== null) {
        popup.close();
        onFailure(problem);
        return;
      }
      submitTicket(popup, entry);
    } catch (err) {
      popup.close();
      onFailure(err instanceof ApiError ? err.message : REQUEST_FAILED, err);
    }
  })();
}

// A POST body keeps the ticket out of browser history and URL access logs.
function submitTicket(popup: Window, entry: ProjectPreviewEntry): void {
  const doc = popup.document;
  // Deck's inherited no-referrer policy would make the POST Origin null.
  // Send only Deck's origin so the gateway can verify this entry gesture.
  const referrer = doc.createElement("meta");
  referrer.name = "referrer";
  referrer.content = "origin";
  doc.head.append(referrer);
  const form = doc.createElement("form");
  form.method = "POST";
  form.action = entry.url;
  const field = doc.createElement("input");
  field.type = "hidden";
  field.name = "ticket";
  field.value = entry.ticket;
  form.append(field);
  doc.body.append(form);
  form.submit();
}
