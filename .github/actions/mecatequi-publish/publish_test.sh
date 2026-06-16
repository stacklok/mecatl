#!/usr/bin/env bash
# publish_test.sh — OFFLINE bash tests for publish.sh. No bats dependency; plain bash with
# fake `gh` / `git` stubs on $PATH and fixture env. Driven by `task test:actions`.
#
# WHAT THIS PROVES. publish.sh holds the write token and turns a mecatequi run's patch +
# summary into a PR or an honest comment. The SECURITY-relevant branches are:
#   - the PROTECTED-PATHS gate: a patch that touches .github/ (or other CI/build-control
#     paths) is REJECTED before `git apply`, so a prompt-injected agent cannot rewrite the
#     workflow that runs it and slip it past review of the "feature" diff;
#   - the TEMPLATE-CONFINEMENT gate (CWE-22): a pr-body-template that resolves OUTSIDE the
#     checkout (a `../` traversal) is rejected and the built-in body is used instead, so no
#     out-of-tree file is spliced into the PR body;
#   - the NO-CHANGE branch: a clean run with no diff posts an informational comment and
#     opens NO PR;
#   - the FAILURE branch: a non-clean run posts an honest failure comment and opens NO PR.
#
# Each test is structured so that REMOVING the guard would FAIL it (the protected-paths test
# asserts NO PR is opened AND the rejection comment is posted; if the gate were removed, the
# stub `gh pr create` WOULD be called and the assertion flips). Per repo convention the
# patch fixtures touch INNOCUOUS protected paths (.github/workflows/x.yml), never destructive
# content; the assertion is on the EFFECT (rejected vs. PR-opened), not on a payload string.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="${HERE}/publish.sh"
ROOT="$(cd "${HERE}/../../.." && pwd)"

# .scratch/ is gitignored, so it is ABSENT in a fresh clone — the first mktemp below would
# then abort under `set -euo pipefail` (the whole test silently never runs in CI). Create it
# up front, before any make_sandbox call.
mkdir -p "${ROOT}/.scratch"

fail=0
note() { printf '%s\n' "$*" >&2; }
pass() { note "  ok: $1"; }
bad() { note "  FAIL: $1"; fail=1; }

# A per-test sandbox under the repo-local .scratch (never /tmp).
make_sandbox() {
  mktemp -d "${ROOT}/.scratch/publish-test.XXXXXX"
}

# Build a fake `gh` + `git` bin dir for a sandbox. Both stubs LOG every invocation (one line
# per call, argv joined) to ${BIN}/../calls.log so a test can assert which subcommands ran.
# `gh pr create` additionally touches a marker so a test can assert a PR WAS / WAS NOT opened
# without parsing the log. `git apply --numstat -z` echoes a caller-provided numstat fixture
# (numstat.fixture) so the protected-paths gate sees the touched paths we want. The fixture is
# the RAW NUL-delimited form `git apply --numstat -z` emits (`<added>\t<deleted>\t<rawpath>\0`)
# — so a fixture path may carry a tab/control/non-ASCII byte exactly as a real quoting-bypass
# patch would, and the gate sees it UNQUOTED.
#
# The git stub is `-z`-AWARE so the mutation check is faithful: real `git apply --numstat`
# WITHOUT -z C-QUOTES a special-byte path (leading `"`, backslash-escaped), but WITH -z emits
# the raw bytes. The stub mirrors that — when the argv carries `-z` it serves numstat.fixture
# (the raw NUL form), otherwise it serves numstat.fixture.noz (the C-QUOTED, newline-delimited
# form a real git would print) if a test planted one, else the raw fixture. So reverting the
# gate to the old non-`-z` parsing makes it read the C-quoted form and FAIL OPEN — exactly the
# bug fix #1 closes.
make_stubs() {
  local work="$1" bin="$1/bin"
  mkdir -p "${bin}"
  cat > "${bin}/gh" <<EOF
#!/usr/bin/env bash
echo "gh \$*" >> "${work}/calls.log"
case "\$1 \$2" in
  "pr create") : > "${work}/PR_CREATED" ;;
  "pr list") echo "" ;;                       # no existing PR
  "repo view") echo "main" ;;                 # default branch
  "issue view")
    # File-driven per-test override (parity with numstat.fixture): a test plants
    # issue-title.fixture to control the title; otherwise a fixed descriptive default.
    if [ -f "${work}/issue-title.fixture" ]; then
      cat "${work}/issue-title.fixture"
    else
      echo "Add a widget to the gizmo"
    fi
    ;;
  "auth setup-git") : ;;
