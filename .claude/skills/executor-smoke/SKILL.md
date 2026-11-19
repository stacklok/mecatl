---
name: executor-smoke
description: Smoke-test the mecatl executor workload image locally with `task executor:smoke`, diagnose its failures, and drive manual Files and Commands scenarios against a kept container through `mecaexec` (unknown outcome, cancellation, limits, no shell, path escape). Use when changing the executor image, `mecatl-executor serve`, the executor services, or `executorclient`, or when asked to try the executor by hand. NOT for Kind or Kubernetes deployments (use mecak8s-kind-manual-verify) or for the in-process conformance suites (`task test`).
---

# Executor smoke test

The in-process suites (`fileconformance`, `commandconformance`, `serve_test.go`)
cover the executor services' semantics. This skill covers what only the real
image shows: its entrypoint, user, certificate mounts, and command helper.

Requires Docker and OpenSSL. Run from the repo root or a worktree.

## Step 1: run the smoke test

```sh
task executor:smoke
```

`scripts/executor-smoke.sh` builds `build/execution-workload/Dockerfile`, mints
throwaway certificates in the layout the image mounts (`/etc/mecatl-executor/tls`
and `/etc/mecatl-executor/provider/{ca.crt,client-uri}`), starts
`mecatl-executor serve -files -commands`, and prints `ok` or `FAIL` per check.
It removes the container and certificates on exit, including on failure, and
prints the container logs when a check fails.

## Step 2: diagnose a failure

| Failure | Look at first |
|---|---|
| Executor did not become ready | Container logs: a missing mount, unreadable `tls.key`, or a bad `client-uri` stops `serve` at startup (`executionexecutor.ServerTLSConfig`). A changed `ENTRYPOINT` in the Dockerfile. |
| File round trip | `cmd/mecatl-executor/serve.go` `newFileServer`; `/workspace` ownership in the Dockerfile. |
| Commands run as the image user | `USER` in the Dockerfile. |
| Command output or exit status | The `supervise-command` helper (`/proc/self/exe`) and `executionexecutor` supervisor; the base image's `/bin/sh`. |
| Non-provider certificate refused | `executionexecutor/servertls.go`: the server must require a client certificate whose single URI SAN equals `client-uri`. |

Reproduce by hand with Step 3 before changing code.

## Step 3: manual scenarios

Keep the container, on a fixed port so it survives `docker restart`:

```sh
EXECUTOR_SMOKE_PORT=7443 task executor:smoke -- --keep
```

The script prints the `mecaexec` command line and the cleanup command; define
`mx` as that command line. Every `mecaexec` error ends with a `class:` line,
which is what to compare.

| Scenario | Steps | Expect |
|---|---|---|
| Unknown outcome, no retry | `mx run 'echo x >> runs.log; sleep 60'`, and in parallel `docker restart -t 1 <container>` | `class: outcome-unknown`; after restart `mx read runs.log` has one line |
| Executor down | `docker stop <container>`, then `mx -timeout 10s run true` | "command did not start", not `outcome-unknown` |
| Cancel | `mx -timeout 3s run 'sleep 300 & sleep 300'`, or SIGINT to the `mecaexec` process itself | `deadline` or `canceled`; `mx run 'ps -ef'` shows no `sleep` |
| Version conflict | `echo v2 \| mx replace-stale <file>` | `version-mismatch` on the replay |
| Limits | `mx -max-file-bytes 2 read <file>`; `mx -max-output-bytes 1000 run 'head -c 5000 /dev/zero'` | `limit-exceeded`; output truncated to 1000 bytes plus a marker, exit 0 |
| Path escape | `mx read ../etc/passwd` | refused with `PermissionDenied` |

When signalling `mecaexec`, signal the binary's PID. A shell function run in
the background is a subshell, and a non-interactive shell's background jobs
ignore SIGINT.

No shell: build a variant without `/bin/sh` and run it in place of the image:

```sh
printf 'FROM mecatl-executor:smoke AS src\nFROM scratch\nCOPY --from=src /mecatl-executor /mecatl-executor\nCOPY --from=src --chown=65532:65532 /workspace /workspace\nUSER 65532:65532\nENTRYPOINT ["/mecatl-executor", "serve"]\n' |
  docker build -q -t mecatl-executor:noshell -
```

Replace the kept container with it, where `<dir>` is the printed certificate
directory:

```sh
docker rm -f <container>
docker run -d --name <container> -p 127.0.0.1:7443:7443 \
  -v <dir>/tls:/etc/mecatl-executor/tls:ro \
  -v <dir>/provider:/etc/mecatl-executor/provider:ro \
  mecatl-executor:noshell -listen 0.0.0.0:7443 -files -commands
```

`mx run true` reports `class: no-shell`, and file operations still work.

Known rough edges, tracked in #2885: path escapes and directory reads come
back as untyped gRPC errors, and `mv`/`cp` errors can name the wrong path.

## Cleanup

Run the cleanup command the script printed. It removes the container and the
certificate directory; the `mecatl-executor:smoke` image stays as a build cache.
