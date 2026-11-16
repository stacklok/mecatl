#!/usr/bin/env python3
"""Read-only module release preview, standalone qualification and public proof.

No GitHub or ref writes. JSON batches map go.work directories to exact versions.
Run qualification only in unprivileged jobs: candidate Go tests execute code.
The caller owns reviewed-request authority and exact-SHA ordinary CI evidence.
"""

import argparse
from contextlib import contextmanager
import importlib.util
import io
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
import zipfile


HERE = Path(__file__).resolve().parent
# Preview must not leave Python cache artifacts in the source checkout.
sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location("module_candidate", HERE / "module-candidate.py")
candidate_helpers = importlib.util.module_from_spec(spec)
spec.loader.exec_module(candidate_helpers)
CheckError = candidate_helpers.CheckError
PUBLISHED = {"public", "generated", "support"}
ROLES = PUBLISHED | {"application", "runtime-artifact", "test"}
SEMVER = re.compile(r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?")
PSEUDO = re.compile(r"v[0-9]+\.[0-9]+\.[0-9]+-(?:[0-9A-Za-z.-]+\.)?[0-9]{14}-([0-9a-f]{12})")


def run(directory, *args, env=None):
    result = subprocess.run(args, cwd=directory, env=env, capture_output=True, text=True)
    if result.returncode:
        raise CheckError(f"{' '.join(args)}:\n{result.stdout}{result.stderr}".rstrip())
    return result.stdout


def git(root, *args, env=None):
    if args and args[0] == "diff":
        args = ("diff", "--no-ext-diff", "--no-textconv", *args[1:])
    return run(root, "git", *args, env=env).strip()


def version_key(version):
    match = SEMVER.fullmatch(version) if isinstance(version, str) else None
    if not match:
        raise CheckError(f"expected canonical exact version, got {version!r}")
    major, minor, patch, pre = match.groups()
    if pre and any(part.isdigit() and len(part) > 1 and part[0] == "0" for part in pre.split(".")):
        raise CheckError(f"noncanonical prerelease: {version}")
    return (int(major), int(minor), int(patch), pre is None,
            tuple((0, int(p)) if p.isdigit() else (1, p) for p in (pre or "").split(".")))


def validate_version(identity, version, release=False):
    key = version_key(version)
    suffix = re.search(r"/v([2-9][0-9]*)$", identity)
    if (suffix and key[0] != int(suffix[1])) or (not suffix and key[0] not in (0, 1)):
        raise CheckError(f"module major/path mismatch: {identity}@{version}")
    if release and PSEUDO.fullmatch(version):
        raise CheckError(f"release requests need a tag version, not a pseudo-version: {version}")
    return key


def load_json(path):
    def unique(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise CheckError(f"duplicate JSON key: {key}")
            result[key] = value
        return result
    return json.loads(Path(path).read_text(), object_pairs_hook=unique)


def public_environment(temporary):
    # GOENV=off also ignores persisted `go env -w` configuration. Do not let
    # ambient private-proxy exceptions or vendor/workspace flags provide proof.
    return dict(os.environ, GOWORK="off", GOENV="off", GOTOOLCHAIN="local",
                GOFLAGS="", GOPROXY="https://proxy.golang.org", GOSUMDB="sum.golang.org",
                GOPRIVATE="none", GONOPROXY="none", GONOSUMDB="none",
                GOMODCACHE=str(temporary / "cache"), GOCACHE=str(temporary / "build-cache"))


@contextmanager
def source_tree(root, sha):
    with tempfile.TemporaryDirectory(prefix="module-release-source-") as temporary:
        result = subprocess.run(["git", "archive", "--format=tar", sha], cwd=root,
                                capture_output=True)
        if result.returncode:
            raise CheckError("cannot archive candidate: " + result.stderr.decode())
        with tarfile.open(fileobj=io.BytesIO(result.stdout)) as archive:
            archive.extractall(temporary, filter="data")
        yield Path(temporary)


class Release:
    """Shared read-only Git/policy validation. `source` must be an exact SHA archive.

    Public CLI creates that archive itself; Python callers can use source_tree.
    No method infers a release version or expands a batch.
    """

    def __init__(self, root, source, sha, policy=None, env=None):
        self.root, self.source, self.sha = Path(root), Path(source), sha
        if not re.fullmatch(r"[0-9a-f]{40}", sha) or git(root, "rev-parse", sha + "^{commit}") != sha:
            raise CheckError("candidate must be an exact full commit SHA")
        self.policy = policy if policy is not None else load_json(HERE / "module-publication-policy.json")
        if self.policy.get("schema") != 1 or not isinstance(self.policy.get("namespace"), str):
            raise CheckError("unsupported publication policy")
        self.env = env if env is not None else dict(os.environ, GOWORK="off", GOENV="off",
                                                   GOTOOLCHAIN="local", GOFLAGS="")
        raw, self.snapshots = candidate_helpers.inventory(self.source, self.env)
        self.modules = {}
        self.by_identity = {}
        for identity, (directory, mod) in raw.items():
            path = str(directory.relative_to(self.source))
            entry = self.policy["modules"].get(path)
            if entry is None:
                raise CheckError(f"unclassified go.work member: {path}")
            if entry.get("role") not in ROLES:
                raise CheckError(f"unknown publication role: {path}")
            expected = self.policy["namespace"] + ("/" + path if path != "." else "")
            if identity != expected:
                raise CheckError(f"module identity mismatch: {path}: {identity}, expected {expected}")
            if entry["role"] in PUBLISHED:
                prefix = re.sub(r"/v[2-9][0-9]*$", "", path) + "/"
                if entry.get("tag_prefix") != prefix:
                    raise CheckError(f"wrong tag namespace for {path}")
                if not (HERE / "module-consumers" / entry["smoke"]).is_file():
                    raise CheckError(f"missing owned consumer fixture: {path}")
                if not (self.source / entry["policy"]).is_file():
                    raise CheckError(f"missing owning compatibility policy: {path}")
            self.modules[path] = dict(entry, identity=identity, mod=mod)
            self.by_identity[identity] = path
        extra = self.policy["modules"].keys() - self.modules.keys()
        if extra:
            raise CheckError("publication policy members absent from go.work: " + ", ".join(sorted(extra)))
        # Even unselected cycles are a broken inventory, not a release order.
        self.order(set(self.modules))

    def order(self, selected):
        done, active, ordered = set(), set(), []
        def visit(path):
            if path in active:
                raise CheckError(f"workspace dependency cycle at {path}")
            if path in done:
                return
            active.add(path)
            for identity in sorted(candidate_helpers.requirements(self.modules[path]["mod"])):
                dep = self.by_identity.get(identity)
                if dep in selected:
                    visit(dep)
            active.remove(path)
            done.add(path)
            ordered.append(path)
        for path in sorted(selected):
            visit(path)
        return ordered

    def main_ancestor(self, sha):
        try:
            git(self.root, "merge-base", "--is-ancestor", sha, "refs/remotes/origin/main")
        except CheckError as error:
            raise CheckError(f"{sha} is not on fetched origin/main") from error

    def tag_commit(self, path, version, expected=None):
        entry = self.modules[path]
        validate_version(entry["identity"], version)
        tag = entry["tag_prefix"] + version
        ref = "refs/tags/" + tag
        exists = subprocess.run(["git", "show-ref", "--verify", "--quiet", ref], cwd=self.root)
        if exists.returncode == 1:
            return None
        if exists.returncode:
            raise CheckError(f"cannot inspect {tag}")
        sha = git(self.root, "rev-parse", ref + "^{commit}")
        self.main_ancestor(sha)
        if expected is not None and sha != expected:
            raise CheckError(f"tag conflict: {tag} is {sha}, expected {expected}")
        manifest = path + "/go.mod"
        # Parse the tagged manifest rather than trusting a tag's spelling.
        with tempfile.TemporaryDirectory(prefix="module-release-identity-") as temporary:
            modfile = Path(temporary) / "go.mod"
            modfile.write_text(git(self.root, "show", sha + ":" + manifest) + "\n")
            actual = candidate_helpers.parse_mod(Path(temporary), self.env, modfile)["Module"]["Path"]
        if actual != entry["identity"]:
            raise CheckError(f"tag module identity mismatch: {tag}: {actual}")
        return sha

    def prerequisite(self, identity, version):
        path = self.by_identity[identity]
        if self.modules[path]["role"] not in PUBLISHED:
            raise CheckError(f"published module depends on excluded workspace member: {path}")
        sha = self.tag_commit(path, version)
        if sha:
            return sha
        pseudo = PSEUDO.fullmatch(version)
        if pseudo:
            sha = git(self.root, "rev-parse", "--verify", pseudo[1] + "^{commit}")
            self.main_ancestor(sha)
            return sha  # Public resolution validates timestamp/canonical identity later.
        raise CheckError(f"missing published prerequisite {path}@{version}; publish it or review an explicit batch/pin update")

    def no_replacements(self, mod, label):
        if mod.get("Replace"):
            raise CheckError(f"{label}: replacement leakage; publication requires declared public dependencies")

    def latest(self, path):
        prefix = self.modules[path]["tag_prefix"]
        versions = []
        for tag in git(self.root, "tag", "--list", prefix + "v*", "--merged", self.sha).splitlines():
            version = tag[len(prefix):]
            try:
                key = validate_version(self.modules[path]["identity"], version, release=True)
            except CheckError:
                continue
            versions.append((key, version))
        return max(versions)[1] if versions else None

    def preview(self, batch):
        if not isinstance(batch, dict) or not batch:
            raise CheckError("batch must be a nonempty module-directory to exact-version JSON object")
        for path, version in batch.items():
            if path not in self.modules or self.modules[path]["role"] not in PUBLISHED:
                raise CheckError(f"not a publishable go.work member: {path}")
            validate_version(self.modules[path]["identity"], version, release=True)
        self.main_ancestor(self.sha)
        releases = []
        for path in self.order(set(batch)):
            entry, version = self.modules[path], batch[path]
            self.no_replacements(entry["mod"], path)
            existing = self.tag_commit(path, version, self.sha)
            latest = self.latest(path)
            if latest and not existing and version_key(version) <= version_key(latest):
                raise CheckError(f"{path}@{version} must be newer than {latest}")
            base = self.tag_commit(path, latest) if latest else None
            if base:
                changes = git(self.root, "diff", "--name-only", base, self.sha, "--", path).splitlines()
            else:
                changes = git(self.root, "ls-tree", "-r", "--name-only", self.sha, "--", path).splitlines()
            # Nested go.work members are not part of their parent's module zip.
            changes = [p for p in changes if not any(
                other != path and other.startswith(path + "/") and p.startswith(other + "/")
                for other in self.modules)]
            pins, updates = {}, {}
            for identity, (pin, _) in candidate_helpers.requirements(entry["mod"]).items():
                dep = self.by_identity.get(identity)
                if dep is None:
                    if identity == self.policy["namespace"] or identity.startswith(self.policy["namespace"] + "/"):
                        raise CheckError(f"unknown same-repository prerequisite: {identity}")
                    continue
                pins[dep] = pin
                if dep in batch:
                    if version_key(batch[dep]) < version_key(pin):
                        raise CheckError(f"batch would downgrade {path}'s {dep} pin")
                    if batch[dep] != pin:
                        updates[dep] = {"from": pin, "to": batch[dep]}
                else:
                    self.prerequisite(identity, pin)
            releases.append(dict(module=path, identity=entry["identity"], version=version,
                                 tag=entry["tag_prefix"] + version, existing_tag=existing,
                                 latest=latest, base_commit=base, changes=changes,
                                 source_diff=git(self.root, "diff", base, self.sha, "--", *changes) if base and changes else None,
                                 policy=entry["policy"],
                                 api_changes=[p for p in changes if p.startswith(entry.get("api", "\0") + "/")],
                                 changelog=entry.get("changelog"), pins=pins, consumer_updates=updates,
                                 human_semver_review_required=True))
        return dict(schema=1, candidate=self.sha, order=[r["module"] for r in releases], releases=releases)

    def policy_check(self, release, env):
        entry = self.modules[release["module"]]
        changelog = entry.get("changelog")
        if changelog:
            text = (self.source / changelog).read_text()
            if not re.search(r"^### (Added|Changed|Deprecated|Removed|Fixed|Security)$", text, re.MULTILINE):
                raise CheckError("missing owning changelog classification")
        if release["api_changes"]:
            if changelog not in release["changes"]:
                raise CheckError("API snapshot changes require an owning changelog classification")
            text = (self.source / changelog).read_text()
            if not re.search(r"^### (Added|Changed|Deprecated|Removed)$", text, re.MULTILINE):
                raise CheckError("missing API changelog classification")
            if release["latest"] and version_key(release["version"])[:2] <= version_key(release["latest"])[:2]:
                raise CheckError("API surface changes require at least a minor release")
        if release["module"] == "engine":
            # Existing owning freshness gate; separate from standalone engine
            # closure below. Root tooling intentionally knows the candidate engine.
            run(self.source, "go", "test", "-count=1", "./internal/apicheck", env=env)

    def verify_prerequisites(self, mod, env, updates=None, verified=None, active=None):
        """Prove exact existing public pins (or explicit preparation overrides).

        Evidence is invocation-local, never an editable success flag. Every exact
        module/version/commit gets its own fresh module cache, dependency first.
        """
        verified = {} if verified is None else verified
        active = set() if active is None else active
        requirements = {identity: pin for identity, (pin, _) in candidate_helpers.requirements(mod).items()}
        for path, version in (updates or {}).items():
            requirements[self.modules[path]["identity"]] = version
        for identity, version in sorted(requirements.items()):
            path = self.by_identity.get(identity)
            if path is None:
                if identity == self.policy["namespace"] or identity.startswith(self.policy["namespace"] + "/"):
                    raise CheckError(f"unknown same-repository prerequisite: {identity}")
                continue
            sha = self.prerequisite(identity, version)
            key = (path, version, sha)
            if key in active:
                raise CheckError(f"public prerequisite cycle: {path}@{version}")
            if key in verified:
                continue
            with tempfile.TemporaryDirectory(prefix="module-prerequisite-") as temporary:
                temporary = Path(temporary)
                proof_env = dict(env, GOMODCACHE=str(temporary / "cache"),
                                 GOCACHE=str(temporary / "build-cache"))
                self._prove(path, version, sha, proof_env, temporary, verified, active)
        return verified

    def qualify(self, batch, env):
        report = self.preview(batch)
        verified = {}
        for release in report["releases"]:
            path = release["module"]
            entry = self.modules[path]
            self.verify_prerequisites(entry["mod"], env, verified=verified)
            self.policy_check(release, env)
            standalone(self.source / path, env)
        candidate_helpers.verify_unchanged(self.snapshots)
        return dict(report, stage="qualified", public_prerequisites=list(verified.values()))

    def prove(self, path, version, env, temporary):
        if path not in self.modules or self.modules[path]["role"] not in PUBLISHED:
            raise CheckError(f"not publishable: {path}")
        validate_version(self.modules[path]["identity"], version, release=True)
        if not self.tag_commit(path, version, self.sha):
            raise CheckError(f"missing exact tag: {self.modules[path]['tag_prefix']}{version}")
        verified = {}
        result = self._prove(path, version, self.sha, env, temporary, verified, set())
        return dict(result, public_prerequisites=[value for key, value in verified.items()
                                                if key != (path, version, self.sha)])

    def _prove(self, path, version, sha, env, temporary, verified, active):
        key = (path, version, sha)
        if key in active:
            raise CheckError(f"public prerequisite cycle: {path}@{version}")
        active = active | {key}
        cache = Path(env["GOMODCACHE"])
        if cache.exists() and any(cache.iterdir()):
            raise CheckError("public proof requires a fresh empty module cache")
        entry = self.modules[path]
        consumer = temporary / "consumer"
        consumer.mkdir()
        # Go's internal visibility is import-path based. Support is not a public
        # extension API; this owned same-repository-path consumer is legitimate.
        consumer_identity = (self.policy["namespace"] + "/publicationcheck" if entry["role"] == "support"
                             else "example.com/mecatl-publicationcheck")
        (consumer / "go.mod").write_text(f"module {consumer_identity}\n\ngo {entry['mod']['Go']}\n\nrequire {entry['identity']} {version}\n")
        shutil.copyfile(HERE / "module-consumers" / entry["smoke"], consumer / "smoke_test.go")
        downloaded = json.loads(run(consumer, "go", "mod", "download", "-json",
                                    entry["identity"] + "@" + version, env=env))
        downloaded_mod = self.check_download(downloaded, path, version, sha, env)
        self.verify_prerequisites(downloaded_mod, env, verified=verified, active=active)
        run(consumer, "go", "mod", "tidy", env=env)
        # go list emits consecutive JSON documents, not a JSON array.
        graph = json_stream(run(consumer, "go", "list", "-m", "-json", "all", env=env))
        selected = {m["Path"]: m for m in graph}
        if selected.get(entry["identity"], {}).get("Version") != version:
            raise CheckError("consumer MVS changed the requested exact version")
        for module in graph:
            if module.get("Replace"):
                raise CheckError("public consumer replacement leakage")
            dep = self.by_identity.get(module["Path"])
            if dep is not None:
                pin = module["Version"]
                dep_sha = self.prerequisite(module["Path"], pin)
                data = json.loads(run(consumer, "go", "mod", "download", "-json", module["Path"] + "@" + pin, env=env))
                self.check_download(data, dep, pin, dep_sha, env)
                if (dep, pin, dep_sha) != key:
                    self.verify_prerequisites({"Require": [{"Path": module["Path"], "Version": pin}]},
                                              env, verified=verified, active=active)
        run(consumer, "go", "mod", "verify", env=env)
        run(consumer, "go", "test", "-mod=readonly", "-count=1", "-timeout=20m", "./...", env=env)
        # Particularly important for support: external imports alone do not prove
        # the implementation module's own dependency/test closure.
        standalone_source = temporary / "standalone"
        shutil.copytree(downloaded["Dir"], standalone_source)
        for file in standalone_source.rglob("*"):
            if file.is_file():
                file.chmod(file.stat().st_mode | 0o200)
        standalone(standalone_source, env)
        candidate_helpers.verify_unchanged(self.snapshots)
        result = dict(schema=1, stage="public-verified", candidate=sha, module=path,
                      version=version, sum=downloaded["Sum"], go_mod_sum=downloaded["GoModSum"])
        verified[key] = result
        return result

    def check_zip(self, data, path, version, sha):
        prefix = self.modules[path]["identity"] + "@" + version + "/"
        tree = {}
        for line in git(self.root, "ls-tree", "-r", sha, "--", path).splitlines():
            metadata, name = line.split("\t", 1)
            if metadata.startswith("100"):
                tree[name[len(path) + 1:]] = name
        nested = [name[:-len("go.mod")] for name in tree if name.endswith("/go.mod")]
        required = {name for name in tree if (name.endswith(".go") or name == "go.mod")
                    and "vendor" not in name.split("/")
                    and not any(name.startswith(directory) for directory in nested)}
        seen = set()
        with zipfile.ZipFile(data["Zip"]) as archive:
            for member in archive.infolist():
                if member.is_dir():
                    continue
                if not member.filename.startswith(prefix):
                    raise CheckError("public zip module identity mismatch")
                name = member.filename[len(prefix):]
                if name in seen or ".." in name.split("/") or name.startswith("/"):
                    raise CheckError("invalid public zip member")
                seen.add(name)
                source = tree.get(name, "LICENSE" if name == "LICENSE" else None)
                if source is None:
                    raise CheckError(f"public zip contains file absent from tag: {name}")
                blob = subprocess.run(["git", "show", sha + ":" + source], cwd=self.root, capture_output=True)
                if blob.returncode or blob.stdout != archive.read(member):
                    raise CheckError(f"public zip/tag source mismatch: {name}")
        if required - seen:
            raise CheckError("public zip omits tagged Go source: " + ", ".join(sorted(required - seen)))
        expected_mod = subprocess.run(["git", "show", sha + ":" + path + "/go.mod"], cwd=self.root, capture_output=True)
        if expected_mod.returncode or expected_mod.stdout != Path(data["GoMod"]).read_bytes():
            raise CheckError("public go.mod differs from exact tag")

    def check_download(self, data, path, version, sha, env):
        entry = self.modules[path]
        if data.get("Error") or data.get("Path") != entry["identity"] or data.get("Version") != version:
            raise CheckError(f"public download identity/version mismatch: {path}@{version}")
        if not data.get("Sum", "").startswith("h1:") or not data.get("GoModSum", "").startswith("h1:"):
            raise CheckError("public download lacks checksum evidence")
        origin = data.get("Origin")
        if origin is not None:
            if origin.get("Hash") != sha or origin.get("VCS") != "git" or origin.get("URL", "").removesuffix(".git") != "https://" + self.policy["namespace"]:
                raise CheckError(f"public origin metadata mismatches exact tag commit: {path}@{version}")
            if origin.get("Subdir", path) != path:
                raise CheckError("public origin metadata has wrong module subdirectory")
            tag_ref = "refs/tags/" + entry["tag_prefix"] + version
            if not PSEUDO.fullmatch(version) and origin.get("Ref", tag_ref) != tag_ref:
                raise CheckError("public origin metadata has wrong tag namespace/version")
        # Proxy .info files may legitimately omit Origin. Prove source equality
        # independently, not just an unauthenticated claim in proxy metadata.
        self.check_zip(data, path, version, sha)
        mod = candidate_helpers.parse_mod(Path(data["GoMod"]).parent, env, Path(data["GoMod"]))
        if mod["Module"]["Path"] != entry["identity"]:
            raise CheckError(f"public go.mod identity mismatch: {path}")
        self.no_replacements(mod, path)
        for identity, (pin, _) in candidate_helpers.requirements(mod).items():
            if identity in self.by_identity:
                self.prerequisite(identity, pin)
            elif identity == self.policy["namespace"] or identity.startswith(self.policy["namespace"] + "/"):
                raise CheckError(f"unknown public same-repository prerequisite: {identity}")
        return mod


def json_stream(text):
    decoder, values = json.JSONDecoder(), []
    while text.strip():
        text = text.lstrip()
        value, end = decoder.raw_decode(text)
        values.append(value)
        text = text[end:]
    return values


def standalone(directory, env):
    mod = candidate_helpers.parse_mod(directory, env, directory / "go.mod")
    if mod.get("Replace"):
        raise CheckError("standalone replacement leakage")
    run(directory, "go", "mod", "tidy", "-diff", env=env)
    run(directory, "go", "build", "-mod=readonly", "./...", env=env)
    run(directory, "go", "test", "-mod=readonly", "-count=1", "-timeout=20m", "./...", env=env)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("inventory", "preview", "qualify", "prove"))
    parser.add_argument("--root", type=Path, default=Path.cwd())
    parser.add_argument("--candidate", required=True, help="exact full main commit SHA")
    parser.add_argument("--batch", type=Path, help="JSON object mapping go.work directories to exact versions")
    parser.add_argument("--module", help="go.work directory for public proof")
    parser.add_argument("--version", help="exact tagged version for public proof")
    args = parser.parse_args(argv)
    if not re.fullmatch(r"[0-9a-f]{40}", args.candidate):
        parser.error("--candidate must be a full commit SHA")
    try:
        with source_tree(args.root, args.candidate) as source, tempfile.TemporaryDirectory(prefix="module-release-proof-") as temporary:
            temporary = Path(temporary)
            env = public_environment(temporary)
            release = Release(args.root.resolve(), source, args.candidate)
            if args.command == "inventory":
                result = dict(schema=1, candidate=args.candidate, modules={p: {k: v for k, v in m.items() if k != "mod"} for p, m in release.modules.items()})
            elif args.command in ("preview", "qualify"):
                if not args.batch:
                    parser.error("--batch is required")
                batch = load_json(args.batch)
                result = release.preview(batch) if args.command == "preview" else release.qualify(batch, env)
            else:
                if not args.module or not args.version:
                    parser.error("prove requires --module and --version")
                result = release.prove(args.module, args.version, env, temporary)
            print(json.dumps(result, indent=2, sort_keys=True))
        return 0
    except (CheckError, OSError, ValueError, KeyError, TypeError) as error:
        print(f"module-release: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
