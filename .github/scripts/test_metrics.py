"""Metadata-only Go test collection and an operator-owned SQLite history."""

import argparse
import datetime as dt
import json
import os
from pathlib import Path
import platform
import sqlite3
import subprocess
import sys
import tempfile
import uuid
import zipfile

VERSION = 1
PREFIX = "test-metrics-"


def collect(command, suite, output):
    if command[:2] != ["go", "test"]:
        raise ValueError("collector requires go test")
    command = command[:2] + ["-json"] + command[2:]
    sha = os.getenv("GITHUB_SHA")
    if not sha:
        try:
            sha = subprocess.check_output(["git", "rev-parse", "HEAD"], text=True,
                                          stderr=subprocess.DEVNULL).strip()
        except (OSError, subprocess.CalledProcessError):
            sha = "unknown"
    record = dict(version=VERSION, repository=os.getenv("GITHUB_REPOSITORY", "local"),
                  run=os.getenv("GITHUB_RUN_ID", str(uuid.uuid4())),
                  attempt=os.getenv("GITHUB_RUN_ATTEMPT", "1"), suite=suite,
                  sha=sha,
                  platform=platform.system() + "/" + platform.machine(),
                  recorded=dt.datetime.now(dt.timezone.utc).isoformat(),
                  results=[], exit_code=None)
    process = subprocess.Popen(command, stdout=subprocess.PIPE, text=True)
    buffers = {}
    try:
        for line in process.stdout:
            try:
                event = json.loads(line)
            except json.JSONDecodeError:
                print(line, end="", flush=True)
                continue
            if not isinstance(event, dict):
                print(line, end="", flush=True)
                continue
            if event.get("Action") == "build-output" and isinstance(event.get("Output"), str):
                print(event["Output"], end="", flush=True)
            package = event.get("Package", "")
            if not isinstance(package, str):
                continue
            if event.get("Action") == "output" and isinstance(event.get("Output"), str):
                # go test -json implies verbosity. Keep successful fixture logs
                # quiet, with a bounded diagnostic tail for failed packages.
                buffers[package] = (buffers.get(package, "") + event["Output"])[-2_000_000:]
            if event.get("Action") in ("pass", "fail", "skip"):
                test = event.get("Test", "")
                elapsed = event.get("Elapsed", 0)
                if not isinstance(test, str) or type(elapsed) not in (int, float):
                    continue
                record["results"].append(dict(package=package,
                                              test=test,
                                              outcome=event["Action"],
                                              seconds=elapsed))
                if not test:
                    buffered = buffers.pop(package, "")
                    if event["Action"] == "fail":
                        print(buffered, end="", flush=True)
                    else:
                        for summary in buffered.splitlines():
                            if summary.startswith(("ok  \t", "?   \t")):
                                print(summary, flush=True)
        record["exit_code"] = process.wait()
    finally:
        if process.poll() is None:
            process.terminate()
            process.wait()
        for buffered in buffers.values():
            print(buffered, end="", flush=True)
        try:
            destination = Path(output)
            destination.parent.mkdir(parents=True, exist_ok=True)
            destination.write_text(json.dumps(record, separators=(",", ":")) + "\n")
        except OSError as error:
            print(f"warning: test metrics not saved: {error}", file=sys.stderr)
    code = record["exit_code"]
    return code if code >= 0 else 128 - code


def connect(path):
    Path(path).parent.mkdir(parents=True, exist_ok=True)
    db = sqlite3.connect(path)
    db.executescript("""
        CREATE TABLE IF NOT EXISTS runs (
          repository TEXT, run TEXT, attempt TEXT, suite TEXT, sha TEXT,
          platform TEXT, recorded TEXT, exit_code INTEGER,
          PRIMARY KEY (repository, run, attempt, suite));
        CREATE TABLE IF NOT EXISTS results (
          repository TEXT, run TEXT, attempt TEXT, suite TEXT, ordinal INTEGER,
          package TEXT, test TEXT, outcome TEXT, seconds REAL,
          PRIMARY KEY (repository, run, attempt, suite, ordinal));
        CREATE TABLE IF NOT EXISTS evidence (
          repository TEXT, package TEXT, test TEXT, kind TEXT, url TEXT,
          PRIMARY KEY (repository, package, test, kind, url));
        CREATE TABLE IF NOT EXISTS artifacts (repository TEXT, id INTEGER,
          PRIMARY KEY (repository, id));
        CREATE INDEX IF NOT EXISTS recovery_lookup
          ON results(repository,package,test,outcome);
        CREATE INDEX IF NOT EXISTS run_date ON runs(recorded);
    """)
    return db


