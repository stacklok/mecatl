#!/usr/bin/env python3
"""Check each go.work member's candidate dependency closure, independently.

Uses the installed Go toolchain and runs each selected module's tests. Dependency
metadata uses temporary files unless --fix is explicit. This is not a publication
check: only exact workspace module identities are replaced by current source trees.
"""

import argparse
import codecs
from contextlib import contextmanager
import json
import os
from pathlib import Path
import re
import selectors
import signal
import subprocess
import sys
import tempfile
import time


class CheckError(Exception):
    pass


@contextmanager
def phase(label, enabled=True):
    start = time.monotonic()
    if enabled:
        print(f"START {label}", file=sys.stderr, flush=True)
    status = "FAIL"
    try:
        yield
        status = "OK"
    finally:
        if enabled:
            print(f"END {label}: {status} ({time.monotonic() - start:.1f}s)",
                  file=sys.stderr, flush=True)


def interrupted(signum, frame):
    # Unwind active runners so nested diagnostic commands also reap their groups.
    raise SystemExit(128 + signum)


def run(command, directory, env=None, timeout=1800, stream=False, cleanup_timeout=5):
    try:
        if not stream:
            result = subprocess.run(command, cwd=directory, env=env,
                                    capture_output=True, text=True, timeout=timeout)
        else:
            with phase(f"{directory}: {' '.join(map(str, command))}"), \
                    subprocess.Popen(command, cwd=directory, env=env, start_new_session=True,
                                     stdout=subprocess.PIPE, stderr=subprocess.PIPE) as process:
                captured = {process.stdout: [], process.stderr: []}
                decoders = {pipe: codecs.getincrementaldecoder("utf-8")("replace")
                            for pipe in captured}
                deadline = time.monotonic() + timeout
                try:
                    with selectors.DefaultSelector() as selector:
                        for pipe in captured:
                            selector.register(pipe, selectors.EVENT_READ)
                        while selector.get_map():
                            remaining = deadline - time.monotonic()
                            if remaining <= 0:
                                raise subprocess.TimeoutExpired(command, timeout)
                            for key, _ in selector.select(min(remaining, 1)):
                                data = os.read(key.fileobj.fileno(), 8192)
                                captured[key.fileobj].append(data)
                                sys.stderr.write(decoders[key.fileobj].decode(data, final=not data))
                                sys.stderr.flush()
                                if not data:
                                    selector.unregister(key.fileobj)
                        process.wait(timeout=max(0, deadline - time.monotonic()))
                    result = subprocess.CompletedProcess(command, process.returncode,
                        *(b"".join(captured[pipe]).decode("utf-8", "replace")
                          for pipe in (process.stdout, process.stderr)))
                finally:
                    # Kill the group even if its leader exited, not just the direct child.
                    try:
                        os.killpg(process.pid, signal.SIGTERM)
                        try:
                            process.wait(timeout=cleanup_timeout)
                        except subprocess.TimeoutExpired:
                            pass
                        os.killpg(process.pid, signal.SIGKILL)
                    except ProcessLookupError:
                        pass
                    process.wait()
                if result.returncode:
                    raise CheckError(f"{command[0]} exited {result.returncode}:\n"
                                     f"{result.stdout}{result.stderr}".rstrip())
    except subprocess.TimeoutExpired as error:
        raise CheckError(f"command timed out after {timeout}s: {command[0]}") from error
    if result.returncode:
        raise CheckError(f"{' '.join(map(str, command))}:\n{result.stdout}{result.stderr}".rstrip())
    return result.stdout


def go(directory, env, *args):
    diagnostics = env.get("MECATL_MODULE_DIAGNOSTICS") == "1" and args[0] in ("test", "build")
    if diagnostics and args[0] == "test":
        args = (args[0], "-v", *args[1:])
    # Escalate before the outer publication runner's five-second cleanup expires.
    return run(["go", *args], directory, env,
               timeout=1800 if diagnostics else None, stream=diagnostics, cleanup_timeout=1)


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

    go(directory, env, "test", "-mod=readonly", "-modfile=" + str(modfile),
       "-count=1", "-timeout=20m", "./...")
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
    parser.add_argument("--diagnostics", action="store_true", help="stream verbose tests and module timings (publication only)")
    parser.add_argument("--fix", action="store_true", help="apply safe tidy metadata repairs after all selected modules pass tests")
    args = parser.parse_args(argv)
    root = args.root
    if root is None:
        root = next((path for path in [Path.cwd(), *Path.cwd().parents]
                     if (path / "go.work").is_file()), None)
        if root is None:
            parser.error("no ancestor go.work; use --root")
    root = root.resolve()
    env = dict(os.environ, GOWORK="off", GOTOOLCHAIN="local", GOFLAGS="")
    # Ambient opt-in must not make ordinary candidate checks verbose.
    env.pop("MECATL_MODULE_DIAGNOSTICS", None)
    if args.diagnostics:
        env["MECATL_MODULE_DIAGNOSTICS"] = "1"
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
                with phase(label, args.diagnostics), \
                        tempfile.TemporaryDirectory(prefix="module-candidate-") as temporary:
                    differences, repair = candidate(identity, directory, original, modules,
                                                    snapshots, env, Path(temporary))
                print(label + (": metadata drift" if differences else ": OK"), flush=True)
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
    signal.signal(signal.SIGTERM, interrupted)
    sys.exit(main())
