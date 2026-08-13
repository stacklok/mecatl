// Package tool is the Tooling bounded context: the Tool interface, the ToolSpec
// the model sees, the Catalog (name→Tool registry with plan-mode filtering), and
// the filesystem seams (FileSystem, Workspace, and Environment) that every tool
// executes against.
//
// FileSystem and Workspace live here, not in engine/port. ARCHITECTURE.md §2
// names the Tooling context as their owner, and placing them here breaks the
// import cycle that would otherwise form: port references tool (LLMRequest.Tools
// is []tool.ToolSpec) and Tool.Execute takes an Environment, so if Environment lived
// in port, port and tool would import each other.
//
// Allowed imports (ARCHITECTURE.md §3): the standard library and the session
// domain package only. It MUST NOT import port, adapter, agent, api, os, the
// OpenAI SDK, or any third-party library.
package tool
