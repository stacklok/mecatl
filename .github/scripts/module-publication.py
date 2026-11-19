#!/usr/bin/env python3
"""Bounded Go module publication: read-only checks and isolated API-only writers.

Run trusted tooling separately from --root (the candidate checkout). Writers accept
metadata as data and never check out or execute candidate code.
"""

import argparse
import base64
import importlib.util
import json
import os
from pathlib import Path
import re
import signal
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request


sys.dont_write_bytecode = True
SPEC = importlib.util.spec_from_file_location(
    "module_candidate", Path(__file__).resolve().with_name("module-candidate.py"))
candidate = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(candidate)
CheckError = candidate.CheckError
ORDER = (
    "agentdef", "engine", "contracts/gen/go/mecatl/driver", "provider/ssefilter",
    "internal/adaptersupport", "authn/oidc", "provider/anthropic",
    "provider/openai", "provider/openaichat", "adapters",
)
CONSUMERS = (".", "environment/microvm", "integration/microvm")
PREFIX = "github.com/stacklok/mecatl"
REPOSITORY = "https://github.com/stacklok/mecatl.git"
IMPORTS = {
    "agentdef": ("",),
    "engine": ("/agent",),
    "contracts/gen/go/mecatl/driver": ("/v1",),
    "provider/ssefilter": ("",),
    "internal/adaptersupport": ("/filewatch", "/soulbody"),
    "authn/oidc": ("",),
    "provider/anthropic": ("",),
    "provider/openai": ("",),
    "provider/openaichat": ("",),
    "adapters": ("/grpcdriver", "/jsonlstore", "/redisstore"),
}


run = candidate.run


def git(root, *args):
    return run(["git", *args], root).strip()


def sha(value):
    if not re.fullmatch(r"[0-9a-f]{40}", value):
        raise CheckError("expected a full lowercase source/parent/head SHA")
    return value


def identity(directory):
    return PREFIX if directory == "." else PREFIX + "/" + directory


def version(value, module):
    if not isinstance(value, str):
        raise CheckError("versions must be strings")
    match = re.fullmatch(r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)"
                         r"(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?", value)
    if not match or any(part.isdigit() and len(part) > 1 and part[0] == "0"
                        for part in (match[4] or "").split(".")):
        raise CheckError(f"noncanonical module version: {value}")
    # Go reserves these semver prereleases for synthesized, untagged revisions.
    if re.fullmatch(r"v[0-9]+\.(0\.0-|[0-9]+\.[0-9]+-([^+]*\.)?0\.)"
                    r"[0-9]{14}-[A-Za-z0-9]+", value):
        raise CheckError(f"Go pseudo-version cannot be a publication tag: {value}")
    suffix = re.search(r"/v([2-9]|[1-9][0-9]+)$", module)
    major = int(match[1])
    if (suffix and major != int(suffix[1])) or (not suffix and major > 1):
        raise CheckError(f"version major incompatible with {module}: {value}")


