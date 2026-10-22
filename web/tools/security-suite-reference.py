#!/usr/bin/env python3
"""Combine verified security derivatives without changing any rendered member."""
import hashlib
import json
from pathlib import Path
import re
import subprocess
import sys
import zipfile

SOURCE_SHA256 = "b4be9064703aaf35d2dd9dd0391c014340d44f0d750cd835331d3ae89e4ace1c"
EXPECTED_IDS = set("1a 1b 1c 1d 1m 2a 2b 2c 2d 2e 2f 2m 2n 3a 3b 3c 3d 3e 3m 3n 4a 4b 4c 4e 4f 4g 4h 4i 4j 4k 4l 4m 4n 5b 5c 5d 5e 5f 5m 6a 6b 6c 6d 6e 6m 7a 7b 7c 7d 7m".split())


def digest(value):
    return hashlib.sha256(value).hexdigest()


def main():
    if len(sys.argv) != 2:
        raise SystemExit("usage: security-suite-reference.py <security-partitioned-offline.zip>")
    source = Path(sys.argv[1])
    if digest(source.read_bytes()) != SOURCE_SHA256:
        raise SystemExit("The input does not match the accepted partitioned source.")
    tools = Path(__file__).resolve().parent
    artifacts = tools.parent.parent / ".visual-diff"
    for helper in ("security-runtime-reference.py", "security-management-reference.py"):
        subprocess.run([sys.executable, str(tools / helper), str(source)], check=True, stdout=subprocess.DEVNULL)
    inputs = [
        ("runtime-reference", "workspace-security-reference.zip"),
        ("management-reference", "workspace-management-reference.zip"),
    ]
    members = {}
    provenance = []
    figures = {}
    for directory, archive in inputs:
        path = artifacts / directory / archive
        record_path = path.with_name("provenance.json")
        record = json.loads(record_path.read_text())
        archive_hash = digest(path.read_bytes())
        if record["sourceSha256"] != SOURCE_SHA256 or record["derivedSha256"] != archive_hash:
            raise SystemExit(f"Derivative provenance mismatch: {directory}")
        provenance.append({"archive": f"{directory}/{archive}", "sha256": archive_hash,
                           "provenanceSha256": digest(record_path.read_bytes())})
        with zipfile.ZipFile(path) as derivative:
            for member in derivative.namelist():
                data = derivative.read(member)
                if member in members:
                    if members[member] != data:
                        raise SystemExit(f"Conflicting duplicate asset: {member}")
                    continue
                members[member] = data
                if member.endswith(".dc.html"):
                    labels = re.findall(r'<figure\b[^>]*data-screen-label="([^"]+)"', data.decode())
                    if len(labels) != 1:
                        raise SystemExit(f"Expected one figure: {member}")
                    figure_id = labels[0].split()[0]
                    if figure_id in figures:
                        raise SystemExit(f"Duplicate figure: {figure_id}")
                    figures[figure_id] = {"member": member, "label": labels[0], "sha256": digest(data)}
    if set(figures) != EXPECTED_IDS:
        raise SystemExit("The combined archive must contain exactly the 50 security pixel targets.")
    output = artifacts / "security-suite-reference"
    output.mkdir(parents=True, exist_ok=True)
    target = output / "workspace-security-suite.zip"
    with zipfile.ZipFile(target, "w", zipfile.ZIP_DEFLATED) as combined:
        for name in sorted(members):
            info = zipfile.ZipInfo(name, date_time=(1980, 1, 1, 0, 0, 0))
            info.compress_type = zipfile.ZIP_DEFLATED
            combined.writestr(info, members[name])
    record = {"sourceSha256": SOURCE_SHA256, "derivedSha256": digest(target.read_bytes()),
              "inputs": provenance, "figures": figures,
              "members": {name: digest(data) for name, data in sorted(members.items())},
              "rule": "Member bytes are unchanged. Duplicate names require byte identity. Figures 4d and 5a remain integration acceptance references."}
    (output / "provenance.json").write_text(json.dumps(record, indent=2) + "\n")
    print(target)
    print(record["derivedSha256"])


if __name__ == "__main__":
    main()
