#!/usr/bin/env python3
"""Apply factual enrollment copy corrections to the partitioned offline export.

The accepted source archive stays untouched. Run before importing references;
the output zip and member provenance are ignored local visual artifacts.
"""
import hashlib
import json
from pathlib import Path
import re
import sys
import zipfile

SOURCE_SHA256 = "b4be9064703aaf35d2dd9dd0391c014340d44f0d750cd835331d3ae89e4ace1c"
CORRECTIONS = {
    "Long enough. At least 12 characters;": "Long enough. At least 15 characters;",
    "This passkey unlocked atlas-7": "Passkey checked",
    "It unlocked atlas-7 in this browser a moment ago.": "Your passkey passed its unlock check. Next, save a recovery key.",
    "Recovery key not saved": "Recovery key not checked",
    "</span> unlocked the workspace · a minute ago": "</span> passed its unlock check · a minute ago",
    "</span> opened the workspace · just now": "</span> passed its unlock check · just now",
    "Your recovery kit was downloaded once. Deck does not keep a copy.": "Your recovery kit download was requested. Deck does not keep a copy.",
    'placeholder="7qh2 kmd4 9xtf b3pv r6nc j5lw 2sye 8agk d4mu x9tb q7hz 3frn w5jc"': 'placeholder="DK1-…"',
}
CAPTURE_CONTEXT = """<style data-enrollment-capture-context>
body { padding: 0 !important; }
figure[data-screen-label] > figcaption { display: none !important; }
</style>
"""


def digest(value):
    return hashlib.sha256(value).hexdigest()


def main():
    if len(sys.argv) != 2:
        raise SystemExit("usage: security-enrollment-reference.py <security-partitioned-offline.zip>")
    source = Path(sys.argv[1])
    if digest(source.read_bytes()) != SOURCE_SHA256:
        raise SystemExit("The partitioned source archive does not match the accepted reference.")
    output = Path(__file__).resolve().parents[2] / ".visual-diff" / "enrollment-reference"
    output.mkdir(parents=True, exist_ok=True)
    target = output / "workspace-enrollment-reference.zip"
    members = []
    figures = []
    applied = {text: 0 for text in CORRECTIONS}
    with zipfile.ZipFile(source) as original, zipfile.ZipFile(target, "w", zipfile.ZIP_DEFLATED) as derivative:
        for info in original.infolist():
            before = original.read(info.filename)
            changes = []
            generated = []
            if info.filename.endswith(".dc.html"):
                text = before.decode("utf-8")
                original_figures = dict((match.group(1).split()[0], match.group(0)) for match in re.finditer(r'<figure\b[^>]*data-screen-label="([^"]+)"[\s\S]*?</figure>', text))
                for old, new in CORRECTIONS.items():
                    count = text.count(old)
                    if count:
                        text = text.replace(old, new)
                        applied[old] += count
                        changes.append({"before": old, "after": new, "count": count})
                # The export draws phone artboards inside a desktop canvas.
                # Apply its supplied narrow token scope and the actual Button
                # `block` prop (the export's `full-width` attribute is ignored).
                def phone_context(match):
                    figure_id = match.group(1).split()[0]
                    figure = match.group(0)
                    if figure_id[0] in "123":
                        figure = re.sub(r'(<div[^>]*class="bp-ink-grid"[^>]*style=")', r'\1--type-caps:normal;', figure, count=1)
                    if figure_id in {"1m", "2m", "2n", "3m", "3n"}:
                        figure = figure.replace('<div class="bp-ink-grid"', '<div data-viewport="narrow" class="bp-ink-grid"', 1)
                        figure = figure.replace('full-width="{{ true }}"', 'block="{{ true }}"')
                    return figure
                text = re.sub(r'<figure\b[^>]*data-screen-label="([^"]+)"[\s\S]*?</figure>', phone_context, text)
                for match in re.finditer(r'<figure\b[^>]*data-screen-label="([^"]+)"[\s\S]*?</figure>', text):
                    figure_id = match.group(1).split()[0]
                    if figure_id[0] not in "123":
                        continue
                    frame = re.search(r'<div[^>]*class="bp-ink-grid"[^>]*style="([^"]+)"', match.group(0)).group(1)
                    dimensions = {key: int(re.search(rf'(?:^|;){key}:(\d+)px', frame).group(1)) for key in ("width", "height")}
                    target_member = f"Workspace Enrollment {figure_id}.dc.html"
                    prefix = text[:text.index("</helmet>")] + CAPTURE_CONTEXT + "</helmet>\n"
                    tail = text[text.rindex("</x-dc>") + len("</x-dc>"):]
                    single = prefix + match.group(0) + "\n</x-dc>" + tail
                    target_info = zipfile.ZipInfo(target_member, date_time=info.date_time)
                    target_info.compress_type = zipfile.ZIP_DEFLATED
                    target_info.external_attr = info.external_attr
                    derivative.writestr(target_info, single.encode())
                    generated.append({"member": target_member, "sha256": digest(single.encode())})
                    figures.append({"id": figure_id, "label": match.group(1), **dimensions,
                                    "member": target_member, "comp": f"workspace-enrollment-{figure_id}",
                                    "sourceSha256": digest(original_figures[figure_id].encode()),
                                    "derivedSha256": digest(match.group(0).encode()),
                                    "narrowContext": figure_id in {"1m", "2m", "2n", "3m", "3n"},
                                    "contextCorrections": ["Interface copy uses sentence case without small caps", "One figure per document; hide the outer caption and remove body padding so the unchanged artboard starts at 0,0"] + (["Use supplied data-viewport=narrow token scope", "Map unsupported full-width attribute to existing Button block prop"] if figure_id in {"1m", "2m", "2n", "3m", "3n"} else [])})
                members.append({"member": info.filename, "sourceSha256": digest(before), "generated": generated, "corrections": changes})
            else:
                derivative.writestr(info, before)
                members.append({"member": info.filename, "sourceSha256": digest(before), "derivedSha256": digest(before)})
    if missing := [text for text, count in applied.items() if count == 0]:
        target.unlink()
        raise SystemExit(f"Expected copy was not present: {missing}")
    if len(figures) != 20:
        raise SystemExit(f"Expected 20 enrollment figures, found {len(figures)}")
    provenance = {"sourceSha256": SOURCE_SHA256, "derivedSha256": digest(target.read_bytes()),
                  "captureOrigin": {"rule": "One figure per document at origin 0,0. Only outer caption visibility and body padding change; the intrinsic frame markup is unchanged.", "contextCssSha256": digest(CAPTURE_CONTEXT.encode())},
                  "figures": figures, "members": members}
    (output / "provenance.json").write_text(json.dumps(provenance, indent=2) + "\n")
    print(target)
    print(provenance["derivedSha256"])


if __name__ == "__main__":
    main()
