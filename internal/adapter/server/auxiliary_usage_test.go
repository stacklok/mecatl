package server

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/eventsource"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestAuxiliaryTokenUsage_Scenario4_RoundTripAndProjection(t *testing.T) {
	env := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "rev-1"}
	s := session.New("auxiliary-round-trip", session.ModeDefault, env, session.Limits{}, time.Unix(1, 0))
	kinds := []session.UsageKind{
		session.UsageKindCompaction,
		session.UsageKindReflection,
		session.UsageKindRouter,
		session.UsageKindAskReviewer,
		session.UsageKindGuardrail,
		session.UsageKindParallelJudge,
		session.UsageKind("future_helper"),
	}
	for i, kind := range kinds {
		s.RecordTokenUsage(kind, "provider", "model", session.Usage{InputTokens: i + 1, OutputTokens: i + 2})
	}
	want := s.TokenUsageSnapshot()

	store := memstore.New()
	if err := store.Save(context.Background(), s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := store.Load(context.Background(), s.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := loaded.TokenUsageSnapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("store ledger = %#v, want %#v", got, want)
	}

	folded, err := eventsource.Fold(eventsource.SessionMeta{
		ID: s.ID, Mode: s.Mode, Limits: s.Limits, EnvironmentRef: env,
		TokenUsage: want, CreatedAt: s.CreatedAt,
	}, func(func(session.Event, error) bool) {})
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if got := folded.TokenUsageSnapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("event-source ledger = %#v, want %#v", got, want)
	}

	projected := toProtoSession(loaded, ResolvedModel{}, nil, port.ProviderCapabilities{})
	summary := toProtoSessionSummary(SessionSummary{SessionID: string(s.ID), TokenUsage: loaded.TokenUsageSnapshot()})
	for _, kind := range kinds {
		key := string(kind)
		if projected.TokenUsage[key] == nil {
			t.Errorf("session projection missing %q", key)
		}
		if summary.TokenUsage[key] == nil {
			t.Errorf("session-summary projection missing %q", key)
		}
	}
}

type auxiliaryUsageStore struct {
	port.SessionStore
	saves int
	loads int
}

func (s *auxiliaryUsageStore) Save(ctx context.Context, sess *session.Session) error {
	s.saves++
	return s.SessionStore.Save(ctx, sess)
}

func (s *auxiliaryUsageStore) Load(ctx context.Context, id session.SessionID) (*session.Session, error) {
	s.loads++
	return s.SessionStore.Load(ctx, id)
}

type auxiliaryUsageLease struct{ acquires int }

func (l *auxiliaryUsageLease) Acquire(context.Context, session.SessionID, string) (port.Lease, error) {
	l.acquires++
	return port.Lease{}, port.ErrLeaseHeld
}
func (*auxiliaryUsageLease) Renew(context.Context, port.Lease) (port.Lease, error) {
	return port.Lease{}, port.ErrLeaseHeld
}
func (*auxiliaryUsageLease) Release(context.Context, port.Lease) error { return nil }

type auxiliaryUsageDiagnostics struct{ messages []string }

func (d *auxiliaryUsageDiagnostics) Log(_ context.Context, _ port.Level, msg string, _ ...any) {
	d.messages = append(d.messages, msg)
}
func (d *auxiliaryUsageDiagnostics) With(...any) port.Diagnostics { return d }
func (d *auxiliaryUsageDiagnostics) count(fragment string) int {
	n := 0
	for _, message := range d.messages {
		if strings.Contains(message, fragment) {
			n++
		}
	}
	return n
}

func completedAuxiliaryUsageSession(t *testing.T, id session.SessionID) *session.Session {
	t.Helper()
	sess := session.New(id, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "rev-1"}, session.Limits{}, time.Unix(1, 0))
	if err := sess.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := sess.Complete(); err != nil {
		t.Fatal(err)
	}
	return sess
}

