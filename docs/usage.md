# Usage documentation moved

The public [Mecatl documentation](https://mecatl.dev/docs/) is the canonical
source for user-facing guides and reference material. This file remains only so
historical ADR links resolve.

See [ADR 0321](./adr/0321-canonical-user-documentation-ownership.md) for the ownership decision.

| Legacy topic | Canonical page |
| --- | --- |
| Installation | [Install Mecatl](https://mecatl.dev/docs/install) |
| Offline demo | [See Mecatl in 60 seconds](https://mecatl.dev/docs/building/getting-started/demo) |
| `mecated` operation | [Run mecated standalone](https://mecatl.dev/docs/building/deployment/mecated) |
| Guardrails and permissions | [Permissions and posture](https://mecatl.dev/docs/features/permissions-and-posture) |
| Models and providers | [Choose models and providers](https://mecatl.dev/docs/features/choose-models) |
| Workspace trust | [Permissions and posture](https://mecatl.dev/docs/features/permissions-and-posture#project-trust) |
| Skills, commands, soul, and user model | [Skills, commands, and soul](https://mecatl.dev/docs/features/skills-commands-and-soul) |
| `mecak8s` | [Cloud-native k8s with mecak8s](https://mecatl.dev/docs/building/deployment/mecak8s) |
| gRPC API | [gRPC API reference](https://mecatl.dev/docs/reference/grpc-api) |
| HTTP and SSE API | [HTTP and SSE API reference](https://mecatl.dev/docs/reference/http-sse-api) |
| Configuration | [Configure Mecatl](https://mecatl.dev/docs/building/deployment/settings) |
| Hooks | [Hook system](https://mecatl.dev/docs/building/what-you-get/hooks) |
| `mecatequi` CI | [Single-shot CI with mecatequi](https://mecatl.dev/docs/building/deployment/mecatequi) |
| Troubleshooting | [Mecatl documentation](https://mecatl.dev/docs/) |

## Plan approval

See [Permissions and guardrails](https://mecatl.dev/docs/building/what-you-get/permissions#plan-mode).

## LLM endpoint login and ToolHive

Native organizational LLM endpoints are configured only in the operator-tier
`llm.endpoints` settings. Embedded local mecatui manages their local credential with:

```sh
mecatui llm status [ENDPOINT]
mecatui llm login ENDPOINT
mecatui llm logout ENDPOINT
```

Status is passive and local. An empty configuration or unknown endpoint reports an
actionable settings/status remedy; it never guesses a default, hostname, or model.
Enrollment is signal-aware and bounded to one five-minute overall browser/callback
operation while the lifecycle lock is held. Success goes to stderr and no access or
refresh token is printed. All callers admitted to the same standalone deployment
share the configured endpoint's dedicated deployment/service gateway identity,
quota, gateway-side audit/retention posture, and model inventory. Use separate
deployments for mutually untrusted or per-user upstream authorization.

`mecatui login ADDRESS` instead authenticates mecatui to a remote mecated. ToolHive
MCP discovery and manual Codex authentication are also separate. For the ToolHive
LLM gateway, use `mecatui llm login toolhive`; the one-release bare
`mecatui llm login` alias warns and remains ToolHive-only. Mecatl no longer prints a
ToolHive token. Scripts that need token stdout must call ToolHive's explicit
`thv llm token` tooling.

See [Run mecated standalone](https://mecatl.dev/docs/building/deployment/mecated#the-toolhive-llm-gateway-no-api-key-needed).
