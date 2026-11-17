#!/usr/bin/env python3
"""Offline artifact feasibility: canonical x/mod versus independent Go VCS fetch.

python3 .github/scripts/module-artifact_test.py
No controller changes, public network, production tags or operator-state writes.
"""

import importlib.util
import json
import os
from pathlib import Path
import shutil
import sys
import tempfile
import unittest
from unittest import mock
import zipfile

HERE = Path(__file__).resolve().parent
PROJECT = HERE.parent.parent
sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location("release_fixtures", HERE / "module-release_test.py")
fixtures = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fixtures)
release = fixtures.release
DEP, APP = fixtures.DEP, fixtures.APP
NS = "github.com/Example/Mecatl"
VERSIONS = {DEP: "v0.2.0-RC.1", APP: "v0.3.0"}


class ArtifactTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tools = tempfile.TemporaryDirectory(prefix="module-artifact-tools-")
        cls.helper = Path(cls.tools.name) / "module-artifact"
        # Only the trusted helper build reads the installed x/mod dependency.
        # Candidate downloads below have entirely test-owned, empty caches.
        env = dict(os.environ, GOENV="off", GOWORK="off", GOFLAGS="",
                   GOTOOLCHAIN="local", GOPROXY="off", GOCACHE=str(Path(cls.tools.name) / "build"))
        release.run(PROJECT, "go", "build", "-o", str(cls.helper),
                    str(HERE / "module-artifact.go"), env=env)

    @classmethod
    def tearDownClass(cls):
        cls.tools.cleanup()

    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="module-artifact-test-")
        self.addCleanup(self.temporary.cleanup)
        self.temp = Path(self.temporary.name)
        isolation = mock.patch.dict(os.environ, fixtures.fixture_environment(self.temp), clear=True)
        isolation.start()
        self.addCleanup(isolation.stop)
        self.f = f = fixtures.Fixture(self.temp)
        f.policy["namespace"] = NS
        f.write(DEP + "/go.mod", f"module {NS}/{DEP}\n\ngo 1.27\n")
        f.add_module(APP, f'package openai\nimport dep "{NS}/{DEP}"\nfunc New() string {{ return dep.NewAPI() }}\n',
                     f"\nrequire {NS}/{DEP} {VERSIONS[DEP]}\n", identity=NS + "/" + APP)
        f.write("LICENSE", "repository license\n")
        f.write(APP + "/LICENSE", "module-specific license\n")
        f.write(DEP + "/nested/go.mod", "module example.com/nested\n\ngo 1.27\n")
        f.write(DEP + "/nested/hidden.go", "package nested\n")
        f.write(DEP + "/vendor/example.com/hidden/hidden.go", "package hidden\n")
        f.write(DEP + "/data.txt", "included data\n")
        f.write(DEP + "/.hidden", "also included\n")
        (f.root / DEP / "link").symlink_to("data.txt")
        self.base = f.commit()

    def stage(self, sha, batch, name, expected=None):
        with self.f.open(sha) as check:
            return check.stage(batch, self.helper, self.temp / name, expected)

    def fresh_env(self, name, proxy):
        root = self.temp / name
        root.mkdir()
        env = dict(self.f.env, GOPROXY=proxy, GOMODCACHE=str(root / "cache"),
                   GOCACHE=str(self.temp / "candidate-build"), GIT_ALLOW_PROTOCOL="file")
        return root, env

    def download(self, name, proxy, identity, version, direct=False):
        root, env = self.fresh_env(name, proxy)
        if direct:
            # Real cmd/go VCS resolution, but git may ONLY use a local file URL.
            # Any accidental network path fails before opening a connection.
            index = int(env["GIT_CONFIG_COUNT"])
            env.update(GIT_CONFIG_COUNT=str(index + 1))
            env[f"GIT_CONFIG_KEY_{index}"] = f"url.{self.f.root.as_uri()}.insteadOf"
            env[f"GIT_CONFIG_VALUE_{index}"] = "https://" + NS
        data = json.loads(release.run(root, "go", "mod", "download", "-json", identity + "@" + version, env=env))
        self.assertNotIn("Error", data)
        return data

    def prepare(self):
        f = self.f
        # The current published API cannot compile this new sibling consumer.
        self.stage(self.base, {DEP: "v0.1.0"}, "old")
        old = self.temp / "old-consumer"
        old.mkdir()
        (old / "go.mod").write_text(f"module example.com/consumer\n\ngo 1.27\nrequire {NS}/{DEP} v0.1.0\n")
        (old / "main.go").write_text(f'package consumer\nimport dep "{NS}/{DEP}"\nvar _ = dep.NewAPI()\n')
        _, old_env = self.fresh_env("old-cache", (self.temp / "old").as_uri())
        release.run(old, "go", "mod", "tidy", env=old_env)
        with self.assertRaisesRegex(release.CheckError, "undefined: dep.NewAPI"):
            release.run(old, "go", "test", "./...", env=old_env)
        # Consumer cannot tidy the selected future pin before staging the dependency.
        with self.assertRaises(release.CheckError):
            release.run(f.root / APP, "go", "mod", "tidy", env=f.env)
        f.write(DEP + "/fixture.go", 'package ssefilter\nfunc NewAPI() string { return "new sibling API" }\n')
        prepared = f.commit()
        dep = self.stage(prepared, {DEP: VERSIONS[DEP]}, "dependency")
        f.env["GOPROXY"] = (self.temp / "dependency").as_uri()
        release.run(f.root / APP, "go", "mod", "tidy", env=f.env)
        sums = (f.root / APP / "go.sum").read_text()
        self.assertIn(f"{NS}/{DEP} {VERSIONS[DEP]} {dep[DEP]['sum']}\n", sums)
        self.assertIn(f"{NS}/{DEP} {VERSIONS[DEP]}/go.mod {dep[DEP]['go_mod_sum']}\n", sums)
        final = f.commit()
        records = self.stage(final, VERSIONS, "final")
        self.assertEqual(records[DEP]["sum"], dep[DEP]["sum"])
        self.assertEqual(records[DEP]["go_mod_sum"], dep[DEP]["go_mod_sum"])
        return final, records

    def test_final_sha_squash_and_independent_public_equivalent_downloads(self):
        final, records = self.prepare()
        f = self.f
        # A real single-parent squash of the exact reviewed tree, not another
        # dependency-specific candidate SHA. No helper .info/checksum inputs reused.
        tree = f.git("rev-parse", final + "^{tree}")
        squash = f.git("commit-tree", tree, "-p", self.base, "-m", "reviewed squash")
        self.assertNotEqual(squash, final)
        self.assertEqual(f.git("rev-parse", squash + "^{tree}"), tree)
        f.write(DEP + "/fixture.go", "dirty worktree must never enter artifacts\n")
        f.write(DEP + "/untracked.txt", "nor may untracked files\n")
        rebuilt = self.stage(squash, VERSIONS, "squashed", records)
        self.assertEqual({r["sha"] for r in rebuilt.values()}, {squash})
        public = self.temp / "public-equivalent"
        public.mkdir()
        for index, (path, version) in enumerate(VERSIONS.items()):
            f.git("tag", path + "/" + version, squash)
            record = rebuilt[path]
            self.assertIn("!example/!mecatl", record["proxy_path"])
            if path == DEP:
                self.assertTrue(record["proxy_path"].endswith("v0.2.0-!r!c.1"))
            direct = self.download(f"direct-{index}", "direct", NS + "/" + path, version, direct=True)
            self.assertEqual(direct["Origin"]["Hash"], squash)
            self.assertEqual(direct["Sum"], record["sum"])
            self.assertEqual(direct["GoModSum"], record["go_mod_sum"])
            # Build the SECOND proxy exclusively from cmd/go's direct VCS output,
            # not from the staging artifact or a shared download cache.
            prefix = public / record["proxy_path"]
            prefix.parent.mkdir(parents=True)
            for key, suffix in (("Zip", ".zip"), ("GoMod", ".mod"), ("Info", ".info")):
                shutil.copyfile(direct[key], str(prefix) + suffix)
            for label, proxy in (("staged", self.temp / "squashed"), ("public", public)):
                data = self.download(f"{label}-{index}", proxy.as_uri(), NS + "/" + path, version)
                self.assertEqual((data["Sum"], data["GoModSum"]), (record["sum"], record["go_mod_sum"]))
            with zipfile.ZipFile(direct["Zip"]) as archive:
                prefix = NS + "/" + path + "@" + version + "/"
                names = {n.removeprefix(prefix) for n in archive.namelist()}
                self.assertIn("go.mod", names)
                expected_license = b"repository license\n" if path == DEP else b"module-specific license\n"
                self.assertEqual(archive.read(prefix + "LICENSE"), expected_license)
                if path == DEP:
                    self.assertTrue({"data.txt", ".hidden"} <= names)
                    self.assertFalse(any(n.startswith(("nested/", "vendor/")) or n == "link" for n in names))
        # Offline standalone builds resolve the real new sibling API from either
        # independent proxy, with identical final manifests and no replacements.
        for label, proxy in (("staged", self.temp / "squashed"), ("public", public)):
            _, env = self.fresh_env("build-" + label, proxy.as_uri())
            with release.source_tree(f.root, squash) as source:
                before = {p: p.read_bytes() for p in (source / APP).glob("go.*")}
                release.run(source / APP, "go", "mod", "tidy", env=env)
                release.run(source / APP, "go", "test", "-mod=readonly", "./...", env=env)
                self.assertEqual(before, {p: p.read_bytes() for p in (source / APP).glob("go.*")})
        print("evidence: dependency-first final sums; one squash SHA; x/mod VCS == cmd/go VCS == two fresh proxy downloads (zip and go.mod h1)")

    def test_missing_unselected_wrong_version_and_checksum_tamper(self):
        final, records = self.prepare()
        # No implicit publication or version expansion, and no fallback.
        with self.assertRaisesRegex(release.CheckError, "selection changed"):
            self.stage(final, {DEP: VERSIONS[DEP]}, "changed-selection", records)
        self.stage(final, {DEP: VERSIONS[DEP]}, "selected-only")
        for index, (path, version) in enumerate(((APP, VERSIONS[APP]), (DEP, "v0.99.0"))):
            with self.assertRaises(release.CheckError):
                self.download(f"missing-{index}", (self.temp / "selected-only").as_uri(), NS + "/" + path, version)
        f = self.f
        f.write(DEP + "/data.txt", "tampered content\n")
        changed = f.commit()
        with self.assertRaisesRegex(release.CheckError, "checksum/identity mismatch"):
            self.stage(changed, VERSIONS, "mismatch", records)
        self.assertFalse((self.temp / "mismatch").exists())
        # Existing final sums detect a corrupted staged dependency using real Go.
        self.stage(changed, VERSIONS, "tampered")
        _, env = self.fresh_env("tampered-download", (self.temp / "tampered").as_uri())
        with release.source_tree(f.root, final) as source:
            with self.assertRaisesRegex(release.CheckError, "checksum mismatch"):
                release.run(source / APP, "go", "mod", "download", env=env)

    def test_invalid_inputs_replacements_and_stale_pin(self):
        sha = self.f.commit(allow_empty=True)
        for index, batch in enumerate(({}, {"unknown": "v0.1.0"}, {DEP: "v2.0.0"},
                                       {DEP: "v0.2.0", APP: VERSIONS[APP]})):
            with self.assertRaises(release.CheckError):
                self.stage(sha, batch, "invalid-" + str(index))
        self.f.write(DEP + "/go.mod", f"module {NS}/{DEP}\n\ngo 1.27\nreplace example.com/other => ../other\n")
        replaced = self.f.commit()
        with self.assertRaisesRegex(release.CheckError, "replace"):
            self.stage(replaced, {DEP: VERSIONS[DEP]}, "replaced")
        for index, flags in enumerate((("-sha", "HEAD"), ("-module", "example.com/wrong"),
                                       ("-dir", "../escape"), ("-dir", "--output=escape"),
                                       ("-version", "v0.2.0+build"), ("-sha", replaced))):
            args = [str(self.helper), "-repo", str(self.f.root), "-sha", sha, "-dir", DEP,
                    "-module", NS + "/" + DEP, "-version", VERSIONS[DEP],
                    "-out", str(self.temp / ("bad-" + str(index))), *flags]
            with self.assertRaises(release.CheckError):
                release.run(self.f.root, *args, env=self.f.env)
        # A different type of Git object is not an immutable commit input.
        with self.assertRaises(release.CheckError):
            self.stage(self.f.git("rev-parse", sha + "^{tree}"), {DEP: VERSIONS[DEP]}, "tree")
        self.stage(sha, {DEP: VERSIONS[DEP]}, "once")
        with self.assertRaises(FileExistsError):
            self.stage(sha, {DEP: VERSIONS[DEP]}, "once")

    def test_export_subst_is_not_tree_invariant_and_cannot_reuse_evidence(self):
        # Git export-subst can make equal trees produce unequal release bytes.
        # Canonical public behavior must be preserved, not "fixed" by ignoring it.
        self.f.write(DEP + "/.gitattributes", "revision.txt export-subst\n")
        self.f.write(DEP + "/revision.txt", "$Format:%H$\n")
        sha = self.f.commit()
        records = self.stage(sha, {DEP: VERSIONS[DEP]}, "subst-original")
        tree = self.f.git("rev-parse", sha + "^{tree}")
        squash = self.f.git("commit-tree", tree, "-p", self.base, "-m", "squash")
        with self.assertRaisesRegex(release.CheckError, "checksum/identity mismatch"):
            self.stage(squash, {DEP: VERSIONS[DEP]}, "subst-squash", records)


if __name__ == "__main__":
    unittest.main()
