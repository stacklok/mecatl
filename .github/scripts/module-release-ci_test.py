#!/usr/bin/env python3
"""Offline caller coverage for the existing CI fanout and release resolution."""
from contextlib import redirect_stdout
import copy
import importlib.util
import io
import json
import multiprocessing
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
import textwrap
import time
import unittest
from unittest import mock

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]
sys.dont_write_bytecode = True


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


ci = load("release_ci", HERE / "module-release-ci.py")
controllers = load("ci_controller_fixtures", HERE / "module-release-github_test.py")
workflow = (ROOT / ".github/workflows/ci.yml").read_text()
blocks = dict(re.findall(r"^  ([\w-]+):\n(.*?)(?=^  [\w-]+:|\Z)", workflow.split("jobs:\n", 1)[1], re.M | re.S))


def publication_layout(f, dependency_version="v0.1.0"):
    """Production publication paths and canonical host replacements, tiny Go APIs."""
    NS, DEP = controllers.fixtures.NS, controllers.DEP
    support, driver, adapters = "internal/adaptersupport", "contracts/gen/go/mecatl/driver", "adapters"
    roles = {support: ("support", "support.go"), driver: ("generated", "driver.go"), adapters: ("public", "adapters.go")}
    for path, (role, smoke) in roles.items():
        f.policy["modules"][path] = dict(role=role, tag_prefix=path + "/", policy="policy.md", smoke=smoke)
        f.write(path + "/go.mod", f"module {NS}/{path}\n\ngo 1.27\n")
    f.write(support + "/go.mod", f"module {NS}/{support}\n\ngo 1.27\n\nrequire {NS}/{DEP} {dependency_version}\n")
    for package in ("filewatch", "soulbody"):
        f.write(support + "/" + package + "/fixture.go", f'package {package}\nimport "{NS}/{DEP}"\nfunc New() {{ ssefilter.New() }}\n')
    f.write(driver + "/v1/fixture.go", "package v1\ntype SessionSnapshot struct{}\n")
    f.write(adapters + "/go.mod", f"module {NS}/{adapters}\n\ngo 1.27\n\nrequire (\n{NS}/{support} v0.1.0\n{NS}/{driver} v0.1.0\n)\n")
    for package in ("grpcdriver", "jsonlstore", "redisstore"):
        f.write(adapters + "/" + package + "/fixture.go", f'package {package}\nimport "{NS}/{support}/filewatch"\nimport driver "{NS}/{driver}/v1"\nfunc New() {{ filewatch.New(); _ = driver.SessionSnapshot{{}} }}\n')
    for path, role in ((".", "application"), ("integration/microvm", "test")):
        f.policy["modules"][path] = dict(role=role)
        prefix, relative = ("", "./") if path == "." else (path + "/", "../../")
        identity = NS if path == "." else NS + "/" + path
        manifest = f"module {identity}\n\ngo 1.27\n\nrequire {NS}/{adapters} v0.1.0\n"
        manifest += "\n" + "\n".join(f"replace {NS}/{p} => {relative}{p}" for p in roles) + "\n"
        if path != ".":
            manifest += f"require {NS} v0.0.1\nreplace {NS} => ../..\n"
        f.write(prefix + "go.mod", manifest)
        f.write(prefix + "host.go", f'package host\nimport "{NS}/{adapters}/grpcdriver"\nvar _ = grpcdriver.New\n')
    f.write("go.work", "go 1.27\nuse (\n" + "".join(" ./" + p + "\n" for p in f.policy["modules"]) + ")\n")
    return roles


