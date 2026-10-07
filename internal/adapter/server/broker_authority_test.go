package server

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	c "github.com/stacklok/mecatl/internal/mcpbroker"
)

type brokerContributionDescriptor struct{ name string }

func (d brokerContributionDescriptor) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: d.name, Schema: []byte(`{"type":"object"}`)}
}
func (brokerContributionDescriptor) ReadOnly() bool { return true }
func (brokerContributionDescriptor) Execute(context.Context, session.ToolCall, tool.Environment) (session.ToolResult, error) {
	return session.ToolResult{}, errors.New("descriptor must execute through broker")
}

type contributionEnrollmentBroker struct {
	*sessionHostAuthBroker
	catalogue   c.Catalogue
	disconnects int
}

func (b *contributionEnrollmentBroker) BeginEnrollment(context.Context, c.SessionRef) (c.BeginEnrollmentOutcome, error) {
	return c.BeginEnrollmentOutcome{Kind: c.EnrollmentCompletedKind, Catalogue: b.catalogue}, nil
}
func (b *contributionEnrollmentBroker) DisconnectTools(context.Context, c.SessionRef, c.ConnectionRef) (c.DisconnectResult, error) {
	b.disconnects++
	if b.initial.Connection() == "" {
		cat, err := c.NewCatalogue(b.initial.Ref(), b.catalogue.Connection(), nil)
		if err != nil {
			return 0, err
		}
		b.initial = cat
	}
	return c.Disconnected, nil
}

func TestBrokerAuthorityHostOverlapAndWithdrawal(t *testing.T) {
	for _, grant := range []string{"none", "before", "after"} {
		t.Run(grant, func(t *testing.T) {
			store := &sessionBrokerFailStore{Store: memstore.New()}
			empty, err := c.NewCatalogue(c.CatalogueRef(hostProofRef(2)), "", nil)
			if err != nil {
				t.Fatal(err)
			}
			published, err := c.NewCatalogue(c.CatalogueRef(hostProofRef(3)), c.ConnectionRef(hostProofRef(5)), []tool.Tool{brokerContributionDescriptor{name: "mcp__fixture__new"}})
			if err != nil {
				t.Fatal(err)
			}
			broker := &contributionEnrollmentBroker{sessionHostAuthBroker: &sessionHostAuthBroker{ref: c.SessionRef(hostProofRef(1)), initial: empty, expires: time.Now().Add(time.Hour), store: store, t: t}, catalogue: published}
			client := sessionBrokerRPCClient(t, broker, &session.Principal{Issuer: "test", Subject: "owner"})
			svc := sessionBrokerTestHost(t, client, store, mockllm.New(mockllm.TextTurn("done")), nil)
			svc.cfg.RootAuthority = func(session.SessionKind) session.Authority {
				return session.Authority{Provenance: "test", CapabilitySet: governance.CapabilitySet{Tools: []string{"Read"}}}
			}
			created, err := svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			grantName := func() {
				sess, err := store.Load(t.Context(), created.ID)
				if err != nil {
					t.Fatal(err)
				}
				if err := sess.GrantToolAuthority([]string{"mcp__fixture__new"}); err != nil {
					t.Fatal(err)
				}
				if err := store.Save(t.Context(), sess); err != nil {
					t.Fatal(err)
				}
			}
			if grant == "before" {
				grantName()
			}
			if _, err := svc.ConnectWorkspaceServices(t.Context(), created.ID); err != nil {
				t.Fatal(err)
			}
			if grant == "after" {
				grantName()
			}
			// A failed withdrawal, including a committed save with a lost reply,
			// performs no remote cleanup and requires durable reload.
			for _, ambiguous := range []bool{false, true} {
				store.fail.Store(true)
				store.ambiguous.Store(ambiguous)
				if err := svc.DisconnectWorkspaceServices(t.Context(), created.ID); err == nil {
					t.Fatal("failed withdrawal succeeded")
				}
				if broker.disconnects != 0 {
					t.Fatal("remote cleanup preceded durable acknowledgement")
				}
				if _, ok := svc.sessionEngines[created.ID]; ok {
					t.Fatal("uncertain withdrawal retained cached wrappers")
				}
				store.fail.Store(false)
			}
			if err := svc.DisconnectWorkspaceServices(t.Context(), created.ID); err != nil {
				t.Fatal(err)
			}
			want := []string{"Read"}
			if grant != "none" {
				want = append(want, "mcp__fixture__new")
			}
			durable, err := store.Load(t.Context(), created.ID)
			if err != nil {
				t.Fatal(err)
			}
			access, _ := durable.BrokerAccess()
			if !reflect.DeepEqual(durable.Authority.CapabilitySet.Tools, want) || !reflect.DeepEqual(access.IndependentTools, want) || !access.Withdrawn {
				t.Fatalf("withdrawal lost independent overlap: %+v", access)
			}
			if _, err := svc.ConnectWorkspaceServices(t.Context(), created.ID); err != nil {
				t.Fatal(err)
			}
			durable, err = store.Load(t.Context(), created.ID)
			if err != nil {
				t.Fatal(err)
			}
			access, _ = durable.BrokerAccess()
			if access.Withdrawn || !reflect.DeepEqual(access.IndependentTools, want) || !durable.Authority.CapabilitySet.AllowsTool("mcp__fixture__new") {
				t.Fatalf("reenrollment drift: %+v", access)
			}
		})
	}
}

