package server

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	brokercontract "github.com/stacklok/mecatl/internal/mcpbroker"
)

type connectorBrokerSpy struct {
	brokercontract.Service
	attaches int
}

func (s *connectorBrokerSpy) AttachSession(ctx context.Context, id session.SessionID) (brokercontract.Attachment, brokercontract.AttachOutcome, error) {
	s.attaches++
	return s.Service.AttachSession(ctx, id)
}

type connectorStoreSpy struct {
	port.SessionStore
	saves int
	loads int
	fail  bool
}

func (s *connectorStoreSpy) Load(ctx context.Context, id session.SessionID) (*session.Session, error) {
	s.loads++
	return s.SessionStore.Load(ctx, id)
}

func (s *connectorStoreSpy) Save(ctx context.Context, sess *session.Session) error {
	s.saves++
	if s.fail {
		return errors.New("injected aggregate save failure")
	}
	return s.SessionStore.Save(ctx, sess)
}

func inspectMultiUpstream(t *testing.T, f *multiUpstreamFixture, enrollment, catalogue string) {
	t.Helper()
	ctx := session.WithPrincipal(t.Context(), connectorOwner())
	f.service.cfg.OwnershipEnforced = true
	f.service.cfg.MCPConnectorInspector = f.process.Runtime
	spy := &connectorStoreSpy{SessionStore: f.service.cfg.Store}
	f.service.cfg.Store = spy
	brokerSpy := &connectorBrokerSpy{Service: f.service.cfg.MCPBroker}
	f.service.cfg.MCPBroker = brokerSpy
	before, err := spy.Load(ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	headersA, callsA := f.mcpA.snapshot()
	headersB, callsB := f.mcpB.snapshot()
	f.issuerA.mu.Lock()
	tokensA, refreshA := f.issuerA.initialTokens, f.issuerA.refreshes
	f.issuerA.mu.Unlock()
	f.issuerB.mu.Lock()
	tokensB, refreshB := f.issuerB.initialTokens, f.issuerB.refreshes
	f.issuerB.mu.Unlock()
	f.mu.Lock()
	builds := len(f.catalogueBuilds)
	f.mu.Unlock()
	for range 3 {
		inventory, err := f.service.ListSessionMcpConnectors(ctx, f.session.ID)
		if err != nil {
			t.Fatal(err)
		}
		if inventory.Availability != "available" || inventory.EnrollmentState != enrollment || len(inventory.Connectors) != 2 {
			t.Fatalf("inventory: %+v", inventory)
		}
		for _, row := range inventory.Connectors {
			wantCount := uint32(0)
			if catalogue == "discovered" {
				wantCount = 1
			}
			if row.CatalogueState != catalogue || row.ToolCount != wantCount {
				t.Fatalf("row: %+v", row)
			}
		}
	}
	after, err := spy.Load(ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if brokerSpy.attaches != 0 {
		t.Fatal("inspection attached or created broker state")
	}
	if spy.saves != 0 || !reflect.DeepEqual(before, after) {
		t.Fatal("inspection changed persisted session")
	}
	gotA, gotCallsA := f.mcpA.snapshot()
	gotB, gotCallsB := f.mcpB.snapshot()
	if !reflect.DeepEqual(headersA, gotA) || !reflect.DeepEqual(headersB, gotB) || callsA != gotCallsA || callsB != gotCallsB {
		t.Fatal("inspection contacted an upstream")
	}
	f.issuerA.mu.Lock()
	defer f.issuerA.mu.Unlock()
	f.issuerB.mu.Lock()
	defer f.issuerB.mu.Unlock()
	if tokensA != f.issuerA.initialTokens || refreshA != f.issuerA.refreshes || tokensB != f.issuerB.initialTokens || refreshB != f.issuerB.refreshes {
		t.Fatal("inspection changed credentials")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if builds != len(f.catalogueBuilds) {
		t.Fatal("inspection rebuilt engine")
	}
}

func TestBrokerMCPStatus_Scenario1_ReadOnly(t *testing.T) {
	for _, phase := range []string{"before consent", "after consent", "published"} {
		t.Run(phase, func(t *testing.T) {
			f := newMultiUpstreamFixture(t, false, session.WithPrincipal(t.Context(), connectorOwner()))
			started := f.start()
			if phase != "before consent" && f.drive(started.URL) != 200 {
				t.Fatal("consent failed")
			}
			enrollment, catalogue := "pending", "hidden"
			if phase == "published" {
				f.connect()
				enrollment, catalogue = "completed", "discovered"
			}
			inspectMultiUpstream(t, f, enrollment, catalogue)
		})
	}
}

func TestBrokerMCPStatus_Scenario1_PublicationBeforeSessionCommit(t *testing.T) {
	for _, failure := range []string{"engine rebuild", "aggregate save"} {
		t.Run(failure, func(t *testing.T) {
			f := newMultiUpstreamFixture(t, false, session.WithPrincipal(t.Context(), connectorOwner()))
			started := f.start()
			if f.drive(started.URL) != 200 {
				t.Fatal("consent failed")
			}
			if failure == "engine rebuild" {
				f.failBuilds = 1
			} else {
				f.service.cfg.Store = &connectorStoreSpy{SessionStore: f.service.cfg.Store, fail: true}
			}
			if _, err := f.service.ConnectWorkspaceServices(t.Context(), f.session.ID); err == nil {
				t.Fatal("injected failure did not fire")
			}
			pending, err := f.service.cfg.Store.Load(t.Context(), f.session.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := pending.PendingWorkspaceEnrollment(); !ok {
				t.Fatal("failed publication cleared persisted pending gate")
			}
			inspectMultiUpstream(t, f, "completed", "discovered")
		})
	}
}
