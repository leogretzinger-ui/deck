# CI checks

## Private runner routing

Private installations can set the repository variable
`NOISEDECK_SELF_HOSTED_CI=true` to run Fast checks on the
`self-hosted`, `Linux`, `deck-ci-private` labels. There is no hosted fallback
if that runner is unavailable: jobs queue rather than spend hosted minutes.
The one exception is the `fast-checks` aggregate gate, which always runs on
`ubuntu-24.04` so a missing runner cannot leave the required check pending.
The runner must be isolated from production data, personal home directories and
provider credentials, with repository-restricted access and bounded capacity.

Public repositories and installations without that opt-in continue to use
standard GitHub-hosted Ubuntu runners.
Private fork PRs are not admitted to the persistent runner; public fork
checks retain their existing hosted execution. Go, Chromium, linter and tmux
caches include the actual runner architecture to prevent AMD64 cache reuse
on ARM64. Passing ARM64 checks does not certify native Linux/AMD64 images
or native macOS installation.

Review and required-check protections are unchanged. Old PR heads must
acquire fresh checks from the updated workflow
before merging; rerunning an old head's hosted workflow is not a migration.

Private same-repository pull requests run only the lightweight preflight:
selector unit tests, public archive/Markdown validation and the required
aggregate. They do not run regression suites or call Jev. Approval lets the
PR enter the merge queue; it does not certify the application tests.

Private merge-queue runs use context-rich Jev selection by default. Each
suite is selected at an impact probability
of at least 50%; documentation remains mandatory. UI, gateway and project-app
preview browser lanes are assessed separately. These are model estimates,
not calibrated guarantees. Full manual Fast checks and release acceptance
are unchanged.

The credentialed CI advisor runs from trusted main when merge-queue Fast
checks is requested, without checking out or executing candidate code. Its
planner refuses PR runs, including manual requests for those runs. It reads the
complete bounded patch and the actual suite implementations as data, then
publishes a small credential-free artifact retained for one day. The consumer
checks the publisher workflow and binds the artifact to the exact repository,
run, event, base and head. It also recomputes the selections from the validated
probabilities. API credentials never enter PR jobs.

The merge-queue scope job waits up to one minute for that plan. Missing credentials, API
failure, stale or malformed plans, unsupported or oversized patches and
unknown queue layouts use the Python path map below. The fallback uses the
complete local merge-base-to-head Git diff, not the last commit or a truncated
API list. Public repositories and forks keep their existing Python/trust
behavior. The workflow summary names the selector and selected checks.

| Changed area | Python fallback checks |
| --- | --- |
| README, contributing, changelog, security documentation; Markdown under docs or skills | Public source archive contents and relative Markdown links |
| Frontend source, assets, tooling and probes | Documentation, frontend typecheck/tests/build, three UI browser lanes |
| Go source under cmd or internal | Documentation, Go formatting, lint/vulnerability checks, parallel and serialized Go tests, experiment tests |
| Multiple recognized areas | Union of their suites |
| Authentication, session, transport and other backend boundary code | Go checks plus frontend and all browser lanes |
| CI, deployment scripts and working agreements | Documentation and deployment contract tests; `.go` changes also run Go checks; repair/jail implementation also runs Go and hostile jail checks |
| Workflow definitions under `.github/workflows/` | Full suite |
| Executable policy documents | Go and deployment contract tests that consume those files |
| Public export implementation | Documentation, deployment contracts and extracted-source validation; `.go` changes also run Go checks |
| Shared build files, Go dependencies, or unrecognized paths | Full suite |

A plan chosen by these rules as full validation stays full: the model-chosen plan cannot narrow it.

This is a conservative path map, not semantic dependency analysis. The map and
its tests live in `.github/scripts/ci_scope.py` and `test_ci_scope.py`. A rename
checks both old and new paths. Missing history, malformed events, ambiguous
merge bases, empty diffs, symlinks and executable-mode changes use full checks.

Backend boundary changes retain all browser lanes rather than relying on Go
unit tests alone. Browser cryptography, API contracts, preview/onboarding
probes and frontend dependency-lock changes also retain gateway and preview
lanes. Ordinary UI changes do not rebuild those unrelated Go fixtures.
Extracted-source validation and the hostile repair jail are no longer charged
to every UI or backend change; they run when their own area changes and in
full manual validation.

Merge-queue runs use the complete queue base-to-head diff, including every
change in a grouped batch. If a selected queue test fails, the PR does not
merge: inspect and fix the failure, then enqueue it again. Running regression
tests only at that revision avoids repeating the same suites before and
after queueing. Public repositories and fork PRs retain their existing checks;
this private-repository policy assumes changes land through the merge queue.
Do not use a private PR preflight as evidence for a direct merge or push.

Manual runs always select the full suite. Fast checks does not
run again on a main push: the merge queue has already tested the exact commit
that lands. Installations that allow direct pushes to main must explicitly
dispatch Fast checks to validate those commits. Release Acceptance, image
publication, schedules and independent review are unchanged.

Fork PRs retain the existing trust boundary: public archive/Markdown checks
and full tests of the exported source run; trusted-only jobs remain skipped.

The required `fast-checks` gate always runs. It fails if scope selection fails,
if a selected job fails, is cancelled or skips unexpectedly, or if an
unselected job runs. An intentional skip is not a successful test: the gate checks the
plan against every actual job result. It permits selective merge-queue plans,
but still requires a full plan for manual, unknown and fork events.

The archive-content and Markdown checks run once per workflow, including
documentation-only changes. Full exported-source build, vet and test lanes
remain mandatory in the full suite. All test invocations retain `-count=1`.

## Jev credentials and repair advice

`TYPESAFE_API_KEY` stays in the existing Actions repository secret and is
received only by trusted-main code on an ephemeral hosted runner. The advisor
is not itself a required check: its absence selects the Python fallback,
not a green exemption from tests. The existing repair workflow's Jev failure
advice remains separate and does not acquire new repair authority.

The API summary reports the model, each probability, selected suites and
browser lanes, latency, token usage and estimated cost. It never prints
patches, source context, credentials or raw API response bodies.
