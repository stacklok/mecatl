#!/usr/bin/env python3
"""Real-Go, offline candidate-closure regressions (stdlib only)."""

import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest import mock
import zipfile


CHECK = Path(__file__).resolve().with_name("module-candidate.py")
GO = shutil.which("go")
SIBLING = "fixture.invalid/sibling"
THIRD = "fixture.invalid/third"


class CandidateTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temporary = tempfile.TemporaryDirectory(prefix="module-candidate-tests-")
        cls.addClassCleanup(cls.temporary.cleanup)
        cls.base = Path(cls.temporary.name)
        cls.cache = cls.base / "cache"

    def setUp(self):
        self.assertIsNotNone(GO, "go must be on PATH")
        temporary = tempfile.TemporaryDirectory(dir=self.base)
        self.addCleanup(temporary.cleanup)
        self.base_dir = Path(temporary.name)
        self.root = self.base_dir / "checkout 'with $spaces"
        self.root.mkdir()
        self.proxy = self.base_dir / "proxy"
        self.env = {
            "PATH": os.environ["PATH"], "HOME": str(self.base_dir / "home"),
            "XDG_CONFIG_HOME": str(self.base_dir / "config"),
            "GOPATH": str(self.base_dir / "gopath"), "GOCACHE": str(self.cache),
            "TMPDIR": str(self.base_dir / "tmp"), "GOENV": "off",
            "GOTOOLCHAIN": "local", "GOPROXY": self.proxy.as_uri(),
            "GOSUMDB": "off", "GOWORK": str(self.base_dir / "poison.work"),
            "GOFLAGS": "",
        }
        for key in ("HOME", "TMPDIR", "XDG_CONFIG_HOME"):
            Path(self.env[key]).mkdir()
        self.write("go.work", 'go 1.23.0\nuse (\n .\n "./sibling space"\n)\n')
        self.write("go.mod", f"module fixture.invalid/root\n\ngo 1.23.0\n\nrequire {SIBLING} v0.2.0\n")
        self.write("main.go", f'package fixture\nimport "{SIBLING}"\nvar Value = sibling.Value\n')
        self.write("sibling space/go.mod", f"module {SIBLING}\n\ngo 1.23.0\n")
        self.write("sibling space/sibling.go", "package sibling\nconst Value = 1\n")
        self.publish(SIBLING, "v0.2.0", "package sibling\nconst Value = 0\n")
        self.publish(THIRD, "v1.0.0", "package third\nconst Old = 1\n")
        self.publish(THIRD, "v1.1.0", "package third\nconst Old = 1\nconst New = 2\n")

    def write(self, name, text):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text)
        return path

    def publish(self, name, version, source, *, go="1.23.0", requires=""):
        versions = self.proxy / name / "@v"
        versions.mkdir(parents=True, exist_ok=True)
        mod = f"module {name}\n\ngo {go}\n{requires}"
        (versions / f"{version}.mod").write_text(mod)
        (versions / f"{version}.info").write_text(json.dumps({
            "Version": version, "Time": "2026-01-01T00:00:00Z"}))
        (versions / "list").write_text("\n".join(sorted(
            path.stem for path in versions.glob("*.mod"))) + "\n")
        with zipfile.ZipFile(versions / f"{version}.zip", "w") as archive:
            archive.writestr(f"{name}@{version}/go.mod", mod)
            archive.writestr(f"{name}@{version}/third.go", source)

    def go(self, *args, cwd=None, workspace=False, succeeds=True):
        result = subprocess.run([GO, *args], cwd=cwd or self.root,
                                env=dict(self.env, GOWORK=str(self.root / "go.work")
                                         if workspace else "off"),
                                capture_output=True, text=True, timeout=120)
        self.assertEqual(result.returncode == 0, succeeds, result.stdout + result.stderr)
        return result

    def third(self, version="v1.0.0", *, module=".", api="Old", indirect=False):
        prefix = "" if module == "." else module + "/"
        path = self.root / prefix / "go.mod"
        path.write_text(path.read_text() + f"\nrequire {THIRD} {version}"
                        + (" // indirect" if indirect else "") + "\n")
        self.write(prefix + "third.go", f'package {"fixture" if module == "." else "sibling"}\n'
                   f'import "{THIRD}"\nvar Third = third.{api}\n')
        self.go("mod", "download", THIRD, cwd=self.root / module)

    def snapshot(self):
        return {str(path.relative_to(self.root)): path.read_bytes()
                for path in self.root.rglob("*")
                if path.is_file() and path.name in ("go.mod", "go.sum", "go.work", "go.work.sum")}

    def check(self, *args, succeeds=True, fix=False):
        before = self.snapshot()
        result = subprocess.run([sys.executable, str(CHECK), "--root", str(self.root),
                                 *args, *(["--fix"] if fix else [])],
                                cwd=self.root, env=self.env, capture_output=True,
                                text=True, timeout=120)
        self.assertEqual(result.returncode == 0, succeeds, result.stdout + result.stderr)
        if not fix:
            self.assertEqual(self.snapshot(), before)
        return result.stdout + result.stderr

    def test_lagging_sibling_versions_spaces_poison_and_no_execution(self):
        marker = self.base_dir / "executed"
        self.write("panic_test.go", 'package fixture\nimport "testing"\n'
                   'func TestNever(t *testing.T) { panic("test ran") }\n')
        self.write("init.go", 'package fixture\nimport "os"\n'
                   f'func init() {{ os.WriteFile({json.dumps(str(marker))}, []byte("ran"), 0600); panic("init ran") }}\n')
        self.check()
        self.assertFalse(marker.exists())

    def test_poisoned_ambient_goflags_do_not_affect_check(self):
        self.env["GOFLAGS"] = "-modfile=poison.mod"
        self.go("list", "./...", succeeds=False)
        self.check()

    def test_explicit_toolchain_directive_is_preserved(self):
        path = self.root / "go.mod"
        for version in ("go1.23.0", "go1.24.0"):
            with self.subTest(toolchain=version):
                path.write_text(f"module fixture.invalid/root\n\ngo 1.23.0\n"
                                f"toolchain {version}\n\nrequire {SIBLING} v0.2.0\n")
                before = self.snapshot()
                self.check("--module", ".")
                self.check("--module", ".", fix=True)
                self.assertEqual(self.snapshot(), before)

    def test_explicit_toolchain_minimum_affects_comparison(self):
        spec = importlib.util.spec_from_file_location("module_candidate", CHECK)
        checker = importlib.util.module_from_spec(spec)
        with mock.patch.object(sys, "dont_write_bytecode", True):
            spec.loader.exec_module(checker)
        go_minimum = {"Go": "1.23.0"}
        explicit = dict(go_minimum, Toolchain="go1.24.0")
        self.assertGreater(checker.effective_toolchain(explicit),
                           checker.effective_toolchain(go_minimum))
        self.assertEqual(checker.effective_toolchain(dict(go_minimum, Toolchain="default")),
                         checker.effective_toolchain(go_minimum))

    def test_test_sources_are_compiled(self):
        self.write("invalid_test.go", "package fixture\nvar _ = DoesNotExist\n")
        self.assertIn("undefined: DoesNotExist", self.check("--module", ".", succeeds=False))

    def test_workspace_masks_missing_thirdparty_requirement(self):
        self.third(module="sibling space")
        self.write("third.go", f'package fixture\nimport "{THIRD}"\nvar Third = third.Old\n')
        self.go("test", "-exec=true", "-run=^$", "./...", workspace=True)
        output = self.check("--module", ".", succeeds=False)
        self.assertIn(THIRD, output)
        self.assertIn("require", output)

    def test_workspace_mvs_masks_api_mismatch_even_when_tidy_passes(self):
        self.third(api="New")
        self.third("v1.1.0", module="sibling space")
        # An unrelated workspace member, not a dependency of root, supplies the upgrade.
        self.write("main.go", "package fixture\n")
        self.write("go.mod", f"module fixture.invalid/root\n\ngo 1.23.0\nrequire {THIRD} v1.0.0\n")
        self.go("test", "-exec=true", "-run=^$", "./...", workspace=True)
        self.go("mod", "tidy")
        self.assertIn(f"{THIRD} v1.0.0", (self.root / "go.mod").read_text())
        self.assertIn("undefined", self.check("--module", ".", succeeds=False))

    def test_missing_sibling_require_is_not_repaired_with_fake_version(self):
        self.write("go.mod", "module fixture.invalid/root\n\ngo 1.23.0\n")
        self.assertIn(SIBLING, self.check("--module", ".", succeeds=False))
        before = self.snapshot()
        self.assertIn("version", self.check("--module", ".", succeeds=False, fix=True))
        self.assertEqual(self.snapshot(), before)

    def test_directness_and_safe_repair_preserve_sibling_version(self):
        self.third(indirect=True)
        self.assertIn("indirect", self.check("--module", ".", succeeds=False))
        self.check("--module", ".", fix=True)
        mod = (self.root / "go.mod").read_text()
        self.assertIn(f"{SIBLING} v0.2.0", mod)
        self.assertNotIn("replace", mod)
        self.assertNotIn("00010101", mod)
        self.assertNotIn("indirect", mod)
        self.check("--module", ".")
        before = self.snapshot()
        self.check("--module", ".", fix=True)
        self.assertEqual(self.snapshot(), before)

    def test_missing_sums_repaired_but_extra_sums_allowed(self):
        self.third()
        sums = self.root / "go.sum"
        required = sums.read_text()
        sums.unlink()
        self.assertIn("sum", self.check("--module", ".", succeeds=False))
        self.check("--module", ".", fix=True)
        self.assertEqual(sums.read_text(), required)
        sums.write_text(required + "fixture.invalid/retained v1.0.0 h1:unused\n")
        self.check("--module", ".")

    def test_every_module_checked_independently(self):
        self.third("v1.1.0")
        self.third(api="New", module="sibling space")
        self.check("--module", ".")
        output = self.check(succeeds=False)
        self.assertIn(SIBLING, output)
        self.assertIn("sibling space", output)
        self.assertIn("undefined", output)

    def test_newer_dependency_go_minimum_requires_metadata_repair(self):
        self.publish("fixture.invalid/newgo", "v1.0.0", "package third\nconst Old = 1\n", go="1.24.0")
        self.write("new.go", 'package fixture\nimport "fixture.invalid/newgo"\nvar New = third.Old\n')
        output = self.check("--module", ".", succeeds=False)
        self.assertIn("go minimum: 1.23.0 -> 1.24.0", output)
        self.assertIn("toolchain minimum:", output)
        self.check("--module", ".", fix=True)
        self.assertIn("go 1.24.0", (self.root / "go.mod").read_text())
        self.check("--module", ".")

    def test_original_relative_and_versioned_replacements_survive_repair(self):
        self.third(indirect=True)
        local = self.root / "local replacement"
        local.mkdir()
        (local / "go.mod").write_text(f"module {THIRD}\n\ngo 1.23.0\n")
        (local / "third.go").write_text("package third\nconst Old = 1\n")
        with (self.root / "go.mod").open("a") as mod:
            mod.write(f'\nreplace {THIRD} v1.0.0 => "./local replacement"\n'
                      f'replace {SIBLING} v0.2.0 => "./sibling space"\n')
        self.check("--module", ".", succeeds=False)
        self.check("--module", ".", fix=True)
        mod = (self.root / "go.mod").read_text()
        self.assertIn(f'{THIRD} v1.0.0 => "./local replacement"', mod)
        self.assertIn(f'{SIBLING} v0.2.0 => "./sibling space"', mod)
        self.assertNotIn(str(self.root), mod)
        self.check("--module", ".")

    def test_same_repo_mvs_version_drift_is_allowed_and_never_saved(self):
        helper = "fixture.invalid/helper"
        self.write("helper/go.mod", f"module {helper}\n\ngo 1.23.0\n")
        self.write("helper/helper.go", "package helper\nconst Value = 1\n")
        with (self.root / "go.work").open("a") as work:
            work.write("\nuse ./helper\n")
        with (self.root / "go.mod").open("a") as mod:
            mod.write(f"\nrequire {helper} v0.1.0\n")
        with (self.root / "sibling space/go.mod").open("a") as mod:
            mod.write(f"\nrequire {helper} v0.9.0\n")
        self.write("helper.go", f'package fixture\nimport "{helper}"\nvar Helper = helper.Value\n')
        self.write("sibling space/helper.go", f'package sibling\nimport "{helper}"\nvar Helper = helper.Value\n')
        self.check()
        self.third(indirect=True)
        self.check("--module", ".", fix=True)
        mod = (self.root / "go.mod").read_text()
        self.assertIn(f"{helper} v0.1.0", mod)
        self.assertNotIn("v0.9.0", mod)
        self.assertNotIn("replace", mod)
        self.check()

    def test_thirdparty_versions_cannot_drift_with_sibling_mvs(self):
        self.third()
        self.third("v1.1.0", module="sibling space")
        output = self.check("--module", ".", succeeds=False)
        self.assertIn(f"require {THIRD}", output)
        self.assertIn("v1.0.0", output)
        self.assertIn("v1.1.0", output)
        self.check("--module", ".", fix=True)
        self.assertIn(f"{THIRD} v1.1.0", (self.root / "go.mod").read_text())
        self.check()

    def test_exact_identity_not_prefix_and_remote_replacement(self):
        name = SIBLING + "/extra"
        self.publish(name, "v1.0.0", "package third\nconst Old = 1\n")
        self.write("extra.go", f'package fixture\nimport "{name}"\nvar Extra = third.Old\n')
        with (self.root / "go.mod").open("a") as mod:
            mod.write(f"\nrequire {name} v1.0.0\n"
                      f"replace {name} => {THIRD} v1.0.0\n")
        # A remote third-party replacement is retained rather than turned into
        # a local replacement just because its identity starts with a sibling.
        output = self.check("--module", ".", succeeds=False)
        self.assertIn("sum", output)
        self.check("--module", ".", fix=True)
        self.assertIn(f"replace {name} => {THIRD} v1.0.0",
                      (self.root / "go.mod").read_text())
        self.check("--module", ".")
        sums = self.root / "go.sum"
        sums.write_text("")
        self.assertIn(THIRD, self.check("--module", ".", succeeds=False))

    def test_indirect_classification_survives_sibling_version_restoration(self):
        self.write("main.go", "package fixture\n")
        self.publish("fixture.invalid/bridge", "v1.0.0",
                     f'package third\nimport "{SIBLING}"\nvar Old = sibling.Value\n',
                     requires=f"\nrequire {SIBLING} v0.2.0\n")
        self.write("bridge.go", 'package fixture\nimport "fixture.invalid/bridge"\nvar Bridge = third.Old\n')
        self.check("--module", ".", fix=True)
        mod = (self.root / "go.mod").read_text()
        self.assertIn(f"{SIBLING} v0.2.0 // indirect", mod)
        self.check("--module", ".")

    def test_relative_replacement_base_with_external_modfile_experiment(self):
        # Verify the real Go command's base, independently of script rewriting.
        # The replacement exists beside source go.mod, NOT beside -modfile.
        self.write("local dependency/go.mod", f"module {THIRD}\n\ngo 1.23.0\n")
        self.write("local dependency/third.go", "package third\nconst Old = 1\n")
        self.write("third.go", f'package fixture\nimport "{THIRD}"\nvar Third = third.Old\n')
        external = self.base_dir / "external"
        external.mkdir()
        modfile = external / "experiment.mod"
        modfile.write_text((self.root / "go.mod").read_text()
                           + f'\nreplace {SIBLING} => "{self.root / "sibling space"}"\n'
                           + f'require {THIRD} v1.0.0\nreplace {THIRD} => "./local dependency"\n')
        before = self.snapshot()
        self.go("mod", "tidy", "-modfile=" + str(modfile))
        self.go("test", "-mod=readonly", "-modfile=" + str(modfile), "-exec=true", "-run=^$", "./...")
        self.assertEqual(self.snapshot(), before)

    def test_repair_refuses_concurrent_metadata_edit(self):
        self.third(indirect=True)
        spec = importlib.util.spec_from_file_location("module_candidate", CHECK)
        checker = importlib.util.module_from_spec(spec)
        with mock.patch.object(sys, "dont_write_bytecode", True):
            spec.loader.exec_module(checker)
        original_apply = checker.apply
        path = self.root / "go.mod"
        concurrent = path.read_bytes() + b"\n// concurrent edit\n"
        before_sum = (self.root / "go.sum").read_bytes()

        def concurrent_apply(changes, snapshots):
            path.write_bytes(concurrent)
            return original_apply(changes, snapshots)

        output = io.StringIO()
        with mock.patch.dict(os.environ, self.env, clear=True), \
                mock.patch.object(checker, "apply", side_effect=concurrent_apply), \
                contextlib.redirect_stdout(output), contextlib.redirect_stderr(output):
            result = checker.main(["--root", str(self.root), "--module", ".", "--fix"])
        self.assertEqual(result, 1)
        self.assertIn("metadata changed", output.getvalue())
        self.assertEqual(path.read_bytes(), concurrent)
        self.assertEqual((self.root / "go.sum").read_bytes(), before_sum)

    def test_no_repairs_if_another_selected_module_fails(self):
        self.third("v1.1.0", indirect=True)
        self.third(module="sibling space", api="New")
        before = self.snapshot()
        self.check(succeeds=False, fix=True)
        self.assertEqual(self.snapshot(), before)

    def test_go_newer_than_installed_fails_without_toolchain_download(self):
        version = self.go("env", "GOVERSION").stdout.strip().removeprefix("go")
        major, minor = version.split(".")[:2]
        self.write("sibling space/go.mod", f"module {SIBLING}\n\ngo {major}.{int(minor) + 1}.0\n")
        before = self.snapshot()
        output = self.check("--module", "sibling space", succeeds=False, fix=True)
        self.assertIn("GOTOOLCHAIN=local", output)
        self.assertEqual(self.snapshot(), before)

    def test_real_task_check_and_repair_selected_module(self):
        root = CHECK.parents[2]
        taskfile = (root / "Taskfile.yml").read_text()
        self.write("Taskfile.yml", taskfile)
        for include in re.findall(r"^\s+taskfile: (.+)$", taskfile, re.M):
            self.write(include, "version: '3'\ntasks: {}\n")
        self.write(".github/scripts/module-candidate.py", CHECK.read_text())
        task = shutil.which("task")
        self.assertIsNotNone(task, "task must be on PATH")
        self.third(module="sibling space", indirect=True)
        before = self.snapshot()
        args = ["MODULE=sibling space"]
        result = subprocess.run([task, "test:module-candidate", *args],
                                cwd=self.root, env=self.env, capture_output=True,
                                text=True, timeout=120)
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("metadata drift", result.stdout + result.stderr)
        self.assertEqual(self.snapshot(), before)
        result = subprocess.run([task, "tidy:module-candidate", *args],
                                cwd=self.root, env=self.env, capture_output=True,
                                text=True, timeout=120)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        result = subprocess.run([task, "test:module-candidate", *args],
                                cwd=self.root, env=self.env, capture_output=True,
                                text=True, timeout=120)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_duplicate_module_identity_and_invalid_selector(self):
        self.assertIn("module", self.check("--module", "not-in-workspace", succeeds=False))
        self.write("sibling space/go.mod", "module fixture.invalid/root\n\ngo 1.23.0\n")
        self.assertIn("duplicate", self.check(succeeds=False).lower())


if __name__ == "__main__":
    unittest.main()
