import type { UploadConflictInfo } from "./files.ts";

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  // conflict is the upload endpoint's 409 ask when the refusal is one:
  // what holds the destination name, and the version whose return as
  // consent approves replacing it. Null on every other error, including
  // a 409 from anywhere else.
  readonly conflict: UploadConflictInfo | null;

  // NOTE: no TS parameter properties — the node test runner strips types
  // only and cannot compile them.
  constructor(status: number, code: string, message: string, conflict: UploadConflictInfo | null = null) {
    super(message);
    this.status = status;
    this.code = code;
    this.conflict = conflict;
  }
}

export type AuthMode = "none" | "password" | "oidc" | "builtin" | "trusted_proxy";

export async function loginWithPassword(password: string): Promise<void> {
  await readJSON<void>(await fetch("/auth/password", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    credentials: "same-origin",
    cache: "no-store",
    body: JSON.stringify({ password }),
  }));
}

// Password and OIDC login issue a readable, session-bound CSRF cookie alongside
// the HttpOnly session cookie. State-changing requests reflect that value in
// the header the backend verifies. Trusted-proxy deployments simply have no
// such cookie, so the header is omitted there.
export function csrfToken(cookie: string): string | null {
  const names = new Set(["__Host-noisedeck_csrf", "noisedeck_csrf"]);
  const values = new Map<string, string>();
  for (const part of cookie.split(";")) {
    const item = part.trim();
    const eq = item.indexOf("=");
    if (eq < 1 || !names.has(item.slice(0, eq))) {
      continue;
    }
    values.set(item.slice(0, eq), item.slice(eq + 1));
  }
  const encoded = values.get("__Host-noisedeck_csrf") ?? values.get("noisedeck_csrf");
  if (encoded === undefined) return null;
  try {
    return decodeURIComponent(encoded);
  } catch {
    return null;
  }
}

export async function readJSON<T>(resp: Response): Promise<T> {
  if (!resp.ok) {
    let code = "unknown";
    let message = resp.statusText;
    try {
      // The backend has two envelopes: flat {error: code, message} and
      // nested {error: {code, message}}. Both decode to the same ApiError —
      // the caller never sees the difference.
      const body = (await resp.json()) as {
        error?: string | { code?: string; message?: string };
        message?: string;
      };
      if (typeof body.error === "string") {
        code = body.error;
        message = body.message ?? message;
      } else if (body.error !== null && typeof body.error === "object") {
        code = body.error.code ?? code;
        message = body.error.message ?? message;
      }
    } catch {
      // a non-JSON error body is still an error, just an opaque one
    }
    throw new ApiError(resp.status, code, message);
  }
  // 204 No Content is a success with no body — parsing one as JSON would
  // turn a successful call into a thrown SyntaxError.
  if (resp.status === 204) {
    return undefined as T;
  }
  return (await resp.json()) as T;
}

// The 403 bodies that mean the browser lost its authorization rather
// than that the endpoint refused: the codes the control plane answers
// with while a re-authentication ceremony is pending. Both transports
// read the same list, and the Files model reads it too, so all three
// agree on what "authorization lost" means.
export const AUTHORIZATION_LOSS_CODES: readonly string[] = [
  "unlock-required", "authorization-required", "browser-grant-required",
];

export async function fetchWithAuthorization(input: RequestInfo | URL, init?: RequestInit, onAuthorizationLost?: () => void): Promise<Response> {
  const response = await fetch(input, init);
  if (onAuthorizationLost && response.status === 401) onAuthorizationLost();
  else if (onAuthorizationLost && response.status === 403) {
    let code: unknown;
    try { code = (await response.clone().json())?.code; }
    catch { return response; }
    if (typeof code === "string" && AUTHORIZATION_LOSS_CODES.includes(code)) onAuthorizationLost();
  }
  return response;
}
