#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
script="$script_dir/release-tag-guard.sh"
work=${TEST_TMPDIR:-.scratch}/release-tag-guard-$$
mkdir -p "$work"
work=$(CDPATH= cd -- "$work" && pwd -P)

repo="$work/repo"
mkdir "$repo"
repo=$(CDPATH= cd -- "$repo" && pwd -P)
git -C "$repo" init -q -b main
git -C "$repo" config user.name test
git -C "$repo" config user.email test@example.com
git -C "$repo" config commit.gpgSign false
git -C "$repo" config tag.gpgSign false
printf 'release\n' >"$repo/file"
git -C "$repo" add file
git -C "$repo" commit -qm release
release_commit=$(git -C "$repo" rev-parse HEAD)
git -C "$repo" tag v1.2.3
printf 'main\n' >>"$repo/file"
git -C "$repo" commit -qam main
main=$(git -C "$repo" rev-parse HEAD)
git -C "$repo" remote add origin "$repo"
git -C "$repo" fetch -q origin main:refs/remotes/origin/main

run_ok() {
  output="$work/output"
  : >"$output"
  (cd "$repo" && VERSION="$1" GITHUB_EVENT_NAME="$2" GITHUB_REF="$3" GITHUB_SHA="$4" GITHUB_OUTPUT="$output" "$script") >/dev/null
  grep -Fx "version=$1" "$output" >/dev/null
  grep -Fx "commit=$5" "$output" >/dev/null
}
run_fail() {
  output="$work/output"
  : >"$output"
  if (cd "$repo" && VERSION="$1" GITHUB_EVENT_NAME="$2" GITHUB_REF="$3" GITHUB_SHA="$4" GITHUB_OUTPUT="$output" "$script") >/dev/null 2>&1; then
    echo "unexpectedly accepted: $1" >&2
    exit 1
  fi
}

# Dispatch starts at main HEAD, but must publish the selected older tag commit.
run_ok v1.2.3 workflow_dispatch refs/heads/main "$main" "$release_commit"
run_fail main workflow_dispatch refs/heads/main "$main"
run_fail refs/heads/main workflow_dispatch refs/heads/main "$main"
marker="$work/pwned"
run_fail "v1.2.3; touch $marker" workflow_dispatch refs/heads/main "$main"
[ ! -e "$marker" ]
run_fail v01.2.3 workflow_dispatch refs/heads/main "$main"
run_fail v9.9.9 workflow_dispatch refs/heads/main "$main"
run_fail v1.2.3 push refs/heads/main "$release_commit"
run_fail v1.2.3 push refs/tags/v1.2.3 0000000000000000000000000000000000000000
run_ok v1.2.3 push refs/tags/v1.2.3 "$release_commit" "$release_commit"

# An annotated tag push reports its tag-object SHA; both sides must peel it.
git -C "$repo" tag -a v1.2.4 -m annotated "$release_commit"
annotated_tag=$(git -C "$repo" rev-parse v1.2.4)
run_ok v1.2.4 push refs/tags/v1.2.4 "$annotated_tag" "$release_commit"

git -C "$repo" switch -q --detach "$release_commit"
printf 'branch\n' >>"$repo/file"
git -C "$repo" commit -qam branch
off_main=$(git -C "$repo" rev-parse HEAD)
git -C "$repo" tag v2.0.0
run_fail v2.0.0 workflow_dispatch refs/heads/main "$main"
run_fail v2.0.0 push refs/tags/v2.0.0 "$off_main"
run_fail v1.2.3 schedule refs/heads/main "$main"

printf 'release tag guard tests passed\n'
