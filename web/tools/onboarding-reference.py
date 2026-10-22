#!/usr/bin/env python3
"""Freeze accepted onboarding-r1 states without changing the original archive.

Only generic Deck-owned screens are new pixel targets. Reused cryptographic
forms retain their existing security references; external choosers and the
schematic workspace are interaction boundaries, not owned pixel targets.
"""
import base64
import hashlib
import json
from pathlib import Path
import re
import sys
import zipfile

SOURCE_SHA256 = "c8a5b5705e60f7bcbbeec26a0a6665c84ab570e9427cdff000832d6d23eafba1"
MEMBER = "Workspace Onboarding Flow.dc.html"
MEMBER_SHA256 = "ce8192d72e21560bf1c252c135b1c6f534c76fa6a91165b1a185fe83b5df742b"
IDS = "welcome welcome-required welcome-unavailable needlink expired used notowner unreachable interrupted resume encryption encryption-required encryption-unavailable encryption-boundary finishing finishing-unencrypted unconfirmed failed done done-unencrypted signin signin-unencrypted welcome-mobile needlink-mobile encryption-mobile encryption-required-mobile encryption-unavailable-mobile resume-mobile done-mobile".split()
CORRECTIONS = {
    "The Google account you sign in with becomes": "The eligible Google account you sign in with becomes",
    'name="clock"': 'name="triangle-alert"',
    "This workspace always encrypts what it stores.": "This workspace requires encryption for its stored data.",
    "This installation cannot use Deck encryption at the moment, so atlas-7 is set up without it.": "This installation does not support Deck encryption. You can finish setting up atlas-7 without it.",
    "Nothing has been started or changed.": "Checking again does not start or change setup.",
    "Setup of atlas-7 started on 21 September.": "Setup of atlas-7 has not finished; it started on 21 September.",
    "Continue to your recovery key</x-import>": "Verify your saved unlock method</x-import>",
    "['Workspace encryption','On']": "['Workspace encryption','Selected']",
    "['Signed in with Google','Done']": "['Google account','Done']",
    "['Recovery key','Not checked yet']": "['Recovery key','Not checked']",
    "If this tab closes, opening atlas-7 again brings you back here.": "If this tab closes, sign in to atlas-7 again to check its progress. Deck may ask for your saved unlock method before continuing.",
    "The connection to atlas-7 dropped while setup was finishing.": "The result of setup on atlas-7 is not known.",
    "atlas-7 is not ready, and it is still set to use encryption. Your {{ methodNoun }} and recovery key are kept, so trying again does not repeat those steps.": "atlas-7 is not ready, and it is still set to use encryption. Verify your saved {{ methodNoun }} to continue safely; Deck checks the existing setup before proceeding.",
    ">Try again</x-import>": ">Verify and continue</x-import>",
    "atlas-7 is already set up. After signing in, this browser verifies an unlock method before it connects.": "This workspace is already set up. After sign-in, verify an unlock method to connect this browser when required.",
    "atlas-7 is already set up. Signing in on this browser takes you straight to it.": "This workspace is already set up. Sign in with its owner account to continue.",
    "Other browsers sign in with Google, then verify an unlock method before they connect.": "Google signs you in. Your unlock methods or recovery key open the workspace. Keep your saved recovery key somewhere independent.",
    "Sign-in is still required. Stored workspace data is protected only by whatever the host\\'s storage provides.": "You sign in with Google. Deck encryption is off; any protection the host\\'s storage provides is separate.",
}
CONTEXT = """<style data-onboarding-reference-context>
body { margin:0 !important; padding:0 !important; }
figure { margin:0; }
[data-screen-label] .bp-ink-grid { border:0 !important; border-radius:0 !important; max-width:none !important; }
[data-screen-label] { --type-caps:normal; }
[data-viewport="narrow"], [data-viewport="narrow"] * { overscroll-behavior:none; }
@media(max-width:600px) { [data-screen-label] button { min-height:44px; } }
</style>"""

def sha(data): return hashlib.sha256(data).hexdigest()
def frame_end(source, start):
    depth = 0
    for match in re.finditer(r"</?div\b[^>]*>", source[start:]):
        depth += -1 if match.group().startswith("</") else 1
        if depth == 0: return start + match.end()
    raise ValueError("Unclosed frame")
