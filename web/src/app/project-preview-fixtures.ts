// Synthetic responses exercise the shipped preview controller and renderer.
import { ApiError } from "../api/http.ts";
import type { ApiClient } from "../api/contracts.ts";
import type { ProjectPreviewSettings } from "../api/project-preview.ts";
import type { ProjectPreviewParam } from "../preview/preview-states.ts";
import type { ProjectPreviewSettingsDialog } from "./project-preview-settings.ts";

const ASSIGNMENT = { origin: "https://rewrite-scheduler.preview.atlas-7.deck.example", port: 41207, upstream: "127.0.0.1:41207", source: "managed" as const };
const pending = <T>(): Promise<T> => new Promise(() => {});

export function configureProjectPreviewFixture(api: ApiClient, state: ProjectPreviewParam): void {
  const disabled = ["enable", "enabling", "failed", "noports", "unconfigured", "static-only", "loading", "menu-disabled", "menu-unconfigured"].includes(state);
  let settings: ProjectPreviewSettings = {
    configured: state !== "unconfigured" && state !== "menu-unconfigured",
    self_service: state !== "static-only" && state !== "static",
    enabled: !disabled,
    ...(!disabled ? ASSIGNMENT : {}),
    ...(state === "static" ? { source: "static" as const } : {}),
  };
  api.getProjectPreviewSettings = async () => state === "loading" ? pending() : { ...settings };
  api.enableProjectPreview = async () => {
    if (state === "enabling") return pending();
    if (state === "failed") throw new Error("Synthetic unavailable response");
    if (state === "noports") throw new ApiError(409, "preview_ports_exhausted", "No preview port is available.");
    settings = { ...settings, ...ASSIGNMENT, enabled: true };
    return { ...settings };
  };
  api.disableProjectPreview = async () => {
    if (state === "disablefailed") throw new Error("Synthetic unavailable response");
    settings = { ...settings, enabled: false };
    return { ...settings };
  };
  api.checkProjectPreview = async () => {
    if (state === "checking") return pending();
    if (state === "checkfailed") throw new Error("Synthetic unavailable response");
    return { reachable: state === "up", checked_at: new Date().toISOString() };
  };
}

export async function applyProjectPreviewFixture(dialog: ProjectPreviewSettingsDialog, state: ProjectPreviewParam, details: boolean): Promise<void> {
  const model = dialog.model;
  model.detailsOpen = details;
  if (["enabling", "failed", "noports"].includes(state)) {
    const result = model.change(true);
    if (state !== "enabling") await result;
  }
  if (state === "setup") model.phase = "setup";
  if (["checking", "up", "down", "checkfailed"].includes(state)) {
    const result = model.checkConnectivity();
    if (state !== "checking") await result;
  }
  if (["disableconfirm", "disablefailed", "disabled"].includes(state)) {
    model.phase = "disableconfirm";
    if (state !== "disableconfirm") await model.change(false);
  }
  model.changed();
}
