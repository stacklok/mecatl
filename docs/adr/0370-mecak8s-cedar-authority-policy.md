# ADR 0370 — mecak8s projects one static Cedar authority policy

- Status: Proposed
- Date: 2026-09-25
- Scope: mecak8s evaluator selection, Helm policy-source projection, OIDC ownership prerequisite, Cedar error sanitization, and policy rollout semantics
- Supersedes: None
- Superseded by: None

## Context

[ADR 0234](./0234-authority-evaluator-port.md) establishes an explicit composition choice
among `local`, `noop`, and `cedar`. Cedar is tightening-only, requires owner identity, reads
one static operator policy at startup, and fails closed when that policy cannot load.
`mecated` exposes this choice through `--authority-evaluator` and
`--cedar-authority-policy`, but the Kubernetes composition root does not. Consequently a
`mecak8s` operator cannot apply caller-scoped restrictions such as denying `WebSearch` to
one exact OIDC bot while retaining it for other authenticated owners.

The Helm chart already has generic argument and restricted volume escape hatches, but an
authority policy is a security boundary. Its default, source ownership, identity
prerequisite, mount shape, validation, and update behavior need one reviewable deployment
contract rather than an undocumented combination of arbitrary values.

## Decision

Expose `--authority-evaluator` and `--cedar-authority-policy` from `mecak8s` with the same
flag names, defaults, accepted evaluator values, and `app.Config` projection as `mecated`.
`local` remains the binary default. After final argument parsing, `mecak8s` applies the same
whitespace trimming and case folding as the shared evaluator selector, then rejects an
effective Cedar selection unless `cfg.oidc.Enabled()` is true; ordinary OIDC validation must
then accept the issuer/audience profile before listeners start. This post-parse check also
covers direct CLI use, selector case/whitespace variants, and later repeated arguments from
Helm `extraArgs`. `app.Build` remains the sole evaluator selector and policy loader, so
invalid Cedar configuration fails before serving and never falls back.

Add this Helm values contract:

```yaml
authority:
  evaluator: local # local, noop, or cedar
  cedarPolicy:
    configMapName: ""
    secretName: ""
    key: authority.cedar
```

For `cedar`, require `oidc.enabled=true`, a valid non-empty key, and exactly one valid
non-empty source name. Source names must be DNS-1123 subdomains; the key must be at most 253
characters and use Kubernetes ConfigMap/Secret data-key syntax (alphanumerics, `-`, `_`,
and `.`). Enforce the same cross-field and syntax rules in JSON Schema and template helpers
so `--skip-schema-validation` cannot bypass them. Reference that existing same-namespace
ConfigMap or Secret without creating or inlining it. Project only the selected key to the
fixed filename `authority.cedar` in one non-optional volume mounted read-only at
`/etc/mecatl-authority`; use `defaultMode: 0444` for a ConfigMap and `0440` for a Secret,
YAML-quote source names and keys, and pass the fixed path
`/etc/mecatl-authority/authority.cedar`. For `local` and `noop`,
require both source names empty and render no policy argument or volume. Render the selected
evaluator explicitly; retain `extraArgs` afterward as the chart's documented precedence
escape hatch, subject to the binary's effective Cedar/OIDC startup check.

Cedar compares the existing separate owner-issuer and owner-subject entities. The chart's
OIDC prerequisite supplies verified, durable session ownership; a static bearer or
ownerless deployment is not silently treated as caller identity. The existing
carried-capability check runs before Cedar, so neither the broad base permit needed by
Cedar's default-deny model nor any other policy can mint a missing tool.

The policy is immutable for the lifetime of one process. Updating the referenced object
does not trigger a Deployment rollout and does not change a running evaluator, but a pod
that independently restarts loads the current object and can temporarily differ from its
surviving peers. Keep the ordinary `RollingUpdate` strategy: mixed policy is accepted while
old pods are replaced, and operators must wait for rollout completion before treating a
change as converged. Recommend versioned immutable ConfigMap/Secret names; selecting a new
name changes the pod template and triggers rollout. Missing objects/keys prevent pod startup;
unreadable, empty, unsafe definition-group-granting, or invalid policies fail `app.Build`.
Neither path weakens enforcement.

Cedar policy parsing and evaluation are secret-bearing boundaries. Adapter errors and
diagnostics expose only stable failure classifications and safe location metadata when
available. They never reproduce policy literals, entity/request values, owner identity,
resource or tool input, or raw third-party Cedar diagnostics.

## Consequences

Existing deployments remain on the local evaluator. Kubernetes operators gain a typed,
schema-validated route to the already-shipped Cedar adapter, with ConfigMap support for
ordinary policy and Secret support when policy contents are sensitive. Policy bytes stay
out of argv, environment variables, chart-created resources, and generated documentation.

The chart gains a public values surface and an OIDC coupling for Cedar. Operators own the
referenced object, its permissions, and its rollout lifecycle. The fixed in-container path
avoids path-shaped chart input and makes command arguments deterministic. Supporting both
source kinds adds mutual-exclusion and syntax validation but avoids forcing non-secret
policy into a Secret or sensitive policy into a ConfigMap.

Rolling replacement avoids Cedar-specific downtime but cannot make policy adoption atomic.
Mutable-object updates can create mixed enforcement after an independent restart and during
the required rollout; versioned immutable source names make adoption visible in the pod
template but still converge through the normal rollout strategy.

`noop` remains available because evaluator parity is explicit, but it is a deliberate
no-enforcement posture and never a fallback. Permission configuration, Cedar adapter
entities, engine ports, tool schemas, session persistence, and event contracts do not
change.

## See also

- [Acceptance plan — mecak8s opt-in Cedar authority](../acceptance/mecak8s-optin-cedar.md)
- [ADR 0048 — mecak8s](./0048-mecak8s.md)
- [ADR 0204 — Caller identity](./0204-caller-identity-threading.md)
- [ADR 0212 — Caller ownership enforcement](./0212-caller-ownership-enforcement.md)
- [ADR 0234 — Authority evaluator port](./0234-authority-evaluator-port.md)
- [Architecture — authority evaluation](../architecture/agent-loop.md#authority-evaluation-at-execution)
