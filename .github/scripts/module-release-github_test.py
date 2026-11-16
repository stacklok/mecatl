#!/usr/bin/env python3
"""Offline executable GitHub transitions with real Git/Go/file-proxy fixtures."""
from contextlib import contextmanager, redirect_stderr, redirect_stdout
import copy
from datetime import datetime, timezone
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import sys
import tempfile
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
git = gh.release.git
DEP, APP, SUPPORT = fixtures.DEP, fixtures.APP, fixtures.SUPPORT
HUMAN = dict(type="User", id=42, login="release-human")
BOT = dict(type="Bot", id=700, login="designated-release[bot]")
STAMP = "2026-11-15T00:00:00Z"


class FakeGitHub(gh.GitHub):
    """Git data API writes really create Git objects/refs; PRs/runs are API fixtures."""
    def __init__(self, fixture):
        super().__init__("stacklok/mecatl", "offline-no-credential")
        self.fixture = fixture
        self.prs, self.runs, self.artifacts = {}, {}, {}
        self.writes, self.reruns = [], []
        self.current_jobs = {}
        self.attempts = {}
        self.permissions = {}
        self.sequence = {}
        self.links = {}
        self.clock = 1

    def call(self, method, path, data=None, missing=False):
        parsed = urlsplit(path)
        path, query = unquote(parsed.path), parse_qs(parsed.query)
        root = self.fixture.root
        if method != "GET":
            self.writes.append((method, path, copy.deepcopy(data)))
        if path.startswith("/collaborators/"):
            return dict(permission=self.permissions.get(path.split("/")[2], "write"))
        if path.startswith("/actions/workflows/") and path.endswith(".yml"):
            return dict(id=10 if path.endswith("/ci.yml") else 30 if path.endswith("/" + gh.REQUEST_WORKFLOW) else 20)
        if path == "/pulls" and method == "GET":
            prs = list(self.prs.values())
            if "head" in query:
                branch = query["head"][0].split(":", 1)[1]
                prs = [p for p in prs if p["head"]["ref"] == branch]
            return prs if query.get("page", ["1"])[0] == "1" else []
        if path == "/pulls" and method == "POST":
            number = max(self.prs, default=0) + 1
            head = git(root, "rev-parse", "refs/heads/" + data["head"])
            base = git(root, "rev-parse", "refs/remotes/origin/main")
            pr = dict(number=number, user=copy.deepcopy(BOT), merged=False, merged_by=None,
                      state="open", merge_commit_sha=None, merged_at=None, closed_at=None,
                      head=dict(sha=head, ref=data["head"], repo=dict(full_name=self.repository)),
                      base=dict(sha=base, ref="main", repo=dict(full_name=self.repository)), body=data["body"])
            changed = git(root, "diff", "--name-status", git(root, "merge-base", base, head), head).splitlines()
            pr["_files"] = [dict(filename=line.split("\t")[1], status={"A": "added", "M": "modified"}[line[0]]) for line in changed]
            pr["changed_files"] = len(pr["_files"])
            self.prs[number] = pr
            return pr
        if path.startswith("/pulls/"):
            parts = path.split("/")
            pr = self.prs[int(parts[2])]
            return pr["_files"] if len(parts) == 4 else pr
        if path.startswith("/contents/"):
            file = path[len("/contents/"):]
            try:
                text = git(root, "show", query["ref"][0] + ":" + file) + "\n"
            except gh.Error:
                if missing:
                    return None
                raise
            import base64
            return dict(type="file", encoding="base64", content=base64.b64encode(text.encode()).decode())
        if path.startswith("/commits/") and path.endswith("/pulls"):
            return [self.prs[n] for n in self.links.get(path.split("/")[2], [])]
        if path.startswith("/git/ref/"):
            ref = "refs/" + path[len("/git/ref/"):]
            try:
                commit = git(root, "rev-parse", "--verify", ref)
            except gh.Error:
                if missing:
                    return None
                raise
            return dict(object=dict(type="commit", sha=commit))
        if path == "/git/refs":
            git(root, "update-ref", data["ref"], data["sha"], "0" * 40)
            return {}
        if path == "/git/blobs":
            blob = subprocess.check_output(["git", "hash-object", "-w", "--stdin"], cwd=root, input=data["content"].encode()).decode().strip()
            return dict(sha=blob)
        if path == "/git/trees":
            env = dict(self.fixture.env, GIT_INDEX_FILE=str(root.parent / "api-index"))
            gh.release.run(root, "git", "read-tree", data["base_tree"], env=env)
            for entry in data["tree"]:
                gh.release.run(root, "git", "update-index", "--add", "--cacheinfo", entry["mode"], entry["sha"], entry["path"], env=env)
            return dict(sha=gh.release.run(root, "git", "write-tree", env=env).strip())
        if path.startswith("/git/commits/"):
            return dict(tree=dict(sha=git(root, "rev-parse", path.split("/")[-1] + "^{tree}")))
        if path == "/git/commits":
            commit = git(root, "commit-tree", data["tree"], "-p", data["parents"][0], "-m", data["message"])
            return dict(sha=commit)
        if path.startswith("/actions/workflows/") and "/runs" in path:
            workflow = 10 if "/ci.yml/" in path else 20
            runs = [r for r in self.runs.values() if r["workflow_id"] == workflow]
            if "head_sha" in query:
                runs = [r for r in runs if r["head_sha"] == query["head_sha"][0]]
            return dict(workflow_runs=runs if query.get("page", ["1"])[0] == "1" else [])
        if path.startswith("/actions/runs/"):
            parts = path.split("/")
            run_id = int(parts[3])
            run = self.attempts.get((run_id, int(parts[5])), self.runs.get(run_id)) if "/attempts/" in path else self.runs[run_id]
            if parts[-1] == "rerun":
                self.reruns.append(run_id)
                return {}
            if parts[-1] == "artifacts":
                return dict(artifacts=[a for a in self.artifacts.values() if a["run_id"] == run_id])
            if parts[-1] == "jobs":
                if run_id in self.current_jobs:
                    return dict(jobs=self.current_jobs[run_id])
                return dict(jobs=run["_jobs"])
            return run
        if path.startswith("/actions/artifacts/"):
            return self.artifacts[int(path.split("/")[3])]["_zip"]
        if path.endswith("/dispatches"):
            return None
        raise AssertionError((method, path, data))

    def merge(self, number):
        pr = self.prs[number]
        git(self.fixture.root, "merge", "--squash", pr["head"]["sha"])
        commit = self.fixture.commit()
        pr.update(merged=True, merged_by=copy.deepcopy(HUMAN), state="closed", merge_commit_sha=commit,
                  merged_at=self.tick(), closed_at=self.tick())
        self.links[commit] = [number]
        self.ci(commit)
        return commit

    def tick(self):
        value = f"2026-11-15T00:{self.clock // 60:02d}:{self.clock % 60:02d}Z"
        self.clock += 1
        return value

    def ci(self, commit, conclusion="success"):
        run_id = 100 + len([r for r in self.runs.values() if r["workflow_id"] == 10])
        self.runs[run_id] = self.run(run_id, commit, 10, conclusion, event="push")
        return self.runs[run_id]

    def run(self, run_id, commit, workflow=20, conclusion="success", event="workflow_dispatch"):
        number = self.runs.get(run_id, {}).get("run_number")
        if number is None:
            number = self.sequence.get(workflow, 0) + 1
            self.sequence[workflow] = number
        return dict(id=run_id, head_sha=commit, workflow_id=workflow, run_number=number,
                    path=".github/workflows/" + ("ci.yml" if workflow == 10 else gh.REQUEST_WORKFLOW if workflow == 30 else gh.WORKFLOW),
                    run_attempt=1, head_branch="main", event=event, created_at=self.tick(),
                    repository=dict(full_name=self.repository), head_repository=dict(full_name=self.repository),
                    status="completed", conclusion=conclusion, actor=copy.deepcopy(HUMAN), triggering_actor=copy.deepcopy(HUMAN))

    def record(self, plan, success=True, execute="success", write="success"):
        run_id = plan["run"]
        commit = git(self.fixture.root, "rev-parse", "refs/remotes/origin/main")
        run = self.run(run_id, commit, conclusion="success" if success else "failure")
        run["run_attempt"] = plan["attempt"]
        failures = json.dumps([f["request"] for f in plan.get("failures", [])])
        run["_jobs"] = [dict(name=f"Execute request-{plan['action'].get('request', '')} blocked-{failures}", conclusion=execute), dict(name=f"Write request-{plan['action'].get('request', '')} blocked-{failures}", conclusion=write)]
        run["run_started_at"] = run["created_at"]
        run["updated_at"] = self.tick()
        self.runs[run_id] = run
        self.attempts[(run_id, plan["attempt"])] = copy.deepcopy(run)
        archive = io.BytesIO()
        with zipfile.ZipFile(archive, "w") as zipped:
            zipped.writestr("plan.json", gh.canonical(plan))
        artifact_id = run_id if plan["attempt"] == 1 else run_id * 10 + plan["attempt"]
        self.artifacts[artifact_id] = dict(id=artifact_id, run_id=run_id, name=f"module-plan-{run_id}-{plan['attempt']}", expired=False, _zip=archive.getvalue())
        self.current_jobs.pop(run_id, None)


