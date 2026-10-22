import assert from "node:assert/strict";
import test from "node:test";

import { pinnedOrder, rankHarnesses } from "./harness-ranking.ts";
import type { HarnessUsage, HarnessUsageReport } from "../../api/sessions.ts";

const SERVED = ["claude", "codex", "kimi", "opencode", "shell"] as const;

const used = (provider: string, launches: number, at = "2026-09-01T00:00:00Z"): HarnessUsage =>
  ({ provider, launches, last_used_at: at });

const report = (
  project: HarnessUsage[] = [],
  global: HarnessUsage[] = [],
): HarnessUsageReport => ({ project, global });

// No answer is not an empty answer: a read that has not happened, and one
// that failed, must leave the served order exactly as it came.
test("without a usage report the served order stands", () => {
  assert.deepEqual(rankHarnesses(SERVED, null), SERVED);
});

// A backend with no launches recorded anywhere is an ordinary state — a
// fresh installation — and it must not shuffle the picker.
test("an empty report leaves the served order", () => {
  assert.deepEqual(rankHarnesses(SERVED, report()), SERVED);
});

// The task's acceptance case: a project whose only history is codex shows
// codex first.
test("the project's own history comes first", () => {
  assert.deepEqual(
    rankHarnesses(SERVED, report([used("codex", 3)])),
    ["codex", "claude", "kimi", "opencode", "shell"],
  );
});

// The picker is opened IN a project, so what that project runs outranks the
// installation's totals — a harness with one launch here beats one with
// forty everywhere else.
test("this project's history outranks the installation's", () => {
  assert.deepEqual(
    rankHarnesses(SERVED, report([used("kimi", 1)], [used("claude", 40), used("kimi", 1)])),
    ["kimi", "claude", "codex", "opencode", "shell"],
  );
});

test("more launches rank higher, and a tie goes to the more recent", () => {
  assert.deepEqual(
    rankHarnesses(SERVED, report([
      used("shell", 2, "2026-09-01T00:00:00Z"),
      used("claude", 9, "2026-01-01T00:00:00Z"),
      used("kimi", 2, "2026-09-05T00:00:00Z"),
    ])),
    ["claude", "kimi", "shell", "codex", "opencode"],
  );
});

// The timestamps are RFC 3339 and can carry offsets, so they are compared as
// instants: sorting the text would rank an offset by its digits.
test("timestamps are compared as instants, not as text", () => {
  assert.deepEqual(
    rankHarnesses(["claude", "codex"], report([
      used("claude", 1, "2026-09-06T01:00:00Z"),
      used("codex", 1, "2026-09-06T04:00:00+02:00"),
    ])),
    ["codex", "claude"],
    "04:00+02:00 is 02:00Z, later than 01:00Z",
  );
});

// A harness nobody has launched still has to be on screen: ranking reorders
// the served list and never filters it.
test("every served harness survives the ranking", () => {
  const ranked = rankHarnesses(SERVED, report([used("codex", 1)], [used("shell", 7)]));
  assert.deepEqual([...ranked].sort(), [...SERVED].sort());
  assert.equal(ranked.length, SERVED.length);
});

// History for a harness this image cannot launch is history for something
// the user cannot pick. The server filters it too; a client that
// appended it would put an unlaunchable chip in the picker.
test("history for an unserved harness adds nothing to the list", () => {
  assert.deepEqual(
    rankHarnesses(["claude", "codex"], report([used("cursor", 99), used("codex", 1)])),
    ["codex", "claude"],
  );
});

// Ranking must be a total order the served list can fall back on, or two
// harnesses the history cannot separate would swap places between renders.
test("harnesses the history cannot separate keep their served order", () => {
  const same = report([used("shell", 2, "2026-09-01T00:00:00Z"), used("codex", 2, "2026-09-01T00:00:00Z")]);
  assert.deepEqual(rankHarnesses(SERVED, same), ["codex", "shell", "claude", "kimi", "opencode"]);
  assert.deepEqual(rankHarnesses(SERVED, same), rankHarnesses(SERVED, same));
});

// A malformed timestamp is the server's record all the same: the entry keeps
// its band and its launch count, and only loses the recency tiebreak.
test("an unparseable timestamp loses the tiebreak rather than the ranking", () => {
  assert.deepEqual(
    rankHarnesses(SERVED, report([used("shell", 5, "not a date"), used("codex", 1)])),
    ["shell", "codex", "claude", "kimi", "opencode"],
  );
  assert.deepEqual(
    rankHarnesses(SERVED, report([used("shell", 1, "not a date"), used("codex", 1)])),
    ["codex", "shell", "claude", "kimi", "opencode"],
    "a real date outranks an unreadable one at equal launches",
  );
});

// The input list is the model's own array; sorting it in place would reorder
// the inventory the rest of the sidebar reads.
test("the served list is not reordered in place", () => {
  const available = [...SERVED];
  rankHarnesses(available, report([used("shell", 4)]));
  assert.deepEqual(available, SERVED);
});

// An open picker keeps the order it is drawing. Null is the served order:
// a picker that has not been ranked yet shows what the server sent, never
// the previous project's ranking.
test("no pin is the served order", () => {
  assert.deepEqual(pinnedOrder(SERVED, null), SERVED);
});

test("a pin decides the order of what the model holds", () => {
  assert.deepEqual(pinnedOrder(SERVED, ["shell", "kimi"]), ["shell", "kimi", "claude", "codex", "opencode"]);
});

// You can retry a failed harness read with the dialog open, so the
// inventory can change under a pin. The pin decides order, never membership:
// a provider it has never seen keeps its served place at the end, and one it
// names that is no longer served simply is not there.
test("a pin never adds or drops a harness", () => {
  assert.deepEqual(
    pinnedOrder(["claude", "codex", "kimi", "pi"], ["kimi", "claude", "gone"]),
    ["kimi", "claude", "codex", "pi"],
  );
  assert.deepEqual(pinnedOrder([], ["kimi"]), []);
});

test("a pinned list is not reordered in place", () => {
  const available = [...SERVED];
  pinnedOrder(available, ["shell"]);
  assert.deepEqual(available, SERVED);
});
