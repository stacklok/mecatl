// Package acp is the Agent Client Protocol (ACP) adapter: it lets an ACP editor
// (Zed, and others that speak ACP) drive the mecatl harness as a subprocess over
// stdio. It is a THIRD wire format alongside gRPC and HTTP/SSE — its own JSON,
// NOT the proto contract — projecting the same domain session.Events onto the
// ACP session/update surface.
//
// ACP is JSON-RPC 2.0 over stdio: the editor spawns the agent and the two speak
// over the agent's stdin/stdout. The adapter is therefore BOTH a JSON-RPC server
// (it handles inbound initialize / session/new / session/prompt requests and the
// session/cancel notification) AND a JSON-RPC client (it issues the outbound
// session/request_permission request and correlates the reply, and it pushes
// session/update notifications). The bidirectional codec lives in conn.go.
//
// Layering: this is an ADAPTER. It consumes the surface-agnostic
// *server.Service (CreateSession / StartRun / Approve / Cancel) and the domain
// session types, exactly as the gRPC/HTTP adapters do. It is wired only in the
// composition root (cmd/mecated, behind the `acp` subcommand). It never imports contracts/gen:
// ACP carries its own JSON, decoupled from the proto.
//
// fs/* DELEGATION (issue #2) — when the CLIENT advertises BOTH fs.readTextFile and
// fs.writeTextFile at initialize, a per-session workspace (fsworkspace.go) routes
// file Read/Write through the editor's buffers (fs/read_text_file /
// fs/write_text_file) instead of disk; Stat/Glob/Grep are composed from a local
// osfs view at the same root, and the Edit read-ledger is synthesized buffer-keyed
// over the delegated reads. When the caps are absent (or on session/load) it falls
// back to the osfs workspace rooted at the session cwd. See the ADR for the bounded
// hybrid's residual (grep sees disk) and the load asymmetry.
//
// SCOPE — the following are DEFERRED to later phases and documented in
// docs/adr/0001-acp-adapter.md (Phase 2/3 landed diff blocks, allow_always rule
// learning, session/load + replay, modes, and slash commands — see the ADR):
//   - grep/glob over editor BUFFERS (the fs/* hybrid searches disk) and fs/*
//     delegation on session/load (a resumed session uses osfs).
//   - DURABLE / broader-granularity learned permissions (today: in-memory,
//     per-session, tool + exact-pattern only).
//   - full-fidelity projection of turn.*/compaction events (dropped or folded
//     into a thought/message chunk).
//
// MULTIMODAL PROMPT CONTENT (issue #5) is DONE: session/prompt content blocks are
// translated by buildPromptContent into the prompt's flattened text PLUS media
// Parts (image/audio), reusing the session.NewContent validating constructors and
// per-prompt size caps at the ACP boundary. promptCapabilities now reflect the
// configured provider (ProviderCapabilities seam): image when the provider
// supports it, embeddedContext (inline-text resources flatten to text), audio
// wired-but-provider-gated (OpenAI Responses has no audio input member, so
// advertised false). A resource_link, an unsupported block type, or a media part
// the provider cannot consume is REJECTED loudly — never silently dropped.
//
// PLAN APPROVAL (issue #206, Wave 4) — the ACP adapter has NO bespoke
// ApprovePlan method. ACP already composes the plan-approval flow from the two
// EXISTING primitives the editor speaks natively: session/set_mode (the operator
// picks default / accept-edits / plan) + session/prompt (the proceed message).
// The native gRPC ApprovePlan RPC and HTTP POST /v1/sessions/{id}/plan:approve
// are the headless COMPOSITION of those same two steps (resume the parked plan
// ask, flip the mode, start the continuation run) into one streamed response — a
// convenience an ACP editor does NOT need because it drives each step itself over
// its own session/* surface. A presented-plan permission.ask over ACP is resolved
// by the editor's existing session/request_permission reply, exactly as any other
// permission ask is; the subsequent mode flip + continuation prompt are ordinary
// session/set_mode + session/prompt calls. No new ACP method, no new capability.
package acp
