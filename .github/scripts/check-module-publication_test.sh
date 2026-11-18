#!/bin/sh
set -eu

root=$(git rev-parse --show-toplevel)
work=$(mktemp -d "${TMPDIR:-/tmp}/module-publication-fixture.XXXXXXXX")
trap 'rm -rf "$work"' EXIT
repo=$work/repo
mkdir -p "$repo/internal/adaptersupport" "$repo/contracts/gen/go/mecatl/driver" "$repo/adapters" "$repo/integration/microvm" "$work/bin" "$work/home"
export HOME=$work/home GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null
export GIT_AUTHOR_NAME=Fixture GIT_AUTHOR_EMAIL=fixture@example.com
export GIT_COMMITTER_NAME=Fixture GIT_COMMITTER_EMAIL=fixture@example.com
export GOWORK=off GOTOOLCHAIN=local
unset MODULE_PUBLICATION_VERSION
real_go=$(command -v go)
export REAL_GO=$real_go PUBLIC_LOG=$work/public-log FIXTURE_PUBLISHED_MOD=$work/published.mod
# Mock only the public Go download/build boundary, not parsing or Git ancestry.
cat > "$work/bin/go" <<'EOF'
#!/bin/sh
set -eu
if [ "$1 $2" = 'mod edit' ]; then exec "$REAL_GO" "$@"; fi
[ "$GOWORK" = off ] && [ "$GOPROXY" = https://proxy.golang.org ]
[ "$GOSUMDB" = sum.golang.org ] && [ "$GOPRIVATE" = none ]
[ "$GONOPROXY" = none ] && [ "$GONOSUMDB" = none ]
[ -d "$GOMODCACHE" ] && [ -z "$(ls -A "$GOMODCACHE")" ]
printf '%s\n' "$*" >> "$PUBLIC_LOG"
case "$1 $2" in
  'mod tidy')
    [ "$GOFLAGS" = -mod=mod ]
    if [ "${FAIL_PUBLIC:-0}" != 0 ]; then echo 'mock public download failure' >&2; exit 1; fi ;;
  'list -m') [ "$GOFLAGS" = -mod=readonly ]; printf '%s\n' "$FIXTURE_PUBLISHED_MOD" ;;
  'test ./...') [ "$GOFLAGS" = -mod=readonly ] ;;
  *) echo "unexpected public Go command: $*" >&2; exit 1 ;;
esac
EOF
chmod +x "$work/bin/go"
export PATH=$work/bin:$PATH

git -C "$repo" init -q
manifest() {
  printf 'module %s\n\ngo 1.27.2\n' "$2" > "$repo/$1"
  if [ "$#" -eq 4 ]; then printf '\nrequire %s %s\n' "$3" "$4" >> "$repo/$1"; fi
}
pins() {
  manifest go.mod example.com/root github.com/stacklok/mecatl/adapters "$1"
  manifest integration/microvm/go.mod example.com/microvm github.com/stacklok/mecatl/adapters "$1"
}
manifest internal/adaptersupport/go.mod github.com/stacklok/mecatl/internal/adaptersupport
manifest contracts/gen/go/mecatl/driver/go.mod github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver
manifest adapters/go.mod github.com/stacklok/mecatl/adapters
pins v0.1.0
git -C "$repo" add .
tree=$(git -C "$repo" write-tree)
commit() { git -C "$repo" commit-tree "$tree" -m "$@"; }
base=$(commit base)
main=$(commit main -p "$base")
git -C "$repo" update-ref refs/remotes/origin/main "$main"
git -C "$repo" tag v0.2.0 "$base"
pins v0.2.0
manifest adapters/go.mod github.com/stacklok/mecatl/adapters github.com/stacklok/mecatl/engine v0.2.0
git -C "$repo" add .
tree=$(git -C "$repo" write-tree)
engine=$(commit engine-release -p "$base")
adapters=$(commit adapters-release -p "$engine")
head=$(commit adoption-head -p "$adapters")
merge=$(commit prospective-merge -p "$main" -p "$head")
git -C "$repo" tag engine/v0.2.0 "$engine"
git -C "$repo" tag adapters/v0.2.0 "$adapters"
cp "$repo/adapters/go.mod" "$FIXTURE_PUBLISHED_MOD"
git -C "$repo" update-ref HEAD "$merge"

