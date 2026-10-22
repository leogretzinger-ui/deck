// Value formatters shared by the workspace panels. Sizes and elapsed
// times render on more than one surface and their units are fixed,
// so they live in one place rather than once per consumer.

// formatFileSize renders a byte count compactly. Decimal KB/MB with one
// decimal; only zero keeps the byte unit.
export function formatFileSize(bytes: number): string {
  if (bytes === 0) {
    return "0 B";
  }
  const kb = bytes / 1000;
  if (kb < 1000) {
    return `${kb.toFixed(1)} KB`;
  }
  return `${(bytes / (1000 * 1000)).toFixed(1)} MB`;
}

// formatSince renders a timestamp as compact elapsed time in the design's
// units: < 1m, 4m, 2h, 5d. Numbers over adjectives. The space
// after the comparison operator is not decorative: Euclid Flex gives "<"
// almost no right side bearing, so at caption size the vertex runs into the
// 1's flag and the pair reads as one damaged glyph. Returns null
// for a missing or unparseable timestamp — the caller then shows nothing
// rather than a guess.
export function formatSince(iso: string | undefined, now: number): string | null {
  if (iso === undefined || iso === "") return null;
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return null;
  const s = Math.max(0, Math.floor((now - t) / 1000));
  if (s < 60) return "< 1m";
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h`;
  return `${Math.floor(h / 24)}d`;
}