def prototype_controls(source, next_action, label):
    # A prototype group can include nested sc-if; select by balanced span tags.
    token = '<span style="display:flex;flex-direction:column;gap:9px;padding:12px 13px;border:1px dashed'
    start = source.index(token)
    depth = 0
    for match in re.finditer(r"</?span\b[^>]*>", source[start:]):
        depth += -1 if match.group().startswith("</") else 1
        if depth == 0:
            end = start + match.end(); break
    replacement = '' if not label else f'<span style="display:flex"><x-import component-from-global-scope="BlueprintDesignSystem_ac1da4.Button" variant="primary" block="{{{{ true }}}}" icon="refresh-cw" onClick="{{{{ {next_action} }}}}" hint-size="100%,44px">{label}</x-import></span>'
    return source[:start] + replacement + source[end:]

def main():
    if len(sys.argv) != 3: raise SystemExit("usage: onboarding-reference.py <accepted-Deck.zip> <SRI-verified-deps-directory>")
    source, deps = Path(sys.argv[1]), Path(sys.argv[2])
    if sha(source.read_bytes()) != SOURCE_SHA256: raise SystemExit("Wrong accepted onboarding archive")
    output = Path(__file__).resolve().parents[2] / ".visual-diff/onboarding-reference"
    output.mkdir(parents=True, exist_ok=True)
    members, figures = {}, {}
    with zipfile.ZipFile(source) as z:
        raw = z.read(MEMBER)
        if sha(raw) != MEMBER_SHA256: raise SystemExit("Wrong onboarding member")
        html = raw.decode()
        for name in z.namelist():
            if name.startswith(("_ds/", "assets/fonts/", "assets/icons/")) or name in ("assets/noisefloor-lockup-inverse.svg", "support.js"):
                if not name.endswith("/"): members["onboarding-assets/" + name] = z.read(name)
    # Support dependencies remain the exact published SRI bytes, served locally.
    support = members["onboarding-assets/support.js"].decode()
    dep_records = {}
    for constant, filename in [("REACT", "react.js"), ("REACT_DOM", "react_dom.js"), ("BABEL", "babel.js")]:
        url = re.search(rf'var {constant}_URL = "([^"]+)"', support).group(1)
        expected = re.search(rf'var {constant}_SRI = "sha384-([^"]+)"', support).group(1)
        data = (deps / filename).read_bytes()
        if base64.b64encode(hashlib.sha384(data).digest()).decode() != expected: raise SystemExit(f"SRI mismatch: {constant}")
        members["onboarding-assets/deps/" + filename] = data
        support = support.replace(url, "./onboarding-assets/deps/" + filename)
        dep_records[filename] = {"url": url, "sha256": sha(data), "sha384": expected}
    members["onboarding-assets/support.js"] = support.encode()
    corrected = html
    for before, after in CORRECTIONS.items():
        if before not in corrected: raise SystemExit(f"Missing correction target: {before}")
        corrected = corrected.replace(before, after)
    # Four design-only outcome pickers become real observation/no-action surfaces.
    corrected = prototype_controls(corrected, "goNeedLink", "Check again")
    corrected = prototype_controls(corrected, "goDone", "")
    corrected = prototype_controls(corrected, "goDone", "")
    corrected = prototype_controls(corrected, "goDone", "Reconnect and check")
    resume_label = 'Verify your saved unlock method</x-import>'
    at = corrected.index(resume_label)
    button_start = corrected.rfind('<span style="display:flex">', 0, at)
    corrected = corrected[:button_start] + '<p style="margin:0;font-size:var(--type-body-sm-size);line-height:1.55;color:var(--text-secondary);text-wrap:pretty">Verify your saved unlock method before continuing. If a new recovery key is needed, save and check its replacement.</p>\n' + corrected[button_start:]
    # Resume has no trustworthy step ordinal until the server supplies its stage.
    resume_start = corrected.index('<sc-if value="{{ isResume }}"')
    resume_end = corrected.index('<sc-if value="{{ isEncOptional }}"')
    resume = corrected[resume_start:resume_end]
    resume = re.sub(r'<span style="font-size:var\(--type-micro-size\)[^>]*>\{\{ stepText \}\}</span>', '', resume)
    corrected = corrected[:resume_start] + resume + corrected[resume_end:]
    header_end = corrected.index('</helmet>') + len('</helmet>')
    start = corrected.index('<div class="bp-ink-grid"')
    end = frame_end(corrected, start)
    frame = corrected[start:end].replace('style="--grid-line:', 'style="--type-caps:normal;--grid-line:', 1)
    frame = frame.replace('<h1 style="', '<h1 style="font-family:var(--font-ui);text-transform:none;')
    code = corrected[corrected.index('<script type="text/x-dc"'):]
    for ident in IDS:
        mobile = ident.endswith('-mobile')
        base = ident.removesuffix('-mobile')
        step = base.split('-')[0]
        policy = 'required' if '-required' in base else 'unavailable' if '-unavailable' in base else 'optional'
        encrypted = 'false' if base.endswith('-unencrypted') else 'true' if step in ('resume','finishing','failed','done','signin') else 'null'
        statecode = code.replace("step:'welcome'", f"step:'{step}'", 1).replace("device:'desktop'", f"device:'{'mobile' if mobile else 'desktop'}'", 1).replace("policy: this.props.policy ?? 'optional'", f"policy:'{policy}'", 1).replace("encrypted:null", f"encrypted:{encrypted}", 1).replace("protectOpen:false", f"protectOpen:{str(base == 'encryption-boundary').lower()}", 1)
        width, height = (393, 852) if mobile else (1180, 760)
        doc = corrected[:header_end] + CONTEXT + f'<figure data-screen-label="{ident}" style="width:{width}px;height:{height}px">' + frame + '</figure></x-dc>' + statecode
        # Namespace assets so prior design imports cannot overwrite their bytes.
        doc = doc.replace('src="./support.js"', 'src="onboarding-assets/support.js"').replace('href="_ds/', 'href="onboarding-assets/_ds/').replace('src="_ds/', 'src="onboarding-assets/_ds/').replace("'assets/icons/'", "'onboarding-assets/assets/icons/'").replace('src="assets/', 'src="onboarding-assets/assets/').replace('url("assets/', 'url("onboarding-assets/assets/')
        if mobile:
            doc = doc.replace('<div class="bp-ink-grid"', '<div data-viewport="narrow" class="bp-ink-grid"', 1)
            doc = doc.replace('min-height:36px', 'min-height:44px')
        if base == 'finishing-unencrypted':
            doc = doc.replace(' Deck may ask for your saved unlock method before continuing.', '')
        member = f'onboarding-{ident}.dc.html'
        members[member] = doc.encode()
        figures[ident] = {"member": member, "sourceState": step, "policy": policy, "encrypted": encrypted, "viewport": {"width": width, "height": height}, "sha256": sha(doc.encode())}
    archive = output / 'onboarding-references.zip'
    with zipfile.ZipFile(archive, 'w', zipfile.ZIP_DEFLATED) as z:
        for name, data in sorted(members.items()):
            info = zipfile.ZipInfo(name, (1980,1,1,0,0,0)); info.compress_type = zipfile.ZIP_DEFLATED; z.writestr(info, data)
    record = {"sourceSha256": SOURCE_SHA256, "sourceMember": MEMBER, "sourceMemberSha256": MEMBER_SHA256, "derivedSha256": sha(archive.read_bytes()), "figures": figures, "corrections": CORRECTIONS, "dependencies": dep_records, "members": {n: sha(d) for n,d in sorted(members.items())}, "context": "Deterministic Component state; remove navigation harness/outcome controls; one frame at integer origin per document; remove mock-device border/radius; sentence case with the existing UI optical font cut for headings below 28px; explicit supplied narrow token scope, canonical mobile overscroll suppression, and 44px mobile touch targets; omit unlock-method caption for unencrypted finishing; namespace assets. No pixel masks or threshold changes. Existing security forms remain covered by their original references."}
    (output/'provenance.json').write_text(json.dumps(record, indent=2)+'\n')
    print(archive); print(record['derivedSha256'])
if __name__ == '__main__': main()