esac
exit 0
EOF
  cat > "${bin}/git" <<EOF
#!/usr/bin/env bash
echo "git \$*" >> "${work}/calls.log"
case "\$1 \$2" in
  "apply --numstat")
    # -z anywhere in argv -> raw NUL form; else the C-quoted (non-z) form if planted.
    if printf '%s\0' "\$@" | grep -qz -- '-z'; then
      cat "${work}/numstat.fixture" 2>/dev/null || true
    elif [ -f "${work}/numstat.fixture.noz" ]; then
      cat "${work}/numstat.fixture.noz" 2>/dev/null || true
    else
      cat "${work}/numstat.fixture" 2>/dev/null || true
    fi
    ;;
esac
exit 0
EOF
  chmod +x "${bin}/gh" "${bin}/git"
}

# Run publish.sh in a sandbox with the stubs first on PATH. Extra env is passed as KEY=VALUE
# arguments. Captures rc; the calls log + markers live under ${work}.
run_publish() {
  local work="$1"; shift
  ( cd "${work}"
    PATH="${work}/bin:${PATH}" \
    GH_TOKEN="fake-token" \
    GITHUB_WORKSPACE="${work}" \
    RUNNER_TEMP="${work}" \
    GITHUB_REPOSITORY="acme/widget" \
    GITHUB_RUN_ID="123" \
    GITHUB_SERVER_URL="https://github.com" \
    env "$@" bash "${SCRIPT}" >/dev/null 2>&1
  )
}

called() { grep -qF "$1" "$2"; }   # $1 substring, $2 calls.log path

# ── Test 1 (SECURITY): a patch touching .github/ is REJECTED, opens NO PR ─────────────────
test_protected_paths_rejected() {
  local work; work="$(make_sandbox)"
  make_stubs "${work}"
  # A non-empty summary marking a clean run with a diff, and a numstat fixture whose touched
  # path is under .github/ (the protected zone). The patch file must exist + be non-empty.
  printf '{"stop_reason":"end_turn","non_empty_diff":true,"diff_bytes":10}' > "${work}/summary.json"
  printf 'diff --git a/.github/workflows/x.yml b/.github/workflows/x.yml\n' > "${work}/run.patch"
  # NUL-delimited fixture (the raw `git apply --numstat -z` form).
  printf '1\t0\t.github/workflows/x.yml\0' > "${work}/numstat.fixture"
  local rc=0
  run_publish "${work}" \
    ISSUE_NUMBER=10 \
    EXIT_CLASS=clean \
    PATCH_PATH="${work}/run.patch" \
    SUMMARY_PATH="${work}/summary.json" || rc=$?
  if [ -e "${work}/PR_CREATED" ]; then
    bad "protected-paths gate FAILED OPEN: a PR was created for a .github/ patch"
  elif called "issue comment" "${work}/calls.log" && [ "${rc}" -ne 0 ]; then
    pass "patch touching .github/ rejected (comment posted, no PR, non-zero exit)"
  else
    bad "protected-paths rejection did not post a comment / exit non-zero (rc=${rc})"
  fi
  rm -rf "${work}"
}

# ── Test 2 (control): a patch touching ONLY a normal source file is NOT blocked ───────────
# Proves the gate is specific (it must not reject every patch). With an allowed path the
# script proceeds through the apply/commit/push/PR-create tail (all stubbed), so a PR IS
# created. If the gate were over-broad (blocking everything) this would fail.
test_normal_path_allowed() {
  local work; work="$(make_sandbox)"
  make_stubs "${work}"
  printf '{"stop_reason":"end_turn","non_empty_diff":true,"diff_bytes":10}' > "${work}/summary.json"
  printf 'diff --git a/src/widget.go b/src/widget.go\n' > "${work}/run.patch"
  printf '3\t1\tsrc/widget.go\0' > "${work}/numstat.fixture"
  run_publish "${work}" \
    ISSUE_NUMBER=11 \
    EXIT_CLASS=clean \
    PATCH_PATH="${work}/run.patch" \
    SUMMARY_PATH="${work}/summary.json"
  if [ -e "${work}/PR_CREATED" ]; then
    pass "patch touching a normal source path proceeds to PR creation (gate is specific)"
  else
    bad "an allowed-path patch did NOT reach PR creation (gate over-broad?)"
  fi
  rm -rf "${work}"
}

