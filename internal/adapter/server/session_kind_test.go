package server_test

import (
	"context"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestADR_0108_PublicCreateCannotForgeKind(t *testing.T) {
	ctx := context.Background()
	svc, _ := newMCPServiceStore(t, "ok", nil)

	created, err := svc.CreateSession(ctx, "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	assertMainWithoutLineage(t, created)

	fields := (&mecatlv1.CreateSessionRequest{}).ProtoReflect().Descriptor().Fields()
	for _, name := range []protoreflect.Name{"kind", "relationship", "parent_session_id", "schedule_name", "team_id"} {
		if fields.ByName(name) != nil {
			t.Errorf("public CreateSessionRequest exposes trusted field %q", name)
		}
	}
}

func TestSessionContinuityUX_Scenario1_PeerRebindsRemainMain(t *testing.T) {
	ctx := context.Background()
	svc, store := newMCPServiceStore(t, "ok", carryoverFactory("ok", nil))

	source, err := svc.CreateSession(ctx, "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession source: %v", err)
	}

	peerID, err := svc.ForkSession(ctx, source.ID, "peer", "")
	if err != nil {
		t.Fatalf("ForkSession peer: %v", err)
	}
	peer, err := store.Load(ctx, peerID)
	if err != nil {
		t.Fatalf("Load peer fork: %v", err)
	}
	assertMainWithoutLineage(t, peer)

	forkID, err := svc.ForkSession(ctx, source.ID, "effort", "high")
	if err != nil {
		t.Fatalf("ForkSession effort: %v", err)
	}
	forked, err := store.Load(ctx, forkID)
	if err != nil {
		t.Fatalf("Load effort fork: %v", err)
	}
	assertMainWithoutLineage(t, forked)

	carried, err := svc.CreateSessionWithProfile(ctx, "/ws", session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSourceSession(source.ID))
	if err != nil {
		t.Fatalf("CreateSessionWithProfile carryover: %v", err)
	}
	assertMainWithoutLineage(t, carried)
}

func assertMainWithoutLineage(t *testing.T, s *session.Session) {
	t.Helper()
	if s.Kind != session.SessionKindMain || s.Relationship != (session.SessionRelationship{}) {
		t.Fatalf("session %q metadata = (%q, %+v), want main without lineage", s.ID, s.Kind, s.Relationship)
	}
}
