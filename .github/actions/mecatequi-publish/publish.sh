#!/usr/bin/env bash
# publish.sh — turn a mecatequi run's patch + summary into a pull request (or, on a
# non-clean run, an honest failure comment on the triggering issue).
#
# RUNS IN THE PRIVILEGED JOB. This is the ONLY script that holds a GitHub write token, and
# it NEVER runs agent output as code: the agent's contribution is a unified diff applied as
# DATA via `git apply`, never `eval`/`source`/a script the agent authored. The summary is
# posted as PR/issue body TEXT, never executed.
#
# INPUTS ARE ENV-FED, never argv-interpolated. Every value the workflow passes — the patch
# path, the summary path, the issue number, the exit class, the GitHub token — arrives via
# an environment variable, so no event-derived string is spliced into this script's source.
#
# Env contract:
#   GH_TOKEN        GitHub token with contents:write + pull-requests:write + issues:write
#   ISSUE_NUMBER    the triggering issue number (for the PR linkage + the failure comment)
#   PATCH_PATH      path to the working-tree patch the run produced
#   SUMMARY_PATH    path to the run-summary JSON
#   EXIT_CLASS      clean | run-failure | setup-failure (empty -> treated as setup-failure)
#   REPO            owner/repo (defaults to $GITHUB_REPOSITORY)
#   BASE_BRANCH     base branch for the PR (defaults to the repo default branch, then
#                   $GITHUB_REF_NAME)
#   MQ_PR_BODY_TEMPLATE   OPTIONAL path (relative to the checkout) to a PR-body template
#                   with {{placeholder}} tokens. When empty, the convention path
#                   .github/mecatequi/pr-body.md is used if present, else the built-in body.
#   MQ_PR_TITLE_TEMPLATE  OPTIONAL one-line PR-title template (same placeholders). When empty,
#                   the DEFAULT title is "<issue title> (#<n>)" — the triggering issue's title
#                   fetched READ-only via `gh issue view` in this privileged publish job
#                   (the token boundary is preserved: the agent job holds no GitHub token, and
#                   the fetched title reaches the body/title render only as a literal `jq --arg`
#                   value, never argv/eval/env). When the title cannot be fetched (empty), the
#                   prior built-in "mecatequi: changes for issue #<n>" is used as the fallback.
#
# PR-BODY / PR-TITLE TEMPLATING (untrusted-value safe). A repo may supply its own PR
# description style via a template file with {{placeholder}} tokens. The substituted VALUES
# include the model's own final_text — AGENT-AUTHORED from UNTRUSTED issue text — so the
# substitution is LITERAL (a value containing `& \ /`, backticks, `$(...)`, or `{{...}}` is
# inserted verbatim, never interpreted), SINGLE-PASS (a value that itself contains a token is
# NOT re-expanded), and DISPLAY-ONLY (written to a --body-file, never executed). The render is
# a small python3 pass (`render_template`) that reads the template + a values JSON file (NOT
# argv, NOT the process env) and does ONE regex replacement keyed off a fixed dict, leaving
# unknown {{tokens}} intact. The trust caveat is ALWAYS force-prepended regardless of template,
# so a custom template can never drop the safety warning. Templating applies to the PR-create
# body + the re-run body refresh ONLY — the failure / no-change / de-dup comment paths are
# unchanged.
set -euo pipefail

: "${GH_TOKEN:?publish: GH_TOKEN is required}"
: "${ISSUE_NUMBER:?publish: ISSUE_NUMBER is required}"
# EXIT_CLASS may be EMPTY when the implement job died before setting its output. Treat an
# empty value as a setup-failure so we still post an honest comment, never abort silently.
EXIT_CLASS="${EXIT_CLASS:-setup-failure}"
[ -z "${EXIT_CLASS}" ] && EXIT_CLASS="setup-failure"
PATCH_PATH="${PATCH_PATH:-}"
SUMMARY_PATH="${SUMMARY_PATH:-}"
REPO="${REPO:-${GITHUB_REPOSITORY:-}}"
MQ_PR_BODY_TEMPLATE="${MQ_PR_BODY_TEMPLATE:-}"
MQ_PR_TITLE_TEMPLATE="${MQ_PR_TITLE_TEMPLATE:-}"

