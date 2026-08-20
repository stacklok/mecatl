---
sidebar_position: 4
title: Operate local session storage
---

# Operate local session storage

This guide is for a single-user `mecated` daemon with the embedded JSONL store. It keeps the
executable, operator policy, and plaintext session state at stable paths, and uses mecatl's
management API rather than editing store files. For the API surface, see
[Drive via gRPC / HTTP](grpc-http.md#storage-management-endpoints); for the interactive workflow,
see [mecatui session maintenance](/mecatui/sessions.md#privacy-and-maintenance).

:::warning[The store contains plaintext]

The state directory contains prompts, model output, tool arguments/results, event history, owner
metadata, and possibly secrets in **plaintext**. Keep the directory and every backup owner-only:
`0700` for directories and `0600` for regular files. Do not put the state tree in a shared sync
folder, source-control checkout, or unencrypted multi-user backup. The service account must be the
only account that can read it.

:::

## Put retention in operator settings

Automatic cleanup is daemon-owned retention. Save this policy at the service's exact
`--permission-config` path. Main-session deletion remains disabled; change it only after inspecting
the effective policy and acknowledging its impact. Every zero disables that limit.

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

The operator settings file is policy, not the daemon topology file. Keep it mode `0600`; create its
parent directory mode `0700`. Unknown keys, negative values, and unsupported versions fail startup.
Explicit retention flags override this file. If main retention is enabled, startup also requires
`acknowledge_main_deletion: true` (or the equivalent explicit flag) after logging the effective
planner summary. Unknown, corrupt, active, awaiting, live, and leased sessions remain protected.

## Linux: systemd user service

Install the released `mecated` executable at `/usr/local/bin/mecated`. Create the config and state
directories before enabling the service; `%h` is systemd's stable home-directory specifier. Save the
following as `~/.config/systemd/user/mecated.service`.

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

`ExecStart` is deliberately one directive with no shell, substitution, or wrapper. systemd passes
each whitespace-delimited argument exactly as shown. Provider credentials should come from the
user service manager's protected environment or credential facility, not from this world-readable
example and not from `settings.yaml`.

## macOS: launchd user agent

Install the released executable at `/usr/local/bin/mecated`. Replace `USERNAME` with the login
account in every path, create both `Library/Application Support/mecatl` and its `sessions`
subdirectory owner-only, and save this as
`~/Library/LaunchAgents/com.stacklok.mecatl.plist`. launchd does not expand `~` or shell variables.

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

Every token is one `ProgramArguments` item. In particular, each path containing spaces is one item;
do not collapse the array into a shell command string. Keep provider credentials in a protected
launchd environment or secret facility rather than embedding them in the plist.

## Management truth and safe cleanup

**Do not use cron, `find`, filesystem globs, shell loops, systemd timers, launchd calendar jobs, or
file-age rules to delete anything below the store directory.** External deletion bypasses
session-family ordering, cross-process locks, live-run and lease checks, durable taxonomy,
generation-bound plans, and sidecar-first/snapshot-last deletion. Filename patterns are not a
session-kind authority. There is intentionally no external deletion recipe here.

The management surface depends on where mecatui runs:

- **Embedded mecatui** owns its local server and local state. Its local retention flags/settings and
  Maintenance tab describe that embedded backend only.
- **`mecatui connect`** is a client of the remote daemon. It cannot configure remote retention.
  Storage health, optimization, and cleanup appear only when the server advertises the corresponding
  management capability and the authenticated caller has authority. Missing capability means
  unavailable, not zero usage or zero reclaimable bytes.

For a remote OIDC daemon, authentication alone is not management authority. The operator must list
exact verified issuer/subject pairs in the operator-tier settings file:

```yaml
storage_management:
  version: 1
  principals:
    - issuer: https://idp.example/realms/operators
      subject: storage-admin
```

An absent or empty list fails closed and does not advertise remote storage management. Project
settings, request owner claims, display names, grant types, and system-principal status cannot grant
this authority. Destructive migration, cleanup, and automatic retention additionally require a
working cross-process session-lease backend for every shareable store; a missing, disabled, or
`UNIMPLEMENTED` lease makes destructive management unavailable and causes automatic deletion to
fail closed. A lease held by another process skips the protected family without mutation.
Management authority never doubles as a single-writer proof. Local JSONL stores remain convenient
because every `--store-dir` composition automatically wires the existing flock session lease beneath
the store root; multiple local processes sharing that root contend on the same per-session locks.
Only the process-private in-memory store can safely omit that lease.

Before any change, open **Sessions → Maintenance** and inspect **Storage health**. Record the
effective policy, current/reclaimable bytes, format and durable-kind counts, last/next sweep, active
job, and last failure. Then run **Optimize storage** or **Clean up sessions** to obtain the read-only
dry run. Review its exact scope, protected counts, generation, reclaimable estimate, and temporary
space requirement **before apply**. Optimization preserves sessions; cleanup is destructive and
requires its separate high-friction confirmation. A stale plan must be discarded and planned again.

An unsupported backend must be treated as unsupported: do not infer safety from empty fields, do not
fall back to filesystem deletion, and do not copy a local-backend procedure onto a connected remote
store. Ask that backend's operator for its advertised maintenance and backup contract.

## Quiesced backup, migration, and restore runbook

Use this sequence for upgrades, v1-to-v2 optimization, retention-policy changes, and recovery drills.
It contains no destructive shell command examples; use your platform's backup tooling with the
service stopped.

1. **Stop and quiesce.** Stop the user service and confirm the daemon has exited. Confirm no other
   process or replica points at the same local store and no maintenance job is active. A filesystem
   copy while the daemon is writing is not a supported backup.
2. **Back up.** Snapshot or copy the complete state directory—not selected globs—including current
   snapshots, legacy files, tool/event sidecars, catalog data, maintenance job state, and lock
   sentinels. Preserve ownership, mode, timestamps, and filesystem boundaries. Record the executable
   version, config file, effective policy, and backup checksum alongside it. Keep the backup
   plaintext-sensitive with `0700` directories and `0600` files.
3. **Forecast and plan.** Check filesystem **free space** for the backup plus the migration plan's
   **temporary-space estimate** and safety margin. Open the restored or production instance's
   storage health, then request an optimization or cleanup dry run. If health, planning, durability,
   or management is unavailable, stop: this is an **unsupported backend** for this runbook.
4. **Migrate or apply.** Start only the intended single daemon, recheck the effective policy and dry
   run, and apply that exact generation-bound plan. Follow durable job progress; resume bounded
   migration batches after interruption. Cancellation stops future items and does not roll back
   completed items. Never combine physical migration and destructive cleanup as one assumed action.
5. **Verify.** Require a completed job or account for every sanitized per-item failure. Recheck
   storage health, inventory counts, format counts, policy, and free space. For restore validation,
   **restore to a new directory**, preserve owner-only permissions, point a separate single test
   instance at that directory, and perform read-only validation: inventory pages, representative
   transcripts, sidecars, storage health, and a migration dry run. Never validate by overwriting the
   production directory.
6. **Start.** Stop the validation instance, leave the validated backup unchanged, and start the
   normal service with its original stable config and state paths. Confirm health, effective policy,
   inventory, and the next sweep before admitting new work.

If verification fails, stop the daemon and diagnose before choosing the validated new-directory
restore as the replacement. Do not merge partial trees or delete the only known-good backup.
