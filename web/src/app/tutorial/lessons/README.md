# Tutorial lessons

Each file under `en/` is one lesson. The renderer reads them at build time; nothing here is code.

## Front-matter

- `id`: short lowercase key the saved tutorial position refers to.
- `title`: shown in the catalog and the panel header, sentence case.
- `group`: `course` (the three required lessons) or `explore` (optional chapters).
- `duration`: shown on the catalog row.
- `requires`: capabilities the installation must have, or the row is dimmed with the reason. Values: `agent-chat` (the MemPalace overlay), `two-sessions`, `github`, `claude` (a Claude login or API key for task generation), `previews`, `mempalace`, `reachable-install`, `review-app`.
- `principles`: which core principles the lesson proves, by number. Course lessons only.

## Steps

A file is a `#` title followed by `##` sections. Each `##` section is one step, except `## done`, which is the lesson's closing line.

A step has two parts, in this order:

1. **Settings**: the lines directly under the heading, one `key: value` per line, up to the first blank line. A value of `|` starts a block whose lines are indented by two spaces.
2. **Body**: everything after that blank line. This is the text the user reads. It is plain Markdown: paragraphs, inline code and links.

No setting may appear in the body. A step with no body is allowed.

### Settings

Advancing:

- `advance_on`: the product event that completes the step. `continue` means the step is explanation only and shows a Continue button. `a then b` means both events, in that order. If the event has already happened when the step is shown, the step completes on arrival. Events: `agent_picked`, `starter_created`, `session_signed_in`, `session_attached(project=<name>)`, `task_exists`, `task_exists(requirement=<n>)`, `input_sent`, `input_sent(project=<name>)`, `session_busy`, `file_opened(modified)`, `check_exited`, `task_done`, `project_opened(source=github)`, `lane_session_attached`, `review_seen`, `chat_message_seen`, `page_reloaded(session_live)`.
- `when`: the step is shown only under this condition, and skipped otherwise. Conditions: `agent_needs_login`, `starter_exists`.

Buttons:

- `primary` and `action`: the label and the action of the one main button.
- `then_primary` and `then_action`: the button shown in its place after the first action succeeds.
- `fallback` and `fallback_action`: a second, always visible action. It shows after the primary button, so a step that needs two actions in order puts the first in `primary` and the second here.
- `fallback_when`: show the fallback only under this condition. Conditions: `no_git_identity`, `no_task(requirement=<n>)`.
- `fallback_primary_when`: under this condition the fallback becomes the main button and the primary is shown dimmed. Conditions: `agent_cannot_generate_tasks`.

Actions: `add-prd`, `generate-tasks`, `add-task`, `add-task(requirement=<n>)`, `add-project`, `choose-agent`, `create-starter`, `git-identity`, `insert-prompt`, `mark-task-done`, `new-session`, `open-preview`, `preview-settings`, `request-review`, `run-command`, `show-address`, `start-lane`, `start-session(project=<name>)`, `upload-files`.

Content shown with the buttons:

- `prompt`: text shown in full above an `insert-prompt` button, usually as a `|` block. Inserted into the selected agent's terminal as typed text, with no trailing newline.
- `command`: a command shown above a `run-command` button, which types it into a plain terminal in the project. `command_note` is a line of small text under it.
- `pass_text` and `fail_text`: shown from the exit code of a `run-command` that Deck ran itself.
- `target`: the project an `insert-prompt` or `run-command` button types into: `relnotes` or `ccparse` (the practice projects), `lane` (the lane of the user's own task) or `source` (the user's own project the lane was made from). For a prompt Deck selects that project's agent session first, and types nothing if it has none. For a command it selects the project's plain shell, opening one if needed, and types nothing if the project is missing. Without `target`, the text goes to the selected session or project. Every command step in a course lesson names one.
- `generate_count`: how many tasks `generate-tasks` asks for. Lesson 1 uses 2 so the starter PRD yields exactly its two requirements; checked three runs out of three. It applies to the relnotes practice project. Omit it and `generate-tasks` works on the user's own project with the default count: it generates from the PRD the Tasks panel holds, or opens the panel's Add PRD flow when there is none. `add-task` without a requirement likewise opens the Tasks panel's Add task form.
- `opens`: the dock panel the step opens (`tasks`, `files`, `chats`). The lesson folds to its bar while the panel is open.
- `busy_text`: shown in the lesson bar while the agent is working.
- `primary_note`, `fine_print`, `footer`, `note`: small text in the named place. `note_when` shows `note` only under a condition (`no_review_app`).

### The done section

`## done` has one setting, `next`, naming the lesson that follows (`all-lessons` for the catalog), and a body that is the closing line.

## Text

`{agent}` is replaced with the name of the agent the user picked, `{task.title}` and `{task.description}` with the current task's fields, and `{starter.relnotes}` with the path of the relnotes practice project.

Links inside lesson text use one of two schemes, never a bare relative path: `lesson:<id>` opens another lesson in the panel, and `action:<name>` runs one of the actions above.

Follow the plain-english skill. One primary action per page, body under 26 words where possible, control names exactly as the info bar in `web/src/lib/hints.ts` prints them, and a manual path on every step that asks an agent to do something. The only page that explains what agents can access is `what-an-agent-can-reach.md`.

## Hints the lessons name that do not exist yet

The renderer change adds these to `web/src/lib/hints.ts` with the controls: Choose an agent…, Create relnotes and start {agent}, Add PRD…, Generate tasks, Put it in the terminal, Put in a terminal, Mark task done, Start a lane, Request review, Show step, Hide step, Skip step, Continue, All lessons, Resume.
