#!/usr/bin/env python3
"""Offline release regressions: real Go, Git histories and an immutable file proxy.

Run: python3 .github/scripts/module-release_test.py
No external network, operator Go caches, source edits, or GitHub credentials are required.
"""

from contextlib import contextmanager
import copy
import http.server
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import unittest
from unittest import mock
import zipfile


HERE = Path(__file__).resolve().parent
sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location("module_release", HERE / "module-release.py")
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)
NS = "github.com/stacklok/mecatl"
DEP = "provider/ssefilter"
APP = "provider/openai"
SUPPORT = "internal/adaptersupport"


def fixture_environment(root):
    """Test-owned Git authority; never inherit hooks, signing or repo selection."""
    home, xdg, hooks, template, temporary = (root / name for name in ("home", "xdg", "hooks", "template", "tmp"))
    for directory in (home, xdg, hooks, template, temporary):
        directory.mkdir(parents=True, exist_ok=True)
    env = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
    env.update(HOME=str(home), XDG_CONFIG_HOME=str(xdg), TMPDIR=str(temporary), GIT_CONFIG_NOSYSTEM="1",
               GIT_CONFIG_SYSTEM=os.devnull, GIT_CONFIG_GLOBAL=os.devnull,
               GIT_ATTR_NOSYSTEM="1", GIT_TERMINAL_PROMPT="0", GIT_CONFIG_COUNT="5")
    for index, (key, value) in enumerate((("core.hooksPath", str(hooks)),
                                         ("init.templateDir", str(template)),
                                         ("commit.gpgSign", "false"), ("tag.gpgSign", "false"),
                                         ("init.defaultBranch", "main"))):
        env[f"GIT_CONFIG_KEY_{index}"] = key
        env[f"GIT_CONFIG_VALUE_{index}"] = value
    return env


class Fixture:
    def __init__(self, root):
        self.root = root / "repo"
        self.root.mkdir()
        self.proxy = root / "proxy"
        self.proxy.mkdir()
        self.policy = dict(schema=1, namespace=NS, modules={})
        with mock.patch.dict(os.environ, fixture_environment(root), clear=True):
            self.env = release.public_environment(root)
        self.env.update(GOPROXY=self.proxy.as_uri(), GOSUMDB="off", GOPROXY_TEST="offline")
        self.git("init", "-q")
        self.git("config", "user.name", "Fixture")
        self.git("config", "user.email", "fixture@example.com")
        self.add_module(DEP, "package ssefilter\n\nfunc New() {}\n")
        self.write("policy.md", "Fixture compatibility policy.\n")

    def git(self, *args):
        return release.git(self.root, *args, env=self.env)

    def write(self, path, text):
        target = self.root / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(text)

    def add_module(self, path, source, requirements="", role="public", identity=None):
        self.write(path + "/go.mod", f"module {identity or NS + '/' + path}\n\ngo 1.27\n" + requirements)
        self.write(path + "/fixture.go", source)
        smoke = {DEP: "ssefilter.go", APP: "openai.go", SUPPORT: "support.go"}[path]
        self.policy["modules"][path] = dict(role=role, tag_prefix=path + "/", policy="policy.md", smoke=smoke)
        self.write("go.work", "go 1.27\n\nuse (\n" + "".join(" ./" + p + "\n" for p in self.policy["modules"]) + ")\n")

    def commit(self, allow_empty=False):
        self.git("add", ".")
        self.git("commit", *(["--allow-empty"] if allow_empty else []), "-qm", "fixture")
        sha = self.git("rev-parse", "HEAD")
        self.git("update-ref", "refs/remotes/origin/main", sha)
        return sha

    def publish(self, path, version, sha, origin=True):
        if not release.PSEUDO.fullmatch(version):
            self.git("tag", path + "/" + version, sha)
        target = self.proxy / (NS + "/" + path) / "@v"
        target.mkdir(parents=True, exist_ok=True)
        info = dict(Version=version, Time="2026-01-01T00:00:00Z")
        if origin:
            info["Origin"] = dict(VCS="git", URL="https://" + NS, Hash=sha, Ref="refs/tags/" + path + "/" + version)
        (target / (version + ".info")).write_text(json.dumps(info))
        tree = {}
        for line in self.git("ls-tree", "-r", sha, "--", path).splitlines():
            metadata, file = line.split("\t", 1)
            if metadata.startswith("100"):
                tree[file] = file[len(path) + 1:]
        nested = [name[:-len("go.mod")] for name in tree.values() if name.endswith("/go.mod")]
        with zipfile.ZipFile(target / (version + ".zip"), "w") as archive:
            for file, name in tree.items():
                if "vendor" in name.split("/") or any(name.startswith(directory) for directory in nested):
                    continue
                content = subprocess.run(["git", "show", sha + ":" + file], cwd=self.root,
                                         env=self.env, capture_output=True, check=True).stdout
                archive.writestr(NS + "/" + path + "@" + version + "/" + name, content)
                if name == "go.mod":
                    (target / (version + ".mod")).write_bytes(content)
        versions = target / "list"
        versions.write_text((versions.read_text() if versions.exists() else "") + version + "\n")
        return target

    @contextmanager
    def open(self, sha):
        before = self.git("status", "--porcelain=v1", "--untracked-files=all")
        with mock.patch.dict(os.environ, self.env, clear=True), release.source_tree(self.root, sha) as source:
            yield release.Release(self.root, source, sha, self.policy, self.env)
        after = self.git("status", "--porcelain=v1", "--untracked-files=all")
        if before != after:
            raise AssertionError("check modified original source checkout")


class ReleaseTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="module-release-test-")
        self.addCleanup(self.temporary.cleanup)
        self.temp = Path(self.temporary.name)
        isolation = mock.patch.dict(os.environ, fixture_environment(self.temp), clear=True)
        isolation.start()
        self.addCleanup(isolation.stop)
        self.fixture = Fixture(self.temp)

    def test_inventory_preview_and_exact_candidate(self):
        f = self.fixture
        sha = f.commit()
        with f.open(sha) as check:
            report = check.preview({DEP: "v0.1.0"})
            self.assertEqual(report["candidate"], sha)
            self.assertEqual(report["order"], [DEP])
            self.assertIn(DEP + "/fixture.go", report["releases"][0]["changes"])
        f.write(DEP + "/fixture.go", "uncommitted invalid source")
        with f.open(sha) as check:
            self.assertEqual(check.preview({DEP: "v0.1.0"})["candidate"], sha)

    def test_explicit_versions_and_module_major(self):
        for bad in ("v1", "1.0.0", "v01.0.0", "v0.1.0+build", "v0.1.0-01", "main", "v2.0.0", "v0.0.0-20260101000000-123456789abc"):
            with self.subTest(bad=bad), self.assertRaises(release.CheckError):
                release.validate_version(NS + "/engine", bad, release=True)
        release.validate_version(NS + "/engine/v2", "v2.1.0-rc.1", release=True)
        with self.assertRaises(release.CheckError):
            release.validate_version(NS + "/engine/v2", "v1.1.0")
        self.assertLess(release.version_key("v0.1.0-rc.2"), release.version_key("v0.1.0-rc.10"))
        self.assertLess(release.version_key("v0.1.0-rc.10"), release.version_key("v0.1.0"))

    def test_unclassified_wrong_identity_namespace_and_excluded(self):
        f = self.fixture
        sha = f.commit()
        for edit, message in ((lambda: f.policy["modules"].clear(), "unclassified"),
                              (lambda: f.policy["modules"][DEP].update(tag_prefix="engine/"), "namespace")):
            original = copy.deepcopy(f.policy)
            edit()
            with self.assertRaisesRegex(release.CheckError, message), f.open(sha):
                pass
            f.policy = original
        f.write(DEP + "/go.mod", "module example.com/wrong\n\ngo 1.27\n")
        sha = f.commit()
        with self.assertRaisesRegex(release.CheckError, "identity mismatch"), f.open(sha):
            pass
        f.write(DEP + "/go.mod", "module " + NS + "/" + DEP + "\n\ngo 1.27\n")
        sha = f.commit()
        f.policy["modules"][DEP] = {"role": "test"}
        with f.open(sha) as check, self.assertRaisesRegex(release.CheckError, "not a publishable"):
            check.preview({DEP: "v0.1.0"})

    def test_cycles_and_replacement_leakage(self):
        f = self.fixture
        f.add_module(APP, "package openai\nfunc New() {}\n", "\nrequire " + NS + "/" + DEP + " v0.1.0\n")
        f.write(DEP + "/go.mod", "module " + NS + "/" + DEP + "\n\ngo 1.27\n\nrequire " + NS + "/" + APP + " v0.1.0\n")
        sha = f.commit()
        with self.assertRaisesRegex(release.CheckError, "cycle"), f.open(sha):
            pass
        f.write(DEP + "/go.mod", "module " + NS + "/" + DEP + "\n\ngo 1.27\n\nreplace example.com/other => ../other\n")
        sha = f.commit()
        with f.open(sha) as check, self.assertRaisesRegex(release.CheckError, "replacement leakage"):
            check.preview({DEP: "v0.1.0"})

    def test_dependency_order_missing_prerequisite_and_no_expansion(self):
        f = self.fixture
        f.add_module(APP, 'package openai\nimport "' + NS + '/' + DEP + '"\nfunc New() { ssefilter.New() }\n', "\nrequire " + NS + "/" + DEP + " v0.1.0\n")
        sha = f.commit()
        with f.open(sha) as check:
            with self.assertRaisesRegex(release.CheckError, "missing published prerequisite"):
                check.preview({APP: "v0.1.0"})
            report = check.preview({APP: "v0.1.0", DEP: "v0.1.0"})
            self.assertEqual(report["order"], [DEP, APP])
        f.publish(DEP, "v0.1.0", sha)
        release.run(f.root / APP, "go", "mod", "tidy", env=f.env)
        f.write(DEP + "/fixture.go", "package ssefilter\nfunc New() {}\nfunc UnusedNewAPI() {}\n")
        sha = f.commit()
        with f.open(sha) as check:
            self.assertEqual(check.preview({APP: "v0.1.0"})["order"], [APP])
            self.assertEqual(check.qualify({APP: "v0.1.0"}, f.env)["stage"], "qualified")
            report = check.preview({APP: "v0.1.0", DEP: "v0.2.0"})
            self.assertEqual(report["releases"][1]["consumer_updates"], {DEP: {"from": "v0.1.0", "to": "v0.2.0"}})

    def test_conflicting_tag_main_ancestry_and_latest_diff(self):
        f = self.fixture
        old = f.commit()
        f.publish(DEP, "v0.1.0", old)
        f.write(DEP + "/fixture.go", "package ssefilter\nfunc New() {}\nfunc Next() {}\n")
        new = f.commit()
        with f.open(new) as check:
            with self.assertRaisesRegex(release.CheckError, "tag conflict"):
                check.preview({DEP: "v0.1.0"})
            report = check.preview({DEP: "v0.2.0"})
            self.assertEqual(report["releases"][0]["changes"], [DEP + "/fixture.go"])
            with self.assertRaisesRegex(release.CheckError, "newer than"):
                check.preview({DEP: "v0.0.9"})
        release.git(f.root, "update-ref", "refs/remotes/origin/main", old)
        with f.open(new) as check, self.assertRaisesRegex(release.CheckError, "not on fetched origin/main"):
            check.preview({DEP: "v0.2.0"})

    def test_wrong_tagged_module_identity(self):
        f = self.fixture
        f.write(DEP + "/go.mod", "module example.com/wrong\n\ngo 1.27\n")
        old = f.commit()
        f.publish(DEP, "v0.1.0", old)
        f.write(DEP + "/go.mod", "module " + NS + "/" + DEP + "\n\ngo 1.27\n")
        new = f.commit()
        with f.open(new) as check, self.assertRaisesRegex(release.CheckError, "tag module identity mismatch"):
            check.preview({DEP: "v0.2.0"})

    def test_standalone_qualification_builds_and_tests(self):
        f = self.fixture
        sha = f.commit()
        with f.open(sha) as check:
            report = check.qualify({DEP: "v0.1.0"}, f.env)
            self.assertEqual(report["stage"], "qualified")
        f.write(DEP + "/fixture_test.go", 'package ssefilter\nimport "testing"\nfunc TestBroken(t *testing.T) { t.Fatal("expected failure") }\n')
        sha = f.commit()
        with f.open(sha) as check, self.assertRaisesRegex(release.CheckError, "expected failure"):
            check.qualify({DEP: "v0.1.0"}, f.env)

    def test_qualification_does_not_supply_checkout_dependency_API(self):
        f = self.fixture
        old = f.commit()
        f.publish(DEP, "v0.1.0", old)
        f.write(DEP + "/fixture.go", "package ssefilter\nfunc New() {}\nfunc CandidateOnly() {}\n")
        f.add_module(APP, 'package openai\nimport "' + NS + '/' + DEP + '"\nfunc New() { ssefilter.CandidateOnly() }\n', "\nrequire " + NS + "/" + DEP + " v0.1.0\n")
        # Generate real public-pin metadata without compiling source.
        release.run(f.root / APP, "go", "mod", "tidy", env=f.env)
        sha = f.commit()
        with f.open(sha) as check, self.assertRaisesRegex(release.CheckError, "undefined: ssefilter.CandidateOnly"):
            check.qualify({APP: "v0.1.0"}, f.env)

    def test_tidy_diff_fails_without_editing_manifests(self):
        f = self.fixture
        f.write(DEP + "/go.mod", "module " + NS + "/" + DEP + "\n\ngo 1.27\n\nrequire example.com/unused v0.1.0\n")
        sha = f.commit()
        with f.open(sha) as check, self.assertRaisesRegex(release.CheckError, "tidy -diff"):
            check.qualify({DEP: "v0.1.0"}, f.env)

    def test_exact_public_smoke_and_closure_with_and_without_origin(self):
        f = self.fixture
        sha = f.commit()
        f.publish(DEP, "v0.1.0", sha, origin=False)
        with f.open(sha) as check:
            result = check.prove(DEP, "v0.1.0", f.env, self.temp)
            self.assertEqual(result["stage"], "public-verified")
            self.assertTrue(result["sum"].startswith("h1:"))

    def test_public_metadata_source_and_omission_failures(self):
        f = self.fixture
        sha = f.commit()
        proxy = f.publish(DEP, "v0.1.0", sha)
        with f.open(sha) as check:
            data = json.loads(release.run(f.root, "go", "mod", "download", "-json", NS + "/" + DEP + "@v0.1.0", env=f.env))
            for field, value, pattern in (("Path", "example.com/wrong", "identity/version"),
                                          ("Version", "v0.2.0", "identity/version"),
                                          ("Sum", "", "checksum"),
                                          ("Origin", {"Hash": "a" * 40}, "origin metadata")):
                with self.subTest(field=field), self.assertRaisesRegex(release.CheckError, pattern):
                    check.check_download(dict(data, **{field: value}), DEP, "v0.1.0", sha, f.env)
            for origin in (dict(data["Origin"], Ref="refs/tags/v0.1.0"),
                           dict(data["Origin"], Subdir="engine")):
                with self.assertRaisesRegex(release.CheckError, "origin metadata"):
                    check.check_download(dict(data, Origin=origin), DEP, "v0.1.0", sha, f.env)
            bad_mod = self.temp / "wrong.mod"
            bad_mod.write_text("module example.com/wrong\n\ngo 1.27\n")
            with self.assertRaisesRegex(release.CheckError, "go.mod differs"):
                check.check_download(dict(data, GoMod=str(bad_mod)), DEP, "v0.1.0", sha, f.env)
            corrupt = self.temp / "corrupt.zip"
            with zipfile.ZipFile(corrupt, "w") as archive:
                prefix = NS + "/" + DEP + "@v0.1.0/"
                archive.writestr(prefix + "go.mod", (f.root / DEP / "go.mod").read_bytes())
                archive.writestr(prefix + "fixture.go", "package ssefilter\nfunc New() {} // not tagged\n")
            with self.assertRaisesRegex(release.CheckError, "source mismatch"):
                check.check_download(dict(data, Zip=str(corrupt)), DEP, "v0.1.0", sha, f.env)
            with zipfile.ZipFile(corrupt, "w") as archive:
                archive.writestr(prefix + "go.mod", (f.root / DEP / "go.mod").read_bytes())
            with self.assertRaisesRegex(release.CheckError, "omits tagged Go source"):
                check.check_download(dict(data, Zip=str(corrupt)), DEP, "v0.1.0", sha, f.env)

    def test_public_missing_proxy_version_and_dependency(self):
        f = self.fixture
        sha = f.commit()
        release.git(f.root, "tag", DEP + "/v0.1.0", sha)
        with f.open(sha) as check, self.assertRaisesRegex(release.CheckError, "download"):
            check.prove(DEP, "v0.1.0", f.env, self.temp)

    def test_internal_support_uses_legitimate_import_path_and_standalone(self):
        f = self.fixture
        f.add_module(SUPPORT, "package adaptersupport\n", role="support")
        f.write(SUPPORT + "/filewatch/watch.go", "package filewatch\n")
        f.write(SUPPORT + "/soulbody/body.go", "package soulbody\n")
        sha = f.commit()
        f.publish(SUPPORT, "v0.1.0", sha)
        with f.open(sha) as check:
            self.assertEqual(check.prove(SUPPORT, "v0.1.0", f.env, self.temp)["stage"], "public-verified")
        consumer_mod = (self.temp / "consumer/go.mod").read_text()
        self.assertIn("module " + NS + "/publicationcheck", consumer_mod)

    def test_public_proxy_dependency_closure_cannot_use_checkout(self):
        f = self.fixture
        f.add_module(APP, 'package openai\nimport "' + NS + '/' + DEP + '"\nfunc New() { ssefilter.New() }\n', "\nrequire " + NS + "/" + DEP + " v0.1.0\n")
        sha = f.commit()
        # Both tags exist locally, but only the consumer is publicly resolvable.
        release.git(f.root, "tag", DEP + "/v0.1.0", sha)
        f.publish(APP, "v0.1.0", sha)
        with f.open(sha) as check, self.assertRaisesRegex(release.CheckError, "no such file|cannot find"):
            check.prove(APP, "v0.1.0", f.env, self.temp)

    def test_checksum_mismatch_is_not_ignored(self):
        f = self.fixture
        sha = f.commit()
        f.publish(DEP, "v0.1.0", sha)
        f.add_module(APP, 'package openai\nimport "' + NS + '/' + DEP + '"\nfunc New() { ssefilter.New() }\n', "\nrequire " + NS + "/" + DEP + " v0.1.0\n")
        f.write(APP + "/go.sum", NS + "/" + DEP + " v0.1.0 h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n")
        sha = f.commit()
        with f.open(sha) as check, self.assertRaisesRegex(release.CheckError, "checksum mismatch"):
            check.qualify({APP: "v0.1.0"}, f.env)

    def test_owning_api_changelog_and_version_policy(self):
        f = self.fixture
        entry = f.policy["modules"][DEP]
        entry.update(api=DEP + "/api", changelog=DEP + "/CHANGELOG.md")
        f.write(DEP + "/api/surface.txt", "func New()\n")
        f.write(DEP + "/CHANGELOG.md", "## [Unreleased]\n\n### Added\n\n- New.\n")
        old = f.commit()
        f.publish(DEP, "v0.1.0", old)
        f.write(DEP + "/api/surface.txt", "func New()\nfunc Next()\n")
        sha = f.commit()
        with f.open(sha) as check, self.assertRaisesRegex(release.CheckError, "owning changelog"):
            check.qualify({DEP: "v0.2.0"}, f.env)
        f.write(DEP + "/CHANGELOG.md", "## [Unreleased]\n\n### Added\n\n- New and Next.\n")
        sha = f.commit()
        with f.open(sha) as check:
            with self.assertRaisesRegex(release.CheckError, "minor release"):
                check.qualify({DEP: "v0.1.1"}, f.env)
            self.assertEqual(check.qualify({DEP: "v0.2.0"}, f.env)["stage"], "qualified")

    def test_public_proof_rejects_warmed_cache_and_wrong_exact_tag(self):
        f = self.fixture
        old = f.commit()
        f.publish(DEP, "v0.1.0", old)
        f.write(DEP + "/fixture.go", "package ssefilter\nfunc New() {}\nfunc Next() {}\n")
        sha = f.commit()
        with f.open(sha) as check, self.assertRaisesRegex(release.CheckError, "tag conflict"):
            check.prove(DEP, "v0.1.0", f.env, self.temp)
        Path(f.env["GOMODCACHE"]).mkdir()
        (Path(f.env["GOMODCACHE"]) / "ambient").write_text("not proof")
        with f.open(old) as check, self.assertRaisesRegex(release.CheckError, "fresh empty"):
            check.prove(DEP, "v0.1.0", f.env, self.temp)

    def test_public_environments_ignore_private_and_workspace_overrides(self):
        with mock.patch.dict(os.environ, GOWORK="/ambient/go.work", GOPROXY="file:///ambient", GONOSUMDB="*", GOFLAGS="-mod=vendor"):
            env = release.public_environment(self.temp)
        self.assertEqual(env["GOWORK"], "off")
        self.assertEqual(env["GOENV"], "off")
        self.assertEqual(env["GOPROXY"], "https://proxy.golang.org")
        self.assertEqual(env["GOSUMDB"], "sum.golang.org")
        self.assertEqual(env["GONOSUMDB"], "none")
        self.assertEqual(env["GOFLAGS"], "")
        self.assertFalse(Path(env["GOMODCACHE"]).exists())

    def test_unselected_public_prerequisite_must_pass_its_own_tests(self):
        f = self.fixture
        f.write(DEP + "/fixture_test.go", 'package ssefilter\nimport "testing"\nfunc TestBroken(t *testing.T) { t.Fatal("prerequisite standalone fails") }\n')
        old = f.commit()
        f.publish(DEP, "v0.1.0", old)
        f.add_module(APP, 'package openai\nimport "' + NS + '/' + DEP + '"\nfunc New() { ssefilter.New() }\n', "\nrequire " + NS + "/" + DEP + " v0.1.0\n")
        release.run(f.root / APP, "go", "mod", "tidy", env=f.env)
        sha = f.commit()
        release.standalone(f.root / APP, f.env)  # Consumer alone passes.
        with f.open(sha) as check, self.assertRaisesRegex(release.CheckError, "prerequisite standalone fails"):
            check.qualify({APP: "v0.1.0"}, f.env)

    def test_recursive_existing_prerequisites_are_proved_once_dependency_first(self):
        f = self.fixture
        old = f.commit()
        f.publish(DEP, "v0.1.0", old)
        f.add_module(SUPPORT, 'package adaptersupport\nimport "' + NS + '/' + DEP + '"\nfunc New() { ssefilter.New() }\n',
                     "\nrequire " + NS + "/" + DEP + " v0.1.0\n", role="support")
        f.write(SUPPORT + "/filewatch/watch.go", "package filewatch\n")
        f.write(SUPPORT + "/soulbody/body.go", "package soulbody\n")
        release.run(f.root / SUPPORT, "go", "mod", "tidy", env=f.env)
        support = f.commit()
        f.publish(SUPPORT, "v0.1.0", support)
        f.add_module(APP, 'package openai\nimport ("' + NS + '/' + DEP + '"; "' + NS + '/' + SUPPORT + '")\nfunc New() { ssefilter.New(); adaptersupport.New() }\n',
                     "\nrequire (\n" + NS + "/" + DEP + " v0.1.0\n" + NS + "/" + SUPPORT + " v0.1.0\n)\n")
        release.run(f.root / APP, "go", "mod", "tidy", env=f.env)
        sha = f.commit()
        with f.open(sha) as check, mock.patch.object(check, "_prove", wraps=check._prove) as prove:
            report = check.qualify({APP: "v0.1.0", SUPPORT: "v0.2.0"}, f.env)
            self.assertEqual([p["module"] for p in report["public_prerequisites"]], [DEP, SUPPORT])
            self.assertEqual(prove.call_count, 2)
            self.assertEqual([(p["module"], p["candidate"]) for p in report["public_prerequisites"]],
                             [(DEP, old), (SUPPORT, support)])

    def test_existing_pseudo_pin_proves_exact_main_commit_without_new_tag(self):
        f = self.fixture
        old = f.commit()
        version = "v0.0.0-20260101000000-" + old[:12]
        f.publish(DEP, version, old)
        f.add_module(APP, 'package openai\nimport "' + NS + '/' + DEP + '"\nfunc New() { ssefilter.New() }\n',
                     "\nrequire " + NS + "/" + DEP + " " + version + "\n")
        release.run(f.root / APP, "go", "mod", "tidy", env=f.env)
        sha = f.commit()
        with f.open(sha) as check:
            report = check.qualify({APP: "v0.1.0"}, f.env)
            self.assertEqual(report["public_prerequisites"][0]["candidate"], old)
            self.assertEqual(report["public_prerequisites"][0]["version"], version)
        self.assertEqual(f.git("tag", "--list"), "")
        f.git("update-ref", "refs/remotes/origin/main", old)
        f.write(DEP + "/unmerged.go", "package ssefilter\n")
        unmerged = f.commit()
        f.git("update-ref", "refs/remotes/origin/main", old)
        with f.open(sha) as check, self.assertRaisesRegex(release.CheckError, "not on fetched origin/main"):
            check.prerequisite(NS + "/" + DEP, "v0.0.0-20260101000000-" + unmerged[:12])

    def test_recursive_public_history_cycle_fails_before_consumer(self):
        f = self.fixture
        f.add_module(APP, "package openai\nfunc New() {}\n", "\nrequire " + NS + "/" + DEP + " v0.1.0\n")
        f.write(DEP + "/go.mod", "module " + NS + "/" + DEP + "\n\ngo 1.27\n\nrequire " + NS + "/" + APP + " v0.1.0\n")
        old = f.commit()
        f.publish(DEP, "v0.1.0", old)
        f.publish(APP, "v0.1.0", old)
        f.write(DEP + "/go.mod", "module " + NS + "/" + DEP + "\n\ngo 1.27\n")
        sha = f.commit()
        with f.open(sha) as check, self.assertRaisesRegex(release.CheckError, "public prerequisite cycle"):
            check.verify_prerequisites(check.modules[APP]["mod"], f.env)

    def test_git_fixture_scrubs_poison_and_allows_explicit_injection(self):
        poison = self.temp / "poison"
        poison.mkdir()
        hooks = poison / "hooks"
        hooks.mkdir()
        marker = poison / "executed"
        hook = hooks / "pre-commit"
        hook.write_text('#!/bin/sh\ntouch "' + str(marker) + '"\nexit 91\n')
        hook.chmod(0o755)
        config = poison / "gitconfig"
        config.write_text('[commit]\n gpgSign = true\n[core]\n hooksPath = ' + str(hooks) + '\n[init]\n templateDir = ' + str(poison) + '\n')
        child = self.temp / "isolated"
        child.mkdir()
        with mock.patch.dict(os.environ, HOME=str(poison), XDG_CONFIG_HOME=str(poison),
                             GIT_CONFIG_SYSTEM=str(config), GIT_CONFIG_GLOBAL=str(config),
                             GIT_CONFIG_PARAMETERS="'commit.gpgSign=true'", GIT_CONFIG_COUNT="1",
                             GIT_CONFIG_KEY_0="core.hooksPath", GIT_CONFIG_VALUE_0=str(hooks),
                             GIT_DIR=str(poison / "wrong.git"), GIT_WORK_TREE=str(poison),
                             GIT_INDEX_FILE=str(poison / "index"), GIT_OBJECT_DIRECTORY=str(poison),
                             GIT_TEMPLATE_DIR=str(poison), GIT_COMMON_DIR=str(poison)):
            f = Fixture(child)
            sha = f.commit()
            with f.open(sha) as check:
                self.assertEqual(check.preview({DEP: "v0.1.0"})["candidate"], sha)
            # Child processes/CLI receive the same explicit controls.
            result = subprocess.run(["git", "rev-parse", "--show-toplevel"], cwd=f.root,
                                    env=f.env, capture_output=True, text=True, check=True)
            self.assertEqual(result.stdout.strip(), str(f.root))
            injected = dict(f.env, GIT_CONFIG_COUNT="6", GIT_CONFIG_KEY_5="release.fixture", GIT_CONFIG_VALUE_5="explicit")
            self.assertEqual(release.git(f.root, "config", "--get", "release.fixture", env=injected), "explicit")
        self.assertFalse(marker.exists())

    def test_evidence_diff_never_executes_external_or_textconv_helpers(self):
        f = self.fixture
        f.write(".gitattributes", "*.go diff=poison\n")
        old = f.commit()
        f.publish(DEP, "v0.1.0", old)
        marker = self.temp / "diff-executed"
        helper = self.temp / "diff-helper"
        helper.write_text('#!/bin/sh\ntouch "' + str(marker) + '"\nprintf "forged evidence\\n"\n')
        helper.chmod(0o755)
        f.git("config", "diff.external", str(helper))
        f.git("config", "diff.poison.textconv", str(helper))
        f.write(DEP + "/fixture.go", "package ssefilter\nfunc New() {}\nfunc Next() {}\n")
        sha = f.commit()
        with f.open(sha) as check:
            report = check.preview({DEP: "v0.2.0"})
            self.assertIn("+func Next()", report["releases"][0]["source_diff"])
        diff = release.git(f.root, "diff", old, sha, env=dict(f.env, GIT_EXTERNAL_DIFF=str(helper)))
        self.assertIn("+func Next()", diff)
        self.assertFalse(marker.exists())

    def test_checksum_service_transport_failure_fails_closed(self):
        f = self.fixture
        sha = f.commit()
        f.publish(DEP, "v0.1.0", sha)
        requests = []

        class RejectChecksum(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                requests.append(self.path)
                self.send_error(503, "synthetic checksum service unavailable")

            def log_message(self, *args):
                pass

        server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), RejectChecksum)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            env = dict(f.env, GOSUMDB=f"sum.golang.org http://127.0.0.1:{server.server_port}")
            with f.open(sha) as check, self.assertRaisesRegex(release.CheckError, "503.*synthetic checksum"):
                check.prove(DEP, "v0.1.0", env, self.temp)
            self.assertTrue(any(path.startswith(("/lookup/", "/tile/")) for path in requests), requests)
        finally:
            server.shutdown()
            server.server_close()
            thread.join()

    def test_duplicate_request_keys_rejected(self):
        path = self.temp / "batch.json"
        path.write_text('{"engine":"v0.1.0","engine":"v0.2.0"}')
        with self.assertRaisesRegex(release.CheckError, "duplicate JSON key"):
            release.load_json(path)


if __name__ == "__main__":
    # Keep real Go compilation/proxy integration opt-in; these exercise policy
    # against committed test-owned Git histories without running module suites.
    quick = "--quick" in sys.argv
    if quick:
        sys.argv.remove("--quick")
    unittest.main(defaultTest=["ReleaseTests." + name for name in (
        "test_inventory_preview_and_exact_candidate",
        "test_explicit_versions_and_module_major",
        "test_unclassified_wrong_identity_namespace_and_excluded",
        "test_cycles_and_replacement_leakage",
        "test_conflicting_tag_main_ancestry_and_latest_diff",
        "test_public_environments_ignore_private_and_workspace_overrides",
        "test_git_fixture_scrubs_poison_and_allows_explicit_injection",
        "test_evidence_diff_never_executes_external_or_textconv_helpers",
        "test_duplicate_request_keys_rejected",
    )] if quick else None)
