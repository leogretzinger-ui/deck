// The harness inventory the fixture console serves.
//
// It is fixture data, like fixtureProjects is — what this made-up server
// happens to have — and not a statement about what any real image contains.
// A real console asks GET /api/providers and draws the answer; only the
// preview, which has no server, supplies its own. It stands in for a full
// image so the preview's captures and probes see the whole picker, and it is
// in served order, which is sorted.
//
// It lives in its own module because fixtures.ts uses TypeScript syntax the
// test runner's type-stripping loader rejects, and the tests need this list.
export const FIXTURE_PROVIDERS: readonly string[] = [
  "claude", "cline", "codex", "command-code", "cursor", "devin", "grok",
  "kilo", "kimi", "mimo", "omp", "opencode", "pi", "qoder", "qwen", "shell",
];
