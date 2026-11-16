#!/usr/bin/env python3
"""Narrow shared-App authority for the existing root and SDK release callers."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import re
import sys
from urllib.parse import quote

sys.dont_write_bytecode = True
HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("protection", HERE / "module-release-protection.py")
protection = importlib.util.module_from_spec(spec)
spec.loader.exec_module(protection)
require, Error = protection.require, protection.Error
STABLE = r"(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)"


def refs(track, version):
    require(isinstance(version, str) and re.fullmatch(STABLE, version), "release version must be canonical stable MAJOR.MINOR.PATCH")
    require(track in ("root", "sdk"), "unknown release track")
    prefix = "" if track == "root" else "sdk/typescript/"
    branch = "release/v" if track == "root" else "release/sdk-typescript/v"
    return prefix + "v" + version, branch + version


def context():
    require(os.environ.get("GITHUB_REF") == "refs/heads/main", "release App is main-only")
    repository = os.environ["GITHUB_REPOSITORY"]
    require(re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository), "invalid repository")
    require(os.environ.get("REPOSITORY", repository) == repository, "repository mismatch")
    commit = os.environ["GITHUB_SHA"]
    require(re.fullmatch(r"[0-9a-f]{40}", commit), "full event SHA required")
    require(os.environ.get("SHA", commit) == commit, "event SHA mismatch")
    return repository, commit


def request(api, track, version, repository, commit):
    refs(track, version)
    require(os.environ.get("BUMP_TYPE") in ("patch", "minor", "major"), "invalid release bump type")
    require(os.environ.get("GITHUB_EVENT_NAME") == "workflow_dispatch", "release requests require human dispatch")
    workflow = "create-release-pr.yml" if track == "root" else "create-sdk-typescript-release-pr.yml"
    require(os.environ.get("GITHUB_WORKFLOW_REF") == repository + "/.github/workflows/" + workflow + "@refs/heads/main"
            and os.environ.get("GITHUB_WORKFLOW_SHA") == commit, "wrong release request workflow/repository/SHA")
    # GitHub-supplied workflow context is trusted here; public actor metadata
    # avoids adding Actions/read (or a settings reader) to the existing PR job.
    actor = os.environ.get("GITHUB_TRIGGERING_ACTOR", "")
    require(re.fullmatch(r"[A-Za-z0-9_\[\]-]+", actor), "missing/invalid GitHub triggering actor")
    user = api.call("GET", "/users/" + quote(actor, safe=""))
    require(user.get("type") == "User" and user.get("login", "").lower() == actor.lower(), "release request requires a human dispatcher")


def provenance(api, repository, commit, branch, bot_id):
    base = "/repos/" + repository
    prs = api.all(base + "/commits/" + commit + "/pulls")
    matches = []
    for summary in prs:
        if summary.get("head", {}).get("ref") != branch:
            continue
        pr = api.call("GET", base + "/pulls/" + str(int(summary["number"])))
        head, target = pr.get("head", {}), pr.get("base", {})
        if (pr.get("merged") is True and pr.get("merged_at") and pr.get("merge_commit_sha") == commit
                and head.get("ref") == branch and target.get("ref") == "main"
                and head.get("repo", {}).get("full_name") == repository
                and target.get("repo", {}).get("full_name") == repository
                and head.get("repo", {}).get("id") == target.get("repo", {}).get("id")
                and isinstance(head.get("repo", {}).get("id"), int)
                and pr.get("user", {}).get("id") == bot_id and pr["user"].get("type") == "Bot"
                and pr.get("merged_by", {}).get("type") == "User"):
            matches.append(pr)
    require(len(matches) == 1, "expected exactly one same-repository designated-App release PR mapped to this exact merged SHA and merged by a human")


def tag_state(api, repository, commit, tag):
    # matching-refs avoids treating authorization failures as missing refs.
    values = api.call("GET", f"/repos/{repository}/git/matching-refs/tags/" + quote(tag, safe="/"))
    found = [r for r in values if r.get("ref") == "refs/tags/" + tag]
    if not found:
        return "absent"
    require(len(found) == 1, "ambiguous tag")
    obj = found[0]["object"]
    for _ in range(8):
        if obj["type"] == "commit":
            require(obj["sha"] == commit, "immutable release tag conflict; fix forward")
            return "current"
        require(obj["type"] == "tag" and re.fullmatch(r"[0-9a-f]{40}", obj["sha"]), "invalid annotated tag")
        obj = api.call("GET", f"/repos/{repository}/git/tags/" + obj["sha"])["object"]
    raise Error("release tag annotation nesting is too deep")


def decide(api, track, version, repository, commit, bot_id):
    tag, branch = refs(track, version)
    # Even same-SHA retries must prove authority; existence is not provenance.
    provenance(api, repository, commit, branch, bot_id)
    return tag_state(api, repository, commit, tag)


def write_tag(api, track, version, repository, commit):
    tag, _ = refs(track, version)
    if tag_state(api, repository, commit, tag) == "current":
        return
    annotated = api.call("POST", f"/repos/{repository}/git/tags",
                         dict(tag=tag, message="Release " + tag, object=commit, type="commit"))
    require(re.fullmatch(r"[0-9a-f]{40}", annotated["sha"]), "invalid tag object SHA")
    api.call("POST", f"/repos/{repository}/git/refs", dict(ref="refs/tags/" + tag, sha=annotated["sha"]))


def write_sdk_pr(api, reader, version, repository, commit):
    _, branch = refs("sdk", version)
    require(Path("sdk/typescript/VERSION").read_text().strip() == version, "SDK VERSION mismatch")
    manifest = json.loads(Path("sdk/typescript/package.json").read_text())
    require(manifest.get("name") == "@stacklok-oss/mecatl-sdk" and manifest.get("version") == version, "SDK package identity/version mismatch")
    base = "/repos/" + repository
    tree = reader.call("GET", base + "/git/commits/" + commit)["tree"]["sha"]
    entries = []
    for path in ("sdk/typescript/CHANGELOG.md", "sdk/typescript/VERSION", "sdk/typescript/package.json"):
        blob = api.call("POST", base + "/git/blobs", dict(content=Path(path).read_text(), encoding="utf-8"))
        entries.append(dict(path=path, mode="100644", type="blob", sha=blob["sha"]))
    tree = api.call("POST", base + "/git/trees", dict(base_tree=tree, tree=entries))["sha"]
    title = "Release TypeScript SDK v" + version
    created = api.call("POST", base + "/git/commits", dict(message=title, tree=tree, parents=[commit]))["sha"]
    api.call("POST", base + "/git/refs", dict(ref="refs/heads/" + branch, sha=created))
    body = (Path(os.environ["RUNNER_TEMP"]) / "sdk-release-pr.md").read_text()
    pr = api.call("POST", base + "/pulls", dict(title=title, head=branch, base="main", body=body))
    require(isinstance(pr.get("number"), int) and pr["number"] > 0, "invalid created PR number")
    with open(os.environ["GITHUB_OUTPUT"], "a") as output:
        output.write(f"url=https://github.com/{repository}/pull/{pr['number']}\nnumber={pr['number']}\n")


def authority(kind, track, version, repository, commit):
    return dict(kind=kind, track=track, version=version, repository=repository, commit=commit,
                client_id=os.environ["RELEASE_APP_CLIENT_ID"])


def authority_path(kind, track):
    return Path(os.environ["RUNNER_TEMP"]) / f"release-app-{track}-{kind}.json"


def save_authority(record):
    path = authority_path(record["kind"], record["track"])
    try:
        with path.open("x") as output:
            json.dump(record, output)
    except FileExistsError:
        require(json.loads(path.read_text()) == record, "runner-local release authority conflict")


def load_authority(kind, track, version, repository, commit):
    # Only the trusted preflight writes this runner-local, non-secret record.
    # Never download it as an artifact or derive App identity from caller input.
    record = json.loads(authority_path(kind, track).read_text())
    expected = authority(kind, track, version, repository, commit)
    require({k: record.get(k) for k in expected} == expected
            and all(type(record.get(k)) is int and record[k] > 0 for k in ("app_id", "bot_id")),
            "missing/mismatched runner-local release authority")
    return record


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("version", "request", "tag", "write-tag", "write-sdk-pr"))
    parser.add_argument("--track", required=True, choices=("root", "sdk"))
    args = parser.parse_args()
    try:
        if args.command == "version":
            path = "VERSION" if args.track == "root" else "sdk/typescript/VERSION"
            version = Path(path).read_text().strip()
            refs(args.track, version)
            print(version)
            return 0
        repository, commit = context()
        version = os.environ.get("VERSION")
        if args.command == "request" and (args.track == "root" or not version):
            path = "VERSION" if args.track == "root" else "sdk/typescript/VERSION"
            version = Path(path).read_text().strip()
        refs(args.track, version)
        api = protection.GitHub()
        if args.command in ("write-tag", "write-sdk-pr"):
            kind = "tag" if args.command == "write-tag" else "request"
            record = load_authority(kind, args.track, version, repository, commit)
            writer = protection.GitHub(os.environ["RELEASE_APP_WRITE_TOKEN"])
            if args.command == "write-sdk-pr":
                require(args.track == "sdk", "SDK writer cannot write root or module releases")
                request(api, args.track, version, repository, commit)
                write_sdk_pr(writer, api, version, repository, commit)
            else:
                provenance(api, repository, commit, refs(args.track, version)[1], record["bot_id"])
                write_tag(writer, args.track, version, repository, commit)
            return 0
        ids = protection.identity(api, repository,
                                  int(os.environ["RELEASE_APP_ID"]) if os.environ.get("RELEASE_APP_ID") else None,
                                  int(os.environ["RELEASE_APP_BOT_ID"]) if os.environ.get("RELEASE_APP_BOT_ID") else None)
        if args.command == "request":
            request(api, args.track, version, repository, commit)
        else:
            state = decide(api, args.track, version, repository, commit, ids["bot_id"])
        # Root/SDK retain their existing main-only environment contract while
        # module rollout is disabled. Their writers cannot address module refs.
        if os.environ.get("MODULE_RELEASES_ENABLED") == "true":
            protection.run(api, "check", repository, ids["app_id"], ids["bot_id"])
        save_authority(dict(authority(args.command, args.track, version, repository, commit), **ids))
        if args.command == "tag":
            print(state)
    except (Error, KeyError, ValueError, OSError) as error:
        print(str(error) if isinstance(error, Error) else "invalid release authority metadata", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
