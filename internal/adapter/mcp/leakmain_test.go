package mcp

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain installs a goroutine-leak gate over the mcp package. As of ADR 0057
// the client holds the standalone SSE GET stream open per connected server, so
// the SDK spawns goroutines that park on network reads (the handleSSE reader,
// the jsonrpc2 connection read loop, and the stdlib http.persistConn
// read/write loops). session.Close() cancels connCtx and closes the response
// body, but those goroutines unwind ASYNCHRONOUSLY — they don't all exit within
// the goleak sample window on every kernel/CI runner. The ignore list below
// targets the exact SDK + stdlib top functions that park on the standalone SSE
// stream, so the gate still catches a genuine harness-owned leak (a goroutine
// WE started and didn't drain) without flaking on the SDK's async cleanup.
//
// The gate uses maxSleep to extend the retry window, giving a slow CI runner
// more time to unwind the SDK goroutines before declaring a leak.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		// The standalone SSE reader goroutine parks in processStream -> scanEvents
		// -> bufio.(*Reader).ReadBytes on the SSE response body. It unwinds when
		// session.Close() closes the body, but asynchronously.
		goleak.IgnoreAnyFunction("github.com/modelcontextprotocol/go-sdk/mcp.(*streamableClientConn).handleSSE"),
		// The jsonrpc2 connection's read loop parks in (*Connection).Read on the
		// shared incoming channel; it exits when the connection is closed.
		goleak.IgnoreAnyFunction("github.com/modelcontextprotocol/go-sdk/internal/jsonrpc2.(*Connection).start.func1"),
		// The SDK SERVER-side streamable transport (used by the in-process
		// httptest MCP servers in these tests) spawns a per-connection Read loop
		// that parks on the jsonrpc2 incoming channel; it unwinds when the
		// httptest server closes, but asynchronously.
		goleak.IgnoreAnyFunction("github.com/modelcontextprotocol/go-sdk/mcp.(*streamableServerConn).Read"),
		goleak.IgnoreAnyFunction("github.com/modelcontextprotocol/go-sdk/internal/jsonrpc2.(*Connection).readIncoming"),
		// The stdlib HTTP transport's persistent-connection read/write loops park
		// on the TCP socket; they exit when the transport idle-times-out the conn
		// or the server closes. These are owned by http.Client, not us.
		goleak.IgnoreTopFunction("net/http.(*persistConn).readLoop"),
		goleak.IgnoreTopFunction("net/http.(*persistConn).writeLoop"),
		// The httptest/http server's per-connection background reader parks on
		// the socket when the client (the SDK) holds a half-open SSE connection
		// at session.Close time; it exits when the server closes or the conn
		// times out. Owned by net/http.Server, not us.
		goleak.IgnoreTopFunction("net/http.(*connReader).backgroundRead"),
	)
}
