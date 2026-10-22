// The selected session's reconciled record. DOM-free so the logic runs under
// plain node tests. Nothing here infers provider state from native identity
// or terminal output.
import type { ApiClient } from "../api/contracts.ts";
import type { SessionDetail, SessionSummary } from "../api/sessions.ts";

// The backend's reconciled presentation fact is the only browser-side gate
// for mounting a saved session. Unknown and transitional values stay off the
// active surface until the server reports an exact verified attachment.
export function isVerifiedRunningSession(
  session: Pick<SessionSummary, "presentation_state">,
): boolean {
  return session.presentation_state === "running";
}

// A session in a transitional presentation is not mountable, but it EXISTS:
// a live pane the reconciler has not re-verified (a redeploy's limbo) must
// stay reachable from the list, or the user's only path back to it is
// gone. So must a session whose recorded conversation was not continued: the
// row is the only way to the card that offers Resume and Start fresh session,
// and hiding it leaves the user neither choice. Ended and inactive
// sessions are the ones the list hides.
export function sessionStaysListed(
  session: Pick<SessionSummary, "presentation_state">,
): boolean {
  return session.presentation_state === "running"
    || session.presentation_state === "recovering"
    || session.presentation_state === "attachment_unavailable"
    || session.presentation_state === "resume_ended";
}

// The list's content-free attention mark (a warning dot): a listed session
// the server cannot yet verify as running.
export function sessionNeedsAttention(
  session: Pick<SessionSummary, "presentation_state">,
): boolean {
  return session.presentation_state === "recovering"
    || session.presentation_state === "attachment_unavailable"
    || session.presentation_state === "resume_ended";
}

export interface SessionStatusCallbacks {
  onError?: (message: string, cause?: unknown) => void;
}

export class SessionStatusModel {
  session: SessionDetail | null = null;

  private client: ApiClient;
  private sessionID: string;
  private cb: SessionStatusCallbacks;
  private refreshing = false;

  constructor(
    client: ApiClient,
    sessionID: string,
    cb: SessionStatusCallbacks = {},
  ) {
    this.client = client;
    this.sessionID = sessionID;
    this.cb = cb;
  }

  // refresh reloads the session record. One in flight at most; a failed
  // reload keeps the last good state and reports the failure to the caller.
  // A stale response (an older request resolving after a newer one) is
  // discarded, and force supersedes an in-flight refresh.
  private refreshSeq = 0;

  async refresh(force = false): Promise<void> {
    if (this.refreshing && !force) return;
    const seq = ++this.refreshSeq;
    this.refreshing = true;
    try {
      const session = await this.client.getSession(this.sessionID);
      if (seq !== this.refreshSeq) {
        return; // a newer refresh superseded this response
      }
      this.session = session;
    } catch (err) {
      if (seq !== this.refreshSeq) {
        return;
      }
      this.cb.onError?.(err instanceof Error ? err.message : String(err), err);
    } finally {
      if (seq === this.refreshSeq) {
        this.refreshing = false;
      }
    }
  }

}
