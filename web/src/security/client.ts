import { csrfToken } from "../api/http.ts";
import type { MethodEnvelope, MethodKind } from "./envelope.ts";

export interface MethodRef { id: string; revision: number }
export type EncryptionPolicy = "required" | "optional" | "unavailable";
export interface OnboardingState {
  policy: EncryptionPolicy;
  protection: "undecided" | "encrypted" | "none";
  started: boolean;
  setupComplete: boolean;
}
export interface SecurityMethod extends MethodRef {
  kind: MethodKind;
  label: string;
  status: "pending" | "active" | "removed";
  salt: string;
  envelope?: MethodEnvelope;
  verifiedAt?: string;
  credentialId?: string;
}
export interface SecurityState {
  instanceId: string;
  phase: "invitation-required" | "awaiting-protection" | "awaiting-start" | "awaiting-method" | "awaiting-recovery" | "initializing" | "ready";
  onboarding: OnboardingState;
  ownerBound: boolean;
  workspaceAuthorized: boolean;
  plainOperationId?: string;
  commitment?: string;
  storage: "closed" | "open" | "unencrypted" | "unknown";
  runtime: "stopped" | "starting" | "running" | "stopping" | "unknown";
  locked: boolean;
  migration?: "pending" | "complete";
  browserGranted: boolean;
  initializingOperation?: string;
  initializingMethod?: MethodRef;
}
export type SecurityOperation = "verify-method" | "resume-enrollment" | "resume-initialization" |
  "initialize" | "authorize-browser" | "unlock" | "add-method" | "replace-method" | "remove-method" | "rename-method" | "lock";
export interface SecurityAction {
  operation: SecurityOperation;
  method: MethodRef;
  target?: MethodRef;
  replacement?: MethodRef;
  recovery?: MethodRef;
  label?: string;
}
export interface SecurityChallenge {
  id: string;
  operationId: string;
  challenge: string;
  expiresAt: string;
  assertion?: { publicKey: Record<string, unknown> };
}
export interface OperationReceipt { id: string; status: string }
export interface OperationObservation extends OperationReceipt { storage: SecurityState["storage"]; runtime: SecurityState["runtime"]; locked: boolean }
export interface PreparedMethod { method: SecurityMethod; registration?: { publicKey: Record<string, unknown> } }

export class SecurityError extends Error {
  readonly status: number;
  readonly code: string;
  constructor(status: number, code: string) {
    super(status === 401 ? "Sign in again to continue." : "The workspace could not complete this request.");
    this.status = status; this.code = code;
  }
}

export function parseOnboardingState(value: unknown): OnboardingState {
  if (!value || typeof value !== "object") throw new Error("Workspace setup state is unavailable.");
  const state = value as OnboardingState;
  if (!["required", "optional", "unavailable"].includes(state.policy) ||
      !["undecided", "encrypted", "none"].includes(state.protection) ||
      typeof state.started !== "boolean" || typeof state.setupComplete !== "boolean" ||
      (!state.started && (state.setupComplete || state.protection !== "undecided")) ||
      (state.setupComplete && state.protection === "undecided") ||
      (state.policy === "required" && state.protection === "none") ||
      (state.policy === "unavailable" && state.protection === "encrypted")) {
    throw new Error("Workspace setup state is unavailable.");
  }
  return state;
}