func TestBrokerAuthorityEnrollmentSaveBarrier(t *testing.T) {
	for _, mode := range []string{"definitive", "ambiguous"} {
		t.Run(mode, func(t *testing.T) {
			store := &sessionBrokerFailStore{Store: memstore.New()}
			empty, err := c.NewCatalogue(c.CatalogueRef(hostProofRef(2)), "", nil)
			if err != nil {
				t.Fatal(err)
			}
			published, err := c.NewCatalogue(c.CatalogueRef(hostProofRef(3)), c.ConnectionRef(hostProofRef(5)), []tool.Tool{brokerContributionDescriptor{name: "mcp__fixture__new"}})
			if err != nil {
				t.Fatal(err)
			}
			broker := &sessionHostAuthBroker{ref: c.SessionRef(hostProofRef(1)), initial: empty, expires: time.Now().Add(time.Hour), store: store, t: t}
			client := sessionBrokerRPCClient(t, broker, &session.Principal{Issuer: "test", Subject: "owner"})
			svc := sessionBrokerTestHost(t, client, store, mockllm.New(mockllm.TextTurn("done")), nil)
			svc.cfg.RootAuthority = func(session.SessionKind) session.Authority {
				return session.Authority{Provenance: "test", CapabilitySet: governance.CapabilitySet{Tools: []string{"Read"}}}
			}
			created, err := svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			pending := session.PendingWorkspaceEnrollment{ID: "pending", RequiredServices: 1, ExpiresAt: broker.expires}
			if err := created.BeginWorkspaceEnrollment(pending); err != nil {
				t.Fatal(err)
			}
			if err := store.Save(t.Context(), created); err != nil {
				t.Fatal(err)
			}
			original := created.Authority.Clone()
			store.fail.Store(true)
			store.ambiguous.Store(mode == "ambiguous")
			if _, err := svc.publishSessionBrokerEnrollment(t.Context(), created, published); err == nil {
				t.Fatal("save failure succeeded")
			}
			if !reflect.DeepEqual(original, created.Authority) {
				t.Fatal("failed enrollment widened aggregate")
			}
			if _, ok := created.PendingWorkspaceEnrollment(); !ok {
				t.Fatal("failed enrollment consumed cached pending")
			}
			if _, ok := svc.sessionEngines[created.ID]; ok {
				t.Fatal("cached engine survived ambiguous publication")
			}
			durable, err := store.Load(t.Context(), created.ID)
			if err != nil {
				t.Fatal(err)
			}
			if durable.Authority.CapabilitySet.AllowsTool("mcp__fixture__new") != (mode == "ambiguous") {
				t.Fatal("unexpected durable save outcome")
			}
			a, _ := durable.BrokerAccess()
			if !reflect.DeepEqual(a.IndependentTools, []string{"Read"}) {
				t.Fatal("save clobbered independent contribution")
			}
		})
	}
}
