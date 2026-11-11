#!/usr/bin/env python3
"""Check each go.work member's candidate dependency closure, independently.

Uses the installed Go toolchain, never executes compiled tests, and never writes
checkout metadata unless --fix is explicit. This is not a publication check:
only exact workspace module identities are replaced by current source trees.
"""

import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile


class CheckError(Exception):
    pass


def go(directory, env, *args):
    result = subprocess.run(["go", *args], cwd=directory, env=env,
                            capture_output=True, text=True)
    if result.returncode:
        raise CheckError(f"go {' '.join(args)}:\n{result.stdout}{result.stderr}".rstrip())
    return result.stdout


def parse_mod(directory, env, path):
    return json.loads(go(directory, env, "mod", "edit", "-json", str(path)))


def read_optional(path):
    return path.read_bytes() if path.exists() else None


def inventory(root, env):
    work = root / "go.work"
    snapshots = {work: work.read_bytes()}
    parsed = json.loads(go(root, env, "work", "edit", "-json", str(work)))
    modules = {}
    directories = set()
    for use in parsed.get("Use") or []:
        directory = (root / use["DiskPath"]).resolve()
        if not directory.is_relative_to(root):
            raise CheckError(f"workspace module outside checkout: {directory}")
        for name in ("go.mod", "go.sum"):
            path = directory / name
            snapshots[path] = read_optional(path)
        mod = parse_mod(directory, env, directory / "go.mod")
        identity = mod["Module"]["Path"]
        if identity in modules or directory in directories:
            raise CheckError(f"duplicate workspace module identity/path: {identity} ({directory})")
        directories.add(directory)
        modules[identity] = (directory, mod)
    if not modules:
        raise CheckError("go.work has no modules")
    return modules, snapshots


def replacement_key(replacement):
    old = replacement["Old"]
    return old["Path"] + ("@" + old["Version"] if old.get("Version") else "")


def replacement_flag(replacement, directory=None):
    new = replacement["New"]
    target = new["Path"]
    if new.get("Version"):
        target += "@" + new["Version"]
    elif directory is not None:
        # -modfile lives outside the source tree. Never depend on Go's implicit
        # base for relative replacements; repair restores the original spelling.
        target = str((directory / target).resolve())
    return "-replace=" + replacement_key(replacement) + "=" + target


def requirements(mod):
    return {entry["Path"]: (entry["Version"], entry.get("Indirect", False))
            for entry in mod.get("Require") or []}


def minimum(version):
    if not version:
        return (0, 0, 0, 0, 0)
    match = re.fullmatch(r"(?:go)?(\d+)\.(\d+)(?:\.(\d+))?(?:(beta|rc)(\d+))?", version)
    if not match:
        raise CheckError(f"unsupported Go/toolchain minimum: {version}")
    major, minor, patch, prerelease, number = match.groups()
    return (int(major), int(minor), int(patch or 0),
            {"beta": 0, "rc": 1, None: 2}[prerelease], int(number or 0))


def effective_toolchain(mod):
    return max(minimum(mod.get("Go")), minimum(
        None if mod.get("Toolchain") == "default" else mod.get("Toolchain")))


def sums(data, siblings):
    entries = set()
    for line in (data or b"").decode().splitlines():
        fields = tuple(line.split())
        if fields and fields[0] not in siblings:
            entries.add(fields)
    return entries


