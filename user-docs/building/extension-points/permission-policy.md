---
sidebar_position: 6
title: PermissionPolicy
description:
  Implement custom authorization and approval decisions for Mecatl tool calls.
---

# PermissionPolicy

`port.PermissionPolicy` decides whether a tool call can run. Implement it when
Mecatl's permission configuration cannot express your policy, such as when
decisions come from an external authorization service.

Use [Permissions and guardrails](/building/what-you-get/permissions.md) to
configure the supplied policy.

## The interface

```go
type PermissionPolicy interface {
    Evaluate(
        ctx context.Context,
        sessionID session.SessionID,
        mode session.PermissionMode,
        call session.ToolCall,
        workspace tool.WorkspaceReader,
    ) governance.PermissionDecision

    Learn(
        sessionID session.SessionID,
        call session.ToolCall,
    )
}
```

`Evaluate` runs before every tool call and returns an effect and reason:

|Effect|Behavior|
|-|-|
|`governance.Allow`|Run the tool|
|`governance.Ask`|Pause for a client verdict|
|`governance.Deny`|Return the reason to the model without running the tool|

An ask decision carries one `AskProvenance` value. `AskProvenanceConfigured`
preserves an operator's configured ask. `AskProvenanceConfiguredAllowFloor`
allows a one-time child resolution only when a configured allow covers the outer
command, hidden substitution commands are read-only, and the command remains
confined to the isolated workspace. `AskProvenanceBuiltinSubstitutionFloor`
identifies the narrow built-in worker Shell floor that an enforcing contextual
permission review can resolve once. Leave the zero value for default and
unclassified asks; it grants no additional approval authority, while existing
isolation and optional headless-review fallbacks continue to apply.

`workspace` is a read-only view used to resolve project configuration. Treat a
nil value as no project configuration.

`Learn` handles an "allow always" verdict. Record only a narrow, session-scoped
allow for calls that are safe to learn. Learned rules cannot override a deny, a
configured ask, or plan mode.

## Preserve permission resolution

Rules use this shape:

```go
type Rule struct {
    Scope    Scope
    Tool     string
    Pattern  string
    Effect   Effect
    Exact    bool
    Audience Audience
}
```

`Tool` and `Pattern` select calls. An empty value matches all tools or
arguments. Learned rules use `Exact: true` so characters such as `*` and `?`
remain literal.

Resolve matching rules with these safety properties:

1. In plan mode, deny `Edit`, `Write`, `Copy`, `Move`, `Remove`, and
   non-read-only `Shell` calls before consulting rules.
1. A deny at any scope wins.
1. A configured ask wins over every allow.
1. A configured allow can loosen only the built-in ask floor.
1. At the same scope, ask wins over allow.
1. When effects match, the higher-precedence scope wins.
1. Ask when no rule matches.

Scopes, from highest to lowest precedence, are `ScopeManaged`, `ScopeCLI`,
`ScopeLocalProject`, `ScopeSharedProject`, `ScopeUser`, and
`ScopeBuiltinDefault`.

`Audience` restricts a rule to the main engine, child engines, or both.
`AudienceAll` is the zero value and matches every engine. Create separate
policies for main and child engines when your application supports delegation.
Top-level denies should use `AudienceAll` so they continue to protect child
runs. Build the supplied default child policy from
`permpolicy.AllowAllFloorRules()` before appending child-specific rules. Its
`ScopeBuiltinDefault` placement preserves the child approval provenance checks.

## Use the supplied policy

`engine/adapter/permpolicy` implements the port over `governance.Evaluator`:

```go
policy := permpolicy.NewPolicy(
    rules,
    permstore.New(),
    governance.WithAudience(governance.AudienceMain),
)
```

The optional `PermissionStore` holds learned rules:

```go
type PermissionStore interface {
    Record(
        sessionID session.SessionID,
        rule governance.Rule,
    )

    Rules(
        sessionID session.SessionID,
    ) []governance.Rule
}
```

Implementations must be safe for concurrent use, deduplicate identical rules,
and return a copy from `Rules`. `engine/adapter/permstore.New` provides an
in-memory implementation capped at 256 rules per session. In a custom embedding,
call its `Forget(sessionID)` method when the session closes.

Use `permpolicy.NewPolicyWithResolver` when rules depend on the session
workspace:

```go
type RuleResolver interface {
    Resolve(
        ctx context.Context,
        workspace tool.WorkspaceReader,
    ) []governance.Rule
}
```

The policy calls the resolver during `Evaluate`, so cache file or network
lookups. The shipped resolver loads user and explicit CLI rules once at
construction. It revalidates cached project files during evaluation and includes
project allow rules only when constructed with `TrustProject: true`.

## Learn narrow rules

The supplied policy derives one exact allow rule from a learnable call. It does
not learn:

- compound shell commands, pipelines, or command lists;
- shell commands with command substitution, process substitution, or subshell
  grouping; or
- calls whose derived match pattern is empty.

Use the same constraints in a custom policy. Returning without recording a rule
is the safe behavior for an unlearnable call.

## Implement a policy

A custom policy can call an external authorization service and translate its
response into `governance.PermissionDecision`. Reuse `governance.Evaluator` for
local rule folding and the plan-mode gate when possible.

Calls to `Evaluate` are on the tool-dispatch path. Use bounded timeouts and
cache external decisions where appropriate. Return a clear reason for asks and
denials so the client and model can respond. Implement `Learn` as a no-op when
the external policy system does not support session-scoped rules.

## Test the policy

Cover these cases:

- deny over allow at every scope;
- configured ask over allow;
- plan-mode mutations;
- unmatched calls;
- main and child audiences;
- learned rules that cannot widen into glob matches; and
- compound or substituted shell commands rejected by `Learn`.

The tests under `engine/governance` and `engine/adapter/permpolicy` provide
fixtures for the supplied evaluator's resolution rules.

## Next steps

- [Configure permissions and posture](/building/what-you-get/permissions.md).
- [Implement lifecycle hooks](hook-runner.md).
- [Understand permission pauses in the agent loop](/building/what-you-get/agent-loop.md).
