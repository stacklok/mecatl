// Package port defines the interfaces the agent loop consumes — the ports of the
// hexagonal architecture. Adapters implement these and depend inward on the
// domain; the loop targets only these interfaces. Each port has a fake in
// internal/adapter so the loop is unit-testable with no network and no disk.
//
// Allowed imports (ARCHITECTURE.md §3): the standard library (context, io, time,
// iter, encoding/json) and the domain packages (session, tool, prompt,
// governance). Nothing else — no adapter, agent, api, os, or third-party import.
//
// Note (cycle resolution): FileSystem, Workspace, and Environment are intentionally
// NOT defined here. They live in engine/tool, the context that owns them, because
// port already imports tool (LLMRequest.Tools is []tool.ToolSpec) and Tool.Execute
// takes an Environment — defining it here would create a port↔tool cycle.
package port
