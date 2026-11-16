#!/usr/bin/env python3
"""GitHub transport for reviewed module releases (stdlib only).

The release algorithms remain in module-release.py. This driver never merges PRs,
updates tags, or chooses versions. Actions run one durable transition at a time;
every plan scans all requests, not just the event that happened to survive the
Actions concurrency queue. Plan artifacts plus exact trusted run/attempt/job
conclusions are the journal; PR comments and labels are never read.
"""
import argparse
import base64
from contextlib import contextmanager
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
from datetime import datetime, timezone, timedelta

HERE = Path(__file__).resolve().parent
sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location("module_release", HERE / "module-release.py")
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)
Error = release.CheckError
WORKFLOW = "module-releases.yml"
REQUEST_WORKFLOW = "request-module-release.yml"
PREFIX = ".github/module-releases/"
CI_GROUP = "ci-${{ github.workflow }}-${{ github.event_name == 'push' && github.ref == 'refs/heads/main' && github.run_attempt > 1 && format('rerun-{0}', github.run_id) || github.ref }}"


def require(condition, message):
    if not condition:
        raise Error(message)


def sha(value):
    require(isinstance(value, str) and re.fullmatch(r"[0-9a-f]{40}", value), "expected full commit SHA")
    return value


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
            # Artifact redirects carry signed URLs. Never forward an API token.
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
        return base64.b64decode(value["content"], validate=False).decode()


