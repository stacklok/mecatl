#!/usr/bin/env bash
# Classify a NUL-delimited list of changed repository-relative paths.
#
# The allowlist intentionally accepts only documentation content: Markdown under
# docs/ and user-docs/, user-docs' Docusaurus category metadata, and README.md.
# Everything else is full validation, including workflow/configuration and build
# files. In particular, docs/lint Go code and the modelith YAML are not docs-only
# changes even though they live below docs/.
#
# This script never evaluates a path as shell code. It prints exactly "true" or
# "false" and treats empty, unterminated, or unknown input as false.
set -euo pipefail

allowed_path() {
  case "$1" in
    README.md|docs/*.md|user-docs/*.md|user-docs/*.mdx|user-docs/_category_.json|user-docs/**/_category_.json)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

count=0
path=""
while IFS= read -r -d '' path; do
  # An empty record cannot name a repository path and indicates malformed input.
  if [[ -z "$path" ]] || ! allowed_path "$path"; then
    printf 'false\n'
    exit 0
  fi
  count=$((count + 1))
done

# read returns non-zero at EOF. A non-empty remainder means the producer omitted
# the required NUL delimiter; an empty stream must not be a vacuous docs-only run.
if [[ "$count" -eq 0 ]] || [[ -n "$path" ]]; then
  printf 'false\n'
  exit 0
fi

printf 'true\n'
