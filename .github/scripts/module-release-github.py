#!/usr/bin/env python3
"""Trusted-main controller for coordinated, human-reviewed module batches.

Schema 2 deliberately supersedes the undeployed per-module schema 1. Fixed Git
refs discover lifecycle state; authenticated run/attempt artifacts prove execution.
Comments, labels, event payloads and candidate-written success flags confer nothing.
"""
import argparse
import base64
from contextlib import contextmanager
from datetime import datetime, timezone, timedelta
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
from urllib.error import HTTPError
from urllib.parse import quote, urlencode
from urllib.request import Request, build_opener, HTTPRedirectHandler, urlopen
import zipfile

HERE = Path(__file__).resolve().parent
sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location("module_release", HERE / "module-release.py")
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)
Error = release.CheckError
WORKFLOW = "module-releases.yml"
REQUEST_WORKFLOW = "request-module-release.yml"
PREFIX = ".github/module-releases/"
STATE = "module-batch-state/"
ACTIVE = STATE + "active"
SCHEMA = 2


def require(condition, message):
    if not condition:
        raise Error(message)


def sha(value):
    require(isinstance(value, str) and re.fullmatch(r"[0-9a-f]{40}", value), "expected full commit SHA")
    return value


def points_to(value, commit):
    return value and value["object"].get("type") == "commit" and value["object"].get("sha") == commit


def canonical(value):
    return json.dumps(value, sort_keys=True, indent=2) + "\n"


def digest(value):
    return hashlib.sha256(canonical(value).encode()).hexdigest()


def document(text):
    def unique(pairs):
        result = {}
        for key, value in pairs:
            require(key not in result, "duplicate JSON key")
            result[key] = value
        return result
    return json.loads(text, object_pairs_hook=unique)


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class GitHub:
    def __init__(self, repository, token):
        require(re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository), "invalid repository")
        self.repository, self.token = repository, token

    def call(self, method, path, data=None, missing=False):
        require(path.startswith("/") and not path.startswith("//"), "invalid API path")
        req = Request("https://api.github.com/repos/" + self.repository + path,
                      data=None if data is None else json.dumps(data).encode(), method=method,
                      headers={"Authorization": "Bearer " + self.token, "Accept": "application/vnd.github+json",
                               "X-GitHub-Api-Version": "2022-11-28", "Content-Type": "application/json"})
        try:
            with build_opener(NoRedirect).open(req, timeout=60) as response:
                body = response.read()
                return json.loads(body) if body else None
        except HTTPError as error:
            if error.code == 404 and missing:
                return None
            if method == "GET" and error.code == 302 and re.fullmatch(r"/actions/artifacts/[0-9]+/zip", path):
                location = error.headers["Location"]
                require(location.startswith("https://"), "insecure artifact URL")
                with urlopen(location, timeout=60) as response:
                    return response.read()
            raise Error(f"GitHub {method} {path}: HTTP {error.code}") from None

    def all(self, path, field=None):
        result, page = [], 1
        while True:
            values = self.call("GET", path + ("&" if "?" in path else "?") + urlencode(dict(per_page=100, page=page)))
            values = values[field] if field else values
            result.extend(values)
            if len(values) < 100:
                return result
            page += 1

    def file(self, path, ref, missing=False):
        value = self.call("GET", "/contents/" + quote(path, safe="/") + "?" + urlencode(dict(ref=ref)), missing=missing)
        if value is None and missing:
            return None
        require(value["type"] == "file" and value["encoding"] == "base64", "expected ordinary repository file")
        return base64.b64decode(value["content"]).decode()


