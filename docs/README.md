# mecatl documentation

This is the documentation root — a short front door that routes each audience to
the right living guide. The full progressive reading map is in
[`READING.md`](READING.md) — start there.

The coding-agent contract lives in [`../AGENTS.md`](../AGENTS.md); this page does
not restate it.

## Audience routes

| Audience | Route |
| --- | --- |
| **Contributor / agent** | [`READING.md`](READING.md) → foundation spine (architecture → domain → ports → loop) → topic branches |
| **Operator** | [`../README.md`](../README.md) → [public documentation](../user-docs/intro.md) → [run `mecated`](../user-docs/building/deployment/mecated.md) |
| **Library consumer** | [Building on mecatl](../user-docs/building/index.md) → [`engine/session`](../engine/session) → [extension points](../user-docs/building/extension-points/index.md) → [`engine/COMPATIBILITY.md`](../engine/COMPATIBILITY.md) |
| **API client developer** | [Drive via gRPC / HTTP](../user-docs/building/deployment/grpc-http.md) → [`contracts/proto/mecatl/v1/`](../contracts/proto/mecatl/v1) → [gRPC reference](../user-docs/reference/grpc-api.md) or [HTTP/SSE reference](../user-docs/reference/http-sse-api.md) |

## Nearby

- [`READING.md`](READING.md) — the full progressive reader map (contributor foundation spine, topic branches, and operator/library routes).
- [`architecture.md`](architecture.md) — the living architecture reference.
- [Public documentation](../user-docs/intro.md) — canonical user-facing guidance and reference.
- [`adr/README.md`](adr/README.md) — the frozen ADR index (the *why* archive).
- [ADR 0215](adr/0215-openai-subscription-manual-token.md) — the landed,
  experimental `openai-codex` capability and its private-backend boundary.
- [Agent Fabric Protocol](agent-fabric-protocol.md) — a draft, MCP-adjacent
  protocol for remote access to files, folders, and callable actions over
  HTTP; not yet implemented in mecatl.
