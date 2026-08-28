#!/usr/bin/env bash
# check-action-templates.sh — reject a LIVE EMPTY ${{ }} expression in any PARSED scalar of a
# GitHub Actions workflow or composite-action manifest. Driven by `task lint:action-templates`.
#
# WHY THIS EXISTS (the self-inflicted-parse-error guard — issue #70). GitHub's object-templating
# engine EVALUATES `${{ ... }}` expressions inside parsed action.yml / workflow YAML fields
# (name, description, input defaults, output values, run-bodies, …). An EMPTY expression — a
# whitespace-only `${{ }}` / `${{   }}` — is a hard PARSE error ("An expression was expected"),
# so the action FAILS TO LOAD and every run that references it aborts before it starts. This bit
# us for real: the mecatequi-extract-prompt action's `description:` prose explained the
# injection-safety rule with a literal empty `${{ }}` as its own example, which tripped the
# parser and made the whole v0.0.2 reusable workflow unusable for every consumer.
#
# WHAT IT CATCHES (and what it does NOT). It flags an empty/whitespace-only `${{ }}` that appears
# on a NON-COMMENT line of a parsed YAML file. It deliberately EXCLUDES YAML full-line `#`
# comments (the templating engine never evaluates a comment, so a `${{ }}` mention in prose
# documentation there is harmless) — comment exclusion is line-oriented: a line whose first
# non-blank character is `#`. It does NOT scan .md / .sh files (not workflow manifests) and does
# NOT flag a NON-empty expression like `${{ runner.temp }}` (those are valid and load fine). It is
# a TARGETED check for the ONE failure mode, not a general expression linter — actionlint owns the
# rest of the workflow surface.
#
# SCOPE. Every `.github/**/*.yml` (and `*.yaml`) workflow plus every `.github/actions/**/action.yml`
# (and `action.yaml`) composite-action manifest. Plain bash + grep, no dependencies, matching the
# house style of check-reusable-pins.sh.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GH_DIR="$(cd "${HERE}/.." && pwd)"          # the .github/ dir (.github/actions/ -> ..)

# Collect every parsed manifest once: the YAML find already includes composite-action
# manifests below .github/actions/, so no de-duplication structure is needed.
files=()
while IFS= read -r -d '' f; do
  files+=("${f}")
done < <(
  find "${GH_DIR}" -type f \( -name '*.yml' -o -name '*.yaml' \) -print0
)

if [ "${#files[@]}" -eq 0 ]; then
  echo "check-action-templates: found NO workflow/action YAML under ${GH_DIR}" >&2
  echo "  (did the .github/ layout move?)" >&2
  exit 1
fi

fail=0
for f in "${files[@]}"; do
  # grep -n gives the 1-based line number; the ERE matches an empty/whitespace-only expression
  # `${{` + only blanks + `}}`. We then DROP any hit whose line is a YAML full-line comment
  # (first non-blank char is `#`) — the templating engine never evaluates a comment line. The
  # per-line comment filter is robust for these files: action.yml / workflow YAML put `${{ }}`
  # prose either in a leading-`#` comment block (excluded) or in a parsed scalar (flagged).
  while IFS= read -r hit; do
    [ -z "${hit}" ] && continue
    lineno="${hit%%:*}"
    text="${hit#*:}"
    # Strip a leading-comment line: ^[[:space:]]*# means the whole line is a comment.
    if printf '%s\n' "${text}" | grep -Eq '^[[:space:]]*#'; then
      continue
    fi
    echo "check-action-templates: EMPTY \${{ }} expression in a PARSED field: ${f}:${lineno}" >&2
    echo "    ${text}" >&2
    fail=1
  done < <(grep -nE '\$\{\{[[:space:]]*\}\}' "${f}" || true)
done

if [ "${fail}" -ne 0 ]; then
  echo "check-action-templates: a LIVE empty \${{ }} expression will fail GitHub's template parser" >&2
  echo "  (\"An expression was expected\") and abort every run that loads the file. Reword the prose to" >&2
  echo "  drop the empty braces (e.g. \"an inline expression interpolation\"), or move it to a # comment." >&2
  exit 1
fi

echo "check-action-templates: scanned ${#files[@]} workflow/action manifests — no empty \${{ }} in a parsed field"
