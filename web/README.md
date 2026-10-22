# Browser frontend

The frontend uses TypeScript, Vite, Tailwind CSS, and xterm.js. Components
render DOM directly. The backend owns recorded state; the browser owns its
presentation and terminal connection. Native agent CLIs own their terminal
output, conversation history, and approvals.

## Finding the code

| Directory | Responsibility |
| --- | --- |
| `src/main.ts` | Application entry, authentication, and live or preview startup. |
| `src/app/` | Workspace composition, navigation, menus, and panel lifetimes. `live.ts` wires the production workspace; `workspace-preview.ts` wires the design fixtures. |
| `src/app/tutorial/` | Tutorial model, lesson parser, actions, panel, lesson content, and starter projects. Raw asset loaders are separate from modules that run in Node tests. |
| `src/api/` | API contracts by domain, shared HTTP handling, and the production client. `client.ts` remains the compatibility entry point. |
| `src/panels/` | Sidebar, files, chats, and tasks. Models hold presentation state; DOM controllers render it and bind user actions. |
| `src/terminal/` | xterm.js, attachment lifecycle, input, scrolling, and terminal presentation. |
| `src/security/` | Browser cryptography and workspace authorization clients. `src/app/security/` coordinates the corresponding screens. |
| `src/ui/` | Shared markup and controls. |
| `src/lib/` | Small helpers shared across features. |
| `src/styles/` | Design tokens, application styles, and font selection. |
| `src/preview/` | Security and authentication preview states. |
| `tools/` | Browser probes, visual capture helpers, and asset generation. |

The sidebar separates ordering (`order.ts`), drag geometry (`drag.ts`), and
markup (`markup.ts`) from its event controller (`sidebar-dom.ts`). Import
these helpers directly when a caller does not need the controller.

API consumers import contracts from their domain module, such as
`api/projects.ts` or `api/files.ts`. Shared errors, CSRF handling, and
authorization-loss handling live in `api/http.ts`. `HttpApiClient` owns
endpoint requests without caching or deriving server state.

## Development and verification

From the repository root, `make dev` starts the backend and Vite together.
From this directory:

```sh
pnpm install --frozen-lockfile
pnpm typecheck
pnpm test
NOISEDECK_FONTS=free pnpm build
pnpm test:live
```

Unit tests sit beside their modules and run with Node's built-in test runner.
Browser probes under `tools/` run separately against the real DOM and xterm.js.
Use the pinned Playwright client and `tools/browser.mjs`; when the deployed
browser overlay is configured, the resolver connects to it.

Keep accessible names, DOM hooks, and preview fixtures aligned when changing
markup. Update the matching browser probe in the same change. See
[`docs/fonts.md`](../docs/fonts.md) for licensed font setup.
