import assert from "node:assert/strict";
import test from "node:test";
import type { SessionDetail } from "../api/sessions.ts";
import {
  recoveryComposerHtml,
  recoveryFromSession,
  recoveryRegisterHtml,
  recoveryStreamHtml,
  referenceRecovery,
} from "./recovery.ts";

const base: SessionDetail = {
  id: "sess-9", project_id: "proj-1", provider: "claude", display_name: "",
  status: "running", generation: 2, created_at: "t", updated_at: "t",
  recovery_capability: "resume", recovery_state: "unavailable", presentation_state: "running",
};

const detail = (over: Partial<SessionDetail>): SessionDetail => ({ ...base, ...over });

const recovering = (over: Partial<SessionDetail>): SessionDetail => detail({
  status: "exited", exited_at: "2026-08-15T08:12:04.000Z",
  recovery_state: "recovering", presentation_state: "recovering", resume_attempts: 1, ...over,
});

function clock(t: Date): string {
  return [t.getHours(), t.getMinutes(), t.getSeconds()]
    .map((n) => String(n).padStart(2, "0"))
    .join(":");
}

test("recoveryFromSession derives only from the presented state, never inferred", () => {
  for (const presentation of ["running", "ended", "inactive"] as const) {
    assert.equal(recoveryFromSession(detail({ status: "exited", exit_code: 137, presentation_state: presentation })), null);
  }
  const evidence = {
    status: "exited", exit_code: 137, exit_signal: "SIGKILL",
    exited_at: "2026-08-15T08:12:04.000Z", resume_attempts: 5, recovery_state: "recovering",
  } as const;
  for (const presentation of ["running", "ended", "inactive"] as const) {
    assert.equal(recoveryFromSession(detail({ ...evidence, presentation_state: presentation })), null);
  }
  assert.equal(recoveryFromSession(detail({ ...evidence, presentation_state: "recovering" }))?.state, "restarting");
  // An unattachable session presents the unavailable register,
  // still derived from the server's presented state alone, never from
  // status or timers.
  const unavailable = recoveryFromSession(detail({ ...evidence, presentation_state: "attachment_unavailable" }));
  assert.equal(unavailable?.state, "unavailable");
  assert.equal(unavailable?.exitLabel, "exit 137");
  // The card carries the user's way back: Resume runs the
  // explicit recover, Start fresh launches a sibling. Both are live verbs
  // bound by the shell, not preview chrome.
  const card = recoveryStreamHtml(unavailable!);
  assert.match(card, /data-recovery-retry/);
  assert.match(card, />Resume</);
  assert.match(card, /data-recovery-fresh/);
});

test("a resume the provider did not continue presents a card, not a hidden ended row", () => {
  const ended = recoveryFromSession(detail({
    status: "exited", exit_code: 1, exited_at: "2026-08-15T08:12:04.000Z",
    native_session_id: "41f26fd3-5a17-41d0-8820-87534dc87be7",
    presentation_state: "resume_ended",
  }));
  assert.ok(ended !== null);
  assert.equal(ended.state, "resumeEnded");
  assert.equal(ended.exitLabel, "exit 1");
  assert.equal(ended.nativeID, "41f26fd3");
  const card = recoveryStreamHtml(ended);
  assert.match(card, /claude - exit 1 - resume ended/);
  assert.match(card, /resumed 41f26fd3/);
  assert.match(card, /did not continue the recorded conversation\. The session record and its output are kept/);
  const storeGone = recoveryFromSession(detail({
    status: "exited", exit_signal: "SIGKILL", presentation_state: "resume_ended", recovery_refusal: "store_gone",
  }));
  assert.equal(storeGone?.refusal, "store_gone");
  assert.match(recoveryStreamHtml(storeGone!), /store this conversation was recorded in is gone/);
  // Both ways forward are explicit verbs on the card: Resume asks the
  // provider again, Start fresh session launches a sibling. Nothing here
  // implies a fresh conversation was started for the user.
  assert.match(card, /data-recovery-retry[^>]*>Resume</);
  assert.match(card, /data-recovery-fresh[^>]*>Start fresh session</);
  assert.doesNotMatch(card, /relaunched/);
  assert.equal(recoveryComposerHtml(ended), "", "no process, no composer");
  assert.match(recoveryRegisterHtml(ended), /no process/);
  // Without an identity the card still stands on the exit alone.
  const anonymous = recoveryFromSession(detail({
    status: "exited", exit_signal: "SIGKILL", presentation_state: "resume_ended",
  }));
  assert.equal(anonymous?.nativeID, null);
  assert.match(recoveryStreamHtml(anonymous!), /claude - exit SIGKILL - resume ended/);
});

