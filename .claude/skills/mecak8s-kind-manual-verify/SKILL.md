---
name: mecak8s-kind-manual-verify
description: Refresh the mecak8s image in the local mecatl-dev Kind cluster and build mecatui locally, so the user can manually verify in-flight mecatl changes against a real k8s deployment.
---

# mecak8s Kind manual verification loop

Use this when the user wants to manually exercise in-progress mecatl changes
(e.g. an open PR branch) against a real `mecak8s` deployment, rather than only
relying on offline tests. This is the "deploy to kind and build mecatui so I
can click around" loop — fast iteration, not a fresh cluster bring-up.

This assumes a Kind cluster named `mecatl-dev` already exists (see
`deploy/mecak8s-kind/README.md` for first-time setup via
`task mecak8s:kind-setup`). This skill is for the **iterate** step, not initial
provisioning.

## Prerequisites

- Working directory is the mecatl repo (or a worktree of it).
- `ko`, `kubectl`, `kind`, and either `docker` or `podman` installed.
- The `mecatl-dev` Kind cluster already exists (`kind get clusters` shows it).
  If it doesn't, run `task mecak8s:kind-setup` first — that's provisioning,
  not this skill's job.

## Step 1 — Refresh the mecak8s image with current source

From the repo root:

```sh
task mecak8s:image-refresh
```

This rebuilds `ko.local/mecak8s:dev` from the **current source tree** via
`ko build --local --bare --tags=dev ./cmd/mecak8s`, loads it straight into the
running `mecatl-dev` Kind node (no cluster teardown), and does a rolling
restart of `deployment/mecak8s-mecak8s` in the `mecatl` namespace, waiting up
to 240s for the rollout to complete.

Use `KIND_EXPERIMENTAL_PROVIDER=podman task mecak8s:image-refresh` on a Podman
host (must match whatever `kind-setup` was run with).

This is safe to re-run after every code change you want to test — it does
not reset cluster/Redis state.

## Step 2 — Build mecatui locally

```sh
task build
```

Produces `bin/mecatui` (along with `bin/mecated`, `bin/mecademo`) via the
Taskfile — never a bare `go build` at the repo root, which drops stray
binaries.

## Step 3 — Connect and drive it manually

Base fixture (no Keycloak) exposes gRPC at `127.0.0.1:18080` and HTTP at
`127.0.0.1:18081` via static Kind NodePort mappings — check with:

```sh
task mecak8s:kind-status
```

If the fixture was set up with the optional Keycloak/OIDC identity layer
(`task mecak8s:kind-keycloak-setup`), log in and connect with the prebuilt
shorthand tasks:

```sh
task mecak8s:kind-login     # adds /etc/hosts aliases (sudo, once), refreshes fixture CA, runs `mecatui login`
task mecak8s:kind-connect   # adds /etc/hosts aliases, refreshes fixture CA, runs `mecatui connect`
```

Both require `./bin/mecatui` to already exist (Step 2) and
`task mecak8s:kind-keycloak-setup` to have been run at least once. See the
`keycloak-kind-setup` skill if the Keycloak layer itself needs
(re-)provisioning or debugging.

Without the Keycloak layer, connect directly:

```sh
./bin/mecatui connect 127.0.0.1:18080
```

## Iterating

Repeat Step 1 (`task mecak8s:image-refresh`) after each code change, then
reconnect with `mecatui` (Step 3) — no need to rebuild mecatui itself unless
the TUI/client code changed.

## Cleanup

This skill never tears the cluster down. To destroy the whole fixture:

```sh
task mecak8s:kind-destroy
```

## Reference

- `deploy/mecak8s-kind/Taskfile.yml` — `image-refresh`, `kind-status`,
  `kind-login`, `kind-connect`, `kind-destroy` task definitions.
- `deploy/mecak8s-kind/README.md` — full fixture lifecycle, provider modes
  (mock vs. real via `OPENROUTER_API_KEY`), and the optional Keycloak layer.
- `keycloak-kind-setup` skill — for provisioning/recovering the Keycloak layer
  itself, if this fixture uses it.
