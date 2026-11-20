#!/bin/sh
# Verify that tracked Go module metadata is clean without modifying the checkout.
set -eu

root=$(git rev-parse --show-toplevel)
cd "$root"

metadata_paths() {
  git "$@" -- \
    ':(top)go.work' \
    ':(top,glob)**/go.mod' \
    ':(top,glob)**/go.sum' \
    ':(exclude,top)go.work.sum'
}

failed=0
if ! metadata_paths diff --cached --quiet; then
  printf '%s\n' 'Staged Go module metadata changes (HEAD to index):' >&2
  metadata_paths diff --cached --name-only >&2
  failed=1
fi

if ! metadata_paths diff --quiet; then
  printf '%s\n' 'Unstaged Go module metadata changes (index to worktree):' >&2
  metadata_paths diff --name-only >&2
  failed=1
fi

untracked=$(metadata_paths ls-files --others --exclude-standard)
if [ -n "$untracked" ]; then
  printf '%s\n%s\n' 'Untracked Go module metadata:' "$untracked" >&2
  failed=1
fi

if [ "$failed" -ne 0 ]; then
  cat >&2 <<'EOF'
Review the files above and include legitimate metadata changes in the commit.
For workspace version drift, run `task deps:sync` and review
`git diff -- go.work '**/go.mod' '**/go.sum'` and its staged counterpart.
After committing intended metadata, rerun `task test:workspace-sync`.
Metadata changes alone do not imply synchronization is needed.
EOF
  exit 1
fi
