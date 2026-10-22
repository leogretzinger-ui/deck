from __future__ import annotations

import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import install_skills


HERE = Path(__file__).resolve().parent


def write(path: Path, text: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text)


class InstallSkillsTest(unittest.TestCase):
    def setUp(self):
        scratch = tempfile.TemporaryDirectory()
        self.addCleanup(scratch.cleanup)
        self.source = Path(scratch.name) / "skills"
        self.home = Path(scratch.name) / "home"
        write(self.source / "alpha" / "SKILL.md", "alpha v1\n")
        write(self.source / "alpha" / "agents" / "notes.md", "nested\n")
        write(self.source / "beta" / "SKILL.md", "beta\n")
        write(self.source / "not-a-skill" / "README.md", "no skill here\n")
        write(self.source / "install_skills.py", "")

    def run_installer(self) -> subprocess.CompletedProcess:
        env = dict(os.environ, HOME=str(self.home), NOISEDECK_SKILLS_PATH=str(self.source))
        return subprocess.run(
            [sys.executable, "-B", str(HERE / "install_skills.py")],
            env=env, text=True, capture_output=True,
        )

    def test_every_skill_reaches_every_harness(self):
        result = self.run_installer()
        self.assertEqual(result.returncode, 0, result.stderr)
        roots = [
            self.home / ".claude" / "skills",
            self.home / ".codex" / "skills",
            self.home / ".kimi-code" / "skills",
            self.home / ".config" / "opencode" / "skills",
            self.home / ".pi" / "agent" / "skills",
            self.home / ".omp" / "agent" / "skills",
        ]
        self.assertEqual(sorted(install_skills.harness_skill_roots(self.home)), sorted(roots))
        for root in roots:
            self.assertEqual(sorted(p.name for p in root.iterdir()), ["alpha", "beta"], root)
            self.assertEqual((root / "alpha" / "SKILL.md").read_text(), "alpha v1\n")
            self.assertEqual((root / "alpha" / "agents" / "notes.md").read_text(), "nested\n")

    def test_rerun_replaces_deck_skills_and_keeps_others(self):
        claude = self.home / ".claude" / "skills"
        write(claude / "mine" / "SKILL.md", "the person's own skill\n")
        self.assertEqual(self.run_installer().returncode, 0)
        write(self.source / "alpha" / "SKILL.md", "alpha v2\n")
        (self.source / "alpha" / "agents" / "notes.md").unlink()
        result = self.run_installer()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((claude / "alpha" / "SKILL.md").read_text(), "alpha v2\n")
        self.assertFalse((claude / "alpha" / "agents" / "notes.md").exists())
        self.assertEqual((claude / "mine" / "SKILL.md").read_text(), "the person's own skill\n")

    def test_missing_source_fails(self):
        self.source = self.source / "absent"
        result = self.run_installer()
        self.assertNotEqual(result.returncode, 0)

    def test_shipped_skills_install(self):
        names = install_skills.install_all(HERE, self.home)
        self.assertIn("deck-assets", names)
        installed = (self.home / ".codex" / "skills" / "deck-assets" / "SKILL.md").read_text()
        self.assertIn(".deck/assets/", installed)
        self.assertIn("Assets panel", installed)


if __name__ == "__main__":
    unittest.main()
