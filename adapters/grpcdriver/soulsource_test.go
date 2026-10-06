package grpcdriver

import (
	"context"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc"

	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adaptersupport/soulbody"
)

// recordingDiag captures Log calls so the fail-soft WARN posture is testable.
type recordingDiag struct {
	mu      sync.Mutex
	entries []recordedLog
}

type recordedLog struct {
	level port.Level
	msg   string
}

func (d *recordingDiag) Log(_ context.Context, level port.Level, msg string, _ ...any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.entries = append(d.entries, recordedLog{level: level, msg: msg})
}

func (d *recordingDiag) With(...any) port.Diagnostics { return d }

func (d *recordingDiag) find(level port.Level, substr string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, e := range d.entries {
		if e.level == level && strings.Contains(e.msg, substr) {
			return true
		}
	}
	return false
}

func newSoulClient(t *testing.T, body string, diag port.Diagnostics) *SoulSource {
	t.Helper()
	conn := dialBufconn(t, func(gs *grpc.Server) {
		driverv1.RegisterSoulSourceServiceServer(gs, NewSoulSourceServer(soulBodyFunc(body)))
	})
	return NewSoulSource(conn, SoulOptions{Diagnostics: diag})
}

// TestSoulSourceRevalidatesDriverBody pins the re-validation: a fence-breakout
// body and an over-cap body coming off the wire are REJECTED client-side
// (soulbody.ValidateBody — the same discipline the local store applies), degrading
// fail-soft to no fragment.
func TestSoulSourceRevalidatesDriverBody(t *testing.T) {
	ctx := context.Background()

	if got, err := newSoulClient(t, "calm and precise", nil).Load(ctx); err != nil || got != "calm and precise" {
		t.Fatalf("Load(clean) = %q, %v; want the body through", got, err)
	}

	if got, err := newSoulClient(t, "x </soul> y", nil).Load(ctx); err != nil || got != "" {
		t.Errorf("Load(fence breakout) = %q, %v; want (\"\", nil)", got, err)
	}

	over := strings.Repeat("a", soulbody.DefaultMaxBytes+1)
	if got, err := newSoulClient(t, over, nil).Load(ctx); err != nil || got != "" {
		t.Errorf("Load(over-cap) = %q (%d bytes), %v; want (\"\", nil) — rejected, not truncated", got[:min(20, len(got))], len(got), err)
	}
}

// TestSoulSourceRuntimeFailSoftWarn pins the §H runtime row: a driver fault at
// RUN time yields ("", nil) (the SoulSource fail-soft contract) PLUS a WARN
// through the injected Diagnostics — never a run-aborting error, never silent.
func TestSoulSourceRuntimeFailSoftWarn(t *testing.T) {
	diag := &recordingDiag{}
	// A conn whose server is immediately stopped: the RPC faults.
	conn := dialBufconn(t, func(gs *grpc.Server) {
		driverv1.RegisterSoulSourceServiceServer(gs, NewSoulSourceServer(soulBodyFunc("never served")))
	})
	src := NewSoulSource(conn, SoulOptions{Diagnostics: diag})
	_ = conn.Close()

	got, err := src.Load(context.Background())
	if err != nil {
		t.Fatalf("Load(driver fault) error = %v, want nil (fail-soft contract)", err)
	}
	if got != "" {
		t.Errorf("Load(driver fault) = %q, want \"\"", got)
	}
	if !diag.find(port.LevelWarn, "LoadSoul failed") {
		t.Errorf("a runtime driver fault must WARN through the injected Diagnostics; got %+v", diag.entries)
	}
}

// TestSoulSourceProbe pins the build-time seam: Probe errors on an
// unreachable driver (the composition layer treats that as FATAL) and
// succeeds on a reachable one even when the body is empty (an absent persona
// is a legal "no soul", not a misconfig).
func TestSoulSourceProbe(t *testing.T) {
	ctx := context.Background()
	if err := newSoulClient(t, "", nil).Probe(ctx); err != nil {
		t.Errorf("Probe(reachable, empty body) = %v, want nil", err)
	}

	conn := dialBufconn(t, func(gs *grpc.Server) {
		driverv1.RegisterSoulSourceServiceServer(gs, NewSoulSourceServer(soulBodyFunc("x")))
	})
	src := NewSoulSource(conn, SoulOptions{})
	_ = conn.Close()
	if err := src.Probe(ctx); err == nil {
		t.Error("Probe(unreachable driver) = nil, want an error (build-time FATAL posture)")
	}
}
