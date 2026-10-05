#!/usr/bin/env python3
"""Check coding-agent instruction files stay small, linked, and current.

Limits and their basis:
  - AGENTS.md: under 200 lines per file (Anthropic's CLAUDE.md size target).
  - AGENTS.md chain from the repo root to any directory: at most 32 KiB
    (Codex's default project_doc_max_bytes).
  - .claude/rules/*.md: at most 25 lines, because several can load at once.
  - Agent and skill descriptions: at most 1024 characters (Agent Skills spec);
    every session loads them.
Every AGENTS.md needs a sibling CLAUDE.md symlink to it, and every
`task <name>` an AGENTS.md mentions must exist.

Usage: instruction-files.py [--root DIR] [--tasks-file FILE]
--tasks-file lists one valid task name per line instead of asking `task`.
"""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import sys

AGENTS_MAX_LINES = 199
CHAIN_MAX_BYTES = 32 * 1024
RULE_MAX_LINES = 25
DESCRIPTION_MAX_CHARS = 1024

TASK_REF = re.compile(r"(?:^|\s)task ([a-z0-9][a-z0-9:_.-]*[a-z0-9])")
FENCE = re.compile(r"^```[^\n]*\n(.*?)^```", re.S | re.M)
CODE_SPAN = re.compile(r"`([^`\n]+)`")


def code_text(markdown: str) -> str:
    """Return only fenced blocks and inline code spans: where commands live."""
    blocks = FENCE.findall(markdown)
    prose = FENCE.sub("", markdown)
    return "\n".join(blocks + CODE_SPAN.findall(prose))


def tracked_files(root: Path) -> list[Path]:
    out = subprocess.run(
        ["git", "ls-files", "--cached", "--others", "--exclude-standard", "-z"],
        cwd=root, check=True, capture_output=True,
    ).stdout.decode()
    return [Path(p) for p in out.split("\0") if p]


def line_count(path: Path) -> int:
    with path.open(encoding="utf-8") as f:
        return sum(1 for _ in f)


def frontmatter_description(path: Path) -> str | None:
    lines = path.read_text(encoding="utf-8").splitlines()
    if not lines or lines[0] != "---":
        return None
    try:
        end = lines.index("---", 1)
    except ValueError:
        return None
    body = lines[1:end]
    for i, line in enumerate(body):
        if not line.startswith("description:"):
            continue
        value = line[len("description:"):].strip()
        if value and value[0] not in ">|":
            return value.strip("'\"")
        block = []
        for cont in body[i + 1:]:
            if cont and not cont[0].isspace():
                break
            block.append(cont.strip())
        return " ".join(part for part in block if part)
    return None


def task_names(root: Path, tasks_file: Path | None) -> set[str]:
    if tasks_file:
        return {l.strip() for l in tasks_file.read_text().splitlines() if l.strip()}
    out = subprocess.run(
        ["task", "--list-all", "--json"], cwd=root, check=True, capture_output=True,
    ).stdout
    names = set()
    for task in json.loads(out)["tasks"]:
        names.add(task["name"])
        names.update(task.get("aliases") or [])
    return names


def check(root: Path, tasks_file: Path | None) -> list[str]:
    errors = []
    files = tracked_files(root)
    agents = sorted(p for p in files if p.name == "AGENTS.md")
    agent_dirs = {p.parent for p in agents}

    for rel in agents:
        path = root / rel
        lines = line_count(path)
        if lines > AGENTS_MAX_LINES:
            errors.append(f"{rel}: {lines} lines; keep it under 200")
        sibling = path.with_name("CLAUDE.md")
        if not sibling.is_symlink() or os.readlink(sibling) != "AGENTS.md":
            errors.append(f"{rel.with_name('CLAUDE.md')}: must be a symlink to AGENTS.md")
        chain = [d for d in agent_dirs if d == Path(".") or d in rel.parent.parents or d == rel.parent]
        size = sum((root / d / "AGENTS.md").stat().st_size for d in chain)
        if size > CHAIN_MAX_BYTES:
            errors.append(f"{rel}: AGENTS.md files from the root down total {size} bytes; Codex stops at {CHAIN_MAX_BYTES}")

    known = None
    for rel in agents:
        text = code_text((root / rel).read_text(encoding="utf-8"))
        for name in sorted(set(TASK_REF.findall(text))):
            if known is None:
                known = task_names(root, tasks_file)
            if name not in known:
                errors.append(f"{rel}: mentions `task {name}`, which does not exist")

    for rel in sorted(files):
        parts = rel.parts
        if parts[:2] == (".claude", "rules") and rel.suffix == ".md":
            lines = line_count(root / rel)
            if lines > RULE_MAX_LINES:
                errors.append(f"{rel}: {lines} lines; rules stay at or under {RULE_MAX_LINES}")
        is_agent = parts[:2] == (".claude", "agents") and rel.suffix == ".md"
        is_skill = parts[:2] == (".claude", "skills") and rel.name == "SKILL.md"
        if is_agent or is_skill:
            desc = frontmatter_description(root / rel)
            if desc is None:
                errors.append(f"{rel}: no description in frontmatter")
            elif len(desc) > DESCRIPTION_MAX_CHARS:
                errors.append(f"{rel}: description is {len(desc)} characters; keep it at or under {DESCRIPTION_MAX_CHARS}")
    return errors


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", type=Path, default=Path("."))
    parser.add_argument("--tasks-file", type=Path)
    args = parser.parse_args()
    errors = check(args.root.resolve(), args.tasks_file)
    for error in errors:
        print(f"instruction-files: {error}", file=sys.stderr)
    if errors:
        return 1
    print("instruction-files: ok")
    return 0


if __name__ == "__main__":
    sys.exit(main())