# ── Test 2b (SECURITY): a protected path with a QUOTED special byte is still REJECTED ─────
# The path-quoting bypass. WITHOUT `-z`, `git apply --numstat` C-quotes a path containing a
# tab / control / non-ASCII byte as a backslash-escaped, DOUBLE-QUOTE-wrapped string with a
# LEADING `"`, so a `^\.github/` anchor never matches and the gate FAILS OPEN — the file
# under .github/ then gets WRITTEN by the later `git apply`. With `-z` the path is RAW and
# UNQUOTED, so the gate sees `.github/...` directly and blocks it. We feed the gate the RAW
# `-z` form (a real `.github/workflows/` path carrying a literal TAB and a non-ASCII byte)
# and assert it is REJECTED: no PR, a comment, non-zero exit. Innocuous path content — the
# assertion is on the EFFECT (rejected), never on a payload string.
test_protected_paths_quoted_rejected() {
  local work; work="$(make_sandbox)"
  make_stubs "${work}"
  printf '{"stop_reason":"end_turn","non_empty_diff":true,"diff_bytes":10}' > "${work}/summary.json"
  printf 'diff --git a/x b/x\n' > "${work}/run.patch"
  # A protected path whose name carries a literal TAB and a non-ASCII byte (é, 0xC3 0xA9) —
  # exactly what would force git's C-quoting on the non-`-z` path. The third field (the raw
  # path) itself contains a tab, which is why the gate must split on only the FIRST TWO tabs.
  # -z form (raw, NUL-delimited): the fixed gate sees `.github/...` unquoted and blocks it.
  printf '1\t0\t.github/workflows/ci\tinj\xc3\xa9ct.yml\0' > "${work}/numstat.fixture"
  # non-`-z` form (what real git prints WITHOUT -z): the path is C-QUOTED with a LEADING `"`
  # and the tab/byte backslash-escaped, newline-delimited. The OLD parsing's `^\.github/`
  # anchor never matches this leading-quote line -> it FAILS OPEN. Planting it makes the
  # mutation check (revert to the old non-`-z` gate) flip THIS test RED.
  printf '1\t0\t".github/workflows/ci\\tinj\\303\\251ct.yml"\n' > "${work}/numstat.fixture.noz"
  local rc=0
  run_publish "${work}" \
    ISSUE_NUMBER=20 \
    EXIT_CLASS=clean \
    PATCH_PATH="${work}/run.patch" \
    SUMMARY_PATH="${work}/summary.json" || rc=$?
  if [ -e "${work}/PR_CREATED" ]; then
    bad "quoting-bypass gate FAILED OPEN: a PR was created for a tab/non-ASCII .github/ path"
  elif called "issue comment" "${work}/calls.log" && [ "${rc}" -ne 0 ]; then
    pass "protected path with a quoted special byte rejected (comment posted, no PR, non-zero exit)"
  else
    bad "quoted-path rejection did not post a comment / exit non-zero (rc=${rc})"
  fi
  rm -rf "${work}"
}

