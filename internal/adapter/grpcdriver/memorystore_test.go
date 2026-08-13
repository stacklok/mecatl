package grpcdriver

import (
	"context"
	"errors"
	"testing"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/adapter/memmemory"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/memory"
)

// newWiredMemoryStore returns a grpcdriver MemoryStore client over a bufconn
// server wrapping a fresh flock reference store, PLUS the backend itself so
// tests can compare against direct (non-wire) reads.
func newWiredMemoryStore(t *testing.T) (*MemoryStore, *memory.Store) {
	t.Helper()
	backend, err := memory.New(t.TempDir())
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	conn := dialBufconn(t, func(gs *grpc.Server) {
		driverv1.RegisterMemoryStoreServiceServer(gs, NewMemoryStoreServer(backend))
	})
	return NewMemoryStore(conn), backend
}

// TestRecallMissOverWire pins the §C row: a driver Recall miss is
// (zero, false, nil) — never an error, never NOT_FOUND.
func TestRecallMissOverWire(t *testing.T) {
	st, _ := newWiredMemoryStore(t)
	got, found, err := st.Recall(context.Background(), "no/such/key")
	if err != nil {
		t.Fatalf("Recall miss: err = %v, want nil", err)
	}
	if found {
		t.Error("Recall miss: found = true, want false")
	}
	if got != (tool.MemoryEntry{}) {
		t.Errorf("Recall miss: entry = %+v, want the zero value", got)
	}
}

// TestTimestampRoundTrip pins the timestamppb translation: the driver-stamped
// UpdatedAt crosses the wire intact (equal to a direct backend read).
func TestTimestampRoundTrip(t *testing.T) {
	st, backend := newWiredMemoryStore(t)
	ctx := context.Background()
	if err := st.RememberEntry(ctx, tool.MemoryEntry{Key: "k", Value: "v"}); err != nil {
		t.Fatalf("RememberEntry: %v", err)
	}
	wire, found, err := st.Recall(ctx, "k")
	if err != nil || !found {
		t.Fatalf("wire Recall: found=%v err=%v", found, err)
	}
	direct, found, err := backend.Recall(ctx, "k")
	if err != nil || !found {
		t.Fatalf("direct Recall: found=%v err=%v", found, err)
	}
	if wire.UpdatedAt.IsZero() {
		t.Error("wire UpdatedAt is zero, want the driver-stamped write time")
	}
	if !wire.UpdatedAt.Equal(direct.UpdatedAt) {
		t.Errorf("wire UpdatedAt = %v, want the backend's %v", wire.UpdatedAt, direct.UpdatedAt)
	}
}

// TestBlankKeyRejectedOverWire pins the §C row: a blank/whitespace-only key
// on RememberEntry is rejected (the server wrapper pre-validates with
// INVALID_ARGUMENT).
func TestBlankKeyRejectedOverWire(t *testing.T) {
	st, _ := newWiredMemoryStore(t)
	for _, key := range []string{"", "   ", "\t\n"} {
		if err := st.RememberEntry(context.Background(), tool.MemoryEntry{Key: key, Value: "v"}); err == nil {
			t.Errorf("RememberEntry(key=%q) = nil error, want rejection", key)
		}
	}
}

func TestOldDriverOperatorProfileMatchesLocalRendering(t *testing.T) {
	backend, err := memory.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := backend.RememberEntry(ctx, tool.MemoryEntry{Key: "user/language", Value: "日本語 — français", Description: "preferred language"}); err != nil {
		t.Fatal(err)
	}
	conn := dialBufconn(t, func(gs *grpc.Server) {
		driverv1.RegisterMemoryStoreServiceServer(gs, NewMemoryStoreServer(baseOnlyMemoryStore{backend}))
	})
	remoteEntries, err := NewMemoryStore(conn).List(ctx, "user/")
	if err != nil {
		t.Fatal(err)
	}
	localEntries, err := backend.List(ctx, "user/")
	if err != nil {
		t.Fatal(err)
	}
	local := prompt.Build(prompt.Config{OperatorProfile: prompt.OperatorProfileConfig{Entries: localEntries}})
	remote := prompt.Build(prompt.Config{OperatorProfile: prompt.OperatorProfileConfig{Entries: remoteEntries}})
	if remote.VolatileSuffix != local.VolatileSuffix {
		t.Fatalf("remote profile differs from local\nremote: %q\n local: %q", remote.VolatileSuffix, local.VolatileSuffix)
	}
}

func TestLifecycleProjectionRepairsInvalidUTF8(t *testing.T) {
	bad := "bad\xff"
	projected := toProtoRevision(tool.MemoryRevision{Key: bad, Value: bad, Description: bad, Version: tool.MemoryVersion(bad), Status: tool.MemoryStatus(bad), Writer: tool.MemoryWriter(bad), Origin: tool.MemoryOrigin(bad), Source: tool.MemorySource{SessionID: bad}})
	for name, value := range map[string]string{"key": projected.GetKey(), "value": projected.GetValue(), "description": projected.GetDescription(), "version": projected.GetVersion(), "status": projected.GetStatus(), "writer": projected.GetWriter(), "origin": projected.GetOrigin(), "session": projected.GetSource().GetSessionId()} {
		if !utf8.ValidString(value) {
			t.Errorf("%s remains invalid UTF-8", name)
		}
	}
}

type blockingCapabilitiesServer struct {
	driverv1.UnimplementedMemoryStoreServiceServer
}

