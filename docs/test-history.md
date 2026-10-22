# Per-test CI history

Fast checks collects Go test terminal outcomes during the existing test invocation. It does not rerun tests or select or disable tests. Each job uploads a compact `test-metrics-*` JSON artifact, retained for three days. Test names (including free-text subtest names) are retained; test output is not stored in metrics. Passing package logs stay quiet; failed packages replay up to the last 2 MB of diagnostics. Go's JSON mode sets `testing.Verbose()` to true, which tests must not use to change their assertions. Metrics-file write failures warn without changing the test exit code. A killed job may have no artifact; missing observations are never passes. Release acceptance's compiled native-login probe and browser tests are not covered yet.

Keep the SQLite database outside the checkout, preferably on NAS storage. No collector service, OpenTelemetry deployment, additional credentials, or provider API is needed. An authenticated GitHub CLI with repository Actions read access imports available artifacts idempotently:

```sh
python3 .github/scripts/test_metrics.py --db /path/on/nas/deck-tests.sqlite sync --repo example/deck
python3 .github/scripts/test_metrics.py --db /path/on/nas/deck-tests.sqlite report --days 30
```

Run sync at least daily so artifacts are imported before expiry. Schedule it using an operator-owned checkout, not a pull request's scripts. The database keeps compact historical records indefinitely; GitHub artifacts expire after three days. Repository identity, commit, run attempt, suite and platform distinguish observations. Source-export suites are separate from checkout suites. Import a downloaded record with `import result.json`.

The report ranks cumulative observed elapsed time and failures, with observation and skip counts, same-commit recovery and unique-failure counts. It includes PR, merge-queue and manual executions, not just main; event/ref labels are not collected yet. Time includes nested test time: parent and subtest rows overlap and must not be added to estimate compute or billing. A unique failure means no unrelated failing test in the same suite invocation, not that it alone blocked the entire workflow. Recovery means a later pass on the same SHA, suite and platform; this is a suspected flake, not proof. Changed environment and external dependencies can also explain recovery. Conditional CI selection biases the sample; a test that never fails can still protect a critical boundary.

Import checks repository and workflow-run identity against GitHub's artifact listing. The history is advisory: same-repository branches can supply their own test names, attempts and suites, so do not use it as a security or merge gate. Invalid artifacts are marked rejected and skipped on future syncs; other artifacts continue importing. Download failures remain retryable. Sync exits nonzero when either occurs, so an operator can notice missing records.

Confirmed bugs and maintenance cost require human evidence rather than inferred value scores. Link an issue or fix to a test:

```sh
python3 .github/scripts/test_metrics.py --db /path/on/nas/deck-tests.sqlite evidence \
  --repo example/deck --package example/internal/tasks --test TestBoundary \
  --kind bug --url https://github.com/example/deck/pull/123
```

Use `--kind maintenance` for a linked flaky-test repair. The report counts distinct links; it does not infer engineering hours or automatically propose deletion. Review expensive, noisy tests weekly, keeping security and regression coverage in the decision.
