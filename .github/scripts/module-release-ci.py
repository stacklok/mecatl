#!/usr/bin/env python3
"""Trusted CI caller for coordinated module batches (never a publication writer)."""
import argparse
from contextlib import nullcontext
import importlib.util
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile
import time

HERE = Path(__file__).resolve().parent
sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location("module_release_github", HERE / "module-release-github.py")
gh = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gh)
release = gh.release


def context(driver, run, number=0, plan=None, head=None, merge=None):
    """Only authenticated fixed-main tooling can grant release resolution."""
    T = release.git(driver.root, "rev-parse", "HEAD")
    driver.ancestor(T)
    if plan is not None:
        driver.trusted_run(run)
        gh.require(run["head_sha"] == T, "full CI must use fixed run T")
        action = driver.verify_plan(plan, run)
        gh.require(action["kind"] == "qualify", "not whole-batch qualification")
        _, record = driver.active()
        candidate, _ = driver.candidate(record)
        value = dict(kind="preparation", reservation=record, head=candidate["R"], T=T)
        expected = candidate["artifacts"]
    else:
        expected_workflow = driver.api.call("GET", "/actions/workflows/ci.yml")
        gh.require(run["event"] == "pull_request" and run["repository"]["full_name"] == driver.api.repository
                   and run["workflow_id"] == expected_workflow["id"] and run["path"] == ".github/workflows/ci.yml"
                   and run["head_sha"] == head, "not exact PR CI workflow identity")
        # A prefix/label/flag never grants an exception. Unrelated PRs retain the
        # ordinary path; malformed actual release PRs fail instead of being skipped.
        _, record = driver.active()
        if record is None:
            return None
        pr = driver.api.call("GET", f"/pulls/{number}")
        branch = driver.branch(record)
        relevant = pr["base"]["ref"] == branch or pr["head"]["ref"] == branch or pr["head"]["ref"].startswith("module-adoption/")
        if not relevant:
            return None
        value = driver.classify(number, dict(run, head_sha=T))
        gh.require(value["head"] == head, "PR head changed since CI event")
        if value["kind"] == "preparation":
            expected = gh.document(driver.api.file(gh.PREFIX + record["id"] + ".json", head))["artifacts"]
        else:
            # GitHub's prospective merge, not R or the adoption branch alone.
            gh.sha(merge)
            parents = [p["sha"] for p in driver.commit(merge)["parents"]]
            gh.require(parents == [value["base"], head], "stale prospective adoption merge")
            value["head"] = merge
            expected = None
    authority = driver.authority()
    if value["kind"] == "preparation":
        for path, version in record["batch"].items():
            authority.setdefault(path, {})[version] = value["head"]
    else:
        # classify() has authenticated COMPLETE public proof for this exact R.
        for path, version in record["batch"].items():
            authority.setdefault(path, {})[version] = value["binding"]["R"]
    adoption = None
    if value["kind"] == "adoption":
        adoption = dict(pr=number, head=head, base=value["base"], merge=merge, binding=value["binding"])
    return dict(schema=2, kind=value["kind"], T=T, source=value["head"], adoption=adoption,
                batch=record["batch"], expected=expected, authority=authority)


def public_history(driver):
    # Keep this separate from release context: historical grants neither stage
    # dependencies nor force the release CI fanout on ordinary development.
    T = release.git(driver.root, "rev-parse", "HEAD")
    try:
        driver.ancestor(T)
        return dict(schema=2, T=T, authority=driver.authority())
    except (release.CheckError, OSError, ValueError, KeyError, TypeError):
        # An unavailable reader must not break fully main-public checks. The
        # ordinary checker consumes this error only if it encounters off-main pins.
        return dict(schema=2, T=T, error="historical publication provenance unavailable")