func (blockingCapabilitiesServer) Capabilities(ctx context.Context, _ *driverv1.MemoryStoreCapabilitiesRequest) (*driverv1.MemoryStoreCapabilitiesResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestNegotiateMemoryStoreRespectsShorterCallerDeadline(t *testing.T) {
	conn := dialBufconn(t, func(gs *grpc.Server) {
		driverv1.RegisterMemoryStoreServiceServer(gs, blockingCapabilitiesServer{})
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	store, err := NegotiateMemoryStore(ctx, conn)
	if store != nil {
		t.Fatalf("timed-out negotiation returned partial store %T", store)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("negotiation error = %v, want deadline classification", err)
	}
	if elapsed := time.Since(started); elapsed >= memoryCapabilityTimeout {
		t.Fatalf("caller deadline was not respected: elapsed=%s capability ceiling=%s", elapsed, memoryCapabilityTimeout)
	}
}

type baseOnlyMemoryStore struct{ tool.MemoryStore }

type advertisedFailingLifecycleStore struct {
	tool.MemoryStore
	legacyWrites int
}

func (s *advertisedFailingLifecycleStore) RememberEntry(ctx context.Context, entry tool.MemoryEntry) error {
	s.legacyWrites++
	return s.MemoryStore.RememberEntry(ctx, entry)
}

func (*advertisedFailingLifecycleStore) RememberVersioned(context.Context, tool.MemoryEntry, tool.MemoryVersion) (tool.MemoryRecord, error) {
	return tool.MemoryRecord{}, status.Error(codes.Unimplemented, "advertised lifecycle RPC missing")
}

func (s *advertisedFailingLifecycleStore) Inspect(ctx context.Context, key string) (tool.MemoryRecord, bool, error) {
	entry, found, err := s.Recall(ctx, key)
	return tool.MemoryRecord{Current: tool.MemoryRevision{Key: entry.Key, Value: entry.Value}}, found, err
}

func (*advertisedFailingLifecycleStore) ForgetVersioned(context.Context, string, tool.MemoryVersion) (tool.MemoryRecord, error) {
	return tool.MemoryRecord{}, nil
}

func (*advertisedFailingLifecycleStore) UndoLatest(context.Context, string, tool.MemoryVersion) (tool.MemoryRecord, error) {
	return tool.MemoryRecord{}, nil
}

func TestNegotiatedLifecycleFailureNeverFallsBackToLegacyMutation(t *testing.T) {
	backend := memmemory.New()
	ctx := context.Background()
	if err := backend.RememberEntry(ctx, tool.MemoryEntry{Key: "user/existing", Value: "kept"}); err != nil {
		t.Fatal(err)
	}
	advertised := &advertisedFailingLifecycleStore{MemoryStore: backend}
	conn := dialBufconn(t, func(gs *grpc.Server) {
		driverv1.RegisterMemoryStoreServiceServer(gs, NewMemoryStoreServer(advertised))
	})
	store, err := NegotiateMemoryStore(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, ok := store.(tool.MemoryLifecycleStore)
	if !ok {
		t.Fatalf("negotiated store = %T, want lifecycle", store)
	}
	if _, err := lifecycle.RememberVersioned(ctx, tool.MemoryEntry{Key: "user/new", Value: "must-not-write"}, ""); err == nil {
		t.Fatal("failing advertised lifecycle RPC returned nil")
	}
	if advertised.legacyWrites != 0 {
		t.Fatalf("legacy writes = %d, want 0", advertised.legacyWrites)
	}
	if _, found, err := store.Recall(ctx, "user/new"); err != nil || found {
		t.Fatalf("failed lifecycle write mutated base store: found=%v err=%v", found, err)
	}
	if got, found, err := store.Recall(ctx, "user/existing"); err != nil || !found || got.Value != "kept" {
		t.Fatalf("base Recall after lifecycle failure: got=%+v found=%v err=%v", got, found, err)
	}
	if got, err := store.List(ctx, "user/"); err != nil || len(got) != 1 || got[0].Key != "user/existing" {
		t.Fatalf("base List after lifecycle failure: got=%+v err=%v", got, err)
	}
}

func TestBaseOnlyServerKeepsBaseOperationsAndRejectsIrreversibleLifecycle(t *testing.T) {
	backend, err := memory.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	conn := dialBufconn(t, func(gs *grpc.Server) {
		driverv1.RegisterMemoryStoreServiceServer(gs, NewMemoryStoreServer(baseOnlyMemoryStore{backend}))
	})
	store, err := NegotiateMemoryStore(context.Background(), conn)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.(tool.MemoryLifecycleStore); ok {
		t.Fatal("base-only driver falsely advertised lifecycle capability")
	}
	ctx := context.Background()

	if err := store.RememberEntry(ctx, tool.MemoryEntry{Key: "user/editor", Value: "Zed 🦀"}); err != nil {
		t.Fatalf("base remember: %v", err)
	}
	if err := store.RememberEntry(ctx, tool.MemoryEntry{Key: "user/editor", Value: "Helix"}); err != nil {
		t.Fatalf("base overwrite: %v", err)
	}
	if got, found, err := store.Recall(ctx, "user/editor"); err != nil || !found || got.Value != "Helix" {
		t.Fatalf("base recall after overwrite: got=%+v found=%v err=%v", got, found, err)
	}
	listed, err := store.List(ctx, "user/")
	if err != nil || len(listed) != 1 || listed[0].Value != "Helix" {
		t.Fatalf("legacy List profile: entries=%+v err=%v", listed, err)
	}
}