def candidate(identity, directory, original, modules, snapshots, env, temporary):
    modfile = temporary / "candidate.mod"
    sumfile = modfile.with_suffix(".sum")
    modfile.write_bytes(snapshots[directory / "go.mod"])
    original_sum = snapshots[directory / "go.sum"]
    if original_sum is not None:
        sumfile.write_bytes(original_sum)
    flags = []
    for replacement in original.get("Replace") or []:
        if replacement["Old"]["Path"] in modules:
            flags.append("-dropreplace=" + replacement_key(replacement))
        else:
            flags.append(replacement_flag(replacement, directory))
    flags += [f"-replace={name}={source}" for name, (source, _) in modules.items()
              if name != identity]
    if flags:
        go(directory, env, "mod", "edit", *flags, str(modfile))
    go(directory, env, "mod", "tidy", "-modfile=" + str(modfile))
    desired = parse_mod(directory, env, modfile)
    before, after = requirements(original), requirements(desired)
    differences = []
    missing_siblings = []
    for name in sorted(before.keys() | after.keys()):
        old, new = before.get(name), after.get(name)
        if name in modules and old is None and new is not None:
            missing_siblings.append(name)
        if old is None or new is None or old[1] != new[1] or (name not in modules and old[0] != new[0]):
            differences.append(f"require {name}: {old} -> {new} (version, indirect)")
    if minimum(original.get("Go")) != minimum(desired.get("Go")):
        differences.append(f"go minimum: {original.get('Go')} -> {desired.get('Go')}")
    if effective_toolchain(original) != effective_toolchain(desired):
        differences.append(f"toolchain minimum: {original.get('Toolchain') or original.get('Go')}"
                           f" -> {desired.get('Toolchain') or desired.get('Go')}")
    required_sums = sums(read_optional(sumfile), modules)
    for entry in sorted(required_sums - sums(original_sum, modules)):
        differences.append("missing required sum: " + " ".join(entry))

    # -run alone still runs init/TestMain. -exec=true compiles every package and
    # test binary but substitutes a successful executable for the test binary.
    go(directory, env, "test", "-mod=readonly", "-modfile=" + str(modfile),
       "-exec=true", "-run=^$", "./...")
    if missing_siblings:
        raise CheckError("\n".join(differences) + "\nCannot repair missing sibling require: "
                         + ", ".join(missing_siblings)
                         + "; declare a real published version explicitly (no synthetic version will be saved).")
    if not differences:
        return differences, {}

    # Remove all temporary overrides, restore user replacements verbatim in
    # meaning/spelling, and retain every surviving sibling's declared version.
    flags = ["-dropreplace=" + replacement_key(entry)
             for entry in desired.get("Replace") or []]
    flags += [replacement_flag(entry) for entry in original.get("Replace") or []]
    flags += [f"-require={name}@{before[name][0]}" for name in after
              if name in modules and name in before]
    if flags:
        go(directory, env, "mod", "edit", *flags, str(modfile))
    # go mod edit -require retains tidy's indirect classification.
    repaired = modfile.read_bytes()
    retained = {tuple(line.split()) for line in (original_sum or b"").decode().splitlines()
                if line.strip()}
    merged = retained | required_sums
    sum_bytes = ("".join(" ".join(entry) + "\n" for entry in sorted(merged))).encode()
    changes = {directory / "go.mod": repaired}
    if sum_bytes or original_sum is not None:
        changes[directory / "go.sum"] = sum_bytes
    return differences, changes


def verify_unchanged(snapshots):
    for path, expected in snapshots.items():
        if read_optional(path) != expected:
            raise CheckError(f"metadata changed during check; refusing repair: {path}")


def apply(changes, snapshots):
    expected = dict(snapshots)
    verify_unchanged(expected)
    for path, content in changes.items():
        if content == snapshots[path]:
            continue
        # Stage on the same filesystem, preserving existing file permissions.
        with tempfile.NamedTemporaryFile(dir=path.parent, prefix=".candidate-", delete=False) as staged:
            temporary = Path(staged.name)
            try:
                staged.write(content)
                staged.flush()
                temporary.chmod(path.stat().st_mode & 0o777 if path.exists() else 0o644)
                verify_unchanged(expected)
                os.replace(temporary, path)
                expected[path] = content
            finally:
                temporary.unlink(missing_ok=True)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, help="checkout containing go.work (default: nearest ancestor)")
    parser.add_argument("--module", action="append", help="go.work module directory, e.g. . or engine; repeatable")
    parser.add_argument("--fix", action="store_true", help="apply safe tidy metadata repairs after all selected modules compile")
    args = parser.parse_args(argv)
    root = args.root
    if root is None:
        root = next((path for path in [Path.cwd(), *Path.cwd().parents]
                     if (path / "go.work").is_file()), None)
        if root is None:
            parser.error("no ancestor go.work; use --root")
    root = root.resolve()
    env = dict(os.environ, GOWORK="off", GOTOOLCHAIN="local", GOFLAGS="")
    try:
        modules, snapshots = inventory(root, env)
        paths = {str(directory.relative_to(root)): identity
                 for identity, (directory, _) in modules.items()}
        if args.module:
            unknown = set(args.module) - paths.keys()
            if unknown:
                raise CheckError("unknown go.work module directory: " + ", ".join(sorted(unknown)))
            selected = {paths[path] for path in args.module}
        else:
            selected = set(modules)
        failed = False
        dirty = False
        changes = {}
        for identity, (directory, original) in modules.items():
            if identity not in selected:
                continue
            label = f"{identity} ({directory.relative_to(root)})"
            try:
                with tempfile.TemporaryDirectory(prefix="module-candidate-") as temporary:
                    differences, repair = candidate(identity, directory, original, modules,
                                                    snapshots, env, Path(temporary))
                print(label + (": metadata drift" if differences else ": OK"))
                for difference in differences:
                    print("  " + difference)
                dirty |= bool(differences)
                changes.update(repair)
            except CheckError as error:
                print(f"{label}: FAIL\n{error}", file=sys.stderr)
                failed = True
        if failed:
            if args.fix:
                print("No repairs applied: a selected module failed.", file=sys.stderr)
            return 1
        if args.fix:
            apply(changes, snapshots)
            if changes:
                print("Repaired metadata; sibling versions and original replacements preserved.")
            return 0
        if dirty:
            print("Use --fix to apply safe metadata repairs; review the diff.", file=sys.stderr)
        return int(dirty)
    except (CheckError, OSError, ValueError, KeyError) as error:
        print(f"module-candidate: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