# Root the convention-path lookup at the checkout, NOT the cwd. This script is invoked from
# the mecatequi-publish composite action (its $GITHUB_ACTION_PATH), so the cwd is NOT the
# repo checkout — every checkout-relative path (the convention template, the confinement
# root) is resolved against $GITHUB_WORKSPACE explicitly. Fall back to the cwd only when the
# var is absent (e.g. a local dry-run / the offline test harness).
MQ_WORKSPACE="${GITHUB_WORKSPACE:-$(pwd)}"

export GH_TOKEN

# Authenticate git over HTTPS via GH_TOKEN. The publish checkout uses
# persist-credentials:false (the token is never written to .git/config), so a plain
# `git push` has no credentials ("could not read Username for https://github.com").
# gh's credential helper supplies the token from the environment for the push below.
gh auth setup-git

# A link back to this workflow run, for both comment paths.
run_url="${GITHUB_SERVER_URL:-https://github.com}/${REPO}/actions/runs/${GITHUB_RUN_ID:-}"

# Post a comment on the triggering issue, NEVER letting a failed post abort the script.
#   $1 = path to a --body-file
# WHY THIS IS GUARDED (issue #70 secondary observation). The failure / no-change / de-dup
# comment branches below are the LAST-RESORT honest feedback when a run did not produce a PR
# — exactly the silent-failure case the acknowledge job + these branches exist to close. But
# `gh issue comment` can itself FAIL: the resolved publish token (especially a minted App
# installation token whose installation lacks issues:write, or a fine-grained PAT missing the
# Issues scope) can be UNABLE to resolve/comment on the issue ("GraphQL: Could not resolve to
# an issue or pull request with the number of N"), and a transient API error or a
# deleted/transferred issue does the same. Under `set -euo pipefail` an UNGUARDED
# `gh issue comment` then aborts publish.sh mid-branch with NO terminal signal — the very
# silent failure these branches close (observed on stacklok/atrium#416: implement failed at
# action-load, EXIT_CLASS was empty, publish.sh entered the setup-failure branch, and the
# `gh issue comment` threw the GraphQL resolve error and `set -e` killed the job before the
# comment landed and before `exit 0`). The privileged push / PR-create tail already guards
# its comments with `|| true`; this helper extends the same discipline to the EARLY branches.
# On a failed post it emits a LOUD `::error::` (so the operator at least sees WHY in the run
# annotations) and returns 1, but the caller decides whether to abort — the setup/no-change
# branches deliberately continue to their `exit 0` so the JOB still ends cleanly rather than
# red-with-no-explanation.
post_issue_comment() {
  if gh issue comment "${ISSUE_NUMBER}" --repo "${REPO}" --body-file "${1}"; then
    return 0
  fi
  echo "::error::publish: could not post the issue comment on ${REPO}#${ISSUE_NUMBER} — the publish token may lack issues:write for this repo, or the issue is unreachable. The run result could not be reported back to the issue; see this run log: ${run_url}" >&2
  return 1
}

