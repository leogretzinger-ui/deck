// Source figure IDs are stable capture routes; synthetic values live in security-preview.ts.
import { RUNTIME_PREVIEW_FIGURES } from "./security-runtime-preview.ts";
export const ENROLLMENT_PREVIEW_FIGURES = ["1a", "1b", "1c", "1d", "1m", "2a", "2b", "2c", "2d", "2e", "2f", "2m", "2n", "3a", "3b", "3c", "3d", "3e", "3m", "3n"] as const;
export type EnrollmentPreviewFigure = typeof ENROLLMENT_PREVIEW_FIGURES[number];
export const MANAGEMENT_PREVIEW_FIGURES = ["6a", "6b", "6c", "6d", "6e", "6m", "7b"] as const;
export const SECURITY_PREVIEW_FIGURES = [...ENROLLMENT_PREVIEW_FIGURES, ...RUNTIME_PREVIEW_FIGURES, ...MANAGEMENT_PREVIEW_FIGURES] as const;
export type SecurityPreviewFigure = typeof SECURITY_PREVIEW_FIGURES[number];

export function parseSecurityPreview(params: URLSearchParams): SecurityPreviewFigure | null {
  const raw = params.get("security");
  if (raw === null) return null;
  if (!(SECURITY_PREVIEW_FIGURES as readonly string[]).includes(raw)) throw new Error(`invalid security: ${JSON.stringify(raw)}`);
  if ([...params.keys()].some(key => key !== "security" && key !== "theme")) throw new Error("Security preview cannot be combined with workspace fixture axes.");
  return raw as SecurityPreviewFigure;
}

export const SECURITY_PREVIEW_STATES = SECURITY_PREVIEW_FIGURES.map(figure => ({
  name: `security-${figure}`,
  params: { security: figure },
  ready: `[data-security-figure="${figure}"] ${(figure.startsWith("6") || figure === "7b") ? "[data-method-management]" : ".enrollment"}`,
  viewport: figure.endsWith("m") || figure.endsWith("n") ? { width: 393, height: 852 }
    : { width: 1120, height: figure === "1a" ? 900 : 720 },
}));