def ingest(db, record):
    if not isinstance(record, dict):
        raise ValueError("metrics must be an object")
    if record.get("version") != VERSION:
        raise ValueError("unsupported metrics version")
    fields = ("repository", "run", "attempt", "suite", "sha", "platform", "recorded")
    if any(not isinstance(record.get(k), str) or not record[k] for k in fields):
        raise ValueError("missing run metadata")
    timestamp = dt.datetime.fromisoformat(record["recorded"])
    if timestamp.tzinfo is None:
        raise ValueError("timestamp must include timezone")
    record = dict(record, recorded=timestamp.astimezone(dt.timezone.utc).isoformat(timespec="microseconds"))
    if record.get("exit_code") is not None and type(record["exit_code"]) is not int:
        raise ValueError("invalid exit code")
    if not isinstance(record.get("results"), list):
        raise ValueError("results must be a list")
    rows = []
    for index, result in enumerate(record["results"]):
        if (not isinstance(result, dict) or result.get("outcome") not in ("pass", "fail", "skip")
                or not isinstance(result.get("package"), str)
                or not isinstance(result.get("test"), str)
                or type(result.get("seconds")) not in (int, float)
                or not 0 <= result["seconds"] < 1e9):
            raise ValueError("invalid test result")
        rows.append(tuple(record[k] for k in fields[:4]) + (index, result["package"],
                    result["test"], result["outcome"], result["seconds"]))
    key = tuple(record[k] for k in fields[:4])
    # Reimport is idempotent; a corrupt record never partially replaces a run.
    with db:
        db.execute("DELETE FROM results WHERE repository=? AND run=? AND attempt=? AND suite=?", key)
        db.execute("INSERT OR REPLACE INTO runs VALUES (?,?,?,?,?,?,?,?)",
                   tuple(record[k] for k in fields) + (record.get("exit_code"),))
        db.executemany("INSERT INTO results VALUES (?,?,?,?,?,?,?,?,?)", rows)


def sync(db, repository):
    pages = json.loads(subprocess.check_output([
        "gh", "api", "--paginate", "--slurp",
        f"repos/{repository}/actions/artifacts?per_page=100"], text=True))
    count = 0
    errors = 0
    for page in pages:
        for artifact in page["artifacts"]:
            if (not artifact["name"].startswith(PREFIX) or artifact["expired"]
                    or db.execute("SELECT 1 FROM artifacts WHERE repository=? AND id=?",
                                  (repository, artifact["id"])).fetchone()):
                continue
            rejected = False
            try:
                if artifact["size_in_bytes"] > 20_000_000:
                    raise ValueError("metrics artifact exceeds size limit")
                with tempfile.TemporaryFile() as archive:
                    subprocess.run(["gh", "api", f"repos/{repository}/actions/artifacts/{artifact['id']}/zip"],
                                   stdout=archive, check=True)
                    archive.seek(0)
                    with zipfile.ZipFile(archive) as zipped:
                        entries = zipped.infolist()
                        if sum(e.file_size for e in entries) > 20_000_000:
                            raise ValueError("expanded metrics exceed size limit")
                        for entry in entries:
                            if not entry.filename.endswith(".json"):
                                continue
                            record = json.loads(zipped.read(entry))
                            if not isinstance(record, dict) or record.get("repository") != repository:
                                raise ValueError("artifact repository mismatch")
                            if record.get("run") != str(artifact["workflow_run"]["id"]):
                                raise ValueError("artifact run mismatch")
                            ingest(db, record)
            except (subprocess.CalledProcessError, OSError) as error:
                print(f"warning: artifact {artifact['id']} download failed ({type(error).__name__}); will retry", file=sys.stderr)
                errors += 1
                continue
            except (ValueError, KeyError, TypeError, RuntimeError, NotImplementedError,
                    zipfile.BadZipFile, zipfile.LargeZipFile) as error:
                print(f"warning: artifact {artifact['id']} rejected ({type(error).__name__})", file=sys.stderr)
                errors += 1
                rejected = True
            with db:
                db.execute("INSERT INTO artifacts VALUES (?,?)", (repository, artifact["id"]))
            if not rejected:
                count += 1
    print(f"Imported {count} new metrics artifacts")
    return 1 if errors else 0