# A compact, human-readable summary block for the PR/comment body. Read as DATA (jq), never
# executed. Falls back to a placeholder when no summary file exists. When the summary
# carries a non-empty .error (a run-failure terminal), surface it — the binary serialized
# it for exactly this.
summary_block() {
  if [ -n "${SUMMARY_PATH}" ] && [ -s "${SUMMARY_PATH}" ]; then
    jq -r '
      "| field | value |",
      "|---|---|",
      "| stop_reason | " + (.stop_reason // "(none)") + " |",
      "| non_empty_diff | " + ((.non_empty_diff // false) | tostring) + " |",
      "| diff_bytes | " + ((.diff_bytes // 0) | tostring) + " |",
      "| total_tokens | " + ((.usage.total_tokens // 0) | tostring) + " |",
      ( if (.error // "") != "" then "| error | " + (.error | gsub("\n"; " ")) + " |" else empty end )
    ' "${SUMMARY_PATH}"
  else
    echo "_(no run summary was produced)_"
  fi
}

# A single .field read from the summary as DATA (never executed). $1 is a jq path.
summary_field() {
  if [ -n "${SUMMARY_PATH}" ] && [ -s "${SUMMARY_PATH}" ]; then
    jq -r "${1} // \"\"" "${SUMMARY_PATH}"
  fi
}

# Translate the raw exit class into a plain-language cause + next action. The bare class
# string ("setup-failure") is jargon the issue author cannot act on; this maps each class
# to what likely happened and what to do, keeping the run link for the detail.
exit_class_explanation() {
  case "${1}" in
    setup-failure)
      echo "The run could not start — usually a bad/uncatalogued model id or a missing/invalid provider key (e.g. \`OPENROUTER_API_KEY\` / \`OPENAI_API_KEY\`). Check the FIRST error in the run log."
      ;;
    run-failure)
      echo "The run started but ended in failure — a model/provider error, a cancelled run, the no-approver cancel-on-ask (posture \`strict\` + headless), or a \`timeout\`. The \`stop_reason\` in the summary below distinguishes error from cancelled."
      ;;
    *)
      echo "The run ended in an unexpected state (\`${1}\`). Check the run log."
      ;;
  esac
}

# Render a PR-body/title template with LITERAL, SINGLE-PASS {{token}} substitution.
#   $1 = path to the template file
#   $2 = path to a JSON file mapping token name -> replacement value
# Tokens absent from the dict are left INTACT (the operator may use literal {{...}}). Values
# are inserted VERBATIM — sed/regex metacharacters, backticks, $(...) and {{...}} inside a
# value are never interpreted, and a value containing a token is NOT re-expanded (single pass
# over the template, each match resolved once against the dict). The values arrive via a FILE,
# never argv and never the process env, so no attacker-controlled value reaches a shell or
# envsubst. The result is display-only text (a --body-file), never executed.
render_template() {
  python3 - "${1}" "${2}" <<'PY'
import json
import re
import sys

tmpl_path, values_path = sys.argv[1], sys.argv[2]
with open(tmpl_path, "r", encoding="utf-8") as f:
    template = f.read()
with open(values_path, "r", encoding="utf-8") as f:
    values = json.load(f)

# ONE pass: scan the template for {{name}} occurrences; replace each from the dict if the
# name is known, otherwise leave the literal {{name}} untouched. Using a function replacement
# means the replacement string is inserted VERBATIM — re.sub does NOT interpret backrefs/
# metacharacters in a callable's return — and a value that itself contains "{{run_url}}" is
# NOT rescanned (re.sub advances past the inserted text; this is the single-pass guarantee).
pattern = re.compile(r"\{\{\s*([A-Za-z0-9_]+)\s*\}\}")

def repl(m):
    name = m.group(1)
    if name in values:
        return str(values[name])
    return m.group(0)  # unknown token: leave the literal {{...}} intact

sys.stdout.write(pattern.sub(repl, template))
PY
}

# Confine an operator-supplied template path to the checkout (defense-in-depth, CWE-22). The
# template path is OPERATOR-set (not attacker-reachable), but a `../` traversal or a symlink
# pointing outside the checkout would otherwise splice an out-of-tree file into the PR body;
# resolve to an absolute realpath and assert it is inside ${MQ_WORKSPACE} before reading it.
#   $1 = a candidate path (already joined to ${MQ_WORKSPACE})
# Prints the resolved absolute path on success; prints nothing + returns non-zero on escape
# or a missing file (the caller logs the warning + falls back).
confine_to_workspace() {
  local candidate="${1}" ws_real resolved
  ws_real="$(realpath -- "${MQ_WORKSPACE}" 2>/dev/null || true)"
  # -e: the file must exist; resolves symlinks + `..` to a canonical path.
  resolved="$(realpath -e -- "${candidate}" 2>/dev/null || true)"
  [ -z "${ws_real}" ] && return 1
  [ -z "${resolved}" ] && return 1
  # Containment: the resolved path must be ws_real itself or sit under ws_real/.
  case "${resolved}" in
    "${ws_real}" | "${ws_real}"/*) printf '%s\n' "${resolved}"; return 0 ;;
    *) return 1 ;;
  esac
}

# Resolve the PR-body template path per the documented order:
#   1. MQ_PR_BODY_TEMPLATE (explicit, relative to the checkout) if set + present + confined;
#   2. .github/mecatequi/pr-body.md in the checkout (zero-config convention) if present;
#   3. empty -> the caller falls back to the built-in body.
# Prints the resolved absolute path, or nothing when no template applies.
resolve_body_template() {
  local p=""
  if [ -n "${MQ_PR_BODY_TEMPLATE}" ]; then
    if p="$(confine_to_workspace "${MQ_WORKSPACE}/${MQ_PR_BODY_TEMPLATE}")"; then
      printf '%s\n' "${p}"
    else
      echo "::warning::publish: pr-body-template '${MQ_PR_BODY_TEMPLATE}' is missing or resolves outside the checkout; using the built-in PR body" >&2
    fi
    return 0
  fi
  p="${MQ_WORKSPACE}/.github/mecatequi/pr-body.md"
  [ -f "${p}" ] && printf '%s\n' "${p}"
  return 0
}

# ── Non-clean run: post an honest failure comment, open NO PR ────────────────────────────
if [ "${EXIT_CLASS}" != "clean" ]; then
  {
    echo "## mecatequi run did not complete cleanly"
    echo
    echo "$(exit_class_explanation "${EXIT_CLASS}") No pull request was opened."
    echo
    summary_block
    echo
    echo "[View the workflow run](${run_url}) for the per-event trace and the operator verdict line."
  } > "${RUNNER_TEMP}/mecatequi-comment.md"
  # GUARDED: a failed post emits a loud ::error:: but does NOT abort — the run already
  # failed, and the job exiting 0 here means "publish reported what it could"; the operator
  # sees the comment-failure ::error:: in the annotations when the token can't comment.
  post_issue_comment "${RUNNER_TEMP}/mecatequi-comment.md" || true
  echo "publish: posted failure comment for exit_class=${EXIT_CLASS}"
  exit 0
fi

# ── Clean run with no changes: nothing to publish, post an informational comment ─────────
# Branch the headline on the stop reason: end_turn means the model decided no change was
# needed (include its own explanation when present); the budget/limit terminals mean it ran
# out of room before finishing (actionable: raise the budget or narrow the task).
non_empty="$(summary_field '.non_empty_diff')"
if [ "${non_empty}" != "true" ] || [ -z "${PATCH_PATH}" ] || [ ! -s "${PATCH_PATH}" ]; then
  stop_reason="$(summary_field '.stop_reason')"
  final_text="$(summary_field '.final_text')"
  {
    case "${stop_reason}" in
      end_turn)
        echo "## mecatequi run completed — no change needed"
        echo
        echo "The run concluded that no change was needed (\`stop_reason: end_turn\`), so there is nothing to open a PR for."
        if [ -n "${final_text}" ]; then
          echo
          echo "The model's explanation:"
          echo
          printf '%s\n' "${final_text}" | sed 's/^/> /'
        fi
        ;;
      budget | max_turns | max_tool_calls | max_consecutive_failures)
        echo "## mecatequi run stopped before finishing — no file changes"
        echo
        echo "The run hit a budget/limit before finishing (\`stop_reason: ${stop_reason}\`), so it produced no diff. Raise \`max-run-tokens\`/\`timeout\` or the turn/tool-call limits, or narrow the task, then re-run."
        ;;
      no_progress)
        # NOT budget exhaustion — the model ended a turn with no tool call and no meaningful
        # text (an empty/reasoning-only loop). More budget will NOT help; the prompt/task is
        # the lever.
        echo "## mecatequi run stalled — no file changes"
        echo
        echo "The run stopped making progress (\`stop_reason: no_progress\`): the model produced empty / reasoning-only turns and never acted. Raising the budget will NOT help — re-state the task more concretely (a clear, actionable instruction), then re-run."
        ;;
      structured_output)
        # NOT budget exhaustion — the model could not produce output matching the requested
        # schema within the retry budget. A schema/prompt problem, not a token problem.
        echo "## mecatequi run failed schema validation — no file changes"
        echo
        echo "The run could not produce output matching the requested schema (\`stop_reason: structured_output\`) within its validation-retry budget. Raising the token budget will NOT help — check the output schema and the prompt, then re-run."
        ;;
      *)
        echo "## mecatequi run completed — no file changes"
        echo
        echo "The run finished (\`stop_reason: ${stop_reason:-unknown}\`) but produced no working-tree diff. Note: a clean exit is NOT 'task accomplished' — check the stop reason above."
        ;;
    esac
    echo
    summary_block
    echo
    echo "[View the workflow run](${run_url})."
  } > "${RUNNER_TEMP}/mecatequi-comment.md"
  # GUARDED (see post_issue_comment): a failed post is reported as a loud ::error:: but does
  # not abort — the clean-no-diff run is a success, so the job still exits 0.
  post_issue_comment "${RUNNER_TEMP}/mecatequi-comment.md" || true
  echo "publish: clean run with empty diff (stop_reason=${stop_reason}) — posted informational comment"
  exit 0
fi

# ── Clean run with a diff: gate touched paths -> branch -> apply -> commit -> push -> PR ──

# DEFENSE-IN-DEPTH (pwn-request residual): the patch is attacker-INFLUENCED output, so
# reject any patch that touches CI-control / build-control paths before applying it. A
# successful injection could otherwise rewrite the very workflow that runs the agent and
# slip it past human review of the "feature" diff. `git apply --numstat` lists touched
# paths WITHOUT modifying the tree.
#
# WHY -z IS NON-NEGOTIABLE (the path-quoting bypass). WITHOUT -z, `git apply --numstat`
# C-QUOTES any path containing a special byte (a tab, a control char, a non-ASCII byte, a
# backslash or a quote): it wraps the path in DOUBLE QUOTES with a LEADING `"` and
# backslash-escapes the special bytes — e.g. a patch creating `.github/workflows/ci<TAB>x.yml`
# is printed as `".github/workflows/ci\tinject.yml"`. That leading `"` means the
# `^\.github/` anchor NEVER matches, the gate FAILS OPEN, and the later `git apply --3way`
# un-quotes the name and WRITES the file under .github/ — a clean bypass of the residual
# pwn-request defence. `-z` emits NUL-terminated records with the path RAW and UNQUOTED
# (`<added>\t<deleted>\t<rawpath>\0`), so there is no quoting to defeat. A rename emits the
# old and new paths as EXTRA NUL records (their numeric fields are `-`); we therefore test
# EVERY path token, so a rename whose DESTINATION lands under a protected prefix is blocked
# too.
# Write the NUL-delimited numstat to a FILE, never a "$(...)" capture: bash command
# substitution STRIPS NUL bytes, which would re-merge the very records `-z` separates and
# defeat the whole point. We read the records back from the file with `read -d ''`.
numstat_z_file="${RUNNER_TEMP}/mecatequi-numstat.z"
if ! git apply --numstat -z "${PATCH_PATH}" > "${numstat_z_file}" 2>/dev/null; then
  # numstat itself failing means the patch is malformed/un-inspectable for this tree. FAIL
  # SAFE: do NOT fall open — empty the file and fall through to the real apply below, which
  # will fail loudly with a useful message (an un-inspectable patch is never applied past a
  # gate that could not read it).
  : > "${numstat_z_file}"
fi
# Walk the NUL-delimited records and collect every protected RAW path. We split each record
# on the FIRST TWO TABS only (the two numeric fields) and keep the REMAINDER as the raw path
# — the path itself may legitimately contain spaces or further tabs, so an awk space-split
# would corrupt it. A rename's extra path records have no leading numeric fields (no tab to
# strip), so they fall through the cut as the whole token and are matched verbatim. Reading
# NUL-delimited records keeps the embedded-tab/newline payloads intact end-to-end.
protected_re='^(\.github/|\.gitattributes$|\.git/|Makefile$|Taskfile\.ya?ml$)'
blocked=""
while IFS= read -r -d '' record; do
  [ -z "${record}" ] && continue
  # Strip the two leading "<num>\t" fields IFF present (`<added>\t<deleted>\t` prefix). Each
  # substitution removes one leading run-of-non-tab + tab; on a bare extra-path record (no
  # leading numeric field) the first cut is a no-op, so the raw path survives unchanged.
  rawpath="${record}"
  case "${rawpath}" in
    *$'\t'*) rawpath="${rawpath#*$'\t'}"; rawpath="${rawpath#*$'\t'}" ;;
  esac
  if printf '%s' "${rawpath}" | grep -Eq "${protected_re}"; then
    # Collect on its OWN line for the rejection comment (paths may contain tabs/spaces; one
    # protected path per line is enough for a human to see the scope).
    blocked="${blocked}${rawpath}"$'\n'
  fi
done < "${numstat_z_file}"
blocked="${blocked%$'\n'}"
if [ -n "${blocked}" ]; then
  {
    echo "## mecatequi run produced a patch that touches protected paths"
    echo
    echo "The patch modifies CI-control / build-control files, which is rejected automatically (the agent diff is attacker-influenced output). No pull request was opened."
    echo
    echo "Blocked paths:"
    echo
    printf '%s\n' "${blocked}" | sed 's/^/- `/; s/$/`/'
    echo
    echo "[View the workflow run](${run_url})."
  } > "${RUNNER_TEMP}/mecatequi-comment.md"
  # GUARDED (see post_issue_comment): this branch fails the job either way (exit 1); the guard
  # only ensures a failed comment surfaces a ::error:: rather than aborting before the line below.
  post_issue_comment "${RUNNER_TEMP}/mecatequi-comment.md" || true
  echo "::error::publish: patch touches protected paths; refusing to open a PR"
  exit 1
fi

# Deterministic per-issue branch so a re-run updates the SAME PR rather than spawning
# duplicates (documented behaviour).
branch="mecatequi/issue-${ISSUE_NUMBER}"

# Resolve the base branch: explicit BASE_BRANCH, else the repo default branch, else the ref
# this run checked out. The defaultBranchRef call is guarded so a transient API failure does
# not abort the whole publish.
base="${BASE_BRANCH:-}"
if [ -z "${base}" ]; then
  base="$(gh repo view "${REPO}" --json defaultBranchRef --jq '.defaultBranchRef.name' 2>/dev/null || true)"
fi
[ -z "${base}" ] && base="${GITHUB_REF_NAME:-main}"

# Fetch the triggering issue's title (READ-only) for the default PR title + the
# {{issue_title}} placeholder. Same guarded-read idiom as defaultBranchRef above — a
# transient API failure / unreachable issue yields an EMPTY title and the prior literal
# fallback, never an abort. This is a READ in the privileged publish job; the agent job
# holds no GitHub token, so the token boundary is preserved. Flatten to one line + trim a
# trailing space (a PR title is single-line), mirroring the title-template flatten below.
issue_title="$(gh issue view "${ISSUE_NUMBER}" --repo "${REPO}" --json title --jq '.title' 2>/dev/null || true)"
issue_title="$(printf '%s' "${issue_title}" | tr '\n' ' ' | sed 's/[[:space:]]*$//')"

# Post an honest "could not open the PR" comment and exit non-zero. Called from the
# privileged tail (push / PR-create) so a failure THERE is never silent — the run decided
# to open a PR, then could not, and the author must hear about it. $1 is the cause line.
push_failure_comment() {
  {
    echo "## mecatequi could not open the pull request"
    echo
    echo "The run produced a diff that applied cleanly, but the publish step failed: ${1}. No pull request was opened — re-run, or open one from the branch \`${branch}\` manually."
    echo
    echo "[View the workflow run](${run_url})."
  } > "${RUNNER_TEMP}/mecatequi-comment.md"
  gh issue comment "${ISSUE_NUMBER}" --repo "${REPO}" --body-file "${RUNNER_TEMP}/mecatequi-comment.md" || true
}

git config user.name "mecatequi[bot]"
git config user.email "mecatequi@users.noreply.github.com"
git checkout -b "${branch}"

# Apply the agent's patch as DATA. --3way lets trivial drift apply against a slightly-moved
# tree while a REAL conflict still fails loud; git apply parses the diff, never executes it.
if ! git apply --3way --index --whitespace=nowarn "${PATCH_PATH}"; then
  {
    echo "## mecatequi patch did not apply cleanly"
    echo
    echo "The run produced a diff, but it did not apply (even 3-way) onto \`${base}\`. No pull request was opened."
    echo
    echo "[View the workflow run](${run_url})."
  } > "${RUNNER_TEMP}/mecatequi-comment.md"
  # GUARDED (see post_issue_comment): fails the job either way; the guard only ensures a failed
  # comment surfaces a ::error:: rather than aborting before the line below.
  post_issue_comment "${RUNNER_TEMP}/mecatequi-comment.md" || true
  echo "::error::publish: the run patch did not apply (3-way) to ${base}"
  exit 1
fi

# De-dup: if an open mecatequi PR for this issue already exists, push the deterministic
# branch (force-with-lease) and comment instead of opening a duplicate.
existing_pr="$(gh pr list --repo "${REPO}" --head "${branch}" --state open --json number --jq '.[0].number' 2>/dev/null || true)"

git commit -m "$(printf 'mecatequi: changes for issue #%s\n\nAutomated change produced by a mecatequi single-shot run.\n\nCo-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>' "${ISSUE_NUMBER}")"

# From here on a failure must COMMENT before aborting (never silent after deciding to open
# a PR). `set -e` would abort the script on a failed push/PR-create, so guard each with an
# explicit comment-then-exit. The push uses --force-with-lease so a re-run updates the same
# branch safely.
#
# --force-with-lease needs a remote-tracking ref for the branch to lease against. This
# checkout fetched only the run SHA (actions/checkout's refspec), so on a RE-RUN the
# existing remote branch has no tracking ref and the push is rejected with "stale info".
# Fetch the branch into its tracking ref first (a no-op when the branch doesn't exist yet,
# i.e. the first run), so the lease is valid and the force-update overwrites the prior
# attempt as intended.
git fetch origin "+refs/heads/${branch}:refs/remotes/origin/${branch}" 2>/dev/null || true
if ! git push --force-with-lease --set-upstream origin "${branch}"; then
  push_failure_comment "the git push failed (the bot may lack contents:write, or the branch moved)"
  echo "::error::publish: git push to ${branch} failed"
  exit 1
fi

# The PR body is built as TEXT — never executed. It is a DESCRIPTION a reviewer can act
# on, not just harness metadata. The CAVEAT is ALWAYS force-prepended below; the rest of the
# body is either the built-in layout or a repo-provided template.
#
# Built-in body (the default when no template applies — preserved byte-for-byte):
#   1. the model's OWN summary of what it did (`final_text`) — the natural PR description;
#   2. the list of files the patch changed (added/modified/deleted), so the reviewer sees
#      the scope at a glance;
#   3. the run metadata table + run link;
#   4. `Closes #N` (auto-closing on merge is correct — the merge is the human gate; a
#      deployment that prefers NOT to auto-close can swap it for `Refs #N`).
# final_text is the agent's report — it rides in as --body-file DISPLAY text (never argv,
# never executed); the caveat frames it as agent-authored.
final_text="$(summary_field '.final_text')"
changed_files="$(git diff-tree --no-commit-id --name-status -r HEAD 2>/dev/null || true)"

# The trust caveat — NON-NEGOTIABLE, force-prepended to EVERY PR body (built-in or templated)
# so a custom template can never accidentally drop the safety warning. It is NOT a placeholder.
PR_CAVEAT="⚠️ **Agent-authored from the issue text — review carefully before merging.**"

# Emit the built-in default body (everything BELOW the caveat). Factored so the no-template
# path and a future test share one source of truth.
builtin_pr_body() {
  if [ -n "${final_text}" ]; then
    echo "## What the agent did"
    echo
    printf '%s\n' "${final_text}"
  else
    echo "Automated change produced by a mecatequi single-shot run for issue #${ISSUE_NUMBER}."
  fi
  echo
  if [ -n "${changed_files}" ]; then
    echo "## Files changed"
    echo
    printf '%s\n' "${changed_files}" | sed 's/\t/  /; s/^/- `/; s/$/`/'
    echo
  fi
  echo "## Run"
  echo
  summary_block
  echo
  echo "[View the workflow run](${run_url})."
  echo
  echo "Closes #${ISSUE_NUMBER}"
}

# The formatted placeholder values (the SAME formatting the built-in body uses), built once
# and reused for both the body and the title render. When there are no changed files, the
# value is a sentinel (parity with summary_table's "(no run summary…)" fallback) so a
# template author's "## Files changed" header is never left dangling over nothing — the
# built-in body guards its own header, but a template author cannot.
files_changed_fmt="_(no files changed)_"
if [ -n "${changed_files}" ]; then
  files_changed_fmt="$(printf '%s\n' "${changed_files}" | sed 's/\t/  /; s/^/- `/; s/$/`/')"
fi
summary_table_fmt="$(summary_block)"
stop_reason_val="$(summary_field '.stop_reason')"
non_empty_val="$(summary_field '.non_empty_diff')"
diff_bytes_val="$(summary_field '.diff_bytes')"
total_tokens_val="$(summary_field '.usage.total_tokens')"

# Assemble the values dict as JSON via jq (--arg keeps every value LITERAL — no shell/argv
# splicing of the value content). This file, not argv or the env, feeds render_template.
values_json="${RUNNER_TEMP}/mecatequi-pr-values.json"
jq -n \
  --arg what_agent_did "${final_text}" \
  --arg files_changed "${files_changed_fmt}" \
  --arg summary_table "${summary_table_fmt}" \
  --arg run_url "${run_url}" \
  --arg issue "${ISSUE_NUMBER}" \
  --arg issue_ref "#${ISSUE_NUMBER}" \
  --arg issue_title "${issue_title}" \
  --arg stop_reason "${stop_reason_val}" \
  --arg non_empty_diff "${non_empty_val}" \
  --arg diff_bytes "${diff_bytes_val}" \
  --arg total_tokens "${total_tokens_val}" \
  --arg branch "${branch}" \
  --arg base "${base}" \
  '{
    what_agent_did: $what_agent_did,
    files_changed: $files_changed,
    summary_table: $summary_table,
    run_url: $run_url,
    issue: $issue,
    issue_ref: $issue_ref,
    issue_title: $issue_title,
    stop_reason: $stop_reason,
    non_empty_diff: $non_empty_diff,
    diff_bytes: $diff_bytes,
    total_tokens: $total_tokens,
    branch: $branch,
    base: $base
  }' > "${values_json}"

# Resolve the body template (explicit input -> convention path -> none).
body_template="$(resolve_body_template)"

pr_body_file="${RUNNER_TEMP}/mecatequi-pr-body.md"
{
  # The caveat is ALWAYS first, and is never run through the template engine.
  printf '%s\n\n' "${PR_CAVEAT}"
  if [ -n "${body_template}" ]; then
    echo "publish: rendering PR body from template ${body_template}" >&2
    render_template "${body_template}" "${values_json}"
  else
    builtin_pr_body
  fi
} > "${pr_body_file}"

# Resolve the PR title: a template (same placeholders) wins; else the issue title as
# "<issue title> (#<n>)"; else the prior built-in literal (byte-for-byte) as the fallback.
# A title template is rendered, then flattened to one line (a PR title is single-line) and
# the caveat is NOT prepended to the title. The issue-title default uses ${issue_title}
# ONLY as a shell var expanded into the `gh pr create --title "${pr_title}"` argv TOKEN —
# never eval'd — so shell metacharacters in it are inert.
if [ -n "${MQ_PR_TITLE_TEMPLATE}" ]; then
  pr_title="mecatequi: changes for issue #${ISSUE_NUMBER}"
  if title_template="$(confine_to_workspace "${MQ_WORKSPACE}/${MQ_PR_TITLE_TEMPLATE}")"; then
    pr_title="$(render_template "${title_template}" "${values_json}" | tr '\n' ' ' | sed 's/[[:space:]]*$//')"
  else
    echo "::warning::publish: pr-title-template '${MQ_PR_TITLE_TEMPLATE}' is missing or resolves outside the checkout; using the built-in PR title" >&2
  fi
elif [ -n "${issue_title}" ]; then
  pr_title="${issue_title} (#${ISSUE_NUMBER})"
else
  pr_title="mecatequi: changes for issue #${ISSUE_NUMBER}"
fi

if [ -n "${existing_pr}" ]; then
  echo "publish: updated existing PR #${existing_pr} on ${branch}"
  # Refresh the PR description too, so a re-run's PR reflects the LATEST run's summary +
  # changed files (not the stale body from the first attempt).
  gh pr edit "${existing_pr}" --repo "${REPO}" --body-file "${pr_body_file}" || true
  gh issue comment "${ISSUE_NUMBER}" --repo "${REPO}" \
    --body "Updated the existing pull request #${existing_pr} with a fresh mecatequi run. ⚠️ Agent-authored — review carefully before merging. [View the workflow run](${run_url})."
  exit 0
fi

# A failed PR-create after a successful push must COMMENT, never abort silently.
if ! gh pr create \
  --repo "${REPO}" \
  --base "${base}" \
  --head "${branch}" \
  --title "${pr_title}" \
  --body-file "${pr_body_file}"; then
  push_failure_comment "the branch was pushed but \`gh pr create\` failed (the bot may lack pull-requests:write)"
  echo "::error::publish: gh pr create failed for ${branch} -> ${base}"
  exit 1
fi

echo "publish: opened PR from ${branch} -> ${base}"
