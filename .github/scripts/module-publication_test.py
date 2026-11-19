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
import signal
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
    def test_streams_before_completion_and_keeps_stdout_parseable(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            gate = root / "finish"
            child = ("import pathlib,sys,time; print('{\"answer\":42}', flush=True); "
                     "print('child-ready', file=sys.stderr, flush=True); "
                     f"gate=pathlib.Path({str(gate)!r})\n"
                     "while not gate.exists(): time.sleep(0.01)\n")
            # Both buffering layers are real: publication.run -> candidate.run -> child.
            inner = (f"import runpy,signal; p=runpy.run_path({str(SCRIPT)!r}); "
                     "signal.signal(signal.SIGTERM,p['candidate'].interrupted); "
                     f"print(p['run']({[sys.executable, '-c', child]!r}, {temporary!r}, "
                     "stream=True, timeout=10), end='')")
            outer = (f"import runpy; p=runpy.run_path({str(SCRIPT)!r}); "
                     f"print(p['run']({[sys.executable, '-c', inner]!r}, {temporary!r}, "
                     "stream=True, timeout=15), end='')")
            with subprocess.Popen([sys.executable, "-c", outer], stdout=subprocess.PIPE,
                                  stderr=subprocess.PIPE) as process:
                try:
                    observed = b""
                    with selectors.DefaultSelector() as selector:
                        selector.register(process.stderr, selectors.EVENT_READ)
                        deadline = time.monotonic() + 10
                        # Use a complete line so the phase's printed argv cannot satisfy this.
                        while b"\nchild-ready\n" not in observed:
                            self.assertLess(time.monotonic(), deadline, observed)
                            for key, _ in selector.select(0.1):
                                data = os.read(key.fileobj.fileno(), 8192)
                                self.assertTrue(data, observed)
                                observed += data
                    self.assertIsNone(process.poll())
                    self.assertFalse(gate.exists())
                    gate.touch()
                    stdout, stderr = process.communicate(timeout=10)
                    self.assertEqual(process.returncode, 0, stderr)
                    self.assertEqual(json.loads(stdout), {"answer": 42})
                    self.assertIn(b"END ", observed + stderr)
                finally:
                    gate.touch()
                    if process.poll() is None:
                        process.terminate()
                        process.communicate(timeout=20)

    def test_stream_failure_and_timeout_reap_descendants(self):
        for mode in ("failure", "timeout", "nested-timeout"):
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                pidfile = root / "pid"
                # The descendant closes its pipes, outlives its leader and ignores TERM.
                descendant = ("import os,pathlib,signal,time; "
                              "signal.signal(signal.SIGTERM,signal.SIG_IGN); "
                              f"pathlib.Path({str(pidfile)!r}).write_text(str(os.getpid())); "
                              "time.sleep(30)")
                child = ("import pathlib,signal,subprocess,sys,time; "
                         f"subprocess.Popen({[sys.executable, '-c', descendant]!r}, "
                         "stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL); "
                         f"pidfile=pathlib.Path({str(pidfile)!r})\n"
                         "while not pidfile.exists(): time.sleep(0.01)\n"
                         + ("sys.exit(7)" if mode == "failure" else
                            "signal.signal(signal.SIGTERM,signal.SIG_IGN); time.sleep(30)"
                            if mode == "nested-timeout" else "time.sleep(30)"))
                command = [sys.executable, "-c", child]
                if mode == "nested-timeout":
                    inner = (f"import runpy,signal; p=runpy.run_path({str(SCRIPT)!r}); "
                             "signal.signal(signal.SIGTERM,p['candidate'].interrupted); "
                             f"p['run']({command!r}, {temporary!r}, stream=True, timeout=20, cleanup_timeout=1)")
                    command = [sys.executable, "-c", inner]
                with contextlib.redirect_stderr(io.StringIO()) as output:
                    with self.assertRaisesRegex(p.CheckError, "exited 7" if mode == "failure" else "timed out"):
                        p.run(command, root, timeout=1, stream=True)
                self.assertIn("FAIL", output.getvalue())
                self.assertTrue(pidfile.exists(), "fixture did not reach descendant boundary")
                pid = int(pidfile.read_text())
                # A killed orphan can remain a zombie until the host reaps it.
                deadline = time.monotonic() + 2
                while True:
                    state = subprocess.run(["ps", "-p", str(pid), "-o", "stat="],
                                           capture_output=True, text=True, timeout=5)
                    if state.returncode or state.stdout.lstrip().startswith("Z"):
                        break
                    self.assertLess(time.monotonic(), deadline, f"live descendant {pid}")
                    time.sleep(0.01)

    def test_sigterm_allows_nested_runner_to_reap_detached_leaf(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            pidfile = root / "leaf-pid"
            inner_pidfile = root / "inner-pid"
            (root / "go.mod").write_text("module fixture.invalid/root\n\ngo 1.23.0\n")
            go = root / "go"
            go.write_text(f"#!{sys.executable}\n" + '''import json,os,pathlib,signal,sys,time
if sys.argv[1] == "test":
    assert "-timeout=20m" in sys.argv
    signal.signal(signal.SIGTERM,signal.SIG_IGN)
    pathlib.Path("leaf-pid").write_text(str(os.getpid()))
    time.sleep(30)
elif "-json" in sys.argv:
    print(json.dumps({"Module":{"Path":"fixture.invalid/root"},"Go":"1.23.0"}))
''')
            go.chmod(0o755)
            bootstrap = root / "cli-fixture.py"
            # Mock metadata/network seams at main entry, after the real CLI installs its handler.
            bootstrap.write_text('''import os,pathlib,runpy,sys
script = pathlib.Path(sys.argv[1])
root = pathlib.Path.cwd()
def before_main(frame, event, arg):
    if event == "call" and frame.f_code.co_name == "main" and frame.f_code.co_filename == str(script):
        ns = frame.f_globals
        if script.name == "module-candidate.py":
            (root / "inner-pid").write_text(str(os.getpid()))
            original = {"Module":{"Path":"fixture.invalid/root"},"Go":"1.23.0"}
            ns["inventory"] = lambda *_: ({"fixture.invalid/root": (root, original)},
                {root / "go.mod": (root / "go.mod").read_bytes(), root / "go.sum": None})
        else:
            command = [sys.executable, __file__, str(script.with_name("module-candidate.py")),
                       "--root", str(root), "--diagnostics"]
            ns["preflight"] = lambda args, versions, env: ns["run"](command, root, env, stream=True)
        sys.settrace(None)
sys.settrace(before_main)
sys.argv = sys.argv[1:]
runpy.run_path(str(script), run_name="__main__")
''')
            command = [sys.executable, str(bootstrap), str(SCRIPT), "preflight", "--root", str(root),
                       "--source", "a" * 40, "--versions", '{"engine":"v0.2.0"}']
            with subprocess.Popen(command, cwd=root, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                  env=dict(os.environ, PATH=f"{root}:{os.environ['PATH']}")) as process:
                try:
                    deadline = time.monotonic() + 10
                    while not pidfile.exists():
                        self.assertIsNone(process.poll())
                        self.assertLess(time.monotonic(), deadline, "leaf did not start")
                        time.sleep(0.01)
                    self.assertNotEqual(os.getpgid(int(pidfile.read_text())),
                                        os.getpgid(int(inner_pidfile.read_text())))
                    start = time.monotonic()
                    process.terminate()
                    process.wait(timeout=8)
                    self.assertEqual(process.returncode, 128 + signal.SIGTERM)
                    self.assertLess(time.monotonic() - start, 5)
                    pid = int(pidfile.read_text())
                    deadline = time.monotonic() + 2
                    while True:
                        state = subprocess.run(["ps", "-p", str(pid), "-o", "stat="],
                                               capture_output=True, text=True, timeout=5)
                        if state.returncode or state.stdout.lstrip().startswith("Z"):
                            break
                        self.assertLess(time.monotonic(), deadline, f"live detached leaf {pid}")
                        time.sleep(0.01)
                finally:
                    if process.poll() is None:
                        process.kill()
                    for path in (inner_pidfile, pidfile):
                        if path.exists():
                            try:
                                os.kill(int(path.read_text()), signal.SIGKILL)
                            except ProcessLookupError:
                                pass
                    process.communicate(timeout=10)

    def test_go_build_streams_before_completion(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            gate = root / "finish"
            go = root / "go"
            go.write_text(f"#!{sys.executable}\n" + '''import pathlib,sys,time
assert sys.argv[1:] == ["build", "-mod=readonly", "./..."]
print("build-live", flush=True)
while not pathlib.Path("finish").exists(): time.sleep(0.01)
''')
            go.chmod(0o755)
            code = (f"import os,runpy; p=runpy.run_path({str(SCRIPT)!r}); "
                    "print(p['candidate'].go(os.getcwd(), dict(os.environ, "
                    "MECATL_MODULE_DIAGNOSTICS='1'), 'build', '-mod=readonly', './...'), end='')")
            with subprocess.Popen([sys.executable, "-c", code], cwd=root, stdout=subprocess.PIPE,
                                  stderr=subprocess.PIPE,
                                  env=dict(os.environ, PATH=f"{root}:{os.environ['PATH']}")) as process:
                try:
                    observed = b""
                    with selectors.DefaultSelector() as selector:
                        selector.register(process.stderr, selectors.EVENT_READ)
                        deadline = time.monotonic() + 5
                        while b"\nbuild-live\n" not in observed:
                            self.assertLess(time.monotonic(), deadline, observed)
                            for key, _ in selector.select(0.1):
                                data = os.read(key.fileobj.fileno(), 8192)
                                self.assertTrue(data, observed)
                                observed += data
                    self.assertIsNone(process.poll())
                    self.assertFalse(gate.exists())
                    gate.touch()
                    stdout, stderr = process.communicate(timeout=10)
                    self.assertEqual(process.returncode, 0, stderr)
                    self.assertEqual(stdout, b"build-live\n")
                finally:
                    gate.touch()
                    process.communicate(timeout=10)

    def test_publication_timeout_defaults_and_prepare_package_bound(self):
        with mock.patch.object(p.candidate.subprocess, "run") as command:
            command.return_value = subprocess.CompletedProcess(["fixture"], 0, "", "")
            p.run(["fixture"], SCRIPT.parent)
            self.assertEqual(command.call_args.kwargs["timeout"], 1800)
        env = {"MECATL_MODULE_DIAGNOSTICS": "1"}
        with mock.patch.object(p.candidate, "run", return_value="") as command:
            for verb in ("test", "build"):
                p.candidate.go(SCRIPT.parent, env, verb, "./...")
                self.assertEqual(command.call_args.kwargs["timeout"], 1800)
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "go.mod").write_text("module fixture.invalid/root\n\ngo 1.23.0\n")
            original = {"Module": {"Path": "fixture.invalid/root"}, "Go": "1.23.0"}
            with mock.patch.object(p.candidate, "parse_mod", return_value=original), \
                    mock.patch.object(p.candidate, "go") as go:
                p.tidy(root, ("fixture.invalid/root", root, original), {}, {}, env, standalone=True)
            self.assertIn(mock.call(root, env, "test", "-mod=readonly", "-count=1",
                                    "-timeout=20m", "./..."), go.call_args_list)

    def test_machine_output_stays_quiet_even_with_diagnostics(self):
        command = [sys.executable, "-c", "import sys; print('{\"ok\":true}'); print('detail',file=sys.stderr)"]
        for stream in (False, True):
            with self.subTest(stream=stream), contextlib.redirect_stderr(io.StringIO()) as diagnostics:
                self.assertEqual(json.loads(p.run(command, SCRIPT.parent, stream=stream)), {"ok": True})
                self.assertEqual(bool(diagnostics.getvalue()), stream)
        with tempfile.TemporaryDirectory() as temporary:
            env = dict(os.environ, GOWORK="off", GOENV="off", GOFLAGS="",
                       GOTOOLCHAIN="local", MECATL_MODULE_DIAGNOSTICS="1")
            root = Path(temporary)
            (root / "go.mod").write_text("module fixture.invalid/metadata\n\ngo 1.23.0\n")
            with contextlib.redirect_stderr(io.StringIO()) as diagnostics:
                parsed = p.candidate.parse_mod(root, env, root / "go.mod")
            self.assertEqual(parsed["Module"]["Path"], "fixture.invalid/metadata")
            self.assertEqual(diagnostics.getvalue(), "")


@unittest.skipUnless(os.environ.get("PUBLICATION_DIAGNOSTICS_WORKFLOW"), "workflow supplied by Go contract test")
class DiagnosticsWorkflowTests(unittest.TestCase):
    def test_logged_preflight_preserves_exit_and_stops_sampler(self):
        step = json.loads(os.environ["PUBLICATION_DIAGNOSTICS_WORKFLOW"])["preflight"]
        for status in (0, 7):
            with self.subTest(status=status), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                bin_dir = root / "bin"
                bin_dir.mkdir()
                scripts = {
                    "python3": '''import os,pathlib,sys,time
assert sys.argv[1:] == ["tooling/.github/scripts/module-publication.py", "preflight", "--source", "reviewed-source", "--versions", '{"engine":"v0.2.0"}', "--root", os.environ["GITHUB_WORKSPACE"] + "/candidate"]
deadline = time.monotonic() + 10
while not pathlib.Path(os.environ["RUNNER_TEMP"], "sleeper").exists():
    assert time.monotonic() < deadline
    time.sleep(0.01)
print("fixture-preflight", flush=True)
print("fixture-stderr", file=sys.stderr, flush=True)
sys.exit(int(os.environ["FIXTURE_EXIT"]))
''',
                    "sleep": '''import os,pathlib
pathlib.Path(os.environ["RUNNER_TEMP"], "sleeper").write_text(str(os.getpid()))
os.execl("/bin/sleep", "sleep", "60")
''',
                    "ps": '''import sys
assert sys.argv[1:] == ["-eo", "pid,ppid,comm,pcpu,pmem,rss,etime"]
print("PID PPID COMMAND %CPU %MEM RSS ELAPSED")
print("123 1 go 12.3 1.0 1000 01:00")
print("124 1 fixture.test 1.0 0.1 100 01:00")
print("125 1 microvmmanager. 0.0 0.1 100 01:00")
''',
                }
                for name, content in scripts.items():
                    path = bin_dir / name
                    path.write_text(f"#!{sys.executable}\n" + content)
                    path.chmod(0o755)
                result = subprocess.run(["bash", "-c", step["Run"]], cwd=root,
                    env=dict(os.environ, PATH=f"{bin_dir}:{os.environ['PATH']}",
                             RUNNER_TEMP=str(root), GITHUB_WORKSPACE=str(root),
                             SOURCE="reviewed-source", VERSIONS='{"engine":"v0.2.0"}',
                             FIXTURE_EXIT=str(status)), capture_output=True, text=True, timeout=20)
                self.assertEqual(result.returncode, status, result.stdout + result.stderr)
                logs = root / "module-publication-diagnostics"
                log = (logs / "preflight.log").read_text()
                self.assertIn("fixture-preflight", log)
                self.assertIn("fixture-stderr", log)
                self.assertIn("fixture-preflight", result.stdout)
                resources = (logs / "resources.log").read_text()
                self.assertIn("123 1 go", resources)
                self.assertIn("124 1 fixture.test", resources)
                self.assertIn("125 1 microvmmanager.", resources)
                self.assertNotIn("Traceback", resources)
                pid = int((root / "sleeper").read_text())
                with self.assertRaises(ProcessLookupError):
                    os.kill(pid, 0)

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
                    self.assertEqual(p.main(["preflight", "--source", "a" * 40,
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

    def publish(self, path, exact, requires=""):
        module = p.identity(path)
        destination = self.proxy / module / "@v"
        destination.mkdir(parents=True, exist_ok=True)
        mod = f"module {module}\n\ngo 1.27.3\n{requires}"
        (destination / f"{exact}.mod").write_text(mod)
        (destination / f"{exact}.info").write_text(json.dumps({"Version": exact, "Time": "2026-01-01T00:00:00Z"}))
        (destination / "list").write_text("\n".join(sorted(p.stem for p in destination.glob("*.mod"))) + "\n")
        with zipfile.ZipFile(destination / f"{exact}.zip", "w") as archive:
            archive.writestr(f"{module}@{exact}/go.mod", mod)
            archive.writestr(f"{module}@{exact}/fixture.go", "package fixture\nconst Value = 1\n")

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

    def test_inventory_and_drift(self):
        self.assertEqual(set(p.inventory(self.root, self.env)), set(p.ORDER + p.CONSUMERS))
        self.write("unknown/go.mod", "module fixture.invalid/unknown\n\ngo 1.27.3\n")
        p.candidate.go(self.root, dict(self.env, GOWORK=str(self.root / "go.work")), "work", "use", "./unknown")
        with self.assertRaisesRegex(p.CheckError, "inventory drift"):
            p.inventory(self.root, self.env)

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

    def test_directive_changes_and_missing_sibling_rejected(self):
        original = {"Module": {"Path": p.identity("adapters")}, "Go": "1.27.3",
                    "Toolchain": "go1.27.4", "Require": []}
        for key, value in (("Go", "1.28"), ("Toolchain", None),
                           ("Module", {"Path": "bad"}), ("Replace", [{"Old": {"Path": "bad"}}])):
            desired = dict(original, **{key: value})
            with self.subTest(key=key), self.assertRaisesRegex(p.CheckError, "directive"):
                p.check_metadata(original, desired, p.ORDER, {})
        desired = dict(original, Require=[{"Path": p.identity("engine"), "Version": "v0.1.0"}])
        with self.assertRaisesRegex(p.CheckError, "missing new sibling"):
            p.check_metadata(original, desired, p.ORDER, {})

    def test_real_tidy_missing_sibling_rejected(self):
        # Dependency transitively requires a workspace sibling absent from reviewed metadata.
        self.publish("provider/ssefilter", "v0.2.0", f"require {p.identity('engine')} v0.2.0\n")
        # Make the proxy dependency import engine so tidy adds its indirect requirement.
        archive = self.proxy / p.identity("provider/ssefilter") / "@v/v0.2.0.zip"
        with zipfile.ZipFile(archive, "w") as output:
            prefix = p.identity("provider/ssefilter") + "@v0.2.0/"
            output.writestr(prefix + "go.mod", f"module {p.identity('provider/ssefilter')}\n\ngo 1.27.3\n"
                            f"require {p.identity('engine')} v0.2.0\n")
            output.writestr(prefix + "fixture.go", f'package fixture\nimport e "{p.identity("engine")}"\nconst Value = e.Value\n')
        self.require("provider/openai", "provider/ssefilter")
        self.commit()
        with self.assertRaisesRegex(p.CheckError, "missing new sibling"):
            p.prepare(self.args(module="provider/openai"),
                      {"provider/ssefilter": "v0.2.0", "provider/openai": "v0.3.0"}, self.env)

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

    def test_prepare_preserves_reviewed_toolchain_directives_with_real_go(self):
        self.write("engine/go.mod", f"module {p.identity('engine')}\n\ngo 1.27.0\n\ntoolchain go1.27.3\n")
        self.commit()
        args = self.args(module="engine")
        p.prepare(args, {"engine": "v0.2.0"}, self.env)
        self.assertEqual(json.loads(args.metadata.read_text())["files"], {})

    def test_prepare_real_test_fails_without_artifact(self):
        self.write("engine/fail_test.go", 'package fixture\nimport "testing"\n'
                   'func TestFail(t *testing.T) { t.Fatal("fixture failure") }\n')
        self.commit()
        with self.assertRaisesRegex(p.CheckError, "fixture failure"):
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
        with self.assertRaisesRegex(p.CheckError, "internal package.*not allowed"):
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

    def preflight(self, mutate=None, **env):
        original = p.run
        def expensive(command, directory, environment=None, **kwargs):
            if command[0] in ("task", p.sys.executable):
                if mutate and command[0] == "task":
                    mutate()
                return ""
            return original(command, directory, environment, **kwargs)
        with mock.patch.object(p, "run", side_effect=expensive) as run, \
                mock.patch.dict(os.environ, {"GITHUB_RUN_ID": "123", "GITHUB_RUN_ATTEMPT": "2", **env}):
            result = p.preflight(self.args(), {"engine": "v0.2.0"}, self.env)
        return result, run

    def test_preflight_actual_git_and_trusted_candidate_command(self):
        result, run = self.preflight()
        self.assertEqual(result["branch"], "module-publication/123-2")
        calls = [call.args[0] for call in run.call_args_list]
        self.assertEqual(calls.count(["task", "test:workspace"]), 1)
        candidates = [command for command in calls if command[0] == p.sys.executable]
        self.assertEqual(len(candidates), 1)
        self.assertEqual(candidates[0][1], str(SCRIPT.with_name("module-candidate.py")))
        self.assertIn("--diagnostics", candidates[0])
        self.assertEqual(candidates[0][-2:], ["--module", "engine"])
        expensive = [call for call in run.call_args_list if call.args[0][0] in ("task", sys.executable)]
        self.assertTrue(all(call.kwargs["stream"] for call in expensive))
        self.assertEqual(expensive[0].args[2]["GOFLAGS"], "-v")
        self.assertEqual(self.env["GOFLAGS"], "")
        self.assertEqual(result["source"], self.head)

    def test_preflight_workspace_sums_are_ephemeral_but_source_changes_fail(self):
        self.write(".gitignore", "go.work.sum\n")
        self.commit()
        self.preflight(mutate=lambda: self.write("go.work.sum", "workspace checksum output\n"))
        (self.root / "go.work.sum").unlink()
        with self.assertRaisesRegex(p.CheckError, "modified files"):
            self.preflight(mutate=lambda: self.write("engine/fixture.go", "changed\n"))

    def test_preflight_rejects_clean_head_movement(self):
        source = self.head
        with self.assertRaisesRegex(p.CheckError, "moved HEAD"):
            self.preflight(mutate=lambda: self.command("git", "commit", "--allow-empty", "-m", "moved"))
        self.assertNotEqual(self.command("git", "rev-parse", "HEAD"), source)
        self.assertEqual(self.command("git", "status", "--porcelain"), "")

    def test_preflight_rejects_tag_and_unvalidated_run_identity(self):
        for value in ("", "1\nbranch=bad", "-1", "refs/x"):
            with self.subTest(value=value), self.assertRaisesRegex(p.CheckError, "positive GITHUB"):
                self.preflight(GITHUB_RUN_ID=value)
        self.command("git", "tag", "engine/v0.2.0")
        with self.assertRaisesRegex(p.CheckError, "tag already exists"):
            self.preflight()

    def test_preflight_uses_remote_main_not_stale_tracking_ref(self):
        # Local main is moved to a different history, while origin/main still claims
        # the selected commit is main. ls-remote must defeat that stale claim.
        self.command("git", "update-ref", "refs/remotes/origin/main", self.head)
        self.command("git", "checkout", "--orphan", "other")
        self.write("other-history.txt", "not the selected source\n")
        self.commit()
        self.command("git", "branch", "-f", "main", self.head)
        old = self.command("git", "rev-parse", "refs/remotes/origin/main")
        self.command("git", "checkout", "--detach", old)
        self.head = old
        with self.assertRaises(p.CheckError):
            self.preflight()


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
