#!/usr/bin/env python3
"""Read-only release protection preflight; `apply --confirm` is human-admin only.

See module-release-protection.md for credentials, rollout and exceptional recovery.
App identity/installation inspection uses a short-lived JWT from the existing
release App key; it never creates an installation access token.
"""
import argparse
import base64
import fnmatch
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import time
from urllib.error import HTTPError, URLError
from urllib.parse import quote
from urllib.request import Request, build_opener, HTTPRedirectHandler

HERE = Path(__file__).resolve().parent


class Error(Exception):
    pass


def require(value, message):
    if not value:
        raise Error(message)


def app_jwt():
    supplied = os.environ.get("RELEASE_APP_JWT")
    if supplied:
        require(not any(c.isspace() for c in supplied), "invalid supplied App JWT")
        return supplied
    key, client = os.environ.get("RELEASE_APP_PRIVATE_KEY"), os.environ.get("RELEASE_APP_CLIENT_ID")
    require(key and client, "App inspection needs the existing environment App key/client ID, or an explicitly supplied RELEASE_APP_JWT for local inspection")
    require(re.fullmatch(r"[A-Za-z0-9_.-]{1,200}", client), "invalid App client ID")

    def encoded(value):
        return base64.urlsafe_b64encode(json.dumps(value, separators=(",", ":")).encode()).rstrip(b"=")

    now = int(time.time())
    payload = encoded(dict(alg="RS256", typ="JWT")) + b"." + encoded(dict(iat=now - 60, exp=now + 300, iss=client))
    # OpenSSL performs RSA signing. Key bytes travel only over stdin; the small,
    # public signing input uses an inherited pipe. No key/JWT argv or disk files,
    # and no job credentials/configuration are inherited by OpenSSL.
    reader, writer = os.pipe()
    try:
        os.write(writer, payload)
        os.close(writer)
        writer = None
        signed = subprocess.run(["openssl", "dgst", "-sha256", "-sign", "/dev/stdin", "-passin", "pass:", f"/dev/fd/{reader}"],
                                input=key.encode(), capture_output=True, pass_fds=(reader,), env={"PATH": os.defpath})
    finally:
        os.close(reader)
        if writer is not None:
            os.close(writer)
    require(signed.returncode == 0 and signed.stdout, "unable to sign short-lived App JWT with OpenSSL; verify the existing App key format (details suppressed)")
    return (payload + b"." + base64.urlsafe_b64encode(signed.stdout).rstrip(b"=")).decode()


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class GitHub:
    def __init__(self, token=None):
        self.token, self.jwt = token, None

    def call(self, method, path, data=None, metadata=False, app=False):
        require(not metadata or method == "GET", "metadata credentials are read-only in this tool")
        if app:
            require(method == "GET" and (path == "/app" or re.fullmatch(r"/repos/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+/installation", path)), "App JWT is restricted to identity/installation GETs")
            self.jwt = self.jwt or app_jwt()
            request = Request("https://api.github.com" + path, headers={"Authorization": "Bearer " + self.jwt,
                              "Accept": "application/vnd.github+json", "X-GitHub-Api-Version": "2022-11-28"})
            try:
                with build_opener(NoRedirect).open(request, timeout=60) as response:
                    return json.load(response)
            except (HTTPError, URLError, OSError) as error:
                if isinstance(error, HTTPError):
                    error.close()
                raise Error(f"App-authenticated GET {path} failed; verify the existing App identity/key and installation access (response details suppressed)") from None
        env = os.environ.copy()
        for name in ("RELEASE_APP_PRIVATE_KEY", "RELEASE_APP_JWT"):
            env.pop(name, None)
        if self.token is not None:
            env["GH_TOKEN"] = self.token
        if metadata and env.get("RELEASE_SETTINGS_TOKEN"):
            env["GH_TOKEN"] = env["RELEASE_SETTINGS_TOKEN"]
        command = ["gh", "api", "--method", method, path,
                   "-H", "X-GitHub-Api-Version: 2022-11-28"]
        if data is not None:
            command += ["--input", "-"]
        result = subprocess.run(command, input=None if data is None else json.dumps(data),
                                text=True, capture_output=True, env=env)
        # Never echo transport diagnostics, response bodies or credentials.
        require(result.returncode == 0, f"GitHub {method} {path} unavailable; "
                "this operation needs readable settings metadata. GH_TOKEN is tried by default; "
                "if GitHub withholds it, rollout needs separately authorized human read access "
                "(optional RELEASE_SETTINGS_TOKEN). No new permission or credential is granted by this tool.")
        return json.loads(result.stdout) if result.stdout.strip() else None

    def all(self, path, field=None, metadata=False):
        values, page = [], 1
        while True:
            result = self.call("GET", path + ("&" if "?" in path else "?") + f"per_page=100&page={page}", metadata=metadata)
            result = result[field] if field else result
            values.extend(result)
            if len(result) < 100:
                return values
            page += 1


