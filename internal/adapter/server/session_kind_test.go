package server_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/protobuf/reflect/protoreflect"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
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

func TestInvariant_non_main_sessions_cannot_start_as_chat(t *testing.T) {
	t.Parallel()
	created := time.Unix(0, 0)
	branch := 2
	scheduled, scheduledErr := session.NewScheduled("opaque-scheduled", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, created, "nightly", "", "")
	subagent, subagentErr := session.NewSubagent("opaque-subagent", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, created, "parent", session.NewIncarnationID(), "call")
	parallel, parallelErr := session.NewParallelBranch("opaque-parallel", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, created, "parent", session.NewIncarnationID(), "call", branch)
	team, teamErr := session.NewTeamMember("opaque-team", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, created, "team", "worker", "parent", session.NewIncarnationID())
	fixtures := []struct {
		name string
		sess *session.Session
	}{
		{"scheduled", mustRelatedSession(t, scheduled, scheduledErr)},
		{"subagent", mustRelatedSession(t, subagent, subagentErr)},
		{"parallel", mustRelatedSession(t, parallel, parallelErr)},
		{"team", mustRelatedSession(t, team, teamErr)},
	}
	for _, tc := range fixtures {
		t.Run(tc.name, func(t *testing.T) {
			svc, store := runPurposeService(t, false)
			if err := store.Save(context.Background(), tc.sess); err != nil {
				t.Fatalf("Save: %v", err)
			}
			if _, err := svc.StartRunContent(context.Background(), tc.sess.ID, "do not run", nil); !errors.Is(err, server.ErrInvalidArgument) {
				t.Fatalf("StartRunContent(%s) = %v, want ErrInvalidArgument", tc.sess.Kind, err)
			}
		})
	}
}

func TestADR_0108_SchedulerPurposeOnlyDrivesScheduled(t *testing.T) {
	t.Parallel()
	svc, store := runPurposeService(t, false)
	scheduledSession, scheduledErr := session.NewScheduled("custom-fire-id", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0), "nightly", "", "")
	scheduled := mustRelatedSession(t, scheduledSession, scheduledErr)
	if err := store.Save(context.Background(), scheduled); err != nil {
		t.Fatalf("Save scheduled: %v", err)
	}
	if _, err := svc.StartRunContent(context.Background(), scheduled.ID, "public", nil); !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("public StartRunContent = %v, want ErrInvalidArgument", err)
	}
	run, err := svc.StartScheduledRunContent(context.Background(), scheduled.ID, "trusted", nil)
	if err != nil {
		t.Fatalf("StartScheduledRunContent: %v", err)
	}
	assertRunCompleted(t, svc, scheduled.ID, run)

	main := session.New("ordinary-main", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))
	if err := store.Save(context.Background(), main); err != nil {
		t.Fatalf("Save main: %v", err)
	}
	if _, err := svc.StartScheduledRunContent(context.Background(), main.ID, "wrong purpose", nil); !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("scheduler purpose on main = %v, want ErrInvalidArgument", err)
	}

	fields := (&mecatlv1.Prompt{}).ProtoReflect().Descriptor().Fields()
	if fields.ByName("purpose") != nil || fields.ByName("run_purpose") != nil {
		t.Fatal("public Prompt exposes scheduler run purpose")
	}
}

func TestADR_0108_LegacySafetyGate(t *testing.T) {
	t.Parallel()
	for _, prefix := range []string{agent.SubagentSessionPrefix, agent.ParallelSessionPrefix, agent.TeamSessionPrefix, "sched--"} {
		for _, kind := range []session.SessionKind{session.SessionKindMain, session.SessionKindUnknown} {
			name := prefix + string(kind)
			t.Run(name, func(t *testing.T) {
				svc, store := runPurposeService(t, false)
				sess := session.New(session.SessionID(prefix+"legacy"), session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))
				if err := sess.RestoreSessionMetadata(kind, session.SessionRelationship{}); err != nil {
					t.Fatalf("RestoreSessionMetadata: %v", err)
				}
				if err := store.Save(context.Background(), sess); err != nil {
					t.Fatalf("Save: %v", err)
				}
				if _, err := svc.StartRunContent(context.Background(), sess.ID, "do not run", nil); !errors.Is(err, server.ErrInvalidArgument) {
					t.Fatalf("StartRunContent(%q, %q) = %v, want ErrInvalidArgument", sess.ID, kind, err)
				}
			})
		}
	}

	t.Run("legacy scheduled fallback is trusted-only", func(t *testing.T) {
		svc, store := runPurposeService(t, false)
		sess := session.New("sched--legacy-fire", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))
		if err := sess.RestoreSessionMetadata(session.SessionKindUnknown, session.SessionRelationship{}); err != nil {
			t.Fatal(err)
		}
		if err := store.Save(context.Background(), sess); err != nil {
			t.Fatal(err)
		}
		run, err := svc.StartScheduledRunContent(context.Background(), sess.ID, "trusted legacy", nil)
		if err != nil {
			t.Fatalf("StartScheduledRunContent legacy: %v", err)
		}
		assertRunCompleted(t, svc, sess.ID, run)
	})
}

