---
name: mempalace-logstream
description: Publish privacy-bounded progress to MemPalace Logstream for substantial implementation, review, repair, investigation, rollout, or multi-agent coordination work. Use when an agent should make long-running work observable across projects or sessions, send milestone or heartbeat events, wait for a coordinated reply, or supersede a stale route. Do not use for short answers, hidden reasoning, transcript capture, terminal logging, or patch transport.
---

# MemPalace Logstream

Use Logstream as a durable coordination trace. Treat every event as an agent
declaration, never as authoritative proof of process state, test results, review
approval, or completion.

Keep working if Logstream is unavailable. Report the failure once; do not block
the assigned task and do not fall back to drawers, diaries, or transcript mining
for active coordination.

## Establish one route

Before substantial work, choose these fields once and retain them for the whole
route:

- `stream`: `project/<stable-project-slug>`. Derive the slug from the trusted
  project or repository name, never from an absolute path.
- `room`: one of `implementation`, `reviews`, `repair`, `investigation`, or
  `rollout`.
- `from_agent`: the stable agent or session label. Do not put
  a username, host path, credential, or prompt fragment in it.
- `to_agent`: the assigned coordinator or recipient; otherwise `*`.
- `correlation_id`: a stable task, PR, incident, or rollout identifier. Keep it
  opaque and content-free, for example `deck-implementation-example` or
  `deck-review-example`.
- `branch` and `base_commit`: the exact Git branch and starting commit when the
  work is Git-backed. Omit them otherwise.

Use the work kind matching the room as the event prefix: `implementation`,
`review`, `repair`, `investigation`, or `rollout`.

## Publish milestones

Append events at these transitions:

| Transition | Event suffix | Status |
|---|---|---|
| Accepted the assignment | `claimed` | `claimed` |
| Finished the approach before edits | `plan-ready` | `ready` |
| Made the first material edit | `first-edit` | `claimed` |
| Started a verification command or review pass | `tests-started` | `claimed` |
| Verification finished successfully | `tests-completed` | `ready` |
| Verification finished unsuccessfully | `tests-completed` | `failed` |
| Progress requires outside input or authority | `blocked` | `blocked` |
| Finished and verified the assignment | `completed` | `applied` |
| Replaced or abandoned this route | `superseded` | `superseded` |

Form the event type as `<kind>.<suffix>`, such as `review.claimed`,
`review.tests-started`, or `implementation.completed`.

During an active phase with no milestone, append `<kind>.heartbeat` every 10 to
15 minutes with status `claimed`. A heartbeat means only that the emitting
agent reached that checkpoint. It does not prove the provider process is alive,
busy, healthy, or still authorized.

Omit `body`, `metadata`, and `artifact_ids` by default. The event type, status,
route, and Git identifiers are the complete normal payload.

## Keep the trace content-free

Never place any of the following in an event field:

- chain-of-thought, hidden reasoning, prompts, system or developer messages;
- transcript or provider records, terminal commands or output, logs, test
  output, source code, diffs, file contents, or filesystem paths;
- credentials, tokens, authentication output, environment values, private
  hostnames, or sensitive payloads;
- copied blocker details, review findings, or prose summaries of the work.

Keep blocker details, findings, and results in their authoritative project
surface. Git remains the patch transport. Do not call
`mempalace_artifact_put` or `mempalace_patch_submit` for this workflow.

For reviews, read the branch, commit, and diff live from Git or the code-hosting
review surface. Do not create `.review`, `.reviews`, or other scratch copies of
diffs, prompts, findings, verdicts, schemas, or progress state. Send lifecycle
state through Logstream, return transient tool output on stdout, and post the
actual verdict to the project's authoritative review surface.

## Use the available transport

Prefer `mempalace_event_append` when native MemPalace MCP tools are available.
Pass only the route fields, event type, and status defined above.

For a harness without MCP tools, use the version-correct CLI without a body or
metadata:

```bash
mempalace logstream append --type review.claimed \
  --stream project/deck --room reviews \
  --from-agent reviewer --to-agent coordinator \
  --correlation-id deck-pr-400-review \
  --branch feature/example --base-commit 0123456789abcdef \
  --status claimed --json
```

Use `mempalace_event_list` or `mempalace logstream list` to resume a known
route. Use `since_event_id` to avoid replaying events. Use wait/ack only for an
explicitly assigned coordination exchange; a timeout is not a work failure.

## Close the route honestly

Publish `completed` only after the assignment's real completion gates pass.
Publish `blocked` when work actually requires outside input or authority.
Publish `superseded` before abandoning a route so observers do not mistake
silence for current work.

Store durable decisions or outcomes separately in the palace when they are
worth recalling. Logstream records how work moved; it does not replace memory,
Git, the task tracker, CI, formal review, or runtime observation.
