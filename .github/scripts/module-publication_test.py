#!/usr/bin/env python3
"""Offline publication checks: real Go/git seams, mocked public propagation only."""

import argparse
import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import selectors
import shlex
import subprocess
import sys
import tempfile
import time
import unittest
from unittest import mock
import zipfile


sys.dont_write_bytecode = True
SCRIPT = Path(__file__).resolve().with_name("module-publication.py")
SPEC = importlib.util.spec_from_file_location("publication", SCRIPT)
p = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(p)


class DiagnosticsTests(unittest.TestCase):

    def test_go_build_streams_before_completion_and_reports_failure(self):
        for verbose, status in ((False, 0), (True, 0), (False, 7)):
            with self.subTest(verbose=verbose, status=status), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                gate = root / "finish"
                go = root / "go"
                go.write_text(f"#!{sys.executable}\n" + '''import pathlib,sys,time
assert sys.argv[1:] == ["build", "-mod=readonly", "./..."]
print("build-live", flush=True)
while not pathlib.Path("finish").exists(): time.sleep(0.01)
''' + f"sys.exit({status})\n")
                go.chmod(0o755)
                code = (f"import os,runpy; p=runpy.run_path({str(SCRIPT)!r}); "
                        "p['candidate'].go(os.getcwd(), os.environ, 'build', '-mod=readonly', './...')")
                with subprocess.Popen([sys.executable, "-c", code], cwd=root, stdout=subprocess.PIPE,
                                      stderr=subprocess.PIPE,
                                      env=dict(os.environ, PATH=f"{root}:{os.environ['PATH']}",
                                               GOFLAGS="-v" if verbose else "")) as process:
                    try:
                        with selectors.DefaultSelector() as selector:
                            selector.register(process.stdout, selectors.EVENT_READ)
                            self.assertTrue(selector.select(5), "build output was buffered")
                            self.assertEqual(os.read(process.stdout.fileno(), 8192), b"build-live\n")
                        self.assertIsNone(process.poll())
                        self.assertFalse(gate.exists())
                        gate.touch()
                        stdout, stderr = process.communicate(timeout=10)
                        self.assertEqual(process.returncode == 0, status == 0, stderr)
                        if status:
                            self.assertIn(b"exit 7", stderr)
                    finally:
                        gate.touch()
                        process.communicate(timeout=10)

    def test_publication_timeout_defaults_and_prepare_package_bound(self):
        with mock.patch.object(p.candidate.subprocess, "run") as command:
            command.return_value = subprocess.CompletedProcess(["fixture"], 0, "", "")
            p.run(["fixture"], SCRIPT.parent)
            self.assertEqual(command.call_args.kwargs["timeout"], 1800)
        env = {"GOFLAGS": ""}
        with mock.patch.object(p.candidate, "run", return_value="") as command:
            for verb in ("test", "build"):
                p.candidate.go(SCRIPT.parent, env, verb, "./...")
                self.assertIsNone(command.call_args.kwargs["timeout"])
                self.assertFalse(command.call_args.kwargs["capture"])
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "go.mod").write_text("module fixture.invalid/root\n\ngo 1.23.0\n")
            (root / "fixture.go").write_text("package fixture\n")
            original = {"Module": {"Path": "fixture.invalid/root"}, "Go": "1.23.0"}
            env = p.environment(root / "execution")
            env.update(GOPROXY="off", GOSUMDB="off")
            with mock.patch.object(p.candidate, "go", wraps=p.candidate.go) as go:
                p.tidy(root, ("fixture.invalid/root", root, original), {}, {}, env, standalone=True)
            self.assertIn(mock.call(root, env, "test", "-mod=readonly", "-count=1",
                                    "-timeout=20m", "./..."), go.call_args_list)

    def test_parsed_stdout_is_preserved(self):
        command = [sys.executable, "-c", "import sys; print('{\"ok\":true}'); print('detail',file=sys.stderr)"]
        self.assertEqual(json.loads(p.run(command, SCRIPT.parent)), {"ok": True})
        with tempfile.TemporaryDirectory() as temporary:
            env = dict(os.environ, GOWORK="off", GOENV="off", GOFLAGS="", GOTOOLCHAIN="local")
            root = Path(temporary)
            (root / "go.mod").write_text("module fixture.invalid/metadata\n\ngo 1.23.0\n")
            with contextlib.redirect_stderr(io.StringIO()) as diagnostics:
                parsed = p.candidate.parse_mod(root, env, root / "go.mod")
            self.assertEqual(parsed["Module"]["Path"], "fixture.invalid/metadata")
            self.assertEqual(diagnostics.getvalue(), "")


@unittest.skipUnless(os.environ.get("PUBLICATION_DIAGNOSTICS_WORKFLOW"), "workflow supplied by Go contract test")
class DiagnosticsWorkflowTests(unittest.TestCase):
    @unittest.skipUnless(sys.platform == "linux", "publication runs on Linux")
    def test_runner_temp_root_allows_embedded_unix_socket(self):
        steps = json.loads(os.environ["PUBLICATION_DIAGNOSTICS_WORKFLOW"])["preflight"]
        for step in steps:
            args = shlex.split(step["Run"])
            root = args[args.index("--environment-dir") + 1]
            # Bind relative to a test-owned cwd so the real kernel sees the hosted
            # runner's path length, independent of this test's own temporary root.
            root = root.replace("$RUNNER_TEMP", "home/runner/work/_temp")
            with self.subTest(step=step["Run"]), tempfile.TemporaryDirectory() as temporary:
                result = subprocess.run([sys.executable, "-c", '''import pathlib,runpy,socket,sys
publication = runpy.run_path(sys.argv[1])
env = publication["environment"](pathlib.Path(sys.argv[2]))
runtime = pathlib.Path(env["TMPDIR"]) / "mecatui-test-home-4294967295/runtime/mecatui-4294967295"
runtime.mkdir(parents=True, mode=0o700)
with socket.socket(socket.AF_UNIX) as listener:
    listener.bind(str(runtime / "mecated.sock"))
''', str(SCRIPT), root], cwd=temporary, capture_output=True, text=True, timeout=10)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_summary_distinguishes_diagnostics_from_publication(self):
        step = json.loads(os.environ["PUBLICATION_DIAGNOSTICS_WORKFLOW"])["summary"]
        for only in ("true", "false"):
            with self.subTest(preflight_only=only), tempfile.TemporaryDirectory() as temporary:
                summary = Path(temporary) / "summary"
                result = subprocess.run(["bash", "-c", step["Run"]],
                    env=dict(os.environ, SOURCE="reviewed-source", VERSIONS='{"engine":"v0.2.0"}',
                             BRANCH="module-publication/123-1", PREFLIGHT_ONLY=only,
                             RESULTS='{"preflight":{"result":"success"}}',
                             GITHUB_STEP_SUMMARY=str(summary)), capture_output=True, text=True, timeout=10)
                self.assertEqual(result.returncode, 0, result.stderr)
                output = summary.read_text()
                self.assertIn("success", output)
                self.assertEqual("Branch:" in output, only == "false")
                self.assertEqual("publication and adoption disabled" in output, only == "true")