class Wiring(unittest.TestCase):
    def test_registry_enumerates_every_real_job_and_matrix(self):
        names = []
        for key, block in blocks.items():
            name = re.search(r"^    name: (.*)$", block, re.M)[1]
            if key == "test":
                name = "Test (race)"
            matrix = re.search(r"\$\{\{ matrix\.([\w-]+) \}\}", name)
            if matrix:
                field = matrix[1]
                values = re.search(r"^        " + field + r": \[(.*?)\]", block, re.M)
                values = [v.strip(" '\"") for v in values[1].split(",")] if values else re.findall(r"^            " + field + r": (.*)$", block, re.M)
                self.assertTrue(values, key)
                names.extend("Full release CI / " + name.replace(matrix[0], value) for value in values)
            else:
                names.append("Full release CI / " + name)
        registry = json.loads((HERE / "module-release-ci.json").read_text())
        self.assertEqual(registry["schema"], 2)
        self.assertCountEqual(registry["required_jobs"], names)
        self.assertEqual(len(registry["required_jobs"]), len(set(registry["required_jobs"])))

    def test_existing_suite_is_called_without_secrets_or_mutable_tooling(self):
        caller = (ROOT / ".github/workflows/module-releases.yml").read_text()
        fanout = caller.split("  full-ci:\n", 1)[1].split("  proof:\n", 1)[0]
        self.assertIn("uses: ./.github/workflows/ci.yml", fanout)
        self.assertNotIn("secrets:", fanout)
        self.assertNotIn("environment:", fanout)
        self.assertNotIn("write", fanout.split("  full-ci-receipt:", 1)[0])
        self.assertIn("module-release-ci.py receipt --input plan.json", fanout)
        self.assertIn("full-ci-receipt", caller.split("  continue:\n", 1)[1])
        self.assertIn("workflow_call:", workflow)
        self.assertNotIn("rerun-", workflow)
        self.assertNotIn("run_attempt > 1", workflow)
        self.assertIn("test:workspace:race", blocks["test"])
        self.assertIn("task --concurrency 3 test:race", blocks["test"])
        bootstrap = (ROOT / ".github/actions/module-release-ci-bootstrap/action.yml").read_text()
        self.assertIn("release-ci-context:", bootstrap)
        self.assertNotIn("GH_TOKEN", bootstrap)
        self.assertLess(bootstrap.index('git fetch --tags origin main "$T" "$source"'),
                        bootstrap.index('git archive "$T" .github/scripts'))
        self.assertLess(bootstrap.index('git archive "$T" .github/scripts'),
                        bootstrap.index('module-release-ci.py" install'))
        checkout_jobs = []
        for name, block in blocks.items():
            if "uses: actions/checkout@" in block and name != "changes":
                checkout_jobs.append(name)
                self.assertIn("uses: ./.github/actions/module-release-ci-bootstrap", block, name)
                self.assertIn("if: inputs.release-batch != '' || needs.changes.outputs.release_context != ''", block, name)
                self.assertIn("release-ci-context: ${{ needs.changes.outputs.release_context }}", block, name)
                self.assertIn("ref: ${{ needs.changes.outputs.release_context != '' && fromJSON(needs.changes.outputs.release_context).T || github.sha }}", block, name)
                self.assertLess(block.index("uses: actions/checkout@"),
                                block.index("uses: ./.github/actions/module-release-ci-bootstrap"), name)
            if "uses: actions/setup-go@" in block:
                self.assertIn("module-release-ci.py\" start", block, name)
        self.assertEqual(workflow.count("uses: ./.github/actions/module-release-ci-bootstrap"), len(checkout_jobs))
        self.assertNotIn('module-release-ci.py" install', workflow)
        self.assertIn('git archive "$T" .github/scripts', blocks["changes"])
        self.assertIn("public_history: ${{ steps.release.outputs.public_history }}", blocks["changes"])
        ordinary = blocks["build"].split("- name: Verify ordinary nested module publication", 1)[1].split("- name: Verify nested module publication", 1)[0]
        self.assertIn("needs.changes.outputs.public_history", ordinary)
        self.assertIn("ordinary-ci-publication", ordinary)
        self.assertIn('if ! git cat-file -e "$T:.github/scripts/module-release-ci.py"; then\n            task test:module-publication\n            exit 0\n          fi', ordinary)
        self.assertNotIn("GH_TOKEN", ordinary)
        self.assertNotIn("permissions:", blocks["build"])
        changes = blocks["changes"]
        self.assertIn('if ! git cat-file -e "$T:.github/scripts/module-release-ci.py"; then', changes)
        self.assertIn('[ -n "$RELEASE_BATCH" ] || { [ "$MODULE_RELEASES_ENABLED" = true ] && [ -n "$RELEASE_PR" ]; }', changes)
        self.assertLess(changes.index('git cat-file -e "$T:.github/scripts/module-release-ci.py"'), changes.index('git archive "$T" .github/scripts'))
        self.assertIn("module-adoption-${{ github.run_id }}-1", changes)
        self.assertIn("fromJSON(steps.release.outputs.context).kind == 'adoption'", changes)
        self.assertLess(changes.index("Preserve authenticated premerge adoption identities"), changes.index("Classify changed paths"))
        self.assertIn("steps.release.outputs.context", blocks["changes"])
        self.assertIn("if: inputs.release-batch != '' || github.event_name == 'pull_request' || github.event_name == 'push'", blocks["changes"])
        self.assertIn("RELEASE_BATCH_HUMAN_TEAM", caller)
        self.assertIn('"ci.yml"', (HERE / "module-release-protection.py").read_text())

    def test_environment_is_an_allowlist_not_a_secret_name_denylist(self):
        env = ci.candidate_environment(dict(PATH="/bin", GH_TOKEN="sentinel", RELEASE_SETTINGS_TOKEN="sentinel",
                                            SOMETHING_UNEXPECTED="sentinel", GITHUB_OUTPUT="/output", STAMP="v0.0.0"))
        self.assertEqual(env["PATH"], "/bin")
        self.assertEqual(env["GITHUB_OUTPUT"], "/output")
        self.assertEqual(env["STAMP"], "v0.0.0")
        self.assertNotIn("sentinel", env.values())


