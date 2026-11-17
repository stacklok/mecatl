#!/usr/bin/env python3
"""Offline coordinated controller tests: fake GitHub with real Git objects/refs.

Synthetic full-CI receipts below exercise the caller contract, NOT the full gate
suite. module-release-ci_test.py separately checks the reusable production fanout.
"""
from contextlib import contextmanager, redirect_stderr, redirect_stdout
import base64
import copy
from datetime import datetime, timezone
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import unittest
from unittest import mock
from urllib.parse import parse_qs, unquote, urlsplit
import zipfile

HERE = Path(__file__).resolve().parent
sys.dont_write_bytecode = True


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


gh = load("release_github", HERE / "module-release-github.py")
fixtures = load("release_fixtures", HERE / "module-release_test.py")
artifacts = load("artifact_fixtures", HERE / "module-artifact_test.py")
git = gh.release.git
DEP, APP = fixtures.DEP, fixtures.APP
HUMAN = dict(type="User", id=42, login="release-human")
BOT = dict(type="Bot", id=700, login="release[bot]")


class FakeGitHub(gh.GitHub):
    """API writes create actual Git data; no network or operator Git configuration."""
    def __init__(self, fixture):
        super().__init__("stacklok/mecatl", "offline-not-a-credential")
        self.f = fixture
        self.prs, self.runs, self.artifacts = {}, {}, {}
        self.writes, self.links, self.permissions = [], {}, {}
        self.clock = 0
        self.ref_lock = threading.Lock()
        self.before_write = None

    def stamp(self):
        self.clock += 1
        return f"2026-11-17T00:{self.clock // 60:02d}:{self.clock % 60:02d}Z"

    def call(self, method, path, data=None, missing=False):
        parsed = urlsplit(path)
        path, query = unquote(parsed.path), parse_qs(parsed.query)
        f, root = self.f, self.f.root
        if method != "GET":
            if self.before_write:
                self.before_write(method, path, data)
            self.writes.append((method, path, copy.deepcopy(data)))
        if path.startswith("/collaborators/"):
            return dict(permission=self.permissions.get(path.split("/")[2], "write"))
        if path.startswith("/actions/workflows/"):
            workflow = path.split("/")[3]
            if path.endswith("/dispatches"):
                return None
            if path.endswith("/runs"):
                values = [r for r in self.runs.values() if r["path"].endswith("/" + workflow)]
                return dict(workflow_runs=values if query.get("page", ["1"])[0] == "1" else [])
            return dict(id=30 if workflow == gh.REQUEST_WORKFLOW else 20)
        if path.startswith("/actions/runs/"):
            parts = path.split("/")
            run = self.runs[int(parts[3])]
            if path.endswith("/jobs"):
                return dict(jobs=run["_jobs"])
            if path.endswith("/artifacts"):
                return dict(artifacts=[a for a in self.artifacts.values() if a["run_id"] == run["id"]])
            return run
        if path.startswith("/actions/artifacts/"):
            return self.artifacts[int(path.split("/")[3])]["_zip"]
        if path.startswith("/compare/"):
            old, new = path.split("/")[-1].split("...")
            status = "identical" if old == new else "ahead" if subprocess.run(
                ["git", "merge-base", "--is-ancestor", old, new], cwd=root, env=f.env).returncode == 0 else "diverged"
            return dict(status=status)
        if path.startswith("/contents/"):
            result = subprocess.run(["git", "show", query["ref"][0] + ":" + path[len("/contents/"):]],
                                    cwd=root, env=f.env, capture_output=True)
            if result.returncode and missing:
                return None
            gh.require(result.returncode == 0, "missing fixture file")
            return dict(type="file", encoding="base64", content=base64.b64encode(result.stdout).decode())
        if path.startswith("/git/ref/"):
            name = "refs/" + path[len("/git/ref/"):]
            if method == "GET":
                result = subprocess.run(["git", "rev-parse", "--verify", name], cwd=root, env=f.env, capture_output=True, text=True)
                if result.returncode and missing:
                    return None
                gh.require(result.returncode == 0, "missing fixture ref")
                return dict(ref=name, object=dict(type="commit", sha=result.stdout.strip(), url="https://api.invalid/object"))
        if path.startswith("/git/matching-refs/heads/"):
            prefix = "refs/heads/" + path[len("/git/matching-refs/heads/"):]
            lines = git(root, "for-each-ref", "--format=%(refname) %(objectname)", prefix).splitlines()
            return [dict(ref=line.split()[0], object=dict(type="commit", sha=line.split()[1])) for line in lines]
        if path == "/git/refs" and method == "POST":
            with self.ref_lock:
                result = subprocess.run(["git", "update-ref", data["ref"], data["sha"], "0" * 40], cwd=root, env=f.env, capture_output=True)
                gh.require(result.returncode == 0, "atomic ref already exists")
            return {}
        if path.startswith("/git/refs/heads/") and method == "DELETE":
            git(root, "update-ref", "-d", "refs/heads/" + path[len("/git/refs/heads/"):])
            return None
        if path.startswith("/git/commits/"):
            commit = path.split("/")[-1]
            parents = git(root, "show", "-s", "--format=%P", commit).split()
            return dict(sha=commit, tree=dict(sha=git(root, "rev-parse", commit + "^{tree}")), parents=[dict(sha=p) for p in parents])
        if path.startswith("/git/trees/"):
            lines = git(root, "ls-tree", "-r", path.split("/")[-1]).splitlines()
            entries = []
            for line in lines:
                meta, name = line.split("\t")
                mode, kind, value = meta.split()
                entries.append(dict(path=name, mode=mode, type=kind, sha=value))
            return dict(truncated=False, tree=entries)
        if path == "/git/blobs":
            value = subprocess.check_output(["git", "hash-object", "-w", "--stdin"], cwd=root, env=f.env, input=data["content"].encode()).decode().strip()
            return dict(sha=value)
        if path == "/git/trees":
            with tempfile.TemporaryDirectory(dir=f.env["TMPDIR"]) as temporary:
                env = dict(f.env, GIT_INDEX_FILE=str(Path(temporary) / "index"))
                gh.release.run(root, "git", "read-tree", data["base_tree"], env=env)
                for entry in data["tree"]:
                    gh.release.run(root, "git", "update-index", "--add", "--cacheinfo", entry["mode"], entry["sha"], entry["path"], env=env)
                return dict(sha=gh.release.run(root, "git", "write-tree", env=env).strip())
        if path == "/git/commits":
            args = ["commit-tree", data["tree"]]
            for parent in data["parents"]:
                args.extend(["-p", parent])
            return dict(sha=git(root, *args, "-m", data["message"]))
        if path == "/pulls" and method == "GET":
            values = list(self.prs.values())
            if "head" in query:
                values = [p for p in values if p["head"]["ref"] == query["head"][0].split(":", 1)[1]]
            if "base" in query:
                values = [p for p in values if p["base"]["ref"] == query["base"][0]]
            return values if query.get("page", ["1"])[0] == "1" else []
        if path == "/pulls" and method == "POST":
            number = len(self.prs) + 1
            pr = dict(number=number, user=copy.deepcopy(BOT), merged=False, state="open", merged_by=None,
                      merge_commit_sha=None, head=dict(ref=data["head"], sha=git(root, "rev-parse", "refs/heads/" + data["head"]), repo=dict(full_name=self.repository)),
                      base=dict(ref=data["base"], sha=git(root, "rev-parse", "refs/heads/" + data["base"]), repo=dict(full_name=self.repository)), body=data["body"])
            self.prs[number] = pr
            return pr
        if path.startswith("/pulls/"):
            return self.prs[int(path.split("/")[2])]
        if path.startswith("/commits/") and path.endswith("/pulls"):
            return [self.prs[n] for n in self.links.get(path.split("/")[2], [])]
        raise AssertionError((method, path, data))

    def run(self, number, trusted, request=False):
        workflow = gh.REQUEST_WORKFLOW if request else gh.WORKFLOW
        stamp = self.stamp()
        value = dict(id=number, run_attempt=1, run_number=1 + sum(r["path"].endswith("/" + workflow) for r in self.runs.values()),
                     workflow_id=30 if request else 20, path=".github/workflows/" + workflow,
                     repository=dict(full_name=self.repository), head_repository=dict(full_name=self.repository),
                     head_sha=trusted, head_branch="main", event="workflow_dispatch", status="in_progress", conclusion=None,
                     actor=copy.deepcopy(HUMAN), triggering_actor=copy.deepcopy(HUMAN), created_at=stamp, updated_at=stamp, _jobs=[])
        self.runs[number] = value
        return value

    def artifact(self, run, name, filename, value):
        stream = io.BytesIO()
        with zipfile.ZipFile(stream, "w") as archive:
            archive.writestr(filename, gh.canonical(value))
        number = len(self.artifacts) + 1
        self.artifacts[number] = dict(id=number, run_id=run["id"], name=f"module-{name}-{run['id']}-1", expired=False, _zip=stream.getvalue())

    def finish(self, run, plan, result=None, ci=True, success=True):
        action = plan["action"]
        kind, batch = action["kind"], action["id"]
        run.update(status="completed", conclusion="success" if success else "failure", updated_at=self.stamp())
        names = {"prepare": "Prepare", "qualify": "Qualify", "prove": "Proof", "tag": "Tag"}
        name = names.get(kind, "Write" if kind in ("tag", "bind", "adopt") else "Continue")
        run["_jobs"] = [dict(name=name + " " + batch, conclusion="success" if success else "failure")]
        self.artifact(run, "plan", "plan.json", plan)
        if result:
            self.artifact(run, "result", "result.json", result)
        if kind == "qualify" and ci:
            run["_jobs"].append(dict(name="Fixture full CI", conclusion="success"))
            self.artifact(run, "full-ci", "full-ci.json", dict(schema=2, kind="full-ci", binding=action["binding"],
                                                             run=run["id"], attempt=1, jobs=["Fixture full CI"]))

    def merge(self, pr, method="squash"):
        head, base = pr["head"]["sha"], pr["base"]["sha"]
        args = ["commit-tree", git(self.f.root, "rev-parse", head + "^{tree}"), "-p", base]
        if method == "merge":
            args.extend(["-p", head])
        commit = git(self.f.root, *args, "-m", "human reviewed " + method)
        git(self.f.root, "update-ref", "refs/heads/" + pr["base"]["ref"], commit)
        pr.update(merged=True, merged_by=copy.deepcopy(HUMAN), state="closed", merge_commit_sha=commit, merged_at=self.stamp())
        pr["base"]["sha"] = commit  # GitHub's mutable postmerge projection.
        self.links[commit] = [pr["number"]]
        return commit


