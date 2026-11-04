# Threat model

*This chapter describes the intended design, not current implementation status.*

This chapter lists the attackers the design considers, what each can reach, and what must hold to contain it. It
covers the Kubernetes placement. Who may act on behalf of which user, and the threats to that, are the subject of the
[agent identity threat model](../agent-identity/threat-model.md).

The attackers, grouped by the component they act from:

- **Executor Pod**
  - [untrusted shell programs](#untrusted-shell-programs) run by the agent;
  - [a compromised executor](#a-compromised-executor), including an escape from its container;
  - [a stale executor](#a-stale-executor) that keeps writing after it has been replaced.
- **Agent loop**
  - [a compromised agent loop](#a-compromised-agent-loop) acting within its policy for any owner.
- **Trusted service**
  - [a compromised trusted service](#a-compromised-trusted-service), which controls every environment it manages.

Compromise of the Kubernetes control plane, the storage system, or the node is [out of scope](#out-of-scope).

## Untrusted shell programs

Shell programs are the main threat. Anything the agent runs through `Shell` may be hostile: a command written by a
prompt-injected model, code in the repository, or a dependency it installs. The design assumes these programs act
against the session.

What they can reach:

- the session's files under `/workspace`, which is the point of the session;
- the network egress the template allows, and nothing else, because executor Pods deny all other traffic by default;
- the executor server, on `localhost`, because they share the executor Pod's network namespace.

What they cannot reach:

- the executor server's operations, because the server accepts only callers presenting the trusted service's client
  certificate, which is never in the executor Pod (see
  [transport and authentication](executor-protocol.md#transport-and-authentication));
- the Kubernetes API, because the executor Pod mounts no ServiceAccount token;
- other environments, because each has its own PVC and executor Pods admit inbound traffic only from the trusted
  service;
- credentials of the trusted service, the agent loop, or the storage system, none of which are in the executor Pod.

One risk remains open. The executor server runs in the same container as shell programs, as the same user. A shell
program can therefore read the server's private key, signal the server, or replace the server process with one that
returns false results. The damage stays within the session that is already compromised, since the server serves only
that session's files and holds no authority beyond them, but the agent can no longer trust what file and command
results say. Running the server in a separate container or as a separate user would close this; whether that is
required is an open question.

## A compromised executor

If an attacker controls the executor itself, through a vulnerability in the server or by replacing it as above, it
reaches what the executor Pod reaches: its own environment's files and network egress, its own server certificate, and
nothing that would let it act on other environments. Whether the trusted service can tell one executor from another by
its certificate depends on how executor certificates are issued, which is still an open question in the
[executor protocol](executor-protocol.md#open-questions).

Escaping the container to the node is the container runtime's responsibility. Templates select a RuntimeClass for this
reason: a runtime such as gVisor or Kata Containers adds a boundary that a standard container runtime does not. Mecatl
does not provide that boundary itself.

## A compromised agent loop

The agent loop is trusted to state which user a session belongs to, and the trusted service accepts what it says within
the caller's policy. A compromised mecak8s can therefore ask for environments, capabilities, and file and command
operations for any owner its policy allows it to act for. The trusted service still enforces the caller's template
allowlist, the admitted capabilities, and the run claims, so a compromised agent loop cannot exceed its policy, but it
can act for any user within it. Whether the owner should be verified end to end is an open question in the
[implementation companion](implementation.md#open-questions), and the identity threat model covers the broader case.

## A compromised trusted service

The trusted service holds the most authority in the design: it can create and delete executor Pods and PVCs, it is
expected to issue the certificates executors use, and it decides which calls reach which executor. An attacker who
controls it controls every environment it manages.

The design treats the trusted service as trusted and limits what it holds. It needs rights over executor Pods and PVCs
in its namespace, and a way to issue executor certificates, but no access to the users' credentials, and it never runs
user commands itself. It also gives up the `pods/exec` permission it holds today: that permission exists only for the
Pod exec transport, and it lets the provider run any command in any Pod in its namespace. Replacing Pod exec with the
executor server removes the need for it.

## A stale executor

An executor that has been replaced, or whose termination cannot be proven, may still be running and writing to the
session's files. Starting another executor on the same storage would let two writers change the same files. The fencing
rules in [storage and recovery](storage-and-recovery.md#termination-cannot-be-proven) prevent this: no new executor
starts until the previous one is proven terminated or its node has been fenced.

## Out of scope

The design does not defend against compromise of:

- the Kubernetes control plane, which can change any Pod, volume, or policy;
- the storage system, which holds every session's files;
- the node's kernel or container runtime, beyond the boundary the selected RuntimeClass provides.

## Open questions

- **Process separation.** Whether the executor server must run in a separate container or as a separate user from shell
  programs, so that a shell program cannot read its key or replace it.