# ── Test 2c (SECURITY): a RENAME whose DESTINATION is protected is REJECTED ───────────────
# With `-z`, a rename emits the old + new paths as extra NUL records. A rename FROM an
# innocent source TO a path under .github/ must still be blocked — so the gate tests every
# path token in the record set, not just the first. Source is innocuous; destination lands
# under .github/.
test_protected_paths_rename_dest_rejected() {
  local work; work="$(make_sandbox)"
  make_stubs "${work}"
  printf '{"stop_reason":"end_turn","non_empty_diff":true,"diff_bytes":10}' > "${work}/summary.json"
  printf 'diff --git a/src/old.txt b/.github/workflows/evil.yml\n' > "${work}/run.patch"
  # A rename record: numeric fields then the source path, then the destination path as an
  # extra NUL token. The destination is protected.
  printf '0\t0\tsrc/old.txt\0.github/workflows/evil.yml\0' > "${work}/numstat.fixture"
  local rc=0
  run_publish "${work}" \
    ISSUE_NUMBER=21 \
    EXIT_CLASS=clean \
    PATCH_PATH="${work}/run.patch" \
    SUMMARY_PATH="${work}/summary.json" || rc=$?
  if [ -e "${work}/PR_CREATED" ]; then
    bad "rename-dest gate FAILED OPEN: a PR was created for a rename INTO .github/"
  elif called "issue comment" "${work}/calls.log" && [ "${rc}" -ne 0 ]; then
    pass "rename whose destination is under .github/ rejected (comment, no PR, non-zero exit)"
  else
    bad "rename-dest rejection did not post a comment / exit non-zero (rc=${rc})"
  fi
  rm -rf "${work}"
}

# ── Test 3 (SECURITY): a ../ traversal pr-body-template is confined; built-in body is used ─
# Point MQ_PR_BODY_TEMPLATE at a path OUTSIDE the checkout (a ../ escape to a file we plant
# outside ${GITHUB_WORKSPACE}). The confinement must REJECT it (warning + fallback) and NOT
# read its contents into the PR body. The PR is still created from the built-in body. We
# assert the PR body file does NOT contain the out-of-tree sentinel.
test_template_traversal_confined() {
  local work; work="$(make_sandbox)"
  make_stubs "${work}"
  # Plant an out-of-tree "template" with a sentinel ABOVE the workspace.
  local outside="${work}/../publish-outside-$RANDOM.md"
  printf 'OUT_OF_TREE_SENTINEL_SHOULD_NOT_APPEAR\n' > "${outside}"
  printf '{"stop_reason":"end_turn","non_empty_diff":true,"diff_bytes":10,"final_text":"did work"}' > "${work}/summary.json"
  printf 'diff --git a/src/ok.go b/src/ok.go\n' > "${work}/run.patch"
  printf '1\t0\tsrc/ok.go\0' > "${work}/numstat.fixture"
  # A ../ relative template path that escapes the workspace root.
  local rel="../$(basename "${outside}")"
  run_publish "${work}" \
    ISSUE_NUMBER=12 \
    EXIT_CLASS=clean \
    PATCH_PATH="${work}/run.patch" \
    SUMMARY_PATH="${work}/summary.json" \
    MQ_PR_BODY_TEMPLATE="${rel}"
  # The rendered PR body publish.sh writes (RUNNER_TEMP=${work}).
  local body="${work}/mecatequi-pr-body.md"
  if [ -f "${body}" ] && grep -qF "OUT_OF_TREE_SENTINEL_SHOULD_NOT_APPEAR" "${body}"; then
    bad "template confinement FAILED: an out-of-checkout file was spliced into the PR body"
  elif [ -e "${work}/PR_CREATED" ]; then
    pass "../ traversal template rejected; built-in body used, no out-of-tree content"
  else
    bad "template-confinement path did not reach PR creation (unexpected)"
  fi
  rm -f "${outside}"
  rm -rf "${work}"
}

# ── Test 4: a clean run with NO diff posts an informational comment, opens NO PR ──────────
test_no_change_comments() {
  local work; work="$(make_sandbox)"
  make_stubs "${work}"
  printf '{"stop_reason":"end_turn","non_empty_diff":false,"diff_bytes":0}' > "${work}/summary.json"
  : > "${work}/empty.patch"   # empty patch
  local rc=0
  run_publish "${work}" \
    ISSUE_NUMBER=13 \
    EXIT_CLASS=clean \
    PATCH_PATH="${work}/empty.patch" \
    SUMMARY_PATH="${work}/summary.json" || rc=$?
  if [ -e "${work}/PR_CREATED" ]; then
    bad "no-change run opened a PR (should only comment)"
  elif called "issue comment" "${work}/calls.log" && [ "${rc}" -eq 0 ]; then
    pass "clean run with no diff posts an informational comment, no PR, exit 0"
  else
    bad "no-change branch did not comment / exited non-zero (rc=${rc})"
  fi
  rm -rf "${work}"
}

