#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
script="$script_dir/resolve-main-build.sh"
if [ -n "${TEST_TMPDIR:-}" ]; then
  mkdir -p "$TEST_TMPDIR"
  work=$(mktemp -d "$TEST_TMPDIR/resolve-main-build.XXXXXX")
else
  work=$(mktemp -d)
fi
trap 'rm -rf -- "$work"' EXIT

# Stubs: helm prints $work/chart (or fails with $work/chart-error), cosign
# exits with $work/cosign-status, and gh prints $work/ci-runs.
bin="$work/bin"
mkdir "$bin"
cat >"$bin/helm" <<'EOF'
#!/bin/sh
if [ -f "$STUB_DIR/chart" ]; then cat "$STUB_DIR/chart"; exit 0; fi
cat "$STUB_DIR/chart-error"
exit 1
EOF
cat >"$bin/cosign" <<'EOF'
#!/bin/sh
exit "$(cat "$STUB_DIR/cosign-status")"
EOF
cat >"$bin/gh" <<'EOF'
#!/bin/sh
cat "$STUB_DIR/ci-runs"
EOF
chmod +x "$bin/helm" "$bin/cosign" "$bin/gh"

repo="$work/repo"
mkdir "$repo"
git -C "$repo" init -q -b main
git -C "$repo" config user.name test
git -C "$repo" config user.email test@example.com
git -C "$repo" config commit.gpgSign false
commit() {
  printf '%s\n' "$1" >"$repo/VERSION"
  git -C "$repo" add VERSION
  git -C "$repo" commit -q --allow-empty -m "$2"
  git -C "$repo" rev-parse HEAD
}
c1=$(commit 0.0.47 one)
c2=$(commit 0.0.47 two)
c3=$(commit 0.0.48 release)
c4=$(commit 0.0.48 four)
git -C "$repo" checkout -q -b side "$c2"
off_main=$(commit 0.0.47 side)
git -C "$repo" checkout -q main

short() { printf '%s' "$1" | cut -c1-8; }
published() {
  rm -f "$work/chart" "$work/chart-error"
  printf 'apiVersion: v2\nname: mecatl-studio\nversion: %s\nannotations:\n  org.opencontainers.image.revision: %s\n' "$1" "$2" >"$work/chart"
}
nothing_published() {
  rm -f "$work/chart"
  echo 'Error: GET "https://ghcr.io/v2/stacklok/mecatl/main/charts/mecatl-studio/tags/list": response status code 404: name unknown: repository name not known to registry' >"$work/chart-error"
}
registry_error() {
  rm -f "$work/chart"
  echo 'Error: response status code 403: denied' >"$work/chart-error"
}
reset() {
  nothing_published
  echo 0 >"$work/cosign-status"
  echo 1 >"$work/ci-runs"
  event=workflow_run event_sha=$c4 run_sha=$c4 input_sha='' force='' run_number=30
}
resolve() {
  output="$work/output"
  : >"$output"
  (cd "$repo" && PATH="$bin:$PATH" STUB_DIR="$work" \
    GITHUB_EVENT_NAME="$event" GITHUB_SHA="$event_sha" GITHUB_REPOSITORY=stacklok/mecatl \
    GITHUB_OUTPUT="$output" RUN_SHA="$run_sha" INPUT_SHA="$input_sha" FORCE="$force" \
    RUN_NUMBER="$run_number" RUN_ATTEMPT=1 OCI_REGISTRY=ghcr.io/stacklok/mecatl/main/charts \
    SIGNING_IDENTITY=identity SIGNING_ISSUER=issuer "$script") >"$work/log" 2>&1
}
expect_publish() { # sha version
  if ! resolve; then
    cat "$work/log" >&2
    echo "FAIL ($case): expected to publish $1" >&2
    exit 1
  fi
  for line in publish=true "sha=$1" "version=$2"; do
    grep -Fx "$line" "$output" >/dev/null || {
      cat "$work/log" "$output" >&2
      echo "FAIL ($case): expected $line" >&2
      exit 1
    }
  done
}
expect_skip() {
  if ! resolve || ! grep -Fx publish=false "$output" >/dev/null; then
    cat "$work/log" "$output" >&2
    echo "FAIL ($case): expected an already-published skip" >&2
    exit 1
  fi
}
expect_fail() {
  if resolve; then
    cat "$work/log" "$output" >&2
    echo "FAIL ($case): unexpectedly succeeded" >&2
    exit 1
  fi
  [ ! -s "$output" ] || {
    echo "FAIL ($case): failed run wrote outputs" >&2
    exit 1
  }
}

case='first publication'
reset
expect_publish "$c4" "0.0.49-main.30.1.g$(short "$c4")"

case='newer commit than the last publication'
reset
published "0.0.49-main.29.1.g$(short "$c3")" "$c3"
expect_publish "$c4" "0.0.49-main.30.1.g$(short "$c4")"

case='older and equal commits are skipped'
reset
published "0.0.49-main.29.1.g$(short "$c3")" "$c3"
run_sha=$c2
expect_skip
run_sha=$c3
expect_skip

case='an unverified last chart lets the same commit publish again'
reset
published "0.0.49-main.29.1.g$(short "$c3")" "$c3"
echo 1 >"$work/cosign-status"
run_sha=$c3
expect_publish "$c3" "0.0.49-main.30.1.g$(short "$c3")"
run_sha=$c2
expect_skip

case='dispatch publishes its own commit once CI passed'
reset
event=workflow_dispatch event_sha=$c4 run_sha=''
expect_publish "$c4" "0.0.49-main.30.1.g$(short "$c4")"
echo 0 >"$work/ci-runs"
expect_fail
echo '' >"$work/ci-runs"
expect_fail

case='dispatch rejects another commit without force'
reset
event=workflow_dispatch event_sha=$c4 run_sha='' input_sha=$c3
expect_fail

case='forced rollback crosses a VERSION bump'
reset
event=workflow_dispatch event_sha=$c4 run_sha='' input_sha=$c1 force=true
echo 0 >"$work/ci-runs"
published "0.0.49-main.29.1.g$(short "$c4")" "$c4"
expect_publish "$c1" "0.0.49-main.30.1.g$(short "$c1")"

case='forced run that would not sort above the last publication'
run_number=29
expect_fail

case='invalid or off-main commits'
reset
for bad in '' HEAD "$(short "$c4")" "${c4}0" "$(printf '%s\n%s' "$c1" "$c4")" "$off_main"; do
  run_sha=$bad
  expect_fail
done
event=workflow_dispatch event_sha=$c4 run_sha='' input_sha="$off_main" force=true
expect_fail

case='a failed lookup blocks publishing unless forced'
for setup in registry_error unknown_revision foreign_version; do
  reset
  case "$setup" in
    registry_error) registry_error ;;
    unknown_revision) published "0.0.49-main.29.1.g$(short "$off_main")" 0000000000000000000000000000000000000000 ;;
    foreign_version) published 0.0.49 "$c3" ;;
  esac
  expect_fail
  force=true
  expect_publish "$c4" "0.0.49-main.30.1.g$(short "$c4")"
done

case='last publication off main blocks publishing unless forced'
reset
published "0.0.49-main.29.1.g$(short "$off_main")" "$off_main"
expect_fail
force=true
expect_publish "$c4" "0.0.49-main.30.1.g$(short "$c4")"

case='unsupported event and VERSION'
reset
event=push
expect_fail
reset
c5=$(commit 0.0.48-rc.1 five)
run_sha=$c5
expect_fail

echo "resolve-main-build: all cases passed"
