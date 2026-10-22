// isSafeUrl gates links opened from terminal content: only
// http and https may open. Everything else — javascript:, data:, file:,
// vbscript:, and anything that does not parse — is refused. The check is
// deliberately on the parsed scheme, never on string prefixes.
export function isSafeUrl(raw: string): boolean {
  try {
    const u = new URL(raw);
    return u.protocol === "http:" || u.protocol === "https:";
  } catch {
    return false;
  }
}
