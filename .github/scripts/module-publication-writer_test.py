#!/usr/bin/env python3
"""Offline writer tests through main and a fake urllib GitHub transport."""

import base64
import contextlib
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock
import urllib.error
import urllib.parse

sys.dont_write_bytecode = True
SPEC = importlib.util.spec_from_file_location(
    "publication", Path(__file__).with_name("module-publication.py"))
p = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(p)


def digest(data):
    return hashlib.sha1(json.dumps(data, sort_keys=True).encode()).hexdigest()


class FakeGitHub:
    def __init__(self):
        self.blobs, self.trees, self.commits, self.refs, self.calls, self.pulls = {}, {}, {}, {}, [], []
        self.fail_tag = None
        self.redirect = False
        self.truncated = False
        entries = {}
        for directory in p.ORDER + p.CONSUMERS:
            path = str(Path(directory) / "go.mod")
            text = f"module {p.identity(directory)}\n\ngo 1.27.3\n"
            if directory != "engine":
                text += f"\nrequire {p.identity('engine')} v0.1.0\n"
            entries[path] = self.blob(path, text)
        entries["engine/code.go"] = self.blob("engine/code.go", "package engine\n")
        tree = self.store_tree(entries)
        self.source = self.commit(tree, [])
        self.refs["heads/main"] = self.source

    def blob(self, path, text, mode="100644"):
        raw = text.encode()
        sha = hashlib.sha1(b"blob " + str(len(raw)).encode() + b"\0" + raw).hexdigest()
        self.blobs[sha] = raw
        return {"path": path, "type": "blob", "mode": mode, "sha": sha, "size": len(raw)}

    def store_tree(self, entries):
        sha = digest(entries)
        self.trees[sha] = entries
        return sha

    def commit(self, tree, parents, **extra):
        data = {"tree": {"sha": tree}, "parents": [{"sha": sha} for sha in parents],
                "committer": {"date": "2026-01-01T00:00:00Z"}, **extra}
        sha = digest(data)
        self.commits[sha] = data
        return sha

    def ancestor(self, base, head):
        return base == head or any(self.ancestor(base, parent["sha"])
                                   for parent in self.commits[head]["parents"])

    def entries(self, commit):
        return self.trees[self.commits[commit]["tree"]["sha"]]

    def text(self, commit, path):
        return self.blobs[self.entries(commit)[path]["sha"]].decode()

    def error(self, request, code):
        raise urllib.error.HTTPError(request.full_url, code, "private error body", {},
                                     io.BytesIO(b"DO NOT LOG ERROR BODY"))

    def open(self, request, timeout):
        assert request.full_url.startswith("https://api.github.com/repos/stacklok/mecatl/")
        assert request.headers["Authorization"] == "Bearer fixture-not-a-secret"
        assert timeout == 60
        path = urllib.parse.unquote(request.full_url.split("/mecatl/", 1)[1])
        method = request.method
        data = json.loads(request.data) if request.data else None
        self.calls.append((method, path, data))
        if self.redirect:
            self.error(request, 302)
        if method == "GET" and path.startswith("git/ref/"):
            name = path.removeprefix("git/ref/")
            if name not in self.refs:
                self.error(request, 404)
            result = {"ref": "refs/" + name, "object": {"type": "commit", "sha": self.refs[name]}}
        elif method == "GET" and path.startswith("compare/"):
            base, head = path.removeprefix("compare/").split("...")
            result = {"status": "identical" if base == head else
                      "ahead" if self.ancestor(base, head) else "diverged"}
        elif method == "GET" and path.startswith("git/commits/"):
            result = self.commits[path.rsplit("/", 1)[1]]
        elif method == "GET" and path.startswith("git/trees/"):
            tree = path.rsplit("/", 1)[1].split("?")[0]
            result = {"tree": list(self.trees[tree].values()), "truncated": self.truncated}
        elif method == "GET" and path.startswith("git/blobs/"):
            raw = self.blobs[path.rsplit("/", 1)[1]]
            result = {"content": base64.b64encode(raw).decode(), "encoding": "base64", "size": len(raw)}
        elif method == "POST" and path == "git/trees":
            entries = self.trees[data["base_tree"]].copy()
            for entry in data["tree"]:
                if entry.get("sha", "not-null") is None:
                    entries.pop(entry["path"])
                else:
                    entries[entry["path"]] = self.blob(entry["path"], entry["content"], entry["mode"])
            result = {"sha": self.store_tree(entries)}
        elif method == "POST" and path == "git/commits":
            result = {"sha": self.commit(data["tree"], data["parents"], message=data["message"])}
        elif method == "POST" and path == "git/refs":
            name = data["ref"].removeprefix("refs/")
            if name in self.refs or name == self.fail_tag:
                self.error(request, 422)
            self.refs[name] = data["sha"]
            result = {}
        elif method == "PATCH" and path.startswith("git/refs/"):
            name = path.removeprefix("git/refs/")
            assert data["force"] is False
            if not self.ancestor(self.refs[name], data["sha"]):
                self.error(request, 422)
            self.refs[name] = data["sha"]
            result = {}
        elif method == "POST" and path == "pulls":
            self.pulls.append(data)
            result = {"number": len(self.pulls)}
        else:
            raise AssertionError((method, path, data))
        return io.BytesIO(json.dumps(result).encode())


class WriterTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.metadata = self.root / "metadata.json"
        self.output = self.root / "output"
        self.summary = self.root / "summary"
        self.api = FakeGitHub()
        self.source = self.api.source
        self.parent = self.source
        self.branch = "module-publication/123-1"
        self.versions = {"engine": "v0.2.0"}
        self.files = {}
        self.logs = ""

    def call(self, command="publish", module="engine", raw=None):
        self.metadata.write_text(raw if raw is not None else
                                 json.dumps({"parent": self.parent, "files": self.files}))
        self.output.write_text("")
        args = [command, "--source", self.source, "--parent", self.parent,
                "--versions", json.dumps(self.versions), "--branch", self.branch,
                "--metadata", str(self.metadata)]
        if command == "publish":
            args += ["--module", module]
        with mock.patch.object(p.urllib.request, "build_opener", return_value=self.api), \
                mock.patch.object(p.subprocess, "run", side_effect=AssertionError("writer subprocess")), \
                mock.patch.object(p, "environment", side_effect=AssertionError("writer Go environment")), \
                mock.patch.dict(os.environ, {"GH_TOKEN": "fixture-not-a-secret", "GITHUB_OUTPUT": str(self.output),
                                             "GITHUB_STEP_SUMMARY": str(self.summary)}, clear=True), \
                contextlib.redirect_stdout(io.StringIO()) as stdout, \
                contextlib.redirect_stderr(io.StringIO()) as stderr:
            result = p.main(args)
        self.logs = stdout.getvalue() + stderr.getvalue()
        return result

    def head(self):
        return self.output.read_text().strip().split("=", 1)[1]

    def prepare_bytes(self, module="engine"):
        self.files = {f"{module}/go.mod": self.api.text(self.parent, f"{module}/go.mod").replace(
            "v0.1.0", "v0.2.0"), f"{module}/go.sum": "fixture v0.1.0 h1:tested\n"}

    def final_metadata(self):
        self.files = {}
        for directory in p.ORDER + p.CONSUMERS:
            path = str(Path(directory) / "go.mod")
            text = self.api.text(self.parent, path)
            desired = text.replace("v0.1.0", "v0.2.0")
            if desired != text:
                self.files[path] = desired

    def test_exact_bytes_only_lightweight_tag_and_retry_requires_manual_recovery(self):
        self.prepare_bytes()
        expected = self.api.entries(self.parent).copy()
        self.assertEqual(self.call(), 0, self.logs)
        head = self.head()
        self.assertNotEqual(head, self.parent)
        for path, text in self.files.items():
            self.assertEqual(self.api.text(head, path).encode(), text.encode())
        for path in expected.keys() - self.files.keys():
            self.assertEqual(self.api.entries(head)[path], expected[path])
        self.assertEqual(self.api.refs["tags/engine/v0.2.0"], head)
        self.assertEqual(self.api.refs["heads/" + self.branch], head)
        self.assertIn(head, self.summary.read_text())
        self.assertIn("Published immutable tag", self.logs)
        self.api.calls.clear()
        self.assertEqual(self.call(), 1, self.logs)
        self.assertIn("recover manually", self.logs)
        self.assertEqual(self.output.read_text(), "")
        self.assertFalse(any(method != "GET" for method, _, _ in self.api.calls))
        self.assertEqual(self.api.refs["heads/" + self.branch], head)
        self.assertEqual(self.api.refs["tags/engine/v0.2.0"], head)

    def test_pseudo_versions_fail_before_transport_or_writes(self):
        for value in ("v0.0.0-20260101000000-aaaaaaaaaaaa",
                      "v1.2.4-0.20260101000000-aaaaaaaaaaaa",
                      "v1.2.3-pre.0.20260101000000-aaaaaaaaaaaa"):
            for command in ("publish", "open-pr"):
                with self.subTest(value=value, command=command):
                    self.versions = {"engine": value}
                    with mock.patch.object(p, "GitHub", side_effect=AssertionError("transport constructed")):
                        self.assertEqual(self.call(command), 1)
                    self.assertEqual(self.api.calls, [])
                    self.assertEqual(self.output.read_text(), "")
                    self.assertEqual(self.api.refs, {"heads/main": self.source})

    def test_existing_tag_at_expected_parent_fails_before_writes(self):
        self.api.refs["tags/engine/v0.2.0"] = self.parent
        self.assertEqual(self.call(), 1, self.logs)
        self.assertFalse(any(method != "GET" for method, _, _ in self.api.calls))
        self.assertNotIn("heads/" + self.branch, self.api.refs)

    def test_no_change_tags_parent(self):
        self.assertEqual(self.call(), 0, self.logs)
        self.assertEqual(self.head(), self.source)
        self.assertFalse(any(path in ("git/trees", "git/commits") for _, path, _ in self.api.calls))

    def test_path_and_schema_tamper_fail_without_writes(self):
        cases = [({"engine/code.go": "bad"}, None), ({"../go.mod": "bad"}, None),
                 ({"provider/openai/go.sum": "bad"}, None), ({"engine/go.mod": None}, None),
                 ({"engine/go.sum": 42}, None), ({}, '{"parent":"x","files":{},"extra":1}'),
                 ({}, '{"parent":"x","files":{},"files":{}}')]
        for files, raw in cases:
            with self.subTest(files=files, raw=raw):
                self.files = files
                self.api.calls.clear()
                self.assertEqual(self.call(raw=raw), 1)
                self.assertFalse(any(method != "GET" for method, _, _ in self.api.calls))
                self.assertEqual(self.output.read_text(), "")

    def test_artifact_parent_mismatch_fails_before_transport(self):
        stale = json.dumps({"parent": "f" * 40, "files": {}})
        self.assertNotEqual("f" * 40, self.parent)
        self.assertEqual(self.call(raw=stale), 1, self.logs)
        self.assertEqual(self.api.calls, [])
        self.assertEqual(self.output.read_text(), "")
        self.assertEqual(self.api.refs, {"heads/main": self.source})

    def test_ref_misuse_and_unselected_writer_fail_without_transport(self):
        for branch in ("main", "module-publication/1-1/extra", "module-publication/01-1",
                       "module-publication/1-1\nrefs/heads/main"):
            with self.subTest(branch=branch):
                self.branch = branch
                self.assertEqual(self.call(), 1, self.logs)
                self.assertEqual(self.api.calls, [])
        self.branch = "module-publication/123-1"
        self.assertEqual(self.call(module="provider/openai"), 1, self.logs)
        self.assertEqual(self.api.calls, [])

    def test_incoming_artifact_and_file_size_bounds(self):
        self.files = {"engine/go.sum": "x" * (4 * 1024 * 1024 + 1)}
        self.assertEqual(self.call(), 1, self.logs)
        self.assertEqual(self.api.calls, [])
        self.assertEqual(self.call(raw=" " * (16 * 1024 * 1024 + 1)), 1, self.logs)
        self.assertEqual(self.api.calls, [])

    def test_writer_accepts_semantically_surprising_metadata_as_data(self):
        self.files = {"engine/go.mod": 'module "surprising.invalid/identity"\n'
                      'go 1.99\ntoolchain go1.99.1\nreplace x => ../x\n'
                      f'require {p.identity("provider/openai")} v0.99.0\n',
                      "engine/go.sum": "not even a valid checksum file\n"}
        self.assertEqual(self.call(), 0, self.logs)
        for path, text in self.files.items():
            self.assertEqual(self.api.text(self.head(), path), text)
            self.assertEqual(self.api.entries(self.head())[path]["mode"], "100644")
        blobs = [path for method, path, _ in self.api.calls
                 if method == "GET" and path.startswith("git/blobs/")]
        self.assertEqual(len(blobs), 1, "only read an existing blob for no-op detection")

    def test_parent_source_code_delta_is_rejected(self):
        for change in ("contents", "mode", "deletion"):
            with self.subTest(change=change):
                entries = self.api.entries(self.source).copy()
                if change == "deletion":
                    del entries["engine/code.go"]
                else:
                    entries["engine/code.go"] = self.api.blob(
                        "engine/code.go", "evil" if change == "contents" else "package engine\n",
                        "100755" if change == "mode" else "100644")
                self.parent = self.api.commit(self.api.store_tree(entries), [self.source])
                self.api.refs["heads/" + self.branch] = self.parent
                self.api.calls.clear()
                self.assertEqual(self.call(), 1, self.logs)
                self.assertIn("parent changed reviewed source", self.logs)
                self.assertFalse(any(method != "GET" for method, _, _ in self.api.calls))
        self.source = "not-a-full-sha"
        self.assertEqual(self.call(), 1)

    def test_non_main_source_truncated_tree_and_non_descendant_parent(self):
        self.api.truncated = True
        self.assertEqual(self.call(), 1)
        self.api.truncated = False
        unrelated = self.api.commit(self.api.commits[self.source]["tree"]["sha"], [], message="other")
        self.parent = unrelated
        self.assertEqual(self.call(), 1)
        self.source = unrelated
        self.assertEqual(self.call(), 1)

    def test_branch_movement_and_conflicting_tag_never_overwrite_refs(self):
        self.prepare_bytes()
        moved = self.api.commit(self.api.commits[self.source]["tree"]["sha"], [self.source], message="moved")
        self.api.refs["heads/" + self.branch] = moved
        self.assertEqual(self.call(), 1)
        self.assertEqual(self.api.refs["heads/" + self.branch], moved)
        self.assertFalse(any(method == "POST" and path == "git/commits" for method, path, _ in self.api.calls))
        self.assertNotIn("tags/engine/v0.2.0", self.api.refs)
        self.api.refs["heads/" + self.branch] = self.source
        self.api.refs["tags/engine/v0.2.0"] = moved
        self.api.calls.clear()
        self.assertEqual(self.call(), 1)
        self.assertFalse(any(method != "GET" for method, _, _ in self.api.calls))
        self.assertEqual(self.api.refs["tags/engine/v0.2.0"], moved)
        self.assertEqual(self.api.refs["heads/" + self.branch], self.source)

    def test_missing_branch_after_first_parent_fails(self):
        self.parent = self.api.commit(self.api.commits[self.source]["tree"]["sha"], [self.source], message="next")
        self.assertEqual(self.call(), 1)
        self.assertNotIn("heads/" + self.branch, self.api.refs)

    def test_publications_then_final_adoption_pr(self):
        self.versions["provider/openai"] = "v0.2.0"
        self.files = {}
        self.assertEqual(self.call(), 0, self.logs)
        engine = self.head()
        self.parent = engine
        self.prepare_bytes("provider/openai")
        self.assertEqual(self.call(module="provider/openai"), 0, self.logs)
        self.parent = self.head()
        self.final_metadata()
        self.assertEqual(self.call("open-pr"), 0, self.logs)
        self.assertEqual(len(self.api.pulls), 1)
        self.assertEqual(self.api.pulls[0]["base"], "main")
        self.assertIn("HUMAN MERGE COMMIT required", self.api.pulls[0]["body"])
        self.assertIn("Do not squash or rebase", self.api.pulls[0]["body"])
        self.assertTrue(self.api.ancestor(engine, self.head()))
        self.assertEqual(self.api.text(self.head(), "go.mod"), self.files["go.mod"])

    def test_writer_does_not_revalidate_predecessor_tags_or_pins(self):
        self.versions["provider/openai"] = "v0.2.0"
        other = self.api.commit(self.api.commits[self.source]["tree"]["sha"], [self.source], message="other")
        self.api.refs["tags/engine/v0.2.0"] = other
        self.files = {"provider/openai/go.mod": self.api.text(self.parent, "provider/openai/go.mod")}
        self.assertEqual(self.call(module="provider/openai"), 0, self.logs)
        self.assertEqual(self.head(), self.parent)
        self.assertFalse(any(path == "git/ref/tags/engine/v0.2.0" for _, path, _ in self.api.calls))
        del self.api.refs["tags/engine/v0.2.0"]
        self.files = {}
        self.assertEqual(self.call("open-pr"), 0, self.logs)
        self.assertEqual(len(self.api.pulls), 1)

    def test_no_metadata_delta_still_needs_pr_when_release_commit_is_not_main(self):
        self.versions["engine"] = "v0.1.0"
        self.prepare_bytes()
        self.assertEqual(self.call(), 0, self.logs)
        self.parent = self.head()
        self.files = {}
        self.assertEqual(self.call("open-pr"), 0, self.logs)
        self.assertEqual(len(self.api.pulls), 1)

    def test_existing_metadata_modes_are_not_blanket_validated_but_writes_are_regular(self):
        entries = self.api.entries(self.source).copy()
        entries["engine/go.sum"] = self.api.blob("engine/go.sum", "target", "120000")
        entries["go.sum"] = self.api.blob("go.sum", "untouched", "100755")
        self.parent = self.api.commit(self.api.store_tree(entries), [self.source])
        self.api.refs["heads/" + self.branch] = self.parent
        self.files = {"engine/go.sum": "target"}
        self.assertEqual(self.call(), 0, self.logs)
        self.assertEqual(self.api.entries(self.head())["engine/go.sum"]["mode"], "100644")
        self.assertEqual(self.api.entries(self.head())["go.sum"], entries["go.sum"])
        self.assertFalse(any(path.startswith("git/blobs/") for _, path, _ in self.api.calls))

    def test_branch_is_rechecked_before_fast_forward(self):
        self.prepare_bytes()
        self.api.refs["heads/" + self.branch] = self.source
        original = self.api.open
        reads = 0
        moved = self.api.commit(self.api.commits[self.source]["tree"]["sha"], [self.source], message="movement")
        def move(request, timeout):
            nonlocal reads
            if request.full_url.endswith("git/ref/heads/" + self.branch):
                reads += 1
                if reads == 2:
                    self.api.refs["heads/" + self.branch] = moved
            return original(request, timeout)
        self.api.open = move
        self.assertEqual(self.call(), 1)
        self.assertEqual(self.api.refs["heads/" + self.branch], moved)
        self.assertNotIn("tags/engine/v0.2.0", self.api.refs)

    def test_failed_public_proof_has_no_head_for_the_next_call(self):
        self.assertEqual(self.call(), 0)
        published = self.head()
        self.output.write_text("")
        calls = list(self.api.calls)
        with mock.patch.object(p, "prove", side_effect=p.CheckError("public proof failed")), \
                mock.patch.dict(os.environ, {"GITHUB_OUTPUT": str(self.output)}), \
                contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(p.main(["prove", "--module", "engine", "--versions", json.dumps(self.versions),
                                     "--head", published]), 1)
        self.assertEqual(self.output.read_text(), "")
        self.assertEqual(self.api.calls, calls)
        self.assertEqual(self.api.refs["tags/engine/v0.2.0"], published)
        self.assertFalse(self.api.pulls)

    def test_partial_failure_reports_commits_retains_previous_tags_and_no_pr(self):
        self.versions["provider/openai"] = "v0.2.0"
        self.assertEqual(self.call(), 0)
        engine = self.head()
        self.parent = engine
        self.prepare_bytes("provider/openai")
        self.api.fail_tag = "tags/provider/openai/v0.2.0"
        self.assertEqual(self.call(module="provider/openai"), 1)
        self.assertEqual(self.api.refs["tags/engine/v0.2.0"], engine)
        self.assertNotIn(self.api.fail_tag, self.api.refs)
        self.assertNotEqual(self.api.refs["heads/" + self.branch], engine)
        self.assertIn(self.api.refs["heads/" + self.branch], self.logs)
        self.assertNotIn("DO NOT LOG ERROR BODY", self.logs)
        self.assertNotIn("fixture-not-a-secret", self.logs)
        self.assertFalse(self.api.pulls)
        self.assertEqual(self.output.read_text(), "")
        partial = self.api.refs["heads/" + self.branch]
        self.api.fail_tag = None
        self.api.calls.clear()
        self.assertEqual(self.call(module="provider/openai"), 1, self.logs)
        self.assertIn("recover manually", self.logs)
        self.assertFalse(any(method != "GET" for method, _, _ in self.api.calls))
        self.assertEqual(self.api.refs["heads/" + self.branch], partial)
        self.assertEqual(self.api.refs["tags/engine/v0.2.0"], engine)
        self.assertNotIn("tags/provider/openai/v0.2.0", self.api.refs)
        self.assertFalse(self.api.pulls)

    def test_no_pr_only_when_all_commits_on_main_and_no_changes(self):
        self.versions["engine"] = "v0.1.0"
        self.assertEqual(self.call(), 0)
        self.assertEqual(self.call("open-pr"), 0, self.logs)
        self.assertFalse(self.api.pulls)
        self.versions["engine"] = "v0.2.0"
        self.prepare_bytes()
        self.assertEqual(self.call(), 0)
        self.parent = self.head()
        self.final_metadata()
        self.assertEqual(self.call("open-pr"), 0, self.logs)
        self.assertEqual(len(self.api.pulls), 1)

    def test_prepared_and_tested_artifact_is_exact_api_tree(self):
        spec = importlib.util.spec_from_file_location(
            "publication_readonly_tests", Path(__file__).with_name("module-publication_test.py"))
        readonly = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(readonly)
        fixture = readonly.LocalTests(methodName="runTest")
        self.addCleanup(fixture.doCleanups)
        fixture.setUp()
        fixture.require("provider/openai")
        fixture.commit()
        # Snapshot API source before trusted preparation changes any metadata.
        entries = {}
        for path in fixture.command("git", "ls-files").splitlines():
            entries[path] = self.api.blob(path, (fixture.root / path).read_text())
        tree = self.api.store_tree(entries)
        self.api.commits[fixture.head] = {"tree": {"sha": tree}, "parents": [],
                                         "committer": {"date": "2026-01-01T00:00:00Z"}}
        self.source = self.parent = fixture.head
        self.api.refs["heads/main"] = self.source
        self.versions["provider/openai"] = "v0.2.0"
        prepared = self.root / "tested.json"
        with mock.patch.object(p, "environment", return_value=fixture.env), \
                mock.patch.dict(os.environ, {"GITHUB_OUTPUT": str(self.output)}):
            self.assertEqual(p.main(["prepare", "--root", str(fixture.root),
                                     "--source", self.source, "--parent", self.parent,
                                     "--module", "provider/openai", "--versions", json.dumps(self.versions),
                                     "--metadata", str(prepared)]), 0)
        self.assertEqual(self.call(), 0, self.logs)
        self.parent = self.head()
        data = json.loads(prepared.read_text())
        # First module had no metadata delta; consumer was tested at that parent.
        self.assertEqual(data["parent"], self.parent)
        self.files = data["files"]
        self.assertEqual(self.call(module="provider/openai"), 0, self.logs)
        published = self.head()
        for path in fixture.command("git", "ls-files").splitlines():
            self.assertEqual(self.api.text(published, path).encode(), (fixture.root / path).read_bytes())

    def test_redirects_are_not_followed_and_errors_are_sanitized(self):
        handler = p.NoRedirect()
        self.assertIsNone(handler.redirect_request(None, None, 302, "", {}, "https://evil.invalid"))
        self.api.redirect = True
        self.assertEqual(self.call(), 1)
        self.assertEqual(len(self.api.calls), 1)
        self.assertIn("HTTP 302", self.logs)
        self.assertNotIn("DO NOT LOG ERROR BODY", self.logs)
        self.api.open = mock.Mock(side_effect=ValueError("Invalid header value fixture-not-a-secret"))
        self.assertEqual(self.call(), 1)
        self.assertNotIn("fixture-not-a-secret", self.logs)
        self.assertIn("transport or response failed", self.logs)


