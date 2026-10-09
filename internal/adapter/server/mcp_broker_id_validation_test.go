package server

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	brokercontract "github.com/stacklok/mecatl/internal/mcpbroker"
)

type brokerIDPlacement struct {
	brokerPlacementProvider
	calls int
}

func (p *brokerIDPlacement) Bind(ctx context.Context, req PlacementBindRequest) (PlacementBinding, error) {
	p.calls++
	return p.brokerPlacementProvider.Bind(ctx, req)
}

func TestCreateSessionWithBrokerInvalidIDRejected(t *testing.T) {
	for _, tc := range []struct {
		name     string
		id       session.SessionID
		explicit bool
		broker   bool
		wantErr  bool
	}{
		{name: "explicit control", id: "invalid\x00id", explicit: true, broker: true, wantErr: true},
		{name: "generated control", id: "invalid\x00id", broker: true, wantErr: true},
		{name: "explicit oversized", id: session.SessionID(strings.Repeat("a", brokercontract.MaxLogicalSessionIDBytes+1)), explicit: true, broker: true, wantErr: true},
		{name: "generated malformed UTF-8", id: "invalid\xffid", broker: true, wantErr: true},
		{name: "byte limit", id: session.SessionID(strings.Repeat("a", brokercontract.MaxLogicalSessionIDBytes)), explicit: true, broker: true},
		{name: "unicode byte limit", id: session.SessionID(strings.Repeat("é", brokercontract.MaxLogicalSessionIDBytes/2)), explicit: true, broker: true},
		{name: "brokerless explicit control", id: "invalid\x00id", explicit: true},
		{name: "brokerless generated control", id: "invalid\x00id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			placement := &brokerIDPlacement{}
			store := memstore.New()
			factoryCalls := 0
			cfg := Config{
				Engine: brokerEngineResult().Engine, Store: store,
				NewID:             func() session.SessionID { return tc.id },
				PlacementProvider: placement, PlacementScope: "test",
				SessionEngine: func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode) (SessionEngineResult, error) {
					factoryCalls++
					return brokerEngineResult(), nil
				},
				SessionEngineWithTools: func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode, []tool.Tool) (SessionEngineResult, error) {
					factoryCalls++
					return brokerEngineResult(), nil
				},
			}
			if tc.broker {
				runtime := testBrokerRuntime(t)
				t.Cleanup(func() { _ = runtime.Close() })
				cfg.MCPBroker = runtime
			}
			svc, err := NewService(cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(svc.Close)
			var options []CreateSessionOption
			if tc.explicit {
				options = append(options, WithSessionID(tc.id))
			}
			sess, err := svc.CreateSessionWithProfile(t.Context(), session.ModeDefault, session.Limits{}, ProviderSelector{}, ProfileDefault, options...)
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidArgument) || sess != nil {
					t.Fatalf("invalid broker ID: session=%v err=%v", sess, err)
				}
				stored, listErr := store.List(t.Context())
				if listErr != nil {
					t.Fatal(listErr)
				}
				if placement.calls != 0 || factoryCalls != 0 || len(stored) != 0 {
					t.Fatalf("invalid ID caused side effects: placements=%d factories=%d sessions=%d", placement.calls, factoryCalls, len(stored))
				}
				return
			}
			if err != nil || sess == nil || sess.ID != tc.id {
				t.Fatalf("admissible session ID rejected: session=%v err=%v", sess, err)
			}
		})
	}
}
