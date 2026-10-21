#!/usr/bin/env python3
"""Exercise module-metadata.sh against isolated Git repositories."""

import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import unittest


CHECK = Path(__file__).resolve().with_name("module-metadata.sh")
ROOT = CHECK.parents[2]


class ModuleMetadataTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="module-metadata-test-")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name) / "checkout with spaces"
        self.root.mkdir()
        home = Path(temporary.name) / "home"
        home.mkdir()
        templates = Path(temporary.name) / "templates"
        templates.mkdir()
        self.env = {"PATH": os.environ["PATH"], "HOME": str(home),
                    "XDG_CONFIG_HOME": str(home / "config"),
                    "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull,
                    "GIT_TEMPLATE_DIR": str(templates)}
        self.git("init", "-q")
        self.git("config", "user.email", "test@example.invalid")
        self.git("config", "user.name", "Module Metadata Test")
        self.write(".gitignore", "/go.work.sum\n/.scratch/\n/.tools/\n")
        self.write("go.work", "go 1.27.2\n")
        self.write("go.mod", "module fixture.invalid/root\n\ngo 1.27.2\n")
        self.write("go.sum", "fixture.invalid/root v1.0.0 h1:root\n")
        self.write("nested/go.mod", "module fixture.invalid/nested\n\ngo 1.27.2\n")
        self.write("nested/go.sum", "fixture.invalid/nested v1.0.0 h1:nested\n")
        self.write("go.work.sum", "fixture.invalid/work v1.0.0 h1:work\n")
        self.git("add", ".")
        self.git("add", "-f", "go.work.sum")
        self.git("commit", "-qm", "fixture")

    def git(self, *args):
        return subprocess.run(["git", *args], cwd=self.root, env=self.env, check=True,
                              capture_output=True, text=True)

    def write(self, path, content):
        target = self.root / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(content)

    def snapshot(self):
        files = {str(path.relative_to(self.root)): path.read_bytes()
                 for path in self.root.rglob("*")
                 if path.is_file() and path.name in
                 ("go.mod", "go.sum", "go.work", "go.work.sum")}
        return files, self.git("ls-files", "--stage", "-z").stdout

    def run_check(self, succeeds=True):
        before = self.snapshot()
        result = subprocess.run(["sh", str(CHECK)], cwd=self.root, env=self.env,
                                capture_output=True, text=True)
        self.assertEqual(result.returncode == 0, succeeds,
                         f"{result.stdout}{result.stderr}")
        self.assertEqual(self.snapshot(), before)
        return result

    def test_clean_fixture_passes(self):
        self.run_check()

    def test_tracked_root_and_nested_metadata_changes_fail(self):
        for path in ("go.work", "go.mod", "go.sum", "nested/go.mod", "nested/go.sum"):
            self.write(path, (self.root / path).read_text() + "changed\n")
        result = self.run_check(False)
        for path in ("go.work", "go.mod", "go.sum", "nested/go.mod", "nested/go.sum"):
            self.assertIn(path, result.stderr)
        self.assertIn("Unstaged Go module metadata changes", result.stderr)
        self.assertIn("task tidy", result.stderr)
        self.assertIn("task test:module-metadata", result.stderr)

    def test_untracked_metadata_fails_but_unrelated_and_ignored_files_do_not(self):
        self.write(".scratch/dependency/go.mod", "module ignored\n")
        self.write(".tools/dependency/go.sum", "ignored\n")
        self.write("notes.txt", "unrelated\n")
        self.run_check()
        self.write("new module/go.mod", "module fixture.invalid/new\n\ngo 1.27.2\n")
        self.write("new module/go.sum", "fixture.invalid/new v1.0.0 h1:new\n")
        result = self.run_check(False)
        self.assertIn("Untracked Go module metadata", result.stderr)
        self.assertIn("new module/go.mod", result.stderr)
        self.assertIn("new module/go.sum", result.stderr)
        self.assertNotIn(".scratch/dependency/go.mod", result.stderr)
        self.assertNotIn(".tools/dependency/go.sum", result.stderr)
        self.assertNotIn("notes.txt", result.stderr)

    def test_staged_add_delete_edit_and_reversal_fail(self):
        self.write("added/go.mod", "module fixture.invalid/added\n\ngo 1.27.2\n")
        self.git("add", "added/go.mod")
        self.git("rm", "nested/go.sum")
        self.write("go.mod", (self.root / "go.mod").read_text() + "// staged\n")
        self.git("add", "go.mod")
        self.git("show", "HEAD:go.mod")
        (self.root / "go.mod").write_text(self.git("show", "HEAD:go.mod").stdout)
        result = self.run_check(False)
        self.assertIn("Staged Go module metadata changes", result.stderr)
        self.assertIn("added/go.mod", result.stderr)
        self.assertIn("nested/go.sum", result.stderr)
        self.assertIn("go.mod", result.stderr)

    def test_legacy_tracked_root_workspace_sum_is_allowed(self):
        self.write("go.work.sum", "fixture.invalid/work v1.0.0 h1:changed\n")
        self.git("add", "-f", "go.work.sum")
        self.write("go.work.sum", "fixture.invalid/work v1.0.0 h1:worktree\n")
        self.run_check()

    def test_untracked_workspace_sum_and_root_only_ignore_policy(self):
        self.git("rm", "--cached", "go.work.sum")
        self.git("commit", "-qm", "untrack workspace sum")
        self.write("go.work.sum", "regenerated workspace checksums\n")
        self.run_check()
        self.assertEqual(self.git("check-ignore", "go.work.sum").stdout.strip(), "go.work.sum")
        patterns = (ROOT / ".gitignore").read_text().splitlines()
        self.assertIn("/go.work.sum", patterns)
        self.assertNotIn("go.work.sum", patterns)
        for path in ("go.sum", "nested/go.sum"):
            self.assertIn(path, self.git("ls-files", "--", path).stdout)

    def test_whitespace_metadata_path_is_reported(self):
        self.write("nested space/go.mod", "module fixture.invalid/space\n\ngo 1.27.2\n")
        self.git("add", "nested space/go.mod")
        self.git("commit", "-qm", "space module")
        self.write("nested space/go.mod", "module fixture.invalid/space\n\ngo 1.27.2\n// changed\n")
        result = self.run_check(False)
        self.assertIn("nested space/go.mod", result.stderr)

    def check_ci_wiring(self, workflow):
        test = re.search(r"^  test:\n(.*?)(?=^  [\w-]+:)", workflow, re.M | re.S)[1]
        build = re.search(r"^  build:\n(.*?)(?=^  [\w-]+:)", workflow, re.M | re.S)[1]
        guard = "        run: task test:module-metadata\n"
        infrastructure = "        run: task test:workspace-infrastructure\n"
        suite = "        run: task --concurrency 3 test:race\n"
        self.assertIn(guard, test)
        self.assertIn(infrastructure, test)
        self.assertIn(suite, test)
        self.assertLess(test.index(infrastructure), test.index(suite))
        self.assertLess(test.index(suite), test.index(guard))
        self.assertIn("          task tidy\n          task test:module-metadata\n", build)
        condition = ("        if: needs.changes.outputs.docs_only != 'true' "
                     "&& needs.changes.outputs.go_relevant == 'true'\n")
        for command in (guard, infrastructure):
            self.assertIn(condition + command, test)
        self.assertNotRegex(workflow, r"\btask[^\n]*\btest:workspace(?::race)?(?:\s|$)")

    def test_ci_wiring_and_negative_controls(self):
        workflow = (ROOT / ".github/workflows/ci.yml").read_text()
        self.check_ci_wiring(workflow)
        guard = "        run: task test:module-metadata\n"
        for broken in (
            workflow.replace(guard, '        run: echo "task test:module-metadata"\n'),
            workflow.replace(guard, ""),
            workflow.replace("          task tidy\n          task test:module-metadata\n",
                             "          task test:module-metadata\n          task tidy\n"),
            workflow.replace("task --concurrency 3 test:race\n", "task --concurrency 3 test:workspace:race\n"),
        ):
            with self.subTest(broken=broken != workflow):
                with self.assertRaises(AssertionError):
                    self.check_ci_wiring(broken)

    def test_real_metadata_task_invokes_guard(self):
        taskfile = (ROOT / "Taskfile.yml").read_text()
        self.write("Taskfile.yml", taskfile)
        for include in re.findall(r"^\s+taskfile: (.+)$", taskfile, re.M):
            self.write(include, "version: '3'\ntasks: {}\n")
        self.write(".github/scripts/module-metadata.sh", CHECK.read_text())
        task = shutil.which("task")
        self.assertIsNotNone(task, "task must be on PATH")
        for dirty in (False, True):
            if dirty:
                self.write("go.sum", "changed checksums\n")
            before = self.snapshot()
            result = subprocess.run([task, "test:module-metadata"], cwd=self.root,
                                    env=self.env, capture_output=True, text=True)
            self.assertEqual(result.returncode == 0, not dirty, result.stdout + result.stderr)
            self.assertEqual(self.snapshot(), before)
            if dirty:
                self.assertIn("Unstaged Go module metadata", result.stderr)


if __name__ == "__main__":
    unittest.main()
