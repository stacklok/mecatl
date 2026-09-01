package app

import (
	"context"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// clientMCPReportFactory builds the REAL sessionEngineFactory — the production one
// Build wires — so these tests pin what composition actually reports rather than a
// stand-in.
func clientMCPReportFactory(t *testing.T) server.SessionEngineFactory {
	t.Helper()
	llm := mockllm.New(mockllm.TextTurn("ok"))
	reg := regForTest(llm, providerOpenAI, "gpt-5")
	return sessionEngineFactory(Config{Model: "gpt-5"}, reg, llm, memstore.New(),
		permpolicy.NewPolicy(defaultRules(), nil), hookexec.New(nil), nil,
		prompt.RootAssembler{}, catalogAssets{}, nil)
}

// deadLoopbackURL returns a loopback URL that is guaranteed to refuse a
// connection: an httptest server is started (so the port was really bound and is
// really ours) and then closed. No external network is touched and the dial fails
// immediately with ECONNREFUSED rather than waiting out mcp.ClientConnectTimeout.
func deadLoopbackURL(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(nil)
	url := srv.URL
	srv.Close()
	return url
}

// TestSessionEngineFactoryReportsOnlyConnectedClientMCP pins the COMPOSITION half
// of the all-or-nothing wire contract (review finding on PR #903).
//
// The Service refuses a wire create whose requested MCP servers did not all mount,
// and it decides that from SessionEngineResult.MountedClientMCP. That check is only
// as good as this factory's honesty, and the server-side tests necessarily use a
// stand-in factory — so without this test, nothing would catch the production
// factory reporting the REQUESTED set (which would restore the silent partial
// mount) or reporting nothing at all.
//
// Offline: both MCP servers are in-process. The unreachable one is a closed
// loopback listener, so "unreachable" is a real ECONNREFUSED dial, not a stub.
func TestSessionEngineFactoryReportsOnlyConnectedClientMCP(t *testing.T) {
	liveURL := newMCPTestServer(t)
	deadURL := deadLoopbackURL(t)

	t.Run("partial: only the connected server is reported", func(t *testing.T) {
		factory := clientMCPReportFactory(t)
		res, err := factory(context.Background(), server.ProviderSelector{}, []mcp.ServerConfig{
			{Name: "live", URL: liveURL},
			{Name: "dead", URL: deadURL},
		}, server.ProfileDefault, "", session.ModeDefault)
		if err != nil {
			t.Fatalf("factory: %v", err)
		}
		defer func() { _ = res.Close() }()

		if !slices.Contains(res.MountedClientMCP, "live") {
			t.Fatalf("MountedClientMCP = %v, want it to contain the server that DID connect", res.MountedClientMCP)
		}
		if slices.Contains(res.MountedClientMCP, "dead") {
			t.Fatalf("MountedClientMCP = %v: it reports a server that never connected, which is what makes a partial mount silent", res.MountedClientMCP)
		}
		// And the mounted set must agree with the tools actually registered — the
		// report is not allowed to drift from the catalog it describes.
		if !res.Engine.HasTool("mcp__live__echo") {
			t.Fatal("the reachable server's tool is missing: the report claims a mount the catalog does not have")
		}
	})

	t.Run("all connected: every requested name is reported", func(t *testing.T) {
		factory := clientMCPReportFactory(t)
		res, err := factory(context.Background(), server.ProviderSelector{}, []mcp.ServerConfig{
			{Name: "live", URL: liveURL},
		}, server.ProfileDefault, "", session.ModeDefault)
		if err != nil {
			t.Fatalf("factory: %v", err)
		}
		defer func() { _ = res.Close() }()

		if !slices.Contains(res.MountedClientMCP, "live") {
			t.Fatalf("MountedClientMCP = %v, want [live]: a fully-successful mount must report itself, or the wire path refuses every create", res.MountedClientMCP)
		}
	})

	t.Run("none requested: nothing reported", func(t *testing.T) {
		factory := clientMCPReportFactory(t)
		res, err := factory(context.Background(), server.ProviderSelector{ProviderID: providerOpenAI}, nil,
			server.ProfileDefault, "", session.ModeDefault)
		if err != nil {
			t.Fatalf("factory: %v", err)
		}
		defer func() { _ = res.Close() }()

		if len(res.MountedClientMCP) != 0 {
			t.Fatalf("MountedClientMCP = %v, want empty for a selector-only session", res.MountedClientMCP)
		}
	})

	t.Run("all unreachable: nothing reported", func(t *testing.T) {
		// Composition still returns a usable core-only engine here (the ACP
		// contract). Reporting an empty set is what lets the Service turn that into
		// a loud wire refusal instead of a silently toolless session.
		factory := clientMCPReportFactory(t)
		res, err := factory(context.Background(), server.ProviderSelector{}, []mcp.ServerConfig{
			{Name: "dead", URL: deadURL},
		}, server.ProfileDefault, "", session.ModeDefault)
		if err != nil {
			t.Fatalf("factory must still build a core-only engine when every client server fails: %v", err)
		}
		defer func() { _ = res.Close() }()

		if len(res.MountedClientMCP) != 0 {
			t.Fatalf("MountedClientMCP = %v, want empty when nothing connected", res.MountedClientMCP)
		}
	})
}
