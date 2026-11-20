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
if os.environ.get("PACKAGE_FIXTURE"):
    if sys.argv[1] == "build":
        Path(sys.argv[sys.argv.index("-o") + 1]).write_text("fixture binary\\n")
    elif sys.argv[1] == "run":
        print("sha256:" + "a" * 64)
sys.exit(1 if os.environ.get("FAIL_LEG") == ("tag" if tagged else module) else 0)
''')
        fake.chmod(0o755)
        self.env.update(PATH=f"{bin_dir}:{self.env['PATH']}",
                        CALLS=str(self.base / "calls.jsonl"), FIXTURE_ROOT=str(self.root))

    def dispatch(self, task, ambient, *overrides, succeeds=True, fail="", cwd=None):
        Path(self.env["CALLS"]).write_text("")
        env = dict(self.env, FAIL_LEG=fail)
        if ambient:
            env["GOWORK"] = ambient
        else:
            env.pop("GOWORK", None)
        self.run_command([TASK, task, *overrides], succeeds=succeeds, cwd=cwd, env=env)
        return [json.loads(line) for line in Path(self.env["CALLS"]).read_text().splitlines()]

    def assert_dispatch(self, calls, gowork, race):
        modules = [call for call in calls if "-tags=microvm_dev" not in call["args"]]
        tags = [call for call in calls if "-tags=microvm_dev" in call["args"]]
        self.assertCountEqual([call["module"] for call in modules], self.modules)
        self.assertEqual(len(tags), 1)
        for call in calls:
            self.assertEqual(call["gowork"], gowork)
            self.assertEqual(call["args"][0], "test")
            self.assertIn("-mod=readonly", call["args"])
            self.assertEqual("-race" in call["args"], race)
        for call in modules:
            self.assertIn("./...", call["args"])
        self.assertEqual(tags[0]["module"], ".")
        for package in ("./internal/adapter/microvmmanager", "./cmd/mecated", "./cmd/mecatui"):
            self.assertIn(package, tags[0]["args"])

    def test_dispatch_inventory_flags_and_workspace_selection(self):
        self.fake_go()
        ordinary = ("test", "test:non-race", "test:race")
        workspace = ("test:workspace", "test:workspace:race")
        standalone = ("test:standalone", "test:standalone:race")
        for task in ordinary + workspace + standalone:
            race = task.endswith(":race")
            for ambient in ("", "off", str(self.unrelated)):
                with self.subTest(task=task, ambient=ambient):
                    expected = ("off" if task in standalone else str(self.root / "go.work")
                                if task in workspace or not ambient else ambient)
                    self.assert_dispatch(self.dispatch(task, ambient), expected, race)
            if task in workspace:
                nested = self.root / next(module for module in self.modules if "/" in module)
                self.assert_dispatch(self.dispatch(task, "off", cwd=nested),
                                     str(self.root / "go.work"), race)
        # The direct tagged gate follows the ordinary Task environment too.
        for ambient in ("", "off", str(self.unrelated)):
            calls = self.dispatch("test:microvm-dev-tag", ambient)
            self.assertEqual(len(calls), 1)
            self.assertEqual(calls[0]["gowork"], str(self.root / "go.work") if not ambient else ambient)

    def test_ordinary_build_lint_and_microvm_selection(self):
        self.fake_go()
        lint = self.root / ".tools/bin/golangci-lint"
        lint.parent.mkdir(parents=True)
        shutil.copyfile(self.base / "bin/go", lint)
        lint.chmod(0o755)
        # Keep tool installation offline; exercise the real lint task commands.
        installer = self.root / "scripts/install-golangci-lint.sh"
        installer.parent.mkdir()
        installer.write_text("#!/bin/sh\nexit 0\n")
        for task in ("build", "microvm:build", "lint", "lint:cgo"):
            for ambient in ("", "off", str(self.unrelated)):
                with self.subTest(task=task, ambient=ambient):
                    calls = self.dispatch(task, ambient, cwd=self.root / "provider/openai")
                    self.assertTrue(calls)
                    self.assertEqual({call["gowork"] for call in calls},
                                     {str(self.root / "go.work") if not ambient else ambient})

    def test_ko_build_and_publish_selection(self):
        self.fake_go()
        # The fake records --push without contacting a registry.
        ko = self.base / "bin/ko"
        shutil.copyfile(self.base / "bin/go", ko)
        ko.chmod(0o755)
        self.env["KO_DOCKER_REPO"] = "fixture.invalid/images"
        for suffix, target in (("", "mecated"), (":k8s", "mecak8s"), (":mecatui", "mecatui")):
            for mode in ("build", "publish"):
                for ambient in ("", "off", str(self.unrelated)):
                    with self.subTest(suffix=suffix, mode=mode, ambient=ambient):
                        calls = self.dispatch(f"ko:{mode}{suffix}", ambient,
                                              cwd=self.root / "provider/openai")
                        self.assertEqual(len(calls), 1)
                        self.assertEqual(calls[0]["gowork"], str(self.root / "go.work") if not ambient else ambient)
                        self.assertIn(f"./cmd/{target}", calls[0]["args"])
                        self.assertEqual("--push" in calls[0]["args"], mode == "publish")

    def test_kind_default_and_pinned_selection(self):
        self.fake_go()
        for tool in ("kind", "helm", "kubectl", "docker"):
            stub = self.base / "bin" / tool
            stub.write_text("#!/bin/sh\nexit 0\n")
            stub.chmod(0o755)
        for task, ambient, expected in (
            ("e2e:k8s", "", str(self.root / "go.work")),
            ("e2e:k8s", "off", "off"),
            ("e2e:k8s:pinned", str(self.unrelated), "off"),
        ):
            with self.subTest(task=task, ambient=ambient):
                calls = self.dispatch(task, ambient, cwd=self.root / "provider/openai")
                self.assertEqual(len(calls), 1)
                self.assertEqual(calls[0]["gowork"], expected)
                self.assertIn("kind_e2e", calls[0]["args"])

    def test_microvm_preparation_and_release_packaging_selection(self):
        self.fake_go()
        self.env["PACKAGE_FIXTURE"] = "1"
        for relative in ("environment/microvm/e2e/prepare.sh",
                         ".github/scripts/package-microvm-release.sh",
                         ".github/scripts/install-microvm-release.sh"):
            destination = self.root / relative
            destination.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(ROOT / relative, destination)
            destination.chmod(0o755)
        signer = self.root / ".github/scripts/sign-microvm-release-evidence.sh"
        signer.write_text("#!/bin/sh\nexit 0\n")
        signer.chmod(0o755)
        # Only artifact/network/signing tools are synthetic. Both production shell
        # scripts run end-to-end, including packaging's digest build AND go run.
        tools = self.base / "bin"
        stub = tools / "artifact-tool"
        stub.write_text('''#!/usr/bin/env python3