class Driver:
    def __init__(self, api, root, app_id, bot_id, current_run=0, current_attempt=1, now=None):
        self.api, self.root = api, Path(root)
        self.app_id, self.bot_id = int(app_id), int(bot_id)
        require(self.app_id > 0 and self.bot_id > 0, "designated App and bot IDs are required")
        self.current_run, self.current_attempt = int(current_run), int(current_attempt)
        self.now = now or datetime.now(timezone.utc)

    def ancestor(self, commit):
        sha(commit)
        release.git(self.root, "merge-base", "--is-ancestor", commit, "refs/remotes/origin/main")

    @contextmanager
    def check(self, commit):
        self.ancestor(commit)
        with release.source_tree(self.root, commit) as source:
            yield release.Release(self.root, source, commit)

    def human(self, user):
        require(user and user.get("type") == "User" and isinstance(user.get("id"), int), "non-bot human authorization required")
        permission = self.api.call("GET", "/collaborators/" + quote(user["login"], safe="") + "/permission")["permission"]
        require(permission in ("admin", "maintain", "write"), "human must have repository write permission")

    def files(self, pr):
        files = self.api.all(f"/pulls/{pr['number']}/files")
        require(len(files) == pr["changed_files"], "incomplete PR file evidence")
        return files

    def authority(self, pr, merged=True):
        require(pr["user"].get("id") == self.bot_id and pr["user"].get("type") == "Bot", "PR is not authored by designated App")
        require(pr["head"]["repo"] and pr["head"]["repo"]["full_name"] == self.api.repository
                and pr["base"]["repo"]["full_name"] == self.api.repository and pr["base"]["ref"] == "main", "fork or wrong PR base")
        sha(pr["head"]["sha"])
        if merged:
            require(pr["merged"] and pr["state"] == "closed", "PR must be merged")
            self.human(pr["merged_by"])
            self.ancestor(pr["merge_commit_sha"])
            linked = self.api.all(f"/commits/{pr['merge_commit_sha']}/pulls")
            require(any(p["number"] == pr["number"] for p in linked), "merge commit is not linked to PR")

    def reviewed_document(self, pr, path, merged=True):
        self.authority(pr, merged)
        head = document(self.api.file(path, pr["head"]["sha"]))
        if merged:
            require(document(self.api.file(path, pr["merge_commit_sha"])) == head, "merged document differs from reviewed PR")
        return head

    def requests(self, prs):
        result, seen = [], set()
        for pr in prs:
            files = self.files(pr)
            paths = [f["filename"] for f in files if f["filename"].startswith(PREFIX + "request-")]
            if not paths or not pr["merged"]:
                continue
            require(len(paths) == len(files) == 1 and files[0]["status"] == "added", "request PR may only add one request")
            request = self.reviewed_document(pr, paths[0])
            require(document(self.api.file(paths[0], release.git(self.root, "rev-parse", "refs/remotes/origin/main"))) == request, "durable request was changed after review")
            seen.add(paths[0])
            require(set(request) == {"schema", "kind", "batch", "nonce", "supersedes", "base"} and request["schema"] == 1 and request["kind"] == "request", "invalid request schema")
            require(re.fullmatch(r"[0-9]+-[0-9]+", request["nonce"]), "invalid request nonce")
            require(isinstance(request["supersedes"], list) and all(type(n) is int and 0 < n < pr["number"] for n in request["supersedes"]), "invalid superseded request IDs")
            require(paths[0] == PREFIX + "request-" + digest(request) + ".json", "request content/path mismatch")
            batch = request["batch"]
            require(isinstance(batch, dict) and batch, "explicit nonempty batch required")
            result.append(dict(request=pr["number"], candidate=pr["merge_commit_sha"], base=sha(request["base"]), batch=batch, order=[],
                               supersedes=request["supersedes"], merged_at=pr["merged_at"]))
        durable = {p for p in release.git(self.root, "ls-tree", "-r", "--name-only", "refs/remotes/origin/main", "--", PREFIX).splitlines()
                   if p.startswith(PREFIX + "request-")}
        require(durable == seen, "missing durable request commit/PR evidence")
        numbers = {r["request"] for r in result}
        superseded = {n for r in result for n in r["supersedes"]}
        require(superseded <= numbers, "supersession must name reviewed request PRs")
        active = sorted([r for r in result if r["request"] not in superseded], key=lambda r: r["request"])
        for request in active:
            try:
                self.ancestor(request["base"])
                release.git(self.root, "merge-base", "--is-ancestor", request["base"], request["candidate"])
                with self.check(request["base"]) as check:
                    paths = set()
                    for path, version in request["batch"].items():
                        require(path in check.modules and check.modules[path]["role"] in release.PUBLISHED, "unselected/nonpublishable module")
                        release.validate_version(check.modules[path]["identity"], version, release=True)
                        paths.update(self.source_paths(check, path))
                    request["order"] = check.order(set(request["batch"]))
                    require(not release.git(self.root, "diff", "--name-only", request["base"], request["candidate"], "--", *sorted(paths)),
                            "module source/API/changelog changed after reviewed request base; fresh reviewed request required")
            except (Error, KeyError, TypeError, ValueError) as error:
                request["validation_error"] = str(error)
        return active

    def trusted_run(self, run, workflow=WORKFLOW):
        expected = self.api.call("GET", "/actions/workflows/" + workflow)
        require(run["workflow_id"] == expected["id"] and run["path"] == ".github/workflows/" + workflow,
                "wrong workflow identity")
        require(run["repository"]["full_name"] == self.api.repository
                and run["head_repository"]["full_name"] == self.api.repository
                and run["head_branch"] == "main" and run["event"] in ("workflow_dispatch", "workflow_run", "pull_request", "push"), "untrusted workflow run provenance")
        if run["event"] == "pull_request":
            raise Error("PR runs are not trusted release evidence")
        self.ancestor(run["head_sha"])
        # Historical main-side workflow revisions remain trusted provenance.
        # Their artifacts are independently schema/attempt/authority checked.
        release.git(self.root, "cat-file", "-e", run["head_sha"] + ":.github/workflows/" + workflow)

    def history(self, requests):
        records = []
        active = {r["request"]: r for r in requests}
        runs = self.api.all("/actions/workflows/" + WORKFLOW + "/runs", "workflow_runs")
        # A deleted *whole* run must not erase a failed attempt. GitHub's
        # immutable workflow-wide run_number exposes holes even when its jobs
        # and artifacts are gone. The next surviving run bounds affected intent.
        current = getattr(self, "run_metadata", None)
        if current is not None:
            numbered = {r["run_number"]: r for r in runs}
            numbered[current["run_number"]] = current
            previous = 0
            for number, surviving in sorted(numbered.items()):
                if number != previous + 1:
                    bound = surviving["created_at"]
                    for request in requests:
                        if request["merged_at"] <= bound:
                            records.append(dict(key=(bound, surviving["id"], 0), created_at=bound, finished_at=bound,
                                                action=dict(kind="uncertain", request=request["request"], reason="missing workflow run history; new superseding reviewed request required"),
                                                success=False, jobs={}))
                previous = number
        for summary in runs:
            if summary["head_branch"] != "main" or summary["event"] == "pull_request":
                continue  # Count their sequence slots, never their release authority.
            run_id = summary["id"]
            for attempt in range(1, summary["run_attempt"] + 1):
                if (run_id, attempt) == (self.current_run, self.current_attempt):
                    continue
                try:
                    run = self.api.call("GET", f"/actions/runs/{run_id}/attempts/{attempt}")
                    if run["status"] in ("queued", "requested", "pending"):
                        continue
                    jobs = self.api.all(f"/actions/runs/{run_id}/attempts/{attempt}/jobs", "jobs")
                except Error as error:
                    finished = summary.get("updated_at", summary["created_at"])
                    for request in requests:
                        if request["merged_at"] <= finished:
                            records.append(dict(key=(finished, run_id, attempt), created_at=finished, finished_at=finished,
                                                action=dict(kind="uncertain", request=request["request"], reason=str(error)), success=False, jobs={}))
                    continue
                started = min([j["started_at"] for j in jobs if j.get("started_at")] or [run.get("run_started_at", run["created_at"])])
                finished = max([j["completed_at"] for j in jobs if j.get("completed_at")] or [run.get("updated_at", started)])
                eligible = {n for n, r in active.items() if r["merged_at"] <= finished}
                bindings = set()
                for job in jobs:
                    match = re.fullmatch(r"(?:Execute|Write|Recover CI) request-([1-9][0-9]*)(?: blocked-(\[[0-9, ]*\]))?", job["name"])
                    if match:
                        bindings.add(int(match[1]))
                        if match[2]:
                            bindings.update(document(match[2]))
                # Job names are immutable server-side run evidence, not PR flags.
                # New workflows bind the affected request even if artifacts expire.
                scope = eligible & bindings if bindings else eligible
                if not scope:
                    continue
                key = (started, run_id, attempt)
                common = dict(key=key, created_at=started, finished_at=finished)
                try:
                    self.trusted_run(run)
                    require(run["run_attempt"] == attempt and run["id"] == run_id, "run/attempt mismatch")
                    require(run["status"] == "completed", "another release transition is still running")
                    artifacts = self.api.all(f"/actions/runs/{run_id}/artifacts", "artifacts")
                    artifacts = [a for a in artifacts if a["name"] == f"module-plan-{run_id}-{attempt}"]
                    executed = any(j["name"].split(" request-")[0] in ("Execute", "Write", "Recover CI", "Continue")
                                   and j["conclusion"] != "skipped" for j in jobs)
                    if not artifacts and not executed:
                        continue
                    require(len(artifacts) == 1 and not artifacts[0]["expired"], "missing/expired durable plan evidence; new superseding reviewed request required")
                    data = self.api.call("GET", f"/actions/artifacts/{artifacts[0]['id']}/zip")
                    with zipfile.ZipFile(io.BytesIO(data)) as archive:
                        require(archive.namelist() == ["plan.json"] and archive.getinfo("plan.json").file_size < 1000000, "invalid plan artifact")
                        plan = document(archive.read("plan.json"))
                    require(plan.get("schema") == 1 and plan["run"] == run_id and plan["attempt"] == attempt, "artifact schema/run/attempt mismatch")
                    action = plan["action"]
                    require(action["kind"] in ("prepare", "release", "prove", "recover-ci", "retry", "fail", "wait"), "unknown historical action schema")
                    if action["kind"] == "retry":
                        require(run["event"] == "workflow_dispatch", "retry must be a human dispatch")
                        self.human(run["actor"])
                        self.human(run["triggering_actor"])
                        common["created_at"] = run["created_at"]  # Dispatch intent, not time spent waiting for the lock.
                        if attempt > 1:
                            action = dict(kind="fail", request=action["request"], reason="rerun retry is not fresh human authorization")
                    conclusions = {j["name"].split(" request-")[0]: j["conclusion"] for j in jobs}
                    records.append(dict(common, action=action, success=run["conclusion"] == "success" and attempt == 1, jobs=conclusions))
                    for failure in plan.get("failures", []):
                        require(type(failure["request"]) is int and isinstance(failure["reason"], str), "invalid scoped failure evidence")
                        records.append(dict(common, action=dict(kind="fail", **failure), success=False, jobs={}))
                except (Error, OSError, ValueError, KeyError, TypeError, zipfile.BadZipFile) as error:
                    for number in scope:
                        records.append(dict(common, action=dict(kind="uncertain", request=number, reason=str(error)), success=False, jobs={}))
        return sorted(records, key=lambda r: r["key"])

    def ci(self, candidate, evidence=None):
        self.ancestor(candidate)
        workflow = self.api.call("GET", "/actions/workflows/ci.yml")
        original = release.git(self.root, "show", candidate + ":.github/workflows/ci.yml")
        require(any(line.strip() == "group: " + CI_GROUP for line in original.splitlines()), "candidate has unsafe old CI concurrency; new reviewed candidate required")
        if evidence is None:
            runs = self.api.all("/actions/workflows/ci.yml/runs?" + urlencode(dict(head_sha=candidate, event="push", branch="main")), "workflow_runs")
            require(len(runs) == 1, "missing or ambiguous exact-SHA main-push CI; new reviewed candidate required")
            summary = runs[0]
        else:
            summary = dict(id=evidence["run"], run_attempt=evidence["attempt"])
        run = self.api.call("GET", f"/actions/runs/{summary['id']}/attempts/{summary['run_attempt']}")
        require(run["id"] == summary["id"] and run["run_attempt"] == summary["run_attempt"]
                and run["workflow_id"] == workflow["id"] and run["path"] == ".github/workflows/ci.yml"
                and run["head_sha"] == candidate and run["event"] == "push" and run["head_branch"] == "main"
                and run["repository"]["full_name"] == self.api.repository
                and run["head_repository"]["full_name"] == self.api.repository, "CI repository/workflow/SHA/attempt mismatch")
        if evidence is not None:
            require(run["status"] == "completed" and run["conclusion"] == "success", "original CI qualification evidence is no longer valid")
        if run["status"] != "completed":
            return dict(kind="wait", reason="exact CI pending")
        if run["conclusion"] == "success":
            self.ci_receipt = dict(run=run["id"], attempt=run["run_attempt"])
            return None
        created = datetime.fromisoformat(run["created_at"].replace("Z", "+00:00"))
        require(self.now - created < timedelta(days=25), "CI recovery expired; new reviewed candidate required")
        require(run["conclusion"] == "cancelled" and run["run_attempt"] < 3, "CI failed or rerun budget exhausted; new reviewed candidate required")
        return dict(kind="recover-ci", ci_run=run["id"], ci_attempt=run["run_attempt"])

    def source_paths(self, check, module):
        entry = check.modules[module]
        return sorted({module} | {entry[k] for k in ("policy", "changelog", "api") if entry.get(k)})

    def source_diff(self, previous, base, module):
        with self.check(base) as check:
            return release.git(self.root, "diff", previous, base, "--", *self.source_paths(check, module))

    def preparations(self, prs, request, module):
        result = []
        for pr in prs:
            paths = [f["filename"] for f in self.files(pr) if f["filename"].startswith(PREFIX + "prep-")]
            for path in paths:
                metadata = document(self.api.file(path, pr["head"]["sha"]))
                if metadata.get("request") != request["request"] or metadata.get("module") != module:
                    continue
                self.reviewed_document(pr, path, pr["merged"])
                require(metadata["version"] == request["batch"][module] and metadata["batch"] == request["batch"], "preparation changes reviewed mapping")
                require(metadata["previous_candidate"] == request["candidate"], "unlinked preparation candidate")
                self.ancestor(metadata["base"])
                changes = self.source_diff(request["candidate"], metadata["base"], module)
                require(set(metadata) == {"schema", "kind", "request", "module", "version", "batch", "base", "previous_candidate", "source_diff_sha256"}
                        and metadata["schema"] == 1 and metadata["kind"] == "preparation", "invalid preparation metadata schema")
                require(metadata["source_diff_sha256"] == hashlib.sha256(changes.encode()).hexdigest(), "incorrect newly included source evidence")
                if pr["merged"]:
                    release.git(self.root, "merge-base", "--is-ancestor", metadata["base"], pr["merge_commit_sha"])
                    with self.check(metadata["base"]) as check:
                        changed = set(release.git(self.root, "diff", "--name-only", metadata["base"], pr["merge_commit_sha"], "--", *self.source_paths(check, module)).splitlines())
                    require(changed <= {module + "/go.mod", module + "/go.sum"}, "module source/API/changelog changed after reviewed preparation base; fresh reviewed preparation/request required")
                    for manifest in ("go.mod", "go.sum"):
                        name = module + "/" + manifest
                        require(self.api.file(name, pr["head"]["sha"], missing=True) == self.api.file(name, pr["merge_commit_sha"], missing=True), "merged manifests differ from reviewed preparation")
                allowed = {path, module + "/go.mod", module + "/go.sum"}
                require(all(f["filename"] in allowed and f["status"] != "renamed" for f in self.files(pr)), "preparation includes unrelated changes")
                result.append((pr, metadata))
        return sorted(result, key=lambda item: item[0]["number"])

    def next_action(self, requests, prs, records, fixed=None):
        reserved = set()
        self.finished_requests = set()
        self.active_request = None
        for request in requests:
            number, batch = request["request"], request["batch"]
            self.active_request = number
            require("validation_error" not in request, request.get("validation_error", "invalid request"))
            own = [r for r in records if r["action"].get("request") == number]
            retries = sorted([r for r in own if r["action"]["kind"] == "retry" and r["success"]], key=lambda r: (r["created_at"], r["key"]))
            retry_time = retries[-1]["created_at"] if retries else ""
            failed = [r for r in own if r["action"]["kind"] not in ("retry", "wait") and not r["success"]
                      and (r["action"]["kind"] == "uncertain" or r["finished_at"] >= retry_time)]
            if set(batch) & reserved:
                continue
            reserved.update(batch)
            if failed:
                continue  # Stop the WHOLE batch, including independent nodes.
            if not any(r["action"]["kind"] in ("release", "prepare", "prove")
                       and r["jobs"].get("Execute") == "success" and r["jobs"].get("Write") == "success" for r in own):
                # Validate the entire explicit set before its first side effect,
                # not just the first node (which might be independent of a bad
                # later consumer). Later candidates split after reviewed prep PRs.
                with self.check(request["candidate"]) as check:
                    check.preview(batch)
            for module in request["order"]:
                candidate = request["candidate"]
                preps = self.preparations(prs, request, module)
                if preps:
                    pr, metadata = preps[-1]
                    if pr["merged"]:
                        candidate = pr["merge_commit_sha"]
                    elif pr["state"] == "open":
                        break
                    elif not retries or retries[-1]["created_at"] <= pr["closed_at"]:
                        break  # Closing a prep PR is not release authorization.
                base_action = dict(request=number, module=module, version=batch[module], candidate=candidate)
                completed = [r for r in own if r["action"] == dict(base_action, kind="prove") and r["success"] and r["jobs"].get("Execute") == "success"]
                released = [r for r in own if r["action"].get("kind") == "release"
                            and all(r["action"].get(k) == v for k, v in base_action.items())
                            and r["jobs"].get("Execute") == "success" and r["jobs"].get("Write") == "success"]
                for record in released:
                    self.ci(candidate, record["action"]["ci"])
                with self.check(candidate) as check:
                    tag = check.tag_commit(module, batch[module], candidate)
                    if tag and not released:
                        qualified = [r for r in own if r["action"].get("kind") == "release"
                                     and all(r["action"].get(k) == v for k, v in base_action.items())
                                     and r["jobs"].get("Execute") == "success"]
                        require(qualified and retries, "existing tag lacks durable qualification/retry authority; new reviewed request required")
                        for record in qualified:
                            self.ci(candidate, record["action"]["ci"])
                    require(not completed or (tag and released), "missing tag/qualification authority for completed public proof")
                    if completed:
                        continue
                    if tag and released:
                        return dict(base_action, kind="prove")
                    # Use the reviewed request mapping to compute candidate/public
                    # split. Do not qualify against unpublished batch replacements.
                    pins = release.candidate_helpers.requirements(check.modules[module]["mod"])
                    updates = {dep: version for dep, version in batch.items() if dep != module
                               and check.modules[dep]["identity"] in pins
                               and pins[check.modules[dep]["identity"]][0] != version}
                    for dep in check.order(set(batch)):
                        if dep == module:
                            break
                        if check.modules[dep]["identity"] in pins:
                            proofs = [r for r in own if r["action"].get("module") == dep and r["action"]["kind"] == "prove" and r["success"] and r["jobs"].get("Execute") == "success"]
                            require(proofs, "selected prerequisite has no public proof")
                            expected = proofs[-1]["action"]["candidate"]
                            require(check.tag_commit(dep, batch[dep], expected), "prerequisite public proof lost its tag")
                    if updates:
                        # Only selected consumers are prepared, after public proofs.
                        base = release.git(self.root, "rev-parse", "refs/remotes/origin/main")
                        if fixed and fixed.get("kind") == "prepare" and all(fixed.get(k) == v for k, v in base_action.items()):
                            base = sha(fixed["base"])
                            self.ancestor(base)
                            release.git(self.root, "merge-base", "--is-ancestor", request["candidate"], base)
                        return dict(base_action, kind="prepare", base=base, updates=updates,
                                    batch=batch, previous_candidate=request["candidate"])
                    check.preview({module: batch[module]})
                evidence = fixed["ci"] if fixed and fixed.get("kind") == "release" and all(fixed.get(k) == v for k, v in base_action.items()) else None
                ci = self.ci(candidate, evidence)
                return dict(base_action, **(ci or {"kind": "release", "ci": self.ci_receipt}))
            else:
                self.finished_requests.add(number)
                reserved.difference_update(batch)  # Completed batches do not lock versions forever.
        return dict(kind="wait", reason="no authorized transition (complete, failed, paused, or waiting for review)")

    def plan(self, retry=None, actor=None, fixed=None):
        require(self.current_attempt == 1, "release reruns are not accepted; use a fresh dispatch and explicit human retry")
        # Discover every PR: event payloads and branch names are not authority.
        summaries = self.api.all("/pulls?state=all&base=main")
        # Identity/prefix only reduce API reads, never establish authority.
        # requests() also reconciles every durable main-side request file, so a
        # deleted/renamed branch or missing PR cannot silently hide a batch.
        prs = [self.api.call("GET", f"/pulls/{p['number']}") for p in summaries
               if p["user"].get("id") == self.bot_id or p["head"]["ref"].startswith("module-release/")]
        requests = self.requests(prs)
        records = self.history(requests) if requests else []
        failures = []
        if retry is not None:
            self.human(actor)
            require(any(r["request"] == retry for r in requests), "retry must name an existing reviewed request")
            action = dict(kind="retry", request=retry)
        else:
            action = dict(kind="wait", reason="no authorized transition (complete, failed, paused, or waiting for review)")
            reserved = set()
            for request in requests:
                if set(request["batch"]) & reserved:
                    continue
                try:
                    selected = self.next_action([request], prs, records, fixed)
                    if selected["kind"] != "wait":
                        action = selected
                        break
                    if request["request"] in self.finished_requests:
                        continue
                except (Error, KeyError, TypeError, ValueError) as error:
                    failures.append(dict(request=request["request"], reason=str(error)))
                reserved.update(request["batch"])
            if failures and action["kind"] == "wait":
                action = dict(kind="fail", **failures[0])
        result = dict(schema=1, run=self.current_run, attempt=self.current_attempt, action=action)
        if failures:
            result["failures"] = failures
        return result

    def request_plan(self, batch, actor, supersedes=None, candidate=None):
        self.human(actor)
        candidate = candidate or release.git(self.root, "rev-parse", "refs/remotes/origin/main")
        with self.check(candidate) as check:
            preview = check.preview(batch)
        supersedes = supersedes or []
        require(isinstance(supersedes, list) and all(type(n) is int and n > 0 for n in supersedes), "supersedes must be explicit request PR numbers")
        request = dict(schema=1, kind="request", base=candidate, batch=batch, nonce=f"{self.current_run}-{self.current_attempt}", supersedes=supersedes)
        path = PREFIX + "request-" + digest(request) + ".json"
        # Keep the PR readable and within GitHub's body limit. Exact source
        # diffs are linked to immutable commits; never interpret them as code.
        for item in preview["releases"]:
            item.pop("source_diff", None)
            item["source_url"] = "https://github.com/" + self.api.repository + "/tree/" + candidate + "/" + item["module"]
        require(len(canonical(preview)) < 55000, "preview too large for request PR; use a smaller explicit batch")
        return dict(kind="request", base=candidate, files={path: canonical(request)},
                    branch="module-release/request-" + digest(request), title="Request exact Go module releases",
                    body="Human merge authorizes exactly this mapping at the merge SHA. No auto-merge.\n\n```json\n" + canonical(preview) + "```\n")

    def execute(self, action):
        kind = action["kind"]
        if kind in ("wait", "retry"):
            return {}
        if kind == "fail":
            raise Error(action["reason"])
        if kind == "recover-ci":
            current = self.ci(action["candidate"])
            require(current and current["kind"] == "recover-ci" and current["ci_run"] == action["ci_run"] and current["ci_attempt"] == action["ci_attempt"], "CI recovery evidence changed")
            self.api.call("POST", f"/actions/runs/{action['ci_run']}/rerun", {})
            return {}
        if kind == "prove":
            last = None
            for _ in range(3):
                try:
                    with tempfile.TemporaryDirectory(prefix="module-public-proof-") as temporary, self.check(action["candidate"]) as check:
                        temporary = Path(temporary)
                        return check.prove(action["module"], action["version"], release.public_environment(temporary), temporary)
                except Error as error:
                    last = error
            raise Error(f"published-but-unverified (3 public attempts): {last}")
        if kind == "release":
            require(self.ci(action["candidate"], action["ci"]) is None, "exact candidate CI no longer successful")
            with tempfile.TemporaryDirectory(prefix="module-qualification-") as temporary, self.check(action["candidate"]) as check:
                return check.qualify({action["module"]: action["version"]}, release.public_environment(Path(temporary)))
        require(kind == "prepare", "unknown transition")
        with tempfile.TemporaryDirectory(prefix="module-prepare-") as temporary, self.check(action["base"]) as check:
            temporary = Path(temporary)
            env = release.public_environment(temporary)
            module = action["module"]
            check.no_replacements(check.modules[module]["mod"], module)
            directory = check.source / module
            before = {name: (directory / name).read_text() if (directory / name).exists() else None for name in ("go.mod", "go.sum")}
            verified = check.verify_prerequisites(check.modules[module]["mod"], env, updates=action["updates"])
            for dep, version in action["updates"].items():
                release.run(directory, "go", "mod", "edit", "-require=" + check.modules[dep]["identity"] + "@" + version, env=env)
            release.run(directory, "go", "mod", "tidy", env=env)
            files = {module + "/" + name: (directory / name).read_text() for name in before
                     if (directory / name).exists() and (directory / name).read_text() != before[name]}
            changes = self.source_diff(action["previous_candidate"], action["base"], module)
            metadata = dict(schema=1, kind="preparation", request=action["request"], module=module,
                            version=action["version"], batch=action["batch"], base=action["base"],
                            previous_candidate=action["previous_candidate"],
                            source_diff_sha256=hashlib.sha256(changes.encode()).hexdigest())
            path = PREFIX + f"prep-{action['request']}-{self.current_run}-{self.current_attempt}.json"
            files[path] = canonical(metadata)
            comparison = "https://github.com/" + self.api.repository + "/compare/" + action["previous_candidate"] + "..." + action["base"]
            evidence = "```diff\n" + changes + "\n```\n" if len(changes) < 40000 else comparison
            return dict(kind="prepare", base=action["base"], files=files,
                        public_prerequisites=list(verified.values()),
                        branch=f"module-release/prep-{action['request']}-{self.current_run}-{self.current_attempt}",
                        title=f"Prepare {module}@{action['version']} (request #{action['request']})",
                        body="Human merge reconfirms this exact version and newly included source. No auto-merge.\n\n"
                             + "```json\n" + canonical(metadata) + "```\n\nSource since request: " + comparison + "\n" + evidence)

    def validate_write(self, plan, result):
        action = plan["action"]
        require(plan["run"] == self.current_run and plan["attempt"] == self.current_attempt, "write plan run mismatch")
        # Re-read durable authority; a downloaded plan is data, not authorization.
        require(self.plan(fixed=action)["action"] == action, "release authority changed since planning")
        jobs = self.api.all(f"/actions/runs/{self.current_run}/attempts/{self.current_attempt}/jobs", "jobs")
        require(any(j["name"].split(" request-")[0] == "Execute" and j["conclusion"] == "success" for j in jobs), "unprivileged execution did not succeed")
        if action["kind"] == "release":
            require(result.get("stage") == "qualified" and result.get("candidate") == action["candidate"]
                    and result.get("order") == [action["module"]]
                    and [(r.get("module"), r.get("version")) for r in result.get("releases", [])] == [(action["module"], action["version"])], "missing exact qualification result")
            with self.check(action["candidate"]) as check:
                tag = check.modules[action["module"]]["tag_prefix"] + action["version"]
            return dict(kind="tag", tag=tag, sha=action["candidate"])
        require(action["kind"] == "prepare" and result.get("kind") == "prepare", "invalid write stage")
        expected_branch = f"module-release/prep-{action['request']}-{self.current_run}-{self.current_attempt}"
        require(result["base"] == action["base"] and result["branch"] == expected_branch, "preparation base/branch mismatch")
        module = action["module"]
        metadata_path = PREFIX + f"prep-{action['request']}-{self.current_run}-{self.current_attempt}.json"
        expected_metadata = {key: action[key] for key in ("request", "module", "version", "batch", "base", "previous_candidate")}
        changes = self.source_diff(action["previous_candidate"], action["base"], module)
        expected_metadata.update(schema=1, kind="preparation", source_diff_sha256=hashlib.sha256(changes.encode()).hexdigest())
        require(document(result["files"][metadata_path]) == expected_metadata, "preparation metadata mismatch")
        require(set(result["files"]) <= {metadata_path, module + "/go.mod", module + "/go.sum"}, "unselected consumer or unrelated file")
        require(module + "/go.mod" in result["files"], "preparation has no pin changes")
        # Parse data with the trusted Go tool, never with candidate scripts. No
        # tests, downloads, tidy, or candidate code execute in this protected job.
        with self.check(action["base"]) as check, tempfile.TemporaryDirectory(prefix="module-pin-validation-") as temporary:
            temporary = Path(temporary)
            (temporary / "go.mod").write_text(result["files"][module + "/go.mod"])
            env = release.public_environment(temporary)
            after = release.candidate_helpers.parse_mod(temporary, env, temporary / "go.mod")
            before = check.modules[module]["mod"]
            require({k: v for k, v in before.items() if k not in ("Require", "Go")} == {k: v for k, v in after.items() if k not in ("Require", "Go")}, "tidy changed unrelated directives/replacements")
            old, new = release.candidate_helpers.requirements(before), release.candidate_helpers.requirements(after)
            for dep, version in action["updates"].items():
                require(new.get(check.modules[dep]["identity"], (None,))[0] == version, "missing selected consumer pin")
            identities = {check.modules[d]["identity"] for d in action["updates"]}
            for identity, value in old.items():
                if identity not in identities and not value[1]:
                    require(new.get(identity) == value, "tidy altered unrelated direct dependency")
            for identity, value in new.items():
                require(identity in old or identity in identities or value[1], "tidy introduced unrelated direct dependency")
        require(len(canonical(result)) < 1000000, "preparation too large")
        return result


