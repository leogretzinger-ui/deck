// The session/project mark palette: pure colour data with no DOM
// dependencies, shared by the sidebar, the session menu, and the
// auto-color ramp.
// SESSION_COLOR_TAGS is the tag palette: the full mark palette the harness
// chips wear — all eleven hues in circle order — after "no color", so
// anything a chip can be, a tag can be. Every mark names its var directly,
// so app.css stays the single source of hex and a tag and a chip of the
// same family are the same colour. The strip closes with four neutral
// identities: ink/line-ramp values written as raw hex
// on purpose — as var() they would follow the theme fork and drift off
// the pinned identity between light and dark grounds.
export const SESSION_COLOR_TAGS: readonly {
  key: string;
  name: string;
  value: string | null;
}[] = [
  { key: "none", name: "No color", value: null },
  { key: "crimson", name: "Crimson", value: "var(--color-mark-crimson)" },
  { key: "vermilion", name: "Vermilion", value: "var(--color-mark-vermilion)" },
  { key: "gold", name: "Gold", value: "var(--color-mark-gold)" },
  { key: "lime", name: "Lime", value: "var(--color-mark-lime)" },
  { key: "emerald", name: "Emerald", value: "var(--color-mark-emerald)" },
  { key: "teal", name: "Teal", value: "var(--color-mark-teal)" },
  { key: "cerulean", name: "Cerulean", value: "var(--color-mark-cerulean)" },
  { key: "sapphire", name: "Sapphire", value: "var(--color-mark-sapphire)" },
  { key: "indigo", name: "Indigo", value: "var(--color-mark-indigo)" },
  { key: "violet", name: "Violet", value: "var(--color-mark-violet)" },
  { key: "orchid", name: "Orchid", value: "var(--color-mark-orchid)" },
  { key: "pearl", name: "Pearl", value: "#B3B8C2" },
  { key: "silver", name: "Silver", value: "#7B8799" },
  { key: "graphite", name: "Graphite", value: "#43536B" },
  { key: "ink", name: "Ink", value: "#0E1B2C" },
];

// Stored rows still carry the legacy family keys (red, amber, green,
// blue, magenta) and gray. Dropping a mapping here leaves those rows
// with no rendered colour and no swatch marked current in the strip;
// the map resolves each to its canonical key — gray reads as silver,
// the nearest neutral to the tertiary ink it painted.
const LEGACY_TAG_KEYS: Record<string, string> = {
  red: "crimson",
  amber: "gold",
  green: "emerald",
  blue: "sapphire",
  magenta: "orchid",
  gray: "silver",
};

export const SESSION_COLOR_VARS: Record<string, string> = Object.fromEntries(
  SESSION_COLOR_TAGS.flatMap((tag) => tag.value === null ? [] : [[tag.key, tag.value] as const]),
);
for (const [legacy, canonical] of Object.entries(LEGACY_TAG_KEYS)) {
  SESSION_COLOR_VARS[legacy] = SESSION_COLOR_VARS[canonical]!;
}

// resolveTagKey maps a stored colour to its canonical strip key, or null
// when nothing renders (a genuinely retired key).
export function resolveTagKey(color: string | null | undefined): string | null {
  if (!color) return null;
  if (SESSION_COLOR_TAGS.some((tag) => tag.key === color)) return color;
  return LEGACY_TAG_KEYS[color] ?? null;
}

// operatorProjectColorKey resolves the colour you gave a project
// to the durable tag key session records accept, and null when you never
// gave one. It is deliberately blind to the auto-color tint: that tint is
// a render-time preference whose hue follows row position, so promoting it
// to a session record would tag sessions with whatever the project ranked
// at that moment and outlive the preference that drew it.
export function operatorProjectColorKey(
  projectColor: string | null | undefined,
): string | null {
  const tagged = resolveTagKey(projectColor);
  return tagged !== null && tagged in SESSION_COLOR_VARS ? tagged : null;
}

// effectiveProjectTint resolves the colour a project's own surfaces paint
// (the chevron, the rail, the no-session titlebar tile): your
// tag through the palette — a stored key that skips the palette paints
// the CSS *named* colour of its spelling ("red" renders pure #FF0000)
// instead of the mark the strip showed — else the derived auto-color
// tint. Null when the project wears neither.
export function effectiveProjectTint(
  color: string | null | undefined,
  autoColor: string | null | undefined,
): string | null {
  return (color ? SESSION_COLOR_VARS[color] : undefined) ?? autoColor ?? null;
}
