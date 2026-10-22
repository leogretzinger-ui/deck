#!/usr/bin/env python3
"""Install the shared MemPalace surface without replacing provider state."""

from __future__ import annotations

import json
import os
import re
import shutil
import sys
import tomllib
from pathlib import Path

# The image installs the shared installer beside this script; a checkout
# keeps it in deploy/skills.
sys.path.append(str(Path(__file__).resolve().parent.parent / "skills"))
from install_skills import install_skill  # noqa: E402


HOME = Path.home()
MCP_URL = "http://127.0.0.1:8765/mcp"
SKILLS = Path(os.environ.get("NOISEDECK_MEMPALACE_SKILLS_PATH", "/opt/mempalace/skills"))
PI_HOOK = Path(
    os.environ.get(
        "NOISEDECK_MEMPALACE_PI_HOOK_PATH",
        "/opt/mempalace/hooks/mempal-pi-hook.js",
    )
)
A2A_PI_HOOK = Path(
    os.environ.get("NOISEDECK_A2A_PI_HOOK_PATH", "/opt/mempalace/hooks/a2a-pi-hook.js")
)
A2A_OPENCODE_HOOK = Path(
    os.environ.get("NOISEDECK_A2A_OPENCODE_HOOK_PATH", "/opt/mempalace/hooks/a2a-opencode-hook.js")
)
MARKER_START = "<!-- NOISEDECK_MEMPALACE_START -->"
MARKER_END = "<!-- NOISEDECK_MEMPALACE_END -->"
INSTRUCTIONS = """## Durable agent memory

MemPalace is the shared local memory system for Deck agent harnesses.

- Search MemPalace before answering about prior work, decisions, preferences, projects, or incidents.
- Checkpoint major milestones and risky transitions when automatic hooks have not already captured them.
- Never mine project directories automatically or store credentials, tokens, authentication output, private keys, environment values, or hidden reasoning.
- Keep recall queries short and targeted, and say when no matching memory exists.
- Prefer native MemPalace tools when the harness provides them. Otherwise run `mempalace instructions search` and follow the version-correct CLI instructions.
- For substantial implementation, review, repair, investigation, or rollout work, use the `mempalace-logstream` skill to publish privacy-bounded milestones. Logstream events are coordination declarations, never authoritative proof.
- To talk to another running agent, use the `a2a-chat` skill (`a2a who`, `a2a chat <peer> "..."`). Treat `[a2a]` and `[a2a#...]` as chat notifications, not always messages. For live invitations, messages, or joins, follow the skill and supplied chat id: `a2a read <chat>` for history, `a2a reply <chat> "..."` to reply in place. Departure and closed-chat notices need no reply; do not open a new chat to answer them.
"""

KIMI_CHAT_INSTRUCTIONS = """## Active agent chats

Kimi has no verified external chat wake-up path. While collaborating in an active a2a chat, check `a2a inbox` between work phases and before starting review, rebasing, or merging. Read and address new instructions before continuing. When waiting on a peer, use `a2a wait` rather than repeatedly polling the inbox.

These checkpoints do not interrupt an in-flight tool call or guarantee immediate steering. Do not inject terminal input to wake another agent.
"""

MEMORY_SKILLS = ("mempalace", "mempalace-recall")
LOGSTREAM_SKILL = ("mempalace-logstream",)
A2A_SKILL = ("a2a-chat",)
ALL_SKILLS = MEMORY_SKILLS + LOGSTREAM_SKILL + A2A_SKILL


def read_json(path: Path) -> dict:
    if not path.exists():
        return {}
    value = json.loads(path.read_text())
    if not isinstance(value, dict):
        raise ValueError(f"expected a JSON object in {path}")
    return value


