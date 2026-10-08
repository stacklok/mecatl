#!/bin/sh
set -eu

root=$(git rev-parse --show-toplevel)
repo="$root/.scratch/module-publication-fixture-$$"
mkdir -p "$repo/internal/adaptersupport" "$repo/contracts/gen/go/mecatl/driver" "$repo/adapters"
trap 'rm -rf "$repo"' EXIT

git -C "$repo" init -q
printf 'module github.com/stacklok/mecatl/internal/adaptersupport\n\ngo 1.27.0\n' > "$repo/internal/adaptersupport/go.mod"
printf 'module github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver\n\ngo 1.27.0\n' > "$repo/contracts/gen/go/mecatl/driver/go.mod"
printf 'module github.com/stacklok/mecatl/adapters\n\ngo 1.27.0\n' > "$repo/adapters/go.mod"
git -C "$repo" add internal/adaptersupport/go.mod contracts/gen/go/mecatl/driver/go.mod adapters/go.mod
git -C "$repo" -c user.name=Fixture -c user.email=fixture@example.com commit -qm base
base=$(git -C "$repo" rev-parse HEAD)
git -C "$repo" update-ref refs/remotes/origin/main "$base"
git -C "$repo" tag v0.1.0 "$base"
git -C "$repo" -c user.name=Fixture -c user.email=fixture@example.com commit --allow-empty -qm pr-head
short=$(git -C "$repo" rev-parse --short=12 HEAD)

printf 'module github.com/stacklok/mecatl/adapters\n\ngo 1.27.0\n\nrequire github.com/stacklok/mecatl/engine v0.0.0-20260929205240-%s\n' "$short" > "$repo/adapters/go.mod"
if (cd "$repo" && sh "$root/.github/scripts/check-module-publication.sh") > "$repo/result" 2>&1; then
  echo 'accepted a PR-head-only module revision' >&2
  exit 1
fi
grep -q 'not on origin/main' "$repo/result"

printf 'module github.com/stacklok/mecatl/adapters\n\ngo 1.27.0\n\nrequire github.com/stacklok/mecatl/engine v0.1.0\n' > "$repo/adapters/go.mod"
if (cd "$repo" && sh "$root/.github/scripts/check-module-publication.sh") > "$repo/result" 2>&1; then
  echo 'accepted a root tag as an engine module tag' >&2
  exit 1
fi
grep -q 'no published nested tag' "$repo/result"

# A publicly named adapters tag on a PR-only commit is still unsafe.
printf 'module github.com/stacklok/mecatl/adapters\n\ngo 1.27.0\n' > "$repo/adapters/go.mod"
printf 'module example.com/root\n\ngo 1.27.0\n\nrequire github.com/stacklok/mecatl/adapters v0.1.0\n' > "$repo/go.mod"
mkdir -p "$repo/integration/microvm"
printf 'module example.com/microvm\n\ngo 1.27.0\n\nrequire github.com/stacklok/mecatl/adapters v0.1.0\n' > "$repo/integration/microvm/go.mod"
git -C "$repo" tag adapters/v0.1.0 HEAD
if (cd "$repo" && sh "$root/.github/scripts/check-module-publication.sh") > "$repo/result" 2>&1; then
  echo 'accepted a PR-head-only adapters tag' >&2
  exit 1
fi
grep -q 'adapters/v0.1.0 is not on origin/main' "$repo/result"

# A parser failure must fail before the checker reaches later manifest checks.
mkdir "$repo/failing-jq"
printf '#!/bin/sh\nexit 42\n' > "$repo/failing-jq/jq"
chmod +x "$repo/failing-jq/jq"
set +e
(cd "$repo" && PATH="$repo/failing-jq:$PATH" sh "$root/.github/scripts/check-module-publication.sh") > "$repo/result" 2>&1
status=$?
set -e
if [ "$status" -ne 42 ]; then
  echo "jq failure produced status $status, want 42" >&2
  exit 1
fi

echo 'module publication negative controls: passed'
