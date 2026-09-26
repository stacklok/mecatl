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
  [ "$(git -C "$repo" rev-parse HEAD)" = "$5" ]
}
run_fail() {
  output="$work/output"
  : >"$output"
  if (cd "$repo" && VERSION="$1" GITHUB_EVENT_NAME="$2" GITHUB_REF="$3" GITHUB_SHA="$4" GITHUB_OUTPUT="$output" "$script") >/dev/null 2>&1; then
    echo "unexpectedly accepted: $1" >&2
    exit 1
  fi
  [ ! -s "$output" ]
}

# Dispatch must select the same immutable tag ref as its input.
run_ok v1.2.3 workflow_dispatch refs/tags/v1.2.3 "$release_commit" "$release_commit"
run_fail v1.2.3 workflow_dispatch refs/heads/main "$main"
run_fail v1.2.3 workflow_dispatch refs/tags/v1.2.4 "$release_commit"
run_fail main workflow_dispatch refs/tags/main "$main"
run_fail refs/heads/main workflow_dispatch refs/tags/refs/heads/main "$main"
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
run_ok v1.2.4 push refs/tags/v1.2.4 "$release_commit" "$release_commit"
run_ok v1.2.4 workflow_dispatch refs/tags/v1.2.4 "$release_commit" "$release_commit"

# Both commits are on main: moving the same tag after the event was captured
# must fail before checkout/output, even though ref-name and ancestry checks pass.
git -C "$repo" tag -f v1.2.3 "$main" >/dev/null
for event in push workflow_dispatch; do
  run_fail v1.2.3 "$event" refs/tags/v1.2.3 "$release_commit"
  run_fail v1.2.4 "$event" refs/tags/v1.2.4 "$main"
  run_ok v1.2.3 "$event" refs/tags/v1.2.3 "$main" "$main"
  for invalid_sha in '' HEAD refs/tags/v1.2.3 "${main%?}" "${main}0" \
    0000000000000000000000000000000000000000 "$(printf '%s\nHEAD' "$main")"; do
    run_fail v1.2.3 "$event" refs/tags/v1.2.3 "$invalid_sha"
  done
done
# An annotated event object must not hide a moved-tag mismatch either.
git -C "$repo" tag -f -a v1.2.4 -m moved "$main" >/dev/null
run_fail v1.2.4 push refs/tags/v1.2.4 "$annotated_tag"
run_fail v1.2.4 workflow_dispatch refs/tags/v1.2.4 "$release_commit"

workflow="$script_dir/../workflows/release.yml"
# Execute the real validator agreement step, not a duplicate test-only check.
agreement=$(awk '/^      - name: Assert guard and immutable run commits agree$/{on=1; next} on && /^        run: /{sub(/^        run: /, ""); print; exit}' "$workflow")
[ -n "$agreement" ]
(cd "$repo" && RELEASE_COMMIT="$main" sh -eu -c "$agreement")
if (cd "$repo" && RELEASE_COMMIT="$release_commit" sh -eu -c "$agreement"); then
  echo 'validator accepted a different on-main guard commit' >&2
  exit 1
fi
if (cd "$repo" && RELEASE_COMMIT='' sh -eu -c "$agreement"); then
  echo 'validator accepted an empty guard commit' >&2
  exit 1
fi

git -C "$repo" switch -q --detach "$release_commit"
printf 'branch\n' >>"$repo/file"
git -C "$repo" commit -qam branch
off_main=$(git -C "$repo" rev-parse HEAD)
git -C "$repo" tag v2.0.0
run_fail v2.0.0 workflow_dispatch refs/tags/v2.0.0 "$off_main"
run_fail v2.0.0 push refs/tags/v2.0.0 "$off_main"
run_fail v1.2.3 schedule refs/tags/v1.2.3 "$release_commit"

workflow="$script_dir/../workflows/release.yml"
# The privileged graph consumes only the commit emitted by a guard implementation
# checked out from protected main; dispatch must select the requested tag ref.
grep -F "if: github.event_name == 'push' || (github.event_name == 'workflow_dispatch' && github.ref == format('refs/tags/{0}', inputs.tag))" "$workflow" >/dev/null
grep -F 'ref: refs/heads/main' "$workflow" >/dev/null
if grep -F 'org.opencontainers.image.revision=${{ github.sha }}' "$workflow" >/dev/null; then
  echo "release image revision still uses the event workflow SHA" >&2
  exit 1
fi
[ "$(grep -Fc 'org.opencontainers.image.revision=${{ needs.guard.outputs.commit }}' "$workflow")" -eq 5 ]

printf 'release tag guard tests passed\n'
