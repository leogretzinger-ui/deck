import { buildStarterBundle } from "./starter.ts";

// The glob lives here alone, as in lesson-files.ts: node has no
// import.meta.glob, so the unit tests build the bundle from files read off
// disk instead of importing this module.
const files = import.meta.glob<string>("./starters/**/*", { query: "?raw", import: "default", eager: true });

export const tutorialStarterBundle = buildStarterBundle(files);