export function parseSecurityState(value: unknown): SecurityState {
  if (!value || typeof value !== "object") throw new Error("Workspace security state is unavailable.");
  const state = value as SecurityState;
  const onboarding = parseOnboardingState(state.onboarding);
  if (typeof state.instanceId !== "string" || !/^[A-Za-z0-9_-]{1,128}$/.test(state.instanceId) ||
      !["invitation-required", "awaiting-protection", "awaiting-start", "awaiting-method", "awaiting-recovery", "initializing", "ready"].includes(state.phase) ||
      !["closed", "open", "unencrypted", "unknown"].includes(state.storage) ||
      !["stopped", "starting", "running", "stopping", "unknown"].includes(state.runtime) ||
      typeof state.locked !== "boolean" || typeof state.browserGranted !== "boolean" ||
      (state.migration !== undefined && !["pending", "complete"].includes(state.migration)) ||
      (state.locked && (state.storage !== "closed" || state.runtime !== "stopped")) ||
      typeof state.ownerBound !== "boolean" || state.ownerBound !== onboarding.started ||
      typeof state.workspaceAuthorized !== "boolean" || (state.workspaceAuthorized && !onboarding.setupComplete) ||
      ((state.phase === "ready") !== onboarding.setupComplete) ||
      (state.storage === "unencrypted" && onboarding.protection !== "none") ||
      (state.browserGranted && (onboarding.protection !== "encrypted" || !onboarding.setupComplete)) ||
      (onboarding.protection === "none" && (state.locked || state.commitment !== undefined || state.initializingOperation !== undefined ||
        !["unknown", "unencrypted"].includes(state.storage) ||
        typeof state.plainOperationId !== "string" || !/^[A-Za-z0-9_-]{43}$/.test(state.plainOperationId))) ||
      (onboarding.protection !== "none" && state.plainOperationId !== undefined) ||
      (state.commitment !== undefined && typeof state.commitment !== "string") ||
      (state.initializingOperation !== undefined && typeof state.initializingOperation !== "string") ||
      (state.initializingMethod !== undefined && (!state.initializingMethod || typeof state.initializingMethod.id !== "string" || !Number.isInteger(state.initializingMethod.revision) || state.initializingMethod.revision < 1))) {
    throw new Error("Workspace security state is unavailable.");
  }
  return state;
}

function parseOperationObservation(value: unknown): OperationObservation {
  if (!value || typeof value !== "object") throw new Error("The operation result is unavailable.");
  const observed = value as OperationObservation;
  if (typeof observed.id !== "string" || typeof observed.status !== "string" || typeof observed.locked !== "boolean" ||
      !["closed", "open", "unencrypted", "unknown"].includes(observed.storage) ||
      !["stopped", "starting", "running", "stopping", "unknown"].includes(observed.runtime)) {
    throw new Error("The operation result is unavailable.");
  }
  return observed;
}

export function mayOpenWorkspace(state: SecurityState): boolean {
  if (state.phase !== "ready" || !state.onboarding.setupComplete || !state.workspaceAuthorized || state.runtime !== "running" || state.locked) return false;
  return state.onboarding.protection === "none"
    ? state.storage === "unencrypted" && !state.browserGranted && !state.locked
    : state.onboarding.protection === "encrypted" && state.browserGranted && state.storage === "open";
}

// Only the gateway response is authoritative. A previous browser grant or an
// open filesystem alone must never admit a Google-only browser to private APIs.
export class SecurityClient {
  onAuthorizationLost?: () => void;
  private readonly request: typeof fetch;
  private readonly cookie: () => string;
  constructor(request: typeof fetch = (input, init) => fetch(input, init), cookie: () => string = () => document.cookie) {
    this.request = request; this.cookie = cookie;
  }

  private async send(path: string, method = "GET", body?: unknown, signal?: AbortSignal): Promise<unknown> {
    const headers = new Headers();
    if (method !== "GET") {
      const csrf = csrfToken(this.cookie());
      if (!csrf) throw new SecurityError(403, "csrf-unavailable");
      headers.set("X-CSRF-Token", csrf);
      headers.set("Content-Type", "application/json");
    }
    const response = await this.request(`/api/security/v1${path}`, {
      method, credentials: "same-origin", cache: "no-store", redirect: "error", headers, signal,
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    });
    if (response.headers.get("X-Deck-Workspace-Security") !== "v1") throw new Error("The workspace did not identify its security service.");
    if (!response.ok) {
      if (response.status === 401) this.onAuthorizationLost?.();
      let code = "security-unavailable";
      try { const value = await response.json(); if (typeof value.code === "string") code = value.code; } catch { /* Error bodies can be empty after a proxy failure. */ }
      throw new SecurityError(response.status, code);
    }
    return response.status === 204 ? undefined : response.json();
  }

