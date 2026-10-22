// ProjectPreviewEntry is the Open preview answer: the exact entry
// URL the one-use ticket must be POSTed to, and the ticket itself. The
// URL is submitted as a form body, never as a URL parameter, so nothing
// here is safe to log or embed.
export interface ProjectPreviewEntry {
  url: string;
  ticket: string;
}

export interface ProjectPreviewSettings {
  configured: boolean;
  self_service: boolean;
  enabled: boolean;
  source?: "managed" | "static";
  origin?: string;
  port?: number;
  upstream?: string;
}

export interface ProjectPreviewCheck {
  reachable: boolean;
  checked_at: string;
}
