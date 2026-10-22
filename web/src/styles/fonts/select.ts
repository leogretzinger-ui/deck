export type FontSet = "licensed" | "free";

export interface FontSetInputs {
  override: string | undefined;
  licensedSheet: string | undefined;
  supplied: boolean;
  present: (urlPath: string) => boolean;
}

// The unchanged licensed stylesheet is the manifest for the complete set.
export function licensedFiles(sheet: string): string[] {
  const files = new Set<string>();
  for (const m of sheet.matchAll(/url\(["']?(\/fonts\/[^"')]+)["']?\)/g)) files.add(m[1]!);
  return [...files].sort();
}

export function selectFontSet(inputs: FontSetInputs): FontSet {
  const mode = inputs.override || "auto";
  if (!["auto", "free", "licensed"].includes(mode)) {
    throw new Error(`NOISEDECK_FONTS must be "auto", "free" or "licensed", got ${JSON.stringify(mode)}`);
  }
  if (mode === "free") return "free";
  if (inputs.licensedSheet === undefined) {
    if (mode === "licensed") {
      throw new Error("NOISEDECK_FONTS=licensed requires the licensed stylesheet, which is absent from this source tree. Select NOISEDECK_FONTS=free or use a checkout with the licensed stylesheet (see docs/fonts.md)");
    }
    return "free";
  }
  if (!inputs.supplied) {
    if (mode === "licensed") {
      throw new Error("NOISEDECK_FONTS=licensed requires assets: set NOISEDECK_LICENSED_FONTS_DIR to your complete licensed font directory (see docs/fonts.md)");
    }
    return "free";
  }
  const files = licensedFiles(inputs.licensedSheet);
  if (files.length === 0) throw new Error("The licensed stylesheet contains no font files");
  const missing = files.filter((f) => !inputs.present(f));
  if (missing.length) {
    throw new Error(
      `Incomplete licensed font set: ${missing.length} missing or invalid file(s): ${missing.join(", ")}. ` +
      "Supply the complete set in NOISEDECK_LICENSED_FONTS_DIR, or select NOISEDECK_FONTS=free (see docs/fonts.md)",
    );
  }
  return "licensed";
}
