# Qualify local microVM source builds

**What this covers:** local microVM source-build preparation and offline guest checks.

**Prerequisites:** [Architecture overview](../architecture.md) and an explicitly owned development environment.

**Follow-on:** [Operator guides](../../user-docs/operating/index.md).

Repository developers can exercise the implemented Darwin path on a non-root Apple Silicon
host running macOS 15 or newer with Hypervisor.framework. Artifact preparation uses the
current-platform development descriptor and pinned go-microvm v0.0.41 runtime and firmware.
The following agent run is offline and uses no provider credential or paid model.

The provider-offline production-composed journey prepares the current-platform
release fixture and runs the scripted mock-provider checks:

```sh
task e2e:microvm
```

This journey requires artifact-network access during preparation. It exercises guest Read,
Write, and Shell operations, fixed UID 65532, host-path and source isolation, isolated-child
merge and conflict handling, graceful microvmd restart, and exact reattachment. It does not
contact an LLM provider.

For an interactive developer check through the public HTTP API, prepare the artifacts and
source binaries:

```sh
task microvm:dev:prepare
task microvm:dev:build
QUAL_ROOT="$(pwd)/.scratch/microvm-darwin-check"
mkdir -p "$QUAL_ROOT/config" "$QUAL_ROOT/runtime" "$QUAL_ROOT/state"
cat >"$QUAL_ROOT/mock.json" <<'JSON'
{"turns":[
  {"tool_calls":[{"id":"shell-uid","name":"Shell","args":{"command":"id -u && pwd"}}]},
  {"tool_calls":[{"id":"write-proof","name":"Write","args":{"path":"darwin-vm-proof.txt","content":"darwin microvm proof\n"}}]},
  {"tool_calls":[{"id":"read-proof","name":"Read","args":{"path":"darwin-vm-proof.txt"}}]},
  {"text":"offline Darwin microVM check complete"}
]}
JSON
```

Start the development server from the repository root:

```sh
QUAL_ROOT="$(pwd)/.scratch/microvm-darwin-check"
export XDG_STATE_HOME="$QUAL_ROOT/state"
export XDG_CONFIG_HOME="$QUAL_ROOT/config"
export XDG_RUNTIME_DIR="$QUAL_ROOT/runtime"
.scratch/microvm-dev/bin/mecated serve --headless --posture auto \
  --store-dir="$QUAL_ROOT/sessions" \
  --default-placement microvm-local \
  --mock-script="$QUAL_ROOT/mock.json" \
  --microvm-dev-release="$(pwd)/.scratch/microvm-dev/darwin-arm64/release.json" \
  --microvm-dev-acknowledge-untrusted-local-artifacts
```

In a second terminal, create and prompt a session through the public HTTP API. Set the same
private XDG roots so local administration inspects the server's state:

```sh
QUAL_ROOT="$(pwd)/.scratch/microvm-darwin-check"
export XDG_STATE_HOME="$QUAL_ROOT/state"
export XDG_CONFIG_HOME="$QUAL_ROOT/config"
export XDG_RUNTIME_DIR="$QUAL_ROOT/runtime"
curl -sS -X POST http://127.0.0.1:8081/v1/sessions \
  -H 'Content-Type: application/json' -d '{}'
SESSION_ID=copy-from-create-response
curl -sS -N -X POST \
  "http://127.0.0.1:8081/v1/sessions/${SESSION_ID}/prompt" \
  -H 'Content-Type: application/json' \
  -d '{"text":"Run the scripted offline Darwin microVM check."}'
test ! -e darwin-vm-proof.txt
.scratch/microvm-dev/bin/mecated microvm doctor
.scratch/microvm-dev/bin/mecated microvm status
```

Confirm that Shell reports UID 65532, Write and Read use the logical worktree, the marker is
absent from the source checkout, and doctor/status are healthy. To check exact normal restart
reattachment, replace the mock script before restarting:

```sh
cat >"$QUAL_ROOT/mock.json" <<'JSON'
{"turns":[
  {"tool_calls":[{"id":"read-after-restart","name":"Read","args":{"path":"darwin-vm-proof.txt"}}]},
  {"text":"offline Darwin microVM restart check complete"}
]}
JSON
```

Stop and restart only the `mecated serve` command with the same XDG roots and store directory,
then prompt the same `SESSION_ID` to read `darwin-vm-proof.txt`.

This procedure is qualification work, not an installation path. Released support still
requires the native journey and a non-publishing signed-candidate journey. If the Darwin
launch supervisor dies while its runner retains the ownership lock, replacement fails closed
and reports that operator recovery is required. Mecatl does not signal a stored PID or fall
back to host execution.


## Related guidance

- [MicroVM architecture](microvm-environments.md) owns platform evidence and lifecycle boundaries.
- [Public microVM operation](../../user-docs/operating/microvm-environments.md) owns supported operator tasks.