def protection_check(repository, app_id, bot_id):
    """Task-5 integration: fixed trusted checker; missing checker fails CLOSED.

    CLI: module-release-protection.py check --repository OWNER/REPO --app-id N
         --bot-id N
    Successful stdout: {schema:1, repository:..., app_id:N, bot_id:N, protected:true}.
    Uses read-only GH_TOKEN for settings and the existing protected App key/JWT
    for identity/installation GETs, before minting an installation write token.
    """
    require(os.environ.get("MODULE_RELEASES_ENABLED") == "true", "module releases are disabled")
    checker = HERE / "module-release-protection.py"
    require(checker.is_file(), "release protection checker not installed; releases remain disabled")
    env = os.environ.copy()
    # Keep App authentication confined to the checker, not later Go parsing or
    # candidate-materialization subprocesses in protected validation.
    for name in ("RELEASE_APP_PRIVATE_KEY", "RELEASE_APP_JWT"):
        os.environ.pop(name, None)
    result = subprocess.run([sys.executable, str(checker), "check", "--repository", repository,
                             "--app-id", str(app_id), "--bot-id", str(bot_id)], capture_output=True, text=True, env=env)
    require(result.returncode == 0, "release protection check failed; run module-release-protection.py check locally with the same repository/App/bot IDs for diagnostics; see .github/scripts/module-release-protection.md for metadata credentials and repair")
    evidence = document(result.stdout)
    require(evidence == dict(schema=1, repository=repository, app_id=int(app_id), bot_id=int(bot_id), protected=True), "invalid protection evidence")


