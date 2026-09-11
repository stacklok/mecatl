# Local microVM environments

`microvm-local` is an opt-in deployment default placement provider for a local Git repository on Linux
amd64 with KVM. Model-controlled filesystem and shell tools run in the microVM.
Providers, MCP, hooks, credentials, memory, and the mecatl server remain on the host.
It supports one local operator and one canonical Git repository per VM. Linux arm64,
macOS, remote placement, schedules, multi-user sharing, and non-Git sources are not
available.

The embedded journey requires both host binaries: `mecatui` runs the in-process server,
while `mecated` supplies the local administration commands. No separately running
`mecated` process is required. The headless journey needs only `mecated`.

`task e2e:microvm` is the opt-in automated Linux amd64 KVM gate. It uses the
deterministic mock provider, never reads `OPENROUTER_API_KEY`, and does not prove a live-provider
journey. A separate manual qualification was executed on 2026-09-10 with OpenRouter
`openai/gpt-5-mini` through the public HTTP create and prompt APIs. Write, Read, and Bash ran in
the Wolfi guest as UID 65532, with a proof marker absent from the source checkout. The same
session reattached after restarting only mecated while microvmd remained alive; doctor and status
were healthy. No credential, private placement ref, socket, or host path was retained. See the
acceptance plan for the reproducible qualification steps. This is not a microvmd-restart recovery
claim.

## Before either journey

Use a published, release-stamped binary. Verify it before installation:

```sh
VERSION=vX.Y.Z
PLATFORM=linux-amd64
mkdir -p "$HOME/.local/bin" .scratch/mecatl-host-release
cd .scratch/mecatl-host-release
for BINARY in mecatui mecated; do
  gh release download "$VERSION" --repo stacklok/mecatl \
    --pattern "${BINARY}-${VERSION}-${PLATFORM}" \
    --pattern "${BINARY}-${VERSION}-${PLATFORM}.sha256" \
    --pattern "${BINARY}-${VERSION}-${PLATFORM}.sigstore.json"
  cosign verify-blob \
    --bundle "${BINARY}-${VERSION}-${PLATFORM}.sigstore.json" \
    --certificate-identity "https://github.com/stacklok/mecatl/.github/workflows/release.yml@refs/tags/${VERSION}" \
    --certificate-oidc-issuer https://token.actions.githubusercontent.com \
    "${BINARY}-${VERSION}-${PLATFORM}"
  gh attestation verify "${BINARY}-${VERSION}-${PLATFORM}" --repo stacklok/mecatl
  sha256sum --check "${BINARY}-${VERSION}-${PLATFORM}.sha256"
  install -m 0755 "${BINARY}-${VERSION}-${PLATFORM}" "$HOME/.local/bin/${BINARY}"
done
export PATH="$HOME/.local/bin:$PATH"
cd ../..
```

The Sigstore identity and GitHub attestation verify the host binary; the checksum also
checks for corruption. The verified binary supplies the runtime release and trust
information required by this profile. Host requirements are Git, Python 3, read-write
access to `/dev/kvm`, and enabled unprivileged user namespaces with available per-user
quota. Mecatl does not run sudo or change groups, ACLs, sysctls, or namespace limits.

## Developer source workflow

This workflow is for repository developers testing the local profile. It is separate
from ordinary release-binary use. Build the developer binaries and use the generated
local descriptor only from this checkout:

```sh
task microvm:dev:prepare
task microvm:dev:build
DESCRIPTOR="$(pwd)/.scratch/microvm-dev/linux-amd64/release.json"
DEV_CONFIG="$(pwd)/.scratch/microvm-dev/config"
mkdir -p "$DEV_CONFIG/mecatl"
cat >"$DEV_CONFIG/mecatl/settings.yaml" <<'YAML'
execution:
  default_placement: microvm-local
YAML

# Headless server composition:
.scratch/microvm-dev/bin/mecated serve --headless \
  --default-placement microvm-local \
  --microvm-dev-release="$DESCRIPTOR" \
  --microvm-dev-acknowledge-untrusted-local-artifacts

# Embedded local mecatui composition; the isolated operator settings above are required:
XDG_CONFIG_HOME="$DEV_CONFIG" .scratch/microvm-dev/bin/mecatui \
  --microvm-dev-release="$DESCRIPTOR" \
  --microvm-dev-acknowledge-untrusted-local-artifacts
```

The descriptor, bundle, and key paths must be absolute, owner-only regular files and
must identify the matching Linux amd64 developer build and runtime bundle. Both flags
are required. They are unavailable to published binaries and cannot be supplied through
settings, environment variables, HTTP, or gRPC. Runtime artifact verification and
admission still apply.

## Embedded mecatui journey

Set the operator-owned deployment policy once in `~/.config/mecatl/settings.yaml`:

```yaml
execution:
  default_placement: microvm-local
```

Then use the canonical administration command and launch the embedded server normally:

```sh
mecated microvm doctor
mecatui
```

`microvm doctor` is diagnostic only. With prerequisites satisfied, a fresh home reports
`backend: ready to configure on first use` and exits successfully. Bare `mecatui` embeds
its own server; do not start a separate `mecated serve` process. It resolves the operator
execution policy, then the first-session screen shows bounded download, verification,
installation, and daemon-start progress while the verified runtime is prepared. A
preparation failure names a stable stage and directs the operator to `mecated microvm
doctor` and the exact mecatui diagnostics log. `mecatui connect ADDRESS` is a pure remote
client and never reads or forwards local placement intent.