# ── Test 5: a non-clean run posts an honest FAILURE comment, opens NO PR ──────────────────
test_failure_comments() {
  local work; work="$(make_sandbox)"
  make_stubs "${work}"
  # A run-failure: EXIT_CLASS=run-failure. No patch needed; the failure branch fires first.
  printf '{"stop_reason":"error","non_empty_diff":false,"error":"model blew up"}' > "${work}/summary.json"
  local rc=0
  run_publish "${work}" \
    ISSUE_NUMBER=14 \
    EXIT_CLASS=run-failure \
    SUMMARY_PATH="${work}/summary.json" || rc=$?
  if [ -e "${work}/PR_CREATED" ]; then
    bad "failure run opened a PR (should only comment)"
  elif called "issue comment" "${work}/calls.log" && [ "${rc}" -eq 0 ]; then
    pass "non-clean run posts an honest failure comment, no PR, exit 0"
  else
    bad "failure branch did not comment / exited non-zero (rc=${rc})"
  fi
  rm -rf "${work}"
}

# ── Test 6: an EMPTY EXIT_CLASS is treated as setup-failure (still comments, no PR) ───────
test_empty_exit_class_is_setup_failure() {
  local work; work="$(make_sandbox)"
  make_stubs "${work}"
  local rc=0
  run_publish "${work}" \
    ISSUE_NUMBER=15 \
    EXIT_CLASS="" || rc=$?
  if [ -e "${work}/PR_CREATED" ]; then
    bad "empty EXIT_CLASS opened a PR (should be treated as setup-failure)"
  elif called "issue comment" "${work}/calls.log" && [ "${rc}" -eq 0 ]; then
    pass "empty EXIT_CLASS treated as setup-failure (honest comment, no PR)"
  else
    bad "empty EXIT_CLASS did not comment / exited non-zero (rc=${rc})"
  fi
  rm -rf "${work}"
}

# ── Test 6b (issue #70): a FAILED `gh issue comment` on the setup-failure path does NOT ───
# abort the job silently — it emits a loud ::error:: and STILL exits 0.
# This is the secondary observation from issue #70: on stacklok/atrium#416, implement failed
# at action-LOAD (so EXIT_CLASS was empty -> setup-failure), publish.sh entered the failure
# branch, but the resolved App-installation token could not resolve the issue and
# `gh issue comment` threw "GraphQL: Could not resolve to an issue …". Under `set -euo
# pipefail` the UNGUARDED comment aborted publish.sh mid-branch with NO terminal signal — the
# very silent failure the branch exists to close. The guard (post_issue_comment) must turn
# that into a ::error:: + a clean exit 0. We install a gh stub whose `issue comment` FAILS and
# assert: the script exits 0 (not aborted by set -e), the comment WAS attempted, NO PR was
# opened, and the ::error:: diagnostic was emitted. Removing the guard (reverting to a bare
# `gh issue comment`) flips rc to non-zero and drops the ::error:: — failing this test.
test_failed_comment_does_not_abort_silently() {
  local work; work="$(make_sandbox)"
  make_stubs "${work}"
  # Override the gh stub so `issue comment` FAILS (mimicking the unresolvable-issue / missing
  # issues:write case). Other subcommands behave as before so the branch logic is unchanged.
  cat > "${work}/bin/gh" <<EOF
#!/usr/bin/env bash
echo "gh \$*" >> "${work}/calls.log"
case "\$1 \$2" in
  "issue comment") echo "GraphQL: Could not resolve to an issue or pull request" >&2; exit 1 ;;
  "pr create") : > "${work}/PR_CREATED" ;;
  "pr list") echo "" ;;
  "repo view") echo "main" ;;
  "auth setup-git") : ;;
