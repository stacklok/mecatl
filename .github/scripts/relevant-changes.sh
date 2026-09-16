#!/usr/bin/env bash
# Classify a NUL-delimited list of changed repository-relative paths for the CI
# job families in ci.yml, so a change that provably cannot affect a family skips
# that family's runners. It answers ONE question per category: could any changed
# path affect this category of job?
#
# Usage:  ... | bash relevant-changes.sh <go|sdk|site>
#
#   go   — the Go build/test matrix (build, test-race-*, analysis/lint, vuln,
#          fuzz-smoke, engine-standalone, provider-standalone, api-compat).
#          Irrelevant paths are documentation, the docs/user-facing site, and the
#          TypeScript SDK frontend (pure TS cannot change a Go binary or Go test).
#          EVERYTHING else — including contracts/ (proto is the codegen source for
#          the Go bindings), cmd/, examples/, perf/, e2e/, and .github/ — is
#          relevant, because the Linux `go test ./...` / `task build` closure
#          covers them. (This is why this script is SEPARATE from
#          macos-relevant-changes.sh, whose `go` category excludes leaf dirs that
#          are outside the darwin build closure but INSIDE this one.)
#   sdk  — the pure-TypeScript SDK unit job (the `sdk` Node matrix). It runs
#          against the COMMITTED sdk/typescript/src/gen, so it is relevant to
#          sdk/typescript/ changes, to contracts/ (the proto that gen is generated
#          from), and to the CI-control files that DEFINE the job and the task
#          recipes it runs (.github/, Taskfile.yml) — a change there can alter the
#          job with no frontend change, so it fails closed and RUNs. The SDK jobs
#          that spawn/build mecated (sdk-integration, sdk-deno, sdk-browser,
#          slack-bot-example) are gated on go OR sdk in the workflow, not on this
#          category alone.
#   site — the Docusaurus build (the `user-docs` job). Relevant to website/ and
#          user-docs/ (the site content), to sdk/typescript/ (the job's
#          `task sdk:docs:check` verifies committed SDK reference pages generated
#          from the TS declarations), and to the CI-control files that define the
#          job and its task recipes (.github/, Taskfile.yml). Everything else is
#          irrelevant.
#
# The classification is fail-closed. Any malformed, empty, or unterminated input
# prints "true" (RUN the family) regardless of category, and an unknown category
# treats nothing as provably irrelevant. For the `go` category, any path outside
# the small irrelevant allowlist also prints "true" (RUN) — the expensive/critical
# path defaults to running. The `sdk` and `site` categories use small positive
# allowlists covering both their content directories AND the CI-control files
# (.github/, Taskfile.yml) that define the job and its task recipes, so a
# well-formed path outside the allowlist cannot affect them; the
# category-independent malformed-input backstop below still fails to RUN.
# The script prints "false" (SKIP) only when the input is well-formed, non-empty,
# and EVERY path is irrelevant. It never evaluates a path as shell code.
#
# Note on matching: POSIX `case` globs treat `*` as matching any string INCLUDING
# `/`, so `docs/*.md` matches `docs/adr/nested.md` too and `sdk/typescript/*`
# matches every path under it (same convention as docs-only-changes.sh and
# macos-relevant-changes.sh).
set -euo pipefail

category="${1:-}"

irrelevant() {
  case "$category" in
    go)
      # Documentation, the docs/user-facing site, and the TypeScript SDK frontend
      # cannot affect a Go binary or Go test. Anything else RUNs.
      case "$1" in
        README.md|docs/*.md|docs/*.mdx|user-docs/*|website/*|sdk/typescript/*)
          return 0 ;;
      esac
      return 1
      ;;
    sdk)
      # Pure-TS unit suite: relevant to the SDK frontend, the proto contracts its
      # committed bindings are generated from, and the CI-control files that define
      # the job and the task recipes it runs (a change there can alter the job with
      # no frontend change — fail closed and RUN).
      case "$1" in
        sdk/typescript/*|contracts/*|.github/*|Taskfile.yml) return 1 ;;
      esac
      return 0
      ;;
    site)
      # Docusaurus build: relevant to the site content, the SDK declarations its
      # generated reference pages are checked against, and the CI-control files
      # that define the job and the task recipes it runs.
      case "$1" in
        website/*|user-docs/*|sdk/typescript/*|.github/*|Taskfile.yml) return 1 ;;
      esac
      return 0
      ;;
    *)
      # Unknown category: nothing is provably irrelevant, so fail closed to RUN.
      return 1
      ;;
  esac
}

count=0
path=""
while IFS= read -r -d '' path; do
  # A relevant path — or an empty record, which cannot name a repository path and
  # indicates malformed input — means the family must RUN.
  if [[ -z "$path" ]] || ! irrelevant "$path"; then
    printf 'true\n'
    exit 0
  fi
  count=$((count + 1))
done

# read returns non-zero at EOF. A non-empty remainder means the producer omitted
# the required NUL delimiter; an empty stream must not vacuously SKIP the family.
if [[ "$count" -eq 0 ]] || [[ -n "$path" ]]; then
  printf 'true\n'
  exit 0
fi

printf 'false\n'
