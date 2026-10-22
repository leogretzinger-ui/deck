import { buildTutorialCatalog } from "./catalog.ts";

// The glob lives here alone: node has no import.meta.glob, so the unit
// tests build the catalog from files read off disk instead of importing
// this module.
const files = import.meta.glob<string>("./lessons/en/*.md", { query: "?raw", import: "default", eager: true });

export const tutorialCatalog = buildTutorialCatalog(files);
