#!/usr/bin/env python3
"""Exercise instruction-files.py against isolated fixture repositories."""

from __future__ import annotations

import os
import shutil
from pathlib import Path
import subprocess
import sys
import tempfile

CHECK = Path(__file__).resolve().with_name("instruction-files.py")
failures = 0


def fixture(files: dict[str, str], symlinks: dict[str, str] | None = None) -> Path:
    root = Path(tempfile.mkdtemp(prefix="instruction-files-test-"))
    subprocess.run(["git", "init", "-q"], cwd=root, check=True)
    for rel, body in files.items():
        path = root / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(body)
    for rel, target in (symlinks or {}).items():
        os.symlink(target, root / rel)
    (root / "tasks.txt").write_text("build\ndocs\nsite:build\n")
    return root


def good_files() -> tuple[dict[str, str], dict[str, str]]:
    files = {
        "AGENTS.md": "# Root\n\nRun `task build` and:\n\n```sh\ntask docs\n```\n\nThe task list is long.\n",
        "website/AGENTS.md": "# Site\n\nUse `task site:build`.\n",
        ".claude/rules/tests.md": "---\npaths:\n  - \"**/*_test.go\"\n---\n# Tests\n\nKeep tests offline.\n",
        ".claude/agents/reviewer.md": "---\nname: reviewer\ndescription: >-\n  Reviews code.\n  Read-only.\ntools: [Read]\n---\nBody\n",
        ".claude/skills/release/SKILL.md": "---\nname: release\ndescription: Cut a release.\n---\nBody\n",
    }
    links = {"CLAUDE.md": "AGENTS.md", "website/CLAUDE.md": "AGENTS.md"}
    return files, links


def run(name: str, root: Path, want_ok: bool, want_text: str = "") -> None:
    global failures
    proc = subprocess.run(
        [sys.executable, str(CHECK), "--root", str(root), "--tasks-file", str(root / "tasks.txt")],
        capture_output=True, text=True,
    )
    ok = proc.returncode == 0
    if ok != want_ok or (want_text and want_text not in proc.stderr):
        failures += 1
        print(f"FAIL: {name}: exit={proc.returncode}\n{proc.stdout}{proc.stderr}")
    else:
        print(f"ok: {name}")


def case(name: str, want_ok: bool, want_text: str = "", edit=None) -> None:
    files, links = good_files()
    if edit:
        edit(files, links)
    root = fixture(files, links)
    try:
        run(name, root, want_ok, want_text)
    finally:
        shutil.rmtree(root)


case("clean fixture passes", True)
case("199-line AGENTS.md passes", True,
     edit=lambda f, l: f.__setitem__("website/AGENTS.md", "x\n" * 199))
case("200-line AGENTS.md fails", False, "website/AGENTS.md: 200 lines",
     edit=lambda f, l: f.__setitem__("website/AGENTS.md", "x\n" * 200))
case("missing CLAUDE.md symlink fails", False, "website/CLAUDE.md: must be a symlink",
     edit=lambda f, l: l.pop("website/CLAUDE.md"))
case("CLAUDE.md copy instead of symlink fails", False, "website/CLAUDE.md: must be a symlink",
     edit=lambda f, l: (l.pop("website/CLAUDE.md"), f.__setitem__("website/CLAUDE.md", "copy\n")))
case("unknown task in code fails", False, "`task nope`",
     edit=lambda f, l: f.__setitem__("website/AGENTS.md", "Run `task nope`.\n"))
case("task word in prose is ignored", True,
     edit=lambda f, l: f.__setitem__("website/AGENTS.md", "Finish the task quickly.\n"))
case("chain over 32 KiB fails", False, "Codex stops at",
     edit=lambda f, l: (f.__setitem__("AGENTS.md", ("y" * 200 + "\n") * 90),
                        f.__setitem__("website/AGENTS.md", ("z" * 200 + "\n") * 90)))
case("26-line rule fails", False, ".claude/rules/tests.md: 26 lines",
     edit=lambda f, l: f.__setitem__(".claude/rules/tests.md", "r\n" * 26))
case("long folded agent description fails", False, "reviewer.md: description is",
     edit=lambda f, l: f.__setitem__(".claude/agents/reviewer.md",
                                     "---\nname: reviewer\ndescription: >-\n" + ("  word word word\n" * 80) + "---\n"))
case("long inline skill description fails", False, "SKILL.md: description is",
     edit=lambda f, l: f.__setitem__(".claude/skills/release/SKILL.md",
                                     "---\nname: release\ndescription: " + "a" * 1025 + "\n---\n"))
case("missing description fails", False, "no description",
     edit=lambda f, l: f.__setitem__(".claude/skills/release/SKILL.md", "---\nname: release\n---\n"))

if failures:
    print(f"instruction-files tests: {failures} failure(s)")
    sys.exit(1)
print("instruction-files tests: all checks passed")