class FixtureDriver(gh.Driver):
    @contextmanager
    def check(self, commit, authority=None):
        with gh.release.source_tree(self.root, commit) as source:
            yield gh.release.Release(self.root, source, commit, self.api.f.policy, self.api.f.env,
                                     authority if authority is not None else self.authority())


class Controller(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="module-controller-test-")
        self.addCleanup(self.temp.cleanup)
        self.directory = Path(self.temp.name)
        patch = mock.patch.dict(os.environ, fixtures.fixture_environment(self.directory), clear=True)
        patch.start()
        self.addCleanup(patch.stop)
        self.f = f = fixtures.Fixture(self.directory)
        f.write(DEP + "/fixture.go", '''package ssefilter
import "os"
func New() {}
func init() {
 for _, key := range []string{"GH_TOKEN", "MODULE_RELEASE_WRITE_TOKEN", "RELEASE_APP_PRIVATE_KEY", "UNKNOWN_CONTROLLER_SECRET"} {
  if os.Getenv(key) != "" { panic("controller credential leaked: " + key) }
 }
}
''')
        f.write(DEP + "/isolation_test.go", 'package ssefilter\nimport "testing"\nfunc TestIsolation(t *testing.T) { New() }\n')
        f.add_module(APP, f'package openai\nimport dep "{fixtures.NS}/{DEP}"\nfunc New() {{ dep.New() }}\n', f'\nrequire {fixtures.NS}/{DEP} v0.2.0\n')
        f.write(".github/scripts/module-publication-policy.json", gh.canonical(f.policy))
        f.write(".github/scripts/module-release-ci.json", gh.canonical(dict(schema=2, required_jobs=["Fixture full CI"])))
        f.write(".github/workflows/" + gh.WORKFLOW, (HERE.parent / "workflows" / gh.WORKFLOW).read_text())
        f.write(".github/workflows/" + gh.REQUEST_WORKFLOW, (HERE.parent / "workflows" / gh.REQUEST_WORKFLOW).read_text())
        f.write(".github/workflows/ci.yml", (HERE.parent / "workflows/ci.yml").read_text())
        self.B = f.commit()
        self.api = FakeGitHub(f)
        self.intake_run = self.api.run(100, self.B, request=True)
        self.counter = 100
        self.batch = {APP: "v0.2.0", DEP: "v0.2.0"}
        self.record = self.driver(self.intake_run).intake(self.batch, self.B, self.intake_run)
        gh.Writer(self.driver(self.intake_run)).reserve(self.record)
        self.api.finish(self.intake_run, dict(action=dict(kind="intake", id=self.record["id"])))

    def driver(self, run):
        return FixtureDriver(self.api, self.f.root, 70, 700, run["id"], now=datetime(2026, 11, 17, 1, tzinfo=timezone.utc))

    def next(self):
        self.counter += 1
        run = self.api.run(self.counter, self.B)
        d = self.driver(run)
        return d, run, d.envelope(d.plan(run), run)

    def prepared(self):
        d, run, plan = self.next()
        self.assertEqual(plan["action"]["kind"], "prepare")
        records = {p: dict(module=fixtures.NS + "/" + p, version=v, sum="h1:fixture-zip", go_mod_sum="h1:fixture-mod") for p, v in self.batch.items()}
        metadata = dict(schema=2, kind="preparation", reservation=gh.digest(self.record), repository=self.api.repository,
                        B=self.B, batch=self.batch, policy=self.record["policy"], base=d.branch(self.record), head=d.branch(self.record) + "-prep", artifacts=records)
        result = dict(schema=2, run=run["id"], attempt=1, kind="prepared", reservation=gh.digest(self.record),
                      files={gh.PREFIX + self.record["id"] + ".json": gh.canonical(metadata)})
        run["_jobs"] = [dict(name="Prepare " + self.record["id"], conclusion="success")]
        tx = d.validate_write(plan, result, run)
        gh.Writer(d).apply(tx)
        self.api.finish(run, plan, result)
        return d.pr(d.branch(self.record) + "-prep", d.branch(self.record))

    def candidate(self):
        pr = self.prepared()
        self.R = self.api.merge(pr)
        d, run, plan = self.next()
        self.assertEqual(plan["action"]["kind"], "bind")
        gh.Writer(d).apply(d.validate_write(plan, {}, run))
        self.api.finish(run, plan)
        return self.R

    def qualification(self, ci=True, success=True):
        d, run, plan = self.next()
        self.assertEqual(plan["action"]["kind"], "qualify")
        candidate, _ = d.candidate(self.record)
        records = {p: dict(r, sha=self.R) for p, r in candidate["artifacts"].items()}
        result = dict(schema=2, binding=plan["action"]["binding"], run=run["id"], attempt=1,
                      report=dict(stage="staged-qualified", candidate=self.R, order=[p for p in (DEP, APP) if p in self.batch], artifacts=records,
                                  releases=[dict(module=p, version=self.batch[p]) for p in (DEP, APP) if p in self.batch]))
        self.api.finish(run, plan, result, ci=ci, success=success)
        return run

    def publish_one(self):
        d, run, plan = self.next()
        self.assertEqual(plan["action"]["kind"], "tag")
        tx = d.validate_write(plan, {}, run)
        with mock.patch.object(d, "check", side_effect=AssertionError("writer materialized candidate")), \
                mock.patch.object(d, "current", side_effect=AssertionError("writer invoked Git")), \
                mock.patch.object(d, "execute", side_effect=AssertionError("writer executed candidate")):
            gh.Writer(d).apply(tx)
        self.api.finish(run, plan)
        d, run, plan = self.next()
        self.assertEqual(plan["action"]["kind"], "prove")
        action = plan["action"]
        result = dict(schema=2, binding=action["binding"], run=run["id"], attempt=1, module=action["module"], version=action["version"],
                      report=dict(stage="public-verified", candidate=self.R, module=action["module"], version=action["version"],
                                  sum=action["expected"]["sum"], go_mod_sum=action["expected"]["go_mod_sum"]))
        self.api.finish(run, plan, result)
        return action["module"]

    def adoption_ci(self, pr, tree=None, number=900):
        head, base = pr["head"]["sha"], pr["base"]["sha"]
        merge = git(self.f.root, "commit-tree", tree or git(self.f.root, "rev-parse", head + "^{tree}"),
                    "-p", base, "-p", head, "-m", "CI prospective merge")
        run = self.api.run(number, head)
        run.update(event="pull_request", path=".github/workflows/ci.yml", status="completed", conclusion="success")
        run["_jobs"] = [dict(name="Fixture full CI", conclusion="success")]
        d = self.driver(run)
        candidate, _ = d.candidate(self.record)
        receipt = dict(schema=2, kind="adoption", run=run["id"], attempt=1, T=self.B, pr=pr["number"],
                       head=head, base=base, merge=merge, binding=d.binding(self.record, candidate, self.B))
        self.api.artifact(run, "adoption", "module-adoption.json", receipt)
        return run, receipt

    def retry(self):
        d, run, _ = self.next()
        plan = d.envelope(d.plan(run, "retry", self.record["id"]), run)
        self.api.finish(run, plan)

    def test_durable_reservation_precedes_all_branches_and_intake_rejects_active(self):
        refs = [data["ref"] for method, path, data in self.api.writes if path == "/git/refs"]
        self.assertEqual(refs[0], "refs/heads/" + gh.ACTIVE)
        self.assertEqual(len(refs), 2)
        self.assertFalse(self.api.prs)
        run = self.api.run(999, self.B, request=True)
        with self.assertRaisesRegex(gh.Error, "one active batch"):
            self.driver(run).intake({DEP: "v0.3.0"}, self.B, run)

    def test_same_R_dependency_first_proofs_then_human_merge_adoption(self):
        self.candidate()
        self.qualification()
        self.assertEqual(self.publish_one(), DEP)
        self.assertEqual(self.publish_one(), APP)
        d, run, plan = self.next()
        self.assertEqual(plan["action"]["kind"], "adopt")
        gh.Writer(d).apply(d.validate_write(plan, {}, run))
        self.api.finish(run, plan)
        self.assertEqual(len(self.api.prs), 2)
        self.adoption_ci(self.api.prs[2])
        A = self.api.merge(self.api.prs[2], "merge")
        self.assertEqual(self.api.prs[2]["base"]["sha"], A)
        newer = git(self.f.root, "commit-tree", git(self.f.root, "rev-parse", A + "^{tree}"), "-p", A, "-m", "later main")
        git(self.f.root, "update-ref", "refs/heads/main", newer)
        self.api.prs[2]["base"]["sha"] = newer
        d, run, plan = self.next()
        self.assertEqual(plan["action"]["kind"], "complete")
        gh.Writer(d).apply(d.validate_write(plan, {}, run))
        self.assertIsNone(d.ref(gh.ACTIVE, missing=True))
        self.assertEqual({git(self.f.root, "rev-parse", "refs/tags/" + p + "/v0.2.0") for p in self.batch}, {self.R})
        d.ancestor(self.R, A)

    def test_adoption_receipt_rejects_unrelated_parents_and_incomplete_ci(self):
        self.candidate()
        self.qualification()
        self.publish_one()
        self.publish_one()
        d, run, plan = self.next()
        gh.Writer(d).apply(d.validate_write(plan, {}, run))
        self.api.finish(run, plan)
        pr = self.api.prs[2]
        ci, receipt = self.adoption_ci(pr)
        A = self.api.merge(pr, "merge")
        # Mutable API base need not equal the independently recorded premerge base.
        self.assertEqual(pr["base"]["sha"], A)
        ci["_jobs"][0]["conclusion"] = "failure"
        with self.assertRaisesRegex(gh.Error, "premerge head/base/CI merge"):
            d.adoption_evidence(self.record, d.candidate(self.record)[0], pr)
        ci["_jobs"][0]["conclusion"] = "success"
        tree = git(self.f.root, "rev-parse", A + "^{tree}")
        unrelated = git(self.f.root, "commit-tree", tree, "-m", "unrelated base")
        wrong = git(self.f.root, "commit-tree", tree, "-p", unrelated, "-p", self.R, "-m", "unrelated parents")
        pr["merge_commit_sha"] = wrong
        self.api.links[wrong] = [2]
        git(self.f.root, "update-ref", "refs/heads/main", wrong)
        with self.assertRaisesRegex(gh.Error, "premerge head/base/CI merge"):
            d.adoption_evidence(self.record, d.candidate(self.record)[0], pr)
        # A two-parent rebase-like result retaining neither reviewed head identity
        # nor its tested parent pair cannot substitute for a merge commit either.
        rewritten = git(self.f.root, "commit-tree", tree, "-p", self.B, "-m", "rewritten head")
        wrong = git(self.f.root, "commit-tree", tree, "-p", self.B, "-p", rewritten, "-m", "rewritten merge")
        pr["merge_commit_sha"] = wrong
        self.api.links[wrong] = [2]
        git(self.f.root, "update-ref", "refs/heads/main", wrong)
        with self.assertRaisesRegex(gh.Error, "premerge head/base/CI merge"):
            d.adoption_evidence(self.record, d.candidate(self.record)[0], pr)
        pr["merge_commit_sha"] = A
        git(self.f.root, "update-ref", "refs/heads/main", A)
        d, run, plan = self.next()
        tx = d.validate_write(plan, {}, run)
        # The late API-only writer rechecks the SAME independent receipt.
        ci["_jobs"][0]["conclusion"] = "failure"
        with self.assertRaisesRegex(gh.Error, "premerge head/base/CI merge"):
            gh.Writer(d).apply(tx)
        ci["_jobs"][0]["conclusion"] = "success"
        gh.Writer(d).apply(tx)
        self.assertIsNone(d.ref(gh.ACTIVE, missing=True))

    def test_zero_tags_without_full_qualification_and_explicit_retry(self):
        self.candidate()
        self.qualification(ci=False)
        d, run, plan = self.next()
        self.assertEqual(plan["action"]["kind"], "wait")
        self.assertIn("stopped", plan["action"])
        self.assertEqual(git(self.f.root, "tag", "-l"), "")
        self.api.finish(run, plan)
        self.retry()
        self.qualification()
        self.assertEqual(self.publish_one(), DEP)

    def test_qualification_failure_stops_independent_remaining_modules(self):
        self.candidate()
        self.qualification(success=False)
        d, run, plan = self.next()
        self.assertEqual(plan["action"]["kind"], "wait")
        self.assertEqual(git(self.f.root, "tag", "-l"), "")

    def test_logical_freeze_revalidated_immediately_before_every_write(self):
        self.candidate()
        self.qualification()
        d, run, plan = self.next()
        tx = d.validate_write(plan, {}, run)
        moved = git(self.f.root, "commit-tree", git(self.f.root, "rev-parse", self.R + "^{tree}"), "-p", self.R, "-m", "unexpected update")
        git(self.f.root, "update-ref", "refs/heads/" + d.branch(self.record), moved)
        before = len(self.api.writes)
        with self.assertRaisesRegex(gh.Error, "logical freeze"):
            gh.Writer(d).apply(tx)
        self.assertEqual(len(self.api.writes), before)
        self.assertEqual(git(self.f.root, "tag", "-l"), "")

    def test_wrong_parent_tree_app_human_repository_and_mapping_rejected(self):
        self.candidate()
        for change, message in ((lambda p: p["user"].update(id=701), "App"),
                                (lambda p: p["merged_by"].update(type="Bot"), "human"),
                                (lambda p: p["head"]["repo"].update(full_name="evil/fork"), "repository")):
            saved = copy.deepcopy(self.api.prs[1])
            change(self.api.prs[1])
            with self.subTest(message=message), self.assertRaisesRegex(gh.Error, message):
                self.driver(self.intake_run).candidate(self.record)
            self.api.prs[1] = saved
        pr = self.api.prs[1]
        tree = git(self.f.root, "rev-parse", self.R + "^{tree}")
        wrong = git(self.f.root, "commit-tree", tree, "-p", self.B, "-p", pr["head"]["sha"], "-m", "wrong method")
        pr["merge_commit_sha"] = wrong
        self.api.links[wrong] = [1]
        with self.assertRaisesRegex(gh.Error, "sole parent"):
            self.driver(self.intake_run).candidate(self.record)

    def test_authoritative_writer_rejects_wrong_tree_single_parent_and_mapping_without_writes(self):
        self.candidate()
        self.qualification()
        d, run, plan = self.next()
        tx = d.validate_write(plan, {}, run)
        pr = self.api.prs[1]
        tree = git(self.f.root, "rev-parse", self.R + "^{tree}")
        wrong_tree = git(self.f.root, "commit-tree", git(self.f.root, "rev-parse", self.B + "^{tree}"),
                         "-p", self.B, "-m", "unreviewed tree")
        wrong_parent = git(self.f.root, "commit-tree", tree, "-p", pr["head"]["sha"], "-m", "wrong sole parent")
        for commit, message in ((wrong_tree, "whole reviewed preparation tree"), (wrong_parent, "sole parent B")):
            with self.subTest(message=message):
                pr["merge_commit_sha"] = commit
                self.api.links[commit] = [1]
                before = len(self.api.writes)
                with self.assertRaisesRegex(gh.Error, message):
                    gh.Writer(d).apply(tx)
                self.assertEqual(len(self.api.writes), before)
                self.assertEqual(git(self.f.root, "tag", "-l"), "")
        pr["merge_commit_sha"] = self.R
        original = self.api.file
        def changed_mapping(path, ref, **kwargs):
            value = original(path, ref, **kwargs)
            if path == gh.PREFIX + self.record["id"] + ".json":
                value = gh.document(value)
                value["batch"][DEP] = "v0.9.0"
                return gh.canonical(value)
            return value
        before = len(self.api.writes)
        with mock.patch.object(self.api, "file", side_effect=changed_mapping), self.assertRaisesRegex(gh.Error, "changed mapping"):
            gh.Writer(d).apply(tx)
        self.assertEqual(len(self.api.writes), before)
        self.assertEqual(git(self.f.root, "tag", "-l"), "")

    def test_writer_rechecks_staged_and_public_checksums_before_any_write(self):
        self.candidate()
        qualification = self.qualification()
        for stage in ("staged", "public"):
            if stage == "public":
                self.publish_one()
            d, run, plan = self.next()
            tx = d.validate_write(plan, {}, run)
            artifact = max((a for a in self.api.artifacts.values() if a["name"].startswith("module-result-")
                            and (a["run_id"] == qualification["id"] if stage == "staged" else a["run_id"] != qualification["id"])),
                           key=lambda a: a["id"])
            saved = artifact["_zip"]
            for field in ("sum", "go_mod_sum"):
                with self.subTest(stage=stage, field=field):
                    with zipfile.ZipFile(io.BytesIO(saved)) as archive:
                        value = gh.document(archive.read("result.json"))
                    target = value["report"]["artifacts"][DEP] if stage == "staged" else value["report"]
                    target[field] = "h1:corrupt"
                    stream = io.BytesIO()
                    with zipfile.ZipFile(stream, "w") as archive:
                        archive.writestr("result.json", gh.canonical(value))
                    artifact["_zip"] = stream.getvalue()
                    before = len(self.api.writes)
                    tags = git(self.f.root, "tag", "-l")
                    with self.assertRaisesRegex(gh.Error, "lost full qualification authority"):
                        gh.Writer(d).apply(tx)
                    self.assertEqual(len(self.api.writes), before)
                    self.assertEqual(git(self.f.root, "tag", "-l"), tags)
                    artifact["_zip"] = saved
            self.api.finish(run, plan, success=False)
            if stage == "staged":
                self.retry()
                qualification = self.qualification()

    def test_control_plane_diff_and_forged_classification_rejected(self):
        pr = self.prepared()
        d = self.driver(self.intake_run)
        bad = gh.Writer(d).data_commit(pr["head"]["sha"], {".github/workflows/ci.yml": "malicious change"}, "not permitted", lambda: None)
        pr["head"]["sha"] = bad
        with self.assertRaisesRegex(gh.Error, "forbidden"):
            d.classify(1, self.intake_run)
        for path in ("VERSION", "SDKVERSION", "sdk/typescript/VERSION", "policy.md", ".github/scripts/module-consumers/openai.go", "Taskfile.yml"):
            self.assertFalse(d.allowed(path, self.f.policy, "expected-metadata.json"), path)

    def test_expired_receipt_is_not_tag_success(self):
        self.candidate()
        self.qualification()
        self.publish_one()
        for a in self.api.artifacts.values():
            if a["name"].startswith("module-result-"):
                a["expired"] = True
        d, run, plan = self.next()
        self.assertEqual(plan["action"]["kind"], "wait")
        self.api.finish(run, plan)
        self.retry()
        self.qualification()
        d, run, plan = self.next()
        self.assertEqual(plan["action"]["kind"], "prove")
        self.assertEqual(plan["action"]["module"], DEP)

    def test_closed_PR1_pauses_without_recreation(self):
        pr = self.prepared()
        pr["state"] = "closed"
        d, run, plan = self.next()
        self.assertEqual(plan["action"]["kind"], "wait")
        self.assertIn("closed", plan["action"]["reason"])
        self.assertEqual(len(self.api.prs), 1)

    def test_closed_PR2_keeps_reservation_and_never_recreates(self):
        self.candidate()
        self.qualification()
        self.publish_one()
        self.publish_one()
        d, run, plan = self.next()
        gh.Writer(d).apply(d.validate_write(plan, {}, run))
        self.api.finish(run, plan)
        self.api.prs[2]["state"] = "closed"
        d, run, plan = self.next()
        self.assertEqual(plan["action"]["kind"], "wait")
        self.assertIn("closed", plan["action"]["reason"])
        self.assertEqual(len(self.api.prs), 2)
        self.assertIsNotNone(d.ref(gh.ACTIVE))

    def test_post_tag_crash_needs_human_retry_qualification_and_fresh_proof(self):
        self.candidate()
        self.qualification()
        d, run, plan = self.next()
        gh.Writer(d).apply(d.validate_write(plan, {}, run))
        self.api.finish(run, plan, success=False)  # Tag exists; response/journal failed.
        d, run, plan = self.next()
        self.assertEqual(plan["action"]["kind"], "wait")
        self.api.finish(run, plan)
        self.retry()
        self.qualification()
        tags = [data for _, path, data in self.api.writes if path == "/git/refs" and data["ref"].startswith("refs/tags/")]
        d, run, plan = self.next()
        self.assertEqual(plan["action"]["kind"], "prove")
        self.assertEqual(plan["action"]["module"], DEP)
        self.assertEqual(len(tags), 1)
        self.assertFalse(d.ref("main") == self.R)

    def test_intake_racing_reservation_release_cannot_acquire_early(self):
        d, run, _ = self.next()
        plan = d.envelope(d.plan(run, "abandon", self.record["id"]), run)
        tx = d.validate_write(plan, {}, run)
        intake_run = self.api.run(999, self.B, request=True)
        rejected = []
        def race(method, path, data):
            if method == "DELETE":
                try:
                    self.driver(intake_run).intake({DEP: "v0.3.0"}, self.B, intake_run)
                except gh.Error as error:
                    rejected.append(str(error))
        self.api.before_write = race
        gh.Writer(d).apply(tx)
        self.assertEqual(len(rejected), 1)
        self.assertIn("one active batch", rejected[0])
        self.assertEqual(self.driver(intake_run).intake({DEP: "v0.3.0"}, self.B, intake_run)["id"], "999-1")

    def test_wrong_adoption_merge_method_pauses(self):
        self.candidate()
        self.qualification()
        self.publish_one()
        self.publish_one()
        d, run, plan = self.next()
        gh.Writer(d).apply(d.validate_write(plan, {}, run))
        self.api.finish(run, plan)
        self.api.merge(self.api.prs[2], "squash")
        with self.assertRaisesRegex(gh.Error, "wrong merge method"):
            self.next()
        self.assertIsNotNone(d.ref(gh.ACTIVE))

    def test_abandoned_public_versions_retain_authority(self):
        self.candidate()
        self.qualification()
        self.publish_one()
        d, run, _ = self.next()
        plan = d.envelope(d.plan(run, "abandon", self.record["id"]), run)
        gh.Writer(d).apply(d.validate_write(plan, {}, run))
        self.assertIsNone(d.ref(gh.ACTIVE, missing=True))
        self.assertEqual(d.authority()[DEP]["v0.2.0"], self.R)
        with d.check(self.B) as check:
            self.assertEqual(check.latest(DEP), "v0.2.0")
            with self.assertRaises(gh.Error):
                check.preview({DEP: "v0.1.0"})
        terminal = d.record(d.ref(d.state_ref(self.record, "terminal")))
        self.assertEqual(terminal["published"], {DEP: dict(version="v0.2.0", sha=self.R)})

    def test_read_only_history_command_uses_authenticated_identity_not_local_flags(self):
        self.candidate()
        self.qualification()
        self.publish_one()
        original = self.api.call
        def read(method, path, *args, **kwargs):
            if path == "/actions/variables/RELEASE_APP_ID":
                return dict(value="70")
            if path == "/actions/variables/RELEASE_APP_BOT_ID":
                return dict(value="700")
            return original(method, path, *args, **kwargs)
        before = len(self.api.writes)
        with mock.patch.object(gh, "GitHub", return_value=self.api), \
                mock.patch.object(self.api, "call", side_effect=read), \
                mock.patch.dict(os.environ, GH_TOKEN="offline-read-token", RELEASE_APP_ID="999", RELEASE_APP_BOT_ID="999"), \
                mock.patch.object(sys, "argv", ["module-release-github.py", "authority", "--root", str(self.f.root)]), \
                redirect_stdout(io.StringIO()) as output:
            self.assertEqual(gh.main(), 0)
        self.assertEqual(gh.document(output.getvalue()), {DEP: {"v0.2.0": self.R}})
        self.assertEqual(len(self.api.writes), before)
        self.api.prs[1]["user"]["id"] = 999
        with mock.patch.object(gh, "GitHub", return_value=self.api), \
                mock.patch.object(self.api, "call", side_effect=read), \
                mock.patch.object(sys, "argv", ["module-release-github.py", "authority", "--root", str(self.f.root)]), \
                redirect_stdout(io.StringIO()), redirect_stderr(io.StringIO()) as errors:
            self.assertEqual(gh.main(), 1)
        self.assertIn("App", errors.getvalue())
        self.assertEqual(len(self.api.writes), before)

    def test_old_reruns_and_candidate_as_T_rejected(self):
        self.candidate()
        d, run, plan = self.next()
        run["run_attempt"] = 2
        with self.assertRaisesRegex(gh.Error, "reruns"):
            d.current()
        run["run_attempt"] = 1
        run["head_sha"] = self.R
        with self.assertRaisesRegex(gh.Error, "ancestry"):
            d.current()

    def test_deleted_whole_run_stops_until_new_retry(self):
        self.candidate()
        qualified = self.qualification()
        del self.api.runs[qualified["id"]]
        # Preserve GitHub's monotonic sequence after deletion.
        d, run, plan = self.next()
        run["run_number"] += 1
        plan = d.envelope(d.plan(run), run)
        self.assertEqual(plan["action"]["kind"], "wait")

    def test_intake_racing_reservation_atomic_create_never_creates_second_PR(self):
        # Two stale previews can exist, but create-only active acquisition is the
        # final authority. Shared workflow concurrency additionally serializes it.
        old = self.record
        d = self.driver(self.intake_run)
        git(self.f.root, "update-ref", "-d", "refs/heads/" + gh.ACTIVE)
        second_run = self.api.run(999, self.B, request=True)
        second = self.driver(second_run).intake({DEP: "v0.3.0"}, self.B, second_run)
        gh.Writer(d).reserve(old)
        before = len(self.api.writes)
        with self.assertRaisesRegex(gh.Error, "active batch"):
            gh.Writer(self.driver(second_run)).reserve(second)
        self.assertEqual(len(self.api.writes), before)
        self.assertFalse(self.api.prs)

    def test_full_CI_receipt_requires_server_jobs_and_exact_binding(self):
        self.candidate()
        run = self.qualification()
        run["_jobs"][-1]["conclusion"] = "skipped"
        d, _, plan = self.next()
        self.assertEqual(plan["action"]["kind"], "wait")
        self.assertEqual(git(self.f.root, "tag", "-l"), "")

    def test_concurrent_stale_intakes_only_one_atomic_reservation_wins(self):
        git(self.f.root, "update-ref", "-d", "refs/heads/" + gh.ACTIVE)
        run = self.api.run(999, self.B, request=True)
        second = self.driver(run).intake({DEP: "v0.3.0"}, self.B, run)
        barrier = threading.Barrier(2)
        errors = []
        def pause(method, path, data):
            if path == "/git/refs" and data["ref"] == "refs/heads/" + gh.ACTIVE:
                barrier.wait(timeout=10)
        self.api.before_write = pause
        def reserve(record, run):
            try:
                gh.Writer(self.driver(run)).reserve(record)
            except gh.Error as error:
                errors.append(str(error))
        threads = [threading.Thread(target=reserve, args=(r, n)) for r, n in ((self.record, self.intake_run), (second, run))]
        for thread in threads:
            thread.start()
        for thread in threads:
            thread.join(timeout=15)
            self.assertFalse(thread.is_alive())
        self.assertEqual(len(errors), 1)
        self.assertIn("atomic ref", errors[0])
        _, active = self.driver(run).active()
        self.assertIn(active["id"], (self.record["id"], second["id"]))
        self.assertFalse(self.api.prs)

    def test_interrupted_prepare_and_terminal_deletion_are_idempotent(self):
        original = self.api.call
        failed = False
        def interrupted(method, path, data=None, missing=False):
            nonlocal failed
            if method == "POST" and path == "/pulls" and not failed:
                failed = True
                raise gh.Error("crash before PR creation")
            return original(method, path, data, missing)
        with mock.patch.object(self.api, "call", side_effect=interrupted):
            with self.assertRaisesRegex(gh.Error, "crash"):
                self.prepared()
        # A failed preparation stops the batch; a fresh explicit retry can recover
        # the already-created branch without another human review checkpoint.
        failed_run = self.api.runs[self.counter]
        failed_run.update(status="completed", conclusion="failure", updated_at=self.api.stamp())
        self.retry()
        self.prepared()
        self.assertEqual(len(self.api.prs), 1)
        d, run, _ = self.next()
        plan = d.envelope(d.plan(run, "abandon", self.record["id"]), run)
        tx = d.validate_write(plan, {}, run)
        def delete_crash(method, path, data=None, missing=False):
            if method == "DELETE":
                raise gh.Error("crash before reservation deletion")
            return original(method, path, data, missing)
        with mock.patch.object(self.api, "call", side_effect=delete_crash), self.assertRaisesRegex(gh.Error, "deletion"):
            gh.Writer(d).apply(tx)
        d, run, plan = self.next()
        self.assertEqual(plan["action"]["kind"], "finalize")
        gh.Writer(d).apply(d.validate_write(plan, {}, run))
        self.assertIsNone(d.ref(gh.ACTIVE, missing=True))

    def test_exceptional_integration_is_discovered_and_preserves_R(self):
        pr1 = self.prepared()
        writer = gh.Writer(self.driver(self.intake_run))
        original = self.api.file(DEP + "/fixture.go", self.B)
        source = writer.data_commit(pr1["head"]["sha"], {DEP + "/fixture.go": original + "func SourceChange() {}\n"},
                                    "reviewed source change", lambda: None)
        pr1["head"]["sha"] = source
        git(self.f.root, "update-ref", "refs/heads/" + pr1["head"]["ref"], source)
        self.R = self.api.merge(pr1)
        d, run, plan = self.next()
        gh.Writer(d).apply(d.validate_write(plan, {}, run))
        self.api.finish(run, plan)
        self.qualification()
        self.publish_one()
        self.publish_one()
        main = writer.data_commit(self.B, {DEP + "/fixture.go": original + "func MainChange() {}\n"},
                                  "divergent main source", lambda: None)
        self.B = main
        for ref in ("refs/heads/main", "refs/remotes/origin/main"):
            git(self.f.root, "update-ref", ref, main)
        resolution = writer.data_commit(main, {DEP + "/fixture.go": original + "func MainChange() {}\nfunc SourceChange() {}\n"},
                                       "reviewed conflict resolution", lambda: None)
        integrated = git(self.f.root, "commit-tree", git(self.f.root, "rev-parse", resolution + "^{tree}"),
                         "-p", main, "-p", self.R, "-m", "reviewed integration preserving source and main")
        self.assertNotEqual(git(self.f.root, "rev-parse", integrated + "^{tree}"), git(self.f.root, "rev-parse", self.R + "^{tree}"))
        git(self.f.root, "update-ref", "refs/heads/module-adoption/reviewed", integrated)
        d, run, _ = self.next()
        plan = d.envelope(d.plan(run, integration="module-adoption/reviewed"), run)
        gh.Writer(d).apply(d.validate_write(plan, {}, run))
        self.api.finish(run, plan)
        pr = self.api.prs[2]
        self.adoption_ci(pr)
        candidate = d.candidate(self.record)[0]
        wrong = git(self.f.root, "commit-tree", git(self.f.root, "rev-parse", self.R + "^{tree}"),
                    "-p", main, "-p", integrated, "-m", "wrong integration result")
        pr.update(merged=True, merge_commit_sha=wrong)
        git(self.f.root, "update-ref", "refs/heads/main", wrong)
        with self.assertRaisesRegex(gh.Error, "premerge head/base/CI merge"):
            d.adoption_evidence(self.record, candidate, pr)
        # A genuine two-parent prospective merge at the old base is still stale.
        # Advancing main twice must not promote that receipt into fresh evidence.
        for index in (1, 2):
            main = writer.data_commit(main, {f"advance{index}.go": "package advance\n"}, "main advance", lambda: None)
            self.B = main
            pr["base"]["sha"] = main
            result = writer.data_commit(integrated, {f"advance{i}.go": "package advance\n" for i in range(1, index + 1)},
                                        "prospective integration", lambda: None)
            tree = git(self.f.root, "rev-parse", result + "^{tree}")
            actual = git(self.f.root, "commit-tree", tree, "-p", main, "-p", integrated, "-m", "actual adoption")
            pr["merge_commit_sha"] = actual
            git(self.f.root, "update-ref", "refs/heads/main", actual)
            with self.subTest(advance=index), self.assertRaisesRegex(gh.Error, "premerge head/base/CI merge"):
                d.adoption_evidence(self.record, candidate, pr)
        pr["merged"] = False
        git(self.f.root, "update-ref", "refs/heads/main", main)
        git(self.f.root, "update-ref", "refs/remotes/origin/main", main)
        self.adoption_ci(pr, tree=tree, number=901)
        pr.update(merged=True, merged_by=copy.deepcopy(HUMAN), state="closed", merge_commit_sha=actual)
        self.api.links[actual] = [2]
        git(self.f.root, "update-ref", "refs/heads/main", actual)
        d, run, plan = self.next()
        self.assertEqual(plan["action"]["kind"], "complete")
        gh.Writer(d).apply(d.validate_write(plan, {}, run))
        self.assertIsNone(d.ref(gh.ACTIVE, missing=True))
        self.assertEqual(len(self.api.prs), 2)

    def test_historical_receipt_cannot_substitute_controller_T(self):
        self.candidate()
        run = self.qualification()
        artifact = next(a for a in self.api.artifacts.values() if a["run_id"] == run["id"] and a["name"].startswith("module-plan-"))
        with zipfile.ZipFile(io.BytesIO(artifact["_zip"])) as archive:
            plan = gh.document(archive.read("plan.json"))
        plan["T"] = self.R
        stream = io.BytesIO()
        with zipfile.ZipFile(stream, "w") as archive:
            archive.writestr("plan.json", gh.canonical(plan))
        artifact["_zip"] = stream.getvalue()
        d, run, plan = self.next()
        self.assertEqual(plan["action"]["kind"], "wait")
        self.assertEqual(git(self.f.root, "tag", "-l"), "")

    def test_receipt_schema1_is_not_batch_qualification(self):
        self.candidate()
        run = self.qualification()
        artifact = next(a for a in self.api.artifacts.values() if a["run_id"] == run["id"] and a["name"].startswith("module-result-"))
        with zipfile.ZipFile(io.BytesIO(artifact["_zip"])) as archive:
            result = gh.document(archive.read("result.json"))
        result["schema"] = 1
        stream = io.BytesIO()
        with zipfile.ZipFile(stream, "w") as archive:
            archive.writestr("result.json", gh.canonical(result))
        artifact["_zip"] = stream.getvalue()
        d, run, plan = self.next()
        self.assertEqual(plan["action"]["kind"], "wait")

    def test_protected_writer_source_has_no_execution_calls(self):
        import ast
        tree = ast.parse((HERE / "module-release-github.py").read_text())
        writer = next(n for n in tree.body if isinstance(n, ast.ClassDef) and n.name == "Writer")
        forbidden = {"run", "git", "source_tree", "check", "helper", "execute", "current", "Popen", "system"}
        for node in ast.walk(writer):
            if isinstance(node, ast.Call) and isinstance(node.func, ast.Attribute):
                self.assertNotIn(node.func.attr, forbidden)


