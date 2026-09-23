#!/usr/bin/env bash
# Offline tests for macos-relevant-changes.sh. Fixtures are NUL-delimited exactly
# like `git diff --name-only -z --no-renames` in ci.yml. Recall the inverted
# sense versus docs-only-changes.sh: here "true" means RUN the macOS job and
# "false" means it can be SKIPPED.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
classifier="$here/macos-relevant-changes.sh"
fails=0

run() {
  local name="$1" category="$2" want="$3"; shift 3
  local got
  got="$(printf '%s\0' "$@" | bash "$classifier" "$category")"
  if [[ "$got" != "$want" ]]; then
    printf 'FAIL: %s — expected %q, got %q\n' "$name" "$want" "$got"
    fails=$((fails + 1))
  else
    printf 'ok: %s\n' "$name"
  fi
}

run_raw() {
  local name="$1" category="$2" want="$3" input="$4" got
  got="$(printf '%s' "$input" | bash "$classifier" "$category")"
  if [[ "$got" != "$want" ]]; then
    printf 'FAIL: %s — expected %q, got %q\n' "$name" "$want" "$got"
    fails=$((fails + 1))
  else
    printf 'ok: %s\n' "$name"
  fi
}

# --- go category: native Go macOS smokes ---------------------------------------
# Provably irrelevant paths skip: docs, the site, user-docs, and the TS SDK.
run "go: docs-only skips" go false README.md docs/intro.md docs/adr/0093-provider-modules.md docs/design/notes.mdx
run "go: user-docs + website skip" go false user-docs/intro.md user-docs/building/_category_.json website/package.json website/src/pages/index.tsx
run "go: SDK frontend skips (cannot affect a Go binary)" go false sdk/typescript/src/client.ts sdk/typescript/pnpm-lock.yaml
run "go: mixed irrelevant skips" go false docs/x.md sdk/typescript/src/a.ts website/b.md

# Any Go/build/config path runs the go smokes.
run "go: Go source runs" go true engine/agent/loop.go
run "go: go.mod/go.sum run" go true go.mod engine/go.sum
run "go: goreleaser + ko run (linker stamp / build shape)" go true .goreleaser.yaml .ko.yaml
run "go: Taskfile + workflow run (self-validation)" go true Taskfile.yml .github/workflows/ci.yml
run "go: docs non-Markdown assets run" go true docs/architecture/mecatl.modelith.yaml
run "go: mixed docs + Go runs" go true user-docs/intro.md internal/adapter/osfs/osfs.go

# --- terminal leaf dirs: outside the darwin closure of every macOS job ----------
# (guarded against drift by macos-closure-guard.sh). Irrelevant to BOTH categories.
for cat in go sdk; do
  run "$cat: other command binaries skip" "$cat" false cmd/mecademo/main.go cmd/mecak8s/main.go cmd/mecatequi/main.go
  run "$cat: examples skip" "$cat" false examples/first-agent/main.go examples/first-agent-tool/main.go
  run "$cat: perf harness skips" "$cat" false perf/kpi/capture.go perf/scenarios/loop_bench_test.go perf/cmd/allocsgate/main.go
  run "$cat: e2e + deploy skip" "$cat" false e2e/k8s/suite_test.go deploy/helm/mecak8s/values_test.go
  run "$cat: docs-lint tool skips" "$cat" false docs/lint/citations.go
  run "$cat: Studio apps/ workspace skips" "$cat" false apps/web/src/main.tsx apps/server/src/app.ts apps/pnpm-lock.yaml
  run "$cat: Studio apps/ + in-closure package runs" "$cat" true apps/server/src/app.ts internal/app/build.go
  # A change mixing a terminal dir with an in-closure package still RUNS.
  run "$cat: terminal dir + in-closure package runs" "$cat" true perf/kpi/capture.go internal/app/build.go
done
# mecated/mecatui themselves are IN the closure — never treat them as terminal.
run "go: cmd/mecated is in-closure (runs)" go true cmd/mecated/main.go
run "go: cmd/mecatui is in-closure (runs)" go true cmd/mecatui/main.go

# --- sdk category: the SDK macOS spawn smoke -----------------------------------
# The SDK spawns mecated, so BOTH Go and sdk/typescript changes run it; only docs
# and the site are irrelevant.
run "sdk: docs + site skip" sdk false README.md docs/x.md user-docs/intro.md website/a.md
run "sdk: SDK frontend RUNS (unlike the go category)" sdk true sdk/typescript/src/spawn.ts
run "sdk: Go change RUNS (spawns mecated)" sdk true cmd/mecated/main.go
run "sdk: mixed docs + SDK runs" sdk true docs/x.md sdk/typescript/src/spawn.ts

# --- fail-closed / safety across categories ------------------------------------
for cat in go sdk; do
  run_raw "$cat: empty input fails closed to RUN" "$cat" true ''
  run_raw "$cat: unterminated input fails closed to RUN" "$cat" true 'docs/x.md'
  run "$cat: empty NUL record fails closed to RUN" "$cat" true ''
done

# An unknown category is fail-closed: nothing is provably irrelevant, so RUN.
run "unknown category fails closed to RUN" bogus true docs/x.md

# CI disables rename detection, so both sides of a rename are classified. A rename
# crossing the relevance boundary runs the job.
run "go: rename out of SDK into Go runs" go true sdk/typescript/old.ts internal/new.go

# Paths are records, never shell fragments: metacharacters remain one NUL-delimited
# filename and cannot execute anything.
sentinel="$here/macos-relevant-changes-sentinel"
rm -f "$sentinel"
run "go: NUL-delimited unusual filename is safe" go false $'docs/a name;$(touch .github/scripts/macos-relevant-changes-sentinel)\n.md'
if [[ -e "$sentinel" ]]; then
  printf 'FAIL: filename was evaluated as shell code\n'
  fails=$((fails + 1))
else
  printf 'ok: filename was not evaluated as shell code\n'
fi

if [[ "$fails" -ne 0 ]]; then
  printf 'macos-relevant classifier tests: %d failure(s)\n' "$fails"
  exit 1
fi
printf 'macos-relevant classifier tests: all checks passed\n'