class Driver:
    def __init__(self, api, root, app_id, bot_id, current_run=0, current_attempt=1, now=None):
        self.api, self.root = api, Path(root)
        self.app_id, self.bot_id = int(app_id), int(bot_id)
        require(self.app_id > 0 and self.bot_id > 0, "designated App and bot IDs are required")
        self.current_run, self.current_attempt = int(current_run), int(current_attempt)
        self.now = now or datetime.now(timezone.utc)

    def ref(self, name, missing=False):
        value = self.api.call("GET", "/git/ref/heads/" + quote(name, safe="/"), missing=missing)
        if value is None:
            return None
        require(value["object"]["type"] == "commit", "state ref is not a commit")
        return sha(value["object"]["sha"])

    def commit(self, value):
        return self.api.call("GET", "/git/commits/" + sha(value))

    def ancestor(self, older, newer=None):
        newer = newer or self.ref("main")
        result = self.api.call("GET", "/compare/" + sha(older) + "..." + sha(newer))
        require(result["status"] in ("ahead", "identical"), "required commit ancestry is absent")

    def is_descendant(self, older, newer):
        result = self.api.call("GET", "/compare/" + sha(older) + "..." + sha(newer))
        return result["status"] in ("ahead", "identical")

    def human(self, user):
        require(user and user.get("type") == "User" and type(user.get("id")) is int, "non-bot human authorization required")
        permission = self.api.call("GET", "/collaborators/" + quote(user["login"], safe="") + "/permission")["permission"]
        require(permission in ("admin", "maintain", "write"), "human must have repository write permission")

    def trusted_run(self, run, workflow=WORKFLOW):
        expected = self.api.call("GET", "/actions/workflows/" + workflow)
        require(run["workflow_id"] == expected["id"] and run["path"] == ".github/workflows/" + workflow,
                "wrong workflow identity")
        require(run["repository"]["full_name"] == self.api.repository
                and run["head_repository"]["full_name"] == self.api.repository
                and run["head_branch"] == "main" and run["event"] == "workflow_dispatch", "untrusted workflow run provenance")
        self.ancestor(sha(run["head_sha"]))
        require(run["run_attempt"] == 1, "old reruns cannot retry a batch")

    def current(self, workflow=WORKFLOW):
        run = self.api.call("GET", f"/actions/runs/{self.current_run}")
        self.trusted_run(run, workflow)
        require(run["id"] == self.current_run and run["run_attempt"] == self.current_attempt == 1, "stale run attempt")
        require(sha(run["head_sha"]) == release.git(self.root, "rev-parse", "HEAD"), "controller checkout is not fixed trusted T")
        return run

    def policy(self, trusted):
        return document(self.api.file(".github/scripts/module-publication-policy.json", sha(trusted)))

    def record(self, commit):
        value = document(self.api.file(PREFIX + "state.json", commit))
        require(value.get("schema") == SCHEMA, "obsolete release evidence schema; undeployed schema 1 is not accepted")
        return value

    def reservation(self, commit):
        record = self.record(commit)
        require(set(record) == {"schema", "kind", "id", "repository", "B", "T", "batch", "policy", "run", "attempt", "sequence"}
                and record["kind"] == "batch" and record["repository"] == self.api.repository, "invalid batch reservation")
        require(re.fullmatch(r"[1-9][0-9]*-1", record["id"]) and record["id"] == f"{record['run']}-1"
                and record["attempt"] == 1, "invalid intake identity")
        self.ancestor(sha(record["B"]))
        self.ancestor(sha(record["T"]))
        require(record["policy"] == digest(self.policy(record["T"])), "reservation policy mismatch")
        run = self.api.call("GET", f"/actions/runs/{record['run']}/attempts/1")
        self.trusted_run(run, REQUEST_WORKFLOW)
        require(run["head_sha"] == record["T"], "intake trusted T mismatch")
        self.human(run["actor"])
        self.human(run["triggering_actor"])
        policy = self.policy(record["T"])
        require(isinstance(record["batch"], dict) and record["batch"], "explicit nonempty mapping required")
        for path, version in record["batch"].items():
            require(path in policy["modules"] and policy["modules"][path]["role"] in release.PUBLISHED, "excluded module")
            release.validate_version(policy["namespace"] + "/" + path, version, release=True)
        require([p["sha"] for p in self.commit(commit)["parents"]] == [record["B"]], "reservation parent mismatch")
        return record

    def active(self):
        commit = self.ref(ACTIVE, missing=True)
        return (commit, self.reservation(commit)) if commit else (None, None)

    @staticmethod
    def branch(record):
        return "module-batch/" + record["id"]

    @staticmethod
    def state_ref(record, suffix):
        return STATE + record["id"] + "/" + suffix

    def pr(self, head, base):
        prs = self.api.all("/pulls?state=all&head=" + quote(self.api.repository.split("/")[0] + ":" + head, safe=""))
        require(len(prs) <= 1, "ambiguous fixed release PR")
        if not prs:
            return None
        pr = self.api.call("GET", f"/pulls/{prs[0]['number']}")
        require(pr["user"].get("id") == self.bot_id and pr["user"].get("type") == "Bot", "wrong App PR author")
        require(pr["head"]["repo"] and pr["head"]["repo"]["full_name"] == self.api.repository
                and pr["base"]["repo"]["full_name"] == self.api.repository
                and pr["head"]["ref"] == head and pr["base"]["ref"] == base, "wrong repository or PR identity")
        sha(pr["head"]["sha"])
        if pr["merged"]:
            require(pr["state"] == "closed", "invalid merge state")
            self.human(pr["merged_by"])
            linked = self.api.all(f"/commits/{sha(pr['merge_commit_sha'])}/pulls")
            require(any(p["number"] == pr["number"] for p in linked), "merge is not linked to PR")
        return pr

    def trees(self, commit):
        result = self.api.call("GET", "/git/trees/" + self.commit(commit)["tree"]["sha"] + "?recursive=1")
        require(not result["truncated"], "incomplete whole-tree evidence")
        return {e["path"]: (e["mode"], e["type"], e["sha"]) for e in result["tree"] if e["type"] != "tree"}

    def allowed(self, path, policy, metadata):
        # Default-deny control planes, including policy/fixture inputs. Source and
        # owning API/changelog updates remain reviewable; release identities do not.
        if path == metadata:
            return True
        if path in ("VERSION", "SDKVERSION", "AGENTS.md", "CLAUDE.md", "Taskfile.yml", "go.work", "go.work.sum"):
            return False
        if path.startswith((".github/", ".claude/", ".agents/", "scripts/")):
            return False
        if any(path == e.get("policy") for e in policy["modules"].values()):
            return False
        return (path.endswith((".go", "/go.mod", "/go.sum")) or path in ("go.mod", "go.sum")
                or any(path == e.get("changelog") or (e.get("api") and path.startswith(e["api"] + "/"))
                       for e in policy["modules"].values()))

    def candidate(self, record, live=True):
        branch = self.branch(record)
        pr = self.pr(branch + "-prep", branch)
        if not pr:
            if live:
                tip = self.ref(branch, missing=True)
                require(tip is None or tip == record["B"], "unexpected pre-PR1 batch-tip movement")
            return None, None
        if not pr["merged"]:
            require(pr["base"]["sha"] == record["B"], "PR1 base moved from B")
        head = pr["head"]["sha"]
        self.ancestor(record["B"], head)
        R = sha(pr["merge_commit_sha"]) if pr["merged"] else head
        if pr["merged"]:
            require([p["sha"] for p in self.commit(R)["parents"]] == [record["B"]], "R must have sole parent B (human squash)")
            require(self.commit(R)["tree"]["sha"] == self.commit(head)["tree"]["sha"], "R differs from whole reviewed preparation tree")
        bound = self.ref(self.state_ref(record, "candidate"), missing=True)
        require(bound is None or (pr["merged"] and bound == R), "permanent R binding changed")
        if live:
            require(self.ref(branch) == (R if pr["merged"] else record["B"]), "logical freeze: unexpected batch-tip movement")
        metadata = PREFIX + record["id"] + ".json"
        value = document(self.api.file(metadata, R))
        expected = dict(schema=SCHEMA, kind="preparation", reservation=digest(record), repository=self.api.repository,
                        B=record["B"], batch=record["batch"], policy=record["policy"], base=branch, head=branch + "-prep")
        require(set(value) == set(expected) | {"artifacts"} and all(value[k] == v for k, v in expected.items()), "changed mapping or preparation metadata")
        require(set(value["artifacts"]) == set(record["batch"]), "incomplete expected final artifacts")
        before, after = self.trees(record["B"]), self.trees(R)
        require(all(self.allowed(p, self.policy(record["T"]), metadata) for p in before.keys() | after.keys()
                    if before.get(p) != after.get(p)), "PR1 changes forbidden control-plane or release identity paths")
        return (dict(R=R, pr1=pr["number"], head=head, tree=self.commit(R)["tree"]["sha"], artifacts=value["artifacts"], merged_at=pr["merged_at"]) if pr["merged"] else None), pr

    def authority(self):
        """All protected publication grants, including abandoned off-main batches.

        State namespace rules protect controller-only immutable grant creation.
        A grant is created BEFORE its tag, never by candidate execution.
        """
        result = {}
        refs = self.api.all("/git/matching-refs/heads/" + STATE)
        reservations = {r["ref"].split("/")[-2]: r["object"]["sha"] for r in refs if r["ref"].endswith("/reservation")}
        for ref in refs:
            match = re.fullmatch(r"refs/heads/" + STATE + r"([1-9][0-9]*-1)/grant-([0-9a-f]{64})", ref["ref"])
            if not match:
                continue
            require(match[1] in reservations, "publication grant lost reservation provenance")
            record = self.reservation(reservations[match[1]])
            candidate, _ = self.candidate(record, live=False)
            require(candidate and ref["object"]["sha"] == candidate["R"]
                    and self.ref(self.state_ref(record, "candidate"), missing=True) == candidate["R"], "publication grant lost exact R provenance")
            pairs = [(p, v) for p, v in record["batch"].items() if digest([p, v]) == match[2]]
            require(len(pairs) == 1, "unknown granted version")
            path, version = pairs[0]
            previous = result.setdefault(path, {}).setdefault(version, candidate["R"])
            require(previous == candidate["R"], "conflicting public release authority")
        return result

    @contextmanager
    def check(self, commit, authority=None):
        with release.source_tree(self.root, sha(commit)) as source:
            yield release.Release(self.root, source, commit, authority=authority or self.authority())

    def binding(self, record, candidate, T):
        return dict(repository=self.api.repository, batch=digest(record), id=record["id"], B=record["B"], R=candidate["R"],
                    T=sha(T), pr1=candidate["pr1"], tree=candidate["tree"], mapping=record["batch"], policy=record["policy"])

    def artifact(self, run, name, filename):
        values = [a for a in self.api.all(f"/actions/runs/{run['id']}/artifacts", "artifacts")
                  if a["name"] == f"module-{name}-{run['id']}-1"]
        require(len(values) == 1 and not values[0]["expired"], "lost/expired receipt; fresh human retry requalifies and reproves")
        data = self.api.call("GET", f"/actions/artifacts/{values[0]['id']}/zip")
        with zipfile.ZipFile(io.BytesIO(data)) as archive:
            require(archive.namelist() == [filename] and archive.getinfo(filename).file_size < 4000000, "invalid receipt archive")
            return document(archive.read(filename))

    def jobs(self, run):
        jobs = self.api.all(f"/actions/runs/{run['id']}/attempts/1/jobs", "jobs")
        names = [j["name"] for j in jobs]
        require(len(names) == len(set(names)), "ambiguous receipt job identities")
        return {j["name"]: j["conclusion"] for j in jobs}

    def full_ci(self, run, binding, jobs):
        # The registry is read at authenticated historical T, never from R.
        config = self.api.file(".github/scripts/module-release-ci.json", binding["T"], missing=True)
        require(config is not None, "missing full release CI registry")
        config = document(config)
        require(set(config) == {"schema", "required_jobs"} and config["schema"] == SCHEMA
                and isinstance(config["required_jobs"], list) and config["required_jobs"]
                and all(isinstance(n, str) and n for n in config["required_jobs"]), "invalid full CI registry")
        receipt = self.artifact(run, "full-ci", "full-ci.json")
        require(receipt == dict(schema=SCHEMA, kind="full-ci", binding=binding, run=run["id"], attempt=1,
                                jobs=config["required_jobs"]), "full CI receipt binding mismatch")
        require(all(jobs.get(n) == "success" for n in config["required_jobs"]), "incomplete full release qualification")

    def history(self, record, candidate, T):
        # Evidence remains tied to its historical trusted T, not a moving main.
        require(record["policy"] == digest(self.policy(T)), "publication policy changed; explicit recovery required")
        records, stopped, epoch = [], None, ""
        self.history_error = None
        runs = self.api.all("/actions/workflows/" + WORKFLOW + "/runs", "workflow_runs")
        require(type(record["sequence"]) is int and record["sequence"] >= 0, "invalid reservation sequence")
        previous = record["sequence"]
        for summary in sorted([r for r in runs if r["run_number"] > previous], key=lambda r: r["run_number"]):
            if summary["run_number"] != previous + 1:
                stopped = max(stopped or "", summary["created_at"])
            previous = summary["run_number"]
            if summary["id"] == self.current_run:
                continue
            try:
                run = self.api.call("GET", f"/actions/runs/{summary['id']}/attempts/1")
                jobs = self.jobs(run)
            except (Error, KeyError, TypeError):
                stopped = max(stopped or "", summary.get("updated_at", summary["created_at"]))
                continue
            if run["head_branch"] != "main" or run["event"] != "workflow_dispatch":
                continue
            if not any(n.endswith(" " + record["id"]) for n in jobs):
                if run["conclusion"] != "success":
                    stopped = max(stopped or "", run.get("updated_at", run["created_at"]))
                continue
            try:
                self.trusted_run(run)
                require(summary["run_attempt"] == 1, "old workflow rerun cannot retry")
                plan = self.artifact(run, "plan", "plan.json")
                require(plan["schema"] == SCHEMA and plan["run"] == run["id"] and plan["attempt"] == 1
                        and plan["T"] == run["head_sha"], "invalid historical plan/T")
                action = plan["action"]
                require("binding" not in action or action["binding"]["T"] == run["head_sha"], "receipt controller T mismatch")
                require(action["id"] == record["id"], "historical batch mismatch")
                # A retry is a NEW dispatch and binds this same batch and R. Its
                # intent timestamp, not its eventual execution time, clears failure.
                if action["kind"] == "retry":
                    require(action["R"] == (candidate["R"] if candidate else None)
                            or (candidate and action["R"] is None and run["created_at"] < candidate["merged_at"]), "retry changes R")
                    self.human(run["actor"])
                    self.human(run["triggering_actor"])
                    require(run["status"] == "completed" and run["conclusion"] == "success", "retry did not complete")
                    if not stopped or run["created_at"] > stopped:
                        records, stopped, epoch = [], None, run["created_at"]
                        self.history_error = None
                    continue
                require(run["status"] == "completed" and run["conclusion"] == "success", "batch transition failed or incomplete")
                if action["kind"] not in ("qualify", "prove", "tag"):
                    continue
                require(action["binding"]["R"] == candidate["R"], "history changes permanent R")
                binding = self.binding(record, candidate, run["head_sha"])
                require(action["binding"] == binding, "historical batch binding mismatch")
                require(record["policy"] == digest(self.policy(run["head_sha"])), "historical policy identity mismatch")
                require(datetime.fromisoformat(run["updated_at"].replace("Z", "+00:00")) > self.now - timedelta(days=30), "receipt requires fresh qualification/proof")
                if action["kind"] == "tag":
                    require(jobs.get("Tag " + record["id"]) == "success", "protected tag writer did not succeed")
                if action["kind"] in ("qualify", "prove"):
                    require(jobs.get(("Qualify " if action["kind"] == "qualify" else "Proof ") + record["id"]) == "success", "execution job did not succeed")
                    result = self.artifact(run, "result", "result.json")
                    require(result["schema"] == SCHEMA and result["binding"] == binding and result["run"] == run["id"]
                            and result["attempt"] == 1, "result schema/binding mismatch")
                    if action["kind"] == "qualify":
                        self.full_ci(run, binding, jobs)
                        report = result["report"]
                        require(report["stage"] == "staged-qualified" and report["candidate"] == candidate["R"], "missing whole-batch staged qualification")
                        order = report["order"]
                        require(set(order) == set(record["batch"]) and len(order) == len(record["batch"])
                                and [(r["module"], r["version"]) for r in report["releases"]] == [(p, record["batch"][p]) for p in order], "qualification mapping/order mismatch")
                        expected = candidate["artifacts"]
                        artifacts = result["report"]["artifacts"]
                        require(set(artifacts) == set(expected) and all(
                            {k: v for k, v in artifacts[p].items() if k != "sha"} == expected[p]
                            and artifacts[p]["sha"] == candidate["R"] for p in expected), "final artifact mismatch")
                    else:
                        path = action["module"]
                        require(result["module"] == path and result["version"] == record["batch"][path], "public proof pair mismatch")
                        report = result["report"]
                        expected = candidate["artifacts"][path]
                        require(report["stage"] == "public-verified" and report["candidate"] == candidate["R"]
                                and report["module"] == path and report["version"] == record["batch"][path]
                                and all(report[k] == expected[k] for k in ("sum", "go_mod_sum")), "public proof checksum/source mismatch")
                    records.append((action, result))
            except (Error, OSError, ValueError, KeyError, TypeError, zipfile.BadZipFile) as error:
                stopped = max(stopped or "", run.get("updated_at", run["created_at"]))
                self.history_error = dict(run=run["id"], attempt=1, reason=str(error))
        return records, stopped, epoch

    def intake(self, batch, base, run):
        self.human(run["actor"])
        self.human(run["triggering_actor"])
        existing, active = self.active()
        if active:
            require(active["id"] == f"{self.current_run}-1", "one active batch: " + active["id"])
            return active
        self.ancestor(sha(base))
        require(isinstance(batch, dict) and batch, "explicit nonempty batch required")
        with self.check(base) as check:
            check.preview(batch)
        return dict(schema=SCHEMA, kind="batch", id=f"{self.current_run}-1", repository=self.api.repository,
                    B=base, T=run["head_sha"], batch=batch, policy=digest(self.policy(run["head_sha"])),
                    run=self.current_run, attempt=1, sequence=max([r["run_number"] for r in self.api.all(
                        "/actions/workflows/" + WORKFLOW + "/runs", "workflow_runs")] or [0]))

    def adoption_evidence(self, record, candidate, pr):
        """Authenticate premerge CI identities, never postmerge base.sha.

        The changes job uploads before executing candidate code. Its workflow
        must be byte-identical to trusted main, and the COMPLETE CI registry must
        succeed on that observed prospective merge. API-only for writer reuse.
        """
        A = sha(pr["merge_commit_sha"])
        parents = [p["sha"] for p in self.commit(A)["parents"]]
        require(len(parents) == 2, "PR2 wrong merge method: ancestry repair required")
        expected_workflow = self.api.call("GET", "/actions/workflows/ci.yml")
        for run in self.api.all("/actions/workflows/ci.yml/runs", "workflow_runs"):
            if not (run["workflow_id"] == expected_workflow["id"] and run["path"] == ".github/workflows/ci.yml"
                    and run["event"] == "pull_request" and run["run_attempt"] == 1
                    and run["repository"]["full_name"] == self.api.repository
                    and run["head_repository"]["full_name"] == self.api.repository
                    and run["head_sha"] == pr["head"]["sha"]
                    and run["status"] == "completed" and run["conclusion"] == "success"):
                continue
            try:
                receipt = self.artifact(run, "adoption", "module-adoption.json")
                T = sha(receipt["T"])
                self.ancestor(T)
                require(receipt == dict(schema=SCHEMA, kind="adoption", run=run["id"], attempt=1, T=T,
                                        pr=pr["number"], head=parents[1], base=parents[0], merge=receipt["merge"],
                                        binding=self.binding(record, candidate, T)), "premerge adoption binding mismatch")
                require(receipt["head"] == pr["head"]["sha"], "adoption head changed")
                merge = self.commit(sha(receipt["merge"]))
                require([p["sha"] for p in merge["parents"]] == parents
                        and merge["tree"]["sha"] == self.commit(A)["tree"]["sha"], "adoption differs from tested merge")
                require(self.api.file(".github/workflows/ci.yml", receipt["merge"]) == self.api.file(".github/workflows/ci.yml", T),
                        "adoption CI workflow differs from trusted main")
                registry = document(self.api.file(".github/scripts/module-release-ci.json", T))
                require(registry["schema"] == SCHEMA and registry["required_jobs"], "missing adoption CI registry")
                jobs = self.jobs(run)
                require(all(jobs.get(name.removeprefix("Full release CI / ")) == "success" for name in registry["required_jobs"]),
                        "incomplete adoption CI")
                self.ancestor(candidate["R"], parents[1])
                self.ancestor(A)
                return receipt
            except (Error, KeyError, TypeError, ValueError, zipfile.BadZipFile):
                continue
        raise Error("PR2 lacks authenticated premerge head/base/CI merge evidence: ancestry repair required")

    def plan(self, run, operation="reconcile", requested="", integration=""):
        require(self.current_attempt == 1, "old reruns cannot retry")
        reservation, record = self.active()
        if not record:
            return dict(kind="wait", id="none", reason="no active batch")
        terminal_sha = self.ref(self.state_ref(record, "terminal"), missing=True)
        if terminal_sha:
            terminal = self.record(terminal_sha)
            require(terminal["kind"] in ("complete", "abandon") and terminal["batch"] == digest(record), "invalid terminal authority")
            original = self.api.call("GET", f"/actions/runs/{terminal['run']}/attempts/1")
            self.trusted_run(original)
            if terminal["kind"] == "abandon":
                self.human(original["actor"])
                self.human(original["triggering_actor"])
            return dict(kind="finalize", id=record["id"], reservation=reservation, terminal=terminal_sha)
        require(record["policy"] == digest(self.policy(run["head_sha"])), "publication policy changed; explicit recovery required")
        candidate, pr1 = self.candidate(record)
        if operation == "abandon":
            self.human(run["actor"])
            self.human(run["triggering_actor"])
            require(requested == record["id"], "abandon must name active batch")
            return dict(kind="abandon", id=record["id"], reservation=reservation, R=candidate["R"] if candidate else None)
        if operation == "retry":
            self.human(run["actor"])
            self.human(run["triggering_actor"])
            require(requested == record["id"], "retry must explicitly name same batch")
            return dict(kind="retry", id=record["id"], R=candidate["R"] if candidate else None)
        if not candidate:
            _, stopped, _ = self.history(record, None, run["head_sha"])
            if stopped:
                return dict(kind="wait", id=record["id"], reason="preparation stopped; fresh explicit human retry required", stopped=stopped, failure=self.history_error)
            if pr1:
                return dict(kind="wait", id=record["id"], reason="PR1 awaits human squash" if pr1["state"] == "open" else "PR1 closed: paused")
            return dict(kind="prepare", id=record["id"], reservation=reservation)
        binding = self.binding(record, candidate, run["head_sha"])
        if self.ref(self.state_ref(record, "candidate"), missing=True) is None:
            return dict(kind="bind", id=record["id"], reservation=reservation, binding=binding)
        records, stopped, epoch = self.history(record, candidate, run["head_sha"])
        if stopped:
            return dict(kind="wait", id=record["id"], reason="batch stopped; fresh explicit human retry required", stopped=stopped, failure=self.history_error)
        common = dict(id=record["id"], reservation=reservation, binding=binding, epoch=epoch)
        qualified = [result for action, result in records if action["kind"] == "qualify"]
        if not qualified:
            return dict(common, kind="qualify")
        authority = self.authority()
        for path, version in record["batch"].items():
            authority.setdefault(path, {})[version] = candidate["R"]
        with self.check(candidate["R"], authority) as check:
            order = check.order(set(record["batch"]))
            for path in order:
                version = record["batch"][path]
                tag = check.modules[path]["tag_prefix"] + version
                existing = self.api.call("GET", "/git/ref/tags/" + quote(tag, safe="/"), missing=True)
                if existing:
                    require(points_to(existing, candidate["R"]), "immutable tag conflict")
                    require(self.ref(self.state_ref(record, "grant-" + digest([path, version])), missing=True) == candidate["R"], "existing tag lacks durable publication grant")
                    proofs = [result for action, result in records if action["kind"] == "prove" and action["module"] == path]
                    if proofs:
                        continue
                    return dict(common, kind="prove", module=path, version=version, expected=qualified[-1]["report"]["artifacts"][path])
                return dict(common, kind="tag", module=path, version=version, tag=tag)
        head = integration or self.branch(record)
        if not integration:
            # Re-discover exceptional PR2 after a lost dispatch/crash. A branch
            # name only narrows the scan; pr() and R ancestry establish identity.
            alternatives = [p["head"]["ref"] for p in self.api.all("/pulls?state=all&base=main")
                            if p["head"]["ref"].startswith("module-adoption/")
                            and p["user"].get("id") == self.bot_id
                            and self.is_descendant(candidate["R"], p["head"]["sha"])]
            require(len(set(alternatives)) <= 1, "ambiguous adoption integration PR")
            if alternatives:
                head = alternatives[0]
        if head != self.branch(record):
            require(re.fullmatch(r"module-adoption/[A-Za-z0-9_-]+", head), "invalid exceptional integration branch")
            self.ancestor(candidate["R"], self.ref(head))
        pr2 = self.pr(head, "main")
        if not pr2:
            return dict(common, kind="adopt", head=head, head_sha=self.ref(head))
        if not pr2["merged"]:
            return dict(kind="wait", id=record["id"], reason="PR2 awaits human MERGE COMMIT" if pr2["state"] == "open" else "PR2 closed: paused")
        self.adoption_evidence(record, candidate, pr2)
        return dict(common, kind="complete", pr2=pr2["number"], A=sha(pr2["merge_commit_sha"]))

    def envelope(self, action, run):
        return dict(schema=SCHEMA, run=self.current_run, attempt=1, T=run["head_sha"], action=action)

    def verify_plan(self, plan, run):
        require(plan["schema"] == SCHEMA and plan["run"] == self.current_run and plan["attempt"] == 1
                and plan["T"] == run["head_sha"], "plan run/T/attempt mismatch")
        action = plan["action"]
        operation = action["kind"] if action["kind"] in ("retry", "abandon") else "reconcile"
        require(self.plan(run, operation, action["id"], action.get("head", "") if action["kind"] == "adopt" and action["head"].startswith("module-adoption/") else "") == action,
                "batch authority changed since planning")
        return action

    def helper(self, temporary, env):
        helper = Path(temporary) / "module-artifact"
        # root is checked against run.head_sha in current(); never build at R.
        release.run(self.root, "go", "build", "-o", str(helper), str(HERE / "module-artifact.go"), env=env)
        return helper

    def execute(self, action, run):
        _, record = self.active()
        require(record and record["id"] == action["id"], "lost active reservation")
        candidate, _ = self.candidate(record)
        authority = self.authority()
        if candidate:
            for path, version in record["batch"].items():
                authority.setdefault(path, {})[version] = candidate["R"]
        with tempfile.TemporaryDirectory(prefix="module-batch-execute-") as temporary:
            temporary = Path(temporary)
            env = release.public_environment(temporary)
            kind = action["kind"]
            if kind == "prepare":
                with self.check(record["B"], authority) as check:
                    tree = temporary / "prepared"
                    report = check.prepare_batch(record["batch"], self.helper(temporary, env), tree, env)
                    files = {}
                    # Algorithm preparation only changes manifests. API writer
                    # accepts these data files, never a candidate Git bundle.
                    for path in check.modules:
                        for name in ("go.mod", "go.sum"):
                            relative = name if path == "." else path + "/" + name
                            before, after = check.source / relative, tree / relative
                            if after.exists() and (not before.exists() or after.read_bytes() != before.read_bytes()):
                                files[relative] = after.read_text()
                    metadata = dict(schema=SCHEMA, kind="preparation", reservation=digest(record), repository=self.api.repository,
                                    B=record["B"], batch=record["batch"], policy=record["policy"], base=self.branch(record),
                                    head=self.branch(record) + "-prep", artifacts=report["artifacts"])
                    files[PREFIX + record["id"] + ".json"] = canonical(metadata)
                    return dict(schema=SCHEMA, run=self.current_run, attempt=1, kind="prepared", reservation=digest(record), files=files)
            require(kind in ("qualify", "prove") and candidate, "not a credential-free execution stage")
            with self.check(candidate["R"], authority) as check:
                if kind == "qualify":
                    report = check.qualify_staged(record["batch"], self.helper(temporary, env), temporary / "proxy", env, expected=candidate["artifacts"])
                    return dict(schema=SCHEMA, binding=action["binding"], run=self.current_run, attempt=1, report=report)
                report = None
                for _ in range(3):
                    try:
                        with tempfile.TemporaryDirectory(prefix="module-batch-public-") as fresh:
                            fresh = Path(fresh)
                            report = check.prove(action["module"], action["version"], release.public_environment(fresh), fresh, expected=action["expected"])
                        break
                    except Error:
                        if _ == 2:
                            raise
                return dict(schema=SCHEMA, binding=action["binding"], run=self.current_run, attempt=1,
                            module=action["module"], version=action["version"], report=report)

    def validate_write(self, plan, result, run):
        action = self.verify_plan(plan, run)
        _, record = self.active()
        if action["kind"] == "prepare":
            require(result["schema"] == SCHEMA and result["run"] == self.current_run and result["attempt"] == 1
                    and result["kind"] == "prepared" and result["reservation"] == digest(record), "preparation receipt mismatch")
            require(self.jobs(run).get("Prepare " + record["id"]) == "success", "preparation did not succeed")
            policy = self.policy(run["head_sha"])
            allowed = {("" if p == "." else p + "/") + name for p in policy["modules"] for name in ("go.mod", "go.sum")}
            metadata = PREFIX + record["id"] + ".json"
            require(isinstance(result["files"], dict) and set(result["files"]) <= allowed | {metadata}
                    and metadata in result["files"] and len(canonical(result)) < 4000000, "unrelated preparation files")
            value = document(result["files"][metadata])
            expected = dict(schema=SCHEMA, kind="preparation", reservation=digest(record), repository=self.api.repository,
                            B=record["B"], batch=record["batch"], policy=record["policy"], base=self.branch(record), head=self.branch(record) + "-prep")
            require(set(value) == set(expected) | {"artifacts"} and all(value[k] == v for k, v in expected.items())
                    and set(value["artifacts"]) == set(record["batch"]), "invalid preparation metadata")
            action = dict(action, files=result["files"])
        require(action["kind"] in ("prepare", "bind", "tag", "adopt", "complete", "abandon", "finalize"), "not a protected write stage")
        return dict(schema=SCHEMA, run=self.current_run, T=run["head_sha"], action=action)

    def classify(self, number, run):
        """Read-only task-5 CI seam: exact PR identities, never prefix/label authority."""
        _, record = self.active()
        require(record, "not an active release PR")
        candidate, pr1 = self.candidate(record)
        if pr1 and pr1["number"] == number:
            require(pr1["state"] == "open" and not pr1["merged"], "PR1 not open")
            return dict(schema=SCHEMA, kind="preparation", reservation=record, pr=number, head=pr1["head"]["sha"], T=run["head_sha"])
        require(candidate, "PR2 lacks exact R")
        records, stopped, _ = self.history(record, candidate, run["head_sha"])
        require(not stopped and {a["module"] for a, _ in records if a["kind"] == "prove"} == set(record["batch"]), "PR2 lacks complete public proof authority")
        pr = self.api.call("GET", f"/pulls/{number}")
        head = pr["head"]["ref"]
        require(head == self.branch(record) or re.fullmatch(r"module-adoption/[A-Za-z0-9_-]+", head), "unrecognized adoption head")
        require(self.pr(head, "main")["number"] == number and pr["state"] == "open", "not exact open adoption PR")
        self.ancestor(candidate["R"], pr["head"]["sha"])
        return dict(schema=SCHEMA, kind="adoption", binding=self.binding(record, candidate, run["head_sha"]),
                    pr=number, head=pr["head"]["sha"], base=pr["base"]["sha"])