def candidate_environment(env):
    result = release.execution_environment(env)
    # CI plumbing and explicit non-secret test/build inputs. Do not inherit
    # arbitrary environment variables, even credentials with unfamiliar names.
    names = {"CI", "GITHUB_ACTIONS", "GITHUB_WORKSPACE", "GITHUB_OUTPUT", "GITHUB_ENV", "GITHUB_PATH",
             "GITHUB_STEP_SUMMARY", "GITHUB_EVENT_NAME", "GITHUB_EVENT_PATH", "GITHUB_SHA", "GITHUB_REF",
             "GITHUB_BASE_REF", "GITHUB_HEAD_REF", "GITHUB_REPOSITORY", "GITHUB_RUN_ID", "GITHUB_RUN_ATTEMPT",
             "RUNNER_TEMP", "RUNNER_OS", "RUNNER_ARCH", "RUNNER_TRACKING_ID", "STAMP", "BASE_REF", "MECATL_PREBUILT_MECATED",
             "MECATL_PREBUILT_MECATUI", "PLAYWRIGHT_BROWSERS_PATH", "PNPM_HOME", "NODE_OPTIONS",
             "CHANGES_RESULT", "DOCS_ONLY", "GO_RELEVANT", "STUDIO_RELEVANT", "STUDIO_RESULT",
             "STUDIO_BROWSER_RESULT", "ANALYSIS_RESULT", "MODULE_LINT_RESULT", "ACTION_SCRIPTS_RESULT",
             "SITE_RELEVANT", "USER_DOCS_RESULT", "MODULE_RELEASE_CI_DIRECTORY"}
    result.update({k: v for k, v in env.items() if k in names})
    result["GOFLAGS"] = "-mod=readonly"
    return result


def install(root, value, directory):
    gh.require(value["schema"] == 2, "invalid CI context schema")
    gh.sha(value["T"])
    gh.sha(value["source"])
    directory.mkdir()
    trusted = directory / "trusted"
    trusted.mkdir()
    archive = subprocess.run(["git", "archive", value["T"], ".github/scripts", "go.mod", "go.sum"],
                             cwd=root, capture_output=True, check=True)
    with tarfile.open(fileobj=io.BytesIO(archive.stdout)) as files:
        files.extractall(trusted, filter="data")
    (directory / "context.json").write_text(gh.canonical(value))
    subprocess.run(["git", "checkout", "--detach", value["source"]], cwd=root, check=True)
    # BASH_ENV applies to explicit bash shells and composite actions too. The
    # wrapper removes itself before starting the actual candidate shell.
    helper = trusted / ".github/scripts/module-release-ci.py"
    hook = directory / "bash-env"
    hook.write_text('exec python3 "' + str(helper) + '" shell "$0"\n')
    with open(os.environ["GITHUB_ENV"], "a") as output:
        output.write(f"MODULE_RELEASE_CI_DIRECTORY={directory}\nBASH_ENV={hook}\n")


def serve(root, directory):
    value = gh.document((directory / "context.json").read_text())
    env = release.public_environment(directory)
    trusted = directory / "trusted"
    helper = directory / "module-artifact"
    with release.source_tree(root, value["source"]) as source:
        check = release.Release(root, source, value["source"], authority=value["authority"])
        proxy = directory / "proxy"
        if value["kind"] == "preparation":
            release.run(trusted, "go", "build", "-o", str(helper), str(trusted / ".github/scripts/module-artifact.go"), env=env)
            check.preview(value["batch"])
            check.stage(value["batch"], helper, proxy, value["expected"])
            manager = check.staged_environment(value["batch"], proxy, env)
        else:
            manager = nullcontext(env)
        with manager as resolved:
            ready = directory / "environment.tmp"
            ready.write_text(gh.canonical(dict(resolved, GOFLAGS="-mod=readonly")))
            ready.replace(directory / "environment.json")
            while True:
                time.sleep(60)


def start(root, directory):
    log = open(directory / "proxy.log", "w")
    env = release.execution_environment(os.environ)
    if "RUNNER_TRACKING_ID" in os.environ:
        env["RUNNER_TRACKING_ID"] = os.environ["RUNNER_TRACKING_ID"]
    process = subprocess.Popen([sys.executable, str(Path(__file__).resolve()), "serve", "--root", str(root),
                                "--directory", str(directory)], env=env,
                               stdin=subprocess.DEVNULL, stdout=log, stderr=log, start_new_session=True)
    (directory / "pid").write_text(str(process.pid))
    deadline = time.monotonic() + 600
    while not (directory / "environment.json").exists():
        gh.require(process.poll() is None, "release resolver failed: " + (directory / "proxy.log").read_text())
        gh.require(time.monotonic() < deadline, "release resolver startup timed out")
        time.sleep(0.1)
    with open(os.environ["GITHUB_ENV"], "a") as output:
        for key, value in gh.document((directory / "environment.json").read_text()).items():
            if key.startswith("GO"):
                gh.require("\n" not in value, "invalid Go environment")
                output.write(f"{key}={value}\n")


