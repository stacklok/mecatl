package app

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/alicebob/miniredis/v2"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestBuildWiresSessionLoadFailureObservability(t *testing.T) {
	redis := miniredis.RunT(t)
	const id = session.SessionID("private-session")
	redis.Set("mecatl:session-metadata:state", "redis-metadata-index/1")
	redis.Set("mecatl:session-lineage:state", "redis-lineage-index/2")
	redis.HSet("mecatl:session:"+string(id), "blob", "malformed snapshot containing TOPSECRET")

	diag := newSessionLoadBuildDiagnostics()
	var metrics []port.SessionLoadFailureClass
	built, err := buildIsolated(t, t.Context(), Config{
		Workspace:                        t.TempDir(),
		Model:                            "mock",
		UseMock:                          true,
		RedisURL:                         redis.Addr(),
		RedisAllowPlaintext:              true,
		OwnershipEnforced:                true,
		Diagnostics:                      diag,
		SessionLoadFailureMetricsEmitter: func(class port.SessionLoadFailureClass) { metrics = append(metrics, class) },
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	_, _ = built.Service.GetSession(t.Context(), id)

	records := diag.sessionLoadRecords()
	if len(records) != 1 {
		t.Fatalf("session-load warnings = %d, want 1: %v", len(records), records)
	}
	if got := records[0]; got != "session load failed[class snapshot ownership enforced]" {
		t.Fatalf("session-load warning = %q, want bounded factory-wired record", got)
	}
	if strings.Contains(records[0], string(id)) || strings.Contains(records[0], "TOPSECRET") {
		t.Fatalf("session-load warning leaked target or snapshot: %q", records[0])
	}
	if len(metrics) != 1 || metrics[0] != port.SessionLoadFailureSnapshot {
		t.Fatalf("session-load metrics = %v, want [snapshot]", metrics)
	}
}

type sessionLoadBuildDiagnostics struct {
	mu      *sync.Mutex
	records *[]string
}

func newSessionLoadBuildDiagnostics() *sessionLoadBuildDiagnostics {
	return &sessionLoadBuildDiagnostics{mu: &sync.Mutex{}, records: &[]string{}}
}

func (d *sessionLoadBuildDiagnostics) Log(_ context.Context, _ port.Level, message string, args ...any) {
	if message != "session load failed" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	*d.records = append(*d.records, fmt.Sprintf("%s%v", message, args))
}

func (d *sessionLoadBuildDiagnostics) With(...any) port.Diagnostics { return d }

func (d *sessionLoadBuildDiagnostics) sessionLoadRecords() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), (*d.records)...)
}
