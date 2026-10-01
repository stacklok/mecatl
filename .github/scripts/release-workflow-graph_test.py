#!/usr/bin/env python3
"""Check release workflow dependency and execution-image contracts."""

import re
from pathlib import Path
import sys


def test_release_workflow_graph_and_contract(release_path: Path, release_pr_path: Path, release_tag_path: Path) -> None:
    workflow = release_path.read_text(encoding="utf-8").splitlines()
    jobs = {}
    job_lines = {}
    current = None
    for line in workflow:
        match = re.fullmatch(r"  ([A-Za-z0-9_-]+):", line)
        if match:
            current = match.group(1)
            jobs[current] = []
            job_lines[current] = []
            continue
        if current is None:
            continue
        job_lines[current].append(line)
        match = re.fullmatch(r"    needs: (.+)", line)
        if not match:
            continue
        value = match.group(1).strip()
        if value.startswith("[") and value.endswith("]"):
            jobs[current] = [item.strip() for item in value[1:-1].split(",")]
        else:
            jobs[current] = [value]

    def reaches_validation(job, seen=None):
        if job == "validate-release-ref":
            return True
        seen = set() if seen is None else seen
        if job in seen:
            return False
        seen.add(job)
        return any(reaches_validation(dep, seen) for dep in jobs.get(job, []))

    for job, lines in job_lines.items():
        for producer in re.findall(r"needs\.([A-Za-z0-9_-]+)\.outputs\.", "\n".join(lines)):
            if producer not in jobs[job]:
                raise SystemExit(f"job {job} reads outputs without directly needing {producer}")

    validation = "\n".join(job_lines["validate-release-ref"])
    assert '    needs: guard' in validation
    assert '          ref: ${{ github.sha }}' in validation
    assert '        run: test "$(git rev-parse HEAD)" = "${GITHUB_SHA}"' in validation
    assert '''      - name: Assert guard and immutable run commits agree
        env:
          RELEASE_COMMIT: ${{ needs.guard.outputs.commit }}
        run: test "$(git rev-parse HEAD)" = "${RELEASE_COMMIT}"''' in validation

    for job in sorted(name for name in jobs if name == "publish" or name.startswith("publish-")):
        if not reaches_validation(job):
            raise SystemExit(f"publishing job {job} bypasses validate-release-ref")

    # The mecated image must receive the same completed microVM defaults as mecatui
    # before ko templates its linker flags. A whole-workflow string check misses this.
    publish = "\n".join(job_lines["publish"])
    assert "publish-microvm" in jobs["publish"]
    assert publish.index("pattern: microvm-default-*") < publish.index("MICROVM_RELEASE_DEFAULTS_B64=") < publish.index("ko build")
    assert "map({key:.platform,value:.}) | from_entries" in publish

    resolver = "\n".join(job_lines["resolve-brood-base"])
    assert 'BROOD_DISCOVERY_REFERENCE: ghcr.io/stacklok/brood-box/base:latest' in resolver
    assert 'reference="ghcr.io/stacklok/brood-box/base@${manifest}"' in resolver
    assert 'GOARCH=amd64 GOWORK=off go -C environment/microvm build' in resolver
    assert '-o "../../brood-tree/digest" ./cmd/mecatl-oci-tree-digest' in resolver
    assert resolver.index("mkdir -p brood-tree") < resolver.index("GOWORK=off go") < resolver.index("for arch in amd64 arm64; do")
    assert 'docker run --rm --platform linux/amd64' in resolver
    assert '"/release/digest" "${reference}" "/release/cache-${arch}" "linux/${arch}"' in resolver
    assert 'docker run --rm --platform "linux/${arch}"' not in resolver
    assert "setup-qemu" not in resolver and "binfmt" not in resolver

    validator = "sh .github/scripts/validate-execution-base-images.sh"
    execution = "\n".join(job_lines["publish-execution-images"])
    assert 'ref: "${{ needs.guard.outputs.commit }}"' in execution
    assert execution.index("actions/checkout@") < execution.index("docker/setup-buildx-action@") < execution.index(validator) < execution.index("docker/login-action@") < execution.index("docker/build-push-action@")
    assert "build-args:" not in execution
    assert "vars.EXECUTION_" not in execution
    assert "setup-qemu" not in execution
    release_pr = release_pr_path.read_text(encoding="utf-8")
    assert "vars.EXECUTION_" not in release_pr
    preflight = release_pr.split("  preflight:\n", 1)[1].split("  release-pr:\n", 1)[0]
    assert validator in preflight and "environment: release" not in preflight
    assert "ref: refs/heads/main" in preflight
    assert "persist-credentials: false" in preflight
    assert "contents: read" in preflight and "contents: write" not in preflight
    assert "secrets." not in preflight
    assert preflight.index("ref: refs/heads/main") < preflight.index("docker/setup-buildx-action@") < preflight.index(validator) < preflight.index("name: Build execution images without publishing")
    assert preflight.count('docker buildx build --platform linux/amd64,linux/arm64 --file "$dockerfile" --output=type=cacheonly .') == 1
    assert "    needs: preflight" in release_pr
    assert release_pr.index(validator) < release_pr.index("name: Refuse or clean up an in-flight release") < release_pr.index("name: Mint the release app token")
    release_tag = release_tag_path.read_text(encoding="utf-8")
    assert "timeout-minutes: 10" in release_tag
    assert release_tag.index("name: Decide whether to tag") < release_tag.index("name: Validate execution base images before tagging") < release_tag.index("name: Mint the release app token")
    assert "if: steps.decide.outputs.should_tag == 'true'\n        run: " + validator in release_tag
    assert "Build execution images before tagging" not in release_tag
    assert 'docker buildx build --platform linux/amd64,linux/arm64 --file "$dockerfile" --output=type=cacheonly .' not in release_tag


def main() -> None:
    test_release_workflow_graph_and_contract(
        Path(sys.argv[1]), Path(sys.argv[2]), Path(sys.argv[3])
    )


if __name__ == "__main__":
    main()
