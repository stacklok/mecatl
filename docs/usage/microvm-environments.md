# Local microVM environments

`microvm-local` is an opt-in deployment default placement provider for a local Git repository on Linux
amd64 with KVM. Model-controlled filesystem and shell tools run in the microVM.
Providers, MCP, hooks, credentials, memory, and the mecatl server remain on the host.
It supports one local operator and one canonical Git repository per VM. Linux arm64,
macOS, remote placement, schedules, multi-user sharing, and non-Git sources are not
available.

Install only the host binary for the journey you use. Installing both `mecatui` and
`mecated` is optional.

## Before either journey

Use a published, release-stamped binary. Verify it before installation:

```sh
VERSION=vX.Y.Z
PLATFORM=linux-amd64
BINARY=mecatui # or mecated
mkdir -p "$HOME/.local/bin" .scratch/mecatl-host-release
cd .scratch/mecatl-host-release
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

# Interactive root:
.scratch/microvm-dev/bin/mecatui \
  --default-placement microvm-local \
  --microvm-dev-release="$DESCRIPTOR" \
  --microvm-dev-acknowledge-untrusted-local-artifacts

# Or headless server root:
.scratch/microvm-dev/bin/mecated serve --headless \
  --default-placement microvm-local \
  --microvm-dev-release="$DESCRIPTOR" \
  --microvm-dev-acknowledge-untrusted-local-artifacts
```

The descriptor, bundle, and key paths must be absolute, owner-only regular files and
must identify the matching Linux amd64 developer build and runtime bundle. Both flags
are required. They are unavailable to published binaries and cannot be supplied through
settings, environment variables, HTTP, or gRPC. Runtime artifact verification and
admission still apply.

## mecatui-only journey

From the Git repository:

```sh
mecatui microvm doctor
mecatui --default-placement microvm-local
```

`microvm doctor` is diagnostic only. It can report `backend: not configured` on a fresh
home. Selecting `microvm-local` prepares the verified local runtime and creates the
session. Bare `mecatui` remains host-local.

Inspect placement and resume with the same profile:

```sh
mecatui microvm status
mecatui --default-placement microvm-local --resume SESSION_ID
```

Placement selection is per `mecatui` invocation. A microVM session retains its exact
persisted placement and is never moved to host execution; the same
`--default-placement microvm-local` is needed only when starting a new embedded server.

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

```sh
# Block all guest egress.
mecatui --default-placement microvm-local \
  --microvm-guest-egress=deny-all

# Allow only these guest destinations.
mecated serve --headless \
  --microvm-guest-egress=allowlist \
  --microvm-guest-allow=api.example.com:443/tcp \
  --microvm-guest-allow=dns.example.com:53/udp
```

`--microvm-guest-egress` accepts `permissive`, `deny-all`, or `allowlist`.
`--microvm-guest-allow` is repeatable and requires at least one valid rule with
`allowlist`. Rules use `HOST:PORT/tcp|udp`; IP literals (including IPv6), wildcards,
invalid ports or protocols, whitespace/control characters, and duplicate rules are
invalid. Invalid policy or unavailable enforcement stops readiness. Existing validated
owner-only policy is retained across daemon restarts and session resumes; pass an
explicit mode to change it, including `--microvm-guest-egress=permissive` to reset it.

These flags are host-local only. HTTP/gRPC requests and project configuration cannot select
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

`microvm doctor` and `microvm status` are read-only. Use status before deleting one
specific logical attachment:

```sh
mecated microvm status --output json
mecated microvm delete --session ID --ref REF --generation N --yes
```

`mecatui microvm ...` provides the same local commands. Delete retains dirty worktrees
and does not delete the shared repository VM. After a daemon restart, repository
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