def patterns():
    policy = json.loads((HERE / "module-publication-policy.json").read_text())
    return ["refs/tags/v*", "refs/tags/sdk/typescript/v*"] + [
        "refs/tags/" + value["tag_prefix"] + "v*"
        for value in policy["modules"].values() if "tag_prefix" in value]


def covers(pattern, target):
    """Sufficient containment proof for GitHub FNM_PATHNAME patterns.

    The target has fixed directory segments and a final v*. Unknown expressions
    are not guessed: add the explicit managed rules alongside inherited rules.
    Unlike Python fnmatch, '*' never consumes '/'; '**/' can consume segments.
    """
    if pattern == "~ALL":
        return True
    # Ruby's bracket/escape semantics differ from Python's. Only the shared
    # literal, '?' and '*' subset is used as a containment proof.
    if any(char in pattern for char in "[]\\"):
        return False
    parts, wanted = pattern.split("/"), target.split("/")

    def match(i, j):
        if i == len(parts):
            return j == len(wanted)
        if parts[i] == "**" and i + 1 < len(parts):
            return match(i + 1, j) or (j < len(wanted) and match(i, j + 1))
        if j == len(wanted):
            return False
        accepted = parts[i] in ("*", "**", "v*") if wanted[j] == "v*" else fnmatch.fnmatchcase(wanted[j], parts[i])
        return accepted and match(i + 1, j + 1)
    return match(0, 0)


def protects(rule, target, kind, app_id):
    if rule.get("target") != "tag" or rule.get("enforcement") != "active":
        return False
    condition = rule.get("conditions", {}).get("ref_name", {})
    # Exclusions can contain arbitrary fnmatch expressions. Do not claim full
    # namespace coverage from a rule with holes, even for currently absent tags.
    if condition.get("exclude") or not any(covers(p, target) for p in condition.get("include", [])):
        return False
    if kind not in {r["type"] for r in rule.get("rules", [])}:
        return False
    require(isinstance(rule.get("bypass_actors"), list),
            "GitHub withheld ruleset bypass actors; use a human-admin metadata reader that can inspect these rules (never infer an empty bypass list)")
    bypass = rule["bypass_actors"]
    if kind == "creation":
        return all(b.get("actor_type") == "Integration" and b.get("actor_id") == app_id
                   and b.get("bypass_mode") == "always" for b in bypass)
    return not bypass


def desired(app_id):
    return [dict(name="release-tag-" + name, target="tag", enforcement="active",
                 bypass_actors=([dict(actor_id=app_id, actor_type="Integration", bypass_mode="always")]
                                if name == "creation" else []),
                 conditions=dict(ref_name=dict(include=patterns(), exclude=[])),
                 rules=[dict(type=kind) for kind in kinds])
            for name, kinds in (("creation", ["creation"]), ("immutability", ["update", "deletion"]))]


