// Package mcpbrokerserver owns the process-local singleton MCP broker network boundary.
// Workload-JWT-authenticated gRPC and deliberately unauthenticated opaque-state
// callbacks share one admission gate. Lifecycle shutdown drains that gate before
// closing the RPC adapter, verifier, and broker runtime in order.
package mcpbrokerserver
