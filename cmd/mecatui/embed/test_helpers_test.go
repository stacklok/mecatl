package embed_test

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/internal/app"
)

//nolint:revive // Test helpers conventionally keep testing.TB first.
func buildIsolated(t testing.TB, ctx context.Context, cfg app.Config) (*app.Built, error) {
	t.Helper()
	if cfg.UserModelDir == "" {
		cfg.UserModelDir = t.TempDir()
	}
	return app.Build(ctx, cfg)
}
