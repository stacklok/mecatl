# ADR 0254 — Dedicated session debugger and per-instance admin transport

- Status: Accepted
- Date: 2026-08-30
- Scope: stored-session diagnosis and the embedded mecatui performance-admin listener
- Supersedes: ADR 0018 decision 7's fixed `127.0.0.1:9099` default only
- Superseded by: ADR 0284 (decision paragraph 3's `DEBUG target #<digest>` presentation clause only)

## Context

Diagnosing a stored session requires model assistance over sensitive transcript and event evidence, but attaching an ordinary chat to the target would let the diagnostic run resume, lease, mutate, or otherwise alter the subject it is meant to inspect. Accepting a target ID from the model would also turn authorization into prompt-controlled routing. The debugger therefore needs a durable identity and authority boundary of its own.

Embedded performance administration had a related boundary problem. ADR 0018 chose a fixed loopback port so a perf MCP client could use a stable URL. That made unrelated mecatui instances contend for one process-global address and exposed sensitive, unauthenticated runtime endpoints more broadly than a local in-process client requires. Streaming-HTTP MCP still needs an HTTP URL and cannot use the private UNIX-socket path directly; stdio MCP is prohibited.

## Decision

Create debugging as a separate durable `debug` session whose trusted relationship metadata binds exactly one target. The server authorizes that target before creation while concealing ownership failures as not-found, and the debug engine receives the target from trusted construction rather than tool arguments. It uses the no-filesystem profile, an empty workspace, a trusted stable-prefix debugging contract, and exactly one read-only tool: `InspectSession`.

`InspectSession` exposes bounded status and a projection of the authoritative snapshot transcript, including model-visible typed parts. Textual/structured parts are bounded with explicit truncation metadata; binary/media bytes are omitted with safe metadata and an explicit marker. Pagination scan completion is distinct from projection completeness. Tool arguments preserve valid JSON number tokens without decoding through floating point, while malformed JSON or UTF-8 is explicitly omitted. Optional EventLog-derived activity and performance views are never authoritative or evidentially complete; performance may separately report that the available log was scanned to EOF. Every evidence response is fenced as untrusted. A debug run never resumes, mutates, approves, cancels, steers, or leases its target. Persist the debug kind and target lineage so restart rehydration rebuilds the same narrow engine; invalid metadata, an absent factory, or an unavailable target fails closed rather than falling back to the shared engine.

Treat `mecatui debug SESSION_ID` and `mecatui connect ADDRESS debug SESSION_ID` as explicit consent to send the target's stored diagnostic evidence to the selected model. Print a privacy disclosure before entering the TUI. Before the first model call, submit one genuine user turn containing the existing sanitized `/diagnostics` client/server report followed by the default diagnosis request; a custom `--prompt` replaces only that request. Remote server-info lookup uses the authenticated client path, classifies failures without raw errors, and never blocks diagnosis. The baseline is labelled as current debugger runtime state, not target evidence; `InspectSession` remains the only source of target evidence and the only debug model tool. Put amber/bold `DEBUG target #<digest>` immediately after `mecatui` in the ordinary padded header, preserve that complete identity ahead of model/mode/server details at narrow widths, expose the safely quoted exact target ID and a copy key in `/session`, retain the target-derived terminal title, and suppress controls that could break the immutable binding.

For embedded `mecatui --perf`, replace the fixed TCP default with an owner-private `admin.sock` beside that instance's private gRPC socket. An explicit `--perf-addr` selects TCP and is accepted only on loopback. When `--perf-mcp` is enabled without an explicit address, use ephemeral loopback TCP because the supported MCP transport is streaming HTTP and requires a URL. Never use stdio. Perf admin data remains an operator surface and is not added to the debug engine or model context.

## Consequences

Multiple embedded mecatui instances no longer collide on a fixed admin port, and ordinary local perf endpoints inherit the private runtime directory's ownership and cleanup. MCP clients must discover the logged ephemeral URL when no address is explicit; operators needing a stable URL may choose an explicit loopback port.

The debugger can reason over raw stored conversation content, including secrets, so invocation is a consequential disclosure even though the target cannot be mutated. Event-log-derived views may be absent or incomplete and must never override the snapshot transcript. Enhanced structured diagnostics, sanitized network-attempt timing, live target following, raw audit/tool records, and support-bundle export remain follow-up work.

## See also

- [Architecture overview](../architecture.md)
- [Observability, persistence and reliability](../architecture/observability.md)
- [TUI reference](../tui.md)
- [Production readiness tracker](../design/PRODUCTION-READINESS.md)
- [ADR 0018](./0018-perf-observability.md)
- [ADR 0027](./0027-cloud-native.md)
