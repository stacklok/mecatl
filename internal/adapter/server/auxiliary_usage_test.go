package server

import (
	"context"
	"errors"
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

type auxiliaryUsageDiagnostics struct {
	messages []string
	attrs    [][]any
}

func (d *auxiliaryUsageDiagnostics) Log(_ context.Context, _ port.Level, msg string, attrs ...any) {
	d.messages = append(d.messages, msg)
	d.attrs = append(d.attrs, attrs)
}
func (d *auxiliaryUsageDiagnostics) With(...any) port.Diagnostics { return d }
func (d *auxiliaryUsageDiagnostics) hasAttr(key string, value any) bool {
	for _, attrs := range d.attrs {
		for i := 0; i+1 < len(attrs); i += 2 {
			if attrs[i] == key && attrs[i+1] == value {
				return true
			}
		}
	}
	return false
}
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

func TestReflectSessionOwnershipLossCannotOverwriteSuccessor(t *testing.T) {
	usage := session.Usage{InputTokens: 5, OutputTokens: 2}
	aux := session.AuxiliaryUsage{Buckets: map[session.UsageKind]session.TokenUsage{
		session.UsageKindReflection: {Total: usage, Models: map[string]session.Usage{"server-provider/server-model": usage}},
	}}
	reflector := func(context.Context, *session.Session) (ReflectionReceipt, error) {
		return ReflectionReceipt{Disposition: "completed", Usage: aux}, nil
	}

	t.Run("current owner reads once without accounting save", func(t *testing.T) {
		store := &auxiliaryUsageStore{SessionStore: memstore.New()}
		id := session.SessionID("current-owner")
		if err := store.Save(t.Context(), completedAuxiliaryUsageSession(t, id)); err != nil {
			t.Fatal(err)
		}
		store.saves, store.loads = 0, 0
		diagnostics := &auxiliaryUsageDiagnostics{}
		svc := &Service{cfg: Config{
			Store: store, ReflectSession: reflector, Diagnostics: diagnostics,
			MutationCapability: NewSessionMutationCapability(false),
		}}
		if _, err := svc.ReflectSession(t.Context(), id); err != nil {
			t.Fatal(err)
		}
		if store.loads != 1 || store.saves != 0 {
			t.Fatalf("reflection loaded=%d saved=%d, want 1,0", store.loads, store.saves)
		}
		if diagnostics.count("reflection usage dropped") != 1 || !diagnostics.hasAttr("bucket_count", 1) {
			t.Fatalf("drop diagnostics = %#v attrs=%#v, want one structured record", diagnostics.messages, diagnostics.attrs)
		}
		persisted, err := store.SessionStore.Load(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if got := persisted.UsageFor(session.UsageKindReflection); got != (session.Usage{}) {
			t.Fatalf("persisted reflection usage = %+v, want none", got)
		}
	})

	t.Run("reflection does not wait for session mutation lock", func(t *testing.T) {
		store := memstore.New()
		id := session.SessionID("lock-independent")
		if err := store.Save(t.Context(), completedAuxiliaryUsageSession(t, id)); err != nil {
			t.Fatal(err)
		}
		svc := &Service{cfg: Config{Store: store, ReflectSession: reflector, Diagnostics: port.NopDiagnostics{}}}
		unlock := svc.runEntryMu.lock(id)
		result := make(chan error, 1)
		go func() {
			_, err := svc.ReflectSession(t.Context(), id)
			result <- err
		}()
		select {
		case err := <-result:
			unlock()
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			unlock()
			<-result
			t.Fatal("reflection waited for the session mutation lock")
		}
	})

	t.Run("error path does not persist partial usage", func(t *testing.T) {
		store := &auxiliaryUsageStore{SessionStore: memstore.New()}
		id := session.SessionID("current-owner-error")
		if err := store.Save(t.Context(), completedAuxiliaryUsageSession(t, id)); err != nil {
			t.Fatal(err)
		}
		store.saves, store.loads = 0, 0
		diagnostics := &auxiliaryUsageDiagnostics{}
		svc := &Service{cfg: Config{
			Store: store, Diagnostics: diagnostics, MutationCapability: NewSessionMutationCapability(false),
			ReflectSession: func(context.Context, *session.Session) (ReflectionReceipt, error) {
				return ReflectionReceipt{Usage: aux}, ErrUnavailable
			},
		}}
		if _, err := svc.ReflectSession(t.Context(), id); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("ReflectSession error = %v, want unavailable", err)
		}
		if store.loads != 1 || store.saves != 0 || diagnostics.count("reflection usage dropped") != 1 || !diagnostics.hasAttr("bucket_count", 1) {
			t.Fatalf("error reflection loaded=%d saved=%d diagnostics=%#v attrs=%#v", store.loads, store.saves, diagnostics.messages, diagnostics.attrs)
		}
		persisted, err := store.SessionStore.Load(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if got := persisted.UsageFor(session.UsageKindReflection); got != (session.Usage{}) {
			t.Fatalf("error-path persisted reflection usage = %+v, want none", got)
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
		if diagnostics.count("reflection usage dropped") != 1 || !diagnostics.hasAttr("bucket_count", 1) {
			t.Fatalf("drop diagnostics = %#v attrs=%#v, want one structured record", diagnostics.messages, diagnostics.attrs)
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
			successor := completedAuxiliaryUsageSession(t, id)
			successor.RecordTokenUsage(session.UsageKindMain, "successor-provider", "successor-model", session.Usage{InputTokens: 99})
			if err := store.SessionStore.Save(t.Context(), successor); err != nil {
				t.Fatal(err)
			}
			return ReflectionReceipt{Disposition: "completed", Usage: aux}, nil
		}
		if _, err := svc.ReflectSession(t.Context(), id); err != nil {
			t.Fatal(err)
		}
		if store.saves != 0 {
			t.Fatalf("post-lease-loss accounting saves = %d, want zero", store.saves)
		}
		persisted, err := store.SessionStore.Load(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if got := persisted.UsageFor(session.UsageKindMain); got.InputTokens != 99 {
			t.Fatalf("successor main usage = %+v, want successor state preserved", got)
		}
		if got := persisted.UsageFor(session.UsageKindReflection); got != (session.Usage{}) {
			t.Fatalf("late reflection usage overwrote successor state: %+v", got)
		}
		if diagnostics.count("reflection usage dropped") != 1 || !diagnostics.hasAttr("bucket_count", 1) {
			t.Fatalf("drop diagnostics = %#v attrs=%#v, want one structured record", diagnostics.messages, diagnostics.attrs)
		}
	})
}

func TestAuxiliaryTokenUsage_Scenario2_ReflectionRecordsSelectedModel(t *testing.T) {
	usage := session.Usage{InputTokens: 5, OutputTokens: 2}
	store := &auxiliaryUsageStore{SessionStore: memstore.New()}
	id := session.SessionID("reflection-usage-purpose")
	if err := store.Save(t.Context(), completedAuxiliaryUsageSession(t, id)); err != nil {
		t.Fatal(err)
	}
	store.saves, store.loads = 0, 0
	diagnostics := &auxiliaryUsageDiagnostics{}
	svc := &Service{cfg: Config{
		Store: store, Diagnostics: diagnostics,
		ReflectSession: func(context.Context, *session.Session) (ReflectionReceipt, error) {
			return ReflectionReceipt{Disposition: "completed", Usage: session.AuxiliaryUsage{Buckets: map[session.UsageKind]session.TokenUsage{
				session.UsageKindReflection: {Total: usage, Models: map[string]session.Usage{"reflection-provider/reflection-model": usage}},
			}}}, nil
		},
		MutationCapability: NewSessionMutationCapability(false),
	}}
	if _, err := svc.ReflectSession(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if store.loads != 1 || store.saves != 0 || diagnostics.count("reflection usage dropped") != 1 || !diagnostics.hasAttr("bucket_count", 1) {
		t.Fatalf("reflection loaded=%d saved=%d diagnostics=%#v attrs=%#v", store.loads, store.saves, diagnostics.messages, diagnostics.attrs)
	}
	persisted, err := store.SessionStore.Load(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got := persisted.TokenUsageSnapshot(); len(got) != 0 {
		t.Fatalf("reflection changed source usage ledger: %#v", got)
	}
}