def identity(api, repository, app_id=None, bot_id=None):
    app = api.call("GET", "/app", app=True)
    client = os.environ.get("RELEASE_APP_CLIENT_ID")
    require(client and app.get("client_id") == client, "App JWT identity does not match the configured client ID")
    require(type(app.get("id")) is int and app["id"] > 0 and (app_id is None or app["id"] == app_id), "wrong designated App ID")
    require(all(app.get("permissions", {}).get(p, "none") == "none" for p in ("administration", "workflows")),
            "release App must not request administration or workflows rights")
    slug = app["slug"]
    require(re.fullmatch(r"[A-Za-z0-9_-]+", slug), "invalid authenticated App slug")
    bot = api.call("GET", "/users/" + quote(slug + "[bot]", safe=""))
    require(bot.get("type") == "Bot" and bot.get("login") == slug + "[bot]"
            and type(bot.get("id")) is int and bot["id"] > 0
            and (bot_id is None or bot["id"] == bot_id), "authenticated App does not map to the designated bot")
    # This JWT-only endpoint proves that THIS repository is in the installation;
    # the production response has account/target fields, not a repository list.
    installation = api.call("GET", "/repos/" + repository + "/installation", app=True)
    repo = api.call("GET", "/repos/" + repository)
    owner, account = repo["owner"], installation["account"]
    require(repo.get("full_name", "").lower() == repository.lower()
            and account.get("id") == owner["id"] and account.get("login", "").lower() == repository.split("/")[0].lower()
            and installation.get("target_id") == owner["id"] and installation.get("target_type") == owner["type"], "installation repository/owner mismatch")
    require(installation.get("app_id") == app["id"] and installation.get("app_slug") == slug, "wrong release installation App")
    require(type(installation.get("id")) is int and installation["id"] > 0
            and "suspended_at" in installation and installation["suspended_at"] is None
            and installation.get("repository_selection") in ("all", "selected"), "release installation suspended or unverifiable")
    permissions = installation.get("permissions", {})
    require(all(permissions.get(p) == "write" for p in ("contents", "pull_requests")), "release installation needs existing contents/PR write rights; request any extension separately")
    require(permissions.get("administration", "none") == "none", "release App must not have administration rights")
    require(permissions.get("workflows", "none") == "none", "release App must not have workflows write rights")
    return dict(app_id=app["id"], bot_id=bot["id"])


def environment(api, base):
    env = api.call("GET", base + "/environments/release", metadata=True)
    require(env.get("deployment_branch_policy") == dict(protected_branches=False, custom_branch_policies=True),
            "release environment must use custom main-only branch policies; repair manually without changing reviewers/wait timers")
    branches = api.all(base + "/environments/release/deployment-branch-policies", "branch_policies", metadata=True)
    require(len(branches) == 1 and branches[0].get("name") == "main" and branches[0].get("type") == "branch",
            "release environment must allow only branch main (not a tag); repair environment branch policies manually")


def secret_scope(api, repository):
    # Names/scope only: GitHub never returns secret values from these endpoints.
    base = "/repos/" + repository
    repo = api.call("GET", base, metadata=True)
    protected = {"RELEASE_APP_PRIVATE_KEY", "RELEASE_SETTINGS_TOKEN"}
    secrets = api.all(base + "/actions/secrets", "secrets", metadata=True)
    require(not any(s["name"] in protected for s in secrets),
            "release credentials must not be repository secrets; move them to the main-only release environment")
    if repo["owner"]["type"] == "Organization":
        org = "/orgs/" + repository.split("/")[0]
        secrets = api.all(org + "/actions/secrets", "secrets", metadata=True)
        for secret in secrets:
            if secret["name"] not in protected:
                continue
            visibility = secret["visibility"]
            require(visibility in ("all", "private", "selected"), "unknown organization secret visibility")
            available = visibility == "all" or (visibility == "private" and repo["private"])
            if visibility == "selected":
                selected = api.all(org + "/actions/secrets/" + secret["name"] + "/repositories", "repositories", metadata=True)
                available = any(r["id"] == repo["id"] for r in selected)
            require(not available, "an organization release credential is exposed to this repository outside the release environment; restrict it before enabling releases")
    names = {s["name"] for s in api.all(f"/repositories/{int(repo['id'])}/environments/release/secrets", "secrets", metadata=True)}
    require("RELEASE_APP_PRIVATE_KEY" in names, "release environment has no release App key metadata")


