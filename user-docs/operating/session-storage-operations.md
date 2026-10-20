---
sidebar_position: 110
title: Operate local session storage
description:
  Inspect, clean up, and back up the local JSONL session store safely.
---

# Operate local session storage

Use Mecatl's management API to inspect and clean up a local JSONL session store.
Do not edit or delete store files directly.

This guide covers a single-user `mecated` daemon with stable configuration and
state paths. The [API-major-2 upgrade procedure](#upgrade-to-api-major-2) also
covers shared stores and remote drivers. For the API, see the
[gRPC reference](/reference/grpc-api.md). For
the terminal workflow, see
[mecatui session maintenance](/mecatui/sessions.md#privacy-and-maintenance).

:::warning[The store contains plaintext]

The state directory contains prompts, model output, tool arguments/results,
event history, owner metadata, and possibly secrets in **plaintext**. Keep the
directory and every backup owner-only: `0700` for directories and `0600` for
regular files. Do not put the state tree in a shared sync folder, source-control
checkout, or unencrypted multi-user backup. The service account must be the only
account that can read it.

:::

## Put retention in operator settings

Configure automatic cleanup in the operator settings file passed through
`--permission-config`. A zero value disables the corresponding limit. The
following policy retains main sessions and limits child and scheduled sessions
to seven days:

{/* scenario9-retention */}

```yaml
retention:
  version: 1
  main:
    max_age: 0
    max_count: 0
  child:
    max_age: 168h
    max_count: 500
  scheduled:
    max_age: 168h
    max_count: 0
  sweep_cadence: 1h
  acknowledge_main_deletion: false
```

Keep the settings file mode `0600` and its parent directory mode `0700`. Unknown
keys, negative values, and unsupported versions prevent startup. Explicit
retention flags override file settings.

Main-session retention also requires `acknowledge_main_deletion: true` or the
equivalent flag. Review the effective retention summary before enabling it.
Retention protects unknown, corrupt, active, awaiting, live, and leased
sessions.

## Linux: systemd user service

Install `mecated`, then confirm its absolute path. The example uses
`/usr/local/bin/mecated`; Homebrew on Linux usually installs it at
`/home/linuxbrew/.linuxbrew/bin/mecated`. systemd does not search `PATH` or
expand shell expressions in `ExecStart`.

Create the configuration and state directories before enabling the service. Save
the following unit as `~/.config/systemd/user/mecated.service`. `%h` is the
systemd home-directory specifier.

{/* scenario9-systemd */}

```ini
[Unit]
Description=mecatl agent service
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/mecated serve --permission-config %h/.config/mecatl/settings.yaml --store-dir %h/.local/state/mecatl/sessions
Restart=on-failure
RestartSec=5
UMask=0077

[Install]
WantedBy=default.target
```

Replace the executable path if needed. Supply provider credentials through the
user service manager's protected environment or credential facility. Do not put
them in this unit or in `settings.yaml`.

## macOS: launchd user agent

Confirm the executable path with `brew --prefix mecatl`. Homebrew usually uses
`/opt/homebrew/bin/mecated` on Apple silicon and `/usr/local/bin/mecated` on
Intel. launchd requires an absolute path and does not expand `~` or shell
variables.

Replace every `USERNAME` placeholder below, and create the application and
`sessions` directories with owner-only permissions. Save the file as
`~/Library/LaunchAgents/com.stacklok.mecatl.plist`.

{/* scenario9-launchd */}

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.stacklok.mecatl</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/local/bin/mecated</string>
    <string>serve</string>
    <string>--permission-config</string>
    <string>/Users/USERNAME/Library/Application Support/mecatl/settings.yaml</string>
    <string>--store-dir</string>
    <string>/Users/USERNAME/Library/Application Support/mecatl/sessions</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <dict>
    <key>SuccessfulExit</key>
    <false/>
  </dict>
  <key>ProcessType</key>
  <string>Background</string>
  <key>Umask</key>
  <integer>63</integer>
</dict>
</plist>
```

Keep every token as a separate `ProgramArguments` item. A path containing spaces
must remain one item. Supply provider credentials through a protected launchd
environment or secret facility rather than the property list.

<span id="management-truth-and-safe-cleanup"></span>

## Inspect storage and clean up sessions

Use only Mecatl's management API for cleanup. External deletion can bypass
session-family ordering, locks, active-run and Lease checks, generation-bound
plans, and safe sidecar ordering. File names do not reliably identify session
types.

The available management controls depend on how `mecatui` runs:

- **Embedded `mecatui`** manages its own local server and state.
- **`mecatui connect`** can display remote management controls only when the
  server advertises them and the caller has authority. It cannot configure
  remote retention.

A missing capability means the operation is unavailable. It does not mean the
store has zero usage or zero reclaimable bytes.

Storage health reports bounded counts and identifier samples for ownerless
sessions and schedules. Use them to assess an OIDC ownership cutover before
admitting tenant traffic. Enabling caller identity makes existing ownerless
records unavailable to all callers, with no adoption path. See the
[Kubernetes deployment](./mecak8s.md) for the cutover procedure.

For a remote OIDC daemon, authentication alone is not management authority. The
operator must list exact verified issuer/subject pairs in the operator-tier
settings file:

```yaml
storage_management:
  version: 1
  principals:
    - issuer: https://idp.example/realms/operators
      subject: storage-admin
```

An absent or empty principal list disables remote storage management. Project
settings, owner claims, display names, grant types, and system-principal status
cannot grant this authority.

Destructive cleanup and retention require a cross-process session-lease backend
for every shared store. If the backend is missing, disabled, or unimplemented,
destructive operations remain unavailable. A lease held by another process
protects that session family from mutation. Local JSONL stores automatically use
per-session file locks under the store root.

Before deleting sessions:

1. Open **Sessions > Maintenance**, then inspect **Storage health**.
1. Record the effective policy, current bytes, session counts, last and next
   sweep, active cleanup, and last failure.
1. Select **Clean up sessions** to create a read-only plan.
1. Review its scope, protected counts, generation, and reclaimable estimate.
1. Apply the plan after the separate destructive confirmation.

Discard a stale plan and create a new one.

For an unsupported backend, use the maintenance and backup procedure supplied by
that backend's operator. Do not infer safety from empty fields or fall back to
filesystem deletion.

## Quiesced backup and restore runbook

Use this sequence for upgrades, retention changes, and recovery drills. Use your
platform's backup tooling while the service is stopped.

1. **Stop and quiesce the store.** Stop the service and confirm that no daemon,
   replica, or cleanup job uses the same store. The store and its parent
   directories must be physical directories, not symbolic links. On macOS, use
   the physical `/private/...` path instead of its `/var/...` alias.
1. **Confirm durability.** Review the storage-health result before relying on a
   backup. Mecatl can report filesystems that do not support its full durability
   contract. Successful sync probes show syscall support, but the underlying
   storage must still honor sync and atomic rename. Temporary filesystems do not
   survive host failure. Event-log append, deletion, and retention fail before
   mutation when directory sync is unavailable. A malformed complete JSONL
   record requires operator recovery; only an interrupted final record can be
   treated as a torn tail.
1. **Back up the complete state directory.** Include current snapshots under
   `sid-v1`, event and tool sidecars, catalog data, and lock sentinels. Preserve
   ownership, permissions, timestamps, and filesystem boundaries. Record the
   Mecatl version, configuration, effective retention policy, and backup
   checksum. Keep directories mode `0700` and files mode `0600`.
1. **Validate a restore.** Restore the backup to a new directory with owner-only
   permissions. Point a separate test instance at it, then inspect inventory,
   representative transcripts, sidecars, and storage health. Do not overwrite
   the production directory for validation.
1. **Return to service.** Stop the test instance, preserve the validated backup,
   and start the normal service with its stable configuration and state paths.
   Confirm storage health, policy, inventory, and the next retention sweep
   before admitting new work.

If inventory reports a malformed current snapshot, quiesce every service using
the namespace and preserve a byte-for-byte copy for diagnosis. Restore the
complete namespace from a verified backup in a separate location, validate it,
and then switch the service to that restored namespace. Do not directly edit the
artifact or delete the damaged namespace as the default recovery action.

If validation fails, keep the service stopped. Preserve the failed namespace and
the last known-good backup for diagnosis; do not merge partial state trees.

## Upgrade to API major 2

Coordinate the server, `mecatui`, TypeScript SDK applications, and Studio with
both snapshot and event storage drivers. API major 2 identifies tool invocations
with globally unique engine IDs and keeps provider replay private. Software
package versions are independent: the SDK's `0.5.0` version is not a wire-major
number. Check `GetCompatibilityInfo` or `GET /v1/compatibility` for the wire major;
the existing `mecatl.v1` package and `/v1` routes remain in use.

1. **Drain active writers.** Stop admitting work, let active runs settle, and stop
   all harness processes, replicas, and maintenance writers using the namespace.
   For local JSONL storage, take and validate a complete backup using the
   [quiesced runbook](#quiesced-backup-and-restore-runbook). For remote backends,
   use the operator's backup procedure for the complete snapshot/event namespace.
1. **Upgrade both decoding drivers.** Upgrade the snapshot driver and the event
   driver, even if configured independently. Current writers use
   `sessnap-json/2` and `eventlog-json/2`; readers accept `/1` and `/2` with
   matching inner identity schemas. A decoding wrapper can return `/2` even
   when reading old backend data, so keep old harness readers stopped. Raw
   opaque drivers must preserve the exact format tag and payload bytes.
1. **Upgrade the harness and clients together.** Deploy API-major-2 server and
   client builds before reopening access. There is no public major-1
   identity-alias mode. Rebuild client-maintained views keyed by old invocation
   IDs from current transcripts or event replay. Retain sessions and logs;
   watch cursors are log positions and do not need resetting for this upgrade.
1. **Verify before admitting work.** Check compatibility discovery, representative
   old transcripts, valid pending approvals/authorization, event reads, and
   learning citations against the upgraded deployment. Resume writers only with
   the coordinated versions.

Keep old binaries away from the migrated namespace. Mixed-version writers and
rollback after new-format writes are unsupported. Old decoding wrappers reject
`/2` per operation before backend mutation; format rejection is not a startup
negotiation guarantee. The unchanged `session-store/1` capability marker does
not negotiate formats, and event drivers have no format handshake. Local JSONL
snapshots retain their private outer envelope, and Redis events retain
`redisstore-eventlog/1`. Old local readers can ignore the inner schema and
misinterpret identity, so these tags do not make old readers safe.

### Retained sessions and historical records

Valid old sessions and provable pending continuations remain readable and
resumable. When a continuation cannot be uniquely associated with its invocation,
Mecatl abandons it during decoding and clears its runnable controls. The session
accepts a separate new prompt, and its history includes an upgrade notice that
prior tool effects and completion may be unknown. Check any external effects
before asking the agent to repeat interrupted work. Abandonment does not roll
back tool effects or revoke an external authorization.

History with a proven call/result association renders normally. Unsupported
associations render as standalone historical text/media with an unavailable
attachment indication. Mecatl does not guess tool-card or parent-delegation
links from provider IDs. Copied history retains an invocation's identity, and
clients must still handle repeated delivery idempotently.

Public `ConversationMessage` omits the opaque `reasoning` and
`reasoning_item_id` fields (reserved tags 5 and 8). Readable reasoning events,
text/media, and `provider_phase` remain available; exact provider replay is
retained privately in snapshots and internal archives.

New learning evidence uses `reflection-evidence/v2`. Legacy-v0/v1 evidence keeps
its original digests, coordinates, and stored attempts and remains verifiable
while its source exists. An available citation can lack a canonical tool-call
attachment. Pruned source remains unavailable; this upgrade does not promise
indefinite archival or recovery of missing evidence.

## Next steps

- [Review the gRPC API](/reference/grpc-api.md) for session cleanup and
  storage-health methods.
- [Manage sessions in mecatui](/mecatui/sessions.md#privacy-and-maintenance).
- [Configure Mecatl](./settings.md) with stable operator settings and
  credentials.
