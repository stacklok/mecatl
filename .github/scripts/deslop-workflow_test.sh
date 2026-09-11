#!/usr/bin/env bash
# Offline contract test for the advisory Deslop workflow's authority boundaries.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
workflow="$root/.github/workflows/deslop.yml"
checksum="$root/.github/deslop/deslop-v0.34.0-linux-x64.sha256"
config="$root/.github/deslop/deslop.toml"
failures=0

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  failures=$((failures + 1))
}

require() {
  local text="$1" message="$2"
  if ! grep -Fq -- "$text" "$workflow"; then
    fail "$message"
  fi
}

forbid() {
  local text="$1" message="$2"
  if grep -Fq -- "$text" "$workflow"; then
    fail "$message"
  fi
}

if [[ ! -f "$checksum" ]] || ! grep -Fxq '05c86a283bc98fb9163afe62a57bbd24883dcaf1eb07c69919a99fb6b9d1d562  deslop-0.34.0-linux-x64.tar.gz' "$checksum"; then
  fail 'the committed checksum must pin the Deslop release archive'
fi
if [[ ! -f "$config" ]]; then
  fail 'the committed Deslop configuration is missing'
else
  for pattern in '".scratch/**"' '"contracts/gen/**"' '"**/*_test.go"' '"**/testdata/**"' '"**/node_modules/**"'; do
    if ! grep -Fqxq "  $pattern," "$config"; then
      fail "Deslop configuration must exclude $pattern"
    fi
  done
fi

uses_count="$(grep -Ec '^[[:space:]]*-[[:space:]]+uses:' "$workflow" || true)"
if [[ "$uses_count" -ne 1 ]]; then
  fail 'checkout must be the workflow’s only action dependency'
fi
permissions_count="$(grep -Ec '^permissions:' "$workflow" || true)"
job_permissions_count="$(grep -Ec '^    permissions:' "$workflow" || true)"
permissions_block="$(awk '/^permissions:/{in_permissions=1; next} in_permissions && /^[^[:space:]]/{in_permissions=0} in_permissions && /^  [[:alnum:]_-]+:/{print}' "$workflow")"
if [[ "$permissions_count" -ne 1 || "$job_permissions_count" -ne 0 || "$permissions_block" != '  contents: read' ]]; then
  fail 'workflow must have exactly the read-only contents permission with no job override'
fi

require '  pull_request:' 'workflow must run for pull requests'
require '  workflow_dispatch:' 'workflow must support manual dispatch'
require '  contents: read' 'workflow must use read-only contents permission'
require '    runs-on: ubuntu-24.04' 'job must use ubuntu-24.04'
require '    timeout-minutes: 10' 'job must have a 10-minute timeout'
require '  group: deslop-${{ github.workflow }}-${{ github.ref }}' 'concurrency must be keyed by workflow and ref'
require '  cancel-in-progress: true' 'superseded runs must be cancelled'
require 'actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1' 'checkout must be SHA-pinned'
require 'persist-credentials: false' 'checkout credentials must be disabled'
require 'archive="$tool_dir/deslop-0.34.0-linux-x64.tar.gz"' 'archive path must be fixed'
require 'curl --fail --location --silent --show-error' 'release download must fail closed'
require '--output "$archive"' 'release download must write the fixed archive path'
require 'https://github.com/Nimblesite/Deslop/releases/download/v0.34.0/deslop-0.34.0-linux-x64.tar.gz' 'release URL must be fixed'
require 'sha256sum --check "$GITHUB_WORKSPACE/.github/deslop/deslop-v0.34.0-linux-x64.sha256"' 'download must be verified against the committed checksum'
require 'tar --extract --gzip --file "$archive" --directory "$tool_dir" --strip-components=1' 'archive extraction must use the verified fixed archive'
require '"$RUNNER_TEMP/deslop-v0.34.0/deslop" .' 'workflow must execute the fixed extracted binary against the repository root'
require '--config "$GITHUB_WORKSPACE/.github/deslop/deslop.toml"' 'workflow must use the committed Deslop configuration'
for flag in '--embeddings off' '--no-incremental' '--no-fail-over' '--min-nodes 100' '--nohtml' '--log-to-console' '--output "$RUNNER_TEMP/deslop-v0.34.0/deslop-report"'; do
  require "$flag" "advisory invocation must include $flag"
done

forbid 'pull_request_target:' 'pull_request_target must never be used'
forbid '  push:' 'push trigger must never be used'
forbid 'actions/cache@' 'cache actions must never be used'
forbid 'actions/upload-artifact@' 'artifact actions must never be used'
forbid 'actions/download-artifact@' 'artifact actions must never be used'
forbid 'uses: Nimblesite/Deslop@' 'the Nimblesite composite action must never be used'

archive_assignments="$(grep -Ec '^[[:space:]]+archive=' "$workflow" || true)"
if [[ "$archive_assignments" -ne 1 ]]; then
  fail 'the verified archive path must not be rebound'
fi

checksum_line="$(grep -nF 'sha256sum --check "$GITHUB_WORKSPACE/.github/deslop/deslop-v0.34.0-linux-x64.sha256"' "$workflow" | head -n 1 | cut -d: -f1 || true)"
extract_line="$(grep -nF 'tar --extract --gzip --file "$archive" --directory "$tool_dir" --strip-components=1' "$workflow" | head -n 1 | cut -d: -f1 || true)"
execute_line="$(grep -nF '"$RUNNER_TEMP/deslop-v0.34.0/deslop" .' "$workflow" | head -n 1 | cut -d: -f1 || true)"
if [[ -z "$checksum_line" || -z "$extract_line" || -z "$execute_line" || "$checksum_line" -ge "$extract_line" || "$extract_line" -ge "$execute_line" ]]; then
  fail 'checksum verification must happen before extraction and binary execution'
fi

if [[ "$failures" -ne 0 ]]; then
  exit 1
fi
printf 'Deslop workflow wiring: all checks passed\n'