def write_transaction(api, transaction):
    """Only called with a prevalidated local transaction in the protected job.

    This function is deliberately API-only: no candidate checkout, subprocess,
    hook, dependency resolution, dynamic import, or artifact execution.
    """
    if transaction["kind"] == "tag":
        commit, tag = sha(transaction["sha"]), transaction["tag"]
        require(re.fullmatch(r"[A-Za-z0-9_/-]+/v[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9.-]+)?", tag) and ".." not in tag, "invalid module tag")
        existing = api.call("GET", "/git/ref/tags/" + quote(tag, safe="/"), missing=True)
        if existing:
            require(existing["object"]["type"] == "commit" and existing["object"]["sha"] == commit, "immutable tag conflict")
        else:
            api.call("POST", "/git/refs", dict(ref="refs/tags/" + tag, sha=commit))
        return
    require(transaction["kind"] in ("request", "prepare"), "invalid write transaction")
    base = sha(transaction["base"])
    require(re.fullmatch(r"module-release/(?:request-[0-9a-f]{64}|prep-[0-9]+-[0-9]+-[0-9]+)", transaction["branch"]), "invalid release PR branch")
    require(isinstance(transaction["files"], dict) and transaction["files"], "empty release transaction")
    tree = api.call("GET", "/git/commits/" + base)["tree"]["sha"]
    entries = []
    for path, content in sorted(transaction["files"].items()):
        require(not path.startswith("/") and all(p not in ("", ".", "..") for p in path.split("/")), "unsafe transaction path")
        blob = api.call("POST", "/git/blobs", dict(content=content, encoding="utf-8"))
        entries.append(dict(path=path, mode="100644", type="blob", sha=blob["sha"]))
    tree = api.call("POST", "/git/trees", dict(base_tree=tree, tree=entries))["sha"]
    commit = api.call("POST", "/git/commits", dict(message=transaction["title"], tree=tree, parents=[base]))["sha"]
    ref = "/git/ref/heads/" + transaction["branch"]
    existing = api.call("GET", ref, missing=True)
    if existing:
        # A crash may leave this exact immutable branch. Never reset another
        # candidate or recreate a closed PR in response to an unrelated event.
        existing_tree = api.call("GET", "/git/commits/" + existing["object"]["sha"])["tree"]["sha"]
        require(existing_tree == tree, "release PR branch conflict")
    else:
        api.call("POST", "/git/refs", dict(ref="refs/heads/" + transaction["branch"], sha=commit))
    prs = api.all("/pulls?state=all&head=" + quote(api.repository.split("/")[0] + ":" + transaction["branch"], safe=""))
    if prs:
        require(len(prs) == 1, "ambiguous release PR")
        return
    api.call("POST", "/pulls", dict(title=transaction["title"], body=transaction["body"], head=transaction["branch"], base="main"))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("plan", "execute", "validate-write", "write", "request-plan", "request-validate", "continue"))
    parser.add_argument("--root", type=Path, default=Path.cwd())
    parser.add_argument("--input", type=Path)
    parser.add_argument("--result", type=Path)
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    try:
        repository = os.environ["GITHUB_REPOSITORY"]
        app_id, bot_id = int(os.environ["RELEASE_APP_ID"]), int(os.environ["RELEASE_APP_BOT_ID"])
        require(os.environ.get("MODULE_RELEASES_ENABLED") == "true", "module releases are disabled")
        require(os.environ.get("GITHUB_REF") == "refs/heads/main", "release workflows must run from main")
        api = GitHub(repository, os.environ.get("GH_TOKEN", ""))
        driver = Driver(api, args.root, app_id, bot_id, os.environ["GITHUB_RUN_ID"], os.environ["GITHUB_RUN_ATTEMPT"])
        value = release.load_json(args.input) if args.input else None
        actor = api.call("GET", "/actions/runs/" + str(driver.current_run))
        driver.run_metadata = actor
        require(actor["id"] == driver.current_run and actor["run_attempt"] == driver.current_attempt == 1,
                "stale run attempt/rerun; use a fresh dispatch and explicit human retry")
        require(actor["event"] == os.environ["GITHUB_EVENT_NAME"], "workflow event provenance mismatch")
        if args.command != "write":
            driver.trusted_run(actor, REQUEST_WORKFLOW if args.command in ("request-plan", "request-validate") else WORKFLOW)
        if args.command == "request-validate":
            require(os.environ["GITHUB_EVENT_NAME"] == "workflow_dispatch", "requests require human dispatch")
        if args.command == "plan":
            retry = os.environ.get("RETRY_REQUEST", "")
            require(not retry or re.fullmatch(r"[1-9][0-9]*", retry), "retry must be an exact request PR number")
            if retry:
                require(os.environ["GITHUB_EVENT_NAME"] == "workflow_dispatch", "retry requires human dispatch")
                driver.human(actor["triggering_actor"])
            value = driver.plan(int(retry) if retry else None, actor["actor"])
        elif args.command == "request-plan":
            require(os.environ["GITHUB_EVENT_NAME"] == "workflow_dispatch", "requests require human dispatch")
            driver.human(actor["triggering_actor"])
            value = driver.request_plan(document(os.environ["MODULE_BATCH"]), actor["actor"], document(os.environ.get("SUPERSEDES_REQUESTS", "[]")))
        elif args.command == "execute":
            require(value["run"] == driver.current_run and value["attempt"] == driver.current_attempt, "execution plan run/attempt mismatch")
            require(driver.plan(fixed=value["action"])["action"] == value["action"], "release authority changed before execution")
            value = driver.execute(value["action"])
        elif args.command in ("validate-write", "request-validate"):
            protection_check(repository, app_id, bot_id)
            if args.command == "validate-write":
                value = driver.validate_write(value, release.load_json(args.result))
            else:
                driver.human(actor["triggering_actor"])
                require(driver.request_plan(document(os.environ["MODULE_BATCH"]), actor["actor"], document(os.environ.get("SUPERSEDES_REQUESTS", "[]")), candidate=sha(value["base"])) == value, "request changed after preview")
        elif args.command == "write":
            # No subprocesses after minting. Only the trusted prevalidation step
            # writes this runner-local file; it is NOT a downloaded artifact.
            api = GitHub(repository, os.environ["MODULE_RELEASE_WRITE_TOKEN"])
            write_transaction(api, value)
            value = {}
        else:
            require(value["run"] == driver.current_run and value["attempt"] == driver.current_attempt, "continuation plan run/attempt mismatch")
            require(value["action"]["kind"] not in ("wait", "recover-ci", "fail"), "no immediate continuation needed")
            api.call("POST", "/actions/workflows/" + WORKFLOW + "/dispatches", dict(ref="main"))
            value = {}
        if args.output:
            args.output.write_text(canonical(value))
        else:
            print(canonical(value), end="")
        return 0
    except (Error, OSError, ValueError, KeyError, TypeError) as error:
        print(f"module-release-github: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
