#!/usr/bin/env bash
# Offline self-test for govulncheck-gate.go — feeds SYNTHETIC govulncheck JSON
# (no network, no real govulncheck run) and asserts the allowlist + reachability
# + fail-closed behaviour. Mirrors the repo's other tested .github scripts. Run:
#   bash .github/scripts/govulncheck-gate_test.sh
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
src="$here/govulncheck-gate.go"
fails=0

# Compile to a temp binary so the REAL exit code propagates: `go run` collapses
# any non-zero child exit to 1 (the true code only reaches its `exit status N`
# stderr line), which would make the fail-closed exit-2 cases indistinguishable
# from the exit-1 gate-fail cases. The compiled binary preserves both.
gate="$(mktemp)"
trap 'rm -f "$gate"' EXIT
go build -o "$gate" "$src"

# run <name> <expected-exit> <allowlist-args...> < <json on stdin>
run() {
  local name="$1" want="$2"; shift 2
  local out got
  set +e
  out="$("$gate" "$@" 2>&1)"
  got=$?
  set -e
  if [ "$got" -ne "$want" ]; then
    echo "FAIL: $name — expected exit $want, got $got"
    echo "  output: $out"
    fails=$((fails + 1))
  else
    echo "ok: $name (exit $got)"
  fi
}

# A minimal valid stream always opens with a config message, so every fixture
# below includes one (its absence is itself a tested fail-closed case).
config='{"config":{"protocol_version":"v1.0.0"}}'

# A reachable finding for <osv>: trace[0] carries a function frame.
reachable() { printf '{"finding":{"osv":"%s","trace":[{"function":"Foo","package":"p","module":"m"}]}}' "$1"; }
# A module-level (NOT reachable) finding for <osv>: trace[0] has no function.
moduleonly() { printf '{"finding":{"osv":"%s","trace":[{"module":"m"}]}}' "$1"; }

# 1. Clean stream (config only) → PASS.
run "clean stream passes" 0 <<< "$config"

# 2. One reachable id, allowlisted → PASS (and reports it as accepted-risk).
out="$("$gate" GO-2026-4887 <<< "$config$(reachable GO-2026-4887)")"
if echo "$out" | grep -q "allowlisted"; then
  echo "ok: allowlisted reachable id passes and is reported"
else
  echo "FAIL: allowlisted id not reported as accepted-risk"; echo "  $out"; fails=$((fails + 1))
fi

# 3. THE BITE: a reachable id NOT on the allowlist → FAIL (exit 1). This proves
#    the gate is not a blanket pass.
run "new reachable id fails (the bite)" 1 GO-2026-4887 \
  <<< "$config$(reachable GO-2026-4887)$(reachable GO-9999-0001)"

# 4. THE BITE, empty allowlist (the engine-module STRICT form): any reachable
#    id fails.
run "strict (no allowlist) fails on any reachable id" 1 \
  <<< "$config$(reachable GO-2026-4883)"

# 5. The exact production root case: both docker ids reachable, both allowlisted
#    → PASS.
run "production root allowlist (both docker ids) passes" 0 GO-2026-4887 GO-2026-4883 \
  <<< "$config$(reachable GO-2026-4887)$(reachable GO-2026-4883)"

# 6. Removing one id from the allowlist while it is reachable → FAIL (proves
#    each allowlist entry is load-bearing, not decorative).
run "dropping one allowlisted-but-reachable id fails" 1 GO-2026-4887 \
  <<< "$config$(reachable GO-2026-4887)$(reachable GO-2026-4883)"

# 7. A module-only (non-reachable) finding for a NON-allowlisted id → PASS
#    (reachability filter works; require-but-don't-call is not a failure).
run "non-reachable finding does not fail" 0 \
  <<< "$config$(moduleonly GO-9999-0002)"

# 7b. STALE allowlist: an allowlisted id that is NOT reachable in this scan
#     (e.g. fixed upstream and dropped out) → PASS (exit 0) + a non-fatal
#     WARNING so the dead entry can be pruned. Here GO-2026-4887 is reachable
#     and allowlisted; GO-2026-4883 is allowlisted but absent from the stream.
out="$("$gate" GO-2026-4887 GO-2026-4883 <<< "$config$(reachable GO-2026-4887)")"
got=$?
if [ "$got" -eq 0 ] && echo "$out" | grep -q "GO-2026-4883 (allowlisted but not reachable)"; then
  echo "ok: stale allowlist entry warns (non-fatal, exit 0)"
else
  echo "FAIL: stale allowlist entry not warned or wrong exit (got exit $got)"; echo "  $out"; fails=$((fails + 1))
fi

# 8. FAIL-CLOSED: empty input (no config preamble) → exit 2, never a vacuous pass.
run "empty input fails closed" 2 < /dev/null

# 9. FAIL-CLOSED: a stream with findings but NO config preamble (truncated
#    capture) → exit 2.
run "no-config stream fails closed" 2 \
  <<< "$(reachable GO-2026-4887)"

# 10. FAIL-CLOSED: malformed JSON → exit 2.
run "malformed json fails closed" 2 <<< '{"config": this is not json'

if [ "$fails" -ne 0 ]; then
  echo "govulncheck-gate self-test: $fails failure(s)"; exit 1
fi
echo "govulncheck-gate self-test: all checks passed"
