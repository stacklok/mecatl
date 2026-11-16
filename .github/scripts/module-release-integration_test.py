#!/usr/bin/env python3
"""Opt-in, offline production-source release integration (not the daily quick gate).

python3 .github/scripts/module-release-integration_test.py --synthetic
python3 .github/scripts/module-release-integration_test.py --offline-proxy /path/to/proxy

The first command compiles all nine unmodified owned fixtures against a hermetic
synthetic proxy using production inventory/policy identities and dependency edges.
The second additionally runs actual production source and the real engine API gate.
The explicit read-only file proxy must contain the third-party versions required
by the checkout (a preseeded Go cache/download directory is a suitable input).
There is no network fallback. Same-repository modules never come from that input:
they are published from actual production source into test-owned Git/proxy history.
Every proof still uses fresh module/build caches. Temporary manifests adopt only
synthetic sibling versions; original source/manifests are never changed.
"""

from contextlib import contextmanager
from datetime import datetime
import http.server
import importlib.util
import json
import os
from pathlib import Path
import shutil
import sys
import tempfile
import threading
import unittest
from unittest import mock


HERE = Path(__file__).resolve().parent
PROJECT = HERE.parent.parent
sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location("release_fixtures", HERE / "module-release_test.py")
fixtures = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fixtures)
release = fixtures.release
OFFLINE_PROXY = None


@contextmanager
def proxy_server(owned, external):
    class Proxy(http.server.SimpleHTTPRequestHandler):
        def translate_path(self, path):
            # Never let a missing owned version fall back to a cached real tag.
            self.directory = str(owned if path.startswith("/github.com/stacklok/mecatl/") else external)
            return super().translate_path(path)

        def do_GET(self):
            path = Path(self.translate_path(self.path))
            if path.suffix == ".info" and not path.exists() and path.with_suffix(".mod").is_file():
                # Lazy download caches may contain graph-only .mod files but no
                # .info. Metadata is synthetic test data; source zips and .mod
                # checksums still have to come from the explicit offline seed.
                version = path.stem
                timestamp = "2026-01-01T00:00:00Z"
                if release.PSEUDO.fullmatch(version):
                    raw = version.rsplit("-", 2)[-2].rsplit(".", 1)[-1]
                    timestamp = datetime.strptime(raw, "%Y%m%d%H%M%S").isoformat() + "Z"
                data = json.dumps(dict(Version=version, Time=timestamp)).encode()
                self.send_response(200)
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)
                return
            return super().do_GET()

        def log_message(self, *args):
            pass

    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Proxy)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        yield f"http://127.0.0.1:{server.server_port}"
    finally:
        server.shutdown()
        server.server_close()
        thread.join()


