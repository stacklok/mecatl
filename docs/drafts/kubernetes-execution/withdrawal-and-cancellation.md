# Withdrawal and cancellation

*This chapter describes the intended design, not current implementation status.*

Cancellation is a user stopping work they started. Withdrawal is taking back access that was already granted: a run
claim lapses, an administrator revokes an environment, a template is revoked, or the policy that allowed a caller
changes. This chapter describes how each of these stops file access and running commands in the execution environment.

Who may act, and how sign-out or the revocation of an agent's identity propagates, is the subject of the
[agent identity](../agent-identity/README.md) work, including its
[all-tool mediation](../agent-identity/all-tool-mediation.md) direction for filesystem tools and `Shell`. This chapter
starts where those decisions reach the execution environment.

## How it works today

Withdrawal today is enforced by the provider while it relays an operation:

- **A user cancels a task.** The agent loop cancels its call, the provider closes the Pod exec stream, and the executor
  kills the command's process group and every process the command started.
- **A run claim lapses.** Run claims last at most five minutes and must be renewed; once a claim is no longer current,
  the provider refuses further calls made with it.
- **An administrator revokes an environment.** `RevokeEnvironment` gives the environment a new grant generation, and the
  provider refuses further calls and renewals made under the old one.
- **A template is revoked.** The provider deletes the executor Pods of every environment that uses it.
- **The caller's policy changes.** The provider reloads its security manifest, and rechecks it every ten seconds while
  an operation runs, cancelling the operation if the caller is no longer allowed.

Every one of these depends on the provider being there. If the provider crashes while a command runs, nothing stops the
command until its own timeout, which can be up to 30 minutes, because the command is tied only to an exec stream that
nobody is watching.

## How withdrawal reaches the executor

With the [executor protocol](executor-protocol.md), a command's lifetime is tied to the call that started it: closing
the call cancels the command, and the executor then stops the command's process group and every process it started.
The executor holds no authority of its own; it serves whatever the trusted service forwards. Withdrawal therefore needs
only two things from the trusted service: stop forwarding new calls, and close the calls in flight.

- **Cancellation.** The agent loop cancels its call, the trusted service cancels the forwarded call, and the executor
  stops the command.
- **Revocation, a lapsed claim, a revoked template, or a policy change.** The trusted service refuses new calls for the
  environment and closes every forwarded call in flight for it. A revoked template still deletes the executor Pod, as
  today.
- **The trusted service crashes or loses its connection to the executor.** The connection to the executor breaks, and
  the executor cancels every call that connection carried. A connection that stays open but stops responding, such as
  across a network partition, is detected with gRPC keepalives.

This removes today's gap: a command no longer outlives the trusted service that started it.

## Timing

Mecatl does not promise a fixed bound on how quickly withdrawal takes effect. The delay is the sum of intervals that
operators can configure:

| Interval | What it bounds | Suggested default |
| --- | --- | --- |
| Policy recheck | How long the trusted service takes to notice a policy change or revocation for calls in flight | 10 seconds, as today |
| Keepalive interval and timeout | How long the executor takes to notice that the trusted service is gone | 10 seconds each, about 20 seconds in total |
| Run claim lifetime | How long a claim stays current without renewal | 5 minutes, as today's maximum |

Shorter intervals withdraw access faster at the cost of more traffic between the trusted service and the executor.

## What withdrawal does not do

- **It does not undo changes.** Files written before withdrawal stay written. The workspace keeps its state, and a later
  session bound to the same environment sees it.
- **It does not recall data.** Content already returned to the agent cannot be taken back.
- **It does not always prove that a command stopped.** If the executor cannot confirm that every process of a cancelled
  command has stopped, for example because a process is stuck on storage, it reports the outcome as unknown. The
  environment is then fenced, as described in
  [storage and recovery](storage-and-recovery.md#termination-cannot-be-proven).

## Open questions

- **Partial withdrawal.** Whether withdrawal can remove one capability, such as `Shell`, while leaving file access in
  place, or always applies to the whole environment.
