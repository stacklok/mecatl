package server_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// TestCreateSessionWithSessionIDOverride: a caller passing WithSessionID mints
// the session under that id (the override wins over the Service's NewID). Pins
// ADR 0059 decision #7 Phase-2: the scheduler fire path passes a "sched--"
// id and the persisted session carries it.
func TestCreateSessionWithSessionIDOverride(t *testing.T) {
	svc := newService(t, mockllm.New(mockllm.TextTurn("ok")), nil)

	const want = "sched--nightly-20260713-010203-deadbeef"
	sess, err := svc.CreateSessionWithProfile(
		context.Background(), "/ws", session.ModeDefault, session.Limits{},
		server.ProviderSelector{}, server.ProfileDefault,
		server.WithSessionID(session.SessionID(want)),
	)
	if err != nil {
		t.Fatalf("CreateSessionWithProfile WithSessionID: %v", err)
	}
	if string(sess.ID) != want {
		t.Errorf("session id = %q, want the override %q", sess.ID, want)
	}
	// The override id is persisted: a GetSession returns the same id.
	loaded, err := svc.GetSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if string(loaded.ID) != want {
		t.Errorf("loaded id = %q, want %q", loaded.ID, want)
	}
}

// TestCreateSessionWithSessionIDNoOverrideIsByteIdentical: with no
// WithSessionID option, the Service's NewID generator mints the id — the
// pre-Phase-2 path is unchanged.
func TestCreateSessionWithSessionIDNoOverrideIsByteIdentical(t *testing.T) {
	var newIDCalls atomic.Int32
	shared := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("ok")),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(nil, nil),
		Model:   "test-model",
	})
	svc, err := server.NewService(server.Config{
		Engine:     shared,
		Store:      memstore.New(),
		Workspaces: func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now:        func() time.Time { return time.Unix(0, 0) },
		NewID: func() session.SessionID {
			newIDCalls.Add(1)
			return "generated-abc"
		},
		DefaultCapabilities: mockllm.New().Capabilities(),
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	sess, err := svc.CreateSessionWithProfile(
		context.Background(), "/ws", session.ModeDefault, session.Limits{},
		server.ProviderSelector{}, server.ProfileDefault,
	)
	if err != nil {
		t.Fatalf("CreateSessionWithProfile (no opts): %v", err)
	}
	if string(sess.ID) != "generated-abc" {
		t.Errorf("session id = %q, want the generated id", sess.ID)
	}
	if newIDCalls.Load() != 1 {
		t.Errorf("NewID called %d times, want 1", newIDCalls.Load())
	}
}

// TestCreateSessionWithSessionIDCollisionRejected: an override id that collides
// with a LIVE per-session engine (one already in the sessionEngines map) is
// rejected with ErrInvalidArgument — it would shadow an in-flight session.
func TestCreateSessionWithSessionIDCollisionRejected(t *testing.T) {
	var closed atomic.Int32
	perSession := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("per-session reply")),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(nil, nil),
		Model:   "test-model",
	})
	factory := func(_ context.Context, _ server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
		return server.SessionEngineResult{Engine: perSession, Close: func() error { closed.Add(1); return nil }}, nil
	}
	svc := newMCPService(t, "SHARED-REPLY", factory)

	// Create a session under a known id via a non-zero selector (registers a
	// per-session engine in the sessionEngines map).
	const live = "sched--live-1234"
	first, err := svc.CreateSessionWithProfile(
		context.Background(), "/ws", session.ModeDefault, session.Limits{},
		server.ProviderSelector{ProviderID: "openai", ModelID: "gpt-x"},
		server.ProfileDefault,
		server.WithSessionID(session.SessionID(live)),
	)
	if err != nil {
		t.Fatalf("first CreateSessionWithProfile: %v", err)
	}
	if string(first.ID) != live {
		t.Fatalf("first session id = %q, want %q", first.ID, live)
	}
	defer svc.CloseSession(first.ID)

	// A second create with the SAME override id must be rejected (the live engine
	// collides). This is the shared-engine fast path (zero selector), which still
	// consults the sessionEngines map for the collision check.
	_, err = svc.CreateSessionWithProfile(
		context.Background(), "/ws", session.ModeDefault, session.Limits{},
		server.ProviderSelector{},
		server.ProfileDefault,
		server.WithSessionID(session.SessionID(live)),
	)
	if err == nil {
		t.Fatal("second CreateSessionWithProfile with a colliding id succeeded, want ErrInvalidArgument")
	}
	if !errors.Is(err, server.ErrInvalidArgument) {
		t.Errorf("collision err = %v, want ErrInvalidArgument", err)
	}
	if !strings.Contains(err.Error(), live) {
		t.Errorf("collision err = %v, want it to name the colliding id %q", err, live)
	}
}
