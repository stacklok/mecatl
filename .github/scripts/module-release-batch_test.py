#!/usr/bin/env python3
"""Offline batch algorithm tests: real Git/Go, no production authority or network."""

from contextlib import contextmanager
import importlib.util
import http.server
import threading
import zipfile
import json
import os
from pathlib import Path
import shutil
import sys
import tempfile
import unittest
from unittest import mock

HERE = Path(__file__).resolve().parent
sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location("artifact_fixtures", HERE / "module-artifact_test.py")
artifacts = importlib.util.module_from_spec(spec)
spec.loader.exec_module(artifacts)
fixtures = artifacts.fixtures
release = fixtures.release
NS, DEP, APP = fixtures.NS, fixtures.DEP, fixtures.APP
BATCH = {APP: "v0.2.0", DEP: "v0.2.0"}
SECRETS = ("GH_TOKEN", "GITHUB_TOKEN", "RELEASE_APP_PRIVATE_KEY", "RELEASE_SETTINGS_TOKEN", "UNKNOWN_CONTROLLER_SECRET")


class BatchTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        artifacts.ArtifactTests.setUpClass.__func__(cls)

    @classmethod
    def tearDownClass(cls):
        cls.tools.cleanup()

    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="module-batch-test-")
        self.addCleanup(self.temporary.cleanup)
        self.temp = Path(self.temporary.name)
        patch = mock.patch.dict(os.environ, fixtures.fixture_environment(self.temp), clear=True)
        patch.start()
        self.addCleanup(patch.stop)
        self.f = fixtures.Fixture(self.temp)
        self.counter = 0

    def fresh(self):
        self.counter += 1
        root = self.temp / ("execution-" + str(self.counter))
        root.mkdir()
        return root, dict(self.f.env, GOMODCACHE=str(root / "cache"), GOCACHE=str(self.temp / "build"))

    @contextmanager
    def open(self, sha, authority=None):
        with release.source_tree(self.f.root, sha) as source:
            yield release.Release(self.f.root, source, sha, self.f.policy, self.f.env, authority)

    def candidate(self, hostile=False):
        f = self.f
        old = f.commit()
        f.publish(DEP, "v0.1.0", old)
        source = 'package ssefilter\nfunc New() {}\nfunc NewAPI() string { return "new API" }\n'
        if hostile:
            # init runs both in standalone tests AND in the downloaded consumer.
            source = '''package ssefilter
import "os"
func New() {}
func NewAPI() string { return "new API" }
func init() {
 for _, key := range []string{"GH_TOKEN", "GITHUB_TOKEN", "RELEASE_APP_PRIVATE_KEY", "RELEASE_SETTINGS_TOKEN", "UNKNOWN_CONTROLLER_SECRET"} {
  if os.Getenv(key) != "" { panic("credential leaked: " + key) }
 }
}
'''
            f.write(DEP + "/hostile_test.go", '''package ssefilter
import "testing"
func TestCandidate(t *testing.T) { NewAPI() }
''')
        f.write(DEP + "/fixture.go", source)
        f.add_module(APP, f'package openai\nimport dep "{NS}/{DEP}"\nfunc New() string {{ return dep.NewAPI() }}\n',
                     f'\nrequire {NS}/{DEP} v0.1.0\n')
        # Host/test modules must adopt pins as well, not merely selected modules.
        for path, role in ((".", "application"), ("integration/microvm", "test"), ("environment/microvm", "runtime-artifact")):
            f.policy["modules"][path] = {"role": role}
            prefix = "" if path == "." else path + "/"
            identity = NS if path == "." else NS + "/" + path
            f.write(prefix + "go.mod", f'module {identity}\n\ngo 1.27\n\nrequire {NS}/{APP} v0.1.0\n')
            f.write(prefix + "host.go", f'package host\nimport "{NS}/{APP}"\nvar _ = openai.New\n')
        f.write("go.work", "go 1.27\nuse (\n" + "".join(" ./" + p + "\n" for p in f.policy["modules"]) + ")\n")
        return f.commit()

    def prepare(self, sha, batch=BATCH):
        output = self.temp / "prepared"
        _, env = self.fresh()
        with self.open(sha) as check:
            result = check.prepare_batch(batch, self.helper, output, env)
        return output, result

    def install(self, prepared):
        for file in prepared.rglob("*"):
            if file.is_file():
                target = self.f.root / file.relative_to(prepared)
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(file, target)
        return self.f.commit()

    def test_whole_batch_new_API_final_squash_public_checksums_and_secrets(self):
        sha = self.candidate(hostile=True)
        sentinels = {key: "sentinel-not-for-go" for key in SECRETS}
        self.f.env.update(sentinels)
        with mock.patch.dict(os.environ, sentinels):
            self.assertFalse(set(SECRETS) & release.public_environment(self.temp).keys())
            with self.assertRaisesRegex(release.CheckError, "credential leaked: GH_TOKEN"):
                release.run(self.f.root / DEP, "go", "test", "-count=1", "./...", env=self.f.env)
            prepared, result = self.prepare(sha)
            self.assertFalse((prepared / ".git").exists())
            for path in (APP, ".", "integration/microvm", "environment/microvm"):
                text = (prepared / path / "go.mod").read_text()
                self.assertIn("v0.2.0", text)
                self.assertNotIn("v0.1.0", text)
                self.assertTrue((prepared / path / "go.sum").is_file())
            final = self.install(prepared)
            # Model PR1 squash away from main, authorized per exact pair only.
            tree = self.f.git("rev-parse", final + "^{tree}")
            squash = self.f.git("commit-tree", tree, "-p", sha, "-m", "reviewed squash")
            self.f.git("update-ref", "refs/remotes/origin/main", sha)
            authority = {p: {v: squash} for p, v in BATCH.items()}
            _, env = self.fresh()
            with self.open(squash, authority) as check:
                qualified = check.qualify_staged(BATCH, self.helper, self.temp / "staged", env, result["artifacts"])
            self.assertEqual(qualified["stage"], "staged-qualified")
            self.assertEqual({r["sha"] for r in qualified["artifacts"].values()}, {squash})
            for path in (DEP, APP):
                self.f.publish(path, BATCH[path], squash, origin=False)
                root, env = self.fresh()
                with self.open(squash, authority) as check:
                    proof = check.prove(path, BATCH[path], env, root, expected=result["artifacts"][path])
                self.assertEqual(proof["sum"], qualified["artifacts"][path]["sum"])
                if path == DEP:
                    with self.assertRaisesRegex(release.CheckError, "credential leaked: GH_TOKEN"):
                        release.run(root / "consumer", "go", "test", "-count=1", "./...", env=dict(env, **sentinels))
        print("evidence: prepared host/test/runtime pins; hostile candidate and downloaded consumers credential-free; one authorized squash; unchanged final sums equal fresh public downloads")

    def test_production_host_runtime_integration_replacements_and_pin_agreement(self):
        self.candidate()
        f = self.f
        f.policy["modules"]["engine"] = dict(role="public", tag_prefix="engine/", policy="policy.md", smoke="engine.go")
        f.write("engine/go.mod", f"module {NS}/engine\n\ngo 1.27\n")
        f.write("engine/port/fixture.go", "package port\ntype LLMRequest struct{}\n")
        f.write("internal/apicheck/fixture_test.go", f'package apicheck\nimport "{NS}/engine/port"\nvar _ port.LLMRequest\n')
        f.write("environment/microvm/engine.go", f'package host\nimport "{NS}/engine/port"\nvar _ port.LLMRequest\n')
        f.policy["modules"]["adapters"] = dict(role="public", tag_prefix="adapters/", policy="policy.md", smoke="adapters.go")
        f.write("adapters/go.mod", f"module {NS}/adapters\n\ngo 1.27\n\nrequire {NS}/{APP} v0.1.0\n")
        for package in ("grpcdriver", "jsonlstore", "redisstore"):
            f.write(f"adapters/{package}/fixture.go", f'package {package}\nimport "{NS}/{APP}"\nvar _ = openai.New\n')
        replacements = {}
        for path in (".", "environment/microvm", "integration/microvm"):
            prefix = "" if path == "." else "../../"
            text = (f.root / path / "go.mod").read_text()
            if path in (".", "environment/microvm"):
                text += f"\nrequire {NS}/engine v0.1.0\n"
            if path != "environment/microvm":
                text += f"\nrequire {NS}/adapters v0.1.0\n"
                f.write(("" if path == "." else path + "/") + "adapters.go",
                        f'package host\nimport _ "{NS}/adapters/grpcdriver"\n')
            replacements[path] = (f"replace (\n\t{NS}/{DEP} => {prefix or './'}{DEP}\n"
                                  f"\t{NS}/{APP} => {prefix or './'}{APP}\n"
                                  f"\t{NS}/adapters => {prefix or './'}adapters\n"
                                  f"\t{NS}/engine => {prefix or './'}engine\n")
            if path == "environment/microvm":
                replacements[path] = f"replace (\n\t{NS}/engine => ../../engine\n"
            if path == "integration/microvm":
                text += f"\nrequire {NS} v0.0.0\n"
                f.write(path + "/root.go", f'package host\nimport _ "{NS}"\n')
                replacements[path] += f"\t{NS} => ../..\n"
            replacements[path] += ")\n"
            f.write(("" if path == "." else path + "/") + "go.mod", text + "\n" + replacements[path])
            release.run(f.root / path, "go", "mod", "edit", "-fmt", env=f.env)
            manifest = (f.root / path / "go.mod").read_text()
            replacements[path] = manifest[manifest.index("replace "):]
        f.write("go.work", "go 1.27\nuse (\n" + "".join(" ./" + p + "\n" for p in f.policy["modules"]) + ")\n")
        sha = f.commit()
        batch = dict(BATCH, adapters="v0.2.0", engine="v0.2.0")
        prepared, report = self.prepare(sha, batch)
        for path, block in replacements.items():
            self.assertIn(block, (prepared / path / "go.mod").read_text())
            self.assertIn(f"{NS}/{APP} v0.2.0", (prepared / path / "go.mod").read_text())
        self.assertIn(f"{NS}/{APP} v0.2.0 h1:", (prepared / "environment/microvm/go.sum").read_text())
        final = self.install(prepared)
        _, env = self.fresh()
        with self.open(final) as check:
            check.pin_agreement()
            qualified = check.qualify_staged(batch, self.helper, self.temp / "production-proxy", env, report["artifacts"])
            with check.staged_environment(batch, self.temp / "production-proxy", env) as staged:
                for path in replacements:
                    # Ordinary local-dev tidy must stay clean after adoption too.
                    release.run(prepared / path, "go", "mod", "tidy", "-diff", env=staged)
        self.assertEqual(qualified["stage"], "staged-qualified")
        # Final qualification neither strips replacements nor repairs missing sums.
        (f.root / "environment/microvm/go.sum").unlink()
        missing = f.commit()
        _, env = self.fresh()
        with self.open(missing) as check, self.assertRaisesRegex(release.CheckError, "tidy -diff"):
            check.qualify_staged(batch, self.helper, self.temp / "production-missing", env, report["artifacts"])
        f.write("integration/microvm/go.mod", (f.root / "integration/microvm/go.mod").read_text().replace(
            f"{NS}/adapters v0.2.0", f"{NS}/adapters v0.1.0"))
        mismatch = f.commit()
        with self.open(mismatch) as check, self.assertRaisesRegex(release.CheckError, "same published adapters version"):
            check.pin_agreement()

    def test_dev_replacements_reject_external_unknown_and_published_leakage(self):
        self.candidate()
        manifest = self.f.root / "environment/microvm/go.mod"
        original = manifest.read_text()
        for replacement in (f"{NS}/{DEP} => ../../../outside", f"{NS}/{DEP} => ../../{APP}",
                            f"{NS}/{DEP} => /outside", "example.com/unknown => ../../" + DEP,
                            f"{NS}/{DEP} => {NS}/{DEP} v0.1.0"):
            with self.subTest(replacement=replacement):
                manifest.write_text(original + "\nreplace " + replacement + "\n")
                sha = self.f.commit()
                with self.open(sha) as check, self.assertRaisesRegex(release.CheckError, "canonical workspace directory"):
                    check.validate_replacements()
        manifest.write_text(original)
        published = self.f.root / APP / "go.mod"
        published.write_text(published.read_text() + f"\nreplace {NS}/{DEP} => ../../{DEP}\n")
        sha = self.f.commit()
        with self.open(sha) as check, self.assertRaisesRegex(release.CheckError, "replacement leakage"):
            check.validate_replacements()

    def test_preparation_preserves_symlinks_and_artifact_checksums(self):
        self.candidate()
        links = {"CLAUDE.md": "policy.md", DEP + "/source-link.txt": "fixture.go"}
        for path, target in links.items():
            (self.f.root / path).symlink_to(target)
        sha = self.f.commit()
        prepared, result = self.prepare(sha)
        for path, target in links.items():
            self.assertTrue((prepared / path).is_symlink(), path)
            self.assertEqual(os.readlink(prepared / path), target)
        final = self.install(prepared)
        changed = self.f.git("diff", "--name-only", sha, final).splitlines()
        self.assertTrue(changed)
        self.assertTrue(all(path.endswith(("go.mod", "go.sum")) for path in changed), changed)
        for path in links:
            self.assertTrue(self.f.git("ls-tree", final, "--", path).startswith("120000 "))
        with self.open(final) as check:
            rebuilt = check.stage(BATCH, self.helper, self.temp / "symlink-proxy", result["artifacts"])
        self.assertEqual(set(rebuilt), set(BATCH))

    def test_stale_pins_missing_sums_and_changed_artifacts_fail(self):
        sha = self.candidate()
        _, env = self.fresh()
        with self.open(sha) as check, self.assertRaisesRegex(release.CheckError, "stale selected pin"):
            check.qualify_staged(BATCH, self.helper, self.temp / "stale", env)
        prepared, result = self.prepare(sha)
        (prepared / APP / "go.sum").unlink()
        stale = self.install(prepared)
        _, env = self.fresh()
        with self.open(stale) as check, self.assertRaisesRegex(release.CheckError, "tidy -diff"):
            check.qualify_staged(BATCH, self.helper, self.temp / "missing-sums", env)
        (prepared / DEP / "fixture.go").write_text("package ssefilter\nfunc New() {}\n")
        changed = self.install(prepared)
        _, env = self.fresh()
        with self.open(changed) as check, self.assertRaisesRegex(release.CheckError, "checksum/identity mismatch"):
            check.qualify_staged(BATCH, self.helper, self.temp / "changed", env, result["artifacts"])

    def test_router_missing_selected_never_falls_back_old_pair_public_proof(self):
        sha = self.f.commit()
        self.f.publish(DEP, "v0.1.0", sha)
        self.f.publish(DEP, "v0.2.0", sha)  # Public has selected pair, staging does not.
        proxy = self.temp / "empty-staging"
        proxy.mkdir()
        root, env = self.fresh()
        verified = {}
        with self.open(sha) as check, check.staged_environment({DEP: "v0.2.0"}, proxy, env, verified) as staged:
            self.assertEqual(staged["GONOSUMDB"], NS + "/" + DEP)
            with self.assertRaises(release.CheckError):
                release.run(root, "go", "mod", "download", "-json", NS + "/" + DEP + "@v0.2.0", env=staged)
            data = json.loads(release.run(root, "go", "mod", "download", "-json", NS + "/" + DEP + "@v0.1.0", env=staged))
            self.assertTrue(data["Sum"].startswith("h1:"))
            self.assertIn((DEP, "v0.1.0", sha), verified)
        with self.open(sha) as check, self.assertRaisesRegex(release.CheckError, "production staging requires"):
            with check.staged_environment({DEP: "v0.2.0"}, proxy, dict(env, GOPROXY="https://proxy.golang.org")):
                self.fail("must reject globally disabled production sumdb before networking")
        # Tampered public old pair cannot borrow the selected identity exemption.
        self.f.write(DEP + "/fixture.go", "package ssefilter\nfunc New() {}\nfunc Changed() {}\n")
        changed = self.f.commit()
        target = self.f.proxy / NS / DEP / "@v/v0.1.0.mod"
        target.write_text(f"module {NS}/{DEP}\n\ngo 1.27\n// tampered\n")
        root, env = self.fresh()
        with self.open(changed) as check, check.staged_environment({DEP: "v0.2.0"}, proxy, env) as staged:
            with self.assertRaises(release.CheckError):
                release.run(root, "go", "mod", "download", "-json", NS + "/" + DEP + "@v0.1.0", env=staged)

    def test_old_pair_routing_binds_bytes_to_public_proof(self):
        sha = self.f.commit()
        target = self.f.publish(DEP, "v0.1.0", sha)
        proxy = self.temp / "stage"
        proxy.mkdir()
        root, env = self.fresh()
        with self.open(sha) as check:
            original = check.verify_prerequisites
            changed = []

            def prove_then_tamper(*args, **kwargs):
                verified = original(*args, **kwargs)
                if verified and not changed:
                    changed.append(True)
                    (target / "v0.1.0.mod").write_text(f"module {NS}/{DEP}\n\ngo 1.27\n// changed after proof\n")
                return verified

            with mock.patch.object(check, "verify_prerequisites", side_effect=prove_then_tamper):
                with check.staged_environment({DEP: "v0.2.0"}, proxy, env) as staged:
                    with self.assertRaises(release.CheckError):
                        release.run(root, "go", "mod", "download", "-json", NS + "/" + DEP + "@v0.1.0", env=staged)
            self.assertTrue(changed)

    def test_external_dependencies_keep_normal_sumdb_verification(self):
        sha = self.f.commit()
        identity, version = "example.com/external", "v0.1.0"
        target = self.f.proxy / identity / "@v"
        target.mkdir(parents=True)
        mod = f"module {identity}\n\ngo 1.27\n"
        (target / (version + ".mod")).write_text(mod)
        (target / (version + ".info")).write_text(json.dumps({"Version": version, "Time": "2026-01-01T00:00:00Z"}))
        with zipfile.ZipFile(target / (version + ".zip"), "w") as archive:
            archive.writestr(identity + "@" + version + "/go.mod", mod)
            archive.writestr(identity + "@" + version + "/external.go", "package external\n")
        requests = []

        class SumDB(http.server.BaseHTTPRequestHandler):
            def log_message(self, *_args):
                pass

            def do_GET(self):
                requests.append(self.path)
                self.send_error(503, "test sumdb rejects unauthenticated artifact")

        server = http.server.HTTPServer(("127.0.0.1", 0), SumDB)
        worker = threading.Thread(target=server.serve_forever, daemon=True)
        worker.start()
        try:
            proxy = self.temp / "stage"
            proxy.mkdir()
            root, env = self.fresh()
            env["GOSUMDB"] = f"sum.golang.org http://127.0.0.1:{server.server_port}"
            with self.open(sha) as check, check.staged_environment({DEP: "v0.2.0"}, proxy, env) as staged:
                self.assertNotEqual(staged["GOSUMDB"], "off")
                self.assertEqual(staged["GONOSUMDB"], NS + "/" + DEP)
                with self.assertRaisesRegex(release.CheckError, "sumdb rejects"):
                    release.run(root, "go", "mod", "download", "-json", identity + "@" + version, env=staged)
            self.assertTrue(any(path.startswith("/lookup/example.com/external@v0.1.0") for path in requests), requests)
        finally:
            server.shutdown()
            worker.join()
            server.server_close()

    def test_api_classification_and_minor_version_gate(self):
        f = self.f
        f.policy["modules"][DEP].update(api=DEP + "/api", changelog=DEP + "/CHANGELOG.md")
        f.write(DEP + "/CHANGELOG.md", "### Changed\nInitial API\n")
        f.write(DEP + "/api/surface.txt", "New\n")
        base = f.commit()
        f.publish(DEP, "v0.1.0", base)
        f.write(DEP + "/api/surface.txt", "New\nNewAPI\n")
        f.write(DEP + "/CHANGELOG.md", "### Changed\nAdd NewAPI\n")
        sha = api_sha = f.commit()
        _, env = self.fresh()
        with self.open(sha) as check, self.assertRaisesRegex(release.CheckError, "at least a minor release"):
            check.qualify_staged({DEP: "v0.1.1"}, self.helper, self.temp / "patch-api", env)
        _, env = self.fresh()
        with self.open(sha) as check:
            self.assertEqual(check.qualify_staged({DEP: "v0.2.0"}, self.helper, self.temp / "minor-api", env)["stage"], "staged-qualified")
        f.write(DEP + "/CHANGELOG.md", "### Fixed\nNot an API classification\n")
        sha = f.commit()
        _, env = self.fresh()
        with self.open(sha) as check, self.assertRaisesRegex(release.CheckError, "API changelog classification"):
            check.qualify_staged({DEP: "v0.2.0"}, self.helper, self.temp / "bad-api", env)
        f.publish(DEP, "v0.1.1", api_sha)
        _, env = self.fresh()
        with self.open(api_sha) as check, self.assertRaisesRegex(release.CheckError, "at least a minor release"):
            check.qualify_staged({DEP: "v0.1.1"}, self.helper, self.temp / "retry-api", env)

    def test_real_MVS_graph_cannot_substitute_selected_version(self):
        f = self.f
        sha = f.commit()
        f.publish(DEP, "v0.1.0", sha)
        consumer, env = self.fresh()
        (consumer / "go.mod").write_text(f"module example.com/consumer\n\ngo 1.27\nrequire {NS}/{DEP} v0.1.0\n")
        (consumer / "main.go").write_text(f'package consumer\nimport "{NS}/{DEP}"\nvar _ = ssefilter.New\n')
        release.run(consumer, "go", "mod", "tidy", env=env)
        with self.open(sha) as check, self.assertRaisesRegex(release.CheckError, "MVS changed selected version"):
            check.check_selection(consumer, env, {DEP: "v0.2.0"}, {}, {}, env)

    def test_candidate_mutating_manifests_fails_final_check(self):
        f = self.f
        f.write(DEP + "/hostile_test.go", '''package ssefilter
import ("os"; "testing")
func TestMutate(t *testing.T) {
 if err := os.WriteFile("go.sum", []byte("mutated\\n"), 0600); err != nil { t.Fatal(err) }
}
''')
        sha = f.commit()
        _, env = self.fresh()
        with self.open(sha) as check, self.assertRaises(release.CheckError):
            check.qualify_staged({DEP: "v0.1.0"}, self.helper, self.temp / "mutated", env)

    def test_off_main_abandoned_release_remains_version_and_source_baseline(self):
        base = self.f.commit()
        self.f.write(DEP + "/fixture.go", "package ssefilter\nfunc New() {}\nfunc Abandoned() {}\n")
        abandoned = self.f.commit()
        self.f.publish(DEP, "v0.9.0", abandoned)
        self.f.git("update-ref", "refs/remotes/origin/main", base)
        with self.open(base) as check, self.assertRaisesRegex(release.CheckError, "not on fetched origin/main"):
            check.preview({DEP: "v0.2.0"})
        authority = {DEP: {"v0.9.0": abandoned}}
        with self.open(base, authority) as check:
            with self.assertRaisesRegex(release.CheckError, "newer than v0.9.0"):
                check.preview({DEP: "v0.2.0"})
            result = check.preview({DEP: "v0.10.0"})
            self.assertEqual(result["releases"][0]["base_commit"], abandoned)
            self.assertIn("Abandoned", result["releases"][0]["source_diff"])
        with self.open(abandoned, {DEP: {"v0.9.0": base}}) as check:
            with self.assertRaisesRegex(release.CheckError, "authority conflict"):
                check.tag_commit(DEP, "v0.9.0")
        with self.open(abandoned, authority) as check:
            with self.assertRaisesRegex(release.CheckError, "not on fetched origin/main"):
                check.preview({DEP: "v0.10.0"})  # Old authority cannot authorize new pair.


if __name__ == "__main__":
    unittest.main()
