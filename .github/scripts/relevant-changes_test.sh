#!/usr/bin/env bash
# Offline tests for relevant-changes.sh. Fixtures are NUL-delimited exactly like
# `git diff --name-only -z --no-renames` in ci.yml. Recall the inverted sense
# versus docs-only-changes.sh: here "true" means RUN the job family and "false"
# means it can be SKIPPED.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
classifier="$here/relevant-changes.sh"
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

# --- go category: the Go build/test matrix -------------------------------------
# Provably irrelevant: docs, the site, user-docs, and the TS SDK frontend.
run "go: docs-only skips" go false README.md docs/intro.md docs/adr/0093-provider-modules.md docs/design/notes.mdx
run "go: user-docs + website skip" go false user-docs/intro.md user-docs/building/_category_.json website/package.json website/src/pages/index.tsx
run "go: SDK frontend skips (cannot affect a Go binary/test)" go false sdk/typescript/src/client.ts sdk/typescript/pnpm-lock.yaml
run "go: mixed irrelevant skips" go false docs/x.md sdk/typescript/src/a.ts website/b.md

# Any path in the Linux go test / task build closure RUNS — unlike the macOS
# closure, this includes cmd/, examples/, perf/, e2e/, deploy/, and contracts/.
run "go: Go source runs" go true engine/agent/loop.go
run "go: go.mod/go.sum run" go true go.mod engine/go.sum
run "go: contracts (proto codegen source) runs" go true contracts/proto/mecatl/v1/agent.proto
run "go: other command binaries run (in the Linux closure)" go true cmd/mecademo/main.go
run "go: examples run (in the Linux closure)" go true examples/first-agent/main.go
run "go: perf harness runs (in the Linux closure)" go true perf/scenarios/loop_bench_test.go
run "go: e2e + deploy run (in the Linux closure)" go true e2e/k8s/suite_test.go deploy/helm/mecak8s/values_test.go
run "go: docs-lint tool runs (Go code under docs/)" go true docs/lint/citations.go
run "go: docs non-Markdown assets run" go true docs/architecture/mecatl.modelith.yaml
run "go: Taskfile + workflow run (self-validation)" go true Taskfile.yml .github/workflows/ci.yml
run "go: mixed docs + Go runs" go true user-docs/intro.md internal/adapter/osfs/osfs.go

# --- sdk category: the pure-TS SDK unit job (`sdk`) -----------------------------
# Relevant only to the SDK frontend and the proto contracts its committed bindings
# are generated from; a Go-internal or site change cannot alter the committed TS.
run "sdk: SDK frontend runs" sdk true sdk/typescript/src/client.ts
run "sdk: contracts (proto → committed gen) runs" sdk true contracts/proto/mecatl/v1/agent.proto
run "sdk: Go engine change skips (cannot change committed TS)" sdk false engine/agent/loop.go
run "sdk: cmd change skips" sdk false cmd/mecated/main.go
run "sdk: website + user-docs skip" sdk false website/a.tsx user-docs/intro.md
run "sdk: docs skip" sdk false docs/x.md
run "sdk: mixed SDK + Go runs" sdk true sdk/typescript/src/a.ts engine/b.go
# CI-control files that DEFINE the sdk job / its task recipes must RUN it, even
# with no frontend change (the false-SKIP a positive allowlist would otherwise
# introduce for orchestration-only changes).
run "sdk: ci.yml (job definition) runs" sdk true .github/workflows/ci.yml
run "sdk: Taskfile (task recipes) runs" sdk true Taskfile.yml
run "sdk: .github/scripts change runs" sdk true .github/scripts/relevant-changes.sh

# --- site category: the Docusaurus build (`user-docs`) --------------------------
# Relevant to website/ and user-docs/ (site content) and sdk/typescript/ (the
# SDK reference pages the job checks are generated from the TS declarations).
run "site: website runs" site true website/src/pages/index.tsx
run "site: user-docs runs" site true user-docs/reference/configuration.md
run "site: SDK declarations run (task sdk:docs:check)" site true sdk/typescript/src/client.ts
run "site: Go engine change skips" site false engine/agent/loop.go
run "site: internal docs (matlatl corpus) skip" site false docs/adr/0093-provider-modules.md
run "site: mixed site + Go runs" site true website/a.tsx internal/b.go
# CI-control files that DEFINE the user-docs job / its task recipes must RUN it.
run "site: ci.yml (job definition) runs" site true .github/workflows/ci.yml
run "site: Taskfile (task recipes) runs" site true Taskfile.yml

# --- fail-closed / safety across categories ------------------------------------
for cat in go sdk site; do
  run_raw "$cat: empty input fails closed to RUN" "$cat" true ''
  run_raw "$cat: unterminated input fails closed to RUN" "$cat" true 'docs/x.md'
  run "$cat: empty NUL record fails closed to RUN" "$cat" true ''
done

# An unknown category is fail-closed: nothing is provably irrelevant, so RUN.
run "unknown category fails closed to RUN" bogus true docs/x.md

# CI disables rename detection, so both sides of a rename are classified. A rename
# crossing the relevance boundary runs the family.
run "go: rename out of SDK into Go runs" go true sdk/typescript/old.ts internal/new.go
run "sdk: rename out of Go into SDK runs" sdk true internal/old.go sdk/typescript/new.ts

# Paths are records, never shell fragments: metacharacters remain one NUL-delimited
# filename and cannot execute anything.
sentinel="$here/relevant-changes-sentinel"
rm -f "$sentinel"
run "go: NUL-delimited unusual filename is safe" go false $'docs/a name;$(touch .github/scripts/relevant-changes-sentinel)\n.md'
if [[ -e "$sentinel" ]]; then
  printf 'FAIL: filename was evaluated as shell code\n'
  fails=$((fails + 1))
else
  printf 'ok: filename was not evaluated as shell code\n'
fi

if [[ "$fails" -ne 0 ]]; then
  printf 'relevant classifier tests: %d failure(s)\n' "$fails"
  exit 1
fi
printf 'relevant classifier tests: all checks passed\n'
