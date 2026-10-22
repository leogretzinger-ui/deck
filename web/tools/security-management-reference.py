#!/usr/bin/env python3
"""Derive the accepted method figures with factual security copy corrections."""
import hashlib
import json
from pathlib import Path
import re
import sys
import zipfile

SOURCE_SHA256 = "b4be9064703aaf35d2dd9dd0391c014340d44f0d750cd835331d3ae89e4ace1c"
NOTICE = "Removing or replacing a method disconnects browsers authorized by it. Other browsers stay connected. This does not change the storage key or make previously copied data and a working key unreadable."
CORRECTIONS = {
    "added and tested · just now": "active · checked just now",
    "Two old methods can still unlock atlas-7": "3 everyday methods can unlock atlas-7",
    "Add an encryption password too": "Add another method",
    "The old key keeps working until the new one is checked.": "The old key keeps working until the new one is checked and replacement is confirmed.",
    "Added 2 Aug · unlocked 3 hours ago": "Checked 3 hours ago",
    "Added 2 Aug · unlocked 6 days ago": "Checked 6 days ago",
    "Set 2 Aug · never used to unlock": "No unlock check recorded",
    "Checked 2 Aug · for when everything else is gone": "Checked 2 Aug",
    "Unlocked 3 hours ago": "Checked 3 hours ago",
    "Unlocked 6 days ago": "Checked 6 days ago",
    "Never used to unlock": "No unlock check recorded",
    "Security key · just created": "Passkey · just saved",
    "It unlocked atlas-7": "It passed its unlock check",
    "Only you see this list. Name it after the object, not the date.": "Name it after the object, not the date.",
    "The old one keeps working until you remove it": "The old one keeps working until replacement is confirmed",
    "New passkey added": "New passkey saved",
    "New phone passkey · added just now": "New phone passkey",
    "Tested a moment ago, on this browser": "Checked a moment ago, in this browser",
    "Remove the old passkey": "Replace the old passkey",
    "Remove “Phone passkey”": "Replace “Phone passkey”",
    "This is the only way to unlock atlas-7": "This is the only everyday way to unlock atlas-7",
    "Add another passkey or an encryption password, test it, then come back. Removal turns on by itself once a second method has unlocked the workspace.": "Add another passkey or an encryption password, test and enable it, then come back.",
    "The new password is checked by unlocking atlas-7 with it. The old one keeps working until that passes.": "At least 15 characters. You will enter it again to check the saved method.</span><span style=\"font-size:var(--type-caption-size);line-height:var(--type-caption-lh);color:var(--text-tertiary)\">The new password is checked against its saved envelope. The old one keeps working until replacement is confirmed.",
    "Adding or removing methods changes which unlock methods atlas-7 accepts. Browsers already unlocked stay connected until it locks or restarts, and nothing here reaches copies of the data that already left the machine.": NOTICE,
    "This changes which unlock methods the workspace accepts. It does not repair a storage key that was already copied: if data and a working key were taken before now, that copy stays readable. Browsers already unlocked stay connected until atlas-7 locks or restarts.": NOTICE,
    "This changes which unlock methods the workspace accepts. It does not repair a storage key that was already copied, and it does not undo an old password having been seen. Browsers already unlocked stay connected until atlas-7 locks or restarts.": NOTICE,
    "Changes here affect which methods atlas-7 accepts — not copies of data that already left the machine.": NOTICE,
}
CONTEXT = '<style data-management-capture-context>body{padding:0!important}figure[data-screen-label]>figcaption{display:none!important}</style>'


def digest(value):
    return hashlib.sha256(value).hexdigest()


def main():
    if len(sys.argv) != 2:
        raise SystemExit("usage: security-management-reference.py <security-partitioned-offline.zip>")
    source = Path(sys.argv[1])
    if digest(source.read_bytes()) != SOURCE_SHA256:
        raise SystemExit("Source does not match the accepted partitioned reference.")
    output = Path(__file__).resolve().parents[2] / ".visual-diff/management-reference"
    output.mkdir(parents=True, exist_ok=True)
    target = output / "workspace-management-reference.zip"
    figures = []
    with zipfile.ZipFile(source) as original, zipfile.ZipFile(target, "w", zipfile.ZIP_DEFLATED) as derivative:
        for info in original.infolist():
            before = original.read(info.filename)
            if not info.filename.endswith(".dc.html"):
                derivative.writestr(info, before)
                continue
            text = before.decode()
            for match in re.finditer(r'<figure\b[^>]*data-screen-label="((?:6[^"]+|7b[^"]+))"[\s\S]*?</figure>', text):
                figure = match.group(0)
                figure_id = match.group(1).split()[0]
                changes = []
                for old, new in CORRECTIONS.items():
                    if old in figure:
                        changes.append({"before": old, "after": new, "count": figure.count(old)})
                        figure = figure.replace(old, new)
                figure = re.sub(r'<span class="bp-badge bp-badge--sm">This (?:browser|phone)</span>', '', figure)
                if figure_id == "6b":
                    figure = re.sub(r'<span style="font-size:var\(--type-caption-size\);color:var\(--text-tertiary\)">just now</span>', '', figure)
                frame = re.search(r'<div(?: class="[^"]+")? style="[^"]*?width:(\d+)px;height:(\d+)px;', figure)
                width, height = map(int, frame.groups())
                figure = figure[:frame.start()] + figure[frame.start():].replace('style="', 'style="--type-caps:normal;', 1)
                if figure_id == "6m":
                    figure = figure[:frame.start()] + figure[frame.start():].replace('<div style="', '<div data-viewport="narrow" style="', 1)
                    figure = figure.replace('full-width="{{ true }}"', 'block="{{ true }}"')
                member = f"Workspace Management {figure_id}.dc.html"
                single = text[:text.index('</helmet>')] + CONTEXT + '</helmet>\n' + figure + '\n</x-dc>' + text[text.rindex('</x-dc>') + len('</x-dc>'):]
                derivative.writestr(member, single)
                figures.append({"id": figure_id, "label": match.group(1), "width": width, "height": height,
                                "member": member, "sourceSha256": digest(match.group(0).encode()), "derivedSha256": digest(figure.encode()),
                                "documentSha256": digest(single.encode()), "corrections": changes,
                                "contextCorrections": ["Sentence case without small caps", "One artboard at 0,0 with outer annotation hidden", "Remove unverifiable current-browser badge and unsupported unlock timestamps"] + (["Use supplied narrow viewport tokens and supported Button block prop"] if figure_id == "6m" else [])})
    if {figure["id"] for figure in figures} != {"6a", "6b", "6c", "6d", "6e", "6m", "7b"}:
        raise SystemExit("The reference must contain all seven management figures.")
    (output / "provenance.json").write_text(json.dumps({"sourceSha256": SOURCE_SHA256, "derivedSha256": digest(target.read_bytes()), "contextCssSha256": digest(CONTEXT.encode()), "figures": figures}, indent=2) + "\n")
    print(target)
    print(digest(target.read_bytes()))


if __name__ == "__main__":
    main()
