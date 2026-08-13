package grpcdriver

import (
	"context"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"

	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/adapter/eventlogconformance"
	"github.com/stacklok/mecatl/engine/adapter/leaseconformance"
	"github.com/stacklok/mecatl/engine/adapter/memconformance"
	"github.com/stacklok/mecatl/engine/adapter/memlease"
	"github.com/stacklok/mecatl/engine/adapter/memschedulestore"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/scheduleconformance"
	"github.com/stacklok/mecatl/engine/adapter/sourceconformance"
	"github.com/stacklok/mecatl/engine/adapter/storeconformance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/memory"
)

// The grpcdriver RETROFIT runs: the SAME shared conformance suites the
// in-process stores pass, run over the wire — client → bufconn → server
// wrapper → reference backend. Because the session server wrapper runs
// sessnap server-side, the session run exercises the FULL
// encode→wire→decode→state-machine→encode→wire→decode path.

// TestGRPCSessionStoreConformance runs the shared SessionStore conformance
// table over grpcdriver → bufconn → NewSessionStoreServer(memstore.New()).
func TestGRPCSessionStoreConformance(t *testing.T) {
	storeconformance.Run(t, func(t *testing.T) port.SessionStore {
		conn := dialBufconn(t, func(gs *grpc.Server) {
			driverv1.RegisterSessionStoreServiceServer(gs, NewSessionStoreServer(memstore.New()))
		})
		return NewSessionStore(conn)
	})
}

// TestGRPCSessionStorePrunableConformance runs the shared PrunableStore
// (retention seam) table over the same client → bufconn → server wrapper →
// memstore path, so List/Delete are proven over the wire (proto Timestamp
// round-trip, NOT_FOUND-tolerant Delete) exactly like Save/Load.
func TestGRPCSessionStorePrunableConformance(t *testing.T) {
	storeconformance.RunPrunable(t, func(t *testing.T) port.SessionStore {
		conn := dialBufconn(t, func(gs *grpc.Server) {
			driverv1.RegisterSessionStoreServiceServer(gs, NewSessionStoreServer(memstore.New()))
		})
		return NewSessionStore(conn)
	})
}

// TestGRPCEventLogConformance runs the shared EventLog conformance table over
// grpcdriver → bufconn → NewEventLogServer(memstore.NewEventLog()): the SAME
// suite the local jsonlstore passes, now over the full client → wire →
// server-wrapper → reference-backend path (the dual-path contract-unification —
// the Go port is the contract, the gRPC service is one adapter). The
// server-streaming Read RPC is exercised end-to-end, including the empty-stream
// (unknown-session) and append-order subtests.
func TestGRPCEventLogConformance(t *testing.T) {
	eventlogconformance.Run(t, func(t *testing.T) port.EventLog {
		conn := dialBufconn(t, func(gs *grpc.Server) {
			driverv1.RegisterEventLogServiceServer(gs, NewEventLogServer(memstore.NewEventLog()))
		})
		return NewEventLog(conn)
	})
}

// leaseFakeClock is an advanceable port.Clock the lease conformance suite drives
// forward to cross the TTL. The advance callback closes over the SERVER-SIDE
// memlease clock — the wire cannot carry "advance the clock", so the suite reaches
// the server's clock directly through this shared value.
type leaseFakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *leaseFakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *leaseFakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// TestGRPCSessionLeaseConformance runs the shared SessionLease conformance table
// over grpcdriver → bufconn → NewSessionLeaseServer(memlease.New(...)): the SAME
// suite the in-memory reference passes, now over the full client → wire →
// server-wrapper → reference-backend path, including FAILED_PRECONDITION →
// ErrLeaseHeld. The advance callback drives the server-side fake clock.
func TestGRPCSessionLeaseConformance(t *testing.T) {
	leaseconformance.Run(t, func(t *testing.T) (port.SessionLease, func(time.Duration)) {
		clk := &leaseFakeClock{t: time.Unix(1_700_000_000, 0)}
		backend := memlease.New(clk, leaseconformance.TTL)
		conn := dialBufconn(t, func(gs *grpc.Server) {
			driverv1.RegisterSessionLeaseServiceServer(gs, NewSessionLeaseServer(backend))
		})
		return NewSessionLease(conn), clk.advance
	})
}

// TestGRPCMemoryStoreConformance runs the shared MemoryStore conformance
// table over grpcdriver → bufconn → NewMemoryStoreServer(memory.New(tmp)).
func TestGRPCMemoryStoreConformance(t *testing.T) {
	memconformance.Run(t, func(t *testing.T) tool.MemoryStore {
		backend, err := memory.New(t.TempDir())
		if err != nil {
			t.Fatalf("memory.New: %v", err)
		}
		conn := dialBufconn(t, func(gs *grpc.Server) {
			driverv1.RegisterMemoryStoreServiceServer(gs, NewMemoryStoreServer(backend))
		})
		return NewMemoryStore(conn)
	})
}