@unittest.skipUnless(os.environ.get("PUBLICATION_WORKFLOW"), "commands supplied by parsed Go workflow test")
class WorkflowTests(unittest.TestCase):
    """Bounded call chain: actual shell commands/main/writers/prove, offline I/O seams."""

    setUp = WriterTests.setUp

    def command(self, operation, module, parent, published=None):
        spec = json.loads(os.environ["PUBLICATION_WORKFLOW"])
        step = spec["commands"][operation]
        bindings = {"${{ inputs.source }}": self.source,
                    "${{ inputs.versions }}": json.dumps(self.versions),
                    "${{ inputs.module }}": module, "${{ inputs.parent }}": parent,
                    "${{ inputs.branch }}": self.branch,
                    "${{ needs.preflight.outputs.source }}": self.source,
                    "${{ needs.preflight.outputs.versions }}": json.dumps(self.versions),
                    "${{ needs.preflight.outputs.branch }}": self.branch,
                    "${{ needs.adapters.outputs.head }}": parent,
                    "${{ steps.app.outputs.token }}": "fixture-not-a-secret",
                    "${{ needs.prepare.outputs.selected == 'true' && needs.publish.outputs.head || inputs.parent }}":
                        published if module in self.versions else parent}
        env = {"PATH": str(self.root) + os.pathsep + os.environ["PATH"],
               "GITHUB_OUTPUT": str(self.output), "GITHUB_STEP_SUMMARY": str(self.summary),
               "GITHUB_WORKSPACE": str(self.root), "RUNNER_TEMP": str(self.root),
               "CAPTURE": str(self.root / "argv.json")}
        for scope in (spec["environments"][operation], step["Env"] or {}):
            env.update({key: bindings[value] for key, value in scope.items()})
        # Execute the parsed YAML shell, intercepting only its Python executable.
        # Calling main below preserves shared fake API state without a server.
        stub = self.root / "python3"
        stub.write_text(f"#!{sys.executable}\nimport json, os, sys\n"
                        "with open(os.environ['CAPTURE'], 'w') as f: json.dump(sys.argv[1:], f)\n")
        stub.chmod(0o700)
        capture = Path(env["CAPTURE"])
        capture.unlink(missing_ok=True)
        self.output.write_text("")
        shell = subprocess.run(["/bin/sh", "-eu", "-c", step["Run"]], env=env,
                               cwd=self.root, capture_output=True, text=True, timeout=10)
        self.assertEqual(shell.returncode, 0, shell.stderr)
        self.assertTrue(capture.exists(), "workflow bypassed the publication helper")
        argv = json.loads(capture.read_text())
        self.assertEqual(argv[:2], ["tooling/.github/scripts/module-publication.py", operation])
        if operation != "prove":
            self.assertEqual(argv[argv.index("--parent") + 1], parent,
                             "workflow parent does not match preceding proof")
        self.events.append((operation, module))
        with mock.patch.dict(os.environ, env, clear=True), \
                contextlib.redirect_stdout(io.StringIO()) as stdout, \
                contextlib.redirect_stderr(io.StringIO()) as stderr:
            result = p.main(argv[1:])
        self.logs = stdout.getvalue() + stderr.getvalue()
        outputs = dict(line.split("=", 1) for line in self.output.read_text().splitlines())
        return result, outputs

    def flow(self, fail_proof=False, fail_write=False):
        self.versions = {"engine": "v0.2.0", "provider/openai": "v0.2.0", "adapters": "v0.2.0"}
        self.events, self.proven = [], set()
        if fail_write:
            self.api.fail_tag = "tags/provider/openai/v0.2.0"
        (self.root / "prepared").mkdir()
        current = {}

        def prepare(args, versions, env):
            self.assertEqual(args.parent, parent)
            if args.module != "engine":
                self.assertIn("engine", self.proven, "consumer prepared before dependency public proof")
            files = {f"{args.module}/go.sum": "fixture v0.1.0 h1:tested\n"}
            path = f"{args.module}/go.mod"
            files[path] = self.api.text(args.parent, path).replace("v0.1.0", "v0.2.0")
            args.metadata.write_text(json.dumps({"parent": args.parent, "files": files}))
            (self.root / "prepared/metadata.json").write_bytes(args.metadata.read_bytes())
            return {"selected": "true"}

        def reconcile(args, versions, env):
            self.assertEqual(self.proven, set(versions))
            files = {}
            for directory in p.ORDER + p.CONSUMERS:
                path = str(Path(directory) / "go.mod")
                text = self.api.text(args.parent, path)
                desired = text.replace("v0.1.0", "v0.2.0")
                if desired != text:
                    files[path] = desired
            args.metadata.write_text(json.dumps({"parent": args.parent, "files": files}))
            (self.root / "prepared/metadata.json").write_bytes(args.metadata.read_bytes())
            return {}

        def download(command, directory, env, timeout):
            self.assertEqual(command[:4], ["go", "mod", "download", "-json"])
            module, exact = command[4].split("@")
            path = module.removeprefix(p.PREFIX + "/")
            self.events.append(("public-download", path))
            if fail_proof:
                raise p.CheckError("public propagation failed")
            current.update(Path=module, Version=exact, directory=path)
            return json.dumps({"Path": module, "Version": exact, "GoMod": "fixture.mod",
                               "Origin": {"VCS": "git", "URL": p.REPOSITORY,
                                          "Ref": "refs/tags/" + path + "/" + exact,
                                          "Hash": self.api.refs["tags/" + path + "/" + exact]}})

        def go(directory, env, *args):
            if args[:3] == ("mod", "edit", "-json"):
                return json.dumps({"Module": {"Path": current["Path"]}})
            if args[:3] == ("list", "-m", "-json"):
                return json.dumps({"Path": current["Path"], "Version": current["Version"]})
            if args == ("build", "-mod=readonly", "./..."):
                self.proven.add(current["directory"])
                self.events.append(("public-build", current["directory"]))
            else:
                self.assertEqual(args, ("mod", "tidy"))
            return ""

        parent = self.source
        with mock.patch.object(p.urllib.request, "build_opener", return_value=self.api), \
                mock.patch.object(p, "prepare", side_effect=prepare), \
                mock.patch.object(p, "reconcile", side_effect=reconcile), \
                mock.patch.object(p, "run", side_effect=download), \
                mock.patch.object(p.candidate, "go", side_effect=go), \
                mock.patch.object(p.time, "sleep"):
            for module in p.ORDER:
                result, selected = self.command("prepare", module, parent)
                self.assertEqual(result, 0)
                published = None
                if selected["selected"] == "true":
                    result, output = self.command("publish", module, parent)
                    if result:
                        self.assertEqual(output, {})
                        return "write"
                    published = output["head"]
                result, output = self.command("prove", module, parent, published)
                if result:
                    self.assertEqual(output, {})
                    return "proof"
                expected = published if module in self.versions else parent
                self.assertEqual(output["head"], expected)
                parent = output["head"]
            self.assertEqual(self.command("reconcile", "", parent)[0], 0)
            self.assertEqual(self.command("open-pr", "", parent)[0], 0)
        return "success"

    def test_public_proof_precedes_later_consumer_and_pr(self):
        self.assertEqual(self.flow(), "success", self.logs)
        self.assertEqual(self.proven, set(self.versions))
        self.assertLess(self.events.index(("public-build", "engine")),
                        self.events.index(("prepare", "provider/openai")))
        self.assertIn(("prove", "contracts/gen/go/mecatl/driver"), self.events)
        self.assertEqual(len(self.api.pulls), 1)
        self.assertEqual(len([ref for ref in self.api.refs if ref.startswith("tags/")]), 3)

    def test_public_failure_stops_before_next_preparation_or_write(self):
        self.assertEqual(self.flow(fail_proof=True), "proof")
        self.assertEqual([event for event in self.events if event[0] == "prepare"],
                         [("prepare", "agentdef"), ("prepare", "engine")])
        self.assertEqual([event for event in self.events if event[0] == "publish"], [("publish", "engine")])
        self.assertIn("tags/engine/v0.2.0", self.api.refs)
        self.assertFalse(self.api.pulls)
        self.assertNotIn(("reconcile", ""), self.events)

    def test_later_write_failure_retains_partial_tags_and_stops(self):
        self.assertEqual(self.flow(fail_write=True), "write")
        self.assertEqual(self.proven, {"engine"})
        self.assertIn("tags/engine/v0.2.0", self.api.refs)
        self.assertNotIn("tags/provider/openai/v0.2.0", self.api.refs)
        self.assertNotIn(("prepare", "adapters"), self.events)
        self.assertNotIn(("prove", "provider/openai"), self.events)
        self.assertNotIn(("reconcile", ""), self.events)
        self.assertFalse(self.api.pulls)


if __name__ == "__main__":
    unittest.main()
