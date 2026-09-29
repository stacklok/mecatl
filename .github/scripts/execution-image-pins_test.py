#!/usr/bin/env python3
"""Exercise execution-image pin validation with isolated fixtures."""

import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile


def test_execution_image_pin_validation(root: Path, validator: Path) -> None:
    paths = [Path(f"build/execution-{role}/Dockerfile") for role in ("provider", "workload")]
    originals = {path: (root / path).read_text() for path in paths}
    (root / ".scratch").mkdir(exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="execution-image-test-", dir=root / ".scratch") as tmp:
        fixture = Path(tmp)
        script = fixture / ".github/scripts/validate-execution-base-images.sh"
        script.parent.mkdir(parents=True)
        shutil.copyfile(validator, script)
        for path in paths:
            (fixture / path).parent.mkdir(parents=True)
        bin_dir = fixture / "bin"
        bin_dir.mkdir()
        docker = bin_dir / "docker"
        docker.write_text('''#!/usr/bin/env python3
import json, os, sys
with open(os.environ["CALLS"], "a") as log:
    log.write(json.dumps(sys.argv[1:]) + "\\n")
assert sys.argv[1:5] == ["buildx", "imagetools", "inspect", "--raw"]
if os.environ.get("FAIL_IMAGE") == sys.argv[5]:
    sys.exit(1)
print(os.environ["MANIFEST"])
''')
        docker.chmod(0o755)
        calls = fixture / "calls"
        manifest = json.dumps({"manifests": [
            {"platform": {"os": "linux", "architecture": arch}}
            for arch in ("amd64", "arm64", "unknown")
        ]})
        env = dict(os.environ, PATH=f"{bin_dir}:{os.environ['PATH']}",
                   CALLS=str(calls), MANIFEST=manifest,
                   GO_IMAGE="ignored", PROVIDER_RUNTIME_IMAGE="ignored")

        def run(changes=None, *, succeeds=False, registry_calls=0, **overrides):
            for path, content in originals.items():
                (fixture / path).write_text((changes or {}).get(path, content))
            calls.write_text("")
            result = subprocess.run(["sh", str(script)], cwd=fixture / "bin",
                                    env=dict(env, **overrides), capture_output=True, text=True)
            assert (result.returncode == 0) == succeeds, result.stderr
            actual = [json.loads(line) for line in calls.read_text().splitlines()]
            assert len(actual) == registry_calls, actual
            return [call[-1] for call in actual]

        def pin(content, name):
            return next(line.removeprefix(f"ARG {name}=") for line in content.splitlines()
                        if line.startswith(f"ARG {name}="))

        expected = [pin(originals[paths[0]], "GO_IMAGE"),
                    pin(originals[paths[0]], "RUNTIME_IMAGE"),
                    pin(originals[paths[1]], "RUNTIME_IMAGE")]
        assert run(succeeds=True, registry_calls=3) == expected
        digest = "0" * 64
        for path, content in originals.items():
            # Native Dockerfile extraction resolves these global ARG-backed FROMs.
            assert "FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS build" in content
            assert "FROM ${RUNTIME_IMAGE}" in content
            assert "ARG TARGETOS\nARG TARGETARCH" in content
            assert "CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build" in content
            assert "\nRUN " not in content.split("FROM ${RUNTIME_IMAGE}", 1)[1]
            for name in ("GO_IMAGE", "RUNTIME_IMAGE"):
                default = f"ARG {name}={pin(content, name)}"
                for bad in ("", "example.invalid/image:latest",
                            f"example.invalid/image@sha256:{digest}",
                            "example.invalid/image:latest@sha256:abcd",
                            f"example.invalid/image:latest@sha256:{digest[:-1]}g",
                            f"example.invalid/image:latest@sha256:{'A' * 64}",
                            f"$(touch {fixture}/sourced)"):
                    run({path: content.replace(default, f"ARG {name}={bad}")})
                run({path: content.replace(default, f"ARG {name}")})
                run({path: content.replace(default, f"{default}\n{default}")})
                run({path: content.replace(default, f"{default}\nARG  {name}=example.invalid/image:latest")})
                run({path: content.replace(default, f"{default}\nARG\t{name}=example.invalid/image:latest")})
        assert not (fixture / "sourced").exists()
        run({paths[1]: originals[paths[1]].replace(expected[0],
             f"docker.io/library/golang:1.27@sha256:{digest}")})
        for index, image in enumerate(expected, 1):
            run(FAIL_IMAGE=image, registry_calls=index)
        for bad in ("not json", "{}", '{"manifests":[]}',
                    '{"manifests":[{"platform":{"os":"linux","architecture":"amd64"}}]}',
                    '{"manifests":[{"platform":{"os":"linux","architecture":"arm64"}}]}',
                    '{"manifests":[{"platform":{"os":"windows","architecture":"amd64"}},'
                    '{"platform":{"os":"linux","architecture":"arm64"}}]}',
                    '{"manifests":[{"platform":{"os":"linux","architecture":"amd64"}},'
                    '{"platform":{"os":"linux","architecture":"amd64"}},'
                    '{"platform":{"os":"linux","architecture":"arm64"}}]}'):
            run(MANIFEST=bad, registry_calls=1)
        workload = originals[paths[1]]
        assert " /mecatl-executor\n" in workload
        assert "USER 65532:65532" in workload
        assert "ENV HOME=/workspace TMPDIR=/tmp GOTMPDIR=/tmp" in workload
        assert 'ENTRYPOINT ["/bin/sh", "-c", "trap : TERM INT; sleep infinity & wait"]' in workload


def main() -> None:
    test_execution_image_pin_validation(Path(sys.argv[1]), Path(sys.argv[2]))
    print("execution image pin validation tests passed")


if __name__ == "__main__":
    main()
