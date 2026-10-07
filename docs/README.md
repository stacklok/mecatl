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
| **Operator** | [`../README.md`](../README.md) → [public documentation](https://mecatl.dev/docs/) → [run `mecated`](https://mecatl.dev/docs/operating/mecated) |
| **Library consumer** | [Building on mecatl](https://github.com/stacklok/mecatl/blob/main/user-docs/building/index.md) → [`engine/session`](../engine/session) → [extension points](https://github.com/stacklok/mecatl/blob/main/user-docs/building/go/extension-points/index.md) → [`engine/COMPATIBILITY.md`](../engine/COMPATIBILITY.md) |
| **API client developer** | [Drive via gRPC / HTTP](https://mecatl.dev/docs/building/grpc-http) → [`contracts/proto/mecatl/v1/`](../contracts/proto/mecatl/v1) → [gRPC reference](https://mecatl.dev/docs/reference/grpc-api) or [HTTP/SSE reference](https://mecatl.dev/docs/reference/http-sse-api) |

## What goes where

Each kind of guidance has one home. Put a fact at the narrowest level that covers it.

| Home | Holds | Loaded |
| --- | --- | --- |
| Root [`AGENTS.md`](../AGENTS.md) | Commands, layout, and invariants for the whole repo | Every agent session |
| Module `AGENTS.md` | Where to change what in that module, its commands and invariants | When an agent works in that directory |
| `.claude/rules/*.md` | Invariants for a file pattern that spans directories, such as `*_test.go` | When an agent reads a matching file |
| `.claude/skills/` | Multi-step workflows, such as cutting a release | When invoked |
| `docs/` | How the system works inside, for contributors | When read |
| [`docs/proposals/`](proposals/README.md) | Designs not yet built, each with a status and owner; never current behavior | When read |
| [`user-docs/`](../user-docs/intro.md) | How to use Mecatl, published at mecatl.dev | When read |

Every `AGENTS.md` stays under 200 lines and has a `CLAUDE.md` symlink beside it; rules
stay at or under 25 lines. `task docs` checks these limits.

## Nearby

- [`READING.md`](READING.md) — the full progressive reader map (contributor foundation spine, topic branches, and operator/library routes).
- [`architecture.md`](architecture.md) — the living architecture reference.
- [Public documentation](../user-docs/intro.md) — canonical user-facing guidance and reference.
- [Proposals](proposals/README.md) — designs, not current behavior.
