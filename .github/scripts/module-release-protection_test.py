#!/usr/bin/env python3
"""Offline effective-protection and real shared-App caller regressions."""
import base64
import copy
import hashlib
import io
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch
from urllib.parse import urlsplit, parse_qs
from urllib.error import HTTPError

sys.dont_write_bytecode = True
HERE = Path(__file__).resolve().parent
ROOT = HERE.parent.parent


def load(name):
    spec = importlib.util.spec_from_file_location(name.replace("-", "_"), HERE / (name + ".py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


p = load("module-release-protection")
g = load("release-app-guard")
COMMIT = "a" * 40
OTHER = "b" * 40
REPOSITORY = "owner/repo"
BASE = "/repos/" + REPOSITORY


def fixture(track="root", current=False):
    tag, branch = g.refs(track, "1.2.3")
    rules = p.desired(70) + p.batch_desired(70, 80)
    for i, rule in enumerate(rules, 1):
        rule.update(id=i, source=REPOSITORY, source_type="Repository")
    return dict(rules=rules, environment=dict(deployment_branch_policy=dict(protected_branches=False, custom_branch_policies=True)),
                branches=[dict(id=1, name="main", type="branch")],
                bot=dict(id=700, login="release-test[bot]", type="Bot"),
                app=dict(id=70, slug="release-test", client_id="Iv1.public"),
                installations=[dict(id=7, app_id=70, app_slug="release-test", suspended_at=None,
                                    account=dict(id=5, login="owner", type="Organization"), target_id=5, target_type="Organization",
                                    repository_selection="selected", permissions=dict(contents="write", pull_requests="write"))],
                repository=dict(id=9, full_name=REPOSITORY, private=False, allow_merge_commit=True, owner=dict(id=5, login="owner", type="Organization")),
                main_rules=[], classic=None, team=dict(id=80), team_access=dict(permissions=dict(push=True)),
                team_members=[dict(id=3, type="User")],
                repositories=[dict(id=9, full_name=REPOSITORY)], repo_secrets=[], org_secrets=[],
                environment_secrets=[dict(name="RELEASE_APP_PRIVATE_KEY")], unavailable_paths=[], app_reads=[],
                pr=dict(number=12, merged=True, merged_at="2026-11-15T00:00:00Z", merge_commit_sha=COMMIT,
                        user=dict(id=700, type="Bot"), merged_by=dict(id=3, type="User"),
                        head=dict(ref=branch, repo=dict(id=9, full_name=REPOSITORY)),
                        base=dict(ref="main", repo=dict(id=9, full_name=REPOSITORY))),
                tags=([dict(ref="refs/tags/" + tag, object=dict(type="commit", sha=COMMIT))] if current else []),
                run=dict(path=".github/workflows/create-" + ("release" if track == "root" else "sdk-typescript-release") + "-pr.yml",
                         head_sha=COMMIT, head_branch="main", event="workflow_dispatch", repository=dict(full_name=REPOSITORY),
                         triggering_actor=dict(id=3, login="release-human", type="User")), writes=[], reads=[], human=dict(type="User"), admin=True)


class Fake(p.GitHub):
    def __init__(self, state):
        self.state = state

    def call(self, method, path, data=None, metadata=False, app=False):
        state = self.state
        commit = state.get("commit", COMMIT)
        parsed = urlsplit(path)
        path, query = parsed.path, parse_qs(parsed.query)
        if path in state["unavailable_paths"]:
            raise p.Error("fixture metadata unavailable")
        if app:
            if method != "GET" or path not in ("/app", BASE + "/installation"):
                raise AssertionError("unexpected App JWT request")
            state["app_reads"].append(path)
        if method == "POST" and path == "/graphql":
            if data != dict(query=p.MAIN_POLICY_QUERY, variables=dict(owner="owner", name="repo")):
                raise AssertionError("only the fixed read-only main policy query is permitted")
            state["reads"].append(path)
            return state.get("graphql", dict(data=dict(repository=dict(ref=dict(branchProtectionRule=state["classic"])))))
        if method != "GET":
            state["writes"].append([method, path, data])
            if method == "POST" and path == BASE + "/rulesets":
                rule = copy.deepcopy(data)
                rule.update(id=max((r["id"] for r in state["rules"]), default=0) + 1, source=REPOSITORY, source_type="Repository")
                state["rules"].append(rule)
                return rule
            if method == "POST" and path in (BASE + "/git/blobs", BASE + "/git/trees", BASE + "/git/commits"):
                return dict(sha=OTHER)
            if method == "POST" and path == BASE + "/pulls":
                return dict(number=13)
            if method == "POST" and path == BASE + "/git/tags":
                return dict(sha=OTHER)
            if method == "POST" and path == BASE + "/git/refs":
                state["tags"].append(dict(ref=data["ref"], object=dict(sha=data["sha"], type="tag")))
                return state["tags"][-1]
            raise AssertionError((method, path, data))
        state["reads"].append(path)
        if query.get("page", ["1"])[0] != "1":
            raise AssertionError("fixture should fit one page")
        if path == "/users/release-human":
            return state["run"]["triggering_actor"]
        if path == "/users/release-test%5Bbot%5D":
            return state["bot"]
        if path == "/app":
            if not app:
                raise AssertionError("App identity must be JWT authenticated")
            return state["app"]
        if path == BASE + "/installation":
            if not app:
                raise AssertionError("installation must be JWT authenticated")
            return state["installations"][0]
        if path == BASE + "/environments/release":
            return state["environment"]
        if path == BASE + "/environments/release/deployment-branch-policies":
            return dict(branch_policies=state["branches"])
        if path == BASE + "/rulesets":
            return [{key: r[key] for key in ("id", "source", "source_type")} for r in state["rules"]]
        if re.fullmatch(r"/(repos/owner/repo|orgs/owner)/rulesets/[0-9]+", path):
            return next(r for r in state["rules"] if r["id"] == int(path.rsplit("/", 1)[1]))
        if path == BASE + "/git/commits/" + commit:
            return dict(tree=dict(sha=OTHER))
        if path == BASE + "/git/ref/heads/main":
            return dict(object=dict(sha=commit))
        if path.startswith((BASE + "/contents/.github/workflows/", BASE + "/contents/.github/actions/")):
            content = (ROOT / path.split("/contents/", 1)[1]).read_bytes()
            blob = hashlib.sha1(b"blob " + str(len(content)).encode() + b"\0" + content).hexdigest()
            return dict(type="file", sha=state.get("workflow_sha", blob))
        if path == BASE + "/contents/sdk/typescript/VERSION" and query.get("ref"):
            return dict(content=base64.b64encode(state["previous_version"].encode()).decode())
        if path == BASE + "/rules/branches/main":
            return state["main_rules"]
        if path == "/orgs/owner/teams/release-humans":
            return state["team"]
        if path == "/orgs/owner/teams/release-humans/repos/owner/repo":
            return state["team_access"]
        if path == "/orgs/owner/teams/release-humans/members":
            return state["team_members"]
        if path == "/user":
            return state["human"]
        if path == BASE:
            return dict(state["repository"], permissions=dict(admin=state["admin"]))
        if path == BASE + "/actions/secrets":
            return dict(secrets=state["repo_secrets"])
        if path == "/orgs/owner/actions/secrets":
            return dict(secrets=state["org_secrets"])
        if path.startswith("/orgs/owner/actions/secrets/") and path.endswith("/repositories"):
            return dict(repositories=state["repositories"])
        if path == "/repositories/9/environments/release/secrets":
            return dict(secrets=state["environment_secrets"])
        if path == BASE + "/commits/" + commit + "/pulls":
            return [state["pr"]]
        if path == BASE + "/pulls/12":
            return state["pr"]
        if path.startswith(BASE + "/git/matching-refs/tags/"):
            tag = path.split("/git/matching-refs/", 1)[1]
            return [r for r in state["tags"] if r["ref"].startswith("refs/" + tag)]
        if path == BASE + "/git/tags/" + OTHER:
            return dict(object=dict(sha=COMMIT, type="commit"))
        if path == BASE + "/actions/runs/42":
            return state["run"]
        raise AssertionError((method, path))


def fake_cli():
    # Executed only by the test-owned gh executable. Unknown calls fail; there
    # is no fallback to a host gh binary or network client.
    state_path = Path(os.environ["RELEASE_TEST_STATE"])
    state = json.loads(state_path.read_text())
    args = sys.argv[1:]
    method, path = args[args.index("--method") + 1:args.index("--method") + 3]
    if method == "GET" and "-f" in args:
        path += "?" + args[args.index("-f") + 1]
    data = json.load(sys.stdin) if "--input" in args else None
    try:
        result = Fake(state).call(method, path, data)
    except p.Error:
        print("fixture metadata unavailable", file=sys.stderr)
        sys.exit(1)
    state_path.write_text(json.dumps(state))
    if "--jq" in args and args[args.index("--jq") + 1] == ".content":
        print(result["content"])
    else:
        print(json.dumps(result))


def fake_http(request):
    require_url = urlsplit(request.full_url)
    if require_url.scheme != "https" or require_url.netloc != "api.github.com" or request.get_method() != "GET":
        raise AssertionError("unexpected HTTP request (network is disabled)")
    if request.get_header("Authorization") != "Bearer test-app-jwt-not-a-secret":
        raise AssertionError("App request must use the isolated JWT")
    state_path = Path(os.environ["RELEASE_TEST_STATE"])
    state = json.loads(state_path.read_text())
    try:
        result = Fake(state).call("GET", require_url.path, app=True)
    except p.Error:
        raise HTTPError(request.full_url, 403, "fixture denied", {}, None) from None
    state_path.write_text(json.dumps(state))
    return io.BytesIO(json.dumps(result).encode())


def step(workflow, name):
    lines = (ROOT / ".github/workflows" / workflow).read_text().splitlines()
    start = lines.index("      - name: " + name)
    end = next((i for i in range(start + 1, len(lines)) if lines[i].startswith("      - ")), len(lines))
    block = lines[start:end]
    run = next(i for i, line in enumerate(block) if line.startswith("        run:"))
    if block[run] == "        run: |":
        return "\n".join(line[10:] for line in block[run + 1:] if line.startswith("          "))
    return block[run].split("run: ", 1)[1]


class Protection(unittest.TestCase):
    def setUp(self):
        self.state = fixture()
        self.env = patch.dict(os.environ, RELEASE_APP_CLIENT_ID="Iv1.public", RELEASE_BATCH_HUMAN_TEAM="release-humans")
        self.env.start()
        self.addCleanup(self.env.stop)

    def run_check(self, command="check", confirm=False):
        return p.run(Fake(self.state), command, REPOSITORY, 70, 700, confirm)

    def test_exact_evidence_no_writes(self):
        self.assertEqual(self.run_check(), dict(schema=2, repository=REPOSITORY, app_id=70, bot_id=700, protected=True))
        self.assertEqual(self.state["writes"], [])

    def test_inherited_stronger_patterns_and_no_creation_bypass(self):
        for rule in self.state["rules"]:
            rule.update(source="owner", source_type="Organization")
            if rule["target"] == "tag":
                rule["conditions"]["ref_name"]["include"] = ["refs/tags/**/*"]
            rule["bypass_actors"] = []
        self.assertTrue(self.run_check()["protected"])
        self.assertIn("/orgs/owner/rulesets/1", self.state["reads"])

    def test_pathname_pattern_containment(self):
        for expression in ("refs/tags/**/*", "**/v*", "refs/tags/engine/**", "refs/tags/*/v*"):
            self.assertTrue(p.covers(expression, "refs/tags/engine/v*"), expression)
        for expression in ("refs/tags/*", "refs/tags/**", "refs/tags/v*", "refs/tags/provider/*", "refs/tags/engine/v1*", "refs/tags/engine/v*/**", "refs/tags/[^p]*/v*"):
            self.assertFalse(p.covers(expression, "refs/tags/engine/v*"), expression)
        self.assertFalse(p.covers("refs/tags/*/v*", "refs/tags/provider/openai/v*"))

    def test_creation_bypass_does_not_authorize_mutation(self):
        self.state["rules"][1]["bypass_actors"] = copy.deepcopy(self.state["rules"][0]["bypass_actors"])
        with self.assertRaises(p.Error):
            self.run_check()

    def test_missing_coverage_exclusion_disabled_and_wrong_app(self):
        mutations = [lambda s: s["rules"][0]["bypass_actors"][0].update(actor_id=71),
                     lambda s: s["rules"][0]["bypass_actors"][0].update(actor_type="RepositoryRole"),
                     lambda s: s["rules"][0]["bypass_actors"][0].update(bypass_mode="pull_request"),
                     lambda s: s["rules"][1].pop("bypass_actors"),
                     lambda s: s["rules"][0].update(enforcement="evaluate"),
                     lambda s: s["rules"][1]["conditions"]["ref_name"].update(include=["refs/tags/v*"]),
                     lambda s: s["rules"][1]["conditions"]["ref_name"].update(exclude=["refs/tags/engine/v9*"]),
                     lambda s: s["rules"][1].update(rules=[dict(type="deletion")])]
        for mutate in mutations:
            with self.subTest(mutate=mutate):
                self.state = fixture()
                mutate(self.state)
                with self.assertRaises(p.Error):
                    self.run_check()

    def test_environment_app_mapping_installation_and_workflow_fail_closed(self):
        mutations = [lambda s: s["branches"][0].update(name="release/*"),
                     lambda s: s["branches"][0].update(type="tag"),
                     lambda s: s["branches"].append(dict(name="*", type="branch")),
                     lambda s: s["environment"].update(deployment_branch_policy=None),
                     lambda s: s["app"].update(id=71),
                     lambda s: s["app"].update(client_id="Iv1.other"),
                     lambda s: s["installations"][0]["permissions"].update(administration="write"),
                     lambda s: s["installations"][0]["permissions"].update(contents="read"),
                     lambda s: s["installations"][0].update(suspended_at="today"),
                     lambda s: s["installations"][0]["permissions"].update(workflows="write"),
                     lambda s: s["installations"][0].update(app_id=71),
                     lambda s: s["installations"][0]["account"].update(id=6),
                     lambda s: s["repository"].update(full_name="owner/other"),
                     lambda s: s.update(unavailable_paths=[BASE + "/installation"]),
                     lambda s: s.update(unavailable_paths=[BASE + "/contents/.github/actions/module-release-ci-bootstrap/action.yml"]),
                     lambda s: s.update(workflow_sha="c" * 40)]
        for mutate in mutations:
            with self.subTest(mutate=mutate):
                self.state = fixture()
                mutate(self.state)
                with self.assertRaises(p.Error):
                    self.run_check()
                self.assertFalse(self.state["writes"])

    def test_secret_scope_is_explicit_rollout_audit_not_routine_requirement(self):
        self.state["unavailable_paths"] = ["/orgs/owner/actions/secrets", BASE + "/actions/secrets"]
        self.assertTrue(self.run_check()["protected"])
        with self.assertRaises(p.Error):
            self.run_check("audit")
        for mutate in (lambda s: s.update(repo_secrets=[dict(name="RELEASE_APP_PRIVATE_KEY")]),
                       lambda s: s.update(org_secrets=[dict(name="RELEASE_APP_PRIVATE_KEY", visibility="all")]),
                       lambda s: s.update(org_secrets=[dict(name="RELEASE_SETTINGS_TOKEN", visibility="selected")]),
                       lambda s: s.update(environment_secrets=[])):
            self.state = fixture()
            mutate(self.state)
            with self.assertRaises(p.Error):
                self.run_check("audit")
        self.state = fixture()
        self.assertTrue(self.run_check("audit")["credential_scope_audited"])
        self.assertFalse(self.state["writes"])

    def test_batch_and_state_rules_are_independent_and_complete(self):
        rules = {r["name"]: r for r in self.state["rules"]}
        for suffix in ("review", "safety", "state-updates", "state-permanent"):
            self.assertEqual(rules["module-batch-" + suffix]["bypass_actors"], [])
        self.assertEqual(rules["module-batch-updates"]["bypass_actors"],
                         [dict(actor_id=80, actor_type="Team", bypass_mode="pull_request")])
        for suffix in ("creation", "state-creation", "state-active"):
            self.assertEqual(rules["module-batch-" + suffix]["bypass_actors"],
                             [dict(actor_id=70, actor_type="Integration", bypass_mode="always")])
        for name in ("active", "42-1/reservation", "42-1/candidate", "42-1/grant-" + "a" * 64,
                     "42-1/terminal", "future/deeper/authority"):
            target = "refs/heads/module-batch-state/" + name
            applicable = [r for r in self.state["rules"] if r["target"] == "branch"
                          and any(p.covers(x, target) for x in r["conditions"]["ref_name"]["include"])
                          and not any(p.covers(x, target) for x in r["conditions"]["ref_name"]["exclude"])]
            self.assertTrue(any(not r["bypass_actors"] and any(x["type"] == "update" for x in r["rules"]) for r in applicable), name)
            deletions = [r for r in applicable if any(x["type"] == "deletion" for x in r["rules"])]
            self.assertTrue(deletions, name)
            self.assertEqual(any(not r["bypass_actors"] for r in deletions), name != "active", name)

    def test_branch_wildcard_containment_does_not_guess(self):
        self.assertTrue(p.covers("refs/heads/module-batch/*", "refs/heads/module-batch/*"))
        self.assertFalse(p.covers("refs/heads/module-batch/?", "refs/heads/module-batch/*"))
        self.assertFalse(p.covers("refs/heads/module-batch/42*", "refs/heads/module-batch/*"))
        for pattern in ("refs/heads/module-batch-state/*", "refs/heads/module-batch-state/**",
                        "refs/heads/module-batch-state/*/*", "refs/heads/module-batch-state/active"):
            self.assertFalse(p.covers(pattern, "refs/heads/module-batch-state/**/*"), pattern)

    def test_no_merge_toggle_and_no_policy_weakening(self):
        for classic, main_rules in ((dict(requiresLinearHistory=True), []),
                                    (None, [dict(type="required_linear_history")]),
                                    (None, [dict(type="pull_request", parameters=dict(allowed_merge_methods=["squash"]))])):
            self.state = fixture()
            self.state.update(classic=classic, main_rules=main_rules)
            self.assertTrue(self.run_check("diff")["prerequisites"])
            self.run_check("apply", True)
            with self.assertRaises(p.Error):
                self.run_check()
            self.assertFalse(self.state["writes"])
        self.state = fixture()
        self.state["repository"]["allow_merge_commit"] = False
        self.assertTrue(self.run_check("diff")["prerequisites"])
        self.run_check("apply", True)
        with self.assertRaises(p.Error):
            self.run_check()
        self.assertFalse(self.state["writes"])
        self.state = fixture()
        self.state["classic"] = dict(requiresLinearHistory=False)
        self.state["main_rules"] = [dict(type="pull_request", parameters=dict(allowed_merge_methods=["merge", "squash"]))]
        self.assertTrue(self.run_check()["protected"])

    def test_dry_run_apply_idempotent_preserves_inherited_and_existing(self):
        inherited = copy.deepcopy(self.state["rules"][1])
        inherited.update(name="strong organization policy", id=10, source="owner", source_type="Organization")
        inherited["conditions"]["ref_name"]["include"] = ["refs/tags/engine/**"]
        self.state["rules"] = [inherited]
        before = copy.deepcopy(inherited)
        count = len(p.desired(70) + p.batch_desired(70, 80))
        self.assertEqual(len(self.run_check("diff")["create_rulesets"]), count)
        self.run_check("apply")
        self.assertFalse(self.state["writes"])
        self.run_check("apply", True)
        self.assertEqual(len(self.state["writes"]), count)
        self.assertEqual(self.state["rules"][0], before)
        self.run_check("apply", True)
        self.assertEqual(len(self.state["writes"]), count)
        self.assertTrue(self.run_check()["protected"])
        self.state["human"] = dict(type="Bot")
        with self.assertRaises(p.Error):
            self.run_check("apply", True)


class Callers(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="release-protection-test-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        scripts = self.root / ".github/scripts"
        scripts.mkdir(parents=True)
        for name in ("release-app-guard.py", "module-release-protection.py", "module-publication-policy.json"):
            shutil.copyfile(HERE / name, scripts / name)
        shutil.copytree(ROOT / ".github/workflows", self.root / ".github/workflows")
        shutil.copytree(ROOT / ".github/actions/module-release-ci-bootstrap",
                        self.root / ".github/actions/module-release-ci-bootstrap")
        self.state_path = self.root / "state.json"
        gh = self.root / "gh"
        gh.write_text("#!/usr/bin/env python3\nimport os,runpy\nrunpy.run_path(os.environ['RELEASE_TEST_MODULE'])['fake_cli']()\n")
        gh.chmod(0o700)
        (self.root / "sitecustomize.py").write_text(
            "import os,runpy,urllib.request\n"
            "class OfflineOpener:\n"
            " def open(self, request, timeout):\n"
            "  return runpy.run_path(os.environ['RELEASE_TEST_MODULE'])['fake_http'](request)\n"
            "urllib.request.build_opener = lambda *args: OfflineOpener()\n")
        self.env = dict(os.environ, PATH=str(self.root) + os.pathsep + os.environ["PATH"],
                        RELEASE_TEST_STATE=str(self.state_path), RELEASE_TEST_MODULE=str(Path(__file__).resolve()),
                        GITHUB_REPOSITORY=REPOSITORY, REPOSITORY=REPOSITORY, GITHUB_SHA=COMMIT, SHA=COMMIT,
                        GITHUB_REF="refs/heads/main", GITHUB_EVENT_NAME="workflow_dispatch", GITHUB_RUN_ID="42",
                        GITHUB_TRIGGERING_ACTOR="release-human", GITHUB_WORKFLOW_SHA=COMMIT,
                        RELEASE_APP_ID="70", RELEASE_APP_BOT_ID="700", RELEASE_APP_CLIENT_ID="Iv1.public",
                        RELEASE_BATCH_HUMAN_TEAM="release-humans",
                        GH_TOKEN="test-read-token-not-a-secret", RELEASE_APP_WRITE_TOKEN="test-write-token-not-a-secret",
                        RELEASE_SETTINGS_TOKEN="test-settings-token-not-a-secret", RELEASE_APP_JWT="test-app-jwt-not-a-secret",
                        MODULE_RELEASES_ENABLED="true", PYTHONPATH=str(self.root),
                        BUMP_TYPE="patch", VERSION="1.2.3", GITHUB_OUTPUT=str(self.root / "output"),
                        RUNNER_TEMP=str(self.root),
                        PYTHONDONTWRITEBYTECODE="1")
        self.env.pop("RELEASE_APP_PRIVATE_KEY", None)
        (self.root / "VERSION").write_text("1.2.3\n")

    def invoke(self, state, command, extra=None):
        self.state_path.write_text(json.dumps(state))
        context = dict(GITHUB_WORKFLOW_REF=REPOSITORY + "/" + state["run"]["path"] + "@refs/heads/main")
        environment = dict(self.env, **context)
        for name, value in (extra or {}).items():
            if value is None:
                environment.pop(name, None)
            else:
                environment[name] = value
        result = subprocess.run(["bash", "-c", command], env=environment, cwd=self.root,
                                text=True, capture_output=True, timeout=30)
        state.update(json.loads(self.state_path.read_text()))
        for name in ("GH_TOKEN", "RELEASE_APP_WRITE_TOKEN", "RELEASE_SETTINGS_TOKEN", "RELEASE_APP_JWT"):
            self.assertNotIn(self.env[name], result.stdout + result.stderr)
        return result

    def authorize(self, state, track, kind="tag", extra=None):
        command = f"python3 .github/scripts/release-app-guard.py {kind} --track {track}"
        result = self.invoke(state, command, extra)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(state["writes"])

    def git(self, *args):
        return subprocess.run(["git", *args], cwd=self.root, text=True, capture_output=True, check=True).stdout.strip()

    def sdk_release_steps(self):
        directory = self.root / "sdk/typescript"
        directory.mkdir(parents=True)
        preamble = "# Changelog\n\nNotable changes to `@stacklok-oss/mecatl-sdk` are recorded here.\n\nFor installation and API entry points, see the [TypeScript SDK README](./README.md).\n\n"
        (directory / "VERSION").write_text("1.2.2\n")
        (directory / "CHANGELOG.md").write_text(preamble)
        (directory / "package.json").write_text(json.dumps(dict(name="@stacklok-oss/mecatl-sdk", version="1.2.2")))
        self.git("init", "-q")
        self.git("config", "user.email", "test@example.invalid")
        self.git("config", "user.name", "Release Test")
        self.git("add", "sdk/typescript")
        self.git("commit", "-qm", "SDK baseline")
        self.git("tag", "sdk/typescript/v1.2.2")
        (directory / "README.md").write_text("SDK change\n")
        self.git("add", "sdk/typescript/README.md")
        self.git("commit", "-qm", "Add SDK capability")
        output = self.root / "output"
        output.write_text("")
        result = self.invoke(fixture("sdk"), step("create-sdk-typescript-release-pr.yml", "Resolve the next SDK version"))
        self.assertEqual(result.returncode, 0, result.stderr)
        values = dict(line.split("=", 1) for line in output.read_text().splitlines())
        self.assertEqual(values, dict(current="1.2.2", version="1.2.3", tag="sdk/typescript/v1.2.3", branch="release/sdk-typescript/v1.2.3"))
        for name in ("Verify the current release lineage", "Apply the SDK version bump", "Generate the SDK changelog entry", "Verify the release diff", "Prepare the release PR body"):
            result = self.invoke(fixture("sdk"), step("create-sdk-typescript-release-pr.yml", name),
                                 dict(CURRENT=values["current"], VERSION=values["version"], TAG=values["tag"]))
            self.assertEqual(result.returncode, 0, result.stderr)
        self.git("add", "sdk/typescript")
        self.git("commit", "-qm", "Release TypeScript SDK v1.2.3")
        return values, self.git("rev-parse", "HEAD")

    def test_real_checker_cli_and_schema2_controller_seam(self):
        state = fixture()
        command = "python3 .github/scripts/module-release-protection.py check --repository owner/repo --app-id 70 --bot-id 700"
        result = self.invoke(state, command)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout), dict(schema=2, repository=REPOSITORY, app_id=70, bot_id=700, protected=True))
        # The controller invokes the real checker subprocess and requires schema 2.
        driver = load("module-release-github")
        with patch.dict(os.environ, self.env), patch.object(driver, "HERE", self.root / ".github/scripts"):
            os.environ["MODULE_RELEASES_ENABLED"] = "true"
            driver.protection_check(REPOSITORY, 70, 700)
            self.assertNotIn("RELEASE_APP_JWT", os.environ)
            os.environ["RELEASE_APP_JWT"] = self.env["RELEASE_APP_JWT"]
            state["branches"][0]["name"] = "*"
            self.state_path.write_text(json.dumps(state))
            with self.assertRaises(driver.Error):
                driver.protection_check(REPOSITORY, 70, 700)
            old = dict(schema=1, repository=REPOSITORY, app_id=70, bot_id=700, protected=True)
            with patch.object(driver.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, json.dumps(old), "")):
                with self.assertRaises(driver.Error):
                    driver.protection_check(REPOSITORY, 70, 700)
        self.assertFalse(state["writes"])

    def test_actual_checker_rejects_static_protection_gaps(self):
        command = "python3 .github/scripts/module-release-protection.py check --repository owner/repo --app-id 70 --bot-id 700"

        def rule(state, suffix):
            return next(r for r in state["rules"] if r["name"] == "module-batch-" + suffix)

        def bypass(state, suffix):
            rule(state, suffix)["bypass_actors"] = copy.deepcopy(rule(state, "creation")["bypass_actors"])

        mutations = {
            "app-bypasses-review": lambda s: bypass(s, "review"),
            "app-bypasses-updates": lambda s: bypass(s, "updates"),
            "app-bypasses-safety": lambda s: bypass(s, "safety"),
            "wrong-creation-app": lambda s: rule(s, "creation")["bypass_actors"][0].update(actor_id=71),
            "human-creation": lambda s: rule(s, "creation")["bypass_actors"][0].update(actor_type="Team", actor_id=80),
            "direct-human-update": lambda s: rule(s, "updates")["bypass_actors"][0].update(bypass_mode="always"),
            "non-team-writer": lambda s: rule(s, "updates")["bypass_actors"][0].update(actor_type="RepositoryRole", actor_id=4),
            "no-approval": lambda s: rule(s, "review")["rules"][0]["parameters"].update(required_approving_review_count=0),
            "stale-approval": lambda s: rule(s, "review")["rules"][0]["parameters"].update(dismiss_stale_reviews_on_push=False),
            "not-squash-only": lambda s: rule(s, "review")["rules"][0]["parameters"].update(allowed_merge_methods=["merge", "squash"]),
            "unknown-merge-methods": lambda s: rule(s, "review")["rules"][0]["parameters"].pop("allowed_merge_methods"),
            "force-push": lambda s: rule(s, "safety").update(rules=[dict(type="deletion")]),
            "batch-deletion": lambda s: rule(s, "safety").update(rules=[dict(type="non_fast_forward")]),
            "partial-batch": lambda s: rule(s, "review")["conditions"]["ref_name"].update(include=["refs/heads/module-batch/42*"]),
            "batch-exclusion": lambda s: rule(s, "safety")["conditions"]["ref_name"].update(exclude=["refs/heads/module-batch/42-1"]),
            "state-missing-recursion": lambda s: rule(s, "state-creation")["conditions"]["ref_name"].update(include=["refs/heads/module-batch-state/*"]),
            "state-update-bypass": lambda s: bypass(s, "state-updates"),
            "state-permanent-bypass": lambda s: bypass(s, "state-permanent"),
            "state-candidate-deletion": lambda s: rule(s, "state-permanent")["conditions"]["ref_name"]["exclude"].append("refs/heads/module-batch-state/*/candidate"),
            "state-grant-deletion": lambda s: rule(s, "state-permanent")["conditions"]["ref_name"]["exclude"].append("refs/heads/module-batch-state/*/grant-*"),
            "state-reservation-deletion": lambda s: rule(s, "state-permanent")["conditions"]["ref_name"]["exclude"].append("refs/heads/module-batch-state/*/reservation"),
            "state-terminal-deletion": lambda s: rule(s, "state-permanent")["conditions"]["ref_name"]["exclude"].append("refs/heads/module-batch-state/*/terminal"),
            "active-human-deletion": lambda s: rule(s, "state-active")["bypass_actors"][0].update(actor_type="Team", actor_id=80),
            "hidden-state-bypasses": lambda s: rule(s, "state-updates").pop("bypass_actors"),
            "disabled-state-rule": lambda s: rule(s, "state-permanent").update(enforcement="disabled"),
            "merge-disabled": lambda s: s["repository"].update(allow_merge_commit=False),
            "merge-unreadable": lambda s: s["repository"].pop("allow_merge_commit"),
            "main-linear-ruleset": lambda s: s["main_rules"].append(dict(type="required_linear_history")),
            "main-linear-classic": lambda s: s.update(classic=dict(requiresLinearHistory=True)),
            "main-classic-unreadable": lambda s: s.update(graphql=dict(errors=[dict(message="denied")])),
            "main-classic-incomplete": lambda s: s.update(classic={}),
            "main-no-merge": lambda s: s["main_rules"].append(dict(type="pull_request", parameters=dict(allowed_merge_methods=["squash"]))),
            "main-methods-malformed": lambda s: s["main_rules"].append(dict(type="pull_request", parameters=dict(allowed_merge_methods="merge"))),
            "nonhuman-team": lambda s: s["team_members"].append(dict(type="Bot", id=700)),
            "team-not-writer": lambda s: s["team_access"]["permissions"].update(push=False),
            "empty-team": lambda s: s.update(team_members=[]),
            "main-policy-unreadable": lambda s: s.update(unavailable_paths=[BASE + "/rules/branches/main"]),
            "bridge-not-deployed": lambda s: s.update(unavailable_paths=[BASE + "/contents/.github/workflows/module-release-wakeup.yml"]),
        }
        for name, mutate in mutations.items():
            with self.subTest(name=name):
                state = fixture()
                mutate(state)
                result = self.invoke(state, command)
                self.assertNotEqual(result.returncode, 0, result.stdout)
                self.assertFalse(state["writes"])
        state = fixture()
        self.assertNotEqual(self.invoke(state, command, dict(RELEASE_BATCH_HUMAN_TEAM="")).returncode, 0)
        self.assertFalse(state["writes"])

    def test_actual_apply_adds_only_static_rules_and_is_human_only(self):
        command = "python3 .github/scripts/module-release-protection.py apply --repository owner/repo --app-id 70 --bot-id 700"
        state = fixture()
        state["rules"] = []
        state["repository"]["allow_merge_commit"] = False
        result = self.invoke(state, command)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(state["writes"])
        self.assertTrue(json.loads(result.stdout)["prerequisites"])
        result = self.invoke(state, command + " --confirm")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(state["writes"]), 10)
        self.assertTrue(all(method == "POST" and path == BASE + "/rulesets" for method, path, _ in state["writes"]))
        self.assertFalse(state["repository"]["allow_merge_commit"])
        for mutate in (lambda s: s.update(human=dict(type="Bot")), lambda s: s.update(admin=False)):
            state = fixture()
            state["rules"] = []
            mutate(state)
            self.assertNotEqual(self.invoke(state, command + " --confirm").returncode, 0)
            self.assertFalse(state["writes"])

    def test_actual_tag_decision_callers_same_sha_validate_provenance(self):
        for track, workflow in (("root", "create-release-tag.yml"),):
            command = step(workflow, "Decide whether to tag")
            for mutation in (None, "wrong-author", "fork", "wrong-merge", "bot-merger", "wrong-base", "wrong-app"):
                with self.subTest(track=track, mutation=mutation):
                    state = fixture(track, current=True)
                    if mutation == "wrong-author":
                        state["pr"]["user"]["id"] = 701
                    elif mutation == "fork":
                        state["pr"]["head"]["repo"].update(id=10, full_name="fork/repo")
                    elif mutation == "wrong-merge":
                        state["pr"]["merge_commit_sha"] = OTHER
                    elif mutation == "bot-merger":
                        state["pr"]["merged_by"]["type"] = "Bot"
                    elif mutation == "wrong-base":
                        state["pr"]["base"]["ref"] = "other"
                    elif mutation == "wrong-app":
                        state["app"]["id"] = 71
                    result = self.invoke(state, command, dict(TAG=g.refs(track, "1.2.3")[0]))
                    self.assertEqual(result.returncode == 0, mutation is None, result.stderr)
                    self.assertFalse(state["writes"])

    def test_sdk_workflow_steps_carry_manifest_changelog_and_lineage_to_decision_and_writer(self):
        values, commit = self.sdk_release_steps()
        state = fixture("sdk", current=True)
        state.update(commit=commit, previous_version="1.2.2")
        state["pr"]["merge_commit_sha"] = commit
        state["tags"] = [dict(ref="refs/tags/" + values["tag"], object=dict(type="commit", sha=commit))]
        output = self.root / "output"
        output.write_text("")
        result = self.invoke(state, step("create-sdk-typescript-release-tag.yml", "Read and validate the SDK version"))
        self.assertEqual(result.returncode, 0, result.stderr)
        tag_values = dict(line.split("=", 1) for line in output.read_text().splitlines())
        self.assertEqual(tag_values["version"], values["version"])
        result = self.invoke(state, step("create-sdk-typescript-release-tag.yml", "Decide whether to tag"),
                             dict(GITHUB_SHA=commit, SHA=commit, VERSION=tag_values["version"], TAG=tag_values["tag"]))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("should_tag=false", output.read_text())

        tag_writer = step("create-sdk-typescript-release-tag.yml", "Create and push the SDK tag")
        (self.root / "release-app-sdk-tag.json").unlink()
        result = self.invoke(state, tag_writer, dict(GITHUB_SHA=commit, SHA=commit, VERSION=tag_values["version"]))
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(state["writes"])

        tag_state = fixture("sdk")
        tag_state.update(commit=commit, previous_version="1.2.2")
        tag_state["pr"]["merge_commit_sha"] = commit
        output.write_text("")
        result = self.invoke(tag_state, step("create-sdk-typescript-release-tag.yml", "Read and validate the SDK version"))
        self.assertEqual(result.returncode, 0, result.stderr)
        tag_values = dict(line.split("=", 1) for line in output.read_text().splitlines())
        result = self.invoke(tag_state, step("create-sdk-typescript-release-tag.yml", "Decide whether to tag"),
                             dict(GITHUB_SHA=commit, SHA=commit, VERSION=tag_values["version"], TAG=tag_values["tag"]))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("should_tag=true", output.read_text())
        result = self.invoke(tag_state, tag_writer,
                             dict(GITHUB_SHA=commit, SHA=commit, VERSION=tag_values["version"]))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(tag_state["writes"][-1][2],
                         dict(ref="refs/tags/" + tag_values["tag"], sha=OTHER))
        annotation = next(write[2] for write in tag_state["writes"] if write[1].endswith("/git/tags"))
        self.assertEqual((annotation["tag"], annotation["type"], annotation["object"]),
                         (tag_values["tag"], "commit", commit))

        request = step("create-sdk-typescript-release-pr.yml", "Verify release request authority and protections")
        result = self.invoke(state, request, dict(GITHUB_SHA=commit, SHA=commit, GITHUB_WORKFLOW_SHA=commit, VERSION=values["version"]))
        self.assertEqual(result.returncode, 0, result.stderr)
        writer = step("create-sdk-typescript-release-pr.yml", "Push the version bump and open the PR")
        result = self.invoke(state, writer, dict(GITHUB_SHA=commit, SHA=commit, GITHUB_WORKFLOW_SHA=commit, VERSION=values["version"]))
        self.assertEqual(result.returncode, 0, result.stderr)
        tree = next(write[2] for write in state["writes"] if write[1].endswith("/git/trees"))
        self.assertEqual({entry["path"] for entry in tree["tree"]},
                         {"sdk/typescript/CHANGELOG.md", "sdk/typescript/VERSION", "sdk/typescript/package.json"})

        decision = step("create-sdk-typescript-release-tag.yml", "Decide whether to tag")
        for name, mutate in (
                ("wrong-scope", lambda: state["pr"]["head"].update(ref="release/v1.2.3")),
                ("invalid-source", lambda: state.update(previous_version="not-a-version")),
                ("unexpected-file", lambda: ((self.root / "outside-sdk.txt").write_text("wrong scope\n"), self.git("add", "outside-sdk.txt"), self.git("commit", "-qm", "Unexpected release file"))),
                ("missing-lineage-ref", lambda: self.git("tag", "-d", "sdk/typescript/v1.2.2")),
        ):
            with self.subTest(name=name):
                state["pr"]["head"].update(ref="release/sdk-typescript/v1.2.3")
                state["previous_version"] = "1.2.2"
                mutate()
                if name == "unexpected-file":
                    commit = self.git("rev-parse", "HEAD")
                    state["commit"] = commit
                    state["pr"]["merge_commit_sha"] = commit
                    state["tags"] = [dict(ref="refs/tags/" + values["tag"], object=dict(type="commit", sha=commit))]
                before = len(state["writes"])
                result = self.invoke(state, decision, dict(GITHUB_SHA=state["commit"], SHA=state["commit"], VERSION=values["version"], TAG=values["tag"]))
                self.assertNotEqual(result.returncode, 0, name)
                self.assertEqual(len(state["writes"]), before)

    def test_actual_tag_write_callers_derive_refs_never_accept_namespace_inputs(self):
        for track, workflow, name in (("root", "create-release-tag.yml", "Create and push the tag"),
                                      ("sdk", "create-sdk-typescript-release-tag.yml", "Create and push the SDK tag")):
            command = step(workflow, name)
            state = fixture(track)
            self.authorize(state, track)
            app_reads = list(state["app_reads"])
            result = self.invoke(state, command, dict(TAG="engine/v9.9.9", BRANCH="refs/tags/engine/v9.9.9", RELEASE_APP_JWT="", RELEASE_APP_PRIVATE_KEY=""))
            self.assertEqual(state["app_reads"], app_reads)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(state["writes"][-1][2]["ref"], "refs/tags/" + g.refs(track, "1.2.3")[0])
            for invalid in ("9.9.9", "../engine/v1.2.3", "1.2.3/../../engine/v1.2.3", "01.2.3", "1.2.3\nref=engine/v9.9.9", "1.2. 3", "--all"):
                state = fixture(track)
                result = self.invoke(state, command, dict(VERSION=invalid))
                self.assertNotEqual(result.returncode, 0, invalid)
                self.assertFalse(state["writes"])
            state = fixture(track, current=True)
            state["pr"]["user"]["id"] = 701
            self.assertNotEqual(self.invoke(state, command).returncode, 0)
            self.assertFalse(state["writes"])
            state = fixture(track, current=True)
            self.assertEqual(self.invoke(state, command).returncode, 0)
            self.assertFalse(state["writes"])

    def test_actual_request_callers_validate_version_dispatch_and_settings(self):
        for track, workflow in (("root", "create-release-pr.yml"), ("sdk", "create-sdk-typescript-release-pr.yml")):
            command = step(workflow, "Verify release request authority and protections")
            state = fixture(track)
            result = self.invoke(state, command)
            self.assertEqual(result.returncode, 0, result.stderr)
            for extra in (dict(BUMP_TYPE="patch\nrefs/tags/engine/v1.2.3"), dict(GITHUB_REF="refs/tags/main")):
                state = fixture(track)
                self.assertNotEqual(self.invoke(state, command, extra).returncode, 0)
                self.assertFalse(state["writes"])
            state = fixture(track)
            state["run"]["triggering_actor"]["type"] = "Bot"
            self.assertNotEqual(self.invoke(state, command).returncode, 0)
            self.assertFalse(state["writes"])
        command = step("create-release-pr.yml", "Verify release request authority and protections")
        for invalid in ("1.2. 3", "01.2.3", "1.2.3\nengine/v9.9.9", "engine/v1.2.3", "--all"):
            (self.root / "VERSION").write_text(invalid)
            state = fixture()
            self.assertNotEqual(self.invoke(state, command).returncode, 0)
            self.assertFalse(state["writes"])

    def test_actual_version_reader_rejects_root_ref_escape_and_internal_whitespace(self):
        command = step("create-release-tag.yml", "Read the version")
        for value, allowed in (("1.2.3\n", True), ("1.2. 3\n", False), ("01.2.3\n", False),
                               ("1.2.3/../../engine/v9.9.9", False), ("1.2.3\nrefs/tags/engine/v9.9.9", False)):
            (self.root / "VERSION").write_text(value)
            state = fixture()
            result = self.invoke(state, command)
            self.assertEqual(result.returncode == 0, allowed, result.stderr)
            self.assertFalse(state["writes"])

    def test_actual_sdk_pr_writer_cannot_write_module_refs_or_other_files(self):
        directory = self.root / "sdk/typescript"
        directory.mkdir(parents=True)
        (directory / "VERSION").write_text("1.2.3\n")
        (directory / "CHANGELOG.md").write_text("# Test release\n")
        (directory / "package.json").write_text(json.dumps(dict(name="@stacklok-oss/mecatl-sdk", version="1.2.3")))
        (self.root / "sdk-release-pr.md").write_text("Test-only PR body")
        command = step("create-sdk-typescript-release-pr.yml", "Push the version bump and open the PR")
        state = fixture("sdk")
        missing_preflight = self.invoke(state, command)
        self.assertNotEqual(missing_preflight.returncode, 0)
        self.assertFalse(state["writes"])
        request = step("create-sdk-typescript-release-pr.yml", "Verify release request authority and protections")
        preflight = self.invoke(state, request)
        self.assertEqual(preflight.returncode, 0, preflight.stderr)
        result = self.invoke(state, command, dict(BRANCH="main", TAG="engine/v9.9.9", RELEASE_APP_JWT="", RELEASE_APP_PRIVATE_KEY=""))
        self.assertEqual(result.returncode, 0, result.stderr)
        ref = next(w[2]["ref"] for w in state["writes"] if w[1].endswith("/git/refs"))
        self.assertEqual(ref, "refs/heads/release/sdk-typescript/v1.2.3")
        tree = next(w[2] for w in state["writes"] if w[1].endswith("/git/trees"))
        self.assertEqual({entry["path"] for entry in tree["tree"]},
                         {"sdk/typescript/" + name for name in ("VERSION", "CHANGELOG.md", "package.json")})
        for invalid in ("1.2.3/../../engine/v9.9.9", "01.2.3", "1.2.3\nrefs/tags/engine/v9.9.9"):
            state = fixture("sdk")
            self.assertNotEqual(self.invoke(state, command, dict(VERSION=invalid)).returncode, 0)
            self.assertFalse(state["writes"])
        state = fixture("sdk")
        state["run"]["triggering_actor"]["type"] = "Bot"
        self.assertNotEqual(self.invoke(state, command).returncode, 0)
        self.assertFalse(state["writes"])

    def test_existing_app_only_root_sdk_do_not_depend_on_module_rollout(self):
        settings = [BASE + "/rulesets", BASE + "/environments/release", BASE + "/actions/secrets", "/orgs/owner/actions/secrets", BASE + "/actions/runs/42"]
        sdk_values, sdk_commit = self.sdk_release_steps()
        disabled_values = (dict(MODULE_RELEASES_ENABLED=None, RELEASE_APP_ID="", RELEASE_APP_BOT_ID="", RELEASE_SETTINGS_TOKEN=""),
                           dict(MODULE_RELEASES_ENABLED="false", RELEASE_APP_ID="", RELEASE_APP_BOT_ID="", RELEASE_SETTINGS_TOKEN=""))
        for disabled in disabled_values:
            for track, request_workflow, tag_workflow in (("root", "create-release-pr.yml", "create-release-tag.yml"),
                                                         ("sdk", "create-sdk-typescript-release-pr.yml", "create-sdk-typescript-release-tag.yml")):
                state = fixture(track, current=True)
                extra = dict(disabled, TAG=g.refs(track, "1.2.3")[0])
                if track == "sdk":
                    state.update(commit=sdk_commit, previous_version="1.2.2")
                    state["pr"]["merge_commit_sha"] = sdk_commit
                    state["tags"] = [dict(ref="refs/tags/" + sdk_values["tag"], object=dict(type="commit", sha=sdk_commit))]
                    extra.update(GITHUB_SHA=sdk_commit, SHA=sdk_commit, GITHUB_WORKFLOW_SHA=sdk_commit,
                                 VERSION=sdk_values["version"], TAG=sdk_values["tag"])
                state.update(rules=[], environment_secrets=[], unavailable_paths=settings)
                request_command = step(request_workflow, "Verify release request authority and protections")
                result = self.invoke(state, request_command, extra)
                self.assertEqual(result.returncode, 0, result.stderr)
                result = self.invoke(state, step(tag_workflow, "Decide whether to tag"), extra)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertFalse(state["writes"])
                self.assertFalse(set(settings) & set(state["reads"]))
                self.assertIn(BASE + "/installation", state["app_reads"])
                state["tags"] = []
                name = "Create and push the tag" if track == "root" else "Create and push the SDK tag"
                result = self.invoke(state, step(tag_workflow, name), dict(extra, TAG="engine/v9.9.9", RELEASE_APP_JWT="", RELEASE_APP_PRIVATE_KEY=""))
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(state["writes"][-1][2]["ref"], "refs/tags/" + g.refs(track, "1.2.3")[0])
                state = fixture(track)
                state.update(rules=[], unavailable_paths=settings)
                result = self.invoke(state, request_command, dict(disabled, MODULE_RELEASES_ENABLED="true"))
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(state["writes"])
                self.assertIn("readable settings metadata", result.stderr)
        for change in (lambda s: s["installations"][0].update(suspended_at="today"),
                       lambda s: s["installations"][0]["permissions"].update(administration="write"),
                       lambda s: s["repository"].update(full_name="owner/other")):
            state = fixture()
            change(state)
            self.assertNotEqual(self.invoke(state, step("create-release-pr.yml", "Verify release request authority and protections"), disabled_values[1]).returncode, 0)
            self.assertFalse(state["writes"])

    def test_writer_requires_preflight_record(self):
        state = fixture()
        command = step("create-release-tag.yml", "Create and push the tag")
        self.assertNotEqual(self.invoke(state, command).returncode, 0)
        self.assertFalse(state["writes"])

    def test_short_lived_jwt_uses_openssl_without_key_or_token_files_or_argv(self):
        generated = subprocess.run(["openssl", "genpkey", "-algorithm", "RSA", "-pkeyopt", "rsa_keygen_bits:2048"], capture_output=True, env={"PATH": os.defpath})
        self.assertEqual(generated.returncode, 0)
        private = generated.stdout.decode()
        with patch.dict(os.environ, dict(self.env, RELEASE_APP_JWT="", RELEASE_APP_PRIVATE_KEY=private)), patch.object(p.time, "time", return_value=1700000000), patch.object(p.subprocess, "run", wraps=subprocess.run) as signing:
            token = p.app_jwt()
        command = signing.call_args.args[0]
        self.assertNotIn(private, " ".join(command))
        self.assertNotIn(token, " ".join(command))
        self.assertEqual(signing.call_args.kwargs["env"], {"PATH": os.defpath})
        self.assertEqual(signing.call_args.kwargs["input"], generated.stdout)
        header, claims, signature = token.split(".")
        decode = lambda value: base64.urlsafe_b64decode(value + "=" * (-len(value) % 4))
        self.assertEqual(json.loads(decode(header)), dict(alg="RS256", typ="JWT"))
        self.assertEqual(json.loads(decode(claims)), dict(iat=1699999940, exp=1700000300, iss="Iv1.public"))
        public = subprocess.run(["openssl", "pkey", "-pubout"], input=generated.stdout, capture_output=True, env={"PATH": os.defpath})
        self.assertEqual(public.returncode, 0)
        pub_read, pub_write = os.pipe()
        sig_read, sig_write = os.pipe()
        try:
            os.write(pub_write, public.stdout)
            os.write(sig_write, decode(signature))
            os.close(pub_write)
            os.close(sig_write)
            verified = subprocess.run(["openssl", "dgst", "-sha256", "-verify", f"/dev/fd/{pub_read}", "-signature", f"/dev/fd/{sig_read}"],
                                      input=(header + "." + claims).encode(), capture_output=True, pass_fds=(pub_read, sig_read), env={"PATH": os.defpath})
            self.assertEqual(verified.returncode, 0)
        finally:
            os.close(pub_read)
            os.close(sig_read)
        self.assertFalse(any("PRIVATE KEY" in path.read_text(errors="ignore") for path in self.root.glob("*.json")))
        with patch.dict(os.environ, RELEASE_APP_PRIVATE_KEY="invalid-key-not-a-secret", RELEASE_APP_CLIENT_ID="Iv1.public", RELEASE_APP_JWT=""):
            with self.assertRaises(p.Error) as error:
                p.app_jwt()
            self.assertNotIn("invalid-key-not-a-secret", str(error.exception))

    def test_transport_failure_diagnostics_do_not_echo_tokens(self):
        with patch.object(p.subprocess, "run", return_value=subprocess.CompletedProcess([], 1, "test-read-token-not-a-secret", "test-write-token-not-a-secret")):
            with self.assertRaises(p.Error) as error:
                p.GitHub().call("GET", BASE + "/rulesets", metadata=True)
        self.assertNotIn("test-read-token-not-a-secret", str(error.exception))
        self.assertNotIn("test-write-token-not-a-secret", str(error.exception))
        self.assertIn("optional RELEASE_SETTINGS_TOKEN", str(error.exception))
        with self.assertRaises(p.Error):
            p.GitHub().call("POST", BASE + "/rulesets", {}, metadata=True)
        with self.assertRaises(p.Error):
            p.GitHub().call("POST", "/graphql", dict(query="mutation { deleteRepository { clientMutationId } }"), metadata=True)
        with self.assertRaises(p.Error):
            p.GitHub().call("POST", BASE + "/installation/access_tokens", app=True)
        with patch.dict(os.environ, dict(self.env, RELEASE_APP_PRIVATE_KEY="test-private-key-not-a-secret")), patch.object(p.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, "{}", "")) as transport:
            p.GitHub().call("GET", BASE + "/rulesets", metadata=True)
            self.assertEqual(transport.call_args.kwargs["env"]["GH_TOKEN"], self.env["RELEASE_SETTINGS_TOKEN"])
            self.assertNotIn("RELEASE_APP_PRIVATE_KEY", transport.call_args.kwargs["env"])
            self.assertNotIn("RELEASE_APP_JWT", transport.call_args.kwargs["env"])
            os.environ["RELEASE_SETTINGS_TOKEN"] = ""
            p.GitHub().call("GET", BASE + "/rulesets", metadata=True)
            self.assertEqual(transport.call_args.kwargs["env"]["GH_TOKEN"], self.env["GH_TOKEN"])
            p.GitHub(self.env["RELEASE_APP_WRITE_TOKEN"]).call("POST", BASE + "/git/refs", {})
            self.assertEqual(transport.call_args.kwargs["env"]["GH_TOKEN"], self.env["RELEASE_APP_WRITE_TOKEN"])
        with patch.dict(os.environ, RELEASE_APP_JWT=self.env["RELEASE_APP_JWT"]), patch.object(p, "build_opener") as opener:
            opener.return_value.open.side_effect = HTTPError("https://api.github.com/app", 401, self.env["RELEASE_APP_JWT"], {}, None)
            with self.assertRaises(p.Error) as error:
                p.GitHub().call("GET", "/app", app=True)
            self.assertNotIn(self.env["RELEASE_APP_JWT"], str(error.exception))
            self.assertEqual(opener.return_value.open.call_args.args[0].get_header("Authorization"), "Bearer " + self.env["RELEASE_APP_JWT"])

    def test_every_shared_app_workflow_mints_after_guard(self):
        for name in p.WORKFLOWS:
            content = (ROOT / ".github/workflows" / name).read_text()
            if name == "module-release-wakeup.yml":
                for forbidden in ("actions/checkout@", "secrets.", "environment: release", "actions/create-github-app-token@"):
                    self.assertNotIn(forbidden, content)
                continue
            guard = "release-app-guard.py" if name.startswith("create-") else "module-release-github.py " + ("request-validate" if name.startswith("request-") else "validate-write")
            minted = 0
            for job in re.split(r"(?m)^  [A-Za-z0-9_-]+:\n", content):
                if "uses: actions/create-github-app-token@" not in job:
                    self.assertNotIn("secrets.RELEASE_APP_PRIVATE_KEY", job)
                    continue
                minted += 1
                self.assertLess(job.index(guard), job.index("uses: actions/create-github-app-token@"), name)
                self.assertIn("environment: release", job)
                self.assertIn("permission-contents: write", job)
                self.assertIn("repositories: ${{ github.event.repository.name }}", job)
                self.assertIn("owner: ${{ github.repository_owner }}", job)
                self.assertNotIn("permission-administration:", job)
                self.assertNotIn("permission-workflows:", job)
                self.assertNotIn("RELEASE_INSTALLATION_TOKEN", job)
                self.assertEqual(sum(line.strip().startswith("RELEASE_APP_PRIVATE_KEY:") for line in job.splitlines()), 1,
                                 "key environment must be confined to each protected preflight, not candidate/writer steps")
            expected = 0 if name in ("ci.yml", "module-release-wakeup.yml") else 2 if name == "module-releases.yml" else 1
            self.assertEqual(minted, expected, name)
            if expected == 0:
                self.assertNotRegex(content, r"\$\{\{\s*secrets\.")
                self.assertNotIn("environment: release", content)


if __name__ == "__main__":
    # In-process API fixtures run the real settings checks and dry-run/apply
    # decisions. Subprocess/workflow caller coverage remains in the full suite.
    quick = "--quick" in sys.argv
    if quick:
        sys.argv.remove("--quick")
    unittest.main(defaultTest="Protection" if quick else None)
