#!/usr/bin/env python3
"""Frame the unmodified r52/r9 login surfaces for visual comparison."""
import argparse
import base64
import hashlib
import html
import json
import re
import zipfile
from pathlib import Path

SOURCE_SHA = "f4d1ce37c57fcbbd2065850ccba58291f7edd15e306a9edba843f581ac229acd"
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("source", type=Path)
parser.add_argument("dependencies", type=Path)
parser.add_argument("output", type=Path)
parser.add_argument("--font-set", choices=["licensed", "ofl", "both"], default="both")
args = parser.parse_args()
if hashlib.sha256(args.source.read_bytes()).hexdigest() != SOURCE_SHA:
    raise ValueError("Unexpected source export")
if args.output.exists():
    raise FileExistsError("Preserve earlier references; choose a new output directory")
args.output.mkdir(parents=True)
web = Path(__file__).resolve().parents[1]
with zipfile.ZipFile(args.source) as archive:
    members = {name: archive.read(name) for name in archive.namelist()
               if name.startswith(("assets/", "_ds/")) and not name.endswith("/")}
    support = archive.read("support.js").decode()
    for name in ["REACT", "REACT_DOM", "BABEL"]:
        url = re.search(r'var ' + name + r'_URL = "([^"]+)"', support)[1]
        sri = re.search(r'var ' + name + r'_SRI = "([^"]+)"', support)[1]
        data = (args.dependencies / (name.lower() + ".js")).read_bytes()
        if "sha384-" + base64.b64encode(hashlib.sha384(data).digest()).decode() != sri:
            raise ValueError(f"Dependency integrity mismatch: {name}")
        target = "deps/" + name.lower() + ".js"
        members[target] = data
        support = support.replace(url, target)
    members["support.js"] = support.encode()
    records = []
    for font_set in (["licensed", "ofl"] if args.font_set == "both" else [args.font_set]):
        for device, filename in [("desktop", "Workspace 6 Desktop Polish.dc.html"), ("mobile", "Workspace 5 Mobile.dc.html")]:
            original = archive.read(filename)
            source = original.decode().replace("data-screen-label=", "data-source-screen-label=")
            if device == "desktop":
                def pending(match):
                    props = json.loads(html.unescape(match[1]))
                    props["codexLogin"]["default"] = "pending"
                    props["tutorial"]["default"] = "off"
                    return 'data-props="' + html.escape(json.dumps(props), quote=True) + '"'
                source = re.sub(r'data-props="([^"]*)"', pending, source, count=1)
                anchor = 'class="bp-dialog" role="dialog" aria-modal="true" aria-labelledby="cx-title"'
            else:
                start = source.index('data-source-screen-label="16 Finish Codex sign-in"')
                before, source = source[:start], source[start:]
                anchor = 'role="dialog" aria-modal="true"'
            if anchor not in source:
                raise ValueError(f"Missing dialog anchor: {filename}")
            source = source.replace(anchor, anchor + ' data-screen-label="dialog"', 1)
            if device == "mobile":
                source = before + source
            if font_set == "ofl":
                css = (web / "src/styles/fonts/free.css").read_text().replace('/fonts/free/', './fonts/free/').replace(':root {', 'html:root {')
                # The export embeds mock index.html listings that carry their own
                # '</head>' strings; only the initial document head, verified to
                # close immediately before <body>, receives the overlay.
                close, body = source.find('</head>'), source.find('<body')
                if not source.startswith('<!DOCTYPE html>') or not 0 <= source.find('<head>') < close < body or source[close + len('</head>'):body].strip():
                    raise ValueError(f"Unexpected document head: {filename}")
                source = source[:close] + '<style>' + css + '</style>' + source[close:]
                for path in (web / "public/fonts/free").glob('*'):
                    if path.is_file(): members['fonts/free/' + path.name] = path.read_bytes()
            comp = f'harness-login-{device}-{font_set}'
            members[comp + '.dc.html'] = source.encode()
            records.append({"comp": comp + "#dialog", "source": filename, "sourceSha256": hashlib.sha256(original).hexdigest(),
                            "state": "pending" if device == "desktop" else "invalid"})
with zipfile.ZipFile(args.output / "references.zip", "w", zipfile.ZIP_DEFLATED) as archive:
    for name, data in sorted(members.items()): archive.writestr(name, data)
(args.output / "provenance.json").write_text(json.dumps({"sourceSha256": SOURCE_SHA, "fontSet": args.font_set,
    "transforms": ["Select desktop pending state and disable tutorial", "Annotate only the complete login dialog for cropping", "Bundle integrity-verified original runtime dependencies", "OFL mode overlays the complete free.css (font faces, typography variables, tracking, word spacing and body rules) with bundled font URLs; html:root keeps the export runtime from overriding the selected type set"],
    "unchanged": "Layout, copy, controls, placeholders, and mobile invalid example remain the supplied design, including known contract differences.",
    "states": records}, indent=2) + '\n')
print(args.output / "references.zip")
