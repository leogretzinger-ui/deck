import test from "node:test";
import assert from "node:assert/strict";
import { ApiError } from "../api/http.ts";
import type { ApiClient } from "../api/contracts.ts";
import type { ProjectPreviewSettings } from "../api/project-preview.ts";
import { ProjectPreviewSettingsModel, ProjectPreviewObservations, previewConnectionLabel, previewCheckedWhen, projectPreviewSettingsHtml } from "./project-preview-settings.ts";
import { projectPreviewMenuHtml } from "./session-menu.ts";

const enabled: ProjectPreviewSettings = { configured: true, self_service: true, enabled: true, source: "managed", origin: "https://app.preview.example", port: 41207 };
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}
function model(api: Partial<ApiClient>): ProjectPreviewSettingsModel {
  return new ProjectPreviewSettingsModel(api as ApiClient, "project/1", "project <one>");
}

test("enabling never infers a dev server or connectivity observation", async () => {
  const m = model({ getProjectPreviewSettings: async () => ({ ...enabled, enabled: false }), enableProjectPreview: async () => enabled });
  await m.load();
  await m.change(true);
  assert.equal(m.phase, "setup");
  assert.equal(m.check, null);
  assert.equal(previewConnectionLabel(m), "Not checked yet");
  assert.match(projectPreviewSettingsHtml(m), /connectivity hasn&#39;t been checked|connectivity hasn't been checked/);
});

test("a failed disable keeps retry even when reconciliation observes revoked gateway access", async () => {
  let calls = 0;
  const m = model({
    getProjectPreviewSettings: async () => ({ ...enabled, enabled: calls === 0 }),
    disableProjectPreview: async () => { calls++; throw new Error("write failed after gateway revocation"); },
  });
  await m.load();
  m.phase = "disableconfirm";
  await m.change(false);
  assert.equal(m.settings?.enabled, false);
  assert.equal(m.phase, "disableconfirm");
  assert.equal(m.issue?.title, "Disable outcome unconfirmed");
  assert.match(projectPreviewSettingsHtml(m), /Try again/);
  await m.change(false);
  assert.equal(calls, 2, "reconciled runtime false must not prevent retrying durable disable");
});

test("a lost enable response reconciles assignment without claiming success", async () => {
  let written = false;
  const m = model({
    getProjectPreviewSettings: async () => ({ ...enabled, enabled: written }),
    enableProjectPreview: async () => { written = true; throw new TypeError("network lost"); },
  });
  await m.load();
  await m.change(true);
  assert.equal(m.phase, "settings");
  assert.equal(m.settings?.origin, enabled.origin);
  assert.equal(m.issue?.title, "Enable outcome unconfirmed");
  assert.equal(m.check, null);
});

test("exhausted reservations never suggest disabling another preview frees capacity", async () => {
  const m = model({
    getProjectPreviewSettings: async () => ({ ...enabled, enabled: false }),
    enableProjectPreview: async () => { throw new ApiError(409, "preview_ports_exhausted", "full"); },
  });
  await m.load(); await m.change(true);
  assert.equal(m.phase, "enable");
  assert.match(m.issue!.detail, /Disabling a preview keeps its reserved port/);
});

test("a disposed request cannot reopen or repaint the old project's dialog", async () => {
  const response = deferred<ProjectPreviewSettings>();
  const m = model({ getProjectPreviewSettings: () => response.promise });
  let paints = 0; m.onChange = () => { paints++; };
  const loading = m.load();
  m.dispose();
  response.resolve(enabled);
  await loading;
  assert.equal(paints, 1);
  assert.equal(m.settings, null);
});

test("a later settings request wins over a slower earlier response", async () => {
  const response = deferred<ProjectPreviewSettings>();
  let calls = 0;
  const m = model({ getProjectPreviewSettings: () => ++calls === 1 ? response.promise : Promise.resolve({ ...enabled, enabled: false }) });
  const old = m.load();
  await m.load();
  response.resolve(enabled); await old;
  assert.equal(m.phase, "enable");
  assert.equal(m.settings?.enabled, false);
});

test("pending mutations cannot be submitted twice", async () => {
  const response = deferred<ProjectPreviewSettings>(); let calls = 0;
  const m = model({ getProjectPreviewSettings: async () => ({ ...enabled, enabled: false }), enableProjectPreview: () => { calls++; return response.promise; } });
  await m.load();
  const first = m.change(true); await m.change(true);
  assert.equal(calls, 1);
  response.resolve(enabled); await first;
  assert.equal(m.busy, null);
});

test("a failed check clears stale success and never fabricates a timestamp", async () => {
  let fail = false;
  const m = model({ getProjectPreviewSettings: async () => enabled, checkProjectPreview: async () => { if (fail) throw new Error("offline"); return { reachable: true, checked_at: "2026-09-21T00:00:00Z" }; } });
  await m.load(); await m.checkConnectivity();
  assert.equal(previewConnectionLabel(m), "Accepting connections");
  fail = true; await m.checkConnectivity();
  assert.equal(previewConnectionLabel(m), "Check failed");
  assert.equal(m.check, null);
});

test("static registrations stay inspectable without mutation controls", async () => {
  const m = model({ getProjectPreviewSettings: async () => ({ ...enabled, source: "static", self_service: false }) });
  await m.load();
  const html = projectPreviewSettingsHtml(m);
  assert.match(html, /data-preview-open/);
  assert.doesNotMatch(html, /data-preview-confirm-disable/);
  assert.match(html, /managed by the installation/);
});

test("unconfigured and static-only installations have distinct states", async () => {
  for (const [configured, phase] of [[false, "unconfigured"], [true, "static-only"]] as const) {
    const m = model({ getProjectPreviewSettings: async () => ({ configured, self_service: false, enabled: false }) });
    await m.load(); assert.equal(m.phase, phase);
    assert.doesNotMatch(projectPreviewSettingsHtml(m), /data-preview-enable/);
  }
});

test("assignment data is text and the dialog is labelled by its escaped heading", async () => {
  const m = model({ getProjectPreviewSettings: async () => ({ ...enabled, enabled: false }) }); await m.load();
  const html = projectPreviewSettingsHtml(m);
  assert.match(html, /Preview project &lt;one&gt;/);
  assert.match(html, /aria-labelledby="project-preview-title"/);
  assert.match(html, /id="project-preview-title"/);
  assert.doesNotMatch(html, /Copy agent instructions|Start server|Stop server/);
});

test("menu offers entry only from observed enabled settings", () => {
  assert.doesNotMatch(projectPreviewMenuHtml(), /data-project-menu-preview(?:\s|>)/);
  assert.match(projectPreviewMenuHtml(enabled), /Open preview/);
  assert.match(projectPreviewMenuHtml(enabled), /Preview settings…/);
  assert.match(projectPreviewMenuHtml({ configured: false, self_service: false, enabled: false }), /Not set up/);
});

test("check ages are derived from the server timestamp", () => {
  assert.equal(previewCheckedWhen("2026-09-21T00:00:00Z", Date.parse("2026-09-21T00:00:02Z")), "just now");
  assert.equal(previewCheckedWhen("2026-09-21T00:00:00Z", Date.parse("2026-09-21T00:00:05Z")), "5 s ago");
});


test("preview observations stay project-local and reject changed assignments", () => {
  const cache = new ProjectPreviewObservations();
  const observation = { check: { reachable: true, checked_at: "2026-09-21T00:00:00Z" }, checkFailed: false };
  cache.remember("one", enabled, observation);
  assert.deepEqual(cache.get("one", enabled), observation);
  assert.equal(cache.get("two", enabled), null);
  assert.equal(cache.get("one", { ...enabled, port: 41208 }), null);
  assert.equal(cache.get("one", enabled), null, "an old assignment cannot revive a cleared observation");
  cache.remember("one", enabled, observation);
  assert.equal(cache.get("one", { ...enabled, enabled: false }), null);
});

test("only explicit checks publish observations; registration attempts clear them", async () => {
  const observations: unknown[] = [];
  const m = model({
    getProjectPreviewSettings: async () => enabled,
    checkProjectPreview: async () => ({ reachable: false, checked_at: "2026-09-21T00:00:00Z" }),
    disableProjectPreview: async () => { throw new Error("uncertain disable"); },
  });
  m.onObservation = (value) => { observations.push(value); };
  await m.load();
  assert.deepEqual(observations, [], "settings reads never claim a connectivity observation");
  await m.checkConnectivity();
  assert.deepEqual(observations, [{ check: { reachable: false, checked_at: "2026-09-21T00:00:00Z" }, checkFailed: false }]);
  await m.change(false);
  assert.equal(observations.at(-1), null, "even an uncertain mutation invalidates the earlier observation");
  assert.equal(m.check, null);
});

test("failed checks publish a distinct failure rather than a negative TCP result", async () => {
  const m = model({ getProjectPreviewSettings: async () => enabled, checkProjectPreview: async () => { throw new Error("offline"); } });
  let observed: unknown;
  m.onObservation = (value) => { observed = value; };
  await m.load(); await m.checkConnectivity();
  assert.deepEqual(observed, { check: null, checkFailed: true });
});

test("the preview menu preserves the open action name and describes the last explicit check", () => {
  const html = projectPreviewMenuHtml(enabled, { check: { reachable: true, checked_at: "2026-09-21T00:00:00Z" }, checkFailed: false });
  assert.match(html, /aria-label="Open preview"/);
  assert.match(html, /aria-describedby="project-preview-menu-status"/);
  assert.match(html, /Accepting connections/);
  assert.doesNotMatch(html, /Not checked yet/);
  assert.match(projectPreviewMenuHtml(enabled, { check: null, checkFailed: true }), /Check failed/);
  assert.match(projectPreviewMenuHtml(enabled), /Not checked yet/);
});


test("an enabled preview's setup description reflects completed connectivity attempts", async () => {
  for (const outcome of ["accepting", "not-responding", "failed"] as const) {
    const m = model({
      getProjectPreviewSettings: async () => ({ ...enabled, enabled: false }),
      enableProjectPreview: async () => enabled,
      checkProjectPreview: async () => {
        if (outcome === "failed") throw new Error("check unavailable");
        return { reachable: outcome === "accepting", checked_at: "2026-09-21T00:00:00Z" };
      },
    });
    await m.load();
    await m.change(true);
    assert.match(projectPreviewSettingsHtml(m), /connectivity hasn't been checked/);
    await m.checkConnectivity();
    assert.equal(m.phase, "setup");
    const html = projectPreviewSettingsHtml(m);
    assert.doesNotMatch(html, /connectivity hasn't been checked/, outcome);
    assert.match(html, /has a stable private URL/);
    assert.match(html, new RegExp(outcome === "accepting" ? "Accepting connections" : outcome === "not-responding" ? "Not responding" : "Check failed"));
  }
});
