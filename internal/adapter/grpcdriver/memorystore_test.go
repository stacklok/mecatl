package grpcdriver

import (
	"context"
	"testing"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc"

	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/memory"
)

func newWiredMemoryStore(t *testing.T) (*MemoryStore, *memory.Store) {
	t.Helper()
	backend, err := memory.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	conn := dialBufconn(t, func(server *grpc.Server) {
		driverv1.RegisterMemoryStoreServiceServer(server, NewMemoryStoreServer(backend))
	})
	return NewMemoryStore(conn), backend
}

func TestMemoryCASRoundTripOverCurrentGeneratedBridge(t *testing.T) {
	store, backend := newWiredMemoryStore(t)
	ctx := context.Background()
	created, err := store.Remember(ctx, tool.MemoryEntry{Key: "profile/editor", Value: "helix"}, tool.MemoryCurrent{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Remember(ctx, tool.MemoryEntry{Key: "profile/editor", Value: "stale"}, tool.MemoryCurrent{}); err == nil {
		t.Fatal("create-only remote Remember overwrote existing memory")
	}
	updated, err := store.Remember(ctx, tool.MemoryEntry{Key: "profile/editor", Value: "vim"}, tool.MemoryCurrent{Exists: true, Version: created.Current.Version})
	if err != nil {
		t.Fatal(err)
	}
	wire, found, err := store.Recall(ctx, "profile/editor")
	if err != nil || !found || wire.Value != "vim" {
		t.Fatalf("Recall = (%+v, %v, %v)", wire, found, err)
	}
	direct, _, _ := backend.Recall(ctx, "profile/editor")
	if wire.UpdatedAt.IsZero() || !wire.UpdatedAt.Equal(direct.UpdatedAt) {
		t.Fatalf("timestamp wire=%v direct=%v", wire.UpdatedAt, direct.UpdatedAt)
	}
	deleted, err := store.Forget(ctx, "profile/editor", updated.Current.Version)
	if err != nil || deleted.Current.Status != tool.MemoryStatusDeleted {
		t.Fatalf("Forget = (%+v, %v)", deleted, err)
	}
	restored, err := store.Undo(ctx, "profile/editor", deleted.Current.Version)
	if err != nil || restored.Current.Value != "vim" {
		t.Fatalf("Undo = (%+v, %v)", restored, err)
	}
}

func TestRecallMissOverWire(t *testing.T) {
	store, _ := newWiredMemoryStore(t)
	entry, found, err := store.Recall(context.Background(), "missing")
	if err != nil || found || entry != (tool.MemoryEntry{}) {
		t.Fatalf("Recall miss = (%+v, %v, %v)", entry, found, err)
	}
}

func TestLifecycleProjectionRepairsInvalidUTF8(t *testing.T) {
	bad := "bad\xff"
	projected := toProtoRevision(tool.MemoryRevision{Key: bad, Value: bad, Description: bad, Version: tool.MemoryVersion(bad), Status: tool.MemoryStatus(bad), Writer: tool.MemoryWriter(bad), Origin: tool.MemoryOrigin(bad), Source: tool.MemorySource{SessionID: bad, ProposalID: bad}})
	for name, value := range map[string]string{"key": projected.GetKey(), "value": projected.GetValue(), "description": projected.GetDescription(), "version": projected.GetVersion(), "status": projected.GetStatus(), "writer": projected.GetWriter(), "origin": projected.GetOrigin(), "session": projected.GetSource().GetSessionId(), "proposal": projected.GetSource().GetProposalId()} {
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
	conn := dialBufconn(t, func(server *grpc.Server) {
		driverv1.RegisterMemoryStoreServiceServer(server, blockingCapabilitiesServer{})
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := NegotiateMemoryStore(ctx, conn); err == nil {
		t.Fatal("negotiation unexpectedly succeeded")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("negotiation ignored caller deadline: %v", elapsed)
	}
}

func TestNegotiateMemoryStoreRequiresMandatoryCapabilities(t *testing.T) {
	conn := dialBufconn(t, func(server *grpc.Server) {
		driverv1.RegisterMemoryStoreServiceServer(server, &driverv1.UnimplementedMemoryStoreServiceServer{})
	})
	if _, err := NegotiateMemoryStore(context.Background(), conn); err == nil {
		t.Fatal("old driver without capabilities was accepted")
	}
}
