# Canonical Shell command tool — acceptance plan

**Contract:** human-reviewed/v1
**Phase:** command-tool identity and POSIX compatibility
**Status:** approved, 2026-09-09. The issue owner reviewed and approved this amendment; its merged commit becomes the required implementation baseline before work resumes.
**Delivery:** Split. This changes model-facing tool schemas, compatibility behavior, operator configuration, durable awaiting-call handling, and an engine-module dependency; those interfaces require a Plan / Interface checkpoint before implementation.
**Expected tasks:** deferred to orchestration
**Issue:** [stacklok/mecatl#1109](https://github.com/stacklok/mecatl/issues/1109).
**ADR:** [ADR 0317](../adr/0317-canonical-shell-command-tool.md) — canonical Shell command-tool identity and portable POSIX diagnostics; [ADR 0324](../adr/0324-internal-shell-compatibility-diagnostic.md) — internal diagnostic ownership amendment.

The command-execution tool will be named `Shell`, accurately describing that it invokes the
configured shell rather than promising Bash. `Bash` remains accepted only in legacy operator
configuration; a new agent `Bash` tool call, including a call restored while a human-in-the-loop
approval is pending, is rejected. All tool discovery, help, and current documentation teach
`Shell`.

The change also adds parser-backed portable-POSIX compatibility feedback for model-facing
commands. It is deliberately a correctness aid, not an authorization boundary or sandbox: the
existing permission, guardrail, trust, secret-scrubbing, and runner controls remain independently
load-bearing.

## Human decisions

- [x] Canonical model-facing tool names — Decision: rename `Bash` to `Shell`, `BashStatus` to `ShellStatus`, and the synthetic system-temp permission capability to `ShellSystemTemp`.
- [x] Legacy spelling — Decision: accept and normalize `Bash` only in legacy operator policy/configuration (including permission and guardrail matchers), tool-list configuration, and legacy `--no-bash`. Do not normalize a new or restored agent `Bash` tool call; it is rejected because only `Shell` is a catalog tool. A backend upgrade during a human-in-the-loop legacy Bash approval may therefore resolve as an unknown-tool error. Document `Bash` only as a migration footnote.
- [x] Public capability compatibility — Decision: retain the existing `ServerCapabilities.bash` protobuf field and Go `Capabilities.Bash` member as stable shell-availability indicators; document their meaning rather than rename wire/API fields.
- [x] Syntax profile and outcome — Decision: run the bounded portability diagnostic only when the configured shell path has basename `sh` or `dash`, without resolving symlinks. A configured `bash` path allows Bash syntax. A selected AST construct under the recognized old-school-shell heuristic returns a normal, non-executing `Shell` tool error; it is compatibility feedback, not a security control.
- [x] Validation scope — Decision: inspect the final, post-PreToolUse command immediately before model-facing foreground, background, and temporary-scope command execution; do not apply it to operator hooks or internal git operations.
- [x] Parser strategy — Decision: add `mvdan.cc/sh/v3` to the engine module and parse the effective command in Bash mode. Reject only the explicitly non-portable AST forms `TestClause` (`[[ … ]]`), `ProcSubst` (input/output process substitution), `ArrayExpr`, and dollar-prefixed `SglQuoted` (`$'…'`); accept all other parsed forms unchanged. `set -o pipefail` is not rejected. Fixtures must prove quoted text, comments, escaped content, and here-document bodies are not rejected as syntax.
- [x] Legacy flags and configuration — Decision: accept legacy `Bash` configuration values and `--no-bash` without advertising them; all help and documentation use the new Shell names.
- [x] Parser ownership and API boundary — Decision: place the parser-backed diagnostic in `engine/internal/shellcompat`, shared only by the engine’s Shell implementations. It is execution-compatibility feedback, not governance policy. The internal package may expose `Check(shellPath, command string) error` only to engine descendants; Go's `internal` boundary prevents external consumers from importing it. Its AST implementation and concrete error stay unexported, and it adds no public engine API beyond the approved Shell rename.

## Interface contract

- **gRPC / protobuf:** No schema changes. `ToolCall.name`, hook/event tool-name strings, and generic result fields carry the canonical `Shell` value. Retain `ServerCapabilities.bash` as the compatibility-stable shell-availability field.
- **Exported Go APIs / interfaces:** Replace `tool.BashToolName`, `agent.BashTool`/`NewBashTool`, and exported BashStatus counterparts with canonical Shell-named symbols; retain no Bash-named Go source aliases. Record this breaking engine API change in `engine/api/*.txt` and `engine/CHANGELOG.md` under the engine compatibility policy. Add `mvdan.cc/sh/v3` as an explicit engine-module dependency in `engine/go.mod`/`engine/go.sum`; its parser use lives only in `engine/internal/shellcompat`. That package exposes `Check(shellPath, command string) error` only to engine descendants; it exposes no AST values or concrete error type to its callers and is not an externally importable engine API. Permit only `engine/agent` and `engine/adapter/fstools` to import it. Update the helper, consumer depguard allowlists, `engine/arch` import-direction table, and living architecture import rule accordingly; permit only `mvdan.cc/sh/v3/syntax` in the helper and preserve the `GOWORK=off` engine-standalone proof. Do not widen `tool.CommandRunner` for profile discovery.
- **Tool schemas:** Register and advertise `Shell` with the existing `command`, `timeout_ms`, `temp_scope`, and agent-only `background` arguments. Register and advertise `ShellStatus` with the existing status-tool schema. Do not register or normalize a new agent `Bash` tool call: it remains an unknown tool and cannot execute. The tool description identifies the configured shell and states that the bounded diagnostic applies only when its configured path has basename `sh` or `dash`.
- **CLI / config:** Rename advertised `--no-bash` to `--no-shell` while retaining `--no-bash` as an undocumented compatibility alias. Existing `--shell` stays canonical. Accept `Bash(...)` only in legacy operator configuration, including permission and guardrail matchers, and normalize it to `Shell`; emitted help, generated configuration reference, examples, and user documentation use `Shell`.
- **Events / persistence:** Persist new calls, pending asks, hook events, approvals, and event-log tool names as `Shell`. A restored legacy `Bash` pending call is not translated: after its human verdict it follows ordinary unknown-tool handling. Completed historical transcript/event records retain their original spelling. No snapshot or protobuf shape changes or migration are required.
- **Security / authority:** Move every Bash-specific policy, plan-mode, command-extraction, learning, system-temp, guardrail, and hook/waiver matcher atomically to canonical `Shell`; legacy operator configuration normalization must preserve deny dominance and guardrail coverage. A new or restored agent `Bash` tool call must not normalize into an executable Shell call. Enable the bounded AST diagnostic only when the trusted configured shell path has basename `sh` or `dash`, without symlink resolution; `bash` and every other path execute its parsed command unchanged. Keep the existing raw-text `SplitCommands`, `Canonicalize`, `ReadOnlyBash`, `SubstitutionReadOnly`, and `IsolationApprovable` semantics and fuzz proofs unchanged: the internal parser output is a private, separate execution-compatibility diagnostic and is never printer-derived input to policy matching. The diagnostic occurs after trusted PreToolUse mutation and before process spawn, but does not grant, deny, or replace authorization. Secret scrubbing and runner isolation remain unchanged.
- **Compatibility / migration:** This is an intentional canonical model-tool rename before the public release. `Bash` is accepted only for the specified legacy operator configuration and legacy flag; it is not an accepted new or restored agent tool-call spelling. An in-flight pre-upgrade Bash approval may finish with an unknown-tool error after upgrade; that one-time switch-over breakage is accepted. No catalog alias may bypass command-specific policy. Frozen ADR 0201 remains historical; ADR 0310 supersedes its tool-identity decision only.

## In scope — 3 scenarios, in implementation order

### Scenario 1 — Canonical Shell discovery retains command-policy safety

A model and operator discover one command tool named `Shell` and, when supported, one
background-status tool named `ShellStatus`. The name reaches the same command-specific policy
paths that previously protected `Bash`; it must never become an ordinary tool because that would
bypass the compound-command and plan-mode gates described in [ADR 0201](../adr/0201-background-bash.md)
and the [ports architecture](../architecture/ports.md).

**Acceptance:**
- AC1.1: Every shell-enabled main and child catalog advertises `Shell`, never a second `Bash` command tool; background-capable catalogs advertise `ShellStatus` under the existing availability rules.
  - verify: `TestCanonicalShellTool_Scenario1_CatalogNames`
- AC1.2: A new agent `Bash` tool call is rejected as an unknown tool and never normalized or executed as Shell.
  - verify: `TestCanonicalShellTool_Scenario1_RejectsLegacyToolCall`
- AC1.3: Canonical `Shell` calls use the existing deny-dominant command extraction, substitution-aware evaluation, plan-mode behavior, learned-rule handling, system-temporary-scope permission, and guardrail/waiver paths.
  - verify: `TestCanonicalShellTool_Scenario1_CommandPolicyCoverage`
- AC1.4: `ServerCapabilities.bash` and Go `Capabilities.Bash` retain their existing wire/API spelling while truthfully reporting canonical Shell availability.
  - verify: `TestCanonicalShellTool_Scenario1_CapabilityCompatibility`

### Scenario 2 — Legacy operator configuration retains its permission safety

Existing operator configuration must not lose a deny or other command-specific policy merely
because the model-facing canonical name changed. In-flight agent calls deliberately do not receive
that migration treatment: an upgrade during a human-in-the-loop legacy Bash approval may resolve
as an unknown-tool error. This follows the deny-dominant permission invariant in [AGENTS.md](../../AGENTS.md).

**Acceptance:**
- AC2.1: Legacy `Bash(...)` permission and guardrail configuration plus agent/skill tool-list configuration normalize to `Shell` before command-policy and guardrail evaluation, preserving deny dominance.
  - verify: `TestCanonicalShellTool_Scenario2_LegacyConfigPreservesDeny`
- AC2.2: A restored legacy `Bash` pending call is not translated into Shell: after a human verdict it follows ordinary unknown-tool handling and does not execute.
  - verify: `TestCanonicalShellTool_Scenario2_LegacyPendingCallRejected`
- AC2.3: `--no-shell` is the advertised switch and legacy `--no-bash` has identical disabling behavior without appearing in help; current configuration and user documentation present only Shell, with one migration footnote for Bash.
  - verify: `TestCanonicalShellTool_Scenario2_LegacyNoBashFlag`

### Scenario 3 — Bounded Bash-AST portability feedback is non-authoritative

The command tool parses the post-hook effective command in Bash mode only when the configured
shell path has basename `sh` or `dash`, without resolving symlinks. It detects only the four
explicit AST forms selected by this contract. A configured `bash` path, or any other path, permits
Bash syntax. This is a pragmatic path-name compatibility aid, not interpreter detection or shell
emulation; the runner remains the final executor, as specified in [architecture ports](../architecture/ports.md).

**Acceptance:**
- AC3.1: With configured shell basename `sh` or `dash`, `[[ … ]]` (`TestClause`), process substitution (`ProcSubst`), array expressions (`ArrayExpr`), and ANSI-C quoting (`SglQuoted.Dollar`) each return a bounded Shell tool error and do not spawn a process across foreground, background, and temporary-scope paths.
  - verify: `TestCanonicalShellTool_Scenario3_NonPortableASTDoesNotExecute`
- AC3.2: With configured shell basename `bash`, the selected AST forms, `set -o pipefail`, parse failures, and all other forms execute unchanged; the detector is not enabled for an unrecognized shell-path basename.
  - verify: `TestCanonicalShellTool_Scenario3_BashAndUnknownShellPassThrough`
- AC3.3: With the `sh`/`dash` heuristic enabled, quoted strings, comments, escaped content, and here-document bodies that contain Bash-looking text do not produce a compatibility error solely from that text; parsed forms outside the selected set pass the original command bytes to the configured shell unchanged.
  - verify: `TestCanonicalShellTool_Scenario3_SyntaxContextAndParseFailure`
- AC3.4: The built engine’s model-visible Shell description states the configured shell and that the bounded diagnostic applies only to `sh`/`dash` path basenames; permission, guardrail, trust, and secret-scrubbing behavior remain unchanged by a compatibility error.
  - verify: `TestCanonicalShellTool_Scenario3_SystemPromptAndAuthoritySeparation`
- AC3.5: A PreToolUse mutation is the exact raw command supplied to the parser and runner; parser inspection never formats or reconstructs AST text, and each permitted execution path receives those effective bytes unchanged.
  - verify: `TestCanonicalShellTool_Scenario3_EffectiveCommandBytePreservation`
- AC3.6: The portability parser is shared only through `engine/internal/shellcompat.Check`; only `engine/agent` and `engine/adapter/fstools` may import it. Its AST and concrete error remain private to the package, it is inaccessible to external consumers under Go's `internal` boundary, and no exported governance or tool diagnostic helper is added to the engine API.
  - verify: `TestShellCompatibilityDiagnostic`, `TestCoreImportDirection`, `task lint`, `task api:check`, `task test:engine-standalone`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Use parser results for authorization, sandboxing, or security classification | never for #1109 | Compatibility diagnostics are not a security boundary. |
| Extend linting to operator hooks or internal git commands | future dedicated plan | Their commands are trusted operator/harness input, outside the model-facing affordance. |
| Reuse `mvdan.cc/sh/v3` in unrelated command-processing paths | future approved plan | This issue proves one internal Shell-compatibility helper only. |
| Expose the diagnostic as a public engine API | future public-API decision | Consumers receive compatibility feedback through the Shell tools; the parser heuristic stays internal. |
| Replace or rename stable capability protobuf/API fields | future major API decision | Preserve `ServerCapabilities.bash` and `Capabilities.Bash`. |

## Definition of done

1. Deterministic offline tests prove the bounded AST behavior in Scenario 3, including its four rejected constructs, its explicit `pipefail` pass-through, syntax-looking data, and raw-byte preservation.
2. `task api:check`, `task lint`, `task test`, `task docs`, and `go run ./cmd/mecademo` pass.
3. `task ac-trace-strict` resolves every named proof when this plan becomes `landed`.
4. `engine/CHANGELOG.md` and `engine/api/*.txt` record the breaking exported engine API rename under the classification required by `engine/COMPATIBILITY.md`.
5. The implementation PR links this Plan / Interface PR and its merged approved commit, reports each interface clause’s conformance, and `/panel-review` reports no ship blocker or unwaived failure.

## Deferred decisions and known risks

- The detector is intentionally bounded to `TestClause`, `ProcSubst`, `ArrayExpr`, and dollar-prefixed `SglQuoted`; it does not promise to emulate an installed shell or diagnose every extension.
- The accepted one-time switch-over break applies to a legacy Bash call that is already awaiting human approval when the backend upgrades; legacy operator configuration remains normalized to preserve its permission safety.
- If implementation discovers a material compatibility ingress absent from this contract, it must stop as contract drift rather than silently choosing migration behavior.