import hashlib, json, sys
from pathlib import Path
name, args = Path(sys.argv[0]).name, sys.argv[1:]
if name == "uname":
    print("Linux" if args == ["-s"] else "x86_64")
elif name == "curl":
    Path(args[args.index("-o") + 1]).write_text("download fixture")
elif name == "sha256sum":
    path = Path(args[0])
    digests = {
        "runtime.tar.gz": "01371a0149ca39065f73d0c78d480f40236714a314bf7bef8ea292bacbfdf483",
        "firmware.tar.gz": "df7fb76e31ccbd9709e04ccc1a9d33546ad07100e8c4650d90b939553dd20007",
        "go-microvm-runtime-": "01371a0149ca39065f73d0c78d480f40236714a314bf7bef8ea292bacbfdf483",
        "go-microvm-firmware-": "df7fb76e31ccbd9709e04ccc1a9d33546ad07100e8c4650d90b939553dd20007",
        "alpine-minirootfs-": "0e5cc5702ad72a4e151f219976ba946d50161c3acce210ef3b122a529aba1270",
    }
    digest = next((value for prefix, value in digests.items() if path.name.startswith(prefix)), None)
    print((digest or hashlib.sha256(path.read_bytes()).hexdigest()) + "  " + str(path))
elif name == "tar":
    (Path(args[args.index("-C") + 1]) / "artifact").write_text("artifact fixture")
elif name == "docker" and args[:3] == ["buildx", "imagetools", "inspect"]:
    print(json.dumps({"manifests": [{"digest": "sha256:" + "b" * 64,
                                    "platform": {"os": "linux", "architecture": "amd64"}}]}))
elif name == "cosign":
    prefix = args[args.index("--output-key-prefix") + 1]
    Path(prefix + ".key").write_text("synthetic signing key")
    Path(prefix + ".pub").write_text("synthetic public key")