class ProductionReleaseIntegration(unittest.TestCase):
    def test_production_policy_all_owned_fixtures_offline(self):
        with tempfile.TemporaryDirectory(prefix="module-release-policy-") as directory:
            temporary = Path(directory)
            with mock.patch.dict(os.environ, fixtures.fixture_environment(temporary), clear=True):
                f = fixtures.Fixture(temporary)
                policy = release.load_json(HERE / "module-publication-policy.json")
                actual, snapshots = release.candidate_helpers.inventory(PROJECT, f.env)
                paths = {identity: str(path.relative_to(PROJECT)) for identity, (path, _) in actual.items()}
                self.assertEqual(set(paths.values()), set(policy["modules"]))
                f.policy = policy
                f.write("go.work", (PROJECT / "go.work").read_text())
                published = {path for path, entry in policy["modules"].items() if entry["role"] in release.PUBLISHED}
                self.assertEqual(len(published), 9)
                public_package = {path: policy["namespace"] + "/" + path for path in published}
                public_package.update({"engine": policy["namespace"] + "/engine/port",
                                       "contracts/gen/go/mecatl/driver": policy["namespace"] + "/contracts/gen/go/mecatl/driver/v1",
                                       "internal/adaptersupport": policy["namespace"] + "/internal/adaptersupport/filewatch",
                                       "adapters": policy["namespace"] + "/adapters/jsonlstore"})
                # Minimal synthetic APIs, but actual production identities, module
                # graph, publication roles and all nine unmodified owned fixtures.
                version = "v0.1.0"
                for identity, (directory, mod) in actual.items():
                    path = paths[identity]
                    deps = [dep for dep in release.candidate_helpers.requirements(mod) if paths.get(dep) in published] if path in published else []
                    requires = "\nrequire (\n" + "".join(dep + " " + version + "\n" for dep in deps) + ")\n" if deps else ""
                    imports = "\nimport (\n" + "".join('_ "' + public_package[paths[dep]] + '"\n' for dep in deps) + ")\n" if deps else ""
                    f.write(path + "/go.mod", f"module {identity}\n\ngo {mod['Go']}\n" + requires)
                    name = path.rsplit("/", 1)[-1] if path.startswith("provider/") or path == "authn/oidc" else "fixture"
                    declarations = "\nfunc New() {}\n" if path.startswith("provider/") else "\ntype Config struct{}\n" if path == "authn/oidc" else ""
                    f.write(path + "/fixture.go", "package " + name + "\n" + imports + declarations)
                for path, body in {
                    "engine/port/fixture.go": "package port\ntype LLMRequest struct{}\n",
                    "contracts/gen/go/mecatl/driver/v1/fixture.go": "package driver\ntype SessionSnapshot struct{}\n",
                    "internal/adaptersupport/filewatch/fixture.go": "package filewatch\n",
                    "internal/adaptersupport/soulbody/fixture.go": "package soulbody\n",
                    "adapters/grpcdriver/fixture.go": "package grpcdriver\n",
                    "adapters/jsonlstore/fixture.go": "package jsonlstore\n",
                    "adapters/redisstore/fixture.go": "package redisstore\n",
                }.items():
                    f.write(path, body)
                for entry in policy["modules"].values():
                    for key in ("policy", "changelog"):
                        if key in entry:
                            f.write(entry[key], (PROJECT / entry[key]).read_text())
                candidate = f.commit()
                with f.open(candidate) as check:
                    order = check.order(published)
                for path in order:
                    release.run(f.root / path, "go", "mod", "tidy", env=f.env)
                    candidate = f.commit(allow_empty=True)
                    f.publish(path, version, candidate)
                with f.open(candidate) as check:
                    requirements = {"Require": [{"Path": check.modules[path]["identity"], "Version": version} for path in order]}
                    evidence = check.verify_prerequisites(requirements, f.env)
                    self.assertEqual({item["module"] for item in evidence.values()}, published)
                    self.assertEqual(len(evidence), 9)
                    self.assertTrue(all(item["stage"] == "public-verified" for item in evidence.values()))
                release.candidate_helpers.verify_unchanged(snapshots)

    @contextmanager
    def production_source(self):
        self.assertIsNotNone(OFFLINE_PROXY, "provide an explicit --offline-proxy fixture")
        self.assertTrue(OFFLINE_PROXY.is_dir(), "offline proxy fixture directory does not exist")
        with tempfile.TemporaryDirectory(prefix="module-release-production-") as directory:
            temporary = Path(directory)
            with mock.patch.dict(os.environ, fixtures.fixture_environment(temporary), clear=True):
                f = fixtures.Fixture(temporary)
                policy = release.load_json(HERE / "module-publication-policy.json")
                actual, snapshots = release.candidate_helpers.inventory(PROJECT, f.env)
                self.assertEqual({str(path.relative_to(PROJECT)): identity for identity, (path, _) in actual.items()},
                                 {path: policy["namespace"] + ("/" + path if path != "." else "") for path in policy["modules"]})
                # Committed sources plus current owning policy/workspace metadata.
                (f.root / fixtures.DEP / "fixture.go").unlink()
                sha = release.git(PROJECT, "rev-parse", "HEAD")
                with release.source_tree(PROJECT, sha) as source:
                    shutil.copytree(source, f.root, dirs_exist_ok=True)
                for path in {"go.work"} | {entry["policy"] for entry in policy["modules"].values() if "policy" in entry}:
                    shutil.copyfile(PROJECT / path, f.root / path)
                f.policy = policy
                f.git("add", "--force", ".")
                with proxy_server(f.proxy, OFFLINE_PROXY) as proxy:
                    f.env["GOPROXY"] = proxy
                    try:
                        yield f, actual
                    finally:
                        release.candidate_helpers.verify_unchanged(snapshots)

    def test_real_engine_api_gate_and_stale_baseline(self):
        with self.production_source() as (f, _):
            candidate = f.commit()
            with f.open(candidate) as check:
                # Production engine dispatch and actual internal/apicheck Go
                # tests, not a synthetic gate or a patched command runner.
                engine_release = dict(module="engine", api_changes=[])
                check.policy_check(engine_release, f.env)
                baseline = next((check.source / "engine/api").glob("*.txt"))
                original = baseline.read_bytes()
                baseline.write_bytes(original + b"synthetic stale API declaration\n")
                try:
                    with self.assertRaisesRegex(release.CheckError, "engine public API changed"):
                        check.policy_check(engine_release, f.env)
                finally:
                    baseline.write_bytes(original)

    def test_all_production_source_public_closures(self):
        with self.production_source() as (f, actual):
            published = {path for path, entry in f.policy["modules"].items() if entry["role"] in release.PUBLISHED}
            self.assertEqual(len(published), 9)
            version = "v0.0.1"  # Test-only versions, never production proposals.
            identities = {identity: str(path.relative_to(PROJECT)) for identity, (path, _) in actual.items()}
            for path in sorted(published):
                manifest = f.root / path / "go.mod"
                mod = release.candidate_helpers.parse_mod(manifest.parent, f.env, manifest)
                flags = ["-require=" + identity + "@" + version for identity in release.candidate_helpers.requirements(mod)
                         if identities.get(identity) in published]
                if flags:
                    release.run(manifest.parent, "go", "mod", "edit", *flags, env=f.env)
            candidate = f.commit()
            with f.open(candidate) as check:
                order = check.order(published)
            for path in order:
                print("prepare offline production source: " + path, flush=True)
                release.run(f.root / path, "go", "mod", "tidy", env=f.env)
                candidate = f.commit(allow_empty=True)
                f.publish(path, version, candidate)
            with f.open(candidate) as check:
                requirements = {"Require": [{"Path": check.modules[path]["identity"], "Version": version} for path in order]}
                print("prove all nine owned consumers, exact source and standalone closures", flush=True)
                evidence = check.verify_prerequisites(requirements, f.env)
                self.assertEqual({item["module"] for item in evidence.values()}, published)
                self.assertTrue(all(item["stage"] == "public-verified" for item in evidence.values()))


if __name__ == "__main__":
    if "--synthetic" in sys.argv:
        sys.argv.remove("--synthetic")
        unittest.main(defaultTest="ProductionReleaseIntegration.test_production_policy_all_owned_fixtures_offline")
    if "--offline-proxy" not in sys.argv:
        raise SystemExit(__doc__)
    index = sys.argv.index("--offline-proxy")
    if index + 1 == len(sys.argv):
        raise SystemExit("--offline-proxy requires a read-only fixture directory")
    OFFLINE_PROXY = Path(sys.argv[index + 1]).resolve()
    del sys.argv[index:index + 2]
    unittest.main()
