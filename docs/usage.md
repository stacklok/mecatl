# Usage documentation moved

The public [Mecatl documentation](../user-docs/intro.md) is the canonical
source for user-facing guides and reference material. This file remains only so
historical ADR links resolve.

See [ADR 0321](./adr/0321-canonical-user-documentation-ownership.md) for the ownership decision.

| Legacy topic | Canonical page |
| --- | --- |
| Installation | [Install Mecatl](../user-docs/install.md) |
| Offline demo | [See Mecatl in 60 seconds](../user-docs/building/getting-started/demo.md) |
| `mecated` operation | [Run mecated standalone](../user-docs/building/deployment/mecated.md) |
| Guardrails and permissions | [Permissions and posture](../user-docs/features/permissions-and-posture.md) |
| Models and providers | [Choose models and providers](../user-docs/features/choose-models.md) |
| Workspace trust | [Permissions and posture](../user-docs/features/permissions-and-posture.md#project-trust) |
| Skills, commands, soul, and user model | [Skills, commands, and soul](../user-docs/features/skills-commands-and-soul.md) |
| `mecak8s` | [Cloud-native k8s with mecak8s](../user-docs/building/deployment/mecak8s.md) |
| gRPC API | [gRPC API reference](../user-docs/reference/grpc-api.md) |
| HTTP and SSE API | [HTTP and SSE API reference](../user-docs/reference/http-sse-api.md) |
| Configuration | [Configure Mecatl](../user-docs/building/deployment/settings.md) |
| Hooks | [Hook system](../user-docs/building/what-you-get/hooks.md) |
| `mecatequi` CI | [Single-shot CI with mecatequi](../user-docs/building/deployment/mecatequi.md) |
| Troubleshooting | [Mecatl documentation](../user-docs/intro.md) |

## Plan approval

See [Permissions and guardrails](../user-docs/building/what-you-get/permissions.md#plan-mode).

## Provider login and ToolHive

Operator-defined providers live under the operator-tier `providers` settings, with
shared OIDC custody under `credential_store.oidc`. Embedded local mecatui manages
providers and their locally owned credentials with:

```sh
mecatui providers
mecatui providers login PROVIDER
mecatui providers logout PROVIDER
```

Status is passive and local. An empty configuration or unknown provider reports an
actionable setup/status remedy; it never guesses a default, hostname, or model.
Enrollment is signal-aware and bounded to one five-minute overall browser/callback
operation while the lifecycle lock is held. Success goes to stderr and no access or
refresh token is printed. All callers admitted to the same standalone deployment
share the configured provider's dedicated deployment/service gateway identity, quota,
gateway-side audit/retention posture, and model inventory. Use separate deployments
for mutually untrusted or per-user upstream authorization.

`mecatui login ADDRESS` instead authenticates mecatui to a remote mecated. ToolHive
MCP discovery, ToolHive LLM setup, and manual Codex authentication are also separate.
Use `thv llm` tooling for ToolHive's externally owned credential lifecycle; Mecatl
does not print or copy a ToolHive token.
`thv llm token` tooling.

See [Run mecated standalone](../user-docs/building/deployment/mecated.md#a-toolhive-managed-llm-gateway-no-api-key-needed).