Inspect placement and resume without reselecting it:

```sh
mecated microvm status
mecatui --resume SESSION_ID
```

A microVM session retains its exact persisted placement and is never moved to host
execution. Public session `profile` remains limited to the deployment default and `no-fs`.

## Headless mecated-only journey

In one terminal:

```sh
mecated microvm doctor
mecated serve --headless --mock --default-placement microvm-local
```

In another terminal, create the placed session, copy `session_id`, then prompt and
inspect it:

```sh
curl -sS -X POST http://127.0.0.1:8081/v1/sessions \
  -H 'Content-Type: application/json' \
  -d '{}'

SESSION_ID=copy-from-create-response
curl -sS -N -X POST "http://127.0.0.1:8081/v1/sessions/${SESSION_ID}/prompt" \
  -H 'Content-Type: application/json' \
  -d '{"text":"Inspect this repository and report its test command."}'
curl -sS "http://127.0.0.1:8081/v1/sessions/${SESSION_ID}"
mecated microvm status
```

The deployment default prepares the local runtime. `--headless` is for unattended API use:
configure main-agent permissions for autonomous work. `--mock` is only for offline
smoke testing; configure a real provider for model work. Public session responses expose
only bounded placement metadata; exact refs and host/guest paths remain private.

## Guest egress policy

Guest IPv4 egress is permissive by default. External IPv6 is unrouted and unsupported.
The local host operator can restrict guest networking:

```yaml
execution:
  default_placement: microvm-local
  microvm:
    guest_egress:
      mode: allowlist
      allow:
        - api.example.com:443/tcp
        - dns.example.com:53/udp
```

The strict operator-tier `mode` accepts `permissive`, `deny-all`, or `allowlist`.
`allow` requires at least one valid rule with `allowlist`. Rules use
`HOST:PORT/tcp|udp`; IP literals (including IPv6), wildcards, invalid ports or
protocols, whitespace/control characters, and duplicate rules are invalid. Invalid
policy or unavailable enforcement stops readiness. Omission means permissive.
Explicit `mecated serve` flags remain higher-precedence one-run overrides.

This policy is host-operator owned. HTTP/gRPC requests and project configuration cannot select
or weaken `microvm-local` placement or guest egress. Guest egress does not cover host
provider, WebFetch, WebSearch, MCP, hook, artifact, or telemetry traffic. The fixed
VM defaults are 2 virtual CPUs and 4 GiB memory.

## Worktrees and operations

One local operator and canonical repository share a mutable VM, root filesystem, guest
home, packages, and declared caches. Each session and isolated child receives its own
Git worktree and logical environment reference. Worktrees separate Git state and
routing; they do not provide kernel isolation between mutually hostile processes in the
same VM. Different canonical repositories receive different VMs. A direct-write child
uses its parent's environment.

`microvm doctor` and `microvm status` are read-only and inspect only state owned by
the current OS principal on the execution host. Status JSON names the backend as
`backend` and daemon attachment rows as `attachment_id`; those attachment IDs are not
public mecatl session IDs. A configured-but-stopped or unhealthy backend still emits a
bounded JSON document with `state`, stable `error`, and `remediation` fields, then exits
nonzero. `mecated microvm delete` removes one exact logical attachment:
copy `backend`, `attachment_id`, `ref`, and `generation` from one status row and confirm:

```sh
mecated microvm delete \
  --backend microvm-local \
  --attachment-id ATTACHMENT_ID \
  --ref REF \
  --generation GENERATION
```

Deletion preserves a dirty worktree and never deletes or resets the repository VM. It is
local-only administration in `mecated`; `mecatui connect` cannot invoke it.

One repository-scoped daemon is shared by sessions and host processes. Ordinary readiness
installs and starts only genuinely fresh state and reuses only an exactly compatible healthy
daemon. A different desired release or guest-egress policy, corrupt configuration, identity
mismatch, stopped daemon, or orphaned runtime fails loudly without stopping the daemon,
rewriting its active config, deleting state, or replacing repository runtime. Resolve the
reported local state conflict explicitly before retrying. After a daemon restart, repository
records, rootfs, and worktrees can remain available while live hosted dependencies do
not. Such sessions report the problem; mecatl does not fall back to host filesystem or
shell execution and does not create an empty replacement environment.

## Paths and trust boundary

| Purpose | Default |
|---|---|
| Daemon config | `~/.config/mecatl/microvmd.json` |
| Verified data and daemon binary | `~/.local/share/mecatl/microvm/` |
| Registry, worktrees, PID, and log | `~/.local/state/mecatl/microvm/` |
| Control socket | `$XDG_RUNTIME_DIR/mecatl-microvm/microvmd.sock`, with a short owner-specific fallback |

The manager owns these private XDG paths. Artifact, transport, or readiness failures do
not fall back to host filesystem or shell execution.

## See also

- [MicroVM architecture](../architecture/microvm-environments.md) explains the execution
  boundary, lifecycle, and routing model.
- [Configuration reference](../configuration-reference.md) lists the operator settings
  schema.
- [Public deployment guide](https://github.com/stacklok/mecatl/blob/main/user-docs/building/deployment/microvm-environments.md)
  provides a concise deployment overview.