check() { (cd "$repo" && sh "$root/.github/scripts/check-module-publication.sh" "$@"); }
accept() {
  label=$1; shift
  : > "$PUBLIC_LOG"
  if ! check "$@" > "$work/result" 2>&1; then
    printf 'FAIL: %s\n' "$label" >&2; cat "$work/result" >&2; exit 1
  fi
  grep -q '^test ./\.\.\.$' "$PUBLIC_LOG" || { echo "$label: public smoke skipped" >&2; exit 1; }
}
reject() {
  label=$1; expected=$2; shift 2
  : > "$PUBLIC_LOG"
  if check "$@" > "$work/result" 2>&1; then echo "FAIL: accepted $label" >&2; exit 1; fi
  if [ -n "$expected" ] && ! grep -q "$expected" "$work/result"; then
    printf 'FAIL: %s (wrong failure)\n' "$label" >&2; cat "$work/result" >&2; exit 1
  fi
}
prospective() { "$@" --prospective-main "$merge" --pr-head "$head"; }

prospective accept 'release-branch tags in prospective main'
# Main can advance after GitHub constructs the event's test merge.
advanced_main=$(commit advanced-main -p "$main")
git -C "$repo" update-ref refs/remotes/origin/main "$advanced_main"
prospective accept 'main advanced since prospective merge creation'
# GitHub can also regenerate the merge while the event's base.sha stays older.
regenerated=$(commit regenerated-merge -p "$advanced_main" -p "$head")
git -C "$repo" update-ref HEAD "$regenerated"
PR_BASE=$main accept 'regenerated merge with older event base' --prospective-main "$regenerated" --pr-head "$head"
git -C "$repo" update-ref HEAD "$merge"
git -C "$repo" update-ref refs/remotes/origin/main "$main"
reject 'release-branch tags before adoption' 'not on origin/main'
# Both the direct adapters tag check and nested manifest checks enforce ancestry.
manifest adapters/go.mod github.com/stacklok/mecatl/adapters
reject 'adapters tag before adoption' 'adapters/v0.2.0 is not on origin/main'
manifest adapters/go.mod github.com/stacklok/mecatl/adapters github.com/stacklok/mecatl/engine v0.2.0

# A real merge preserves the exact tagged commits; squash/rebase do not.
git -C "$repo" update-ref refs/remotes/origin/main "$merge"
accept 'actual main merge adoption'
squash=$(commit squash-adoption -p "$main")
rebased_engine=$(commit rebased-engine -p "$main")
rebased_adapters=$(commit rebased-adapters -p "$rebased_engine")
for rewritten in "$squash" "$rebased_adapters"; do
  git -C "$repo" update-ref refs/remotes/origin/main "$rewritten"
  git -C "$repo" update-ref HEAD "$rewritten"
  reject 'squash/rebase adoption' 'not on origin/main'
done
git -C "$repo" update-ref refs/remotes/origin/main "$main"
git -C "$repo" update-ref HEAD "$merge"

reject 'missing PR head' 'prospective main must' --prospective-main "$merge"
reject 'missing prospective main' 'prospective main must' --pr-head "$head"
reject 'wrong reviewed PR head' 'prospective main must' --prospective-main "$merge" --pr-head "$adapters"
reject 'revision expression' 'requires a full commit SHA' --prospective-main HEAD --pr-head "$head"
reject 'head revision expression' 'requires a full commit SHA' --prospective-main "$merge" --pr-head HEAD
reject 'missing CLI value' 'missing value' --pr-head
reject 'unknown option' 'unknown argument' --base "$base"
# Reject non-main first parents, incorrect second parents and non-two-parent merges.
outside_base=$(commit outside-base -p "$base")
wrong_base=$(commit wrong-base -p "$outside_base" -p "$head")
reversed=$(commit reversed-parents -p "$head" -p "$main")
octopus=$(commit octopus -p "$main" -p "$head" -p "$base")
for forged in "$base" "$head" "$wrong_base" "$reversed" "$octopus"; do
  git -C "$repo" update-ref HEAD "$forged"
  reject 'forged prospective merge' 'prospective main must' --prospective-main "$forged" --pr-head "$head"