def request(raw):
    def unique(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise CheckError(f"duplicate module selection: {key}")
            result[key] = value
        return result
    values = json.loads(raw, object_pairs_hook=unique)
    if not isinstance(values, dict) or not values:
        raise CheckError("versions must be a nonempty directory/version JSON object")
    if values.keys() - set(ORDER):
        raise CheckError("unknown or consumer-only publication target")
    for directory, value in values.items():
        version(value, identity(directory))
    return {directory: values[directory] for directory in ORDER if directory in values}


def environment(temporary):
    """No ambient Go configuration, private bypasses, credentials, or warmed cache."""
    env = {"PATH": os.environ["PATH"], "GOENV": "off", "GOFLAGS": "",
           "GOWORK": "off", "GOTOOLCHAIN": "local", "GOTELEMETRY": "off",
           "GOPROXY": "https://proxy.golang.org", "GOSUMDB": "sum.golang.org",
           "GOPRIVATE": "", "GONOPROXY": "none", "GONOSUMDB": "none",
           "GOINSECURE": "", "GIT_TERMINAL_PROMPT": "0", "GIT_CONFIG_NOSYSTEM": "1",
           "MECATL_MODULE_DIAGNOSTICS": "1"}
    for key, name in (("HOME", "home"), ("GOPATH", "gopath"), ("GOCACHE", "build"),
                      ("GOMODCACHE", "modules"), ("TMPDIR", "tmp")):
        path = temporary / name
        path.mkdir()
        env[key] = str(path)
    return env


def inventory(root, env):
    modules, _ = candidate.inventory(root, env)
    paths = {str(directory.relative_to(root)): (name, directory, mod)
             for name, (directory, mod) in modules.items()}
    if set(paths) != set(ORDER + CONSUMERS):
        raise CheckError("workspace inventory drift: update the explicit publication order/consumers")
    for path, (name, directory, mod) in paths.items():
        if name != identity(path):
            raise CheckError(f"module identity drift: {path}: {name}")
        for filename in ("go.mod", "go.sum"):
            if (directory / filename).is_symlink():
                raise CheckError(f"symlink metadata is unsupported: {path}/{filename}")
        if path not in ORDER:
            continue
        if mod.get("Replace"):
            raise CheckError(f"publishable module has replacements: {path}")
        for dependency in candidate.requirements(mod):
            if dependency != PREFIX and not dependency.startswith(PREFIX + "/"):
                continue
            sibling = next((p for p in paths if identity(p) == dependency), None)
            if sibling not in ORDER or ORDER.index(sibling) >= ORDER.index(path):
                raise CheckError(f"unexpected dependency/cycle/order drift: {path} -> {dependency}")
    return paths


def metadata_paths():
    return {str(Path(directory) / name)
            for directory in ORDER + CONSUMERS for name in ("go.mod", "go.sum")}


def checkout(root, source, parent):
    sha(source)
    sha(parent)
    if Path(git(root, "rev-parse", "--show-toplevel")).resolve() != root:
        raise CheckError("--root must be the candidate checkout root")
    if git(root, "rev-parse", "HEAD") != parent:
        raise CheckError("checkout HEAD differs from expected parent")
    if git(root, "status", "--porcelain", "--untracked-files=all"):
        raise CheckError("candidate checkout must be clean")
    git(root, "merge-base", "--is-ancestor", source, parent)
    changed = set(git(root, "diff", "--name-only", source, parent).splitlines())
    if changed - metadata_paths():
        raise CheckError("parent differs from source outside known dependency metadata")


def check_changes(root, allowed=()):
    # Inspect index and worktree separately, including deletions and mode changes.
    changed = set()
    for args in (("diff", "--cached", "HEAD"), ("diff",)):
        changed.update(filter(None, run(
            ["git", "-c", "core.fileMode=true", *args, "--no-renames", "--name-only", "-z"], root).split("\0")))
    changed.update(filter(None, run(
        ["git", "ls-files", "--others", "--exclude-standard", "-z"], root).split("\0")))
    if changed - set(allowed):
        raise CheckError("candidate execution modified files outside allowed metadata")
    return changed


def check_metadata(original, desired, modules, pins):
    for key in ("Module", "Go", "Toolchain", "Replace"):
        if original.get(key) != desired.get(key):
            raise CheckError(f"tidy changed reviewed {key} directive(s)")
    before, after = candidate.requirements(original), candidate.requirements(desired)
    siblings = {identity(path) for path in modules}
    missing = (after.keys() - before.keys()) & siblings
    if missing:
        raise CheckError("missing new sibling require in reviewed metadata: " + ", ".join(sorted(missing)))
    for name, (exact, _) in after.items():
        if (name == PREFIX or name.startswith(PREFIX + "/")) and name not in siblings:
            raise CheckError(f"unexpected repository dependency: {name}")
        if name in siblings and name in before and name not in pins and exact != before[name][0]:
            raise CheckError(f"tidy changed an unselected sibling pin: {name}")
    for name, exact in pins.items():
        if after.get(name, (None,))[0] != exact:
            raise CheckError(f"requested exact pin did not survive tidy: {name}@{exact}")


def tidy(root, entry, modules, versions, env, standalone):
    name, directory, original = entry
    requirements = candidate.requirements(original)
    pins = {identity(path): exact for path, exact in versions.items()
            if identity(path) in requirements and identity(path) != name}
    if pins:
        candidate.go(directory, env, "mod", "edit",
                     *[f"-require={module}@{exact}" for module, exact in pins.items()])
    candidate.go(directory, env, "mod", "tidy")
    desired = candidate.parse_mod(directory, env, directory / "go.mod")
    check_metadata(original, desired, modules, pins)
    captured = {str((directory / filename).relative_to(root)): candidate.read_optional(directory / filename)
                for filename in ("go.mod", "go.sum")}
    candidate.go(directory, env, "mod", "tidy", "-diff")
    if standalone:
        candidate.go(directory, env, "build", "-mod=readonly", "./...")
        candidate.go(directory, env, "test", "-mod=readonly", "-count=1", "-timeout=20m", "./...")
    if any(candidate.read_optional(root / path) != data for path, data in captured.items()):
        raise CheckError("candidate execution modified files after metadata was tested")
    return captured


def artifact(root, parent, captured, destination):
    changed = check_changes(root, captured)
    if git(root, "rev-parse", "HEAD") != parent:
        raise CheckError("candidate execution moved HEAD")
    files = {}
    for path, data in sorted(captured.items()):
        target = root / path
        if target.is_symlink() or (target.exists() and target.stat().st_mode & 0o111):
            raise CheckError(f"metadata must be a nonexecutable regular file: {path}")
        if path.endswith("go.mod") and not target.is_file():
            raise CheckError(f"module metadata was deleted: {path}")
        if candidate.read_optional(target) != data:
            raise CheckError(f"tested metadata bytes changed before artifact capture: {path}")
        if path in changed:
            files[path] = None if data is None else data.decode("utf-8")
    # Create-only, after testing and source-change checks; never clobber candidate files.
    with destination.open("x", encoding="utf-8") as output:
        json.dump({"parent": parent, "files": files}, output, ensure_ascii=False)
        output.write("\n")
    return {"metadata": str(destination.resolve())}


def preflight(args, versions, env):
    root = args.root
    with candidate.phase("request validation"):
        checkout(root, args.source, args.source)
        # A stale local origin/main is not authority for main ancestry. Do not fetch or
        # mutate refs here: the runner must fetch the actual main tip before checking.
        remote = git(root, "ls-remote", "--exit-code", "origin", "refs/heads/main").split()
        if len(remote) != 2 or remote[1] != "refs/heads/main":
            raise CheckError("cannot resolve actual origin/main")
        git(root, "merge-base", "--is-ancestor", args.source, sha(remote[0]))
        inventory(root, env)
        for path, exact in versions.items():
            tag = f"refs/tags/{path}/{exact}"
            if git(root, "ls-remote", "origin", tag):
                raise CheckError(f"requested tag already exists: {tag}")
        runid, attempt = os.environ.get("GITHUB_RUN_ID", ""), os.environ.get("GITHUB_RUN_ATTEMPT", "")
        if not all(re.fullmatch(r"[1-9][0-9]*", value) for value in (runid, attempt)):
            raise CheckError("positive GITHUB_RUN_ID and GITHUB_RUN_ATTEMPT are required")
    with candidate.phase("workspace tests"):
        run(["task", "test:workspace"], root, dict(env, GOFLAGS="-v"), stream=True)
    command = [sys.executable, str(Path(candidate.__file__).resolve()), "--diagnostics", "--root", str(root)]
    for path in versions:
        command += ["--module", path]
    with candidate.phase("selected module candidate checks"):
        run(command, root, env, stream=True)
    with candidate.phase("post-test source integrity"):
        check_changes(root)
        if git(root, "rev-parse", "HEAD") != args.source:
            raise CheckError("candidate execution moved HEAD")
    return {"source": args.source, "versions": json.dumps(versions, separators=(",", ":")),
            "branch": f"module-publication/{runid}-{attempt}"}


def prepare(args, versions, env):
    root = args.root
    checkout(root, args.source, args.parent)
    modules = inventory(root, env)
    earlier = {path: exact for path, exact in versions.items()
               if ORDER.index(path) < ORDER.index(args.module)}
    captured = tidy(root, modules[args.module], modules, earlier, env, standalone=True)
    result = artifact(root, args.parent, captured, args.metadata)
    return dict(result, selected="true")


def reconcile(args, versions, env):
    root = args.root
    checkout(root, args.source, args.parent)
    modules = inventory(root, env)
    allowed = set()
    for path in ORDER + ("environment/microvm", ".", "integration/microvm"):
        entry = modules[path]
        if candidate.requirements(entry[2]).keys() & {identity(p) for p in versions}:
            tidy(root, entry, modules, versions, env, standalone=False)
            allowed.update(str(Path(path) / name) for name in ("go.mod", "go.sum"))
    # Later consumers (with development replacements) can observe earlier changes.
    # Verify the entire resulting metadata is stable, not just each intermediate tree.
    captured = {path: candidate.read_optional(root / path) for path in allowed}
    for path, (_, directory, original) in modules.items():
        if str(Path(path) / "go.mod") in allowed:
            candidate.go(directory, env, "mod", "tidy", "-diff")
            desired = candidate.parse_mod(directory, env, directory / "go.mod")
            pins = {identity(p): exact for p, exact in versions.items()
                    if identity(p) in candidate.requirements(original)}
            check_metadata(original, desired, modules, pins)
    return artifact(root, args.parent, captured, args.metadata)


def tag_identity(module, exact, head, downloaded, directory, env):
    if downloaded.get("Path") != module or downloaded.get("Version") != exact:
        raise CheckError("download resolved the wrong module/version")
    tag = "refs/tags/" + module.removeprefix(PREFIX + "/") + "/" + exact
    origin = downloaded.get("Origin") or {}
    if (origin.get("VCS") == "git" and origin.get("URL", "").removesuffix(".git")
            == REPOSITORY.removesuffix(".git") and origin.get("Ref") == tag):
        resolved = origin.get("Hash")
    else:
        # Public proxies need not include Origin. Resolve the canonical tag without
        # executing anything from the candidate or using the checkout's local refs.
        lines = run(["git", "ls-remote", "--exit-code", REPOSITORY, tag, tag + "^{}"],
                    directory, env, timeout=120).splitlines()
        refs = dict(line.split()[::-1] for line in lines)
        resolved = refs.get(tag + "^{}", refs.get(tag))
    if resolved != head:
        raise CheckError("public module tag does not identify the expected head")


def prove(args, versions, env, temporary):
    module, exact = identity(args.module), versions[args.module]
    directory = temporary / "consumer"
    directory.mkdir()
    # The internal import rule allows this identity; an example.com consumer cannot
    # import internal/adaptersupport, even when the module is publicly downloadable.
    (directory / "go.mod").write_text(
        f"module {PREFIX}/publication-smoke\n\ngo 1.27.3\n\nrequire {module} {exact}\n", encoding="utf-8")
    imports = "\n".join(f'_ "{module}{suffix}"' for suffix in IMPORTS[args.module])
    (directory / "smoke.go").write_text(f"package smoke\nimport (\n{imports}\n)\n", encoding="utf-8")
    for attempt in range(6):
        try:
            downloaded = json.loads(run(["go", "mod", "download", "-json", f"{module}@{exact}"],
                                        directory, env, timeout=120))
            if downloaded.get("Error"):
                raise CheckError(downloaded["Error"])
            break
        except CheckError:
            if attempt == 5:
                raise
            time.sleep(10)
    tag_identity(module, exact, args.head, downloaded, directory, env)
    mod = candidate.parse_mod(directory, env, Path(downloaded["GoMod"]))
    if mod["Module"]["Path"] != module or mod.get("Replace"):
        raise CheckError("downloaded module identity/replacements are invalid")
    candidate.go(directory, env, "mod", "tidy")
    resolved = json.loads(candidate.go(directory, env, "list", "-m", "-json", module))
    if resolved.get("Path") != module or resolved.get("Version") != exact or resolved.get("Replace"):
        raise CheckError("smoke consumer did not retain the exact public pin")
    candidate.go(directory, env, "build", "-mod=readonly", "./...")
    return {"head": args.head}


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class GitHub:
    def __init__(self):
        self.token = os.environ.get("GH_TOKEN", "")
        if not self.token:
            raise CheckError("writer requires GH_TOKEN")
        self.opener = urllib.request.build_opener(NoRedirect)

    def call(self, method, path, data=None, absent=False):
        # Neither the host nor repository can be supplied by an artifact/request.
        url = "https://api.github.com/repos/stacklok/mecatl/" + path
        request = urllib.request.Request(url, method=method,
            data=None if data is None else json.dumps(data).encode("utf-8"),
            headers={"Authorization": "Bearer " + self.token,
                     "Accept": "application/vnd.github+json", "Content-Type": "application/json",
                     "X-GitHub-Api-Version": "2022-11-28"})
        try:
            with self.opener.open(request, timeout=60) as response:
                return json.load(response)
        except urllib.error.HTTPError as error:
            if absent and error.code == 404:
                return None
            # Never include error bodies, headers, URLs or credentials in diagnostics.
            raise CheckError(f"GitHub API {method} failed (HTTP {error.code})") from None
        except (OSError, ValueError):
            raise CheckError("GitHub API transport or response failed") from None

    def ref(self, name):
        result = self.call("GET", "git/ref/" + urllib.parse.quote(name, safe="/"), absent=True)
        if result is None:
            return None
        if result.get("ref") != "refs/" + name or result["object"]["type"] != "commit":
            raise CheckError("expected exact lightweight commit ref")
        return sha(result["object"]["sha"])

    def ancestor(self, base, head):
        result = self.call("GET", f"compare/{sha(base)}...{sha(head)}")
        return result.get("status") in ("ahead", "identical")

    def commit_tree(self, commit):
        result = self.call("GET", "git/commits/" + sha(commit))
        tree = sha(result["tree"]["sha"])
        result_tree = self.call("GET", "git/trees/" + tree + "?recursive=1")
        if result_tree.get("truncated") is not False:
            raise CheckError("truncated GitHub tree")
        return result, tree, {entry["path"]: entry for entry in result_tree["tree"]
                              if entry["type"] != "tree"}

    def text(self, entry):
        blob = self.call("GET", "git/blobs/" + sha(entry["sha"]))
        return base64.b64decode(blob["content"]).decode("utf-8")


def notice(message):
    print(message, flush=True)
    if os.environ.get("GITHUB_STEP_SUMMARY"):
        with open(os.environ["GITHUB_STEP_SUMMARY"], "a", encoding="utf-8") as summary:
            summary.write(message + "\n")


def write_metadata(args, versions, api):
    if not re.fullmatch(r"module-publication/[1-9][0-9]*-[1-9][0-9]*", args.branch):
        raise CheckError("invalid publication branch")
    if (args.metadata.is_symlink() or not args.metadata.is_file()
            or args.metadata.stat().st_size > 16 * 1024 * 1024):
        raise CheckError("invalid/oversized metadata artifact")
    def unique(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise CheckError("duplicate metadata key")
            result[key] = value
        return result
    data = json.loads(args.metadata.read_text(encoding="utf-8"), object_pairs_hook=unique)
    if (not isinstance(data, dict) or set(data) != {"parent", "files"}
            or data["parent"] != args.parent or not isinstance(data["files"], dict)):
        raise CheckError("invalid metadata artifact schema/parent")
    files = data["files"]
    allowed = ({f"{args.module}/go.mod", f"{args.module}/go.sum"}
               if args.command == "publish" else metadata_paths())
    if files.keys() - allowed:
        raise CheckError("artifact changed paths outside operation scope")
    for path, text in files.items():
        if (text is not None and (not isinstance(text, str) or len(text.encode("utf-8")) > 4 * 1024 * 1024)):
            raise CheckError("invalid metadata file bytes/size")
        if path.endswith("go.mod") and text is None:
            raise CheckError("cannot delete go.mod")
    main = api.ref("heads/main")
    if not main or not api.ancestor(args.source, main) or not api.ancestor(args.source, args.parent):
        raise CheckError("source must be on main and ancestor of parent")
    _, _, source = api.commit_tree(args.source)
    _, base, parent = api.commit_tree(args.parent)
    changed = {p for p in source.keys() | parent.keys() if source.get(p) != parent.get(p)}
    if changed - metadata_paths():
        raise CheckError("parent changed reviewed source")
    tip = api.ref("heads/" + args.branch)
    if tip != args.parent and not (tip is None and args.parent == args.source):
        raise CheckError("release branch differs from expected parent; inspect and recover manually")
    if args.command == "publish":
        tag = f"tags/{args.module}/{versions[args.module]}"
        if api.ref(tag) is not None:
            raise CheckError("requested tag already exists; inspect and recover manually")
    entries = []
    for path, text in sorted(files.items()):
        entry = {"path": path, "mode": "100644", "type": "blob"}
        if text is None:
            if path not in parent:
                continue
            entry["sha"] = None
        else:
            if (path in parent and parent[path]["mode"] == "100644"
                    and parent[path]["type"] == "blob" and api.text(parent[path]) == text):
                continue
            entry["content"] = text
        entries.append(entry)
    head = args.parent
    if entries:
        tree = api.call("POST", "git/trees", {"base_tree": base, "tree": entries})["sha"]
        head = sha(api.call("POST", "git/commits", {
            "message": "Publish module metadata: " + (args.module if args.command == "publish" else "adoption"),
            "tree": tree, "parents": [args.parent]})["sha"])
        notice("Created metadata commit: " + head)
    if tip is None:
        api.call("POST", "git/refs", {"ref": "refs/heads/" + args.branch, "sha": args.source})
        notice("Created branch: " + args.branch + " at " + args.source)
        tip = args.source
    # Recheck immediately before advancing; never force a ref update.
    if api.ref("heads/" + args.branch) != tip:
        raise CheckError("release branch moved during publication")
    if tip != head:
        api.call("PATCH", "git/refs/heads/" + args.branch, {"sha": head, "force": False})
        notice("Advanced branch: " + args.branch + " to " + head)
    if args.command == "publish":
        api.call("POST", "git/refs", {"ref": "refs/" + tag, "sha": head})
        notice("Published immutable tag: " + tag + " at " + head)
    else:
        tags = [api.ref(f"tags/{p}/{v}") for p, v in versions.items()]
        all_on_main = all(tagged and api.ancestor(tagged, main) for tagged in tags)
        if head == args.parent and all_on_main and api.ancestor(head, main):
            notice("No adoption PR needed: all release commits are already on main")
            return {"head": head}
        body = ("HUMAN MERGE COMMIT required. Do not squash or rebase: preserve every module tag's "
                "commit as an ancestor of main. No automatic merge.\n\nSource: " + args.source +
                "\n\nVersions: `" + json.dumps(versions, sort_keys=True) + "`")
        pull = api.call("POST", "pulls", {"title": "Adopt published Go module metadata", "base": "main",
                                         "head": args.branch, "body": body})
        notice("Opened adoption PR: " + str(pull["number"]))
    return {"head": head}


def outputs(values):
    lines = []
    for key, value in values.items():
        if "\n" in value or "\r" in value:
            raise CheckError("invalid multiline workflow output")
        lines.append(f"{key}={value}\n")
    if os.environ.get("GITHUB_OUTPUT"):
        with open(os.environ["GITHUB_OUTPUT"], "a", encoding="utf-8") as output:
            output.writelines(lines)
    else:
        print("".join(lines), end="")


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    for command in ("preflight", "prepare", "publish", "prove", "reconcile", "open-pr"):
        sub = commands.add_parser(command)
        sub.add_argument("--versions", required=True)
        if command != "prove":
            sub.add_argument("--source", required=True)
        if command in ("preflight", "prepare", "reconcile"):
            sub.add_argument("--root", type=Path, required=True)
        if command in ("prepare", "reconcile", "publish", "open-pr"):
            sub.add_argument("--parent", required=True)
            sub.add_argument("--metadata", type=Path, default=Path("metadata.json"))
        if command in ("prepare", "prove", "publish"):
            sub.add_argument("--module", choices=ORDER, required=True)
        if command in ("publish", "open-pr"):
            sub.add_argument("--branch", required=True)
        if command == "prepare":
            sub.add_argument("--selection-only", action="store_true")
        if command == "prove":
            sub.add_argument("--head", required=True)
    args = parser.parse_args(argv)
    try:
        versions = request(args.versions)
        for name in ("source", "parent", "head"):
            if hasattr(args, name):
                sha(getattr(args, name))
        if args.command in ("prepare", "prove") and args.module not in versions:
            outputs({"selected": "false", "head": args.parent if args.command == "prepare" else args.head})
            return 0
        if args.command == "prepare" and args.selection_only:
            outputs({"selected": "true", "artifact-name": "module-metadata-" + args.module.replace("/", "-")})
            return 0
        if args.command in ("publish", "open-pr"):
            if args.command == "publish" and args.module not in versions:
                raise CheckError("writer module is not selected")
            notice("Publication source: " + args.source + "; branch: " + args.branch +
                   "; versions: " + json.dumps(versions, sort_keys=True))
            outputs(write_metadata(args, versions, GitHub()))
            return 0
        if hasattr(args, "root"):
            args.root = args.root.resolve()
        with tempfile.TemporaryDirectory(prefix="module-publication-") as temporary, \
                candidate.phase(f"publication {args.command}"):
            temporary = Path(temporary)
            env = environment(temporary)
            if args.command == "prove":
                result = prove(args, versions, env, temporary)
            else:
                result = globals()[args.command](args, versions, env)
        outputs(result)
        return 0
    except (CheckError, OSError, ValueError, KeyError) as error:
        print(f"module-publication: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    signal.signal(signal.SIGTERM, candidate.interrupted)
    sys.exit(main())