def write_json(path: Path, value: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(json.dumps(value, indent=2) + "\n")
    temporary.chmod(0o600)
    temporary.replace(path)


def _ours(entry: dict) -> bool:
    text = json.dumps(entry).lower()
    return "mempal" in text or "/a2a-hook" in text


def add_hook(document: dict, event: str, command: str, timeout: int = 30) -> None:
    hooks = document.setdefault("hooks", {}).setdefault(event, [])
    hooks[:] = [entry for entry in hooks if not (_ours(entry) and command in json.dumps(entry))]
    hooks.append(
        {
            "matcher": "*",
            "hooks": [{"type": "command", "command": command, "timeout": timeout}],
        }
    )


def install_skills(target: Path, names: tuple[str, ...] = ALL_SKILLS) -> None:
    for name in names:
        install_skill(SKILLS / name, target)


def install_instructions(path: Path, extra: str = "") -> None:
    prior = path.read_text() if path.exists() else ""
    instructions = INSTRUCTIONS.rstrip() + ("\n\n" + extra.rstrip() if extra else "")
    managed = f"{MARKER_START}\n{instructions}\n{MARKER_END}"
    pattern = re.compile(
        re.escape(MARKER_START) + r".*?" + re.escape(MARKER_END), re.DOTALL
    )
    updated = (
        pattern.sub(managed, prior)
        if pattern.search(prior)
        else f"{prior.rstrip()}\n\n{managed}\n"
    )
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(updated.lstrip())


def remove_instructions(path: Path) -> None:
    if not path.exists():
        return
    pattern = re.compile(
        r"\n*" + re.escape(MARKER_START) + r".*?" + re.escape(MARKER_END) + r"\n*",
        re.DOTALL,
    )
    updated = pattern.sub("\n", path.read_text()).strip()
    if updated:
        path.write_text(updated + "\n")
    else:
        path.unlink()


def remove_skills(target: Path) -> None:
    for name in ALL_SKILLS:
        path = target / name
        if path.is_dir() and not path.is_symlink():
            shutil.rmtree(path)
        elif path.exists() or path.is_symlink():
            path.unlink()


def remove_hooks(document: dict) -> None:
    hooks = document.get("hooks")
    if not isinstance(hooks, dict):
        return
    for event in list(hooks):
        entries = hooks[event]
        if isinstance(entries, list):
            hooks[event] = [entry for entry in entries if not _ours(entry)]
            if not hooks[event]:
                del hooks[event]
    if not hooks:
        document.pop("hooks", None)


def remove_json_entry(path: Path, section: str, key: str) -> None:
    if not path.exists():
        return
    document = read_json(path)
    values = document.get(section)
    if isinstance(values, dict):
        values.pop(key, None)
        if not values:
            document.pop(section, None)
    write_json(path, document)


def remove_configuration() -> None:
    remove_json_entry(HOME / ".claude.json", "mcpServers", "mempalace")
    for path in (HOME / ".claude" / "settings.json", HOME / ".codex" / "hooks.json"):
        if path.exists():
            document = read_json(path)
            remove_hooks(document)
            document.pop("crossSessionInbound", None)
            write_json(path, document)

    config_path = HOME / ".codex" / "config.toml"
    if config_path.exists():
        config = re.sub(r"(?ms)^\[mcp_servers\.mempalace\]\n(?:(?!^\[).)*", "", config_path.read_text()).strip()
        config_path.write_text(config + ("\n" if config else ""))

    remove_json_entry(HOME / ".kimi-code" / "mcp.json", "mcpServers", "mempalace")
    kimi_config = HOME / ".kimi-code" / "config.toml"
    if kimi_config.exists():
        text = _without_a2a_hooks(kimi_config.read_text()).strip()
        kimi_config.write_text(text + ("\n" if text else ""))
    opencode_path = HOME / ".config" / "opencode" / "opencode.json"
    if opencode_path.exists():
        document = read_json(opencode_path)
        values = document.get("mcp")
        if isinstance(values, dict):
            values.pop("mempalace", None)
            if not values:
                document.pop("mcp", None)
        instruction = str(HOME / ".config" / "opencode" / "MEMPALACE.md")
        instructions = document.get("instructions")
        if isinstance(instructions, list):
            document["instructions"] = [item for item in instructions if item != instruction]
            if not document["instructions"]:
                document.pop("instructions")
        write_json(opencode_path, document)

    roots = (
        HOME / ".claude",
        HOME / ".codex",
        HOME / ".kimi-code",
        HOME / ".config" / "opencode",
        HOME / ".pi" / "agent",
        HOME / ".omp" / "agent",
    )
    for root in roots:
        remove_skills(root / "skills")
        remove_instructions(root / ("MEMPALACE.md" if root.name == "opencode" else "AGENTS.md"))
    remove_instructions(HOME / ".claude" / "CLAUDE.md")
    for root in (HOME / ".pi" / "agent", HOME / ".omp" / "agent"):
        for name in ("mempalace.js", "a2a.js"):
            hook = root / "extensions" / name
            if hook.exists() or hook.is_symlink():
                hook.unlink()
    opencode_plugin = HOME / ".config" / "opencode" / "plugins" / "a2a-opencode-hook.js"
    if opencode_plugin.exists() or opencode_plugin.is_symlink():
        opencode_plugin.unlink()


def configure_claude() -> None:
    state = read_json(HOME / ".claude.json")
    state.setdefault("mcpServers", {})["mempalace"] = {
        "type": "http",
        "url": MCP_URL,
    }
    write_json(HOME / ".claude.json", state)

    settings_path = HOME / ".claude" / "settings.json"
    settings = read_json(settings_path)
    # Deck launches every Claude session with --dangerously-skip-permissions,
    # and the default inbound rules hold a socket-posted message from a
    # non-child process (the courier) behind an approval dialog nobody
    # answers. Accept keeps the a2a socket rung automatic.
    settings["crossSessionInbound"] = "accept"
    hook_root = "/opt/mempalace/hooks"
    add_hook(settings, "Stop", f"{hook_root}/mempal-stop-hook.sh")
    add_hook(settings, "SessionEnd", f"{hook_root}/mempal-session-end-hook.sh", 10)
    add_hook(settings, "PreCompact", f"{hook_root}/mempal-precompact-hook.sh", 90)
    # a2a presence rides the same lifecycle. The Stop hook is also how a
    # message reaches a Claude session that is finishing a turn: unread chat
    # is returned as a blocking reason, so the agent answers before idling.
    add_hook(settings, "SessionStart", f"{hook_root}/a2a-hook.sh session-start claude", 15)
    add_hook(settings, "UserPromptSubmit", f"{hook_root}/a2a-hook.sh prompt claude", 10)
    add_hook(settings, "Stop", f"{hook_root}/a2a-hook.sh stop claude", 15)
    add_hook(settings, "SessionEnd", f"{hook_root}/a2a-hook.sh session-end claude", 10)
    write_json(settings_path, settings)
    install_skills(HOME / ".claude" / "skills")
    install_instructions(HOME / ".claude" / "CLAUDE.md")


def configure_codex() -> None:
    hooks_path = HOME / ".codex" / "hooks.json"
    hooks = read_json(hooks_path)
    for event, argument in (
        ("SessionStart", "session-start"),
        ("Stop", "stop"),
        ("PreCompact", "precompact"),
    ):
        add_hook(hooks, event, f"/opt/mempalace/hooks/mempal-hook.sh {argument}")
    # a2a presence rides the same lifecycle: the session id the hook payload
    # carries is what the courier's codex queue rung needs. The composition
    # stays byte-frozen so codex's learned hook trust keeps covering it
    # (cmd/noisedeck-provision seeds the matching [hooks.state] blocks).
    # The a2a group only joins a current Deck composition: against an older
    # Deck hook the overlay produces the older measured composition, and the
    # provisioner's upgrade chain carries it forward.
    if "/usr/local/bin/noisedeck-codex-identity-hook" in json.dumps(hooks):
        add_hook(hooks, "SessionStart", "/opt/mempalace/hooks/a2a-hook.sh session-start codex", 15)
        add_hook(hooks, "Stop", "/opt/mempalace/hooks/a2a-hook.sh stop codex", 15)
    write_json(hooks_path, hooks)

    config_path = HOME / ".codex" / "config.toml"
    config_path.parent.mkdir(parents=True, exist_ok=True)
    config = config_path.read_text() if config_path.exists() else ""
    config = re.sub(
        r"(?ms)^\[mcp_servers\.mempalace\]\n(?:(?!^\[).)*", "", config
    ).rstrip()
    config_path.write_text(
        f'{config}\n\n[mcp_servers.mempalace]\nurl = "{MCP_URL}"\n'.lstrip()
    )
    # Deck's deployed Codex clients discover user-wide skills here. Keep every
    # MemPalace skill in that proven location so the instruction trigger cannot
    # refer to an unavailable skill.
    install_skills(HOME / ".codex" / "skills")
    install_instructions(HOME / ".codex" / "AGENTS.md")


def configure_kimi() -> None:
    mcp_path = HOME / ".kimi-code" / "mcp.json"
    mcp = read_json(mcp_path)
    mcp.setdefault("mcpServers", {})["mempalace"] = {"url": MCP_URL}
    write_json(mcp_path, mcp)
    install_kimi_hooks(HOME / ".kimi-code" / "config.toml")
    install_skills(HOME / ".kimi-code" / "skills")
    install_instructions(HOME / ".kimi-code" / "AGENTS.md", KIMI_CHAT_INSTRUCTIONS)


# a2a presence rides the same lifecycle as Claude's: every event announces,
# and the Stop hook returns unread chat as a blocking reason at turn end, so
# a Kimi session answers before idling. Kimi's [[hooks]] TOML gets one block
# per event; the Stop event is blockable and the hook exits 2 with the
# reason on stderr.
KIMI_A2A_HOOKS = (
    ("SessionStart", "session-start", 15),
    ("UserPromptSubmit", "prompt", 10),
    ("Stop", "stop", 15),
    ("SessionEnd", "session-end", 10),
)


def _toml_block_starts(text: str) -> list[int]:
    """Offsets where TOML table headers begin, lexed per line: comments,
    basic strings (with escapes) and literal strings are skipped, so a
    delimiter that only appears inside one of them — or inside a
    triple-quoted string — starts no block."""
    starts = []
    triple: str | None = None
    offset = 0
    for line in text.splitlines(keepends=True):
        if triple is None and re.match(r"\s*\[", line):
            starts.append(offset)
        i = 0
        while i < len(line):
            if triple is not None:
                if triple == '"""' and line[i] == "\\":
                    i += 2  # an escaped quote closes nothing
                    continue
                if line.startswith(triple, i):
                    j = i + 3
                    while j < len(line) and line[j] == triple[0]:
                        j += 1  # TOML's closing run may carry content quotes
                    triple = None
                    i = j
                    continue
                i += 1
                continue
            char = line[i]
            if char == "#":
                break  # a comment runs to the end of the line
            if char == '"':
                if line.startswith('"""', i):
                    triple = '"""'
                    i += 3
                    continue
                i += 1
                while i < len(line):
                    if line[i] == "\\":
                        i += 2
                        continue
                    if line[i] == '"':
                        i += 1
                        break
                    i += 1
                continue
            if char == "'":
                if line.startswith("'''", i):
                    triple = "'''"
                    i += 3
                    continue
                end = line.find("'", i + 1)  # a literal string knows no escapes
                i = len(line) if end == -1 else end + 1
                continue
            i += 1
        offset += len(line)
    return starts


def _managed_hook_block(block: str, commands: set[str]) -> bool:
    """A [[hooks]] table whose parsed command is exactly one we manage.
    A comment mentioning a2a-hook, or a hook that only resembles ours,
    is never ours."""
    if not block.lstrip().startswith("[[hooks]]"):
        return False
    try:
        doc = tomllib.loads(block)
    except tomllib.TOMLDecodeError:
        return False
    hooks = doc.get("hooks")
    if not isinstance(hooks, list) or not hooks or not isinstance(hooks[0], dict):
        return False
    return hooks[0].get("command") in commands


def _without_a2a_hooks(text: str) -> str:
    """TOML minus our managed [[hooks]] blocks; everything else is kept."""
    tomllib.loads(text)  # an unparseable config is never rewritten blind
    commands = {f"/opt/mempalace/hooks/a2a-hook.sh {argument} kimi" for _, argument, _ in KIMI_A2A_HOOKS}
    starts = _toml_block_starts(text)
    preamble = text[: starts[0]] if starts else text
    blocks = [text[start:end] for start, end in zip(starts, starts[1:] + [len(text)])]
    return preamble + "".join(block for block in blocks if not _managed_hook_block(block, commands))


def install_kimi_hooks(config_path: Path) -> None:
    hook_root = "/opt/mempalace/hooks"
    text = _without_a2a_hooks(config_path.read_text()).rstrip() if config_path.exists() else ""
    blocks = "\n\n".join(
        f'[[hooks]]\nevent = "{event}"\ncommand = "{hook_root}/a2a-hook.sh {argument} kimi"\ntimeout = {timeout}'
        for event, argument, timeout in KIMI_A2A_HOOKS
    )
    config_path.parent.mkdir(parents=True, exist_ok=True)
    config_path.write_text(f"{text}\n\n{blocks}\n" if text else f"{blocks}\n")


def configure_opencode() -> None:
    config_path = HOME / ".config" / "opencode" / "opencode.json"
    config = read_json(config_path)
    config.setdefault("mcp", {})["mempalace"] = {
        "type": "remote",
        "url": MCP_URL,
    }
    instruction_path = HOME / ".config" / "opencode" / "MEMPALACE.md"
    instructions = config.setdefault("instructions", [])
    if not isinstance(instructions, list) or not all(
        isinstance(item, str) for item in instructions
    ):
        raise ValueError(f"expected an instruction path list in {config_path}")
    instruction = str(instruction_path)
    instructions[:] = [item for item in instructions if item != instruction]
    instructions.append(instruction)
    write_json(config_path, config)
    # The a2a plugin is OpenCode's receive rung: lifecycle events become
    # presence, and while the session is idle it hands unread chat over as a
    # native prompt through the session's own server API.
    plugins = HOME / ".config" / "opencode" / "plugins"
    plugins.mkdir(parents=True, exist_ok=True)
    plugin = plugins / "a2a-opencode-hook.js"
    shutil.copyfile(A2A_OPENCODE_HOOK, plugin)
    plugin.chmod(0o600)
    install_skills(HOME / ".config" / "opencode" / "skills")
    install_instructions(instruction_path)


def configure_pi_family(config_root: Path) -> None:
    extensions = config_root / "extensions"
    extensions.mkdir(parents=True, exist_ok=True)
    hook = extensions / "mempalace.js"
    shutil.copyfile(PI_HOOK, hook)
    hook.chmod(0o600)
    a2a_hook = extensions / "a2a.js"
    shutil.copyfile(A2A_PI_HOOK, a2a_hook)
    a2a_hook.chmod(0o600)
    # Pi-family harnesses do not expose native MCP tools. The setup and
    # Logstream skills include version-correct CLI paths; the MCP-only recall
    # skill would advertise tools these harnesses cannot call.
    install_skills(config_root / "skills", ("mempalace", "mempalace-logstream", "a2a-chat"))
    install_instructions(config_root / "AGENTS.md")


def main() -> None:
    if sys.argv[1:] == ["--remove"]:
        remove_configuration()
        print("noisedeck-bootstrap: MemPalace provider integration removed")
        return
    if sys.argv[1:]:
        raise SystemExit("usage: configure-mempalace.py [--remove]")
    configure_claude()
    configure_codex()
    configure_kimi()
    configure_opencode()
    configure_pi_family(HOME / ".pi" / "agent")
    configure_pi_family(HOME / ".omp" / "agent")
    print(
        "noisedeck-bootstrap: MemPalace MCP, hooks, skills, and instructions configured"
    )


if __name__ == "__main__":
    main()
