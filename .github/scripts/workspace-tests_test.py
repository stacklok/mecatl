#!/usr/bin/env python3
"""Offline runner contracts: python3 .github/scripts/workspace-tests_test.py.

Requires task and a Go 1.27 or newer binary on PATH (toolchain downloads are disabled).
"""

import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import unittest
import zipfile


ROOT = Path(__file__).resolve().parents[2]
TASK = shutil.which("task")
GO = shutil.which("go")


class WorkspaceTests(unittest.TestCase):
    def setUp(self):
        self.assertIsNotNone(TASK, "task must be on PATH")
        self.assertIsNotNone(GO, "go must be on PATH")
        temporary = tempfile.TemporaryDirectory(prefix="workspace-tests-")
        self.addCleanup(temporary.cleanup)
        self.base = Path(temporary.name)
        self.root = self.base / "checkout 'with $spaces"
        self.root.mkdir()
        self.env = {
            "PATH": os.environ["PATH"],
            "HOME": str(self.base / "home"),
            "GOPATH": str(self.base / "gopath"),
            "GOCACHE": str(self.base / "cache"),
            "TMPDIR": str(self.base / "tmp"),
            "GOENV": "off", "GOTOOLCHAIN": "local",
            "GOPROXY": "off", "GOSUMDB": "off", "GOWORK": "off",
        }
        for key in ("HOME", "TMPDIR"):
            Path(self.env[key]).mkdir()
        taskfile = (ROOT / "Taskfile.yml").read_text()
        (self.root / "Taskfile.yml").write_text(taskfile)
        for include in re.findall(r"^\s+taskfile: (.+)$", taskfile, re.MULTILINE):
            path = self.root / include
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("version: '3'\ntasks: {}\n")
        shutil.copyfile(ROOT / "go.work", self.root / "go.work")
        parsed = self.run_command([GO, "work", "edit", "-json", "go.work"]).stdout
        self.modules = [Path(entry["DiskPath"]).as_posix()
                        for entry in json.loads(parsed)["Use"]]
        self.assertEqual(len(self.modules), len(set(self.modules)))
        for module in self.modules:
            (self.root / module).mkdir(parents=True, exist_ok=True)
        other = self.base / "unrelated"
        other.mkdir()
        (other / "go.mod").write_text("module fixture.invalid/unrelated\n\ngo 1.27\n")
        self.unrelated = self.base / "unrelated.work"
        self.unrelated.write_text("go 1.27\n\nuse ./unrelated\n")

    def run_command(self, args, *, succeeds=True, env=None, cwd=None):
        result = subprocess.run(args, cwd=cwd or self.root, env=env or self.env,
                                capture_output=True, text=True, timeout=120)
        self.assertEqual(result.returncode == 0, succeeds,
                         f"{args}\n{result.stdout}\n{result.stderr}")
        return result

    def fake_go(self):
        bin_dir = self.base / "bin"
        bin_dir.mkdir()
        fake = bin_dir / "go"
        fake.write_text('''#!/usr/bin/env python3
import json, os, sys
from pathlib import Path
module = Path.cwd().relative_to(os.environ["FIXTURE_ROOT"]).as_posix()
tagged = "-tags=microvm_dev" in sys.argv
with open(os.environ["CALLS"], "a") as log:
    log.write(json.dumps({"module": module, "args": sys.argv[1:],
                          "gowork": os.environ.get("GOWORK")}) + "\\n")
sys.exit(1 if os.environ.get("FAIL_LEG") == ("tag" if tagged else module) else 0)
''')
        fake.chmod(0o755)
        self.env.update(PATH=f"{bin_dir}:{self.env['PATH']}",
                        CALLS=str(self.base / "calls.jsonl"), FIXTURE_ROOT=str(self.root))

    def dispatch(self, task, ambient, *overrides, succeeds=True, fail="", cwd=None):
        Path(self.env["CALLS"]).write_text("")
        self.run_command([TASK, task, *overrides], succeeds=succeeds, cwd=cwd,
                         env=dict(self.env, GOWORK=ambient, FAIL_LEG=fail))
        return [json.loads(line) for line in Path(self.env["CALLS"]).read_text().splitlines()]

    def assert_dispatch(self, calls, workspace, race, ambient):
        modules = [call for call in calls if "-tags=microvm_dev" not in call["args"]]
        tags = [call for call in calls if "-tags=microvm_dev" in call["args"]]
        self.assertCountEqual([call["module"] for call in modules], self.modules)
        self.assertEqual(len(tags), 1)
        for call in modules:
            self.assertEqual(call["gowork"], str(self.root / "go.work") if workspace else "off")
            self.assertEqual(call["args"], ["test", "-mod=readonly", "-timeout=20m"]
                             + (["-race"] if race else []) + ["./..."])
        self.assertEqual(tags[0]["module"], ".")
        self.assertEqual(tags[0]["gowork"], str(self.root / "go.work") if workspace else ambient)
        self.assertEqual(tags[0]["args"], ["test", "-mod=readonly"]
                         + (["-timeout=20m"] if workspace else [])
                         + (["-race"] if race else [])
                         + ["-tags=microvm_dev", "./internal/adapter/microvmmanager",
                            "./cmd/mecated", "./cmd/mecatui"])

    def test_dispatch_inventory_flags_and_pinning(self):
        self.fake_go()
        for task in ("test", "test:race", "test:workspace", "test:workspace:race"):
            workspace = "workspace" in task
            race = task.endswith(":race")
            for ambient in ("", "off", str(self.unrelated)):
                with self.subTest(task=task, ambient=ambient):
                    self.assert_dispatch(self.dispatch(task, ambient), workspace, race, ambient)
            for override in ("off", str(self.unrelated)):
                with self.subTest(task=task, override=override):
                    calls = self.dispatch(task, "off", f"TEST_GOWORK={override}",
                                          f"GOWORK={override}", f"ROOT_DIR={self.base / 'wrong'}")
                    self.assert_dispatch(calls, workspace, race, "off")
            if workspace:
                nested = self.root / next(module for module in self.modules if "/" in module)
                self.assert_dispatch(self.dispatch(task, "off", cwd=nested), True, race, "off")
        # Calling the original tag task directly must also retain ambient GOWORK.
        calls = self.dispatch("test:microvm-dev-tag", str(self.unrelated))
        self.assertEqual(len(calls), 1)
        self.assertEqual(calls[0]["gowork"], str(self.unrelated))
        self.assertNotIn("-timeout=20m", calls[0]["args"])

    def test_module_dispatch_flags_pinning_and_failures(self):
        self.fake_go()
        for task in ("test:module", "test:module:race", "test:workspace:module",
                     "test:workspace:module:race"):
            workspace = "workspace" in task
            race = task.endswith(":race")
            for module in self.modules:
                for ambient in ("", "off", str(self.unrelated)):
                    with self.subTest(task=task, module=module, ambient=ambient):
                        calls = self.dispatch(task, ambient, f"MODULE={module}",
                                              f"TEST_GOWORK={self.unrelated}",
                                              f"GOWORK={self.unrelated}",
                                              f"ROOT_DIR={self.base / 'wrong'}")
                        self.assertEqual(calls, [{
                            "module": module,
                            "gowork": str(self.root / "go.work") if workspace else "off",
                            "args": ["test", "-mod=readonly", "-timeout=20m"]
                                    + (["-race"] if race else []) + ["./..."],
                        }])
            nested = self.root / "provider/openai"
            calls = self.dispatch(task, "off", "MODULE=provider/openai", cwd=nested)
            self.assertEqual(len(calls), 1)
            self.assertEqual(calls[0]["module"], "provider/openai")
            self.assertEqual(calls[0]["gowork"], str(self.root / "go.work") if workspace else "off")
            calls = self.dispatch(task, "off", "MODULE=provider/openai", succeeds=False,
                                  fail="provider/openai")
            self.assertEqual(len(calls), 1)

    def test_module_validation_and_tidy_dispatch(self):
        self.fake_go()
        tasks = ("test:module", "test:module:race", "test:workspace:module",
                 "test:workspace:module:race", "tidy:module")
        for task in tasks:
            for overrides in ((), ("MODULE=",), ("MODULE=../unrelated",),
                              (f"MODULE={self.root / 'engine'}",),
                              ("MODULE=provider",), ("MODULE=engine; false",)):
                with self.subTest(task=task, overrides=overrides):
                    self.assertEqual(self.dispatch(task, "off", *overrides, succeeds=False), [])
        for module in self.modules:
            with self.subTest(module=module):
                self.assertEqual(self.dispatch("tidy:module", str(self.unrelated),
                                               f"MODULE={module}",
                                               f"TEST_GOWORK={self.unrelated}"), [{
                    "module": module, "gowork": "off", "args": ["mod", "tidy"],
                }])
        calls = self.dispatch("tidy:module", "off", "MODULE=engine", succeeds=False, fail="engine")
        self.assertEqual(len(calls), 1)

    def test_real_tidy_only_changes_selected_module(self):
        for module in self.modules:
            (self.root / module / "go.mod").write_text(
                f"module fixture.invalid/{module if module != '.' else 'root'}\n\n"
                "go 1.27\n\nrequire fixture.invalid/unused v1.0.0\n")
            (self.root / module / "fixture.go").write_text("package fixture\n")
        metadata = [self.root / "go.work"]
        metadata += [self.root / module / "go.mod" for module in self.modules]
        before = {path: path.read_bytes() for path in metadata}
        self.run_command([TASK, "tidy:module", "MODULE=provider/openai"],
                         env=dict(self.env, GOWORK=str(self.unrelated)))
        selected = self.root / "provider/openai/go.mod"
        self.assertNotIn("fixture.invalid/unused", selected.read_text())
        for path in metadata:
            if path != selected:
                self.assertEqual(path.read_bytes(), before[path])

    def test_nested_and_tag_failures_propagate(self):
        self.fake_go()
        nested = next(module for module in self.modules if "/" in module)
        for task in ("test", "test:race", "test:workspace", "test:workspace:race"):
            for fail in (nested, "tag"):
                with self.subTest(task=task, fail=fail):
                    calls = self.dispatch(task, "off", succeeds=False, fail=fail)
                    self.assertTrue(any(("-tags=microvm_dev" in call["args"])
                                        if fail == "tag" else call["module"] == fail
                                        for call in calls), calls)

    def test_real_dependency_resolution_and_supplemental_sums(self):
        dependency = "fixture.invalid/dependency"
        candidate = next(module for module in self.modules if module != ".")
        for index, module in enumerate(self.modules):
            directory = self.root / module
            name = dependency if module == candidate else f"fixture.invalid/module{index}"
            (directory / "go.mod").write_text(f"module {name}\n\ngo 1.27\n")
            (directory / "fixture.go").write_text("package fixture\n")

        old = self.base / "published"
        old.mkdir()
        (old / "go.mod").write_text(f"module {dependency}\n\ngo 1.27\n")
        (old / "fixture.go").write_text('package fixture\nfunc Standalone() string { return "published" }\n')
        with (self.root / "go.mod").open("a") as mod:
            mod.write(f'\nrequire {dependency} v1.0.0\nreplace {dependency} => "{old}"\n')

        # A file proxy exercises genuine downloads/checksums without network access.
        proxy = self.base / "proxy"
        versions = proxy / "fixture.invalid/supplement/@v"
        versions.mkdir(parents=True)
        mod = "module fixture.invalid/supplement\n\ngo 1.27\n"
        (versions / "v1.0.0.mod").write_text(mod)
        (versions / "v1.0.0.info").write_text('{"Version":"v1.0.0","Time":"2026-01-01T00:00:00Z"}')
        with zipfile.ZipFile(versions / "v1.0.0.zip", "w") as archive:
            archive.writestr("fixture.invalid/supplement@v1.0.0/go.mod", mod)
            archive.writestr("fixture.invalid/supplement@v1.0.0/value.go",
                             'package supplement\nconst Value = "workspace"\n')
        self.env["GOPROXY"] = proxy.as_uri()
        with (self.root / candidate / "go.mod").open("a") as mod:
            mod.write("\nrequire fixture.invalid/supplement v1.0.0\n")
        (self.root / candidate / "fixture.go").write_text(
            'package fixture\nimport "fixture.invalid/supplement"\n'
            'func Candidate() string { return supplement.Value }\n')
        consumer = ('package fixture\nimport ("testing"; dep "fixture.invalid/dependency")\n'
                    'func TestSelected(t *testing.T) { if got := dep.Candidate(); got != "workspace" '
                    '{ t.Fatalf("selected %q", got) } }\n')
        (self.root / "consumer_test.go").write_text("//go:build !standalone\n\n" + consumer)
        (self.root / "standalone_test.go").write_text(
            '//go:build standalone\n\npackage fixture\n'
            'import ("testing"; dep "fixture.invalid/dependency")\n'
            'func TestStandalone(t *testing.T) { if dep.Standalone() != "published" { t.Fatal("wrong source") } }\n')
        for package in ("internal/adapter/microvmmanager", "cmd/mecated", "cmd/mecatui"):
            directory = self.root / package
            directory.mkdir(parents=True, exist_ok=True)
            (directory / "consumer_test.go").write_text("//go:build microvm_dev\n\n" + consumer)

        metadata = [self.root / "go.work"]
        metadata += [self.root / module / name for module in self.modules
                     for name in ("go.mod", "go.sum")]
        before = {path: path.read_bytes() if path.exists() else None for path in metadata}
        # Standalone is a valid old dependency, but cannot compile the candidate-only API.
        self.run_command([GO, "test", "-mod=readonly", "-tags=standalone", "."])
        failure = self.run_command([GO, "test", "-mod=readonly", "."], succeeds=False)
        self.assertIn("undefined: dep.Candidate", failure.stdout + failure.stderr)
        for ambient in ("off", str(self.unrelated)):
            with self.subTest(ambient=ambient):
                self.run_command([TASK, "test:workspace", f"TEST_GOWORK={ambient}",
                                  f"GOWORK={ambient}"], env=dict(self.env, GOWORK=ambient))
                self.run_command([TASK, "test:workspace:module", "MODULE=.",
                                  f"TEST_GOWORK={ambient}", f"GOWORK={ambient}"],
                                 env=dict(self.env, GOWORK=ambient))
        sums = (self.root / "go.work.sum").read_text()
        self.assertIn("fixture.invalid/supplement v1.0.0 h1:", sums)
        self.assertIn("fixture.invalid/supplement v1.0.0/go.mod h1:", sums)
        self.assertEqual(before, {path: path.read_bytes() if path.exists() else None
                                  for path in metadata})


if __name__ == "__main__":
    unittest.main()
