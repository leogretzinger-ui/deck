// agentHandle is a session's a2a handle, derived the way the server and the
// client derive it (internal/chats.Handle, deploy/a2a/a2a.py): the harness
// name and the first four characters of the session id. A harness a2a does
// not know has no handle, and so no chats.
const HARNESS: Record<string, string> = {
  claude: "claude",
  codex: "codex",
  kimi: "kimi",
  opencode: "opencode",
  pi: "pi",
  omp: "omp",
};

export function agentHandle(session: { id: string; provider: string }): string | null {
  const harness = HARNESS[session.provider];
  if (harness === undefined || session.id.length < 4) return null;
  return `${harness}-${session.id.slice(0, 4).toLowerCase()}`;
}