esac
exit 0
EOF
  chmod +x "${work}/bin/gh"
  # Capture stderr so we can assert the ::error:: diagnostic was emitted.
  local rc=0
  ( cd "${work}"
    PATH="${work}/bin:${PATH}" \
    GH_TOKEN="fake-token" \
    GITHUB_WORKSPACE="${work}" \
    RUNNER_TEMP="${work}" \
    GITHUB_REPOSITORY="acme/widget" \
    GITHUB_RUN_ID="123" \
    GITHUB_SERVER_URL="https://github.com" \
    env ISSUE_NUMBER=416 EXIT_CLASS="" \
      bash "${SCRIPT}" >/dev/null 2>"${work}/stderr.log"
  ) || rc=$?
  if [ -e "${work}/PR_CREATED" ]; then
    bad "failed-comment setup-failure path opened a PR (should only attempt a comment)"
  elif [ "${rc}" -ne 0 ]; then
    bad "a failed gh issue comment ABORTED publish.sh on the setup-failure path (rc=${rc}) — the silent-failure bug (issue #70) is back"
  elif ! called "issue comment" "${work}/calls.log"; then
    bad "the setup-failure path did not even attempt the issue comment"
  elif ! grep -qF "::error::publish: could not post the issue comment" "${work}/stderr.log"; then
    bad "a failed issue comment did not emit the loud ::error:: diagnostic"
  else
    pass "a failed issue comment emits ::error:: and still exits 0 (no silent abort)"
  fi
  rm -rf "${work}"
}

# ── Test 7 (SECURITY): the PR-body template render is LITERAL + SINGLE-PASS ───────────────
# render_template substitutes {{token}} values that INCLUDE the model's own final_text —
# AGENT-AUTHORED from untrusted issue text. The render must be (a) DISPLAY-ONLY: a value
# containing $(...)/backticks/& is written verbatim, never executed; (b) SINGLE-PASS: a
# {{run_url}} that appears INSIDE the final_text value is NOT re-expanded. We supply a custom
# pr-body template (under the workspace) plus a hostile final_text and assert the EFFECT: the
# always-prepended caveat is present, the $(...)/backtick did NOT run (marker absent), and the
# literal `{{run_url}}` from the value survives intact. Innocuous markers — never destructive.
test_render_template_literal_single_pass() {
  local work; work="$(make_sandbox)"
  make_stubs "${work}"
  local marker="${work}/RENDER_INJECTED_MARKER"
  rm -f "${marker}"
  # A repo-provided template under the workspace, with a {{what_agent_did}} body token and a
  # literal {{unknown_token}} that must be left intact.
  mkdir -p "${work}/.github/mecatequi"
  printf '## Body\n\n{{what_agent_did}}\n\nLiteral kept: {{unknown_token}}\n' \
    > "${work}/.github/mecatequi/pr-body.md"
  # A hostile final_text: a command substitution + a backtick + an & + a {{run_url}} token
  # embedded INSIDE the value (must NOT be re-expanded by the single pass). Built so THIS
  # shell never expands it (single quotes), then carried into the summary JSON via jq.
  local hostile
  hostile='did work $(touch '"${marker}"') and `touch '"${marker}"'` & here is {{run_url}} inline'
  jq -n --arg t "${hostile}" '{stop_reason:"end_turn",non_empty_diff:true,diff_bytes:10,final_text:$t}' \
    > "${work}/summary.json"
  printf 'diff --git a/src/ok.go b/src/ok.go\n' > "${work}/run.patch"
  printf '1\t0\tsrc/ok.go\0' > "${work}/numstat.fixture"
  run_publish "${work}" \
    ISSUE_NUMBER=22 \
    EXIT_CLASS=clean \
    PATCH_PATH="${work}/run.patch" \
    SUMMARY_PATH="${work}/summary.json"
  local body="${work}/mecatequi-pr-body.md"
  if [ -e "${marker}" ]; then
    bad "render_template EXECUTED a value's \$(...)/backtick (marker created) — not display-only"
  elif [ ! -f "${body}" ]; then
    bad "render_template path did not produce a PR body file"
  elif ! grep -qF "Agent-authored from the issue text" "${body}"; then
    bad "render_template dropped the always-prepended PR_CAVEAT line"
  elif ! grep -qF 'is {{run_url}} inline' "${body}"; then
    # The {{run_url}} INSIDE the final_text value must survive verbatim (single pass: the
    # value is inserted by the callable and never rescanned). The TEMPLATE's own {{run_url}}
    # token, by contrast, would resolve — but this template has none, so any {{run_url}} in
    # the output must be the one from the value.
    bad "render_template re-expanded a {{run_url}} that lived inside the value (not single-pass)"
  else
    pass "render_template is literal (no \$(...) execution) + single-pass + caveat-forced"
  fi
  rm -rf "${work}"
}