class Execution(unittest.TestCase):
    setUp = Controller.setUp
    driver = Controller.driver
    next = Controller.next

    @classmethod
    def setUpClass(cls):
        artifacts.ArtifactTests.setUpClass.__func__(cls)

    @classmethod
    def tearDownClass(cls):
        cls.tools.cleanup()

    def cli(self, command, run, value=None, result=None):
        output = self.directory / (command + "-output.json")
        if output.exists():
            output.unlink()
        argv = ["module-release-github.py", command, "--root", str(self.f.root), "--output", str(output)]
        if value is not None:
            input_path = self.directory / "input.json"
            input_path.write_text(gh.canonical(value))
            argv.extend(["--input", str(input_path)])
        if result is not None:
            result_path = self.directory / "result.json"
            result_path.write_text(gh.canonical(result))
            argv.extend(["--result", str(result_path)])
        env = dict(GITHUB_REPOSITORY=self.api.repository, RELEASE_APP_ID="70", RELEASE_APP_BOT_ID="700",
                   GITHUB_RUN_ID=str(run["id"]), GITHUB_RUN_ATTEMPT="1", MODULE_RELEASES_ENABLED="true",
                   GITHUB_REF="refs/heads/main", GH_TOKEN="controller-sentinel", MODULE_RELEASE_WRITE_TOKEN="writer-sentinel")
        with mock.patch.dict(os.environ, env), mock.patch.object(sys, "argv", argv), \
                mock.patch.object(gh, "GitHub", return_value=self.api), mock.patch.object(gh, "Driver", FixtureDriver), \
                mock.patch.object(gh, "protection_check") as protections, redirect_stderr(io.StringIO()) as errors, redirect_stdout(io.StringIO()):
            code = gh.main()
        self.assertEqual(code, 0, errors.getvalue())
        if command == "validate-write":
            protections.assert_called_once_with(self.api.repository, 70, 700)
        return gh.document(output.read_text())

    def test_actual_CLI_prepare_qualify_public_proofs_same_R(self):
        original = gh.release.public_environment
        def public(temporary):
            return dict(original(temporary), GOPROXY=self.f.proxy.as_uri(), GOSUMDB="off", GOPROXY_TEST="offline",
                        GOCACHE=str(self.directory / "go-build"))
        with mock.patch.object(gh.release, "public_environment", side_effect=public), \
                mock.patch.object(FixtureDriver, "helper", return_value=self.helper):
            d, run, _ = self.next()
            plan = self.cli("plan", run)
            prepared = self.cli("execute", run, plan)
            run["_jobs"] = [dict(name="Prepare " + self.record["id"], conclusion="success")]
            tx = self.cli("validate-write", run, plan, prepared)
            self.cli("write", run, tx)
            self.api.finish(run, plan, prepared)
            R = self.api.merge(self.api.prs[1])
            self.assertNotEqual(R, self.B)
            d, run, _ = self.next()
            plan = self.cli("plan", run)
            self.assertEqual(plan["action"]["kind"], "bind")
            self.cli("write", run, self.cli("validate-write", run, plan))
            self.api.finish(run, plan)
            d, run, _ = self.next()
            plan = self.cli("plan", run)
            self.assertEqual(plan["action"]["kind"], "qualify")
            qualified = self.cli("execute", run, plan)
            self.assertEqual(qualified["report"]["stage"], "staged-qualified")
            self.assertEqual({r["sha"] for r in qualified["report"]["artifacts"].values()}, {R})
            self.assertEqual(git(self.f.root, "tag", "-l"), "")
            # This is a synthetic COMPLETE-caller receipt, not a claim that this
            # test runs the production full-CI gate set (task 5 owns that wiring).
            self.api.finish(run, plan, qualified)
            authority = {p: {v: R} for p, v in self.batch.items()}
            proxy = self.directory / "canonical-public"
            with d.check(R, authority) as check:
                check.stage(self.batch, self.helper, proxy, qualified["report"]["artifacts"])
            import shutil
            seen = []
            for path in (DEP, APP):
                d, run, _ = self.next()
                plan = self.cli("plan", run)
                self.assertEqual(plan["action"]["kind"], "tag")
                self.assertEqual(plan["action"]["module"], path)
                self.cli("write", run, self.cli("validate-write", run, plan))
                self.api.finish(run, plan)
                identity = fixtures.NS + "/" + path
                shutil.copytree(proxy / identity, self.f.proxy / identity, dirs_exist_ok=True)
                d, run, _ = self.next()
                plan = self.cli("plan", run)
                self.assertEqual(plan["action"]["kind"], "prove")
                proof = self.cli("execute", run, plan)
                self.assertEqual(proof["report"]["sum"], qualified["report"]["artifacts"][path]["sum"])
                self.api.finish(run, plan, proof)
                seen.append(proof["report"]["candidate"])
            self.assertEqual(seen, [R, R])
            d, run, _ = self.next()
            self.assertEqual(self.cli("plan", run)["action"]["kind"], "adopt")


