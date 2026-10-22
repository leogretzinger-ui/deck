#!/usr/bin/env python3
"""Frame the approved r52/r9 global menus, reshaping only the accepted menu rows and fixture defaults."""
import argparse
import base64
import hashlib
import html
import json
import re
import zipfile
from pathlib import Path

SOURCE_SHA = "f4d1ce37c57fcbbd2065850ccba58291f7edd15e306a9edba843f581ac229acd"


def reshape(source, filename, edits):
    for old, new in edits:
        found = source.count(old)
        if found != 1:
            raise ValueError(f"Expected exactly one match in {filename}, found {found}: {old[:72]!r}")
        source = source.replace(old, new)
    return source


# Accepted control reshapes; every other byte of the frames stays the supplied design.
DESKTOP_MENU_EDITS = [
    (
        '            <div onClick="{{ toggleTutorial }}" data-hint="Show the tutorial panel. Your lesson progress is kept" class="bp-menuitem" style="gap:var(--space-3)">\n'
        '              <span style="flex:1;min-width:0;color:{{ tutorialLabel }}">Show tutorial</span>\n'
        '            </div>',
        '            <button type="button" onClick="{{ toggleTutorial }}" data-hint="Show the tutorial panel. Your lesson progress is kept" class="bp-menuitem">Show tutorial</button>',
    ),
    (
        '            <button type="button" onClick="{{ openGhAccount }}" data-hint="The GitHub account Deck clones and pushes with — not who commits are signed as" class="bp-menuitem">GitHub account…</button>',
        '            <button type="button" class="bp-menuitem">Finish harness login</button>\n'
        '            <button type="button" onClick="{{ openGhAccount }}" data-hint="The GitHub account Deck clones and pushes with — not who commits are signed as" class="bp-menuitem">GitHub connection…</button>',
    ),
]
MOBILE_FIXTURE_EDITS = [
    (
        'state = { yolo: true, confirmClose: false,',
        'state = { yolo: false, confirmClose: true,',
    ),
]
MOBILE_SETTINGS_EDITS = [
    (
        '          <div class="bp-row bp-row--center" style="gap:14px">\n'
        '            <span class="bp-row__label" style="color:var(--text-primary)">Yolo mode</span>',
        '          <div class="bp-row bp-row--center">\n'
        '            <span class="bp-row__label">Show tutorial</span>\n'
        '          </div>\n'
        '          <div class="bp-row bp-row--center" style="gap:14px">\n'
        '            <span class="bp-row__label" style="color:var(--text-primary)">Yolo mode</span>',
    ),
    (
        '          <div class="bp-row bp-row--center">\n'
        '            <span class="bp-row__label">Sign out</span>\n'
        '          </div>',
        '          <div class="bp-row bp-row--center">\n'
        '            <span class="bp-row__label">Finish harness login</span>\n'
        '          </div>\n'
        '          <div class="bp-row bp-row--center">\n'
        '            <span class="bp-row__label">GitHub connection…</span>\n'
        '          </div>\n'
        '          <div class="bp-row bp-row--center">\n'
        '            <span class="bp-row__label">Sign out</span>\n'
        '          </div>',
    ),
]
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
                def menu(match):
                    props = json.loads(html.unescape(match[1]))
                    props["codexLogin"]["default"] = "off"
                    props["tutorial"]["default"] = "off"
                    return 'data-props="' + html.escape(json.dumps(props), quote=True) + '"'
                if len(re.findall(r'data-props="([^"]*)"', source)) != 1:
                    raise ValueError("Expected one desktop properties declaration")
                source = re.sub(r'data-props="([^"]*)"', menu, source, count=1)
                if source.count("menuOpen: !!this.state.menuOpen,") != 1:
                    raise ValueError("Expected one desktop menu visibility binding")
                source = source.replace("menuOpen: !!this.state.menuOpen,", "menuOpen: true,", 1)
                source = reshape(source, filename, DESKTOP_MENU_EDITS)
                for label, shortcut in [("Show side panel", "Ctrl+B"), ("Show info bar", "Ctrl+/"), ("Enable yolo mode", "Ctrl+Alt+Y")]:
                    pattern = (re.escape(label + '</span>') + r'(\s*)'
                               r'(<span style="flex:none;display:inline-flex;align-items:center;gap:0.34em;color:var\(--text-tertiary\);white-space:nowrap">)'
                               r'(?:<span[^>]*>[^<]*</span>)+</span>')
                    if len(re.findall(pattern, source)) != 1:
                        raise ValueError(f"Expected one desktop shortcut fixture: {label}")
                    source = re.sub(pattern, lambda m: label + '</span>' + m[1] + m[2] + shortcut + '</span>', source, count=1)
                for action in ["fontDec", "fontInc"]:
                    pattern = r'(<button type="button" onClick="{{ ' + action + r' }}" style=")([^"]+)(")'
                    matches = list(re.finditer(pattern, source))
                    if len(matches) != 1:
                        raise ValueError(f"Expected one desktop font control: {action}")
                    original_style = matches[0][2]
                    voice = 'font-family:var(--font-mono);letter-spacing:var(--type-mono-tracking);font-weight:var(--type-mono-weight);font-size:min(var(--type-mono-size),calc(12px * var(--type-mono-xh-ratio)))'
                    if original_style.count(voice) != 1:
                        raise ValueError(f"Unexpected font control typography: {action}")
                    source = source.replace(original_style, original_style.replace(voice, 'font-family:var(--font-ui);font-weight:400;font-size:12px'), 1)
                anchor = 'class="bp-menu" style="position:absolute;top:calc(100% + 6px);right:0;z-index:70;min-width:252px"'
            else:
                source = reshape(source, filename, MOBILE_FIXTURE_EDITS)
                start = source.index('data-source-screen-label="07 Settings"')
                end = source.index("</figure>", start) + len("</figure>")
                before, source, after = source[:start], source[start:end], source[end:]
                account_pattern = (
                    r'(<span class="bp-row__label">Sign out</span>\n)'
                    r'            <span style="flex:none;font-size:var\(--type-body-sm-size\);color:var\(--text-tertiary\)">[^<\n]+</span>\n'
                )
                if len(re.findall(account_pattern, source)) != 1:
                    raise ValueError("Expected one Settings sign-out account suffix")
                source = re.sub(account_pattern, r'\1', source, count=1)
                source = reshape(source, filename, MOBILE_SETTINGS_EDITS)
                source = reshape(source, filename, [
                    ('<span style="flex:1;font-size:var(--type-title-3-size);font-weight:var(--weight-medium);color:var(--text-primary)">Session settings</span>', '<span style="flex:1;font-size:17px;font-weight:var(--weight-medium);color:var(--text-primary)">Session settings</span>'),
                    ('background:#181D26;border:1px solid var(--border)', 'background:#0A1522;border:1px solid var(--border)'),
                    ('<div data-viewport="narrow" style="position:relative;flex:none;width:393px;height:852px;', '<div data-viewport="narrow" data-menu-reference-viewport style="position:relative;flex:none;width:393px;height:852px;'),
                    ('border:1px solid var(--border-strong);border-bottom-width:35px;border-bottom-color:transparent;border-radius:44px;', 'border:0;border-radius:0;'),
                    ('min-width:34px;text-align:center;font-family:var(--font-mono);letter-spacing:var(--type-mono-tracking);font-weight:var(--type-mono-weight);font-size:min(var(--type-mono-size),calc(var(--type-body-sm-size) * var(--type-mono-xh-ratio)))',
                     'min-width:34px;text-align:center;font-family:var(--font-ui);font-weight:400;font-size:var(--type-body-sm-size)'),
                ])
                anchor = 'class="bp-dialog"'
            if source.count(anchor) != 1:
                raise ValueError(f"Missing menu anchor: {filename}")
            source = source.replace(anchor, anchor + ' data-screen-label="menu" data-menu-reference-surface', 1)
            if device == "mobile":
                source = before + source + after
            prologue = '<!DOCTYPE html>\n<html>\n<head>\n'
            if not source.startswith(prologue):
                raise ValueError(f"Unexpected document prologue: {filename}")
            close = source.find('</head>', len(prologue))
            if not source.startswith('</head>\n<body>', close):
                raise ValueError(f"Unexpected closing head boundary: {filename}")
            framing = (web / "tools/global-menu-reference-frame.js").read_text()
            source = source[:close] + '<script>' + framing + '</script>' + source[close:]
            if font_set == "ofl":
                css = (web / "src/styles/fonts/free.css").read_text().replace('/fonts/free/', './fonts/free/').replace(':root {', 'html:root {')
                # The mock index.html has another </head>; insert only in the outer document.
                prologue = '<!DOCTYPE html>\n<html>\n<head>\n'
                if not source.startswith(prologue):
                    raise ValueError(f"Unexpected document prologue: {filename}")
                close = source.find('</head>', len(prologue))
                if not source.startswith('</head>\n<body>', close):
                    raise ValueError(f"Unexpected closing head boundary: {filename}")
                source = source[:close] + '<style>' + css + '</style>' + source[close:]
                for path in (web / "public/fonts/free").glob('*'):
                    if path.is_file(): members['fonts/free/' + path.name] = path.read_bytes()
            comp = f'global-menu-{device}-{font_set}'
            members[comp + '.dc.html'] = source.encode()
            records.append({"comp": comp + "#menu", "source": filename, "sourceSha256": hashlib.sha256(original).hexdigest(),
                            "derivedSha256": hashlib.sha256(source.encode()).hexdigest(),
                            "state": "menu-open" if device == "desktop" else "07 Settings"})