# ── Test 8: the DEFAULT PR title is derived from the issue title -> "<title> (#<n>)" ──────
# A clean run with a diff and a descriptive issue title must open a PR whose --title is
# "<issue title> (#<n>)" (the new default, replacing the prior "mecatequi: changes …"
# literal). Assert the PR was created AND the logged `gh pr create … --title …` carries it.
test_pr_title_from_issue_title() {
  local work; work="$(make_sandbox)"
  make_stubs "${work}"
  printf 'Add a widget to the gizmo\n' > "${work}/issue-title.fixture"
  printf '{"stop_reason":"end_turn","non_empty_diff":true,"diff_bytes":10,"final_text":"did work"}' > "${work}/summary.json"
  printf 'diff --git a/src/ok.go b/src/ok.go\n' > "${work}/run.patch"
  printf '1\t0\tsrc/ok.go\0' > "${work}/numstat.fixture"
  run_publish "${work}" \
    ISSUE_NUMBER=30 \
    EXIT_CLASS=clean \
    PATCH_PATH="${work}/run.patch" \
    SUMMARY_PATH="${work}/summary.json"
  if [ ! -e "${work}/PR_CREATED" ]; then
    bad "issue-title default: no PR was created"
  elif called "Add a widget to the gizmo (#30)" "${work}/calls.log"; then
    pass "default PR title derives from the issue title: '<title> (#<n>)'"
  else
    bad "the logged gh pr create did not carry the issue-title default '<title> (#30)'"
  fi
  rm -rf "${work}"
}

# ── Test 8b: MQ_PR_TITLE_TEMPLATE takes PRECEDENCE over a present issue title ──────────────
# A title template is the highest-priority title source: when set (and confined to the
# checkout) it wins even when `gh issue view` returns a perfectly good title. Plant a title
# template UNDER the workspace with a distinctive placeholder render (`Custom: {{issue_ref}}`),
# set a PRESENT issue title too, and assert the logged `gh pr create … --title …` carries the
# RENDERED TEMPLATE (`Custom: #<n>`) and NOT the issue-title default (`Some Issue Title (#<n>)`).
test_pr_title_template_precedence() {
  local work; work="$(make_sandbox)"
  make_stubs "${work}"
  # A present issue title that WOULD be used if the template did not win.
  printf 'Some Issue Title\n' > "${work}/issue-title.fixture"
  # A title template under the workspace (MQ_WORKSPACE defaults to GITHUB_WORKSPACE=${work}).
  mkdir -p "${work}/.github/mecatequi"
  printf 'Custom: {{issue_ref}}\n' > "${work}/.github/mecatequi/pr-title.md"
  printf '{"stop_reason":"end_turn","non_empty_diff":true,"diff_bytes":10,"final_text":"did work"}' > "${work}/summary.json"
  printf 'diff --git a/src/ok.go b/src/ok.go\n' > "${work}/run.patch"
  printf '1\t0\tsrc/ok.go\0' > "${work}/numstat.fixture"
  run_publish "${work}" \
    ISSUE_NUMBER=33 \
    EXIT_CLASS=clean \
    PATCH_PATH="${work}/run.patch" \
    SUMMARY_PATH="${work}/summary.json" \
    MQ_PR_TITLE_TEMPLATE=".github/mecatequi/pr-title.md"
  if [ ! -e "${work}/PR_CREATED" ]; then
    bad "title-template precedence: no PR was created"
  elif called "Some Issue Title (#33)" "${work}/calls.log"; then
    bad "title template did NOT win: the issue-title default 'Some Issue Title (#33)' was used"
  elif called "Custom: #33" "${work}/calls.log"; then
    pass "MQ_PR_TITLE_TEMPLATE wins over a present issue title (rendered 'Custom: #33')"
  else
    bad "the logged gh pr create carried neither the rendered template nor the issue-title default"
  fi
  rm -rf "${work}"
}

