# Local execution

*This chapter describes the intended design, not current implementation status.*

Mecatl also runs file tools and `Shell` on a developer's machine. This chapter compares how a session's files and
commands work locally and on Kubernetes, describes how local execution uses the same executor server as Kubernetes, and
records which differences the design closes and which it keeps.

## Three placements

A placement decides where a session's file tools and `Shell` run. Mecatl has two local placements, selected with
`execution.default_placement`, and the Kubernetes one:

- **`host-local`**, the default. File tools use `osfs` directly on the user's checkout, inside the Mecatl process, and
  `Shell` runs commands as child processes on the user's own machine. There is no container, VM, or separate process.
- **`microvm-local`.** File tools and `Shell` run inside a lightweight VM, one per repository, served by a guest agent.
  The host daemon `mecatl-microvmd` creates and starts the VM when a session first needs it, and proxies requests to
  the guest. The session works on a Git worktree captured from the user's checkout, uncommitted changes included. The
  [microVM environments](../../architecture/microvm-environments.md) chapter describes it.
- **Kubernetes.** File tools and `Shell` run in a dedicated executor Pod on a PVC, reached through the trusted service,
  as the rest of this design describes.

In every placement the agent loop sees only a `Workspace` and a `CommandRunner`. Whatever creates or starts the
execution environment, whether nothing for `host-local`, `mecatl-microvmd`, or the provider's controller, does so out of
the agent loop's sight.

| | `host-local` | `microvm-local` | Kubernetes |
| --- | --- | --- | --- |
| What serves files and commands | `osfs` inside the Mecatl process | the guest agent in a VM | the executor in a Pod |
| Where the files live | the user's checkout | a Git worktree captured from the checkout | a PVC |
| Who manages the environment | nothing to manage | `mecatl-microvmd`, started on demand | the provider's controller |
| Isolation | none; full access as the user | a VM with configurable egress | a Pod with a RuntimeClass and default-deny network policy |
| Background `Shell` | supported | supported | not supported |
| Read ledger | per session | per session | per run |
| Subagents, teams, and parallel work | supported | supported | disabled |
| Project instructions | loaded if the project is trusted | loaded from the guest worktree when selected | never loaded |

## Executor server in local environments

Local execution in this design is `microvm-local` running the executor server described in the
[executor protocol](executor-protocol.md), in place of today's guest agent. The executor server is built from the guest
agent's workspace and exec services, so the VM runs essentially the same code it runs today, now shared with
Kubernetes.

What stays specific to the VM:

- `mecatl-microvmd` keeps managing the VM's lifecycle, out of the agent loop's sight, as today;
- the connection between the host and the guest stays the VM's own: the guest dials out over vsock and proves it holds
  the VM's boot key;
- the workspace is still the captured Git worktree, shared into the VM.

What becomes shared with Kubernetes is the file service and the command service: their operations, their errors, and
their guarantees. `host-local` does not change and uses no executor.

## Which parts apply locally

On Kubernetes, the trusted service sits between the agent loop and the executor and is responsible for admission,
authorization, run claims, administration, and withdrawal. There is no trusted service locally. The parts of the design
that exist because of it are either unnecessary on a single user's machine or already have a microVM equivalent:

- **File and command operations** apply unchanged. They are what the two placements share.
- **Binding a session to an environment** is already handled by `mecatl-microvmd`'s own lifecycle operations, which
  create, resolve, detach, and fork environments.
- **Run claims, owner and binding checks, and the opaque context** are unnecessary. There is one user and one machine,
  and `mecatl-microvmd`'s acquisitions already keep one session's work exclusive. The executor ignores the opaque
  context in every placement, so a local caller sends none.
- **Capability admission** reduces to local settings: nobody else needs to admit the capabilities, but the switch that
  disables the command service still applies.
- **Environment administration** is mostly unnecessary. `mecated microvm status`, `delete`, and `doctor` cover the local
  cases, and on one machine Mecatl can always tell whether a VM or process has stopped, so the fencing for unprovable
  termination in [storage and recovery](storage-and-recovery.md#termination-cannot-be-proven) does not arise.
- **Withdrawal** of authority is unnecessary: there is no one else whose access could be taken back.
- **Cancellation** applies unchanged, because a command's lifetime is tied to its call in every placement; see
  [withdrawal and cancellation](withdrawal-and-cancellation.md).

The interface stays the same everywhere. The local caller simply does not use the parts that only make sense with a
trusted service in between.

## Differences that remain

The design closes some of the differences between local and Kubernetes execution:

- **Read ledger scope.** Kubernetes scopes the ledger to the session, as local placements already do, so the agent no
  longer has to re-read files before editing them on every prompt.
- **Streaming commands.** The executor server streams command output. Background `Shell` jobs are built on streaming
  runners, so this removes the technical obstacle to background jobs on Kubernetes; whether Kubernetes enables them is
  outside this work. The microVM keeps its background jobs.

Other differences remain on purpose:

- **Isolation.** Each placement keeps its own boundary: none for `host-local`, a VM for `microvm-local`, and a Pod for
  Kubernetes.
- **Where files live.** The user's checkout, a captured worktree, and a PVC serve different needs and stay as they are.
- **Project instructions.** Kubernetes still never loads [project instructions][instruction-sources], because local
  project trust cannot authorize them for a remote deployment.

One large difference is outside this work: Kubernetes sessions cannot use subagents, teams, or parallel work, because
remote execution disables them. Closing that gap is a separate decision.

## Open questions

- **Guest agent features.** The guest agent's file service lacks rename, list, remove, and copy and caps files at
  512 KiB. Porting these into the shared server also changes what `microvm-local` sessions can do.

[instruction-sources]: ../../../user-docs/features/security-and-execution/execution-environments.md#instructions-and-permission-sources
