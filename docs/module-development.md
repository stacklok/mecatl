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

If imports or requirements changed, tidy the affected module and review the diff.
For example:

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

For each dependency module:

1. Verify the module against available declared dependencies. From its directory,
   run `GOWORK=off go mod tidy -diff`, `GOWORK=off go build ./...`, and
   `GOWORK=off go test -mod=readonly ./...`.
2. Have a human merge the reviewed PR. Identify the actual merged commit reachable
   from `main`; a squash merge does not preserve the PR-head SHA.
3. Publish that module's independent tag through its owning release process, with
   authorization. For example, the engine uses `engine/vX.Y.Z`, while adapters use
   `adapters/vX.Y.Z`. A root release tag does not release nested modules.
4. Update dependent modules to the real published version, tidy their manifests,
   and repeat verification. Publish changed dependencies before their callers.

After the engine release, set `ENGINE_VERSION` to its published version and update
an affected provider, for example:

```sh
(cd provider/openai &&
  GOWORK=off go get "github.com/stacklok/mecatl/engine@$ENGINE_VERSION" &&
  GOWORK=off go mod tidy &&
  GOWORK=off go mod tidy -diff &&
  GOWORK=off go test -mod=readonly ./...)
```

Review the manifest/sum diff before proceeding. Repeat for each affected module;
`go get` can also update transitive requirements.

`GOWORK=off` disables `go.work`, but leaves local `replace` directives effective.
Root, the microVM runtime, and microVM integration use local replacements. Their
workspace-disabled tests are not proofs of public dependency resolution. A real
external-consumer proof must resolve released modules without checkout replacements.

Follow [adapter release verification](../adapters/COMPATIBILITY.md#release-verification)
for the persistence modules:

```sh
task test:module-publication
# Set ADAPTERS_VERSION to the authorized published version:
task test:module-publication VERSION="$ADAPTERS_VERSION"
```

This proof requires fetched `origin/main` history/tags and public proxy access.
It checks the specified persistence-module pins and builds a published adapters
version in a fresh external module/cache. It does not prove publication of every
module in the repository.

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
| `task test:module-metadata` | Non-mutating cleanliness check of committed Go metadata, excluding root `go.work.sum` |
| `task test:module-publication` | Public resolution/main-ancestry proof for the persistence release policy |

## Next steps

Use the owning module's compatibility and release policy to select versions, and
record the candidate, standalone, and publication evidence separately in the PR.