def inspect(api, repository, app_id, bot_id):
    identity(api, repository, app_id, bot_id)
    base = "/repos/" + repository
    environment(api, base)
    summaries = api.all(base + "/rulesets?includes_parents=true", metadata=True)
    rules = []
    for summary in summaries:
        if summary.get("target") not in (None, "tag"):
            continue
        # Fetch the source endpoint, not a repository endpoint with an inherited ID.
        source, kind = summary.get("source"), summary.get("source_type")
        if kind == "Repository":
            require(source == repository, "unexpected ruleset repository source")
            path = base + "/rulesets/" + str(int(summary["id"]))
        elif kind == "Organization":
            require(source == repository.split("/")[0], "unexpected ruleset organization source")
            path = "/orgs/" + source + "/rulesets/" + str(int(summary["id"]))
        else:
            raise Error("unsupported inherited ruleset source; inspect enterprise policy with its administrator before release")
        rules.append(api.call("GET", path, metadata=True))
    missing = {kind: [p for p in patterns() if not any(protects(r, p, kind, app_id) for r in rules)]
               for kind in ("creation", "update", "deletion")}
    additions = []
    for rule in desired(app_id):
        if not any(missing[k["type"]] for k in rule["rules"]):
            continue
        require(not any(r.get("name") == rule["name"] for r in rules),
                "managed ruleset exists but is insufficient; human must inspect/repair it (never overwrite stronger rules)")
        additions.append(rule)
    return dict(missing=missing, create_rulesets=additions)


WORKFLOWS = ("create-release-pr.yml", "create-release-tag.yml",
             "create-sdk-typescript-release-pr.yml", "create-sdk-typescript-release-tag.yml",
             "request-module-release.yml", "module-releases.yml")


def workflows(api, repository):
    # Compare Git blob identities, not workflow names or mutable check names.
    # Local code is the reviewed/deployed checker checkout. Settings-only diff
    # remains useful before deployment; successful check requires deployed code.
    base = "/repos/" + repository
    commit = api.call("GET", base + "/git/ref/heads/main")["object"]["sha"]
    require(re.fullmatch(r"[0-9a-f]{40}", commit), "invalid main SHA")
    for name in WORKFLOWS:
        path = ".github/workflows/" + name
        content = (HERE.parent.parent / path).read_bytes()
        blob = hashlib.sha1(b"blob " + str(len(content)).encode() + b"\0" + content).hexdigest()
        remote = api.call("GET", base + "/contents/" + path + "?ref=" + commit)
        require(remote.get("type") == "file" and remote.get("sha") == blob,
                f"shared-App workflow {name} differs from trusted checkout; deploy/review the complete shared-authority changes before enabling releases")


def run(api, command, repository, app_id, bot_id, confirm=False):
    require(re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository), "invalid repository")
    require(app_id > 0 and bot_id > 0, "positive App and bot IDs required")
    result = inspect(api, repository, app_id, bot_id)
    if command == "audit":
        secret_scope(api, repository)
        result["credential_scope_audited"] = True
    if command == "apply" and confirm:
        user = api.call("GET", "/user")
        require(user.get("type") == "User", "settings apply requires a human admin, never the release App")
        repo = api.call("GET", "/repos/" + repository)
        require(repo.get("permissions", {}).get("admin") is True, "settings apply requires human repository admin authority")
        for rule in result["create_rulesets"]:
            api.call("POST", "/repos/" + repository + "/rulesets", rule)
        result = inspect(api, repository, app_id, bot_id)
    if command == "check":
        require(not any(result["missing"].values()), "missing effective tag protections; run diff, then have a human admin review/apply")
        workflows(api, repository)
        return dict(schema=1, repository=repository, app_id=app_id, bot_id=bot_id, protected=True)
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("check", "diff", "audit", "apply"))
    parser.add_argument("--repository", required=True)
    parser.add_argument("--app-id", required=True, type=int)
    parser.add_argument("--bot-id", required=True, type=int)
    parser.add_argument("--confirm", action="store_true", help="explicit human authorization for settings writes (apply only)")
    args = parser.parse_args()
    try:
        require(not args.confirm or args.command == "apply", "--confirm is apply-only")
        print(json.dumps(run(GitHub(), args.command, args.repository, args.app_id, args.bot_id, args.confirm), sort_keys=True))
    except (Error, KeyError, ValueError, OSError) as error:
        # API field failures fail closed, but do not dump potentially sensitive data.
        print(str(error) if isinstance(error, Error) else "invalid or unavailable GitHub protection metadata", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
