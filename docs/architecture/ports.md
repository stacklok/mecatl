# The ports

A port is an interface the agent loop or the server consumes and an adapter
implements. Ports live in [`engine/port`](../../engine/port), which imports only the
standard library and the domain packages (`session`, `tool`, `prompt`, `governance`).
The loop receives its ports by injection through `agent.Deps`, so the same loop runs
against a real provider and disk in production and against in-memory fakes in tests.

## Why the ports stay narrow

`engine/` is a separately published module, and its ports are its public extension
points. Two rules follow.

First, ports stay provider-neutral. `port.LLMRequest` carries exactly four fields
(system prompt, messages, tool specs, and an opaque model string), and
`llm_neutral_test.go` fails if a field is added. Provider-specific options, such as a
thinking budget or a base URL, belong on the adapter's constructor because the loop
must never branch on provider.

Second, adding a method to a published interface breaks every implementer. New
behavior arrives as a separate optional interface that the consumer discovers by type
assertion, for example `HookApprovalLearner`, `RunAwareToolCallRecorder`,
`PrunableStore`, `SessionCreator`, and `CursorEventLog`. When a capability is missing,
the feature is either skipped or reported as unsupported. A decorator must forward the
optional interfaces and capability signals of what it wraps; an `LLMProvider`
decorator, for example, must forward `Capabilities`. See
[`engine/COMPATIBILITY.md`](../../engine/COMPATIBILITY.md) for the stability rules.

## The main ports

| Port | What it abstracts |
| --- | --- |
| `LLMProvider` | One streaming model call returning provider-neutral `Chunk`s, plus the input modalities the provider accepts. |
| `SessionStore` | Saving and loading a session. Not-found wraps `ErrSessionNotFound`. |
| `PermissionPolicy` | Deny, then ask, then allow evaluation of a tool call, and learning an "allow always" verdict. |
| `AuthorityEvaluator` | Authorizing a tool call against the session's carried capability set; it can only narrow that set. |
| `HookRunner` | Running a lifecycle hook and returning its outcome. |
| `EventSink` | Relaying live events to the API stream. |
| `EventLog` | The durable, append-only event timeline per session. |
| `ToolCallRecorder` | Per-tool-call audit records. |
| `Diagnostics` | Operational logging, injected instead of a global logger. |
| `Clock` | The wall clock. |
| `SessionLease` | Optional cross-process single-writer exclusion for one session. |
| `SessionLiveness` | Keeping engine-owned child sessions marked live for their whole lifecycle. |
| `ScheduleStore` | The registry of scheduled tasks, with an atomic, at-most-once `Claim` per fire. |
| `DeliveryQueue` | Durable delivery of scheduled-task results into the session that created the schedule. |

Some contracts matter beyond their signatures:

- `PermissionPolicy` is implemented by `engine/adapter/permpolicy`, which wraps the
  session-free `governance.Evaluator`. Governance cannot import `session`, so the
  session-typed adapter sits outside it. `PermissionStore` holds learned rules.
- `SessionStore.Load` may rebuild a session by folding an event log;
  `engine/adapter/eventsource.Fold` is the reference. A fold restores the structure but
  not the opaque provider replay fields, so it replays byte-for-byte only for
  providers that do not use them.
- The loop emits events but never calls `EventLog` or `SessionLease`: the server
  relay persists events, and the server run-entry path holds the lease. The schedule
  tools reach `ScheduleStore` only through the server's `ScheduleManager`.
- `EventSink` receives an already-cancelled context for terminal events, so a sink
  must not drop events because the context is done.
- `Diagnostics` is separate from audit and events; an unset sink is `NopDiagnostics`.
- Every wall-clock read in core packages goes through `Clock`, enforced by
  `engine/arch/clock_test.go`, so a host can drive the engine deterministically.

## The tool contract

`tool.Tool` has three methods. `Spec` returns the name, description, and JSON schema
the model sees. `ReadOnly` decides scheduling: read-only calls in one turn may run in
parallel, while mutating calls run one at a time. `Execute` receives the call and a
`tool.Environment`. A tool-level failure returns a result with `IsError` set, which
the model sees and can recover from; a Go error means the harness itself failed.

`tool.Catalog` is the name-to-tool registry and projects it per permission mode: plan
mode exposes only read-only tools. Optional marker interfaces adjust that projection:
`PlanOnly` tools appear only in plan mode, `Disclosable` tools advertise a short spec
until the model loads the full one, and `DispatchSerial` makes a read-only call run
alone.

## Environment, Workspace, and the read ledger

`FileSystem`, `Workspace`, and `Environment` live in `engine/tool`, not `engine/port`.
The port package already imports `tool`, because `LLMRequest` carries `tool.ToolSpec`
values. `Tool.Execute` takes an `Environment`, so defining these types in `port` would
make the two packages import each other.

A `tool.Environment` is an immutable bundle for one namespace: an `EnvironmentRef`, a
required `Workspace`, a required `ReadLedger`, and an optional `CommandRunner`. It
carries no policy, hooks, or MCP, so it is not a service locator. A fork gets a new
`Environment` from an `EnvironmentForker`, always with a fresh ledger.

`Workspace` is rooted and rejects path escapes. Agent-facing reads return content
plus an opaque `FileVersion`. Writes are either create-only `CreateFile` or
conditional `ReplaceFile`, which fails when the file changed since the caller's read.
There is no unconditional write, and a zero version never matches. These operations
are atomic for calls through the same backend; `osfs` also locks across instances in
one process. A process that writes outside the Workspace, such as a shell command, can
still race a replace, so this is best-effort compare-and-swap, not kernel locking.

`ReadLedger` stores the read-before-edit evidence separately from file content. A
lookup reports found, absent, or unavailable, and Edit and Write fail closed when the
ledger is unavailable.

`CommandRunner` is bound to one namespace at construction, so a command's working
directory always matches the Workspace. Only the Shell tool uses it, and a nil runner
yields `ErrNoShell`. Shell must register as `tool.ShellToolName` because the permission
evaluator applies its compound-command checks to that name only.

## Reference adapters and conformance suites

`engine/adapter` holds offline reference adapters: `mockllm` replays scripted model
turns, `memfs` is an in-memory Workspace with a programmable command runner,
`memstore` is the in-memory default `SessionStore`, and `memledger`, `memlease`, and
`memschedulestore` cover their ports. `nofs` is the workspace for sessions with no
filesystem: reads find nothing and writes fail loudly.

Each port with several implementations has a shared conformance suite, such as
`storeconformance`, `fsconformance`, `eventlogconformance`, `leaseconformance`,
`ledgerconformance`, and `scheduleconformance`. The in-memory reference and the
production adapters (for example `osfs`, `jsonlstore`, `redisstore`, and
`k8slease`) run the same suite. That shared suite is why tests use the reference
adapters instead of hand-written mocks: they are offline and deterministic, and they
behave like production because both pass the same contract.

## Related

- [The domain model](domain-model.md)
- [The agent loop](agent-loop.md)
- [Providers](providers.md)
- [Extension points (public docs)](../../user-docs/building/go/extension-points/index.md)