def shell(script, directory):
    env = candidate_environment(os.environ)
    if (directory / "environment.json").exists():
        os.kill(int((directory / "pid").read_text()), 0)
        env.update({k: v for k, v in gh.document((directory / "environment.json").read_text()).items() if k.startswith("GO")})
    os.execve(shutil.which("bash"), ["bash", "--noprofile", "--norc", "-eo", "pipefail", script], env)


def ordinary_publication(root, authority_loader=None):
    """The normal public-only check; history authentication is lazy and read-only."""
    root = root.resolve()
    with tempfile.TemporaryDirectory(prefix="module-publication-") as temporary:
        temporary = Path(temporary)
        env = release.execution_environment(release.public_environment(temporary))
        check = release.Release(root, root, release.git(root, "rev-parse", "HEAD"),
                                authority_loader=authority_loader or (lambda: release.historical_authority(root)))
        check.pin_agreement()
        def requirements(mod):
            for identity, (version, _) in release.candidate_helpers.requirements(mod).items():
                if identity.startswith(check.policy["namespace"] + "/"):
                    gh.require(identity in check.by_identity, "unknown same-repository prerequisite: " + identity)
                    check.prerequisite(identity, version)
        for path in ("internal/adaptersupport", "contracts/gen/go/mecatl/driver", "adapters"):
            requirements(check.modules[path]["mod"])
        identity = check.modules["adapters"]["identity"]
        pinned = release.candidate_helpers.requirements(check.modules["."]["mod"])[identity][0]
        version = os.environ.get("MODULE_PUBLICATION_VERSION", pinned)
        for selected in (pinned, version):
            gh.require(check.tag_commit("adapters", selected), "adapters version has no published nested tag")
        consumer = temporary / "consumer"
        consumer.mkdir()
        (consumer / "go.mod").write_text(f"module example.com/mecatl-module-publication-check\n\ngo 1.27\n\nrequire {identity} {version}\n")
        shutil.copyfile(HERE / "module-consumers/adapters.go", consumer / "smoke_test.go")
        release.run(consumer, "go", "mod", "tidy", env=dict(env, GOFLAGS="-mod=mod"))
        published = release.run(consumer, "go", "list", "-m", "-f", "{{.GoMod}}", identity, env=env).strip()
        requirements(release.candidate_helpers.parse_mod(consumer, env, Path(published)))
        release.run(consumer, "go", "test", "-mod=readonly", "./...", env=env)


