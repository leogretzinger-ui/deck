#!/usr/bin/env python3
"""Install every Deck skill into each harness's user skill folder."""

from __future__ import annotations

import os
import shutil
import sys
from pathlib import Path


SOURCE = Path(os.environ.get("NOISEDECK_SKILLS_PATH", "/opt/noisedeck/skills"))


def harness_skill_roots(home: Path) -> tuple[Path, ...]:
    return (
        home / ".claude" / "skills",
        home / ".codex" / "skills",
        home / ".kimi-code" / "skills",
        home / ".config" / "opencode" / "skills",
        home / ".pi" / "agent" / "skills",
        home / ".omp" / "agent" / "skills",
    )


def install_skill(skill: Path, target: Path) -> None:
    """Replace target/<skill name> with a copy of the skill directory."""
    target.mkdir(parents=True, exist_ok=True)
    destination = target / skill.name
    if destination.is_dir() and not destination.is_symlink():
        shutil.rmtree(destination)
    elif destination.exists() or destination.is_symlink():
        destination.unlink()
    shutil.copytree(skill, destination)


def skills(source: Path) -> list[Path]:
    """Every directory under source that holds a SKILL.md."""
    return sorted(
        path for path in source.iterdir() if path.is_dir() and (path / "SKILL.md").is_file()
    )


def install_all(source: Path, home: Path) -> list[str]:
    found = skills(source)
    for root in harness_skill_roots(home):
        for skill in found:
            install_skill(skill, root)
    return [skill.name for skill in found]


def main() -> None:
    if sys.argv[1:]:
        raise SystemExit("usage: install_skills.py")
    if not SOURCE.is_dir():
        raise SystemExit(f"install_skills.py: {SOURCE} is not a directory")
    names = install_all(SOURCE, Path.home())
    print(f"noisedeck-bootstrap: Deck skills installed: {', '.join(names) or 'none'}")


if __name__ == "__main__":
    main()
