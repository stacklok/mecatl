# Executor protocol

*This chapter describes the intended design, not current implementation status.*

The executor serves one session's files and commands from inside its Pod. This chapter defines how the trusted service
reaches it: an RPC server in the executor Pod with a file service and a command service. It replaces the current
transport, in which the provider starts a new `mecatl-executor` process through Kubernetes Pod exec for every operation
(see [how a tool call flows today](implementation.md#how-a-tool-call-flows-today)).

## Why Pod exec goes

Pod exec ties every operation to a connection from the provider through the Kubernetes API server, and the executor
keeps no state between operations. Each file read pays for a process start and two Kubernetes API writes, `osfs`'s
per-path locking lasts for a single operation, command output cannot stream, and the provider has to record every
in-flight operation in the `ExecutionEnvironment` status so that a provider crash can be detected. A long-running server
in the executor Pod knows what it is running, so none of that is needed.

## The server

The executor runs one gRPC server on one port. It registers two services:

- **Files** serves the [`Workspace` operations][workspace-contract] on `/workspace`: versioned read, stat, create-only
  write, conditional replacement, list, remove, rename, copy, glob, and grep. These match the operations the provider
  already forwards through `FileRequest` in `execution/v1`.
- **Commands** runs a command through `/bin/sh -c` in the workspace and streams its output and exit status. Cancelling
  the call kills the command's process group.

Each service is enabled by its own flag, and the server registers only the services that are enabled. The `Shell`
switch is therefore not registering the command service; a file-only executor has no code path that starts a process.

## One interface, several implementations

The trusted service and the executor implement the same two services. The trusted service acts as an authorizing
proxy: it receives a call from the agent loop, checks it, and forwards the same call to the session's executor. The
executor performs it on `/workspace`. A local executor implements the same services again, so the agent loop uses one
client whether it talks to the trusted service or directly to an executor.

This follows the model of a virtual file system interface, such as FreeBSD's VFS, where every file system implements
one interface and implementation-specific data travels as opaque values. Each request carries the operation's
arguments and a request context, today's `RequestContext` copied into the new package. Each implementation interprets
only the context it understands:

- the trusted service reads its request context from it (owner, run, claim, epoch, and grant generation), authorizes
  the call, and forwards it;
- the executor ignores it.

Ignoring the context does not mean skipping authentication. The executor authenticates every connection and accepts
only the trusted service (see [transport and authentication](#transport-and-authentication)); it does not need to know
on whose behalf a call is made, because it serves one session and the trusted service has already authorized the call.

The executor already receives none of this context today: the provider checks it and forwards only the operation and
its arguments. If the executor later needs to check something itself, such as an epoch to reject stale calls, it
reads that field from the context it already receives.

Lifecycle operations, such as binding, run claims, retirement, replacement, and revocation, stay in a separate service
that only the trusted service implements. `ExecutionProviderService` keeps the lifecycle operations; its `Files`,
`StartCommand`, `CommandStatus`, and `CancelCommand` RPCs are removed, and the trusted service serves the shared file and
command services instead. This changes the RPCs mecak8s calls, so mecak8s and the trusted service must be upgraded
together. That break is accepted for this work.

## Capability discovery

The trusted service does not need to discover which services an executor offers. It already knows the session's
admitted capabilities from admission, and it calls the command service only when `Shell` was admitted. A call to a
service the server did not register fails with the standard gRPC `Unimplemented` status, which the trusted service
treats as a misconfigured executor.

Discovery uses the standard gRPC health service: `grpc.health.v1.Health/Check` returns `SERVING` for a registered
service and `NOT_FOUND` otherwise. mecak8s checks the trusted service this way at startup. Server reflection is for
debugging tools and is not used for this.

## Commands

Commands stay in the foreground, as today: one call runs one command and ends when the command ends. Unlike Pod exec,
the call streams standard output and standard error while the command runs, and closing the call cancels the command.
Background commands are outside this work.

Each command runs over one bidirectional stream. The caller opens it by sending the command, which the server runs with
`/bin/sh -c` in the workspace root, under the requested timeout capped at the server's own limit. The server first
confirms that the command started, then streams its output as it arrives, and finally sends a single result: the command
succeeded, failed with an exit status, was cancelled, timed out, or ended with an unknown outcome because the server
could not confirm that every process it started is gone.

The stream runs both ways so that stopping a command does not cost the caller its result. A caller that wants to stop a
command sends a cancel on the open stream and still receives the result. Closing the stream also kills the command, but
the result is lost with it. The trusted service needs that result, because an unknown outcome is what makes it fence the
environment.

The start confirmation tells the caller what a broken stream means. If the stream fails before the server confirms the
start, the command never ran. If it fails after the start but before a result, the outcome is unknown, and the caller
does not retry the command. A result whose outcome is known replaces Pod exec's `terminal_receipt` as proof that the
command ended and left no process running.

`StartCommand` is removed; the trusted service relays the command stream between mecak8s and the executor.

## File content

File content travels in chunks, so the protocol sets no file size limit and every client, proxy, and server keeps
gRPC's default 4 MiB message limit. `ReadFile` streams its response, and `CreateFile` and `ReplaceFile` stream their
requests; every other file call is unary. Senders split content into chunks of at most 3 MiB, with no negotiation,
which leaves room under the limit for the other fields of the first message. Empty content travels as one message with
no data.

A write sends the request context, the path, and for a replacement the expected version in its first message; later
messages carry only data, and the server ignores other fields in them. The server writes each chunk to a temporary file
in the workspace as it arrives. When the caller closes its side of the stream, the server checks the expected version
under the serialization described in [concurrency and visibility](#concurrency-and-visibility) and renames the
temporary file into place, refusing an existing file for a create. A stream that fails or is cancelled before the
caller closes it writes nothing, and the server removes the temporary file. If the stream fails after the caller closes
it, the caller cannot tell whether the write happened, as with a unary call, and re-reads the file.

A read copies the file to the stream from an open file, and its last message carries the version of exactly the bytes
sent, so the version describes what the caller received even if a shell program changes the file during the read. The
caller uses the content only if the stream ends with a version and without an error.

The executor does not bound how much a write spools to disk. A caller could fill the volume as easily with many small
files, so disk usage is bounded by the volume's size alone.

The `Workspace` port keeps its whole-file methods, which the agent's tools use because they need the whole content;
for those calls the client assembles a read before returning it and splits a write before sending it. Transfers that
must not hold a whole file in memory, such as writing a download to a client's disk one chunk at a time, go through an
optional `WorkspaceStreamer` extension, discovered by type assertion like `BoundedWorkspaceReader`: `ReadVersionTo`
copies a file to an `io.Writer` and returns its version, and `CreateFileFrom` and `ReplaceFileFrom` read an
`io.Reader` and commit only when it ends. The executor server spools through it, and the client implements it by
forwarding chunks. A workspace without the extension declines such a transfer: the caller reports it as unsupported
and never falls back to a whole-file transfer. The authorizing proxy decides from the first message and relays the
chunks that follow without assembling the file.

## Concurrency and visibility

The server accepts concurrent calls. File operations and shell programs see the same files because they share one
filesystem, but the server adds no consistency layer of its own. The server serializes create-only writes and
conditional replacements on the same path, so that the version check and the write happen together. That serialization
covers other file-service calls only. A shell program can still write a file between a read and a conditional
replacement, in which case the replacement fails with a version mismatch and the agent re-reads.

The provider's current limit of one operation per environment, and the operation lease that enforces it, exist only for
Pod exec. They are removed once the trusted service uses this server.

## Transport and authentication

The connection is always initiated by the trusted service. Today the `mecatl-execution` chart installs a default-deny
network policy for every executor Pod, with no allowed inbound or outbound traffic; per-template policies then add the
outbound traffic a template allows for shell commands. Denying all inbound traffic works today because nothing reaches
the executor over the Pod network: the provider uses Pod exec, which goes through the Kubernetes API server and the
kubelet rather than the Pod's network. An RPC server is reached over the Pod network, so the policy gains a rule that
admits the server port from the trusted service only. Network policies take effect only where the cluster's network
plugin enforces them.

The network policy is not enough on its own. Shell programs run in the executor Pod and share its network namespace, so
they can reach the server on `localhost`. The server must therefore authenticate its caller and accept only the trusted
service, using mutual TLS.

Each executor gets its own server certificate, and no certificate authority is involved. Before creating the executor
Pod, the trusted service generates a key pair and a self-signed certificate naming the environment, stores them in a
Secret mounted into the Pod, and records the certificate's fingerprint on the environment. Once the Pod exists, it
also records the Pod UID. The trusted service connects only to the Pod with that UID and accepts only that
fingerprint, so a reused Pod IP or an earlier incarnation of the executor is never trusted. A container restart keeps
the Pod and its certificate; a replacement Pod gets a new certificate, which also serves as rotation.

The trusted service presents a client certificate issued by the platform PKI that already issues its server
certificate. Each executor receives that CA bundle and the trusted service's expected URI through a mounted volume
that updates in place, so a CA rotation reaches running executors. The trusted service needs permission to create and
delete Secrets in its namespace, but holds no signing key.

## What is reused

The executor's file and command logic already exists in two places. `internal/executionexecutor` runs on `osfs` and
supports every file operation listed above. The microVM guest agent's workspace and exec services
(`environment/microvm/workspace` and `environment/microvm/guestexec`) add streaming command output, cancellation, and
replacement through a temporary file and rename, but lack rename, list, remove, and copy, and cap files at 512 KiB.
The [implementation companion](implementation.md#the-microvm-guest-agent) compares them.

The gRPC server and client are thin adapters over the engine's file and command interfaces, so each placement plugs in
its own implementation: the Kubernetes executor uses `internal/executionexecutor` and `osfs`, and the microVM guest
keeps its workspace and exec services. `fsconformance` and a command conformance suite hold both to the same
semantics. The generated code and the adapters move into two standalone modules, one for the generated code and one
under `internal/`, so that both the root module and `environment/microvm` can import them. The microVM guest agent's
own framing and handshake are not used.

## Rollout

The server replaced Pod exec rather than running alongside it: the native Kubernetes execution path is not deployed in
staging, so there were no users to migrate. The Pod exec transport, the per-operation lease, the one-operation limit,
and the provider's `pods/exec` permission were removed when mecak8s switched to the shared services, together with
mutual TLS, so the executor is never reachable without authentication, encryption, and a pinned certificate.

## Local execution

The same server runs locally inside the microVM placement, in place of today's guest agent, with no trusted service in
between. The [local execution](local-execution.md) chapter describes it.

## Open questions

- **Process separation.** The server and shell programs run in the same container as the same user, so a shell program
  can read the server's key or replace the server process. The trusted service rechecks the Pod after the handshake, so
  a copied key works only against the same Pod. Running the server in a separate container, or as a separate user,
  would narrow that. The [threat model](threat-model.md) chapter tracks whether it is required.
- **Binding to the environment.** Whether the executor checks the environment identity and epoch in the request
  context so that the executor can reject calls meant for a previous incarnation, or whether the trusted
  service's own fencing is sufficient.
- **Request context fields.** Whether owner, run, claim, and epoch are all needed, or some can be removed once the
  trusted service is reworked.

[workspace-contract]: ../../architecture/ports.md#environment-workspace-and-the-read-ledger
