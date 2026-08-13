# ADR 0105 — Built-in bounded WebFetch

- Status: Accepted
- Date: 2026-08-13
- Scope: Built-in web retrieval, engine reference adapters, outbound-network safety
- Supersedes: None
- Superseded by: None

## Context

`WebSearch` can discover sources, but reading one still required an MCP server or a shell command. The catalog carried a `WebFetch` stub so the model knew the intended workflow, but every call failed. Making that tool real introduces a direct outbound-network trust boundary: the model chooses a URL, DNS and redirects can point somewhere different, compressed responses can expand, and visible page text can contain prompt injection.

Other agent tools set useful bounds, but their public implementations do not consistently close redirect SSRF or DNS-rebinding paths. We need the built-in to be safe without relying on deployment-specific egress controls.

## Decision

Ship `WebFetch` as the `engine/adapter/webfetch` reference adapter and re-export it from `internal/adapter/tools`. Keep the model contract to one absolute HTTP(S) URL and GET only. Do not accept caller-supplied headers, credentials, cookies, request bodies, or proxy settings.

Resolve and validate every destination before dialing. Reject the whole target when any DNS answer is private, loopback, link-local, metadata, multicast, documentation, benchmarking, reserved, or otherwise non-public. Dial only the validated addresses while retaining the original hostname for HTTP and TLS. Follow only standard GET redirects, at most five, and repeat the full URL, DNS, and address checks at every hop.

Cap URLs at 8 KiB, the total call at 15 seconds, raw and decompressed bodies at 5 MiB each, and model-visible output at 25,000 bytes. Accept a narrow textual MIME set. Parse HTML with `golang.org/x/net/html`, without rendering, script execution, or subresource loading. This adds `x/net/html` to the engine module's small dependency closure; a handwritten HTML parser is not an acceptable substitute for hostile input.

Run fetched text through the existing `agent.FenceUntrusted` choke point. This is the same narrow adapter-to-application dependency already used by `engine/adapter/search`; duplicating framing-neutralisation in each network adapter would create a security drift point.

## Consequences

Engine consumers get a working reference fetch tool without an MCP dependency. Default and no-filesystem sessions keep the same catalog name and permission posture, but the tool now performs network I/O.

Private or unusual network targets, binary documents, authenticated pages, non-UTF-8 charset conversion, JavaScript-rendered sites, and responses above the fixed bounds are intentionally unsupported. Operators can still enforce stricter egress outside the process or override the existing `WebFetch` permission rule.

The engine module gains one Go-maintained dependency. The fetch transport is call-scoped: it starts no goroutine, retains no connection pool after the call, and adds no restart-sensitive resource to the cloud-native inventory.

## See also

- [Architecture guide](../architecture.md)
- [ADR 0036: engine module](./0036-engine-module.md)
- [ADR 0021: guardrails](./0021-guardrails.md)
