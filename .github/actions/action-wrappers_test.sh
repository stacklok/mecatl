#!/usr/bin/env bash
# action-wrappers_test.sh — OFFLINE contract tests for the mecatequi COMPOSITE action.yml
# wrappers. Driven by `task test:actions`.
#
# WHY THIS EXISTS (the untested-wrapper gap). actionlint CANNOT lint a composite action.yml
# (it errors "on section missing" — it only understands workflows), so the env-mapping
# contract between an action.yml and the script it wraps is otherwise UNCHECKED: a renamed
# input key or a silently-dropped env mapping would ship green. These grep-based assertions
# pin the contract so a rename or an accidental TOKEN input goes RED here.
#
# WHAT IS UNDER TEST (the load-bearing seams, not cosmetics):
#   1. mecatequi-publish/action.yml maps its inputs onto the EXACT env vars publish.sh reads
#      (ISSUE_NUMBER, PATCH_PATH, SUMMARY_PATH, EXIT_CLASS, REPO, BASE_BRANCH,
#      MQ_PR_BODY_TEMPLATE, MQ_PR_TITLE_TEMPLATE) — drop or rename one and publish.sh silently
#      loses that value; AND it declares NO token/GH_TOKEN INPUT (the token must arrive via the
#      job-level env path, never an input that could leak into logs / the inputs surface).
#   2. mecatequi-extract-prompt/action.yml maps out-file -> OUT_FILE (the one env the script
#      reads).
#   3. mecatequi/action.yml (the MAIN action) maps its `instructions` input onto MQ_INSTRUCTIONS
#      in the Run step's env AND wires that env to the binary via the conditional
#      `args+=( --instructions … )`. Both are load-bearing: drop the env mapping and the
#      operator framing never reaches the binary; drop the args+= line and it ships INERT
#      (the input is accepted, the env is set, but the flag is never passed) while the Go unit
#      tests stay green. Pinning both here closes that gap.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PUBLISH_YML="${HERE}/mecatequi-publish/action.yml"
EXTRACT_YML="${HERE}/mecatequi-extract-prompt/action.yml"
MECATEQUI_YML="${HERE}/mecatequi/action.yml"

fail=0
note() { printf '%s\n' "$*" >&2; }
pass() { note "  ok: $1"; }
bad() { note "  FAIL: $1"; fail=1; }

# Assert the file contains a regex (the mapping line). $1 file, $2 ERE, $3 label.
has() {
  if grep -Eq "$2" "$1"; then pass "$3"; else bad "$3 (missing: $2)"; fi
}
# Assert the file does NOT contain a regex. $1 file, $2 ERE, $3 label.
lacks() {
  if grep -Eq "$2" "$1"; then bad "$3 (present but must NOT be: $2)"; else pass "$3"; fi
}

[ -f "${PUBLISH_YML}" ] || { note "FATAL: ${PUBLISH_YML} not found"; exit 1; }
[ -f "${EXTRACT_YML}" ] || { note "FATAL: ${EXTRACT_YML} not found"; exit 1; }
[ -f "${MECATEQUI_YML}" ] || { note "FATAL: ${MECATEQUI_YML} not found"; exit 1; }

note "action-wrappers contract tests:"

# ── mecatequi-publish: every env var publish.sh reads is mapped from an input ─────────────
# The mapping line is `<ENV>: ${{ inputs.<key> }}`. We assert the exact ENV NAME on the LHS
# (so renaming the env key — which publish.sh reads by name — goes RED) AND that it is fed
# from an `inputs.` expression (so it is the input contract, not a hardcoded literal). The
# specific input key on the RHS is asserted where the kebab/snake mapping is the load-bearing
# part (issue-number -> ISSUE_NUMBER etc.).
has "${PUBLISH_YML}" '^[[:space:]]*ISSUE_NUMBER:[[:space:]]+\$\{\{[[:space:]]*inputs\.issue-number[[:space:]]*\}\}' \
  "publish maps issue-number -> ISSUE_NUMBER"
has "${PUBLISH_YML}" '^[[:space:]]*PATCH_PATH:[[:space:]]+\$\{\{[[:space:]]*inputs\.patch-path[[:space:]]*\}\}' \
  "publish maps patch-path -> PATCH_PATH"