func TestGRPCMemoryLifecycleConformance(t *testing.T) {
	memconformance.RunLifecycle(t, func(t *testing.T) (tool.MemoryStore, tool.MemoryLifecycleStore) {
		backend, err := memory.New(t.TempDir())
		if err != nil {
			t.Fatalf("memory.New: %v", err)
		}
		conn := dialBufconn(t, func(gs *grpc.Server) {
			driverv1.RegisterMemoryStoreServiceServer(gs, NewMemoryStoreServer(backend))
		})
		store, err := NegotiateMemoryStore(context.Background(), conn)
		if err != nil {
			t.Fatalf("NegotiateMemoryStore: %v", err)
		}
		lifecycle, ok := store.(tool.MemoryLifecycleStore)
		if !ok {
			t.Fatal("negotiated store lacks lifecycle capability")
		}
		return store, lifecycle
	})
}

// TestGRPCSkillSourceConformance runs the shared SkillSource conformance
// table over grpcdriver → bufconn → NewSkillSourceServer(FixtureSource): the
// same canonical fixture the in-memory reference and the FS source answer
// for, now over the full client → wire → server-wrapper path (which also
// exercises the server's logical-name pre-validation — the client does NOT
// pre-validate, so the invalid-name subtest hits the wire).
func TestGRPCSkillSourceConformance(t *testing.T) {
	sourceconformance.RunSkillSource(t, func(t *testing.T) tool.SkillSource {
		conn := dialBufconn(t, func(gs *grpc.Server) {
			driverv1.RegisterSkillSourceServiceServer(gs, NewSkillSourceServer(sourceconformance.NewFixtureSource()))
		})
		return NewSkillSource(conn)
	})
}

// TestGRPCAgentSourceConformance runs the shared AgentDefSource conformance
// table over grpcdriver → bufconn → NewAgentSourceServer(AgentFixtureSource):
// the same canonical fixture the in-memory reference and the FS source answer
// for, now over the full client → wire → server-wrapper path (including the
// client's defensive re-normalization and unconditional driver-origin stamp).
func TestGRPCAgentSourceConformance(t *testing.T) {
	sourceconformance.RunAgentSource(t, func(t *testing.T) tool.AgentDefSource {
		conn := dialBufconn(t, func(gs *grpc.Server) {
			driverv1.RegisterAgentSourceServiceServer(gs, NewAgentSourceServer(sourceconformance.NewAgentFixtureSource()))
		})
		return NewAgentSource(conn, AgentOptions{})
	})
}

// TestGRPCCommandSourceConformance runs the shared CommandSource conformance
// table over grpcdriver → bufconn → NewCommandSourceServer(CommandFixture):
// the wire path also exercises the NOT_FOUND → (found=false, nil) mapping the
// unknown-name subtest pins.
func TestGRPCCommandSourceConformance(t *testing.T) {
	sourceconformance.RunCommandSource(t, func(t *testing.T) prompt.CommandSource {
		conn := dialBufconn(t, func(gs *grpc.Server) {
			driverv1.RegisterCommandSourceServiceServer(gs, NewCommandSourceServer(sourceconformance.NewCommandFixtureSource()))
		})
		return NewCommandSource(conn, CommandOptions{})
	})
}

// soulBodyFunc adapts a fixed body to prompt.SoulSource for the wire fixture.
// It returns the body VERBATIM (no trimming/validation server-side), so the
// conformance run proves the CLIENT's re-validation upholds the fail-soft
// discipline — a driver is never trusted to sanitize.
type soulBodyFunc string

func (b soulBodyFunc) Load(context.Context) (string, error) { return string(b), nil }

// TestGRPCSoulSourceConformance runs the shared SoulSource conformance table
// over grpcdriver → bufconn → NewSoulSourceServer(verbatim fake).
func TestGRPCSoulSourceConformance(t *testing.T) {
	sourceconformance.RunSoulSource(t, func(t *testing.T, body string) prompt.SoulSource {
		conn := dialBufconn(t, func(gs *grpc.Server) {
			driverv1.RegisterSoulSourceServiceServer(gs, NewSoulSourceServer(soulBodyFunc(body)))
		})
		return NewSoulSource(conn, SoulOptions{})
	})
}

// TestGRPCScheduleStoreConformance runs the shared ScheduleStore conformance
// table over grpcdriver → bufconn → NewScheduleStoreServer +
// NewScheduleOneShotReArmerServer(memschedulestore.New()): the SAME suite the
// in-memory reference passes, now over the full client → wire →
// server-wrapper → reference-backend path. The factory registers BOTH
// ScheduleStoreService and ScheduleOneShotReArmerService over the SAME
// memschedulestore instance (which implements both port.ScheduleStore and
// port.ScheduleOneShotReArmer), so the re-arm subtests inside Run (which
// type-assert the store for port.ScheduleOneShotReArmer) RUN over the wire —
// the integration test proves Claim/ClaimNow/ReArmOneShot atomicity +
// RecordFire idempotency hold through the real gRPC path (the
// encode→wire→decode→state-machine→encode→wire→decode round trip).
func TestGRPCScheduleStoreConformance(t *testing.T) {
	scheduleconformance.Run(t, func(t *testing.T) port.ScheduleStore {
		backend := memschedulestore.New()
		conn := dialBufconn(t, func(gs *grpc.Server) {
			driverv1.RegisterScheduleStoreServiceServer(gs, NewScheduleStoreServer(backend))
			driverv1.RegisterScheduleOneShotReArmerServiceServer(gs, NewScheduleOneShotReArmerServer(backend))
		})
		return NewScheduleStore(conn)
	})
}
