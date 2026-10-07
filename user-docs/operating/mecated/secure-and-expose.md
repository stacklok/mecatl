---
title: Secure and expose mecated
description: Authenticate callers and encrypt access to a standalone Mecatl server.
sidebar_position: 1
---

# Secure and expose mecated

Secure the listeners before making this server reachable beyond loopback.

## The trust model

Mecatl exposes command and file execution. Both API listeners therefore bind to
loopback by default. Before binding to another interface:

- authenticate callers with `--auth-token`, OIDC, mTLS, or an operator-managed
  private edge;
- encrypt traffic with `--tls-cert` and `--tls-key`, unless the edge terminates
  TLS; and
- keep the unauthenticated admin listener on loopback.

Add `--client-ca` to require client certificates. OIDC records distinct caller
identities; a shared bearer token does not. Authentication does not isolate a
shared workspace. A non-loopback API with no authentication is allowed for
service-mesh deployments but produces a startup warning.

## Caller identity (OIDC)

`--oidc-issuer` names the identity provider whose tokens identify your callers.
Every request must then present a bearer token, and `--oidc-audience` is
required. Mecatl records the verified `(issuer, subject)` pair as the owner of
sessions, schedules, teams, and memory records. This ownership does not isolate
a shared workspace; separate working files at the deployment layer.

Startup fails when the initial signing-key fetch fails. Afterward, cached keys
remain valid for `--oidc-max-jwks-staleness` (default `1h`). A failed refresh
after that bound returns 503; a rejected token returns 401. Setting the bound to
`0` accepts cached keys regardless of age.

With `--rate-limit`, rejected tokens use a separate bucket keyed by the direct
peer IP and return 429 when exhausted. Mecatl does not trust forwarded-IP
headers for this check. Authentication logs omit credentials, tokens, claims,
and caller identifiers. See
[Caller identity and OIDC](/features/security-and-execution/caller-identity.md)
for the full behavior.

## Posture

|Flag|Notes|
|-|-|
|`--posture strict`|Default approval posture; configured permission rules apply|
|`--posture trusted`|Honor a project's ALLOW rules (alias: `--trust-project`)|
|`--posture auto`|Allow unattended calls within configured rules; retain child injection defense|
|`--posture yolo`|Also disable child injection defense. Isolated single-tenant only. Refused as root without `MECATL_SANDBOX=1`|

On a **headless** root (`--headless`), posture never raises `TrustProject`.
Explicit `--trust-project`, `trustedWorkspaces:`, or undrifted remembered trust
admits repository steering and the read-only child shell. Without a trust source,
`--posture auto` does not grant either access merely because the directory contains `.git`. See
[Permissions and posture](/features/security-and-execution/permissions-and-posture.md#project-trust)
for the trust sources and headless behavior.

See
[Permissions & guardrails](/features/security-and-execution/permissions-and-posture.md)
for the full rule engine. Posture is read from the operator-global
`settings.yaml` (`posture:` key) and out-ranked by the CLI flag when both are
set.

## Guardrails

|Flag|Default|Notes|
|-|-|-|
|`--guardrails-model`|`""` (off)|Model id or alias for the content checker. Configuring a model **enables** guardrails|
|`--guardrails`|`""`|Kill-switch only: pass `--guardrails=off` to force off regardless of model config|

The rule list and cost knobs live in the operator-global `settings.yaml`
(`guardrails:` subtree). A project-tier `guardrails:` block is ignored with a
WARN because a checked-in file cannot weaken an operator security check.
Checker outage is fail-closed by default; set `onCheckerDown: warn` only when
continue-with-warning is the intended deployment policy. The owner-authorized
coverage and transient detail APIs are gRPC-only; no HTTP paths are implied.

## Configure a Cedar authority policy

`mecated` uses the local authority evaluator by default. To apply an operator
Cedar policy, create a trusted static policy file and select it at startup:

```cedar
permit(principal, action, resource);
forbid(principal, action, resource)
when { resource.path like "/srv/project/vendor/*" };
```

```sh
mecated serve \
  --workspace /srv/project \
  --oidc-issuer https://idp.example.com \
  --oidc-audience mecatl \
  --tls-cert /etc/mecatl/server.crt \
  --tls-key /etc/mecatl/server.key \
  --authority-evaluator cedar \
  --cedar-authority-policy /etc/mecatl/authority.cedar
```

Adjust the example's path to the workspace's canonical absolute path. The broad
permit establishes the example baseline; the explicit forbid blocks access to
its vendor subtree. Review the policy for your deployment before using it.
Cedar reads the file once at startup and rejects missing, unreadable, or invalid
policies. Restart the server to load a changed policy.

Cedar requires a verified session owner. OIDC supplies that identity; a shared
bearer token or transport-only mTLS does not. Ownerless authority evaluation
fails closed. Authority checks complement configured permission rules; they do
not turn Ask or Deny into Allow. See [permissions and delegated authority](/features/security-and-execution/permissions-and-posture.md)
for the shared behavior. `mecak8s` does not expose these Cedar selection flags.

## Browsers and CORS

For local browser development, allow each origin explicitly:

```sh
mecated serve --http-addr 127.0.0.1:8081 \
  --cors-origins http://localhost:5173 \
  --cors-origins https://app.internal.example.com
```

Origins require an exact scheme, host, and port. Wildcards, paths, queries, and
fragments are rejected. Grant only origins you control because the HTTP API can
start agent runs.

:::warning[Local development only]

In production, place a same-origin backend in front of `mecated`. Keep the
bearer token on the server and enforce Origin and CSRF policy there.

:::

## Next steps

- [Configure providers and storage](/operating/mecated/configure-providers-and-storage.md).
- [Operate the instance](/operating/mecated/operate-instance.md).
