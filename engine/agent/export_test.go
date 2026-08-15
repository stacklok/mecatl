package agent

// This file is the Go export_test.go seam: it hands unexported identifiers to
// the external agent_test package without widening the shipped API surface
// (engine/api/agent.txt is generated from non-test files only).

// WithSessionOrigin exposes withSessionOrigin so the external tests can build an
// origin-bearing context directly, instead of only through Engine.Run. Kept
// test-only on purpose — see the withSessionOrigin doc-comment (ADR 0209).
var WithSessionOrigin = withSessionOrigin