def protection_check(repository, app_id, bot_id):
    require(os.environ.get("MODULE_RELEASES_ENABLED") == "true", "module releases are disabled")
    env = os.environ.copy()
    for name in ("RELEASE_APP_PRIVATE_KEY", "RELEASE_APP_JWT"):
        os.environ.pop(name, None)
    result = subprocess.run([sys.executable, str(HERE / "module-release-protection.py"), "check", "--repository", repository,
                             "--app-id", str(app_id), "--bot-id", str(bot_id)], capture_output=True, text=True, env=env)
    require(result.returncode == 0, "release protection check failed")
    evidence = document(result.stdout)
    # Schema 2 includes static batch/state namespaces and merge-commit support,
    # as well as the existing App/environment/tag restrictions.
    require(evidence == dict(schema=SCHEMA, repository=repository, app_id=int(app_id), bot_id=int(bot_id), protected=True),
            "coordinated batch protection contract mismatch")


class Writer:
    """API-only; no Git/Go/subprocess/candidate execution with App authority."""
    def __init__(self, driver, write_api=None):
        self.d = driver
        self.api = write_api or driver.api

    def create_ref(self, name, commit, guard):
        guard()
        existing = self.d.ref(name, missing=True)
        if existing:
            require(existing == commit, "immutable state/branch conflict")
        else:
            guard()
            self.api.call("POST", "/git/refs", dict(ref="refs/heads/" + name, sha=sha(commit)))

    def data_commit(self, base, files, title, guard):
        entries = []
        for path, content in sorted(files.items()):
            require(not path.startswith("/") and all(p not in ("", ".", "..") for p in path.split("/")), "unsafe data path")
            guard()
            blob = self.api.call("POST", "/git/blobs", dict(content=content, encoding="utf-8"))
            entries.append(dict(path=path, mode="100644", type="blob", sha=blob["sha"]))
        guard()
        tree = self.api.call("POST", "/git/trees", dict(base_tree=self.d.commit(base)["tree"]["sha"], tree=entries))["sha"]
        guard()
        return self.api.call("POST", "/git/commits", dict(message=title, tree=tree, parents=[base]))["sha"]

    def context(self, trusted, workflow=WORKFLOW):
        run = self.api.call("GET", f"/actions/runs/{self.d.current_run}")
        self.d.trusted_run(run, workflow)
        require(run["id"] == self.d.current_run and run["head_sha"] == sha(trusted)
                and self.d.current_attempt == 1, "writer run/T mismatch")
        return run

    def reserve(self, record):
        run = self.context(record["T"], REQUEST_WORKFLOW)
        require(record["run"] == self.d.current_run, "reservation run mismatch")
        self.d.human(run["actor"])
        self.d.human(run["triggering_actor"])
        def guard():
            require(self.d.ref(self.d.state_ref(record, "terminal"), missing=True) is None, "batch is already terminal")
            active = self.d.ref(ACTIVE, missing=True)
            require(active is None or self.d.reservation(active) == record, "one active batch already reserved")
        guard()
        commit = self.d.ref(ACTIVE, missing=True)
        if commit is None:
            commit = self.d.ref(self.d.state_ref(record, "reservation"), missing=True)
            if commit:
                require(self.d.reservation(commit) == record, "reservation identity conflict")
            else:
                commit = self.data_commit(record["B"], {PREFIX + "state.json": canonical(record)}, "Reserve module batch " + record["id"], guard)
            # Atomic create-only ref is the lifecycle mutex. NOTHING else visible
            # (archive, candidate branches or PR) precedes its acquisition.
            self.create_ref(ACTIVE, commit, guard)
        self.create_ref(self.d.state_ref(record, "reservation"), commit, guard)

    def apply(self, transaction):
        require(transaction["schema"] == SCHEMA and transaction["run"] == self.d.current_run, "writer transaction schema/run mismatch")
        run = self.context(transaction["T"])
        action = transaction["action"]
        if action["kind"] == "abandon":
            self.d.human(run["actor"])
            self.d.human(run["triggering_actor"])
        reservation, record = self.d.active()
        if record is None and action["kind"] in ("complete", "abandon", "finalize"):
            terminal_sha = self.d.ref(STATE + action["id"] + "/terminal", missing=True)
            require(terminal_sha is not None, "lost terminal authority")
            terminal = self.d.record(terminal_sha)
            require(terminal["run"] == transaction["run"] and terminal["T"] == transaction["T"], "terminal retry mismatch")
            return
        require(record and record["id"] == action["id"], "no matching active reservation")
        def guard():
            require(self.d.ref(ACTIVE) == reservation, "active batch changed")
            current, _ = self.d.candidate(record)  # logical exact-R freeze
            if action["kind"] in ("tag", "adopt", "complete"):
                require(current and action["binding"] == self.d.binding(record, current, transaction["T"]), "write binding changed")
                records, stopped, _ = self.d.history(record, current, transaction["T"])
                qualified = [r for a, r in records if a["kind"] == "qualify"]
                require(not stopped and qualified, "write lost full qualification authority")
                order = qualified[-1]["report"]["order"]
                require(set(order) == set(record["batch"]) and len(order) == len(record["batch"]), "qualification order mismatch")
                needed = order[:order.index(action["module"])] if action["kind"] == "tag" else order
                proven = {a["module"] for a, _ in records if a["kind"] == "prove"}
                require(set(needed) <= proven, "write lacks prerequisite public proof")
                policy = self.d.policy(transaction["T"])
                for path in needed:
                    tag = policy["modules"][path]["tag_prefix"] + record["batch"][path]
                    require(points_to(self.api.call("GET", "/git/ref/tags/" + quote(tag, safe="/"), missing=True), current["R"]), "public proof lost immutable tag")
                if action["kind"] == "complete":
                    pr = self.api.call("GET", f"/pulls/{action['pr2']}")
                    validated = self.d.pr(pr["head"]["ref"], "main")
                    require(validated["number"] == action["pr2"] and validated["merged"]
                            and validated["merge_commit_sha"] == action["A"], "adoption merge identity changed")
                    self.d.adoption_evidence(record, current, validated)
                if action["kind"] == "tag":
                    require(action["version"] == record["batch"][action["module"]]
                            and action["tag"] == policy["modules"][action["module"]]["tag_prefix"] + action["version"], "expanded tag transaction")
        guard()
        self.create_ref(self.d.state_ref(record, "reservation"), reservation, guard)
        kind, branch = action["kind"], self.d.branch(record)
        if kind == "finalize":
            require(self.d.ref(self.d.state_ref(record, "terminal")) == action["terminal"], "terminal authority changed")
            guard()
            self.api.call("DELETE", "/git/refs/heads/" + ACTIVE)
            return
        if kind == "prepare":
            self.create_ref(branch, record["B"], guard)
            head = branch + "-prep"
            existing = self.d.ref(head, missing=True)
            if existing is None:
                commit = self.data_commit(record["B"], action["files"], "Prepare complete module batch " + record["id"], guard)
                self.create_ref(head, commit, guard)
            else:
                # A crash after creating the immutable preparation branch resumes
                # only its original data, not a new tree from a later execution.
                for path, content in action["files"].items():
                    require(self.api.file(path, existing) == content, "preparation changed after interrupted creation")
            before, after = self.d.trees(record["B"]), self.d.trees(self.d.ref(head))
            metadata = PREFIX + record["id"] + ".json"
            require(all(self.d.allowed(p, self.d.policy(record["T"]), metadata) for p in before.keys() | after.keys()
                        if before.get(p) != after.get(p)), "preparation branch changes forbidden control-plane paths")
            self.open_pr(head, branch, "Prepare module batch " + record["id"],
                         "Review the COMPLETE exact mapping and diff. Human SQUASH only into fixed B.\n\n" + canonical(record["batch"]), guard)
            return
        candidate, _ = self.d.candidate(record)
        if candidate:
            self.create_ref(self.d.state_ref(record, "candidate"), candidate["R"], guard)
        if kind == "bind":
            return
        if kind == "tag":
            require(candidate and action["binding"] == self.d.binding(record, candidate, transaction["T"]), "tag identity mismatch")
            self.create_ref(self.d.state_ref(record, "grant-" + digest([action["module"], action["version"]])), candidate["R"], guard)
            path = "/git/ref/tags/" + quote(action["tag"], safe="/")
            existing = self.api.call("GET", path, missing=True)
            if existing:
                require(points_to(existing, candidate["R"]), "immutable tag conflict")
            else:
                guard()
                self.api.call("POST", "/git/refs", dict(ref="refs/tags/" + action["tag"], sha=candidate["R"]))
            return
        if kind == "adopt":
            require(self.d.ref(action["head"]) == action["head_sha"], "adoption head moved")
            self.d.ancestor(candidate["R"], action["head_sha"])
            self.open_pr(action["head"], "main", "Adopt published module batch " + record["id"],
                         "HUMAN MERGE COMMIT ONLY. Do not squash or rebase. Preserve R=" + candidate["R"]
                         + ". Normal CI must validate the prospective main merge. After verified adoption, separately authorize temporary-branch cleanup; retain tags and module-batch-state refs.\n\n" + canonical(record["batch"]), guard)
            return
        require(kind in ("complete", "abandon"), "unknown write stage")
        # Explicit permanent terminal receipt records all extant public versions;
        # archive/grants remain discoverable after the lifecycle mutex is released.
        published = {}
        policy = self.d.policy(record["T"])
        for path, version in record["batch"].items():
            tag = policy["modules"][path]["tag_prefix"] + version
            value = self.api.call("GET", "/git/ref/tags/" + quote(tag, safe="/"), missing=True)
            if value:
                require(candidate and points_to(value, candidate["R"]), "terminal tag conflict")
                published[path] = dict(version=version, sha=candidate["R"])
        terminal = dict(schema=SCHEMA, kind=kind, batch=digest(record), R=candidate["R"] if candidate else None,
                        pr1=candidate["pr1"] if candidate else None, pr2=action.get("pr2"),
                        run=transaction["run"], attempt=1, T=transaction["T"], published=published, adoption=action.get("A"),
                        adoption_ci=self.d.adoption_evidence(record, candidate, self.api.call("GET", f"/pulls/{action['pr2']}"))
                        if kind == "complete" else None)
        name = self.d.state_ref(record, "terminal")
        commit = self.d.ref(name, missing=True)
        if commit:
            require(self.d.record(commit) == terminal, "terminal recovery conflict")
        else:
            commit = self.data_commit(reservation, {PREFIX + "state.json": canonical(terminal)}, "Finish module batch", guard)
            self.create_ref(name, commit, guard)
        guard()
        self.api.call("DELETE", "/git/refs/heads/" + ACTIVE)

    def open_pr(self, head, base, title, body, guard):
        existing = self.d.pr(head, base)
        if existing:
            return  # Closed unmerged PRs pause; never silently recreate them.
        guard()
        self.api.call("POST", "/pulls", dict(title=title, body=body, head=head, base=base))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("authority", "request-plan", "request-validate", "reserve", "plan", "execute", "validate-write", "write", "continue", "classify"))
    parser.add_argument("--root", type=Path, default=Path.cwd())
    parser.add_argument("--input", type=Path)
    parser.add_argument("--result", type=Path)
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    try:
        if args.command == "authority":
            # The local caller extracts this reader from fetched main. Identity
            # comes from authenticated repository configuration, not local flags.
            namespace = release.load_json(HERE / "module-publication-policy.json")["namespace"]
            require(namespace.startswith("github.com/"), "not a GitHub publication policy")
            api = GitHub(namespace.removeprefix("github.com/"), os.environ.get("GH_TOKEN", ""))
            app_id = int(api.call("GET", "/actions/variables/RELEASE_APP_ID")["value"])
            bot_id = int(api.call("GET", "/actions/variables/RELEASE_APP_BOT_ID")["value"])
            driver = Driver(api, args.root, app_id, bot_id, 0, 1)
            driver.ancestor(release.git(args.root, "rev-parse", "refs/remotes/origin/main"))
            print(canonical(driver.authority()), end="")
            return 0
        require(os.environ.get("MODULE_RELEASES_ENABLED") == "true", "module releases are disabled")
        require(os.environ.get("GITHUB_REF") == "refs/heads/main", "controller must run from main")
        repository = os.environ["GITHUB_REPOSITORY"]
        app_id, bot_id = int(os.environ["RELEASE_APP_ID"]), int(os.environ["RELEASE_APP_BOT_ID"])
        api = GitHub(repository, os.environ.get("GH_TOKEN", ""))
        driver = Driver(api, args.root, app_id, bot_id, os.environ["GITHUB_RUN_ID"], os.environ["GITHUB_RUN_ATTEMPT"])
        value = document(args.input.read_text()) if args.input else None
        # API-only writers run AFTER minting; current()/git must not execute here.
        if args.command in ("write", "reserve"):
            writer = Writer(driver, GitHub(repository, os.environ["MODULE_RELEASE_WRITE_TOKEN"]))
            if args.command == "reserve":
                writer.reserve(value)
            else:
                writer.apply(value)
            value = {}
        else:
            request = args.command in ("request-plan", "request-validate")
            run = driver.current(REQUEST_WORKFLOW if request else WORKFLOW)
            if request:
                fresh = driver.intake(document(os.environ["MODULE_BATCH"]), os.environ["MODULE_BASE"], run)
                if args.command == "request-validate":
                    require(value == fresh, "intake mapping/base changed")
                    protection_check(repository, app_id, bot_id)
                value = fresh
            elif args.command == "plan":
                value = driver.envelope(driver.plan(run, os.environ.get("BATCH_OPERATION", "reconcile"), os.environ.get("BATCH_ID", ""), os.environ.get("ADOPTION_HEAD", "")), run)
            elif args.command == "classify":
                value = driver.classify(int(os.environ["RELEASE_PR"]), run)
            elif args.command == "execute":
                value = driver.execute(driver.verify_plan(value, run), run)
            elif args.command == "validate-write":
                value = driver.validate_write(value, document(args.result.read_text()) if args.result else {}, run)
                protection_check(repository, app_id, bot_id)
            else:
                require(value["schema"] == SCHEMA and value["run"] == driver.current_run and value["attempt"] == 1
                        and value["T"] == run["head_sha"], "continuation provenance mismatch")
                require(value["action"]["kind"] not in ("wait", "abandon", "complete"), "no continuation")
                api.call("POST", "/actions/workflows/" + WORKFLOW + "/dispatches", dict(ref="main"))
                value = {}
        if args.output:
            args.output.write_text(canonical(value))
        else:
            print(canonical(value), end="")
        return 0
    except (Error, OSError, ValueError, KeyError, TypeError, zipfile.BadZipFile) as error:
        print(f"module-release-github: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
