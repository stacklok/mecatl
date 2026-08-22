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
| **Operator** | [`../README.md`](../README.md) → [usage & install](usage/install.md) → [quickstart](usage/quickstart.md) → [running `mecated`](usage/mecated.md) → [usage guide](usage.md) |
| **Library consumer** | [Building on mecatl](https://github.com/stacklok/mecatl/blob/main/user-docs/building/index.md) → [`engine/session`](../engine/session) → [extension points](https://github.com/stacklok/mecatl/blob/main/user-docs/building/extension-points/index.md) → [`engine/COMPATIBILITY.md`](../engine/COMPATIBILITY.md) |
| **API client developer** | [Drive via gRPC / HTTP](https://github.com/stacklok/mecatl/blob/main/user-docs/building/deployment/grpc-http.md) → [`contracts/proto/mecatl/v1/`](../contracts/proto/mecatl/v1) → [gRPC reference](usage/grpc-api.md) or [HTTP/SSE reference](usage/http-sse-api.md) |

## Nearby

- [`READING.md`](READING.md) — the full progressive reader map (contributor foundation spine, topic branches, and operator/library routes).
- [`architecture.md`](architecture.md) — the living architecture reference.
- [`usage.md`](usage.md) — the living usage & operator guide.
- [`adr/README.md`](adr/README.md) — the frozen ADR index (the *why* archive).
- [`design/PRODUCTION-READINESS.md`](design/PRODUCTION-READINESS.md) — the live shipped/deferred status tracker.
- [ADR 0215](adr/0215-openai-subscription-manual-token.md) — the landed,
  experimental `openai-codex` capability and its private-backend boundary.
