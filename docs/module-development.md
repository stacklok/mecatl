# Develop changes across Go modules

Use this procedure when a change spans the engine, providers, persistence modules,
or host. First verify the same-checkout candidate. Then verify each released module
against its declared dependencies as you publish dependencies before their callers.
These are separate kinds of evidence.

Run commands from the repository root unless a step specifies a module directory.
Use the Go toolchain declared in `go.mod` and install [Task](https://taskfile.dev/).
The module inventory is in [`go.work`](../go.work); root `go test ./...` does not
run nested modules' own tests.

## 1. Identify the affected modules

Inspect [`go.work`](../go.work) for module membership, then read the changed
modules' `go.mod` files and the requirements of other workspace members to find
their callers. Directory nesting alone does not establish a dependency; the
manifests define the dependency graph.

Classify public Go API changes under the [engine compatibility policy](../engine/COMPATIBILITY.md)
and [adapter compatibility policy](../adapters/COMPATIBILITY.md). For engine API
changes, regenerate snapshots with `task api:update` and record the changelog
classification. Assess [wire compatibility](architecture/api-surface.md#wire-api-compatibility)
and stored-data compatibility separately when those contracts change. Use owning
generation tasks for generated files.

## 2. Test the assembled candidate

Develop the coordinated source changes together before updating requirements to
versions that have not been published. For focused iteration, select one module
against this checkout's workspace:

```sh
task test:workspace:module MODULE=provider/openai
task test:workspace:module:race MODULE=provider/openai
```

`MODULE` accepts a module directory from `go.work`; use `MODULE=.` for root.
To test that module with workspace mode disabled, use `task test:module` or
`task test:module:race` with the same `MODULE` argument.

Then verify the complete candidate:

```sh
task --concurrency 3 test:workspace
task --concurrency 3 test:workspace:race
```

These tasks select this checkout's absolute `go.work`, even if the environment has
`GOWORK=off` or names another workspace. They run root and every nested module's
tests plus the offline `microvm_dev` tagged proof. They do not tidy requirements.

Workspace mode uses the local modules together with a combined third-party
module graph. A shared dependency can resolve to a higher version than a module
uses independently. A pass proves candidate integration, not standalone dependency
closure or public availability.

For a coordinated release, include your local workspace result in the PR with the
commit, any uncommitted candidate changes, Go version, command, and outcome. Rerun
checks affected by later changes. Ordinary CI does not automatically run the full
workspace suite; the local assertion is accepted as workspace evidence.

## 3. Reconcile dependency metadata deliberately

Keep `go.work` and every module's `go.mod` and `go.sum` committed. Root
`go.work.sum` is ignored: Go creates and updates its supplemental workspace
checksums locally. Those changes do not belong in the PR.

Before reconciling published dependencies, test a module against the current sibling
source and check its dependency metadata:

```sh
task test:module-candidate MODULE=provider/openai
```

Omit `MODULE` to check every member of `go.work` independently. The check uses
temporary manifests with local sibling replacements and excludes unrelated
workspace members from dependency resolution. It reports missing or stale
requirements, dependency classifications, required third-party checksums, and
Go/toolchain minimums, then runs the module's Go tests with the temporary manifest
and a 20-minute timeout per test binary. Each invocation executes tests with Go's
test-result cache disabled; test failures fail the task.
Use the repository's required Go toolchain; this check disables automatic toolchain
downloads and ignores ambient `GOWORK` and `GOFLAGS` values.

The dependency commands use temporary copies of checkout metadata. Tests run in
the source directories with their normal filesystem access. To apply metadata repairs:

```sh
task tidy:module-candidate MODULE=provider/openai
git diff -- provider/openai/go.mod provider/openai/go.sum
```

Repair preserves declared sibling versions and original replacements. A missing
sibling requirement needs an explicitly chosen published version; repair refuses
to save a synthetic version. All selected modules must pass their tests before any
repairs are applied. Files are replaced individually after checking for concurrent metadata
edits; an interrupted repair can leave partial changes, so review the diff and rerun.

Candidate checks prove dependency resolution with current sibling source, whose
requirements still contribute to the selected dependency versions. They do not
prove compatibility with published sibling versions or replace the standalone and
publication checks below. Ordinary CI still uses its existing standalone gates.

If imports or requirements changed, tidy the affected module against its declared
published dependencies and review the diff. For example:

```sh
task tidy:module MODULE=provider/openai
git diff -- provider/openai/go.mod provider/openai/go.sum
```

`task tidy:module` always uses `GOWORK=off` and tidies only the selected module.
Inspect its `replace` directives as well as
its requirements. Tidy can fail while source depends on an API absent from its
published dependency version; report that boundary and finish reconciliation after
the prerequisite release. Do not invent a PR-head version to bypass publication.

`task tidy` is broader: it tidies root, runs `go work sync`, then tidies nested
modules with workspace mode disabled. **`go work sync` can upgrade requirements in
member modules to workspace-selected versions.** Run it only when that
synchronization is intended, and review all resulting manifests. It is not a
checksum-only repair command.

A growing `go.work.sum` can reflect additional package, platform, or dependency
metadata loading. Regeneration covers only the commands run; it is not a complete
all-platform checksum inventory. Per-module sums remain shared checksum evidence.
Normal public checksum-database verification still applies, but an ignored
workspace sum does not share prior supplemental checksum expectations, including
for private modules outside that database. Investigate checksum mismatches; do not
erase sum files to suppress them.

After reviewing and committing intended metadata through the normal development
workflow, rerun verification and check cleanliness:

```sh
task test:module-metadata
```

The check reports staged, unstaged, and newly untracked module metadata without
modifying it. Intentional uncommitted metadata fails too. CI uses the same check
after its existing Build preparation and Test suite; ignored `go.work.sum` updates
are allowed. A clean result proves only that tracked metadata is stable.

## 4. Merge and release dependencies first

Prepare the coordinated PRs in dependency order. When source changes require an
unpublished dependency API, project CI will be red during the transition: standalone
checks still use the old published pins, even if the assembled workspace passes.
This is an expected dependency-publication gap. Record the affected checks and the
missing dependency versions in each PR; investigate unrelated failures normally.

Each module's standalone checks must pass again after its prerequisite dependencies
are published and its pins are updated and tidied. Project-wide CI must return to
green after the final root and integration pin updates in step 5. A workspace pass
alone does not close this gap or waive required checks. Obtain an explicit maintainer
decision on any intermediate merge that cannot satisfy the existing required checks.

### Preview the explicit release batch

Use the reviewed module-release workflows for nested Go tags. Application releases
(`v*`) and SDK releases (`sdk/typescript/v*`, including npm staging and human 2FA)
remain separate, with independent versions. A root release does not publish nested
Go modules.

The [publication policy](../.github/scripts/module-publication-policy.json)
classifies every `go.work` member and determines which modules are eligible for
independent publication. Use the `inventory` command below for the current
classification. See the [policy reference](../.github/scripts/module-publication-policy.md)
for field meanings, how the tooling uses them, and when to update the file.

Use a full checkout with fetched `origin/main` and tags, Python 3, and the declared
Go toolchain. Save the exact module-directory-to-version map in
`.scratch/module-release-batch.json`. For example, replace both placeholders in:

```json
{"engine": "<EXACT_ENGINE_VERSION>", "provider/openai": "<EXACT_PROVIDER_VERSION>"}
```

Humans choose canonical `vX.Y.Z` versions (or canonical prereleases) under each
module's compatibility policy. Automation does not choose versions, add modules,
or accept a pseudo-version as a requested release. Run the local read-only checks:

```sh
CANDIDATE='<FULL_MAIN_COMMIT_SHA>'
BATCH=.scratch/module-release-batch.json
python3 .github/scripts/module-release.py inventory --candidate "$CANDIDATE"
python3 .github/scripts/module-release.py preview --candidate "$CANDIDATE" --batch "$BATCH"
```

Preview archives the exact commit, ignoring uncommitted source changes. It shows
dependency order, changes since each previous tag, compatibility/changelog paths,
existing pins, and proposed selected-module pin updates. Review the source diffs
and version choices; the reported policy paths are not an automated SemVer verdict.
Fix unclassified modules, cycles, replacement leakage, tag conflicts, or missing
prerequisites before requesting a release. An unchanged prerequisite can retain a
suitable published pin; selection never expands automatically. Preview uses local
Git/Go metadata, makes no GitHub/ref writes, and does not establish public download
availability or human authorization.

### Request and review publication

Before the first release request, an administrator must complete the
[release protection setup](#release-protection-setup). The repository-wide Actions
variable `MODULE_RELEASES_ENABLED` is an initial rollout gate: leave it unset
through deployment, settings verification, and the separately authorized
enforcement rehearsal, then set it to exactly `true` and leave it enabled for
normal releases. It is not a per-run input or a switch to toggle between release
stages. Passing offline tests does not establish those live prerequisites.

1. With explicit authorization for the exact mapping, manually run
   [`request-module-release.yml`](../.github/workflows/request-module-release.yml)
   on `main`. GitHub calls this a workflow **dispatch** (`workflow_dispatch`): in
   **Actions**, select **Request module releases**, choose **Run workflow**, and
   select `main`. Supply the JSON object above as the required `batch` input,
   rather than a local filename. An operator can run it directly or instruct an
   agent to run it using authorized human-account authentication and the approved
   mapping; the workflow requires a human triggering account. This opens a
   designated-App-authored request PR under `.github/module-releases/`, rather
   than publishing immediately. Inspect that PR's actual candidate and diff;
   main can have advanced since your local preview.
2. Review the exact set, versions, dependency order, and compatibility evidence.
   Have a human merge the request PR into `main`. The actual merge SHA, including a
   squash merge, fixes the candidate; the PR-head SHA is not the released identity.
   Changing the authorized set or versions requires a new reviewed request.
3. Watch [`module-releases.yml`](../.github/workflows/module-releases.yml). It
   discovers outstanding reviewed requests after request merges, main CI completion,
   and manual dispatch, and queues continuation after successful stages. It verifies
   designated-App authorship, same-repository provenance, exact reviewed bytes,
   merge linkage, and a non-bot human merger. No PR auto-merges.
4. For each generated preparation PR, review the selected module's dependency pins,
   tidy diff, requested version, and any source changes since the original candidate.
   A human merge reconfirms those contents and supplies the next exact candidate.
   Closing it unmerged pauses the batch. Only selected Go modules receive preparation
   PRs; root/integration adoption remains step 5.

The workflow qualifies dependencies before their callers. Before each tag it
requires successful ordinary main-push CI for that exact candidate, identified by
repository, workflow, run, and attempt. A cancelled original CI run can be rerun at
its original SHA, up to attempt 3 and within the tooling's 25-day evidence window.
The main-push rerun concurrency group keeps recovery separate from newer main CI.
Candidates predating that concurrency fix need a new reviewed request: reruns use
the original workflow revision. Missing, failed, expired, or ineligible CI stops
publication; newer-SHA CI cannot substitute for it.

Qualification runs workspace-disabled tidy-diff, build, and tests against declared
published dependencies, plus owning API/changelog checks. It supplies no checkout
replacements. Advisory `gorelease` remains advisory; human diff review owns SemVer
judgment. Candidate execution runs without App secrets or write credentials.
Protected main-side jobs revalidate authority and settings before minting narrow,
short-lived App tokens for the exact PR or tag write.

After each tag, a fresh external module and caches resolve the exact version
through `proxy.golang.org` and `sum.golang.org`, verify tag/commit/source identity,
exercise the owned import fixture, and check standalone closure. The support
fixture uses a legitimate same-repository-path import of Go `internal` packages.
Only successful public proof permits downstream preparation and publication.

### Run release-only diagnostics locally

These commands do not write GitHub refs or replace reviewed-request/CI authority.
They execute candidate or downloaded Go code: use an unprivileged environment
without release credentials. They can download public dependencies and run full
selected-module suites; keep them out of routine infrastructure checks.

```sh
python3 .github/scripts/module-release.py qualify --candidate "$CANDIDATE" --batch "$BATCH"
MODULE=provider/openai
VERSION='<EXACT_PUBLISHED_VERSION>'
python3 .github/scripts/module-release.py prove --candidate "$CANDIDATE" --module "$MODULE" --version "$VERSION"
```

`qualify` cannot resolve unpublished batch versions through local replacements;
qualify dependent modules after public prerequisites and reviewed pin preparation.
For `prove`, set `CANDIDATE` to the exact tagged commit. Public checks override
ambient workspace/private-proxy settings and use fresh module/build caches.
The shell entrypoint `check-module-publication.sh` also forwards explicit
subcommands to this CLI.

`GOWORK=off` alone leaves local `replace` directives effective. Root and both
microVM modules retain local replacements, so their workspace-disabled tests do
not prove public dependency resolution. The existing persistence check remains:

```sh
task test:module-publication
# Set ADAPTERS_VERSION to the authorized published version:
task test:module-publication VERSION="$ADAPTERS_VERSION"
```

It retains persistence-module pin/main-ancestry checks, root/integration adapters
pin agreement, and a fresh external build of the chosen adapters version. It
requires public proxy access and does not prove every module's publication.

### Resume or repair a stopped batch

A failed stage stops the entire batch, including independent modules not yet
published. Completed tags remain immutable. A failed post-tag proof is
**published-but-unverified**, not a rollback or success; the workflow tries public
proof at most three times before stopping. Inspect the failed run and fix the
cause before a human dispatches `module-releases.yml` on `main` with
`retry_request` set to the exact merged request PR number. Empty input only
reconciles; unrelated events do not retry failures. Use a fresh workflow dispatch,
not **Re-run jobs** on an older release run. Explicit retry can open a fresh
preparation PR after an unmerged close, still requiring human merge.

Retry reconstructs authority from reviewed commits, PRs, exact tags, and trusted
run/attempt artifacts. Same-version/same-qualified-SHA work can resume; a different
SHA is a hard conflict, and tag existence alone is insufficient evidence. Later
main activity does not advance a fixed candidate silently. Missing or expired
provenance requires a new reviewed request; use the request workflow's optional
`supersedes` JSON list of earlier reviewed request PR numbers to retire them without
reusing their proofs. Plan/result artifacts have 90-day retention; cancelled-CI
recovery has the shorter window above. Successful CI evidence does not expire merely
by age while its trusted run/attempt remains available. Preserve request/preparation
PR, run/attempt, candidate, tag, and public-proof links when reporting completion.

Never move, delete, or reuse a production tag as recovery. Invalid public versions
need forward fixes because Go proxy/checksum history cannot be retracted. For an
exceptional administrative repair, stop automation, inspect effective repository
and inherited rules plus exact ref/provenance, obtain separate approval, and have
an authorized administrator repair only that scope where organization policy
allows. Restore protections and repeat configuration checks and the relevant
separately authorized enforcement rehearsal before enabling releases again.

### Release protection setup

Deploy the module workflows and all shared-App application/SDK guards together
before enabling module releases; root/SDK namespace and provenance hardening can
be deployed first while the module feature remains disabled.
Use the [settings reference](../.github/scripts/module-release-protection.md) for
identity variables, credential metadata, effective rules, and read-only
`diff`/`check`. `apply` is also a dry run unless `--confirm` is explicitly supplied;
confirmed apply requires separate human authorization and an administrator's
credential. The release App must not administer its own protections.

The checker reuses the existing environment-scoped App key/client ID to sign a
short-lived App JWT for identity and repository-installation **GETs before minting
any installation write token**. Installed rights and repository membership are
verified through the App-authenticated API; no human OAuth/user-token/refresh flow
is required. Key/JWT bytes stay out of arguments, logs, artifacts and durable files,
and candidate jobs never receive them. Local human metadata inspection can instead
use an explicitly supplied short-lived `RELEASE_APP_JWT` with normal `gh` auth.

Routine `check` verifies effective tag/environment protections and deployed workflow
identities. Normal `GITHUB_TOKEN` read access is tried first; `RELEASE_SETTINGS_TOKEN`
is only an optional human metadata override if the platform withholds required
environment/ruleset details. Missing access is an actionable rollout blocker, not
a request to broaden the App's permissions. The separate rollout-only `audit`
checks key scope via secret-name metadata (or use an equivalent authorized admin
audit); it never requires a reader secret of a particular name to exist. Complete
this scope audit before module enablement, not on every release. The App itself
needs Contents/write and Pull requests/write without Administration or Workflows;
job `GITHUB_TOKEN` Actions/write handles CI recovery and continuation.

With `MODULE_RELEASES_ENABLED` unset/false, existing root/SDK flows keep their
main-only environment, verified existing-App identity, constrained namespaces and
exact human-merge provenance without depending on module-wide settings or new
numeric identity variables. The coordinated enabled rollout adds the full global
settings preflight to every shared-App path before write-token minting.

Verify App-only tag creation and separate no-bypass update/deletion restrictions
for root, SDK, and every supported Go prefix. Keep stronger inherited rules. Before
enabling entrypoints, obtain separate authorization for a denial rehearsal in an
isolated repository or non-release namespace with equivalent protections, and map
production prefixes to their effective rules and App installation. Record
**live-enforced in the fixture** separately from **configuration-verified in
production**. A successful configuration check is not live enforcement evidence.

## 5. Finish integration

Once dependency releases exist, finish root and integration pins, tidy affected
modules, and run the existing full gates:

```sh
task lint
task --concurrency 3 test
task --concurrency 3 test:race
go run ./cmd/mecademo
```

Run the workspace race suite against the final coordinated candidate, then verify
metadata cleanliness. Keep unavailable publication proofs or failing checks visible
in the PR rather than treating a workspace pass as release readiness.

## Command reference

| Command | Dependency/test scope |
| --- | --- |
| `task build` | Builds binaries and nested modules using normal workspace discovery |
| `task test`, `task test:race` | All modules with `GOWORK=off`; local replacements remain; tagged leg uses ambient workspace selection |
| `task test:workspace`, `task test:workspace:race` | All modules and tagged proof against this checkout's workspace |
| `task test:module MODULE=provider/openai`, `task test:module:race MODULE=provider/openai` | Selected module with `GOWORK=off`; local replacements remain |
| `task test:workspace:module MODULE=provider/openai`, `task test:workspace:module:race MODULE=provider/openai` | Selected module against this checkout's workspace |
| `task test:engine-standalone`, `task test:provider-standalone` | Module build/tests with workspace off; inspect manifests for the actual dependency closure |
| `task test:microvm-standalone`, `task test:microvm-integration-standalone` | Workspace-disabled verification with local replacements retained |
| `task tidy:module MODULE=provider/openai` | Tidy only the selected module with `GOWORK=off`; no workspace synchronization |
| `task tidy` | Root tidy, workspace synchronization, then nested module tidy; can edit requirements |
| `task test:module-candidate MODULE=provider/openai` | Per-module metadata check and Go tests with current sibling source, using temporary manifests; omit `MODULE` for all workspace members |
| `task tidy:module-candidate MODULE=provider/openai` | Repair candidate metadata while preserving sibling versions and original replacements; review the diff |
| `task test:module-metadata` | Non-mutating cleanliness check of committed Go metadata, excluding root `go.work.sum` |
| `task test:module-release-tools` | Fast offline policy/state/settings API tests with test-owned fixtures; also runs in `test:workspace-infrastructure` |
| `task test:module-release-integration` | Explicit comprehensive offline release tooling tests, including real temporary Go modules/file proxy and mocked GitHub/workflow callers; no live releases |
| `task test:module-publication` | Public resolution/main-ancestry proof for the persistence release policy |

## Next steps

Use the owning module's compatibility and release policy to select versions, and
record the candidate, standalone, and publication evidence separately in the PR.
