#!/usr/bin/env python3
"""Create auditable runtime capture references without changing the accepted export."""
import hashlib
import json
from pathlib import Path
import re
import subprocess
import sys
import zipfile

SOURCE_SHA256 = "b4be9064703aaf35d2dd9dd0391c014340d44f0d750cd835331d3ae89e4ace1c"
IDS = "4a 4b 4c 4e 4f 4g 4h 4i 4j 4k 4l 4m 4n 5b 5c 5d 5e 5f 5m 7a 7c 7d 7m".split()
MOBILE = {"4m", "4n", "5m", "7m"}
CONTEXT = '<style data-runtime-capture-context>body { padding:0 !important; } figure[data-screen-label] > figcaption { display:none !important; }</style>\n'
CORRECTIONS = {
 "Your passkey is intact and your workspace is unharmed. This browser does not support what unlocking needs.": "This passkey did not provide the encryption result this browser needs. Try another browser or another unlock method.",
 "If you also set an encryption password, that works in any browser.": "If you also set an encryption password, you can use that in a supported browser.",
 "This is not a problem with your passkey or your recovery key — there is nothing to unlock until the machine responds.": "Your unlock methods have not been changed. Check the connection and try again.",
 "Ask operator op-4412": "Get help reaching it",
 "Your passkey answered, but it did not open this workspace's storage. That usually means it belongs to a different workspace, or it was removed from atlas-7's unlock methods.": "The workspace did not confirm an unlock. Check the workspace address and try another saved unlock method.",
 "Your passkey answered, but it did not open this workspace's storage. It may belong to another workspace, or it may have been removed here.": "The workspace did not confirm an unlock. Check the workspace address and try another saved unlock method.",
 "The request was accepted, but one session did not stop and the storage is still open. Your work is still running and still readable on the machine.": "Storage is still open. Your work may still be running and readable on the machine.",
 "Try locking again": "Check workspace state",
 "If it will not stop, operator op-4412 can restart the machine — a restart also leaves atlas-7 locked.": "Checking the result does not repeat the lock request.",
 "If it did lock, you will be asked to unlock. If it did not, you will land back in the workspace and can lock it again.": "Checking the result does not repeat the request.",
 "What is stored in atlas-7 cannot be opened": "Without an unlock method, atlas-7 cannot be opened",
 "Every unlock method for this workspace is gone and there is no recovery key. The stored data stays encrypted, and Deck gives operator op-4412 no override. There is no reset to request.": "If every unlock method for this workspace is gone and there is no recovery key, the stored data stays encrypted. Deck gives whoever runs the machine no override. There is no reset to request.",
 "Your account still works. You can start a fresh workspace and clone your repositories into it.": "Your account still works. Ask whoever runs the machine about a fresh workspace, where you can clone your repositories again.",
 "Operator op-4412 can delete this machine and its encrypted storage when you tell them to.": "Whoever runs the machine can delete the encrypted storage when you ask them to.",
 "Start a new workspace": "Back to unlock methods",
 "Ask the operator to delete atlas-7": "Get help",
 "then add a new unlock method.": "then set up a new unlock method.",
 "operator op-4412 can help with it": "whoever runs the machine can help with it",
 "7qh2 kmd4 9xtf b3pv r6nc j5lw 2sye 8agk d4mu x9tb q7hz 3frn w5jc": "DK1-7QH2-KMD4-9XTF-B3PV-R6NC-J5LW-2SYE-8AGK-D4MU-X9TB-Q7HZ-3FRN-W5JC-5K2A",
}
def digest(value): return hashlib.sha256(value).hexdigest()
def main():
 if len(sys.argv) != 2: raise SystemExit("usage: security-runtime-reference.py <security-partitioned-offline.zip>")
 source = Path(sys.argv[1]); repo = Path(__file__).resolve().parents[2]
 if digest(source.read_bytes()) != SOURCE_SHA256: raise SystemExit("Accepted partition archive hash mismatch")
 subprocess.run([sys.executable, str(Path(__file__).with_name("security-enrollment-reference.py")), str(source)], check=True, stdout=subprocess.DEVNULL)
 output = repo / ".visual-diff/runtime-reference"; output.mkdir(parents=True, exist_ok=True)
 target = output / "workspace-security-reference.zip"
 figures = []; members = []
 with zipfile.ZipFile(source) as original, zipfile.ZipFile(repo / ".visual-diff/enrollment-reference/workspace-enrollment-reference.zip") as enrollment, zipfile.ZipFile(target, "w", zipfile.ZIP_DEFLATED) as derivative:
  for info in enrollment.infolist(): derivative.writestr(info, enrollment.read(info.filename))
  for info in original.infolist():
   if not info.filename.endswith(".dc.html"): continue
   text = original.read(info.filename).decode()
   for match in re.finditer(r'<figure\b[^>]*data-screen-label="([^"]+)"[\s\S]*?</figure>', text):
    id = match[1].split()[0]
    if id not in IDS: continue
    before = match[0]; figure = before; corrections = []
    for old,new in CORRECTIONS.items():
     if old in figure: figure = figure.replace(old,new); corrections.append({"before":old,"after":new})
    if id == "7m": figure = re.sub(r'placeholder="DK1-[^"]+"', 'placeholder="DK1-…"', figure)
    # The source export ignores full-width. Use the existing Button block prop.
    figure = figure.replace('full-width="{{ true }}"', 'block="{{ true }}"')
    caption_end = figure.index('</figcaption>') + len('</figcaption>')
    frame_start = figure.index('<div', caption_end)
    figure = figure[:frame_start] + figure[frame_start:].replace('<div ', '<div '+ ('data-viewport="narrow" ' if id in MOBILE else '') ,1)
    frame_start = figure.index('<div', caption_end)
    style_start = figure.index('style="',frame_start)+len('style="')
    figure = figure[:style_start]+'--type-caps:normal;'+figure[style_start:]
    if id == "4i":
     figure = figure.replace('>agent-console</span>', '></span>').replace('>rewrite-scheduler</span>', '></span>').replace('>/</span>', '></span>')
     corrections.append({"rule":"Remove private project/session breadcrumbs from the locked backdrop; preserve its geometry."})
    if id in {"5b","5m"}:
     for command, age in [("pnpm test","40s"),("claude","11m")]:
      old = f'font-family:var(--font-mono);font-size:var(--type-micro-size);color:var(--text-tertiary)">{command} · {age}'
      new = f'font-size:var(--type-micro-size);color:var(--text-tertiary)"><span style="font-family:var(--font-mono)">{command}</span> · {age}'
      figure = figure.replace(old,new)
     corrections.append({"rule":"Ages are interface voice; only the command keeps the mono face."})
    if id == "5e":
     figure = re.sub(r'\s*<x-import[^>]+>Open flaky-test-hunt</x-import>', '',figure)
     figure = re.sub(r'\s*<span[^>]+>no reply, 30s</span>', '',figure)
     corrections.append({"rule":"A lock result cannot reopen a private session without a new browser grant; omit unavailable failure timing."})
    if id == "7a":
     old = '— it leaves your recovery key unused.</span>'
     figure = figure.replace(old, old+'<span style="margin-top:-12px;font-size:var(--type-caption-size);line-height:var(--type-caption-lh);color:var(--accent)">Need help getting back in?</span>')
     corrections.append({"rule":"Expose the implemented recovery help route."})
    if id == "7c":
     figure = re.sub(r'Operator <span[^>]*>op-4412</span> can help\.', 'Whoever runs the machine can help.',figure)
     figure = re.sub(r'<span style="display:inline-flex;align-self:flex-start"><x-import[^>]+>Contact the operator</x-import></span>', '<span style="font-size:var(--type-caption-size);line-height:var(--type-caption-lh);color:var(--text-tertiary)">Contact the person who gave you this workspace using your usual contact method.</span>',figure)
     figure = figure.replace('Neither of those opens the storage.</span>', 'Neither of those opens the storage. <span style="color:var(--accent)">No unlock methods left?</span></span>')
     corrections.append({"rule":"No configured contact destination exists; show actionable guidance and the implemented lost-method route."})
    frame = re.search(r'<div[^>]*style="([^"]*(?:width:1120px|width:393px)[^"]*)"', figure)[1]
    dimensions = {key:int(re.search(rf'(?:^|;){key}:(\d+)px',frame)[1]) for key in ("width","height")}
    member = f"Workspace Runtime {id}.dc.html"
    prefix=text[:text.index('</helmet>')]+CONTEXT+'</helmet>\n'
    tail=text[text.rindex('</x-dc>')+len('</x-dc>'):]
    rendered=(prefix+figure+'\n</x-dc>'+tail).encode()
    derivative.writestr(member,rendered)
    figures.append({"id":id,"label":match[1],**dimensions,"member":member,"comp":f"workspace-runtime-{id}","sourceFigureSha256":digest(before.encode()),"derivedFigureSha256":digest(figure.encode()),"memberSha256":digest(rendered),"corrections":corrections,"narrowContext":id in MOBILE})
    members.append({"sourceMember":info.filename,"sourceSha256":digest(original.read(info.filename)),"member":member})
 if len(figures)!=23: raise SystemExit(f"Expected23 runtime figures, found{len(figures)}")
 provenance={"sourceSha256":SOURCE_SHA256,"derivedSha256":digest(target.read_bytes()),"figures":figures,"members":members,"enrollmentProvenance":"../enrollment-reference/provenance.json","context":{"origin":"Each figure is alone at0,0; only outer caption/body padding change.","cssSha256":digest(CONTEXT.encode()),"mobile":"Supplied narrow tokens and existing Button block prop; no new styling.","interfaceCopy":"Sentence case without small caps."},"integrationOnlyFigures":{"4d":"Verify actual LiveWorkspace admission; terminal remains the real implementation.","5a":"Verify actual LiveWorkspace menu/lock/signout consequences; schematic terminal is not a pixel target."}}
 (output/'provenance.json').write_text(json.dumps(provenance,indent=2)+'\n')
 print(target);print(provenance['derivedSha256'])
if __name__=='__main__':main()
