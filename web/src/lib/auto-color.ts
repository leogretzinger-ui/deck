// The Auto-color projects preference: a local toggle that gives
// every project a distinct color from the palette. There is no backend
// setting for it yet, so it persists in localStorage like the info-bar
// preference. Storage may be blocked (private mode, a quota, a disabled
// origin), so both the read and the write are guarded: an unreadable
// preference keeps the toggle off, and a blocked write applies for this
// page only.
import { SESSION_COLOR_TAGS } from "./session-colors.ts";

// AutoColorRow is the structural slice of a sidebar row group this ramp
// reads and tints; the sidebar's full row type satisfies it.
export interface AutoColorRow {
  project: { id: string; name: string };
  color?: string | null;
  autoColor?: string | null;
}

const STORAGE_KEY = "noisedeck-auto-color-projects";

// showAutoColorProjects reads the persisted preference. Anything but an
// explicit "on" keeps it off, so a storage read that throws does too.
export function showAutoColorProjects(): boolean {
  try {
    return localStorage.getItem(STORAGE_KEY) === "on";
  } catch {
    return false;
  }
}

// setAutoColorProjects persists the choice and returns the applied value, so
// the menu can render exactly what took effect. A blocked write must not
// abort the toggle; the preference simply fails to persist.
export function setAutoColorProjects(value: boolean): boolean {
  try {
    localStorage.setItem(STORAGE_KEY, value ? "on" : "off");
  } catch {
    // Blocked storage: the choice applies for this page only.
  }
  return value;
}

// autoColorWheel is the eleven-hue wheel auto-color samples:
// the tag palette's marks in circle order, neutrals
// excluded. A tag is on the wheel exactly when its value names a mark
// var — "no color" carries null and the neutral identities are pinned
// raw hex, not mark vars — so the wheel holds wherever the strip lists
// its neutrals; the marks are never assumed to be the palette's first
// entries.
export function autoColorWheel(): readonly string[] {
  return SESSION_COLOR_TAGS
    .filter((tag) => tag.value?.startsWith("var(--color-mark-") ?? false)
    .map((tag) => tag.key);
}

// autoColorHues hands each displayed row a hue. The rule has two regimes,
// one on each side of the wheel size, and both serve one requirement: two
// rows side by side must be distinguishable.
//
// Up to eleven rows the wheel is sampled at even intervals: row i takes
// wheel slot round(i × 11 / n). The intervals span the
// whole wheel, so the ramp reads as a full rainbow at any count, and
// neighbours at low counts sit far apart on the wheel — two projects take
// crimson and cerulean, half a wheel apart, not crimson and vermilion.
// The round step closes the circle, and two consequences are deliberate:
// just under the wheel size it can skip a hue (ten of eleven take every
// hue but teal, because round(5.5) rounds up and the intervals stay
// even), and wheel neighbours can land on the ends of a run, because the
// sample wraps the way the wheel it samples does. Even spacing is the
// requirement; the ends of a run get no special treatment.
//
// Twelve rows cannot have eleven unique hues, and the even intervals
// collapse before the wheel runs out: with a stride under one slot,
// round(i × 11 / 12) lands two ADJACENT rows on cerulean, and two
// side-by-side rows in one hue read as one project. Past the wheel size
// the ramp therefore walks the wheel two slots per row: row i takes slot
// (2i) mod 11. Two is coprime with eleven, so the walk visits every hue
// before any repeat and every hue still appears; adjacent rows sit two
// hues apart on the wheel, which keeps every neighbouring pair clear of
// the palette's closest hues (sapphire and indigo, dEok 0.055) — from
// twelve rows up that pair never renders side by side.
export function autoColorHues(count: number): string[] {
  if (count <= 0) return [];
  const wheel = autoColorWheel();
  const size = wheel.length;
  if (count > size) {
    const at = (i: number): string => wheel[(i * 2) % size]!;
    return Array.from({ length: count }, (_, i) => at(i));
  }
  const at = (i: number): string => wheel[Math.round((i * size) / count) % size]!;
  return Array.from({ length: count }, (_, i) => at(i));
}

// applyAutoColors fills in project row colours from the mark palette when the
// you have turned on Auto-color projects. The ramp must read down the
// rail, so hues are assigned over the order the rows are DISPLAYED in:
// presentedOrder, the id sequence in row order, handed in by the DOM shell
// at its render choke point. The shell owns the drag-built row order and
// this function never stores it — the sequence is used for one derivation
// and dropped, so the model never holds a second copy of row order that
// could drift from the shell's on every drag. Without an order (the
// model's own refresh paths, ahead of the next render) the alphabetical
// default applies — the display order until you drag — and the
// render pass re-derives with the true order before any tint is painted.
//
// Hue follows the row's position on screen, and that is deliberately
// unstable: dragging a project re-colours it and its neighbours, and a
// project leaving shifts every hue behind it. Keying each project's hue
// to its name instead would keep hues stable as projects come and go,
// but it scrambles the ramp whenever your row order is not
// alphabetical — a rainbow shuffled down the sidebar instead of reading
// in sequence. A hue that moves on a drag is over the moment the drag
// is; a ramp that never reads in sequence is wrong every day. The ramp
// wins.
//
// A project that already has a user-assigned colour keeps it;
// auto-color only fills the gaps. The derived tint is kept in a separate
// field, so turning the preference off removes only the tints this
// function added, leaving deliberate choices intact.
export function applyAutoColors(
  groups: AutoColorRow[],
  enabled: boolean,
  presentedOrder?: readonly string[] | null,
): void {
  if (!enabled) {
    for (const g of groups) {
      g.autoColor = undefined;
    }
    return;
  }
  if (groups.length === 0) return;
  const rank = presentedOrder ?? [...groups]
    .sort((a, b) => a.project.name.localeCompare(b.project.name))
    .map((g) => g.project.id);
  const position = new Map(rank.map((id, i) => [id, i]));
  // An id missing from the order sorts last, stable on input order, so a
  // stale order never leaves a project untinted.
  const ordered = [...groups].sort((a, b) =>
    (position.get(a.project.id) ?? Number.MAX_SAFE_INTEGER) -
    (position.get(b.project.id) ?? Number.MAX_SAFE_INTEGER));
  const hues = autoColorHues(ordered.length);
  const hueById = new Map(ordered.map((g, i) => [g.project.id, hues[i]!]));
  for (const g of groups) {
    // A user-assigned colour leaves no derived tint behind it. Session tiles read
    // autoColor directly rather than through the chevron's precedence, so a
    // tint left standing under a user's choice paints the tiles one hue
    // while the chevron and rail carry another.
    if (g.color) {
      g.autoColor = undefined;
      continue;
    }
    const hue = hueById.get(g.project.id);
    if (hue !== undefined) {
      g.autoColor = `var(--color-mark-${hue})`;
    }
  }
}
