#!/usr/bin/env bash
# Drift guard for the TERMINAL leaf directories that macos-relevant-changes.sh
# treats as irrelevant to the macOS CI jobs. Those jobs cost ~10x a Linux minute,
# so the classifier skips them when a change touches only leaf packages that no
# macOS-job binary or test imports. That skip is sound ONLY while those packages
# stay OUTSIDE the darwin+freebsd build/test closure of what the jobs exercise.
#
# This guard recomputes that closure with `go list -deps` and fails if any
# allowlisted directory has crept into it (e.g. someone wired perf/kpi into
# mecated). It also cross-checks that every directory it guards is actually named
# in the classifier, so the two lists cannot silently drift apart.
#
# Requires Go + resolved modules, so it runs in the `build` CI job, not the
# offline shell-contract step. Fail-closed: any go list error exits non-zero.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/../.." && pwd)"
classifier="$here/macos-relevant-changes.sh"
mod="github.com/stacklok/mecatl"
cd "$root"

# TERMINAL directories the classifier skips (repository-relative). Keep in lockstep
# with the terminal-dir patterns in macos-relevant-changes.sh; the cross-check
# below enforces it. docs/ and the site are validated by docs-only-changes.sh and
# are not Go packages, so they are not part of this closure guard.
terminal_dirs=(
  cmd/mecademo
  cmd/mecak8s
  cmd/mecatequi
  examples
  perf
  e2e
  deploy
  docs/lint
)

# Package roots the three macOS jobs BUILD or TEST (see ci.yml: smoke-darwin-arm64,
# managed-temp-macos, sdk-macos-spawn). If a terminal dir is absent from the union
# closure of these under both target GOOSes, skipping the jobs for it is sound.
darwin_roots=(
  ./cmd/mecated ./cmd/mecatui ./internal/app
  ./internal/adapter/managedtemp ./internal/adapter/osfs
  ./internal/adapter/privatefile ./internal/adapter/authfile
  ./internal/adapter/permconfig ./internal/adapter/credentialstore
)
# managed-temp-macos also cross-compiles these for freebsd (`GOOS=freebsd go test -c`).
freebsd_roots=(./internal/adapter/osfs ./internal/app)

closure="$(mktemp)"
trap 'rm -f "$closure"' EXIT

# arm64 for darwin (the runner), amd64 for the freebsd cross-compile.
for r in "${darwin_roots[@]}"; do GOOS=darwin GOARCH=arm64 go list -deps "$r" >> "$closure"; done
for r in "${freebsd_roots[@]}"; do GOOS=freebsd GOARCH=amd64 go list -deps "$r" >> "$closure"; done
sort -u -o "$closure" "$closure"

failures=0

for dir in "${terminal_dirs[@]}"; do
  # (1) Cross-check: the classifier must actually skip this directory.
  if ! grep -Fq "$dir/" "$classifier"; then
    printf 'FAIL: %s is guarded here but not skipped by macos-relevant-changes.sh\n' "$dir" >&2
    failures=$((failures + 1))
  fi
  # (2) Closure check: the directory must be OUTSIDE the macOS-job build closure.
  if grep -q "^$mod/$dir\(/\|$\)" "$closure"; then
    printf 'FAIL: %s is now in the darwin/freebsd closure of a macOS job — skipping it is no longer sound.\n' "$dir" >&2
    printf '      Offending package(s):\n' >&2
    grep "^$mod/$dir\(/\|$\)" "$closure" | sed 's/^/        /' >&2
    printf '      Remove it from macos-relevant-changes.sh AND this guard, or break the new import.\n' >&2
    failures=$((failures + 1))
  fi
done

if [[ "$failures" -ne 0 ]]; then
  printf 'macOS closure guard: %d failure(s)\n' "$failures" >&2
  exit 1
fi
printf 'macOS closure guard: all %d terminal directories are outside the macOS-job closure\n' "${#terminal_dirs[@]}"
