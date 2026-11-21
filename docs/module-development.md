# Develop changes across Go modules

Use this guide when a change spans Go modules or changes dependency metadata.
Run commands from the repository root with the toolchain declared in `go.mod`.
Find module directories in [`go.work`](../go.work); root `go test ./...` covers
only the root module. Use `task --list` for additional commands.

## Choose the dependency mode

- **Workspace integration** tests sibling source together using a combined
  third-party dependency graph. Ordinary Task commands default to this checkout's
  `go.work`, but an inherited `GOWORK` wins. Explicit `test:workspace` tasks select
  this checkout even under an override.
- **Workspace synchronization** records the workspace's selected third-party
  dependency versions in member metadata with `task deps:sync`. PR validation
  requires those changes to be committed.
- **Standalone qualification** uses `GOWORK=off` to check declared dependencies.
  Module-local `replace` directives still apply, so inspect them before claiming
  a published-dependency result. Missing published sibling APIs can block this
  qualification while workspace integration passes.

Ordinary development needs checkout integration and synchronized metadata.
Independent module releases qualify declared dependencies and public downloads
through [module publication](module-publication.md). Unreleased revisions can
use sibling APIs that are not available at their declared published versions.

## Develop and validate a cross-module change

1. Inspect the affected modules' `go.mod` files and their dependents. Assess public
   Go changes under the [engine](../engine/COMPATIBILITY.md) or
   [adapter](../adapters/COMPATIBILITY.md) compatibility policy. Assess
   [wire compatibility](architecture/api-surface.md#wire-api-compatibility) and
   stored-data compatibility separately when those contracts change.
2. Develop coordinated source changes together, keeping sibling requirements at
   real published versions. For focused module-level integration, run:

   ```sh
   task test:workspace:module MODULE=provider/openai
   ```

   Use `MODULE=.` for root. For narrower changes, select package tests; for
   concurrency changes, use the race variant.
3. Widen verification to affected callers when shared contracts change. Use a
   complete local suite only when warranted: choose `task test` or `task test:race`.
   CI supplies the broader platform and deployment coverage. Report the commands,
   results, and any qualification blocked on publication in the PR.

For Kind integration, `task e2e:k8s` shares the ordinary workspace default and
caller override. This matches official application releases, which compile sibling
source from the immutable release checkout. Use `task e2e:k8s:pinned` only when you
need declared-pin qualification; it reuses the same suite.

Development verification is complete when the affected behavior and direct
integration checks pass, intended metadata changes are reviewed, and any blocked
qualification is identified. Publishing sibling releases is a separate task.

## Synchronize dependency metadata

After changing dependency requirements, synchronize the workspace and review the
resulting metadata before committing:

```sh
task deps:sync
git diff -- go.work '**/go.mod' '**/go.sum'
```

This runs native `go work sync`, explicitly using this checkout's workfile even
under an inherited `GOWORK` override. It raises relevant member requirements to
the versions selected by the combined workspace graph. `task tidy` is an alias
for this operation.

After committing intended metadata, verify synchronization:

```sh
task test:workspace-sync
```

CI Build and local `ci`/`check:pr` use this check. It runs sync and fails on staged,
unstaged, or untracked metadata changes, leaving any resulting edits for review.
It can modify files even when it fails. `task test:module-metadata` checks only
cleanliness; CI also runs it after tests to catch incidental metadata changes.

Sync does not remove every unused requirement or add missing sibling requirements.
When adding a sibling dependency, declare a real published baseline explicitly.
For targeted standalone maintenance, use `task tidy:module MODULE=provider/openai`
and `task test:module MODULE=provider/openai`. Standalone tidy resolves that
module's own graph and can conflict with workspace alignment or fail on an
unpublished sibling package. Such reconciliation can wait until module publication.

Commit intended `go.work`, `go.mod`, and `go.sum` changes through normal review.
Root `go.work.sum` is ignored supplemental state; investigate checksum mismatches
rather than deleting sum files. Workspace synchronization does not prove standalone
compatibility or lock external users to an exact dependency graph.

## Add a module

Complete registration in the PR that introduces the module:

1. Create `go.mod` with path `github.com/stacklok/mecatl/<DIRECTORY>` and the
   repository's Go version. Add the directory to `go.work` and `Taskfile.yml`'s
   `GO_MODULES`; include its generated `go.sum` when present.
2. Add build, lint, and vulnerability coverage to the owning Task and CI jobs;
   these enumerate modules separately. Update import allowlists for new
   dependencies across enforced package boundaries.
3. Register publication handling in
   [`module-publication.py`](../.github/scripts/module-publication.py).
   Independently versioned modules need dependency-ordered `ORDER` and `IMPORTS`
   entries; root, runtime, and test-only modules need `CONSUMERS` and reconciliation
   handling. For a publishable module, follow the
   [workflow registration checklist](../.github/workflows/publish-modules.yml).
4. If the published adapters dependency closure changes, update
   [`check-module-publication.sh`](../.github/scripts/check-module-publication.sh)
   and metadata paths in [`relevant-changes.sh`](../.github/scripts/relevant-changes.sh).
5. Run `task test:module-publication-tools` to validate inventory and dependency
   order, then select the affected integration and metadata checks above.

## Publish modules

For an independently versioned release, follow
[Publish selected Go modules](module-publication.md). Ordinary source changes can
complete review and merge before that procedure is needed.