class Deployment(unittest.TestCase):
    def test_predeployment_workflow_callers_keep_public_proof_without_candidate_credentials(self):
        def step(job, name):
            block = blocks[job].split("- name: " + name, 1)[1].split("\n      - ", 1)[0]
            return re.search(r"        run: \|\n(.*)", block, re.S)[1].replace("\n          ", "\n").lstrip()

        changes = step("changes", "Authenticate release CI context with fixed main tooling")
        publication = step("build", "Verify ordinary nested module publication with fixed main tooling")
        with tempfile.TemporaryDirectory(prefix="module-ci-deployment-") as directory:
            temporary = Path(directory)
            env = controllers.fixtures.fixture_environment(temporary)
            with mock.patch.dict(os.environ, env, clear=True):
                f = controllers.fixtures.Fixture(temporary)
                roles = publication_layout(f)
                f.write(".github/scripts/old-tool.sh", "# Predeployment main has neither reader nor registry.\n")
                trusted = f.commit()
                for path in (controllers.DEP, *roles):
                    f.publish(path, "v0.1.0", trusted)
                origin = temporary / "origin.git"
                subprocess.run(["git", "clone", "--bare", str(f.root), str(origin)], env=env, check=True, capture_output=True)
                f.git("remote", "add", "origin", str(origin))
                leak, forged_reader = temporary / "credential-leak", temporary / "candidate-authority"
                guard = f'import os, pathlib\nif os.environ.get("GH_TOKEN"):\n pathlib.Path({str(leak)!r}).touch()\n raise RuntimeError("candidate received GH_TOKEN")\n'
                for name in ("module-release-ci.py", "module-release-github.py", "module-release.py", "module-candidate.py"):
                    f.write(".github/scripts/" + name, guard + (HERE / name).read_text())
                shutil.copytree(HERE / "module-consumers", f.root / ".github/scripts/module-consumers")
                f.write(".github/scripts/module-publication-policy.json", json.dumps(f.policy))
                candidate = f.commit()
                f.git("update-ref", "refs/remotes/origin/main", trusted)
                # A forged candidate registry/reader must not supply history in
                # the absence of the trusted-main implementation.
                forged = {controllers.DEP: {"v0.1.0": candidate}}
                f.write(".github/scripts/module-release-ci.json", json.dumps(dict(authority=forged)))
                reader = f.root / ".github/scripts/module-release-github.py"
                reader.write_text(guard + f'if __name__ == "__main__":\n pathlib.Path({str(forged_reader)!r}).touch()\n print({json.dumps(forged)!r})\n raise SystemExit(0)\n' + (HERE / reader.name).read_text())
                tools = temporary / "tools"
                tools.mkdir()
                task = tools / "task"
                # Only replace proxy transport with the offline fixture. Run the
                # production ordinary caller and Release validation unchanged.
                task.write_text(f'''#!{sys.executable}
import importlib.util, json, os, pathlib, sys
assert sys.argv[1:] == ["test:module-publication"]
assert not os.environ.get("GH_TOKEN")
pathlib.Path({str(temporary / "task-ran")!r}).touch()
spec = importlib.util.spec_from_file_location("candidate_ci", ".github/scripts/module-release-ci.py")
ci = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ci)
ci.release.public_environment = lambda _: json.loads({json.dumps(f.env)!r})
sys.argv = ["module-release-ci.py", "ordinary-publication"]
raise SystemExit(ci.main())
''')
                task.chmod(0o755)
                gh = tools / "gh"
                gh.write_text('#!/bin/sh\n[ "$1 $2" = "auth token" ] || exit 1\nprintf "%s\\n" offline-read-token\n')
                gh.chmod(0o755)
                output = temporary / "output"
                run_env = dict(env, PATH=str(tools) + os.pathsep + env["PATH"], RUNNER_TEMP=str(temporary),
                               GITHUB_OUTPUT=str(output), GITHUB_SHA=trusted, MODULE_RELEASES_ENABLED="false",
                               RELEASE_PR="123", RELEASE_BATCH="", PUBLICATION_CI_CONTEXT="")
                def run(script, **extra):
                    return subprocess.run(["bash", "--noprofile", "--norc", "-eo", "pipefail", "-c", script],
                                          cwd=f.root, env=dict(run_env, **extra), text=True, capture_output=True)
                result = run(changes, GH_TOKEN="offline-changes-token")
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(output.read_text(), "context=\npublic_history=\n")
                self.assertFalse((temporary / "module-ci-classifier").exists())
                result = run(publication)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertTrue((temporary / "task-ran").exists())
                f.git("tag", "-f", controllers.DEP + "/v0.1.0", candidate)
                tag = "refs/tags/" + controllers.DEP + "/v0.1.0"
                subprocess.run(["git", "-C", str(origin), "fetch", "--no-tags", str(f.root), "+" + tag + ":" + tag],
                               env=env, check=True, capture_output=True)
                result = run(publication)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("cannot authenticate historical publication provenance", result.stderr)
                self.assertFalse(leak.exists())
                self.assertFalse(forged_reader.exists())
                for required in (dict(RELEASE_BATCH="explicit-batch"), dict(MODULE_RELEASES_ENABLED="true")):
                    with self.subTest(required=required):
                        output.write_text("")
                        result = run(changes, GH_TOKEN="offline-changes-token", **required)
                        self.assertNotEqual(result.returncode, 0)
                        self.assertIn("Trusted main has no release CI reader", result.stderr)
                        self.assertEqual(output.read_text(), "")
                self.assertFalse(leak.exists())
                # Once deployed, the fixed-main reader runs even with publication
                # disabled; only its authenticated history output may reach Build.
                f.write(".github/scripts/module-release-ci.py", '''import os
assert os.environ["GH_TOKEN"] == "offline-changes-token"
assert os.environ["MODULE_RELEASES_ENABLED"] == "false"
with open(os.environ["GITHUB_OUTPUT"], "a") as output:
    output.write('context=\\npublic_history={"trusted":true}\\n')
''')
                f.commit()
                subprocess.run(["git", "-C", str(origin), "fetch", "--no-tags", str(f.root), "main:main"],
                               env=env, check=True, capture_output=True)
                f.write(".github/scripts/module-release-ci.py", guard + 'raise RuntimeError("candidate reader executed")\n')
                output.write_text("")
                result = run(changes, GH_TOKEN="offline-changes-token")
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(output.read_text(), 'context=\npublic_history={"trusted":true}\n')
                self.assertFalse(leak.exists())


