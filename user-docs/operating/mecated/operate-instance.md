---
title: Operate a mecated instance
description: Configure, monitor, and recover a standalone Mecatl server.
sidebar_position: 3
---

# Operate a mecated instance

Maintain the running instance, its session ownership, and its shutdown behavior.

## Daemon config file (`daemon.yaml`)

Put listener addresses, TLS files, and rate limits in `daemon.yaml` when you do
not want to repeat flags. This is separate from the policy and provider settings
in `settings.yaml`. Mecatl loads it only when you pass `--config`:

```sh
mecated config daemon init                          # write the conventional skeleton
mecated config daemon init --print                  # print it to stdout, no file
mecated config daemon validate                      # validate the conventional path
mecated config daemon validate --file /etc/mecatl/daemon.yaml
mecated serve --config ~/.config/mecatl/daemon.yaml # start with it
```

The strict v1 schema rejects unknown keys. Explicit flags override file values,
including empty values and zero. Keep the bearer token in `MECATL_AUTH_TOKEN` or
`--auth-token`; `daemon.yaml` does not accept it. Validation prints neither
secrets nor file contents.

## Multi-replica

By default, route every request for a session to the same replica.

For failover or routing without session affinity, configure a lease backend.
Only one replica can hold a session lease; competing requests return gRPC
`FAILED_PRECONDITION` or HTTP 409.

Three lease backends are available:

|Backend|Flag|When to use|
|-|-|-|
|flock (single-host)|`--session-lease-dir <dir>`|Multiple `mecated` processes on one machine. flock auto-releases on crash|
|k8s Lease|`--session-lease-k8s-namespace <ns>`|Multi-replica in Kubernetes; uses `coordination.k8s.io` Leases|
|gRPC driver|`--session-lease-url <host:port>`|Custom or managed lease backend via the driver protocol|

The ServiceAccount for the k8s backend needs `get,create,update,delete` on
`leases.coordination.k8s.io` in the configured namespace. It does not need
`list` or `watch`.

:::warning[Remote shared stores still need an explicit lease]

A local `--store-dir` automatically uses a flock lease beneath the store root,
so multiple current mecated processes on one host participate without another
flag. Remote stores and multi-host filesystems still require an explicit
Kubernetes or gRPC lease backend (and local flock is not reliable over NFS/EFS).
Without one, use session affinity; destructive maintenance fails closed.

:::

The lease TTL defaults to 30 seconds. After a crash, another replica can claim
the session when that TTL expires.

## Operator subcommands

Two subcommands run a service:

```sh
mecated serve [flags]
mecated acp [flags]
```

The remaining commands perform offline operator tasks:

|Task|Command|
|-|-|
|Create operator settings|`mecated config init`|
|Validate operator settings|`mecated config validate [--file PATH]`|
|Create daemon settings|`mecated config daemon init`|
|Validate daemon settings|`mecated config daemon validate [--file PATH]`|
|Promote a legacy draft skill|`mecated skills promote [flags] NAME`|
|Print perf MCP configuration|`mecated perf-mcp print-config`|

Add `--print` to either `config init` command to write the skeleton to standard
output. Both validation commands are read-only and omit settings values. Use
`config validate --learning-patch PATH` to test one `learning:` mapping without
changing the base file.

`skills promote` is a deprecated compatibility path for model-authored files in
`--skills-draft-dir`; it does not activate skill-lifecycle repository records.
Manage schedules through the `Schedule` tool or the gRPC/REST API. See
[Scheduled tasks](/features/sessions/scheduled-tasks.md).

## Graceful shutdown

On `SIGINT` or `SIGTERM`, `mecated` drains listeners for 10 seconds, flushes
telemetry for up to five seconds, and persists configured session state.
Interrupted runs recover from their snapshots after restart.

EOF on an inherited `--lifetime-pipe-fd` or piped `--lifetime-stdin` follows the
same path. See [Host a local daemon](/building/local-daemon.md).

## Monitor the instance

The loopback admin listener exposes `/metrics`, `/debug/pprof`, `/debug/vars`, and `/debug/flightrecorder`. Keep it on loopback: diagnostics can contain prompts, file paths, and goroutine stacks. Configure collectors using [observability](/operating/observability.md). Look up scheduler and LLM timeout tuning in the [server CLI reference](/reference/server-cli.md).

## Connect tools and trusted instructions

Configure streaming-HTTP MCP connections through [MCP client configuration](/features/security-and-execution/mcp-client.md). Authorize global OAuth profiles before serving with `mecated mcp login SERVER [--no-browser]`; the daemon restores and refreshes credentials without opening a browser. Use [skills and project instructions](/features/agent-behavior/skills-commands-and-soul.md) for trusted instruction sources.

## Offline mock providers (no credentials)

`--mock` provides one canned text response for offline smoke tests.
`--mock-script PATH` instead loads ordered text and tool-call turns from a
strict JSON file. Both start without provider credentials and fail before
binding a listener when the script is invalid.

```json
{
  "turns": [
    {
      "tool_calls": [
        {
          "id": "write-1",
          "name": "Write",
          "args": { "path": "proof.txt", "content": "ok\n" }
        }
      ]
    },
    { "text": "continued after the tool" }
  ]
}
```


## Next steps

- [Operate local session storage](/operating/session-storage-operations.md).
- [Deploy a shared-team service](/operating/mecak8s.md).
