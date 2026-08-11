package app

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain installs a goroutine-leak gate over the composition layer. The
// composition-OWNED background goroutine this gate exists to protect is the
// live-model refresh (startLiveModelRefresh): its returned closer (cancel +
// wg.Wait) is folded into Build's closeAll, so a test that defers built.Close()
// must leave nothing behind. The live-model tests (TestAsyncRefresh*, TestLive*,
// TestBuildAsyncSwap) pass this gate with NO ignore entry — a leaked refresh
// goroutine (a missing Close, a swap that ignores cancellation) would fail here.
//
// The two IgnoreTopFunction entries below are NOT live-model code — they are
// pre-existing, slow-to-drain goroutines from the composition's full-Build e2e
// tests (which wire MCP + run the agent loop), surfaced only now that this package
// is gated at all:
//
//   - the modelcontextprotocol go-sdk streamable connection READER goroutine: a
//     THIRD-PARTY library goroutine spawned per MCP connection; it unwinds on
//     connection close but asynchronously, past goleak's retry budget when it is
//     the last test's teardown. Not mecatl-owned, not closable faster from here.
//
//   - the agent askRegistry.await goroutine: an e2e run paused on a permission ask;
//     it exits on approve/cancel/run-completion, again draining just past the
//     retry window in aggregate. Owned + gated cleanly by engine/agent's own
//     goleak suite (which exercises it deterministically); here it is only e2e
//     teardown noise.
//
//   - the godbus/dbus goroutines: when a test exercises the direct-mode ToolHive
//     entry (newDirectGatewayEntry → DirectTokenSource), secrets.GetSystemSecretsProvider
//     opens a D-Bus connection to reach the system keyring. On a host without
//     a real D-Bus session (CI/build), the connection goroutine drains past
//     goleak's retry budget. Not mecatl-owned; the real code path owns it
//     correctly (the connection is process-scoped and cleaned at exit).
//
// All are narrowly pinned by top-of-stack so the gate still catches ANY other
// leak — including, crucially, a live-model refresh leak.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreTopFunction("github.com/modelcontextprotocol/go-sdk/mcp.(*streamableServerConn).Read"),
		goleak.IgnoreTopFunction("github.com/stacklok/mecatl/engine/agent.(*askRegistry).await"),
		goleak.IgnoreTopFunction("github.com/godbus/dbus/v5.newConn.func1"),
		goleak.IgnoreAnyFunction("github.com/godbus/dbus/v5.(*Conn).inWorker"),
	)
}
