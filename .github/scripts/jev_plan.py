"""Read a credential-free CI plan published by the trusted-main workflow."""
import io
import json
import math
import os
import subprocess
import time
import zipfile

MODEL = "jev-1.14.0"
SUITES = ("documentation", "frontend", "probes", "lint", "go-serialized", "go-parallel",
          "deploy-suite", "experiment-enabled", "jail-probe", "source-export")
QUESTIONS = set(SUITES) - {"probes"} | {"browser-ui", "browser-gateway", "browser-previews"}


def selection(probabilities):
    jobs = {"documentation"} | {s for s in SUITES if s != "probes" and probabilities[s] >= .5}
    shards = []
    for name, lanes in (("browser-ui", [1, 2, 3]), ("browser-gateway", ["gateway"]), ("browser-previews", ["previews"])):
        if probabilities[name] >= .5:
            shards.extend(lanes)
    if shards:
        jobs.add("probes")
    return {"mode": "selective", "jobs": [s for s in SUITES if s in jobs], "probe_shards": shards or [1, 2, 3]}


def api(path, raw=False):
    result = subprocess.run(["gh", "api", path], capture_output=True, timeout=15)
    if result.returncode or len(result.stdout) > 1_000_000:
        raise ValueError("plan_read_failed")
    return result.stdout if raw else json.loads(result.stdout)


def validate(plan, repo, run_id, event, base, head, source_sha):
    if (plan.get("schema") != 1 or plan.get("repository") != repo
            or type(plan.get("target_run_id")) is not int or plan["target_run_id"] != int(run_id)
            or plan.get("event") != event or plan.get("base_sha") != base or plan.get("head_sha") != head
            or plan.get("planner_source_sha") != source_sha or plan.get("model") != MODEL
            or plan.get("threshold") != .5 or plan.get("mode") != "selective"):
        raise ValueError("plan_identity_mismatch")
    probabilities = plan.get("probabilities")
    if not isinstance(probabilities, dict) or set(probabilities) != QUESTIONS:
        raise ValueError("invalid_plan_questions")
    if any(type(p) not in (int, float) or not math.isfinite(p) or not 0 <= p <= 1 for p in probabilities.values()):
        raise ValueError("invalid_plan_probability")
    chosen = selection(probabilities)
    if plan.get("jobs") != chosen["jobs"] or plan.get("probe_shards") != chosen["probe_shards"]:
        raise ValueError("plan_decision_mismatch")
    return {"mode": "selective", "jobs": plan["jobs"], "probe_shards": plan["probe_shards"],
            "reason": "Jev default selection at 50%; documentation minimum retained", "selector": "jev"}


def lookup(repo, run_id, event, base, head, request=api):
    prefix = "repos/" + repo
    artifacts = request(prefix + f"/actions/artifacts?name=jev-ci-plan-{int(run_id)}&per_page=10")
    for artifact in artifacts["artifacts"]:
        if artifact.get("expired") or artifact.get("name") != f"jev-ci-plan-{int(run_id)}":
            continue
        publisher = request(prefix + "/actions/runs/" + str(int(artifact["workflow_run"]["id"])))
        if (publisher.get("path") != ".github/workflows/ci-advisor.yaml"
                or publisher.get("event") not in ("workflow_run", "workflow_dispatch")
                or publisher.get("head_branch") != "main"
                or (publisher.get("repository") or {}).get("full_name") != repo
                or (publisher.get("head_repository") or {}).get("full_name") != repo
                or publisher.get("status") != "completed" or publisher.get("conclusion") != "success"):
            continue
        archive = request(prefix + "/actions/artifacts/" + str(int(artifact["id"])) + "/zip", raw=True)
        with zipfile.ZipFile(io.BytesIO(archive)) as bundle:
            if bundle.namelist() != ["plan.json"] or bundle.getinfo("plan.json").file_size > 32_768:
                raise ValueError("invalid_plan_archive")
            plan = json.loads(bundle.read("plan.json"))
        return validate(plan, repo, run_id, event, base, head, publisher["head_sha"])
    return None


def load(repo, run_id, event, base, head):
    if not os.environ.get("GH_TOKEN"):
        return None
    deadline = time.monotonic() + 60
    try:
        while True:
            if plan := lookup(repo, run_id, event, base, head):
                return plan
            if time.monotonic() >= deadline:
                return None
            time.sleep(5)
    except (KeyError, TypeError, ValueError, OSError, subprocess.SubprocessError, zipfile.BadZipFile):
        return None