func TestInvariant_session_ids_are_not_client_classifiers(t *testing.T) {
	t.Parallel()
	svc, store := runPurposeService(t, false)
	sess := session.New("opaque/01JZ:custom.child", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))
	if err := store.Save(context.Background(), sess); err != nil {
		t.Fatalf("Save: %v", err)
	}
	run, err := svc.StartRunContent(context.Background(), sess.ID, "continue", nil)
	if err != nil {
		t.Fatalf("StartRunContent ordinary main: %v", err)
	}
	assertRunCompleted(t, svc, sess.ID, run)
}

func TestSessionContinuityUX_Scenario2_OwnershipOracleClosed(t *testing.T) {
	t.Parallel()
	svc, store := runPurposeService(t, true)
	alice := &session.Principal{Issuer: "issuer", Subject: "alice", GrantType: session.GrantTypeUser}
	bob := &session.Principal{Issuer: "issuer", Subject: "bob", GrantType: session.GrantTypeUser}
	aliceCtx := session.WithPrincipal(context.Background(), alice)

	foreign := session.New("foreign", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))
	if err := foreign.RestoreLabels(bob, session.Authority{}); err != nil {
		t.Fatal(err)
	}
	ownerless := session.New("ownerless", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))
	pruned := session.New("pruned", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))
	if err := pruned.RestoreLabels(alice, session.Authority{}); err != nil {
		t.Fatal(err)
	}
	for _, sess := range []*session.Session{foreign, ownerless, pruned} {
		if err := store.Save(context.Background(), sess); err != nil {
			t.Fatalf("Save %q: %v", sess.ID, err)
		}
	}
	if err := store.Delete(context.Background(), pruned.ID); err != nil {
		t.Fatalf("Delete pruned: %v", err)
	}

	for _, id := range []session.SessionID{"unknown", pruned.ID, ownerless.ID, foreign.ID} {
		t.Run(string(id), func(t *testing.T) {
			assertNotFound := func(surface string, err error) {
				t.Helper()
				if !errors.Is(err, server.ErrNotFound) {
					t.Errorf("%s(%q) = %v, want ErrNotFound", surface, id, err)
				}
			}
			_, err := svc.GetSession(aliceCtx, id)
			assertNotFound("GetSession", err)
			_, err = svc.LoadSession(aliceCtx, id)
			assertNotFound("LoadSession", err)
			_, err = svc.StreamSessionEvents(aliceCtx, id)
			assertNotFound("StreamSessionEvents", err)
			_, err = svc.StartRunContent(aliceCtx, id, "resume", nil)
			assertNotFound("StartRunContent", err)
		})
	}
	rows, err := svc.ListSessions(aliceCtx)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("ListSessions exposed inaccessible rows: %+v", rows)
	}
}

func mustRelatedSession(t *testing.T, sess *session.Session, err error) *session.Session {
	t.Helper()
	if err != nil {
		t.Fatalf("construct related session: %v", err)
	}
	return sess
}

func runPurposeService(t *testing.T, ownership bool) (*server.Service, *memstore.Store) {
	t.Helper()
	store := memstore.New()
	eng := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("ok")),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(nil, nil),
		Model:   "test-model",
		Store:   store,
	})
	svc, err := newPlacementTestService(server.Config{
		Engine:            eng,
		Store:             store,
		EventLog:          memstore.NewEventLog(),
		Workspaces:        func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now:               func() time.Time { return time.Unix(0, 0) },
		OwnershipEnforced: ownership,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc, store
}

func assertRunCompleted(t *testing.T, svc *server.Service, id session.SessionID, run *agent.Run) {
	t.Helper()
	var result *session.ResultPayload
	for ev := range run.Events() {
		if ev.Type == session.EvResult {
			result = ev.Result
		}
	}
	svc.FinishRun(id, run)
	if result == nil || result.Stop != session.StopEndTurn {
		t.Fatalf("run result = %+v, want end_turn", result)
	}
}
