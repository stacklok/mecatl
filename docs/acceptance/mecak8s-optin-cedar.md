# mecak8s opt-in Cedar authority — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — adds a public mecak8s CLI and Helm security interface for caller-scoped execution authority and fixes the operator-owned policy-file trust boundary.
**Decision record:** [ADR 0370](../adr/0370-mecak8s-cedar-authority-policy.md)
**Phase:** Kubernetes authority-evaluator parity
**Status:** proposed, 2026-09-25. Derived from [stacklok/mecatl#1368](https://github.com/stacklok/mecatl/issues/1368) and the existing Cedar composition contract.
**Delivery:** Split. The public Helm values, policy-source custody, OIDC prerequisite, and fail-closed security behavior require interface review before implementation.
**Expected tasks:** deferred to orchestration
**Issue:** [stacklok/mecatl#1368](https://github.com/stacklok/mecatl/issues/1368).

`mecak8s` exposes the same explicit `local`, `noop`, or `cedar` evaluator selection as
`mecated`. Cedar remains opt-in and consumes one operator-owned static policy projected from
an existing same-namespace ConfigMap or Secret. A verified OIDC owner supplies the exact
issuer/subject identity used for caller-scoped policy; missing effective OIDC configuration,
identity, policy projection, policy readability, or policy validity fails closed without
weakening to another evaluator.

This plan extends the deployment surface and narrowly hardens the shared Cedar adapter's
error boundary so a Secret-backed policy is not disclosed. The evaluator port,
carried-capability pre-check, policy semantics, entity/request shape, and authorization
decision contract remain those established by [ADR 0234](../adr/0234-authority-evaluator-port.md)
and the [authority execution boundary](../architecture/agent-loop.md#authority-evaluation-at-execution).

## Human decisions

None — issue #1368 fixes evaluator parity, static operator policy, opt-in defaults, fail-closed startup, exact OIDC issuer/subject behavior, offline coverage, and deployment documentation; ADR 0370 fixes the remaining runtime OIDC gate, sanitized diagnostics, Helm source, mode, mount, rolling-convergence, and validation details without leaving implementation choices open.

## Interface contract

- **gRPC / protobuf:** None — evaluator selection is deployment composition; no RPC, message, field, status, or wire behavior changes.
- **Exported Go APIs / interfaces:** None — reuse `app.Config.AuthorityEvaluator`, `app.Config.CedarAuthorityPolicy`, `port.AuthorityEvaluator`, and `port.AuthorityOwnerRequirement` without changing signatures or the guarded engine API.
- **Tool schemas:** None — `WebSearch` and every other tool retain their existing names and schemas; Cedar is consulted at the existing execution chokepoint after carried-capability selection.
- **CLI / config:** `mecak8s` adds `--authority-evaluator` with default `local` and accepted values `local`, `noop`, and `cedar`, plus `--cedar-authority-policy PATH`; flag names, defaults, accepted evaluator values, and parse-to-`app.Config` semantics match `mecated`. After all arguments are parsed, `mecak8s` applies the same whitespace trimming and case folding as the shared evaluator selector, then rejects an effective `cedar` selection unless `cfg.oidc.Enabled()` is true; the existing OIDC validation then requires a valid issuer/audience profile before listeners start. The Helm chart adds required `authority.evaluator` (`local` default; enum `local|noop|cedar`) and required `authority.cedarPolicy` with `configMapName`, `secretName`, and `key` (`authority.cedar` default). Selecting `cedar` requires `oidc.enabled=true`, a valid non-empty key, and exactly one valid non-empty source name. Selecting `local` or `noop` requires both source names empty. Cedar renders `--authority-evaluator=cedar`, `--cedar-authority-policy=/etc/mecatl-authority/authority.cedar`, and one read-only projection; `local` and `noop` render their explicit evaluator argument and no policy argument or volume. `extraArgs` remains later in argv and retains its documented escape-hatch precedence, but cannot bypass the post-parse Cedar/OIDC startup check.
- **Events / persistence:** None — policy, evaluator state, and OIDC-derived Cedar entities are process-local and add no session field, event, Redis key, durable policy copy, or migration. The policy is read once per process startup.
- **Security / authority:** The chart references, but never creates or inlines, one existing same-namespace ConfigMap or Secret whose name is a valid DNS-1123 subdomain and whose selected data key uses Kubernetes ConfigMap/Secret key syntax (at most 253 characters from alphanumerics, `-`, `_`, and `.`). It projects only that key as fixed filename `authority.cedar` in a dedicated volume mounted read-only at `/etc/mecatl-authority`; the source is non-optional. ConfigMap projection uses `defaultMode: 0444`; Secret projection uses `defaultMode: 0440`, matching the chart's existing public and sensitive projections. Cedar requires verified OIDC ownership and compares the exact issuer and subject independently. Evaluation first checks the carried capability, so policy can only deny. Missing projection prevents pod startup; absent, unreadable, empty, definition-group-granting, malformed, or otherwise invalid policy prevents `app.Build` and listener startup; no case falls back to `local`, `noop`, or permit-all. Cedar load and evaluation errors expose only a stable classification plus safe location metadata when available; returned errors and diagnostics never include policy literals, entity/request values, policy contents, or caller credentials.
- **Compatibility / migration:** Existing CLI invocations and default chart installations remain `local`. Operators opting into Cedar create the referenced object, enable OIDC, and select `authority.evaluator: cedar`. The evaluator reads policy only at process startup. Updating a mutable object updates the projected file but does not reload a running process or change the pod template; an independently restarted pod can therefore adopt the new policy before its peers. Operators must explicitly roll the Deployment and wait for rollout completion before treating a policy change as converged. Versioned immutable objects, selected by a Helm value change, are recommended because the changed source name updates the pod template and triggers a rollout. Rolling convergence is accepted; the chart does not switch Cedar to `Recreate`, so old and new policy may coexist during rollout. Removing Cedar means selecting `local` and waiting for rollout completion. Existing generic `extraArgs`/`extraVolumes`/`extraVolumeMounts` remain supported and are not rewritten.

## In scope — 3 scenarios, in implementation order

### Scenario 1 — mecak8s composes the selected evaluator

The Kubernetes command root mirrors `mecated`'s evaluator flags and threads them through its
thin config into the shared builder. `app.Build` remains the sole production selector and
loads Cedar before listeners start, as required by
[ADR 0234](../adr/0234-authority-evaluator-port.md#decision) and mecak8s's
[thin composition-root boundary](../adr/0048-mecak8s.md#decision).

**Acceptance:**
- AC1.1: With no authority flags, `mecak8s` parses and projects `AuthorityEvaluator="local"` and an empty `CedarAuthorityPolicy`.
  - verify: `TestADR_0370_Mecak8sCedar_Scenario1_DefaultIsLocal`
- AC1.2: Explicit evaluator and policy flags preserve their exact values through `parseFlags` and `appConfig` into the two existing `app.Config` fields.
  - verify: `TestADR_0370_Mecak8sCedar_Scenario1_FlagsThreadToComposition`
- AC1.3: Selecting Cedar with an absent, unreadable, empty, definition-group-granting, or syntactically invalid policy fails the ordinary mecak8s build before serving and never substitutes another evaluator.
  - verify: `TestADR_0370_Mecak8sCedar_Scenario1_InvalidPolicyFailsStartup`
- AC1.4: After final argument parsing, every Cedar spelling accepted by the shared selector (including case and surrounding-whitespace variants) with OIDC disabled fails startup before `app.Build` or listener creation, including when a direct invocation omits `--oidc-issuer` and when a later repeated argument clears it; `local` and `noop` retain their existing authentication choices.
  - verify: `TestADR_0370_Mecak8sCedar_Scenario1_CedarRequiresEffectiveOIDC`
- AC1.5: Cedar load and evaluation failures return a stable sanitized classification without reproducing canary literals from malformed policy, policy entities, owner issuer/subject, resource, or tool input in errors or diagnostics.
  - verify: `TestADR_0370_Mecak8sCedar_Scenario1_DiagnosticsDoNotDiscloseInputs`

### Scenario 2 — Helm projects one operator-owned static policy

The production chart provides a typed authority block instead of requiring operators to
assemble a security boundary solely from escape hatches. It follows the chart's existing
read-only Secret/ConfigMap projection posture in
[`deployment.yaml`](../../deploy/helm/mecak8s/templates/deployment.yaml) and keeps the pod
storage-free under [ADR 0048](../adr/0048-mecak8s.md).

**Acceptance:**
- AC2.1: The default chart render passes `--authority-evaluator=local`, and renders no Cedar policy argument, volume mount, volume, or policy-object content.
  - verify: `TestADR_0370_Mecak8sCedar_Scenario2_HelmDefaultIsLocal`
- AC2.2: A Cedar ConfigMap source and a Cedar Secret source each render the exact evaluator/policy arguments and one non-optional, read-only, single-key projection to `/etc/mecatl-authority/authority.cedar`, using `defaultMode: 0444` and `0440` respectively; source names and keys are YAML-quoted so valid string-like values retain their type, and the chart creates neither source object nor renders policy bytes.
  - verify: `TestADR_0370_Mecak8sCedar_Scenario2_HelmPolicyProjection`
- AC2.3: Both JSON Schema and template-helper validation (including `helm template --skip-schema-validation`) reject unknown evaluators, Cedar without OIDC, Cedar without exactly one source, invalid or overlong DNS-1123 source names, an empty/invalid/overlong Kubernetes data key, both source kinds together, and policy sources attached to `local` or `noop`.
  - verify: `TestADR_0370_Mecak8sCedar_Scenario2_HelmValidation`
- AC2.4: `extraArgs` remains after the chart-owned evaluator argument, preserving its existing documented precedence and compatibility escape hatch.
  - verify: `TestADR_0370_Mecak8sCedar_Scenario2_ExtraArgsPrecedence`
- AC2.5: Cedar retains the chart's ordinary `RollingUpdate` strategy. The rendered workload has no false content-checksum promise for the external object, while changing its configured source name changes the pod template; deployment documentation requires waiting for rollout completion and recommends versioned immutable source names.
  - verify: `TestADR_0370_Mecak8sCedar_Scenario2_RolloutContract`

### Scenario 3 — exact OIDC bot identity loses WebSearch only

An offline composed mecak8s run receives caller principals through the existing session-owner
path established by [ADR 0204](../adr/0204-caller-identity-threading.md). Cedar sees
separate `OwnerIssuer` and `OwnerSubject` entities from the
[adapter request mapping](../../internal/adapter/cedarauthority/cedar.go), while the engine
continues to authorize the selected tool rather than trusting disclosure.

**Acceptance:**
- AC3.1: With Cedar selected and `WebSearch` present in both carried capability sets, requests entering through the real in-process mecak8s authentication interceptor and an offline fake `PrincipalValidator` create durable sessions for their verified principals. A policy forbidding `Tool::"WebSearch"` for one exact owner `(issuer, subject)` returns a Cedar authority denial for that bot before the search provider runs, while another authenticated subject under the same issuer reaches the offline fake search provider successfully.
  - verify: `TestADR_0370_Mecak8sCedar_Scenario3_ExactOIDCBotWebSearchDenial`
- AC3.2: The same subject under a different issuer and a different subject under the same issuer do not collide with the forbidden pair; an ownerless Cedar execution remains denied before the tool body.
  - verify: `TestADR_0370_Mecak8sCedar_Scenario3_PrincipalPairIsExact`
- AC3.3: The canonical mecak8s deployment guide documents ConfigMap and Secret projection and modes, the exact bot-denial example, OIDC ownership, local default, startup failure, tightening-only behavior, the possible mixed-policy window from independent restarts or rolling replacement, waiting for rollout completion, and the recommendation to use versioned immutable source names; the shared authority guide links to that deployment procedure without duplicating Helm values.
  - verify: `task docs`, `task site:build`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Dynamic policy reload or policy watch | Future policy-lifecycle work | ADR 0234 defines one static startup-loaded policy; operators roll pods to adopt changes. |
| Policy authoring API, CRD, admission controller, or chart-created policy object | Future operator UX work | This slice references one existing operator-owned object and adds no cluster writer. |
| Cedar policy-language changes, new entities, raw tool arguments, or credential claims | Separate authority-evaluator plan | Reuse the existing provider-neutral request and authorization semantics; only sanitizing the adapter's error boundary is in this slice. |
| Permission-rule changes | Separate permission-policy work | Cedar remains distinct from `--permission-config` and does not change Ask/Allow/Deny rules. |
| Live-provider, live-IdP, or paid Kubernetes e2e | Operator qualification | Required proofs use mock LLM/search and an offline fake OIDC principal validator through the real authentication interceptor. |

## Definition of done

1. Focused `cmd/mecak8s`, `internal/app`, and Helm chart tests pass during implementation.
2. `task lint`, `task test:race`, `task docs`, and `task site:build` pass on the final candidate; `task api:check` confirms the guarded engine surface remains unchanged.
3. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
4. `go run ./cmd/mecademo` remains green and exercises tool call, permission approval, and result.
5. The implementation PR links the Plan / Interface PR and approved commit and reports exact interface conformance.
6. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- Kubernetes updates projected ConfigMap/Secret files eventually, but a running process deliberately does not reload them; independently restarted and surviving replicas can therefore enforce different versions until a rollout completes.
- An external policy object is not part of the pod-template checksum, so changing its contents alone does not trigger a rollout; a versioned source-name change does.
- `noop` is intentionally equivalent to the existing mecated deployment choice and disables carried-authority enforcement; documentation must label it as an explicit unsafe posture, not a Cedar fallback.
