#!/usr/bin/env bash
# Offline tests for docs-only-changes.sh. Fixtures are NUL-delimited exactly like
# `git diff --name-only -z --no-renames` in ci.yml.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
classifier="$here/docs-only-changes.sh"
fails=0

run() {
  local name="$1" want="$2"; shift 2
  local got
  got="$(printf '%s\0' "$@" | bash "$classifier")"
  if [[ "$got" != "$want" ]]; then
    printf 'FAIL: %s — expected %q, got %q\n' "$name" "$want" "$got"
    fails=$((fails + 1))
  else
    printf 'ok: %s\n' "$name"
  fi
}

run_raw() {
  local name="$1" want="$2" input="$3" got
  got="$(printf '%s' "$input" | bash "$classifier")"
  if [[ "$got" != "$want" ]]; then
    printf 'FAIL: %s — expected %q, got %q\n' "$name" "$want" "$got"
    fails=$((fails + 1))
  else
    printf 'ok: %s\n' "$name"
  fi
}

# Ordinary documentation content is the only positive case.
run "documentation content passes" true user-docs/intro.md user-docs/building/getting-started/demo.md user-docs/building/_category_.json user-docs/building/deployment/_category_.json README.md

# Fail closed rather than treating no paths or an unterminated record as harmless.
run_raw "empty input fails closed" false ''
run_raw "unterminated input fails closed" false 'user-docs/intro.md'
run "empty NUL record fails closed" false ''

# Any source/build/configuration path makes the whole change full validation.
run "mixed documentation and Go fails closed" false user-docs/intro.md engine/agent/loop.go
run "workflow and configuration paths fail closed" false user-docs/intro.md .github/workflows/ci.yml Taskfile.yml .matlatl.yml website/package-lock.json
run "docs implementation files fail closed" false docs/lint/citations.go docs/architecture/mecatl.modelith.yaml
run "unknown paths fail closed" false user-docs/intro.md notes.txt AGENTS.md CLAUDE.md

# CI disables rename detection before piping paths here, so both sides of a
# rename are classified. A rename crossing the allowlist boundary is full.
run "rename out of docs fails closed" false user-docs/intro.md internal/usage.go
run "rename into docs fails closed" false internal/usage.go user-docs/intro.md

# Paths are records, never shell fragments: spaces, newlines, and metacharacters
# remain one NUL-delimited Markdown filename and cannot execute anything.
sentinel="$here/docs-only-changes-sentinel"
rm -f "$sentinel"
run "NUL-delimited unusual filename is safe" true $'docs/a name;$(touch .github/scripts/docs-only-changes-sentinel)\n.md'
if [[ -e "$sentinel" ]]; then
  printf 'FAIL: filename was evaluated as shell code\n'
  fails=$((fails + 1))
else
  printf 'ok: filename was not evaluated as shell code\n'
fi

if [[ "$fails" -ne 0 ]]; then
  printf 'docs-only classifier tests: %d failure(s)\n' "$fails"
  exit 1
fi
printf 'docs-only classifier tests: all checks passed\n'