class WorkflowBoundary(unittest.TestCase):
    def test_fixed_T_shared_serialization_and_static_credential_boundaries(self):
        release = (HERE.parent / "workflows" / gh.WORKFLOW).read_text()
        request = (HERE.parent / "workflows" / gh.REQUEST_WORKFLOW).read_text()
        for value in (release, request):
            self.assertIn("group: module-releases\n  cancel-in-progress: false", value)
            self.assertNotIn("ref: refs/heads/main", value)
            self.assertEqual(value.count("uses: actions/checkout@"), value.count("ref: ${{ github.sha }}"))
            self.assertNotIn("permission-administration:", value)
            self.assertNotIn("permission-workflows:", value)
        proof = release.split("  proof:\n", 1)[1].split("  write:\n", 1)[0]
        self.assertNotIn("secrets.", proof)
        self.assertNotIn("environment: release", proof)
        self.assertNotIn("MODULE_RELEASE_WRITE_TOKEN", proof)
        writer = release.split("  write:\n", 1)[1].split("  continue:\n", 1)[0]
        after_mint = writer.split("permission-pull-requests: write", 1)[1]
        self.assertNotIn("execute --", after_mint)
        self.assertIn("write --input", after_mint)
        tag = release.split("  tag:\n", 1)[1].split("  continue:\n", 1)[0]
        self.assertNotIn("permission-pull-requests: write", tag)
        self.assertNotIn("execute --", tag)
        self.assertIn("environment: release", tag)
        self.assertIn("uses: ./.github/workflows/ci.yml", release)
        self.assertIn("module-full-ci-${{ github.run_id }}-${{ github.run_attempt }}", release)
        self.assertNotIn("is not wired", release)
        self.assertNotIn("recover-ci", release)
        self.assertNotIn("supersedes", request)

    def test_merge_bridge_is_metadata_only_and_main_dispatch(self):
        bridge = (HERE.parent / "workflows/module-release-wakeup.yml").read_text()
        self.assertIn("pull_request_target:", bridge)
        self.assertNotIn("checkout", bridge.split("jobs:", 1)[1])
        self.assertNotIn("secrets.", bridge)
        self.assertIn("--ref main", bridge)
        self.assertNotIn("github.event.pull_request.head", bridge)


if __name__ == "__main__":
    quick = "--quick" in sys.argv
    if quick:
        sys.argv.remove("--quick")
    unittest.main(defaultTest=["WorkflowBoundary", "Controller.test_durable_reservation_precedes_all_branches_and_intake_rejects_active",
                               "Controller.test_logical_freeze_revalidated_immediately_before_every_write",
                               "Controller.test_full_CI_receipt_requires_server_jobs_and_exact_binding",
                               "Controller.test_concurrent_stale_intakes_only_one_atomic_reservation_wins"] if quick else None)
