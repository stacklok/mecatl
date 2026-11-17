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

## 4. Prepare, publish, and adopt a coordinated batch

A Go-module release is one explicit, coordinated batch. It has two human-merged
pull requests: PR1 prepares every selected module against a fixed `main` commit
`B`, and PR2 adopts the published batch into `main`. The controller qualifies
the entire PR1 merge commit `R` before creating the first tag, then creates and
publicly proves immutable dependency-first tags from `R`. It never advances `R`
while publishing.

The batch controller runs only from trusted `main` tooling. Its workflow run
commit `T` is distinct from `R`: `T` identifies the controller and policy, and
`R` identifies the release source. Root application and TypeScript SDK releases
remain independent tracks. A module batch does not change their versions or
release them.

The [publication policy](../.github/scripts/module-publication-policy.json)
classifies every `go.work` member and determines which modules are eligible for
independent publication. Use the `inventory` command below for the current
classification. See the [policy reference](../.github/scripts/module-publication-policy.md)
for field meanings, how the tooling uses them, and when to update the file.

### Preview the explicit release batch

Use a full checkout with fetched `origin/main` and tags, Python 3, and the
declared Go toolchain. Select an exact `main` commit for `B` and save the exact
module-directory-to-version map in `.scratch/module-release-batch.json`. For
example, replace both placeholders in:

```json
{"engine": "<EXACT_ENGINE_VERSION>", "provider/openai": "<EXACT_PROVIDER_VERSION>"}
```

Humans choose canonical `vX.Y.Z` versions (or canonical prereleases) under each
module's compatibility policy. Automation does not choose versions, add modules,
or accept a pseudo-version as a requested release. Run the local read-only
checks:

```sh
BASE='<FULL_MAIN_COMMIT_SHA>'
BATCH=.scratch/module-release-batch.json
python3 .github/scripts/module-release.py inventory --candidate "$BASE"
python3 .github/scripts/module-release.py preview --candidate "$BASE" --batch "$BATCH"
```

Preview archives the exact commit, ignoring uncommitted source changes. It shows
dependency order, changes since each previous tag, compatibility/changelog paths,
existing pins, and proposed selected-module pin updates. Review the source diffs
and version choices; the reported policy paths are not an automated SemVer
verdict. Fix unclassified modules, cycles, replacement leakage, tag conflicts,
or missing prerequisites before requesting a release. An unchanged prerequisite
can retain a suitable published pin; selection never expands automatically.
Preview uses local Git/Go metadata for releases already on main. Off-main public
release history requires authenticated read access through `GH_TOKEN` or `gh`
authentication; provenance is checked by tooling from fetched `origin/main`.
Preview makes no GitHub/ref writes and does not establish public download
availability or human authorization.

### Request and review a coordinated batch