class RequestTests(unittest.TestCase):
    def test_pseudo_versions_rejected_before_preflight(self):
        for value in ("v0.0.0-20260101000000-aaaaaaaaaaaa",
                      "v1.2.4-0.20260101000000-aaaaaaaaaaaa",
                      "v1.2.3-pre.0.20260101000000-aaaaaaaaaaaa",
                      "v1.0.0-20260101000000-AbC123"):
            with self.subTest(value=value):
                raw = json.dumps({"engine": value})
                with self.assertRaisesRegex(p.CheckError, "pseudo-version"):
                    p.request(raw)
                with mock.patch.object(p, "environment") as env, \
                        mock.patch.object(p, "preflight") as preflight, \
                        contextlib.redirect_stderr(io.StringIO()):
                    self.assertEqual(p.main(["preflight", "--phase", "validate", "--source", "a" * 40,
                                             "--versions", raw, "--root", "/does-not-exist"]), 1)
                    env.assert_not_called()
                    preflight.assert_not_called()
        for value in ("v1.2.3-rc.1", "v1.2.3-20260101000000-aaaaaaaaaaaa",
                      "v0.0.0-20260101-aaaaaaaaaaaa", "v1.2.3-pre.20260101000000-abc"):
            with self.subTest(normal_prerelease=value):
                self.assertEqual(p.request(json.dumps({"engine": value})), {"engine": value})

    def test_requests(self):
        self.assertEqual(p.request('{"adapters":"v1.2.3-rc.1","engine":"v0.2.0"}'),
                         {"engine": "v0.2.0", "adapters": "v1.2.3-rc.1"})
        for raw in ('{}', '[]', '{"engine":1}', '{".":"v0.1.0"}',
                    '{"environment/microvm":"v0.1.0"}', '{"new":"v0.1.0"}',
                    '{"engine":"v0.1.0","engine":"v0.2.0"}'):
            with self.subTest(raw=raw), self.assertRaises(p.CheckError):
                p.request(raw)
        for value in ("1.0.0", "v01.0.0", "v1.2", "v1.0.0-01", "v1.0.0+x", "v2.0.0",
                      "v1.0.0\nbranch=bad"):
            with self.subTest(value=value), self.assertRaises(p.CheckError):
                p.version(value, p.identity("engine"))
        p.version("v2.0.0", p.identity("engine") + "/v2")
        with self.assertRaises(p.CheckError):
            p.version("v1.0.0", p.identity("engine") + "/v2")

    def test_unselected_never_executes_candidate(self):
        for command, extra in (("prepare", ["--root", "/does-not-exist", "--source", "a" * 40,
                                            "--parent", "b" * 40]),
                               ("prove", ["--head", "b" * 40])):
            with self.subTest(command=command), mock.patch.object(p, "environment") as env, \
                    mock.patch.object(p, "run") as run, mock.patch.object(p.candidate, "go") as go, \
                    mock.patch.dict(os.environ, {"GITHUB_OUTPUT": ""}), \
                    contextlib.redirect_stdout(io.StringIO()) as output:
                self.assertEqual(p.main([command, "--module", "adapters", "--versions",
                                         '{"engine":"v0.2.0"}', *extra]), 0)
                self.assertEqual(output.getvalue(), "selected=false\nhead=" + "b" * 40 + "\n")
                env.assert_not_called()
                run.assert_not_called()
                go.assert_not_called()

    def test_selection_only_never_checks_out_or_executes_candidate(self):
        for module in p.ORDER:
            with self.subTest(module=module), mock.patch.object(p, "environment") as env, \
                    mock.patch.object(p, "run") as run, \
                    mock.patch.dict(os.environ, {"GITHUB_OUTPUT": ""}), \
                    contextlib.redirect_stdout(io.StringIO()) as output:
                self.assertEqual(p.main(["prepare", "--selection-only", "--module", module,
                                         "--root", "/does-not-exist", "--source", "a" * 40,
                                         "--parent", "b" * 40, "--versions", json.dumps({module: "v0.2.0"})]), 0)
                self.assertEqual(output.getvalue(), "selected=true\nartifact-name=module-metadata-" +
                                 module.replace("/", "-") + "\n")
                env.assert_not_called()
                run.assert_not_called()

    def test_proof_environment_and_outputs(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            with mock.patch.dict(os.environ, {"GOENV": "/poison", "GOFLAGS": "-overlay=bad",
                                              "GOPROXY": "file:///poison", "GOPRIVATE": "*",
                                              "GONOSUMDB": "*", "GOINSECURE": "*",
                                              "GIT_CONFIG_GLOBAL": "/poison", "TOKEN": "not-a-secret"}):
                env = p.environment(root)
                marker = Path(env["GOCACHE"]) / "shared-cache"
                marker.touch()
                self.assertEqual(p.environment(root), env)
                self.assertTrue(marker.exists())
            self.assertEqual(env["GOENV"], "off")
            self.assertEqual(env["GOFLAGS"], "")
            self.assertEqual(env["GOPROXY"], "https://proxy.golang.org")
            self.assertEqual(env["GOSUMDB"], "sum.golang.org")
            self.assertEqual(env["GONOSUMDB"], "none")
            self.assertEqual(env["GONOPROXY"], "none")
            self.assertEqual(env["GOPRIVATE"], "")
            self.assertEqual(env["GOINSECURE"], "")
            self.assertNotIn("TOKEN", env)
            self.assertNotIn("GIT_CONFIG_GLOBAL", env)
            self.assertEqual(list(Path(env["GOMODCACHE"]).iterdir()), [])
            output = root / "outputs"
            with mock.patch.dict(os.environ, {"GITHUB_OUTPUT": str(output)}):
                p.outputs({"head": "a" * 40, "versions": '{"engine":"v1.0.0"}'})
                with self.assertRaises(p.CheckError):
                    p.outputs({"head": "a\nbranch=bad"})
            self.assertEqual(output.read_text(), 'head=' + 'a' * 40 + '\nversions={"engine":"v1.0.0"}\n')


class RepositoryTests(unittest.TestCase):
    def test_workspace_publication_inventory(self):
        with tempfile.TemporaryDirectory(prefix="publication-inventory-") as temporary:
            env = p.environment(Path(temporary))
            env.update(GOPROXY="off", GOSUMDB="off")
            p.inventory(SCRIPT.parents[2], env)
        self.assertEqual(set(p.IMPORTS), set(p.ORDER))


class LocalTests(unittest.TestCase):
    def setUp(self):
        self.workflow = os.environ.get("PUBLICATION_DIAGNOSTICS_WORKFLOW")
        temporary = tempfile.TemporaryDirectory(prefix="publication-tests-")
        self.addCleanup(temporary.cleanup)
        self.base = Path(temporary.name)
        self.root = self.base / "candidate with spaces"
        self.root.mkdir()
        execution = self.base / "execution"
        execution.mkdir()
        self.env = p.environment(execution)
        self.proxy = self.base / "proxy"
        self.env.update(GOPROXY=self.proxy.as_uri(), GOSUMDB="off")
        isolated = mock.patch.dict(os.environ, self.env, clear=True)
        isolated.start()
        self.addCleanup(isolated.stop)
        self.write("go.work", "go 1.27.3\nuse (\n" +
                   "\n".join(" ./" + path if path != "." else " ." for path in p.CONSUMERS + p.ORDER) + "\n)\n")
        for path in p.ORDER + p.CONSUMERS:
            self.write(str(Path(path) / "go.mod"), f"module {p.identity(path)}\n\ngo 1.27.3\n")
            self.write(str(Path(path) / "fixture.go"), "package fixture\nconst Value = 1\n")
        self.publish("engine", "v0.1.0")
        self.publish("engine", "v0.2.0")
        self.publish("provider/ssefilter", "v0.1.0")
        self.publish("provider/ssefilter", "v0.2.0")
        self.command("git", "init", "-b", "main")
        self.command("git", "config", "user.email", "fixture@example.invalid")
        self.command("git", "config", "user.name", "Fixture")
        self.command("git", "remote", "add", "origin", str(self.root))
        self.commit()

    def write(self, path, text):
        target = self.root / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(text, encoding="utf-8")
        return target

    def command(self, *args):
        return p.run(list(args), self.root, self.env).strip()

    def commit(self):
        self.command("git", "add", ".")
        self.command("git", "commit", "--allow-empty", "-m", "fixture")
        self.head = self.command("git", "rev-parse", "HEAD")

    def publish(self, path, exact, requires="", go_version="1.27.3", source=None):
        module = p.identity(path)
        destination = self.proxy / module / "@v"
        destination.mkdir(parents=True, exist_ok=True)
        mod = f"module {module}\n\ngo {go_version}\n{requires}"
        (destination / f"{exact}.mod").write_text(mod)
        (destination / f"{exact}.info").write_text(json.dumps({"Version": exact, "Time": "2026-01-01T00:00:00Z"}))
        (destination / "list").write_text("\n".join(sorted(p.stem for p in destination.glob("*.mod"))) + "\n")
        with zipfile.ZipFile(destination / f"{exact}.zip", "w") as archive:
            archive.writestr(f"{module}@{exact}/go.mod", mod)
            archive.writestr(f"{module}@{exact}/fixture.go", source or "package fixture\nconst Value = 1\n")

    def require(self, path, dependency="engine", exact="v0.1.0", replacement=False):
        mod = self.root / path / "go.mod"
        module = p.identity(dependency)
        mod.write_text(mod.read_text() + f"\nrequire {module} {exact}\n")
        if replacement:
            relative = os.path.relpath(self.root / dependency, mod.parent)
            if not relative.startswith("."):
                relative = "./" + relative
            mod.write_text(mod.read_text() + f"\nreplace {module} => {relative}\n")
        self.write(str(Path(path) / "fixture.go"),
                   f'package fixture\nimport dep "{module}"\nconst Value = dep.Value\n')
        p.candidate.go(mod.parent, self.env, "mod", "tidy")

    def args(self, **extra):
        return argparse.Namespace(root=self.root, source=self.head, parent=self.head,
                                  metadata=self.base / "metadata.json", **extra)

    def test_prepare_upgrade_uses_public_dependencies_and_accepts_tidy_metadata(self):
        self.publish("engine", "v0.1.0", go_version="1.27")
        self.write("adapters/go.mod", f"module {p.identity('adapters')}\n\ngo 1.27\n")
        self.require("adapters")
        p.candidate.go(self.root / "adapters", self.env, "mod", "tidy", "-diff")
        self.publish("agentdef", "v0.1.0")
        self.publish("engine", "v0.2.0", f"require {p.identity('agentdef')} v0.1.0\n",
                     source=f'package fixture\nimport a "{p.identity("agentdef")}"\nconst Released = a.Value + 6\n')
        self.write("adapters/fixture.go", f'package fixture\nimport e "{p.identity("engine")}"\nconst Value = e.Released\n')
        self.write("adapters/fixture_test.go", 'package fixture\nimport "testing"\n'
                   'func TestReleasedValue(t *testing.T) { if Value != 7 { t.Fatalf("value = %d", Value) } }\n')
        self.write("engine/fixture.go", "invalid local selected dependency\n")
        self.write("agentdef/fixture.go", "invalid local unselected dependency\n")
        self.commit()
        with self.assertRaises(p.CheckError):
            p.candidate.go(self.root / "adapters", self.env, "build", "-mod=readonly", "./...")
        args = self.args(module="adapters")
        p.prepare(args, {"engine": "v0.2.0", "adapters": "v0.3.0"}, self.env)
        desired = p.candidate.parse_mod(self.root, self.env, self.root / "adapters/go.mod")
        self.assertEqual(desired["Go"], "1.27.3")
        self.assertFalse(desired.get("Replace"))
        self.assertEqual(p.candidate.requirements(desired), {
            p.identity("engine"): ("v0.2.0", False), p.identity("agentdef"): ("v0.1.0", True)})
        artifact = json.loads(args.metadata.read_text())
        self.assertEqual(artifact["parent"], self.head)
        self.assertEqual(set(artifact["files"]), {"adapters/go.mod", "adapters/go.sum"})
        for path, data in artifact["files"].items():
            self.assertEqual(data, (self.root / path).read_text())

    def test_resolved_selected_version_mismatch_without_root_requirement(self):
        self.publish("agentdef", "v0.2.0")
        self.publish("agentdef", "v0.3.0")
        # A dependency used only by engine's own tests stays out of the consumer's requirements.
        self.publish("engine", "v0.2.0", f'require {p.identity("agentdef")} v0.3.0\n')
        archive = self.proxy / p.identity("engine") / "@v/v0.2.0.zip"
        with zipfile.ZipFile(archive, "a") as output:
            output.writestr(p.identity("engine") + '@v0.2.0/fixture_test.go',
                            f'package fixture\nimport _ "{p.identity("agentdef")}"\n')
        self.require("adapters")
        self.commit()
        directory = self.root / "adapters"
        p.candidate.go(directory, self.env, "mod", "edit", f'-require={p.identity("engine")}@v0.2.0')
        p.candidate.go(directory, self.env, "mod", "tidy")
        desired = p.candidate.parse_mod(directory, self.env, directory / "go.mod")
        self.assertNotIn(p.identity("agentdef"), p.candidate.requirements(desired))
        resolved = json.loads(p.candidate.go(directory, self.env, "list", "-m", "-json", p.identity("agentdef")))
        self.assertEqual(resolved["Version"], "v0.3.0")
        self.command("git", "restore", "--", "adapters/go.mod", "adapters/go.sum")
        with self.assertRaisesRegex(p.CheckError, "requested exact pin"):
            p.prepare(self.args(module="adapters"),
                      {"agentdef": "v0.2.0", "engine": "v0.2.0", "adapters": "v0.3.0"}, self.env)

    def test_prepare_does_not_infer_new_direct_sibling_from_selection(self):
        self.write("adapters/fixture.go", f'package fixture\nimport e "{p.identity("engine")}"\nconst Value = e.Value\n')
        self.commit()
        args = self.args(module="adapters")
        with self.assertRaisesRegex(p.CheckError, "missing new direct sibling"):
            p.prepare(args, {"engine": "v0.2.0", "adapters": "v0.3.0"}, self.env)
        self.assertFalse(args.metadata.exists())

    def test_inventory_and_drift(self):
        self.assertEqual(set(p.inventory(self.root, self.env)), set(p.ORDER + p.CONSUMERS))
        self.write("unknown/go.mod", "module fixture.invalid/unknown\n\ngo 1.27.3\n")
        p.candidate.go(self.root, dict(self.env, GOWORK=str(self.root / "go.work")), "work", "use", "./unknown")
        with self.assertRaisesRegex(p.CheckError, "inventory drift"):
            p.inventory(self.root, self.env)
        self.commit()
        with self.assertRaisesRegex(p.CheckError, "inventory drift"):
            self.preflight(phases=("final",))

    def test_identity_drift(self):
        self.write("engine/go.mod", "module fixture.invalid/engine\n\ngo 1.27.3\n")
        with self.assertRaisesRegex(p.CheckError, "identity drift"):
            p.inventory(self.root, self.env)

    def test_order_cycles_and_consumer_dependency(self):
        for dependency in ("provider/openai", ".", "new", "engine"):
            with self.subTest(dependency=dependency):
                self.write("engine/go.mod", f"module {p.identity('engine')}\n\ngo 1.27.3\n"
                           f"require {p.identity(dependency)} v0.1.0\n")
                with self.assertRaisesRegex(p.CheckError, "dependency/cycle/order drift"):
                    p.inventory(self.root, self.env)

    def test_prepare_real_go_exact_bytes_and_only_earlier_selected_pins(self):
        self.require("adapters")
        self.commit()
        args = self.args(module="adapters")
        result = p.prepare(args, {"engine": "v0.2.0", "adapters": "v0.3.0"}, self.env)
        data = json.loads(args.metadata.read_text())
        self.assertEqual(result["selected"], "true")
        self.assertEqual(data["parent"], self.head)
        self.assertTrue(data["files"])
        for name, contents in data["files"].items():
            self.assertTrue(name.startswith("adapters/go."))
            self.assertEqual((self.root / name).read_bytes(), contents.encode())
        mod = p.candidate.parse_mod(self.root, self.env, self.root / "adapters/go.mod")
        self.assertEqual(p.candidate.requirements(mod)[p.identity("engine")][0], "v0.2.0")
        p.check_changes(self.root, data["files"])

    def test_prepare_engine_with_selected_or_existing_agentdef(self):
        for exact in ("v0.1.0", "v0.2.0"):
            self.publish("agentdef", exact)
        self.require("engine", "agentdef")
        self.commit()
        args = self.args(module="engine")
        p.prepare(args, {"engine": "v0.2.0"}, self.env)
        self.assertEqual(json.loads(args.metadata.read_text())["files"], {})
        args.metadata = self.base / "selected-metadata.json"
        p.prepare(args, {"agentdef": "v0.2.0", "engine": "v0.2.0"}, self.env)
        data = json.loads(args.metadata.read_text())
        self.assertEqual(set(data["files"]), {"engine/go.mod", "engine/go.sum"})
        parsed = p.candidate.parse_mod(self.root, self.env, self.root / "engine/go.mod")
        self.assertEqual(p.candidate.requirements(parsed)[p.identity("agentdef")][0], "v0.2.0")

    def test_prepare_allows_ignored_test_outputs(self):
        self.write(".gitignore", "test-output/\n")
        self.write("engine/output_test.go", 'package fixture\nimport ("os"; "testing")\n'
                   'func TestOutput(t *testing.T) {\n'
                   ' if err := os.MkdirAll("../test-output", 0755); err != nil { t.Fatal(err) }\n'
                   ' if err := os.WriteFile("../test-output/result", []byte("output"), 0644); err != nil { t.Fatal(err) }\n'
                   '}\n')
        self.commit()
        args = self.args(module="engine")
        p.prepare(args, {"engine": "v0.2.0"}, self.env)
        self.assertTrue((self.root / "test-output/result").exists())
        self.assertEqual(json.loads(args.metadata.read_text())["files"], {})

    def test_prepare_retains_unselected_dependency_pin_and_no_change_artifact(self):
        self.require("adapters")
        self.commit()
        args = self.args(module="adapters")
        p.prepare(args, {"adapters": "v0.3.0"}, self.env)
        self.assertEqual(json.loads(args.metadata.read_text())["files"], {})

    def test_replacements_fail_in_any_publishable_module(self):
        self.require("adapters", replacement=True)
        self.commit()
        with self.assertRaisesRegex(p.CheckError, "replacements"):
            p.prepare(self.args(module="engine"), {"engine": "v0.2.0"}, self.env)
        self.assertFalse((self.base / "metadata.json").exists())

    def test_reconcile_all_consumers_and_unselected_publishable(self):
        affected = (".", "environment/microvm", "integration/microvm", "authn/oidc")
        for path in affected:
            self.require(path, replacement=path in p.CONSUMERS)
        # The integration module also retains local root/runtime dependencies.
        integration = self.root / "integration/microvm"
        p.candidate.go(integration, self.env, "mod", "edit",
                       f"-require={p.identity('.')}@v0.0.1",
                       f"-require={p.identity('environment/microvm')}@v0.0.1",
                       f"-replace={p.identity('.')}=../..",
                       f"-replace={p.identity('environment/microvm')}=../../environment/microvm")
        self.write("integration/microvm/fixture.go", 'package fixture\nimport (\n'
                   f'e "{p.identity("engine")}"\nr "{p.identity(".")}"\n'
                   f'm "{p.identity("environment/microvm")}"\n)\n'
                   'const Value = e.Value + r.Value + m.Value\n')
        p.candidate.go(integration, self.env, "mod", "tidy")
        self.commit()
        originals = p.inventory(self.root, self.env)
        args = self.args()
        p.reconcile(args, {"engine": "v0.2.0"}, self.env)
        data = json.loads(args.metadata.read_text())
        for path in affected:
            name = str(Path(path) / "go.mod")
            self.assertIn(name, data["files"])
            parsed = p.candidate.parse_mod(self.root / path, self.env, self.root / name)
            self.assertEqual(p.candidate.requirements(parsed)[p.identity("engine")][0], "v0.2.0")
            for key in ("Module", "Go", "Toolchain", "Replace"):
                self.assertEqual(parsed.get(key), originals[path][2].get(key))
        self.assertNotIn("engine/go.mod", data["files"])

    def test_reconcile_selected_module_with_selected_dependency(self):
        self.publish("agentdef", "v0.2.0")
        self.require("engine", "agentdef", "v0.2.0")
        self.commit()
        args = self.args()
        p.reconcile(args, {"agentdef": "v0.2.0", "engine": "v0.2.0"}, self.env)
        self.assertEqual(json.loads(args.metadata.read_text())["files"], {})

    def test_tidy_cannot_raise_or_remove_requested_pin(self):
        # A real public dependency raises engine through MVS, despite -require.
        self.publish("provider/ssefilter", "v0.2.0", f"require {p.identity('engine')} v0.2.0\n")
        self.require("provider/openai", "provider/ssefilter")
        mod = self.root / "provider/openai/go.mod"
        mod.write_text(mod.read_text() + f"\nrequire {p.identity('engine')} v0.1.0\n")
        self.write("provider/openai/fixture.go", 'package fixture\nimport (\n'
                   f'e "{p.identity("engine")}"\ns "{p.identity("provider/ssefilter")}"\n)\n'
                   'const Value = e.Value + s.Value\n')
        with self.assertRaisesRegex(p.CheckError, "exact pin"):
            p.tidy(self.root, (p.identity("provider/openai"), mod.parent,
                              p.candidate.parse_mod(mod.parent, self.env, mod)), p.ORDER + p.CONSUMERS,
                   {"engine": "v0.1.0", "provider/ssefilter": "v0.2.0"}, self.env, standalone=False)
        self.assertFalse((self.base / "metadata.json").exists())

    def test_tidy_cannot_implicitly_upgrade_unselected_sibling(self):
        self.publish("provider/ssefilter", "v0.2.0", f"require {p.identity('engine')} v0.2.0\n")
        self.require("provider/openai", "provider/ssefilter")
        mod = self.root / "provider/openai/go.mod"
        mod.write_text(mod.read_text() + f"\nrequire {p.identity('engine')} v0.1.0\n")
        self.write("provider/openai/fixture.go", 'package fixture\nimport (\n'
                   f'e "{p.identity("engine")}"\ns "{p.identity("provider/ssefilter")}"\n)\n'
                   'const Value = e.Value + s.Value\n')
        self.commit()
        with self.assertRaisesRegex(p.CheckError, "unselected sibling pin"):
            p.prepare(self.args(module="provider/openai"),
                      {"provider/openai": "v0.3.0", "provider/ssefilter": "v0.2.0"}, self.env)
        self.assertFalse((self.base / "metadata.json").exists())

    def test_metadata_identity_direct_dependencies_and_order(self):
        original = {"Module": {"Path": p.identity("adapters")}, "Go": "1.27.3", "Require": []}
        for key, value in (("Module", {"Path": "bad"}), ("Replace", [{"Old": {"Path": "bad"}}])):
            with self.subTest(key=key), self.assertRaisesRegex(p.CheckError, "directive"):
                p.check_metadata(original, dict(original, **{key: value}), p.ORDER, {})
        desired = dict(original, Require=[{"Path": p.identity("engine"), "Version": "v0.1.0"}])
        with self.assertRaisesRegex(p.CheckError, "missing new direct sibling"):
            p.check_metadata(original, desired, p.ORDER, {})
        for path in ("unknown", "adapters", "."):
            desired = dict(original, Require=[{"Path": p.identity(path), "Version": "v0.1.0", "Indirect": True}])
            with self.subTest(path=path), self.assertRaisesRegex(p.CheckError, "dependency/order"):
                p.check_metadata(original, desired, p.ORDER + p.CONSUMERS, {})

    def test_prepare_allows_new_indirect_unselected_sibling(self):
        self.publish("provider/ssefilter", "v0.2.0", f"require {p.identity('engine')} v0.2.0\n",
                     source=f'package fixture\nimport e "{p.identity("engine")}"\nconst Value = e.Value\n')
        self.require("provider/openai", "provider/ssefilter")
        self.commit()
        p.prepare(self.args(module="provider/openai"),
                  {"provider/ssefilter": "v0.2.0", "provider/openai": "v0.3.0"}, self.env)
        desired = p.candidate.parse_mod(self.root, self.env, self.root / "provider/openai/go.mod")
        self.assertEqual(p.candidate.requirements(desired)[p.identity("engine")], ("v0.2.0", True))

    def test_source_or_tested_metadata_mutation_fails(self):
        original_go = p.candidate.go
        for mutation in ("engine/fixture.go", "engine/go.sum", "generated.txt", "go.work"):
            with self.subTest(mutation=mutation):
                target = self.root / mutation
                before = p.candidate.read_optional(target)
                def tamper(directory, env, *args):
                    result = original_go(directory, env, *args)
                    if args[0] == "test":
                        target.write_text("changed\n")
                    return result
                with mock.patch.object(p.candidate, "go", side_effect=tamper), \
                        self.assertRaisesRegex(p.CheckError, "modified files"):
                    p.prepare(self.args(module="engine"), {"engine": "v0.2.0"}, self.env)
                self.assertFalse((self.base / "metadata.json").exists())
                if before is None:
                    target.unlink()
                else:
                    target.write_bytes(before)

    def test_git_checks_reject_index_worktree_deletions_modes_and_untracked(self):
        target = self.root / "engine/fixture.go"
        original = target.read_bytes()
        target.write_text("staged source change\n")
        self.command("git", "add", "engine/fixture.go")
        target.write_bytes(original)  # Clean against HEAD, but index is not clean.
        with self.assertRaisesRegex(p.CheckError, "modified files"):
            p.check_changes(self.root, p.metadata_paths())
        self.command("git", "reset", "HEAD", "--", "engine/fixture.go")
        target.unlink()
        with self.assertRaisesRegex(p.CheckError, "modified files"):
            p.check_changes(self.root, p.metadata_paths())
        target.write_bytes(original)
        target.chmod(0o755)
        with self.assertRaisesRegex(p.CheckError, "modified files"):
            p.check_changes(self.root, p.metadata_paths())
        target.chmod(0o644)
        self.write("unexpected\nfile", "untracked\n")
        with self.assertRaisesRegex(p.CheckError, "modified files"):
            p.check_changes(self.root, p.metadata_paths())

    def test_prepare_rejects_head_movement(self):
        original_go = p.candidate.go
        def move(directory, env, *args):
            result = original_go(directory, env, *args)
            if args[0] == "test":
                self.commit()
            return result
        with mock.patch.object(p.candidate, "go", side_effect=move), \
                self.assertRaisesRegex(p.CheckError, "moved HEAD"):
            p.prepare(self.args(module="engine"), {"engine": "v0.2.0"}, self.env)
        self.assertFalse((self.base / "metadata.json").exists())

    def test_source_mutation_during_tidy_rejected_before_artifact(self):
        original_go = p.candidate.go
        def tamper(directory, env, *args):
            result = original_go(directory, env, *args)
            if args == ("mod", "tidy"):
                self.write("engine/fixture.go", "package fixture\nconst Value = 2\n")
            return result
        with mock.patch.object(p.candidate, "go", side_effect=tamper), \
                self.assertRaisesRegex(p.CheckError, "modified files"):
            p.prepare(self.args(module="engine"), {"engine": "v0.2.0"}, self.env)
        self.assertFalse((self.base / "metadata.json").exists())

    def test_parent_identity_cleanliness_and_metadata_only_ancestry(self):
        with self.assertRaisesRegex(p.CheckError, "HEAD"):
            p.checkout(self.root, self.head, "b" * 40)
        self.write("engine/fixture.go", "package fixture\nconst Value = 2\n")
        with self.assertRaisesRegex(p.CheckError, "clean"):
            p.checkout(self.root, self.head, self.head)
        source = self.head
        self.commit()
        with self.assertRaisesRegex(p.CheckError, "outside known"):
            p.checkout(self.root, source, self.head)

    def test_blobless_checkout_preserves_prepare_and_source_integrity(self):
        self.write("obsolete.txt", "historical-only contents\n")
        self.commit()
        obsolete = self.command("git", "rev-parse", "HEAD:obsolete.txt")
        (self.root / "obsolete.txt").unlink()
        self.commit()
        source = self.head
        self.write("engine/go.mod", (self.root / "engine/go.mod").read_text() + "\n// release metadata\n")
        self.commit()
        parent = self.head
        self.command("git", "tag", "engine/v0.2.0")
        self.command("git", "config", "uploadpack.allowFilter", "true")
        self.command("git", "config", "uploadpack.allowAnySHA1InWant", "true")
        clone = self.base / "blobless"
        self.command("git", "clone", "--filter=blob:none", "--no-checkout", self.root.as_uri(), str(clone))
        self.root = clone
        self.command("git", "checkout", "--detach", parent)
        self.command("git", "fetch", "--filter=blob:none", "--force", "origin", "main", "--tags")
        self.assertEqual(self.command("git", "rev-parse", "--is-shallow-repository"), "false")
        self.assertEqual(self.command("git", "rev-parse", "engine/v0.2.0"), parent)
        self.assertIn("?" + obsolete, self.command("git", "rev-list", "--objects", "--all", "--missing=print"))
        p.checkout(clone, source, parent)
        args = self.args(module="engine")
        args.source, args.parent = source, parent
        p.prepare(args, {"engine": "v0.3.0"}, self.env)
        self.assertTrue(args.metadata.exists())
        self.assertIn("?" + obsolete, self.command("git", "rev-list", "--objects", "--all", "--missing=print"))
        self.write("engine/fixture.go", "package fixture\nconst Value = 2\n")
        self.command("git", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid",
                     "commit", "-am", "non-metadata change")
        with self.assertRaisesRegex(p.CheckError, "outside known"):
            p.checkout(clone, source, self.command("git", "rev-parse", "HEAD"))

    def test_prepare_uses_go_tidy_toolchain_output(self):
        original = f"module {p.identity('engine')}\n\ngo 1.27.0\n\ntoolchain default\n"
        manifest = self.write("engine/go.mod", original)
        p.candidate.go(manifest.parent, self.env, "mod", "tidy")
        expected = manifest.read_text()
        self.assertNotEqual(expected, original)
        manifest.write_text(original)
        self.commit()
        args = self.args(module="engine")
        p.prepare(args, {"engine": "v0.2.0"}, self.env)
        self.assertEqual(json.loads(args.metadata.read_text())["files"]["engine/go.mod"], expected)

    def test_prepare_real_test_fails_without_artifact(self):
        self.write("engine/fail_test.go", 'package fixture\nimport "testing"\n'
                   'func TestFail(t *testing.T) { t.Fatal("fixture failure") }\n')
        self.commit()
        with self.assertRaisesRegex(p.CheckError, "exit 1"):
            p.prepare(self.args(module="engine"), {"engine": "v0.2.0"}, self.env)
        self.assertFalse((self.base / "metadata.json").exists())

    def test_prove_real_go_and_git_internal_import_namespace(self):
        path, exact = "internal/adaptersupport", "v0.2.0"
        self.publish(path, exact)
        module = p.identity(path)
        archive = self.proxy / module / "@v" / f"{exact}.zip"
        with zipfile.ZipFile(archive, "w") as output:
            prefix = f"{module}@{exact}/"
            output.writestr(prefix + "go.mod", f"module {module}\n\ngo 1.27.3\n")
            for package in ("filewatch", "soulbody"):
                output.writestr(prefix + package + "/fixture.go", "package fixture\nconst Value = 1\n")
        self.command("git", "tag", f"{path}/{exact}")
        temporary = self.base / "proof"
        temporary.mkdir()
        args = argparse.Namespace(module=path, head=self.head)
        # Only the repository location differs from production; the download,
        # checksum-free offline fixture, Git tag resolution, tidy, and build run.
        with mock.patch.object(p, "REPOSITORY", str(self.root)):
            self.assertEqual(p.prove(args, {path: exact}, self.env, temporary), {"head": self.head})
        consumer = temporary / "consumer"
        mod = consumer / "go.mod"
        mod.write_text(mod.read_text().replace(p.PREFIX + "/publication-smoke", "example.com/not-allowed"))
        with self.assertRaisesRegex(p.CheckError, "exit 1"):
            p.candidate.go(consumer, self.env, "build", "-mod=readonly", "./...")

    def test_artifact_rejects_metadata_symlink_and_module_deletion(self):
        mod = self.root / "engine/go.mod"
        original = mod.read_bytes()
        mod.unlink()
        with self.assertRaisesRegex(p.CheckError, "deleted"):
            p.artifact(self.root, self.head, {"engine/go.mod": None}, self.base / "metadata.json")
        mod.symlink_to(self.root / "go.mod")
        with self.assertRaisesRegex(p.CheckError, "regular file"):
            p.artifact(self.root, self.head, {"engine/go.mod": None}, self.base / "metadata.json")
        mod.unlink()
        mod.write_bytes(original)

    def test_metadata_mutation_after_tests_cannot_enter_artifact(self):
        original = p.artifact
        def tamper(root, parent, captured, destination):
            mod = root / "engine/go.mod"
            mod.write_bytes(mod.read_bytes() + b"\n// not the tested bytes\n")
            return original(root, parent, captured, destination)
        with mock.patch.object(p, "artifact", side_effect=tamper), \
                self.assertRaisesRegex(p.CheckError, "tested metadata bytes changed"):
            p.prepare(self.args(module="engine"), {"engine": "v0.2.0"}, self.env)
        self.assertFalse((self.base / "metadata.json").exists())

    def test_metadata_is_captured_exactly_and_artifact_is_create_only(self):
        args = self.args(module="engine")
        args.metadata.write_text("do not overwrite\n")
        with self.assertRaises(FileExistsError):
            p.prepare(args, {"engine": "v0.2.0"}, self.env)
        self.assertEqual(args.metadata.read_text(), "do not overwrite\n")

    def test_sum_deletion_artifact(self):
        self.write("engine/go.sum", "unused sum\n")
        self.commit()
        (self.root / "engine/go.sum").unlink()
        args = self.args()
        p.artifact(self.root, self.head, {"engine/go.sum": None}, args.metadata)
        self.assertIsNone(json.loads(args.metadata.read_text())["files"]["engine/go.sum"])

    @unittest.skipUnless(os.environ.get("PUBLICATION_DIAGNOSTICS_WORKFLOW"), "workflow supplied by Go contract test")
    def test_preflight_workflow_boundaries(self):
        steps = json.loads(self.workflow)["preflight"]
        workspace = self.base / "workspace"
        workspace.mkdir()
        (workspace / "candidate").symlink_to(self.root, target_is_directory=True)
        (workspace / "tooling").symlink_to(SCRIPT.parents[2], target_is_directory=True)
        mode = self.base / "mode"
        task = self.base / "task"
        task.write_text(f"#!{sys.executable}\n" + f'''import json,os,pathlib,sys
assert sys.argv[1:] == ["test:workspace"]
assert "TOKEN" not in os.environ
root = pathlib.Path({str(self.base)!r})
(root / "workspace-env").write_text(json.dumps(dict(os.environ)))
mode = (root / "mode").read_text()
if mode == "workspace-mutation":
    pathlib.Path({str(self.root / 'fixture.go')!r}).write_text("changed")
print("fixture-workspace", flush=True)
if os.environ["GOFLAGS"] == "-v":
    print("fixture-verbose", flush=True)
sys.exit(7 if mode == "workspace-failure" else 0)
''')
        task.chmod(0o755)
        for scenario in ("normal", "verbose", "workspace-failure", "workspace-mutation"):
            with self.subTest(scenario=scenario):
                mode.write_text(scenario)
                self.write("fixture.go", "package fixture\nconst Value = 1\n")
                output = self.base / "step-output"
                output.write_text("")
                env = dict(self.env, PATH=f"{self.base}:{self.env['PATH']}", TOKEN="fixture-not-a-secret",
                           SOURCE=self.head, VERSIONS='{"engine":"v0.2.0"}',
                           VERBOSE="true" if scenario == "verbose" else "false",
                           GITHUB_RUN_ID="123", GITHUB_RUN_ATTEMPT="2", GITHUB_OUTPUT=str(output),
                           GITHUB_WORKSPACE=str(workspace), RUNNER_TEMP=str(self.base / "runner"))
                for index, step in enumerate(steps):
                    result = subprocess.run(["sh", "-ec", step["Run"]], cwd=workspace, env=env,
                                            capture_output=True, text=True, timeout=120)
                    if index < 2 or result.returncode:
                        self.assertEqual(output.read_text(), "", result.stdout + result.stderr)
                    if result.returncode:
                        break  # Native Actions' default success-only step sequencing.
                    if index == 1:
                        actual = json.loads((self.base / "workspace-env").read_text())
                        shared = p.environment(self.base / "runner/mp")
                        for key in ("HOME", "GOPATH", "GOCACHE", "GOMODCACHE", "TMPDIR"):
                            self.assertEqual(actual[key], shared[key])
                        self.assertEqual(actual["GOFLAGS"], "-v" if scenario == "verbose" else "")
                        self.assertIn("fixture-workspace", result.stdout)
                        self.assertEqual("fixture-verbose" in result.stdout, scenario == "verbose")
                if scenario in ("normal", "verbose"):
                    self.assertEqual(index, 2)
                    self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                    self.assertEqual(output.read_text(), "source=" + self.head +
                                     '\nversions={"engine":"v0.2.0"}\nbranch=module-publication/123-2\n')
                else:
                    self.assertNotEqual(result.returncode, 0)
                    self.assertEqual(index, 1 if scenario == "workspace-failure" else 2)

    def preflight(self, mutate=None, phases=("validate", "workspace", "final"), **env):
        original = p.run
        def expensive(command, directory, environment=None, **kwargs):
            if command[0] == "task":
                if mutate:
                    mutate()
                return ""
            return original(command, directory, environment, **kwargs)
        with mock.patch.object(p, "run", side_effect=expensive) as run, \
                mock.patch.dict(os.environ, {"GITHUB_RUN_ID": "123", "GITHUB_RUN_ATTEMPT": "2", **env}):
            args = self.args(phase="validate", verbose="false")
            for args.phase in phases:
                result = p.preflight(args, {"engine": "v0.2.0"}, self.env)
        return result, run

    def test_preflight_validates_and_tests_workspace_once(self):
        result, run = self.preflight()
        self.assertEqual(result["branch"], "module-publication/123-2")
        calls = [call.args[0] for call in run.call_args_list]
        self.assertEqual(calls.count(["task", "test:workspace"]), 1)
        candidates = [command for command in calls if command[0] == p.sys.executable]
        self.assertEqual(candidates, [])
        expensive = [call for call in run.call_args_list if call.args[0][0] == "task"]
        self.assertTrue(all(not call.kwargs["capture"] and call.kwargs["timeout"] is None for call in expensive))
        self.assertEqual(expensive[0].args[2]["GOFLAGS"], "")
        self.assertEqual(self.env["GOFLAGS"], "")
        self.assertEqual(result["source"], self.head)

    def test_preflight_workspace_sums_are_ephemeral_but_source_changes_fail(self):
        self.write(".gitignore", "go.work.sum\n")
        self.commit()
        self.preflight(mutate=lambda: self.write("go.work.sum", "workspace checksum output\n"))
        (self.root / "go.work.sum").unlink()
        with self.assertRaisesRegex(p.CheckError, "clean"):
            self.preflight(mutate=lambda: self.write("engine/fixture.go", "changed\n"),
                           phases=("workspace", "final"))

    def test_preflight_rejects_clean_head_movement(self):
        source = self.head
        with self.assertRaisesRegex(p.CheckError, "HEAD"):
            self.preflight(mutate=lambda: self.command("git", "commit", "--allow-empty", "-m", "moved"),
                           phases=("workspace", "final"))
        self.assertNotEqual(self.command("git", "rev-parse", "HEAD"), source)
        self.assertEqual(self.command("git", "status", "--porcelain"), "")

    def test_preflight_rejects_tag_and_unvalidated_run_identity(self):
        for value in ("", "1\nbranch=bad", "-1", "refs/x"):
            with self.subTest(value=value), self.assertRaisesRegex(p.CheckError, "positive GITHUB"):
                self.preflight(GITHUB_RUN_ID=value)
        self.command("git", "tag", "engine/v0.2.0")
        for phase in ("validate", "final"):
            with self.subTest(phase=phase), self.assertRaisesRegex(p.CheckError, "tag already exists"):
                self.preflight(phases=(phase,))

    def test_preflight_fetches_main_advance_between_phases(self):
        remote = self.base / "remote.git"
        self.command("git", "clone", "--bare", str(self.root), str(remote))
        self.command("git", "remote", "set-url", "origin", str(remote))
        self.preflight(phases=("validate",))
        advanced = self.command("git", "--git-dir", str(remote),
                                "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid",
                                "commit-tree", self.head + "^{tree}", "-p", self.head, "-m", "advance main")
        self.command("git", "--git-dir", str(remote), "update-ref", "refs/heads/main", advanced)
        with self.assertRaises(p.CheckError):
            self.command("git", "cat-file", "-e", advanced)
        result, _ = self.preflight(phases=("final",))
        self.assertEqual(result["source"], self.head)
        self.assertEqual(self.command("git", "rev-parse", "FETCH_HEAD"), advanced)
        self.command("git", "remote", "set-url", "origin", str(self.base / "missing.git"))
        with self.assertRaisesRegex(p.CheckError, "git fetch"):
            self.preflight(phases=("final",))

    def test_preflight_uses_remote_main_not_stale_tracking_ref(self):
        # Local main is moved to a different history, while origin/main still claims
        # the selected commit is main. A fresh fetch must defeat that stale claim.
        self.command("git", "update-ref", "refs/remotes/origin/main", self.head)
        self.command("git", "checkout", "--orphan", "other")
        self.write("other-history.txt", "not the selected source\n")
        self.commit()
        self.command("git", "branch", "-f", "main", self.head)
        old = self.command("git", "rev-parse", "refs/remotes/origin/main")
        self.command("git", "checkout", "--detach", old)
        self.head = old
        for phase in ("validate", "final"):
            with self.subTest(phase=phase), self.assertRaises(p.CheckError):
                self.preflight(phases=(phase,))


class ProofTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="proof-tests-")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.env = p.environment(self.root)
        self.head = "a" * 40
        self.args = argparse.Namespace(module="internal/adaptersupport", head=self.head)
        self.module = p.identity(self.args.module)
        self.download = {"Path": self.module, "Version": "v0.2.0", "GoMod": str(self.root / "download.mod"),
                         "Origin": {"VCS": "git", "URL": p.REPOSITORY, "Hash": self.head,
                                    "Ref": "refs/tags/internal/adaptersupport/v0.2.0"}}

    def fake_go(self, directory, env, *args):
        self.assertIs(env, self.env)
        if args[0:3] == ("list", "-m", "-json"):
            return json.dumps({"Path": self.module, "Version": "v0.2.0"})
        return ""

    def test_retry_owned_internal_consumer_and_success_only_head(self):
        with mock.patch.object(p, "run", side_effect=[p.CheckError("not propagated"), json.dumps(self.download)]) as run, \
                mock.patch.object(p.time, "sleep") as sleep, \
                mock.patch.object(p.candidate, "parse_mod", return_value={"Module": {"Path": self.module}}), \
                mock.patch.object(p.candidate, "go", side_effect=self.fake_go) as go:
            result = p.prove(self.args, {self.args.module: "v0.2.0"}, self.env, self.root)
        self.assertEqual(result, {"head": self.head})
        self.assertEqual(run.call_count, 2)
        sleep.assert_called_once_with(10)
        self.assertIn(f"module {p.PREFIX}/publication-smoke", (self.root / "consumer/go.mod").read_text())
        smoke = (self.root / "consumer/smoke.go").read_text()
        self.assertIn(self.module + "/filewatch", smoke)
        self.assertIn(self.module + "/soulbody", smoke)
        self.assertIn(mock.call(self.root / "consumer", self.env, "build", "-mod=readonly", "./..."), go.call_args_list)

    def test_retry_bound(self):
        with mock.patch.object(p, "run", side_effect=p.CheckError("not propagated")) as run, \
                mock.patch.object(p.time, "sleep") as sleep, \
                self.assertRaisesRegex(p.CheckError, "not propagated"):
            p.prove(self.args, {self.args.module: "v0.2.0"}, self.env, self.root)
        self.assertEqual(run.call_count, 6)
        self.assertEqual(sleep.call_count, 5)
        self.assertTrue(all(call.kwargs["timeout"] == 120 for call in run.call_args_list))

    def test_wrong_identity_commit_and_consumer_upgrade(self):
        for key, wrong in (("Path", "bad"), ("Version", "v0.3.0")):
            with self.subTest(key=key), self.assertRaisesRegex(p.CheckError, "wrong module/version"):
                p.tag_identity(self.module, "v0.2.0", self.head,
                               dict(self.download, **{key: wrong}), self.root, self.env)
        with self.assertRaisesRegex(p.CheckError, "expected head"):
            p.tag_identity(self.module, "v0.2.0", "b" * 40, self.download, self.root, self.env)
        def raised_pin(directory, env, *args):
            if args[0] == "list":
                return json.dumps({"Path": self.module, "Version": "v0.3.0"})
            return ""
        with mock.patch.object(p, "run", return_value=json.dumps(self.download)), \
                mock.patch.object(p.candidate, "parse_mod", return_value={"Module": {"Path": self.module}}), \
                mock.patch.object(p.candidate, "go", side_effect=raised_pin), \
                self.assertRaisesRegex(p.CheckError, "exact public pin"):
            p.prove(self.args, {self.args.module: "v0.2.0"}, self.env, self.root)

    def test_tag_fallback_is_read_only_and_annotated_tag_uses_peeled_commit(self):
        downloaded = {key: value for key, value in self.download.items() if key != "Origin"}
        tag = "refs/tags/internal/adaptersupport/v0.2.0"
        with mock.patch.object(p, "run", return_value=f"{'b' * 40}\t{tag}\n{self.head}\t{tag}^{{}}\n") as run:
            p.tag_identity(self.module, "v0.2.0", self.head, downloaded, self.root, self.env)
        self.assertEqual(run.call_args.args[0], ["git", "ls-remote", "--exit-code", p.REPOSITORY, tag, tag + "^{}"])
        self.assertIs(run.call_args.args[2], self.env)


if __name__ == "__main__":
    unittest.main()
