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
- **Candidate metadata validation** tests each module with current siblings and
  temporary manifests, excluding unrelated workspace members from resolution.
  This catches metadata gaps that workspace integration can hide. The checker
  ignores ambient `GOWORK` and `GOFLAGS` and disables toolchain downloads.
- **Standalone qualification** uses `GOWORK=off` to check declared dependencies.
  Module-local `replace` directives still apply, so inspect them before claiming
  a published-dependency result. Missing published sibling APIs can block this
  qualification while workspace integration passes.

Ordinary development needs checkout integration and valid candidate metadata.
Use standalone qualification when assessing released dependency pins; public
availability is verified during [module publication](module-publication.md).

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
   concurrency changes, use the race variant. Candidate validation below also
   runs the selected module's tests, so count that coverage when choosing checks.
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

## Repair dependency metadata when needed

Check the affected module against current sibling source:

```sh
task test:module-candidate MODULE=provider/openai
```

Omit `MODULE` to check every workspace member independently. The checker runs
module tests and reports stale requirements or missing checksums without editing
the checkout manifests. Tests retain their normal filesystem access.

If the checker reports repairable drift, apply the repair, review it, and recheck:

```sh
task tidy:module-candidate MODULE=provider/openai
git diff -- provider/openai/go.mod provider/openai/go.sum
task test:module-candidate MODULE=provider/openai
```

Repair preserves declared sibling versions and original replacements. A missing
sibling requirement needs an explicitly chosen published baseline. All selected
modules must pass tests before repair starts; files are replaced individually,
so an interrupted repair can be partial. Review the diff before retrying.

When reconciling published dependencies, use `task tidy:module MODULE=provider/openai`
followed by `task test:module MODULE=provider/openai` for standalone tests. This can
wait until a required sibling API is published. Reserve broad `task tidy` for
intentional workspace synchronization: its `go work sync` can upgrade member
requirements, so review every resulting manifest.

Commit intended `go.work`, `go.mod`, and `go.sum` changes through the normal review
workflow. Root `go.work.sum` is ignored supplemental state; investigate checksum
mismatches rather than deleting sum files. After committing intended metadata,
run `task test:module-metadata` to check for staged, unstaged, or untracked drift.
This is a cleanliness check, not a dependency-resolution proof.

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