class Context(unittest.TestCase):
    def setUp(self):
        self.f = controllers.Controller(methodName="runTest")
        self.f.setUp()
        self.addCleanup(self.f.doCleanups)

    def pr_run(self, head):
        run = self.f.api.run(900, head)
        run.update(event="pull_request", workflow_id=99, path=".github/workflows/ci.yml")
        original = self.f.api.call
        def call(method, path, *args, **kwargs):
            if path == "/actions/workflows/ci.yml":
                return dict(id=99)
            return original(method, path, *args, **kwargs)
        return run, mock.patch.object(self.f.api, "call", side_effect=call)

    def test_verified_preparation_and_forged_identity_negative_controls(self):
        pr = self.f.prepared()
        run, api = self.pr_run(pr["head"]["sha"])
        with api:
            value = ci.context(self.f.driver(run), run, number=1, head=pr["head"]["sha"])
            self.assertEqual(value["source"], pr["head"]["sha"])
            self.assertEqual(value["batch"], self.f.batch)
            self.assertEqual(value["T"], self.f.B)
            with self.assertRaisesRegex(Exception, "PR CI workflow identity"):
                ci.context(self.f.driver(run), run, number=1, head="a" * 40)
            pr["user"]["id"] += 1
            with self.assertRaisesRegex(Exception, "App"):
                ci.context(self.f.driver(run), run, number=1, head=pr["head"]["sha"])

    def test_full_candidate_plan_and_stale_plan_are_not_flags(self):
        self.f.candidate()
        driver, run, plan = self.f.next()
        value = ci.context(driver, run, plan=plan)
        self.assertEqual(value["source"], self.f.R)
        plan["action"]["binding"]["R"] = "a" * 40
        with self.assertRaisesRegex(Exception, "authority changed"):
            ci.context(driver, run, plan=plan)

    def test_adoption_needs_complete_proofs_and_actual_prospective_merge(self):
        self.f.candidate()
        self.f.qualification()
        self.f.publish_one()
        self.f.publish_one()
        driver, run, plan = self.f.next()
        controllers.gh.Writer(driver).apply(driver.validate_write(plan, {}, run))
        self.f.api.finish(run, plan)
        pr = self.f.api.prs[2]
        head = pr["head"]["sha"]
        merge = controllers.git(self.f.f.root, "commit-tree", controllers.git(self.f.f.root, "rev-parse", head + "^{tree}"),
                                "-p", pr["base"]["sha"], "-p", head, "-m", "prospective integration")
        run, api = self.pr_run(head)
        with api:
            value = ci.context(self.f.driver(run), run, number=2, head=head, merge=merge)
            self.assertEqual(value["kind"], "adoption")
            self.assertEqual(value["source"], merge)
            self.assertIsNone(value["expected"])
            with self.assertRaisesRegex(Exception, "prospective"):
                ci.context(self.f.driver(run), run, number=2, head=head, merge=head)
            artifact = next(a for a in self.f.api.artifacts.values() if a["name"].startswith("module-full-ci-"))
            artifact["expired"] = True
            with self.assertRaisesRegex(Exception, "complete public proof"):
                ci.context(self.f.driver(run), run, number=2, head=head, merge=merge)

    def test_adoption_keeps_abandoned_unselected_public_grants_not_arbitrary_tags(self):
        f = self.f
        f.candidate()
        old_R = f.R
        f.qualification()
        f.publish_one()  # DEP only; abandon before APP is tagged.
        driver, run, _ = f.next()
        plan = driver.envelope(driver.plan(run, "abandon", f.record["id"]), run)
        controllers.gh.Writer(driver).apply(driver.validate_write(plan, {}, run))
        f.api.finish(run, plan)
        f.batch = {controllers.APP: "v0.3.0"}
        intake = f.api.run(1000, f.B, request=True)
        f.record = f.driver(intake).intake(f.batch, f.B, intake)
        controllers.gh.Writer(f.driver(intake)).reserve(f.record)
        f.api.finish(intake, dict(action=dict(kind="intake", id=f.record["id"])))
        f.candidate()
        f.qualification()
        f.publish_one()
        driver, run, plan = f.next()
        controllers.gh.Writer(driver).apply(driver.validate_write(plan, {}, run))
        f.api.finish(run, plan)
        pr = f.api.prs[max(f.api.prs)]
        head = pr["head"]["sha"]
        merge = controllers.git(f.f.root, "commit-tree", controllers.git(f.f.root, "rev-parse", head + "^{tree}"),
                                "-p", pr["base"]["sha"], "-p", head, "-m", "later batch prospective merge")
        run, api = self.pr_run(head)
        with api:
            value = ci.context(f.driver(run), run, number=pr["number"], head=head, merge=merge)
        self.assertEqual(value["authority"][controllers.DEP]["v0.2.0"], old_R)
        self.assertEqual(value["authority"][controllers.APP]["v0.3.0"], f.R)
        with f.driver(run).check(merge, value["authority"]) as check:
            self.assertEqual(check.prerequisite(controllers.fixtures.NS + "/" + controllers.DEP, "v0.2.0"), old_R)
            controllers.git(f.f.root, "tag", controllers.DEP + "/v0.9.0", old_R)
            with self.assertRaisesRegex(Exception, "not on fetched origin/main"):
                check.prerequisite(controllers.fixtures.NS + "/" + controllers.DEP, "v0.9.0")
        controllers.git(f.f.root, "tag", "-d", controllers.DEP + "/v0.9.0")
        f.adoption_ci(pr)
        adopted = f.api.merge(pr, "merge")
        driver, run, plan = f.next()
        self.assertEqual(plan["action"]["kind"], "complete")
        controllers.gh.Writer(driver).apply(driver.validate_write(plan, {}, run))
        f.api.finish(run, plan)
        self.assertIsNone(driver.active()[1])
        ordinary_run, api = self.pr_run(adopted)
        with api:
            self.assertIsNone(ci.context(f.driver(ordinary_run), ordinary_run, number=999, head=adopted))
        controllers.git(f.f.root, "checkout", "--detach", adopted)
        controllers.git(f.f.root, "update-ref", "refs/remotes/origin/main", adopted)
        # The selected R is now on main, but the abandoned prerequisite is not.
        with self.assertRaises(Exception):
            driver.ancestor(old_R)
        real_release = ci.release.Release
        def check(root, tree, sha, **kwargs):
            return real_release(root, tree, sha, f.f.policy, f.f.env, **kwargs)
        batch = f.directory / "preview.json"
        batch.write_text(json.dumps({controllers.DEP: "v0.3.0"}))
        with mock.patch.object(ci.release, "Release", side_effect=check), \
                mock.patch.object(ci.release, "historical_authority", side_effect=lambda _root: driver.authority()), \
                mock.patch.object(ci.release, "public_environment", return_value=f.f.env):
            # Exercise the local public CLI, not just an injected Release object.
            with redirect_stdout(io.StringIO()) as output:
                code = ci.release.main(["preview", "--root", str(f.f.root), "--candidate", adopted, "--batch", str(batch)])
            self.assertEqual(code, 0)
            self.assertEqual(json.loads(output.getvalue())["releases"][0]["latest"], "v0.2.0")
            controllers.git(f.f.root, "tag", controllers.DEP + "/v0.9.0", old_R)
            with mock.patch("sys.stderr", new_callable=io.StringIO) as errors, redirect_stdout(io.StringIO()):
                code = ci.release.main(["preview", "--root", str(f.f.root), "--candidate", adopted, "--batch", str(batch)])
            self.assertEqual(code, 1)
            self.assertIn("not on fetched origin/main", errors.getvalue())
            controllers.git(f.f.root, "tag", "-d", controllers.DEP + "/v0.9.0")
            # Normal post-adoption development adds a main-public adapters layout
            # that transitively consumes the abandoned public dependency.
            roles = publication_layout(f.f, dependency_version="v0.2.0")
            main = f.f.commit()
            original_git = f.f.git
            def existing_tag(*args):
                if args[:1] == ("tag",):
                    return ""  # These controller-created immutable tags already exist.
                return original_git(*args)
            with mock.patch.object(f.f, "git", side_effect=existing_tag):
                f.f.publish(controllers.DEP, "v0.2.0", old_R)
            for path in roles:
                f.f.publish(path, "v0.1.0", main)
            loader = mock.Mock(side_effect=lambda _root: driver.authority())
            with mock.patch.object(ci.release, "historical_authority", loader):
                ci.ordinary_publication(f.f.root)
            loader.assert_called_once()
            controllers.git(f.f.root, "update-ref", "refs/heads/main", main)
            history = ci.public_history(driver)
            self.assertIn("authority", history)
            with mock.patch.dict(os.environ, PUBLICATION_CI_CONTEXT=json.dumps(history)), \
                    mock.patch.object(sys, "argv", ["module-release-ci.py", "ordinary-ci-publication", "--root", str(f.f.root)]), \
                    mock.patch.object(ci.release, "historical_authority", side_effect=AssertionError("CI must not need local credentials")):
                self.assertEqual(ci.main(), 0)
            with mock.patch.object(driver, "authority", side_effect=ci.release.CheckError("read permission unavailable")):
                unavailable = ci.public_history(driver)
            with mock.patch.dict(os.environ, PUBLICATION_CI_CONTEXT=json.dumps(unavailable)), \
                    mock.patch.object(sys, "argv", ["module-release-ci.py", "ordinary-ci-publication", "--root", str(f.f.root)]), \
                    mock.patch("sys.stderr", new_callable=io.StringIO) as errors:
                self.assertEqual(ci.main(), 1)
            self.assertIn("historical publication provenance unavailable", errors.getvalue())
            # No active context or staged proxy is needed; moving a granted
            # version to an unrelated off-main target is still rejected.
            forged = controllers.git(f.f.root, "commit-tree", controllers.git(f.f.root, "rev-parse", old_R + "^{tree}"),
                                     "-p", old_R, "-m", "forged off-main publication")
            controllers.git(f.f.root, "tag", "-f", controllers.DEP + "/v0.2.0", forged)
            with self.assertRaisesRegex(Exception, "release authority conflict"):
                ci.ordinary_publication(f.f.root, driver.authority)

    def test_history_CI_output_is_independent_of_active_batch_and_publication_enablement(self):
        f = self.f
        f.candidate()
        f.qualification()
        f.publish_one()
        driver, run, _ = f.next()
        plan = driver.envelope(driver.plan(run, "abandon", f.record["id"]), run)
        controllers.gh.Writer(driver).apply(driver.validate_write(plan, {}, run))
        output = f.directory / "ci-output"
        env = dict(GITHUB_REPOSITORY=f.api.repository, GITHUB_RUN_ID="2001", GITHUB_RUN_ATTEMPT="1",
                   GITHUB_OUTPUT=str(output), RELEASE_APP_ID="70", RELEASE_APP_BOT_ID="700",
                   MODULE_RELEASES_ENABLED="false", RELEASE_PR="999")
        with mock.patch.dict(os.environ, env), mock.patch.object(ci.gh, "GitHub", return_value=f.api), \
                mock.patch.object(sys, "argv", ["module-release-ci.py", "context", "--root", str(f.f.root)]):
            self.assertEqual(ci.main(), 0)
        values = dict(line.split("=", 1) for line in output.read_text().splitlines())
        self.assertEqual(values["context"], "")
        self.assertEqual(json.loads(values["public_history"])["authority"], {controllers.DEP: {"v0.2.0": f.R}})
        output.write_text("")
        env.update(RELEASE_APP_ID="", RELEASE_APP_BOT_ID="")
        with mock.patch.dict(os.environ, env), mock.patch.object(ci.gh, "GitHub", return_value=f.api), \
                mock.patch.object(sys, "argv", ["module-release-ci.py", "context", "--root", str(f.f.root)]):
            self.assertEqual(ci.main(), 0)
        values = dict(line.split("=", 1) for line in output.read_text().splitlines())
        self.assertEqual(values["context"], "")
        self.assertIn("error", json.loads(values["public_history"]))

    def test_local_history_transport_executes_fetched_main_reader_not_candidate(self):
        f = self.f
        f.candidate()
        f.qualification()
        f.publish_one()
        for name in ("module-release-github.py", "module-release.py", "module-candidate.py"):
            f.f.write(".github/scripts/" + name, (HERE / name).read_text())
        trusted = f.f.commit()
        controllers.git(f.f.root, "update-ref", "refs/heads/main", trusted)
        f.f.write(".github/scripts/module-release-github.py", 'raise RuntimeError("candidate reader executed")\n')
        f.f.commit()
        controllers.git(f.f.root, "update-ref", "refs/remotes/origin/main", trusted)
        actual_run, actual_call = subprocess.run, f.api.call
        def api(method, path, *args, **kwargs):
            if path == "/actions/variables/RELEASE_APP_ID":
                return dict(value="70")
            if path == "/actions/variables/RELEASE_APP_BOT_ID":
                return dict(value="700")
            return actual_call(method, path, *args, **kwargs)
        def transport(args, *positional, **kwargs):
            if args[:3] == ["gh", "auth", "token"]:
                return subprocess.CompletedProcess(args, 0, stdout="offline-gh-auth-token\n", stderr="")
            if args[0] == sys.executable:
                # Run the actual extracted reader with an authenticated fake API;
                # only the transport is replaced, not provenance validation.
                reader = load("extracted_history_reader", Path(args[1]))
                with mock.patch.object(reader, "GitHub", return_value=f.api), \
                        mock.patch.dict(os.environ, kwargs["env"], clear=True), \
                        mock.patch.object(sys, "argv", args[1:]), \
                        redirect_stdout(io.StringIO()) as output, mock.patch("sys.stderr", new_callable=io.StringIO) as errors:
                    code = reader.main()
                return subprocess.CompletedProcess(args, code, stdout=output.getvalue(), stderr=errors.getvalue())
            return actual_run(args, *positional, **kwargs)
        for token in ("offline-explicit-token", ""):
            with self.subTest(transport="explicit" if token else "gh auth"), \
                    mock.patch.dict(os.environ, GH_TOKEN=token, RELEASE_APP_ID="999", RELEASE_APP_BOT_ID="999"), \
                    mock.patch.object(f.api, "call", side_effect=api), \
                    mock.patch.object(ci.release.subprocess, "run", side_effect=transport):
                self.assertEqual(ci.release.historical_authority(f.f.root), {controllers.DEP: {"v0.2.0": f.R}})

    def test_installed_shell_hook_uses_T_not_candidate_tools(self):
        fixture = self.f.f
        for name in ("module-release-ci.py", "module-release-github.py", "module-release.py", "module-candidate.py"):
            fixture.write(".github/scripts/" + name, (HERE / name).read_text())
        fixture.write("go.mod", "module example.com/controller\n\ngo 1.27\n")
        fixture.write("go.sum", "")
        trusted = fixture.commit()
        fixture.write(".github/scripts/module-release-ci.py", 'raise RuntimeError("candidate tooling executed")\n')
        source = fixture.commit()
        directory = self.f.directory / "installed-caller"
        output = self.f.directory / "job-env"
        with mock.patch.dict(os.environ, GITHUB_ENV=str(output)):
            ci.install(fixture.root, dict(schema=2, T=trusted, source=source), directory)
        env = dict(os.environ, **dict(line.split("=", 1) for line in output.read_text().splitlines()),
                   GH_TOKEN="sentinel", UNKNOWN_CONTROLLER_SECRET="sentinel")
        script = self.f.directory / "candidate-step.sh"
        script.write_text("python3 -c 'import os; assert not any(os.getenv(k) for k in (\"GH_TOKEN\", \"UNKNOWN_CONTROLLER_SECRET\")); print(\"clean\")'\n")
        result = subprocess.run(["bash", "--noprofile", "--norc", "-e", str(script)], cwd=fixture.root,
                                env=env, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(result.stdout, "clean\n")
        self.assertEqual(controllers.git(fixture.root, "rev-parse", "HEAD"), source)

    def test_bootstrap_action_loads_from_T_before_installer_selects_candidate(self):
        fixture = self.f.f
        action = ".github/actions/module-release-ci-bootstrap/action.yml"
        fixture.write(action, (ROOT / action).read_text())
        fixture.write(".github/scripts/module-release-ci.py", '''import json, os, pathlib, subprocess, sys
assert sys.argv[1] == "install"
context = json.loads(os.environ["RELEASE_CI_CONTEXT"])
pathlib.Path(os.environ["HELPER_MARKER"]).write_text("trusted helper ran")
subprocess.run(["git", "checkout", "--detach", context["source"]], check=True)
''')
        trusted = fixture.commit()
        origin = self.f.directory / "bootstrap-origin.git"
        subprocess.run(["git", "clone", "--bare", str(fixture.root), str(origin)], check=True, capture_output=True)
        fixture.git("remote", "add", "origin", str(origin))
        fixture.git("rm", action)
        missing = fixture.commit()
        fixture.git("checkout", "--detach", trusted)
        hostile = self.f.directory / "hostile-composite-ran"
        fixture.write(action, f'''name: Hostile candidate action
runs:
  using: composite
  steps:
    - shell: bash
      run: touch {hostile}
''')
        hostile_source = fixture.commit()
        fixture.git("push", "origin", f"{missing}:refs/heads/missing-action")
        fixture.git("push", "origin", f"{hostile_source}:refs/heads/main")

        # This is the release-context checkout. Read and execute the composite
        # only after it has selected T, rather than from a missing or hostile
        # candidate action. The trusted helper then selects the candidate.
        for label, source in (("missing", missing), ("hostile", hostile_source)):
            with self.subTest(candidate_action=label):
                fixture.git("checkout", "--detach", trusted)
                bootstrap = (fixture.root / action).read_text()
                script = textwrap.dedent(bootstrap.split("      run: |\n", 1)[1])
                runner = self.f.directory / label
                runner.mkdir()
                marker = runner / "trusted-helper-ran"
                environment = dict(PATH=os.environ["PATH"], HOME=os.environ.get("HOME", ""),
                                   RUNNER_TEMP=str(runner), HELPER_MARKER=str(marker),
                                   RELEASE_CI_CONTEXT=json.dumps(dict(schema=2, T=trusted, source=source)))
                result = subprocess.run(["bash", "--noprofile", "--norc", "-eo", "pipefail", "-c", script],
                                        cwd=fixture.root, env=environment, text=True, capture_output=True)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertEqual(marker.read_text(), "trusted helper ran")
                self.assertFalse(hostile.exists())
                self.assertEqual(controllers.git(fixture.root, "rev-parse", "HEAD"), source)

    def test_merged_PR_base_api_tip_is_not_original_B_graph_authority(self):
        self.f.candidate()
        self.f.api.prs[1]["base"]["sha"] = self.f.R
        candidate, _ = self.f.driver(self.f.intake_run).candidate(self.f.record)
        self.assertEqual(candidate["R"], self.f.R)

    def test_historical_T_survives_ordinary_main_advance(self):
        self.f.candidate()
        self.f.qualification()
        self.f.publish_one()
        self.f.f.write("ordinary.go", "package ordinary\n")
        self.f.B = self.f.f.commit()
        self.f.f.git("update-ref", "refs/remotes/origin/main", self.f.B)
        self.f.f.git("update-ref", "refs/heads/main", self.f.B)
        driver, run, plan = self.f.next()
        self.assertEqual(plan["action"]["kind"], "tag", driver.history_error)
        self.assertNotEqual(plan["action"]["binding"]["T"], self.f.record["T"])


class Execution(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.batch = load("ci_batch_fixtures", HERE / "module-release-batch_test.py")
        cls.batch.artifacts.ArtifactTests.setUpClass.__func__(cls)

    @classmethod
    def tearDownClass(cls):
        cls.tools.cleanup()

    def test_real_publication_caller_staged_preparation_then_public_adoption(self):
        fixture = self.batch.BatchTests(methodName="runTest")
        fixture.helper = self.helper
        fixture.setUp()
        self.addCleanup(fixture.doCleanups)
        f, release = fixture.f, self.batch.release
        NS, DEP = self.batch.NS, self.batch.DEP
        support, driver, adapters = "internal/adaptersupport", "contracts/gen/go/mecatl/driver", "adapters"
        f.write(DEP + "/fixture.go", '''package ssefilter
import "os"
func New() {}
func init() {
 for _, key := range []string{"GH_TOKEN", "GITHUB_TOKEN", "UNKNOWN_CONTROLLER_SECRET"} {
  if os.Getenv(key) != "" { panic("publication credential leaked: " + key) }
 }
}
''')
        old = f.commit()
        f.publish(DEP, "v0.1.0", old)
        roles = publication_layout(f)
        base = f.commit()
        batch = {p: "v0.2.0" for p in roles}
        prepared, report = fixture.prepare(base, batch)
        source = fixture.install(prepared)
        f.git("update-ref", "refs/remotes/origin/main", base)
        directory = fixture.temp / "publication-job"
        directory.mkdir()
        authority = {p: {v: source} for p, v in batch.items()}
        value = dict(schema=2, kind="preparation", source=source, batch=batch, expected=report["artifacts"], authority=authority)
        (directory / "context.json").write_text(json.dumps(value))
        real_release = ci.release.Release
        def check(root, tree, sha, **kwargs):
            return real_release(root, tree, sha, f.policy, f.env, **kwargs)
        with mock.patch.object(ci.release, "Release", side_effect=check), \
                mock.patch.object(ci.release, "public_environment", return_value=f.env):
            with fixture.open(source, authority) as staged:
                proxy = fixture.temp / "publication-stage"
                staged.stage(batch, self.helper, proxy, report["artifacts"])
                with staged.staged_environment(batch, proxy, f.env) as env:
                    (directory / "environment.json").write_text(json.dumps(env))
                    ci.publication(f.root, directory)
                    # Canonical host replacements are legitimate; a mismatched
                    # host pin or artifact checksum is not hidden by staging.
                    integration = f.root / "integration/microvm/go.mod"
                    original = integration.read_text()
                    integration.write_text(original.replace("adapters v0.2.0", "adapters v0.9.0"))
                    with self.assertRaisesRegex(Exception, "pin the same"):
                        ci.publication(f.root, directory)
                    integration.write_text(original)
                    bad = copy.deepcopy(value)
                    bad["expected"][support]["sum"] = "h1:corrupt"
                    (directory / "context.json").write_text(json.dumps(bad))
                    with self.assertRaisesRegex(Exception, "checksum mismatch"):
                        ci.publication(f.root, directory)
                    (directory / "context.json").write_text(json.dumps(value))
            # PR2 has only actual public versions. No staged server/environment
            # survives; the exact same production caller proves the public closure.
            shutil.copytree(proxy, f.proxy, dirs_exist_ok=True)
            for path, version in batch.items():
                f.git("tag", path + "/" + version, source)
            value.update(kind="adoption", expected=None)
            (directory / "context.json").write_text(json.dumps(value))
            (directory / "environment.json").write_text(json.dumps(f.env))
            shutil.rmtree(proxy)
            ci.publication(f.root, directory)
            ci.ordinary_publication(f.root, authority_loader=lambda: authority)
            # Fully main-public normal checks never authenticate historical state,
            # even if no token, App configuration or usable history is available.
            f.git("update-ref", "refs/remotes/origin/main", source)
            with mock.patch.dict(os.environ, PUBLICATION_CI_CONTEXT="", GH_TOKEN="sentinel-not-for-go"), \
                    mock.patch.object(sys, "argv", ["module-release-ci.py", "ordinary-ci-publication", "--root", str(f.root)]), \
                    mock.patch.object(ci.release, "historical_authority", side_effect=AssertionError("unneeded authentication")):
                self.assertEqual(ci.main(), 0)
            f.git("update-ref", "refs/remotes/origin/main", base)
            # Existing unselected prerequisites still need independent ancestry,
            # even after every selected member has passed public verification.
            f.git("tag", "-f", DEP + "/v0.1.0", source)
            with self.assertRaisesRegex(Exception, "not on fetched origin/main"):
                ci.publication(f.root, directory)

    def test_actual_shell_caller_keeps_staging_alive_across_steps_and_scrubs_credentials(self):
        fixture = self.batch.BatchTests(methodName="runTest")
        fixture.helper = self.helper
        fixture.setUp()
        self.addCleanup(fixture.doCleanups)
        source = fixture.candidate(hostile=True)
        prepared, report = fixture.prepare(source)
        source = fixture.install(prepared)
        directory = fixture.temp / "ci-job"
        directory.mkdir()
        (directory / "trusted").mkdir()
        value = dict(schema=2, kind="preparation", T=source, source=source, batch=self.batch.BATCH,
                     expected=report["artifacts"], authority={p: {v: source} for p, v in self.batch.BATCH.items()})
        (directory / "context.json").write_text(json.dumps(value))
        actual_run = ci.release.run
        def build(root, *args, **kwargs):
            if args[:2] == ("go", "build"):
                shutil.copy2(self.helper, directory / "module-artifact")
                return ""
            return actual_run(root, *args, **kwargs)
        def check(root, source, sha, **kwargs):
            return self.batch.release.Release(root, source, sha, fixture.f.policy, fixture.f.env, **kwargs)
        with mock.patch.object(ci.release, "run", side_effect=build), \
                mock.patch.object(ci.release, "Release", side_effect=check), \
                mock.patch.object(ci.release, "public_environment", return_value=fixture.f.env):
            process = multiprocessing.get_context("fork").Process(target=ci.serve, args=(fixture.f.root, directory))
            process.start()
        self.addCleanup(process.join, 5)
        self.addCleanup(process.terminate)
        (directory / "pid").write_text(str(process.pid))
        deadline = time.monotonic() + 30
        while not (directory / "environment.json").exists():
            self.assertTrue(process.is_alive())
            self.assertLess(time.monotonic(), deadline)
            time.sleep(0.05)
        hostile = dict(os.environ, MODULE_RELEASE_CI_DIRECTORY=str(directory),
                       **{key: "sentinel" for key in self.batch.SECRETS})
        for command in ("go test -count=1 ./...", "go list -m all"):
            script = directory / "step.sh"
            script.write_text(command + "\n")
            result = subprocess.run([sys.executable, str(HERE / "module-release-ci.py"), "shell", str(script)],
                                    cwd=fixture.f.root / self.batch.APP, env=hostile, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertTrue(process.is_alive())
        process.terminate()
        process.join(5)
        script.write_text("exit 0\n")
        result = subprocess.run([sys.executable, str(HERE / "module-release-ci.py"), "shell", str(script)],
                                cwd=fixture.f.root, env=hostile, capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0, "dead staging must never run the task")


class ShellPublication(unittest.TestCase):
    """Fast negative controls through the public shell entry point."""

    def fixture(self):
        temporary = tempfile.TemporaryDirectory(prefix="module-publication-shell-")
        self.addCleanup(temporary.cleanup)
        directory = Path(temporary.name)
        f = controllers.fixtures.Fixture(directory)
        policy = json.loads((HERE / "module-publication-policy.json").read_text())
        f.policy = policy
        namespace = policy["namespace"]
        for path, entry in policy["modules"].items():
            prefix = "" if path == "." else path + "/"
            identity = namespace if path == "." else namespace + "/" + path
            f.write(prefix + "go.mod", f"module {identity}\n\ngo 1.27\n")
            f.write(prefix + "fixture.go", "package fixture\n")
        # The ordinary caller checks these exact host pins after prerequisite
        # validation. Keep the full production inventory in go.work so a missing
        # member cannot make a negative control pass at an earlier seam.
        f.write("adapters/go.mod", f"module {namespace}/adapters\n\ngo 1.27\n")
        for path in (".", "integration/microvm"):
            prefix = "" if path == "." else path + "/"
            identity = namespace if path == "." else namespace + "/" + path
            f.write(prefix + "go.mod", f"module {identity}\n\ngo 1.27\n\nrequire {namespace}/adapters v0.1.0\n")
        f.write("go.work", "go 1.27\nuse (\n" + "".join(" ./" + path + "\n" for path in policy["modules"]) + ")\n")
        f.write("engine/COMPATIBILITY.md", "fixture\n")
        f.write("adapters/COMPATIBILITY.md", "fixture\n")
        scripts = f.root / ".github/scripts"
        scripts.mkdir(parents=True)
        for name in ("check-module-publication.sh", "module-release-ci.py", "module-release.py",
                     "module-release-github.py", "module-candidate.py", "module-publication-policy.json"):
            shutil.copyfile(HERE / name, scripts / name)
        shutil.copytree(HERE / "module-consumers", scripts / "module-consumers")
        base = f.commit()
        return f, base

    def check(self, f, extra_env=None):
        env = dict(f.env, GH_TOKEN="", GOWORK="off", GOENV="off", GOTOOLCHAIN="local")
        if extra_env:
            env.update(extra_env)
        return subprocess.run(["sh", ".github/scripts/check-module-publication.sh"], cwd=f.root,
                              env=env, text=True, capture_output=True)

    def test_actual_shell_checker_rejects_off_main_and_wrong_namespace_before_download(self):
        namespace = controllers.fixtures.NS
        with self.subTest(control="off-main pseudo prerequisite"):
            f, base = self.fixture()
            f.git("commit", "--allow-empty", "-qm", "pr-head")
            pseudo = "v0.0.0-20260929205240-" + f.git("rev-parse", "--short=12", "HEAD")
            f.write("adapters/go.mod", f"module {namespace}/adapters\n\ngo 1.27\n\nrequire {namespace}/engine {pseudo}\n")
            f.git("add", "adapters/go.mod")
            f.git("commit", "-qm", "pin PR head")
            result = self.check(f)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("is not on fetched origin/main", result.stderr)
            self.assertEqual(f.git("rev-parse", "refs/remotes/origin/main"), base)
        with self.subTest(control="root tag is not an engine tag"):
            f, base = self.fixture()
            f.write("adapters/go.mod", f"module {namespace}/adapters\n\ngo 1.27\n\nrequire {namespace}/engine v0.1.0\n")
            f.git("add", "adapters/go.mod")
            f.git("commit", "-qm", "pin engine")
            f.git("tag", "v0.1.0", base)
            result = self.check(f)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("missing published prerequisite engine@v0.1.0", result.stderr)
        with self.subTest(control="off-main adapters tag is ungranted"):
            f, base = self.fixture()
            for path in ("internal/adaptersupport", "contracts/gen/go/mecatl/driver"):
                f.git("tag", path + "/v0.1.0", base)
            f.git("commit", "--allow-empty", "-qm", "pr-head")
            f.git("tag", "adapters/v0.1.0", "HEAD")
            tools = f.root.parent / "tools"
            tools.mkdir()
            gh = tools / "gh"
            attempted = f.root.parent / "gh-attempted"
            gh.write_text("#!/bin/sh\ntouch \"$TMPDIR/gh-attempted\"\nexit 1\n")
            gh.chmod(0o755)
            result = self.check(f, dict(PATH=str(tools) + os.pathsep + f.env["PATH"], TMPDIR=str(f.root.parent)))
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("off-main publication provenance requires a read-only GH_TOKEN or gh authentication", result.stderr)
            self.assertTrue(attempted.exists())
        with self.subTest(control="parser failure precedes public download"):
            f, _ = self.fixture()
            tools = f.root.parent / "failing-go"
            tools.mkdir()
            downloaded = f.root.parent / "downloaded"
            go = tools / "go"
            go.write_text("#!/bin/sh\n[ \"$1 $2\" = 'mod download' ] && touch \"$DOWNLOAD_MARKER\"\nexit 42\n")
            go.chmod(0o755)
            result = self.check(f, dict(PATH=str(tools) + os.pathsep + f.env["PATH"], DOWNLOAD_MARKER=str(downloaded)))
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("go work edit -json", result.stderr)
            self.assertFalse(downloaded.exists())


if __name__ == "__main__":
    quick = "--quick" in sys.argv
    if quick:
        sys.argv.remove("--quick")
    unittest.main(defaultTest=["Wiring", "Deployment", "Context"] if quick else None)