done
reject 'prospective merge not checked out' 'prospective main must' --prospective-main "$merge" --pr-head "$head"
git -C "$repo" update-ref HEAD "$merge"
git -C "$repo" update-ref refs/remotes/origin/main "$base"
prospective reject 'main does not contain merge base' 'prospective main must'
unrelated=$(commit unrelated-main)
git -C "$repo" update-ref refs/remotes/origin/main "$unrelated"
prospective reject 'main history unrelated to merge base' 'prospective main must'
git -C "$repo" update-ref refs/remotes/origin/main "$main"

# A pseudo-version on the PR branch is still forbidden even in a valid merge.
short=$(git -C "$repo" rev-parse --short=12 "$engine")
manifest adapters/go.mod github.com/stacklok/mecatl/adapters github.com/stacklok/mecatl/engine "v0.0.0-20260929205240-$short"
prospective reject 'PR-only pseudo-version' 'not on origin/main'
short=$(git -C "$repo" rev-parse --short=12 "$base")
manifest adapters/go.mod github.com/stacklok/mecatl/adapters github.com/stacklok/mecatl/engine "v0.0.0-20260929205240-$short"
prospective accept 'actual main pseudo-version'
manifest adapters/go.mod github.com/stacklok/mecatl/adapters github.com/stacklok/mecatl/engine v0.2.0

git -C "$repo" tag -d engine/v0.2.0 >/dev/null
prospective reject 'root tag substituted for nested tag' 'no published nested tag'
git -C "$repo" tag engine/v0.2.0 "$engine"
git -C "$repo" tag -d adapters/v0.2.0 >/dev/null
prospective reject 'missing adapters tag' 'no published nested tag'
outside=$(commit outside-release -p "$base")
git -C "$repo" tag adapters/v0.2.0 "$outside"
prospective reject 'tag outside prospective history' 'adapters/v0.2.0 is not on origin/main'
git -C "$repo" tag -d adapters/v0.2.0 >/dev/null
git -C "$repo" tag adapters/v0.2.0 "$adapters"
manifest integration/microvm/go.mod example.com/microvm github.com/stacklok/mecatl/adapters v0.1.0
prospective reject 'root/integration pin mismatch' 'must pin the same'
pins v0.2.0
MODULE_PUBLICATION_VERSION=v9.9.9 prospective reject 'override missing tag' 'no published nested tag'
FAIL_PUBLIC=1 prospective reject 'public download failure' 'mock public download failure'
# Published metadata must pass the same ancestry policy, not only local metadata.
printf '\nrequire github.com/stacklok/mecatl/provider/openai v0.9.9\n' >> "$FIXTURE_PUBLISHED_MOD"
prospective reject 'unpublished dependency in public manifest' 'no published nested tag'
cp "$repo/adapters/go.mod" "$FIXTURE_PUBLISHED_MOD"
short=$(git -C "$repo" rev-parse --short=12 "$engine")
printf 'module github.com/stacklok/mecatl/adapters\n\ngo 1.27.2\n\nrequire github.com/stacklok/mecatl/engine v0.0.0-20260929205240-%s\n' "$short" > "$FIXTURE_PUBLISHED_MOD"
prospective reject 'PR-only pseudo-version in public manifest' 'not on origin/main (squash-merge risk)'
cp "$repo/adapters/go.mod" "$FIXTURE_PUBLISHED_MOD"
git -C "$repo" update-ref -d refs/remotes/origin/main
reject 'missing fetched main' 'requires a fetched origin/main'
git -C "$repo" update-ref refs/remotes/origin/main "$main"

# Parser failures must not silently turn into an empty requirements list.
mkdir "$work/failing-jq"
printf '#!/bin/sh\nexit 42\n' > "$work/failing-jq/jq"
chmod +x "$work/failing-jq/jq"
set +e
(PATH="$work/failing-jq:$PATH" check) > "$work/result" 2>&1
status=$?
set -e
[ "$status" -eq 42 ] || { echo "jq failure produced status $status, want 42" >&2; exit 1; }

echo 'module publication ancestry and public-boundary controls: passed'
