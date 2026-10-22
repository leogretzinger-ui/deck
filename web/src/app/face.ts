// The UI typeface selection. It rides on <html> as data-face, beside
// data-theme, because the face trims in app.css re-declare theme-level
// tokens: on an inner element the scale would freeze against the default
// ramp the same way the composite colour tokens do.
//
// An absent attribute is the licensed cut at its own tuning — the ramp in
// @theme IS that tuning, so the default path is not a special case.
//
// A face renders only if the browser can resolve its family. The app serves
// under font-src 'self' (internal/server/static.go), so a candidate ships
// the way the licensed faces do: its woff2 into web/public/fonts and an
// @font-face beside theirs. Until then a scope resolves to the fallback
// stack, or to the face where it happens to be installed locally.

const KEY = "noisedeck.face";

// Every face the stylesheet scopes. A value outside this set is ignored
// rather than written through, so a stale or hand-edited store cannot put
// the document into a scope no rule matches.
export const FACES = ["inter", "public-sans", "work-sans", "figtree"] as const;

export type Face = (typeof FACES)[number];

function isFace(v: string | null): v is Face {
  return v !== null && (FACES as readonly string[]).includes(v);
}

// The query parameter wins over the store and persists, so a link carries a
// face to a fresh browser and the choice survives the next load.
export function currentFace(): Face | null {
  let stored: string | null = null;
  try {
    stored = localStorage.getItem(KEY);
  } catch {
    stored = null;
  }
  let asked: string | null = null;
  try {
    asked = new URLSearchParams(location.search).get("face");
  } catch {
    asked = null;
  }
  if (asked === "") {
    try {
      localStorage.removeItem(KEY);
    } catch { /* private mode */ }
    return null;
  }
  if (isFace(asked)) {
    try {
      localStorage.setItem(KEY, asked);
    } catch { /* private mode */ }
    return asked;
  }
  return isFace(stored) ? stored : null;
}

export function applyFace(face: Face | null): void {
  if (face === null) {
    delete document.documentElement.dataset.face;
    return;
  }
  document.documentElement.dataset.face = face;
}