# ── Test 9: an EMPTY issue title falls back to the prior built-in literal ──────────────────
# When `gh issue view` yields an empty title (transient API failure / unreachable issue),
# the default must be byte-for-byte the prior literal "mecatequi: changes for issue #<n>".
test_pr_title_empty_falls_back() {
  local work; work="$(make_sandbox)"
  make_stubs "${work}"
  : > "${work}/issue-title.fixture"   # empty title
  printf '{"stop_reason":"end_turn","non_empty_diff":true,"diff_bytes":10,"final_text":"did work"}' > "${work}/summary.json"
  printf 'diff --git a/src/ok.go b/src/ok.go\n' > "${work}/run.patch"
  printf '1\t0\tsrc/ok.go\0' > "${work}/numstat.fixture"
  run_publish "${work}" \
    ISSUE_NUMBER=31 \
    EXIT_CLASS=clean \
    PATCH_PATH="${work}/run.patch" \
    SUMMARY_PATH="${work}/summary.json"
  if [ ! -e "${work}/PR_CREATED" ]; then
    bad "empty-title fallback: no PR was created"
  elif called "mecatequi: changes for issue #31" "${work}/calls.log"; then
    pass "an empty issue title falls back to the prior literal title"
  else
    bad "empty-title fallback did not use the prior literal 'mecatequi: changes for issue #31'"
  fi
  rm -rf "${work}"
}

# ── Test 10 (SECURITY): an untrusted issue title with shell metacharacters is LITERAL ─────
# The issue title is attacker-controllable. It reaches the title ONLY as a shell var
# expanded into the `gh pr create --title "${pr_title}"` argv token (never eval'd) and the
# {{issue_title}} placeholder via the literal `jq --arg` values JSON (never argv/env/eval).
# Plant a title containing shell metacharacters, a $(...) command substitution, and an
# injected {{run_url}}; assert (a) the $(...) did NOT run (no marker file), (b) a PR was
# created, (c) the logged --title carries the metacharacters VERBATIM. Built single-quoted
# in the fixture file so the TEST shell never expands it either.
test_pr_title_untrusted_metacharacters() {
  local work; work="$(make_sandbox)"
  make_stubs "${work}"
  local marker="${work}/TITLE_INJECTED_MARKER"
  rm -f "${marker}"
  # Single-quoted heredoc so neither this test shell nor publish.sh expands the payload.
  cat > "${work}/issue-title.fixture" <<EOF
Fix \$(touch ${marker}) & \`touch ${marker}\` {{run_url}} done
EOF
  printf '{"stop_reason":"end_turn","non_empty_diff":true,"diff_bytes":10,"final_text":"did work"}' > "${work}/summary.json"
  printf 'diff --git a/src/ok.go b/src/ok.go\n' > "${work}/run.patch"
  printf '1\t0\tsrc/ok.go\0' > "${work}/numstat.fixture"
  run_publish "${work}" \
    ISSUE_NUMBER=32 \
    EXIT_CLASS=clean \
    PATCH_PATH="${work}/run.patch" \
    SUMMARY_PATH="${work}/summary.json"
  if [ -e "${marker}" ]; then
    bad "untrusted title EXECUTED a \$(...)/backtick (marker created) — title is not literal"
  elif [ ! -e "${work}/PR_CREATED" ]; then
    bad "untrusted-title test: no PR was created"
  elif called 'Fix $(touch' "${work}/calls.log" && called '{{run_url}} done' "${work}/calls.log"; then
    pass "untrusted issue title with metacharacters is carried into --title verbatim, never executed"
  else
    bad "the logged --title did not carry the untrusted metacharacters verbatim"
  fi
  rm -rf "${work}"
}

note "publish.sh tests:"
test_protected_paths_rejected
test_protected_paths_quoted_rejected
test_protected_paths_rename_dest_rejected
test_normal_path_allowed
test_template_traversal_confined
test_no_change_comments
test_failure_comments
test_empty_exit_class_is_setup_failure
test_failed_comment_does_not_abort_silently
test_render_template_literal_single_pass
test_pr_title_from_issue_title
test_pr_title_template_precedence
test_pr_title_empty_falls_back
test_pr_title_untrusted_metacharacters

if [ "${fail}" -ne 0 ]; then
  note "publish.sh: FAILURES"
  exit 1
fi
note "publish.sh: all tests passed"