func TestReflectSessionAuxiliaryUsageRequiresCurrentOwnership(t *testing.T) {
	usage := session.Usage{InputTokens: 5, OutputTokens: 2}
	aux := session.AuxiliaryUsage{Buckets: map[session.UsageKind]session.TokenUsage{
		session.UsageKindReflection: {Total: usage, Models: map[string]session.Usage{"server-provider/server-model": usage}},
	}}
	reflector := func(context.Context, *session.Session) (ReflectionReceipt, error) {
		return ReflectionReceipt{Disposition: "completed", Usage: aux}, nil
	}

	t.Run("current owner persists through existing capability", func(t *testing.T) {
		store := &auxiliaryUsageStore{SessionStore: memstore.New()}
		id := session.SessionID("current-owner")
		if err := store.Save(t.Context(), completedAuxiliaryUsageSession(t, id)); err != nil {
			t.Fatal(err)
		}
		store.saves = 0
		svc := &Service{cfg: Config{
			Store: store, ReflectSession: reflector, Diagnostics: port.NopDiagnostics{},
			MutationCapability: NewSessionMutationCapability(false),
		}}
		if _, err := svc.ReflectSession(t.Context(), id); err != nil {
			t.Fatal(err)
		}
		if store.saves != 1 {
			t.Fatalf("accounting saves = %d, want one", store.saves)
		}
		persisted, err := store.Load(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if got := persisted.UsageFor(session.UsageKindReflection); got != usage {
			t.Fatalf("persisted reflection usage = %+v, want %+v", got, usage)
		}
	})

	t.Run("detached path drops without reacquire reload or save", func(t *testing.T) {
		store := &auxiliaryUsageStore{SessionStore: memstore.New()}
		id := session.SessionID("detached-owner")
		if err := store.Save(t.Context(), completedAuxiliaryUsageSession(t, id)); err != nil {
			t.Fatal(err)
		}
		store.saves, store.loads = 0, 0
		lease := &auxiliaryUsageLease{}
		diagnostics := &auxiliaryUsageDiagnostics{}
		svc := &Service{cfg: Config{
			Store: store, ReflectSession: reflector, Diagnostics: diagnostics,
			SessionLease: lease, MutationCapability: NewSessionMutationCapability(true),
		}}
		if _, err := svc.ReflectSession(t.Context(), id); err != nil {
			t.Fatal(err)
		}
		if lease.acquires != 0 || store.loads != 1 || store.saves != 0 {
			t.Fatalf("detached accounting acquired=%d loaded=%d saved=%d, want 0,1,0", lease.acquires, store.loads, store.saves)
		}
		if diagnostics.count("reflection usage dropped") != 1 {
			t.Fatalf("drop diagnostics = %#v, want one bounded message", diagnostics.messages)
		}
	})

	t.Run("lease loss drops before stale save", func(t *testing.T) {
		store := &auxiliaryUsageStore{SessionStore: memstore.New()}
		id := session.SessionID("lost-owner")
		if err := store.Save(t.Context(), completedAuxiliaryUsageSession(t, id)); err != nil {
			t.Fatal(err)
		}
		store.saves = 0
		capability := NewSessionMutationCapability(true)
		capability.Grant(id)
		leaseCtx, loseLease := context.WithCancel(context.Background())
		diagnostics := &auxiliaryUsageDiagnostics{}
		svc := &Service{cfg: Config{
			Store: store, Diagnostics: diagnostics, SessionLease: &auxiliaryUsageLease{}, MutationCapability: capability,
		}, heldLeases: map[session.SessionID]*heldLease{id: {ctx: leaseCtx, valid: true}}}
		svc.cfg.ReflectSession = func(context.Context, *session.Session) (ReflectionReceipt, error) {
			capability.Invalidate(id)
			loseLease()
			return ReflectionReceipt{Disposition: "completed", Usage: aux}, nil
		}
		if _, err := svc.ReflectSession(t.Context(), id); err != nil {
			t.Fatal(err)
		}
		if store.saves != 0 {
			t.Fatalf("post-lease-loss accounting saves = %d, want zero", store.saves)
		}
		if diagnostics.count("reflection usage dropped") != 1 {
			t.Fatalf("drop diagnostics = %#v, want one bounded message", diagnostics.messages)
		}
	})
}

func TestAuxiliaryTokenUsage_Scenario2_ReflectionRecordsSelectedModel(t *testing.T) {
	reflectionUsage := session.Usage{InputTokens: 5, OutputTokens: 2}
	secondModelUsage := session.Usage{InputTokens: 3, OutputTokens: 1}
	injectedUsage := session.Usage{InputTokens: 13, OutputTokens: 8}
	reflector := func(context.Context, *session.Session) (ReflectionReceipt, error) {
		return ReflectionReceipt{Disposition: "completed", Usage: session.AuxiliaryUsage{Buckets: map[session.UsageKind]session.TokenUsage{
			session.UsageKindReflection: {Models: map[string]session.Usage{
				"reflection-provider/reflection-model": reflectionUsage,
				"second-provider/second-model":         secondModelUsage,
			}},
			session.UsageKindMain:   {Models: map[string]session.Usage{"injected/main": injectedUsage}},
			session.UsageKindRouter: {Models: map[string]session.Usage{"injected/router": injectedUsage}},
		}}}, nil
	}
	store := &auxiliaryUsageStore{SessionStore: memstore.New()}
	id := session.SessionID("reflection-usage-purpose")
	if err := store.Save(t.Context(), completedAuxiliaryUsageSession(t, id)); err != nil {
		t.Fatal(err)
	}
	svc := &Service{cfg: Config{
		Store: store, ReflectSession: reflector, Diagnostics: port.NopDiagnostics{},
		MutationCapability: NewSessionMutationCapability(false),
	}}
	if _, err := svc.ReflectSession(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	persisted, err := store.Load(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	ledger := persisted.TokenUsageSnapshot()
	reflection := ledger[session.UsageKindReflection]
	if got, want := reflection.Models["reflection-provider/reflection-model"], reflectionUsage; got != want {
		t.Fatalf("reflection model usage = %+v, want %+v", got, want)
	}
	if got, want := reflection.Models["second-provider/second-model"], secondModelUsage; got != want {
		t.Fatalf("second reflection model usage = %+v, want %+v", got, want)
	}
	if got, want := reflection.Models["injected/main"], injectedUsage; got != want {
		t.Fatalf("remapped main model usage = %+v, want %+v", got, want)
	}
	if got, want := reflection.Models["injected/router"], injectedUsage; got != want {
		t.Fatalf("remapped router model usage = %+v, want %+v", got, want)
	}
	if got, want := reflection.Total, reflectionUsage.Add(secondModelUsage).Add(injectedUsage).Add(injectedUsage); got != want {
		t.Fatalf("reflection total = %+v, want %+v", got, want)
	}
	if len(ledger) != 1 {
		t.Fatalf("usage ledger = %#v, want reflection only", ledger)
	}
}
