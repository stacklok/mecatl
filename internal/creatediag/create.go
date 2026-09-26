// Package creatediag records content-free, request-scoped remote-create progress.
package creatediag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/stacklok/mecatl/engine/port"
)

type key struct{}
type trace struct {
	diag    port.Diagnostics
	start   time.Time
	session string
}

// Start is used only by the remote HTTP create handler, after authentication.
func Start(ctx context.Context, diag port.Diagnostics) context.Context {
	return context.WithValue(ctx, key{}, &trace{diag: diag, start: time.Now()})
}

// Session hashes even caller-supplied IDs; no owner or placement identity is logged.
// The create request is sequential; the trace never escapes its request context.
func Session(ctx context.Context, id string) {
	if t, ok := ctx.Value(key{}).(*trace); ok {
		sum := sha256.Sum256([]byte(id))
		t.session = hex.EncodeToString(sum[:16])
	}
}

// Note accepts only harness-owned stage/reason literals, never producer text.
func Note(ctx context.Context, stage, reason string, calls int) {
	if t, ok := ctx.Value(key{}).(*trace); ok {
		t.diag.Log(ctx, port.LevelDebug, "remote create stage", "stage", stage,
			"reason", reason, "elapsed_ms", time.Since(t.start).Milliseconds(),
			"calls", calls, "session", t.session)
	}
}

// Begin emits both sides of a blocking boundary without exposing its error text.
func Begin(ctx context.Context, stage string) func(error) {
	Note(ctx, stage, "begin", 0)
	return func(err error) {
		reason := "ok"
		switch {
		case errors.Is(err, context.Canceled):
			reason = "cancelled"
		case errors.Is(err, context.DeadlineExceeded):
			reason = "deadline"
		case err != nil:
			reason = "error"
		}
		Note(ctx, stage, reason, 1)
	}
}
