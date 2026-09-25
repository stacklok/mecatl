package server

import (
	"context"
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	brokercontract "github.com/stacklok/mecatl/internal/mcpbroker"
)

// successorBrokerStore fails only the successor publication, after the source
// has been created successfully.
type successorBrokerStore struct {
	*memstore.Store
	failCreate bool
}

func (s *successorBrokerStore) Create(ctx context.Context, sess *session.Session) error {
	if s.failCreate {
		return errors.New("injected successor persistence failure")
	}
	return s.Store.Create(ctx, sess)
}

func newBrokerSuccessorService(t *testing.T, store port.SessionStore, ids ...session.SessionID) *Service {
	t.Helper()
	runtime := testBrokerRuntime(t)
	t.Cleanup(func() { runtime.Close() })
	var next int
	service, err := NewService(Config{
		Engine:            brokerEngineResult().Engine,
		Store:             store,
		PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test",
		NewID: func() session.SessionID {
			id := ids[next]
			next++
			return id
		},
		MCPBroker: runtime,
		RootAuthority: func(session.SessionKind) session.Authority {
			return session.Authority{CapabilitySet: governance.CapabilitySet{Tools: []string{"broker-old"}}, Provenance: "test"}
		},
		SessionEngine: func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode) (SessionEngineResult, error) {
			t.Fatal("broker successor used ordinary SessionEngine")
			return SessionEngineResult{}, nil
		},
		SessionEngineWithTools: func(_ context.Context, _ ProviderSelector, _ []mcp.ServerConfig, _ SessionProfile, _ string, _ session.PermissionMode, tools []tool.Tool, _ []string) (SessionEngineResult, error) {
			if tools == nil {
				t.Fatal("broker successor received nil broker tools")
			}
			res := brokerEngineResult()
			res.BrokerRegistrationKeys = []string{"broker-old"}
			return res, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	return service
}

func TestWorkspaceEnrollmentAuthority_Scenario2_PersistsAcrossResumeAndFork(t *testing.T) {
	for _, tc := range []struct {
		name  string
		clear bool
	}{
		{name: "fork"},
		{name: "clear", clear: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := memstore.New()
			svc := newBrokerSuccessorService(t, store, "source", "successor")
			source, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Save(context.Background(), source); err != nil {
				t.Fatal(err)
			}
			var successorID session.SessionID
			if tc.clear {
				successorID, err = svc.ClearSessionSuccessor(context.Background(), source.ID, SuccessorPlacement{})
			} else {
				successorID, err = svc.ForkSessionSuccessor(context.Background(), ForkSuccessorRequest{Source: source.ID})
			}
			if err != nil {
				t.Fatalf("successor: %v", err)
			}
			successor, err := store.Load(context.Background(), successorID)
			if err != nil {
				t.Fatal(err)
			}
			keys, present := successor.WorkspaceEnrollmentBrokerKeys()
			if !present || len(keys) != 1 || keys[0] != "broker-old" {
				t.Fatalf("successor ledger = %q, %t", keys, present)
			}
		})
	}
}

func TestBrokerSuccessorsRejectPriorBrokerKeyOwnedByNonBroker(t *testing.T) {
	for _, tc := range []struct {
		name  string
		clear bool
	}{
		{name: "fork"},
		{name: "clear", clear: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := memstore.New()
			svc := newBrokerSuccessorService(t, store, "source", "successor")
			source, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Save(context.Background(), source); err != nil {
				t.Fatal(err)
			}
			var closed bool
			svc.cfg.SessionEngineWithTools = func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode, []tool.Tool, []string) (SessionEngineResult, error) {
				return SessionEngineResult{
					Engine:                    brokerEngineResult().Engine,
					NonBrokerRegistrationKeys: []string{"broker-old"},
					Close:                     func() error { closed = true; return nil },
				}, nil
			}

			if tc.clear {
				_, err = svc.ClearSessionSuccessor(context.Background(), source.ID, SuccessorPlacement{})
			} else {
				_, err = svc.ForkSessionSuccessor(context.Background(), ForkSuccessorRequest{Source: source.ID})
			}
			var collision *WorkspaceEnrollmentCollisionError
			if !errors.As(err, &collision) || collision.Key != "broker-old" {
				t.Fatalf("successor collision = %v, want safe prior registration key", err)
			}
			if !closed {
				t.Fatal("successor left the unpublished conflicting candidate open")
			}
			svc.mu.Lock()
			published := svc.sessionEngines["successor"]
			svc.mu.Unlock()
			if published != nil {
				t.Fatal("successor published a non-broker implementation under a prior broker key")
			}
		})
	}
}

