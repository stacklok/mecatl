#!/usr/bin/env bash
# Classify a NUL-delimited list of changed repository-relative paths for the
# macOS CI jobs, which cost ~10x a Linux runner minute. It answers ONE question
# per category: could any changed path affect this category of macOS job?
#
# Usage:  ... | bash macos-relevant-changes.sh <go|sdk>
#
#   go   — the native Go macOS smokes (smoke-darwin-arm64, managed-temp-macos):
#          build the mecated/mecatui binaries and run Go tests. Irrelevant paths
#          are documentation, the docs/user-facing site, and the TypeScript SDK
#          frontend (pure TS cannot change a Go binary or Go test).
#   sdk  — the SDK macOS spawn smoke (sdk-macos-spawn): the TypeScript SDK spawns
#          mecated, so BOTH Go changes and sdk/typescript changes are relevant;
#          only documentation and the site are irrelevant.
#
# The allowlists are INVERSE and fail-closed: a path is "irrelevant" only when it
# provably cannot affect the category. Any path outside the allowlist — or any
# malformed, empty, or unterminated input — prints "true" (RUN the job). The
# script prints "false" (SKIP) only when the input is well-formed, non-empty, and
# EVERY path is irrelevant. It never evaluates a path as shell code.
#
# Beyond docs/site/SDK, the "irrelevant" set includes TERMINAL leaf directories —
# other command binaries, examples, the perf harness, e2e, deploy, and the
# docs-lint tool — proven OUTSIDE the darwin+freebsd build/test closure of every
# macOS job (see .github/scripts/macos-closure-guard.sh, which fails CI if any of
# them ever enters that closure, so this list cannot silently drift fail-open).
#
# Note on matching: POSIX `case` globs treat `*` as matching any string INCLUDING
# `/`, so `docs/*.md` matches `docs/adr/nested.md` too (same convention as
# docs-only-changes.sh). Suffix patterns keep non-Markdown assets under docs/
# (e.g. docs/architecture/*.yaml) OUT of the irrelevant set.
set -euo pipefail

category="${1:-}"

irrelevant() {
  # Unknown category: nothing is provably irrelevant, so fail closed to RUN.
  case "$category" in
    go|sdk) ;;
    *) return 1 ;;
  esac

  # Common to both categories: documentation, the docs/user-facing site, and the
  # terminal leaf packages that no macOS-job binary or test imports.
  case "$1" in
    README.md|docs/*.md|docs/*.mdx|user-docs/*|website/*)
      return 0 ;;
    cmd/mecademo/*|cmd/mecak8s/*|cmd/mecatequi/*|examples/*|perf/*|e2e/*|deploy/*|docs/lint/*)
      return 0 ;;
  esac

  # go-only: the TypeScript SDK frontend cannot affect a Go binary or Go test.
  # (The sdk category omits this — sdk-macos-spawn spawns mecated from the SDK.)
  if [[ "$category" == go ]]; then
    case "$1" in
      sdk/typescript/*) return 0 ;;
    esac
  fi

  return 1
}

count=0
path=""
while IFS= read -r -d '' path; do
  # A relevant path — or an empty record, which cannot name a repository path and
  # indicates malformed input — means the job must RUN.
  if [[ -z "$path" ]] || ! irrelevant "$path"; then
    printf 'true\n'
    exit 0
  fi
  count=$((count + 1))
done

# read returns non-zero at EOF. A non-empty remainder means the producer omitted
# the required NUL delimiter; an empty stream must not vacuously SKIP the job.
if [[ "$count" -eq 0 ]] || [[ -n "$path" ]]; then
  printf 'true\n'
  exit 0
fi

printf 'false\n'
