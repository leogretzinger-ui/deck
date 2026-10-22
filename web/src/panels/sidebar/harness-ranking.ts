// Ranking the new-session picker by what you actually launch,
// from the launch history GET /api/harness-usage serves at both scopes.
//
// The console ranks; it never counts. The rows are the server's record of
// what was launched, and this reorders a list the server sent — no usage is
// derived here from anything the browser can see.
import type { HarnessUsage, HarnessUsageReport } from "../../api/sessions.ts";

// A harness the project itself has run outranks one it has not, whatever the
// installation-wide totals say: the picker is opened in a project, and the
// question in front of you is which harness THIS repository runs.
// Below that, more launches first, and a tie goes to the more recent one.
// Everything with no history keeps its served position, at the end — an
// unranked harness must still be reachable without searching for it.
export function rankHarnesses(
  available: readonly string[],
  usage: HarnessUsageReport | null,
): readonly string[] {
  if (usage === null) return available;
  const served = new Map(available.map((provider, index) => [provider, index]));
  const rank = (scope: readonly HarnessUsage[]): Map<string, HarnessUsage> => {
    const byProvider = new Map<string, HarnessUsage>();
    for (const one of scope) {
      if (served.has(one.provider)) byProvider.set(one.provider, one);
    }
    return byProvider;
  };
  const project = rank(usage.project);
  const global = rank(usage.global);
  // Three bands, in order: this project's history, history elsewhere, none.
  const band = (provider: string): number =>
    project.has(provider) ? 0 : global.has(provider) ? 1 : 2;
  const entry = (provider: string): HarnessUsage | undefined =>
    project.get(provider) ?? global.get(provider);
  return [...available].sort((a, b) => {
    const bands = band(a) - band(b);
    if (bands !== 0) return bands;
    const left = entry(a);
    const right = entry(b);
    if (left !== undefined && right !== undefined) {
      if (left.launches !== right.launches) return right.launches - left.launches;
      // Timestamps are RFC 3339 from the server; comparing them as strings
      // would order "2026-09-06T09:00:00Z" against a "+02:00" offset by its
      // text. Parse, and let an unparseable one lose to a real date rather
      // than reordering the pair on a NaN comparison that reads as equal.
      const at = Date.parse(left.last_used_at);
      const bt = Date.parse(right.last_used_at);
      const usable = Number.isFinite(at) && Number.isFinite(bt);
      if (usable && at !== bt) return bt - at;
      if (!usable && Number.isFinite(at) !== Number.isFinite(bt)) {
        return Number.isFinite(at) ? -1 : 1;
      }
    }
    // Ties fall back to the served order, so ranking never shuffles
    // harnesses the history cannot separate.
    return served.get(a)! - served.get(b)!;
  });
}

// An open picker keeps the order it is drawing, and this applies that pinned
// order to whatever inventory the model holds now: you can retry a
// failed harness read while the dialog is open, and the answer may carry
// providers the pin has never seen. Those keep their served places at the
// end rather than being dropped — the pin decides order, never membership.
export function pinnedOrder(
  available: readonly string[],
  pinned: readonly string[] | null,
): readonly string[] {
  if (pinned === null) return available;
  const rank = new Map(pinned.map((provider, index) => [provider, index]));
  const served = new Map(available.map((provider, index) => [provider, index]));
  return [...available].sort((a, b) => {
    const left = rank.get(a);
    const right = rank.get(b);
    if (left !== undefined && right !== undefined) return left - right;
    if (left !== undefined) return -1;
    if (right !== undefined) return 1;
    return served.get(a)! - served.get(b)!;
  });
}