test("restarting swaps the status line for a spinner and drops the exit line", () => {
  const recovery = recoveryFromSession(recovering({ exit_code: 137 }));
  assert.ok(recovery !== null);
  assert.equal(recovery.state, "restarting");
  assert.equal(recovery.exitLabel, "exit 137");
  // The clock renders the user's local wall time, so the assertion
  // derives it from the server's UTC timestamp rather than pinning a zone.
  const local = clock(new Date("2026-08-15T08:12:04.000Z"));
  assert.equal(recovery.exitClock, local);
  // resume_attempts is the in-flight attempt (the backend persists 1 before
  // exposing recovering), so the mapper renders the presented value directly.
  assert.equal(recovery.attempt, 1);
  // The mapper's restarting register carries no budget yet.
  assert.match(recoveryRegisterHtml(recovery), /restarting claude - attempt 1</);
  const signaled = recoveryFromSession(recovering({ exit_code: undefined, exit_signal: "SIGKILL" }));
  assert.ok(signaled !== null);
  assert.equal(signaled.exitLabel, "exit SIGKILL");
  const noExit = recoveryFromSession(detail({
    status: "interrupted", recovery_state: "recovering", presentation_state: "recovering", resume_attempts: 1,
  }));
  assert.ok(noExit !== null);
  assert.equal(noExit.exitLabel, null);
  assert.equal(recoveryStreamHtml(noExit), "");
  assert.match(recoveryStreamHtml(referenceRecovery("restarting")), /exit 137 - 08:12:04/);
  const register = recoveryRegisterHtml(referenceRecovery("restarting"));
  assert.match(register, /restarting claude - attempt 2\/5/);
  assert.match(register, /- recording held - input queued/);
  assert.match(register, /animate-spinner/);
  assert.doesNotMatch(register, /accept edits on/);
  assert.match(recoveryComposerHtml(referenceRecovery("restarting")), /›/, "restarting keeps the composer");
});

test("replayed draws the centred rule and the resumed lines", () => {
  const replayed = recoveryFromSession(detail({
    presentation_state: "running", status: "running",
    replayed: { generation: 2, native_id: "46f913b4" },
  }));
  assert.ok(replayed !== null);
  assert.equal(replayed.state, "replayed");
  assert.equal(replayed.generation, 2);
  assert.equal(replayed.nativeID, "46f913b4");
  assert.equal(replayed.attempt, null);
  const stream = recoveryStreamHtml(referenceRecovery("replayed"));
  assert.match(stream, /08:12:04 - exit 137 - restarted - gen 2 - scrollback replayed/);
  assert.match(stream, /resumed 46f913b4 - gen 2 - context restored/);
  assert.match(stream, /claude --resume - 0 queued lost/);
});

test("failed owns the only bordered card and drops the composer", () => {
  const failed = recoveryFromSession(detail({
    presentation_state: "ended", status: "exited", exit_code: 1,
    exited_at: "2026-08-15T08:12:04.000Z",
    failure: {
      attempt_budget: 5, recording_bytes: 219136, queued: 2, resent: 0,
      panic: "panic: runtime error in provider.go:214",
    },
  }));
  assert.ok(failed !== null);
  assert.equal(failed.state, "failed");
  assert.equal(failed.exitLabel, "exit 1");
  assert.equal(failed.attemptBudget, 5);
  assert.equal(failed.failedBudget, "limit 5");
  assert.equal(failed.failedPanic, "panic: runtime error in provider.go:214");
  assert.equal(failed.recording, "recording 214 KB");
  assert.equal(failed.failedCounts, "recording 214 KB - 2 queued - 0 resent");
  const stream = recoveryStreamHtml(referenceRecovery("failed"));
  assert.match(stream, /claude - exit 1 - restart stopped/);
  assert.match(stream, /5 exits in 90s - limit 5/);
  assert.match(stream, /panic: runtime error in provider.go:214/);
  assert.match(stream, /recording 214 KB - 2 queued - 0 resent/);
  assert.match(stream, /border-error/);
  assert.match(stream, /Start fresh session/);
  assert.match(stream, /Retry/);
  assert.equal(recoveryComposerHtml(referenceRecovery("failed")), "", "the failed composer is gone");
  const register = recoveryRegisterHtml(referenceRecovery("failed"));
  assert.match(register, /no process/);
  assert.match(register, /recording 214 KB/);
  assert.doesNotMatch(register, /accept edits on/);
});

test("the live state keeps the unchanged live output line", () => {
  assert.match(recoveryStreamHtml(referenceRecovery("live")), /✻ Thinking…/);
  const register = recoveryRegisterHtml(referenceRecovery("live"));
  assert.match(register, /accept edits on/);
  // Each segment is its own span so the row's 7px flex gap falls on both
  // sides of the · separator; merging them collapses that side to a space.
  assert.match(register, /text-\(--t-accent\)">▶▶</);
  assert.match(register, /text-\(--t-accent\)">accept edits on</);
  assert.match(register, /<span>·<\/span>/);
  assert.match(register, /<span>← 1 agent<\/span>/);
});

test("server-presented machine values are escaped, never trusted as markup", () => {
  const recovery = recoveryFromSession(recovering({ provider: "claude & <friends>", exit_code: 137 }));
  assert.ok(recovery !== null);
  const html = recoveryRegisterHtml(recovery);
  assert.ok(!html.includes("claude & <friends>"));
  assert.ok(html.includes("claude &amp; &lt;friends&gt;"));
});