def report(db, days, limit):
    cutoff = (dt.datetime.now(dt.timezone.utc) - dt.timedelta(days=days)).isoformat()
    rows = db.execute("""
      SELECT r.repository,r.suite,r.platform,t.package,t.test,
        COUNT(*),SUM(t.seconds),SUM(t.outcome='fail'),SUM(t.outcome='skip'),
        SUM(CASE WHEN t.outcome='fail' THEN EXISTS (
          SELECT 1 FROM results p JOIN runs q USING(repository,run,attempt,suite)
          WHERE p.repository=t.repository AND p.package=t.package AND p.test=t.test
            AND q.sha=r.sha AND q.sha<>'unknown' AND q.suite=r.suite AND q.platform=r.platform
            AND q.recorded>r.recorded AND p.outcome='pass') ELSE 0 END),
        SUM(CASE WHEN t.outcome='fail' THEN NOT EXISTS (
          SELECT 1 FROM results other WHERE other.repository=t.repository
            AND other.run=t.run AND other.attempt=t.attempt AND other.suite=t.suite
            AND other.test<>'' AND other.outcome='fail'
            AND (other.package<>t.package OR (
              other.test<>t.test
              AND substr(other.test,1,length(t.test)+1)<>t.test || '/'
              AND substr(t.test,1,length(other.test)+1)<>other.test || '/')))
            ELSE 0 END)
      FROM results t JOIN runs r USING(repository,run,attempt,suite)
      WHERE r.recorded>=? AND t.test<>''
      GROUP BY r.repository,r.suite,r.platform,t.package,t.test
      ORDER BY SUM(t.seconds) DESC
    """, (cutoff,)).fetchall()
    print(f"# Test history: last {days} days\n")
    print("Observed test elapsed time, not CPU or billed minutes. Parent/subtest time overlaps; do not sum rows.")
    print("Same-commit recovery is a flake signal, not proof. Unique failures are within one suite, not the entire workflow.")
    print("Selection is conditional: absence is not a pass; never failing is not evidence of low value.\n")
    def render(row):
        repo, suite, host, package, test, observations, seconds, failures, skips, recovery, unique = row
        evidence = dict(db.execute("SELECT kind,COUNT(*) FROM evidence WHERE repository=? AND package=? AND test=? GROUP BY kind",
                                   (repo, package, test)).fetchall())
        label = f"{repo}: {package}.{test} ({suite} / {host})".replace("|", "\\|").replace("\n", " ")
        print(f"| {label} | {observations} | {seconds:.2f} | {failures} | {skips} | {recovery} | {unique} | {evidence.get('bug', 0)} / {evidence.get('maintenance', 0)} |")
    print("\nMost elapsed time:\n")
    header = "| Test (suite / platform) | Observations | Seconds | Fail | Skip | Same-commit recovery | Unique failures | Bug / maintenance links |"
    print(header)
    print("|---|---:|---:|---:|---:|---:|---:|---:|")
    for row in rows[:limit]:
        render(row)
    print("\nMost failures (including correlated parent/subtest failures):\n")
    print(header)
    print("|---|---:|---:|---:|---:|---:|---:|---:|")
    for row in sorted((r for r in rows if r[7]), key=lambda r: r[7], reverse=True)[:limit]:
        render(row)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--db", default="test-history.sqlite")
    commands = parser.add_subparsers(dest="action", required=True)
    run = commands.add_parser("run")
    run.add_argument("--suite", required=True)
    run.add_argument("--output", required=True)
    run.add_argument("command", nargs=argparse.REMAINDER)
    load = commands.add_parser("import")
    load.add_argument("files", nargs="+")
    fetch = commands.add_parser("sync")
    fetch.add_argument("--repo", required=True)
    summary = commands.add_parser("report")
    summary.add_argument("--days", type=int, default=30)
    summary.add_argument("--limit", type=int, default=20)
    note = commands.add_parser("evidence")
    for field in ("repo", "package", "test", "url"):
        note.add_argument("--" + field, required=True)
    note.add_argument("--kind", choices=("bug", "maintenance"), required=True)
    args = parser.parse_args()
    if args.action == "run":
        command = args.command[1:] if args.command[:1] == ["--"] else args.command
        return collect(command, args.suite, args.output)
    with connect(args.db) as db:
        if args.action == "import":
            for file in args.files:
                ingest(db, json.loads(Path(file).read_text()))
        elif args.action == "sync":
            return sync(db, args.repo)
        elif args.action == "report":
            report(db, args.days, args.limit)
        else:
            if not args.url.startswith("https://"):
                parser.error("evidence must link to an HTTPS issue or fix")
            db.execute("INSERT OR IGNORE INTO evidence VALUES (?,?,?,?,?)",
                       (args.repo, args.package, args.test, args.kind, args.url))
    return 0


if __name__ == "__main__":
    sys.exit(main())