''')
        stub.chmod(0o755)
        for tool in ("uname", "curl", "sha256sum", "tar", "docker", "cosign"):
            (tools / tool).symlink_to(stub)
        for ambient in ("off", str(self.unrelated)):
            with self.subTest(ambient=ambient):
                Path(self.env["CALLS"]).write_text("")
                self.run_command([str(self.root / "environment/microvm/e2e/prepare.sh")],
                                 cwd=self.root / "provider/openai",
                                 env=dict(self.env, GOWORK=ambient))
                calls = [json.loads(line) for line in Path(self.env["CALLS"]).read_text().splitlines()]
                self.assertEqual(len(calls), 7)
                self.assertEqual({call["gowork"] for call in calls}, {str(self.root / "go.work")})
                self.assertTrue(any(call["args"][-1] == "./cmd/mecatl-artifact-digest"
                                    and call["args"][0] == "build" for call in calls))
                self.assertEqual(sum(call["args"][:2] == ["run", "./cmd/mecatl-artifact-digest"]
                                     for call in calls), 3)
        # Official packaging must ignore even an ambient development workspace.
        release_env = dict(self.env, GOWORK=str(self.root / "go.work"),
                           MICROVM_RELEASE_EXECUTION_IMAGE_REF="fixture.invalid/base@sha256:" + "b" * 64,
                           MICROVM_RELEASE_EXECUTION_IMAGE_MANIFEST_DIGEST="sha256:" + "b" * 64,
                           MICROVM_RELEASE_EXECUTION_IMAGE_TREE_DIGEST="sha256:" + "a" * 64,
                           MICROVM_RELEASE_EXECUTION_IMAGE_DISCOVERY_REFERENCE="ghcr.io/stacklok/brood-box/base:latest",
                           MICROVM_RELEASE_EXECUTION_IMAGE_RESOLUTION_EVIDENCE="sha256:" + "c" * 64,
                           MICROVM_RELEASE_EXECUTION_IMAGE_PLATFORM="linux/amd64")
        Path(self.env["CALLS"]).write_text("")
        self.run_command([str(self.root / ".github/scripts/package-microvm-release.sh"),
                          str(self.base / "release"), "linux-amd64", "fixture"], env=release_env)
        calls = [json.loads(line) for line in Path(self.env["CALLS"]).read_text().splitlines()]
        self.assertEqual(len(calls), 6)
        self.assertEqual({call["gowork"] for call in calls}, {"off"})
        self.assertEqual([call["args"][-1] for call in calls if call["args"][0] == "build"],
                         ["./cmd/mecatl-microvmd", "./cmd/mecatl-guest-agent",
                          "./cmd/mecatl-artifact-digest"])

    def test_module_dispatch_flags_pinning_and_failures(self):
        self.fake_go()
        for task in ("test:module", "test:module:race", "test:workspace:module",
                     "test:workspace:module:race"):
            workspace = "workspace" in task
            race = task.endswith(":race")
            for module in (".", "provider/openai"):
                with self.subTest(task=task, module=module):
                    calls = self.dispatch(task, str(self.unrelated), f"MODULE={module}")
                    self.assertEqual(len(calls), 1)
                    call = calls[0]
                    self.assertEqual(call["module"], module)
                    self.assertEqual(call["gowork"], str(self.root / "go.work") if workspace else "off")
                    self.assertIn("-mod=readonly", call["args"])
                    self.assertEqual("-race" in call["args"], race)
                    self.assertIn("./...", call["args"])
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
        for task in ("test", "test:race", "test:workspace", "test:workspace:race",
                     "test:standalone", "test:standalone:race"):
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
        # Both dependency graphs are valid, but expose different APIs.
        self.run_command([TASK, "test:module", "MODULE=."],
                         env=dict(self.env, GOWORK=str(self.unrelated), GOFLAGS="-tags=standalone"))
        checkout_env = dict(self.env)
        checkout_env.pop("GOWORK")
        self.run_command([TASK, "test"], env=checkout_env, cwd=self.root / candidate)
        self.run_command([TASK, "test:workspace"],
                         env=dict(self.env, GOWORK=str(self.unrelated)))
        failure = self.run_command([TASK, "test:standalone"], succeeds=False,
                                   env=dict(self.env, GOWORK=str(self.unrelated)))
        self.assertIn("undefined: dep.Candidate", failure.stdout + failure.stderr)
        sums = (self.root / "go.work.sum").read_text()
        self.assertIn("fixture.invalid/supplement v1.0.0 h1:", sums)
        self.assertIn("fixture.invalid/supplement v1.0.0/go.mod h1:", sums)
        self.assertEqual(before, {path: path.read_bytes() if path.exists() else None
                                  for path in metadata})


if __name__ == "__main__":
    unittest.main()