with zipfile.ZipFile(args.output / "references.zip", "w", zipfile.ZIP_DEFLATED) as archive:
    for name, data in sorted(members.items()):
        info = zipfile.ZipInfo(name, date_time=(1980, 1, 1, 0, 0, 0))
        archive.writestr(info, data, compress_type=zipfile.ZIP_DEFLATED)
(args.output / "provenance.json").write_text(json.dumps({"sourceSha256": SOURCE_SHA, "derivedArchiveSha256": hashlib.sha256((args.output / "references.zip").read_bytes()).hexdigest(), "fontSet": args.font_set,
    "transforms": ["Open the desktop global menu and disable tutorial/sign-in overlays",
        "Desktop menu: convert the Show tutorial toggle div into a plain bp-menuitem action button using the menu's supplied button recipe, keeping its onClick binding and data-hint",
        "Desktop menu: insert a Finish harness login action row before the GitHub row and rename GitHub account… to GitHub connection…",
        "Mobile 07 Settings sheet: insert Show tutorial before Yolo mode in the second group and Finish harness login plus GitHub connection… before Sign out in the third group, each cloned from the sheet's simple Sign out bp-row shape, and remove only the sample email from the Sign out row",
        "Mobile 07 Settings fixture: normalize the comp state initializer to yolo false and confirmClose true; the exported data-props does not feed those switches, so the fixture state is edited directly",
        "the export's `_ds/blueprint-ac1da416-122f-4dbd-8421-498543a11fe7/tokens/typography.css` defines `--type-title-3-size: 17px` at root with a `max-width: 1800px` override to 16px that the 393px phone width inherits, so the export mobile title renders 16px; this adaptation deliberately departs from that 16px to preserve Deck's accepted shared phone heading contract of 17px/500, holding size/weight fixed while tracking, word-spacing, and line-height converge to the inherited body recipe.", "Normalize only the three desktop shortcut fixtures to Linux Ctrl labels", "Mobile Noisefloor swatch fixture uses the accepted terminal theme background #0A1522; foreground #E4E1D8 and cursor #E0764E remain supplied", "Normalize the selected mobile hardware wrapper to its declared 393px application viewport, without hardware border/radius, and move that whole declared viewport to document origin using guarded relative left/top positioning, avoiding fractional transform rasterization; runtime checks preserve every visible descendant rectangle relative to its viewport", "Use interface type for desktop font-size stepper labels and mobile numeric setting, following the UI-copy rule", "Enclose each independently rendered menu border box with an inert floor/ceil integer frame; readiness requires the completed frame", "Bundle integrity-verified original runtime dependencies", "OFL mode appends the application's complete free.css stylesheet, including font faces, typography variables, tracking, word spacing, and body rules; font URLs use bundled OFL assets, and html:root keeps the export runtime from overriding the selected type set"],
    "unchanged": "Unlisted menu/sheet design bytes remain supplied by the export. Other export frames remain unchanged except for the shared mobile preference initializer.",
    "states": records}, indent=2) + '\n')
print(args.output / "references.zip")
