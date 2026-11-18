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

Inspect the changed modules' `go.mod` files and the modules that depend on them.
Directory nesting alone does not establish a dependency. The main relationships
within this repository are:

| Module | Same-repository dependencies |
| --- | --- |
| `engine` | None |
| `provider/ssefilter` | None |
| `provider/anthropic` | `engine` |
| `provider/openai`, `provider/openaichat` | `engine`, `provider/ssefilter` |
| `authn/oidc` | `engine` |
| `contracts/gen/go/mecatl/driver` | None |
| `internal/adaptersupport` | `engine` |
| `adapters` | `engine`, `internal/adaptersupport`, generated driver |
| `environment/microvm` | `engine` |
| Root host | Engine, providers, OIDC, persistence modules |
| `integration/microvm` | Root host, engine, microVM runtime; host dependencies transitively |

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

## 4. Publish selected Go modules

Use the bounded publication workflow for every independently published Go module.
Do not create module tags by hand or include a module release in the root application
release. The root application's annotated `vX.Y.Z` release procedure remains separate;
module publication creates lightweight nested tags such as `engine/vX.Y.Z`.

Before dispatching, confirm that `publish-modules.yml` is present on `main`, the
`release` environment has the release GitHub App configured, and repository settings
allow merge commits. Those are rollout prerequisites. This repository has no hosted
publication or public-download evidence yet, so do not treat this procedure as proof
that the required settings are live.

1. Choose a reviewed commit that is reachable from `main` and supply its full SHA as
   `source`. Choose every release explicitly in the `versions` JSON map, including
   its exact version. For example,
   `{"engine":"v0.12.0","provider/openai":"v0.8.0"}` selects only those two
   modules. The workflow never infers versions or expands the selection.
2. Dispatch `publish-modules.yml` from `main`. Its `preflight` job validates the
   source, versions, module inventory, and fixed dependency order, then runs the
   assembled workspace check once. A workspace result proves the coordinated source;
   it does not prove the published dependency closure or public availability.
3. The workflow walks its fixed dependency-first chain. For each selected module,
   `prepare` updates only existing sibling requirements to versions already selected
   and published earlier in the chain, tidies metadata, and runs standalone checks
   with `GOWORK=off`. A missing prerequisite fails the run instead of adding a
   version to the map.
4. `publish` commits the prepared dependency metadata to a temporary release branch
   and creates the module's lightweight tag. Release commits contain metadata only,
   never implementation source. A module with no metadata change tags its incoming
   parent commit.
5. `prove` downloads the exact tagged version through the public proxy and checksum
   database, then compiles an owned minimal import. It must finish before the next
   selected module can run. This is the public publication evidence that standalone
   checks cannot provide.
6. After every selected module passes public proof, `reconcile` updates the selected
   version pins in remaining repository modules. It changes metadata only and does
   not tag those modules. `adoption-pr` commits that metadata and opens one PR to
   `main`.
7. A human merges the adoption PR with a merge commit. Never squash or rebase it.
   This makes every tagged release commit an ancestor of `main`. The workflow opens
   the PR but never merges it.

During publication, tags point at metadata commits on the release branch. The adoption
merge later brings those commits into `main`; `main` never receives intermediate pins
for versions that are not publicly available.

### Workflow and job map

[`publish-modules.yml`](../.github/workflows/publish-modules.yml) coordinates the
release and calls [`publish-module.yml`](../.github/workflows/publish-module.yml) for
each module.

| Work | Workflow and job | Credential boundary |
| --- | --- | --- |
| Validate the request and test assembled source | `publish-modules.yml`: `preflight` | Read-only |
| Process the fixed module order | `publish-modules.yml`: `engine` through `adapters` | Calls the reusable workflow |
| Prepare metadata and run standalone checks | `publish-module.yml`: `prepare` | Read-only |
| Commit prepared metadata and create a lightweight tag | `publish-module.yml`: `publish` | Release App contents-write token |
| Download the published module and compile the smoke consumer | `publish-module.yml`: `prove` | Read-only |
| Update remaining repository metadata | `publish-modules.yml`: `reconcile` | Read-only |
| Commit final metadata and open the adoption PR | `publish-modules.yml`: `adoption-pr` | Release App contents- and pull-requests-write token |
| Merge the adoption PR | Human action in GitHub | Human only |

Read-only jobs have no release environment or writer credential. Writer jobs run only
trusted workflow tooling and use the GitHub API to write prepared metadata; they do not
check out or execute candidate source. Dispatch authentication is separate from the
release App credential and must not enter candidate execution.

The current fixed order is `engine`, generated driver, `provider/ssefilter`,
`internal/adaptersupport`, `authn/oidc`, `provider/anthropic`, `provider/openai`,
`provider/openaichat`, and `adapters`. Every call runs. When its module is absent from
the version map, it succeeds and returns its incoming commit SHA to the next call.
Do not skip that job, because later modules still need the chain.

### Add a publishable module

1. Add the module to `go.work`, give it the correct module identity and dependency
   requirements, and decide its version under its compatibility policy. Runtime and
   test modules remain consumers unless they are deliberately added as publication
   targets.
2. Add the module to the explicit eligibility, import, and dependency-order checks in
   `.github/scripts/module-publication.py`. Add an owned public-download import
   example. Keep the fixed inventory and dependency-first order validated against
   `go.work` and `go.mod`.
3. Add an unconditional reusable-workflow job in `publish-modules.yml` after its
   published dependencies and before its callers. Pass the shared `source`,
   `versions`, and `branch` values, set its `module` directory, and set `parent` to
   the preceding job's `head` output.
4. Thread its `head` output to the following module. If it becomes the last module,
   make both `reconcile` and `adoption-pr` depend on it and use its `head` as their
   parent. The first module continues to take `preflight.source` as its parent.
5. Add focused coverage for the inventory and order, unselected pass-through, exact
   pins, and proof before callers run. Do not introduce a dynamic workflow generator
   for a new module.

### Handle a partial publication

A failed preparation, write, or public proof stops the remaining chain and prevents
adoption PR creation. Tags and metadata commits that succeeded remain valid. Inspect
the branch, tags, and workflow logs, then recover manually with a new explicitly
approved action. Never move or recreate a published tag, and do not assume a rerun can
resume a partial publication.

## 5. Review the adoption PR

Review the final metadata PR and its normal checks after all selected modules complete
public proof. The prospective-merge publication check confirms tagged commits would
remain ancestors of a merge commit; it cannot control which merge method a human
chooses. After adoption, the main-ancestry check rejects squash or rebase results
that lost the tagged commits. Have a human use a merge commit and record the workspace,
standalone, and public-download evidence separately in the PR or release record.

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
| `task test:module-publication` | Public resolution/main-ancestry proof for the persistence release policy |

## Next steps

Use the owning module's compatibility and release policy to select versions, and
record the candidate, standalone, and publication evidence separately in the PR.