class FixtureDriver(gh.Driver):
    def __init__(self, *args, **kwargs):
        kwargs.setdefault("now", datetime(2026, 11, 15, 1, tzinfo=timezone.utc))
        super().__init__(*args, **kwargs)

    @contextmanager
    def check(self, commit):
        self.ancestor(commit)
        with gh.release.source_tree(self.root, commit) as source:
            yield gh.release.Release(self.root, source, commit, self.api.fixture.policy, self.api.fixture.env)


class GitHubTransitions(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory(prefix="module-github-test-")
        self.addCleanup(temp.cleanup)
        isolation = mock.patch.dict(gh.os.environ, fixtures.fixture_environment(Path(temp.name)), clear=True)
        isolation.start()
        self.addCleanup(isolation.stop)
        self.fixture = fixtures.Fixture(Path(temp.name))
        f = self.fixture
        f.write(".github/workflows/ci.yml", (HERE.parent / "workflows/ci.yml").read_text())
        f.write(".github/workflows/" + gh.WORKFLOW, (HERE.parent / "workflows" / gh.WORKFLOW).read_text())
        f.write(".github/workflows/" + gh.REQUEST_WORKFLOW, (HERE.parent / "workflows" / gh.REQUEST_WORKFLOW).read_text())
        self.initial = f.commit()
        self.api = FakeGitHub(f)
        self.run_id = 1000
        self.now = datetime(2026, 11, 15, 1, tzinfo=timezone.utc)
        original_environment = gh.release.public_environment

        def environment(path):
            return dict(original_environment(path), GOPROXY=f.proxy.as_uri(), GOSUMDB="off")
        self.patch = mock.patch.object(gh.release, "public_environment", side_effect=environment)
        self.patch.start()
        self.addCleanup(self.patch.stop)

    def driver(self):
        return FixtureDriver(self.api, self.fixture.root, 70, 700, self.run_id, now=self.now)

    def request(self, batch, supersedes=None):
        tx = self.driver().request_plan(batch, HUMAN, supersedes)
        gh.write_transaction(self.api, tx)
        number = max(self.api.prs)
        merged = self.api.merge(number)
        self.run_id += 1
        return number, merged

    def step(self, publish=True):
        driver = self.driver()
        plan = driver.plan()
        action = plan["action"]
        if action["kind"] not in ("wait", "retry"):
            result = driver.execute(action)
            if action["kind"] in ("release", "prepare"):
                self.api.current_jobs[self.run_id] = [dict(name="Execute", conclusion="success")]
                transaction = driver.validate_write(plan, result)
                if action["kind"] == "release" and publish:
                    self.fixture.publish(action["module"], action["version"], action["candidate"])
                gh.write_transaction(self.api, transaction)
        self.api.record(plan)
        self.run_id += 1
        return action

    def retry(self, request):
        plan = self.driver().plan(request, HUMAN)
        self.api.record(plan, execute="skipped", write="skipped")
        self.run_id += 1
        return plan

    def test_executable_request_qualify_tag_public_proof_and_duplicates(self):
        number, candidate = self.request({DEP: "v0.1.0"})
        self.assertNotEqual(candidate, self.api.prs[number]["head"]["sha"], "must exercise squash identity")
        self.assertEqual(self.step()["kind"], "release")
        self.assertEqual(self.step()["kind"], "prove")
        count = len(self.api.writes)
        self.assertEqual(self.step()["kind"], "wait")
        self.assertEqual(self.step()["kind"], "wait")
        self.assertEqual(len(self.api.writes), count)
        self.assertFalse(any(method in ("PATCH", "DELETE") or path.endswith("/merge") for method, path, _ in self.api.writes))

    def test_request_authority_rejects_wrong_app_fork_bot_merge_unlinked_and_changed_bytes(self):
        number, _ = self.request({DEP: "v0.1.0"})
        original = copy.deepcopy(self.api.prs[number])
        for edit, message in (
            (lambda p: p["user"].update(id=701), "designated App"),
            (lambda p: p["head"]["repo"].update(full_name="attacker/fork"), "fork"),
            (lambda p: p.update(merged_by=BOT), "human"),
            (lambda p: self.api.links.clear(), "linked"),
        ):
            self.api.prs[number] = copy.deepcopy(original)
            self.api.links[original["merge_commit_sha"]] = [number]
            edit(self.api.prs[number])
            writes = len(self.api.writes)
            with self.assertRaisesRegex(gh.Error, message):
                self.driver().plan()
            self.assertEqual(len(self.api.writes), writes)
        self.api.prs[number] = original
        self.api.links[original["merge_commit_sha"]] = [number]
        original_file = self.api.file
        def changed(path, ref):
            value = original_file(path, ref)
            if ref == original["merge_commit_sha"]:
                value = value.replace("v0.1.0", "v0.2.0")
            return value
        with mock.patch.object(self.api, "file", side_effect=changed), self.assertRaisesRegex(gh.Error, "differs"):
            self.driver().plan()

    def test_ci_identity_mismatches_expiry_and_safe_original_rerun(self):
        _, candidate = self.request({DEP: "v0.1.0"})
        run = next(r for r in self.api.runs.values() if r["workflow_id"] == 10)
        original = copy.deepcopy(run)
        for key, value in (("workflow_id", 99), ("head_sha", self.initial), ("run_attempt", 2),
                           ("head_repository", dict(full_name="attacker/fork")), ("event", "pull_request")):
            self.api.runs[original["id"]] = copy.deepcopy(original)
            # Change only the fetched attempt, not the discovery summary.
            attempt = dict(original, **{key: value})
            real_call = self.api.call
            def call(method, path, data=None, missing=False):
                if "/attempts/" in path:
                    return attempt
                return real_call(method, path, data, missing)
            with mock.patch.object(self.api, "call", side_effect=call), self.assertRaises(gh.Error):
                self.driver().ci(candidate)
        self.api.runs[original["id"]] = dict(original, created_at="2025-01-01T00:00:00Z")
        self.assertIsNone(self.driver().ci(candidate))
        self.api.runs[original["id"]]["conclusion"] = "cancelled"
        with self.assertRaisesRegex(gh.Error, "expired"):
            self.driver().ci(candidate)
        self.api.runs[original["id"]] = dict(original, conclusion="cancelled")
        action = self.driver().plan()["action"]
        self.assertEqual(action["kind"], "recover-ci")
        # New main activity must not substitute its SHA or cancel its CI.
        self.fixture.write("unrelated", "new main\n")
        newer = self.fixture.commit()
        new_run = self.api.ci(newer)
        self.driver().execute(action)
        self.assertEqual(self.api.reruns, [original["id"]])
        self.assertEqual(new_run["conclusion"], "success")
        self.api.runs[original["id"]].update(run_attempt=3)
        with self.assertRaisesRegex(gh.Error, "budget"):
            self.driver().ci(candidate)

    def test_no_silent_new_tip_and_immutable_tag_conflict(self):
        _, candidate = self.request({DEP: "v0.1.0"})
        self.fixture.write("unrelated", "later commit\n")
        newer = self.fixture.commit()
        self.api.ci(newer)
        action = self.driver().plan()["action"]
        self.assertEqual(action["candidate"], candidate)
        self.fixture.publish(DEP, "v0.1.0", newer)
        plan = self.driver().plan()
        self.assertEqual(plan["action"]["kind"], "fail")
        with self.assertRaisesRegex(gh.Error, "conflict"):
            self.driver().execute(plan["action"])
        writes = len(self.api.writes)
        with self.assertRaisesRegex(gh.Error, "conflict"):
            gh.write_transaction(self.api, dict(kind="tag", tag=DEP + "/v0.1.0", sha=candidate))
        self.assertEqual(len(self.api.writes), writes)

    def add_consumer(self):
        f = self.fixture
        # An already published public pin, then candidate dependency source.
        f.publish(DEP, "v0.1.0", self.initial)
        f.add_module(APP, 'package openai\nimport "' + fixtures.NS + '/' + DEP + '"\nfunc New() { ssefilter.New() }\n',
                     "\nrequire " + fixtures.NS + "/" + DEP + " v0.1.0\n")
        gh.release.run(f.root / APP, "go", "mod", "tidy", env=f.env)
        f.write(DEP + "/fixture.go", "package ssefilter\nfunc New() {}\nfunc CandidateOnly() {}\n")
        f.commit()

    def test_preparation_proves_unchanged_unselected_public_pins(self):
        self.add_consumer()
        f = self.fixture
        f.add_module(SUPPORT, "package adaptersupport\nfunc Ready() {}\n", role="support")
        f.write(SUPPORT + "/filewatch/watch.go", "package filewatch\n")
        f.write(SUPPORT + "/soulbody/body.go", "package soulbody\n")
        f.write(SUPPORT + "/fixture_test.go", 'package adaptersupport\nimport "testing"\nfunc TestBroken(t *testing.T) { t.Fatal("unchanged support public closure fails") }\n')
        support = f.commit()
        f.publish(SUPPORT, "v0.1.0", support)
        f.write(APP + "/fixture.go", 'package openai\nimport ("' + fixtures.NS + '/' + DEP + '"; "' + fixtures.NS + '/' + SUPPORT + '")\nfunc New() { ssefilter.New(); adaptersupport.Ready() }\n')
        gh.release.run(f.root / APP, "go", "mod", "edit", "-require=" + fixtures.NS + "/" + SUPPORT + "@v0.1.0", env=f.env)
        gh.release.run(f.root / APP, "go", "mod", "tidy", env=f.env)
        f.commit()
        self.request({APP: "v0.1.0", DEP: "v0.2.0"})
        self.step()
        self.step()
        action = self.driver().plan()["action"]
        self.assertEqual((action["kind"], action["updates"]), ("prepare", {DEP: "v0.2.0"}))
        writes = len(self.api.writes)
        with self.assertRaisesRegex(gh.Error, "unchanged support public closure fails"):
            self.driver().execute(action)
        self.assertEqual(len(self.api.writes), writes)
        self.assertEqual(git(f.root, "tag", "--list", APP + "/*"), "")

    def test_public_dependency_then_real_selective_pin_tidy_pr_and_human_merge(self):
        self.add_consumer()
        number, candidate = self.request({APP: "v0.1.0", DEP: "v0.2.0"})
        self.assertEqual(self.step()["module"], DEP)
        self.assertEqual(self.step()["kind"], "prove")
        self.fixture.write(APP + "/fixture.go", 'package openai\nimport "' + fixtures.NS + '/' + DEP + '"\nfunc New() { ssefilter.CandidateOnly() }\n')
        new_source = self.fixture.commit()
        action = self.step()
        self.assertEqual(action["kind"], "prepare")
        self.assertEqual(action["base"], new_source)
        prep = self.api.prs[max(self.api.prs)]
        paths = {f["filename"] for f in prep["_files"]}
        self.assertIn(APP + "/go.mod", paths)
        self.assertIn(APP + "/go.sum", paths)
        self.assertTrue(all(p.startswith(APP + "/") or p.startswith(gh.PREFIX) for p in paths))
        self.assertIn("CandidateOnly", prep["body"])
        self.assertEqual(self.step()["kind"], "wait")
        merged = self.api.merge(prep["number"])
        action = self.step()
        self.assertEqual((action["kind"], action["module"], action["candidate"]), ("release", APP, merged))
        self.assertNotEqual(merged, candidate)
        self.assertEqual(self.step()["kind"], "prove")
        self.assertEqual(self.step()["kind"], "wait")

    def test_closed_preparation_pauses_until_explicit_retry_and_fresh_merge(self):
        self.add_consumer()
        number, _ = self.request({APP: "v0.1.0", DEP: "v0.2.0"})
        self.step()
        self.step()
        self.step()
        prep = self.api.prs[max(self.api.prs)]
        prep.update(state="closed", closed_at=self.api.tick())
        writes = len(self.api.writes)
        for _ in range(3):
            self.assertEqual(self.step()["kind"], "wait")
        self.assertEqual(len(self.api.writes), writes)
        self.retry(number)
        self.assertEqual(self.step()["kind"], "prepare")
        fresh = self.api.prs[max(self.api.prs)]
        self.assertNotEqual(prep["number"], fresh["number"])
        self.assertEqual(self.step()["kind"], "wait")
        self.api.merge(fresh["number"])
        self.assertEqual(self.step()["kind"], "release")

    def test_failed_public_proof_stops_entire_batch_until_human_retry(self):
        self.fixture.add_module(SUPPORT, "package adaptersupport\n", role="support")
        self.fixture.write(SUPPORT + "/filewatch/watch.go", "package filewatch\n")
        self.fixture.write(SUPPORT + "/soulbody/body.go", "package soulbody\n")
        self.fixture.commit()
        number, _ = self.request({DEP: "v0.1.0", SUPPORT: "v0.1.0"})
        first = self.step()["module"]
        remaining = ({DEP, SUPPORT} - {first}).pop()
        plan = self.driver().plan()
        self.assertEqual(plan["action"]["kind"], "prove")
        with mock.patch.object(gh.release.Release, "prove", side_effect=gh.Error("offline proxy missing")) as prove:
            with self.assertRaisesRegex(gh.Error, "published-but-unverified"):
                self.driver().execute(plan["action"])
            self.assertEqual(prove.call_count, 3)
        self.api.record(plan, success=False, execute="failure", write="skipped")
        self.run_id += 1
        for _ in range(3):
            self.assertEqual(self.step()["kind"], "wait")
        self.assertEqual(git(self.fixture.root, "tag", "--list", remaining + "/*"), "")
        with self.assertRaises(gh.Error):
            self.driver().plan(number, BOT)
        self.retry(number)
        self.assertEqual(self.step()["kind"], "prove")
        self.assertEqual(self.step()["module"], remaining)

    def test_three_events_displaced_pending_discover_all_and_serialize_overlaps(self):
        first, first_sha = self.request({DEP: "v0.1.0"})
        # Two more reviewed requests while a running discovery holds the lock;
        # GitHub may discard the middle pending event. Neither event is input
        # to Driver.plan: the surviving run must discover every request itself.
        second, _ = self.request({DEP: "v0.2.0"})
        third, _ = self.request({DEP: "v0.3.0"})
        # Model a replaced pending run and an older still-queued slot. Neither
        # has executed a plan or owns any batch progress.
        tip = git(self.fixture.root, "rev-parse", "refs/remotes/origin/main")
        cancelled = self.api.run(900, tip, conclusion="cancelled")
        cancelled["_jobs"] = []
        queued = self.api.run(901, tip)
        queued.update(status="queued", conclusion=None)
        self.api.runs.update({900: cancelled, 901: queued})
        self.assertEqual(len(self.driver().requests(list(self.api.prs.values()))), 3)
        self.assertEqual(self.step()["request"], first)
        self.assertEqual(self.step()["kind"], "prove")
        self.assertEqual(self.step()["request"], second)
        self.assertEqual(self.step()["kind"], "prove")
        self.assertEqual(self.step()["request"], third)
        self.assertEqual(self.step()["kind"], "prove")
        self.assertEqual(self.step()["kind"], "wait")
        self.assertEqual(git(self.fixture.root, "rev-parse", "refs/tags/" + DEP + "/v0.1.0"), first_sha)

    def test_missing_expired_or_wrong_workflow_evidence_fails_closed(self):
        self.request({DEP: "v0.1.0"})
        self.step()
        recorded = next(r for r in self.api.runs.values() if r["workflow_id"] == 20)
        artifact = self.api.artifacts[recorded["id"]]
        artifact["expired"] = True
        self.assertEqual(self.driver().plan()["action"]["kind"], "wait")
        requests = self.driver().requests(list(self.api.prs.values()))
        self.assertIn("expired", self.driver().history(requests)[0]["action"]["reason"])
        artifact["expired"] = False
        self.api.attempts[(recorded["id"], 1)]["head_repository"] = dict(full_name="fork/repo")
        self.assertEqual(self.driver().plan()["action"]["kind"], "wait")
        self.assertIn("provenance", self.driver().history(requests)[0]["action"]["reason"])
        recorded["head_repository"] = dict(full_name=self.api.repository)
        recorded["workflow_id"] = 99
        # Simulate a forged attempt response with the discovery still correct.
        with self.assertRaisesRegex(gh.Error, "workflow"):
            self.driver().trusted_run(recorded)

    def test_closed_request_is_not_authority_and_exact_mapping_no_expansion(self):
        tx = self.driver().request_plan({DEP: "v0.1.0"}, HUMAN)
        request = gh.document(next(iter(tx["files"].values())))
        self.assertEqual(request["batch"], {DEP: "v0.1.0"})
        gh.write_transaction(self.api, tx)
        pr = self.api.prs[max(self.api.prs)]
        pr.update(state="closed", closed_at=self.api.tick())
        self.assertEqual(self.driver().plan()["action"]["kind"], "wait")
        with self.assertRaises(gh.Error):
            self.driver().request_plan({"../engine": "v0.1.0"}, HUMAN)
        with self.assertRaises(gh.Error):
            gh.document('{"engine":"v1.0.0","engine":"v1.0.1"}')

    def test_old_ci_workflow_and_rerun_ineligible_are_durable_failures(self):
        self.fixture.write(".github/workflows/ci.yml", "name: CI\nconcurrency:\n  group: ci-main\n")
        self.fixture.commit()
        number, _ = self.request({DEP: "v0.1.0"})
        plan = self.driver().plan()
        self.assertEqual(plan["action"]["kind"], "fail")
        self.assertIn("unsafe old", plan["action"]["reason"])
        self.api.record(plan, success=False, execute="failure")
        self.run_id += 1
        self.assertEqual(self.driver().plan()["action"]["kind"], "wait")
        self.retry(number)
        self.assertEqual(self.driver().plan()["action"]["kind"], "fail")

    def test_partial_batch_qualification_failure_preserves_prior_proof_and_stops(self):
        self.fixture.add_module(SUPPORT, "package adaptersupport\n", role="support")
        self.fixture.write(SUPPORT + "/filewatch/watch.go", "package filewatch\n")
        self.fixture.write(SUPPORT + "/soulbody/body.go", "package soulbody\n")
        self.fixture.write(DEP + "/fixture_test.go", 'package ssefilter\nimport "testing"\nfunc TestFail(t *testing.T) { t.Fatal("deliberate qualification failure") }\n')
        self.fixture.commit()
        number, candidate = self.request({DEP: "v0.1.0", SUPPORT: "v0.1.0"})
        self.assertEqual(self.step()["module"], SUPPORT)
        self.assertEqual(self.step()["kind"], "prove")
        plan = self.driver().plan()
        with self.assertRaisesRegex(gh.Error, "deliberate qualification failure"):
            self.driver().execute(plan["action"])
        self.api.record(plan, success=False, execute="failure", write="skipped")
        self.run_id += 1
        count = len(self.api.writes)
        self.assertEqual(self.step()["kind"], "wait")
        self.assertEqual(len(self.api.writes), count)
        self.retry(number)
        action = self.driver().plan()["action"]
        self.assertEqual((action["kind"], action["module"], action["candidate"]), ("release", DEP, candidate))
        self.assertEqual(git(self.fixture.root, "tag", "--list", DEP + "/*"), "")

    def test_preparation_artifacts_cannot_expand_writes_or_change_direct_dependencies(self):
        self.add_consumer()
        self.request({APP: "v0.1.0", DEP: "v0.2.0"})
        self.step()
        self.step()
        driver = self.driver()
        plan = driver.plan()
        result = driver.execute(plan["action"])
        self.api.current_jobs[self.run_id] = [dict(name="Execute", conclusion="success")]
        edits = (
            lambda r: r["files"].update({DEP + "/go.mod": "bad"}),
            lambda r: r.update(base=self.initial),
            lambda r: r["files"].update({APP + "/go.mod": r["files"][APP + "/go.mod"] + "\nreplace example.com/a => ../local\n"}),
            lambda r: r["files"].update({APP + "/go.mod": r["files"][APP + "/go.mod"] + "\nrequire example.com/unrelated v1.0.0\n"}),
        )
        count = len(self.api.writes)
        for edit in edits:
            changed = copy.deepcopy(result)
            edit(changed)
            with self.assertRaises(gh.Error):
                driver.validate_write(plan, changed)
        wrong_attempt = dict(plan, attempt=2)
        with self.assertRaisesRegex(gh.Error, "run mismatch"):
            driver.validate_write(wrong_attempt, result)
        self.assertEqual(len(self.api.writes), count)
        tx = driver.validate_write(plan, result)
        gh.write_transaction(self.api, tx)
        self.assertEqual(self.api.prs[max(self.api.prs)]["state"], "open")

    def test_tag_write_partial_failure_requires_retry_and_fresh_qualification(self):
        number, candidate = self.request({DEP: "v0.1.0"})
        driver = self.driver()
        plan = driver.plan()
        result = driver.execute(plan["action"])
        self.api.current_jobs[self.run_id] = [dict(name="Execute", conclusion="success")]
        tx = driver.validate_write(plan, result)
        gh.write_transaction(self.api, tx)
        # Simulate an interruption after the immutable ref reached GitHub, but
        # before the protected job could record success.
        self.api.record(plan, success=False, write="failure")
        self.run_id += 1
        self.assertEqual(self.step()["kind"], "wait")
        self.retry(number)
        next_plan = self.driver().plan()
        self.assertEqual(next_plan["action"]["kind"], "release", "tag existence is not qualification authority")
        result = self.driver().execute(next_plan["action"])
        self.api.current_jobs[self.run_id] = [dict(name="Execute", conclusion="success")]
        tx = self.driver().validate_write(next_plan, result)
        count = len(self.api.writes)
        gh.write_transaction(self.api, tx)
        self.assertEqual(len(self.api.writes), count, "same-SHA resume must not move the tag")
        self.assertEqual(git(self.fixture.root, "rev-parse", "refs/tags/" + DEP + "/v0.1.0"), candidate)

    def test_new_reviewed_request_can_retire_expired_evidence_without_reusing_it(self):
        first, _ = self.request({DEP: "v0.1.0"})
        plan = self.driver().plan()
        self.api.record(plan, success=False, execute="failure", write="skipped")
        self.run_id += 1
        self.api.artifacts[plan["run"]]["expired"] = True
        self.assertEqual(self.driver().plan()["action"]["kind"], "wait")
        second, _ = self.request({DEP: "v0.1.0"}, [first])
        action = self.driver().plan()["action"]
        self.assertEqual((action["kind"], action["request"]), ("release", second))
        self.assertNotEqual(action["candidate"], plan["action"]["candidate"])

    def test_recovery_api_ineligible_stops_batch_and_does_not_dispatch_replacement_ci(self):
        number, candidate = self.request({DEP: "v0.1.0"})
        ci = next(r for r in self.api.runs.values() if r["workflow_id"] == 10)
        ci["conclusion"] = "cancelled"
        plan = self.driver().plan()
        original = self.api.call
        def ineligible(method, path, data=None, missing=False):
            if path == f"/actions/runs/{ci['id']}/rerun":
                raise gh.Error("GitHub rerun HTTP 422: ineligible")
            return original(method, path, data, missing)
        with mock.patch.object(self.api, "call", side_effect=ineligible), self.assertRaisesRegex(gh.Error, "ineligible"):
            self.driver().execute(plan["action"])
        self.api.record(plan, success=False, execute="skipped", write="skipped")
        self.run_id += 1
        count = len(self.api.writes)
        for _ in range(3):
            self.assertEqual(self.step()["kind"], "wait")
        self.assertEqual(len(self.api.writes), count)
        self.assertFalse(any(path.endswith("/dispatches") for _, path, _ in self.api.writes))

    def test_candidate_release_scripts_are_never_executed(self):
        self.fixture.write(".github/scripts/module-release.py", "raise SystemExit('candidate script must not run')\n")
        self.fixture.commit()
        self.request({DEP: "v0.1.0"})
        self.assertEqual(self.step()["kind"], "release")
        self.assertEqual(self.step()["kind"], "prove")

    def test_cli_executes_discovery_with_injected_github_api(self):
        number, candidate = self.request({DEP: "v0.1.0"})
        current = self.api.run(self.run_id, candidate)
        current.update(status="in_progress", conclusion=None)
        self.api.runs[self.run_id] = current
        output = self.fixture.root.parent / "cli-plan.json"
        env = dict(MODULE_RELEASES_ENABLED="true", GITHUB_REF="refs/heads/main",
                   GITHUB_REPOSITORY=self.api.repository, RELEASE_APP_ID="70", RELEASE_APP_BOT_ID="700",
                   GITHUB_RUN_ID=str(self.run_id), GITHUB_RUN_ATTEMPT="1", GITHUB_EVENT_NAME="workflow_dispatch")
        argv = [str(HERE / "module-release-github.py"), "plan", "--root", str(self.fixture.root), "--output", str(output)]
        with mock.patch.dict(gh.os.environ, env), mock.patch.object(sys, "argv", argv), \
                mock.patch.object(gh, "GitHub", return_value=self.api), mock.patch.object(gh, "Driver", FixtureDriver):
            self.assertEqual(gh.main(), 0)
        action = gh.document(output.read_text())["action"]
        self.assertEqual((action["kind"], action["request"], action["candidate"]), ("release", number, candidate))

    def test_whole_batch_shared_validation_precedes_even_independent_publication(self):
        f = self.fixture
        f.add_module(SUPPORT, "package adaptersupport\n", role="support")
        f.add_module(APP, "package openai\nfunc New() {}\n", "\nrequire " + fixtures.NS + "/" + SUPPORT + " v0.1.0\n")
        base = f.commit()
        # Model an App PR edited before human merge: identity/merge is necessary,
        # but cannot replace shared validation of the exact reviewed mapping.
        request = dict(schema=1, kind="request", base=base, batch={DEP: "v0.1.0", APP: "v0.1.0"}, nonce="999-1", supersedes=[])
        key = gh.digest(request)
        gh.write_transaction(self.api, dict(kind="request", base=base, branch="module-release/request-" + key,
                                           files={gh.PREFIX + "request-" + key + ".json": gh.canonical(request)},
                                           title="Exact mapping", body="Review exact map"))
        self.api.merge(max(self.api.prs))
        count = len(self.api.writes)
        action = self.driver().plan()["action"]
        self.assertEqual(action["kind"], "fail")
        self.assertIn("missing published prerequisite", action["reason"])
        self.assertEqual(len(self.api.writes), count)
        self.assertEqual(git(f.root, "tag", "--list"), "")

    def test_failed_transition_waits_for_explicit_human_retry_without_writes(self):
        number, candidate = self.request({DEP: "v0.1.0"})
        plan = self.driver().plan()
        self.assertEqual(plan["action"]["kind"], "release")
        # A failed qualification run has durable evidence but no tag. Exercise
        # reconciliation, not qualification itself (covered by integration).
        self.api.record(plan, success=False, execute="failure", write="skipped")
        self.run_id += 1
        writes = len(self.api.writes)
        for _ in range(3):
            self.assertEqual(self.driver().plan()["action"]["kind"], "wait")
        with self.assertRaisesRegex(gh.Error, "human"):
            self.driver().plan(number, BOT)
        self.retry(number)
        action = self.driver().plan()["action"]
        self.assertEqual((action["kind"], action["candidate"]), ("release", candidate))
        self.assertEqual(len(self.api.writes), writes)
        self.assertEqual(git(self.fixture.root, "tag", "--list"), "")

    def current(self, workflow=20):
        tip = git(self.fixture.root, "rev-parse", "refs/remotes/origin/main")
        self.api.runs[self.run_id] = self.api.run(self.run_id, tip, workflow)
        self.api.runs[self.run_id].update(status="in_progress", conclusion=None, _jobs=[])

    def cli(self, command, value=None, result=None, **changes):
        directory = self.fixture.root.parent
        output = directory / (command + "-output.json")
        if output.exists():
            output.unlink()
        argv = [str(HERE / "module-release-github.py"), command, "--root", str(self.fixture.root), "--output", str(output)]
        if value is not None:
            path = directory / (command + "-input.json")
            path.write_text(gh.canonical(value))
            argv.extend(["--input", str(path)])
        if result is not None:
            path = directory / (command + "-result.json")
            path.write_text(gh.canonical(result))
            argv.extend(["--result", str(path)])
        env = {k: v for k, v in self.fixture.env.items()
               if k in ("PATH", "HOME", "LANG", "XDG_CONFIG_HOME", "TMPDIR") or k.startswith("GIT_")}
        env.update(MODULE_RELEASES_ENABLED="true", GITHUB_REF="refs/heads/main", GITHUB_REPOSITORY=self.api.repository,
                   RELEASE_APP_ID="70", RELEASE_APP_BOT_ID="700", GH_TOKEN="read-only-fixture",
                   GITHUB_RUN_ID=str(self.run_id), GITHUB_RUN_ATTEMPT="1", GITHUB_EVENT_NAME="workflow_dispatch",
                   MODULE_BATCH=gh.canonical({DEP: "v0.1.0"}), SUPERSEDES_REQUESTS="[]", RETRY_REQUEST="")
        if command == "write":
            env["MODULE_RELEASE_WRITE_TOKEN"] = "late-fixture"
        env.update(changes)
        stderr = io.StringIO()
        with mock.patch.dict(gh.os.environ, env, clear=True), mock.patch.object(sys, "argv", argv), \
                mock.patch.object(gh, "GitHub", return_value=self.api), mock.patch.object(gh, "Driver", FixtureDriver), \
                mock.patch.object(gh, "protection_check") as guard, redirect_stderr(stderr), redirect_stdout(io.StringIO()):
            self.assertEqual(gh.release.public_environment(directory)["TMPDIR"], self.fixture.env["TMPDIR"])
            self.assertTrue(Path(self.fixture.env["TMPDIR"]).is_dir())
            code = gh.main()
            if command in ("request-validate", "validate-write") and code == 0:
                guard.assert_called_once_with(self.api.repository, 70, 700)
        return code, gh.document(output.read_text()) if output.exists() else stderr.getvalue()

    def test_preparation_rejects_module_source_landing_between_reviewed_base_and_merge(self):
        self.add_consumer()
        self.request({APP: "v0.1.0", DEP: "v0.2.0"})
        self.step()
        self.step()
        self.step()
        prep = self.api.prs[max(self.api.prs)]
        self.fixture.write(APP + "/new.go", "package openai\nfunc Unreviewed() {}\n")
        self.fixture.commit()
        self.api.merge(prep["number"])
        count = len(self.api.writes)
        action = self.driver().plan()["action"]
        self.assertEqual(action["kind"], "fail")
        self.assertIn("changed after reviewed preparation base", action["reason"])
        self.assertEqual(len(self.api.writes), count)
        self.assertEqual(git(self.fixture.root, "tag", "--list", APP + "/*"), "")

    def test_preparation_preserves_fixed_base_and_accepts_only_unrelated_main_advance(self):
        self.add_consumer()
        self.request({APP: "v0.1.0", DEP: "v0.2.0"})
        self.step()
        self.step()
        driver = self.driver()
        plan = driver.plan()
        result = driver.execute(plan["action"])
        base = plan["action"]["base"]
        self.fixture.write("unrelated-before-write", "main may keep moving\n")
        self.fixture.commit()
        self.api.current_jobs[self.run_id] = [dict(name=f"Execute request-{plan['action']['request']}", conclusion="success")]
        tx = driver.validate_write(plan, result)
        self.assertEqual(tx["base"], base)
        gh.write_transaction(self.api, tx)
        self.api.record(plan)
        self.run_id += 1
        self.fixture.write("unrelated-before-merge", "still not a new module candidate\n")
        self.fixture.commit()
        prep = self.api.prs[max(self.api.prs)]
        merged = self.api.merge(prep["number"])
        action = self.driver().plan()["action"]
        self.assertEqual((action["kind"], action["module"], action["candidate"]), ("release", APP, merged))

    def test_preparation_rejects_midflight_owning_policy_and_manifest_changes(self):
        self.add_consumer()
        self.request({APP: "v0.1.0", DEP: "v0.2.0"})
        self.step()
        self.step()
        self.step()
        prep = self.api.prs[max(self.api.prs)]
        self.fixture.write("policy.md", "Unreviewed compatibility policy change\n")
        self.fixture.commit()
        self.api.merge(prep["number"])
        action = self.driver().plan()["action"]
        self.assertEqual(action["kind"], "fail")
        self.assertIn("source/API/changelog", action["reason"])
        # Even explicitly allowed filenames must retain the reviewed contents.
        request = self.driver().requests(list(self.api.prs.values()))[0]
        with self.assertRaises(gh.Error):
            self.driver().preparations(list(self.api.prs.values()), request, APP)

    def test_old_waiting_run_cannot_hide_newer_failure_or_override_retry_chronology(self):
        number, candidate = self.request({DEP: "v0.1.0"})
        old_run = self.run_id
        wait = dict(schema=1, run=old_run, attempt=1, action=dict(kind="wait", reason="CI pending"))
        self.api.record(wait, execute="skipped", write="skipped")
        self.run_id += 1
        failure = self.driver().plan()
        self.api.record(failure, success=False, execute="failure", write="skipped")
        self.run_id += 1
        count = len(self.api.writes)
        old = FixtureDriver(self.api, self.fixture.root, 70, 700, old_run)
        self.assertEqual(old.plan()["action"]["kind"], "wait", "later run IDs cannot disappear")
        with self.assertRaisesRegex(gh.Error, "reruns"):
            FixtureDriver(self.api, self.fixture.root, 70, 700, old_run, 2).plan()
        self.retry(number)
        # A rerun of an OLD failed run happens after that accepted fresh retry.
        # It must not sort before/revive the retry just because its run ID is old.
        rerun = dict(failure, attempt=2)
        self.api.record(rerun, success=False, execute="failure", write="skipped")
        self.assertEqual(self.driver().plan()["action"]["kind"], "wait")
        self.retry(number)
        self.assertEqual(self.driver().plan()["action"]["candidate"], candidate)
        self.assertEqual(len(self.api.writes), count)

    def test_expired_scoped_history_does_not_block_independent_or_superseding_request(self):
        self.fixture.add_module(SUPPORT, "package adaptersupport\n", role="support")
        self.fixture.commit()
        old, _ = self.request({DEP: "v0.1.0"})
        failure = self.driver().plan()
        self.api.record(failure, success=False, execute="failure", write="skipped")
        self.run_id += 1
        other, _ = self.request({SUPPORT: "v0.1.0"})
        # The old request's later attempt overlaps the new request's lifetime.
        # Its durable job binding, not a global oldest-since window, scopes loss.
        self.api.record(dict(failure, attempt=2), success=False, execute="failure", write="skipped")
        for artifact in self.api.artifacts.values():
            artifact["expired"] = True
        self.assertEqual(self.driver().plan()["action"].get("request"), other)
        same, _ = self.request({DEP: "v0.2.0"})
        action = self.driver().plan()["action"]
        self.assertNotEqual(action.get("request"), same, "overlap with uncertain batch must remain locked")
        replacement, _ = self.request({DEP: "v0.3.0"}, [old, same, other])
        self.assertEqual(self.driver().plan()["action"].get("request"), replacement)

    def test_historical_workflow_revision_and_aged_successful_ci_remain_valid(self):
        number, candidate = self.request({DEP: "v0.1.0"})
        plan = self.driver().plan()
        self.api.record(plan, success=False, execute="failure", write="skipped")
        self.run_id += 1
        self.fixture.write(".github/workflows/" + gh.WORKFLOW, "# Later trusted main revision\n" + (HERE.parent / "workflows" / gh.WORKFLOW).read_text())
        self.fixture.commit()
        self.assertEqual(self.driver().plan()["action"]["kind"], "wait")
        self.retry(number)
        self.assertEqual(self.driver().plan()["action"]["kind"], "release")
        ci = next(r for r in self.api.runs.values() if r["workflow_id"] == 10)
        ci["created_at"] = "2020-01-01T00:00:00Z"
        self.assertIsNone(self.driver().ci(candidate, dict(run=ci["id"], attempt=1)))

    def test_cli_workflow_chain_fixed_intake_base_execution_write_and_continuation(self):
        self.fixture.write(DEP + "/credentials_test.go", 'package ssefilter\nimport ("os"; "testing")\nfunc TestNoWriteCredentials(t *testing.T) { for _, key := range []string{"MODULE_RELEASE_WRITE_TOKEN", "RELEASE_APP_PRIVATE_KEY", "RELEASE_SETTINGS_TOKEN", "RELEASE_APP_JWT"} { if os.Getenv(key) != "" { t.Fatal("write credential exposed to candidate") } } }\n')
        self.fixture.commit()
        self.current(30)
        code, request = self.cli("request-plan")
        self.assertEqual(code, 0, request)
        fixed = request["base"]
        self.fixture.write("intake-main-advance", "unrelated\n")
        self.fixture.commit()
        code, sealed = self.cli("request-validate", request)
        self.assertEqual(code, 0, sealed)
        self.assertEqual(sealed["base"], fixed)
        self.assertEqual(self.cli("write", sealed)[0], 0)
        number = max(self.api.prs)
        candidate = self.api.merge(number)
        self.run_id += 1
        self.current()
        code, plan = self.cli("plan")
        self.assertEqual(code, 0, plan)
        code, result = self.cli("execute", plan)
        self.assertEqual(code, 0, result)
        self.api.current_jobs[self.run_id] = [dict(name=f"Execute request-{number}", conclusion="success")]
        code, sealed = self.cli("validate-write", plan, result)
        self.assertEqual(code, 0, sealed)
        # Publish the file-proxy fixture between prevalidation and idempotent
        # tag write; the write still checks exact immutable commit identity.
        self.fixture.publish(DEP, "v0.1.0", candidate)
        self.assertEqual(self.cli("write", sealed)[0], 0)
        run_head = self.api.runs[self.run_id]["head_sha"]
        self.fixture.write("continuation-main-advance", "unrelated\n")
        tip = self.fixture.commit()
        self.assertNotEqual(run_head, tip)
        self.assertEqual(self.cli("continue", plan)[0], 0)
        continuation = (HERE.parent / "workflows" / gh.WORKFLOW).read_text().split("  continue:\n", 1)[1]
        self.assertIn("fetch-depth: 0", continuation)
        self.api.record(plan)
        self.run_id += 1
        self.current()
        code, proof_plan = self.cli("plan")
        self.assertEqual(code, 0, proof_plan)
        self.assertEqual(proof_plan["action"]["kind"], "prove")
        code, proof = self.cli("execute", proof_plan)
        self.assertEqual(code, 0, proof)
        self.assertEqual(proof["stage"], "public-verified")

    def test_cli_rejects_stale_attempt_nonwriter_and_nondispatch_retry_without_writes(self):
        number, _ = self.request({DEP: "v0.1.0"})
        self.current()
        plan = self.driver().plan()
        count = len(self.api.writes)
        for command in ("plan", "execute", "validate-write", "write", "continue"):
            code, error = self.cli(command, plan, GITHUB_RUN_ATTEMPT="2")
            self.assertEqual(code, 1)
            self.assertIn("stale run attempt", error)
        self.assertEqual(self.cli("plan", RETRY_REQUEST=str(number), GITHUB_EVENT_NAME="workflow_run")[0], 1)
        self.api.runs[self.run_id]["event"] = "workflow_run"
        code, error = self.cli("plan", RETRY_REQUEST=str(number), GITHUB_EVENT_NAME="workflow_run")
        self.assertEqual(code, 1)
        self.assertIn("retry requires human dispatch", error)
        self.api.runs[self.run_id]["event"] = "workflow_dispatch"
        self.api.permissions[HUMAN["login"]] = "read"
        self.assertEqual(self.cli("plan", RETRY_REQUEST=str(number))[0], 1)
        self.current(30)
        self.assertEqual(self.cli("request-plan")[0], 1)
        self.api.permissions[HUMAN["login"]] = "write"
        code, request = self.cli("request-plan")
        self.assertEqual(code, 0, request)
        self.api.permissions[HUMAN["login"]] = "read"
        self.assertEqual(self.cli("request-validate", request)[0], 1)
        self.assertEqual(len(self.api.writes), count)

    def test_three_intake_dispatches_survive_without_replaceable_pending_inputs(self):
        workflow = (HERE.parent / "workflows" / gh.REQUEST_WORKFLOW).read_text()
        self.assertIn("group: module-release-request-${{ github.run_id }}", workflow)
        pending = []
        for version in ("v0.1.0", "v0.2.0", "v0.3.0"):
            self.current(30)
            env = dict(MODULE_BATCH=gh.canonical({DEP: version}))
            code, tx = self.cli("request-plan", **env)
            self.assertEqual(code, 0, tx)
            pending.append((self.run_id, env, tx))
            self.run_id += 1
        self.fixture.write("main-advances-with-three-intakes", "unrelated\n")
        self.fixture.commit()
        for run_id, env, tx in pending:
            self.run_id = run_id
            code, sealed = self.cli("request-validate", tx, **env)
            self.assertEqual(code, 0, sealed)
            self.assertEqual(self.cli("write", sealed)[0], 0)
        self.assertEqual(len(self.api.prs), 3)
        maps = [gh.document(next(iter(tx["files"].values())))["batch"] for _, _, tx in pending]
        self.assertEqual(maps, [{DEP: "v0.1.0"}, {DEP: "v0.2.0"}, {DEP: "v0.3.0"}])

    def test_cli_preparation_chain_and_midflight_reviewed_manifest_guard(self):
        self.add_consumer()
        self.request({APP: "v0.1.0", DEP: "v0.2.0"})
        self.step()
        self.step()
        self.current()
        code, plan = self.cli("plan")
        self.assertEqual(code, 0, plan)
        self.assertEqual(plan["action"]["kind"], "prepare")
        code, result = self.cli("execute", plan)
        self.assertEqual(code, 0, result)
        self.fixture.write("main-moves-during-tidy", "unrelated\n")
        self.fixture.commit()
        self.api.current_jobs[self.run_id] = [dict(name=f"Execute request-{plan['action']['request']}", conclusion="success")]
        code, sealed = self.cli("validate-write", plan, result)
        self.assertEqual(code, 0, sealed)
        self.assertEqual(self.cli("write", sealed)[0], 0)
        self.api.record(plan)
        self.run_id += 1
        prep = self.api.prs[max(self.api.prs)]
        self.api.merge(prep["number"])
        # Model a merge conflict resolution that adds an unreviewed requirement.
        mod = self.fixture.root / APP / "go.mod"
        self.fixture.write(APP + "/go.mod", mod.read_text() + "\nrequire example.com/unreviewed v1.0.0\n")
        merged = self.fixture.commit()
        prep["merge_commit_sha"] = merged
        self.api.links[merged] = [prep["number"]]
        self.api.ci(merged)
        count = len(self.api.writes)
        action = self.driver().plan()["action"]
        self.assertEqual(action["kind"], "fail")
        self.assertIn("merged manifests differ", action["reason"])
        self.assertEqual(len(self.api.writes), count)

    def test_completed_batch_expired_or_missing_artifact_is_scoped_not_tag_success(self):
        self.fixture.add_module(SUPPORT, "package adaptersupport\n", role="support")
        self.fixture.commit()
        old, _ = self.request({DEP: "v0.1.0"})
        self.step()
        self.step()
        other, _ = self.request({SUPPORT: "v0.1.0"})
        saved = copy.deepcopy(self.api.artifacts)
        for missing in (False, True):
            self.api.artifacts = copy.deepcopy(saved)
            if missing:
                self.api.artifacts.clear()
            else:
                for artifact in self.api.artifacts.values():
                    artifact["expired"] = True
            action = self.driver().plan()["action"]
            self.assertEqual((action["kind"], action["request"]), ("release", other))
            requests = self.driver().requests(list(self.api.prs.values()))
            uncertain = [r for r in self.driver().history(requests) if r["action"]["kind"] == "uncertain"]
            self.assertEqual({r["action"]["request"] for r in uncertain}, {old})
        # Unknown historical schemas likewise affect only their bound request.
        self.api.artifacts = saved
        first = next(iter(self.api.artifacts.values()))
        with zipfile.ZipFile(io.BytesIO(first["_zip"])) as archive:
            plan = gh.document(archive.read("plan.json"))
        plan["schema"] = 999
        buffer = io.BytesIO()
        with zipfile.ZipFile(buffer, "w") as archive:
            archive.writestr("plan.json", gh.canonical(plan))
        first["_zip"] = buffer.getvalue()
        self.assertEqual(self.driver().plan()["action"].get("request"), other)

    def test_retry_rerun_cannot_override_newer_failure_or_disable_fresh_retry(self):
        number, _ = self.request({DEP: "v0.1.0"})
        failed = self.driver().plan()
        self.api.record(failed, success=False, execute="failure", write="skipped")
        self.run_id += 1
        retry = self.retry(number)
        later = self.driver().plan()
        self.api.record(later, success=False, execute="failure", write="skipped")
        self.run_id += 1
        self.api.record(dict(retry, attempt=2), execute="skipped", write="skipped")
        self.assertEqual(self.driver().plan()["action"]["kind"], "wait")
        self.retry(number)
        self.assertEqual(self.driver().plan()["action"]["kind"], "release")

    def test_request_candidate_rejects_midflight_module_source_not_unrelated_main(self):
        self.current(30)
        code, tx = self.cli("request-plan")
        self.assertEqual(code, 0, tx)
        self.fixture.write(DEP + "/new.go", "package ssefilter\nfunc NotInPreview() {}\n")
        self.fixture.commit()
        code, sealed = self.cli("request-validate", tx)
        self.assertEqual(code, 0, f"prevalidation must preserve the planned base, not chase main: {sealed}")
        self.assertEqual(self.cli("write", sealed)[0], 0)
        self.api.merge(max(self.api.prs))
        self.run_id += 1
        action = self.driver().plan()["action"]
        self.assertEqual(action["kind"], "fail")
        self.assertIn("changed after reviewed request base", action["reason"])
        self.assertEqual(git(self.fixture.root, "tag", "--list"), "")

    def test_deleted_whole_run_cannot_erase_failure_or_poison_later_reviewed_intent(self):
        self.fixture.add_module(SUPPORT, "package adaptersupport\n", role="support")
        self.fixture.commit()
        old, _ = self.request({DEP: "v0.1.0"})
        failure = self.driver().plan()
        self.api.record(failure, success=False, execute="failure", write="skipped")
        self.api.runs.pop(failure["run"])
        self.api.attempts.clear()
        self.api.artifacts.clear()
        self.run_id += 1
        self.current()
        count = len(self.api.writes)
        code, boundary = self.cli("plan")
        self.assertEqual(code, 0, boundary)
        self.assertEqual(boundary["action"]["kind"], "wait", "deleted failure cannot silently retry")
        self.assertEqual(len(self.api.writes), count)
        self.api.record(boundary, execute="skipped", write="skipped")
        self.run_id += 1
        newer, _ = self.request({SUPPORT: "v0.1.0"})
        intake_count = len(self.api.writes)
        self.current()
        code, plan = self.cli("plan")
        self.assertEqual(code, 0, plan)
        self.assertEqual((plan["action"]["kind"], plan["action"]["request"]), ("release", newer))
        self.assertNotEqual(plan["action"]["request"], old)
        self.assertEqual(len(self.api.writes), intake_count, "only the explicit new intake may write")

    def test_other_branch_run_numbers_count_without_release_authority(self):
        number, candidate = self.request({DEP: "v0.1.0"})
        branch = self.api.run(900, candidate)
        branch.update(head_branch="feature", _jobs=[])
        self.api.runs[900] = branch
        self.current()
        code, plan = self.cli("plan")
        self.assertEqual(code, 0, plan)
        self.assertEqual((plan["action"]["kind"], plan["action"]["request"]), ("release", number))

    def test_queued_retry_intent_does_not_authorize_a_later_failure(self):
        number, _ = self.request({DEP: "v0.1.0"})
        failed = self.driver().plan()
        self.api.record(failed, success=False, execute="failure", write="skipped")
        self.run_id += 1
        queued = self.driver().plan(number, HUMAN)
        intent = self.api.tick()
        self.run_id += 1
        newer_failure = dict(failed, run=self.run_id)
        self.api.record(newer_failure, success=False, execute="failure", write="skipped")
        self.api.record(queued, execute="skipped", write="skipped")
        self.api.attempts[(queued["run"], 1)]["created_at"] = intent
        self.api.runs[queued["run"]]["created_at"] = intent
        self.run_id += 1
        self.assertEqual(self.driver().plan()["action"]["kind"], "wait")
        # Attempt job timestamps, not a mutable run-wide updated_at, bound the
        # completed failure. A genuinely fresh human dispatch may now resume.
        attempt = self.api.attempts[(newer_failure["run"], 1)]
        for job in attempt["_jobs"]:
            job.update(started_at=attempt["run_started_at"], completed_at=attempt["updated_at"])
        attempt["updated_at"] = "2099-01-01T00:00:00Z"
        self.retry(number)
        self.assertEqual(self.driver().plan()["action"]["kind"], "release")

    def test_gate_defaults_off_and_missing_protection_checker_denies(self):
        with mock.patch.dict(gh.os.environ, {}, clear=True), self.assertRaisesRegex(gh.Error, "disabled"):
            gh.protection_check(self.api.repository, 70, 700)
        # Task 5 supplies this fixed interface; absence is intentionally closed.
        with mock.patch.dict(gh.os.environ, MODULE_RELEASES_ENABLED="true"), mock.patch.object(gh.subprocess, "run") as run:
            run.return_value = subprocess.CompletedProcess([], 0, gh.canonical(dict(schema=1, repository=self.api.repository, app_id=70, bot_id=700, protected=True)), "")
            with mock.patch.object(Path, "is_file", return_value=True):
                gh.protection_check(self.api.repository, 70, 700)
            self.assertEqual(run.call_args.args[0][2:4], ["check", "--repository"])


if __name__ == "__main__":
    # Exercise the actual planner/authority API with cheap histories, leaving
    # qualification, public proofs and generated tidy PRs to integration tests.
    quick = "--quick" in sys.argv
    if quick:
        sys.argv.remove("--quick")
    unittest.main(defaultTest=["GitHubTransitions." + name for name in (
        "test_request_authority_rejects_wrong_app_fork_bot_merge_unlinked_and_changed_bytes",
        "test_ci_identity_mismatches_expiry_and_safe_original_rerun",
        "test_no_silent_new_tip_and_immutable_tag_conflict",
        "test_closed_request_is_not_authority_and_exact_mapping_no_expansion",
        "test_cli_executes_discovery_with_injected_github_api",
        "test_whole_batch_shared_validation_precedes_even_independent_publication",
        "test_failed_transition_waits_for_explicit_human_retry_without_writes",
        "test_old_waiting_run_cannot_hide_newer_failure_or_override_retry_chronology",
        "test_expired_scoped_history_does_not_block_independent_or_superseding_request",
        "test_historical_workflow_revision_and_aged_successful_ci_remain_valid",
        "test_cli_rejects_stale_attempt_nonwriter_and_nondispatch_retry_without_writes",
        "test_three_intake_dispatches_survive_without_replaceable_pending_inputs",
        "test_request_candidate_rejects_midflight_module_source_not_unrelated_main",
        "test_retry_rerun_cannot_override_newer_failure_or_disable_fresh_retry",
        "test_deleted_whole_run_cannot_erase_failure_or_poison_later_reviewed_intent",
        "test_other_branch_run_numbers_count_without_release_authority",
        "test_queued_retry_intent_does_not_authorize_a_later_failure",
        "test_gate_defaults_off_and_missing_protection_checker_denies",
    )] if quick else None)