def publication(root, directory):
    value = gh.document((directory / "context.json").read_text())
    env = gh.document((directory / "environment.json").read_text())
    check = release.Release(root, root, value["source"], authority=value["authority"])
    check.pin_agreement()
    staged = value["batch"] if value["kind"] == "preparation" else {}
    public = release.public_environment(directory / "public-check")
    verified = {}
    for path in ("internal/adaptersupport", "contracts/gen/go/mecatl/driver", "adapters"):
        check.no_replacements(check.modules[path]["mod"], path)
        check.verify_prerequisites(check.modules[path]["mod"], public, verified=verified, staged=staged)
    version = release.candidate_helpers.requirements(check.modules["."]["mod"])[check.modules["adapters"]["identity"]][0]
    if "adapters" not in staged:
        check.verify_prerequisites({"Require": [{"Path": check.modules["adapters"]["identity"], "Version": version}]}, public, verified=verified)
    else:
        gh.require(version == staged["adapters"], "root pin differs from approved staged adapters")
    with tempfile.TemporaryDirectory(prefix="module-ci-publication-") as temporary:
        consumer = Path(temporary)
        (consumer / "go.mod").write_text(f"module example.com/mecatl-publicationcheck\n\ngo 1.27\n\nrequire {check.modules['adapters']['identity']} {version}\n")
        shutil.copyfile(HERE / "module-consumers/adapters.go", consumer / "smoke_test.go")
        release.run(consumer, "go", "mod", "tidy", env=dict(env, GOFLAGS=""))
        check.check_selection(consumer, env, staged, value["expected"] or {}, verified, public,
                              requested="adapters" if "adapters" in staged else None)
        release.run(consumer, "go", "test", "-mod=readonly", "./...", env=env)
    release.candidate_helpers.verify_unchanged(check.snapshots)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("context", "install", "start", "serve", "shell", "publication", "ordinary-publication", "ordinary-ci-publication", "receipt"))
    parser.add_argument("script", nargs="?")
    parser.add_argument("--root", type=Path, default=Path.cwd())
    parser.add_argument("--directory", type=Path)
    parser.add_argument("--input", type=Path)
    args = parser.parse_args()
    directory = args.directory or Path(os.environ.get("MODULE_RELEASE_CI_DIRECTORY", "."))
    try:
        if args.command == "context":
            api = gh.GitHub(os.environ["GITHUB_REPOSITORY"], os.environ.get("GH_TOKEN", ""))
            plan = gh.document(args.input.read_text()) if args.input else None
            classify = plan or (os.environ.get("MODULE_RELEASES_ENABLED") == "true" and os.environ.get("RELEASE_PR"))
            driver = None
            try:
                driver = gh.Driver(api, args.root, os.environ.get("RELEASE_APP_ID") or 0, os.environ.get("RELEASE_APP_BOT_ID") or 0,
                                   os.environ["GITHUB_RUN_ID"], os.environ["GITHUB_RUN_ATTEMPT"])
            except (release.CheckError, ValueError):
                if classify:
                    raise
            value = None
            if classify:
                run = api.call("GET", f"/actions/runs/{driver.current_run}")
                value = context(driver, run, int(os.environ.get("RELEASE_PR") or 0), plan,
                                os.environ.get("PR_HEAD_SHA"), os.environ.get("GITHUB_SHA"))
            history = (public_history(driver) if driver else
                       dict(schema=2, T=release.git(args.root, "rev-parse", "HEAD"),
                            error="historical publication identity unavailable"))
            if value and value["adoption"]:
                receipt = dict(schema=2, kind="adoption", run=run["id"], attempt=1, T=value["T"], **value["adoption"])
                Path(os.environ["RUNNER_TEMP"], "module-adoption.json").write_text(gh.canonical(receipt))
            with open(os.environ["GITHUB_OUTPUT"], "a") as output:
                output.write("context=" + (json.dumps(value, separators=(",", ":")) if value else "") + "\n")
                output.write("public_history=" + json.dumps(history, separators=(",", ":")) + "\n")
        elif args.command == "install":
            install(args.root, gh.document(os.environ["RELEASE_CI_CONTEXT"]), directory)
        elif args.command == "start":
            start(args.root, directory)
        elif args.command == "serve":
            serve(args.root, directory)
        elif args.command == "shell":
            shell(args.script, directory)
        elif args.command in ("ordinary-publication", "ordinary-ci-publication"):
            loader = None
            if args.command == "ordinary-ci-publication":
                def loader():
                    # This input comes ONLY from the API-authenticated Changes
                    # job, never a candidate file or the local public CLI.
                    value = gh.document(os.environ.get("PUBLICATION_CI_CONTEXT") or "{}")
                    gh.require(value.get("schema") == 2 and "authority" in value,
                               "historical publication provenance unavailable")
                    release.git(args.root, "merge-base", "--is-ancestor", gh.sha(value["T"]), "refs/remotes/origin/main")
                    return value["authority"]
            ordinary_publication(args.root, loader)
        elif args.command == "publication":
            publication(args.root, directory)
        else:
            plan = gh.document(args.input.read_text())
            registry = release.load_json(HERE / "module-release-ci.json")
            value = dict(schema=2, kind="full-ci", binding=plan["action"]["binding"], run=plan["run"],
                         attempt=1, jobs=registry["required_jobs"])
            Path("full-ci.json").write_text(gh.canonical(value))
        return 0
    except (release.CheckError, OSError, ValueError, KeyError, TypeError, subprocess.CalledProcessError) as error:
        print(f"module-release-ci: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