has "${PUBLISH_YML}" '^[[:space:]]*SUMMARY_PATH:[[:space:]]+\$\{\{[[:space:]]*inputs\.summary-path[[:space:]]*\}\}' \
  "publish maps summary-path -> SUMMARY_PATH"
has "${PUBLISH_YML}" '^[[:space:]]*EXIT_CLASS:[[:space:]]+\$\{\{[[:space:]]*inputs\.exit-class[[:space:]]*\}\}' \
  "publish maps exit-class -> EXIT_CLASS"
has "${PUBLISH_YML}" '^[[:space:]]*REPO:[[:space:]]+\$\{\{[[:space:]]*inputs\.repo[[:space:]]*\}\}' \
  "publish maps repo -> REPO"
has "${PUBLISH_YML}" '^[[:space:]]*BASE_BRANCH:[[:space:]]+\$\{\{[[:space:]]*inputs\.base-branch[[:space:]]*\}\}' \
  "publish maps base-branch -> BASE_BRANCH"
has "${PUBLISH_YML}" '^[[:space:]]*MQ_PR_BODY_TEMPLATE:[[:space:]]+\$\{\{[[:space:]]*inputs\.pr-body-template[[:space:]]*\}\}' \
  "publish maps pr-body-template -> MQ_PR_BODY_TEMPLATE"
has "${PUBLISH_YML}" '^[[:space:]]*MQ_PR_TITLE_TEMPLATE:[[:space:]]+\$\{\{[[:space:]]*inputs\.pr-title-template[[:space:]]*\}\}' \
  "publish maps pr-title-template -> MQ_PR_TITLE_TEMPLATE"

# ── mecatequi-publish: the token is NEVER an input (it must arrive via job-level env) ─────
# A token routed through an action input would be eligible to leak into logs and the inputs
# surface; publish.sh reads GH_TOKEN from the process environment instead. Assert no input
# named token / gh-token / gh_token / github-token is declared, and GH_TOKEN is not mapped in
# an env: block. We look only at INPUT KEYS (a `<key>:` line under the `inputs:` block) and at
# any `GH_TOKEN:` env mapping; the prose comments naming GH_TOKEN are fine.
lacks "${PUBLISH_YML}" '^[[:space:]]+(token|gh-token|gh_token|github-token|github_token):[[:space:]]*$' \
  "publish declares NO token/gh-token input key"
lacks "${PUBLISH_YML}" '^[[:space:]]*GH_TOKEN:[[:space:]]+\$\{\{' \
  "publish does NOT map GH_TOKEN via an env: expression (token rides the job-level env path)"

# ── mecatequi-extract-prompt: out-file -> OUT_FILE ────────────────────────────────────────
has "${EXTRACT_YML}" '^[[:space:]]*OUT_FILE:[[:space:]]+\$\{\{[[:space:]]*inputs\.out-file[[:space:]]*\}\}' \
  "extract-prompt maps out-file -> OUT_FILE"

# ── mecatequi (MAIN): the `instructions` input must reach the binary, end-to-end ──────────
# The feature is only LIVE if BOTH halves of the wiring are present: (a) the input maps onto
# the MQ_INSTRUCTIONS env in the Run step, and (b) that env is wired to the binary via the
# `args+=( --instructions … )` conditional. With (a) but not (b) the flag is never passed and
# the operator framing ships INERT — yet every Go unit test (which calls buildPrompt directly)
# stays green. Pin both so that silent-inert regression goes RED here.
has "${MECATEQUI_YML}" '^[[:space:]]*MQ_INSTRUCTIONS:[[:space:]]+\$\{\{[[:space:]]*inputs\.instructions[[:space:]]*\}\}' \
  "mecatequi maps instructions -> MQ_INSTRUCTIONS (Run step env)"
has "${MECATEQUI_YML}" 'args\+=\([[:space:]]*--instructions[[:space:]]+"\$\{MQ_INSTRUCTIONS\}"[[:space:]]*\)' \
  "mecatequi wires MQ_INSTRUCTIONS to the binary via args+=( --instructions … )"

if [ "${fail}" -ne 0 ]; then
  note "action-wrappers: FAILURES"
  exit 1
fi
note "action-wrappers: all tests passed"