Before the first release request, an administrator must complete the
[release protection setup](#release-protection-setup). The repository-wide Actions
variable `MODULE_RELEASES_ENABLED` is an initial rollout gate: leave it unset
through deployment, settings verification, and the separately authorized
enforcement rehearsal, then set it to exactly `true` and leave it enabled for
normal releases. It is not a per-run input or a switch to toggle between release
stages. Passing offline tests does not establish those live prerequisites.

The protection setup also requires the repository variable
`RELEASE_BATCH_HUMAN_TEAM`. Set it to the reviewed organization team slug for
human batch writers. The checker verifies that the team has repository writer
access and contains only human accounts. Separately authorize and enable
repository merge commits before rollout. Ordinary pull requests can continue to
use squash merges, but PR2 requires a merge commit.

1. With explicit authorization for the exact mapping and `B`, manually run
   [`request-module-release.yml`](../.github/workflows/request-module-release.yml)
   on `main`. In **Actions**, select **Request module releases**, choose **Run
   workflow**, select `main`, and supply the JSON object as `batch` and the full
   `B` SHA as `base`. The workflow requires a human triggering account. It
   reserves the batch before creating branches or PRs. If another reservation is
   active, it reports that batch instead of queueing or changing a version
   promise.
2. Review PR1, `Prepare module batch <ID>`, from
   `module-batch/<ID>-prep` into `module-batch/<ID>`. It contains the complete
   selected-module pin, sum, and permitted source/metadata diff. A human
   **squash-merges** it. The merge commit `R` must have sole parent `B` and the
   same whole tree as the reviewed preparation head. Do not merge PR1 into
   `main`.
3. The trusted-main controller binds `R`, then runs complete release-aware CI and
   exact-version staged qualification for the whole batch. Candidate execution
   has no App secret or write credential. Qualification checks the final
   manifests, standalone tidy/build/tests, workspace integration, selected
   module consumers, owning API/changelog checks, and staged artifacts. It is
   release authority only when its run, attempt, artifacts, `R`, batch mapping,
   and `T` agree.
4. After whole-batch qualification succeeds, the controller tags selected modules
   from `R` in dependency order. Each tag is immutable and receives a fresh
   public proxy/checksum-database proof before the next tag. No human checkpoint,
   source update, pin update, or candidate advance occurs between module tags.
   A partial failure leaves already-created tags immutable and stops the batch.
5. After every selected module has public proof, the controller opens PR2 from
   `module-batch/<ID>` to `main`. Review the public-version integration result
   and merge it with a **merge commit**, preserving `R` as a direct parent. Do
   not squash, rebase, or cherry-pick PR2. Normal CI validates the prospective
   main merge before adoption.

`module-batch/<ID>` has a logical freeze after PR1. GitHub branch protection
still permits qualifying PR updates, but the controller permanently binds the
release to `R` and rechecks the batch tip, graph, and merge identities before
publication and adoption writes. Any unexpected tip movement stops the batch;
it never reuses qualification at a different SHA.

If current `main` conflicts with PR2, use a separately reviewed
`module-adoption/<NAME>` branch descending from `R`, then dispatch the controller
with that branch as `adoption_head`. Review and test the actual integration
result. The original batch branch and tags remain fixed. If a damaged batch
branch blocks abandonment or finalization, stop automation and use a separately
reviewed repair; the controller does not repair it automatically.

### Run release-only diagnostics locally

These commands do not write GitHub refs or replace batch/qualification authority.
They execute candidate or downloaded Go code: use an unprivileged environment
without release credentials. They can download public dependencies and run full
selected-module suites; keep them out of routine infrastructure checks.

```sh
python3 .github/scripts/module-release.py qualify --candidate "$BASE" --batch "$BATCH"
MODULE=provider/openai
VERSION='<EXACT_PUBLISHED_VERSION>'
python3 .github/scripts/module-release.py prove --candidate "$BASE" --module "$MODULE" --version "$VERSION"
```

Local `qualify` validates a source candidate. The release controller instead
qualifies the fixed PR1 merge `R` against its staged future versions before any
tag. For `prove`, use the exact tagged commit. Public checks override ambient
workspace/private-proxy settings and use fresh module/build caches.

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

### Resume, abandon, or repair a batch

A failed stage stops the whole batch, including independent modules not yet
published. Completed tags remain immutable. A failed post-tag proof is
published but unverified. The proof job attempts a transient failure at most
three times before the batch stops.

Use **Run workflow** for `module-releases.yml` on `main`. Select `retry` or
`abandon` and provide the exact active `batch_id`; `reconcile` only discovers and
continues valid active state. A retry is a new human workflow dispatch for the
same batch and fixed `R`. Do not use **Re-run jobs** on an older release run, and
do not expect events, closed PRs, or later `main` commits to retry it.

Receipts and public proof are valid for 30 days. Missing, deleted, expired, or
older receipts require fresh exact-`R` qualification and public proof before
publication resumes. Existing tags are revalidated against the fixed batch and
`R`; a tag alone is never success. A different SHA cannot reuse a version or
prior evidence.

Closing either unmerged PR pauses the batch. Closing PR2 neither unpublishes tags
nor releases the active reservation. Explicit abandonment records any published
versions and releases the reservation. The permanent state refs preserve the
reservation, fixed `R`, publication grants, and terminal record; retain them even
after adoption or abandonment. Temporary branch cleanup after successful adoption
requires separate authorization.

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
identity variables, the human batch-writer team, credential metadata, effective
rules, and read-only `diff`/`check`. `apply` is also a dry run unless `--confirm`
is explicitly supplied; confirmed apply requires separate human authorization and
an administrator's credential. The release App must not administer its own
protections.

The checker reuses the existing environment-scoped App key/client ID to sign a
short-lived App JWT for identity and repository-installation **GETs before minting
any installation write token**. Installed rights and repository membership are
verified through the App-authenticated API; no human OAuth/user-token/refresh flow
is required. Key/JWT bytes stay out of arguments, logs, artifacts and durable
files, and candidate jobs never receive them. Local human metadata inspection can
instead use an explicitly supplied short-lived `RELEASE_APP_JWT` with normal `gh`
auth.

Routine `check` verifies effective tag, batch-branch, state-ref, environment, and
merge-commit prerequisites, along with deployed workflow identities. Normal
`GITHUB_TOKEN` read access is tried first; `RELEASE_SETTINGS_TOKEN` is only an
optional human metadata override if the platform withholds required environment or
ruleset details. Missing access is an actionable rollout blocker, not a request to
broaden the App's permissions. The separate rollout-only `audit` checks key scope
via secret-name metadata (or use an equivalent authorized admin audit); it never
requires a reader secret of a particular name to exist. Complete this scope audit
before module enablement, not on every release. The App itself needs
Contents/write and Pull requests/write without Administration or Workflows;
`GITHUB_TOKEN` Actions/write continues controller dispatches.

With `MODULE_RELEASES_ENABLED` unset/false, existing root/SDK flows keep their
main-only environment, verified existing-App identity, constrained namespaces and
exact human-merge provenance without depending on module-wide settings or new
numeric identity variables. The coordinated enabled rollout adds the full global
settings preflight to every shared-App path before write-token minting.

Verify App-only tag creation and separate no-bypass update/deletion restrictions
for root, SDK, and every supported Go prefix. Batch branches require the reviewed
human-writer team for updates and squash-only PR1 merges. State refs are
controller-created and permanent, except that the controller removes the active
reservation after a terminal record. Keep stronger inherited rules. Before
enabling entrypoints, obtain separate authorization for a denial rehearsal in an
isolated repository or non-release namespace with equivalent protections, and map
production prefixes to their effective rules and App installation. Record
**live-enforced in the fixture** separately from **configuration-verified in
production**. A successful configuration check is not live enforcement evidence.

## 5. Finish integration

PR1 includes the necessary in-repository root and integration pin adoption for the
fixed batch. PR2 carries that reviewed result into `main` only after all selected
versions have public proof. After PR2 merges, run the existing full gates for any
subsequent integration work:

```sh
task lint
task --concurrency 3 test
task --concurrency 3 test:race
go run ./cmd/mecademo
```

Run the workspace race suite against the final PR2 integration result, then
verify metadata cleanliness. Keep unavailable publication proofs or failing checks
visible in the PR rather than treating a workspace pass as release readiness.

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