func TestWorkspaceEnrollmentAuthority_Scenario2_SuccessorsStartFreshBrokerEnrollment(t *testing.T) {
	for _, tc := range []struct {
		name  string
		clear bool
	}{
		{name: "fork"},
		{name: "clear", clear: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := memstore.New()
			svc := newBrokerSuccessorService(t, store, "source", "successor")
			source, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			if source.ExternalBinding == "" {
				t.Fatal("source has empty external binding")
			}

			var successorID session.SessionID
			if tc.clear {
				successorID, err = svc.ClearSessionSuccessor(context.Background(), source.ID, SuccessorPlacement{})
			} else {
				successorID, err = svc.ForkSessionSuccessor(context.Background(), ForkSuccessorRequest{Source: source.ID})
			}
			if err != nil {
				t.Fatalf("successor: %v", err)
			}
			successor, err := store.Load(context.Background(), successorID)
			if err != nil {
				t.Fatal(err)
			}
			if successor.ExternalBinding == "" || successor.ExternalBinding == source.ExternalBinding {
				t.Fatalf("successor binding = %q, source binding = %q", successor.ExternalBinding, source.ExternalBinding)
			}
			svc.mu.Lock()
			_, hasEngine := svc.sessionEngines[successorID]
			svc.mu.Unlock()
			if !hasEngine {
				t.Fatalf("successor %q has no registered per-session engine", successorID)
			}
		})
	}
}

func TestForkBrokerSuccessorLeavesSourceBindingAndAttachmentUnchanged(t *testing.T) {
	store := memstore.New()
	svc := newBrokerSuccessorService(t, store, "source", "forked")
	source, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	svc.mu.Lock()
	sourceAttachment := svc.brokerAttachments[source.ID]
	svc.mu.Unlock()
	if sourceAttachment == nil || sourceAttachment.Binding() != source.ExternalBinding {
		t.Fatalf("source attachment = %v, persisted binding = %q", sourceAttachment, source.ExternalBinding)
	}

	forkedID, err := svc.ForkSessionSuccessor(context.Background(), ForkSuccessorRequest{Source: source.ID})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ExternalBinding != source.ExternalBinding {
		t.Fatalf("source binding changed from %q to %q", source.ExternalBinding, loaded.ExternalBinding)
	}
	svc.mu.Lock()
	currentSourceAttachment := svc.brokerAttachments[source.ID]
	forkedAttachment := svc.brokerAttachments[forkedID]
	svc.mu.Unlock()
	if currentSourceAttachment != sourceAttachment || currentSourceAttachment.Binding() != source.ExternalBinding {
		t.Fatal("fork changed the source broker attachment")
	}
	if forkedAttachment == nil || forkedAttachment.Binding() == source.ExternalBinding {
		t.Fatalf("fork attachment = %v, source binding = %q", forkedAttachment, source.ExternalBinding)
	}
}

func TestBrokerSuccessorPersistenceFailureRollsBackAttachment(t *testing.T) {
	base := memstore.New()
	store := &successorBrokerStore{Store: base}
	svc := newBrokerSuccessorService(t, store, "source", "failed-successor")
	source, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	store.failCreate = true

	if successorID, err := svc.ForkSessionSuccessor(context.Background(), ForkSuccessorRequest{Source: source.ID}); err == nil || successorID != "" {
		t.Fatalf("failed successor = (%q, %v), want empty id and error", successorID, err)
	}
	svc.mu.Lock()
	_, attached := svc.brokerAttachments["failed-successor"]
	svc.mu.Unlock()
	if attached {
		t.Fatal("failed successor left a committed local broker attachment")
	}
	attachment, outcome, err := svc.cfg.MCPBroker.AttachSession(context.Background(), "failed-successor")
	if err != nil {
		t.Fatal(err)
	}
	defer attachment.Close(context.Background())
	if outcome != brokercontract.AttachCreated {
		t.Fatalf("attachment after rollback outcome = %q, want created", outcome)
	}
}
