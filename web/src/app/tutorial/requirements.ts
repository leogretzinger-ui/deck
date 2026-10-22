import { AGENT_CHAT_SETUP_SENTENCE, AGENT_CHAT_SETUP_URL } from "../../lib/agent-chat-setup.ts";
import { OPEN_AGENT_CHAT_SETUP } from "../../lib/hints.ts";

// What a lesson needs from the installation, as the catalog words it. A
// fact is null while the app has not found out yet, and an unknown fact
// never blocks a lesson.
export interface RequirementFacts {
  // The Chats data the panel reads is available (the MemPalace overlay is on).
  chats: boolean | null;
  // A GitHub account is connected.
  github: boolean | null;
  // The installation has project previews set up.
  previews: boolean | null;
  hostname: string;
}

// Only an answer that says the connection is missing may become false. The
// status endpoint answers HTTP 200 for timeout, malformed, oversized and
// run-error too, and those say nothing about the connection.
export function githubFact(outcome: string): boolean | null {
  switch (outcome) {
    case "authenticated": return true;
    case "no-account":
    case "missing-cli":
    case "auth-issue":
    case "authentication-required": return false;
    default: return null;
  }
}

// A chat read that worked settles the fact as true. Otherwise the status
// endpoint decides, and an install that has not answered yet stays unknown.
export function chatsFact(panelState: string | undefined, available: boolean | null): boolean | null {
  return panelState === "ready" ? true : available;
}

export const unknownRequirementFacts: RequirementFacts = { chats: null, github: null, previews: null, hostname: "" };

export interface RequirementNote {
  text: string;
  // An unmet need dims the lesson row; information leaves it as it is.
  unmet: boolean;
  // How to turn the missing thing on, shown under the row.
  enable?: { sentence: string; label: string; url: string; hint: string };
}

const LOOPBACK_NAME = /^(localhost|.+\.localhost|127(\.\d{1,3}){3}|\[?::1\]?)$/i;

export function isLoopbackHostname(hostname: string): boolean {
  return LOOPBACK_NAME.test(hostname);
}

// Returns the note for one requirement, or null when it is met and has
// nothing to say.
function noteFor(requirement: string, facts: RequirementFacts): RequirementNote | null {
  const need = (text: string, met: boolean): RequirementNote | null => (met ? null : { text, unmet: true });
  switch (requirement) {
    case "agent-chat":
    case "mempalace":
      return facts.chats === false
        ? { text: "Needs agent chat (MemPalace)", unmet: true, enable: { sentence: AGENT_CHAT_SETUP_SENTENCE, label: "Agent chat setup", url: AGENT_CHAT_SETUP_URL, hint: OPEN_AGENT_CHAT_SETUP } }
        : null;
    case "github":
      return need("Needs a GitHub connection", facts.github !== false);
    case "previews":
      return need("Needs project previews", facts.previews !== false);
    case "reachable-install":
      return need("Needs Deck reachable from your phone", !isLoopbackHostname(facts.hostname));
    case "two-sessions":
      return { text: "Uses two agent sessions", unmet: false };
    case "claude":
      return { text: "Uses your Claude plan or API key", unmet: false };
    case "review-app":
      return { text: "Needs the review app", unmet: false };
    default:
      return null;
  }
}

// One note per distinct text, in the order the lesson lists its needs.
export function requirementNotes(requires: readonly string[], facts: RequirementFacts): RequirementNote[] {
  const notes: RequirementNote[] = [];
  for (const requirement of requires) {
    const note = noteFor(requirement, facts);
    if (note !== null && !notes.some((existing) => existing.text === note.text)) notes.push(note);
  }
  return notes;
}
