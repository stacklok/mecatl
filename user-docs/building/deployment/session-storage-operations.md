---
sidebar_position: 110
title: Operate local session storage
description:
  Operate and protect the local JSONL session store through Mecatl's management
  API.
---

# Operate local session storage

Use Mecatl's management API to inspect, optimize, and clean up a local JSONL
session store. Do not edit or delete store files directly.

This guide covers a single-user `mecated` daemon with stable configuration and
state paths. For the API, see the [gRPC reference](/reference/grpc-api.md). For
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

Keep the settings file mode `0600` and its parent directory mode `0700`.
Unknown keys, negative values, and unsupported versions prevent startup.
Explicit retention flags override file settings.

Main-session retention also requires `acknowledge_main_deletion: true` or the
equivalent flag. Review the effective retention summary before enabling it.
Retention protects unknown, corrupt, active, awaiting, live, and leased
sessions.

## Linux: systemd user service

Install `mecated`, then confirm its absolute path. The example uses
`/usr/local/bin/mecated`; Homebrew on Linux usually installs it at
`/home/linuxbrew/.linuxbrew/bin/mecated`. systemd does not search `PATH` or
expand shell expressions in `ExecStart`.

Create the configuration and state directories before enabling the service.
Save the following unit as `~/.config/systemd/user/mecated.service`. `%h` is the
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

## Management truth and safe cleanup

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

Destructive migration, cleanup, and retention also require a cross-process
session-Lease backend for every shared store. If the backend is missing,
disabled, or unimplemented, destructive operations remain unavailable. A Lease
held by another process protects that session family from mutation. Local JSONL
stores automatically use per-session file locks under the store root.

Before changing the store:

1. Open **Sessions > Maintenance**, then inspect **Storage health**.
1. Record the effective policy, current and reclaimable bytes, format counts,
   last and next sweep, active job, and last failure.
1. Select **Optimize storage** or **Clean up sessions** to create a read-only
   plan.
1. Review its scope, protected counts, generation, reclaimable estimate, and
   temporary-space requirement.
1. Apply the plan. Optimization preserves sessions; cleanup is destructive and
   requires a separate confirmation.

Discard a stale plan and create a new one.

For an unsupported backend, use the maintenance and backup procedure supplied
by that backend's operator. Do not infer safety from empty fields or fall back
to filesystem deletion.

## Quiesced backup, migration, and restore runbook

Use this sequence for upgrades, storage optimization, retention changes, and
recovery drills. Use your platform's backup tooling while the service is
stopped.

1. **Stop and quiesce the store.** Stop the service and confirm that no daemon,
   replica, or maintenance job uses the same store. The store and its parent
   directories must be physical directories, not symbolic links. On macOS, use
   the physical `/private/...` path instead of its `/var/...` alias.
1. **Confirm durability.** Check storage health before relying on a backup.
   Mecatl can report filesystems that do not support its full durability
   contract. Successful sync probes show syscall support, but the underlying
   storage must still honor sync and atomic rename. Temporary filesystems do not
   survive host failure. Event-log append, deletion, retention, and migration
   fail before mutation when directory sync is unavailable. A malformed
   complete JSONL record requires operator recovery; only an interrupted final
   record can be treated as a torn tail.
1. **Back up the complete state directory.** Include snapshots, legacy files,
   event and tool sidecars, catalog data, maintenance state, and lock sentinels.
   Preserve ownership, permissions, timestamps, and filesystem boundaries.
   Record the Mecatl version, configuration, effective retention policy, and
   backup checksum. Keep directories mode `0700` and files mode `0600`.
1. **Check space and create a plan.** Allow space for the backup, the plan's
   temporary-space estimate, and a safety margin. Request an optimization or
   cleanup dry run. Stop if storage health, durability, planning, or management
   is unavailable for the backend.
1. **Apply the exact plan.** Start only the intended daemon, confirm the policy
   and dry run again, then apply that generation-bound plan. Follow the durable
   job status. You can resume interrupted migration batches. Cancellation stops
   future items but does not undo completed items. Run migration and destructive
   cleanup as separate operations.
1. **Validate a restore.** Restore the backup to a new directory with owner-only
   permissions. Point a separate test instance at it, then inspect inventory,
   representative transcripts, sidecars, storage health, and a migration dry
   run. Do not overwrite the production directory for validation.
1. **Return to service.** Stop the test instance, preserve the validated backup,
   and start the normal service with its stable configuration and state paths.
   Confirm storage health, policy, inventory, and the next retention sweep
   before admitting new work.

If validation fails, stop the daemon and diagnose the failure. Do not merge
partial state trees or delete the only known-good backup.

## Next steps

- [Review the gRPC API](/reference/grpc-api.md) for storage-management methods.
- [Manage sessions in mecatui](/mecatui/sessions.md#privacy-and-maintenance).
- [Configure Mecatl](./settings.md) with stable operator settings and
  credentials.
