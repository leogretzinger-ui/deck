/// <reference types="vite/client" />

// Vite resolves @fonts to the selected font stylesheet.
declare module "@fonts";

// The type set the build selected, "licensed" or "free" (vite.config.ts).
declare const __NOISEDECK_FONTS__: "licensed" | "free";
