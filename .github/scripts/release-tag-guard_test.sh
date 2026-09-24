#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Offline contract test for the shared release gate and its workflow wiring.
set -eu

root=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
workflow="$root/.github/workflows/release.yml"
fixture="$root/.scratch/release-tag-guard-test-$$"

guard_job=$(awk '
  /^  guard:$/ { in_guard = 1 }
  in_guard && /^  [a-zA-Z0-9_-]+:$/ && $0 != "  guard:" { exit }
  in_guard { print }
' "$workflow")
guard_script=$(printf '%s\n' "$guard_job" | awk '
  /^        run: \|$/ { in_run = 1; next }
  in_run && /^          / { sub(/^          /, ""); print; next }
  in_run { exit }
')
if [ -z "$guard_script" ]; then
  echo 'release guard job has no inline tag check' >&2
  exit 1
fi
for job in publish publish-mecatui publish-mecak8s publish-slack-bot publish-studio publish-helm-chart publish-cli; do
  dependencies=$(awk -v job="$job" '
    $0 == "  " job ":" { in_job = 1 }
    in_job && /^  [a-zA-Z0-9_-]+:$/ && $0 != "  " job ":" { exit }
    in_job && /    needs:/ { print; exit }
  ' "$workflow")
  if ! printf '%s\n' "$dependencies" | grep -Eq 'needs: (guard|\[guard,.*\])'; then
    echo "$job does not depend on the release guard" >&2
    exit 1
  fi
done
mkdir -p "$fixture"
trap 'rm -rf "$fixture"' EXIT HUP INT TERM
git -C "$fixture" init -q
git -C "$fixture" checkout -q -b main
printf 'one\n' > "$fixture/data"
git -C "$fixture" add data
git -C "$fixture" -c user.name=Test -c user.email=test@example.com commit -q -m one
git -C "$fixture" -c user.name=Test -c user.email=test@example.com tag -a v1.2.3 -m v1.2.3
git -C "$fixture" update-ref refs/remotes/origin/main HEAD

accept() {
  if ! (cd "$fixture" && VERSION="$1" sh -c "$guard_script" >/dev/null 2>&1); then
    echo "release guard rejected valid tag: $1" >&2
    exit 1
  fi
}
reject() {
  if (cd "$fixture" && VERSION="$1" sh -c "$guard_script" >/dev/null 2>&1); then
    echo "release guard accepted invalid tag: $1" >&2
    exit 1
  fi
}

accept v1.2.3
reject main
reject v1.2.4
git -C "$fixture" branch vbranch
reject vbranch
printf 'two\n' >> "$fixture/data"
git -C "$fixture" add data
git -C "$fixture" -c user.name=Test -c user.email=test@example.com commit -q -m two
git -C "$fixture" update-ref refs/remotes/origin/main HEAD
reject v1.2.3
git -C "$fixture" checkout -q v1.2.3
accept v1.2.3
git -C "$fixture" checkout -q -b unreviewed
printf 'three\n' >> "$fixture/data"
git -C "$fixture" add data
git -C "$fixture" -c user.name=Test -c user.email=test@example.com commit -q -m three
git -C "$fixture" tag v1.2.5
reject v1.2.5

echo 'release tag guard: all checks passed'
