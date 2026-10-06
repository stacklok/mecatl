package app

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcpauthority"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	"github.com/stacklok/mecatl/internal/adapter/mcpbrokergrpc"
	"github.com/stacklok/mecatl/internal/adapter/server"
	c "github.com/stacklok/mecatl/internal/mcpbroker"
)

type authoritySessionClient struct{ c.SessionService }

func (authoritySessionClient) ResumeToolWrapper(c.SessionRef, c.Catalogue, string, session.ToolCallID, c.AuthorizationRef) (tool.Tool, error) {
	return nil, errors.New("unexpected authorization in anonymous authority proof")
}

func TestBrokerPathRetirement_Scenario2_ComposedAuthority(t *testing.T) {
	ctx := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "test", Subject: "owner", GrantType: session.GrantTypeUser})
	db := miniredis.RunT(t)
	storage := redis.NewClient(&redis.Options{Addr: db.Addr()})
	defer storage.Close()
	process, err := mcpbroker.NewToolHiveProcess(ctx, mcpbroker.ToolHiveConfig{DeferAnonymousDiscovery: true, Profiles: []mcpbroker.ToolHiveProfile{{Name: "live", URL: newMCPTestServer(t), Auth: "none"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer process.Close()
	api, err := mcpbroker.NewSessionAPI(process, storage, func(context.Context) *session.Principal {
		return &session.Principal{Issuer: "test-workload", Subject: "host"}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	workspace, storeDir, userModels := t.TempDir(), filepath.Join(t.TempDir(), "sessions"), t.TempDir()
	config := Config{Workspace: workspace, StoreDir: storeDir, UserModelDir: userModels, NoSoul: true, NoUserModel: true, AllowAllTools: true, MCPAuthority: mcpauthority.NewBroker(mcpauthority.BrokerConfig{}), SessionBrokerFactory: func(context.Context) (mcpbrokergrpc.SessionHostClient, func() error, error) {
		return authoritySessionClient{api}, func() error { return nil }, nil
	}, MockProvider: mockllm.New(mockllm.TextTurn("done"))}
	built, err := buildIsolated(t, ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	fresh, err := built.Service.CreateSession(ctx, session.ModeDefault, defaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	base := fresh.Authority.Clone()
	if base.BrokerToolScope != nil || !base.CapabilitySet.AllowsTool("Read") {
		t.Fatalf("fresh root: %+v", base)
	}
	carried, err := built.Service.CreateSessionWithProfile(ctx, session.ModeDefault, defaultLimits(), server.ProviderSelector{}, server.ProfileDefault, server.WithSourceSession(fresh.ID))
	if err != nil {
		t.Fatal(err)
	}
	if carried.Authority.BrokerToolScope == nil || !reflect.DeepEqual(*carried.Authority.BrokerToolScope, base.CapabilitySet.Tools) {
		t.Fatal("real carried seam did not bound discovery")
	}
	freshRef, _ := fresh.BrokerAccess()
	carriedRef, _ := carried.BrokerAccess()
	if freshRef.Session == carriedRef.Session {
		t.Fatal("successor copied parent broker refs")
	}
	for _, id := range []session.SessionID{fresh.ID, carried.ID} {
		if _, err := built.Service.ConnectWorkspaceServices(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	assert := func(id session.SessionID, eligible, withdrawn bool) {
		t.Helper()
		got, err := built.Service.GetSession(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		a, _ := got.BrokerAccess()
		if !reflect.DeepEqual(a.IndependentTools, base.CapabilitySet.Tools) || a.Withdrawn != withdrawn {
			t.Fatalf("independent authority drift: %+v", a)
		}
		if got.Authority.CapabilitySet.AllowsTool("mcp__live__echo") != eligible || slices.Contains(a.BrokerTools, "mcp__live__echo") != eligible {
			t.Fatalf("broker projection: %+v %+v", got.Authority, a)
		}
		if id == carried.ID && got.Authority.BrokerToolScope == nil {
			t.Fatal("carried scope lost")
		}
	}
	assert(fresh.ID, true, false)
	assert(carried.ID, false, false)
	for _, id := range []session.SessionID{fresh.ID, carried.ID} {
		if err := built.Service.DisconnectWorkspaceServices(ctx, id); err != nil {
			t.Fatal(err)
		}
		assert(id, false, true)
		if _, err := built.Service.ConnectWorkspaceServices(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	assert(fresh.ID, true, false)
	assert(carried.ID, false, false)
	built.Close()
	restarted, err := buildIsolated(t, ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	built = restarted
	for _, id := range []session.SessionID{fresh.ID, carried.ID} {
		if _, err := built.Service.LoadSession(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	assert(fresh.ID, true, false)
	assert(carried.ID, false, false)
}