  async state(signal?: AbortSignal): Promise<SecurityState> { return parseSecurityState(await this.send("/state", "GET", undefined, signal)); }
  async onboarding(signal?: AbortSignal): Promise<OnboardingState> { return parseOnboardingState(await this.send("/onboarding", "GET", undefined, signal)); }
  async selectProtection(protection: "encrypted" | "none", signal?: AbortSignal): Promise<OnboardingState> {
    return parseOnboardingState(await this.send("/onboarding/protection", "POST", { protection }, signal));
  }
  async startPlain(operationId?: string, signal?: AbortSignal): Promise<OperationReceipt> {
    const value = await this.send("/onboarding/start", "POST", operationId ? { operationId } : {}, signal) as OperationReceipt;
    if (!value || typeof value.id !== "string" || !/^[A-Za-z0-9_-]{43}$/.test(value.id) || !["accepted", "unconfirmed"].includes(value.status) || (operationId && value.id !== operationId)) {
      throw new Error("The workspace did not return its start operation reference.");
    }
    return value;
  }
  async observePlain(id: string, signal?: AbortSignal): Promise<OperationObservation> {
    const observed = parseOperationObservation(await this.send(`/onboarding/operations/${encodeURIComponent(id)}`, "GET", undefined, signal));
    if (observed.id !== id || !["unknown", "unencrypted"].includes(observed.storage) || observed.locked) throw new Error("The workspace start result is unavailable.");
    return observed;
  }
  async handoff(token: string, signal?: AbortSignal): Promise<void> {
    const response = await this.request("/api/security/v1/invitation/handoff", {
      method: "POST", credentials: "same-origin", cache: "no-store", redirect: "error",
      headers: { "Content-Type": "application/json" }, body: JSON.stringify({ token }), signal,
    });
    if (response.headers.get("X-Deck-Workspace-Security") !== "v1" || !response.ok) throw new SecurityError(response.status, "invitation-unavailable");
  }
  async redeem(token?: string, signal?: AbortSignal): Promise<void> { await this.send("/invitation/redeem", "POST", token ? { token } : {}, signal); }
  async methods(management = false): Promise<SecurityMethod[]> {
    const value = await this.send(management ? "/methods" : "/unlock-options");
    if (!Array.isArray(value) || value.length > 64) throw new Error("Unlock methods are unavailable.");
    return value as SecurityMethod[];
  }
  async prepare(kind: MethodKind, label: string, signal?: AbortSignal): Promise<PreparedMethod> { return await this.send("/methods/prepare", "POST", { kind, label }, signal) as PreparedMethod; }
  async label(method: MethodRef, label: string): Promise<void> { await this.send(`/methods/${encodeURIComponent(method.id)}/label`, "PATCH", { label }); }
  async store(method: MethodRef, envelope: MethodEnvelope, commitment: string, registration?: unknown, signal?: AbortSignal): Promise<void> {
    await this.send(`/methods/${encodeURIComponent(method.id)}/envelope`, "PUT", { envelope, commitment, ...(registration ? { registration } : {}) }, signal);
  }
  async envelope(method: MethodRef, signal?: AbortSignal): Promise<unknown> { return this.send(`/methods/${encodeURIComponent(method.id)}/envelope`, "GET", undefined, signal); }
  async challenge(action: SecurityAction, signal?: AbortSignal): Promise<SecurityChallenge> { return await this.send("/challenges", "POST", action, signal) as SecurityChallenge; }
  async operate(body: { challengeId: string; signature: string; assertion?: unknown; volumeSecret?: string }, signal?: AbortSignal): Promise<OperationReceipt> {
    return await this.send("/operations", "POST", body, signal) as OperationReceipt;
  }
  async observe(id: string, signal?: AbortSignal): Promise<OperationObservation> { return parseOperationObservation(await this.send(`/operations/${encodeURIComponent(id)}`, "GET", undefined, signal)); }
}
